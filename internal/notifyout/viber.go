package notifyout

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

// Viber: a bot of Viber (its token from partners.viber.com). Viber is not asked for the messages: it calls go24's
// address itself (set_webhook, so go24 needs a public https address), each call signed with the token. The bot's
// link viber://pa?chatURI=<uri>&context=<code> opens the chat with the code in its «conversation_started» call; the
// chat cannot be written before the person writes it, so the bot greets them with a button that sends the code.
type Viber struct {
	API  string // https://chatapi.viber.com/pa (a field for tests)
	HTTP *http.Client

	mu    sync.Mutex
	names map[string]string // the token → the bot's own name: every message is sent with it
}

func init() {
	Register(&Viber{API: "https://chatapi.viber.com/pa", HTTP: &http.Client{Timeout: 20 * time.Second}})
}

func (v *Viber) Name() string      { return "viber" }
func (v *Viber) NeedsServer() bool { return false }

func (v *Viber) LinkURL(b Bot, code string) string {
	if b.Name == "" {
		return ""
	}
	return "viber://pa?chatURI=" + url.QueryEscape(b.Name) + "&context=" + url.QueryEscape(code)
}

// viberError is an answer of the API with a status other than 0 (5, 6 — the person is not subscribed any more).
type viberError struct {
	Status  int
	Message string
}

func (e *viberError) Error() string { return "viber: " + strconv.Itoa(e.Status) + " " + e.Message }

// call asks the API: every method is a POST of JSON with the token in a header.
func (v *Viber) call(ctx context.Context, b Bot, method string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.API+"/"+method, bytes.NewReader(raw))
	if err != nil {
		return plain(err)
	}
	req.Header.Set("X-Viber-Auth-Token", b.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return plain(err)
	}
	defer resp.Body.Close()
	var all json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&all); err != nil {
		return fmt.Errorf("viber %s: %s", method, resp.Status)
	}
	var r struct {
		Status  int    `json:"status"`
		Message string `json:"status_message"`
	}
	if err := json.Unmarshal(all, &r); err != nil {
		return err
	}
	if r.Status != 0 {
		return &viberError{r.Status, r.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(all, out)
}

// Check gives the bot's URI (the chatURI of its link) and notes its name.
func (v *Viber) Check(ctx context.Context, b Bot) (string, error) {
	var a struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	if err := v.call(ctx, b, "get_account_info", map[string]any{}, &a); err != nil {
		return "", err
	}
	if a.URI == "" {
		return "", errors.New("viber: the account has no URI")
	}
	v.mu.Lock()
	if v.names == nil {
		v.names = map[string]string{}
	}
	v.names[b.Token()] = a.Name
	v.mu.Unlock()
	return a.URI, nil
}

// sender is who the messages are from: the bot's name (asked once while go24 runs), Viber takes up to 28 letters.
func (v *Viber) sender(ctx context.Context, b Bot) map[string]string {
	v.mu.Lock()
	name, ok := v.names[b.Token()]
	v.mu.Unlock()
	if !ok {
		_, _ = v.Check(ctx, b)
		v.mu.Lock()
		name = v.names[b.Token()]
		v.mu.Unlock()
	}
	if name == "" {
		name = "go24"
	}
	return map[string]string{"name": cut(name, 28)}
}

// Updates: Viber calls go24 itself.
func (v *Viber) Updates(_ context.Context, _ Bot, next int64) ([]Update, int64, error) {
	return nil, next, nil
}

func (v *Viber) SetHook(ctx context.Context, b Bot, addr string) error {
	body := map[string]any{"url": addr}
	if addr != "" {
		body["event_types"] = []string{"conversation_started", "unsubscribed"}
		body["send_name"] = true
	}
	return v.call(ctx, b, "set_webhook", body, nil)
}

// Hook reads a call of Viber: the signature is the HMAC-SHA256 of the body with the token. A person's message, their
// opening the chat (with the code of the link) and their unsubscribing (taken as «/stop») are taken.
func (v *Viber) Hook(b Bot, h http.Header, body []byte) ([]Update, error) {
	mac := hmac.New(sha256.New, []byte(b.Token()))
	mac.Write(body)
	sign, err := hex.DecodeString(h.Get("X-Viber-Content-Signature"))
	if err != nil || !hmac.Equal(sign, mac.Sum(nil)) {
		return nil, ErrHookSign
	}
	var e struct {
		Event   string `json:"event"`
		Context string `json:"context"`
		UserID  string `json:"user_id"`
		User    struct {
			ID, Name string
		} `json:"user"`
		Sender struct {
			ID, Name string
		} `json:"sender"`
		Message struct {
			Type, Text string
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	switch e.Event {
	case "conversation_started":
		text := "/start"
		if e.Context != "" {
			text += " " + e.Context
		}
		return []Update{{Chat: e.User.ID, Account: e.User.Name, Text: text, Opened: true}}, nil
	case "message":
		if e.Message.Type != "text" {
			return nil, nil
		}
		return []Update{{Chat: e.Sender.ID, Account: e.Sender.Name, Text: e.Message.Text}}, nil
	case "unsubscribed":
		return []Update{{Chat: e.UserID, Text: "/stop"}}, nil
	}
	return nil, nil // «webhook» (the address is checked), «delivered», «seen»…
}

// message is a Viber message of the text, with the button of m (a keyboard of one button that writes Press).
func (v *Viber) message(ctx context.Context, b Bot, m Message) map[string]any {
	text := m.Text
	if m.Link != "" {
		text += "\n" + m.Link
	}
	msg := map[string]any{"sender": v.sender(ctx, b), "type": "text", "text": cut(text, 7000)}
	if m.Button != "" {
		msg["min_api_version"] = 3
		msg["keyboard"] = map[string]any{"Type": "keyboard", "DefaultHeight": false, "Buttons": []map[string]any{
			{"ActionType": "reply", "ActionBody": m.Press, "Text": m.Button, "Silent": false}}}
	}
	return msg
}

func (v *Viber) Welcome(ctx context.Context, b Bot, m Message) []byte {
	raw, _ := json.Marshal(v.message(ctx, b, m))
	return raw
}

func (v *Viber) Send(ctx context.Context, b Bot, chat string, m Message) error {
	msg := v.message(ctx, b, m)
	msg["receiver"] = chat
	err := v.call(ctx, b, "send_message", msg, nil)
	// 5: the person is not in Viber any more; 6: they unsubscribed from the bot
	if ve, ok := err.(*viberError); ok && (ve.Status == 5 || ve.Status == 6) {
		return errors.Join(ErrGone, err)
	}
	return err
}

// cut keeps the first n letters of s.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
