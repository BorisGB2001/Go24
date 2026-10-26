package crm

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Access rights of «Складской учёт», as «Права доступа» of the inventory: roles give each kind of stock
// document actions, limit the warehouses the documents may use, show the purchase prices and open the settings.
// A person has all their roles' rights together, one without roles — the default role's; administrators have every right.

// StockActions on a kind of document.
var StockActions = []string{"read", "write", "conduct", "cancel", "delete"}

// StockPerms are a role's rights.
type StockPerms struct {
	Docs          map[string][]string `json:"docs"`                 // actions by kind
	Warehouses    []string            `json:"warehouses,omitempty"` // the documents' warehouses; none — all
	PurchasePrice bool                `json:"purchase_price,omitempty"`
	Settings      bool                `json:"settings,omitempty"`
}

// Has: the role gives the action on the kind.
func (p StockPerms) Has(kind, action string) bool { return slices.Contains(p.Docs[kind], action) }

// HasWarehouse: the role's documents may use the warehouse.
func (p StockPerms) HasWarehouse(id string) bool {
	return len(p.Warehouses) == 0 || slices.Contains(p.Warehouses, id)
}

// StockRole is a set of rights given to people.
type StockRole struct {
	ID, Code, Name string
	Default        bool
	Perms          StockPerms
	Members        []Ref
}

// StockRights are a person's rights: their roles together.
type StockRights struct {
	docs          map[string]map[string]bool
	all           bool            // every warehouse
	warehouses    map[string]bool // else these
	PurchasePrice bool
	Settings      bool
}

// Can: the action on a kind of document.
func (r StockRights) Can(kind, action string) bool { return r.docs[kind][action] }

// CanAny: the action on some kind.
func (r StockRights) CanAny(action string) bool {
	for _, a := range r.docs {
		if a[action] {
			return true
		}
	}
	return false
}

// Warehouse: the documents may use the warehouse.
func (r StockRights) Warehouse(id string) bool { return r.all || r.warehouses[id] }

// Limited: not every warehouse.
func (r StockRights) Limited() bool { return !r.all }

// WarehouseIDs are the warehouses of a limited person (nil — all).
func (r StockRights) WarehouseIDs() []string {
	if r.all {
		return nil
	}
	ids := make([]string, 0, len(r.warehouses))
	for id := range r.warehouses {
		ids = append(ids, id)
	}
	return ids
}

// Kinds the person may do the action on (all — nil).
func (r StockRights) Kinds(action string) []string {
	out := []string{}
	for _, k := range StockKinds {
		if r.Can(k, action) {
			out = append(out, k)
		}
	}
	if len(out) == len(StockKinds) {
		return nil
	}
	return out
}

// Doc: the action on a document — its kind allows it, and its warehouses: reading needs one of them (or none at
// all), the other actions need every one.
func (r StockRights) Doc(d StockDoc, action string) bool {
	if !r.Can(d.Kind, action) {
		return false
	}
	if r.all {
		return true
	}
	seen := false
	for _, x := range d.Rows {
		for _, w := range []string{x.From, x.To} {
			if w == "" {
				continue
			}
			seen = true
			if r.warehouses[w] && action == "read" {
				return true
			}
			if !r.warehouses[w] && action != "read" {
				return false
			}
		}
	}
	return action != "read" || !seen
}

// AllStockRights are an administrator's.
func AllStockRights() StockRights {
	r := StockRights{docs: map[string]map[string]bool{}, all: true, PurchasePrice: true, Settings: true}
	for _, k := range StockKinds {
		r.docs[k] = map[string]bool{}
		for _, a := range StockActions {
			r.docs[k][a] = true
		}
	}
	return r
}

// StockRightsOf are a person's rights; an administrator (admin) has every one.
func (s *Store) StockRightsOf(ctx context.Context, userID string, admin bool) (StockRights, error) {
	if admin {
		return AllStockRights(), nil
	}
	rows, err := s.db.Query(ctx, `SELECT r.perms FROM crm_stock_roles r WHERE r.id IN (SELECT role_id FROM crm_stock_role_members WHERE user_id::text = $1)
		UNION ALL SELECT r.perms FROM crm_stock_roles r WHERE r.is_default AND NOT EXISTS (SELECT 1 FROM crm_stock_role_members WHERE user_id::text = $1)`, userID)
	if err != nil {
		return StockRights{}, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StockPerms, error) {
		var raw []byte
		var p StockPerms
		if err := r.Scan(&raw); err != nil {
			return p, err
		}
		return p, json.Unmarshal(raw, &p)
	})
	if err != nil {
		return StockRights{}, err
	}
	out := StockRights{docs: map[string]map[string]bool{}, warehouses: map[string]bool{}}
	for _, p := range list {
		// a role's warehouses are those of its documents: one giving no document gives no warehouse
		gives := false
		for _, k := range StockKinds {
			for _, a := range p.Docs[k] {
				if out.docs[k] == nil {
					out.docs[k] = map[string]bool{}
				}
				out.docs[k][a], gives = true, true
			}
		}
		if gives && len(p.Warehouses) == 0 {
			out.all = true
		} else if gives {
			for _, w := range p.Warehouses {
				out.warehouses[w] = true
			}
		}
		out.PurchasePrice = out.PurchasePrice || p.PurchasePrice
		out.Settings = out.Settings || p.Settings
	}
	return out, nil
}

// StockRoles with their members: the default one first.
func (s *Store) StockRoles(ctx context.Context) ([]StockRole, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, coalesce(code, ''), name, is_default, perms FROM crm_stock_roles ORDER BY is_default DESC, created_at`)
	if err != nil {
		return nil, err
	}
	roles, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (StockRole, error) {
		var x StockRole
		var raw []byte
		if err := r.Scan(&x.ID, &x.Code, &x.Name, &x.Default, &raw); err != nil {
			return x, err
		}
		return x, json.Unmarshal(raw, &x.Perms)
	})
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, r := range roles {
		idx[r.ID] = i
	}
	rows, err = s.db.Query(ctx, `SELECT m.role_id::text, u.id, u.name FROM crm_stock_role_members m JOIN users u ON u.id = m.user_id ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var u Ref
		if err := rows.Scan(&role, &u.ID, &u.Name); err != nil {
			return nil, err
		}
		roles[idx[role]].Members = append(roles[idx[role]].Members, u)
	}
	return roles, rows.Err()
}

// cleanStockPerms keeps the known kinds and actions (reading comes with any other action) and the existing warehouses.
func (s *Store) cleanStockPerms(ctx context.Context, p StockPerms) (StockPerms, error) {
	out := StockPerms{Docs: map[string][]string{}, PurchasePrice: p.PurchasePrice, Settings: p.Settings}
	for _, k := range StockKinds {
		var acts []string
		for _, a := range StockActions {
			if p.Has(k, a) || (a == "read" && len(p.Docs[k]) > 0) {
				acts = append(acts, a)
			}
		}
		if acts != nil {
			out.Docs[k] = acts
		}
	}
	if len(p.Warehouses) > 0 {
		rows, err := s.db.Query(ctx, `SELECT id::text FROM crm_warehouses WHERE id::text = ANY($1) ORDER BY is_default DESC, sort, lower(name)`, p.Warehouses)
		if err != nil {
			return out, err
		}
		if out.Warehouses, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return out, err
		}
		if len(out.Warehouses) == 0 {
			return out, ErrNotFound
		}
	}
	return out, nil
}

// SaveStockRole creates (ID "") or changes a role (administrators; checked by the web layer).
func (s *Store) SaveStockRole(ctx context.Context, r StockRole) (string, error) {
	r.Name = strings.TrimSpace(r.Name)
	if len([]rune(r.Name)) > 100 || (r.ID == "" && r.Name == "") {
		return "", i18n.E("err.crm_role")
	}
	p, err := s.cleanStockPerms(ctx, r.Perms)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(p)
	if r.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO crm_stock_roles (name, perms) VALUES ($1, $2) RETURNING id`, r.Name, raw).Scan(&r.ID)
		return r.ID, err
	}
	// a built-in role keeps its name from the locales unless one is given
	tag, err := s.db.Exec(ctx, `UPDATE crm_stock_roles SET perms = $2, name = CASE WHEN $3 = '' AND code IS NOT NULL THEN name ELSE $3 END
		WHERE id::text = $1`, r.ID, raw, r.Name)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return r.ID, err
}

// DeleteStockRole removes a role (not the default one).
func (s *Store) DeleteStockRole(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_stock_roles WHERE id::text = $1 AND NOT is_default`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.crm_role_default")
	}
	return err
}

// SetStockMember gives a person a role (not the default one: it is for those without roles) or takes it away.
func (s *Store) SetStockMember(ctx context.Context, roleID, userID string, on bool) error {
	if !on {
		_, err := s.db.Exec(ctx, `DELETE FROM crm_stock_role_members WHERE role_id::text = $1 AND user_id::text = $2`, roleID, userID)
		return err
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stock_roles WHERE id::text = $1 AND NOT is_default)`, roleID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	_, err := s.db.Exec(ctx, `INSERT INTO crm_stock_role_members (role_id, user_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING`, roleID, userID)
	return sectionErr(err)
}

// Rows: a limited person may put the rows of a kind on their warehouses (one not given is def, the default one).
func (r StockRights) Rows(kind string, rows []StockRow, def string) bool {
	if r.all {
		return true
	}
	for _, x := range rows {
		for _, w := range []struct {
			id   string
			uses bool
		}{{x.From, usesFrom(kind)}, {x.To, usesTo(kind)}} {
			if !w.uses {
				continue
			}
			if w.id == "" {
				w.id = def
			}
			if !r.warehouses[w.id] {
				return false
			}
		}
	}
	return true
}

// DefaultWarehouse is the default warehouse's id ("" — none yet).
func (s *Store) DefaultWarehouse(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM crm_warehouses WHERE is_default), '')`).Scan(&id)
	return id, err
}

// CheckStockDoc: may the person do the action on the stored document id ("" — a new one of the kind); saving (rows not
// nil) also needs the new rows' warehouses to be the person's.
func (s *Store) CheckStockDoc(ctx context.Context, r StockRights, id, kind, action string, rows []StockRow) error {
	if id != "" {
		d, err := s.StockDoc(ctx, id)
		if err != nil {
			return err
		}
		if !r.Doc(d, "read") {
			return ErrNotFound
		}
		if !r.Doc(d, action) {
			return ErrForbidden
		}
		kind = d.Kind
	} else if !r.Can(kind, action) {
		return ErrForbidden
	}
	if rows != nil && r.Limited() {
		def, err := s.DefaultWarehouse(ctx)
		if err != nil {
			return err
		}
		if !r.Rows(kind, rows, def) {
			return i18n.E("err.stock_warehouse_denied")
		}
	}
	return nil
}
