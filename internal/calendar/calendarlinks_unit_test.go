package calendar

import (
	"strings"
	"testing"
)

func TestLinkURL(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{
			in:   "https://example.com/calendar.ics",
			want: "https://example.com/calendar.ics",
		},
		{
			in:   "http://example.com/calendar.ics",
			want: "http://example.com/calendar.ics",
		},
		{
			in:   "webcal://example.com/calendar.ics",
			want: "https://example.com/calendar.ics",
		},
		{
			in:   "WEBCAL://Example.com/calendar.ics",
			want: "https://Example.com/calendar.ics",
		},
		{
			in:   "https://example.com/calendar.ics#events-fragment",
			want: "https://example.com/calendar.ics",
		},
		{
			in:   "  https://example.com/cal.ics  ",
			want: "https://example.com/cal.ics",
		},
		{
			in:      "ftp://example.com/cal.ics",
			wantErr: true,
		},
		{
			in:      "https://user:pass@example.com/cal.ics",
			wantErr: true,
		},
		{
			in:      "https://",
			wantErr: true,
		},
		{
			in:      "",
			wantErr: true,
		},
		{
			in:      "https://example.com/" + strings.Repeat("a", 2005),
			wantErr: true,
		},
	}

	for _, tc := range cases {
		got, err := LinkURL(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("LinkURL(%q) err = %v, wantErr = %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("LinkURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
