package calendar

import (
	"slices"
	"testing"
	"time"
)

func TestSlotsLinkHelpers(t *testing.T) {
	t.Run("Joint link", func(t *testing.T) {
		lPersonal := SlotLink{}
		if lPersonal.Joint() {
			t.Error("link with no members should not be Joint")
		}
		lJoint := SlotLink{Members: []string{"user-2"}}
		if !lJoint.Joint() {
			t.Error("link with members should be Joint")
		}
	})

	t.Run("slotToken generation", func(t *testing.T) {
		tok1 := slotToken()
		tok2 := slotToken()
		if len(tok1) != 32 || len(tok2) != 32 {
			t.Errorf("token length must be 32, got %d and %d", len(tok1), len(tok2))
		}
		if tok1 == tok2 {
			t.Error("tokens must be uniquely generated")
		}
	})

	t.Run("DefaultSlotRules from Settings", func(t *testing.T) {
		settings := Settings{
			WorkFrom: 10,
			WorkTo:   19,
			DaysOff:  []int{6, 7}, // Sat, Sun off
		}
		rules := DefaultSlotRules(settings)
		if len(rules) != 1 {
			t.Fatalf("expected 1 rule, got %d", len(rules))
		}
		r := rules[0]
		if r.From != 600 || r.To != 1140 {
			t.Errorf("expected From=600, To=1140; got From=%d, To=%d", r.From, r.To)
		}
		if !slices.Equal(r.Days, []int{1, 2, 3, 4, 5}) {
			t.Errorf("expected Days=[1 2 3 4 5], got %v", r.Days)
		}
	})

	t.Run("SlotDays calculation", func(t *testing.T) {
		now := time.Date(2026, 10, 28, 15, 30, 0, 0, time.Local)
		link := SlotLink{DaysAhead: 14}
		first, last := link.SlotDays(now)

		wantFirst := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local)
		wantLast := time.Date(2026, 11, 10, 0, 0, 0, 0, time.Local)
		if !first.Equal(wantFirst) {
			t.Errorf("first day = %v, want %v", first, wantFirst)
		}
		if !last.Equal(wantLast) {
			t.Errorf("last day = %v, want %v", last, wantLast)
		}
	})

	t.Run("Offers method", func(t *testing.T) {
		link := SlotLink{
			Rules: []SlotRule{
				{Days: []int{1, 3, 5}, From: 540, To: 1080}, // Mon, Wed, Fri
			},
			Holidays: []string{"2026-10-21"}, // Wednesday 21.10 is holiday
		}

		// Monday 19.10 -> true
		mon := time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local)
		if !link.Offers(mon) {
			t.Error("link should offer Monday")
		}

		// Tuesday 20.10 -> false (not in rules)
		tue := time.Date(2026, 10, 20, 10, 0, 0, 0, time.Local)
		if link.Offers(tue) {
			t.Error("link should not offer Tuesday")
		}

		// Wednesday 21.10 -> false (holiday)
		wed := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
		if link.Offers(wed) {
			t.Error("link should not offer holiday Wednesday")
		}
	})
}

func TestCleanRules(t *testing.T) {
	t.Run("valid rules sorted and deduplicated", func(t *testing.T) {
		input := []SlotRule{
			{Days: []int{5, 1, 3, 3}, From: 600, To: 1000},
		}
		cleaned, err := cleanRules(input, 60)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(cleaned) != 1 {
			t.Fatalf("expected 1 rule, got %d", len(cleaned))
		}
		if !slices.Equal(cleaned[0].Days, []int{1, 3, 5}) {
			t.Errorf("expected sorted days [1 3 5], got %v", cleaned[0].Days)
		}
	})

	t.Run("invalid day number", func(t *testing.T) {
		for _, badDay := range []int{0, 8, -1} {
			input := []SlotRule{
				{Days: []int{1, badDay}, From: 600, To: 1000},
			}
			if _, err := cleanRules(input, 60); err == nil {
				t.Errorf("cleanRules must fail for day %d", badDay)
			}
		}
	})

	t.Run("empty rules or rules with no days", func(t *testing.T) {
		if _, err := cleanRules(nil, 60); err == nil {
			t.Error("cleanRules(nil) must fail")
		}
		input := []SlotRule{
			{Days: []int{}, From: 600, To: 1000},
		}
		if _, err := cleanRules(input, 60); err == nil {
			t.Error("cleanRules with only empty days must fail")
		}
	})

	t.Run("invalid hours range", func(t *testing.T) {
		cases := []struct {
			name   string
			from   int
			to     int
			length int
		}{
			{"negative from", -1, 600, 30},
			{"to over 24h", 600, 24*60 + 1, 30},
			{"meeting length does not fit in span", 600, 620, 30},
			{"from equals to", 600, 600, 30},
			{"from greater than to", 700, 600, 30},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				input := []SlotRule{{Days: []int{1}, From: tc.from, To: tc.to}}
				if _, err := cleanRules(input, tc.length); err == nil {
					t.Errorf("cleanRules should fail for case %s (from=%d, to=%d, len=%d)", tc.name, tc.from, tc.to, tc.length)
				}
			})
		}
	})
}
