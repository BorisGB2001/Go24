package notifyout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"time"
)

// Telegram: a bot made with @BotFather; the Bot API is asked for the messages (getUpdates, so go24 needs no public
// address) and writes the notifications (sendMessage). The bot's link t.me/<bot>?start=<code> sends the code itself.
type Telegram struct {
	API  string // https://api.telegram.org (a field for tests)
	HTTP *http.Client
}

func init() { Register(&Telegram{API: "https://api.telegram.org", HTTP: &http.Client{Timeout: 20 * time.Second}}) }

func (t *Telegram) Name() string      { return "telegram" }
func (t *Telegram) NeedsServer() bool { return false }

func (t *Telegram) LinkURL(b Bot, code string) string {
	if b.Name == "" {
		return ""
	}
	return "https://t.me/" + b.Name + "?start=" + code
}

// call asks the Bot API; an answer that is not ok is an error with Telegram's code (403 — the bot is blocked).
func (t *Telegram) call(ctx context.Context, b Bot, method string, args, out any) error {
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.API+"/bot"+b.Token()+"/"+method, bytes.NewReader(body))
	if err != nil {
		return plain(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return plain(err)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	if !r.OK {
		return &telegramError{r.ErrorCode, r.Description}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

type telegramError struct {
	Code        int
	Description string
}

func (e *telegramError) Error() string { return "telegram: " + strconv.Itoa(e.Code) + " " + e.Description }

func (t *Telegram) Check(ctx context.Context, b Bot) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := t.call(ctx, b, "getMe", map[string]any{}, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

func (t *Telegram) Updates(ctx context.Context, b Bot, next int64) ([]Update, int64, error) {
	var list []struct {
		UpdateID int64 `json:"update_id"`
		Message  *struct {
			Text string `json:"text"`
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			From struct {
				Username  string `json:"username"`
				FirstName string `json:"first_name"`
				LastName  string `json:"last_name"`
			} `json:"from"`
		} `json:"message"`
	}
	if err := t.call(ctx, b, "getUpdates", map[string]any{"offset": next, "timeout": 0, "allowed_updates": []string{"message"}}, &list); err != nil {
		return nil, next, err
	}
	var out []Update
	for _, u := range list {
		next = max(next, u.UpdateID+1)
		m := u.Message
		if m == nil || m.Chat.Type != "private" { // a group: notifications are personal
			continue
		}
		who := m.From.FirstName
		if m.From.LastName != "" {
			who += " " + m.From.LastName
		}
		if m.From.Username != "" {
			who = "@" + m.From.Username
		}
		out = append(out, Update{Chat: strconv.FormatInt(m.Chat.ID, 10), Account: who, Text: m.Text})
	}
	return out, next, nil
}

func (t *Telegram) Send(ctx context.Context, b Bot, chat string, m Message) error {
	args := map[string]any{"chat_id": chat, "text": m.Text, "link_preview_options": map[string]bool{"is_disabled": true}}
	if m.Link != "" {
		args["text"] = `<a href="` + html.EscapeString(m.Link) + `">` + html.EscapeString(m.Text) + `</a>`
		args["parse_mode"] = "HTML"
	}
	err := t.call(ctx, b, "sendMessage", args, nil)
	// 403: the person blocked the bot or deleted the chat; 400 «chat not found»: there is no such chat
	if te, ok := err.(*telegramError); ok && (te.Code == 403 || te.Code == 400 && te.Description == "Bad Request: chat not found") {
		return errors.Join(ErrGone, err)
	}
	return err
}
