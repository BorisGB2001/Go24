package drive

import (
	"context"
	"errors"
	"testing"
)

func TestBit(t *testing.T) {
	if got := bit(true); got != 1 {
		t.Errorf("bit(true) = %d, want 1", got)
	}
	if got := bit(false); got != 0 {
		t.Errorf("bit(false) = %d, want 0", got)
	}
}

func TestLimitsOf(t *testing.T) {
	lim := Limits{
		User:     1000,
		Company:  2000,
		Project:  3000,
		Versions: 5,
	}

	for _, c := range []struct {
		name string
		d    Drive
		want int64
	}{
		{"company drive limit", Drive{Kind: "company"}, 2000},
		{"project drive limit", Drive{Kind: "project"}, 3000},
		{"user drive limit", Drive{Kind: "user"}, 1000},
		{"shared folder uses owner's user drive limit", Drive{Kind: "user", Root: "f1"}, 1000},
		{"unspecified kind defaults to user limit", Drive{}, 1000},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := lim.Of(c.d); got != c.want {
				t.Errorf("Limits.Of(%+v) = %d, want %d", c.d, got, c.want)
			}
		})
	}
}

func TestUsagePercent(t *testing.T) {
	for _, c := range []struct {
		name  string
		usage Usage
		want  int
	}{
		{"zero limit means no limit (0%)", Usage{Used: 500, Limit: 0}, 0},
		{"negative limit returns 0%", Usage{Used: 500, Limit: -100}, 0},
		{"zero used of positive limit", Usage{Used: 0, Limit: 1000}, 0},
		{"quarter used", Usage{Used: 250, Limit: 1000}, 25},
		{"half used", Usage{Used: 500, Limit: 1000}, 50},
		{"fully used", Usage{Used: 1000, Limit: 1000}, 100},
		{"over quota capped at 100%", Usage{Used: 1500, Limit: 1000}, 100},
		{"integer truncation (33% for 1/3)", Usage{Used: 100, Limit: 300}, 33},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.usage.Percent(); got != c.want {
				t.Errorf("Usage(%+v).Percent() = %d, want %d", c.usage, got, c.want)
			}
		})
	}
}

func TestSaveLimitsValidation(t *testing.T) {
	// Negative values for user, company, project, or versions quota must be rejected.
	s := NewStore(nil, nil)
	ctx := context.Background()

	for _, c := range []struct {
		name   string
		limits Limits
	}{
		{"negative user bytes", Limits{User: -1}},
		{"negative company bytes", Limits{Company: -100}},
		{"negative project bytes", Limits{Project: -50}},
		{"negative versions count", Limits{Versions: -2}},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := s.SaveLimits(ctx, c.limits)
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("SaveLimits(%+v) error = %v, want ErrNotFound", c.limits, err)
			}
		})
	}
}
