package company

import (
	"testing"
	"time"

	"go24/internal/i18n"
	"go24/internal/worktime"
)

// TestReportRuleClean: clean validates periods, weekdays, monthdays and clears unused fields.
func TestReportRuleClean(t *testing.T) {
	cases := []struct {
		name         string
		rule         ReportRule
		wantErr      error
		wantWeekday  int
		wantMonthday int
	}{
		{
			name:    "invalid period",
			rule:    ReportRule{Period: "yearly"},
			wantErr: i18n.E("err.bad_form"),
		},
		{
			name:    "weekday negative",
			rule:    ReportRule{Period: "week", Weekday: -1},
			wantErr: i18n.E("err.bad_form"),
		},
		{
			name:    "weekday too large",
			rule:    ReportRule{Period: "week", Weekday: 7},
			wantErr: i18n.E("err.bad_form"),
		},
		{
			name:    "monthday negative",
			rule:    ReportRule{Period: "month", Monthday: -1},
			wantErr: i18n.E("err.bad_form"),
		},
		{
			name:    "monthday too large",
			rule:    ReportRule{Period: "month", Monthday: 32},
			wantErr: i18n.E("err.bad_form"),
		},
		{
			name:         "valid week resets monthday",
			rule:         ReportRule{Period: "week", Weekday: 5, Monthday: 15},
			wantWeekday:  5,
			wantMonthday: 0,
		},
		{
			name:         "valid month resets weekday",
			rule:         ReportRule{Period: "month", Weekday: 3, Monthday: 25},
			wantWeekday:  0,
			wantMonthday: 25,
		},
		{
			name:         "month with zero monthday defaults to 31",
			rule:         ReportRule{Period: "month", Monthday: 0},
			wantWeekday:  0,
			wantMonthday: 31,
		},
		{
			name:         "daily resets weekday and monthday",
			rule:         ReportRule{Period: "day", Weekday: 2, Monthday: 10},
			wantWeekday:  0,
			wantMonthday: 0,
		},
		{
			name:         "at will (empty period) resets weekday and monthday",
			rule:         ReportRule{Period: "", Weekday: 4, Monthday: 12},
			wantWeekday:  0,
			wantMonthday: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.rule
			err := r.clean()
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErr.Error() {
					t.Fatalf("clean() err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("clean() unexpected err = %v", err)
			}
			if r.Weekday != tc.wantWeekday || r.Monthday != tc.wantMonthday {
				t.Errorf("cleaned rule weekday=%d monthday=%d, want weekday=%d monthday=%d",
					r.Weekday, r.Monthday, tc.wantWeekday, tc.wantMonthday)
			}
		})
	}
}

// TestReportRuleIsDayAndBefore: IsDay and before test whether dates match reporting schedules.
func TestReportRuleIsDayAndBefore(t *testing.T) {
	sched := worktime.Default() // default weekend is Sat(6), Sun(0)

	// Daily rule
	rDay := ReportRule{Period: "day"}
	fri := time.Date(2026, 10, 23, 0, 0, 0, 0, time.Local) // Friday
	sat := time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local) // Saturday
	mon := time.Date(2026, 10, 26, 0, 0, 0, 0, time.Local) // Monday

	if !rDay.IsDay(fri, sched) {
		t.Errorf("Friday should be a report day for daily rule")
	}
	if rDay.IsDay(sat, sched) {
		t.Errorf("Saturday should not be a report day for daily rule")
	}

	// before on Monday should find Friday
	prev, ok := rDay.before(mon, sched)
	if !ok || prev != fri {
		t.Errorf("daily before(mon) = %v, %v, want %v, true", prev, ok, fri)
	}

	// Weekly rule (Friday)
	rWeek := ReportRule{Period: "week", Weekday: 5} // Friday
	if !rWeek.IsDay(fri, sched) {
		t.Errorf("Friday should be report day for Friday-weekly rule")
	}
	if rWeek.IsDay(mon, sched) {
		t.Errorf("Monday should not be report day for Friday-weekly rule")
	}
	prevWeek, ok := rWeek.before(mon, sched)
	if !ok || prevWeek != fri {
		t.Errorf("weekly before(mon) = %v, %v, want %v, true", prevWeek, ok, fri)
	}

	// Monthly rule (31st of the month; shorter months cut at last day)
	rMonth := ReportRule{Period: "month", Monthday: 31}
	oct31 := time.Date(2026, 10, 31, 0, 0, 0, 0, time.Local) // 31 days
	nov30 := time.Date(2026, 11, 30, 0, 0, 0, 0, time.Local) // 30 days
	nov29 := time.Date(2026, 11, 29, 0, 0, 0, 0, time.Local)
	feb28 := time.Date(2026, 2, 28, 0, 0, 0, 0, time.Local) // 28 days

	if !rMonth.IsDay(oct31, sched) {
		t.Errorf("Oct 31 should be report day for 31st monthly rule")
	}
	if !rMonth.IsDay(nov30, sched) {
		t.Errorf("Nov 30 should be report day for 31st monthly rule in 30-day month")
	}
	if rMonth.IsDay(nov29, sched) {
		t.Errorf("Nov 29 should not be report day for 31st monthly rule")
	}
	if !rMonth.IsDay(feb28, sched) {
		t.Errorf("Feb 28 should be report day for 31st monthly rule in Feb")
	}

	// Empty period rule
	rEmpty := ReportRule{}
	if rEmpty.IsDay(fri, sched) {
		t.Errorf("empty period should never be a report day")
	}
	if _, ok := rEmpty.before(fri, sched); ok {
		t.Errorf("empty period before() should return false")
	}
}

// TestReportPeriodEmptyAndNextPeriod: Empty and NextPeriod computation for various rules.
func TestReportPeriodEmptyAndNextPeriod(t *testing.T) {
	d := func(m, day int) time.Time { return time.Date(2026, time.Month(m), day, 0, 0, 0, 0, time.Local) }
	sched := worktime.Default()

	// Empty method
	pEmpty := ReportPeriod{From: d(10, 5), To: d(10, 4)}
	if !pEmpty.Empty() {
		t.Errorf("From > To should be Empty()")
	}
	pNonEmpty := ReportPeriod{From: d(10, 4), To: d(10, 5)}
	if pNonEmpty.Empty() {
		t.Errorf("From <= To should not be Empty()")
	}

	// NextPeriod for daily report when last is already sent today
	rDaily := ReportRule{Period: "day"}
	today := d(10, 23) // Friday
	lastSent := today
	npDailySent := NextPeriod(rDaily, sched, &lastSent, today)
	if npDailySent.Due {
		t.Errorf("daily report already sent today should not be Due")
	}
	if !npDailySent.Empty() {
		t.Errorf("daily report already sent today should be Empty, got %+v", npDailySent)
	}

	// NextPeriod for at-will reports (covers up to 30 days if no last)
	rAtWill := ReportRule{}
	npAtWill := NextPeriod(rAtWill, sched, nil, today)
	if npAtWill.Due {
		t.Errorf("at-will report should not be Due")
	}
	if npAtWill.To != today || npAtWill.From != today {
		t.Errorf("at-will without last report: From=%v To=%v, want today", npAtWill.From, npAtWill.To)
	}

	// maxTime helper
	t1 := d(10, 1)
	t2 := d(10, 10)
	if got := maxTime(t1, t2); got != t2 {
		t.Errorf("maxTime(%v, %v) = %v, want %v", t1, t2, got, t2)
	}
	if got := maxTime(t2, t1); got != t2 {
		t.Errorf("maxTime(%v, %v) = %v, want %v", t2, t1, got, t2)
	}
}
