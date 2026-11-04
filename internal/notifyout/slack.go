package notifyout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Slack: a Slack app with a bot, in Socket Mode — the app's events come over a WebSocket go24 opens itself
// (apps.connections.open), so go24 needs no public address, and polling every direct chat would hit Slack's rate
// limits. Two tokens are set as one, separated by a space: the bot's (xoxb-…, the Web API: auth.test,
// chat.postMessage, users.info) and the app-level one (xapp-…, connections:write). go24-worker keeps the socket
// open while it asks for the messages; the person writes the bot the code in its «Messages» tab. Slack takes
// «/stop» for a slash command of its own, so «stop» is taken as one too.
type Slack struct {
	API   string // https://slack.com/api (a field for tests)
	HTTP  *http.Client
	retry time.Duration // the pause before the socket is opened again after an error
	idle  time.Duration // the socket is closed when the messages have not been asked for this long

	mu    sync.Mutex
	socks map[string]*slackSocket // by the app-level token
	names map[string]string       // user ID → @name
}

func init() {
	Register(&Slack{API: "https://slack.com/api", HTTP: &http.Client{Timeout: 20 * time.Second}, retry: 30 * time.Second, idle: 2 * time.Minute})
}

func (s *Slack) Name() string                   { return "slack" }
func (s *Slack) NeedsServer() bool              { return false }
func (s *Slack) LinkURL(b Bot, _ string) string { return "" }

// slackTokens splits the token set into the bot's one and the app-level one.
func slackTokens(b Bot) (bot, app string) {
	for _, f := range strings.Fields(b.Token()) {
		switch {
		case strings.HasPrefix(f, "xoxb-"):
			bot = f
		case strings.HasPrefix(f, "xapp-"):
			app = f
		}
	}
	return bot, app
}

type slackError struct{ Code string }

func (e *slackError) Error() string { return "slack: " + e.Code }

// call asks a Web API method with the token (form-encoded: every method takes it); an answer that is not ok is a
// slackError with Slack's code.
func (s *Slack) call(ctx context.Context, token, method string, args url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.API+"/"+method, strings.NewReader(args.Encode()))
	if err != nil {
		return plain(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return plain(err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return fmt.Errorf("slack %s: %s", method, resp.Status)
	}
	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if !r.OK {
		return &slackError{r.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Check reads the bot's name by its token and opens a socket with the app-level one (Socket Mode is on).
func (s *Slack) Check(ctx context.Context, b Bot) (string, error) {
	bot, app := slackTokens(b)
	if bot == "" || app == "" {
		return "", errors.New("slack: both the bot token (xoxb-) and the app-level token (xapp-) are needed")
	}
	var me struct {
		User string `json:"user"`
	}
	if err := s.call(ctx, bot, "auth.test", nil, &me); err != nil {
		return "", err
	}
	if err := s.call(ctx, app, "apps.connections.open", nil, nil); err != nil {
		return "", err
	}
	return me.User, nil
}

// slackSocket is the open Socket Mode connection of an app and the messages it brought.
type slackSocket struct {
	mu     sync.Mutex
	queue  []Update
	err    error // the last error of opening it, told once
	polled time.Time
	conn   *websocket.Conn
	closed bool
}

func (k *slackSocket) stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closed = true
	if k.conn != nil {
		_ = k.conn.Close()
	}
}

// idleFor: the socket is closed (and its goroutine ends) when nobody asked for its messages for d.
func (k *slackSocket) idleFor(d time.Duration) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.closed && time.Since(k.polled) > d {
		k.closed = true
	}
	return k.closed
}

// Updates takes the messages the socket brought since the last time; the first call opens it (what was written
// before is not answered). next is not used: Slack keeps no number of the events.
func (s *Slack) Updates(ctx context.Context, b Bot, next int64) ([]Update, int64, error) {
	bot, app := slackTokens(b)
	if bot == "" || app == "" {
		return nil, next, errToken
	}
	s.mu.Lock()
	if s.socks == nil {
		s.socks = map[string]*slackSocket{}
	}
	for t, k := range s.socks { // the tokens changed: the old app's socket is closed
		if t != app {
			k.stop()
			delete(s.socks, t)
		}
	}
	k := s.socks[app]
	if k == nil || k.idleFor(s.idle) {
		k = &slackSocket{polled: time.Now()}
		s.socks[app] = k
		go s.run(k, app)
	}
	s.mu.Unlock()

	k.mu.Lock()
	ups, err := k.queue, k.err
	k.queue, k.err, k.polled = nil, nil, time.Now()
	k.mu.Unlock()
	for i := range ups {
		ups[i].Account = s.account(ctx, bot, ups[i].Account)
	}
	return ups, next, err
}

// account is the person's @name by their user ID (without users:read — nothing).
func (s *Slack) account(ctx context.Context, bot, id string) string {
	s.mu.Lock()
	name, ok := s.names[id]
	s.mu.Unlock()
	if ok {
		return name
	}
	var r struct {
		User struct {
			Name string `json:"name"`
		} `json:"user"`
	}
	if err := s.call(ctx, bot, "users.info", url.Values{"user": {id}}, &r); err == nil && r.User.Name != "" {
		name = "@" + r.User.Name
	}
	s.mu.Lock()
	if s.names == nil {
		s.names = map[string]string{}
	}
	s.names[id] = name
	s.mu.Unlock()
	return name
}

// run keeps the socket open until it idles: each event is acknowledged at once, a direct message to the bot goes
// to the queue; Slack's «disconnect» (the connection is refreshed every few hours) or a lost one opens a new one.
func (s *Slack) run(k *slackSocket, app string) {
	for !k.idleFor(s.idle) {
		conn, err := s.dial(app)
		if err != nil {
			k.mu.Lock()
			k.err = err
			k.mu.Unlock()
			time.Sleep(s.retry)
			continue
		}
		k.mu.Lock()
		k.conn = conn
		closed := k.closed
		k.mu.Unlock()
		if !closed {
			s.read(k, conn)
		}
		_ = conn.Close()
		time.Sleep(time.Second)
	}
}

func (s *Slack) dial(app string) (*websocket.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var r struct {
		URL string `json:"url"`
	}
	if err := s.call(ctx, app, "apps.connections.open", nil, &r); err != nil {
		return nil, err
	}
	conn, err := websocket.Dial(r.URL, "", "https://slack.com")
	if err != nil {
		return nil, plain(err) // the socket's address carries a ticket
	}
	return conn, nil
}

// read takes the socket's envelopes until it is closed or goes quiet for five minutes (Slack sends something
// more often: a quiet socket is lost).
func (s *Slack) read(k *slackSocket, conn *websocket.Conn) {
	for !k.idleFor(s.idle) {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		var env struct {
			Type       string `json:"type"`
			EnvelopeID string `json:"envelope_id"`
			Payload    struct {
				Event struct {
					Type        string `json:"type"`
					ChannelType string `json:"channel_type"`
					Channel     string `json:"channel"`
					User        string `json:"user"`
					Text        string `json:"text"`
					BotID       string `json:"bot_id"`
					Subtype     string `json:"subtype"`
				} `json:"event"`
			} `json:"payload"`
		}
		if err := websocket.JSON.Receive(conn, &env); err != nil {
			return
		}
		if env.EnvelopeID != "" {
			if err := websocket.JSON.Send(conn, map[string]string{"envelope_id": env.EnvelopeID}); err != nil {
				return
			}
		}
		if env.Type == "disconnect" {
			return
		}
		e := env.Payload.Event
		// a direct message of a person: not the bot's own, not an edit or a join; a channel — notifications are personal
		if env.Type != "events_api" || e.Type != "message" || e.ChannelType != "im" || e.BotID != "" || e.Subtype != "" || e.User == "" {
			continue
		}
		text := strings.TrimSpace(e.Text)
		if slices.Contains([]string{"stop", "стоп"}, strings.ToLower(text)) {
			text = "/stop"
		}
		k.mu.Lock()
		k.queue = append(k.queue, Update{Chat: e.Channel, Account: e.User, Text: text})
		k.mu.Unlock()
	}
}

// slackEscape escapes the three characters Slack's mrkdwn takes for markup.
var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func (s *Slack) Send(ctx context.Context, b Bot, chat string, m Message) error {
	bot, _ := slackTokens(b)
	text := slackEscape.Replace(m.Text)
	if m.Link != "" {
		text = "<" + m.Link + "|" + text + ">"
	}
	err := s.call(ctx, bot, "chat.postMessage", url.Values{"channel": {chat}, "text": {text}, "unfurl_links": {"false"}}, nil)
	// the chat is no more, or the person was deactivated
	if se, ok := err.(*slackError); ok && slices.Contains([]string{"channel_not_found", "is_archived", "not_in_channel", "user_disabled", "account_inactive"}, se.Code) {
		return errors.Join(ErrGone, err)
	}
	return err
}
