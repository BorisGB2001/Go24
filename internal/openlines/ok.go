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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The channel Odnoklassniki of a line: the company's group in OK (its bot, the Bot API of ok.ru) is a way
// into the line, as the VK community is. OK is not asked for the messages: it calls go24's public address itself (the
// group's webhook subscription, so go24 needs a public https address); the address carries a secret only go24 knows,
// OK does not sign its calls. A client's message to the group becomes the line's dialogue (the queue, automatic
// messages, the CRM lead); the operators' answers, pictures (OK takes them by go24's link) and the chat bot's buttons go
// back to the client's chat with the group, other files as their links. The chat's start gets the line's greeting.

// maxOKFile is the largest client's file go24 takes from OK.
const maxOKFile = 50 << 20

// OKGroup is the OK group of a line as its settings show it.
type OKGroup struct {
	ID, Name, Link, Error string // the group's id in OK (""  — none), name and page; what went wrong the last time
}

// OKHookPath is the address in go24 OK calls the line's group at: its secret part only go24 can make.
func (s *Store) OKHookPath(widgetID string) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines ok hook\x00"), s.Key...))
	m.Write([]byte(widgetID))
	return "/hooks/ol/ok/" + widgetID + "/" + hex.EncodeToString(m.Sum(nil))[:32]
}

// okToken is the visitor's token of an OK chat in a line: only go24 can make it.
func (s *Store) okToken(widgetID, chat string) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines ok\x00"), s.Key...))
	m.Write([]byte(widgetID + ":" + chat))
	return "ok" + hex.EncodeToString(m.Sum(nil))
}

// ---------- the API ----------

// okError is an answer of the API with an error (10 — no rights, 102 — the token is bad, 300 — no such chat…).
type okError struct {
	Code int    `json:"error_code"`
	Msg  string `json:"error_msg"`
}

func (e *okError) Error() string { return "ok: " + strconv.Itoa(e.Code) + " " + e.Msg }

// okGone: the client's chat is no more or the client forbade the group to write — the message will never go.
func okGone(err error) bool {
	var oe *okError
	return errors.As(err, &oe) && (oe.Code == 10 || oe.Code == 300 || oe.Code == 455)
}

// okCall calls the API's method (a POST of JSON, a GET without it; the token in the address) and decodes its answer into out.
func (s *Store) okCall(ctx context.Context, token, method string, body, out any) error {
	verb, rd := http.MethodGet, io.Reader(nil) // no body — a GET
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		verb, rd = http.MethodPost, bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, verb, s.OKAPI+"/"+method+"?access_token="+url.QueryEscape(token), rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json;charset=utf-8")
	res, err := tgShort.Do(req)
	if err != nil {
		return errors.New("ok " + method + ": " + strings.ReplaceAll(strings.ReplaceAll(err.Error(), url.QueryEscape(token), "***"), token, "***"))
	}
	defer res.Body.Close()
	all, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var e okError
	if json.Unmarshal(all, &e) == nil && (e.Code != 0 || e.Msg != "") {
		return &e
	}
	if res.StatusCode != http.StatusOK {
		return errors.New("ok " + method + ": " + res.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(all, out)
}

// ---------- the line's settings ----------

// OK is the line's OK group.
func (s *Store) OK(ctx context.Context, widgetID string) (OKGroup, error) {
	var g OKGroup
	err := s.db.QueryRow(ctx, `SELECT coalesce(ok_group, ''), ok_name, ok_link, ok_error FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&g.ID, &g.Name, &g.Link, &g.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// okLinkRe: the group's page the administrators give for «Write in OK» — an address of ok.ru.
var okLinkRe = regexp.MustCompile(`^https://(m\.)?ok\.ru/[\w./-]+$`)

// ConnectOK makes the OK group with this bot token the line's way in (administrators; checked by the web layer): the
// token is checked with OK, the group is read there, OK is told to call go24 at base (its public address); a group
// serves one line. link — the group's page for the widget's «Write in OK» (empty — by the group's id).
func (s *Store) ConnectOK(ctx context.Context, widgetID, token, link, base string) (string, error) {
	token, link = strings.TrimSpace(token), strings.TrimSpace(link)
	if token == "" || len(token) > 300 || strings.ContainsAny(token, "\r\n ") {
		return "", i18n.E("err.ol_ok_token")
	}
	if link != "" && !okLinkRe.MatchString(link) {
		return "", i18n.E("err.ol_ok_link")
	}
	var a struct {
		Name    string `json:"name"`
		GroupID string `json:"group_id"`
		UserID  string `json:"user_id"`
	}
	if err := s.okCall(ctx, token, "me/info", nil, &a); err != nil {
		return "", i18n.E("err.ol_ok_token")
	}
	id := strings.TrimPrefix(strings.TrimPrefix(a.GroupID, "group:"), "user:")
	if id == "" {
		id = strings.TrimPrefix(a.UserID, "user:")
	}
	if id == "" {
		return "", i18n.E("err.ol_ok_token")
	}
	if link == "" && a.GroupID != "" {
		link = "https://ok.ru/group/" + id
	}
	var other string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE ok_group = $1 AND id::text <> $2`, id, widgetID).Scan(&other)
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
	base = strings.TrimRight(base, "/")
	if err := s.okCall(ctx, token, "me/subscribe", map[string]any{"url": base + s.OKHookPath(widgetID)}, nil); err != nil {
		return "", i18n.E("err.ol_ok_hook", base)
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET ok_secret = $2, ok_group = $3, ok_name = left($4, 200), ok_link = $5, ok_base = $6,
		ok_error = '' WHERE id::text = $1`, widgetID, sealed, id, a.Name, link, base)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return a.Name, nil
}

// DisconnectOK: the group no longer serves the line, OK stops calling go24 (its dialogues stay).
func (s *Store) DisconnectOK(ctx context.Context, widgetID string) error {
	var sealed []byte
	var base string
	err := s.db.QueryRow(ctx, `SELECT ok_secret, ok_base FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&sealed, &base)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if token, err := s.unseal(sealed); sealed != nil && err == nil {
		_ = s.okCall(ctx, token, "me/unsubscribe", map[string]any{"url": base + s.OKHookPath(widgetID)}, nil)
	}
	_, err = s.db.Exec(ctx, `UPDATE ol_widgets SET ok_secret = NULL, ok_group = NULL, ok_name = '', ok_link = '', ok_base = '',
		ok_error = '' WHERE id::text = $1`, widgetID)
	return err
}

// ---------- receiving ----------

type okAttachment struct {
	Type    string `json:"type"`
	Payload struct {
		URL       string  `json:"url"`
		Name      string  `json:"name"`
		Phone     string  `json:"phone"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"payload"`
}

type okEvent struct {
	Type   string `json:"webhookType"`
	Sender struct {
		ID   string `json:"user_id"`
		Name string `json:"name"`
	} `json:"sender"`
	Recipient struct {
		Chat string `json:"chat_id"`
	} `json:"recipient"`
	Message struct {
		Text        string         `json:"text"`
		Attachments []okAttachment `json:"attachments"`
	} `json:"message"`
	Callback struct {
		Payload string `json:"payload"`
	} `json:"callback"`
}

// OKHook takes a call of OK to the line's address (with its secret: else ErrNotFound): a client's message or button
// becomes the line's dialogue's message (the chat's dialogue, or a new one); the chat's start gets the greeting.
func (s *Store) OKHook(ctx context.Context, widgetID, secret string, body []byte) error {
	if !hmac.Equal([]byte(s.OKHookPath(widgetID)), []byte("/hooks/ol/ok/"+widgetID+"/"+secret)) {
		return ErrNotFound
	}
	var sealed []byte
	var active bool
	err := s.db.QueryRow(ctx, `SELECT ok_secret, active FROM ol_widgets WHERE id::text = $1 AND ok_secret IS NOT NULL`, widgetID).Scan(&sealed, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var e okEvent
	if err := json.Unmarshal(body, &e); err != nil {
		return err
	}
	if !active || e.Recipient.Chat == "" {
		return nil
	}
	w, err := s.Widget(ctx, widgetID)
	if err != nil {
		return err
	}
	switch e.Type {
	case "CHAT_STARTED", "BOT_STARTED": // the chat opened: the greeting
		token, err := s.unseal(sealed)
		if w.Greeting == "" || err != nil {
			return nil
		}
		_ = s.okSend(ctx, token, e.Recipient.Chat, "", w.Greeting, "", "", "", nil)
		return nil
	case "MESSAGE_CALLBACK": // a button of the chat bot: its text, as the client's message
		e.Message.Text, e.Message.Attachments = e.Callback.Payload, nil
		return s.okReceive(ctx, w, e)
	case "MESSAGE_CREATED":
		return s.okReceive(ctx, w, e)
	}
	return nil // «MESSAGE_EDITED», «CHAT_SYSTEM»…
}

// okReceive makes a client's message the line's dialogue's message.
func (s *Store) okReceive(ctx context.Context, w Widget, e okEvent) error {
	chat := e.Recipient.Chat
	token := s.okToken(w.ID, chat)
	if _, err := s.db.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, channel, ok_chat, ok_user)
		VALUES ($1, $2, left($3, 200), 'ok', $4, $5)
		ON CONFLICT (token_hash) DO UPDATE SET name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END`,
		w.ID, hash(token), strings.TrimSpace(e.Sender.Name), chat, e.Sender.ID); err != nil {
		return err
	}
	text := strings.TrimSpace(e.Message.Text)
	var files []*File
	for _, a := range e.Message.Attachments {
		p := a.Payload
		switch a.Type {
		case "IMAGE", "VIDEO", "AUDIO", "FILE", "STICKER":
			name := p.Name
			if name == "" {
				name = map[string]string{"IMAGE": "photo.jpg", "VIDEO": "video.mp4", "AUDIO": "audio.mp3", "STICKER": "sticker.png", "FILE": "file"}[a.Type]
			}
			f, err := s.okDownload(ctx, p.URL, name)
			if err != nil {
				text = strings.TrimSpace(text + "\n📎 " + name)
				continue
			}
			files = append(files, f)
		case "SHARE":
			text = strings.TrimSpace(text + "\n" + p.URL)
		case "CONTACT":
			text = strings.TrimSpace(text + "\n👤 " + strings.TrimSpace(p.Name+" "+p.Phone))
		case "LOCATION":
			text = strings.TrimSpace(text + "\n📍 https://maps.google.com/?q=" + strconv.FormatFloat(p.Latitude, 'f', 6, 64) + "," +
				strconv.FormatFloat(p.Longitude, 'f', 6, 64))
		}
	}
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if len(files) == 0 {
		if text == "" {
			return nil
		}
		return s.VisitorSend(ctx, w.ID, token, text, nil)
	}
	for i, f := range files { // the text goes with the first file
		if i > 0 {
			text = ""
		}
		if err := s.VisitorSend(ctx, w.ID, token, text, f); err != nil {
			return err
		}
	}
	return nil
}

// okDownload takes a client's file from OK (its link) into the built-in storage.
func (s *Store) okDownload(ctx context.Context, link, name string) (*File, error) {
	if s.PutFile == nil || link == "" {
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
		return nil, errors.New("ok file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxOKFile+1))
	if err != nil || len(data) == 0 || len(data) > maxOKFile {
		return nil, i18n.E("err.ol_file")
	}
	ct := ""
	if name == "sticker.png" || name == "file" {
		ct = http.DetectContentType(data)
	}
	return s.keepFile(ctx, name, ct, data)
}

// ---------- sending ----------

// SendOK sends to the OK chats the operators' answers and the line's messages not sent yet; it is how many went.
func (s *Store) SendOK(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.ok_chat, w.id::text, w.ok_secret, w.ok_base, coalesce(u.name, ''),
			m.body, m.file_name, m.file_type, m.file_secret, m.buttons
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'ok' AND w.ok_secret IS NOT NULL AND m.id > s.ok_sent AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
		ORDER BY m.id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type out struct {
		ID                         int64
		Session, Chat, Widget      string
		Sealed                     []byte
		Base, Operator, Body       string
		FileName, FileType, Secret string
		Buttons                    []string
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
				link = FileLink(m.Base, m.Widget, Message{ID: m.ID, FileSecret: m.Secret})
			}
			err = s.okSend(ctx, token, m.Chat, m.Operator, m.Body, link, m.FileName, m.FileType, m.Buttons)
		}
		if err != nil && !okGone(err) { // the chat is no more or the client forbade writing: never will be sent
			stopped[m.Session] = true
			errs = append(errs, err)
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET ok_error = left($2, 500) WHERE id::text = $1`, m.Widget, err.Error())
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET ok_sent = $2 WHERE id::text = $1 AND ok_sent < $2`, m.Session, m.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// okSend writes a message to the client's chat: the operator's name, the text; a picture OK takes by its link (the text
// with it), another file as its link; the chat bot's menu as the message's buttons (a button sends its text back, as
// the client's message).
func (s *Store) okSend(ctx context.Context, token, chat, operator, body, link, fileName, fileType string, buttons []string) error {
	text := body
	if operator != "" {
		text = operator + ":"
		if body != "" {
			text += "\n" + body
		}
	}
	var attach []map[string]any
	if link != "" {
		if strings.HasPrefix(fileType, "image/") {
			attach = append(attach, map[string]any{"type": "IMAGE", "payload": map[string]any{"url": link}})
		} else {
			text = strings.TrimSpace(text + "\n📎 " + fileName + " " + link)
		}
	}
	if len(buttons) > 0 {
		rows := [][]map[string]any{}
		for i, b := range buttons {
			if i == 30 {
				break
			}
			b = viberCut(b, 64)
			rows = append(rows, []map[string]any{{"type": "CALLBACK", "text": b, "payload": b}})
		}
		attach = append(attach, map[string]any{"type": "INLINE_KEYBOARD", "payload": map[string]any{"keyboard": map[string]any{"buttons": rows}}})
	}
	msg := map[string]any{"text": viberCut(text, 4000)}
	if len(attach) > 0 {
		msg["attachments"] = attach
	}
	return s.okCall(ctx, token, "me/messages/"+url.PathEscape(chat), map[string]any{"recipient": map[string]string{"chat_id": chat}, "message": msg}, nil)
}

// RunOK sends the answers to the OK chats until ctx ends (go24-worker); the clients' messages come by the webhook.
func (s *Store) RunOK(ctx context.Context, log *slog.Logger) {
	for {
		if _, err := s.SendOK(ctx); err != nil && ctx.Err() == nil {
			log.Error("open lines: ok send", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
