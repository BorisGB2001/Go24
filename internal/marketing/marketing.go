// Package marketing is «Маркетинг» (CRM → Маркетинг): segments of addressees — the CRM cards of a
// filter or a list of addresses —, e-mail campaigns to them sent from a go24 mailbox in the background, and the stop
// list of those who unsubscribed.
package marketing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/i18n"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

// Store keeps the segments, campaigns and the stop list; CRM gives the readers' rights on the cards.
type Store struct {
	db  *pgxpool.Pool
	CRM *crm.Store
}

func NewStore(db *pgxpool.Pool, c *crm.Store) *Store { return &Store{db: db, CRM: c} }

// Entities a segment takes its addressees from; "list" — the addresses written into it.
var Entities = []string{"contact", "company", "lead", "list"}

// Filter of the cards of a segment, as the CRM lists filter them; zero fields don't filter.
type Filter struct {
	Q           string `json:"q,omitempty"`           // in the name, title and address
	Responsible string `json:"responsible,omitempty"` // the responsible's id
	Source      string `json:"source,omitempty"`      // the id of the «Источник» item
	Stage       string `json:"stage,omitempty"`       // leads: the stage's id
	Semantic    string `json:"semantic,omitempty"`    // leads: process | success | failure
	From        string `json:"from,omitempty"`        // created on this day (2006-01-02) or later
	To          string `json:"to,omitempty"`          // … or earlier
}

// Segment is a named set of addressees.
type Segment struct {
	ID, Name, Entity      string
	Filter                Filter
	Emails                string
	CreatedBy, AuthorName string
	UpdatedAt             time.Time
}

// Recipient is an addressee of a segment.
type Recipient struct {
	Email, Name, Entity, EntityID string
}

// Limits.
const (
	maxName      = 200
	maxEmails    = 1 << 20
	MaxAudience  = 50000 // addressees of a campaign
	maxSubject   = 1000
	maxBody      = 512 << 10
	sendPerRound = 50 // letters of a campaign each round of the worker (30 s): 100 a minute
)

const segmentCols = `g.id::text, g.name, g.entity, g.filter, g.emails, coalesce(g.created_by::text, ''), coalesce(u.name, ''), g.updated_at
	FROM mkt_segments g LEFT JOIN users u ON u.id = g.created_by`

func scanSegment(r pgx.CollectableRow) (Segment, error) {
	var g Segment
	var raw []byte
	if err := r.Scan(&g.ID, &g.Name, &g.Entity, &raw, &g.Emails, &g.CreatedBy, &g.AuthorName, &g.UpdatedAt); err != nil {
		return g, err
	}
	return g, json.Unmarshal(raw, &g.Filter)
}

// Segments are the company's segments, by name.
func (s *Store) Segments(ctx context.Context) ([]Segment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+segmentCols+` ORDER BY lower(g.name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSegment)
}

// Segment is one of them.
func (s *Store) Segment(ctx context.Context, id string) (Segment, error) {
	rows, err := s.db.Query(ctx, `SELECT `+segmentCols+` WHERE g.id::text = $1`, id)
	if err != nil {
		return Segment{}, err
	}
	g, err := pgx.CollectExactlyOneRow(rows, scanSegment)
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return g, err
}

func validEntity(e string) bool {
	for _, x := range Entities {
		if x == e {
			return true
		}
	}
	return false
}

// SaveSegment adds a segment (g.ID "") or changes one: its author's or, by an administrator, any.
func (s *Store) SaveSegment(ctx context.Context, userID string, admin bool, g Segment) (string, error) {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" || len([]rune(g.Name)) > maxName || !validEntity(g.Entity) || len(g.Emails) > maxEmails {
		return "", i18n.E("err.mkt_segment")
	}
	for _, d := range []string{g.Filter.From, g.Filter.To} {
		if _, err := time.Parse(time.DateOnly, d); d != "" && err != nil {
			return "", i18n.E("err.mkt_segment")
		}
	}
	if g.Entity == "list" {
		g.Filter = Filter{}
		if len(ParseList(g.Emails)) == 0 {
			return "", i18n.E("err.mkt_list")
		}
	} else {
		g.Emails = ""
		if g.Entity != "lead" {
			g.Filter.Stage, g.Filter.Semantic = "", ""
		}
	}
	raw, _ := json.Marshal(g.Filter)
	if g.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO mkt_segments (name, entity, filter, emails, created_by) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, g.Name, g.Entity, raw, g.Emails, userID).Scan(&g.ID)
		return g.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mkt_segments SET name = $3, entity = $4, filter = $5, emails = $6, updated_at = now()
		WHERE id::text = $1 AND ($7 OR created_by::text = $2)`, g.ID, userID, g.Name, g.Entity, raw, g.Emails, admin)
	if err == nil && tag.RowsAffected() == 0 {
		err = s.missing(ctx, "mkt_segments", g.ID)
	}
	return g.ID, err
}

// DeleteSegment removes a segment: its author's or, by an administrator, any.
func (s *Store) DeleteSegment(ctx context.Context, userID string, admin bool, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mkt_segments WHERE id::text = $1 AND ($3 OR created_by::text = $2)`, id, userID, admin)
	if err == nil && tag.RowsAffected() == 0 {
		err = s.missing(ctx, "mkt_segments", id)
	}
	return err
}

// missing tells a row that is not there from one the user may not change.
func (s *Store) missing(ctx context.Context, table, id string) error {
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id::text = $1)`, id).Scan(&ok); err != nil {
		return err
	}
	if ok {
		return ErrForbidden
	}
	return ErrNotFound
}

// ParseList reads the addresses of a list segment: one a line (or separated by commas), as «Имя <адрес>»,
// «адрес;Имя», «адрес,Имя» or the address alone; wrong lines are skipped, repeated addresses taken once.
func ParseList(text string) []Recipient {
	var out []Recipient
	seen := map[string]bool{}
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		var name, addr string
		if a, err := mail.ParseAddress(line); err == nil {
			name, addr = a.Name, a.Address
		} else if i := strings.IndexAny(line, ";,\t"); i > 0 {
			addr, name = strings.TrimSpace(line[:i]), strings.Trim(strings.TrimSpace(line[i+1:]), `"`)
		} else {
			addr = line
		}
		if !ValidEmail(addr) || seen[strings.ToLower(addr)] {
			continue
		}
		seen[strings.ToLower(addr)] = true
		out = append(out, Recipient{Email: addr, Name: name})
	}
	return out
}

// ValidEmail: a plain address one may send to.
func ValidEmail(addr string) bool {
	a, err := mail.ParseAddress(addr)
	return err == nil && a.Address == addr && strings.Contains(addr[strings.LastIndex(addr, "@")+1:], ".")
}

// audienceSQL is the query of a segment's cards the viewer may read: email, name, entity, id.
var audienceSQL = map[string]string{
	"contact": `SELECT x.email, x.name, 'contact', x.id::text FROM crm_contacts x WHERE trim(x.email) <> ''`,
	"company": `SELECT x.email, x.title, 'company', x.id::text FROM crm_companies x WHERE trim(x.email) <> ''`,
	"lead": `SELECT x.email, coalesce(nullif(x.name, ''), x.title), 'lead', x.id::text FROM crm_leads x
		JOIN crm_stages st ON st.id = x.stage_id WHERE trim(x.email) <> ''`,
}

// searchCols are the columns the search of a segment's filter looks in.
var searchCols = map[string]string{
	"contact": "x.name || ' ' || x.email", "company": "x.title || ' ' || x.email",
	"lead": "x.title || ' ' || x.name || ' ' || x.company_title || ' ' || x.email",
}

// Audience are the addressees of a segment the viewer may see (CRM rights), a valid address each and once.
func (s *Store) Audience(ctx context.Context, viewer string, g Segment) ([]Recipient, error) {
	if g.Entity == "list" {
		return ParseList(g.Emails), nil
	}
	q, ok := audienceSQL[g.Entity]
	if !ok {
		return nil, ErrNotFound
	}
	args := []any{viewer}
	sc, err := s.CRM.ReadScope(ctx, viewer, g.Entity, "x", 1)
	if err != nil {
		return nil, err
	}
	q += ` AND ` + sc
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	f := g.Filter
	if t := strings.TrimSpace(f.Q); t != "" {
		q += ` AND (` + searchCols[g.Entity] + `) ILIKE ` + arg("%"+t+"%")
	}
	if f.Responsible != "" {
		q += ` AND x.responsible_id::text = ` + arg(f.Responsible)
	}
	if f.Source != "" && g.Entity != "company" {
		q += ` AND x.source_id::text = ` + arg(f.Source)
	}
	if g.Entity == "lead" && f.Stage != "" {
		q += ` AND x.stage_id::text = ` + arg(f.Stage)
	}
	if g.Entity == "lead" && f.Semantic != "" {
		q += ` AND st.semantic = ` + arg(f.Semantic)
	}
	if d, err := time.ParseInLocation(time.DateOnly, f.From, time.Local); err == nil {
		q += ` AND x.created_at >= ` + arg(d)
	}
	if d, err := time.ParseInLocation(time.DateOnly, f.To, time.Local); err == nil {
		q += ` AND x.created_at < ` + arg(d.AddDate(0, 0, 1))
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY x.created_at LIMIT `+strconv.Itoa(MaxAudience+1), args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Recipient])
	if err != nil {
		return nil, err
	}
	seen, out := map[string]bool{}, list[:0]
	for _, r := range list {
		r.Email = strings.TrimSpace(r.Email)
		if k := strings.ToLower(r.Email); ValidEmail(r.Email) && !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out, nil
}

// token is a random key of a recipient's links.
func token() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
