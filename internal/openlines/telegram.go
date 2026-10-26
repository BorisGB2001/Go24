package openlines

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The channel Telegram of a line: the company's bot (made with @BotFather) is a way into the line. go24
// asks the Bot API for the messages (getUpdates, so it needs no public address): a client's message becomes the
// line's dialogue as a widget visitor's would (the queue, automatic messages, the CRM lead); the operators' answers and
// the line's messages go back to the chat. Pictures and files go both ways, kept in the built-in storage.

// tgMaxFile is the largest file the Bot API gives a bot.
const tgMaxFile = 20 << 20

// TelegramBot is the bot of a line as its settings show it.
type TelegramBot struct {
	Name, Error string // @name of the bot (""  — none); what went wrong the last time
}

// ---------- the bot's token, sealed ----------

func (s *Store) gcm() (cipher.AEAD, error) {
	k := sha256.Sum256(append([]byte("go24 openlines bots\x00"), s.Key...))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Store) seal(plain string) ([]byte, error) {
	g, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Store) unseal(sealed []byte) (string, error) {
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("openlines: bad token")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
	return string(out), err
}

// tgToken is the visitor's token of a Telegram chat in a line: only go24 can make it (the widget's API does not open
// the chat's dialogue to someone who knows the chat's number).
func (s *Store) tgToken(widgetID string, chat int64) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines telegram\x00"), s.Key...))
	m.Write([]byte(widgetID + ":" + strconv.FormatInt(chat, 10)))
	return "tg" + hex.EncodeToString(m.Sum(nil))
}

// ---------- the Bot API ----------

type tgError struct {
	Code        int
	Description string
}

func (e *tgError) Error() string { return fmt.Sprintf("telegram: %d %s", e.Code, e.Description) }

// tgCall asks the Bot API (JSON, or multipart when body is one) and decodes its result into out.
func (s *Store) tgCall(ctx context.Context, client *http.Client, token, method string, args any, out any) error {
	var body io.Reader
	ct := "application/json"
	if mp, ok := args.(*tgMultipart); ok {
		body, ct = &mp.buf, mp.ct
	} else {
		raw, err := json.Marshal(args)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.TelegramAPI+"/bot"+token+"/"+method, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ct)
	res, err := client.Do(req)
	if err != nil {
		return errors.New("telegram " + method + ": " + strings.ReplaceAll(err.Error(), token, "***")) // the token is in the URL
	}
	defer res.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: %s", method, res.Status)
	}
	if !r.OK {
		return &tgError{r.ErrorCode, r.Description}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

type tgMultipart struct {
	buf bytes.Buffer
	ct  string
}

var tgShort = &http.Client{Timeout: 60 * time.Second}

// ---------- the line's settings ----------

// Telegram is the line's bot.
func (s *Store) Telegram(ctx context.Context, widgetID string) (TelegramBot, error) {
	var b TelegramBot
	err := s.db.QueryRow(ctx, `SELECT tg_name, tg_error FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&b.Name, &b.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// ConnectTelegram makes the bot with this token the line's way in (administrators; checked by the web layer): the
// token is checked with Telegram, the bot's name is read there; a bot serves one line.
func (s *Store) ConnectTelegram(ctx context.Context, widgetID, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 200 || strings.ContainsAny(token, "/?# ") {
		return "", i18n.E("err.ol_tg_token")
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := s.tgCall(ctx, tgShort, token, "getMe", map[string]any{}, &me); err != nil || me.Username == "" {
		return "", i18n.E("err.ol_tg_token")
	}
	var other string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE lower(tg_name) = lower($1) AND id::text <> $2`, me.Username, widgetID).Scan(&other)
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
	// a bot that had a webhook gives no updates: go24 reads them itself
	_ = s.tgCall(ctx, tgShort, token, "deleteWebhook", map[string]any{}, nil)
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET tg_secret = $2, tg_name = $3, tg_next = 0, tg_error = '' WHERE id::text = $1`, widgetID, sealed, me.Username)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return me.Username, err
}

// DisconnectTelegram: the bot no longer serves the line (its dialogues stay).
func (s *Store) DisconnectTelegram(ctx context.Context, widgetID string) error {
	_, err := s.db.Exec(ctx, `UPDATE ol_widgets SET tg_secret = NULL, tg_name = '', tg_next = 0, tg_error = '' WHERE id::text = $1`, widgetID)
	return err
}

// ---------- receiving ----------

type tgBot struct {
	widget Widget
	token  string
	next   int64
}

// tgBots are the bots of the active lines.
func (s *Store) tgBots(ctx context.Context) ([]tgBot, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, tg_secret, tg_next FROM ol_widgets WHERE active AND tg_secret IS NOT NULL`)
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

type tgFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

type tgMessage struct {
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	From struct {
		FirstName string `json:"first_name"`
		LastName  string `json:"last_name"`
		Username  string `json:"username"`
	} `json:"from"`
	Text     string   `json:"text"`
	Caption  string   `json:"caption"`
	Photo    []tgFile `json:"photo"`
	Document *tgFile  `json:"document"`
	Video    *tgFile  `json:"video"`
	Audio    *tgFile  `json:"audio"`
	Voice    *tgFile  `json:"voice"`
	Sticker  *struct {
		Emoji string `json:"emoji"`
	} `json:"sticker"`
	Contact *struct {
		Phone string `json:"phone_number"`
	} `json:"contact"`
}

// PollTelegram reads the new messages of every line's bot once, waiting up to wait for them; it is how many bots
// there are.
func (s *Store) PollTelegram(ctx context.Context, wait time.Duration) (int, error) {
	bots, err := s.tgBots(ctx)
	if err != nil || len(bots) == 0 {
		return 0, err
	}
	var wg sync.WaitGroup
	errs := make([]error, len(bots))
	for i, b := range bots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.pollBot(ctx, b, wait)
		}()
	}
	wg.Wait()
	return len(bots), errors.Join(errs...)
}

func (s *Store) pollBot(ctx context.Context, b tgBot, wait time.Duration) error {
	var updates []struct {
		ID      int64      `json:"update_id"`
		Message *tgMessage `json:"message"`
	}
	client := &http.Client{Timeout: wait + 30*time.Second}
	err := s.tgCall(ctx, client, b.token, "getUpdates", map[string]any{"offset": b.next, "timeout": int(wait.Seconds()),
		"allowed_updates": []string{"message"}}, &updates)
	if err != nil {
		if ctx.Err() == nil {
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET tg_error = left($2, 500) WHERE id::text = $1`, b.widget.ID, err.Error())
		}
		return err
	}
	var failed error
	for _, u := range updates {
		if u.Message != nil && u.Message.Chat.Type == "private" {
			if err := s.tgReceive(ctx, b, *u.Message); err != nil {
				failed = err // the message is lost rather than read again and again
			}
		}
		b.next = u.ID + 1
	}
	msg := ""
	if failed != nil {
		msg = failed.Error()
	}
	_, err = s.db.Exec(ctx, `UPDATE ol_widgets SET tg_next = $2, tg_error = left($3, 500) WHERE id::text = $1`, b.widget.ID, b.next, msg)
	return errors.Join(failed, err)
}

// tgReceive makes a client's message the line's dialogue's message (the chat's dialogue, or a new one).
func (s *Store) tgReceive(ctx context.Context, b tgBot, m tgMessage) error {
	text := strings.TrimSpace(m.Text + m.Caption)
	if strings.HasPrefix(text, "/start") { // the bot opened: the line's greeting
		if b.widget.Greeting != "" {
			return s.tgCall(ctx, tgShort, b.token, "sendMessage", map[string]any{"chat_id": m.Chat.ID, "text": b.widget.Greeting}, nil)
		}
		return nil
	}
	token := s.tgToken(b.widget.ID, m.Chat.ID)
	name := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
	page := ""
	if m.From.Username != "" {
		page = "https://t.me/" + m.From.Username
	}
	// the chat's dialogue; the client's name and @name as Telegram has them now
	if _, err := s.db.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, page, channel, tg_chat, tg_user)
		VALUES ($1, $2, left($3, 200), $4, 'telegram', $5, $6)
		ON CONFLICT (token_hash) DO UPDATE SET tg_user = EXCLUDED.tg_user, page = EXCLUDED.page,
			name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END`,
		b.widget.ID, hash(token), name, page, m.Chat.ID, m.From.Username); err != nil {
		return err
	}
	if m.Contact != nil && m.Contact.Phone != "" { // the client shared their phone
		phone := strings.TrimSpace(m.Contact.Phone)
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET phone = left($2, 100) WHERE token_hash = $1`, hash(token), phone); err != nil {
			return err
		}
		text = "📞 " + phone
	}
	if text == "" && m.Sticker != nil {
		text = m.Sticker.Emoji
	}
	var f *File
	if src := m.file(); src != nil {
		var err error
		if f, err = s.tgDownload(ctx, b, *src); err != nil {
			f = nil
			text = strings.TrimSpace(text + "\n📎 " + src.name()) // the file stays in Telegram: its name at least
		}
	}
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if text == "" && f == nil {
		return nil // a message go24 does not show (a location, a poll…)
	}
	return s.VisitorSend(ctx, b.widget.ID, token, text, f)
}

// file is the message's file: the largest size of a photo, a document, a video, an audio, a voice message.
func (m tgMessage) file() *tgFile {
	if n := len(m.Photo); n > 0 {
		p := m.Photo[n-1]
		p.FileName, p.MimeType = "photo.jpg", "image/jpeg"
		return &p
	}
	for _, f := range []*tgFile{m.Document, m.Video, m.Audio, m.Voice} {
		if f != nil {
			return f
		}
	}
	return nil
}

func (f tgFile) name() string {
	if f.FileName != "" {
		return f.FileName
	}
	ext := ".bin"
	if list, _ := mime.ExtensionsByType(f.MimeType); len(list) > 0 {
		ext = list[0]
	}
	return "file" + ext
}

// tgDownload takes a client's file from Telegram into the built-in storage.
func (s *Store) tgDownload(ctx context.Context, b tgBot, src tgFile) (*File, error) {
	if s.PutFile == nil || src.FileSize > tgMaxFile {
		return nil, i18n.E("err.ol_file")
	}
	var info struct {
		Path string `json:"file_path"`
		Size int64  `json:"file_size"`
	}
	if err := s.tgCall(ctx, tgShort, b.token, "getFile", map[string]any{"file_id": src.FileID}, &info); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.TelegramAPI+"/file/bot"+b.token+"/"+info.Path, nil)
	if err != nil {
		return nil, err
	}
	res, err := tgShort.Do(req)
	if err != nil {
		return nil, errors.New("telegram file: " + strings.ReplaceAll(err.Error(), b.token, "***"))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("telegram file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, tgMaxFile+1))
	if err != nil || len(data) > tgMaxFile {
		return nil, i18n.E("err.ol_file")
	}
	name := CleanName(src.name())
	ct := src.MimeType
	if ct == "" {
		ct = mime.TypeByExtension(path.Ext(name))
	}
	if ct == "" {
		ct = http.DetectContentType(data)
	}
	b16 := make([]byte, 16)
	if _, err := rand.Read(b16); err != nil {
		return nil, err
	}
	key := path.Join("openlines", time.Now().Format("2006/01"), hex.EncodeToString(b16), name)
	if err := s.PutFile(ctx, key, bytes.NewReader(data), int64(len(data)), ct); err != nil {
		return nil, err
	}
	return &File{Key: key, Name: name, Type: ct, Size: int64(len(data))}, nil
}

// ---------- sending ----------

// SendTelegram sends to the chats the operators' answers and the line's messages not sent yet; it is how many went.
func (s *Store) SendTelegram(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.tg_chat, w.id::text, w.tg_secret, coalesce(u.name, ''), m.body, m.file_key, m.file_name, m.file_type
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'telegram' AND w.tg_secret IS NOT NULL AND m.id > s.tg_sent AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
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
			err = s.tgSend(ctx, token, m.Chat, m.Operator, m.Body, m.FileKey, m.FileName, m.FileType)
		}
		var te *tgError
		if err != nil && !(errors.As(err, &te) && (te.Code == 403 || te.Code == 400)) { // blocked or gone: not sent, never will be
			stopped[m.Session] = true
			errs = append(errs, err)
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET tg_error = left($2, 500) WHERE id::text = $1`, m.Widget, err.Error())
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET tg_sent = $2 WHERE id::text = $1 AND tg_sent < $2`, m.Session, m.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// tgSend writes a message to the chat: the operator's name in bold, the text; a file as a photo or a document with
// the text as its caption.
func (s *Store) tgSend(ctx context.Context, token string, chat int64, operator, body, fileKey, fileName, fileType string) error {
	text := html.EscapeString(body)
	if operator != "" {
		text = "<b>" + html.EscapeString(operator) + "</b>"
		if body != "" {
			text += "\n" + html.EscapeString(body)
		}
	}
	if fileKey == "" || s.OpenFile == nil {
		if fileKey != "" {
			text += "\n📎 " + html.EscapeString(fileName)
		}
		return s.tgCall(ctx, tgShort, token, "sendMessage", map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML"}, nil)
	}
	obj, err := s.OpenFile(ctx, fileKey)
	if err != nil {
		return err
	}
	defer obj.Close()
	method, field := "sendDocument", "document"
	if (Message{FileType: fileType}).Image() && fileType != "image/gif" {
		method, field = "sendPhoto", "photo"
	}
	mp := &tgMultipart{}
	w := multipart.NewWriter(&mp.buf)
	_ = w.WriteField("chat_id", strconv.FormatInt(chat, 10))
	if r := []rune(text); len(r) > 1024 { // a caption's limit
		text = string(r[:1024])
	}
	if text != "" {
		_ = w.WriteField("caption", text)
		_ = w.WriteField("parse_mode", "HTML")
	}
	part, err := w.CreateFormFile(field, fileName)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, obj); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	mp.ct = w.FormDataContentType()
	return s.tgCall(ctx, tgShort, token, method, mp, nil)
}

// RunTelegram reads the bots' messages and sends the answers until ctx ends (go24-worker).
func (s *Store) RunTelegram(ctx context.Context, log *slog.Logger) {
	go func() {
		for {
			if _, err := s.SendTelegram(ctx); err != nil && ctx.Err() == nil {
				log.Error("open lines: telegram send", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	for ctx.Err() == nil {
		n, err := s.PollTelegram(ctx, 25*time.Second)
		if err != nil && ctx.Err() == nil {
			log.Error("open lines: telegram", "err", err)
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
