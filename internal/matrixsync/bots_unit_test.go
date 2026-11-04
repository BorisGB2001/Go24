package matrixsync

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestBotFullName verifies bot full name construction.
func TestBotFullName(t *testing.T) {
	tests := []struct {
		name     string
		bot      Bot
		wantFull string
	}{
		{
			name:     "first and last name",
			bot:      Bot{Name: "Support", LastName: "Bot"},
			wantFull: "Support Bot",
		},
		{
			name:     "first name only",
			bot:      Bot{Name: "Assistant", LastName: ""},
			wantFull: "Assistant",
		},
		{
			name:     "extra whitespace trimmed",
			bot:      Bot{Name: "  Helper  ", LastName: "   "},
			wantFull: "Helper",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.bot.FullName(); got != tt.wantFull {
				t.Errorf("FullName() = %q, want %q", got, tt.wantFull)
			}
		})
	}
}

// TestNormBot verifies validation and field normalization when registering bots.
func TestNormBot(t *testing.T) {
	// Valid bot with whitespace and lowercase color
	b1 := Bot{
		Code:         "  my_bot  ",
		Name:         "  My Bot  ",
		LastName:     "  V1  ",
		WorkPosition: "  Helper  ",
		Color:        "  #abcdef  ",
		Type:         "  ", // defaults to B
	}
	if err := normBot(&b1); err != nil {
		t.Fatalf("normBot(b1) error: %v", err)
	}
	if b1.Code != "my_bot" || b1.Name != "My Bot" || b1.LastName != "V1" || b1.WorkPosition != "Helper" {
		t.Errorf("b1 trimmed fields mismatch: %+v", b1)
	}
	if b1.Color != "#ABCDEF" || b1.Type != "B" {
		t.Errorf("b1 Color/Type = %q/%q, want #ABCDEF/B", b1.Color, b1.Type)
	}

	// Open line bot ("O") normalized to "B"
	bOpen := Bot{Name: "OpenBot", Type: "O"}
	if err := normBot(&bOpen); err != nil || bOpen.Type != "B" {
		t.Errorf("normBot('O') err = %v, Type = %q, want B", err, bOpen.Type)
	}

	// Valid types "H" (human) and "S" (supervisor)
	for _, typ := range []string{"H", "h", "S", "s"} {
		b := Bot{Name: "Bot", Type: typ}
		if err := normBot(&b); err != nil || b.Type != strings.ToUpper(typ) {
			t.Errorf("normBot(%q) err = %v, Type = %q", typ, err, b.Type)
		}
	}

	// Invalid bot type error
	bBadType := Bot{Name: "Bot", Type: "INVALID"}
	if err := normBot(&bBadType); err == nil {
		t.Error("normBot with invalid type expected error")
	}

	// Empty name error
	bNoName := Bot{Name: "   ", Type: "B"}
	if err := normBot(&bNoName); err == nil {
		t.Error("normBot with empty name expected error")
	}

	// Name > 100 runes error
	bLongName := Bot{Name: strings.Repeat("Б", 101), Type: "B"}
	if err := normBot(&bLongName); err == nil {
		t.Error("normBot with name > 100 runes expected error")
	}
}

// TestBotUser verifies serialization of employee info for bot events.
func TestBotUser(t *testing.T) {
	// Regular employee
	p1 := imPerson{ID: "u100", Name: "Alice", Bot: false, Guest: false}
	u1 := botUser(p1)
	want1 := map[string]any{"ID": "u100", "NAME": "Alice", "IS_BOT": "N", "IS_EXTRANET": "N"}
	if !reflect.DeepEqual(u1, want1) {
		t.Errorf("botUser(p1) = %+v, want %+v", u1, want1)
	}

	// Bot and Extranet guest
	p2 := imPerson{ID: "u200", Name: "ExtBot", Bot: true, Guest: true}
	u2 := botUser(p2)
	want2 := map[string]any{"ID": "u200", "NAME": "ExtBot", "IS_BOT": "Y", "IS_EXTRANET": "Y"}
	if !reflect.DeepEqual(u2, want2) {
		t.Errorf("botUser(p2) = %+v, want %+v", u2, want2)
	}
}

// TestThreadRel verifies construction of thread relationship maps.
func TestThreadRel(t *testing.T) {
	got := threadRel("$root_event_id")
	want := map[string]any{
		"rel_type":        "m.thread",
		"event_id":        "$root_event_id",
		"is_falling_back": true,
		"m.in_reply_to":   map[string]string{"event_id": "$root_event_id"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("threadRel() = %+v, want %+v", got, want)
	}
}

// TestIgnoreTaskRights verifies error filtering for tasks.ErrForbidden and tasks.ErrNotFound.
func TestIgnoreTaskRights(t *testing.T) {
	if err := ignoreTaskRights(tasks.ErrForbidden); err != nil {
		t.Errorf("ignoreTaskRights(tasks.ErrForbidden) = %v, want nil", err)
	}
	if err := ignoreTaskRights(tasks.ErrNotFound); err != nil {
		t.Errorf("ignoreTaskRights(tasks.ErrNotFound) = %v, want nil", err)
	}

	custom := errors.New("unrelated error")
	if err := ignoreTaskRights(custom); !errors.Is(err, custom) {
		t.Errorf("ignoreTaskRights(custom) = %v, want %v", err, custom)
	}

	if err := ignoreTaskRights(nil); err != nil {
		t.Errorf("ignoreTaskRights(nil) = %v, want nil", err)
	}
}

// TestMXIDOf verifies localpart validation and Matrix ID generation.
func TestMXIDOf(t *testing.T) {
	s := &Service{
		M: matrix.New("http://localhost", "example.com", "token"),
	}

	tests := []struct {
		username string
		wantMXID string
		wantOK   bool
	}{
		{username: "alice", wantMXID: "@alice:example.com", wantOK: true},
		{username: "  Bob.Smith_99  ", wantMXID: "@bob.smith_99:example.com", wantOK: true},
		{username: "user-name=test/dev+1", wantMXID: "@user-name=test/dev+1:example.com", wantOK: true},
		{username: "bad username with spaces", wantOK: false},
		{username: "bad@char!", wantOK: false},
		{username: "", wantOK: false},
		{username: "   ", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.username, func(t *testing.T) {
			got, ok := s.mxidOf(tt.username)
			if ok != tt.wantOK {
				t.Fatalf("s.mxidOf(%q) ok = %v, want %v", tt.username, ok, tt.wantOK)
			}
			if tt.wantOK && got != tt.wantMXID {
				t.Errorf("s.mxidOf(%q) = %q, want %q", tt.username, got, tt.wantMXID)
			}
		})
	}
}

// TestCollabLogKinds verifies mapping from collab log actions to settings categories.
func TestCollabLogKinds(t *testing.T) {
	tests := []struct {
		action   string
		wantKind string
	}{
		{action: "member_added", wantKind: "members"},
		{action: "member_joined", wantKind: "members"},
		{action: "member_removed", wantKind: "members"},
		{action: "left", wantKind: "members"},
		{action: "role_changed", wantKind: "members"},
		{action: "owner_changed", wantKind: "members"},
		{action: "joined", wantKind: "guests"},
		{action: "invite_created", wantKind: "invites"},
		{action: "invite_resent", wantKind: "invites"},
		{action: "invite_revoked", wantKind: "invites"},
		{action: "settings", wantKind: "collab"},
		{action: "info", wantKind: "collab"},
		{action: "archived", wantKind: "collab"},
		{action: "restored", wantKind: "collab"},
		{action: "chat_created", wantKind: "collab"},
		{action: "unknown_action", wantKind: ""},
	}

	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			if got := collabLogKinds[tt.action]; got != tt.wantKind {
				t.Errorf("collabLogKinds[%q] = %q, want %q", tt.action, got, tt.wantKind)
			}
		})
	}
}

// TestBotCommandErrors verifies that errCommand has the expected translatable error key.
func TestBotCommandErrors(t *testing.T) {
	if errCommand == nil || errCommand.Key != "err.imbot_command" {
		t.Errorf("errCommand = %+v, want key 'err.imbot_command'", errCommand)
	}
	if errAttach == nil || errAttach.Key != "err.imbot_attach" {
		t.Errorf("errAttach = %+v, want key 'err.imbot_attach'", errAttach)
	}
	if errCommand.Error() != "err.imbot_command" {
		t.Errorf("errCommand.Error() = %q", errCommand.Error())
	}
}
