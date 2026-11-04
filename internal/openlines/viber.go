package openlines

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The channel Viber of a line: the company's Viber bot (partners.viber.com) is a way into the line, as
// the Telegram bot is. Viber is not asked for the messages: it calls go24's public address itself (set_webhook, so go24
// needs a public https address), each call signed with the bot's token. A client's message becomes the line's dialogue
// (the queue, automatic messages, the CRM lead); the operators' answers and the chat bot's buttons go back to the
// client's chat with the bot, the operators' files as links to go24 Viber takes them from. Opening the chat gets the
// line's greeting.

// maxViberFile is the largest client's file go24 takes from Viber.
const maxViberFile = 50 << 20

// ErrViberSign: a call to the line's Viber address not signed with its bot's token.
var ErrViberSign = errors.New("viber: bad signature")

// ViberBot is the Viber bot of a line as its settings show it.
type ViberBot struct {
	Name, URI, Error string // the bot's name and address in Viber (""  — none); what went wrong the last time
}

// ViberHookPath is the address in go24 the line's bot is called at.
func ViberHookPath(widgetID string) string { return "/hooks/ol/viber/" + widgetID }

// viberToken is the visitor's token of a Viber chat in a line: only go24 can make it.
func (s *Store) viberToken(widgetID, user string) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines viber\x00"), s.Key...))
	m.Write([]byte(widgetID + ":" + user))
	return "vb" + hex.EncodeToString(m.Sum(nil))
}

// ---------- the API ----------

// viberError is an answer of the API with a status other than 0 (2 — the token is bad, 5, 6 — the client is not in
// Viber any more or unsubscribed from the bot…).
type viberError struct {
	Status  int    `json:"status"`
	Message string `json:"status_message"`
}

func (e *viberError) Error() string { return "viber: " + strconv.Itoa(e.Status) + " " + e.Message }

// viberGone: the client left Viber, unsubscribed from the bot or has no device for it — the message will never go.
func viberGone(err error) bool {
	var ve *viberError
	return errors.As(err, &ve) && (ve.Status == 5 || ve.Status == 6 || ve.Status == 11)
}

// viberCall calls the API's method (a POST of JSON with the token in a header) and decodes its answer into out.
func (s *Store) viberCall(ctx context.Context, token, method string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ViberAPI+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("X-Viber-Auth-Token", token)
	req.Header.Set("Content-Type", "application/json")
	res, err := tgShort.Do(req)
	if err != nil {
		return errors.New("viber " + method + ": " + strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer res.Body.Close()
	all, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil || res.StatusCode != http.StatusOK {
		return errors.New("viber " + method + ": " + res.Status)
	}
	var e viberError
	if err := json.Unmarshal(all, &e); err != nil {
		return err
	}
	if e.Status != 0 {
		return &e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(all, out)
}

// ---------- the line's settings ----------

// Viber is the line's Viber bot.
func (s *Store) Viber(ctx context.Context, widgetID string) (ViberBot, error) {
	var b ViberBot
	err := s.db.QueryRow(ctx, `SELECT viber_name, coalesce(viber_uri, ''), viber_error FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&b.Name, &b.URI, &b.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// ConnectViber makes the Viber bot with this token the line's way in (administrators; checked by the web layer): the
// token is checked with Viber, the bot's name is read there, Viber is told to call go24 at base (its public address); a
// bot serves one line.
func (s *Store) ConnectViber(ctx context.Context, widgetID, token, base string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 200 || strings.ContainsAny(token, "\r\n ") {
		return "", i18n.E("err.ol_viber_token")
	}
	var a struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	if err := s.viberCall(ctx, token, "get_account_info", map[string]any{}, &a); err != nil || a.URI == "" {
		return "", i18n.E("err.ol_viber_token")
	}
	var other string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE viber_uri = $1 AND id::text <> $2`, a.URI, widgetID).Scan(&other)
	if err == nil {
		return "", i18n.E("err.ol_tg_taken", other)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	sealed, err := s.seal(token)
	if err != nil {
		return "", err
	}
	// kept before the webhook is set: Viber calls the address at once, signed with the token
	base = strings.TrimRight(base, "/")
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET viber_secret = $2, viber_uri = $3, viber_name = left($4, 200), viber_base = $5,
		viber_error = '' WHERE id::text = $1`, widgetID, sealed, a.URI, a.Name, base)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if err := s.viberCall(ctx, token, "set_webhook", map[string]any{"url": base + ViberHookPath(widgetID),
		"event_types": []string{"message", "conversation_started", "unsubscribed"}, "send_name": true}, nil); err != nil {
		_ = s.DisconnectViber(ctx, widgetID)
		return "", i18n.E("err.ol_viber_hook", base)
	}
	return a.Name, nil
}

// DisconnectViber: the bot no longer serves the line, Viber stops calling go24 (its dialogues stay).
func (s *Store) DisconnectViber(ctx context.Context, widgetID string) error {
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT viber_secret FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&sealed)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if token, err := s.unseal(sealed); sealed != nil && err == nil {
		_ = s.viberCall(ctx, token, "set_webhook", map[string]any{"url": ""}, nil)
	}
	_, err = s.db.Exec(ctx, `UPDATE ol_widgets SET viber_secret = NULL, viber_uri = NULL, viber_name = '', viber_base = '',
		viber_error = '' WHERE id::text = $1`, widgetID)
	return err
}

// ---------- receiving ----------

type viberUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type viberEvent struct {
	Event   string    `json:"event"`
	User    viberUser `json:"user"`   // conversation_started
	Sender  viberUser `json:"sender"` // message
	Message struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Media    string `json:"media"`
		FileName string `json:"file_name"`
		Size     int64  `json:"size"`
		Contact  struct {
			Name  string `json:"name"`
			Phone string `json:"phone_number"`
		} `json:"contact"`
		Location struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"location"`
	} `json:"message"`
}

// ViberHook takes a call of Viber to the line's address (signed with its bot's token: else ErrViberSign): a client's
// message becomes the line's dialogue's message (the chat's dialogue, or a new one); a client opening the chat gets the
// greeting — it is the answer to the call (the reply; nil — none).
func (s *Store) ViberHook(ctx context.Context, widgetID, sign string, body []byte) ([]byte, error) {
	var sealed []byte
	var name string
	var active bool
	err := s.db.QueryRow(ctx, `SELECT viber_secret, viber_name, active FROM ol_widgets WHERE id::text = $1 AND viber_secret IS NOT NULL`,
		widgetID).Scan(&sealed, &name, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	token, err := s.unseal(sealed)
	if err != nil {
		return nil, ErrViberSign // sealed with another secret: the administrators connect the bot anew
	}
	m := hmac.New(sha256.New, []byte(token))
	m.Write(body)
	got, err := hex.DecodeString(sign)
	if err != nil || !hmac.Equal(got, m.Sum(nil)) {
		return nil, ErrViberSign
	}
	var e viberEvent
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	w, err := s.Widget(ctx, widgetID)
	if err != nil {
		return nil, err
	}
	switch e.Event {
	case "conversation_started": // the chat opened: the greeting, as the answer (the client is not subscribed yet)
		if w.Greeting == "" {
			return nil, nil
		}
		return json.Marshal(map[string]any{"sender": viberSender(name), "type": "text", "text": w.Greeting})
	case "message":
		return nil, s.viberReceive(ctx, w, e)
	}
	return nil, nil // «webhook» (the address checked), «subscribed», «unsubscribed», «delivered», «seen»…
}

// viberReceive makes a client's message the line's dialogue's message.
func (s *Store) viberReceive(ctx context.Context, w Widget, e viberEvent) error {
	if e.Sender.ID == "" {
		return nil
	}
	token := s.viberToken(w.ID, e.Sender.ID)
	if _, err := s.db.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, channel, viber_user)
		VALUES ($1, $2, left($3, 200), 'viber', $4)
		ON CONFLICT (token_hash) DO UPDATE SET name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END`,
		w.ID, hash(token), strings.TrimSpace(e.Sender.Name), e.Sender.ID); err != nil {
		return err
	}
	m := e.Message
	text := strings.TrimSpace(m.Text)
	var f *File
	switch m.Type {
	case "picture", "video", "file", "sticker":
		name := m.FileName
		if name == "" {
			name = map[string]string{"picture": "photo.jpg", "video": "video.mp4", "sticker": "sticker.png"}[m.Type]
		}
		var err error
		if f, err = s.viberDownload(ctx, m.Media, name, m.Size); err != nil {
			f = nil
			text = strings.TrimSpace(text + "\n📎 " + name)
		}
	case "contact":
		text = strings.TrimSpace(text + "\n👤 " + strings.TrimSpace(m.Contact.Name+" "+m.Contact.Phone))
	case "location":
		text = strings.TrimSpace(text + "\n📍 https://maps.google.com/?q=" + strconv.FormatFloat(m.Location.Lat, 'f', 6, 64) + "," +
			strconv.FormatFloat(m.Location.Lon, 'f', 6, 64))
	case "url":
		if text == "" {
			text = m.Media
		}
	}
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if text == "" && f == nil {
		return nil
	}
	return s.VisitorSend(ctx, w.ID, token, text, f)
}

// viberDownload takes a client's file from Viber (its link) into the built-in storage.
func (s *Store) viberDownload(ctx context.Context, link, name string, size int64) (*File, error) {
	if s.PutFile == nil || link == "" || size > maxViberFile {
		return nil, i18n.E("err.ol_file")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	res, err := tgShort.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("viber file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxViberFile+1))
	if err != nil || len(data) > maxViberFile {
		return nil, i18n.E("err.ol_file")
	}
	ct := ""
	if name == "sticker.png" {
		ct = http.DetectContentType(data)
	}
	return s.keepFile(ctx, name, ct, data)
}

// ---------- sending ----------

// viberSender is who the line's messages are from in Viber: the bot's name (Viber takes up to 28 letters).
func viberSender(name string) map[string]string {
	if r := []rune(name); len(r) > 28 {
		name = string(r[:28])
	}
	if name == "" {
		name = "go24"
	}
	return map[string]string{"name": name}
}

// SendViber sends to the Viber chats the operators' answers and the line's messages not sent yet; it is how many went.
func (s *Store) SendViber(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.viber_user, w.id::text, w.viber_secret, w.viber_name, w.viber_base, coalesce(u.name, ''),
			m.body, m.file_name, m.file_type, m.file_size, m.file_secret, m.buttons
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'viber' AND w.viber_secret IS NOT NULL AND m.id > s.viber_sent AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
		ORDER BY m.id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type out struct {
		ID                        int64
		Session, User, Widget     string
		Sealed                    []byte
		Bot, Base, Operator, Body string
		FileName, FileType        string
		FileSize                  int64
		FileSecret                string
		Buttons                   []string
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[out])
	if err != nil {
		return 0, err
	}
	sent, stopped := 0, map[string]bool{} // a dialogue whose message did not go waits: its next ones go after it
	var errs []error
	for _, m := range list {
		if stopped[m.Session] {
			continue
		}
		token, err := s.unseal(m.Sealed)
		if err == nil {
			link := ""
			if m.FileName != "" {
				link = FileLink(m.Base, m.Widget, Message{ID: m.ID, FileSecret: m.FileSecret})
			}
			err = s.viberSend(ctx, token, m.User, m.Bot, m.Operator, m.Body, link, m.FileName, m.FileType, m.FileSize, m.Buttons)
		}
		if err != nil && !viberGone(err) { // left Viber or unsubscribed: not sent, never will be
			stopped[m.Session] = true
			errs = append(errs, err)
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET viber_error = left($2, 500) WHERE id::text = $1`, m.Widget, err.Error())
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET viber_sent = $2 WHERE id::text = $1 AND viber_sent < $2`, m.Session, m.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// viberSend writes a message to the client's chat: the operator's name, the text; a file as a picture (the text under
// it, when short) or a file Viber takes by its link; the chat bot's menu as the chat's keyboard (a button sends its
// text, as the client's message).
func (s *Store) viberSend(ctx context.Context, token, user, bot, operator, body, link, fileName, fileType string, fileSize int64, buttons []string) error {
	text := body
	if operator != "" {
		text = operator + ":"
		if body != "" {
			text += "\n" + body
		}
	}
	msg := func(typ string) map[string]any {
		return map[string]any{"receiver": user, "sender": viberSender(bot), "type": typ}
	}
	var last map[string]any
	if link != "" {
		// a picture Viber shows is a JPEG, PNG or GIF; its text — up to 768 letters, a longer one goes first by itself
		switch picture := fileType == "image/jpeg" || fileType == "image/png" || fileType == "image/gif"; {
		case picture && len([]rune(text)) <= 768:
			last = msg("picture")
			last["media"], last["text"] = link, text
			text = ""
		case picture:
			last = msg("picture")
			last["media"], last["text"] = link, ""
		default:
			last = msg("file")
			last["media"], last["size"], last["file_name"] = link, fileSize, viberCut(fileName, 256)
		}
	}
	if text != "" {
		t := msg("text")
		t["text"] = viberCut(text, 7000)
		if last == nil {
			last = t
		} else if err := s.viberCall(ctx, token, "send_message", t, nil); err != nil {
			return err
		}
	}
	if last == nil {
		return nil
	}
	if len(buttons) > 0 {
		list := []map[string]any{}
		for i, b := range buttons {
			if i == 24 { // the most a keyboard takes
				break
			}
			b = viberCut(b, 250)
			list = append(list, map[string]any{"ActionType": "reply", "ActionBody": b, "Text": b, "Columns": 6, "Rows": 1})
		}
		last["min_api_version"] = 3
		last["keyboard"] = map[string]any{"Type": "keyboard", "DefaultHeight": false, "Buttons": list}
	}
	err := s.viberCall(ctx, token, "send_message", last, nil)
	var ve *viberError
	if errors.As(err, &ve) && ve.Status == 3 && last["type"] != "text" { // a file Viber does not take (.exe…): its link
		delete(last, "media")
		delete(last, "size")
		delete(last, "file_name")
		caption, _ := last["text"].(string)
		last["type"], last["text"] = "text", strings.TrimSpace(viberCut(caption+"\n📎 "+fileName+" "+link, 7000))
		err = s.viberCall(ctx, token, "send_message", last, nil)
	}
	return err
}

// viberCut keeps the first n letters of s.
func viberCut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// RunViber sends the answers to the Viber chats until ctx ends (go24-worker); the clients' messages come by the webhook.
func (s *Store) RunViber(ctx context.Context, log *slog.Logger) {
	for {
		if _, err := s.SendViber(ctx); err != nil && ctx.Err() == nil {
			log.Error("open lines: viber send", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
