package openlines

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The channels of applications, as imconnector: an installed application registers its own way into the
// lines (a messenger go24 does not know) and turns it on for a line; the clients' messages it passes become the line's
// dialogues (the queue, automatic messages, the CRM lead, as from any channel); the operators' answers and the line's
// messages go to the application as the event ONIMCONNECTORMESSAGEADD (its handler bound by event.bind), a closed
// dialogue as ONIMCONNECTORDIALOGFINISH, a connector the administrators turned off on a line as
// ONIMCONNECTORSTATUSDELETE. A client is one chat and user of the application's messenger.

// maxConnectorFile is the largest client's file go24 takes from an application.
const maxConnectorFile = 50 << 20

// The events of the applications' channels.
const (
	EventConnectorMessage = "ONIMCONNECTORMESSAGEADD"
	EventConnectorFinish  = "ONIMCONNECTORDIALOGFINISH"
	EventConnectorStatus  = "ONIMCONNECTORSTATUSDELETE"
)

// Connector is an application's channel.
type Connector struct {
	ID, AppID, Code, Name, Icon, Placement string
	CreatedAt                              time.Time
}

// ConnectorLine is a connector on a line: turned on or off, what the application told about its account there.
type ConnectorLine struct {
	Connector
	WidgetID string
	Active   bool
	Data     map[string]any // id, url, url_im, name
	Error    string
}

var connectorCode = regexp.MustCompile(`^[a-z0-9_.-]{1,50}$`)

const connectorSQL = `SELECT id::text, app_id::text, code, name, icon, placement, created_at FROM ol_connectors`

// RegisterConnector registers the application's channel by its code, or changes its name, icon and settings page when
// the application has it already; the code of another application's channel is refused.
func (s *Store) RegisterConnector(ctx context.Context, appID string, c Connector) (Connector, error) {
	c.Code, c.Name = strings.ToLower(strings.TrimSpace(c.Code)), strings.TrimSpace(c.Name)
	if !connectorCode.MatchString(c.Code) {
		return c, i18n.E("err.ol_connector_code")
	}
	if c.Name == "" || len([]rune(c.Name)) > 100 {
		return c, i18n.E("err.ol_connector_name")
	}
	if len(c.Icon) > 200000 {
		return c, i18n.E("err.ol_connector_icon")
	}
	err := s.db.QueryRow(ctx, `INSERT INTO ol_connectors (app_id, code, name, icon, placement) VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, icon = EXCLUDED.icon, placement = EXCLUDED.placement
		WHERE ol_connectors.app_id = EXCLUDED.app_id RETURNING id::text, app_id::text, created_at`, appID, c.Code, c.Name, c.Icon, c.Placement).
		Scan(&c.ID, &c.AppID, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, i18n.E("err.ol_connector_taken")
	}
	return c, err
}

// UnregisterConnector removes the application's channel from every line; its dialogues stay.
func (s *Store) UnregisterConnector(ctx context.Context, appID, code string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM ol_connectors WHERE app_id::text = $1 AND code = lower($2)`, appID, strings.TrimSpace(code))
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Connectors are the channels of the applications, by name.
func (s *Store) Connectors(ctx context.Context) ([]Connector, error) {
	rows, err := s.db.Query(ctx, connectorSQL+` ORDER BY lower(name), code`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Connector])
}

// AppConnector is the application's channel of the code.
func (s *Store) AppConnector(ctx context.Context, appID, code string) (Connector, error) {
	rows, err := s.db.Query(ctx, connectorSQL+` WHERE app_id::text = $1 AND code = lower($2)`, appID, strings.TrimSpace(code))
	if err != nil {
		return Connector{}, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Connector])
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return c, err
}

// ConnectorLines are the connectors of the lines, turned on or not (by line, by name); widgetID "" — of every line.
func (s *Store) ConnectorLines(ctx context.Context, widgetID string) ([]ConnectorLine, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.app_id::text, c.code, c.name, c.icon, c.placement, c.created_at, l.widget_id::text, l.active, l.data, l.error
		FROM ol_connector_lines l JOIN ol_connectors c ON c.id = l.connector_id WHERE $1 = '' OR l.widget_id::text = $1
		ORDER BY l.widget_id, lower(c.name), c.code`, widgetID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ConnectorLine, error) {
		var l ConnectorLine
		err := r.Scan(&l.ID, &l.AppID, &l.Code, &l.Name, &l.Icon, &l.Placement, &l.CreatedAt, &l.WidgetID, &l.Active, &l.Data, &l.Error)
		return l, err
	})
}

// ConnectorLine is the connector on the line (Active false — never turned on there).
func (s *Store) ConnectorLine(ctx context.Context, c Connector, widgetID string) (ConnectorLine, error) {
	l := ConnectorLine{Connector: c, WidgetID: widgetID, Data: map[string]any{}}
	err := s.db.QueryRow(ctx, `SELECT active, data, error FROM ol_connector_lines WHERE connector_id::text = $1 AND widget_id::text = $2`,
		c.ID, widgetID).Scan(&l.Active, &l.Data, &l.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return l, err
}

// ActivateConnector turns the connector on or off on the line (the application, imconnector.activate); base is
// go24's address the files of the answers are links to.
func (s *Store) ActivateConnector(ctx context.Context, c Connector, widgetID string, on bool, base string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO ol_connector_lines (connector_id, widget_id, active, base) VALUES ($1::uuid, $2::uuid, $3, $4)
		ON CONFLICT (connector_id, widget_id) DO UPDATE SET active = EXCLUDED.active, base = EXCLUDED.base, error = ''`, c.ID, widgetID, on, base)
	return err
}

// SetConnectorData keeps what the application tells about its account on the line (imconnector.connector.data.set).
func (s *Store) SetConnectorData(ctx context.Context, c Connector, widgetID string, data map[string]any) error {
	keep := map[string]any{}
	for _, k := range []string{"id", "url", "url_im", "name"} {
		if v, ok := data[k]; ok {
			if t := strings.TrimSpace(toText(v)); t != "" {
				keep[k] = cut(t, 500)
			}
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_connector_lines SET data = $3 WHERE connector_id::text = $1 AND widget_id::text = $2`, c.ID, widgetID, keep)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.ol_connector_off")
	}
	return err
}

// TurnOffConnector: an administrator turns the connector off on the line; the application hears
// ONIMCONNECTORSTATUSDELETE.
func (s *Store) TurnOffConnector(ctx context.Context, connectorID, widgetID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var appID, code string
	var line int64
	err = tx.QueryRow(ctx, `UPDATE ol_connector_lines l SET active = false FROM ol_connectors c, ol_widgets w
		WHERE c.id = l.connector_id AND w.id = l.widget_id AND l.connector_id::text = $1 AND l.widget_id::text = $2 AND l.active
		RETURNING c.app_id::text, c.code, w.num`, connectorID, widgetID).Scan(&appID, &code, &line)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.appEvent(ctx, tx, appID, EventConnectorStatus, map[string]any{"connector": code, "line": line}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.wakeApps()
	return nil
}

// ---------- the clients' messages ----------

// ConnectorUser is the client as the application tells about them.
type ConnectorUser struct {
	ID, Name, LastName, Email, Phone, URL string
}

// ConnectorMessage is a client's message the application passes: the client, their chat (its id, name, link) and
// the message (its id there, the text, the files by their links).
type ConnectorMessage struct {
	User                      ConnectorUser
	ChatID, ChatName, ChatURL string
	MessageID, Text           string
	Files                     []ConnectorFile
}

// ConnectorFile is a file of a client's message: go24 takes it by its link.
type ConnectorFile struct{ URL, Name string }

// ConnectorSent is where a client's message went: the dialogue (its number, CHAT_ID and SESSION_ID) and
// the message's number in it (0 — not taken: the dialogue is spam).
type ConnectorSent struct {
	Session string
	Num     int64
	Message int64
}

// connectorToken is the visitor's token of a client of an application's channel on a line: only go24 can make it.
func (s *Store) connectorToken(connectorID, widgetID, chat, user string) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines connector\x00"), s.Key...))
	m.Write([]byte(connectorID + ":" + widgetID + ":" + chat + ":" + user))
	return "ap" + hex.EncodeToString(m.Sum(nil))
}

// activeOn: the connector is turned on on the line, and the line is on.
func (s *Store) activeOn(ctx context.Context, c Connector, widgetID string) error {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT l.active AND w.active FROM ol_connector_lines l JOIN ol_widgets w ON w.id = l.widget_id
		WHERE l.connector_id::text = $1 AND l.widget_id::text = $2`, c.ID, widgetID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !ok {
		return i18n.E("err.ol_connector_off")
	}
	return err
}

// ConnectorReceive makes a client's message the line's dialogue's message (imconnector.send.messages): the dialogue
// of the client's chat, or a new one.
func (s *Store) ConnectorReceive(ctx context.Context, c Connector, widgetID string, m ConnectorMessage) (ConnectorSent, error) {
	m.ChatID, m.User.ID = strings.TrimSpace(m.ChatID), strings.TrimSpace(m.User.ID)
	if m.ChatID == "" {
		return ConnectorSent{}, i18n.E("err.ol_connector_chat")
	}
	if err := s.activeOn(ctx, c, widgetID); err != nil {
		return ConnectorSent{}, err
	}
	name := cut(strings.TrimSpace(m.User.Name+" "+m.User.LastName), 200)
	if name == "" {
		name = cut(strings.TrimSpace(m.ChatName), 200)
	}
	page := m.ChatURL
	if hookURL(page) != nil {
		page = ""
	}
	token := s.connectorToken(c.ID, widgetID, m.ChatID, m.User.ID)
	var out ConnectorSent
	if err := s.db.QueryRow(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, email, phone, page, channel, connector_id, app_chat, app_user)
		VALUES ($1, $2, $3, left($4, 200), left($5, 50), left($6, 1000), 'app', $7, $8, $9)
		ON CONFLICT (token_hash) DO UPDATE SET name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END,
			email = CASE WHEN ol_sessions.email = '' THEN EXCLUDED.email ELSE ol_sessions.email END,
			phone = CASE WHEN ol_sessions.phone = '' THEN EXCLUDED.phone ELSE ol_sessions.phone END
		RETURNING id::text, num`, widgetID, hash(token), name, strings.TrimSpace(m.User.Email), strings.TrimSpace(m.User.Phone), page,
		c.ID, cut(m.ChatID, 200), cut(m.User.ID, 200)).Scan(&out.Session, &out.Num); err != nil {
		return out, err
	}
	text := strings.TrimSpace(m.Text)
	var f *File
	for i, file := range m.Files { // one file a message, as the line keeps it; the others as their links
		if i == 0 {
			var err error
			if f, err = s.connectorDownload(ctx, file); err == nil {
				continue
			}
			f = nil
		}
		text = strings.TrimSpace(text + "\n📎 " + cmp.Or(file.Name, file.URL))
	}
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if text == "" && f == nil {
		return out, i18n.E("err.ol_message")
	}
	var before int64
	if err := s.db.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM ol_messages WHERE session_id = $1::uuid`, out.Session).Scan(&before); err != nil {
		return out, err
	}
	if err := s.VisitorSend(ctx, widgetID, token, text, f); err != nil {
		return out, err
	}
	// the client's message is the dialogue's first new one of the visitor (spam: none)
	err := s.db.QueryRow(ctx, `UPDATE ol_messages SET app_id = nullif(left($3, 200), '') WHERE id = (SELECT min(id) FROM ol_messages
		WHERE session_id = $1::uuid AND id > $2 AND author_id IS NULL AND kind = '') RETURNING id`, out.Session, before, m.MessageID).Scan(&out.Message)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return out, err
}

// hookURL: an http(s) address.
func hookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return i18n.E("err.ol_file")
	}
	return nil
}

// connectorDownload takes a client's file from the application (its link) into the built-in storage.
func (s *Store) connectorDownload(ctx context.Context, f ConnectorFile) (*File, error) {
	if s.PutFile == nil || hookURL(f.URL) != nil {
		return nil, i18n.E("err.ol_file")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	res, err := tgShort.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("connector file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxConnectorFile+1))
	if err != nil || len(data) > maxConnectorFile {
		return nil, i18n.E("err.ol_file")
	}
	name := f.Name
	if name == "" {
		if u, err := url.Parse(f.URL); err == nil {
			name = path.Base(u.Path)
		}
	}
	if name == "" || name == "/" || name == "." {
		name = "file"
	}
	return s.keepFile(ctx, name, "", data)
}

// connectorSession is the dialogue of the client's chat with the connector on the line (the latest, when the chat
// has several users).
func (s *Store) connectorSession(ctx context.Context, c Connector, widgetID, chat string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM ol_sessions WHERE connector_id::text = $1 AND widget_id::text = $2 AND app_chat = $3
		ORDER BY last_at DESC LIMIT 1`, c.ID, widgetID, strings.TrimSpace(chat)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return id, err
}

// ConnectorEdit changes the text of a client's message by its id in the application (imconnector.update.messages),
// or marks it deleted (text "", imconnector.delete.messages). It tells whether the message was found.
func (s *Store) ConnectorEdit(ctx context.Context, c Connector, widgetID, chat, messageID, text string, deleted bool) (bool, error) {
	id, err := s.connectorSession(ctx, c, widgetID, chat)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if deleted {
		text = s.tr("ol.app_deleted")
	}
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if text == "" {
		return false, i18n.E("err.ol_message")
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_messages SET body = $3 WHERE session_id::text = $1 AND app_id = $2 AND author_id IS NULL AND kind = ''`,
		id, strings.TrimSpace(messageID), text)
	return err == nil && tag.RowsAffected() > 0, err
}

// ConnectorRead: the client has read the dialogue up to the message (its number; 0 — every message so far) —
// imconnector.send.status.reading.
func (s *Store) ConnectorRead(ctx context.Context, c Connector, widgetID, chat string, upTo int64) error {
	id, err := s.connectorSession(ctx, c, widgetID, chat)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE ol_sessions s SET vis_read = greatest(vis_read, (SELECT coalesce(max(m.id), 0) FROM ol_messages m
		WHERE m.session_id = s.id AND ($2 = 0 OR m.id <= $2))) WHERE s.id::text = $1`, id, upTo)
	return err
}

// ConnectorChatName names the client's chat (imconnector.chat.name.set): the dialogue's title.
func (s *Store) ConnectorChatName(ctx context.Context, c Connector, widgetID, chat, name string) error {
	name = cut(strings.TrimSpace(name), 200)
	if name == "" {
		return i18n.E("err.ol_connector_name")
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_sessions SET name = $4 WHERE connector_id::text = $1 AND widget_id::text = $2 AND app_chat = $3`,
		c.ID, widgetID, strings.TrimSpace(chat), name)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// ---------- the answers ----------

// appEvent queues the event for the application's handlers bound to it.
func (s *Store) appEvent(ctx context.Context, q dbtx, appID, event string, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO app_event_deliveries (event_id, event, data) SELECT e.id, $2, $3 FROM app_events e
		JOIN apps a ON a.id = e.app_id WHERE e.app_id::text = $1 AND e.event = $2 AND a.installed_at IS NOT NULL`, appID, event, raw)
	return err
}

func (s *Store) wakeApps() {
	if s.AppQueued != nil {
		s.AppQueued()
	}
}

// SendConnectors passes to the applications the operators' answers and the line's messages not passed yet, and tells
// them of the dialogues closed; it is how many events were queued. The events go out with the others of the
// applications (apps.DeliverEvents), retried there.
func (s *Store) SendConnectors(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.num, s.app_chat, c.app_id::text, c.code, w.id::text, w.num, l.base,
			coalesce(u.num, 0), coalesce(u.name, ''), m.body, m.at, m.file_name, m.file_type, m.file_size, m.file_secret, m.buttons
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_connectors c ON c.id = s.connector_id
		JOIN ol_connector_lines l ON l.connector_id = c.id AND l.widget_id = s.widget_id JOIN ol_widgets w ON w.id = s.widget_id
		LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'app' AND l.active AND m.id > s.app_sent AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
		ORDER BY m.id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type out struct {
		ID                      int64
		Session                 string
		Num                     int64
		Chat, App, Code, Widget string
		Line                    int64
		Base                    string
		UserNum                 int64
		Operator, Body          string
		At                      time.Time
		FileName, FileType      string
		FileSize                int64
		FileSecret              string
		Buttons                 []string
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[out])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, m := range list {
		msg := map[string]any{"user_id": m.UserNum, "text": m.Body, "date": m.At.Unix()}
		if m.Operator != "" {
			msg["user_name"] = m.Operator
		}
		if m.FileName != "" {
			msg["files"] = []map[string]any{{"name": m.FileName, "type": m.FileType, "size": m.FileSize,
				"link": FileLink(m.Base, m.Widget, Message{ID: m.ID, FileSecret: m.FileSecret})}}
		}
		if len(m.Buttons) > 0 {
			msg["keyboard"] = m.Buttons
		}
		data := map[string]any{"CONNECTOR": m.Code, "LINE": m.Line, "MESSAGES": []map[string]any{{
			"im": map[string]any{"chat_id": m.Num, "message_id": m.ID}, "message": msg, "chat": map[string]any{"id": m.Chat}}}}
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return n, err
		}
		err = s.appEvent(ctx, tx, m.App, EventConnectorMessage, data)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE ol_sessions SET app_sent = $2 WHERE id::text = $1 AND app_sent < $2`, m.Session, m.ID)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if err != nil {
			return n, err
		}
		n++
	}
	// the dialogues closed since the application was told last
	rows, err = s.db.Query(ctx, `SELECT s.id::text, s.num, s.app_chat, c.app_id::text, c.code, w.num, s.closed_at
		FROM ol_sessions s JOIN ol_connectors c ON c.id = s.connector_id JOIN ol_widgets w ON w.id = s.widget_id
		WHERE s.channel = 'app' AND s.status = 'closed' AND s.closed_at IS NOT NULL AND (s.app_finished IS NULL OR s.app_finished < s.closed_at)
		ORDER BY s.closed_at LIMIT 200`)
	if err != nil {
		return n, err
	}
	type closed struct {
		Session         string
		Num             int64
		Chat, App, Code string
		Line            int64
		At              time.Time
	}
	done, err := pgx.CollectRows(rows, pgx.RowToStructByPos[closed])
	if err != nil {
		return n, err
	}
	for _, d := range done {
		tx, err := s.db.Begin(ctx)
		if err != nil {
			return n, err
		}
		err = s.appEvent(ctx, tx, d.App, EventConnectorFinish, map[string]any{"CONNECTOR": d.Code, "LINE": d.Line,
			"CHAT_ID": d.Chat, "SESSION_ID": d.Num, "IM_CHAT_ID": d.Num})
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE ol_sessions SET app_finished = $2 WHERE id::text = $1`, d.Session, d.At)
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		tx.Rollback(ctx)
		if err != nil {
			return n, err
		}
		n++
	}
	if n > 0 {
		s.wakeApps()
	}
	return n, nil
}

// RunConnectors passes the answers to the applications' channels until ctx ends (go24-worker); the clients' messages
// come by REST.
func (s *Store) RunConnectors(ctx context.Context, log *slog.Logger) {
	for {
		if _, err := s.SendConnectors(ctx); err != nil && ctx.Err() == nil {
			log.Error("open lines: application channels", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// toText is a value of JSON as text.
func toText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	case nil:
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// cut keeps the first n letters of s.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
