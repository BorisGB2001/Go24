package notifyout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMaxMetadata(t *testing.T) {
	m := &Max{}
	if got := m.Name(); got != "max" {
		t.Errorf("Max.Name() = %q, want %q", got, "max")
	}
	if got := m.NeedsServer(); got != false {
		t.Errorf("Max.NeedsServer() = %v, want false", got)
	}
}

func TestMaxLinkURL(t *testing.T) {
	m := &Max{}
	tests := []struct {
		name string
		bot  Bot
		code string
		want string
	}{
		{
			name: "empty bot name",
			bot:  Bot{Name: ""},
			code: "c1",
			want: "",
		},
		{
			name: "valid bot name and code",
			bot:  Bot{Name: "max_help_bot"},
			code: "start_code_99",
			want: "https://max.ru/max_help_bot?start=start_code_99",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.LinkURL(tt.bot, tt.code); got != tt.want {
				t.Errorf("Max.LinkURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMaxCheck(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/me" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "max_tok_1" {
				t.Errorf("header Authorization = %q, want max_tok_1", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("access_token") != "max_tok_1" {
				t.Errorf("query access_token = %q, want max_tok_1", r.URL.Query().Get("access_token"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"username":"bot_user","name":"Bot Name"}`)
		}))
		defer srv.Close()

		m := &Max{API: srv.URL, HTTP: srv.Client()}
		name, err := m.Check(context.Background(), Bot{token: "max_tok_1"})
		if err != nil {
			t.Fatalf("Check() unexpected error: %v", err)
		}
		if name != "bot_user" {
			t.Errorf("Check() = %q, want bot_user", name)
		}
	})

	t.Run("api error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"invalid_token","message":"The token is expired"}`)
		}))
		defer srv.Close()

		m := &Max{API: srv.URL, HTTP: srv.Client()}
		_, err := m.Check(context.Background(), Bot{token: "bad_tok"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var me *maxError
		if !errors.As(err, &me) {
			t.Fatalf("expected *maxError, got %T: %v", err, err)
		}
		if me.Status != 401 || me.Code != "invalid_token" || me.Message != "The token is expired" {
			t.Errorf("me = %+v, want Status 401, Code invalid_token, Message The token is expired", me)
		}
		if me.Error() != "max: 401 invalid_token The token is expired" {
			t.Errorf("Error() = %q, want %q", me.Error(), "max: 401 invalid_token The token is expired")
		}
	})
}

func TestMaxUpdates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/updates" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("marker") != "50" {
			t.Errorf("marker = %q, want 50", q.Get("marker"))
		}
		if q.Get("types") != "message_created,bot_started" {
			t.Errorf("types = %q, want message_created,bot_started", q.Get("types"))
		}

		resp := `{
			"marker": 75,
			"updates": [
				{
					"update_type": "bot_started",
					"chat_id": 501,
					"user": {"username": "alex", "name": "Alex"},
					"payload": "secretcode"
				},
				{
					"update_type": "bot_started",
					"chat_id": 0,
					"user": {"name": "Ignored"},
					"payload": "ignored"
				},
				{
					"update_type": "message_created",
					"message": {
						"sender": {"name": "Daria"},
						"recipient": {"chat_id": 502, "chat_type": "dialog"},
						"body": {"text": "hello from max"}
					}
				},
				{
					"update_type": "message_created",
					"message": {
						"sender": {"username": "ivan", "name": "Ivan"},
						"recipient": {"chat_id": 999, "chat_type": "chat"},
						"body": {"text": "group message"}
					}
				},
				{
					"update_type": "unknown_event"
				}
			]
		}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, resp)
	}))
	defer srv.Close()

	m := &Max{API: srv.URL, HTTP: srv.Client()}
	ups, next, err := m.Updates(context.Background(), Bot{token: "max_tok"}, 50)
	if err != nil {
		t.Fatalf("Updates() unexpected error: %v", err)
	}

	if next != 75 {
		t.Errorf("Updates() next = %d, want 75", next)
	}
	if len(ups) != 2 {
		t.Fatalf("len(ups) = %d, want 2", len(ups))
	}

	// First update: bot_started
	if ups[0].Chat != "501" || ups[0].Account != "@alex" || ups[0].Text != "/start secretcode" {
		t.Errorf("ups[0] = %+v, want Chat=501, Account=@alex, Text=/start secretcode", ups[0])
	}
	// Second update: message_created dialog, fallback to user Name when Username is empty
	if ups[1].Chat != "502" || ups[1].Account != "Daria" || ups[1].Text != "hello from max" {
		t.Errorf("ups[1] = %+v, want Chat=502, Account=Daria, Text=hello from max", ups[1])
	}
}

func TestMaxSend(t *testing.T) {
	t.Run("plain message without link", func(t *testing.T) {
		var sentBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/messages" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.URL.Query().Get("chat_id") != "501" {
				t.Errorf("query chat_id = %s, want 501", r.URL.Query().Get("chat_id"))
			}
			if r.URL.Query().Get("disable_link_preview") != "true" {
				t.Errorf("disable_link_preview = %s, want true", r.URL.Query().Get("disable_link_preview"))
			}
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		m := &Max{API: srv.URL, HTTP: srv.Client()}
		err := m.Send(context.Background(), Bot{token: "tok"}, "501", Message{Text: "Plain notification"})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		if sentBody["text"] != "Plain notification" {
			t.Errorf("text = %v, want Plain notification", sentBody["text"])
		}
		if sentBody["format"] != nil {
			t.Errorf("format = %v, want nil", sentBody["format"])
		}
	})

	t.Run("html formatted message with link", func(t *testing.T) {
		var sentBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		m := &Max{API: srv.URL, HTTP: srv.Client()}
		err := m.Send(context.Background(), Bot{token: "tok"}, "501", Message{
			Text: "Alert: <System> & Status",
			Link: "https://max.ru/notif/1?foo=bar&baz=1",
		})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		if sentBody["format"] != "html" {
			t.Errorf("format = %v, want html", sentBody["format"])
		}
		expectedText := `<a href="https://max.ru/notif/1?foo=bar&amp;baz=1">Alert: &lt;System&gt; &amp; Status</a>`
		if sentBody["text"] != expectedText {
			t.Errorf("text = %q, want %q", sentBody["text"], expectedText)
		}
	})

	t.Run("err gone on 403 forbidden", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"forbidden","message":"user blocked the bot"}`)
		}))
		defer srv.Close()

		m := &Max{API: srv.URL, HTTP: srv.Client()}
		err := m.Send(context.Background(), Bot{token: "tok"}, "501", Message{Text: "test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected ErrGone, got %v", err)
		}
	})

	t.Run("err gone on 404 not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"chat_not_found","message":"no such chat"}`)
		}))
		defer srv.Close()

		m := &Max{API: srv.URL, HTTP: srv.Client()}
		err := m.Send(context.Background(), Bot{token: "tok"}, "501", Message{Text: "test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected ErrGone, got %v", err)
		}
	})

	t.Run("other error on 500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"code":"internal_error","message":"server error"}`)
		}))
		defer srv.Close()

		m := &Max{API: srv.URL, HTTP: srv.Client()}
		err := m.Send(context.Background(), Bot{token: "tok"}, "501", Message{Text: "test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, ErrGone) {
			t.Errorf("did not expect ErrGone for status 500, got %v", err)
		}
	})
}
