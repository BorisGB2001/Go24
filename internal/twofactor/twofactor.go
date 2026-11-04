// Package twofactor is «Двухэтапная авторизация» of «Настройки → Безопасность»: an administrator makes
// the second factor required — for administrators or for every employee — and gives days to set it up. The factor
// itself (a code app, a key) is Authentik's: go24 sees it in the ID token (amr «mfa») and lets in, reminds or refuses.
// The policy is a company setting (company_settings "two_factor"); users.mfa_at is the last sign-in with the factor.
package twofactor

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

const settingKey = "two_factor"

// Modes: "" — not required, "admins" — for administrators, "all" — for every employee.
var Modes = []string{"", "admins", "all"}

// DaysChoice are the periods to set the factor up offered in the form (0 — at once).
var DaysChoice = []int{0, 1, 3, 7, 14, 30}

// Policy: Since is when the factor became required (a change of the mode or days starts the period anew).
type Policy struct {
	Mode  string    `json:"mode"`
	Days  int       `json:"days"`
	Since time.Time `json:"since"`
}

// For: the factor is required of the employee.
func (p Policy) For(admin bool) bool { return p.Mode == "all" || p.Mode == "admins" && admin }

// Deadline is when an employee who joined at joined must have the factor: the period counts from the requirement or
// from joining, whichever is later.
func (p Policy) Deadline(joined time.Time) time.Time {
	from := p.Since
	if joined.After(from) {
		from = joined
	}
	return from.Add(time.Duration(p.Days) * 24 * time.Hour)
}

// Verdict of a sign-in.
type Verdict int

const (
	Pass   Verdict = iota // the factor is not required, or was used
	Remind                // required, not used, the period still runs: in, with a reminder
	Deny                  // required, not used, the period is over
)

// Judge decides on a sign-in with (mfa) or without the second factor.
func (p Policy) Judge(admin, mfa bool, joined, now time.Time) (Verdict, time.Time) {
	if mfa || !p.For(admin) {
		return Pass, time.Time{}
	}
	due := p.Deadline(joined)
	if now.Before(due) {
		return Remind, due
	}
	return Deny, due
}

// MFA: amr of the ID token names a second factor.
func MFA(amr []string) bool { return slices.Contains(amr, "mfa") }

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Policy is the saved policy (none — not required).
func (s *Store) Policy(ctx context.Context) (Policy, error) {
	var p Policy
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil || !slices.Contains(Modes, p.Mode) {
		return Policy{}, nil
	}
	return p, nil
}

// Save keeps the policy; a changed mode or period starts it from now. The caller checks the actor is an
// administrator. It reports whether anything changed.
func (s *Store) Save(ctx context.Context, actorID string, p Policy) (bool, error) {
	if !slices.Contains(Modes, p.Mode) {
		return false, i18n.E("twofa.err_mode")
	}
	if p.Days < 0 || p.Days > 365 {
		return false, i18n.E("twofa.err_days")
	}
	old, err := s.Policy(ctx)
	if err != nil {
		return false, err
	}
	if p.Mode == "" {
		p.Days = 0
	}
	if p.Mode == old.Mode && p.Days == old.Days {
		return false, nil
	}
	p.Since = time.Now()
	raw, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingKey, raw, actorID)
	return err == nil, err
}

// Used notes a sign-in of the employee with the second factor.
func (s *Store) Used(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET mfa_at = now() WHERE id::text = $1`, userID)
	return err
}

// Person is a line of the employees' list: whether the factor is required of them and their last sign-in with it.
type Person struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Admin    bool       `json:"admin"`
	Required bool       `json:"required"`
	MFAAt    *time.Time `json:"mfa_at"`
	Deadline *time.Time `json:"deadline,omitempty"` // required and never used: until when they may sign in without
}

// People are the active employees (not guests), those who never used the factor first, then by name; adminGroups
// are Authentik's administrators' groups.
func (s *Store) People(ctx context.Context, p Policy, adminGroups []string) ([]Person, error) {
	if adminGroups == nil {
		adminGroups = []string{}
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, name, admin OR groups && $1::text[], mfa_at, created_at FROM users
		WHERE active AND NOT guest AND oidc_sub NOT LIKE 'legacy:%' ORDER BY mfa_at IS NOT NULL, lower(name)`, adminGroups)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var x Person
		var joined time.Time
		if err := rows.Scan(&x.ID, &x.Name, &x.Admin, &x.MFAAt, &joined); err != nil {
			return nil, err
		}
		x.Required = p.For(x.Admin)
		if x.Required && x.MFAAt == nil {
			d := p.Deadline(joined)
			x.Deadline = &d
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
