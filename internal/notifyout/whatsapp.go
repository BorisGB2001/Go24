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
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// WhatsApp: the company's WhatsApp Business number (the Cloud API of Meta). Its «token» is four parts joined by «:» —
// the number's id, an access token, the application's secret and, optionally, the WhatsApp Business account's id.
// Meta is not asked for the messages: it calls go24's address itself (with the account's id go24 subscribes it, else
// the administrators set the address and its verify token in the application), each call signed with the
// application's secret. The link wa.me/<number>?text=<code> opens the chat with the code ready. WhatsApp lets the
// business write freely only within 24 hours of the person's last message; later a notification goes as the
// approved template «go24_notification» with one parameter.
type WhatsApp struct {
	API  string // https://graph.facebook.com/v21.0 (a field for tests)
	HTTP *http.Client
}

// waTemplate is the template the administrators approve in WhatsApp Manager: its body is one parameter, {{1}}.
const waTemplate = "go24_notification"

// waLangs are the languages of the template tried in turn.
var waLangs = []string{"ru", "en", "en_US"}

func init() {
	Register(&WhatsApp{API: "https://graph.facebook.com/v21.0", HTTP: &http.Client{Timeout: 20 * time.Second}})
}

func (w *WhatsApp) Name() string      { return "whatsapp" }
func (w *WhatsApp) NeedsServer() bool { return false }

// waKey is the bot's token read into its parts.
type waKey struct{ phone, token, secret, waba string }

// waIDRe: the ids of the Cloud API (the number's, the business account's) are digits.
var waIDRe = regexp.MustCompile(`^\d{5,30}$`)

func waParts(b Bot) (waKey, bool) {
	p := strings.Split(b.Token(), ":")
	if len(p) < 3 || len(p) > 4 {
		return waKey{}, false
	}
	k := waKey{phone: strings.TrimSpace(p[0]), token: strings.TrimSpace(p[1]), secret: strings.TrimSpace(p[2])}
	if len(p) == 4 {
		k.waba = strings.TrimSpace(p[3])
	}
	return k, waIDRe.MatchString(k.phone) && (k.waba == "" || waIDRe.MatchString(k.waba)) && k.token != "" && k.secret != ""
}

func (w *WhatsApp) LinkURL(b Bot, code string) string {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, b.Name)
	if digits == "" {
		return ""
	}
	return "https://wa.me/" + digits + "?text=" + url.QueryEscape(code)
}

// waError is an answer of the Graph API with an error (190 — the token is bad, 131047 — more than 24 hours since the
// person's last message, 132001 — no such template in the language, 131026 — the person cannot get it…).
type waError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *waError) Error() string { return "whatsapp: " + strconv.Itoa(e.Code) + " " + e.Message }

// call asks the Graph API (body nil — a GET) with the token in a header and its appsecret_proof, so a wrong secret
// is told at once.
func (w *WhatsApp) call(ctx context.Context, k waKey, path string, body, out any) error {
	mac := hmac.New(sha256.New, []byte(k.secret))
	mac.Write([]byte(k.token))
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	path += sep + "appsecret_proof=" + hex.EncodeToString(mac.Sum(nil))
	verb, rd := http.MethodGet, io.Reader(nil)
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		verb, rd = http.MethodPost, bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, verb, w.API+path, rd)
	if err != nil {
		return plain(err)
	}
	req.Header.Set("Authorization", "Bearer "+k.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.HTTP.Do(req)
	if err != nil {
		return plain(err)
	}
	defer resp.Body.Close()
	all, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var e struct {
		Error *waError `json:"error"`
	}
	if json.Unmarshal(all, &e) == nil && e.Error != nil {
		return e.Error
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("whatsapp %s: %s", path[:strings.IndexAny(path+"?", "?")], resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(all, out)
}

// Check gives the number (as Meta shows it: +7 900 123-45-67), the bot's name and its link.
func (w *WhatsApp) Check(ctx context.Context, b Bot) (string, error) {
	k, ok := waParts(b)
	if !ok {
		return "", errors.New("whatsapp: the token is not «number id:access token:app secret[:account id]»")
	}
	var a struct {
		Number string `json:"display_phone_number"`
	}
	if err := w.call(ctx, k, "/"+k.phone+"?fields=display_phone_number,verified_name", nil, &a); err != nil {
		return "", err
	}
	if a.Number == "" {
		return "", errors.New("whatsapp: the number has no phone")
	}
	return a.Number, nil
}

// Updates: Meta calls go24 itself.
func (w *WhatsApp) Updates(_ context.Context, _ Bot, next int64) ([]Update, int64, error) {
	return nil, next, nil
}

// VerifyToken is the word Meta shows back when it checks go24's address (hub.verify_token).
func (w *WhatsApp) VerifyToken(b Bot) string {
	k, _ := waParts(b)
	mac := hmac.New(sha256.New, []byte(k.secret))
	mac.Write([]byte("go24 notify whatsapp verify\x00" + k.phone))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// Challenge answers Meta's check of the address: the challenge back when the verify token is go24's.
func (w *WhatsApp) Challenge(b Bot, q url.Values) (string, bool) {
	if q.Get("hub.mode") != "subscribe" || !hmac.Equal([]byte(q.Get("hub.verify_token")), []byte(w.VerifyToken(b))) {
		return "", false
	}
	return q.Get("hub.challenge"), true
}

// SetHook, when the business account's id is given, subscribes the application to the account and points the
// number's own webhook at go24 (Meta checks the address at once); without it the administrators set the address in
// the application. Forgetting the token leaves them: the application may serve the open lines' numbers too.
func (w *WhatsApp) SetHook(ctx context.Context, b Bot, addr string) error {
	k, _ := waParts(b)
	if k.waba == "" || addr == "" {
		return nil
	}
	if err := w.call(ctx, k, "/"+k.waba+"/subscribed_apps", map[string]any{}, nil); err != nil {
		return err
	}
	return w.call(ctx, k, "/"+k.phone, map[string]any{"webhook_configuration": map[string]any{
		"override_callback_uri": addr, "verify_token": w.VerifyToken(b)}}, nil)
}

// Hook reads a call of Meta: the signature is the HMAC-SHA256 of the body with the application's secret. The
// messages to the number are taken: a text, a template's quick reply or an interactive button.
func (w *WhatsApp) Hook(b Bot, h http.Header, body []byte) ([]Update, error) {
	k, ok := waParts(b)
	sign, err := hex.DecodeString(strings.TrimPrefix(h.Get("X-Hub-Signature-256"), "sha256="))
	mac := hmac.New(sha256.New, []byte(k.secret))
	mac.Write(body)
	if !ok || err != nil || !hmac.Equal(sign, mac.Sum(nil)) {
		return nil, ErrHookSign
	}
	var e struct {
		Entry []struct {
			Changes []struct {
				Field string `json:"field"`
				Value struct {
					Metadata struct {
						Phone string `json:"phone_number_id"`
					} `json:"metadata"`
					Contacts []struct {
						Profile struct{ Name string } `json:"profile"`
						WaID    string                `json:"wa_id"`
					} `json:"contacts"`
					Messages []struct {
						From string `json:"from"`
						Type string `json:"type"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
						Button struct {
							Payload string `json:"payload"`
							Text    string `json:"text"`
						} `json:"button"`
						Interactive struct {
							Reply struct {
								ID string `json:"id"`
							} `json:"button_reply"`
						} `json:"interactive"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &e); err != nil {
		return nil, err
	}
	var ups []Update
	for _, en := range e.Entry {
		for _, c := range en.Changes {
			// one application may serve several numbers: only this one's messages
			if c.Field != "messages" || c.Value.Metadata.Phone != k.phone {
				continue
			}
			names := map[string]string{}
			for _, ct := range c.Value.Contacts {
				names[ct.WaID] = ct.Profile.Name
			}
			for _, m := range c.Value.Messages {
				var text string
				switch m.Type {
				case "text":
					text = m.Text.Body
				case "button":
					text = m.Button.Payload
				case "interactive":
					text = m.Interactive.Reply.ID
				default:
					continue
				}
				who := names[m.From]
				if who == "" {
					who = "+" + m.From
				}
				ups = append(ups, Update{Chat: m.From, Account: who, Text: text})
			}
		}
	}
	return ups, nil // statuses (sent, delivered, read) are not messages
}

// Welcome: a person writes the number first (the link's code), so there is never a call to answer.
func (w *WhatsApp) Welcome(context.Context, Bot, Message) []byte { return nil }

func (w *WhatsApp) Send(ctx context.Context, b Bot, chat string, m Message) error {
	k, ok := waParts(b)
	if !ok {
		return errors.New("whatsapp: a bad token")
	}
	text := m.Text
	if m.Link != "" {
		text += "\n" + m.Link
	}
	path := "/" + k.phone + "/messages"
	msg := map[string]any{"messaging_product": "whatsapp", "recipient_type": "individual", "to": chat}
	if m.Button != "" {
		msg["type"] = "interactive"
		msg["interactive"] = map[string]any{"type": "button", "body": map[string]any{"text": cut(text, 1024)},
			"action": map[string]any{"buttons": []map[string]any{{"type": "reply", "reply": map[string]any{"id": m.Press, "title": cut(m.Button, 20)}}}}}
	} else {
		msg["type"] = "text"
		msg["text"] = map[string]any{"body": cut(text, 4096), "preview_url": false}
	}
	err := w.call(ctx, k, path, msg, nil)
	var we *waError
	if !errors.As(err, &we) || we.Code != 131047 {
		return err
	}
	// more than 24 hours since the person wrote: the template, whose parameter takes no line breaks
	param := cut(strings.Join(strings.Fields(text), " "), 1000)
	for _, lang := range waLangs {
		err = w.call(ctx, k, path, map[string]any{"messaging_product": "whatsapp", "to": chat, "type": "template",
			"template": map[string]any{"name": waTemplate, "language": map[string]any{"code": lang},
				"components": []map[string]any{{"type": "body", "parameters": []map[string]any{{"type": "text", "text": param}}}}}}, nil)
		if !errors.As(err, &we) || we.Code != 132001 {
			break
		}
	}
	return err
}
