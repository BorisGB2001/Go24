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
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The channel Avito of a line: the company's Avito account (an application of developers.avito.ru with
// the Messenger API: its client id and secret) is a way into the line — buyers' messages about the company's ads become
// the line's dialogues (the queue, automatic messages, the CRM lead), the ad the buyer asks about is shown in the
// dialogue. go24 asks the API for the chats itself (no public address needed) and marks the chats it took read; the
// operators' answers and pictures go back to the buyer's chat, other files as their links in go24, the chat bot's menu
// as its numbered items (Avito has no buttons). Chats older than the connection are not taken.

// maxAvitoFile is the largest buyer's file go24 takes from Avito.
const maxAvitoFile = 50 << 20

// AvitoAccount is the Avito account of a line as its settings show it.
type AvitoAccount struct {
	ID         int64  // the account's id in Avito (0 — none)
	Name, Link string // its name and profile page
	Error      string // what went wrong the last time
}

// avitoVisitor is the visitor's token of an Avito chat in a line: only go24 can make it.
func (s *Store) avitoVisitor(widgetID, chat string) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines avito\x00"), s.Key...))
	m.Write([]byte(widgetID + ":" + chat))
	return "av" + hex.EncodeToString(m.Sum(nil))
}

// ---------- the API ----------

// avitoError is an answer of the API that is not 200: its HTTP status (401 — the token is bad, 403, 404 — the chat is
// closed or no more) and message.
type avitoError struct {
	Status  int
	Message string
}

func (e *avitoError) Error() string { return "avito: " + strconv.Itoa(e.Status) + " " + e.Message }

// avitoGone: the buyer's chat is closed or no more — the message will never go.
func avitoGone(err error) bool {
	var ae *avitoError
	return errors.As(err, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusNotFound)
}

// avitoAccess is an application's access token and till when it is good.
type avitoAccess struct {
	token string
	until time.Time
}

// avitoTokens are the access tokens got (the API's address and the client id → avitoAccess): a token lives a day.
var avitoTokens sync.Map

// avitoAuth is the application's access token (client_credentials): the one got before while it is good, unless fresh.
func (s *Store) avitoAuth(ctx context.Context, clientID, secret string, fresh bool) (string, error) {
	key := s.AvitoAPI + "\x00" + clientID
	if v, ok := avitoTokens.Load(key); ok && !fresh && time.Now().Before(v.(avitoAccess).until) {
		return v.(avitoAccess).token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.AvitoAPI+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := tgShort.Do(req)
	if err != nil {
		return "", errors.New("avito token: " + strings.ReplaceAll(err.Error(), secret, "***"))
	}
	defer res.Body.Close()
	var r struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
		Error   string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&r)
	if res.StatusCode != http.StatusOK || r.Token == "" {
		return "", &avitoError{http.StatusUnauthorized, strings.TrimSpace("token " + r.Error)}
	}
	if r.Expires <= 0 {
		r.Expires = 3600
	}
	avitoTokens.Store(key, avitoAccess{r.Token, time.Now().Add(time.Duration(r.Expires)*time.Second - time.Minute)})
	return r.Token, nil
}

// avitoAcc is a connected account: its line, the application's keys, go24's address for the files' links.
type avitoAcc struct {
	widget   Widget
	user     int64
	clientID string
	secret   string
	base     string
	since    int64
}

// avitoCredentials are the application's client id and secret, sealed together.
func avitoCredentials(plain string) (string, string) {
	id, secret, _ := strings.Cut(plain, "\n")
	return id, secret
}

// call asks the API (the access token in the Authorization header; a rejected token is got anew once): body — JSON, or a
// *multipart.Writer's form with its bytes; out — where the answer goes.
func (a avitoAcc) call(ctx context.Context, s *Store, method, path string, q url.Values, body any, out any) error {
	u := s.AvitoAPI + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	for try := 0; ; try++ {
		token, err := s.avitoAuth(ctx, a.clientID, a.secret, try > 0)
		if err != nil {
			return err
		}
		var rd io.Reader = http.NoBody
		ct := ""
		switch b := body.(type) {
		case nil:
		case avitoForm:
			rd, ct = bytes.NewReader(b.data), b.contentType
		default:
			raw, err := json.Marshal(b)
			if err != nil {
				return err
			}
			rd, ct = bytes.NewReader(raw), "application/json"
		}
		req, err := http.NewRequestWithContext(ctx, method, u, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		res, err := tgShort.Do(req)
		if err != nil {
			return errors.New("avito " + path + ": " + strings.ReplaceAll(err.Error(), token, "***"))
		}
		all, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
		res.Body.Close()
		if err != nil {
			return err
		}
		if res.StatusCode == http.StatusUnauthorized && try == 0 {
			continue
		}
		if res.StatusCode != http.StatusOK {
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(all, &e)
			return &avitoError{res.StatusCode, strings.TrimSpace(e.Error.Message + " " + e.Message)}
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(all, out)
	}
}

// avitoForm is a multipart form to send.
type avitoForm struct {
	data        []byte
	contentType string
}

// ---------- the line's settings ----------

// Avito is the line's Avito account.
func (s *Store) Avito(ctx context.Context, widgetID string) (AvitoAccount, error) {
	var a AvitoAccount
	var id *int64
	err := s.db.QueryRow(ctx, `SELECT avito_user, avito_name, avito_link, avito_error FROM ol_widgets WHERE id::text = $1`, widgetID).
		Scan(&id, &a.Name, &a.Link, &a.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if id != nil {
		a.ID = *id
	}
	return a, err
}

// avitoLinkRe: the account's profile page Avito gives, for «Write on Avito».
var avitoLinkRe = regexp.MustCompile(`^https://(www\.|m\.)?avito\.ru/[\w./?=&%-]+$`)

// ConnectAvito makes the Avito account of the application with these client id and secret the line's way in
// (administrators; checked by the web layer): the keys are checked with Avito, the account is read there; an account
// serves one line. base — go24's public address for the links to the operators' files.
func (s *Store) ConnectAvito(ctx context.Context, widgetID, clientID, secret, base string) (string, error) {
	clientID, secret = strings.TrimSpace(clientID), strings.TrimSpace(secret)
	if clientID == "" || secret == "" || len(clientID) > 200 || len(secret) > 200 || strings.ContainsAny(clientID+secret, "\r\n ") {
		return "", i18n.E("err.ol_avito_key")
	}
	a := avitoAcc{clientID: clientID, secret: secret}
	var me struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		Profile string `json:"profile_url"`
	}
	if err := a.call(ctx, s, http.MethodGet, "/core/v1/accounts/self", nil, nil, &me); err != nil || me.ID == 0 {
		return "", i18n.E("err.ol_avito_key")
	}
	if me.Name == "" {
		me.Name = strconv.FormatInt(me.ID, 10)
	}
	if !avitoLinkRe.MatchString(me.Profile) {
		me.Profile = ""
	}
	var other string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE avito_user = $1 AND id::text <> $2`, me.ID, widgetID).Scan(&other)
	if err == nil {
		return "", i18n.E("err.ol_tg_taken", other)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	sealed, err := s.seal(clientID + "\n" + secret)
	if err != nil {
		return "", err
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET avito_secret = $2, avito_user = $3, avito_name = left($4, 200), avito_link = $5,
		avito_base = $6, avito_since = $7, avito_error = '' WHERE id::text = $1`,
		widgetID, sealed, me.ID, me.Name, me.Profile, strings.TrimRight(base, "/"), time.Now().Unix())
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return me.Name, err
}

// DisconnectAvito: the account no longer serves the line (its dialogues stay).
func (s *Store) DisconnectAvito(ctx context.Context, widgetID string) error {
	_, err := s.db.Exec(ctx, `UPDATE ol_widgets SET avito_secret = NULL, avito_user = NULL, avito_name = '', avito_link = '',
		avito_base = '', avito_since = 0, avito_error = '' WHERE id::text = $1`, widgetID)
	return err
}

// ---------- receiving ----------

// avitoAccounts are the Avito accounts of the active lines.
func (s *Store) avitoAccounts(ctx context.Context) ([]avitoAcc, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, avito_secret, avito_user, avito_base, avito_since FROM ol_widgets
		WHERE active AND avito_secret IS NOT NULL AND avito_user IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID     string
		Sealed []byte
		User   int64
		Base   string
		Since  int64
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return nil, err
	}
	out := []avitoAcc{}
	for _, r := range list {
		plain, err := s.unseal(r.Sealed)
		if err != nil {
			continue // sealed with another secret: the administrators connect the account anew
		}
		w, err := s.Widget(ctx, r.ID)
		if err != nil {
			continue
		}
		id, secret := avitoCredentials(plain)
		out = append(out, avitoAcc{widget: w, user: r.User, clientID: id, secret: secret, base: r.Base, since: r.Since})
	}
	return out, nil
}

type avitoMessage struct {
	ID        string `json:"id"`
	Author    int64  `json:"author_id"`
	Created   int64  `json:"created"`
	Direction string `json:"direction"`
	Type      string `json:"type"`
	Content   struct {
		Text  string `json:"text"`
		Image *struct {
			Sizes map[string]string `json:"sizes"`
		} `json:"image"`
		Voice *struct {
			ID string `json:"voice_id"`
		} `json:"voice"`
		Link *struct {
			Text string `json:"text"`
			URL  string `json:"url"`
		} `json:"link"`
		Item *struct {
			Title string `json:"title"`
			URL   string `json:"item_url"`
		} `json:"item"`
		Location *struct {
			Title string  `json:"title"`
			Text  string  `json:"text"`
			Lat   float64 `json:"lat"`
			Lon   float64 `json:"lon"`
		} `json:"location"`
	} `json:"content"`
}

type avitoChat struct {
	ID      string `json:"id"`
	Context struct {
		Type  string `json:"type"`
		Value struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"value"`
	} `json:"context"`
	Users []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"users"`
	Last *avitoMessage `json:"last_message"`
}

// PollAvito takes the buyers' new messages of every line's Avito account once; it is how many accounts there are.
func (s *Store) PollAvito(ctx context.Context) (int, error) {
	accs, err := s.avitoAccounts(ctx)
	if err != nil || len(accs) == 0 {
		return 0, err
	}
	var errs []error
	for _, a := range accs {
		err := s.pollAvito(ctx, a)
		msg := ""
		if err != nil {
			msg = err.Error()
			errs = append(errs, err)
		}
		if ctx.Err() == nil {
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET avito_error = left($2, 500) WHERE id::text = $1`, a.widget.ID, msg)
		}
	}
	return len(accs), errors.Join(errs...)
}

// pollAvito looks through the account's latest chats: a chat whose last message is the buyer's and newer than those
// taken gets its new messages taken and is marked read.
func (s *Store) pollAvito(ctx context.Context, a avitoAcc) error {
	var r struct {
		Chats []avitoChat `json:"chats"`
	}
	acc := "/messenger/v2/accounts/" + strconv.FormatInt(a.user, 10)
	if err := a.call(ctx, s, http.MethodGet, acc+"/chats", url.Values{"limit": {"50"}, "chat_types": {"u2i,u2u"}}, nil, &r); err != nil {
		return err
	}
	var failed error
	for _, c := range r.Chats {
		if c.ID == "" || c.Last == nil || c.Last.Direction != "in" || c.Last.Created <= a.since {
			continue
		}
		token := s.avitoVisitor(a.widget.ID, c.ID)
		seen, known := a.since, true
		err := s.db.QueryRow(ctx, `SELECT avito_seen FROM ol_sessions WHERE token_hash = $1`, hash(token)).Scan(&seen)
		if errors.Is(err, pgx.ErrNoRows) {
			seen, known, err = a.since, false, nil
		}
		if err != nil {
			return err
		}
		if c.Last.Created <= seen {
			continue
		}
		if err := s.avitoChat(ctx, a, c, token, seen, known); err != nil {
			failed = err // the next chats are taken all the same
		}
	}
	return failed
}

// avitoChat takes the buyer's messages of the chat newer than seen into the chat's dialogue (a new one — with the ad
// the buyer asks about).
func (s *Store) avitoChat(ctx context.Context, a avitoAcc, c avitoChat, token string, seen int64, known bool) error {
	user := strconv.FormatInt(a.user, 10)
	var raw json.RawMessage
	if err := a.call(ctx, s, http.MethodGet, "/messenger/v3/accounts/"+user+"/chats/"+url.PathEscape(c.ID)+"/messages/",
		url.Values{"limit": {"50"}}, nil, &raw); err != nil {
		return err
	}
	var msgs []avitoMessage // the API answers with the list or with {"messages": […]}
	if json.Unmarshal(raw, &msgs) != nil {
		var r struct {
			Messages []avitoMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		msgs = r.Messages
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Created < msgs[j].Created })
	name, buyer := "", ""
	for _, u := range c.Users {
		if u.ID != a.user {
			name, buyer = strings.TrimSpace(u.Name), strconv.FormatInt(u.ID, 10)
			break
		}
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, channel, avito_chat, avito_user, avito_seen)
		VALUES ($1, $2, left($3, 200), 'avito', $4, $5, $6)
		ON CONFLICT (token_hash) DO UPDATE SET name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END`,
		a.widget.ID, hash(token), name, c.ID, buyer, seen); err != nil {
		return err
	}
	ad := "" // the ad the buyer asks about: with the first message of a new dialogue
	if v := c.Context.Value; !known && c.Context.Type == "item" && v.Title != "" {
		ad = strings.TrimSpace("🏷 " + v.Title + " " + v.URL)
	}
	last := seen
	for _, m := range msgs {
		if m.Direction != "in" || m.Created <= seen || m.Type == "system" || m.Type == "deleted" {
			continue
		}
		text, f := s.avitoContent(ctx, a, m)
		if ad != "" && (text != "" || f != nil) {
			text, ad = strings.TrimSpace(ad+"\n"+text), ""
		}
		if r := []rune(text); len(r) > 5000 {
			text = string(r[:5000])
		}
		if text != "" || f != nil {
			if err := s.VisitorSend(ctx, a.widget.ID, token, text, f); err != nil {
				return err
			}
		}
		last = max(last, m.Created)
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET avito_seen = $2 WHERE token_hash = $1 AND avito_seen < $2`, hash(token), last); err != nil {
			return err
		}
	}
	if last == seen { // nothing new after all: the chat is not looked at again till its next message
		last = c.Last.Created
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET avito_seen = $2 WHERE token_hash = $1 AND avito_seen < $2`, hash(token), last); err != nil {
			return err
		}
	}
	return a.call(ctx, s, http.MethodPost, "/messenger/v1/accounts/"+user+"/chats/"+url.PathEscape(c.ID)+"/read", nil, nil, nil)
}

// avitoContent is a buyer's message as go24 shows it: its text and its file (a photo, a voice message).
func (s *Store) avitoContent(ctx context.Context, a avitoAcc, m avitoMessage) (string, *File) {
	c := m.Content
	text := strings.TrimSpace(c.Text)
	switch {
	case c.Image != nil:
		if f, err := s.avitoDownload(ctx, avitoBiggest(c.Image.Sizes), "photo.jpg", ""); err == nil {
			return text, f
		}
		return strings.TrimSpace(text + "\n📎 photo.jpg"), nil
	case c.Voice != nil && c.Voice.ID != "":
		var r struct {
			URLs map[string]string `json:"voices_urls"`
		}
		err := a.call(ctx, s, http.MethodGet, "/messenger/v1/accounts/"+strconv.FormatInt(a.user, 10)+"/getVoiceFiles",
			url.Values{"voice_ids": {c.Voice.ID}}, nil, &r)
		if err == nil {
			if f, err := s.avitoDownload(ctx, r.URLs[c.Voice.ID], "voice.m4a", "audio/mp4"); err == nil {
				return text, f
			}
		}
		return strings.TrimSpace(text + "\n📎 voice.m4a"), nil
	case c.Link != nil:
		return strings.TrimSpace(text + "\n" + strings.TrimSpace(c.Link.Text+" "+c.Link.URL)), nil
	case c.Item != nil:
		return strings.TrimSpace(text + "\n🏷 " + strings.TrimSpace(c.Item.Title+" "+c.Item.URL)), nil
	case c.Location != nil:
		return strings.TrimSpace(text + "\n📍 " + strings.TrimSpace(c.Location.Title+" "+c.Location.Text) + " https://maps.google.com/?q=" +
			strconv.FormatFloat(c.Location.Lat, 'f', 6, 64) + "," + strconv.FormatFloat(c.Location.Lon, 'f', 6, 64)), nil
	}
	return text, nil
}

// avitoBiggest is the link to the biggest size of a photo («1280x960» → its link).
func avitoBiggest(sizes map[string]string) string {
	best, area := "", -1
	for k, v := range sizes {
		w, h, _ := strings.Cut(k, "x")
		x, _ := strconv.Atoi(w)
		y, _ := strconv.Atoi(h)
		if x*y > area || x*y == area && v < best {
			best, area = v, x*y
		}
	}
	return best
}

// avitoDownload takes a buyer's file from Avito (its link) into the built-in storage.
func (s *Store) avitoDownload(ctx context.Context, link, name, ct string) (*File, error) {
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
		return nil, errors.New("avito file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxAvitoFile+1))
	if err != nil || len(data) == 0 || len(data) > maxAvitoFile {
		return nil, i18n.E("err.ol_file")
	}
	return s.keepFile(ctx, name, ct, data)
}

// ---------- sending ----------

// SendAvito sends to the Avito chats the operators' answers and the line's messages not sent yet; it is how many went.
func (s *Store) SendAvito(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.avito_chat, w.id::text, w.avito_secret, w.avito_user, w.avito_base, coalesce(u.name, ''),
			m.body, m.file_key, m.file_name, m.file_type, m.file_secret, m.buttons
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'avito' AND w.avito_secret IS NOT NULL AND w.avito_user IS NOT NULL AND m.id > s.avito_sent
			AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
		ORDER BY m.id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type out struct {
		ID                          int64
		Session, Chat, Widget       string
		Sealed                      []byte
		User                        int64
		Base, Operator, Body        string
		FileKey, FileName, FileType string
		Secret                      string
		Buttons                     []string
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
			id, secret := avitoCredentials(plain)
			a := avitoAcc{user: m.User, clientID: id, secret: secret, base: m.Base}
			link := ""
			if m.FileName != "" && m.Base != "" {
				link = FileLink(m.Base, m.Widget, Message{ID: m.ID, FileSecret: m.Secret})
			}
			err = s.avitoSend(ctx, a, m.Chat, m.Operator, m.Body, m.FileKey, m.FileName, m.FileType, link, m.Buttons)
		}
		if err != nil && !avitoGone(err) { // the chat is closed or no more: never will be sent
			stopped[m.Session] = true
			errs = append(errs, err)
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET avito_error = left($2, 500) WHERE id::text = $1`, m.Widget, err.Error())
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET avito_sent = $2 WHERE id::text = $1 AND avito_sent < $2`, m.Session, m.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// avitoSend writes a message to the buyer's chat: the operator's name, the text, the chat bot's menu as its numbered
// items (the buyer answers with the number); a picture goes after the text as Avito's image, another file as its link.
func (s *Store) avitoSend(ctx context.Context, a avitoAcc, chat, operator, body, fileKey, fileName, fileType, link string, buttons []string) error {
	text := body
	if operator != "" {
		text = operator + ":"
		if body != "" {
			text += "\n" + body
		}
	}
	for _, b := range buttons {
		text = strings.TrimSpace(text + "\n" + b)
	}
	image := ""
	if fileKey != "" {
		var err error
		if strings.HasPrefix(fileType, "image/") {
			if image, err = s.avitoUpload(ctx, a, fileKey, fileName); err != nil {
				return err
			}
		}
		if image == "" {
			text = strings.TrimSpace(text + "\n📎 " + strings.TrimSpace(fileName+" "+link))
		}
	}
	path := "/messenger/v1/accounts/" + strconv.FormatInt(a.user, 10) + "/chats/" + url.PathEscape(chat) + "/messages"
	if image != "" && text == operator+":" { // only the picture: the operator's name goes with it all the same
		text = operator
	}
	if text != "" {
		if r := []rune(text); len(r) > 4000 {
			text = string(r[:4000])
		}
		if err := a.call(ctx, s, http.MethodPost, path, nil, map[string]any{"message": map[string]string{"text": text}, "type": "text"}, nil); err != nil {
			return err
		}
	}
	if image == "" {
		return nil
	}
	return a.call(ctx, s, http.MethodPost, path+"/image", nil, map[string]string{"image_id": image}, nil)
}

// avitoUpload puts a picture into Avito (uploadImages) and is its id there; "" — there is no storage to read it from.
func (s *Store) avitoUpload(ctx context.Context, a avitoAcc, fileKey, fileName string) (string, error) {
	if s.OpenFile == nil {
		return "", nil
	}
	obj, err := s.OpenFile(ctx, fileKey)
	if err != nil {
		return "", err
	}
	defer obj.Close()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("uploadfile[]", fileName)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, io.LimitReader(obj, maxAvitoFile)); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	var got map[string]json.RawMessage // the image's id → the links of its sizes
	if err := a.call(ctx, s, http.MethodPost, "/messenger/v1/accounts/"+strconv.FormatInt(a.user, 10)+"/uploadImages", nil,
		avitoForm{buf.Bytes(), w.FormDataContentType()}, &got); err != nil {
		return "", err
	}
	for id := range got {
		return id, nil
	}
	return "", errors.New("avito upload: no image")
}

// RunAvito takes the buyers' messages from the Avito accounts and sends the answers until ctx ends (go24-worker).
func (s *Store) RunAvito(ctx context.Context, log *slog.Logger) {
	go func() {
		for {
			if _, err := s.SendAvito(ctx); err != nil && ctx.Err() == nil {
				log.Error("open lines: avito send", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	for ctx.Err() == nil {
		n, err := s.PollAvito(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error("open lines: avito", "err", err)
		}
		pause := 10 * time.Second // Avito does not hold the request till a message comes
		if n == 0 {
			pause = 30 * time.Second // no accounts yet
		}
		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
}
