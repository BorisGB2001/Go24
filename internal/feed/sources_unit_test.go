package feed

import (
	"testing"
	"time"
)

func TestPostSourceTime(t *testing.T) {
	for _, c := range []struct {
		name       string
		sourceData map[string]string
		key        string
		want       time.Time
	}{
		{
			name:       "nil source data",
			sourceData: nil,
			key:        "start",
			want:       time.Time{},
		},
		{
			name:       "missing key",
			sourceData: map[string]string{"other": "2026-10-28T14:30:00Z"},
			key:        "start",
			want:       time.Time{},
		},
		{
			name:       "invalid time string",
			sourceData: map[string]string{"start": "not-a-timestamp"},
			key:        "start",
			want:       time.Time{},
		},
		{
			name:       "valid UTC RFC3339",
			sourceData: map[string]string{"start": "2026-10-28T14:30:00Z"},
			key:        "start",
			want:       time.Date(2026, 10, 28, 14, 30, 0, 0, time.UTC),
		},
		{
			name:       "valid RFC3339 with offset",
			sourceData: map[string]string{"start": "2026-10-28T17:30:00+03:00"},
			key:        "start",
			want:       time.Date(2026, 10, 28, 17, 30, 0, 0, time.FixedZone("", 3*3600)),
		},
	} {
		p := &Post{SourceData: c.sourceData}
		got := p.SourceTime(c.key)
		if !got.Equal(c.want) {
			t.Errorf("%s: SourceTime(%q) = %v, want %v", c.name, c.key, got, c.want)
		}
	}
}

func TestSourceConstants(t *testing.T) {
	if SourceWelcome != "welcome" {
		t.Errorf("SourceWelcome = %q, want 'welcome'", SourceWelcome)
	}
	if SourceMeeting != "meeting" {
		t.Errorf("SourceMeeting = %q, want 'meeting'", SourceMeeting)
	}
	if SourceKB != "kb" {
		t.Errorf("SourceKB = %q, want 'kb'", SourceKB)
	}
}
