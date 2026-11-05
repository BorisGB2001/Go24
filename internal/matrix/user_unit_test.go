package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestFreshSync(t *testing.T) {
	s1 := freshSync()
	s2 := freshSync()
	if s1 == "" || s2 == "" {
		t.Fatal("freshSync returned empty string")
	}
	if s1 == s2 {
		t.Errorf("freshSync returned identical successive values: %q", s1)
	}
}

func TestPowerLevels(t *testing.T) {
	t.Run("Level lookup", func(t *testing.T) {
		pl := PowerLevels{
			"users": map[string]any{
				"@admin:example.com": float64(100),
				"@mod:example.com":   float64(50),
			},
			"users_default": float64(10),
		}

		if got := pl.Level("@admin:example.com"); got != 100 {
			t.Errorf("Level(admin) = %d, want 100", got)
		}
		if got := pl.Level("@mod:example.com"); got != 50 {
			t.Errorf("Level(mod) = %d, want 50", got)
		}
		if got := pl.Level("@regular:example.com"); got != 10 {
			t.Errorf("Level(regular) = %d, want 10 (users_default)", got)
		}

		// When users_default is missing, defaults to 0.
		plNoDefault := PowerLevels{}
		if got := plNoDefault.Level("@anyone:example.com"); got != 0 {
			t.Errorf("Level(anyone) = %d, want 0", got)
		}
	})

	t.Run("SetLevel", func(t *testing.T) {
		// When "users" map already exists
		pl := PowerLevels{
			"users": map[string]any{
				"@user1:example.com": float64(10),
			},
		}
		pl.SetLevel("@user2:example.com", 75)
		if got := pl.Level("@user2:example.com"); got != 75 {
			t.Errorf("Level(user2) = %d, want 75", got)
		}

		// When "users" map is missing initially
		plEmpty := PowerLevels{}
		plEmpty.SetLevel("@newowner:example.com", 100)
		if got := plEmpty.Level("@newowner:example.com"); got != 100 {
			t.Errorf("Level(newowner) = %d, want 100", got)
		}
	})
}

func TestReceiptsOf(t *testing.T) {
	t.Run("non-receipt event returns nil", func(t *testing.T) {
		ev := Event{Type: "m.room.message"}
		if got := ReceiptsOf(ev); got != nil {
			t.Errorf("ReceiptsOf(message) = %v, want nil", got)
		}
	})

	t.Run("valid receipt extraction and thread filtering", func(t *testing.T) {
		ev := Event{
			Type: "m.receipt",
			Content: map[string]any{
				"$ev1": map[string]any{
					"m.read": map[string]any{
						"@alice:example.com": map[string]any{
							"ts": float64(1700000001000),
						},
						"@bob:example.com": map[string]any{
							"ts":        float64(1700000002000),
							"thread_id": "main",
						},
						"@carol:example.com": map[string]any{
							"ts":        float64(1700000003000),
							"thread_id": "$some_thread_root", // Thread receipt should be ignored!
						},
					},
				},
				"$ev2": map[string]any{
					"m.read": map[string]any{
						"@dave:example.com": map[string]any{
							"ts": float64(1700000004000),
						},
					},
				},
			},
		}

		receipts := ReceiptsOf(ev)
		// Carol should be filtered out, so 3 receipts remain: Alice, Bob, Dave
		if len(receipts) != 3 {
			t.Fatalf("len(receipts) = %d, want 3: %+v", len(receipts), receipts)
		}

		sort.Slice(receipts, func(i, j int) bool {
			return receipts[i].User < receipts[j].User
		})

		expected := []Receipt{
			{User: "@alice:example.com", Event: "$ev1", TS: 1700000001000},
			{User: "@bob:example.com", Event: "$ev1", TS: 1700000002000},
			{User: "@dave:example.com", Event: "$ev2", TS: 1700000004000},
		}

		for i, want := range expected {
			if receipts[i] != want {
				t.Errorf("receipts[%d] = %+v, want %+v", i, receipts[i], want)
			}
		}
	})

	t.Run("malformed content panics on unchecked type assertion", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("expected panic on malformed content due to user.go:412 unchecked type assertion, got none")
			}
		}()
		ev := Event{
			Type: "m.receipt",
			Content: map[string]any{
				"$ev1": "invalid-non-map-data",
			},
		}
		_ = ReceiptsOf(ev)
	})
}

func TestSyncAs(t *testing.T) {
	t.Run("since is empty (fresh sync)", func(t *testing.T) {
		var receivedQuery string
		var receivedAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedQuery = r.URL.RawQuery
			receivedAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"next_batch":"s1"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "bot-tok")
		res, err := c.SyncAs(context.Background(), "user-tok", "", 10*time.Second, 20, "m.receipt", "m.typing")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.NextBatch != "s1" {
			t.Errorf("NextBatch = %q, want 's1'", res.NextBatch)
		}
		if receivedAuth != "Bearer user-tok" {
			t.Errorf("auth = %q, want 'Bearer user-tok'", receivedAuth)
		}
		if strings.Contains(receivedQuery, "since=") {
			t.Errorf("query should not have 'since=', got %q", receivedQuery)
		}
		if !strings.Contains(receivedQuery, "%22limit%22%3A20") && !strings.Contains(receivedQuery, `"limit":20`) {
			t.Errorf("query missing limit: %q", receivedQuery)
		}
	})

	t.Run("since is present", func(t *testing.T) {
		var receivedQuery string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedQuery = r.URL.RawQuery
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"next_batch":"s2"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "bot-tok")
		_, err := c.SyncAs(context.Background(), "user-tok", "batch_prev", 15*time.Second, 50)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(receivedQuery, "since=batch_prev") {
			t.Errorf("query missing 'since=batch_prev': %q", receivedQuery)
		}
		if !strings.Contains(receivedQuery, "timeout=15000") {
			t.Errorf("query missing 'timeout=15000': %q", receivedQuery)
		}
	})
}

func TestMessages_And_Variants(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_matrix/client/v3/rooms/!room:example.com/messages" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("dir") != "b" || q.Get("limit") != "30" {
			t.Errorf("unexpected dir/limit: %v", q)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"chunk": [
				{"event_id": "$m1", "type": "m.room.message"}
			],
			"end": "token_end_123"
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "bot-tok")

	// Messages (default messageTypes)
	evs, end, err := c.Messages(context.Background(), "user-tok", "!room:example.com", "token_from", 30)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evs) != 1 || evs[0].EventID != "$m1" || end != "token_end_123" {
		t.Errorf("unexpected result: evs=%+v end=%q", evs, end)
	}

	// MessagesOf with custom types
	evs2, end2, err := c.MessagesOf(context.Background(), "user-tok", "!room:example.com", "token_from", 30, `["m.room.message"]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(evs2) != 1 || end2 != "token_end_123" {
		t.Errorf("unexpected result: evs2=%+v end2=%q", evs2, end2)
	}
}

func TestThread_Threads_LastInThread(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/relations/$root1/m.thread"):
			q := r.URL.Query()
			if q.Get("dir") == "b" && q.Get("limit") == "1" {
				// LastInThread
				_, _ = w.Write([]byte(`{"chunk":[{"event_id":"$latest_reply"}]}`))
				return
			}
			// Thread
			_, _ = w.Write([]byte(`{"chunk":[{"event_id":"$reply1"},{"event_id":"$reply2"}]}`))
		case strings.HasSuffix(path, "/threads"):
			// Threads
			_, _ = w.Write([]byte(`{"chunk":[{"event_id":"$root1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "bot-tok")

	// Thread
	replies, err := c.Thread(context.Background(), "tok", "!room:example.com", "$root1")
	if err != nil || len(replies) != 2 || replies[0].EventID != "$reply1" {
		t.Errorf("Thread = %+v, %v", replies, err)
	}

	// Threads
	roots, err := c.Threads(context.Background(), "tok", "!room:example.com", 10)
	if err != nil || len(roots) != 1 || roots[0].EventID != "$root1" {
		t.Errorf("Threads = %+v, %v", roots, err)
	}

	// LastInThread
	last, err := c.LastInThread(context.Background(), "tok", "!room:example.com", "$root1")
	if err != nil || last != "$latest_reply" {
		t.Errorf("LastInThread = %q, %v", last, err)
	}

	// LastInThread with empty chunk
	emptySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"chunk":[]}`))
	}))
	defer emptySrv.Close()

	emptyClient := New(emptySrv.URL, "example.com", "tok")
	lastEmpty, err := emptyClient.LastInThread(context.Background(), "tok", "!room:example.com", "$root_empty")
	if err != nil || lastEmpty != "" {
		t.Errorf("LastInThread on empty = %q, %v", lastEmpty, err)
	}
}

func TestReactions_And_Relations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/m.annotation/m.reaction"):
			_, _ = w.Write([]byte(`{"chunk":[{"event_id":"$react1","type":"m.reaction"}]}`))
		case strings.Contains(r.URL.Path, "/m.reference"):
			_, _ = w.Write([]byte(`{"chunk":[{"event_id":"$ref1","type":"m.reference"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// Reactions
	reacts, err := c.Reactions(context.Background(), "tok", "!room:example.com", "$ev1")
	if err != nil || len(reacts) != 1 || reacts[0].EventID != "$react1" {
		t.Errorf("Reactions = %+v, %v", reacts, err)
	}

	// Relations
	rels, err := c.Relations(context.Background(), "tok", "!room:example.com", "$ev1", "m.reference")
	if err != nil || len(rels) != 1 || rels[0].EventID != "$ref1" {
		t.Errorf("Relations = %+v, %v", rels, err)
	}
}

func TestEventAs_And_ReadReceipt(t *testing.T) {
	var receivedMarkers map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/event/$ev_one"):
			_, _ = w.Write([]byte(`{"event_id":"$ev_one","type":"m.room.message"}`))
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/read_markers"):
			_ = json.NewDecoder(r.Body).Decode(&receivedMarkers)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	ev, err := c.EventAs(context.Background(), "tok", "!room:example.com", "$ev_one")
	if err != nil || ev.EventID != "$ev_one" {
		t.Errorf("EventAs = %+v, %v", ev, err)
	}

	err = c.ReadReceipt(context.Background(), "tok", "!room:example.com", "$ev_one")
	if err != nil {
		t.Fatalf("ReadReceipt error: %v", err)
	}
	if receivedMarkers["m.read"] != "$ev_one" || receivedMarkers["m.fully_read"] != "$ev_one" {
		t.Errorf("unexpected read markers: %+v", receivedMarkers)
	}
}

func TestCreateDM_CreateNotes_JoinAs(t *testing.T) {
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_matrix/client/v3/createRoom":
			_ = json.NewDecoder(r.Body).Decode(&receivedBody)
			_, _ = w.Write([]byte(`{"room_id":"!created:example.com"}`))
		case strings.HasPrefix(r.URL.Path, "/_matrix/client/v3/join/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"room_id":"!created:example.com"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// CreateDM
	dmID, err := c.CreateDM(context.Background(), "tok", "@peer:example.com")
	if err != nil || dmID != "!created:example.com" {
		t.Errorf("CreateDM = %q, %v", dmID, err)
	}
	if receivedBody["is_direct"] != true {
		t.Errorf("CreateDM is_direct = %v", receivedBody["is_direct"])
	}

	// CreateNotes
	notesID, err := c.CreateNotes(context.Background(), "tok", "My Notes")
	if err != nil || notesID != "!created:example.com" {
		t.Errorf("CreateNotes = %q, %v", notesID, err)
	}
	if receivedBody["name"] != "My Notes" {
		t.Errorf("CreateNotes name = %v", receivedBody["name"])
	}

	// JoinAs
	err = c.JoinAs(context.Background(), "tok", "!created:example.com")
	if err != nil {
		t.Errorf("JoinAs error: %v", err)
	}
}

func TestDirectRooms(t *testing.T) {
	t.Run("found and updated", func(t *testing.T) {
		var receivedPUT map[string][]string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"@alice:example.com":["!dm1:example.com"]}`))
				return
			}
			if r.Method == http.MethodPut {
				_ = json.NewDecoder(r.Body).Decode(&receivedPUT)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		rooms, err := c.DirectRooms(context.Background(), "tok", "@me:example.com")
		if err != nil || len(rooms["@alice:example.com"]) != 1 {
			t.Fatalf("DirectRooms = %+v, %v", rooms, err)
		}

		err = c.SetDirectRooms(context.Background(), "tok", "@me:example.com", map[string][]string{
			"@bob:example.com": {"!dm2:example.com"},
		})
		if err != nil {
			t.Fatalf("SetDirectRooms error: %v", err)
		}
		if len(receivedPUT["@bob:example.com"]) != 1 || receivedPUT["@bob:example.com"][0] != "!dm2:example.com" {
			t.Errorf("unexpected PUT data: %+v", receivedPUT)
		}
	})

	t.Run("M_NOT_FOUND returns empty map", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "tok")
		rooms, err := c.DirectRooms(context.Background(), "tok", "@me:example.com")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rooms == nil || len(rooms) != 0 {
			t.Errorf("rooms = %+v, want empty map", rooms)
		}
	})
}

func TestCreateGroup_And_SetJoinRuleAs(t *testing.T) {
	var receivedBody map[string]any
	var receivedRule map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/_matrix/client/v3/createRoom":
			_ = json.NewDecoder(r.Body).Decode(&receivedBody)
			_, _ = w.Write([]byte(`{"room_id":"!group:example.com"}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "m.room.join_rules"):
			_ = json.NewDecoder(r.Body).Decode(&receivedRule)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// CreateGroup with nil invite (should become empty slice)
	gid, err := c.CreateGroup(context.Background(), "tok", "@owner:example.com", "Test Group", nil, PowerLevels{"kick": 100})
	if err != nil || gid != "!group:example.com" {
		t.Fatalf("CreateGroup = %q, %v", gid, err)
	}
	inv, ok := receivedBody["invite"].([]any)
	if !ok || len(inv) != 0 {
		t.Errorf("invite = %+v, want empty list", receivedBody["invite"])
	}

	// SetJoinRuleAs restricted to space
	err = c.SetJoinRuleAs(context.Background(), "tok", "!group:example.com", "!space:example.com")
	if err != nil {
		t.Fatalf("SetJoinRuleAs space error: %v", err)
	}
	if receivedRule["join_rule"] != "restricted" {
		t.Errorf("join_rule = %v, want 'restricted'", receivedRule["join_rule"])
	}

	// SetJoinRuleAs invite only (empty space)
	err = c.SetJoinRuleAs(context.Background(), "tok", "!group:example.com", "")
	if err != nil {
		t.Fatalf("SetJoinRuleAs invite error: %v", err)
	}
	if receivedRule["join_rule"] != "invite" {
		t.Errorf("join_rule = %v, want 'invite'", receivedRule["join_rule"])
	}
}

func TestAdminState_JoinedRooms_AdminMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_synapse/admin/v1/rooms/!room:example.com/state":
			_, _ = w.Write([]byte(`{"state":[{"event_id":"$s1","type":"m.room.name"}]}`))
		case r.URL.Path == "/_matrix/client/v3/joined_rooms":
			_, _ = w.Write([]byte(`{"joined_rooms":["!r1:example.com","!r2:example.com"]}`))
		case r.URL.Path == "/_synapse/admin/v1/rooms/!room:example.com/messages":
			_, _ = w.Write([]byte(`{"chunk":[{"event_id":"$am1"}],"end":"end_tok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// AdminState
	st, err := c.AdminState(context.Background(), "!room:example.com")
	if err != nil || len(st) != 1 || st[0].EventID != "$s1" {
		t.Errorf("AdminState = %+v, %v", st, err)
	}

	// JoinedRooms
	rooms, err := c.JoinedRooms(context.Background(), "tok")
	if err != nil || !rooms["!r1:example.com"] || !rooms["!r2:example.com"] {
		t.Errorf("JoinedRooms = %+v, %v", rooms, err)
	}

	// AdminMessages
	ams, end, err := c.AdminMessages(context.Background(), "!room:example.com", "from_tok", 25)
	if err != nil || len(ams) != 1 || end != "end_tok" {
		t.Errorf("AdminMessages = %+v, %q, %v", ams, end, err)
	}

	// JoinedRooms error path
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	errClient := New(errSrv.URL, "example.com", "tok")
	if _, err := errClient.JoinedRooms(context.Background(), "tok"); err == nil {
		t.Fatal("expected error on failed JoinedRooms, got nil")
	}
}

func TestMembershipActions_And_StateAs(t *testing.T) {
	var lastPath string
	var lastBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		lastBody = nil
		_ = json.NewDecoder(r.Body).Decode(&lastBody)
		switch {
		case strings.HasSuffix(r.URL.Path, "/invite") || strings.HasSuffix(r.URL.Path, "/kick") || strings.HasSuffix(r.URL.Path, "/leave"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/state/"):
			_, _ = w.Write([]byte(`{"content_key":"content_val"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// InviteAs
	err := c.InviteAs(context.Background(), "tok", "!room:example.com", "@invitee:example.com")
	if err != nil || lastBody["user_id"] != "@invitee:example.com" {
		t.Errorf("InviteAs error = %v, body = %+v", err, lastBody)
	}

	// KickAs
	err = c.KickAs(context.Background(), "tok", "!room:example.com", "@kicked:example.com")
	if err != nil || lastBody["user_id"] != "@kicked:example.com" {
		t.Errorf("KickAs error = %v, body = %+v", err, lastBody)
	}

	// LeaveAs
	err = c.LeaveAs(context.Background(), "tok", "!room:example.com")
	if err != nil || !strings.HasSuffix(lastPath, "/leave") {
		t.Errorf("LeaveAs error = %v, path = %s", err, lastPath)
	}

	// StateAs
	var out map[string]string
	err = c.StateAs(context.Background(), "tok", "!room:example.com", "m.custom", "", &out)
	if err != nil || out["content_key"] != "content_val" {
		t.Errorf("StateAs = %+v, %v", out, err)
	}
}

func TestSearch(t *testing.T) {
	var receivedQuery string
	var receivedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"search_categories": {
				"room_events": {
					"results": [
						{"result": {"event_id": "$found_1", "type": "m.room.message"}}
					],
					"next_batch": "next_search_token"
				}
			}
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	evs, next, err := c.Search(context.Background(), "tok", "project update", []string{"!r1:example.com"}, "prev_search_token")
	if err != nil {
		t.Fatalf("Search error: %v", err)
	}
	if len(evs) != 1 || evs[0].EventID != "$found_1" {
		t.Errorf("evs = %+v", evs)
	}
	if next != "next_search_token" {
		t.Errorf("next = %q, want 'next_search_token'", next)
	}
	if !strings.Contains(receivedQuery, "next_batch=prev_search_token") {
		t.Errorf("receivedQuery = %q", receivedQuery)
	}
}

func TestContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/context/$target") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"start": "token_start",
			"event": {"event_id": "$target"},
			"events_before": [{"event_id": "$before1"}],
			"events_after": [{"event_id": "$after1"}]
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	around, err := c.Context(context.Background(), "tok", "!room:example.com", "$target", 10)
	if err != nil {
		t.Fatalf("Context error: %v", err)
	}
	if around.Event.EventID != "$target" || len(around.Before) != 1 || len(around.After) != 1 {
		t.Errorf("around = %+v", around)
	}
	if around.Start != "token_start" {
		t.Errorf("around.Start = %q, want 'token_start'", around.Start)
	}
}

func TestThreadReceipt_ThreadCounts_Receipts(t *testing.T) {
	var receivedReceiptBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/_matrix/client/v3/rooms/!room:example.com/receipt/m.read/"):
			_ = json.NewDecoder(r.Body).Decode(&receivedReceiptBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/_matrix/client/v3/sync":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"rooms": {
					"join": {
						"!room:example.com": {
							"unread_thread_notifications": {
								"$root1": {"notification_count": 3, "highlight_count": 1}
							},
							"ephemeral": {
								"events": [
									{
										"type": "m.receipt",
										"content": {
											"$msg1": {
												"m.read": {
													"@alice:example.com": {"ts": 1700000000000}
												}
											}
										}
									}
								]
							}
						}
					}
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// ThreadReceipt
	err := c.ThreadReceipt(context.Background(), "tok", "!room:example.com", "$ev1", "$thread_root")
	if err != nil || receivedReceiptBody["thread_id"] != "$thread_root" {
		t.Errorf("ThreadReceipt error = %v, body = %+v", err, receivedReceiptBody)
	}

	// ThreadCounts
	counts, err := c.ThreadCounts(context.Background(), "tok", "!room:example.com")
	if err != nil || counts["$root1"].Notifications != 3 || counts["$root1"].Highlights != 1 {
		t.Errorf("ThreadCounts = %+v, %v", counts, err)
	}

	// Receipts
	rs, err := c.Receipts(context.Background(), "tok", "!room:example.com")
	if err != nil || len(rs) != 1 || rs[0].User != "@alice:example.com" {
		t.Errorf("Receipts = %+v, %v", rs, err)
	}

	// Error paths
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	errClient := New(errSrv.URL, "example.com", "tok")
	if _, err := errClient.ThreadCounts(context.Background(), "tok", "!room:example.com"); err == nil {
		t.Fatal("expected error on failed ThreadCounts, got nil")
	}
	if _, err := errClient.Receipts(context.Background(), "tok", "!room:example.com"); err == nil {
		t.Fatal("expected error on failed Receipts, got nil")
	}
}

func TestTyping_SetTag_RoomData(t *testing.T) {
	var typingBody map[string]any
	var tagMethod string
	var roomDataBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/typing/"):
			_ = json.NewDecoder(r.Body).Decode(&typingBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/tags/"):
			tagMethod = r.Method
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/account_data/"):
			if r.Method == http.MethodPut {
				_ = json.NewDecoder(r.Body).Decode(&roomDataBody)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"saved_key":"saved_val"}`))
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// Typing on
	err := c.Typing(context.Background(), "tok", "!room:example.com", "@me:example.com", true)
	if err != nil || typingBody["typing"] != true || typingBody["timeout"] != float64(6000) {
		t.Errorf("Typing on error = %v, body = %+v", err, typingBody)
	}

	// Typing off
	err = c.Typing(context.Background(), "tok", "!room:example.com", "@me:example.com", false)
	if err != nil || typingBody["typing"] != false {
		t.Errorf("Typing off error = %v, body = %+v", err, typingBody)
	}

	// SetTag on (PUT)
	err = c.SetTag(context.Background(), "tok", "@me:example.com", "!room:example.com", "m.favourite", true)
	if err != nil || tagMethod != http.MethodPut {
		t.Errorf("SetTag on error = %v, method = %s", err, tagMethod)
	}

	// SetTag off (DELETE)
	err = c.SetTag(context.Background(), "tok", "@me:example.com", "!room:example.com", "m.favourite", false)
	if err != nil || tagMethod != http.MethodDelete {
		t.Errorf("SetTag off error = %v, method = %s", err, tagMethod)
	}

	// SetRoomData
	err = c.SetRoomData(context.Background(), "tok", "@me:example.com", "!room:example.com", "custom.type", map[string]string{"foo": "bar"})
	if err != nil || roomDataBody["foo"] != "bar" {
		t.Errorf("SetRoomData error = %v, body = %+v", err, roomDataBody)
	}

	// RoomData
	var outData map[string]string
	err = c.RoomData(context.Background(), "tok", "@me:example.com", "!room:example.com", "custom.type", &outData)
	if err != nil || outData["saved_key"] != "saved_val" {
		t.Errorf("RoomData error = %v, out = %+v", err, outData)
	}
}

func TestMutedRooms_And_Mute(t *testing.T) {
	var muteMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_matrix/client/v3/pushrules/" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{
				"global": {
					"override": [
						{
							"rule_id": "!muted_room:example.com",
							"enabled": true,
							"actions": []
						},
						{
							"rule_id": "!notified_room:example.com",
							"enabled": true,
							"actions": ["notify"]
						},
						{
							"rule_id": "!disabled_room:example.com",
							"enabled": false,
							"actions": []
						},
						{
							"rule_id": "not_a_room_rule",
							"enabled": true,
							"actions": []
						}
					]
				}
			}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/_matrix/client/v3/pushrules/global/override/") {
			muteMethod = r.Method
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")

	// MutedRooms
	muted, err := c.MutedRooms(context.Background(), "tok")
	if err != nil {
		t.Fatalf("MutedRooms error: %v", err)
	}
	if !muted["!muted_room:example.com"] {
		t.Error("!muted_room:example.com should be muted (true)")
	}
	if muted["!notified_room:example.com"] {
		t.Error("!notified_room:example.com should not be muted (false)")
	}
	if _, exists := muted["!disabled_room:example.com"]; exists {
		t.Error("disabled rule should not be present in map")
	}
	if _, exists := muted["not_a_room_rule"]; exists {
		t.Error("rule not starting with '!' should not be present in map")
	}

	// MutedRooms error path
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer errSrv.Close()

	errClient := New(errSrv.URL, "example.com", "tok")
	if _, err := errClient.MutedRooms(context.Background(), "tok"); err == nil {
		t.Fatal("expected error on failed MutedRooms, got nil")
	}

	// Mute on (PUT)
	err = c.Mute(context.Background(), "tok", "!room:example.com", true)
	if err != nil || muteMethod != http.MethodPut {
		t.Errorf("Mute on error = %v, method = %s", err, muteMethod)
	}

	// Mute off (DELETE)
	err = c.Mute(context.Background(), "tok", "!room:example.com", false)
	if err != nil || muteMethod != http.MethodDelete {
		t.Errorf("Mute off error = %v, method = %s", err, muteMethod)
	}
}

func TestMute_MNotFoundIgnored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	// Unmuting when no override rule exists should succeed quietly.
	err := c.Mute(context.Background(), "tok", "!room:example.com", false)
	if err != nil {
		t.Errorf("expected nil error on M_NOT_FOUND, got %v", err)
	}
}
