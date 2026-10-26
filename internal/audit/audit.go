// Package audit is «Журнал событий» of «Настройки → Безопасность»: who signed in and from where, who
// was refused, who switched the admin mode, who made or took away an administrator, wrong API keys, keys and webhooks
// made and removed, changes of the general settings, employees dismissed and returned. Only administrators read it;
// it keeps Days days (see Prune).
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

// Kinds of events, in the order the filter lists them.
var Kinds = []string{"login", "login_denied", "logout", "admin_on", "admin_off", "admin_granted", "admin_revoked",
	"ip_denied", "ip_rules_saved", "backup_saved", "backup_started", "backup_restore", "api_bad_key", "key_created", "key_revoked", "hook_saved", "hook_deleted", "app_saved", "app_installed", "app_uninstalled", "app_deleted", "portal_saved", "user_fired", "user_returned", "audit_days"}

// DaysChoice are the periods of keeping the log offered in the form.
var DaysChoice = []int{30, 90, 180, 365, 730}

// DefaultDays is how long the log is kept until an administrator chooses.
const DefaultDays = 180

const keyDays = "audit_days"

// Event is a line of the log. UserID is who did it ("" — nobody signed in: a wrong key); Target is what it was
// about (a key's or webhook's name, the employee), TargetID — the employee.
type Event struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	UserID   string    `json:"user_id,omitempty"`
	Name     string    `json:"name,omitempty"`
	IP       string    `json:"ip,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	Target   string    `json:"target,omitempty"`
	TargetID string    `json:"target_id,omitempty"`
}

// Filter of List: a kind, a person (who did it or whom it was about), a period by days ([From, To], To inclusive),
// Before — the events older than this id (the next page), Limit (100 by default, at most 1000).
type Filter struct {
	Kind   string
	UserID string
	From   time.Time
	To     time.Time
	Before int64
	Limit  int
}

type Store struct {
	db *pgxpool.Pool
	mu sync.Mutex
	// bad is when a wrong key from an address (or a refusal by «Ограничение доступа по IP» of a person at an address)
	// was last written: a scanner trying keys makes a line a minute, not a line a request
	bad map[string]time.Time
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db, bad: map[string]time.Time{}} }

// Add writes an event; Name is taken from the user when not given.
func (s *Store) Add(ctx context.Context, e Event) error {
	if e.Kind == "api_bad_key" && !s.badDue(e.IP, time.Now()) {
		return nil
	}
	if e.Kind == "ip_denied" && !s.badDue(e.UserID+" "+e.IP, time.Now()) { // a page asks the server many times
		return nil
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO audit_log (kind, user_id, name, ip, agent, target, target_id)
		VALUES ($1, NULLIF($2, '')::uuid, COALESCE(NULLIF($3, ''), (SELECT name FROM users WHERE id::text = $2), ''),
		        $4, $5, $6, NULLIF($7, '')::uuid)`,
		e.Kind, e.UserID, e.Name, cut(e.IP, 64), cut(e.Agent, 300), cut(e.Target, 300), e.TargetID)
	return err
}

// badDue: a wrong key from ip is to be written now (the last one was a minute ago or more).
func (s *Store) badDue(ip string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.bad[ip]; ok && now.Sub(t) < time.Minute {
		return false
	}
	if len(s.bad) > 10000 { // forget the old addresses rather than grow without end
		for k, t := range s.bad {
			if now.Sub(t) >= time.Minute {
				delete(s.bad, k)
			}
		}
	}
	s.bad[ip] = now
	return true
}

// List gives the events by the filter, latest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Event, error) {
	if f.Kind != "" && !slices.Contains(Kinds, f.Kind) {
		return nil, i18n.E("err.bad_form")
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	f.Limit = min(f.Limit, 1000)
	var from, to *time.Time
	if !f.From.IsZero() {
		from = &f.From
	}
	if !f.To.IsZero() {
		t := f.To.AddDate(0, 0, 1)
		to = &t
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, at, kind, COALESCE(user_id::text, ''), name, ip, agent, target, COALESCE(target_id::text, '')
		FROM audit_log
		WHERE ($1 = '' OR kind = $1)
		  AND ($2 = '' OR user_id::text = $2 OR target_id::text = $2)
		  AND ($3::timestamptz IS NULL OR at >= $3) AND ($4::timestamptz IS NULL OR at < $4)
		  AND ($5 = 0 OR id < $5)
		ORDER BY id DESC LIMIT $6`, f.Kind, f.UserID, from, to, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Event])
}

// Days is how long the log is kept, in days.
func (s *Store) Days(ctx context.Context) (int, error) {
	var days int
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, keyDays).Scan(&days)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultDays, nil
	}
	return days, err
}

// SetDays chooses how long the log is kept (one of DaysChoice).
func (s *Store) SetDays(ctx context.Context, days int, by string) error {
	if !slices.Contains(DaysChoice, days) {
		return i18n.E("err.bad_form")
	}
	raw, _ := json.Marshal(days)
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		keyDays, raw, by)
	return err
}

// Prune removes the events older than the period of keeping; it gives how many went.
func (s *Store) Prune(ctx context.Context, now time.Time) (int64, error) {
	days, err := s.Days(ctx)
	if err != nil {
		return 0, err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM audit_log WHERE at < $1`, now.AddDate(0, 0, -days))
	return tag.RowsAffected(), err
}

// ClientIP is the address of the request's browser: behind a proxy of the private network (caddy of go24) — the
// last address it added to X-Forwarded-For, otherwise the peer's.
func ClientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if peer := net.ParseIP(ip); peer != nil && (peer.IsPrivate() || peer.IsLoopback()) {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			parts := strings.Split(fwd, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return ip
}

// From fills the address and the browser of an event from its request.
func From(r *http.Request, e Event) Event {
	e.IP, e.Agent = ClientIP(r), r.UserAgent()
	return e
}

// browsers in the order they are told apart: Edge and Opera say «Chrome» too, Chrome says «Safari».
var browsers = []struct{ token, name string }{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"YaBrowser/", "Yandex"},
	{"Firefox/", "Firefox"}, {"Chrome/", "Chrome"}, {"Version/", "Safari"}}

var systems = []struct{ token, name string }{{"Windows", "Windows"}, {"Android", "Android"}, {"iPhone", "iOS"},
	{"iPad", "iOS"}, {"Mac OS X", "macOS"}, {"Linux", "Linux"}}

// Browser names the browser and the system of a User-Agent briefly («Chrome 130 · Windows»); a program calling the
// API (curl, n8n) is shown by its first word.
func Browser(agent string) string {
	var name, system string
	for _, b := range browsers {
		if i := strings.Index(agent, b.token); i >= 0 {
			ver := agent[i+len(b.token):]
			if j := strings.IndexAny(ver, ". ;)"); j >= 0 {
				ver = ver[:j]
			}
			name = strings.TrimSpace(b.name + " " + ver)
			break
		}
	}
	for _, s := range systems {
		if strings.Contains(agent, s.token) {
			system = s.name
			break
		}
	}
	switch {
	case name == "":
		name, _, _ = strings.Cut(agent, " ")
		return name
	case system == "":
		return name
	}
	return name + " · " + system
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
