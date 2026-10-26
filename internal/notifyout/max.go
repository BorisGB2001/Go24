package notifyout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Max: a bot of the MAX messenger (platform-api.max.ru, made in the MAX platform for partners). Its API is asked for
// the events (GET /updates with a marker, so go24 needs no public address) and writes the notifications
// (POST /messages). The bot's link max.ru/<bot>?start=<code> starts the dialog with the code (the «bot_started» event).
type Max struct {
	API  string // https://platform-api.max.ru (a field for tests)
	HTTP *http.Client
}

func init() {
	Register(&Max{API: "https://platform-api.max.ru", HTTP: &http.Client{Timeout: 20 * time.Second}})
}

func (m *Max) Name() string      { return "max" }
func (m *Max) NeedsServer() bool { return false }

func (m *Max) LinkURL(b Bot, code string) string {
	if b.Name == "" {
		return ""
	}
	return "https://max.ru/" + b.Name + "?start=" + code
}

// maxError is an answer of the API that is not 200: its HTTP status (403, 404 — the chat is no more) and text.
type maxError struct {
	Status  int
	Code    string
	Message string
}

func (e *maxError) Error() string {
	return "max: " + strconv.Itoa(e.Status) + " " + e.Code + " " + e.Message
}

// call asks the API; the token goes in the Authorization header (and access_token, as the API took it at first).
func (m *Max) call(ctx context.Context, b Bot, method, path string, q url.Values, body, out any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("access_token", b.Token())
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, m.API+path+"?"+q.Encode(), rd)
	if err != nil {
		return plain(err)
	}
	req.Header.Set("Authorization", b.Token())
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
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return &maxError{resp.StatusCode, e.Code, e.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("max %s: %w", path, err)
	}
	return nil
}

func (m *Max) Check(ctx context.Context, b Bot) (string, error) {
	var me struct {
		Username string `json:"username"`
		Name     string `json:"name"`
	}
	if err := m.call(ctx, b, http.MethodGet, "/me", nil, nil, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// maxUser is who wrote the bot.
type maxUser struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

func (u maxUser) account() string {
	if u.Username != "" {
		return "@" + u.Username
	}
	return u.Name
}

func (m *Max) Updates(ctx context.Context, b Bot, next int64) ([]Update, int64, error) {
	q := url.Values{"timeout": {"0"}, "limit": {"100"}, "types": {"message_created,bot_started"}}
	if next > 0 {
		q.Set("marker", strconv.FormatInt(next, 10))
	}
	var r struct {
		Updates []struct {
			Type    string  `json:"update_type"`
			ChatID  int64   `json:"chat_id"` // bot_started
			User    maxUser `json:"user"`
			Payload string  `json:"payload"`
			Message *struct {
				Sender    maxUser `json:"sender"`
				Recipient struct {
					ChatID   int64  `json:"chat_id"`
					ChatType string `json:"chat_type"`
				} `json:"recipient"`
				Body struct {
					Text string `json:"text"`
				} `json:"body"`
			} `json:"message"`
		} `json:"updates"`
		Marker *int64 `json:"marker"`
	}
	if err := m.call(ctx, b, http.MethodGet, "/updates", q, nil, &r); err != nil {
		return nil, next, err
	}
	if r.Marker != nil {
		next = *r.Marker
	}
	var out []Update
	for _, u := range r.Updates {
		switch {
		case u.Type == "bot_started" && u.ChatID != 0: // the bot's link opened: the code comes as /start <code>
			out = append(out, Update{Chat: strconv.FormatInt(u.ChatID, 10), Account: u.User.account(), Text: "/start " + u.Payload})
		case u.Type == "message_created" && u.Message != nil && u.Message.Recipient.ChatType == "dialog": // a group: notifications are personal
			out = append(out, Update{Chat: strconv.FormatInt(u.Message.Recipient.ChatID, 10), Account: u.Message.Sender.account(),
				Text: u.Message.Body.Text})
		}
	}
	return out, next, nil
}

func (m *Max) Send(ctx context.Context, b Bot, chat string, msg Message) error {
	body := map[string]any{"text": msg.Text}
	if msg.Link != "" {
		body["text"] = `<a href="` + html.EscapeString(msg.Link) + `">` + html.EscapeString(msg.Text) + `</a>`
		body["format"] = "html"
	}
	err := m.call(ctx, b, http.MethodPost, "/messages", url.Values{"chat_id": {chat}, "disable_link_preview": {"true"}}, body, nil)
	// 403: the person blocked the bot; 404: there is no such chat
	if me, ok := err.(*maxError); ok && (me.Status == http.StatusForbidden || me.Status == http.StatusNotFound) {
		return errors.Join(ErrGone, err)
	}
	return err
}
