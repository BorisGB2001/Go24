package company

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/worktime"
)

// TestSheet: the violations of a day by the schedule and the month laid out with days off, absences and pending changes.
func TestSheet(t *testing.T) {
	sched := worktime.Default()
	sched.LateAfter, sched.LeaveBefore, sched.MinDuration = "09:15", "17:30", "08:30"
	day := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local) // Tuesday
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	end := at(17, 0)
	ok := at(18, 30)
	for _, c := range []struct {
		d    Workday
		want []string
	}{
		{Workday{Day: day, Started: at(9, 10), Ended: &ok}, nil},
		{Workday{Day: day, Started: at(9, 20), Ended: &ok}, []string{"late"}},
		{Workday{Day: day, Started: at(9, 0), Ended: &end}, []string{"early", "short"}},
		{Workday{Day: day, Started: at(9, 0), Ended: &ok, PauseSecs: 3 * 3600}, []string{"short"}},
		{Workday{Day: day, Started: at(9, 30)}, []string{"late", "open"}},
	} {
		if got := Violations(c.d, sched, now); !slices.Equal(got, c.want) {
			t.Errorf("%v–%v: %v, want %v", c.d.Started, c.d.Ended, got, c.want)
		}
	}
	if got := Violations(Workday{Day: now, Started: now.Add(-3 * time.Hour)}, sched, now); len(got) != 0 { // today is going on
		t.Errorf("today: %v", got)
	}

	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	rows := BuildSheet([]Person{{ID: "a"}, {ID: "b"}},
		[]Workday{{UserID: "a", Day: day, Started: at(9, 0), Ended: &ok}, {UserID: "a", Day: day.AddDate(0, 0, 1), Started: at(9, 40).AddDate(0, 0, 1)}},
		[]Absence{{UserID: "b", Kind: "vacation", From: day, To: day.AddDate(0, 0, 2)}},
		map[string]bool{"a|2026-10-14": true}, nil, sched, month, now)
	if len(rows) != 2 || len(rows[0].Cells) != 31 {
		t.Fatalf("rows %d", len(rows))
	}
	a, b := rows[0], rows[1]
	c13, c14 := a.Cells[12], a.Cells[13]
	if c13.Kind != "work" || c13.Worked != 9*3600+30*60 || len(c13.Flags) != 0 {
		t.Errorf("13th: %+v", c13)
	}
	if c14.Kind != "work" || c14.Worked != 0 || !slices.Equal(c14.Flags, []string{"late", "open"}) || !c14.Pending {
		t.Errorf("14th, forgotten: %+v", c14)
	}
	if a.Days != 2 || a.Worked != 9*3600+30*60 || a.Violations != 1 {
		t.Errorf("totals %d %d %d", a.Days, a.Worked, a.Violations)
	}
	if a.Cells[10].Kind != "off" || a.Cells[14].Kind != "none" || a.Cells[25].Kind != "future" { // 11th Sunday, 15th, 26th
		t.Errorf("kinds %s %s %s", a.Cells[10].Kind, a.Cells[14].Kind, a.Cells[25].Kind)
	}
	if b.Cells[13].Kind != "absence" || b.Cells[13].Absence != "vacation" || b.Cells[15].Kind != "none" {
		t.Errorf("absence %+v", b.Cells[13])
	}
}

// TestDayChanges: an employee's change waits for the manager and is applied on approval, a declined one is not; a
// manager changes the day at once; the forgotten day; who may change whose day; the people under a manager.
func TestDayChanges(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	boss, anna, ivan, olga := user("boss", "Борис"), user("anna", "Анна"), user("ivan", "Иван"), user("olga", "Ольга")
	var top, sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Компания', $1) RETURNING id`, boss).Scan(&top))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Бухгалтерия', $1, $2) RETURNING id`, top, ivan).Scan(&sub))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $3)`, anna, ivan, sub)
	must(err)
	s := NewStore(pool)

	under, err := s.Under(ctx, boss)
	must(err)
	if len(under) != 2 || under[0].Name != "Анна" || under[1].Name != "Иван" {
		t.Fatalf("under the boss: %v", under)
	}
	if under, _ := s.Under(ctx, anna); len(under) != 0 {
		t.Fatalf("under anna: %v", under)
	}

	day := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, time.Local)
	at := func(h, m int) *time.Time {
		v := day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
		return &v
	}
	must(s.WorkAction(ctx, anna, "start", "", *at(9, 40)))
	if d, _ := s.ForgottenDay(ctx, anna, now); d == nil || d.Day.Format(time.DateOnly) != "2026-10-13" {
		t.Fatalf("forgotten day: %v", d)
	}
	var ve *i18n.Error
	// the reason is required, the end is after the start and not in the future, olga may not change anna's day
	if _, err := s.ChangeDay(ctx, anna, anna, day, DayChange{Ended: at(18, 0)}, false, false, now); !errors.As(err, &ve) || ve.Key != "err.workday_reason" {
		t.Fatalf("no reason: %v", err)
	}
	if _, err := s.ChangeDay(ctx, anna, anna, day, DayChange{Ended: at(9, 0), Reason: "x"}, false, false, now); !errors.As(err, &ve) || ve.Key != "err.workday_end" {
		t.Fatalf("end before start: %v", err)
	}
	if _, err := s.ChangeDay(ctx, olga, anna, day, DayChange{Ended: at(18, 0), Reason: "x"}, false, false, now); !errors.As(err, &ve) || ve.Key != "err.forbidden" {
		t.Fatalf("a stranger: %v", err)
	}
	// anna closes the forgotten day: the change waits, the managers (Иван, Борис above him) are told
	applied, err := s.ChangeDay(ctx, anna, anna, day, DayChange{Started: at(9, 0), Ended: at(18, 0), Reason: "забыла"}, false, false, now)
	must(err)
	if applied {
		t.Fatal("an employee's change is applied at once")
	}
	if d, _ := s.Day(ctx, anna, day); d.Ended != nil || !d.Started.Equal(*at(9, 40)) {
		t.Fatalf("a pending change touched the day: %+v", d)
	}
	if d, _ := s.ForgottenDay(ctx, anna, now); d != nil {
		t.Fatal("the forgotten day with a change waiting is asked again")
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'workday_change' AND user_id = $1 AND url LIKE '/worktime/d/%'`, ivan).Scan(&n))
	if n != 1 {
		t.Fatalf("manager notified %d", n)
	}
	pending, err := s.PendingChanges(ctx, []string{anna}, false)
	must(err)
	if len(pending) != 1 || pending[0].AuthorName != "Анна" || pending[0].OldStarted == nil {
		t.Fatalf("pending %+v", pending)
	}
	// a second change of the day replaces the waiting one
	_, err = s.ChangeDay(ctx, anna, anna, day, DayChange{Started: at(9, 5), Ended: at(18, 0), Reason: "точнее"}, false, false, now)
	must(err)
	pending, _ = s.PendingChanges(ctx, []string{anna}, false)
	if len(pending) != 1 || pending[0].Reason != "точнее" {
		t.Fatalf("replaced %+v", pending)
	}
	// anna may not approve her own change, olga neither; ivan approves
	if err := s.DecideChange(ctx, anna, pending[0].ID, true, false, now); !errors.As(err, &ve) || ve.Key != "err.forbidden" {
		t.Fatalf("own approval: %v", err)
	}
	if err := s.DecideChange(ctx, olga, pending[0].ID, true, false, now); !errors.As(err, &ve) || ve.Key != "err.forbidden" {
		t.Fatalf("stranger approval: %v", err)
	}
	must(s.DecideChange(ctx, ivan, pending[0].ID, true, false, now))
	d, _ := s.Day(ctx, anna, day)
	if d.Ended == nil || !d.Ended.Equal(*at(18, 0)) || !d.Started.Equal(*at(9, 5)) || d.Worked(now) != 8*3600+55*60 {
		t.Fatalf("approved day %+v", d)
	}
	if err := s.DecideChange(ctx, ivan, pending[0].ID, false, false, now); !errors.As(err, &ve) || ve.Key != "err.workday_change_gone" {
		t.Fatalf("decided twice: %v", err)
	}
	// a declined change is not applied
	_, err = s.ChangeDay(ctx, anna, anna, day, DayChange{PauseSecs: ptrInt(3600), Reason: "обед"}, false, false, now)
	must(err)
	pending, _ = s.PendingChanges(ctx, nil, true)
	must(s.DecideChange(ctx, boss, pending[0].ID, false, false, now))
	if d, _ := s.Day(ctx, anna, day); d.PauseSecs != 0 {
		t.Fatalf("declined applied: %+v", d)
	}
	// the manager adds a day anna did not start at once; she is told
	day2 := day.AddDate(0, 0, -1)
	s9, s18 := day2.Add(9*time.Hour), day2.Add(18*time.Hour)
	applied, err = s.ChangeDay(ctx, ivan, anna, day2, DayChange{Started: &s9, Ended: &s18, Reason: "работала из дома"}, false, false, now)
	must(err)
	if !applied {
		t.Fatal("a manager's change waits")
	}
	if d, _ := s.Day(ctx, anna, day2); d == nil || d.Worked(now) != 9*3600 {
		t.Fatalf("added day %+v", d)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'workday_decided' AND user_id = $1`, anna).Scan(&n))
	if n != 3 { // approved, declined, changed
		t.Fatalf("anna notified %d", n)
	}
	// with free editing an employee's change is applied at once; a new day needs its start and end
	if _, err := s.ChangeDay(ctx, olga, olga, day, DayChange{Ended: at(18, 0), Reason: "x"}, false, true, now); !errors.As(err, &ve) || ve.Key != "err.workday_new_day" {
		t.Fatalf("new day without start: %v", err)
	}
	applied, err = s.ChangeDay(ctx, olga, olga, day, DayChange{Started: at(10, 0), Ended: at(12, 0), Reason: "x"}, false, true, now)
	must(err)
	if !applied {
		t.Fatal("free edit waits")
	}
	changes, err := s.DayChanges(ctx, anna, day)
	must(err)
	if len(changes) != 2 || changes[0].Status != "declined" || changes[1].Status != "approved" || changes[1].DeciderName != "Иван" {
		t.Fatalf("history %+v", changes)
	}
}

func ptrInt(v int) *int { return &v }
