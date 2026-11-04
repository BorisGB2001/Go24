package apps

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// The data storage of an application, as entity.* (GO24DEV-979, migration 957): entities with sections,
// items and properties of their own. The rights to an entity are access codes (Level).

const (
	MaxEntities     = 100      // entities of one application
	MaxEntityProps  = 100      // properties of one entity
	MaxEntityFile   = 10 << 20 // bytes of a file of an item
	MaxEntityPropOf = 65536    // characters of a property's value
)

var (
	ErrEntityCode       = i18n.E("err.app_entity_code")
	ErrEntityExists     = i18n.E("err.app_entity_exists")
	ErrEntityNotFound   = i18n.E("err.app_entity_not_found")
	ErrEntityMany       = i18n.E("err.app_entity_many", MaxEntities)
	ErrEntityAccess     = i18n.E("err.app_entity_access")
	ErrEntityProp       = i18n.E("err.app_entity_prop")
	ErrEntityPropExists = i18n.E("err.app_entity_prop_exists")
	ErrEntityPropAbsent = i18n.E("err.app_entity_prop_not_found")
	ErrEntityPropMany   = i18n.E("err.app_entity_props_many", MaxEntityProps)
	ErrEntitySection    = i18n.E("err.app_entity_section_not_found")
	ErrEntityItem       = i18n.E("err.app_entity_item_not_found")
	ErrEntityParent     = i18n.E("err.app_entity_parent")
	ErrEntityName       = i18n.E("err.app_entity_name")
	ErrEntityFile       = i18n.E("err.app_entity_file", MaxEntityFile>>20)
)

var (
	entityCode = regexp.MustCompile(`^[A-Za-z0-9_]{1,13}$`)
	propCode   = regexp.MustCompile(`^[A-Za-z0-9_]{1,50}$`)
)

// Entity is a storage of an application; Access — {access code: R, W or X}.
type Entity struct {
	ID     string            `json:"-"`
	Code   string            `json:"ENTITY"`
	Name   string            `json:"NAME"`
	Access map[string]string `json:"-"`
}

// The levels of the rights to an entity: read, write (sections and items), everything (properties, rights, the entity).
const (
	LevelRead  = "R"
	LevelWrite = "W"
	LevelFull  = "X"
)

// Level is the right the access codes give to an entity: the highest of the codes the employee holds; "" — none.
func Level(access map[string]string, codes []string) string {
	best := ""
	for _, c := range codes {
		if l := access[c]; strings.Compare(l, best) > 0 && (l == LevelRead || l == LevelWrite || l == LevelFull) {
			best = l // R < W < X, as letters
		}
	}
	return best
}

// CleanAccess checks {code: R|W|X}: the codes in upper case, the levels writes them.
func CleanAccess(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for code, l := range in {
		code, l = strings.ToUpper(strings.TrimSpace(code)), strings.ToUpper(strings.TrimSpace(l))
		if code == "" || len(code) > 50 || (l != LevelRead && l != LevelWrite && l != LevelFull) {
			return nil, ErrEntityAccess
		}
		out[code] = l
	}
	return out, nil
}

func (s *Store) entities(ctx context.Context, where string, args ...any) ([]Entity, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, code, name, access FROM app_entities `+where+` ORDER BY code`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Entity, error) {
		var e Entity
		err := row.Scan(&e.ID, &e.Code, &e.Name, &e.Access)
		return e, err
	})
}

// Entities of the application.
func (s *Store) Entities(ctx context.Context, appID string) ([]Entity, error) {
	return s.entities(ctx, `WHERE app_id::text = $1`, appID)
}

// EntityByCode is the application's entity of the code (in any case).
func (s *Store) EntityByCode(ctx context.Context, appID, code string) (Entity, error) {
	list, err := s.entities(ctx, `WHERE app_id::text = $1 AND lower(code) = lower($2)`, appID, code)
	if err != nil {
		return Entity{}, err
	}
	if len(list) == 0 {
		return Entity{}, ErrEntityNotFound
	}
	return list[0], nil
}

// AddEntity makes an entity of the application; access nil — everyone reads.
func (s *Store) AddEntity(ctx context.Context, appID, code, name string, access map[string]string) error {
	if !entityCode.MatchString(code) {
		return ErrEntityCode
	}
	if len([]rune(name)) > 255 {
		return ErrEntityName
	}
	if access == nil {
		access = map[string]string{"AU": LevelRead}
	}
	raw, _ := json.Marshal(access)
	tag, err := s.db.Exec(ctx, `INSERT INTO app_entities (app_id, code, name, access)
		SELECT $1::uuid, $2, $3, $4 WHERE (SELECT count(*) FROM app_entities WHERE app_id = $1::uuid) < $5
			AND NOT EXISTS (SELECT 1 FROM app_entities WHERE app_id = $1::uuid AND lower(code) = lower($2))`,
		appID, code, name, raw, MaxEntities)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.EntityByCode(ctx, appID, code); err == nil {
			return ErrEntityExists
		}
		return ErrEntityMany
	}
	return nil
}

// UpdateEntity renames the entity, gives it a new code or new rights (nil — the same).
func (s *Store) UpdateEntity(ctx context.Context, e Entity, name, code *string, access map[string]string) error {
	if name != nil {
		if len([]rune(*name)) > 255 {
			return ErrEntityName
		}
		e.Name = *name
	}
	if code != nil && *code != e.Code {
		if !entityCode.MatchString(*code) {
			return ErrEntityCode
		}
		e.Code = *code
	}
	if access != nil {
		e.Access = access
	}
	raw, _ := json.Marshal(e.Access)
	_, err := s.db.Exec(ctx, `UPDATE app_entities SET name = $2, code = $3, access = $4 WHERE id::text = $1`, e.ID, e.Name, e.Code, raw)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return ErrEntityExists
	}
	return err
}

// DeleteEntity removes the entity with its sections, items and files.
func (s *Store) DeleteEntity(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM app_entities WHERE id::text = $1`, id)
	return err
}

// ---------- properties ----------

// EntityProp is a property of the items of an entity: S — a string, N — a number, F — a file.
type EntityProp struct {
	Code string `json:"PROPERTY"`
	Name string `json:"NAME"`
	Type string `json:"TYPE"`
}

// EntityProps of the entity, in the order of adding.
func (s *Store) EntityProps(ctx context.Context, entityID string) ([]EntityProp, error) {
	rows, err := s.db.Query(ctx, `SELECT code, name, type FROM app_entity_props WHERE entity_id::text = $1 ORDER BY sort, code`, entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[EntityProp])
}

func checkProp(p EntityProp) error {
	if !propCode.MatchString(p.Code) || p.Type != "S" && p.Type != "N" && p.Type != "F" {
		return ErrEntityProp
	}
	if len([]rune(p.Name)) > 255 {
		return ErrEntityName
	}
	return nil
}

// AddEntityProp adds a property to the entity.
func (s *Store) AddEntityProp(ctx context.Context, entityID string, p EntityProp) error {
	if p.Type == "" {
		p.Type = "S"
	}
	if err := checkProp(p); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO app_entity_props (entity_id, code, name, type, sort)
		SELECT $1::uuid, $2, $3, $4, (SELECT coalesce(max(sort), 0) + 10 FROM app_entity_props WHERE entity_id = $1::uuid)
		WHERE (SELECT count(*) FROM app_entity_props WHERE entity_id = $1::uuid) < $5
			AND NOT EXISTS (SELECT 1 FROM app_entity_props WHERE entity_id = $1::uuid AND lower(code) = lower($2))`,
		entityID, p.Code, p.Name, p.Type, MaxEntityProps)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.entityProp(ctx, entityID, p.Code); err == nil {
			return ErrEntityPropExists
		}
		return ErrEntityPropMany
	}
	return nil
}

func (s *Store) entityProp(ctx context.Context, entityID, code string) (EntityProp, error) {
	var p EntityProp
	err := s.db.QueryRow(ctx, `SELECT code, name, type FROM app_entity_props WHERE entity_id::text = $1 AND lower(code) = lower($2)`,
		entityID, code).Scan(&p.Code, &p.Name, &p.Type)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrEntityPropAbsent
	}
	return p, err
}

// UpdateEntityProp changes a property: a new code moves the items' values to it; a new type drops the values the type
// cannot hold (a file — the other values, a number — what is not one, from a file — its files).
func (s *Store) UpdateEntityProp(ctx context.Context, entityID, code string, upd EntityProp) error {
	old, err := s.entityProp(ctx, entityID, code)
	if err != nil {
		return err
	}
	p := old
	if upd.Code != "" {
		p.Code = upd.Code
	}
	if upd.Name != "" {
		p.Name = upd.Name
	}
	if upd.Type != "" {
		p.Type = upd.Type
	}
	if err := checkProp(p); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if !strings.EqualFold(p.Code, old.Code) {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_entity_props WHERE entity_id::text = $1 AND lower(code) = lower($2))`,
			entityID, p.Code).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrEntityPropExists
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE app_entity_props SET code = $3, name = $4, type = $5 WHERE entity_id::text = $1 AND code = $2`,
		entityID, old.Code, p.Code, p.Name, p.Type); err != nil {
		return err
	}
	switch {
	case p.Type == "F" && old.Type != "F":
		_, err = tx.Exec(ctx, `UPDATE app_entity_items SET props = props - $2 WHERE entity_id::text = $1`, entityID, old.Code)
	case old.Type == "F" && p.Type != "F":
		_, err = tx.Exec(ctx, `DELETE FROM app_entity_files f USING app_entity_items i
			WHERE i.id = f.item_id AND i.entity_id::text = $1 AND f.slot = 'P:' || $2`, entityID, old.Code)
	case p.Type == "N" && old.Type == "S":
		_, err = tx.Exec(ctx, `UPDATE app_entity_items SET props = props - $2 WHERE entity_id::text = $1
			AND props ? $2 AND props->>$2 !~ '^\s*[-+]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][-+]?[0-9]+)?\s*$'`, entityID, old.Code)
	}
	if err != nil {
		return err
	}
	if p.Code != old.Code {
		if p.Type == "F" {
			_, err = tx.Exec(ctx, `UPDATE app_entity_files f SET slot = 'P:' || $3 FROM app_entity_items i
				WHERE i.id = f.item_id AND i.entity_id::text = $1 AND f.slot = 'P:' || $2`, entityID, old.Code, p.Code)
		} else {
			_, err = tx.Exec(ctx, `UPDATE app_entity_items SET props = (props - $2) || jsonb_build_object($3::text, props->$2)
				WHERE entity_id::text = $1 AND props ? $2`, entityID, old.Code, p.Code)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteEntityProp removes a property with its values.
func (s *Store) DeleteEntityProp(ctx context.Context, entityID, code string) error {
	p, err := s.entityProp(ctx, entityID, code)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, q := range []string{
		`DELETE FROM app_entity_props WHERE entity_id::text = $1 AND code = $2`,
		`UPDATE app_entity_items SET props = props - $2 WHERE entity_id::text = $1 AND props ? $2`,
		`DELETE FROM app_entity_files f USING app_entity_items i WHERE i.id = f.item_id AND i.entity_id::text = $1 AND f.slot = 'P:' || $2`,
	} {
		if _, err := tx.Exec(ctx, q, entityID, p.Code); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---------- conditions of lists ----------

// EntityCond is a condition of a list: Field — a field of the list (or «P:<code>» of a property), Op — =, !, >, <, >=,
// <=, % (contains), !% (does not); several Vals of = and ! — any of them.
type EntityCond struct {
	Field, Op string
	Vals      []string
}

// EntitySort is a field of a list's order.
type EntitySort struct {
	Field string
	Desc  bool
}

// entityCol is a field of a list in SQL: its expression and kind (text, num, time, bool).
type entityCol struct{ expr, kind string }

var sectionCols = map[string]entityCol{
	"ID": {"x.id", "num"}, "NAME": {"x.name", "text"}, "CODE": {"x.code", "text"}, "DESCRIPTION": {"x.description", "text"},
	"SORT": {"x.sort", "num"}, "ACTIVE": {"x.active", "bool"}, "SECTION": {"x.parent_id", "num"},
	"DATE_CREATE": {"x.created_at", "time"}, "TIMESTAMP_X": {"x.updated_at", "time"},
	"CREATED_BY": {"uc.num", "num"}, "MODIFIED_BY": {"uu.num", "num"},
}

var itemCols = map[string]entityCol{
	"ID": {"x.id", "num"}, "NAME": {"x.name", "text"}, "CODE": {"x.code", "text"}, "SORT": {"x.sort", "num"},
	"ACTIVE": {"x.active", "bool"}, "SECTION": {"x.section_id", "num"},
	"DATE_ACTIVE_FROM": {"x.active_from", "time"}, "DATE_ACTIVE_TO": {"x.active_to", "time"},
	"PREVIEW_TEXT": {"x.preview_text", "text"}, "DETAIL_TEXT": {"x.detail_text", "text"},
	"DATE_CREATE": {"x.created_at", "time"}, "TIMESTAMP_X": {"x.updated_at", "time"},
	"CREATED_BY": {"uc.num", "num"}, "MODIFIED_BY": {"uu.num", "num"},
}

// entityWhere adds the conditions to the query's arguments; props — the entity's properties for «P:<code>».
func entityWhere(cols map[string]entityCol, props []EntityProp, conds []EntityCond, args []any) (string, []any, error) {
	var where []string
	for _, c := range conds {
		col, ok := cols[c.Field]
		if code, isProp := strings.CutPrefix(c.Field, "P:"); isProp {
			i := slices.IndexFunc(props, func(p EntityProp) bool { return strings.EqualFold(p.Code, code) })
			if i < 0 || props[i].Type == "F" {
				return "", nil, ErrEntityPropAbsent
			}
			col, ok = entityCol{"(x.props->>'" + props[i].Code + "')", "text"}, true // the code is [A-Za-z0-9_]
			if props[i].Type == "N" {
				col.kind = "num"
			}
		}
		if !ok {
			return "", nil, i18n.E("err.portal_field", c.Field)
		}
		cast := map[string]string{"text": "text", "num": "numeric", "time": "timestamptz", "bool": "boolean"}[col.kind]
		expr := col.expr
		if col.kind == "num" && strings.HasPrefix(expr, "(x.props") {
			expr = "(" + expr + ")::numeric"
		}
		vals := c.Vals
		if col.kind == "bool" {
			vals = slices.Clone(vals)
			for i, v := range vals {
				vals[i] = strconv.FormatBool(strings.EqualFold(v, "Y") || v == "1" || strings.EqualFold(v, "true"))
			}
		}
		empty := len(vals) == 0 || len(vals) == 1 && vals[0] == ""
		for _, v := range vals {
			if v == "" {
				continue
			}
			if col.kind == "num" {
				if _, err := strconv.ParseFloat(v, 64); err != nil {
					return "", nil, i18n.E("err.portal_field", c.Field)
				}
			}
			if col.kind == "time" {
				if _, err := ParseEntityTime(v); err != nil {
					return "", nil, i18n.E("err.portal_field", c.Field)
				}
			}
		}
		if col.kind == "time" {
			vals = slices.Clone(vals)
			for i, v := range vals {
				if t, err := ParseEntityTime(v); err == nil {
					vals[i] = t.Format(time.RFC3339)
				}
			}
		}
		n := "$" + strconv.Itoa(len(args)+1)
		switch {
		case empty && (c.Op == "=" || c.Op == "%"):
			where = append(where, "("+expr+" IS NULL OR "+col.expr+"::text = '')")
			continue
		case empty && (c.Op == "!" || c.Op == "!%"):
			where = append(where, "("+expr+" IS NOT NULL AND "+col.expr+"::text <> '')")
			continue
		}
		switch c.Op {
		case "=":
			where = append(where, expr+" = ANY("+n+"::"+cast+"[])")
			args = append(args, vals)
		case "!":
			where = append(where, "("+expr+" IS NULL OR NOT "+expr+" = ANY("+n+"::"+cast+"[]))")
			args = append(args, vals)
		case ">", "<", ">=", "<=":
			where = append(where, expr+" "+c.Op+" "+n+"::"+cast)
			args = append(args, vals[0])
		case "%", "!%":
			like := expr + "::text ILIKE '%' || " + n + " || '%'"
			if c.Op == "!%" {
				like = "NOT coalesce(" + like + ", false)"
			}
			where = append(where, like)
			args = append(args, strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(vals[0]))
		default:
			return "", nil, i18n.E("err.portal_field", c.Op+c.Field)
		}
	}
	if len(where) == 0 {
		return "", args, nil
	}
	return " AND " + strings.Join(where, " AND "), args, nil
}

// entityOrder is ORDER BY of the list; the number last, so pages do not mix.
func entityOrder(cols map[string]entityCol, props []EntityProp, order []EntitySort) (string, error) {
	var parts []string
	for _, o := range order {
		col, ok := cols[o.Field]
		if code, isProp := strings.CutPrefix(o.Field, "P:"); isProp {
			i := slices.IndexFunc(props, func(p EntityProp) bool { return strings.EqualFold(p.Code, code) })
			if i < 0 || props[i].Type == "F" {
				return "", ErrEntityPropAbsent
			}
			col, ok = entityCol{"(x.props->>'" + props[i].Code + "')", "text"}, true
			if props[i].Type == "N" {
				col.expr = "(" + col.expr + ")::numeric"
			}
		}
		if !ok {
			return "", i18n.E("err.portal_field", o.Field)
		}
		dir := " ASC NULLS FIRST"
		if o.Desc {
			dir = " DESC NULLS LAST"
		}
		parts = append(parts, col.expr+dir)
	}
	return strings.Join(append(parts, "x.id"), ", "), nil
}

// ParseEntityTime reads a date: ISO 8601 with or without the time and zone, or DD.MM.YYYY [HH:MM[:SS]].
func ParseEntityTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
		"02.01.2006 15:04:05", "02.01.2006 15:04", "02.01.2006"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, i18n.E("err.portal_field", v)
}

// ---------- sections ----------

// EntitySection is a section of an entity; Parent 0 — at the top.
type EntitySection struct {
	ID          int64
	Parent      int64
	Name        string
	Code        string
	Description string
	Sort        int
	Active      bool
	Depth       int
	CreatedAt   time.Time
	CreatedBy   int64 // the employee's number
	UpdatedAt   time.Time
	UpdatedBy   int64
}

// EntitySections are a page of the sections of the entity by the conditions → the page and how many there are.
func (s *Store) EntitySections(ctx context.Context, entityID string, conds []EntityCond, order []EntitySort, offset, limit int) ([]EntitySection, int, error) {
	where, args, err := entityWhere(sectionCols, nil, conds, []any{entityID})
	if err != nil {
		return nil, 0, err
	}
	by, err := entityOrder(sectionCols, nil, order)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `WITH RECURSIVE d AS (
			SELECT id, 1 AS depth FROM app_entity_sections WHERE entity_id::text = $1 AND parent_id IS NULL
			UNION ALL SELECT c.id, d.depth + 1 FROM app_entity_sections c JOIN d ON c.parent_id = d.id)
		SELECT x.id, coalesce(x.parent_id, 0), x.name, x.code, x.description, x.sort, x.active, coalesce(d.depth, 1),
			x.created_at, coalesce(uc.num, 0), x.updated_at, coalesce(uu.num, 0), count(*) OVER ()
		FROM app_entity_sections x LEFT JOIN d ON d.id = x.id
			LEFT JOIN users uc ON uc.id = x.created_by LEFT JOIN users uu ON uu.id = x.updated_by
		WHERE x.entity_id::text = $1`+where+` ORDER BY `+by+` OFFSET `+strconv.Itoa(offset)+` LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EntitySection, error) {
		var x EntitySection
		err := row.Scan(&x.ID, &x.Parent, &x.Name, &x.Code, &x.Description, &x.Sort, &x.Active, &x.Depth, &x.CreatedAt, &x.CreatedBy,
			&x.UpdatedAt, &x.UpdatedBy, &total)
		return x, err
	})
	return list, total, err
}

// EntitySet are the fields to write: a field of the list (as in sectionCols, itemCols) → its value; nil — empty.
type EntitySet map[string]any

var (
	sectionSet = map[string]string{"NAME": "name", "CODE": "code", "DESCRIPTION": "description", "SORT": "sort", "ACTIVE": "active",
		"SECTION": "parent_id"}
	itemSet = map[string]string{"NAME": "name", "CODE": "code", "SORT": "sort", "ACTIVE": "active", "SECTION": "section_id",
		"DATE_ACTIVE_FROM": "active_from", "DATE_ACTIVE_TO": "active_to", "PREVIEW_TEXT": "preview_text", "DETAIL_TEXT": "detail_text"}
)

// entityAssign is «col = $n, …» of the fields; a section (SECTION) is checked to be the entity's.
func (s *Store) entityAssign(ctx context.Context, q pgx.Tx, entityID string, cols map[string]string, set EntitySet, args []any) ([]string, []any, error) {
	var out []string
	for f, v := range set {
		col, ok := cols[f]
		if !ok {
			return nil, nil, i18n.E("err.portal_field", f)
		}
		switch col {
		case "name":
			if name, _ := v.(string); len([]rune(name)) == 0 || len([]rune(name)) > 255 {
				return nil, nil, ErrEntityName
			}
		case "parent_id", "section_id":
			if id, _ := v.(int64); id == 0 {
				v = nil
			} else {
				var ok bool
				if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_entity_sections WHERE id = $1 AND entity_id::text = $2)`,
					id, entityID).Scan(&ok); err != nil {
					return nil, nil, err
				}
				if !ok {
					return nil, nil, ErrEntitySection
				}
			}
		}
		args = append(args, v)
		out = append(out, col+" = $"+strconv.Itoa(len(args)))
	}
	slices.Sort(out)
	return out, args, nil
}

// AddEntitySection adds a section (NAME is needed) → its number.
func (s *Store) AddEntitySection(ctx context.Context, entityID, userID string, set EntitySet) (int64, error) {
	if _, ok := set["NAME"]; !ok {
		return 0, ErrEntityName
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO app_entity_sections (entity_id, created_by, updated_by, name) VALUES ($1::uuid, $2::uuid, $2::uuid, '-')
		RETURNING id`, entityID, userID).Scan(&id); err != nil {
		return 0, err
	}
	assign, args, err := s.entityAssign(ctx, tx, entityID, sectionSet, set, []any{id})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE app_entity_sections SET `+strings.Join(assign, ", ")+` WHERE id = $1`, args...); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// UpdateEntitySection changes the fields of the section; it may not go under itself.
func (s *Store) UpdateEntitySection(ctx context.Context, entityID, userID string, id int64, set EntitySet) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_entity_sections WHERE id = $1 AND entity_id::text = $2)`, id, entityID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrEntitySection
	}
	if p, _ := set["SECTION"].(int64); p != 0 {
		var loop bool
		if err := tx.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id FROM app_entity_sections WHERE id = $1
				UNION ALL SELECT s.id, s.parent_id FROM app_entity_sections s JOIN up ON s.id = up.parent_id)
			SELECT EXISTS (SELECT 1 FROM up WHERE id = $2)`, p, id).Scan(&loop); err != nil {
			return err
		}
		if loop {
			return ErrEntityParent
		}
	}
	assign, args, err := s.entityAssign(ctx, tx, entityID, sectionSet, set, []any{id, userID})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE app_entity_sections SET `+strings.Join(append(assign, "updated_at = now()", "updated_by = $2::uuid"), ", ")+
		` WHERE id = $1`, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteEntitySection removes the section with its sections and items.
func (s *Store) DeleteEntitySection(ctx context.Context, entityID string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM app_entity_sections WHERE id = $1 AND entity_id::text = $2`, id, entityID) // the rest by the keys
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrEntitySection
	}
	return err
}

// ---------- items ----------

// EntityFile is a file of an item: its number and name.
type EntityFile struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// EntityItem is an item of an entity; Props — the values of the string and number properties, Files — by slots
// (PREVIEW_PICTURE, DETAIL_PICTURE, P:<code>).
type EntityItem struct {
	ID          int64
	Section     int64
	Name        string
	Code        string
	Active      bool
	Sort        int
	ActiveFrom  *time.Time
	ActiveTo    *time.Time
	PreviewText string
	DetailText  string
	Props       map[string]string
	Files       map[string]EntityFile
	CreatedAt   time.Time
	CreatedBy   int64
	UpdatedAt   time.Time
	UpdatedBy   int64
}

// EntityItems are a page of the items of the entity by the conditions → the page and how many there are.
func (s *Store) EntityItems(ctx context.Context, entityID string, props []EntityProp, conds []EntityCond, order []EntitySort, offset, limit int) ([]EntityItem, int, error) {
	where, args, err := entityWhere(itemCols, props, conds, []any{entityID})
	if err != nil {
		return nil, 0, err
	}
	by, err := entityOrder(itemCols, props, order)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT x.id, coalesce(x.section_id, 0), x.name, x.code, x.active, x.sort, x.active_from, x.active_to,
			x.preview_text, x.detail_text, x.props,
			coalesce((SELECT jsonb_object_agg(f.slot, jsonb_build_object('id', f.id, 'name', f.name)) FROM app_entity_files f WHERE f.item_id = x.id), '{}'),
			x.created_at, coalesce(uc.num, 0), x.updated_at, coalesce(uu.num, 0), count(*) OVER ()
		FROM app_entity_items x LEFT JOIN users uc ON uc.id = x.created_by LEFT JOIN users uu ON uu.id = x.updated_by
		WHERE x.entity_id::text = $1`+where+` ORDER BY `+by+` OFFSET `+strconv.Itoa(offset)+` LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (EntityItem, error) {
		var x EntityItem
		err := row.Scan(&x.ID, &x.Section, &x.Name, &x.Code, &x.Active, &x.Sort, &x.ActiveFrom, &x.ActiveTo, &x.PreviewText, &x.DetailText,
			&x.Props, &x.Files, &x.CreatedAt, &x.CreatedBy, &x.UpdatedAt, &x.UpdatedBy, &total)
		return x, err
	})
	return list, total, err
}

// EntityFileIn is a file to put in a slot of an item; nil in EntityItemIn.Files removes the slot's file.
type EntityFileIn struct {
	Name string
	Data []byte
}

// EntityItemIn is what is written to an item: the fields, the values of properties ("" removes one) and files.
type EntityItemIn struct {
	Set   EntitySet
	Props map[string]string
	Files map[string]*EntityFileIn
}

// writeItem writes the values of properties and the files of an item; the properties are of the entity.
func (s *Store) writeItem(ctx context.Context, tx pgx.Tx, id int64, props []EntityProp, in EntityItemIn) error {
	set, del := map[string]string{}, []string{}
	for code, v := range in.Props {
		i := slices.IndexFunc(props, func(p EntityProp) bool { return strings.EqualFold(p.Code, code) })
		if i < 0 {
			continue // the portal passes over what is not a property
		}
		p := props[i]
		switch {
		case p.Type == "F":
			continue
		case strings.TrimSpace(v) == "":
			del = append(del, p.Code)
			continue
		case p.Type == "N":
			if _, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err != nil {
				return i18n.E("err.portal_field", "PROPERTY_VALUES["+p.Code+"]")
			}
			v = strings.TrimSpace(v)
		case len([]rune(v)) > MaxEntityPropOf:
			return i18n.E("err.portal_field", "PROPERTY_VALUES["+p.Code+"]")
		}
		set[p.Code] = v
	}
	raw, _ := json.Marshal(set)
	if _, err := tx.Exec(ctx, `UPDATE app_entity_items SET props = (props - $2::text[]) || $3::jsonb WHERE id = $1`, id, del, raw); err != nil {
		return err
	}
	for slot, f := range in.Files {
		if code, ok := strings.CutPrefix(slot, "P:"); ok {
			i := slices.IndexFunc(props, func(p EntityProp) bool { return strings.EqualFold(p.Code, code) && p.Type == "F" })
			if i < 0 {
				continue
			}
			slot = "P:" + props[i].Code
		}
		var err error
		if f == nil {
			_, err = tx.Exec(ctx, `DELETE FROM app_entity_files WHERE item_id = $1 AND slot = $2`, id, slot)
		} else {
			if len(f.Data) > MaxEntityFile || f.Name == "" || len([]rune(f.Name)) > 255 {
				return ErrEntityFile
			}
			_, err = tx.Exec(ctx, `INSERT INTO app_entity_files (item_id, slot, name, data) VALUES ($1, $2, $3, $4)
				ON CONFLICT (item_id, slot) DO UPDATE SET id = DEFAULT, name = EXCLUDED.name, data = EXCLUDED.data`, id, slot, f.Name, f.Data)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// AddEntityItem adds an item (NAME is needed) → its number.
func (s *Store) AddEntityItem(ctx context.Context, entityID, userID string, props []EntityProp, in EntityItemIn) (int64, error) {
	if _, ok := in.Set["NAME"]; !ok {
		return 0, ErrEntityName
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO app_entity_items (entity_id, created_by, updated_by, name) VALUES ($1::uuid, $2::uuid, $2::uuid, '-')
		RETURNING id`, entityID, userID).Scan(&id); err != nil {
		return 0, err
	}
	assign, args, err := s.entityAssign(ctx, tx, entityID, itemSet, in.Set, []any{id})
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE app_entity_items SET `+strings.Join(assign, ", ")+` WHERE id = $1`, args...); err != nil {
		return 0, err
	}
	if err := s.writeItem(ctx, tx, id, props, in); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// UpdateEntityItem changes the item: the fields given, the properties given (the others stay), the files given.
func (s *Store) UpdateEntityItem(ctx context.Context, entityID, userID string, id int64, props []EntityProp, in EntityItemIn) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	assign, args, err := s.entityAssign(ctx, tx, entityID, itemSet, in.Set, []any{id, entityID, userID})
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE app_entity_items SET `+strings.Join(append(assign, "updated_at = now()", "updated_by = $3::uuid"), ", ")+
		` WHERE id = $1 AND entity_id::text = $2`, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrEntityItem
	}
	if err := s.writeItem(ctx, tx, id, props, in); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteEntityItem removes the item with its files.
func (s *Store) DeleteEntityItem(ctx context.Context, entityID string, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM app_entity_items WHERE id = $1 AND entity_id::text = $2`, id, entityID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrEntityItem
	}
	return err
}

// EntityFileData is a file of an item with the entity it belongs to, for its download.
func (s *Store) EntityFileData(ctx context.Context, fileID int64) (Entity, string, []byte, error) {
	var (
		e    Entity
		name string
		data []byte
	)
	err := s.db.QueryRow(ctx, `SELECT e.id::text, e.code, e.name, e.access, f.name, f.data FROM app_entity_files f
		JOIN app_entity_items i ON i.id = f.item_id JOIN app_entities e ON e.id = i.entity_id WHERE f.id = $1`, fileID).
		Scan(&e.ID, &e.Code, &e.Name, &e.Access, &name, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		err = i18n.E("err.not_found")
	}
	return e, name, data, err
}
