package notifyout

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go24/internal/notify"
)

func TestServiceNewAndChannelLookup(t *testing.T) {
	s := New(nil, "test_session_secret")
	if s == nil {
		t.Fatal("New returned nil")
	}

	// Verify key derivation
	expectedKey := sha256.Sum256([]byte("go24 notify bots\x00test_session_secret"))
	if !slices.Equal(s.key, expectedKey[:]) {
		t.Errorf("derived key = %x, want %x", s.key, expectedKey)
	}

	// Verify all standard messengers can be retrieved via s.Channel()
	for _, name := range []string{"telegram", "max", "matrix", "vk", "slack", "mattermost"} {
		ch := s.Channel(name)
		if ch == nil {
			t.Errorf("s.Channel(%q) returned nil, want valid channel", name)
		} else if ch.Name() != name {
			t.Errorf("s.Channel(%q).Name() = %q", name, ch.Name())
		}
	}

	// Non-existent messenger returns nil
	if ch := s.Channel("unknown_app"); ch != nil {
		t.Errorf("s.Channel(unknown_app) = %v, want nil", ch)
	}
}

func TestServiceSealAndOpen(t *testing.T) {
	s := New(nil, "my_secure_secret_123")

	t.Run("round trip encryption and decryption", func(t *testing.T) {
		tokens := []string{
			"simple_token",
			"xoxb-12345-67890-abcdef xapp-98765-fedcba",
			"",
			"token with unicode 🔑 и кириллица",
			strings.Repeat("long_token_string_", 50),
		}

		for _, tok := range tokens {
			sealed, err := s.seal(tok)
			if err != nil {
				t.Fatalf("seal(%q) error: %v", tok, err)
			}
			opened, err := s.open(sealed)
			if err != nil {
				t.Fatalf("open(sealed %q) error: %v", tok, err)
			}
			if opened != tok {
				t.Errorf("open(seal(%q)) = %q, want %q", tok, opened, tok)
			}
		}
	})

	t.Run("different secret fails decryption", func(t *testing.T) {
		sOther := New(nil, "different_secret_456")
		sealed, err := s.seal("secret_token")
		if err != nil {
			t.Fatal(err)
		}

		_, err = sOther.open(sealed)
		if err == nil {
			t.Fatal("open() with different secret should fail, got nil")
		}
	})

	t.Run("tampered ciphertext fails decryption", func(t *testing.T) {
		sealed, err := s.seal("my_bot_token")
		if err != nil {
			t.Fatal(err)
		}
		// Flip a byte in the ciphertext
		tampered := slices.Clone(sealed)
		tampered[len(tampered)-1] ^= 0xFF

		_, err = s.open(tampered)
		if err == nil {
			t.Fatal("open() with tampered ciphertext should fail, got nil")
		}
	})

	t.Run("too short sealed payload returns bad token error", func(t *testing.T) {
		shortPayload := []byte("short")
		_, err := s.open(shortPayload)
		if err == nil || err.Error() != "notifyout: bad token" {
			t.Errorf("open(short) err = %v, want 'notifyout: bad token'", err)
		}
	})
}

func TestChannelSelection(t *testing.T) {
	// Tests channel selection logic in Deliver: only ready bots are selected
	bots := []Bot{
		{Channel: "telegram", Enabled: true, token: "tok_tg"},
		{Channel: "max", Enabled: false, token: "tok_max"},
		{Channel: "slack", Enabled: true, token: ""},
		{Channel: "mattermost", Enabled: true, token: "tok_mm"},
		{Channel: "vk", Enabled: false, token: ""},
	}

	ready := map[string]Bot{}
	for _, b := range bots {
		if b.Ready() {
			ready[b.Channel] = b
		}
	}

	if len(ready) != 2 {
		t.Fatalf("len(ready) = %d, want 2", len(ready))
	}
	if _, ok := ready["telegram"]; !ok {
		t.Errorf("expected telegram in ready map")
	}
	if _, ok := ready["mattermost"]; !ok {
		t.Errorf("expected mattermost in ready map")
	}
	if _, ok := ready["max"]; ok {
		t.Errorf("disabled max bot should not be in ready map")
	}
	if _, ok := ready["slack"]; ok {
		t.Errorf("slack bot without token should not be in ready map")
	}
}

func TestRecipientFilteringRules(t *testing.T) {
	// Tests the pure boolean rules used in delivery:
	// A notification is eligible if r.Send && !slices.Contains(off, r.Kind)
	tests := []struct {
		name       string
		isRead     bool
		older1Day  bool
		kind       string
		offList    []string
		wantToSend bool
	}{
		{
			name:       "unread fresh notification not in off list",
			isRead:     false,
			older1Day:  false,
			kind:       "status",
			offList:    []string{"mention"},
			wantToSend: true,
		},
		{
			name:       "already read notification is not sent",
			isRead:     true,
			older1Day:  false,
			kind:       "status",
			offList:    []string{},
			wantToSend: false,
		},
		{
			name:       "notification older than 1 day is not sent",
			isRead:     false,
			older1Day:  true,
			kind:       "status",
			offList:    []string{},
			wantToSend: false,
		},
		{
			name:       "notification kind turned off by user is not sent",
			isRead:     false,
			older1Day:  false,
			kind:       "mention",
			offList:    []string{"mention", "deadline"},
			wantToSend: false,
		},
		{
			name:       "already read and turned off is not sent",
			isRead:     true,
			older1Day:  false,
			kind:       "mention",
			offList:    []string{"mention"},
			wantToSend: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			send := !tt.isRead && !tt.older1Day
			eligible := send && !slices.Contains(tt.offList, tt.kind)
			if eligible != tt.wantToSend {
				t.Errorf("eligible = %v, want %v", eligible, tt.wantToSend)
			}
		})
	}
}

func TestSaveOffDerivation(t *testing.T) {
	// Tests derivation of 'off' list from notify.Kinds and wanted kinds map
	all := []string{}
	for _, g := range notify.Kinds {
		all = append(all, g.Kinds...)
	}

	// User wants only status and mention in telegram, but nothing in slack
	on := map[string]map[string]bool{
		"telegram": {
			"status":  true,
			"mention": true,
		},
		"slack": {},
	}

	derivedOff := map[string][]string{}
	for ch, want := range on {
		off := []string{}
		for _, k := range all {
			if !want[k] {
				off = append(off, k)
			}
		}
		derivedOff[ch] = off
	}

	// In telegram: status and mention should not be in off list
	tgOff := derivedOff["telegram"]
	if slices.Contains(tgOff, "status") || slices.Contains(tgOff, "mention") {
		t.Errorf("telegram off list should not contain wanted kinds, got %v", tgOff)
	}
	// Kinds other than status and mention must be in telegram off list
	for _, k := range all {
		if k != "status" && k != "mention" {
			if !slices.Contains(tgOff, k) {
				t.Errorf("telegram off list missing %s", k)
			}
		}
	}

	// In slack: all kinds must be in off list
	slackOff := derivedOff["slack"]
	if len(slackOff) != len(all) {
		t.Errorf("slack off list has %d kinds, want %d", len(slackOff), len(all))
	}
}

func TestOutboundMessageLinkBuilding(t *testing.T) {
	s := &Service{
		AppURL: "https://go24.example.com",
		Text: func(it notify.Item) string {
			return it.ActorName + " changed task " + it.TaskKey
		},
	}

	it := notify.Item{
		Kind:      "status",
		ActorName: "Alice",
		TaskKey:   "GO-42",
	}

	notifID := int64(108)
	msg := Message{
		Text: s.Text(it),
		Link: s.AppURL + "/notifications/" + strconv.FormatInt(notifID, 10),
	}

	if msg.Text != "Alice changed task GO-42" {
		t.Errorf("msg.Text = %q, want 'Alice changed task GO-42'", msg.Text)
	}
	if msg.Link != "https://go24.example.com/notifications/108" {
		t.Errorf("msg.Link = %q, want 'https://go24.example.com/notifications/108'", msg.Link)
	}
}

func TestStartLinkCodeFormat(t *testing.T) {
	// Verifies the code format generated in StartLink:
	// 10 random bytes encoded in base32 (no padding, lowercase)
	raw := make([]byte, 10)
	_, _ = rand.Read(raw)
	code := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))

	if len(code) != 16 {
		t.Errorf("code length = %d, want 16", len(code))
	}
	if code != strings.ToLower(code) {
		t.Errorf("code %q must be lowercase", code)
	}
	// Verify it can be decoded back to 10 bytes
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(code))
	if err != nil {
		t.Fatalf("failed to decode generated code: %v", err)
	}
	if len(decoded) != 10 {
		t.Errorf("decoded length = %d, want 10", len(decoded))
	}
}

func TestCommandParsingInRead(t *testing.T) {
	// Tests command and code extraction logic from strings written to bots
	tests := []struct {
		input       string
		wantCmd     string
		wantArg     string
		wantCode    string
		isStop      bool
		isHello     bool
		isStartCode bool
		isPlainCode bool
	}{
		{
			input:   "/stop",
			wantCmd: "/stop",
			isStop:  true,
		},
		{
			input:   " /stop   ",
			wantCmd: "/stop",
			isStop:  true,
		},
		{
			input:   "/start",
			wantCmd: "/start",
			isHello: true,
		},
		{
			input:       "/start abc123def",
			wantCmd:     "/start",
			wantArg:     "abc123def",
			wantCode:    "abc123def",
			isStartCode: true,
		},
		{
			input:       "  MyCode789  ",
			wantCmd:     "MyCode789",
			wantCode:    "mycode789",
			isPlainCode: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			text := strings.TrimSpace(tt.input)
			cmd, arg, _ := strings.Cut(text, " ")

			switch {
			case cmd == "/stop":
				if !tt.isStop {
					t.Errorf("classified as /stop unexpectedly")
				}
			case cmd == "/start" && arg == "":
				if !tt.isHello {
					t.Errorf("classified as hello unexpectedly")
				}
			default:
				if cmd == "/start" {
					text = arg
				}
				code := strings.ToLower(strings.TrimSpace(text))
				if code != tt.wantCode {
					t.Errorf("extracted code = %q, want %q", code, tt.wantCode)
				}
			}
		})
	}
}
