package matrixsync

import (
	"reflect"
	"testing"
)

// TestCallOf verifies parsing of CallLine from go24.call event content.
func TestCallOf(t *testing.T) {
	// Nil / missing / invalid
	if got := callOf(nil); got != nil {
		t.Errorf("callOf(nil) = %+v, want nil", got)
	}
	if got := callOf(map[string]any{}); got != nil {
		t.Errorf("callOf(empty) = %+v, want nil", got)
	}
	if got := callOf(map[string]any{"go24.call": "invalid"}); got != nil {
		t.Errorf("callOf(not map) = %+v, want nil", got)
	}
	if got := callOf(map[string]any{"go24.call": map[string]any{"state": ""}}); got != nil {
		t.Errorf("callOf(empty state) = %+v, want nil", got)
	}

	// Valid with float64 secs and []any people
	c1 := map[string]any{
		"go24.call": map[string]any{
			"state":  "ended",
			"video":  true,
			"secs":   float64(125),
			"people": []any{"Alice", "Bob", 123},
		},
	}
	got1 := callOf(c1)
	if got1 == nil {
		t.Fatal("callOf(c1) returned nil")
	}
	if got1.State != "ended" || !got1.Video || got1.Secs != 125 || len(got1.People) != 2 || got1.People[0] != "Alice" || got1.People[1] != "Bob" {
		t.Errorf("got1 = %+v", got1)
	}

	// Valid with int secs and []string people
	c2 := map[string]any{
		"go24.call": map[string]any{
			"state":  "started",
			"video":  false,
			"secs":   int(0),
			"people": []string{"Carol"},
		},
	}
	got2 := callOf(c2)
	if got2 == nil {
		t.Fatal("callOf(c2) returned nil")
	}
	if got2.State != "started" || got2.Video || got2.Secs != 0 || len(got2.People) != 1 || got2.People[0] != "Carol" {
		t.Errorf("got2 = %+v", got2)
	}
}

// TestCallLineMethods tests content serialization, minute calculation, and caller lookup.
func TestCallLineMethods(t *testing.T) {
	cl := CallLine{
		State:  "ended",
		Video:  true,
		Secs:   95,
		People: []string{"Alice", "Bob"},
	}

	// content()
	m := cl.content()
	wantContent := map[string]any{
		"state":  "ended",
		"video":  true,
		"secs":   95,
		"people": []string{"Alice", "Bob"},
	}
	if !reflect.DeepEqual(m, wantContent) {
		t.Errorf("cl.content() = %+v, want %+v", m, wantContent)
	}

	// By()
	if got := cl.By(); got != "Alice" {
		t.Errorf("cl.By() = %q, want 'Alice'", got)
	}
	clEmpty := CallLine{}
	if got := clEmpty.By(); got != "" {
		t.Errorf("clEmpty.By() = %q, want ''", got)
	}

	// Mins(): formula is max(1, (Secs+30)/60)
	minTests := []struct {
		secs int
		want int
	}{
		{secs: 0, want: 1},
		{secs: 29, want: 1},
		{secs: 30, want: 1},
		{secs: 59, want: 1},
		{secs: 60, want: 1},
		{secs: 89, want: 1},
		{secs: 90, want: 2},
		{secs: 149, want: 2},
		{secs: 150, want: 3},
		{secs: -20, want: 1},
	}
	for _, tt := range minTests {
		t.Run(string(rune(tt.secs)), func(t *testing.T) {
			line := CallLine{Secs: tt.secs}
			if got := line.Mins(); got != tt.want {
				t.Errorf("CallLine{Secs: %d}.Mins() = %d, want %d", tt.secs, got, tt.want)
			}
		})
	}
}
