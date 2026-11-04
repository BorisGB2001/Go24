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
)

func TestVKMetadata(t *testing.T) {
	vk := &VK{}
	if got := vk.Name(); got != "vk" {
		t.Errorf("VK.Name() = %q, want %q", got, "vk")
	}
	if got := vk.NeedsServer(); got != false {
		t.Errorf("VK.NeedsServer() = %v, want false", got)
	}
	if got := vk.LinkURL(Bot{Name: "myclub"}, "123"); got != "" {
		t.Errorf("VK.LinkURL() = %q, want empty string", got)
	}
}

func TestVKCheck(t *testing.T) {
	t.Run("success with screen_name from groups wrapper", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/groups.getById" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			_ = r.ParseForm()
			if r.PostForm.Get("access_token") != "good_token" || r.PostForm.Get("v") != "5.199" {
				t.Errorf("unexpected form: %v", r.PostForm)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"response":{"groups":[{"id":123,"screen_name":"my_vk_group"}]}}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		name, err := vk.Check(context.Background(), Bot{token: "good_token"})
		if err != nil {
			t.Fatalf("Check() error: %v", err)
		}
		if name != "my_vk_group" {
			t.Errorf("Check() = %q, want my_vk_group", name)
		}
	})

	t.Run("success with direct array and empty screen_name fallback", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"response":[{"id":456,"screen_name":""}]}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		name, err := vk.Check(context.Background(), Bot{token: "good_token"})
		if err != nil {
			t.Fatalf("Check() error: %v", err)
		}
		if name != "club456" {
			t.Errorf("Check() = %q, want club456", name)
		}
	})

	t.Run("empty groups list returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"response":{"groups":[]}}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		_, err := vk.Check(context.Background(), Bot{token: "tok"})
		if err == nil || !strings.Contains(err.Error(), "the key is not a community's") {
			t.Errorf("Check() err = %v, want 'the key is not a community's'", err)
		}
	})

	t.Run("vk api error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"error":{"error_code":5,"error_msg":"User authorization failed"}}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		_, err := vk.Check(context.Background(), Bot{token: "bad_tok"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var ve *vkError
		if !errors.As(err, &ve) {
			t.Fatalf("expected *vkError, got %T: %v", err, err)
		}
		if ve.Code != 5 || ve.Message != "User authorization failed" {
			t.Errorf("ve = %+v", ve)
		}
		if ve.Error() != "vk: 5 User authorization failed" {
			t.Errorf("Error() = %q, want %q", ve.Error(), "vk: 5 User authorization failed")
		}
	})
}

func TestVKUpdates(t *testing.T) {
	var mu sync.Mutex
	longPollRequests := 0

	var lpSrv *httptest.Server
	lpSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		longPollRequests++
		mu.Unlock()

		q := r.URL.Query()
		ts := q.Get("ts")
		w.Header().Set("Content-Type", "application/json")

		switch ts {
		case "100":
			// Normal updates
			_, _ = io.WriteString(w, `{
				"ts": 105,
				"failed": 0,
				"updates": [
					{
						"type": "message_new",
						"object": {
							"message": {
								"from_id": 1001,
								"peer_id": 1001,
								"text": "hello vk"
							}
						}
					},
					{
						"type": "message_new",
						"object": {
							"message": {
								"from_id": 1002,
								"peer_id": 2000000001,
								"text": "group chat msg"
							}
						}
					},
					{
						"type": "message_new",
						"object": {
							"message": {
								"from_id": -50,
								"peer_id": -50,
								"text": "community msg"
							}
						}
					},
					{
						"type": "other_event"
					}
				]
			}`)
		case "200":
			// Failed 1: ts updated, wait from new ts
			_, _ = io.WriteString(w, `{"ts": 205, "failed": 1}`)
		case "300":
			// Failed 2: key expired
			_, _ = io.WriteString(w, `{"failed": 2}`)
		case "400":
			// Failed 3: information lost, restart from 0
			_, _ = io.WriteString(w, `{"failed": 3}`)
		default:
			_, _ = io.WriteString(w, `{"ts": `+ts+`, "failed": 0, "updates": []}`)
		}
	}))
	defer lpSrv.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/groups.getById":
			_, _ = io.WriteString(w, `{"response":[{"id":10,"screen_name":"club10"}]}`)
		case "/groups.getLongPollServer":
			_ = r.ParseForm()
			if r.PostForm.Get("group_id") != "10" {
				t.Errorf("group_id = %q, want 10", r.PostForm.Get("group_id"))
			}
			_, _ = io.WriteString(w, `{"response":{"server":"`+lpSrv.URL+`","key":"k1","ts":"100"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	vk := &VK{API: srv.URL, HTTP: srv.Client()}
	bot := Bot{token: "tok_vk"}

	// 1. Initial call with next == 0: retrieves long poll server, returns initial ts (100)
	ups, next, err := vk.Updates(context.Background(), bot, 0)
	if err != nil {
		t.Fatalf("Updates(0) error: %v", err)
	}
	if len(ups) != 0 {
		t.Errorf("len(ups) = %d, want 0", len(ups))
	}
	if next != 100 {
		t.Errorf("next = %d, want 100", next)
	}

	// 2. Call with next == 100: queries long poll server, parses message_new from user
	ups2, next2, err2 := vk.Updates(context.Background(), bot, 100)
	if err2 != nil {
		t.Fatalf("Updates(100) error: %v", err2)
	}
	if len(ups2) != 1 {
		t.Fatalf("len(ups2) = %d, want 1 (group and community messages skipped)", len(ups2))
	}
	if ups2[0].Chat != "1001" || ups2[0].Account != "vk.com/id1001" || ups2[0].Text != "hello vk" {
		t.Errorf("ups2[0] = %+v, want Chat=1001, Account=vk.com/id1001, Text=hello vk", ups2[0])
	}
	if next2 != 105 {
		t.Errorf("next2 = %d, want 105", next2)
	}

	// 3. Failed 1: events lost, returns ts
	_, nextFailed1, errFailed1 := vk.Updates(context.Background(), bot, 200)
	if errFailed1 != nil || nextFailed1 != 205 {
		t.Errorf("Failed 1: next = %d, err = %v, want 205, nil", nextFailed1, errFailed1)
	}

	// 4. Failed 2: key expired, resets poll cache, returns next
	_, nextFailed2, errFailed2 := vk.Updates(context.Background(), bot, 300)
	if errFailed2 != nil || nextFailed2 != 300 {
		t.Errorf("Failed 2: next = %d, err = %v, want 300, nil", nextFailed2, errFailed2)
	}

	// 5. Failed 3: ts lost, resets poll cache, returns 0
	// Re-establish poll server by calling with next=0
	_, _, _ = vk.Updates(context.Background(), bot, 0)
	_, nextFailed3, errFailed3 := vk.Updates(context.Background(), bot, 400)
	if errFailed3 != nil || nextFailed3 != 0 {
		t.Errorf("Failed 3: next = %d, err = %v, want 0, nil", nextFailed3, errFailed3)
	}
}

func TestVKSend(t *testing.T) {
	t.Run("plain message without link", func(t *testing.T) {
		var sentForm url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/messages.send" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			_ = r.ParseForm()
			sentForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"response":12345}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		err := vk.Send(context.Background(), Bot{token: "tok"}, "1001", Message{Text: "Notice text"})
		if err != nil {
			t.Fatalf("Send() error: %v", err)
		}

		if sentForm.Get("peer_id") != "1001" {
			t.Errorf("peer_id = %q, want 1001", sentForm.Get("peer_id"))
		}
		if sentForm.Get("message") != "Notice text" {
			t.Errorf("message = %q, want Notice text", sentForm.Get("message"))
		}
		if sentForm.Get("dont_parse_links") != "1" {
			t.Errorf("dont_parse_links = %q, want 1", sentForm.Get("dont_parse_links"))
		}
		if sentForm.Get("random_id") == "" {
			t.Errorf("random_id is empty")
		}
	})

	t.Run("message with link appended with newline", func(t *testing.T) {
		var sentForm url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			sentForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"response":12346}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		err := vk.Send(context.Background(), Bot{token: "tok"}, "1001", Message{
			Text: "New task assigned",
			Link: "https://go24.local/tasks/55",
		})
		if err != nil {
			t.Fatalf("Send() error: %v", err)
		}

		expectedMessage := "New task assigned\nhttps://go24.local/tasks/55"
		if sentForm.Get("message") != expectedMessage {
			t.Errorf("message = %q, want %q", sentForm.Get("message"), expectedMessage)
		}
	})

	t.Run("err gone on code 901", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"error":{"error_code":901,"error_msg":"Can't send messages for users without permission"}}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		err := vk.Send(context.Background(), Bot{token: "tok"}, "1001", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected ErrGone on 901, got %v", err)
		}
	})

	t.Run("err gone on code 902", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"error":{"error_code":902,"error_msg":"Can't send messages to this user due to their privacy settings"}}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		err := vk.Send(context.Background(), Bot{token: "tok"}, "1001", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrGone) {
			t.Errorf("expected ErrGone on 902, got %v", err)
		}
	})

	t.Run("other error on code 100", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"error":{"error_code":100,"error_msg":"One of the parameters specified was missing or invalid"}}`)
		}))
		defer srv.Close()

		vk := &VK{API: srv.URL, HTTP: srv.Client()}
		err := vk.Send(context.Background(), Bot{token: "tok"}, "1001", Message{Text: "Test"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if errors.Is(err, ErrGone) {
			t.Errorf("did not expect ErrGone on 100, got %v", err)
		}
	})
}
