package matrixsync

import (
	"errors"
	"testing"

	"go24/internal/feed"
)

// TestFeedMine checks detection of go24-originated event markers.
func TestFeedMine(t *testing.T) {
	tests := []struct {
		name    string
		content map[string]any
		want    bool
	}{
		{name: "empty content", content: map[string]any{}, want: false},
		{name: "regular matrix message", content: map[string]any{"body": "hi", "msgtype": "m.text"}, want: false},
		{name: "go24.comment key", content: map[string]any{"body": "hi", "go24.comment": true}, want: true},
		{name: "go24.forwarded key", content: map[string]any{"go24.forwarded": map[string]any{}}, want: true},
		{name: "other prefix", content: map[string]any{"other.go24": true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mine(tt.content); got != tt.want {
				t.Errorf("mine() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPostText verifies post rendering into plain text and HTML for Matrix.
func TestPostText(t *testing.T) {
	// Post with title, important flag, and special HTML characters
	b1 := feed.Bridge{
		Title:     "Announce <Q3>",
		Body:      "Line 1 & more\nLine 2",
		Important: true,
	}
	plain1, rich1 := postText(b1)

	wantPlain1 := "❗ Announce <Q3>\nLine 1 & more\nLine 2"
	wantRich1 := "❗ <b>Announce &lt;Q3&gt;</b><br>Line 1 &amp; more<br>Line 2"

	if plain1 != wantPlain1 {
		t.Errorf("plain1 = %q, want %q", plain1, wantPlain1)
	}
	if rich1 != wantRich1 {
		t.Errorf("rich1 = %q, want %q", rich1, wantRich1)
	}

	// Post without title and not important
	b2 := feed.Bridge{
		Title:     "",
		Body:      "Single line",
		Important: false,
	}
	plain2, rich2 := postText(b2)
	if plain2 != "Single line" || rich2 != "Single line" {
		t.Errorf("plain2 = %q, rich2 = %q", plain2, rich2)
	}
}

// TestSplitTitle verifies extracting title and text from post bodies.
func TestSplitTitle(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantTitle string
		wantRest  string
	}{
		{
			name:      "bold markdown title",
			body:      "**Important Update**\nHere are the details.",
			wantTitle: "Important Update",
			wantRest:  "Here are the details.",
		},
		{
			name:      "important emoji prefix with bold title",
			body:      "❗ **Alert**\nSystem maintenance at 22:00",
			wantTitle: "Alert",
			wantRest:  "System maintenance at 22:00",
		},
		{
			name:      "no bold on first line",
			body:      "Regular first line\nSecond line",
			wantTitle: "",
			wantRest:  "Regular first line\nSecond line",
		},
		{
			name:      "single line without newline",
			body:      "**Only title**",
			wantTitle: "",
			wantRest:  "**Only title**",
		},
		{
			name:      "bold first line but rest is empty",
			body:      "**Title**\n   ",
			wantTitle: "",
			wantRest:  "**Title**", // TrimSpace at start of splitTitle trims trailing spaces
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			title, rest := splitTitle(tt.body)
			if title != tt.wantTitle || rest != tt.wantRest {
				t.Errorf("splitTitle(%q) = (%q, %q), want (%q, %q)", tt.body, title, rest, tt.wantTitle, tt.wantRest)
			}
		})
	}
}

// TestIgnoreRights verifies filtering of feed permission errors.
func TestIgnoreRights(t *testing.T) {
	if err := ignoreRights(feed.ErrForbidden); err != nil {
		t.Errorf("ignoreRights(ErrForbidden) = %v, want nil", err)
	}
	if err := ignoreRights(feed.ErrNotFound); err != nil {
		t.Errorf("ignoreRights(ErrNotFound) = %v, want nil", err)
	}
	customErr := errors.New("database failure")
	if err := ignoreRights(customErr); !errors.Is(err, customErr) {
		t.Errorf("ignoreRights(customErr) = %v, want %v", err, customErr)
	}
	if err := ignoreRights(nil); err != nil {
		t.Errorf("ignoreRights(nil) = %v, want nil", err)
	}
}
