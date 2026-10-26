// Package ipaccess is «Ограничение доступа по IP» of «Настройки → Безопасность»: an administrator
// makes rules — for which employees and departments (with their sub-departments), or for everybody, from which
// addresses, ranges and subnets go24 opens. An employee no rule names works from anywhere; one some rules name — from
// the addresses of any of them. The rules are a company setting (company_settings "ip_access").
package ipaccess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

const settingKey = "ip_access"

// cacheFor is how long the rules read are trusted: every request is checked, the settings are not read for each.
const cacheFor = 15 * time.Second

// Rule: whom it is for (All — every employee; else Users and Depts) and the addresses they work from — an address,
// a range «from-to» or a subnet «address/bits», IPv4 or IPv6.
type Rule struct {
	ID    string   `json:"id"`
	Title string   `json:"title,omitempty"`
	All   bool     `json:"all,omitempty"`
	Users []string `json:"users,omitempty"`
	Depts []string `json:"depts,omitempty"`
	Nets  []string `json:"nets"`
}

type Store struct {
	db *pgxpool.Pool
	mu sync.Mutex
	at time.Time
	rs []Rule
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Rules are the rules in the order they were made.
func (s *Store) Rules(ctx context.Context) ([]Rule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && time.Since(s.at) < cacheFor {
		return s.rs, nil
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		raw, err = []byte("[]"), nil
	}
	if err != nil {
		return nil, err
	}
	var rs []Rule
	if err := json.Unmarshal(raw, &rs); err != nil {
		rs = nil
	}
	s.rs, s.at = rs, time.Now()
	return rs, nil
}

// Save keeps the rules (the caller checks the actor is an administrator and that they do not shut themselves out,
// see Check); each is checked and its addresses written as parsed.
func (s *Store) Save(ctx context.Context, actorID string, rs []Rule) error {
	for i := range rs {
		r := &rs[i]
		r.Title = strings.TrimSpace(r.Title)
		if !r.All && len(r.Users)+len(r.Depts) == 0 {
			return i18n.E("ipaccess.err_whom")
		}
		if r.All {
			r.Users, r.Depts = nil, nil
		}
		nets, err := Parse(strings.Join(r.Nets, "\n"))
		if err != nil {
			return err
		}
		if len(nets) == 0 {
			return i18n.E("ipaccess.err_nets")
		}
		r.Nets = nets
		if r.ID == "" {
			r.ID = fmt.Sprintf("r%d", time.Now().UnixNano())
		}
	}
	if rs == nil {
		rs = []Rule{}
	}
	raw, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingKey, raw, actorID); err != nil {
		return err
	}
	s.mu.Lock()
	s.rs, s.at = rs, time.Now()
	s.mu.Unlock()
	return nil
}

// Allowed reports whether the employee works from the address by the saved rules.
func (s *Store) Allowed(ctx context.Context, userID, ip string) (bool, error) {
	rs, err := s.Rules(ctx)
	if err != nil {
		return false, err
	}
	return s.Check(ctx, userID, ip, rs)
}

// Check reports whether the employee works from the address by the rules rs.
func (s *Store) Check(ctx context.Context, userID, ip string, rs []Rule) (bool, error) {
	if len(rs) == 0 {
		return true, nil
	}
	var depts []string // the employee's departments and the ones above them, asked once when a rule needs them
	asked := false
	named := false
	addr, _ := netip.ParseAddr(ip)
	addr = addr.Unmap()
	for _, r := range rs {
		mine := r.All || slices.Contains(r.Users, userID)
		if !mine && len(r.Depts) > 0 {
			if !asked {
				rows, err := s.db.Query(ctx, `WITH RECURSIVE dep AS (
						SELECT department_id AS id FROM user_departments WHERE user_id::text = $1
						UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
					SELECT id::text FROM dep`, userID)
				if err != nil {
					return false, err
				}
				if depts, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
					return false, err
				}
				asked = true
			}
			mine = slices.ContainsFunc(r.Depts, func(d string) bool { return slices.Contains(depts, d) })
		}
		if !mine {
			continue
		}
		named = true
		if addr.IsValid() && Match(r.Nets, addr) {
			return true, nil
		}
	}
	return !named, nil
}

// Parse reads the addresses written one per line (or by commas, spaces): an address, «from-to» or «address/bits»;
// it gives them written the one way, or the error naming the wrong one.
func Parse(text string) ([]string, error) {
	var out []string
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' || r == ';' || r == ' ' || r == '\t' }) {
		n, err := parseOne(f)
		if err != nil {
			return nil, i18n.E("ipaccess.err_addr", f)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

func parseOne(f string) (string, error) {
	if from, to, ok := strings.Cut(f, "-"); ok {
		a, err := netip.ParseAddr(strings.TrimSpace(from))
		if err != nil {
			return "", err
		}
		b, err := netip.ParseAddr(strings.TrimSpace(to))
		if err != nil {
			return "", err
		}
		a, b = a.Unmap(), b.Unmap()
		if a.Is4() != b.Is4() || b.Less(a) {
			return "", errors.New("bad range")
		}
		return a.String() + "-" + b.String(), nil
	}
	if strings.Contains(f, "/") {
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return "", err
		}
		return p.Masked().String(), nil
	}
	a, err := netip.ParseAddr(f)
	if err != nil {
		return "", err
	}
	return a.Unmap().String(), nil
}

// Match: the address is one of nets (as Parse writes them).
func Match(nets []string, addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, n := range nets {
		if from, to, ok := strings.Cut(n, "-"); ok {
			a, err1 := netip.ParseAddr(from)
			b, err2 := netip.ParseAddr(to)
			if err1 == nil && err2 == nil && a.Is4() == addr.Is4() && !addr.Less(a) && !b.Less(addr) {
				return true
			}
			continue
		}
		if p, err := netip.ParsePrefix(n); err == nil {
			if p.Contains(addr) {
				return true
			}
			continue
		}
		if a, err := netip.ParseAddr(n); err == nil && a == addr {
			return true
		}
	}
	return false
}
