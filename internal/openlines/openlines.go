// Package openlines is «Виджет на сайт» and «Открытые линии»: visitors of a website chat with go24's operators
// through a widget and leave their contacts (a CRM lead). The visitor's side is public (no go24 account): a visitor
// is known by a random token kept in their browser; go24 stores only its hash.
package openlines

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/i18n"
)

var ErrNotFound = i18n.E("err.not_found")

type Store struct {
	db  *pgxpool.Pool
	CRM *crm.Store // contact forms become leads
	// Tr makes the service lines of a dialogue (who took, passed, closed it) in the company's language
	Tr func(key string, args ...any) string
	// SendMail writes a letter from the user's mailbox (the conversation the visitor asked for)
	SendMail func(ctx context.Context, userID, to, subject, text string) error
}

// dbtx is the pool or a transaction.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func NewStore(db *pgxpool.Pool, c *crm.Store) *Store { return &Store{db: db, CRM: c} }

// Widget is a widget's settings: the open line behind it — its queue, working hours and automatic messages.
type Widget struct {
	ID, Name, Greeting, Color string
	Origins                   []string
	Operators                 []string // user ids of the queue
	Active                    bool
	CreatedAt                 time.Time
	// the queue: «all» — every operator is told and the first to answer takes the dialogue; «evenly» — to whoever has
	// the fewest open dialogues; «strict» — in the queue's order. Without an answer in AnswerWait minutes — the next one.
	QueueMode  string
	AnswerWait int
	// working hours (go24's time zone): days 0 Sunday … 6 Saturday, «09:00»–«18:00»; out of them — OfflineText
	HoursOn                  bool
	HoursDays                []int32
	HoursFrom, HoursTo       string
	OfflineText, WelcomeText string
	NoanswerText             string // when nobody has answered in NoanswerAfter minutes
	NoanswerAfter            int
	CloseText                string // when the dialogue is closed
	Autoclose                int    // hours of the visitor's silence after an answer to close the dialogue; 0 — never
	// the quality of service: the visitor rates the answer (VoteText — the question, "" — the default one); the visitor
	// may get the conversation by e-mail
	VoteOn    bool
	VoteText  string
	HistoryOn bool
	CRMAuto   bool // «Создавать лид автоматически»: a new dialogue becomes a lead (or is bound to the client it is)
}

// Day: is the weekday (0 Sunday … 6 Saturday) a working day of the line?
func (w Widget) Day(d int) bool {
	for _, x := range w.HoursDays {
		if int(x) == d {
			return true
		}
	}
	return false
}

// InHours: does the line work at this time (always, without working hours)?
func (w Widget) InHours(t time.Time) bool {
	if !w.HoursOn {
		return true
	}
	t = t.Local()
	if !w.Day(int(t.Weekday())) {
		return false
	}
	now := t.Format("15:04")
	if w.HoursFrom < w.HoursTo {
		return now >= w.HoursFrom && now < w.HoursTo
	}
	return now >= w.HoursFrom || now < w.HoursTo // overnight
}

// Allowed: may a page of this origin («https://site.ru») show the widget?
func (w Widget) Allowed(origin string) bool {
	if len(w.Origins) == 0 {
		return true
	}
	for _, o := range w.Origins {
		if strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

var (
	reColor = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)
	reHour  = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
)

// cleanOrigins keeps «scheme://host[:port]» of each line.
func cleanOrigins(list []string) ([]string, error) {
	out := []string{}
	for _, o := range list {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, i18n.E("err.ol_origin", o)
		}
		out = append(out, u.Scheme+"://"+strings.ToLower(u.Host))
	}
	return out, nil
}

// SaveWidget creates (ID "") or changes a widget (administrators; checked by the web layer).
func (s *Store) SaveWidget(ctx context.Context, userID string, w Widget) (string, error) {
	w.Name, w.Greeting, w.Color = strings.TrimSpace(w.Name), strings.TrimSpace(w.Greeting), strings.TrimPrefix(strings.TrimSpace(w.Color), "#")
	if w.Name == "" || len([]rune(w.Name)) > 100 || len([]rune(w.Greeting)) > 1000 {
		return "", i18n.E("err.ol_widget")
	}
	if w.Color == "" {
		w.Color = "2FC6F6"
	}
	if !reColor.MatchString(w.Color) {
		return "", i18n.E("err.ol_widget")
	}
	origins, err := cleanOrigins(w.Origins)
	if err != nil {
		return "", err
	}
	if err := cleanLine(&w); err != nil {
		return "", err
	}
	if w.ID == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO ol_widgets (name, greeting, color, origins, operators, active, created_by)
			VALUES ($1, $2, $3, $4, $5::text[]::uuid[], $6, $7) RETURNING id`, w.Name, w.Greeting, w.Color, origins, nonNil(w.Operators), w.Active, userID).Scan(&w.ID)
		if err != nil {
			return "", err
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET name = $2, greeting = $3, color = $4, origins = $5, operators = $6::text[]::uuid[], active = $7,
		queue_mode = $8, answer_wait = $9, hours_on = $10, hours_days = $11, hours_from = $12, hours_to = $13, offline_text = $14,
		welcome_text = $15, noanswer_text = $16, noanswer_after = $17, close_text = $18, autoclose = $19,
		vote_on = $20, vote_text = $21, history_on = $22, crm_auto = $23
		WHERE id::text = $1`, w.ID, w.Name, w.Greeting, w.Color, origins, nonNil(w.Operators), w.Active,
		w.QueueMode, w.AnswerWait, w.HoursOn, w.HoursDays, w.HoursFrom, w.HoursTo, w.OfflineText,
		w.WelcomeText, w.NoanswerText, w.NoanswerAfter, w.CloseText, w.Autoclose,
		w.VoteOn, w.VoteText, w.HistoryOn, w.CRMAuto)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return w.ID, err
}

// cleanLine checks the line's queue, hours and automatic messages, filling in the defaults.
func cleanLine(w *Widget) error {
	switch w.QueueMode {
	case "":
		w.QueueMode = "all"
	case "all", "evenly", "strict":
	default:
		return i18n.E("err.ol_widget")
	}
	if w.AnswerWait == 0 {
		w.AnswerWait = 3
	}
	if w.NoanswerAfter == 0 {
		w.NoanswerAfter = 5
	}
	if w.HoursFrom == "" {
		w.HoursFrom, w.HoursTo = "09:00", "18:00"
	}
	if w.HoursDays == nil {
		w.HoursDays = []int32{}
	}
	for _, d := range w.HoursDays {
		if d < 0 || d > 6 {
			return i18n.E("err.ol_widget")
		}
	}
	for _, p := range []*string{&w.OfflineText, &w.WelcomeText, &w.NoanswerText, &w.CloseText, &w.VoteText} {
		*p = strings.TrimSpace(*p)
		if len([]rune(*p)) > 1000 {
			return i18n.E("err.ol_widget")
		}
	}
	if w.AnswerWait < 1 || w.AnswerWait > 1440 || w.NoanswerAfter < 1 || w.NoanswerAfter > 1440 || w.Autoclose < 0 || w.Autoclose > 720 ||
		!reHour.MatchString(w.HoursFrom) || !reHour.MatchString(w.HoursTo) || w.HoursFrom == w.HoursTo || (w.HoursOn && len(w.HoursDays) == 0) {
		return i18n.E("err.ol_hours")
	}
	return nil
}

func (s *Store) DeleteWidget(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM ol_widgets WHERE id::text = $1`, id)
	return err
}

const widgetSQL = `SELECT id, name, greeting, color, origins, operators::text[], active, created_at, queue_mode, answer_wait,
	hours_on, hours_days, hours_from, hours_to, offline_text, welcome_text, noanswer_text, noanswer_after, close_text, autoclose,
	vote_on, vote_text, history_on, crm_auto FROM ol_widgets`

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func (s *Store) Widgets(ctx context.Context) ([]Widget, error) {
	rows, err := s.db.Query(ctx, widgetSQL+` ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Widget])
}

func (s *Store) Widget(ctx context.Context, id string) (Widget, error) {
	rows, err := s.db.Query(ctx, widgetSQL+` WHERE id::text = $1`, id)
	if err != nil {
		return Widget{}, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Widget])
	if err != nil {
		return Widget{}, err
	}
	if len(list) == 0 {
		return Widget{}, ErrNotFound
	}
	return list[0], nil
}

// ---------- the visitor's side ----------

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// Visit finds the visitor's dialogue by their token, or starts one (with a new token) when there is none.
func (s *Store) Visit(ctx context.Context, widgetID, token, page string) (sessionID, newToken string, err error) {
	w, err := s.Widget(ctx, widgetID)
	if err != nil {
		return "", "", err
	}
	if !w.Active {
		return "", "", ErrNotFound
	}
	if token != "" {
		err = s.db.QueryRow(ctx, `SELECT id FROM ol_sessions WHERE token_hash = $1 AND widget_id = $2`, hash(token), w.ID).Scan(&sessionID)
		if err == nil {
			return sessionID, token, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", "", err
		}
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b)
	if len(page) > 1000 {
		page = page[:1000]
	}
	err = s.db.QueryRow(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, page) VALUES ($1, $2, $3) RETURNING id`, w.ID, hash(token), page).Scan(&sessionID)
	return sessionID, token, err
}

// session is the visitor's dialogue by token (of this widget).
func (s *Store) session(ctx context.Context, widgetID, token string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT s.id FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id
		WHERE s.token_hash = $1 AND w.id::text = $2 AND w.active`, hash(token), widgetID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// Message of a dialogue.
type Message struct {
	ID       int64
	Operator string // "" — the visitor (or the line, for its automatic messages)
	Body     string
	At       time.Time
	Kind     string // "" — the visitor's or an operator's; welcome, offline, noanswer, close — the line's; note — a service line
	AuthorID string // the operator's id ("" — the visitor or the line)
}

// Auto: an automatic message of the line.
func (m Message) Auto() bool { return m.Kind != "" && m.Kind != "note" }

// VisitorSay is the visitor's message; it reopens a closed dialogue and tells the operators.
func (s *Store) VisitorSay(ctx context.Context, widgetID, token, body string) error {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return err
	}
	body = strings.TrimSpace(body)
	if body == "" || len([]rune(body)) > 5000 {
		return i18n.E("err.ol_message")
	}
	var recent int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM ol_messages WHERE session_id = $1 AND author_id IS NULL AND at > now() - interval '1 minute'`, id).Scan(&recent); err != nil {
		return err
	}
	if recent >= 20 { // a script on a public page: no flood
		return i18n.E("err.ol_flood")
	}
	w, err := s.Widget(ctx, widgetID)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	var operator *string
	var said int
	var spam bool
	if err := tx.QueryRow(ctx, `SELECT status, operator_id::text, (SELECT count(*) FROM ol_messages m WHERE m.session_id = s.id AND m.author_id IS NULL AND m.kind = ''),
		spam FROM ol_sessions s WHERE id = $1 FOR UPDATE`, id).Scan(&status, &operator, &said, &spam); err != nil {
		return err
	}
	if spam { // the visitor does not know: the message is just not taken
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, body) VALUES ($1, $2)`, id, body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET unread = unread + 1, last_at = now(), status = 'open', waiting_since = coalesce(waiting_since, now()),
		started_at = coalesce(started_at, now()), closed_at = NULL WHERE id = $1`, id); err != nil {
		return err
	}
	fresh := said == 0 || status == "closed" // a new dialogue (or one the visitor came back to)
	if fresh && w.QueueMode != "all" {
		ops, err := s.queue(ctx, tx, w.ID)
		if err != nil {
			return err
		}
		keep := ""
		if operator != nil && contains(ops, *operator) {
			keep = *operator // the one who talked to them before
		}
		if _, err := s.assign(ctx, tx, w, id, keep, nil); err != nil {
			return err
		}
	}
	now := time.Now()
	switch {
	case !w.InHours(now) && w.OfflineText != "":
		// once in a while, not on every message
		if _, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind) SELECT $1, $2, 'offline'
			WHERE NOT EXISTS (SELECT 1 FROM ol_messages WHERE session_id = $1 AND kind = 'offline' AND at > now() - interval '12 hours')`, id, w.OfflineText); err != nil {
			return err
		}
	case fresh && w.WelcomeText != "":
		if _, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind) VALUES ($1, $2, 'welcome')`, id, w.WelcomeText); err != nil {
			return err
		}
	}
	if err := notifyOperators(ctx, tx, id, body); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if said == 0 {
		if err := s.autoLead(ctx, w, id); err != nil {
			return err
		}
	} else if fresh {
		if err := s.follow(ctx, id); err != nil {
			return err
		}
	}
	return s.fire(ctx, id, "openline")
}

// fire tells the CRM triggers of the dialogue's lead (if it has one) of the visitor's message or form.
func (s *Store) fire(ctx context.Context, sessionID, kind string) error {
	var lead *string
	if err := s.db.QueryRow(ctx, `SELECT lead_id::text FROM ol_sessions WHERE id = $1`, sessionID).Scan(&lead); err != nil || lead == nil || s.CRM == nil {
		return err
	}
	return s.CRM.Fire(ctx, "lead", *lead, kind)
}

// VisitorMessages are the dialogue's messages after the given id (the widget polls them).
func (s *Store) VisitorMessages(ctx context.Context, widgetID, token string, after int64) ([]Message, error) {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return nil, err
	}
	list, err := s.messages(ctx, id, after)
	if err != nil {
		return nil, err
	}
	out := list[:0] // the visitor sees the line's messages from the line, and no service lines
	for _, m := range list {
		if m.Kind == "note" {
			continue
		}
		if m.Auto() {
			_ = s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&m.Operator)
		}
		out = append(out, m)
	}
	return out, nil
}

func (s *Store) messages(ctx context.Context, sessionID string, after int64) ([]Message, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, coalesce(u.name, ''), m.body, m.at, m.kind, coalesce(m.author_id::text, '') FROM ol_messages m LEFT JOIN users u ON u.id = m.author_id
		WHERE m.session_id = $1 AND m.id > $2 ORDER BY m.id LIMIT 500`, sessionID, after)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Message])
}

// Contact is the widget's form: the visitor's name, phone, e-mail and question become a CRM lead (source «Сайт»).
func (s *Store) Contact(ctx context.Context, widgetID, token, name, phone, email, text, source string) error {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return err
	}
	name, phone, email, text = strings.TrimSpace(name), strings.TrimSpace(phone), strings.TrimSpace(email), strings.TrimSpace(text)
	if name == "" || (phone == "" && email == "") || len([]rune(name+phone+email)) > 600 || len([]rune(text)) > 5000 {
		return i18n.E("err.ol_contact")
	}
	if email != "" {
		if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
			return i18n.E("err.ol_contact")
		}
	}
	var leadID, operator *string
	var creator string
	var auto, bound bool
	if err := s.db.QueryRow(ctx, `SELECT s.lead_id::text, s.lead_auto, s.contact_id IS NOT NULL, s.operator_id::text, w.created_by::text
		FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id WHERE s.id = $1`, id).Scan(&leadID, &auto, &bound, &operator, &creator); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET name = $2, phone = $3, email = $4, last_at = now() WHERE id = $1`, id, name, phone, email); err != nil {
		return err
	}
	if leadID != nil { // one lead per visitor: the form filled again moves it on (the line's own lead gets what it lacked)
		if auto {
			if err := s.fill(ctx, creator, *leadID, name, phone, email); err != nil {
				return err
			}
		}
		return s.fire(ctx, id, "form")
	}
	if s.CRM == nil || bound {
		return nil
	}
	entity, client, err := s.client(ctx, phone, email) // a client CRM knows: no second card
	if err != nil {
		return err
	}
	if entity != "" {
		if err := s.bind(ctx, id, entity, client, creator, false); err != nil {
			return err
		}
		return s.fire(ctx, id, "form")
	}
	title := name
	if text != "" {
		title = name + ": " + text
	}
	if r := []rune(title); len(r) > 200 {
		title = string(r[:200])
	}
	l := crm.Lead{Title: title, Name: name, Phone: phone, Email: email, Source: source, Origin: crm.Dict{Code: "WEBFORM"}, Comment: text}
	if operator != nil {
		l.Responsible.ID = *operator
	}
	lead, err := s.CRM.SaveLead(ctx, creator, l)
	if err != nil {
		return err
	}
	return s.bind(ctx, id, "lead", lead, creator, true)
}

// ---------- the operators' side ----------

// Session is a dialogue as operators see it.
type Session struct {
	ID, Widget, Name, Email, Phone, Page, Operator, OperatorID, LeadID, Status string
	Unread                                                                     int
	Last                                                                       string // the last message
	CreatedAt, LastAt                                                          time.Time
	Waiting, Spam                                                              bool // the visitor waits for an answer; marked as spam
	// the quality of service: the visitor's rating (1 👍, -1 👎, 0 — none) and comment; the head's rating, comment and name
	Vote, HeadVote                     int
	VoteComment, HeadComment, HeadName string
	ContactID                          string // the CRM contact the visitor turned out to be
	WidgetID                           string
}

// Title is how the dialogue is shown: the visitor's name or «Посетитель #…».
func (x Session) Title() string {
	if x.Name != "" {
		return x.Name
	}
	return "#" + x.ID[:6]
}

const sessionSQL = `SELECT s.id, w.name, s.name, s.email, s.phone, s.page, coalesce(o.name, ''), coalesce(s.operator_id::text, ''),
		coalesce(s.lead_id::text, ''), s.status, s.unread,
		coalesce((SELECT m.body FROM ol_messages m WHERE m.session_id = s.id AND m.kind <> 'note' ORDER BY m.id DESC LIMIT 1), ''), s.created_at, s.last_at,
		s.waiting_since IS NOT NULL, s.spam, s.vote, s.head_vote, s.vote_comment, s.head_comment, coalesce(h.name, ''), coalesce(s.contact_id::text, ''), s.widget_id::text
	FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users o ON o.id = s.operator_id LEFT JOIN users h ON h.id = s.head_by`

// Open is a dialogue with its messages; the operator reads it.
func (s *Store) Open(ctx context.Context, userID, id string) (Session, []Message, error) {
	rows, err := s.db.Query(ctx, sessionSQL+` WHERE s.id::text = $1`, id)
	if err != nil {
		return Session{}, nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Session])
	if err != nil {
		return Session{}, nil, err
	}
	if len(list) == 0 {
		return Session{}, nil, ErrNotFound
	}
	if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET unread = 0 WHERE id::text = $1`, id); err != nil {
		return Session{}, nil, err
	}
	_, _ = s.db.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND kind = 'openline' AND url = '/openlines?s=' || $2 AND read_at IS NULL`, userID, id)
	msgs, err := s.messages(ctx, list[0].ID, 0)
	return list[0], msgs, err
}

// Reply is an operator's message; the first operator to answer takes the dialogue (also one the queue gave another
// operator who has not answered yet).
func (s *Store) Reply(ctx context.Context, userID, id, body string) error {
	body = strings.TrimSpace(body)
	if body == "" || len([]rune(body)) > 5000 {
		return i18n.E("err.ol_message")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var answered bool
	if err := tx.QueryRow(ctx, `SELECT answered_at IS NOT NULL FROM ol_sessions WHERE id::text = $1 FOR UPDATE`, id).Scan(&answered); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET operator_id = CASE WHEN answered_at IS NULL THEN $2::uuid ELSE coalesce(operator_id, $2::uuid) END,
		answered_at = coalesce(answered_at, now()), waiting_since = NULL, noanswer_sent = false, tried = '{}',
		status = 'open', unread = 0, last_at = now()
		WHERE id::text = $1`, id, userID); err != nil {
		return err
	}
	if !answered { // the first answer: who took the dialogue
		if err := s.note(ctx, tx, id, "ol.note_taken", userName(ctx, tx, userID)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO ol_messages (session_id, author_id, body) SELECT id, $2, $3 FROM ol_sessions WHERE id::text = $1`, id, userID, body); err != nil {
		return err
	}
	// whoever answered has seen the dialogue: their notification about it is read (the next message notifies anew)
	if _, err = tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE kind = 'openline' AND url = '/openlines?s=' || $1 AND read_at IS NULL
		AND user_id = $2`, id, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.follow(ctx, id)
}

// SetStatus closes (with the line's closing message) or reopens a dialogue (also one marked as spam).
func (s *Store) SetStatus(ctx context.Context, userID, id string, open bool) error {
	if !open {
		return s.closeSession(ctx, id, userID)
	}
	_, err := s.db.Exec(ctx, `UPDATE ol_sessions SET status = 'open', spam = false, closed_at = NULL WHERE id::text = $1`, id)
	return err
}

// MakeLead makes a CRM lead of the dialogue (the operator's own, the conversation as its comment), or binds it to the
// contact or lead with the visitor's phone or e-mail; it is the card's page.
func (s *Store) MakeLead(ctx context.Context, userID, id, source string) (string, error) {
	x, msgs, err := s.Open(ctx, userID, id)
	if err != nil {
		return "", err
	}
	switch {
	case x.LeadID != "":
		return "/crm/leads/" + x.LeadID, nil
	case x.ContactID != "":
		return "/crm/contacts/" + x.ContactID, nil
	}
	entity, client, err := s.client(ctx, x.Phone, x.Email)
	if err != nil {
		return "", err
	}
	if entity != "" {
		return "/crm/" + entity + "s/" + client, s.bind(ctx, x.ID, entity, client, userID, false)
	}
	var b strings.Builder
	for _, m := range msgs {
		who := x.Title()
		switch {
		case m.Kind == "note":
			continue
		case m.Auto():
			who = x.Widget
		case m.Operator != "":
			who = m.Operator
		}
		b.WriteString(who + ": " + m.Body + "\n")
	}
	comment := b.String()
	if r := []rune(comment); len(r) > 9000 {
		comment = string(r[len(r)-9000:])
	}
	lead, err := s.CRM.SaveLead(ctx, userID, crm.Lead{Title: x.Title(), Name: x.Name, Phone: x.Phone, Email: x.Email, Source: source,
		Origin: crm.Dict{Code: "OPENLINE"}, Comment: comment})
	if err != nil {
		return "", err
	}
	return "/crm/leads/" + lead, s.bind(ctx, x.ID, "lead", lead, userID, false)
}

// Unread is how many dialogues have messages no operator has read (the menu counter).
func (s *Store) Unread(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM ol_sessions WHERE status = 'open' AND unread > 0`).Scan(&n)
	return n, err
}
