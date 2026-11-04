package marketing

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestValidColor(t *testing.T) {
	tests := []struct {
		color string
		want  bool
	}{
		{"#ffffff", true},
		{"#FFFFFF", true},
		{"#000000", true},
		{"#1a2b3c", true},
		{"#4D5E6F", true},
		{"", false},
		{"#fff", false},
		{"#1234567", false},
		{"123456", false},
		{"#12345", false},
		{"#gggggg", false},
		{"#12 456", false},
		{"#+12345", false},
		{"#-12345", false},
		{"red", false},
	}
	for _, tc := range tests {
		if got := validColor(tc.color); got != tc.want {
			t.Errorf("validColor(%q) = %v, want %v", tc.color, got, tc.want)
		}
	}
}

func TestExpenseClean(t *testing.T) {
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

	t.Run("valid expense is trimmed and rounded", func(t *testing.T) {
		e := Expense{
			SourceID: "src-1",
			Day:      day,
			Amount:   123.456,
			Campaign: "  autumn_promo  ",
			Comment:  "  spent on ads  ",
		}
		if err := e.clean(); err != nil {
			t.Fatalf("clean() error: %v", err)
		}
		if e.Campaign != "autumn_promo" {
			t.Errorf("Campaign = %q, want %q", e.Campaign, "autumn_promo")
		}
		if e.Comment != "spent on ads" {
			t.Errorf("Comment = %q, want %q", e.Comment, "spent on ads")
		}
		if e.Amount != 123.46 {
			t.Errorf("Amount = %v, want 123.46", e.Amount)
		}
	})

	t.Run("missing source id", func(t *testing.T) {
		e := Expense{Day: day, Amount: 100}
		if err := e.clean(); err == nil {
			t.Fatal("expected error for empty SourceID")
		}
	})

	t.Run("zero day", func(t *testing.T) {
		e := Expense{SourceID: "s1", Day: time.Time{}, Amount: 100}
		if err := e.clean(); err == nil {
			t.Fatal("expected error for zero Day")
		}
	})

	t.Run("invalid amount values", func(t *testing.T) {
		for _, amount := range []float64{-0.01, -100, 1e15, 1e16, math.NaN(), math.Inf(1), math.Inf(-1)} {
			e := Expense{SourceID: "s1", Day: day, Amount: amount}
			if err := e.clean(); err == nil {
				t.Errorf("expected error for Amount = %v", amount)
			}
		}
	})

	t.Run("campaign length boundary", func(t *testing.T) {
		// Exactly 200 runes is valid
		eValid := Expense{SourceID: "s1", Day: day, Amount: 10, Campaign: strings.Repeat("я", 200)}
		if err := eValid.clean(); err != nil {
			t.Errorf("200 runes campaign should be valid: %v", err)
		}

		// 201 runes is invalid
		eInvalid := Expense{SourceID: "s1", Day: day, Amount: 10, Campaign: strings.Repeat("я", 201)}
		if err := eInvalid.clean(); err == nil {
			t.Error("201 runes campaign should be invalid")
		}
	})

	t.Run("comment length boundary", func(t *testing.T) {
		// Exactly 1000 runes is valid
		eValid := Expense{SourceID: "s1", Day: day, Amount: 10, Comment: strings.Repeat("c", 1000)}
		if err := eValid.clean(); err != nil {
			t.Errorf("1000 runes comment should be valid: %v", err)
		}

		// 1001 runes is invalid
		eInvalid := Expense{SourceID: "s1", Day: day, Amount: 10, Comment: strings.Repeat("c", 1001)}
		if err := eInvalid.clean(); err == nil {
			t.Error("1001 runes comment should be invalid")
		}
	})
}

func TestParseDayUnit(t *testing.T) {
	t.Run("ISO 8601 format", func(t *testing.T) {
		d, err := parseDay(" 2026-10-28 ")
		if err != nil {
			t.Fatalf("parseDay: %v", err)
		}
		if d.Format(time.DateOnly) != "2026-10-28" {
			t.Errorf("got %v, want 2026-10-28", d)
		}
	})

	t.Run("RU format", func(t *testing.T) {
		d, err := parseDay(" 28.10.2026 ")
		if err != nil {
			t.Fatalf("parseDay: %v", err)
		}
		if d.Format(time.DateOnly) != "2026-10-28" {
			t.Errorf("got %v, want 2026-10-28", d)
		}
	})

	t.Run("invalid formats", func(t *testing.T) {
		for _, raw := range []string{"", "invalid", "2026/10/28", "28-10-2026", "32.10.2026", "2026-13-01"} {
			if _, err := parseDay(raw); err == nil {
				t.Errorf("expected error for %q", raw)
			}
		}
	})
}

func TestDateArg(t *testing.T) {
	if got := dateArg(time.Time{}); got != nil {
		t.Errorf("dateArg(zero) = %v, want nil", got)
	}

	d := time.Date(2026, 5, 20, 15, 30, 0, 0, time.UTC)
	if got := dateArg(d); got != "2026-05-20" {
		t.Errorf("dateArg(%v) = %v, want %q", d, got, "2026-05-20")
	}
}

func TestInBase(t *testing.T) {
	got := inBase("d.amount", "d.currency")
	want := `(d.amount * coalesce((SELECT cr.rate FROM crm_currencies cr WHERE cr.code = d.currency), 1))`
	if got != want {
		t.Errorf("inBase = %q, want %q", got, want)
	}
}

func TestRound2(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{12.3456, 12.35},
		{12.3444, 12.34},
		{0, 0},
		{-12.3456, -12.35},
		{-12.3444, -12.34},
		{10.5, 10.5},
	}
	for _, tc := range tests {
		if got := round2(tc.in); got != tc.want {
			t.Errorf("round2(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestChannelAdd(t *testing.T) {
	c1 := Channel{Expenses: 100, Leads: 5, Deals: 3, Won: 2, Revenue: 500}
	c2 := Channel{Expenses: 50, Leads: 2, Deals: 1, Won: 1, Revenue: 200}
	c1.add(c2)

	if c1.Expenses != 150 || c1.Leads != 7 || c1.Deals != 4 || c1.Won != 3 || c1.Revenue != 700 {
		t.Errorf("channel after add: %+v", c1)
	}
}

func TestChannelCampaign(t *testing.T) {
	c := &Channel{Name: "source-1"}
	if len(c.Campaigns) != 0 {
		t.Fatalf("initially campaigns should be empty, got %d", len(c.Campaigns))
	}

	sub1 := c.campaign("summer")
	if sub1 == nil || sub1.Name != "summer" {
		t.Fatalf("campaign(\"summer\") = %+v", sub1)
	}
	if len(c.Campaigns) != 1 {
		t.Fatalf("campaigns count = %d, want 1", len(c.Campaigns))
	}

	sub1.Expenses = 500
	sub1.Leads = 10

	// Looking up existing campaign returns the same pointer
	subAgain := c.campaign("summer")
	if subAgain != sub1 {
		t.Fatalf("expected same pointer, got %p vs %p", subAgain, sub1)
	}
	if subAgain.Expenses != 500 || subAgain.Leads != 10 {
		t.Errorf("campaign modified: %+v", subAgain)
	}

	// Adding another campaign
	sub2 := c.campaign("winter")
	if len(c.Campaigns) != 2 {
		t.Fatalf("campaigns count = %d, want 2", len(c.Campaigns))
	}
	if sub2.Name != "winter" {
		t.Errorf("sub2.Name = %q, want \"winter\"", sub2.Name)
	}
}

func TestChannelROIAndCostsEdgeCases(t *testing.T) {
	t.Run("negative ROI", func(t *testing.T) {
		c := Channel{Expenses: 200, Revenue: 50}
		pct, ok := c.ROI()
		if !ok || pct != -75 {
			t.Errorf("ROI() = (%d, %v), want (-75, true)", pct, ok)
		}
	})

	t.Run("total loss ROI", func(t *testing.T) {
		c := Channel{Expenses: 100, Revenue: 0}
		pct, ok := c.ROI()
		if !ok || pct != -100 {
			t.Errorf("ROI() = (%d, %v), want (-100, true)", pct, ok)
		}
	})

	t.Run("breakeven ROI", func(t *testing.T) {
		c := Channel{Expenses: 100, Revenue: 100}
		pct, ok := c.ROI()
		if !ok || pct != 0 {
			t.Errorf("ROI() = (%d, %v), want (0, true)", pct, ok)
		}
	})

	t.Run("negative or zero expenses ROI", func(t *testing.T) {
		for _, exp := range []float64{0, -10} {
			c := Channel{Expenses: exp, Revenue: 100}
			if _, ok := c.ROI(); ok {
				t.Errorf("ROI() should be ok=false for Expenses = %v", exp)
			}
		}
	})

	t.Run("costs rounding and zero counts", func(t *testing.T) {
		c := Channel{Expenses: 100, Leads: 3, Won: 6}
		if got := c.CostPerLead(); got != 33.33 {
			t.Errorf("CostPerLead = %v, want 33.33", got)
		}
		if got := c.CostPerSale(); got != 16.67 {
			t.Errorf("CostPerSale = %v, want 16.67", got)
		}

		cZero := Channel{Expenses: 100, Leads: 0, Won: 0}
		if cZero.CostPerLead() != 0 || cZero.CostPerSale() != 0 {
			t.Errorf("expected 0 cost when count is 0: lead=%v, sale=%v", cZero.CostPerLead(), cZero.CostPerSale())
		}
	})
}

func TestMatcherEdgeCases(t *testing.T) {
	mEmpty := newMatcher(nil)
	if got := mEmpty.of("utm", "origin"); got != -1 {
		t.Errorf("mEmpty.of = %d, want -1", got)
	}

	srcs := []AdSource{
		{ID: "s1", UTM: []string{"yandex", "direct"}, CRMSource: "crm-1"},
		{ID: "s2", UTM: []string{"vk"}, CRMSource: "crm-2"},
	}
	m := newMatcher(srcs)

	// UTM takes priority over origin
	if got := m.of("vk", "crm-1"); got != 1 {
		t.Errorf("m.of(\"vk\", \"crm-1\") = %d, want 1 (matched by UTM)", got)
	}

	// Case insensitivity and whitespace in UTM
	if got := m.of("  DIRECT  ", ""); got != 0 {
		t.Errorf("m.of(\"  DIRECT  \", \"\") = %d, want 0", got)
	}

	// Unknown UTM falls back to CRM origin
	if got := m.of("unknown", "crm-2"); got != 1 {
		t.Errorf("m.of(\"unknown\", \"crm-2\") = %d, want 1", got)
	}

	// Neither matches
	if got := m.of("unknown", "crm-unknown"); got != -1 {
		t.Errorf("m.of(\"unknown\", \"crm-unknown\") = %d, want -1", got)
	}
}

func TestSplitUTMEdgeCases(t *testing.T) {
	longItem := strings.Repeat("a", 201)
	validLongItem := strings.Repeat("b", 200)

	input := "vk, facebook; google\n" + longItem + "\t" + validLongItem + "  vk  "
	got := SplitUTM(input)

	// longItem (> 200 runes) should be excluded, validLongItem kept, vk deduplicated
	want := []string{"vk", "facebook", "google", validLongItem}
	if len(got) != len(want) {
		t.Fatalf("SplitUTM returned %d items, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestUTMCleanAndValues(t *testing.T) {
	u := UTM{
		Source:   "  google  ",
		Medium:   " cpc ",
		Campaign: " " + strings.Repeat("я", 250) + " ",
		Content:  " banner ",
		Term:     " test ",
	}
	cleaned := u.Clean()

	if cleaned.Source != "google" || cleaned.Medium != "cpc" || cleaned.Content != "banner" || cleaned.Term != "test" {
		t.Errorf("unexpected cleaned UTM: %+v", cleaned)
	}
	if len([]rune(cleaned.Campaign)) != 200 {
		t.Errorf("Campaign runes count = %d, want 200", len([]rune(cleaned.Campaign)))
	}

	vals := cleaned.Values()
	if vals.Get("utm_source") != "google" || vals.Get("utm_medium") != "cpc" {
		t.Errorf("Values() = %+v", vals)
	}

	// Empty UTM has empty Values
	emptyVals := (UTM{}).Values()
	if len(emptyVals) != 0 {
		t.Errorf("empty UTM Values = %+v, want empty", emptyVals)
	}
}
