package matrixsync

import (
	"reflect"
	"testing"
	"time"
)

// TestLinkRe verifies web URL regex matching and character boundaries.
func TestLinkRe(t *testing.T) {
	tests := []struct {
		text string
		want []string
	}{
		{
			text: "Check https://example.com and http://test.org/path?q=1#frag for info.",
			want: []string{"https://example.com", "http://test.org/path?q=1#frag"},
		},
		{
			text: "Links in delimiters: <https://a.com> 'http://b.com' \"https://c.com\" «http://d.com»",
			want: []string{"https://a.com", "http://b.com", "https://c.com", "http://d.com"},
		},
		{
			text: "Not a web url: ftp://example.com or mailto:user@example.com",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got := linkRe.FindAllString(tt.text, -1)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("linkRe.FindAllString(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

// TestMessageLinks verifies extraction, filtering, trailing punctuation stripping, and deduplication of links from messages.
func TestMessageLinks(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	msgs := []Message{
		// Newest first (m1 is newest)
		{
			ID:         "m1",
			SenderName: "Alice",
			At:         t3,
			Body:       "See (https://go.dev/doc) and https://example.com/item, right?",
		},
		{
			ID:         "m2_dup",
			SenderName: "Bob",
			At:         t2,
			Body:       "Old mention of https://example.com/item and https://unique.org/about;",
		},
		// Message with a quoted reply fallback: the quoted link should NOT be extracted
		{
			ID:         "m3_reply",
			SenderName: "Carol",
			At:         t1,
			Body:       "> <@alice:x> quote with https://ignored-in-quote.com\n\nactual text https://carol-link.com!",
		},
		// Messages that must be skipped:
		{ID: "skip_file", File: &File{Name: "pic.png"}, Body: "https://file-link.com"},
		{ID: "skip_encrypted", Encrypted: true, Body: "https://enc-link.com"},
		{ID: "skip_event", Event: "joined", Body: "https://event-link.com"},
		{ID: "skip_call", Call: &CallLine{State: "ended"}, Body: "https://call-link.com"},
		{ID: "skip_poll", Poll: &Poll{Question: "Q"}, Body: "https://poll-link.com"},
	}

	links := messageLinks(msgs)

	// Expected links:
	// From m1: https://go.dev/doc (trimmed ')') and https://example.com/item (trimmed ',')
	// From m2: https://unique.org/about (trimmed ';'), while https://example.com/item is seen so skipped
	// From m3: https://carol-link.com (trimmed '!'), while quote link is stripped
	want := []Link{
		{URL: "https://go.dev/doc", MessageID: "m1", SenderName: "Alice", At: t3},
		{URL: "https://example.com/item", MessageID: "m1", SenderName: "Alice", At: t3},
		{URL: "https://unique.org/about", MessageID: "m2_dup", SenderName: "Bob", At: t2},
		{URL: "https://carol-link.com", MessageID: "m3_reply", SenderName: "Carol", At: t1},
	}

	if len(links) != len(want) {
		t.Fatalf("len(links) = %d, want %d: %+v", len(links), len(want), links)
	}
	for i := range want {
		if links[i].URL != want[i].URL || links[i].MessageID != want[i].MessageID || links[i].SenderName != want[i].SenderName {
			t.Errorf("links[%d] = %+v, want %+v", i, links[i], want[i])
		}
	}
}
