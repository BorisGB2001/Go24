package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetUser(t *testing.T) {
	t.Run("user found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/_synapse/admin/v2/users/@alice:example.com" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"name":"@alice:example.com","displayname":"Alice In Chains","deactivated":false}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		u, err := c.GetUser(context.Background(), "@alice:example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if u == nil {
			t.Fatal("expected user, got nil")
		}
		if u.Name != "@alice:example.com" || u.DisplayName != "Alice In Chains" || u.Deactivated {
			t.Errorf("unexpected user struct: %+v", u)
		}
	})

	t.Run("user not found returns nil nil", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"User not found"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		u, err := c.GetUser(context.Background(), "@missing:example.com")
		if err != nil {
			t.Fatalf("expected nil error on M_NOT_FOUND, got %v", err)
		}
		if u != nil {
			t.Fatalf("expected nil user on M_NOT_FOUND, got %+v", u)
		}
	})

	t.Run("server error returns error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN","error":"Internal error"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		u, err := c.GetUser(context.Background(), "@user:example.com")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if u != nil {
			t.Errorf("expected nil user, got %+v", u)
		}
	})
}

func TestPutUser(t *testing.T) {
	t.Run("with external ID", func(t *testing.T) {
		var receivedPath, receivedMethod string
		var receivedBody map[string]any

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedMethod = r.Method
			receivedPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&receivedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		err := c.PutUser(context.Background(), "@bob:example.com", "Bob", "authentik", "sub_123", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedMethod != http.MethodPut {
			t.Errorf("method = %s, want PUT", receivedMethod)
		}
		if receivedPath != "/_synapse/admin/v2/users/@bob:example.com" {
			t.Errorf("path = %q, want /_synapse/admin/v2/users/@bob:example.com", receivedPath)
		}
		if receivedBody["displayname"] != "Bob" || receivedBody["deactivated"] != false {
			t.Errorf("unexpected body: %+v", receivedBody)
		}
		ext, ok := receivedBody["external_ids"].([]any)
		if !ok || len(ext) != 1 {
			t.Fatalf("unexpected external_ids: %+v", receivedBody["external_ids"])
		}
		extMap := ext[0].(map[string]any)
		if extMap["auth_provider"] != "authentik" || extMap["external_id"] != "sub_123" {
			t.Errorf("unexpected extMap: %+v", extMap)
		}
	})

	t.Run("without external ID", func(t *testing.T) {
		var receivedBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&receivedBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		err := c.PutUser(context.Background(), "@bob:example.com", "Bob", "", "", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedBody["deactivated"] != true {
			t.Errorf("deactivated = %v, want true", receivedBody["deactivated"])
		}
		if _, exists := receivedBody["external_ids"]; exists {
			t.Errorf("external_ids should be absent, got %+v", receivedBody["external_ids"])
		}
	})
}

func TestDeactivate(t *testing.T) {
	var receivedMethod, receivedPath string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	err := c.Deactivate(context.Background(), "@retiree:example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", receivedMethod)
	}
	if receivedPath != "/_synapse/admin/v1/deactivate/@retiree:example.com" {
		t.Errorf("path = %q, want /_synapse/admin/v1/deactivate/@retiree:example.com", receivedPath)
	}
	if receivedBody["erase"] != false {
		t.Errorf("erase = %v, want false", receivedBody["erase"])
	}
}

func TestLoginAs(t *testing.T) {
	var receivedMethod, receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"syt_puppet_token_xyz"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	tok, err := c.LoginAs(context.Background(), "@user:example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMethod != http.MethodPost || receivedPath != "/_synapse/admin/v1/users/@user:example.com/login" {
		t.Errorf("unexpected call %s %s", receivedMethod, receivedPath)
	}
	if tok != "syt_puppet_token_xyz" {
		t.Errorf("tok = %q, want 'syt_puppet_token_xyz'", tok)
	}
}

func TestForceJoin(t *testing.T) {
	var receivedMethod, receivedPath, receivedEscaped string
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedEscaped = r.URL.EscapedPath()
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	err := c.ForceJoin(context.Background(), "!room:example.com", "@newcomer:example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMethod != http.MethodPost || receivedPath != "/_synapse/admin/v1/join/!room:example.com" {
		t.Errorf("unexpected call %s %s", receivedMethod, receivedPath)
	}
	if receivedEscaped != "/_synapse/admin/v1/join/%21room:example.com" {
		t.Errorf("escaped path = %q, want /_synapse/admin/v1/join/%%21room:example.com", receivedEscaped)
	}
	if receivedBody["user_id"] != "@newcomer:example.com" {
		t.Errorf("received user_id = %q, want @newcomer:example.com", receivedBody["user_id"])
	}
}

func TestCreateRoom(t *testing.T) {
	t.Run("regular private room", func(t *testing.T) {
		var receivedBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/_matrix/client/v3/createRoom" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewDecoder(r.Body).Decode(&receivedBody)
			_, _ = w.Write([]byte(`{"room_id":"!newroom:example.com"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		roomID, err := c.CreateRoom(context.Background(), "General Chat", "All things work", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if roomID != "!newroom:example.com" {
			t.Errorf("roomID = %q, want '!newroom:example.com'", roomID)
		}
		if receivedBody["name"] != "General Chat" || receivedBody["topic"] != "All things work" {
			t.Errorf("name/topic mismatch: %+v", receivedBody)
		}
		if receivedBody["preset"] != "private_chat" || receivedBody["visibility"] != "private" {
			t.Errorf("preset/visibility mismatch: %+v", receivedBody)
		}
		if _, hasCreation := receivedBody["creation_content"]; hasCreation {
			t.Errorf("creation_content should not be set for non-space room")
		}
	})

	t.Run("space room", func(t *testing.T) {
		var receivedBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&receivedBody)
			_, _ = w.Write([]byte(`{"room_id":"!space:example.com"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		roomID, err := c.CreateRoom(context.Background(), "Company Space", "Workspace", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if roomID != "!space:example.com" {
			t.Errorf("roomID = %q, want '!space:example.com'", roomID)
		}
		cc, ok := receivedBody["creation_content"].(map[string]any)
		if !ok || cc["type"] != "m.space" {
			t.Errorf("creation_content = %+v, want type: m.space", receivedBody["creation_content"])
		}
	})
}

func TestSetState_And_State(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/_matrix/client/v3/rooms/!room:example.com/state/m.room.name/"
		if r.URL.Path != wantPath {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPut {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"event_id":"$name_event"}`))
			return
		}
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"name":"Updated Room Name"}`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// SetState
	err := c.SetState(context.Background(), "!room:example.com", "m.room.name", "", map[string]string{"name": "Updated Room Name"})
	if err != nil {
		t.Fatalf("SetState error: %v", err)
	}

	// State
	var out struct {
		Name string `json:"name"`
	}
	err = c.State(context.Background(), "!room:example.com", "m.room.name", "", &out)
	if err != nil {
		t.Fatalf("State error: %v", err)
	}
	if out.Name != "Updated Room Name" {
		t.Errorf("out.Name = %q, want 'Updated Room Name'", out.Name)
	}
}

func TestSetStateAs(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "bot-tok")
	err := c.SetStateAs(context.Background(), "user-tok", "!room:example.com", "m.room.member", "@user:example.com", map[string]string{"membership": "join"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedAuth != "Bearer user-tok" {
		t.Errorf("auth = %q, want 'Bearer user-tok'", receivedAuth)
	}
}

func TestAddToSpace(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if calls == 1 {
			// space state: m.space.child with state_key=roomID
			wantPath := "/_matrix/client/v3/rooms/!space:example.com/state/m.space.child/!room:example.com"
			if r.URL.Path != wantPath {
				t.Errorf("call 1: path = %q, want %q", r.URL.Path, wantPath)
			}
			via, _ := body["via"].([]any)
			if len(via) != 1 || via[0] != "example.com" {
				t.Errorf("call 1: unexpected via %+v", via)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if calls == 2 {
			// room state: m.space.parent with state_key=spaceID
			wantPath := "/_matrix/client/v3/rooms/!room:example.com/state/m.space.parent/!space:example.com"
			if r.URL.Path != wantPath {
				t.Errorf("call 2: path = %q, want %q", r.URL.Path, wantPath)
			}
			if body["canonical"] != true {
				t.Errorf("call 2: canonical = %v, want true", body["canonical"])
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	err := c.AddToSpace(context.Background(), "!space:example.com", "!room:example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}

	// Error path: first call fails
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN"}`))
	}))
	defer errSrv.Close()

	errClient := New(errSrv.URL, "example.com", "tok")
	if err := errClient.AddToSpace(context.Background(), "!space:example.com", "!room:example.com"); err == nil {
		t.Fatal("expected error on forbidden state update, got nil")
	}
}

func TestMembers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_matrix/client/v3/rooms/!room:example.com/members" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"chunk": [
				{"state_key":"@alice:example.com", "content":{"membership":"join"}},
				{"state_key":"@bob:example.com", "content":{"membership":"invite"}},
				{"state_key":"@carol:example.com", "content":{"membership":"leave"}},
				{"state_key":"@dave:example.com", "content":{"membership":"ban"}}
			]
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	m, err := c.Members(context.Background(), "!room:example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 2 {
		t.Fatalf("got %d members, want 2", len(m))
	}
	if m["@alice:example.com"] != "join" {
		t.Errorf("alice membership = %q, want 'join'", m["@alice:example.com"])
	}
	if m["@bob:example.com"] != "invite" {
		t.Errorf("bob membership = %q, want 'invite'", m["@bob:example.com"])
	}
	if _, exists := m["@carol:example.com"]; exists {
		t.Errorf("carol (leave) should not be present in members map")
	}
	if _, exists := m["@dave:example.com"]; exists {
		t.Errorf("dave (ban) should not be present in members map")
	}

	// Error path
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"errcode":"M_UNKNOWN"}`))
	}))
	defer errSrv.Close()

	errClient := New(errSrv.URL, "example.com", "tok")
	if _, err := errClient.Members(context.Background(), "!room:example.com"); err == nil {
		t.Fatal("expected error on failed members call, got nil")
	}
}

func TestKick(t *testing.T) {
	var receivedPath string
	var receivedBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	err := c.Kick(context.Background(), "!room:example.com", "@spammer:example.com", "Spamming")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedPath != "/_matrix/client/v3/rooms/!room:example.com/kick" {
		t.Errorf("path = %q, want /_matrix/client/v3/rooms/!room:example.com/kick", receivedPath)
	}
	if receivedBody["user_id"] != "@spammer:example.com" || receivedBody["reason"] != "Spamming" {
		t.Errorf("unexpected body: %+v", receivedBody)
	}
}

func TestTxnID(t *testing.T) {
	id1 := txnID()
	id2 := txnID()
	if id1 == "" || id2 == "" {
		t.Fatal("txnID returned empty string")
	}
	if id1 == id2 {
		t.Errorf("txnID produced identical successive IDs: %q", id1)
	}
	if !strings.Contains(id1, "-") {
		t.Errorf("txnID %q does not contain hyphen", id1)
	}
}

func TestSend_And_SendEvent(t *testing.T) {
	var receivedMethod, receivedPath, receivedAuth string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"event_id":"$event_send_123"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "bot-tok")

	// Send calls SendEvent with m.room.message
	id, err := c.Send(context.Background(), "!room:example.com", map[string]any{"body": "hello"}, "user-tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "$event_send_123" {
		t.Errorf("id = %q, want '$event_send_123'", id)
	}
	if receivedMethod != http.MethodPut {
		t.Errorf("method = %s, want PUT", receivedMethod)
	}
	if !strings.HasPrefix(receivedPath, "/_matrix/client/v3/rooms/!room:example.com/send/m.room.message/") {
		t.Errorf("unexpected path: %q", receivedPath)
	}
	if receivedAuth != "Bearer user-tok" {
		t.Errorf("auth = %q, want 'Bearer user-tok'", receivedAuth)
	}
	if receivedBody["body"] != "hello" {
		t.Errorf("body = %+v, want body: hello", receivedBody)
	}
}

func TestRedact_And_RedactAs(t *testing.T) {
	var receivedAuth string
	var receivedBody map[string]string
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		receivedBody = nil
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"event_id":"$redaction_id"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "bot-tok")

	// Redact (as bot)
	err := c.Redact(context.Background(), "!room:example.com", "$target_event", "offensive")
	if err != nil {
		t.Fatalf("Redact error: %v", err)
	}
	if !strings.HasPrefix(receivedPath, "/_matrix/client/v3/rooms/!room:example.com/redact/$target_event/") {
		t.Errorf("path = %q", receivedPath)
	}
	if receivedAuth != "Bearer bot-tok" {
		t.Errorf("auth = %q, want bot-tok", receivedAuth)
	}
	if receivedBody["reason"] != "offensive" {
		t.Errorf("reason = %q, want 'offensive'", receivedBody["reason"])
	}

	// RedactAs (with employee token)
	err = c.RedactAs(context.Background(), "emp-tok", "!room:example.com", "$target_event")
	if err != nil {
		t.Fatalf("RedactAs error: %v", err)
	}
	if receivedAuth != "Bearer emp-tok" {
		t.Errorf("auth = %q, want 'Bearer emp-tok'", receivedAuth)
	}
	if _, hasReason := receivedBody["reason"]; hasReason {
		t.Errorf("RedactAs should send empty map, got %+v", receivedBody)
	}
}

func TestSync(t *testing.T) {
	var receivedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"next_batch": "s_next_123",
			"rooms": {
				"join": {
					"!room:example.com": {
						"timeline": {
							"events": [
								{
									"event_id": "$ev1",
									"type": "m.room.message",
									"sender": "@alice:example.com",
									"content": {"body": "hello sync"},
									"origin_server_ts": 1700000000000
								}
							]
						}
					}
				}
			}
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	res, err := c.Sync(context.Background(), "s_prev_000", 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if res.NextBatch != "s_next_123" {
		t.Errorf("NextBatch = %q, want 's_next_123'", res.NextBatch)
	}
	room := res.Rooms.Join["!room:example.com"]
	if len(room.Timeline.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(room.Timeline.Events))
	}
	ev := room.Timeline.Events[0]
	if ev.EventID != "$ev1" || ev.Type != "m.room.message" || ev.Sender != "@alice:example.com" {
		t.Errorf("unexpected event: %+v", ev)
	}
	if ev.TS != 1700000000000 {
		t.Errorf("ev.TS = %d, want 1700000000000", ev.TS)
	}
	if !strings.Contains(receivedQuery, "timeout=30000") || !strings.Contains(receivedQuery, "since=s_prev_000") {
		t.Errorf("unexpected query: %q", receivedQuery)
	}
}

func TestPermalink(t *testing.T) {
	tests := []struct {
		elementURL string
		roomID     string
		eventID    string
		want       string
	}{
		{
			"https://chat.example.com",
			"!room:example.com",
			"$event123",
			"https://chat.example.com/#/room/!room:example.com/$event123",
		},
		{
			"https://chat.example.com",
			"!room:example.com",
			"",
			"https://chat.example.com/#/room/!room:example.com",
		},
	}
	for _, tt := range tests {
		if got := Permalink(tt.elementURL, tt.roomID, tt.eventID); got != tt.want {
			t.Errorf("Permalink(%q, %q, %q) = %q, want %q", tt.elementURL, tt.roomID, tt.eventID, got, tt.want)
		}
	}
}

func TestEventJSON(t *testing.T) {
	raw := `{
		"event_id": "$ev100",
		"type": "m.room.message",
		"sender": "@user:example.com",
		"origin_server_ts": 1690000000000,
		"content": {"body": "original message"},
		"redacts": "$target_to_redact",
		"state_key": "",
		"room_id": "!room:example.com",
		"unsigned": {
			"prev_content": {"body": "previous"},
			"m.relations": {
				"m.thread": {
					"count": 5,
					"latest_event": {
						"event_id": "$ev_latest",
						"type": "m.room.message",
						"sender": "@replier:example.com",
						"origin_server_ts": 1690000005000
					}
				},
				"m.replace": {
					"event_id": "$ev_edit",
					"type": "m.room.message",
					"sender": "@user:example.com",
					"origin_server_ts": 1690000002000
				}
			}
		}
	}`

	var ev Event
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if ev.EventID != "$ev100" || ev.Type != "m.room.message" || ev.Sender != "@user:example.com" {
		t.Errorf("basic fields mismatch: %+v", ev)
	}
	if ev.Redacts != "$target_to_redact" {
		t.Errorf("Redacts = %q, want '$target_to_redact'", ev.Redacts)
	}
	if ev.StateKey == nil || *ev.StateKey != "" {
		t.Errorf("StateKey = %v, want empty string pointer", ev.StateKey)
	}
	if ev.RoomID != "!room:example.com" {
		t.Errorf("RoomID = %q, want '!room:example.com'", ev.RoomID)
	}
	if ev.Unsigned.PrevContent["body"] != "previous" {
		t.Errorf("PrevContent = %+v", ev.Unsigned.PrevContent)
	}
	if ev.Unsigned.Relations.Thread == nil || ev.Unsigned.Relations.Thread.Count != 5 {
		t.Errorf("Relations.Thread = %+v, want count 5", ev.Unsigned.Relations.Thread)
	}
	if ev.Unsigned.Relations.Thread.Latest.EventID != "$ev_latest" {
		t.Errorf("Latest = %+v, want $ev_latest", ev.Unsigned.Relations.Thread.Latest)
	}
	if ev.Unsigned.Relations.Replace == nil || ev.Unsigned.Relations.Replace.EventID != "$ev_edit" {
		t.Errorf("Replace = %+v, want $ev_edit", ev.Unsigned.Relations.Replace)
	}
}
