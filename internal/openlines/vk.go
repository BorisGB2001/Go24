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

// The channel VK of a line: the company's VK community (its messages) is a way into the line, as the
// Telegram bot is. go24 reads the community's events through the Bots Long Poll API (so it needs no public address): a
// client's message becomes the line's dialogue (the queue, automatic messages, the CRM lead); the operators' answers,
// files and the chat bot's buttons go back to the client's chat with the community. The «Start» button gets the line's
// greeting.

// vkVersion is the VK API version go24 speaks.
const vkVersion = "5.199"

// maxVKFile is the largest client's file go24 takes from VK.
const maxVKFile = 50 << 20

// vkChats is where VK's peer ids of group chats begin: a group chat is not the line's.
const vkChats = 2000000000

// VKGroup is the VK community of a line as its settings show it.
type VKGroup struct {
	Name, Screen, Error string // the community's name and short address in VK (""  — none); what went wrong the last time
}

// vkToken is the visitor's token of a VK chat in a line: only go24 can make it.
func (s *Store) vkToken(widgetID string, peer int64) string {
	m := hmac.New(sha256.New, append([]byte("go24 openlines vk\x00"), s.Key...))
	m.Write([]byte(widgetID + ":" + strconv.FormatInt(peer, 10)))
	return "vk" + hex.EncodeToString(m.Sum(nil))
}

// ---------- the API ----------

// vkError is an error the API answered with: its code (5 — the key is bad, 901 — the client has not allowed the
// community's messages…) and text.
type vkError struct {
	Code    int    `json:"error_code"`
	Message string `json:"error_msg"`
}

func (e *vkError) Error() string { return "vk: " + strconv.Itoa(e.Code) + " " + e.Message }

// vkGone: the client blocked the community or does not let it write — the message will never go.
func vkGone(err error) bool {
	var ve *vkError
	return errors.As(err, &ve) && (ve.Code == 900 || ve.Code == 901 || ve.Code == 902)
}

// vkCall calls the API's method with the community's key and decodes its answer («response») into out.
func (s *Store) vkCall(ctx context.Context, token, method string, args url.Values, out any) error {
	if args == nil {
		args = url.Values{}
	}
	args.Set("access_token", token)
	args.Set("v", vkVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.VKAPI+"/method/"+method, strings.NewReader(args.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := tgShort.Do(req)
	if err != nil {
		return errors.New("vk " + method + ": " + strings.ReplaceAll(err.Error(), token, "***"))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("vk " + method + ": " + res.Status)
	}
	var r struct {
		Response json.RawMessage `json:"response"`
		Error    *vkError        `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return err
	}
	if r.Error != nil {
		return r.Error
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Response, out)
}

// vkPost sends a file to an upload address VK gave (field — the form's field) and decodes the answer into out.
func vkPost(ctx context.Context, address, field, name string, data []byte, out any) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile(field, name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := tgShort.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("vk upload: " + res.Status)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// vkTS is the long poll's «ts»: VK writes it as a string or as a number.
type vkTS string

func (t *vkTS) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*t = vkTS(n.String())
		return nil
	}
	var str string
	err := json.Unmarshal(b, &str)
	*t = vkTS(str)
	return err
}

type vkServer struct {
	Server string `json:"server"`
	Key    string `json:"key"`
	TS     vkTS   `json:"ts"`
}

// vkLongPoll asks VK for the community's long poll server (its address, key and the current ts).
func (s *Store) vkLongPoll(ctx context.Context, token string, group int64) (vkServer, error) {
	var lp vkServer
	err := s.vkCall(ctx, token, "groups.getLongPollServer", url.Values{"group_id": {strconv.FormatInt(group, 10)}}, &lp)
	if err == nil && (lp.Server == "" || lp.Key == "") {
		err = errors.New("vk: no long poll server")
	}
	return lp, err
}

// ---------- the line's settings ----------

// VK is the line's VK community.
func (s *Store) VK(ctx context.Context, widgetID string) (VKGroup, error) {
	var g VKGroup
	err := s.db.QueryRow(ctx, `SELECT vk_name, vk_screen, vk_error FROM ol_widgets WHERE id::text = $1`, widgetID).Scan(&g.Name, &g.Screen, &g.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// ConnectVK makes the VK community with this access key the line's way in (administrators; checked by the web layer):
// the key is checked with VK, the community's name is read there, its long poll is switched on for the messages; a
// community serves one line.
func (s *Store) ConnectVK(ctx context.Context, widgetID, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 300 || strings.ContainsAny(token, "\r\n ") {
		return "", i18n.E("err.ol_vk_token")
	}
	type group struct {
		ID     int64  `json:"id"`
		Name   string `json:"name"`
		Screen string `json:"screen_name"`
	}
	var raw json.RawMessage
	if err := s.vkCall(ctx, token, "groups.getById", nil, &raw); err != nil {
		return "", i18n.E("err.ol_vk_token")
	}
	var groups struct {
		Groups []group `json:"groups"`
	}
	if json.Unmarshal(raw, &groups) != nil { // the API's older answer: a list
		_ = json.Unmarshal(raw, &groups.Groups)
	}
	if len(groups.Groups) == 0 || groups.Groups[0].ID == 0 {
		return "", i18n.E("err.ol_vk_token") // a user's key, not a community's
	}
	g := groups.Groups[0]
	if g.Screen == "" {
		g.Screen = "club" + strconv.FormatInt(g.ID, 10)
	}
	var other string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_widgets WHERE vk_group = $1 AND id::text <> $2`, g.ID, widgetID).Scan(&other)
	if err == nil {
		return "", i18n.E("err.ol_tg_taken", other)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	// the long poll for the new messages, in go24's version of the API (a key without the right «manage» cannot: then
	// the administrators switch it on in the community's settings themselves)
	_ = s.vkCall(ctx, token, "groups.setLongPollSettings", url.Values{"group_id": {strconv.FormatInt(g.ID, 10)},
		"enabled": {"1"}, "api_version": {vkVersion}, "message_new": {"1"}}, nil)
	lp, err := s.vkLongPoll(ctx, token, g.ID)
	if err != nil {
		return "", i18n.E("err.ol_vk_longpoll")
	}
	sealed, err := s.seal(token)
	if err != nil {
		return "", err
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_widgets SET vk_secret = $2, vk_group = $3, vk_name = left($4, 200), vk_screen = $5,
		vk_server = $6, vk_key = $7, vk_ts = $8, vk_error = '' WHERE id::text = $1`,
		widgetID, sealed, g.ID, g.Name, g.Screen, lp.Server, lp.Key, string(lp.TS))
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return g.Name, err
}

// DisconnectVK: the community no longer serves the line (its dialogues stay).
func (s *Store) DisconnectVK(ctx context.Context, widgetID string) error {
	_, err := s.db.Exec(ctx, `UPDATE ol_widgets SET vk_secret = NULL, vk_group = NULL, vk_name = '', vk_screen = '', vk_server = '',
		vk_key = '', vk_ts = '', vk_error = '' WHERE id::text = $1`, widgetID)
	return err
}

// ---------- receiving ----------

type vkBot struct {
	widget Widget
	token  string
	group  int64
	lp     vkServer
}

// vkBots are the VK communities of the active lines.
func (s *Store) vkBots(ctx context.Context) ([]vkBot, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, vk_secret, vk_group, vk_server, vk_key, vk_ts FROM ol_widgets
		WHERE active AND vk_secret IS NOT NULL AND vk_group IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	type row struct {
		ID              string
		Sealed          []byte
		Group           int64
		Server, Key, TS string
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return nil, err
	}
	out := []vkBot{}
	for _, r := range list {
		token, err := s.unseal(r.Sealed)
		if err != nil {
			continue // sealed with another secret: the administrators connect the community anew
		}
		w, err := s.Widget(ctx, r.ID)
		if err != nil {
			continue
		}
		out = append(out, vkBot{widget: w, token: token, group: r.Group, lp: vkServer{r.Server, r.Key, vkTS(r.TS)}})
	}
	return out, nil
}

type vkSize struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// vkLargest is the largest of a picture's sizes.
func vkLargest(sizes []vkSize) string {
	best, area := "", -1
	for _, z := range sizes {
		if z.URL != "" && z.Width*z.Height > area {
			best, area = z.URL, z.Width*z.Height
		}
	}
	return best
}

type vkAttachment struct {
	Type  string `json:"type"`
	Photo *struct {
		Sizes []vkSize `json:"sizes"`
	} `json:"photo"`
	Doc *struct {
		Title string `json:"title"`
		Ext   string `json:"ext"`
		Size  int64  `json:"size"`
		URL   string `json:"url"`
	} `json:"doc"`
	Sticker *struct {
		Images []vkSize `json:"images"`
	} `json:"sticker"`
	Voice *struct {
		MP3 string `json:"link_mp3"`
	} `json:"audio_message"`
	Video *struct {
		Title string `json:"title"`
	} `json:"video"`
}

// file is where the attachment's file is and its name ("" — VK gives no file to take: a video, a music track…).
func (a vkAttachment) file() (link, name string, size int64) {
	switch {
	case a.Type == "photo" && a.Photo != nil:
		return vkLargest(a.Photo.Sizes), "photo.jpg", 0
	case a.Type == "doc" && a.Doc != nil:
		name = a.Doc.Title
		if a.Doc.Ext != "" && !strings.HasSuffix(strings.ToLower(name), "."+strings.ToLower(a.Doc.Ext)) {
			name += "." + a.Doc.Ext
		}
		return a.Doc.URL, name, a.Doc.Size
	case a.Type == "sticker" && a.Sticker != nil:
		return vkLargest(a.Sticker.Images), "sticker", 0
	case a.Type == "audio_message" && a.Voice != nil:
		return a.Voice.MP3, "voice.mp3", 0
	}
	return "", "", 0
}

// title is how the message tells of an attachment go24 does not take.
func (a vkAttachment) title() string {
	if a.Type == "video" && a.Video != nil && a.Video.Title != "" {
		return a.Video.Title
	}
	return a.Type
}

type vkMessage struct {
	From        int64          `json:"from_id"`
	Peer        int64          `json:"peer_id"`
	Text        string         `json:"text"`
	Payload     string         `json:"payload"`
	Attachments []vkAttachment `json:"attachments"`
}

type vkUpdate struct {
	Type   string `json:"type"`
	Object struct {
		Message *vkMessage `json:"message"`
	} `json:"object"`
}

// PollVK reads the new events of every line's VK community once, waiting up to wait for them; it is how many
// communities there are.
func (s *Store) PollVK(ctx context.Context, wait time.Duration) (int, error) {
	bots, err := s.vkBots(ctx)
	if err != nil || len(bots) == 0 {
		return 0, err
	}
	var wg sync.WaitGroup
	errs := make([]error, len(bots))
	for i, b := range bots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.pollVK(ctx, b, wait)
		}()
	}
	wg.Wait()
	return len(bots), errors.Join(errs...)
}

func (s *Store) pollVK(ctx context.Context, b vkBot, wait time.Duration) error {
	fail := func(err error) error {
		if ctx.Err() == nil {
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET vk_error = left($2, 500) WHERE id::text = $1`, b.widget.ID, err.Error())
		}
		return err
	}
	if b.lp.Server == "" || b.lp.Key == "" { // the server not known yet or its key expired
		lp, err := s.vkLongPoll(ctx, b.token, b.group)
		if err != nil {
			return fail(err)
		}
		if b.lp.TS != "" {
			lp.TS = b.lp.TS // the events not read yet are still read
		}
		b.lp = lp
	}
	q := url.Values{"act": {"a_check"}, "key": {b.lp.Key}, "ts": {string(b.lp.TS)}, "wait": {strconv.Itoa(int(wait.Seconds()))}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.lp.Server+"?"+q.Encode(), nil)
	if err != nil {
		return fail(err)
	}
	res, err := (&http.Client{Timeout: wait + 30*time.Second}).Do(req)
	if err != nil {
		return fail(errors.New("vk long poll: " + strings.ReplaceAll(err.Error(), b.lp.Key, "***")))
	}
	var r struct {
		TS      vkTS       `json:"ts"`
		Failed  int        `json:"failed"`
		Updates []vkUpdate `json:"updates"`
	}
	err = json.NewDecoder(res.Body).Decode(&r)
	res.Body.Close()
	if err != nil {
		return fail(errors.New("vk long poll: " + res.Status))
	}
	var failed error
	switch r.Failed {
	case 0:
		for _, u := range r.Updates {
			if err := s.vkReceive(ctx, b, u); err != nil {
				failed = err // the event is lost rather than read again and again
			}
		}
		b.lp.TS = r.TS
	case 1: // events were lost: go on from the ts VK gave
		b.lp.TS = r.TS
	case 2: // the key expired: a new one next time
		b.lp.Key = ""
	default: // the server's data lost: a new key and ts next time
		b.lp.Key, b.lp.TS = "", ""
	}
	msg := ""
	if failed != nil {
		msg = failed.Error()
	}
	_, err = s.db.Exec(ctx, `UPDATE ol_widgets SET vk_server = $2, vk_key = $3, vk_ts = $4, vk_error = left($5, 500) WHERE id::text = $1`,
		b.widget.ID, b.lp.Server, b.lp.Key, string(b.lp.TS), msg)
	return errors.Join(failed, err)
}

// vkReceive makes a client's message the line's dialogue's message (the chat's dialogue, or a new one); the «Start»
// button gets the greeting.
func (s *Store) vkReceive(ctx context.Context, b vkBot, u vkUpdate) error {
	m := u.Object.Message
	if u.Type != "message_new" || m == nil || m.From <= 0 || m.Peer <= 0 || m.Peer >= vkChats { // a group chat's message is not the line's
		return nil
	}
	text := strings.TrimSpace(m.Text)
	if strings.Contains(m.Payload, `"command":"start"`) { // the «Start» button of the community's chat
		if b.widget.Greeting == "" {
			return nil
		}
		return s.vkCall(ctx, b.token, "messages.send", url.Values{"peer_id": {strconv.FormatInt(m.Peer, 10)},
			"random_id": {"0"}, "message": {b.widget.Greeting}}, nil)
	}
	token := s.vkToken(b.widget.ID, m.Peer)
	// the client's name as VK has it, asked once a dialogue
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM ol_sessions WHERE token_hash = $1`, hash(token)).Scan(&name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if name == "" {
		var users []struct {
			First string `json:"first_name"`
			Last  string `json:"last_name"`
		}
		if s.vkCall(ctx, b.token, "users.get", url.Values{"user_ids": {strconv.FormatInt(m.From, 10)}}, &users) == nil && len(users) > 0 {
			name = strings.TrimSpace(users[0].First + " " + users[0].Last)
		}
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, name, channel, vk_peer, vk_user)
		VALUES ($1, $2, left($3, 200), 'vk', $4, $5)
		ON CONFLICT (token_hash) DO UPDATE SET vk_user = EXCLUDED.vk_user,
			name = CASE WHEN ol_sessions.name = '' THEN EXCLUDED.name ELSE ol_sessions.name END`,
		b.widget.ID, hash(token), name, m.Peer, strconv.FormatInt(m.From, 10)); err != nil {
		return err
	}
	var f *File
	for _, a := range m.Attachments {
		link, fname, size := a.file()
		if link == "" {
			text = strings.TrimSpace(text + "\n📎 " + a.title())
			continue
		}
		if f != nil {
			continue // one file a message in go24
		}
		if f, err = s.vkDownload(ctx, link, fname, size); err != nil {
			f = nil
			text = strings.TrimSpace(text + "\n📎 " + fname)
		}
	}
	if r := []rune(text); len(r) > 5000 {
		text = string(r[:5000])
	}
	if text == "" && f == nil {
		return nil
	}
	return s.VisitorSend(ctx, b.widget.ID, token, text, f)
}

// vkDownload takes a client's file from VK (its link) into the built-in storage.
func (s *Store) vkDownload(ctx context.Context, link, name string, size int64) (*File, error) {
	if s.PutFile == nil || size > maxVKFile {
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
		return nil, errors.New("vk file: " + res.Status)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxVKFile+1))
	if err != nil || len(data) > maxVKFile {
		return nil, i18n.E("err.ol_file")
	}
	ct := ""
	if name == "sticker" { // a sticker is a picture: its type and extension from its bytes
		ct = http.DetectContentType(data)
		ext := ".png"
		if list, _ := mime.ExtensionsByType(ct); len(list) > 0 {
			ext = list[0]
		}
		name += ext
	}
	return s.keepFile(ctx, name, ct, data)
}

// ---------- sending ----------

// SendVK sends to the VK chats the operators' answers and the line's messages not sent yet; it is how many went.
func (s *Store) SendVK(ctx context.Context) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id, s.id::text, s.vk_peer, w.id::text, w.vk_secret, coalesce(u.name, ''), m.body, m.file_key, m.file_name, m.file_type, m.buttons
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id JOIN ol_widgets w ON w.id = s.widget_id LEFT JOIN users u ON u.id = m.author_id
		WHERE s.channel = 'vk' AND w.vk_secret IS NOT NULL AND m.id > s.vk_sent AND m.kind NOT IN ('note', 'hidden') AND (m.author_id IS NOT NULL OR m.kind <> '')
		ORDER BY m.id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	type out struct {
		ID                                          int64
		Session                                     string
		Peer                                        int64
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
			err = s.vkSend(ctx, token, m.Peer, m.ID, m.Operator, m.Body, m.FileKey, m.FileName, m.FileType, m.Buttons)
		}
		if err != nil && !vkGone(err) { // blocked or not allowed: not sent, never will be
			stopped[m.Session] = true
			errs = append(errs, err)
			_, _ = s.db.Exec(ctx, `UPDATE ol_widgets SET vk_error = left($2, 500) WHERE id::text = $1`, m.Widget, err.Error())
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE ol_sessions SET vk_sent = $2 WHERE id::text = $1 AND vk_sent < $2`, m.Session, m.ID); err != nil {
			return sent, err
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// vkSend writes a message to the client's chat: the operator's name, the text; a file as a photo or a document with
// the text; the chat bot's menu as the message's keyboard (a button sends its text, as the client's message). id — the
// line's message: VK sends a message with the same random_id once.
func (s *Store) vkSend(ctx context.Context, token string, peer, id int64, operator, body, fileKey, fileName, fileType string, buttons []string) error {
	text := body
	if operator != "" {
		text = operator + ":"
		if body != "" {
			text += "\n" + body
		}
	}
	args := url.Values{"peer_id": {strconv.FormatInt(peer, 10)}, "random_id": {strconv.FormatInt(id&0x7fffffff, 10)}}
	if fileKey != "" {
		a, err := s.vkUpload(ctx, token, peer, fileKey, fileName, fileType)
		if err != nil {
			return err
		}
		if a == "" {
			text += "\n📎 " + fileName
		} else {
			args.Set("attachment", a)
		}
	}
	if len(buttons) > 0 {
		// in the message itself up to 6 buttons; more — the chat's keyboard, hidden after a press (up to 10)
		inline, limit := len(buttons) <= 6, 10
		rows := [][]map[string]any{}
		for i, b := range buttons {
			if i == limit {
				break
			}
			if r := []rune(b); len(r) > 40 {
				b = string(r[:40])
			}
			rows = append(rows, []map[string]any{{"action": map[string]string{"type": "text", "label": b}}})
		}
		kb, _ := json.Marshal(map[string]any{"inline": inline, "one_time": !inline, "buttons": rows})
		args.Set("keyboard", string(kb))
	}
	if r := []rune(text); len(r) > 4000 {
		text = string(r[:4000])
	}
	args.Set("message", text)
	return s.vkCall(ctx, token, "messages.send", args, nil)
}

// vkUpload puts the file into VK for the chat and is its attachment: a picture as a photo, the rest as a document; ""
// — there is no storage to read the file from.
func (s *Store) vkUpload(ctx context.Context, token string, peer int64, fileKey, fileName, fileType string) (string, error) {
	if s.OpenFile == nil {
		return "", nil
	}
	obj, err := s.OpenFile(ctx, fileKey)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(obj)
	obj.Close()
	if err != nil {
		return "", err
	}
	var slot struct {
		URL string `json:"upload_url"`
	}
	p := strconv.FormatInt(peer, 10)
	if (Message{FileType: fileType}).Image() {
		if err := s.vkCall(ctx, token, "photos.getMessagesUploadServer", url.Values{"peer_id": {p}}, &slot); err != nil {
			return "", err
		}
		var up struct {
			Server json.Number `json:"server"`
			Photo  string      `json:"photo"`
			Hash   string      `json:"hash"`
		}
		if err := vkPost(ctx, slot.URL, "photo", fileName, data, &up); err != nil {
			return "", err
		}
		var saved []struct {
			ID     int64  `json:"id"`
			Owner  int64  `json:"owner_id"`
			Access string `json:"access_key"`
		}
		if err := s.vkCall(ctx, token, "photos.saveMessagesPhoto", url.Values{"server": {up.Server.String()}, "photo": {up.Photo}, "hash": {up.Hash}}, &saved); err != nil {
			return "", err
		}
		if len(saved) == 0 {
			return "", errors.New("vk: the photo not saved")
		}
		a := "photo" + strconv.FormatInt(saved[0].Owner, 10) + "_" + strconv.FormatInt(saved[0].ID, 10)
		if saved[0].Access != "" {
			a += "_" + saved[0].Access
		}
		return a, nil
	}
	if err := s.vkCall(ctx, token, "docs.getMessagesUploadServer", url.Values{"type": {"doc"}, "peer_id": {p}}, &slot); err != nil {
		return "", err
	}
	var up struct {
		File  string `json:"file"`
		Error string `json:"error"`
	}
	if err := vkPost(ctx, slot.URL, "file", fileName, data, &up); err != nil {
		return "", err
	}
	if up.File == "" {
		return "", errors.New("vk upload: " + up.Error)
	}
	var saved struct {
		Doc struct {
			ID    int64 `json:"id"`
			Owner int64 `json:"owner_id"`
		} `json:"doc"`
	}
	if err := s.vkCall(ctx, token, "docs.save", url.Values{"file": {up.File}, "title": {fileName}}, &saved); err != nil {
		return "", err
	}
	return "doc" + strconv.FormatInt(saved.Doc.Owner, 10) + "_" + strconv.FormatInt(saved.Doc.ID, 10), nil
}

// RunVK reads the VK communities' messages and sends the answers until ctx ends (go24-worker).
func (s *Store) RunVK(ctx context.Context, log *slog.Logger) {
	go func() {
		for {
			if _, err := s.SendVK(ctx); err != nil && ctx.Err() == nil {
				log.Error("open lines: vk send", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}()
	for ctx.Err() == nil {
		n, err := s.PollVK(ctx, 25*time.Second)
		if err != nil && ctx.Err() == nil {
			log.Error("open lines: vk", "err", err)
		}
		pause := time.Duration(0)
		switch {
		case n == 0:
			pause = 30 * time.Second // no communities yet
		case err != nil:
			pause = 10 * time.Second
		}
		select {
		case <-ctx.Done():
		case <-time.After(pause):
		}
	}
}
