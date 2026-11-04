package company

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Типы отсутствий» of the absence chart: the built-in reasons (an administrator renames, recolours or
// turns them off) and the company's own ones. A reason turned off is not offered for new absences; the absences kept
// with it still show it.

// AbsenceKinds are the built-in reasons, in their order.
var AbsenceKinds = []string{"vacation", "sick", "trip", "dayoff", "absent", "maternity", "other"}

// AbsenceColors are the colours a reason may have.
var AbsenceColors = []string{"green", "red", "violet", "orange", "blue", "gray"}

var builtinColor = map[string]string{"vacation": "green", "sick": "red", "trip": "violet", "dayoff": "orange", "absent": "red",
	"maternity": "blue", "other": "orange"}

// AbsenceKind is a reason of absence.
type AbsenceKind struct {
	Key     string `json:"key"`
	Name    string `json:"name"` // own name; '' — the built-in one in the reader's language
	Color   string `json:"color"`
	Off     bool   `json:"off"`
	Builtin bool   `json:"builtin"`
}

// kindsCache: the reasons for showing them (absKind, absColor) without a query, refreshed by the pages.
type kindsCache struct {
	at   time.Time
	list []AbsenceKind
}

var absKinds atomic.Pointer[kindsCache]

// defaultKinds: the built-in reasons as they are before any setting.
func defaultKinds() []AbsenceKind {
	out := make([]AbsenceKind, len(AbsenceKinds))
	for i, k := range AbsenceKinds {
		out[i] = AbsenceKind{Key: k, Color: builtinColor[k], Builtin: true}
	}
	return out
}

func kindOf(key string) (AbsenceKind, bool) {
	list := defaultKinds()
	if c := absKinds.Load(); c != nil {
		list = c.list
	}
	i := slices.IndexFunc(list, func(k AbsenceKind) bool { return k.Key == key })
	if i < 0 {
		return AbsenceKind{Key: key, Color: "gray"}, false
	}
	return list[i], true
}

// AbsenceKindName is the reason's name: its own, or the built-in one through tr.
func AbsenceKindName(key string, tr func(string, ...any) string) string {
	k, ok := kindOf(key)
	switch {
	case k.Name != "":
		return k.Name
	case ok && k.Builtin || slices.Contains(AbsenceKinds, key):
		return tr("absence.kind." + key)
	}
	return tr("absence.kind.other")
}

// AbsenceKindColor is the reason's colour (one of AbsenceColors).
func AbsenceKindColor(key string) string {
	k, _ := kindOf(key)
	return k.Color
}

// AbsenceKindList: every reason — the built-in ones first, then the company's own by when they were added.
func (s *Store) AbsenceKindList(ctx context.Context) ([]AbsenceKind, error) {
	rows, err := s.db.Query(ctx, `SELECT key, name, color, off FROM absence_kinds ORDER BY created_at, key`)
	if err != nil {
		return nil, err
	}
	saved, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AbsenceKind, error) {
		var k AbsenceKind
		return k, r.Scan(&k.Key, &k.Name, &k.Color, &k.Off)
	})
	if err != nil {
		return nil, err
	}
	list := defaultKinds()
	for _, k := range saved {
		if i := slices.IndexFunc(list, func(b AbsenceKind) bool { return b.Key == k.Key }); i >= 0 {
			if k.Color == "" {
				k.Color = list[i].Color
			}
			k.Builtin, list[i] = true, k
			continue
		}
		list = append(list, k)
	}
	absKinds.Store(&kindsCache{at: time.Now(), list: slices.Clone(list)})
	return list, nil
}

// ActiveAbsenceKinds: the reasons offered for new absences.
func (s *Store) ActiveAbsenceKinds(ctx context.Context) ([]AbsenceKind, error) {
	list, err := s.AbsenceKindList(ctx)
	return slices.DeleteFunc(slices.Clone(list), func(k AbsenceKind) bool { return k.Off }), err
}

// RefreshAbsenceKinds reads the reasons again when the cache is older than half a minute (another process may have
// changed them).
func (s *Store) RefreshAbsenceKinds(ctx context.Context) {
	if c := absKinds.Load(); c != nil && time.Since(c.at) < 30*time.Second {
		return
	}
	_, _ = s.AbsenceKindList(ctx)
}

// knownKind: the reason exists and is on, or it is the one the absence already has (keep).
func (s *Store) knownKind(ctx context.Context, kind, keep string) (bool, error) {
	list, err := s.AbsenceKindList(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(list, func(k AbsenceKind) bool { return k.Key == kind && (!k.Off || k.Key == keep) }), nil
}

// SaveAbsenceKinds stores the reasons (an administrator's or an HR officer's with «absences»; checked by the caller):
// for the built-in ones the name (” — the standard one), colour and off; the company's own ones need a name, those
// left out of the list are deleted when no absence has them and turned off otherwise. A reason without a key is new.
func (s *Store) SaveAbsenceKinds(ctx context.Context, in []AbsenceKind) error {
	cur, err := s.AbsenceKindList(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	// at least one reason stays on: the ones in the list as given, the built-in ones left out as they are
	on := slices.ContainsFunc(in, func(k AbsenceKind) bool { return !k.Off })
	for _, c := range cur {
		on = on || c.Builtin && !c.Off && !slices.ContainsFunc(in, func(k AbsenceKind) bool { return k.Key == c.Key })
	}
	if !on {
		return i18n.E("err.absence_kind_none")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, k := range in {
		k.Name = strings.TrimSpace(k.Name)
		if k.Color == "" {
			k.Color = builtinColor[k.Key]
		}
		if !slices.Contains(AbsenceColors, k.Color) || len([]rune(k.Name)) > 60 {
			return i18n.E("err.bad_form")
		}
		builtin := slices.Contains(AbsenceKinds, k.Key)
		if !builtin {
			if k.Name == "" {
				return i18n.E("err.absence_kind_name")
			}
			if k.Key == "" {
				k.Key = newKindKey()
			} else if !slices.ContainsFunc(cur, func(c AbsenceKind) bool { return c.Key == k.Key }) {
				return i18n.E("err.bad_form")
			}
		}
		if seen[k.Key] {
			return i18n.E("err.bad_form")
		}
		seen[k.Key] = true
		if _, err := tx.Exec(ctx, `INSERT INTO absence_kinds (key, name, color, off) VALUES ($1, $2, $3, $4)
			ON CONFLICT (key) DO UPDATE SET name = $2, color = $3, off = $4`, k.Key, k.Name, k.Color, k.Off); err != nil {
			return err
		}
	}
	for _, c := range cur {
		if c.Builtin || seen[c.Key] {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM absence_kinds k WHERE key = $1 AND NOT EXISTS (SELECT 1 FROM absences WHERE kind = k.key)`,
			c.Key); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE absence_kinds SET off = true WHERE key = $1`, c.Key); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	_, err = s.AbsenceKindList(ctx)
	return err
}

// newKindKey: "c" and 8 hex digits — never one of the built-in keys.
func newKindKey() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "c" + hex.EncodeToString(b)
}

// IsAbsenceKind: a reason that exists (by the cache — a built-in one, or the company's own as last read).
func IsAbsenceKind(key string) bool {
	_, ok := kindOf(key)
	return ok
}
