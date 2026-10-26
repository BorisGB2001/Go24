package crm

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Связи» of smart processes: the administrator chooses what the elements of a smart process are
// bound to — deals and the elements of other smart processes. An element has at most one parent of each such kind;
// the parent's card shows its child elements of each smart process bound to its kind. A kind is the smart process
// of a card, "" for deals (Deal.TypeID).

// TypeParents are the kinds the elements of a smart process may be bound to, in their order.
func (s *Store) TypeParents(ctx context.Context, typeID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT parent FROM crm_type_relations WHERE type_id::text = $1 ORDER BY sort, parent`, typeID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SetTypeParents puts the kinds the elements of a smart process are bound to: "" — deals, or other smart processes.
// The bindings of its elements to a kind taken away are removed.
func (s *Store) SetTypeParents(ctx context.Context, typeID string, parents []string) error {
	if _, err := s.Type(ctx, typeID); err != nil {
		return err
	}
	var list []string
	for _, p := range parents {
		if slices.Contains(list, p) {
			continue
		}
		if p != "" {
			if p == typeID {
				return i18n.E("err.crm_relation")
			}
			if _, err := s.Type(ctx, p); err != nil {
				return i18n.E("err.crm_relation")
			}
		}
		list = append(list, p)
	}
	if list == nil {
		list = []string{}
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM crm_type_relations WHERE type_id::text = $1 AND NOT (parent = ANY($2))`, typeID, list); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm_type_relations (type_id, parent, sort)
			SELECT $1::uuid, p, n FROM unnest($2::text[]) WITH ORDINALITY AS u(p, n)
			ON CONFLICT (type_id, parent) DO UPDATE SET sort = excluded.sort`, typeID, list); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM crm_deal_parents l USING crm_deals d, crm_pipelines dp, crm_deals pd, crm_pipelines pp
			WHERE d.id = l.deal_id AND dp.id = d.pipeline_id AND pd.id = l.parent_id AND pp.id = pd.pipeline_id
				AND dp.type_id::text = $1 AND NOT (coalesce(pp.type_id::text, '') = ANY($2))`, typeID, list)
		return err
	})
}

// ChildTypes are the smart processes whose elements may be bound to the cards of a kind, in their order.
func (s *Store) ChildTypes(ctx context.Context, kind string) ([]Type, error) {
	return scanTypes(s.db.Query(ctx, typeSQL+` WHERE EXISTS (SELECT 1 FROM crm_type_relations r WHERE r.type_id = t.id AND r.parent = $1)
		ORDER BY t.sort, t.created_at`, kind))
}

// Parents are the cards a card is bound to.
func (s *Store) Parents(ctx context.Context, dealID string) ([]Deal, error) {
	return scanDeals(s.db.Query(ctx, dealSQL+` WHERE d.id IN (SELECT parent_id FROM crm_deal_parents WHERE deal_id::text = $1)
		ORDER BY d.title`, dealID))
}

// Children are the cards bound to a card that the viewer may read, the newest first.
func (s *Store) Children(ctx context.Context, viewer, dealID string) ([]Deal, error) {
	sc, err := s.viewScope(ctx, viewer, "deal", "d", 2)
	if err != nil {
		return nil, err
	}
	return scanDeals(s.db.Query(ctx, dealSQL+` WHERE d.id IN (SELECT deal_id FROM crm_deal_parents WHERE parent_id::text = $1) AND `+sc+`
		ORDER BY d.created_at DESC LIMIT 500`, dealID, viewer))
}

// DealsOfKind are the cards of a kind ("" — deals) matching q the viewer may read: the parents to choose from.
func (s *Store) DealsOfKind(ctx context.Context, viewer, kind, q string) ([]Deal, error) {
	sc, err := s.viewScope(ctx, viewer, "deal", "d", 3)
	if err != nil {
		return nil, err
	}
	return scanDeals(s.db.Query(ctx, dealSQL+` WHERE coalesce(dp.type_id::text, '') = $1 AND d.title ILIKE $2 AND `+sc+`
		ORDER BY d.created_at DESC LIMIT 500`, kind, like(q), viewer))
}

// SetParent binds a card to its parent of a kind ("" — a deal), in place of the one it had; parentID "" unbinds it.
// The kind must be one the card's smart process is bound to, and the parent not the card itself nor its descendant.
func (s *Store) SetParent(ctx context.Context, userID, dealID, kind, parentID string) error {
	d, err := s.Deal(ctx, dealID)
	if err != nil {
		return err
	}
	if d.TypeID == "" {
		return i18n.E("err.crm_relation")
	}
	kinds, err := s.TypeParents(ctx, d.TypeID)
	if err != nil {
		return err
	}
	if !slices.Contains(kinds, kind) {
		return i18n.E("err.crm_relation")
	}
	var p Deal
	if parentID != "" {
		if p, err = s.Deal(ctx, parentID); errors.Is(err, ErrNotFound) || err == nil && p.TypeID != kind {
			return i18n.E("err.crm_relation")
		} else if err != nil {
			return err
		}
		var loop bool
		if err := s.db.QueryRow(ctx, `WITH RECURSIVE down AS (SELECT $1::uuid AS id UNION SELECT l.deal_id FROM crm_deal_parents l JOIN down ON l.parent_id = down.id)
			SELECT EXISTS (SELECT 1 FROM down WHERE id = $2::uuid)`, d.ID, p.ID).Scan(&loop); err != nil {
			return err
		}
		if loop {
			return i18n.E("err.crm_relation_loop")
		}
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var old string
		err := tx.QueryRow(ctx, `DELETE FROM crm_deal_parents l USING crm_deals pd, crm_pipelines pp
			WHERE l.deal_id = $1 AND pd.id = l.parent_id AND pp.id = pd.pipeline_id AND coalesce(pp.type_id::text, '') = $2
			RETURNING pd.title`, d.ID, kind).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if old != "" && parentID == "" {
			return s.event(ctx, tx, "deal", d.ID, userID, "unbound", old)
		}
		if parentID == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm_deal_parents (deal_id, parent_id) VALUES ($1, $2)`, d.ID, p.ID); err != nil {
			return err
		}
		if old == p.Title {
			return nil
		}
		return s.event(ctx, tx, "deal", d.ID, userID, "bound", p.Title)
	})
}

// SetParents puts all the parents of an element at once (REST): one card of each kind its smart process is bound to;
// a kind without one is unbound.
func (s *Store) SetParents(ctx context.Context, userID, dealID string, parentIDs []string) error {
	d, err := s.Deal(ctx, dealID)
	if err != nil {
		return err
	}
	kinds, err := s.TypeParents(ctx, d.TypeID)
	if err != nil {
		return err
	}
	byKind := map[string]string{}
	for _, id := range parentIDs {
		p, err := s.Deal(ctx, id)
		if errors.Is(err, ErrNotFound) || err == nil && (!slices.Contains(kinds, p.TypeID) || byKind[p.TypeID] != "") {
			return i18n.E("err.crm_relation")
		} else if err != nil {
			return err
		}
		byKind[p.TypeID] = p.ID
	}
	if d.TypeID == "" && len(parentIDs) > 0 {
		return i18n.E("err.crm_relation")
	}
	for _, k := range kinds {
		if err := s.SetParent(ctx, userID, d.ID, k, byKind[k]); err != nil {
			return err
		}
	}
	return nil
}
