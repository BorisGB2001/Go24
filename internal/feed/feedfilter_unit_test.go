package feed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPeriodDetails(t *testing.T) {
	msk, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		msk = time.FixedZone("MSK", 3*3600)
	}

	day := func(loc *time.Location, y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	}

	// 2026-10-29 is a Thursday
	nowUTC := time.Date(2026, 10, 29, 15, 30, 0, 0, time.UTC)
	nowMSK := time.Date(2026, 10, 29, 18, 30, 0, 0, msk)

	for _, c := range []struct {
		name       string
		periodName string
		from, to   string
		now        time.Time
		wantA      time.Time
		wantB      time.Time
	}{
		{
			name:       "today UTC",
			periodName: "today",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 10, 29),
			wantB:      day(time.UTC, 2026, 10, 30),
		},
		{
			name:       "yesterday UTC",
			periodName: "yesterday",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 10, 28),
			wantB:      day(time.UTC, 2026, 10, 29),
		},
		{
			name:       "week on Thursday",
			periodName: "week",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 10, 26), // Monday
			wantB:      day(time.UTC, 2026, 11, 2),  // Next Monday
		},
		{
			name:       "week on Monday",
			periodName: "week",
			now:        time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 10, 26),
			wantB:      day(time.UTC, 2026, 11, 2),
		},
		{
			name:       "week on Sunday",
			periodName: "week",
			now:        time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 10, 26),
			wantB:      day(time.UTC, 2026, 11, 2),
		},
		{
			name:       "week on Saturday",
			periodName: "week",
			now:        time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 10, 26),
			wantB:      day(time.UTC, 2026, 11, 2),
		},
		{
			name:       "month October",
			periodName: "month",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 10, 1),
			wantB:      day(time.UTC, 2026, 11, 1),
		},
		{
			name:       "month December cross year",
			periodName: "month",
			now:        time.Date(2026, 12, 15, 10, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 12, 1),
			wantB:      day(time.UTC, 2027, 1, 1),
		},
		{
			name:       "month February leap year 2024",
			periodName: "month",
			now:        time.Date(2024, 2, 15, 10, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2024, 2, 1),
			wantB:      day(time.UTC, 2024, 3, 1),
		},
		{
			name:       "quarter Q1 January",
			periodName: "quarter",
			now:        time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 1, 1),
			wantB:      day(time.UTC, 2026, 4, 1),
		},
		{
			name:       "quarter Q1 March",
			periodName: "quarter",
			now:        time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 1, 1),
			wantB:      day(time.UTC, 2026, 4, 1),
		},
		{
			name:       "quarter Q2 May",
			periodName: "quarter",
			now:        time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 4, 1),
			wantB:      day(time.UTC, 2026, 7, 1),
		},
		{
			name:       "quarter Q3 August",
			periodName: "quarter",
			now:        time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 7, 1),
			wantB:      day(time.UTC, 2026, 10, 1),
		},
		{
			name:       "quarter Q4 October",
			periodName: "quarter",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 10, 1),
			wantB:      day(time.UTC, 2027, 1, 1),
		},
		{
			name:       "quarter Q4 December",
			periodName: "quarter",
			now:        time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			wantA:      day(time.UTC, 2026, 10, 1),
			wantB:      day(time.UTC, 2027, 1, 1),
		},
		{
			name:       "range valid dates",
			periodName: "range",
			from:       "2026-05-01",
			to:         "2026-05-10",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 5, 1),
			wantB:      day(time.UTC, 2026, 5, 11),
		},
		{
			name:       "empty name valid dates",
			periodName: "",
			from:       "2026-06-01",
			to:         "2026-06-05",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 6, 1),
			wantB:      day(time.UTC, 2026, 6, 6),
		},
		{
			name:       "range only from",
			periodName: "range",
			from:       "2026-05-01",
			to:         "",
			now:        nowUTC,
			wantA:      day(time.UTC, 2026, 5, 1),
			wantB:      time.Time{},
		},
		{
			name:       "range only to",
			periodName: "range",
			from:       "",
			to:         "2026-05-10",
			now:        nowUTC,
			wantA:      time.Time{},
			wantB:      day(time.UTC, 2026, 5, 11),
		},
		{
			name:       "range invalid dates",
			periodName: "range",
			from:       "bad-date",
			to:         "invalid",
			now:        nowUTC,
			wantA:      time.Time{},
			wantB:      time.Time{},
		},
		{
			name:       "unknown period name",
			periodName: "year",
			now:        nowUTC,
			wantA:      time.Time{},
			wantB:      time.Time{},
		},
		{
			name:       "today MSK timezone",
			periodName: "today",
			now:        nowMSK,
			wantA:      day(msk, 2026, 10, 29),
			wantB:      day(msk, 2026, 10, 30),
		},
	} {
		gotA, gotB := Period(c.periodName, c.from, c.to, c.now)
		if !gotA.Equal(c.wantA) || !gotB.Equal(c.wantB) {
			t.Errorf("%s: Period(%q, %q, %q) = %v – %v, want %v – %v",
				c.name, c.periodName, c.from, c.to, gotA, gotB, c.wantA, c.wantB)
		}
		if !gotA.IsZero() && gotA.Location() != c.now.Location() {
			t.Errorf("%s: Period start location = %v, want %v", c.name, gotA.Location(), c.now.Location())
		}
	}
}

func TestSavedFilterJSON(t *testing.T) {
	orig := SavedFilter{Name: "Active Filters", Query: "view=mine&period=week"}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal(SavedFilter) err = %v", err)
	}

	var parsed SavedFilter
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("Unmarshal(SavedFilter) err = %v", err)
	}
	if parsed != orig {
		t.Errorf("roundtrip SavedFilter = %+v, want %+v", parsed, orig)
	}
}

func TestSaveFilterValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	for _, c := range []struct {
		name string
		f    SavedFilter
	}{
		{"empty name", SavedFilter{Name: ""}},
		{"whitespace name", SavedFilter{Name: "   \t\n  "}},
		{"name too long ASCII", SavedFilter{Name: strings.Repeat("a", 101)}},
		{"name too long Unicode", SavedFilter{Name: strings.Repeat("ф", 101)}},
	} {
		err := s.SaveFilter(ctx, "user1", c.f)
		if !errors.Is(err, ErrFilterName) {
			t.Errorf("%s: SaveFilter() err = %v, want ErrFilterName", c.name, err)
		}
	}
}

func TestDeptNamesEmpty(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	for _, refs := range [][]string{
		nil,
		{},
		{"user1", "user2"},
		{"deptWithoutColon"},
	} {
		out, err := s.DeptNames(ctx, refs)
		if err != nil {
			t.Errorf("DeptNames(%v) err = %v, want nil", refs, err)
		}
		if len(out) != 0 {
			t.Errorf("DeptNames(%v) = %v, want empty map", refs, out)
		}
	}
}
