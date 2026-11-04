package notifyout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestMattermostMetadata(t *testing.T) {
	mm := &Mattermost{}
	if got := mm.Name(); got != "mattermost" {
		t.Errorf("Mattermost.Name() = %q, want %q", got, "mattermost")
	}
	if got := mm.NeedsServer(); got != true {
		t.Errorf("Mattermost.NeedsServer() = %v, want true", got)
	}
	if got := mm.LinkURL(Bot{Name: "mmbot"}, "code"); got != "" {
		t.Errorf("Mattermost.LinkURL() = %q, want empty string", got)
	}
}

func TestMattermostNoServer(t *testing.T) {
	mm := &Mattermost{HTTP: http.DefaultClient}
	b := Bot{token: "tok", Server: ""}

	if _, err := mm.Check(context.Background(), b); !errors.Is(err, errServer) {
		t.Errorf("Check() err = %v, want errServer", err)
	}
	if _, _, err := mm.Updates(context.Background(), b, 100); !errors.Is(err, errServer) {
		t.Errorf("Updates() err = %v, want errServer", err)
	}
	if err := mm.Send(context.Background(), b, "ch1", Message{Text: "hi"}); !errors.Is(err, errServer) {
		t.Errorf("Send() err = %v, want errServer", err)
	}
}

func TestMattermostCheck(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v4/users/me" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer mm_tok" {
				t.Errorf("header Authorization = %q, want Bearer mm_tok", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"bot_id_1","username":"go24_mm_bot"}`)
		}))
		defer srv.Close()

		mm := &Mattermost{HTTP: srv.Client()}
		name, err := mm.Check(context.Background(), Bot{Server: srv.URL, token: "mm_tok"})
		if err != nil {
			t.Fatalf("Check() unexpected error: %v", err)
		}
		if name != "go24_mm_bot" {
			t.Errorf("Check() = %q, want go24_mm_bot", name)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"Invalid session token"}`)
		}))
		defer srv.Close()

		mm := &Mattermost{HTTP: srv.Client()}
		_, err := mm.Check(context.Background(), Bot{Server: srv.URL, token: "bad_tok"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var me *mattermostError
		if !errors.As(err, &me) {
			t.Fatalf("expected *mattermostError, got %T: %v", err, err)
		}
		if me.Status != 401 || me.Message != "Invalid session token" {
			t.Errorf("me = %+v", me)
		}
		if me.Error() != "mattermost: 401 Invalid session token" {
			t.Errorf("Error() = %q, want %q", me.Error(), "mattermost: 401 Invalid session token")
		}
	})
}

func TestMattermostUpdates(t *testing.T) {
	var mu sync.Mutex
	userFetches := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case path == "/api/v4/users/me":
			_, _ = io.WriteString(w, `{"id":"bot_id","username":"bot"}`)
		case path == "/api/v4/users/me/teams":
			_, _ = io.WriteString(w, `[{"id":"team_1"}]`)
		case path == "/api/v4/users/me/teams/team_1/channels":
			// Returns direct channel (type "D") with last_post > next, and a public channel (type "O")
			_, _ = io.WriteString(w, `[
				{"id":"direct_chan_1","type":"D","last_post_at":1050},
				{"id":"public_chan_1","type":"O","last_post_at":1080},
				{"id":"old_direct_chan","type":"D","last_post_at":500}
			]`)
		case path == "/api/v4/channels/direct_chan_1/posts":
			since := r.URL.Query().Get("since")
			if since != "1000" {
				t.Errorf("since = %q, want 1000", since)
			}
			_, _ = io.WriteString(w, `{
				"posts": {
					"p1": {
						"user_id": "user_alice",
						"message": "  hello mattermost  ",
						"create_at": 1020,
						"channel_id": "direct_chan_1",
						"type": ""
					},
					"p2": {
						"user_id": "user_alice",
						"message": "second message",
						"create_at": 1040,
						"channel_id": "direct_chan_1",
						"type": ""
					},
					"p_bot": {
						"user_id": "bot_id",
						"message": "bot reply",
						"create_at": 1030,
						"channel_id": "direct_chan_1",
						"type": ""
					},
					"p_system": {
						"user_id": "user_alice",
						"message": "joined channel",
						"create_at": 1035,
						"channel_id": "direct_chan_1",
						"type": "system_join_channel"
					},
					"p_old": {
						"user_id": "user_alice",
						"message": "old post",
						"create_at": 990,
						"channel_id": "direct_chan_1",
						"type": ""
					}
				}
			}`)
		case path == "/api/v4/users/user_alice":
			mu.Lock()
			userFetches++
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"user_alice","username":"alice"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	fixedTime := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	mm := &Mattermost{
		HTTP: srv.Client(),
		now:  func() time.Time { return fixedTime },
	}
	bot := Bot{Server: srv.URL, token: "mm_tok"}

	// 1. Initial call with next == 0: returns current time in ms without querying posts
	ups0, next0, err0 := mm.Updates(context.Background(), bot, 0)
	if err0 != nil {
		t.Fatalf("Updates(0) error: %v", err0)
	}
	if len(ups0) != 0 {
		t.Errorf("len(ups0) = %d, want 0", len(ups0))
	}
	if next0 != fixedTime.UnixMilli() {
		t.Errorf("next0 = %d, want %d", next0, fixedTime.UnixMilli())
	}

	// 2. Call with next == 1000: collects posts, sorts by CreateAt, resolves username with cache
	ups, next, err := mm.Updates(context.Background(), bot, 1000)
	if err != nil {
		t.Fatalf("Updates(1000) error: %v", err)
	}

	if len(ups) != 2 {
		t.Fatalf("len(ups) = %d, want 2 (bot own, system, and old posts skipped)", len(ups))
	}
	// Post 1 (create_at 1020)
	if ups[0].Chat != "direct_chan_1" || ups[0].Account != "@alice" || ups[0].Text != "hello mattermost" {
		t.Errorf("ups[0] = %+v", ups[0])
	}
	// Post 2 (create_at 1040)
	if ups[1].Chat != "direct_chan_1" || ups[1].Account != "@alice" || ups[1].Text != "second message" {
		t.Errorf("ups[1] = %+v", ups[1])
	}
	if next != 1040 {
		t.Errorf("next = %d, want 1040", next)
	}

	// Username lookup should be cached per user_id
	if userFetches != 1 {
		t.Errorf("userFetches = %d, want 1 (should be cached)", userFetches)
	}
}

func TestMattermostSend(t *testing.T) {
	t.Run("plain message without link", func(t *testing.T) {
		var sentBody map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v4/posts" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer mm_tok" {
				t.Errorf("header Authorization = %q, want Bearer mm_tok", r.Header.Get("Authorization"))
			}
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"post123"}`)
		}))
		defer srv.Close()

		mm := &Mattermost{HTTP: srv.Client()}
		err := mm.Send(context.Background(), Bot{Server: srv.URL, token: "mm_tok"}, "chan_101", Message{
			Text: "New task notification",
		})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		if sentBody["channel_id"] != "chan_101" {
			t.Errorf("channel_id = %q, want chan_101", sentBody["channel_id"])
		}
		if sentBody["message"] != "New task notification" {
			t.Errorf("message = %q, want New task notification", sentBody["message"])
		}
	})

	t.Run("markdown link with bracket escaping", func(t *testing.T) {
		var sentBody map[string]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"post124"}`)
		}))
		defer srv.Close()

		mm := &Mattermost{HTTP: srv.Client()}
		err := mm.Send(context.Background(), Bot{Server: srv.URL, token: "mm_tok"}, "chan_101", Message{
			Text: "Task [GO-123] Review [Urgent]",
			Link: "https://mm.local/tasks/123",
		})
		if err != nil {
			t.Fatalf("Send() unexpected error: %v", err)
		}

		expectedMessage := `[Task \[GO-123\] Review \[Urgent\]](https://mm.local/tasks/123)`
		if sentBody["message"] != expectedMessage {
			t.Errorf("message = %q, want %q", sentBody["message"], expectedMessage)
		}
	})

	t.Run("err gone on 403 forbidden", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"User is deactivated"}`)
		}))
		defer srv.Close()

		mm := &Mattermost{HTTP: srv.Client()}
		err := mm.Send(context.Background(), Bot{Server: srv.URL, token: "mm_tok"}, "chan_101", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected ErrGone on 403, got %v", err)
		}
	})

	t.Run("err gone on 404 not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"Channel does not exist"}`)
		}))
		defer srv.Close()

		mm := &Mattermost{HTTP: srv.Client()}
		err := mm.Send(context.Background(), Bot{Server: srv.URL, token: "mm_tok"}, "chan_101", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected ErrGone on 404, got %v", err)
		}
	})

	t.Run("other error on 500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"Internal database error"}`)
		}))
		defer srv.Close()

		mm := &Mattermost{HTTP: srv.Client()}
		err := mm.Send(context.Background(), Bot{Server: srv.URL, token: "mm_tok"}, "chan_101", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, ErrGone) {
			t.Errorf("did not expect ErrGone on 500, got %v", err)
		}
	})
}
