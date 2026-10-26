package notifyout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Matrix: a bot account on a Matrix server (go24's own or any other; the address is set with the account's access
// token), so that an employee gets the notifications on their own Matrix account, on whatever server, through the
// federation. The person starts a direct chat with the bot (it joins on the invitation) and writes it the code;
// go24 reads the bot's chats by /sync and writes the notifications there. A chat of more than two is not taken.
type Matrix struct {
	HTTP *http.Client
	now  func() time.Time // for tests

	mu    sync.Mutex
	since map[string]string // the bot (server + token) → the /sync position, kept while go24 runs
	txn   int64
}

func init() { Register(&Matrix{HTTP: &http.Client{Timeout: 20 * time.Second}}) }

func (m *Matrix) Name() string                   { return "matrix" }
func (m *Matrix) NeedsServer() bool              { return true }
func (m *Matrix) LinkURL(b Bot, _ string) string { return "" }

// matrixError is an answer of the server that is not 200: its status and Matrix's errcode.
type matrixError struct {
	Status  int
	Code    string
	Message string
}

func (e *matrixError) Error() string {
	return "matrix: " + strconv.Itoa(e.Status) + " " + e.Code + " " + e.Message
}

func (m *Matrix) call(ctx context.Context, b Bot, method, path string, body, out any) error {
	if b.Server == "" {
		return errServer
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.Server+"/_matrix/client/v3"+path, rd)
	if err != nil {
		return plain(err)
	}
	req.Header.Set("Authorization", "Bearer "+b.Token())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return plain(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Code    string `json:"errcode"`
			Message string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return &matrixError{resp.StatusCode, e.Code, e.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("matrix %s: %w", path, err)
	}
	return nil
}

// Check gives the bot's Matrix ID (@go24:example.org): employees write it.
func (m *Matrix) Check(ctx context.Context, b Bot) (string, error) {
	var me struct {
		UserID string `json:"user_id"`
	}
	if err := m.call(ctx, b, http.MethodGet, "/account/whoami", nil, &me); err != nil {
		return "", err
	}
	return me.UserID, nil
}

// syncFilter takes the messages of the chats and the invitations only.
const syncFilter = `{"presence":{"types":[]},"account_data":{"types":[]},"room":{"timeline":{"limit":50,"types":["m.room.message"]},"state":{"types":[]},"ephemeral":{"types":[]},"account_data":{"types":[]}}}`

// Updates reads the bot's chats since the last /sync: invitations are joined, the messages to the bot after next
// (the time of the last one taken, in milliseconds) are given. The first time it only notes the time: the old
// history is not answered.
func (m *Matrix) Updates(ctx context.Context, b Bot, next int64) ([]Update, int64, error) {
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	key := b.Server + " " + b.Token()
	m.mu.Lock()
	since := m.since[key]
	m.mu.Unlock()
	q := url.Values{"timeout": {"0"}, "filter": {syncFilter}}
	if since != "" {
		q.Set("since", since)
	}
	var r struct {
		Next  string `json:"next_batch"`
		Rooms struct {
			Join map[string]struct {
				Timeline struct {
					Events []struct {
						Type    string `json:"type"`
						Sender  string `json:"sender"`
						TS      int64  `json:"origin_server_ts"`
						Content struct {
							MsgType string `json:"msgtype"`
							Body    string `json:"body"`
						} `json:"content"`
					} `json:"events"`
				} `json:"timeline"`
			} `json:"join"`
			Invite map[string]json.RawMessage `json:"invite"`
		} `json:"rooms"`
	}
	if err := m.call(ctx, b, http.MethodGet, "/sync?"+q.Encode(), nil, &r); err != nil {
		return nil, next, err
	}
	m.mu.Lock()
	if m.since == nil {
		m.since = map[string]string{}
	}
	m.since[key] = r.Next
	m.mu.Unlock()
	for room := range r.Rooms.Invite { // a person started a chat with the bot
		if err := m.call(ctx, b, http.MethodPost, "/rooms/"+url.PathEscape(room)+"/join", map[string]any{}, nil); err != nil {
			return nil, next, err
		}
	}
	if next == 0 {
		return nil, now().UnixMilli(), nil
	}
	bot, err := m.Check(ctx, b)
	if err != nil {
		return nil, next, err
	}
	var out []Update
	last := next
	for room, j := range r.Rooms.Join {
		direct := -1 // a chat of two: asked once, when someone writes
		for _, e := range j.Timeline.Events {
			if e.Type != "m.room.message" || e.Sender == bot || e.TS <= next || e.Content.MsgType != "m.text" {
				continue
			}
			last = max(last, e.TS)
			if direct < 0 {
				var members struct {
					Joined map[string]json.RawMessage `json:"joined"`
				}
				if err := m.call(ctx, b, http.MethodGet, "/rooms/"+url.PathEscape(room)+"/joined_members", nil, &members); err != nil {
					return nil, next, err
				}
				direct = 0
				if len(members.Joined) <= 2 {
					direct = 1
				}
			}
			if direct == 1 {
				out = append(out, Update{Chat: room, Account: e.Sender, Text: e.Content.Body})
			}
		}
	}
	return out, last, nil
}

func (m *Matrix) Send(ctx context.Context, b Bot, chat string, msg Message) error {
	body := map[string]string{"msgtype": "m.text", "body": msg.Text}
	if msg.Link != "" {
		body["body"] = msg.Text + " " + msg.Link
		body["format"] = "org.matrix.custom.html"
		body["formatted_body"] = `<a href="` + html.EscapeString(msg.Link) + `">` + html.EscapeString(msg.Text) + `</a>`
	}
	m.mu.Lock()
	m.txn++
	txn := strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(m.txn, 10)
	m.mu.Unlock()
	err := m.call(ctx, b, http.MethodPut, "/rooms/"+url.PathEscape(chat)+"/send/m.room.message/"+txn, body, nil)
	// 403: the bot is not in the chat any more (the person left it); 404: there is no such chat
	if me, ok := err.(*matrixError); ok && (me.Status == http.StatusForbidden || me.Status == http.StatusNotFound) {
		return errors.Join(ErrGone, err)
	}
	return err
}
