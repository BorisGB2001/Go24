package matrixsync

import (
	"errors"
	"reflect"
	"testing"

	"go24/internal/i18n"
	"go24/internal/matrix"
)

// TestFileOf verifies parsing of file metadata from Matrix event content maps.
func TestFileOf(t *testing.T) {
	// 1. Missing or invalid msgtype
	if got := fileOf(map[string]any{"msgtype": "m.text", "url": "mxc://example.com/abc"}); got != nil {
		t.Errorf("fileOf(text) = %+v, want nil", got)
	}

	// 2. Non-mxc url scheme
	if got := fileOf(map[string]any{"msgtype": "m.file", "url": "https://example.com/file.pdf"}); got != nil {
		t.Errorf("fileOf(https url) = %+v, want nil", got)
	}

	// 3. Valid m.image with filename and info dimensions
	contentImg := map[string]any{
		"msgtype":  "m.image",
		"url":      "mxc://example.com/img1",
		"filename": "photo.jpg",
		"body":     "fallback body",
		"info": map[string]any{
			"mimetype": "image/jpeg",
			"size":     float64(2048),
			"w":        float64(800),
			"h":        float64(600),
		},
	}
	f1 := fileOf(contentImg)
	if f1 == nil {
		t.Fatal("fileOf(image) returned nil")
	}
	if f1.Kind != "image" || f1.Name != "photo.jpg" || f1.Mime != "image/jpeg" || f1.Size != 2048 || f1.Width != 800 || f1.Height != 600 {
		t.Errorf("f1 = %+v", f1)
	}

	// 4. Valid m.audio with filename fallback to body and int64/duration
	contentAudio := map[string]any{
		"msgtype": "m.audio",
		"url":     "mxc://example.com/audio1",
		"body":    "voice.ogg",
		"info": map[string]any{
			"mimetype": "audio/ogg",
			"size":     int64(4096),
			"duration": int(30),
		},
	}
	f2 := fileOf(contentAudio)
	if f2 == nil {
		t.Fatal("fileOf(audio) returned nil")
	}
	if f2.Kind != "audio" || f2.Name != "voice.ogg" || f2.Duration != 30 || f2.Size != 4096 {
		t.Errorf("f2 = %+v", f2)
	}
}

// TestEventPeopleAndAutoDeleteBy verifies identifying people involved in Matrix timeline events.
func TestEventPeopleAndAutoDeleteBy(t *testing.T) {
	// Regular message: sender only
	evMsg := matrix.Event{Sender: "@alice:ex", Type: "m.room.message"}
	if got := eventPeople(evMsg); !reflect.DeepEqual(got, []string{"@alice:ex"}) {
		t.Errorf("eventPeople(msg) = %v", got)
	}

	// Member event: sender and target in StateKey
	target := "@bob:ex"
	evMember := matrix.Event{Sender: "@alice:ex", Type: "m.room.member", StateKey: &target}
	if got := eventPeople(evMember); !reflect.DeepEqual(got, []string{"@alice:ex", "@bob:ex"}) {
		t.Errorf("eventPeople(member) = %v", got)
	}

	// AutoDelete event with "by"
	evAuto := matrix.Event{
		Sender:  "@bot:ex",
		Type:    autoDeleteType,
		Content: map[string]any{"by": "@carol:ex", "ttl": float64(3600)},
	}
	if got := autoDeleteBy(evAuto); got != "@carol:ex" {
		t.Errorf("autoDeleteBy = %q, want '@carol:ex'", got)
	}
	if got := eventPeople(evAuto); !reflect.DeepEqual(got, []string{"@bot:ex", "@carol:ex"}) {
		t.Errorf("eventPeople(autodelete) = %v", got)
	}

	// Non-autodelete event returns ""
	if got := autoDeleteBy(evMsg); got != "" {
		t.Errorf("autoDeleteBy(msg) = %q, want ''", got)
	}
}

// TestLevelChange verifies detecting owner, admin, and unadmin role transitions.
func TestLevelChange(t *testing.T) {
	// No previous content
	evNoPrev := matrix.Event{}
	if target, event := levelChange(evNoPrev); target != "" || event != "" {
		t.Errorf("levelChange(no prev) = (%q, %q), want ('', '')", target, event)
	}

	// Promotion to owner (was 50, now 100)
	evOwner := matrix.Event{
		Content: map[string]any{
			"users": map[string]any{"@bob:ex": float64(100)},
		},
	}
	evOwner.Unsigned.PrevContent = map[string]any{
		"users": map[string]any{"@bob:ex": float64(50)},
	}
	if target, event := levelChange(evOwner); target != "@bob:ex" || event != "owner" {
		t.Errorf("levelChange(owner) = (%q, %q), want ('@bob:ex', 'owner')", target, event)
	}

	// Promotion to admin (was 0, now 50)
	evAdmin := matrix.Event{
		Content: map[string]any{
			"users": map[string]any{"@carol:ex": float64(50)},
		},
	}
	evAdmin.Unsigned.PrevContent = map[string]any{
		"users": map[string]any{"@carol:ex": float64(0)},
	}
	if target, event := levelChange(evAdmin); target != "@carol:ex" || event != "admin" {
		t.Errorf("levelChange(admin) = (%q, %q), want ('@carol:ex', 'admin')", target, event)
	}

	// Demotion from admin (was 50, now 0)
	evUnadmin := matrix.Event{
		Content: map[string]any{
			"users": map[string]any{"@carol:ex": float64(0)},
		},
	}
	evUnadmin.Unsigned.PrevContent = map[string]any{
		"users": map[string]any{"@carol:ex": float64(50)},
	}
	if target, event := levelChange(evUnadmin); target != "@carol:ex" || event != "unadmin" {
		t.Errorf("levelChange(unadmin) = (%q, %q), want ('@carol:ex', 'unadmin')", target, event)
	}
}

// TestApplyEdits verifies replacing message bodies and updating bot parts within event batches.
func TestApplyEdits(t *testing.T) {
	msgs := []Message{
		{ID: "$m1", Body: "Initial text"},
		{ID: "$m2", Body: "Bot text", SenderBot: true},
	}

	evs := []matrix.Event{
		{
			Content: map[string]any{
				"m.relates_to": map[string]any{
					"rel_type": "m.replace",
					"event_id": "$m1",
				},
				"m.new_content": map[string]any{
					"body": "Edited text",
				},
			},
		},
		{
			Content: map[string]any{
				"m.relates_to": map[string]any{
					"rel_type": "m.replace",
					"event_id": "$m2",
				},
				"m.new_content": map[string]any{
					"body": "New bot text",
					"go24.keyboard": []any{
						map[string]any{"text": "Refreshed Button"},
					},
				},
			},
		},
	}

	applyEdits(evs, msgs)

	if msgs[0].Body != "Edited text" || !msgs[0].Edited {
		t.Errorf("msgs[0] edit = %+v", msgs[0])
	}
	if msgs[1].Body != "New bot text" || !msgs[1].Edited {
		t.Errorf("msgs[1] edit = %+v", msgs[1])
	}
	if len(msgs[1].Keyboard) != 1 || len(msgs[1].Keyboard[0]) != 1 || msgs[1].Keyboard[0][0].Text != "Refreshed Button" {
		t.Errorf("msgs[1].Keyboard = %+v", msgs[1].Keyboard)
	}
}

// TestReplyFallback verifies parsing fallback quotes in reply messages.
func TestReplyFallback(t *testing.T) {
	tests := []struct {
		body     string
		wantWho  string
		wantText string
	}{
		{
			body:     "> <@ann:example.com> Original question\n\nMy reply",
			wantWho:  "@ann:example.com",
			wantText: "Original question",
		},
		{
			body:     "> <@bob:example.com>\n> Line 1\n> Line 2\n\nReply",
			wantWho:  "@bob:example.com",
			wantText: "Line 1\nLine 2",
		},
		{
			body:     "> Plain quote without user tag\n\nReply",
			wantWho:  "",
			wantText: "Plain quote without user tag",
		},
		{
			body:     "No quote at all",
			wantWho:  "",
			wantText: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			who, text := replyFallback(tt.body)
			if who != tt.wantWho || text != tt.wantText {
				t.Errorf("replyFallback(%q) = (%q, %q), want (%q, %q)", tt.body, who, text, tt.wantWho, tt.wantText)
			}
		})
	}
}

// TestStripReplyFallback verifies removal of quoted block lines from reply bodies.
func TestStripReplyFallback(t *testing.T) {
	tests := []struct {
		body string
		want string
	}{
		{
			body: "> <@user:s> quoted text\n\nActual response",
			want: "Actual response",
		},
		{
			body: "> quote line 1\n> quote line 2\n\nResponse",
			want: "Response",
		},
		{
			body: "Regular text without quote",
			want: "Regular text without quote",
		},
		{
			body: "> only quotes\n> nothing else",
			want: "> only quotes\n> nothing else", // preserved when entire message is quote
		},
	}

	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			if got := stripReplyFallback(tt.body); got != tt.want {
				t.Errorf("stripReplyFallback(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

// TestTextContent verifies formatting detection between plain text and rich HTML.
func TestTextContent(t *testing.T) {
	// Plain text: no rich formatting
	c1 := textContent("Just plain message")
	if c1["msgtype"] != "m.text" || c1["body"] != "Just plain message" {
		t.Errorf("c1 = %+v", c1)
	}
	if _, hasFormat := c1["format"]; hasFormat {
		t.Errorf("c1 should not have format: %+v", c1)
	}

	// Formatted text with markdown link or formatting
	c2 := textContent("Check [docs](https://example.com) now")
	if c2["format"] != "org.matrix.custom.html" {
		t.Errorf("c2 format = %v, want org.matrix.custom.html", c2["format"])
	}
	if formatted, _ := c2["formatted_body"].(string); formatted == "" {
		t.Errorf("c2 formatted_body empty: %+v", c2)
	}
}

// TestLiveChange verifies detecting deletions, message edits, and call edits from live events.
func TestLiveChange(t *testing.T) {
	roomID := "!room:ex"

	// Redaction with Redacts field
	evRedact1 := matrix.Event{Type: "m.room.redaction", Redacts: "$target1"}
	m1 := liveChange(evRedact1, roomID)
	if m1 == nil || m1.ID != "$target1" || m1.Change != "delete" || m1.RoomID != roomID {
		t.Errorf("m1 = %+v", m1)
	}

	// Redaction with Content["redacts"]
	evRedact2 := matrix.Event{Type: "m.room.redaction", Content: map[string]any{"redacts": "$target2"}}
	m2 := liveChange(evRedact2, roomID)
	if m2 == nil || m2.ID != "$target2" || m2.Change != "delete" {
		t.Errorf("m2 = %+v", m2)
	}

	// Regular text edit
	evEdit := matrix.Event{
		Type: "m.room.message",
		Content: map[string]any{
			"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": "$msg1"},
			"m.new_content": map[string]any{
				"body": "Updated text",
			},
		},
	}
	m3 := liveChange(evEdit, roomID)
	if m3 == nil || m3.ID != "$msg1" || m3.Change != "edit" || m3.Body != "Updated text" || !m3.Edited {
		t.Errorf("m3 = %+v", m3)
	}

	// Call status edit
	evCallEdit := matrix.Event{
		Type: "m.room.message",
		TS:   1700000000000,
		Content: map[string]any{
			"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": "$call1"},
			"m.new_content": map[string]any{
				"body":      "Call ended",
				"go24.call": map[string]any{"state": "ended", "secs": float64(60)},
			},
		},
	}
	m4 := liveChange(evCallEdit, roomID)
	if m4 == nil || m4.ID != "$call1" || m4.Change != "edit" || m4.Call == nil || !m4.Notice {
		t.Errorf("m4 = %+v", m4)
	}

	// Irrelevant event
	evIgnored := matrix.Event{Type: "m.presence"}
	if got := liveChange(evIgnored, roomID); got != nil {
		t.Errorf("liveChange(presence) = %+v, want nil", got)
	}
}

// TestMessageEditable verifies conditions under which an employee can edit a message.
func TestMessageEditable(t *testing.T) {
	// Mine plain text message without file or forward
	m1 := Message{Mine: true, plain: true}
	if !m1.Editable() {
		t.Error("m1 should be editable")
	}

	// Not mine
	m2 := Message{Mine: false, plain: true}
	if m2.Editable() {
		t.Error("m2 (not mine) should not be editable")
	}

	// Not plain (e.g. notice or call)
	m3 := Message{Mine: true, plain: false}
	if m3.Editable() {
		t.Error("m3 (not plain) should not be editable")
	}

	// Has file attachment
	m4 := Message{Mine: true, plain: true, File: &File{Name: "doc.pdf"}}
	if m4.Editable() {
		t.Error("m4 (file attached) should not be editable")
	}

	// Forwarded message
	m5 := Message{Mine: true, plain: true, Forwarded: "Original Author"}
	if m5.Editable() {
		t.Error("m5 (forwarded) should not be editable")
	}
}

// TestRestrictedJoin verifies identifying users joining an open chat without invitation.
func TestRestrictedJoin(t *testing.T) {
	// Authorized join without prior membership
	ev1 := matrix.Event{
		Content: map[string]any{"join_authorised_via_users_server": "example.com"},
	}
	ev1.Unsigned.PrevContent = map[string]any{"membership": "leave"}
	if !restrictedJoin(ev1) {
		t.Error("ev1 should be a restricted join")
	}

	// Prior membership was "join" -> false
	ev2 := matrix.Event{
		Content: map[string]any{"join_authorised_via_users_server": "example.com"},
	}
	ev2.Unsigned.PrevContent = map[string]any{"membership": "join"}
	if restrictedJoin(ev2) {
		t.Error("ev2 should NOT be a restricted join (was already joined)")
	}

	// Prior membership was "invite" -> false
	ev3 := matrix.Event{
		Content: map[string]any{"join_authorised_via_users_server": "example.com"},
	}
	ev3.Unsigned.PrevContent = map[string]any{"membership": "invite"}
	if restrictedJoin(ev3) {
		t.Error("ev3 should NOT be a restricted join (joined via invite)")
	}

	// Missing join_authorised_via_users_server -> false
	ev4 := matrix.Event{
		Content: map[string]any{},
	}
	if restrictedJoin(ev4) {
		t.Error("ev4 should NOT be a restricted join")
	}
}

// TestStateLevel verifies extracting required power level for a state event type.
func TestStateLevel(t *testing.T) {
	// From events map as float64
	pl1 := matrix.PowerLevels{
		"events": map[string]any{
			"m.room.name": float64(50),
		},
	}
	if got := stateLevel(pl1, "m.room.name"); got != 50 {
		t.Errorf("stateLevel(pl1) = %d, want 50", got)
	}

	// Fallback to state_default as float64
	pl2 := matrix.PowerLevels{
		"state_default": float64(25),
	}
	if got := stateLevel(pl2, "m.room.name"); got != 25 {
		t.Errorf("stateLevel(pl2) = %d, want 25", got)
	}

	// Default fallback to 50
	pl3 := matrix.PowerLevels{}
	if got := stateLevel(pl3, "m.room.name"); got != 50 {
		t.Errorf("stateLevel(pl3) = %d, want default 50", got)
	}
}

// TestPersonName verifies user display name resolution.
func TestPersonName(t *testing.T) {
	who := map[string]imPerson{
		"@alice:example.com": {Name: "Алиса"},
	}

	// In who map
	if got := personName("@alice:example.com", who); got != "Алиса" {
		t.Errorf("personName = %q, want 'Алиса'", got)
	}

	// Fallback to username from Matrix ID
	if got := personName("@bob:matrix.org", who); got != "bob" {
		t.Errorf("personName = %q, want 'bob'", got)
	}

	// Fallback without @
	if got := personName("carol:matrix.org", who); got != "carol" {
		t.Errorf("personName = %q, want 'carol'", got)
	}
}

// TestForbiddenError verifies error transformation into err.forbidden.
func TestForbiddenError(t *testing.T) {
	isForbidden := func(err error) bool {
		e, ok := err.(*i18n.Error)
		return ok && e.Key == "err.forbidden"
	}

	mForbidden := &matrix.Error{Code: "M_FORBIDDEN", Message: "Access denied"}
	if err := forbidden(mForbidden); !isForbidden(err) {
		t.Errorf("forbidden(M_FORBIDDEN) = %v, want err.forbidden", err)
	}

	mNotFound := &matrix.Error{Code: "M_NOT_FOUND", Message: "Not found"}
	if err := forbidden(mNotFound); !isForbidden(err) {
		t.Errorf("forbidden(M_NOT_FOUND) = %v, want err.forbidden", err)
	}

	otherErr := errors.New("other network error")
	if err := forbidden(otherErr); !errors.Is(err, otherErr) {
		t.Errorf("forbidden(other) = %v, want %v", err, otherErr)
	}

	if err := forbidden(nil); err != nil {
		t.Errorf("forbidden(nil) = %v, want nil", err)
	}
}

// TestApplyReactionsExtended verifies multiple emojis, unmapped senders, and reaction deduplication.
func TestApplyReactionsExtended(t *testing.T) {
	msgs := []Message{
		{ID: "$msgA"},
		{ID: "$msgB"},
	}

	// /messages order: newest first (so iterating backwards processes oldest first)
	evs := []matrix.Event{
		{Type: "m.reaction", Sender: "@me:server", Content: map[string]any{
			"m.relates_to": map[string]any{"event_id": "$msgA", "key": "🚀"},
		}},
		{Type: "m.reaction", Sender: "@dave:server", Content: map[string]any{
			"m.relates_to": map[string]any{"event_id": "$msgA", "key": "🚀"},
		}},
		{Type: "m.reaction", Sender: "@dave:server", Content: map[string]any{
			"m.relates_to": map[string]any{"event_id": "$msgB", "key": "❤️"},
		}},
		// Invalid / ignored reaction (no key or unknown target)
		{Type: "m.reaction", Sender: "@me:server", Content: map[string]any{
			"m.relates_to": map[string]any{"event_id": "$msgA", "key": ""},
		}},
		{Type: "m.reaction", Sender: "@me:server", Content: map[string]any{
			"m.relates_to": map[string]any{"event_id": "$unknown", "key": "👍"},
		}},
	}

	applyReactions(evs, msgs, "@me:server", map[string]imPerson{"@me:server": {Name: "Myself"}})

	// $msgA should have 🚀 with count 2, mine=true, names "dave, Myself"
	rA := msgs[0].Reactions
	if len(rA) != 1 || rA[0].Key != "🚀" || rA[0].Count != 2 || !rA[0].Mine || rA[0].Names != "dave, Myself" {
		t.Errorf("rA = %+v", rA)
	}

	// $msgB should have ❤️ with count 1, mine=false, names "dave"
	rB := msgs[1].Reactions
	if len(rB) != 1 || rB[0].Key != "❤️" || rB[0].Count != 1 || rB[0].Mine || rB[0].Names != "dave" {
		t.Errorf("rB = %+v", rB)
	}
}
