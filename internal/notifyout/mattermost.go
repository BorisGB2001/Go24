package notifyout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Mattermost: the company's own Mattermost server (its address is set with the token) and a bot account there
// («Интеграции → Бот-аккаунты»). REST API v4: the direct channels of the bot are read for new posts since the last
// one seen (so go24 needs no public address and the server no outgoing webhook), the notifications are posts in the
// direct channel. There is no link with the code ready: the person writes the bot the code.
type Mattermost struct {
	HTTP *http.Client
	now  func() time.Time // for tests
}

func init() { Register(&Mattermost{HTTP: &http.Client{Timeout: 20 * time.Second}}) }

func (m *Mattermost) Name() string                   { return "mattermost" }
func (m *Mattermost) NeedsServer() bool              { return true }
func (m *Mattermost) LinkURL(b Bot, _ string) string { return "" }

// mattermostError is an answer of the server that is not 2xx.
type mattermostError struct {
	Status  int
	Message string
}

func (e *mattermostError) Error() string {
	return "mattermost: " + strconv.Itoa(e.Status) + " " + e.Message
}

func (m *Mattermost) call(ctx context.Context, b Bot, method, path string, body, out any) error {
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
	req, err := http.NewRequestWithContext(ctx, method, b.Server+"/api/v4"+path, rd)
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
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return &mattermostError{resp.StatusCode, e.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("mattermost %s: %w", path, err)
	}
	return nil
}

type mattermostUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (m *Mattermost) Check(ctx context.Context, b Bot) (string, error) {
	var me mattermostUser
	if err := m.call(ctx, b, http.MethodGet, "/users/me", nil, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// Updates reads the posts written to the bot in its direct channels after next (the time of the last one seen, in
// milliseconds). The first time it only notes the time: the old history is not answered.
func (m *Mattermost) Updates(ctx context.Context, b Bot, next int64) ([]Update, int64, error) {
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	if next == 0 {
		return nil, now().UnixMilli(), nil
	}
	var me mattermostUser
	if err := m.call(ctx, b, http.MethodGet, "/users/me", nil, &me); err != nil {
		return nil, next, err
	}
	var teams []struct {
		ID string `json:"id"`
	}
	if err := m.call(ctx, b, http.MethodGet, "/users/me/teams", nil, &teams); err != nil {
		return nil, next, err
	}
	direct := map[string]bool{} // a direct channel is in the list of every team
	for _, t := range teams {
		var chans []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			LastPost int64  `json:"last_post_at"`
		}
		if err := m.call(ctx, b, http.MethodGet, "/users/me/teams/"+url.PathEscape(t.ID)+"/channels", nil, &chans); err != nil {
			return nil, next, err
		}
		for _, c := range chans {
			if c.Type == "D" && c.LastPost > next { // «D» — a direct message; a group or a channel: notifications are personal
				direct[c.ID] = true
			}
		}
	}
	type post struct {
		UserID   string `json:"user_id"`
		Message  string `json:"message"`
		CreateAt int64  `json:"create_at"`
		Channel  string `json:"channel_id"`
		Type     string `json:"type"`
	}
	var posts []post
	for id := range direct {
		var r struct {
			Posts map[string]post `json:"posts"`
		}
		if err := m.call(ctx, b, http.MethodGet, "/channels/"+url.PathEscape(id)+"/posts?since="+strconv.FormatInt(next, 10), nil, &r); err != nil {
			return nil, next, err
		}
		for _, p := range r.Posts {
			if p.CreateAt > next && p.UserID != me.ID && p.Type == "" { // not the bot's own, not a system post
				posts = append(posts, p)
			}
		}
	}
	sort.Slice(posts, func(i, j int) bool { return posts[i].CreateAt < posts[j].CreateAt })
	names := map[string]string{}
	var out []Update
	for _, p := range posts {
		next = max(next, p.CreateAt)
		who, ok := names[p.UserID]
		if !ok {
			var u mattermostUser
			if err := m.call(ctx, b, http.MethodGet, "/users/"+url.PathEscape(p.UserID), nil, &u); err == nil && u.Username != "" {
				who = "@" + u.Username
			}
			names[p.UserID] = who
		}
		out = append(out, Update{Chat: p.Channel, Account: who, Text: strings.TrimSpace(p.Message)})
	}
	return out, next, nil
}

func (m *Mattermost) Send(ctx context.Context, b Bot, chat string, msg Message) error {
	text := msg.Text
	if msg.Link != "" {
		text = "[" + strings.NewReplacer("[", "\\[", "]", "\\]").Replace(msg.Text) + "](" + msg.Link + ")"
	}
	err := m.call(ctx, b, http.MethodPost, "/posts", map[string]string{"channel_id": chat, "message": text}, nil)
	// 403: the bot is out of the channel (deactivated person); 404: there is no such channel
	if me, ok := err.(*mattermostError); ok && (me.Status == http.StatusForbidden || me.Status == http.StatusNotFound) {
		return errors.Join(ErrGone, err)
	}
	return err
}
