package crm

import (
	"slices"
	"strings"
	"testing"
)

func TestLinkURL_Unit(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "valid https url",
			input: "https://example.com/promo/123",
			want:  "https://example.com/promo/123",
		},
		{
			name:  "valid http url with port and query",
			input: "http://localhost:8080/path?foo=bar&baz=1",
			want:  "http://localhost:8080/path?foo=bar&baz=1",
		},
		{
			name:  "trims whitespace",
			input: "   https://example.com/test   \n",
			want:  "https://example.com/test",
		},
		{
			name:  "ftp scheme rejected",
			input: "ftp://example.com/file",
			want:  "",
		},
		{
			name:  "missing host rejected",
			input: "https:///path",
			want:  "",
		},
		{
			name:  "relative path rejected",
			input: "/relative/path",
			want:  "",
		},
		{
			name:  "empty string rejected",
			input: "   ",
			want:  "",
		},
		{
			name:  "url exceeding maxLinkURL 2000 rejected",
			input: "https://example.com/" + strings.Repeat("a", 2005),
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkURL(tt.input); got != tt.want {
				t.Errorf("linkURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLinkInText_Unit(t *testing.T) {
	text := "Здравствуйте! Перейдите по ссылке https://example.com/offer?id=123, чтобы узнать детали."
	matches := linkInText.FindAllString(text, -1)
	if len(matches) != 1 {
		t.Fatalf("linkInText found %d matches, want 1: %v", len(matches), matches)
	}
	if !strings.HasPrefix(matches[0], "https://example.com/offer?id=123") {
		t.Errorf("matched text = %q, want prefix https://example.com/offer?id=123", matches[0])
	}
}

func TestTriggerKinds_Unit(t *testing.T) {
	requiredKinds := []string{
		"mail", "form", "openline", "activity", "invoice_paid", "quote_accepted",
		"booking", "booking_confirmed", "booking_came", "booking_no_show", "booking_cancelled", "booking_moved",
		"call", "call_missed", "field", "webhook", "doc_viewed", "doc_signed", "task", "link",
	}

	for _, k := range requiredKinds {
		if !slices.Contains(TriggerKinds, k) {
			t.Errorf("TriggerKinds missing expected kind %q", k)
		}
	}
}
