package company

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Настройки → Сотрудники»: who invites employees (administrators and HR officers with the right, or
// every employee) and «Быстрая регистрация» — a link anyone may open to ask to join: for some e-mail domains only, an
// administrator confirming each request or the sign-up link mailed at once.

const settingStaff = "staff_settings"

// MaxJoinRequests is how many requests may wait at once: more are refused until some are decided.
const MaxJoinRequests = 500

// StaffSettings is the settings of «Сотрудники».
type StaffSettings struct {
	InviteAll bool     `json:"invite_all"` // every employee invites, not only administrators and HR officers
	Register  bool     `json:"register"`   // «Быстрая регистрация» is on
	Token     string   `json:"token"`      // of the link /register/{token}
	Domains   []string `json:"domains"`    // e-mail domains allowed to register (none — any)
	Approve   bool     `json:"approve"`    // an administrator confirms each request
	By        string   `json:"by"`         // who switched it on: the letters with sign-up links go from their mailbox
}

// StaffSettings reads the settings: by default only administrators invite, the link is off, requests need confirmation.
func (s *Store) StaffSettings(ctx context.Context) (StaffSettings, error) {
	set := StaffSettings{Approve: true}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingStaff).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return set, nil
	}
	if err != nil {
		return set, err
	}
	err = json.Unmarshal(raw, &set)
	return set, err
}

// CleanDomains splits the domains of the form (spaces, commas, new lines; «@» and the case dropped), each once.
func CleanDomains(text string) ([]string, error) {
	out := []string{}
	for _, d := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		d = strings.TrimPrefix(d, "@")
		if !validDomain(d) {
			return nil, i18n.E("err.staff_domain", d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if len(out) > 50 {
		return nil, i18n.E("err.staff_domains")
	}
	return out, nil
}

func validDomain(d string) bool {
	if len(d) > 253 || !strings.Contains(d, ".") || strings.HasPrefix(d, ".") || strings.HasSuffix(d, ".") {
		return false
	}
	for _, r := range d {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r > 127) {
			return false
		}
	}
	return true
}

// SaveStaffSettings keeps the settings; a link switched on gets a token when it has none, and the one who saved it becomes
// the sender of the letters.
func (s *Store) SaveStaffSettings(ctx context.Context, set StaffSettings, by string) (StaffSettings, error) {
	if set.Register && set.Token == "" {
		set.Token = newToken()
	}
	if set.Domains == nil {
		set.Domains = []string{}
	}
	set.By = by
	raw, err := json.Marshal(set)
	if err != nil {
		return set, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingStaff, raw, by)
	return set, err
}

// RenewJoinLink gives the registration link a new token: the old link stops working.
func (s *Store) RenewJoinLink(ctx context.Context, by string) (StaffSettings, error) {
	set, err := s.StaffSettings(ctx)
	if err != nil {
		return set, err
	}
	set.Token = newToken()
	return s.SaveStaffSettings(ctx, set, by)
}

func newToken() string {
	b := make([]byte, 18)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// JoinOpen reports whether the link of the token works now.
func (set StaffSettings) JoinOpen(token string) bool {
	return set.Register && set.Token != "" && subtle.ConstantTimeCompare([]byte(set.Token), []byte(token)) == 1
}

// DomainAllowed: the e-mail is of an allowed domain (any when none are set).
func (set StaffSettings) DomainAllowed(email string) bool {
	if len(set.Domains) == 0 {
		return true
	}
	_, domain, _ := strings.Cut(strings.ToLower(email), "@")
	return slices.Contains(set.Domains, domain)
}

// JoinRequest is a request of «Быстрая регистрация» waiting for an administrator, or decided.
type JoinRequest struct {
	ID, Email, Name, IP string
	State               string // pending, accepted, rejected
	Created             time.Time
	DecidedBy           string // the name
	DecidedAt           *time.Time
	InviteID            string
}

// ErrJoinWaiting: a request of this e-mail waits already, or it is invited and the link still works.
var ErrJoinWaiting = errors.New("join request waiting")

// CheckJoin checks a request of the registration page: a right e-mail of an allowed domain, a person not in go24
// yet (CheckInvite), not invited with a working link and without a request waiting (ErrJoinWaiting).
func (s *Store) CheckJoin(ctx context.Context, set StaffSettings, email, name string) (string, string, error) {
	email, name, err := s.CheckInvite(ctx, email, name)
	if err != nil {
		return "", "", err
	}
	if !set.DomainAllowed(email) {
		return "", "", i18n.E("err.join_domain")
	}
	var waiting bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM join_requests WHERE lower(email) = $1 AND state = 'pending')
		OR EXISTS (SELECT 1 FROM employee_invites WHERE lower(email) = $1 AND revoked_at IS NULL AND expires_at > now())`,
		email).Scan(&waiting); err != nil {
		return "", "", err
	}
	if waiting {
		return email, name, ErrJoinWaiting
	}
	return email, name, nil
}

// AddJoinRequest keeps a request and notifies those who invite employees: the administrators (of go24 or of the
// groups) and HR officers with the right.
func (s *Store) AddJoinRequest(ctx context.Context, email, name, ip string, adminGroups []string) (string, error) {
	var id string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM join_requests WHERE state = 'pending'`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxJoinRequests {
			return i18n.E("err.join_busy")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO join_requests (email, name, ip) VALUES ($1, $2, $3) RETURNING id::text`,
			email, name, ip).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
			SELECT id, 'join_request', $1, '/company/invite#requests' FROM users
			WHERE active AND NOT guest AND (admin OR groups && $2::text[] OR hr AND 'invite' = ANY (hr_rights))`,
			name+" <"+email+">", adminGroups)
		return err
	})
	return id, err
}

// JoinRequests: the waiting requests first, then the decided ones of the last 30 days; the newest first.
func (s *Store) JoinRequests(ctx context.Context) ([]JoinRequest, error) {
	rows, err := s.db.Query(ctx, `SELECT j.id::text, j.email, j.name, j.ip, j.state, j.created_at, coalesce(u.name, ''),
			j.decided_at, coalesce(j.invite_id::text, '')
		FROM join_requests j LEFT JOIN users u ON u.id = j.decided_by
		WHERE j.state = 'pending' OR j.decided_at > now() - interval '30 days'
		ORDER BY j.state <> 'pending', j.created_at DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (JoinRequest, error) {
		var j JoinRequest
		err := r.Scan(&j.ID, &j.Email, &j.Name, &j.IP, &j.State, &j.Created, &j.DecidedBy, &j.DecidedAt, &j.InviteID)
		return j, err
	})
}

// PendingJoin is a waiting request (ErrNotFound — none, or decided).
func (s *Store) PendingJoin(ctx context.Context, id string) (JoinRequest, error) {
	var j JoinRequest
	err := s.db.QueryRow(ctx, `SELECT id::text, email, name, ip, state, created_at FROM join_requests
		WHERE id::text = $1 AND state = 'pending'`, id).Scan(&j.ID, &j.Email, &j.Name, &j.IP, &j.State, &j.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, err
}

// DecideJoin closes a waiting request: accepted (with the invitation made of it) or rejected.
func (s *Store) DecideJoin(ctx context.Context, id, by string, accept bool, inviteID string) error {
	state := "rejected"
	if accept {
		state = "accepted"
	}
	tag, err := s.db.Exec(ctx, `UPDATE join_requests SET state = $2, decided_by = $3, decided_at = now(), invite_id = NULLIF($4, '')::uuid
		WHERE id::text = $1 AND state = 'pending'`, id, state, by, inviteID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
