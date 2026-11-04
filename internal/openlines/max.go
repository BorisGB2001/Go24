package openlines

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The channel MAX of a line: the company's bot in the MAX messenger (made in the MAX platform for
// partners, business.max.ru) is a way into the line, as the Telegram bot is. go24 asks the bot API for the events
// (GET /updates with a marker, so it needs no public address): a client's message becomes the line's dialogue (the
// queue, automatic messages, the CRM lead); the operators' answers, files and the chat bot's buttons go back to the
// chat. The bot's opening (bot_started) gets the line's greeting.

// maxMaxFile is the largest client's file go24 takes from MAX.
const maxMaxFile = 50 << 20

// MaxBot is the MAX bot of a line as its settings show it.
type MaxBot struct {
	Name, Error string // the bot's name in MAX (""  — none); what went wrong the last time
}

// maxToken is the visitor's token of a MAX chat in a line: only go24 can make it.
func (s *Store) maxToken(widgetID string, chat int64) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines max\x00"), s.Key...))
	m.Write([]byte(widgetID + ":" + strconv.FormatInt(chat, 10)))
	return "mx" + hex.EncodeToString(m.Sum(nil))
}

// ---------- the bot API ----------

// maxError is an answer of the API that is not 200: its HTTP status (403, 404 — the chat is no more) and code.
type maxError struct {
	Status        int
	Code, Message string
}

func (e *maxError) Error() string { return "max: " + strconv.Itoa(e.Status) + " " + e.Code + " " + e.Message }

// maxCall asks the API (the token goes in the Authorization header) and decodes its answer into out.
func (s *Store) maxCall(ctx context.Context, client *http.Client, token, method, path string, q url.Values, body, out any) error {
	var rd io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	u := s.MaxAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("max " + path + ": " + strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		return &maxError{res.StatusCode, e.Code, e.Message}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// ---------- the line's settings ----------

// Max is the line's MAX bot.
func (s *Store) Max(ctx context.Context, widgetID string) (MaxBot, error) {
	var b MaxBot
	err := s.db.QueryRow(ctx, `SELECT max_name, max_error FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&b.Name, &b.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// ConnectMax makes the MAX bot with this token the line's way in (administrators; checked by the web layer): the
// token is checked with MAX, the bot's name is read there; a bot serves one line.
func (s *Store) ConnectMax(ctx context.Context, widgetID, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 300 || strings.ContainsAny(token, "\r\n ") {
		return "", i18n.E("err.ol_max_token")
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := s.maxCall(ctx, tgShort, token, http.MethodGet, "/me", nil, nil, &me); err != nil || me.Username == "" {
		return "", i18n.E("err.ol_max_token")
	}
	var other string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE lower(max_name) = lower($1) AND id::text <> $2`, me.Username, widgetID).Scan(&other)
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
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET max_secret = $2, max_name = $3, max_next = 0, max_error = '' WHERE id::text = $1`, widgetID, sealed, me.Username)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return me.Username, err
}

// DisconnectMax: the bot no longer serves the line (its dialogues stay).
func (s *Store) DisconnectMax(ctx context.Context, widgetID string) error {
	_, err := s.db.Exec(ctx, `UPDATE ol_widgets SET max_secret = NULL, max_name = '', max_next = 0, max_error = '' WHERE id::text = $1`, widgetID)
	return err
}

// ---------- receiving ----------

// maxBots are the MAX bots of the active lines (next — the marker).
func (s *Store) maxBots(ctx context.Context) ([]tgBot, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, max_secret, max_next FROM ol_widgets WHERE active AND max_secret IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID     string
		Sealed []byte
		Next   int64
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return nil, err
	}
	out := []tgBot{}
	for _, r := range list {
		token, err := s.unseal(r.Sealed)
		if err != nil {
			continue // sealed with another secret: the administrators connect the bot anew
		}
		w, err := s.Widget(ctx, r.ID)
		if err != nil {
			continue
		}
		out = append(out, tgBot{widget: w, token: token, next: r.Next})
	}
	return out, nil
}

type maxUser struct {
	ID        int64  `json:"user_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Name      string `json:"name"` // the old field of the API
	Username  string `json:"username"`
}

func (u maxUser) fullName() string {
	if n := strings.TrimSpace(u.FirstName + " " + u.LastName); n != "" {
		return n
	}
	return strings.TrimSpace(u.Name)
}

type maxAttachment struct {
	Type    string `json:"type"`
	Payload struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
		VCF      string `json:"vcf_info"`
	} `json:"payload"`
	Filename string `json:"filename"` // a file's name and size, where the API put them
	Size     int64  `json:"size"`
}

type maxUpdate struct {
	Type    string  `json:"update_type"`
	ChatID  int64   `json:"chat_id"` // bot_started
	User    maxUser `json:"user"`
	Message *struct {
		Sender    maxUser `json:"sender"`
		Recipient struct {
			ChatID   int64  `json:"chat_id"`
			ChatType string `json:"chat_type"`
		} `json:"recipient"`
		Body *struct {
			Text        string          `json:"text"`
			Attachments []maxAttachment `json:"attachments"`
		} `json:"body"`
	} `json:"message"`
}

// PollMax reads the new events of every line's MAX bot once, waiting up to wait for them; it is how many bots there are.
func (s *Store) PollMax(ctx context.Context, wait time.Duration) (int, error) {
	bots, err := s.maxBots(ctx)
	if err != nil || len(bots) == 0 {
		return 0, err
	}
	var wg sync.WaitGroup
	errs := make([]error, len(bots))
	for i, b := range bots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.pollMax(ctx, b, wait)
		}()
	}
	wg.Wait()
	return len(bots), errors.Join(errs...)
}

func (s *Store) pollMax(ctx context.Context, b tgBot, wait time.Duration) error {
	q := url.Values{"timeout": {strconv.Itoa(int(wait.Seconds()))}, "limit": {"100"}, "types": {"message_created,bot_started"}}
	if b.next > 0 {
		q.Set("marker", strconv.FormatInt(b.next, 10))
	}
	var r struct {
		Updates []maxUpdate `json:"updates"`
		Marker  *int64      `json:"marker"`
	}
	client := &http.Client{Timeout: wait + 30*time.Second}
	if err := s.maxCall(ctx, client, b.token, http.MethodGet, "/updates", q, nil, &r); err != nil {
		if ctx.Err() == nil {
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET max_error = left($2, 500) WHERE id::text = $1`, b.widget.ID, err.Error())
		}
		return err
	}
	var failed error
	for _, u := range r.Updates {
		if err := s.maxReceive(ctx, b, u); err != nil {
			failed = err // the event is lost rather than read again and again
		}
	}
	if r.Marker != nil {
		b.next = *r.Marker
	}
	msg := ""
	if failed != nil {
		msg = failed.Error()
	}
	_, err := s.db.Exec(ctx, `UPDATE ol_widgets SET max_next = $2, max_error = left($3, 500) WHERE id::text = $1`, b.widget.ID, b.next, msg)
	return errors.Join(failed, err)
}

// maxReceive makes a client's message the line's dialogue's message (the chat's dialogue, or a new one); the bot's
// opening gets the greeting.
func (s *Store) maxReceive(ctx context.Context, b tgBot, u maxUpdate) error {
	greet := func(chat int64) error {
		if b.widget.Greeting == "" || chat == 0 {
			return nil
		}
		return s.maxCall(ctx, tgShort, b.token, http.MethodPost, "/messages", url.Values{"chat_id": {strconv.FormatInt(chat, 10)}},
			map[string]any{"text": b.widget.Greeting}, nil)
	}
	if u.Type == "bot_started" {
		return greet(u.ChatID)
	}
	m := u.Message
	if u.Type != "message_created" || m == nil || m.Recipient.ChatType != "dialog" || m.Body == nil { // a group's message is not the line's
		return nil
	}
	chat := m.Recipient.ChatID
	text := strings.TrimSpace(m.Body.Text)
	if text == "/start" {
		return greet(chat)
	}
	token := s.maxToken(b.widget.ID, chat)
	user := ""
	if m.Sender.ID != 0 {
		user = strconv.FormatInt(m.Sender.ID, 10)
	}
	// the chat's dialogue; the client's name as MAX has it
	if _, err := s.db.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, channel, max_chat, max_user)
		VALUES ($1, $2, left($3, 200), 'max', $4, $5)
		ON CONFLICT (token_hash) DO UPDATE SET max_user = EXCLUDED.max_user,
			name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END`,
		b.widget.ID, hash(token), m.Sender.fullName(), chat, user); err != nil {
		return err
	}
	var f *File
	for _, a := range m.Body.Attachments {
		switch a.Type {
		case "contact": // the client shared a phone
			if phone := vcfPhone(a.Payload.VCF); phone != "" {
				if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET phone = left($2, 100) WHERE token_hash = $1`, hash(token), phone); err != nil {
					return err
				}
				text = strings.TrimSpace(text + "\n📞 " + phone)
			}
		case "image", "file", "video", "audio", "sticker":
			if f != nil || a.Payload.URL == "" {
				continue // one file a message in go24
			}
			var err error
			if f, err = s.maxDownload(ctx, a); err != nil {
				f = nil
				text = strings.TrimSpace(text + "\n📎 " + a.name())
			}
		}
	}
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if text == "" && f == nil {
		return nil // a message go24 does not show (a location, a share…)
	}
	return s.VisitorSend(ctx, b.widget.ID, token, text, f)
}

// vcfPhone is the first phone of a vCard.
func vcfPhone(vcf string) string {
	for _, line := range strings.Split(vcf, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok && strings.HasPrefix(strings.ToUpper(k), "TEL") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (a maxAttachment) name() string {
	switch {
	case a.Payload.Filename != "":
		return a.Payload.Filename
	case a.Filename != "":
		return a.Filename
	case a.Type == "image":
		return "photo.jpg"
	case a.Type == "video":
		return "video.mp4"
	case a.Type == "audio":
		return "audio.mp3"
	}
	return a.Type
}

// maxDownload takes a client's file from MAX (its link) into the built-in storage.
func (s *Store) maxDownload(ctx context.Context, a maxAttachment) (*File, error) {
	if s.PutFile == nil || a.Payload.Size > maxMaxFile || a.Size > maxMaxFile {
		return nil, i18n.E("err.ol_file")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Payload.URL, nil)
	if err != nil {
		return nil, err
	}
	res, err := tgShort.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("max file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxMaxFile+1))
	if err != nil || len(data) > maxMaxFile {
		return nil, i18n.E("err.ol_file")
	}
	name, ct := a.name(), ""
	if a.Type == "sticker" { // a sticker is a picture: its type and extension from its bytes
		ct = http.DetectContentType(data)
		ext := ".webp"
		if list, _ := mime.ExtensionsByType(ct); len(list) > 0 {
			ext = list[0]
		}
		name = "sticker" + ext
	}
	return s.keepFile(ctx, name, ct, data)
}

// ---------- sending ----------

// SendMax sends to the MAX chats the operators' answers and the line's messages not sent yet; it is how many went.
func (s *Store) SendMax(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.max_chat, w.id::text, w.max_secret, coalesce(u.name, ''), m.body, m.file_key, m.file_name, m.file_type, m.buttons
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'max' AND w.max_secret IS NOT NULL AND m.id > s.max_sent AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
		ORDER BY m.id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type out struct {
		ID                                          int64
		Session                                     string
		Chat                                        int64
		Widget                                      string
		Sealed                                      []byte
		Operator, Body, FileKey, FileName, FileType string
		Buttons                                     []string
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
			err = s.maxSend(ctx, token, m.Chat, m.Operator, m.Body, m.FileKey, m.FileName, m.FileType, m.Buttons)
		}
		var me *maxError
		gone := errors.As(err, &me) && (me.Status == http.StatusForbidden || me.Status == http.StatusNotFound ||
			me.Status == http.StatusBadRequest && me.Code != "attachment.not.ready") // blocked or gone: not sent, never will be
		if err != nil && !gone {
			stopped[m.Session] = true
			errs = append(errs, err)
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET max_error = left($2, 500) WHERE id::text = $1`, m.Widget, err.Error())
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET max_sent = $2 WHERE id::text = $1 AND max_sent < $2`, m.Session, m.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// maxSend writes a message to the chat: the operator's name in bold, the text; a file as a picture or a file with the
// text; the chat bot's menu as the message's buttons (a button sends its text, as the client's message).
func (s *Store) maxSend(ctx context.Context, token string, chat int64, operator, body, fileKey, fileName, fileType string, buttons []string) error {
	text := html.EscapeString(body)
	if operator != "" {
		text = "<b>" + html.EscapeString(operator) + "</b>"
		if body != "" {
			text += "\n" + html.EscapeString(body)
		}
	}
	attach := []any{}
	if fileKey != "" {
		a, err := s.maxUpload(ctx, token, fileKey, fileName, fileType)
		if err != nil {
			return err
		}
		if a == nil {
			text += "\n📎 " + html.EscapeString(fileName)
		} else {
			attach = append(attach, a)
		}
	}
	if len(buttons) > 0 {
		rows := [][]map[string]string{}
		for _, b := range buttons {
			if r := []rune(b); len(r) > 128 {
				b = string(r[:128])
			}
			rows = append(rows, []map[string]string{{"type": "message", "text": b}})
		}
		attach = append(attach, map[string]any{"type": "inline_keyboard", "payload": map[string]any{"buttons": rows}})
	}
	if r := []rune(text); len(r) > 4000 {
		text = string(r[:4000])
	}
	msg := map[string]any{"text": text, "format": "html"}
	if len(attach) > 0 {
		msg["attachments"] = attach
	}
	q := url.Values{"chat_id": {strconv.FormatInt(chat, 10)}}
	var err error
	for try := 0; try < 4; try++ { // a file just uploaded may be still processed by MAX
		err = s.maxCall(ctx, tgShort, token, http.MethodPost, "/messages", q, msg, nil)
		var me *maxError
		if !errors.As(err, &me) || me.Code != "attachment.not.ready" {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<try) * time.Second):
		}
	}
	return err
}

// maxUpload puts the file into MAX (POST /uploads, then the file to the address it gave) and is its attachment: a
// picture as an image, the rest as a file; nil — there is no storage to read the file from.
func (s *Store) maxUpload(ctx context.Context, token, fileKey, fileName, fileType string) (any, error) {
	if s.OpenFile == nil {
		return nil, nil
	}
	kind := "file"
	if (Message{FileType: fileType}).Image() {
		kind = "image"
	}
	var slot struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := s.maxCall(ctx, tgShort, token, http.MethodPost, "/uploads", url.Values{"type": {kind}}, nil, &slot); err != nil {
		return nil, err
	}
	if slot.URL == "" {
		return nil, errors.New("max uploads: no address")
	}
	obj, err := s.OpenFile(ctx, fileKey)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("data", fileName)
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, obj); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slot.URL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := tgShort.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("max upload: " + res.Status)
	}
	var got map[string]json.RawMessage
	_ = json.NewDecoder(res.Body).Decode(&got)
	payload := map[string]any{"token": slot.Token}
	if photos, ok := got["photos"]; ok { // a picture: the tokens of its sizes
		payload = map[string]any{"photos": photos}
	} else if t, ok := got["token"]; ok {
		payload["token"] = t
	}
	return map[string]any{"type": kind, "payload": payload}, nil
}

// RunMax reads the MAX bots' messages and sends the answers until ctx ends (go24-worker).
func (s *Store) RunMax(ctx context.Context, log *slog.Logger) {
	go func() {
		for {
			if _, err := s.SendMax(ctx); err != nil && ctx.Err() == nil {
				log.Error("open lines: max send", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	for ctx.Err() == nil {
		n, err := s.PollMax(ctx, 25*time.Second)
		if err != nil && ctx.Err() == nil {
			log.Error("open lines: max", "err", err)
		}
		pause := time.Duration(0)
		switch {
		case n == 0:
			pause = 30 * time.Second // no bots yet
		case err != nil:
			pause = 10 * time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
}
