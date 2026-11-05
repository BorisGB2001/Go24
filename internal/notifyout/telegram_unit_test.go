package notifyout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramMetadata(t *testing.T) {
	tg := &Telegram{}
	if got := tg.Name(); got != "telegram" {
		t.Errorf("Telegram.Name() = %q, want %q", got, "telegram")
	}
	if got := tg.NeedsServer(); got != false {
		t.Errorf("Telegram.NeedsServer() = %v, want false", got)
	}
}

func TestTelegramLinkURL(t *testing.T) {
	tg := &Telegram{}
	tests := []struct {
		name string
		bot  Bot
		code string
		want string
	}{
		{
			name: "empty bot name",
			bot:  Bot{Name: ""},
			code: "code123",
			want: "",
		},
		{
			name: "valid bot name and code",
			bot:  Bot{Name: "my_test_bot"},
			code: "auth_token_456",
			want: "https://t.me/my_test_bot?start=auth_token_456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tg.LinkURL(tt.bot, tt.code); got != tt.want {
				t.Errorf("Telegram.LinkURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTelegramCheck(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/botsecret_token/getMe" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.Method != http.MethodPost {
				t.Errorf("unexpected method: %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":{"username":"go24_unit_bot"}}`)
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		name, err := tg.Check(context.Background(), Bot{token: "secret_token"})
		if err != nil {
			t.Fatalf("Check() unexpected error: %v", err)
		}
		if name != "go24_unit_bot" {
			t.Errorf("Check() = %q, want %q", name, "go24_unit_bot")
		}
	})

	t.Run("unauthorized", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		_, err := tg.Check(context.Background(), Bot{token: "bad_token"})
		if err == nil {
			t.Fatal("Check() expected error, got nil")
		}
		var te *telegramError
		if !errors.As(err, &te) {
			t.Fatalf("expected *telegramError, got %T: %v", err, err)
		}
		if te.Code != 401 || te.Description != "Unauthorized" {
			t.Errorf("te = %+v, want Code 401, Description Unauthorized", te)
		}
		if te.Error() != "telegram: 401 Unauthorized" {
			t.Errorf("Error() = %q, want %q", te.Error(), "telegram: 401 Unauthorized")
		}
	})

	t.Run("http status error on decode", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "Bad Gateway")
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		_, err := tg.Check(context.Background(), Bot{token: "bad_token"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestTelegramUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botvalid_token/getUpdates" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode getUpdates body: %v", err)
		}
		if req["offset"] != float64(10) {
			t.Errorf("req[offset] = %v, want 10", req["offset"])
		}

		resp := `{
			"ok": true,
			"result": [
				{
					"update_id": 10,
					"message": {
						"text": "/start abc",
						"chat": {"id": 101, "type": "private"},
						"from": {"username": "alice", "first_name": "Alice", "last_name": "Smith"}
					}
				},
				{
					"update_id": 11,
					"message": {
						"text": "hi",
						"chat": {"id": 102, "type": "private"},
						"from": {"first_name": "Bob", "last_name": "Jones"}
					}
				},
				{
					"update_id": 12,
					"message": {
						"text": "only first name",
						"chat": {"id": 103, "type": "private"},
						"from": {"first_name": "Charlie"}
					}
				},
				{
					"update_id": 13,
					"message": {
						"text": "group message",
						"chat": {"id": 200, "type": "group", "title": "Team"},
						"from": {"id": 7, "first_name": "Dave"}
					}
				},
				{
					"update_id": 14
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	}))
	defer srv.Close()

	tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
	ups, next, err := tg.Updates(context.Background(), Bot{token: "valid_token"}, 10)
	if err != nil {
		t.Fatalf("Updates() unexpected error: %v", err)
	}

	if next != 15 {
		t.Errorf("Updates() next = %d, want 15 (max update_id + 1)", next)
	}
	if len(ups) != 4 {
		t.Fatalf("len(ups) = %d, want 4 (nil message skipped)", len(ups))
	}

	// Update 1: username prioritized with @
	if ups[0].Chat != "101" || ups[0].Account != "@alice" || ups[0].Text != "/start abc" {
		t.Errorf("ups[0] = %+v, want Chat=101, Account=@alice, Text=/start abc", ups[0])
	}
	// Update 2: first name + last name
	if ups[1].Chat != "102" || ups[1].Account != "Bob Jones" || ups[1].Text != "hi" {
		t.Errorf("ups[1] = %+v, want Chat=102, Account=Bob Jones, Text=hi", ups[1])
	}
	// Update 3: first name only
	if ups[2].Chat != "103" || ups[2].Account != "Charlie" || ups[2].Text != "only first name" {
		t.Errorf("ups[2] = %+v, want Chat=103, Account=Charlie, Text=only first name", ups[2])
	}
	// Update 4: a group's message, with the group's name and who wrote
	if u := ups[3]; !u.Group || u.Chat != "200" || u.Title != "Team" || u.From != "7" || u.Name != "Dave" || u.Text != "group message" {
		t.Errorf("ups[3] = %+v, want a group message of Dave in Team", u)
	}
}

func TestTelegramSend(t *testing.T) {
	t.Run("plain message without link", func(t *testing.T) {
		var sentBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/botgood_tok/sendMessage" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		err := tg.Send(context.Background(), Bot{token: "good_tok"}, "101", Message{Text: "Task assigned"})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		if sentBody["chat_id"] != "101" {
			t.Errorf("chat_id = %v, want 101", sentBody["chat_id"])
		}
		if sentBody["text"] != "Task assigned" {
			t.Errorf("text = %v, want Task assigned", sentBody["text"])
		}
		if sentBody["parse_mode"] != nil {
			t.Errorf("parse_mode = %v, want nil", sentBody["parse_mode"])
		}
		opts, _ := sentBody["link_preview_options"].(map[string]any)
		if opts == nil || opts["is_disabled"] != true {
			t.Errorf("link_preview_options = %v, want is_disabled: true", sentBody["link_preview_options"])
		}
	})

	t.Run("html formatted message with link", func(t *testing.T) {
		var sentBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":43}}`)
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		err := tg.Send(context.Background(), Bot{token: "good_tok"}, "101", Message{
			Text: "Task: <Critical> & Important",
			Link: "https://go24.local/tasks/1?a=1&b=2",
		})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		if sentBody["parse_mode"] != "HTML" {
			t.Errorf("parse_mode = %v, want HTML", sentBody["parse_mode"])
		}
		expectedText := `<a href="https://go24.local/tasks/1?a=1&amp;b=2">Task: &lt;Critical&gt; &amp; Important</a>`
		if sentBody["text"] != expectedText {
			t.Errorf("text = %q, want %q", sentBody["text"], expectedText)
		}
	})

	t.Run("err gone on 403 forbidden", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		err := tg.Send(context.Background(), Bot{token: "tok"}, "101", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected error to wrap ErrGone, got %v", err)
		}
	})

	t.Run("err gone on 400 chat not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		err := tg.Send(context.Background(), Bot{token: "tok"}, "999", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected error to wrap ErrGone, got %v", err)
		}
	})

	t.Run("regular error on 400 other error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`)
		}))
		defer srv.Close()

		tg := &Telegram{API: srv.URL, HTTP: srv.Client()}
		err := tg.Send(context.Background(), Bot{token: "tok"}, "101", Message{Text: strings.Repeat("A", 5000)})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, ErrGone) {
			t.Errorf("did not expect ErrGone for message too long, got %v", err)
		}
	})
}
