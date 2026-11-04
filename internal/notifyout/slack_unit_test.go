package notifyout

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestSlackMetadata(t *testing.T) {
	s := &Slack{}
	if got := s.Name(); got != "slack" {
		t.Errorf("Slack.Name() = %q, want %q", got, "slack")
	}
	if got := s.NeedsServer(); got != false {
		t.Errorf("Slack.NeedsServer() = %v, want false", got)
	}
	if got := s.LinkURL(Bot{Name: "slackbot"}, "123"); got != "" {
		t.Errorf("Slack.LinkURL() = %q, want empty string", got)
	}
}

func TestSlackTokens(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		wantBot string
		wantApp string
	}{
		{
			name:    "standard space separated",
			token:   "xoxb-bot-token-123 xapp-app-token-456",
			wantBot: "xoxb-bot-token-123",
			wantApp: "xapp-app-token-456",
		},
		{
			name:    "reversed order with multiple whitespaces and tabs",
			token:   "  xapp-my-app \t\n xoxb-my-bot  ",
			wantBot: "xoxb-my-bot",
			wantApp: "xapp-my-app",
		},
		{
			name:    "missing bot token",
			token:   "xapp-only-token",
			wantBot: "",
			wantApp: "xapp-only-token",
		},
		{
			name:    "missing app token",
			token:   "xoxb-only-token",
			wantBot: "xoxb-only-token",
			wantApp: "",
		},
		{
			name:    "empty token",
			token:   "",
			wantBot: "",
			wantApp: "",
		},
		{
			name:    "unrelated tokens ignored",
			token:   "xoxb-good other-junk xapp-good extra-token",
			wantBot: "xoxb-good",
			wantApp: "xapp-good",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBot, gotApp := slackTokens(Bot{token: tt.token})
			if gotBot != tt.wantBot || gotApp != tt.wantApp {
				t.Errorf("slackTokens(%q) = bot %q, app %q; want bot %q, app %q",
					tt.token, gotBot, gotApp, tt.wantBot, tt.wantApp)
			}
		})
	}
}

func TestSlackEscape(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{
			in:   "Hello World",
			want: "Hello World",
		},
		{
			in:   "Tasks & Bugs < urgent > & critical",
			want: "Tasks &amp; Bugs &lt; urgent &gt; &amp; critical",
		},
		{
			in:   `Single 'quotes' and "double quotes" remain untouched`,
			want: `Single 'quotes' and "double quotes" remain untouched`,
		},
	}

	for _, tt := range tests {
		if got := slackEscape.Replace(tt.in); got != tt.want {
			t.Errorf("slackEscape.Replace(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSlackCheck(t *testing.T) {
	t.Run("missing tokens returns error", func(t *testing.T) {
		s := &Slack{}
		_, err := s.Check(context.Background(), Bot{token: "xoxb-only"})
		if err == nil || !strings.Contains(err.Error(), "both the bot token") {
			t.Errorf("Check() err = %v, want both tokens needed error", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			auth := r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")

			switch r.URL.Path {
			case "/auth.test":
				if auth != "Bearer xoxb-good" {
					_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true,"user":"go24_slack_bot"}`)
			case "/apps.connections.open":
				if auth != "Bearer xapp-good" {
					_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		s := &Slack{API: srv.URL, HTTP: srv.Client()}
		name, err := s.Check(context.Background(), Bot{token: "xoxb-good xapp-good"})
		if err != nil {
			t.Fatalf("Check() unexpected error: %v", err)
		}
		if name != "go24_slack_bot" {
			t.Errorf("Check() = %q, want go24_slack_bot", name)
		}
	})

	t.Run("api auth error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
		}))
		defer srv.Close()

		s := &Slack{API: srv.URL, HTTP: srv.Client()}
		_, err := s.Check(context.Background(), Bot{token: "xoxb-bad xapp-bad"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var se *slackError
		if !errors.As(err, &se) {
			t.Fatalf("expected *slackError, got %T: %v", err, err)
		}
		if se.Code != "invalid_auth" {
			t.Errorf("se.Code = %q, want invalid_auth", se.Code)
		}
		if se.Error() != "slack: invalid_auth" {
			t.Errorf("Error() = %q, want slack: invalid_auth", se.Error())
		}
	})
}

func TestSlackAccount(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = r.ParseForm()
		if r.PostForm.Get("user") != "U12345" {
			t.Errorf("user = %q, want U12345", r.PostForm.Get("user"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"user":{"name":"alice_slack"}}`)
	}))
	defer srv.Close()

	s := &Slack{API: srv.URL, HTTP: srv.Client()}

	// First call queries API and caches result
	name := s.account(context.Background(), "xoxb-good", "U12345")
	if name != "@alice_slack" {
		t.Errorf("account() = %q, want @alice_slack", name)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}

	// Second call uses cache without network request
	nameCached := s.account(context.Background(), "xoxb-good", "U12345")
	if nameCached != "@alice_slack" {
		t.Errorf("cached account() = %q, want @alice_slack", nameCached)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1 (should be served from cache)", requests)
	}
}

func TestSlackSend(t *testing.T) {
	t.Run("plain message without link", func(t *testing.T) {
		var sentForm url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/chat.postMessage" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer xoxb-bot" {
				t.Errorf("header Authorization = %q, want Bearer xoxb-bot", r.Header.Get("Authorization"))
			}
			_ = r.ParseForm()
			sentForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
		defer srv.Close()

		s := &Slack{API: srv.URL, HTTP: srv.Client()}
		err := s.Send(context.Background(), Bot{token: "xoxb-bot xapp-app"}, "D123", Message{
			Text: "Hello & <Welcome>",
		})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		if sentForm.Get("channel") != "D123" {
			t.Errorf("channel = %q, want D123", sentForm.Get("channel"))
		}
		if sentForm.Get("unfurl_links") != "false" {
			t.Errorf("unfurl_links = %q, want false", sentForm.Get("unfurl_links"))
		}
		expectedText := "Hello &amp; &lt;Welcome&gt;"
		if sentForm.Get("text") != expectedText {
			t.Errorf("text = %q, want %q", sentForm.Get("text"), expectedText)
		}
	})

	t.Run("message with link formatted for slack mrkdwn", func(t *testing.T) {
		var sentForm url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			sentForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
		defer srv.Close()

		s := &Slack{API: srv.URL, HTTP: srv.Client()}
		err := s.Send(context.Background(), Bot{token: "xoxb-bot xapp-app"}, "D123", Message{
			Text: "Task assigned: <Critical>",
			Link: "https://go24.local/tasks/10",
		})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		expectedText := "<https://go24.local/tasks/10|Task assigned: &lt;Critical&gt;>"
		if sentForm.Get("text") != expectedText {
			t.Errorf("text = %q, want %q", sentForm.Get("text"), expectedText)
		}
	})

	t.Run("err gone on deactivated or missing channel errors", func(t *testing.T) {
		goneCodes := []string{"channel_not_found", "is_archived", "not_in_channel", "user_disabled", "account_inactive"}
		for _, code := range goneCodes {
			t.Run(code, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"ok":false,"error":"`+code+`"}`)
				}))
				defer srv.Close()

				s := &Slack{API: srv.URL, HTTP: srv.Client()}
				err := s.Send(context.Background(), Bot{token: "xoxb-bot xapp-app"}, "D123", Message{Text: "Test"})
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, ErrGone) {
					t.Errorf("expected ErrGone for code %s, got %v", code, err)
				}
			})
		}
	})

	t.Run("other error on non-gone error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":false,"error":"rate_limited"}`)
		}))
		defer srv.Close()

		s := &Slack{API: srv.URL, HTTP: srv.Client()}
		err := s.Send(context.Background(), Bot{token: "xoxb-bot xapp-app"}, "D123", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, ErrGone) {
			t.Errorf("did not expect ErrGone for rate_limited, got %v", err)
		}
	})
}

func TestSlackRead(t *testing.T) {
	// Tests s.read parsing socket mode envelopes
	var serverConn *websocket.Conn
	var connReady sync.WaitGroup
	connReady.Add(1)

	var acksMu sync.Mutex
	acks := []string{}

	ackChan := make(chan string, 10)

	wsSrv := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		serverConn = ws
		connReady.Done()
		for {
			var ack struct {
				EnvelopeID string `json:"envelope_id"`
			}
			if err := websocket.JSON.Receive(ws, &ack); err != nil {
				return
			}
			acksMu.Lock()
			acks = append(acks, ack.EnvelopeID)
			acksMu.Unlock()
			ackChan <- ack.EnvelopeID
		}
	}))
	defer wsSrv.Close()

	clientURL := strings.Replace(wsSrv.URL, "http://", "ws://", 1)
	clientConn, err := websocket.Dial(clientURL, "", "http://localhost")
	if err != nil {
		t.Fatalf("websocket.Dial error: %v", err)
	}
	defer clientConn.Close()

	connReady.Wait()

	s := &Slack{idle: time.Hour}
	sock := &slackSocket{polled: time.Now()}

	// Run s.read in background
	readDone := make(chan struct{})
	go func() {
		s.read(sock, clientConn)
		close(readDone)
	}()

	// Send events and wait for each acknowledgment deterministically without time.Sleep
	sendAndAck := func(envID, channelType, channel, user, text, botID string) {
		_ = websocket.JSON.Send(serverConn, map[string]any{
			"type":        "events_api",
			"envelope_id": envID,
			"payload": map[string]any{
				"event": map[string]any{
					"type":         "message",
					"channel_type": channelType,
					"channel":      channel,
					"user":         user,
					"text":         text,
					"bot_id":       botID,
				},
			},
		})
		select {
		case got := <-ackChan:
			if got != envID {
				t.Errorf("got ack %q, want %q", got, envID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for ack of %s", envID)
		}
	}

	// 1. Direct message
	sendAndAck("env_1", "im", "D_ALICE", "U_ALICE", "link-code-123", "")
	// 2. Direct message with Russian "СТОП" converted to "/stop"
	sendAndAck("env_2", "im", "D_BOB", "U_BOB", " СТОП ", "")
	// 3. Bot message (filtered out)
	sendAndAck("env_3", "im", "D_ALICE", "U_BOT", "bot message", "B123")
	// 4. Channel message (filtered out)
	sendAndAck("env_4", "channel", "C_GENERAL", "U_ALICE", "channel message", "")

	// 5. Send disconnect event to terminate read loop
	_ = websocket.JSON.Send(serverConn, map[string]any{"type": "disconnect"})
	<-readDone

	// Check updates queued in socket
	sock.mu.Lock()
	queue := sock.queue
	sock.mu.Unlock()

	if len(queue) != 2 {
		t.Fatalf("len(queue) = %d, want 2", len(queue))
	}
	if queue[0].Chat != "D_ALICE" || queue[0].Account != "U_ALICE" || queue[0].Text != "link-code-123" {
		t.Errorf("queue[0] = %+v", queue[0])
	}
	if queue[1].Chat != "D_BOB" || queue[1].Account != "U_BOB" || queue[1].Text != "/stop" {
		t.Errorf("queue[1] = %+v, want converted /stop", queue[1])
	}

	acksMu.Lock()
	defer acksMu.Unlock()
	expectedAcks := []string{"env_1", "env_2", "env_3", "env_4"}
	if len(acks) != len(expectedAcks) {
		t.Errorf("acks = %v, want %v", acks, expectedAcks)
	}
	if len(acks) != len(expectedAcks) {
		t.Errorf("acks = %v, want %v", acks, expectedAcks)
	}
}
