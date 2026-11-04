package notifyout

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMatrixMetadata(t *testing.T) {
	mx := &Matrix{}
	if got := mx.Name(); got != "matrix" {
		t.Errorf("Matrix.Name() = %q, want %q", got, "matrix")
	}
	if got := mx.NeedsServer(); got != true {
		t.Errorf("Matrix.NeedsServer() = %v, want true", got)
	}
	if got := mx.LinkURL(Bot{Name: "bot"}, "123"); got != "" {
		t.Errorf("Matrix.LinkURL() = %q, want empty string", got)
	}
}

func TestMatrixNoServer(t *testing.T) {
	mx := &Matrix{HTTP: http.DefaultClient}
	b := Bot{token: "tok", Server: ""}

	if _, err := mx.Check(context.Background(), b); !errors.Is(err, errServer) {
		t.Errorf("Check() err = %v, want errServer", err)
	}
	if _, _, err := mx.Updates(context.Background(), b, 0); !errors.Is(err, errServer) {
		t.Errorf("Updates() err = %v, want errServer", err)
	}
	if err := mx.Send(context.Background(), b, "!room:hs", Message{Text: "hi"}); !errors.Is(err, errServer) {
		t.Errorf("Send() err = %v, want errServer", err)
	}
}

func TestMatrixCheck(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/_matrix/client/v3/account/whoami" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer good_tok" {
				t.Errorf("header Authorization = %q, want Bearer good_tok", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"user_id":"@go24bot:matrix.local"}`)
		}))
		defer srv.Close()

		mx := &Matrix{HTTP: srv.Client()}
		name, err := mx.Check(context.Background(), Bot{Server: srv.URL, token: "good_tok"})
		if err != nil {
			t.Fatalf("Check() unexpected error: %v", err)
		}
		if name != "@go24bot:matrix.local" {
			t.Errorf("Check() = %q, want @go24bot:matrix.local", name)
		}
	})

	t.Run("api error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Invalid access token"}`)
		}))
		defer srv.Close()

		mx := &Matrix{HTTP: srv.Client()}
		_, err := mx.Check(context.Background(), Bot{Server: srv.URL, token: "bad_tok"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var me *matrixError
		if !errors.As(err, &me) {
			t.Fatalf("expected *matrixError, got %T: %v", err, err)
		}
		if me.Status != 401 || me.Code != "M_UNKNOWN_TOKEN" || me.Message != "Invalid access token" {
			t.Errorf("me = %+v", me)
		}
		if me.Error() != "matrix: 401 M_UNKNOWN_TOKEN Invalid access token" {
			t.Errorf("Error() = %q, want %q", me.Error(), "matrix: 401 M_UNKNOWN_TOKEN Invalid access token")
		}
	})
}

func TestMatrixUpdates(t *testing.T) {
	var mu sync.Mutex
	joinedRooms := []string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		path := r.URL.Path
		switch {
		case path == "/_matrix/client/v3/account/whoami":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"user_id":"@bot:hs.test"}`)
		case strings.HasPrefix(path, "/_matrix/client/v3/rooms/") && strings.HasSuffix(path, "/join"):
			room := strings.TrimSuffix(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/join")
			joinedRooms = append(joinedRooms, room)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"room_id":"`+room+`"}`)
		case path == "/_matrix/client/v3/sync":
			since := r.URL.Query().Get("since")
			w.Header().Set("Content-Type", "application/json")
			if since == "" {
				// Initial sync
				_, _ = io.WriteString(w, `{
					"next_batch": "batch_token_1",
					"rooms": {
						"invite": {"!invite_room:hs": {}},
						"join": {}
					}
				}`)
			} else {
				// Subsequent sync
				_, _ = io.WriteString(w, `{
					"next_batch": "batch_token_2",
					"rooms": {
						"invite": {},
						"join": {
							"!direct_room:hs": {
								"timeline": {
									"events": [
										{
											"type": "m.room.message",
											"sender": "@user:hs.test",
											"origin_server_ts": 1005,
											"content": {"msgtype": "m.text", "body": "hello matrix"}
										},
										{
											"type": "m.room.message",
											"sender": "@bot:hs.test",
											"origin_server_ts": 1006,
											"content": {"msgtype": "m.text", "body": "bot message"}
										},
										{
											"type": "m.room.message",
											"sender": "@user:hs.test",
											"origin_server_ts": 1000,
											"content": {"msgtype": "m.text", "body": "old message"}
										},
										{
											"type": "m.room.member",
											"sender": "@user:hs.test",
											"origin_server_ts": 1007,
											"content": {"msgtype": "m.text", "body": "join event"}
										}
									]
								}
							},
							"!group_room:hs": {
								"timeline": {
									"events": [
										{
											"type": "m.room.message",
											"sender": "@user2:hs.test",
											"origin_server_ts": 1010,
											"content": {"msgtype": "m.text", "body": "group msg"}
										}
									]
								}
							}
						}
					}
				}`)
			}
		case strings.HasPrefix(path, "/_matrix/client/v3/rooms/") && strings.HasSuffix(path, "/joined_members"):
			room := strings.TrimSuffix(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/joined_members")
			w.Header().Set("Content-Type", "application/json")
			if room == "!direct_room:hs" {
				// 2 members -> direct chat
				_, _ = io.WriteString(w, `{"joined":{"@bot:hs.test":{},"@user:hs.test":{}}}`)
			} else {
				// 3 members -> group chat
				_, _ = io.WriteString(w, `{"joined":{"@bot:hs.test":{},"@user2:hs.test":{},"@user3:hs.test":{}}}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	fixedTime := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	mx := &Matrix{
		HTTP: srv.Client(),
		now:  func() time.Time { return fixedTime },
	}
	bot := Bot{Server: srv.URL, token: "tok_matrix"}

	// 1. Initial call with next == 0: joins invite, returns fixed timestamp
	ups, next, err := mx.Updates(context.Background(), bot, 0)
	if err != nil {
		t.Fatalf("Updates(next=0) error: %v", err)
	}
	if len(ups) != 0 {
		t.Errorf("Updates(next=0) returned %d updates, want 0", len(ups))
	}
	if next != fixedTime.UnixMilli() {
		t.Errorf("Updates(next=0) next = %d, want %d", next, fixedTime.UnixMilli())
	}
	if len(joinedRooms) != 1 || joinedRooms[0] != "!invite_room:hs" {
		t.Errorf("joinedRooms = %v, want [!invite_room:hs]", joinedRooms)
	}

	// 2. Subsequent call with next > 0: processes timeline events
	ups2, next2, err2 := mx.Updates(context.Background(), bot, 1000)
	if err2 != nil {
		t.Fatalf("Updates(next=1000) error: %v", err2)
	}
	if len(ups2) != 1 {
		t.Fatalf("Updates(next=1000) returned %d updates, want 1", len(ups2))
	}
	if ups2[0].Chat != "!direct_room:hs" || ups2[0].Account != "@user:hs.test" || ups2[0].Text != "hello matrix" {
		t.Errorf("ups2[0] = %+v, want Chat=!direct_room:hs, Account=@user:hs.test, Text=hello matrix", ups2[0])
	}
	// next2 tracks the max timestamp seen among valid message events across joined rooms (including group chat 1010)
	if next2 != 1010 {
		t.Errorf("next2 = %d, want 1010", next2)
	}
}

func TestMatrixSend(t *testing.T) {
	t.Run("plain message without link", func(t *testing.T) {
		var sentBody map[string]any
		var sentPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sentPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"event_id":"$event1"}`)
		}))
		defer srv.Close()

		mx := &Matrix{HTTP: srv.Client()}
		err := mx.Send(context.Background(), Bot{Server: srv.URL, token: "tok"}, "!room:hs", Message{Text: "Simple task"})
		if err != nil {
			t.Fatalf("Send() error: %v", err)
		}

		if !strings.HasPrefix(sentPath, "/_matrix/client/v3/rooms/!room:hs/send/m.room.message/") {
			t.Errorf("sentPath = %q, want prefix /_matrix/client/v3/rooms/!room:hs/send/m.room.message/", sentPath)
		}
		if sentBody["msgtype"] != "m.text" || sentBody["body"] != "Simple task" {
			t.Errorf("sentBody = %+v", sentBody)
		}
		if sentBody["format"] != nil || sentBody["formatted_body"] != nil {
			t.Errorf("sentBody format/formatted_body should be nil, got %+v", sentBody)
		}
	})

	t.Run("formatted message with link", func(t *testing.T) {
		var sentBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"event_id":"$event2"}`)
		}))
		defer srv.Close()

		mx := &Matrix{HTTP: srv.Client()}
		err := mx.Send(context.Background(), Bot{Server: srv.URL, token: "tok"}, "!room:hs", Message{
			Text: "Deploy <v1> & ready",
			Link: "https://matrix.org/view?id=1&ref=2",
		})
		if err != nil {
			t.Fatalf("Send() error: %v", err)
		}

		if sentBody["msgtype"] != "m.text" {
			t.Errorf("msgtype = %v, want m.text", sentBody["msgtype"])
		}
		if sentBody["body"] != "Deploy <v1> & ready https://matrix.org/view?id=1&ref=2" {
			t.Errorf("body = %v", sentBody["body"])
		}
		if sentBody["format"] != "org.matrix.custom.html" {
			t.Errorf("format = %v, want org.matrix.custom.html", sentBody["format"])
		}
		expectedHTML := `<a href="https://matrix.org/view?id=1&amp;ref=2">Deploy &lt;v1&gt; &amp; ready</a>`
		if sentBody["formatted_body"] != expectedHTML {
			t.Errorf("formatted_body = %q, want %q", sentBody["formatted_body"], expectedHTML)
		}
	})

	t.Run("err gone on 403 forbidden", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"errcode":"M_FORBIDDEN","error":"You are not a member of this room"}`)
		}))
		defer srv.Close()

		mx := &Matrix{HTTP: srv.Client()}
		err := mx.Send(context.Background(), Bot{Server: srv.URL, token: "tok"}, "!room:hs", Message{Text: "Test"})
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
			_, _ = io.WriteString(w, `{"errcode":"M_NOT_FOUND","error":"Room not found"}`)
		}))
		defer srv.Close()

		mx := &Matrix{HTTP: srv.Client()}
		err := mx.Send(context.Background(), Bot{Server: srv.URL, token: "tok"}, "!room:hs", Message{Text: "Test"})
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
			_, _ = io.WriteString(w, `{"errcode":"M_UNKNOWN","error":"Internal server error"}`)
		}))
		defer srv.Close()

		mx := &Matrix{HTTP: srv.Client()}
		err := mx.Send(context.Background(), Bot{Server: srv.URL, token: "tok"}, "!room:hs", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, ErrGone) {
			t.Errorf("did not expect ErrGone on 500, got %v", err)
		}
	})
}
