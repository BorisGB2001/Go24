package feed

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestGratitudeEmojis(t *testing.T) {
	for _, b := range Badges {
		emoji, ok := BadgeEmoji[b]
		if !ok || emoji == "" {
			t.Errorf("badge %q missing in BadgeEmoji", b)
		}
	}

	if got := (Gratitude{Badge: "cup"}).Emoji(); got != "🏆" {
		t.Errorf("Gratitude.Emoji(cup) = %q, want 🏆", got)
	}
	if got := (Gratitude{Badge: "unknown"}).Emoji(); got != "" {
		t.Errorf("Gratitude.Emoji(unknown) = %q, want empty", got)
	}

	if got := (Thanks{Badge: "heart"}).Emoji(); got != "❤️" {
		t.Errorf("Thanks.Emoji(heart) = %q, want ❤️", got)
	}
	if got := (BoardEntry{Badge: "star"}).Emoji(); got != "⭐" {
		t.Errorf("BoardEntry.Emoji(star) = %q, want ⭐", got)
	}
	if got := (BadgeCount{Badge: "rocket"}).Emoji(); got != "🚀" {
		t.Errorf("BadgeCount.Emoji(rocket) = %q, want 🚀", got)
	}
}

func TestCountOf(t *testing.T) {
	for _, c := range []struct {
		list []string
		v    string
		want int
	}{
		{nil, "a", 0},
		{[]string{}, "a", 0},
		{[]string{"b", "c"}, "a", 0},
		{[]string{"a", "b", "c"}, "a", 1},
		{[]string{"a", "b", "a", "a"}, "a", 3},
		{[]string{"cup", "star", "cup"}, "cup", 2},
	} {
		if got := countOf(c.list, c.v); got != c.want {
			t.Errorf("countOf(%v, %q) = %d, want %d", c.list, c.v, got, c.want)
		}
	}
}

func TestLeaderPhotoURL(t *testing.T) {
	lEmpty := Leader{Person: Person{ID: "u1"}, PhotoKey: ""}
	if got := lEmpty.PhotoURL(); got != "" {
		t.Errorf("Leader with empty photo key has PhotoURL = %q, want empty", got)
	}

	lPhoto := Leader{Person: Person{ID: "u1"}, PhotoKey: "abc.jpg"}
	if got := lPhoto.PhotoURL(); got == "" {
		t.Errorf("Leader with photo key has empty PhotoURL, want non-empty")
	}
}

func TestSetGratitudeValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	err := s.SetGratitude(ctx, "author", "post", "invalid_badge", []string{"u1"})
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.gratitude" {
		t.Errorf("SetGratitude(invalid_badge) err = %v, want err.gratitude", err)
	}

	err = s.SetGratitude(ctx, "author", "post", "cup", nil)
	if !errors.As(err, &ie) || ie.Key != "err.gratitude" {
		t.Errorf("SetGratitude(nil people) err = %v, want err.gratitude", err)
	}

	err = s.SetGratitude(ctx, "author", "post", "cup", []string{})
	if !errors.As(err, &ie) || ie.Key != "err.gratitude" {
		t.Errorf("SetGratitude(empty people) err = %v, want err.gratitude", err)
	}
}

func TestBoardFilterWhere(t *testing.T) {
	// Base empty filter
	{
		args := []any{"viewer1", false}
		where := BoardFilter{}.where("gu.user_id", &args)
		if where != visible {
			t.Errorf("empty filter where = %q, want visible", where)
		}
		if len(args) != 2 {
			t.Errorf("empty filter args len = %d, want 2", len(args))
		}
	}

	// Valid badge
	{
		args := []any{"viewer1", false}
		where := BoardFilter{Badge: "cup"}.where("gu.user_id", &args)
		if !strings.Contains(where, "g.badge = $3") {
			t.Errorf("where with badge missing 'g.badge = $3': %q", where)
		}
		if len(args) != 3 || args[2] != "cup" {
			t.Errorf("args with badge = %v, want [viewer1, false, cup]", args)
		}
	}

	// Invalid badge (must be ignored)
	{
		args := []any{"viewer1", false}
		where := BoardFilter{Badge: "invalid"}.where("gu.user_id", &args)
		if strings.Contains(where, "g.badge") {
			t.Errorf("where with invalid badge should not contain g.badge: %q", where)
		}
		if len(args) != 2 {
			t.Errorf("args with invalid badge len = %d, want 2", len(args))
		}
	}

	// From and To dates
	{
		args := []any{"viewer1", false}
		from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
		where := BoardFilter{From: from, To: to}.where("gu.user_id", &args)
		if !strings.Contains(where, "p.created_at >= $3::date") {
			t.Errorf("where missing from date clause: %q", where)
		}
		if !strings.Contains(where, "p.created_at < $4::date + 1") {
			t.Errorf("where missing to date clause: %q", where)
		}
		if len(args) != 4 || args[2] != "2026-10-01" || args[3] != "2026-10-15" {
			t.Errorf("args with dates = %v", args)
		}
	}

	// UserID and DepartmentID combined
	{
		args := []any{"viewer1", false}
		where := BoardFilter{
			Badge:        "star",
			UserID:       "u-123",
			DepartmentID: "d-456",
		}.where("gu.user_id", &args)

		if !strings.Contains(where, "g.badge = $3") {
			t.Errorf("where missing badge: %q", where)
		}
		if !strings.Contains(where, "gu.user_id::text = $4") {
			t.Errorf("where missing user_id: %q", where)
		}
		if !strings.Contains(where, "id::text = $5") {
			t.Errorf("where missing department_id $5: %q", where)
		}
		if len(args) != 5 || args[2] != "star" || args[3] != "u-123" || args[4] != "d-456" {
			t.Errorf("args with user and dept = %v", args)
		}
	}
}
