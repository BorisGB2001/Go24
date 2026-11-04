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

// The channel WhatsApp of a line: the company's WhatsApp Business number (the Cloud API of Meta: the
// number's id, an access token, the application's secret) is a way into the line. Meta is not asked for the messages:
// it calls go24's public address itself (the webhook of the WhatsApp Business account, which go24 subscribes when told
// the account's id, or the administrators set in the application), every call signed with the application's secret. A
// client's message becomes the line's dialogue (the queue, automatic messages, the CRM lead — the client's number is
// the dialogue's phone); the operators' answers, files (Meta takes them by go24's link) and the chat bot's buttons go
// back to the client. WhatsApp lets the business write only within 24 hours of the client's last message: an answer
// later than that is not delivered, the dialogue says so.

// maxWAFile is the largest client's file go24 takes from WhatsApp.
const maxWAFile = 100 << 20

// ErrWASign: a call to the line's WhatsApp address not signed with its application's secret.
var ErrWASign = errors.New("whatsapp: bad signature")

// WANumber is the WhatsApp number of a line as its settings show it.
type WANumber struct {
	Phone, Number, Name string // the number's id in the Cloud API ("" — none), the number, the business's name
	WABA                string // the WhatsApp Business account whose webhook go24 subscribed ("" — set by hand)
	Hook, Verify        string // go24's address for the webhook and its verify token (for the application's settings)
	Error               string // what went wrong the last time
}

// WAHookPath is the address in go24 Meta calls the line's number at.
func WAHookPath(widgetID string) string { return "/hooks/ol/whatsapp/" + widgetID }

// waVerify is the verify token of the line's webhook: Meta shows it back when it checks the address.
func (s *Store) waVerify(widgetID string) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines whatsapp verify\x00"), s.Key...))
	m.Write([]byte(widgetID))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// waToken is the visitor's token of a WhatsApp chat in a line: only go24 can make it.
func (s *Store) waToken(widgetID, user string) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines whatsapp\x00"), s.Key...))
	m.Write([]byte(widgetID + ":" + user))
	return "wa" + hex.EncodeToString(m.Sum(nil))
}

// ---------- the API ----------

// waError is an answer of the Graph API with an error (190 — the token is bad, 100 — no such number, 131047 — more
// than 24 hours since the client's last message, 131026 — the client cannot get it…).
type waError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *waError) Error() string { return "whatsapp: " + strconv.Itoa(e.Code) + " " + e.Message }

// waGone: the message will never go — the 24 hours are over, the number is not in WhatsApp, the client blocked the
// business or the message is one WhatsApp does not take.
func waGone(err error) bool {
	var we *waError
	return errors.As(err, &we) && (we.Code == 131047 || we.Code == 131026 || we.Code == 131051 || we.Code == 131052 ||
		we.Code == 131053 || we.Code == 470)
}

// waCall calls the Graph API (the token in the Authorization header; body — JSON, nil — a GET) and decodes its answer
// into out.
func (s *Store) waCall(ctx context.Context, token, path string, body, out any) error {
	verb, rd := http.MethodGet, io.Reader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		verb, rd = http.MethodPost, bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, verb, s.WhatsAppAPI+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := tgShort.Do(req)
	if err != nil {
		return errors.New("whatsapp: " + strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer res.Body.Close()
	all, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var e struct {
		Error *waError `json:"error"`
	}
	if json.Unmarshal(all, &e) == nil && e.Error != nil {
		return e.Error
	}
	if res.StatusCode != http.StatusOK {
		return errors.New("whatsapp: " + res.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(all, out)
}

// ---------- the line's settings ----------

// WhatsApp is the line's WhatsApp number.
func (s *Store) WhatsApp(ctx context.Context, widgetID string) (WANumber, error) {
	var n WANumber
	var base string
	err := s.db.QueryRow(ctx, `SELECT coalesce(wa_phone, ''), wa_number, wa_name, wa_waba, wa_base, wa_error FROM ol_widgets WHERE id::text = $1`,
		widgetID).Scan(&n.Phone, &n.Number, &n.Name, &n.WABA, &base, &n.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	if n.Phone != "" {
		n.Hook, n.Verify = base+WAHookPath(widgetID), s.waVerify(widgetID)
	}
	return n, err
}

// waIDRe: the ids of the Cloud API (the number's, the business account's) are digits.
var waIDRe = regexp.MustCompile(`^\d{5,30}$`)

// ConnectWhatsApp makes the WhatsApp Business number with this id and access token the line's way in (administrators;
// checked by the web layer): the token is checked with Meta, the number and the business's name are read there; with
// the business account's id (waba) Meta is told to call go24 at base (its public address), else the administrators
// set the webhook in the application themselves; appSecret checks Meta's calls. A number serves one line.
func (s *Store) ConnectWhatsApp(ctx context.Context, widgetID, phone, waba, token, appSecret, base string) (string, error) {
	phone, waba, token, appSecret = strings.TrimSpace(phone), strings.TrimSpace(waba), strings.TrimSpace(token), strings.TrimSpace(appSecret)
	if !waIDRe.MatchString(phone) || (waba != "" && !waIDRe.MatchString(waba)) || token == "" || len(token) > 1000 ||
		strings.ContainsAny(token, "\r\n ") || appSecret == "" || len(appSecret) > 200 || strings.ContainsAny(appSecret, "\r\n ") {
		return "", i18n.E("err.ol_wa_token")
	}
	var a struct {
		Number string `json:"display_phone_number"`
		Name   string `json:"verified_name"`
	}
	if err := s.waCall(ctx, token, "/"+phone+"?fields=display_phone_number,verified_name", nil, &a); err != nil {
		return "", i18n.E("err.ol_wa_token")
	}
	var other string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE wa_phone = $1 AND id::text <> $2`, phone, widgetID).Scan(&other)
	if err == nil {
		return "", i18n.E("err.ol_tg_taken", other)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	sealed, err := s.seal(token + "\n" + appSecret)
	if err != nil {
		return "", err
	}
	// kept before the webhook is subscribed: Meta checks the address at once
	base = strings.TrimRight(base, "/")
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET wa_secret = $2, wa_phone = $3, wa_number = left($4, 50), wa_name = left($5, 200), wa_waba = $6,
		wa_base = $7, wa_error = '' WHERE id::text = $1`, widgetID, sealed, phone, a.Number, a.Name, waba, base)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if waba != "" {
		if err := s.waCall(ctx, token, "/"+waba+"/subscribed_apps", map[string]any{"override_callback_uri": base + WAHookPath(widgetID),
			"verify_token": s.waVerify(widgetID)}, nil); err != nil {
			_ = s.DisconnectWhatsApp(ctx, widgetID)
			return "", i18n.E("err.ol_wa_hook", base)
		}
	}
	return a.Name, nil
}

// DisconnectWhatsApp: the number no longer serves the line, Meta stops calling go24 (its dialogues stay).
func (s *Store) DisconnectWhatsApp(ctx context.Context, widgetID string) error {
	var sealed []byte
	var waba string
	err := s.db.QueryRow(ctx, `SELECT wa_secret, wa_waba FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&sealed, &waba)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if plain, err := s.unseal(sealed); sealed != nil && err == nil && waba != "" {
		token, _ := avitoCredentials(plain)
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.WhatsAppAPI+"/"+waba+"/subscribed_apps", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+token)
			if res, err := tgShort.Do(req); err == nil {
				res.Body.Close()
			}
		}
	}
	_, err = s.db.Exec(ctx, `UPDATE ol_widgets SET wa_secret = NULL, wa_phone = NULL, wa_number = '', wa_name = '', wa_waba = '', wa_base = '',
		wa_error = '' WHERE id::text = $1`, widgetID)
	return err
}

// WAVerify answers Meta's check of the line's webhook address: the challenge when the verify token is the line's.
func (s *Store) WAVerify(ctx context.Context, widgetID, mode, verify, challenge string) (string, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT wa_secret IS NOT NULL FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !on) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if mode != "subscribe" || !hmac.Equal([]byte(verify), []byte(s.waVerify(widgetID))) {
		return "", ErrWASign
	}
	return challenge, nil
}

// ---------- receiving ----------

// waMedia is a file of a client's message: its id in the Cloud API.
type waMedia struct {
	ID       string `json:"id"`
	Mime     string `json:"mime_type"`
	Caption  string `json:"caption"`
	FileName string `json:"filename"`
	Voice    bool   `json:"voice"`
}

type waMessage struct {
	From     string                `json:"from"`
	Type     string                `json:"type"`
	Text     struct{ Body string } `json:"text"`
	Image    *waMedia              `json:"image"`
	Video    *waMedia              `json:"video"`
	Audio    *waMedia              `json:"audio"`
	Document *waMedia              `json:"document"`
	Sticker  *waMedia              `json:"sticker"`
	Location struct {
		Lat     float64 `json:"latitude"`
		Lon     float64 `json:"longitude"`
		Name    string  `json:"name"`
		Address string  `json:"address"`
	} `json:"location"`
	Contacts []struct {
		Name struct {
			Formatted string `json:"formatted_name"`
		} `json:"name"`
		Phones []struct {
			Phone string `json:"phone"`
		} `json:"phones"`
	} `json:"contacts"`
	Interactive struct {
		Button struct{ Title string } `json:"button_reply"`
		List   struct{ Title string } `json:"list_reply"`
	} `json:"interactive"`
	Button struct{ Text string } `json:"button"` // a template's quick reply
}

type waHook struct {
	Entry []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Metadata struct {
					Phone string `json:"phone_number_id"`
				} `json:"metadata"`
				Contacts []struct {
					WAID    string                `json:"wa_id"`
					Profile struct{ Name string } `json:"profile"`
				} `json:"contacts"`
				Messages []waMessage `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// WAHook takes a call of Meta to the line's address (signed with its application's secret: else ErrWASign): the
// clients' messages to its number become the line's dialogues' messages (the chat's dialogue, or a new one).
func (s *Store) WAHook(ctx context.Context, widgetID, sign string, body []byte) error {
	var sealed []byte
	var phone string
	var active bool
	err := s.db.QueryRow(ctx, `SELECT wa_secret, wa_phone, active FROM ol_widgets WHERE id::text = $1 AND wa_secret IS NOT NULL`,
		widgetID).Scan(&sealed, &phone, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	plain, err := s.unseal(sealed)
	if err != nil {
		return ErrWASign // sealed with another secret: the administrators connect the number anew
	}
	token, appSecret := avitoCredentials(plain)
	m := hmac.New(sha256.New, []byte(appSecret))
	m.Write(body)
	got, err := hex.DecodeString(strings.TrimPrefix(sign, "sha256="))
	if err != nil || !hmac.Equal(got, m.Sum(nil)) {
		return ErrWASign
	}
	var h waHook
	if err := json.Unmarshal(body, &h); err != nil {
		return err
	}
	if !active {
		return nil
	}
	w, err := s.Widget(ctx, widgetID)
	if err != nil {
		return err
	}
	for _, e := range h.Entry {
		for _, c := range e.Changes {
			v := c.Value
			if c.Field != "messages" || v.Metadata.Phone != phone { // statuses come with no messages
				continue
			}
			names := map[string]string{}
			for _, x := range v.Contacts {
				names[x.WAID] = x.Profile.Name
			}
			for _, msg := range v.Messages {
				if err := s.waReceive(ctx, w, token, names[msg.From], msg); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// waReceive makes a client's message the line's dialogue's message; the client's WhatsApp id is their number.
func (s *Store) waReceive(ctx context.Context, w Widget, apiToken, name string, m waMessage) error {
	if !waIDRe.MatchString(m.From) {
		return nil
	}
	token := s.waToken(w.ID, m.From)
	if _, err := s.db.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, phone, channel, wa_user)
		VALUES ($1, $2, left($3, 200), $4, 'whatsapp', $5)
		ON CONFLICT (token_hash) DO UPDATE SET name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END,
			phone = CASE WHEN ol_sessions.phone = '' THEN EXCLUDED.phone ELSE ol_sessions.phone END`,
		w.ID, hash(token), strings.TrimSpace(name), "+"+m.From, m.From); err != nil {
		return err
	}
	text := strings.TrimSpace(m.Text.Body)
	var f *File
	media, def := (*waMedia)(nil), ""
	switch m.Type {
	case "image":
		media, def = m.Image, "photo.jpg"
	case "video":
		media, def = m.Video, "video.mp4"
	case "audio":
		media, def = m.Audio, "audio.ogg"
		if m.Audio != nil && m.Audio.Voice {
			def = "voice.ogg"
		}
	case "document":
		media, def = m.Document, "file"
	case "sticker":
		media, def = m.Sticker, "sticker.webp"
	case "location":
		l := m.Location
		text = strings.TrimSpace("📍 " + strings.TrimSpace(l.Name+" "+l.Address) + "\nhttps://maps.google.com/?q=" +
			strconv.FormatFloat(l.Lat, 'f', 6, 64) + "," + strconv.FormatFloat(l.Lon, 'f', 6, 64))
	case "contacts":
		for _, c := range m.Contacts {
			line := c.Name.Formatted
			for _, p := range c.Phones {
				line += " " + p.Phone
			}
			text = strings.TrimSpace(text + "\n👤 " + strings.TrimSpace(line))
		}
	case "interactive": // a button or a list's item of the chat bot: its text, as the client's message
		text = strings.TrimSpace(m.Interactive.Button.Title + m.Interactive.List.Title)
	case "button":
		text = strings.TrimSpace(m.Button.Text)
	}
	if media != nil {
		text = strings.TrimSpace(media.Caption)
		fileName := CleanName(media.FileName)
		if media.FileName == "" {
			fileName = def
		}
		var err error
		if f, err = s.waDownload(ctx, apiToken, media.ID, fileName, media.Mime); err != nil {
			f = nil
			text = strings.TrimSpace(text + "\n📎 " + fileName)
		}
	}
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if text == "" && f == nil {
		return nil // a reaction, a message WhatsApp calls unsupported…
	}
	return s.VisitorSend(ctx, w.ID, token, text, f)
}

// waDownload takes a client's file from the Cloud API (its id: the link to it, then the file by the token) into the
// built-in storage.
func (s *Store) waDownload(ctx context.Context, token, id, name, ct string) (*File, error) {
	if s.PutFile == nil || id == "" {
		return nil, i18n.E("err.ol_file")
	}
	var meta struct {
		URL  string `json:"url"`
		Mime string `json:"mime_type"`
		Size int64  `json:"file_size"`
	}
	if err := s.waCall(ctx, token, "/"+url.PathEscape(id), nil, &meta); err != nil {
		return nil, err
	}
	if meta.URL == "" || meta.Size > maxWAFile {
		return nil, i18n.E("err.ol_file")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := tgShort.Do(req)
	if err != nil {
		return nil, errors.New("whatsapp file: " + strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("whatsapp file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxWAFile+1))
	if err != nil || len(data) == 0 || len(data) > maxWAFile {
		return nil, i18n.E("err.ol_file")
	}
	if ct == "" {
		ct = meta.Mime
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 { // «audio/ogg; codecs=opus»
		ct = strings.TrimSpace(ct[:i])
	}
	return s.keepFile(ctx, name, ct, data)
}

// ---------- sending ----------

// SendWhatsApp sends to the WhatsApp chats the operators' answers and the line's messages not sent yet; it is how many
// went.
func (s *Store) SendWhatsApp(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.wa_user, w.id::text, w.wa_secret, w.wa_phone, w.wa_base, coalesce(u.name, ''),
			m.body, m.file_name, m.file_type, m.file_secret, m.buttons
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'whatsapp' AND w.wa_secret IS NOT NULL AND m.id > s.wa_sent AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
		ORDER BY m.id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type out struct {
		ID                         int64
		Session, User, Widget      string
		Sealed                     []byte
		Phone, Base, Operator      string
		Body                       string
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
		plain, err := s.unseal(m.Sealed)
		if err == nil {
			token, _ := avitoCredentials(plain)
			link := ""
			if m.FileName != "" {
				link = FileLink(m.Base, m.Widget, Message{ID: m.ID, FileSecret: m.Secret})
			}
			err = s.waSend(ctx, token, m.Phone, m.User, m.Operator, m.Body, link, m.FileName, m.FileType, m.Buttons)
		}
		if waGone(err) { // never will be sent: the operators see why
			var we *waError
			errors.As(err, &we)
			key := "ol.note_wa_failed"
			if we.Code == 131047 || we.Code == 470 {
				key = "ol.note_wa_window"
			}
			if err := s.note(ctx, s.db, m.Session, key, we.Message); err != nil {
				return sent, err
			}
		} else if err != nil {
			stopped[m.Session] = true
			errs = append(errs, err)
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET wa_error = left($2, 500) WHERE id::text = $1`, m.Widget, err.Error())
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET wa_sent = $2 WHERE id::text = $1 AND wa_sent < $2`, m.Session, m.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// waSend writes a message to the client: the operator's name, the text; a picture (JPEG or PNG) or another file Meta
// takes by its link, the text as its caption when short; the chat bot's menu as reply buttons (three at most, short) or
// a list (ten at most), else as its lines — a button sends its text back, as the client's message.
func (s *Store) waSend(ctx context.Context, token, phone, to, operator, body, link, fileName, fileType string, buttons []string) error {
	text := body
	if operator != "" {
		text = operator + ":"
		if body != "" {
			text += "\n" + body
		}
	}
	send := func(typ string, content any) error {
		return s.waCall(ctx, token, "/"+phone+"/messages", map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual",
			"to": to, "type": typ, typ: content}, nil)
	}
	short, list := len(buttons) > 0 && len(buttons) <= 3, len(buttons) > 0 && len(buttons) <= 10
	for _, b := range buttons {
		n := len([]rune(b))
		short = short && n <= 20
		list = list && n <= 24
	}
	if len(buttons) > 0 && !short && !list {
		for _, b := range buttons {
			text = strings.TrimSpace(text + "\n" + b)
		}
		buttons = nil
	}
	if link != "" {
		typ := "document"
		if fileType == "image/jpeg" || fileType == "image/png" {
			typ = "image"
		}
		media := map[string]any{"link": link}
		if typ == "document" {
			media["filename"] = viberCut(fileName, 240)
		}
		if len(buttons) == 0 && len([]rune(text)) <= 1024 { // the text goes as the file's caption
			if text != "" {
				media["caption"] = text
			}
			text = ""
		}
		if err := send(typ, media); err != nil {
			return err
		}
	}
	if len(buttons) > 0 {
		if text == "" {
			text = "…"
		}
		action := map[string]any{}
		if short {
			list := []map[string]any{}
			for _, b := range buttons {
				list = append(list, map[string]any{"type": "reply", "reply": map[string]string{"id": viberCut(b, 256), "title": b}})
			}
			action["buttons"] = list
		} else {
			rows := []map[string]string{}
			for _, b := range buttons {
				rows = append(rows, map[string]string{"id": viberCut(b, 200), "title": b})
			}
			action["button"] = viberCut(s.tr("ol.wa_menu"), 20)
			action["sections"] = []map[string]any{{"rows": rows}}
		}
		kind := map[bool]string{true: "button", false: "list"}[short]
		return send("interactive", map[string]any{"type": kind, "body": map[string]string{"text": viberCut(text, 1024)}, "action": action})
	}
	if text == "" {
		return nil
	}
	return send("text", map[string]any{"body": viberCut(text, 4096), "preview_url": true})
}

// RunWhatsApp sends the answers to the WhatsApp chats until ctx ends (go24-worker); the clients' messages come by the
// webhook.
func (s *Store) RunWhatsApp(ctx context.Context, log *slog.Logger) {
	for {
		if _, err := s.SendWhatsApp(ctx); err != nil && ctx.Err() == nil {
			log.Error("open lines: whatsapp send", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}
