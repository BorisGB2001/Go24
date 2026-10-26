package crm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Корзина» of the portal CRM: a deleted deal, lead, contact, company or smart process card goes to the bin as a
// snapshot of its rows — the card, the rows that the database deletes with it (fields, observers, contacts of a deal,
// invoices…), its deeds, products and requisites — and the links of other cards to it (a deal's contact, a lead's
// deal). Restoring puts them back; the bin is emptied by hand or after RecycleDays. The snapshot follows the foreign
// keys of the schema itself, so new tables of a card come along without changes here.

// RecycleDays: how long the bin keeps a card.
const RecycleDays = 30

// ErrRecycleGone: the card cannot come back — what it must belong to (its stage, its funnel) is gone.
var ErrRecycleGone = i18n.E("err.crm_recycle_gone")

// RecycleItem is a deleted card in the bin.
type RecycleItem struct {
	ID, Entity, EntityID, TypeID, TypeName, Title string
	DeletedBy                                     Ref
	DeletedAt                                     time.Time
}

// PurgeAt: when the bin forgets the card.
func (it RecycleItem) PurgeAt() time.Time { return it.DeletedAt.AddDate(0, 0, RecycleDays) }

type snapRow = map[string]json.RawMessage

type snapTable struct {
	Table string    `json:"table"`
	Rows  []snapRow `json:"rows"`
}

// snapLink: the rows of a table whose column pointed at the card (ON DELETE SET NULL) — set back on restore.
type snapLink struct {
	Table  string   `json:"table"`
	Column string   `json:"column"`
	Value  string   `json:"value"`
	IDs    []string `json:"ids"`
}

type snapshot struct {
	Tables []snapTable `json:"tables"`
	Links  []snapLink  `json:"links"`
}

// polymorphic: the tables bound to a card by (entity, entity_id) that go with it (triggers or the delete itself).
var polymorphic = []string{"crm_activities", "crm_product_rows", "crm_requisites", "crm_comms"}

func ident(name string) string { return pgx.Identifier{name}.Sanitize() }

func unquote(v json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

type fkey struct {
	table, column, ref string
	cascade, nullable  bool
}

// refsTo are the single-column foreign keys of other tables to the table that cascade or set null.
func refsTo(ctx context.Context, tx pgx.Tx, table string) ([]fkey, error) {
	rows, err := tx.Query(ctx, `SELECT c.conrelid::regclass::text, a.attname, af.attname, c.confdeltype = 'c', NOT a.attnotnull
		FROM pg_constraint c JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		JOIN pg_attribute af ON af.attrelid = c.confrelid AND af.attnum = c.confkey[1]
		WHERE c.contype = 'f' AND c.confrelid = $1::regclass AND cardinality(c.conkey) = 1 AND c.confdeltype IN ('c', 'n')
		ORDER BY 1, 2`, table)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (fkey, error) {
		var k fkey
		return k, r.Scan(&k.table, &k.column, &k.ref, &k.cascade, &k.nullable)
	})
}

// refsFrom are the table's own single-column foreign keys (to check on restore that what it points at is there).
func refsFrom(ctx context.Context, tx pgx.Tx, table string) ([]fkey, error) {
	rows, err := tx.Query(ctx, `SELECT c.confrelid::regclass::text, a.attname, af.attname, false, NOT a.attnotnull
		FROM pg_constraint c JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		JOIN pg_attribute af ON af.attrelid = c.confrelid AND af.attnum = c.confkey[1]
		WHERE c.contype = 'f' AND c.conrelid = $1::regclass AND cardinality(c.conkey) = 1`, table)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (fkey, error) {
		var k fkey
		return k, r.Scan(&k.table, &k.column, &k.ref, &k.cascade, &k.nullable)
	})
}

// columns of a table that may be written (not generated).
func columns(ctx context.Context, tx pgx.Tx, table string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `SELECT attname FROM pg_attribute WHERE attrelid = $1::regclass AND attnum > 0
		AND NOT attisdropped AND attgenerated = ''`, table)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m, err
}

type snapper struct {
	tx   pgx.Tx
	snap snapshot
	seen map[string]bool
}

// take adds the rows of a table matching the condition and, after them, what goes with them.
func (p *snapper) take(ctx context.Context, depth int, table, where string, args ...any) error {
	rs, err := p.tx.Query(ctx, `SELECT to_jsonb(t) FROM `+ident(table)+` t WHERE `+where, args...)
	if err != nil {
		return err
	}
	all, err := pgx.CollectRows(rs, pgx.RowTo[snapRow])
	if err != nil {
		return err
	}
	var rows []snapRow
	for _, r := range all {
		b, _ := json.Marshal(r)
		if k := table + string(b); !p.seen[k] {
			p.seen[k] = true
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		return nil
	}
	p.snap.Tables = append(p.snap.Tables, snapTable{table, rows})
	if depth > 5 {
		return nil
	}
	for _, r := range rows {
		entity := map[string]string{"crm_deals": "deal", "crm_leads": "lead", "crm_contacts": "contact", "crm_companies": "company"}[table]
		if table == "crm_invoices" { // an invoice or a quote has its own products
			entity, _ = unquote(r["kind"])
		}
		id, _ := unquote(r["id"])
		if entity == "" || id == "" {
			continue
		}
		for _, t := range polymorphic {
			if err := p.take(ctx, depth+1, t, `entity = $1 AND entity_id::text = $2`, entity, id); err != nil {
				return err
			}
		}
	}
	refs, err := refsTo(ctx, p.tx, table)
	if err != nil {
		return err
	}
	for _, k := range refs {
		var vals []string
		for _, r := range rows {
			if v, ok := unquote(r[k.ref]); ok && !slices.Contains(vals, v) {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			continue
		}
		if k.cascade {
			if err := p.take(ctx, depth+1, k.table, ident(k.column)+`::text = ANY($1)`, vals); err != nil {
				return err
			}
			continue
		}
		cols, err := columns(ctx, p.tx, k.table)
		if err != nil {
			return err
		}
		if !cols["id"] {
			continue
		}
		for _, v := range vals {
			rs, err := p.tx.Query(ctx, `SELECT id::text FROM `+ident(k.table)+` WHERE `+ident(k.column)+`::text = $1`, v)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rs, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if len(ids) > 0 {
				p.snap.Links = append(p.snap.Links, snapLink{k.table, k.column, v, ids})
			}
		}
	}
	return nil
}

// trash moves a card into the bin: keeps its snapshot, then deletes it with the statement del ($1 — the card's ID),
// all in one transaction.
func (s *Store) trash(ctx context.Context, userID, entity, id, title, typeID, responsibleID, del string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	p := snapper{tx: tx, seen: map[string]bool{}}
	if err := p.take(ctx, 0, "crm_"+table(entity), `id::text = $1`, id); err != nil {
		return err
	}
	data, err := json.Marshal(p.snap)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm_recycle (entity, entity_id, type_id, title, responsible_id, deleted_by, data)
		VALUES ($1, $2, NULLIF($3, '')::uuid, $4, NULLIF($5, '')::uuid, NULLIF($6, '')::uuid, $7)`,
		entity, id, typeID, title, responsibleID, userID, data); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, del, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const recycleSQL = `SELECT r.id::text, r.entity, r.entity_id::text, coalesce(r.type_id::text, ''), coalesce(t.name, ''), r.title,
		coalesce(r.deleted_by::text, ''), coalesce(u.name, ''), r.deleted_at
	FROM crm_recycle r LEFT JOIN users u ON u.id = r.deleted_by LEFT JOIN crm_types t ON t.id = r.type_id`

func scanRecycle(r pgx.CollectableRow) (RecycleItem, error) {
	var it RecycleItem
	return it, r.Scan(&it.ID, &it.Entity, &it.EntityID, &it.TypeID, &it.TypeName, &it.Title, &it.DeletedBy.ID, &it.DeletedBy.Name, &it.DeletedAt)
}

// Recycle is the bin, newest first: an administrator sees every card, others the ones they deleted. entity
// narrows it to one kind ("" — all), q to the titles containing it.
func (s *Store) Recycle(ctx context.Context, userID, entity, q string) ([]RecycleItem, error) {
	rows, err := s.db.Query(ctx, recycleSQL+` WHERE ($1 OR r.deleted_by::text = $2) AND ($3 = '' OR r.entity = $3)
		AND ($4 = '' OR r.title ILIKE $5) ORDER BY r.deleted_at DESC, r.id LIMIT 500`,
		tasks.IsAdmin(ctx), userID, entity, strings.TrimSpace(q), like(q))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanRecycle)
}

// recycled is a card of the bin the person may restore or delete: the one who deleted it or an administrator.
func (s *Store) recycled(ctx context.Context, userID, id string) (RecycleItem, error) {
	rows, err := s.db.Query(ctx, recycleSQL+` WHERE r.id::text = $1`, id)
	if err != nil {
		return RecycleItem{}, err
	}
	it, err := pgx.CollectExactlyOneRow(rows, scanRecycle)
	if errors.Is(err, pgx.ErrNoRows) {
		return it, ErrNotFound
	}
	if err == nil && it.DeletedBy.ID != userID && !tasks.IsAdmin(ctx) {
		err = ErrForbidden
	}
	return it, err
}

// insertRow writes a row of the snapshot back: the columns the table still has; the new ones take their defaults.
func insertRow(ctx context.Context, tx pgx.Tx, table string, cols map[string]bool, row snapRow) error {
	var names []string
	for k := range row {
		if cols[k] {
			names = append(names, ident(k))
		}
	}
	slices.Sort(names)
	b, err := json.Marshal(row)
	if err != nil {
		return err
	}
	list := strings.Join(names, ", ")
	_, err = tx.Exec(ctx, `INSERT INTO `+ident(table)+` (`+list+`) OVERRIDING SYSTEM VALUE SELECT `+list+
		` FROM jsonb_populate_record(NULL::`+ident(table)+`, $1) ON CONFLICT DO NOTHING`, b)
	return err
}

// Restore brings a card of the bin back with all it had: what it pointed at and is gone since is left empty
// (a company of a contact), unless the card cannot be without it (its stage) — then ErrRecycleGone. The rows of
// others that cannot come back (a contact of a deal deleted since) are skipped. It returns the card's kind and ID.
func (s *Store) Restore(ctx context.Context, userID, id string) (entity, cardID string, err error) {
	it, err := s.recycled(ctx, userID, id)
	if err != nil {
		return "", "", err
	}
	var snap snapshot
	if err := s.db.QueryRow(ctx, `SELECT data FROM crm_recycle WHERE id::text = $1`, id).Scan(&snap); err != nil {
		return "", "", err
	}
	if len(snap.Tables) == 0 || len(snap.Tables[0].Rows) == 0 {
		return "", "", ErrRecycleGone
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback(ctx)
	for i, t := range snap.Tables {
		cols, err := columns(ctx, tx, t.Table)
		if err != nil || len(cols) == 0 { // a table gone with a migration
			continue
		}
		if i == 0 { // the card itself
			row := t.Rows[0]
			refs, err := refsFrom(ctx, tx, t.Table)
			if err != nil {
				return "", "", err
			}
			for _, k := range refs {
				v, ok := unquote(row[k.column])
				if !ok {
					continue
				}
				var there bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+ident(k.table)+` WHERE `+ident(k.ref)+`::text = $1)`, v).Scan(&there); err != nil {
					return "", "", err
				}
				if !there {
					if !k.nullable {
						return "", "", ErrRecycleGone
					}
					row[k.column] = json.RawMessage("null")
				}
			}
			if err := insertRow(ctx, tx, t.Table, cols, row); err != nil {
				return "", "", err
			}
			continue
		}
		for _, row := range t.Rows {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return "", "", err
			}
			if insertRow(ctx, sp, t.Table, cols, row) != nil {
				sp.Rollback(ctx)
				continue
			}
			if err := sp.Commit(ctx); err != nil {
				return "", "", err
			}
		}
	}
	for _, l := range snap.Links {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return "", "", err
		}
		if _, err := sp.Exec(ctx, `UPDATE `+ident(l.Table)+` SET `+ident(l.Column)+` = $1::uuid WHERE id::text = ANY($2) AND `+
			ident(l.Column)+` IS NULL`, l.Value, l.IDs); err != nil {
			sp.Rollback(ctx)
			continue
		}
		if err := sp.Commit(ctx); err != nil {
			return "", "", err
		}
	}
	if err := s.event(ctx, tx, it.Entity, it.EntityID, userID, "restored", ""); err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM crm_recycle WHERE id::text = $1`, id); err != nil {
		return "", "", err
	}
	return it.Entity, it.EntityID, tx.Commit(ctx)
}

// purge forgets cards of the bin for good, with their timelines.
func (s *Store) purge(ctx context.Context, where string, args ...any) error {
	_, err := s.db.Exec(ctx, `WITH gone AS (DELETE FROM crm_recycle r WHERE `+where+` RETURNING entity, entity_id)
		DELETE FROM crm_timeline t USING gone g WHERE t.entity = g.entity AND t.entity_id = g.entity_id`, args...)
	return err
}

// PurgeRecycled deletes a card of the bin for good.
func (s *Store) PurgeRecycled(ctx context.Context, userID, id string) error {
	if _, err := s.recycled(ctx, userID, id); err != nil {
		return err
	}
	return s.purge(ctx, `r.id::text = $1`, id)
}

// EmptyRecycle: «Очистить корзину» — an administrator empties the whole bin, others their own cards in it.
func (s *Store) EmptyRecycle(ctx context.Context, userID string) error {
	return s.purge(ctx, `($1 OR r.deleted_by::text = $2)`, tasks.IsAdmin(ctx), userID)
}

// PurgeRecycleDue forgets the cards deleted more than RecycleDays ago (the worker).
func (s *Store) PurgeRecycleDue(ctx context.Context, now time.Time) error {
	return s.purge(ctx, `r.deleted_at < $1`, now.AddDate(0, 0, -RecycleDays))
}
