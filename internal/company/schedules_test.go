package company

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/worktime"
)

// TestNorm: the days and violations by a fixed, a shift (with a night shift) and a flexible schedule.
func TestNorm(t *testing.T) {
	sched := worktime.Default()
	tue := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)
	sat := tue.AddDate(0, 0, 4)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	at := func(d time.Time, h, m int) time.Time {
		return d.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	ptr := func(t time.Time) *time.Time { return &t }

	fixed := Norm{Company: sched, WS: &WorkSchedule{Kind: "fixed", MinDuration: "06:00",
		Times: []ScheduleTime{{Days: []int{1, 2, 3, 4}, Start: "10:00", End: "19:00"}, {Days: []int{6}, Start: "10:00", End: "14:00"}}}}
	if p := fixed.Plan(tue); !p.Work || p.Start != "10:00" {
		t.Errorf("tuesday %+v", p)
	}
	if fixed.Plan(tue.AddDate(0, 0, 3)).Work || !fixed.Plan(sat).Work { // Friday off, Saturday works
		t.Error("friday/saturday")
	}
	if fixed.Plan(time.Date(2026, 11, 4, 0, 0, 0, 0, time.Local)).Work { // a company holiday on a Wednesday
		t.Error("holiday")
	}
	for _, c := range []struct {
		d    Workday
		want []string
	}{
		{Workday{Day: tue, Started: at(tue, 9, 55), Ended: ptr(at(tue, 19, 0))}, nil},
		{Workday{Day: tue, Started: at(tue, 10, 5), Ended: ptr(at(tue, 19, 0))}, []string{"late"}},
		{Workday{Day: tue, Started: at(tue, 10, 0), Ended: ptr(at(tue, 15, 0))}, []string{"short"}},
		{Workday{Day: tue.AddDate(0, 0, 3), Started: at(tue.AddDate(0, 0, 3), 13, 0), Ended: ptr(at(tue.AddDate(0, 0, 3), 14, 0))}, nil}, // a day off
	} {
		if got := fixed.Violations(c.d, now); !slices.Equal(got, c.want) {
			t.Errorf("fixed %v: %v, want %v", c.d.Started, got, c.want)
		}
	}
	fixed.WS.LateAfter, fixed.WS.LeaveBefore = "10:15", "18:30"
	if got := fixed.Violations(Workday{Day: tue, Started: at(tue, 10, 10), Ended: ptr(at(tue, 18, 0))}, now); !slices.Equal(got, []string{"early"}) {
		t.Errorf("late after / leave before: %v", got)
	}

	night := Shift{ID: 2, Name: "Ночная", Start: "20:00", End: "08:00"}
	shift := Norm{Company: sched, WS: &WorkSchedule{Kind: "shift", Tolerance: 10, Shifts: []Shift{night}},
		Shifts: map[string]Shift{tue.Format(time.DateOnly): night}}
	if p := shift.Plan(tue); !p.Work || p.Shift != "Ночная" || shift.Plan(tue.AddDate(0, 0, 1)).Work {
		t.Errorf("shift plan %+v", p)
	}
	next := tue.AddDate(0, 0, 1)
	for _, c := range []struct {
		d    Workday
		want []string
	}{
		{Workday{Day: tue, Started: at(tue, 20, 8), Ended: ptr(at(next, 7, 55))}, nil},
		{Workday{Day: tue, Started: at(tue, 20, 15), Ended: ptr(at(next, 8, 0))}, []string{"late"}},
		{Workday{Day: tue, Started: at(tue, 20, 0), Ended: ptr(at(next, 7, 0))}, []string{"early"}},
		{Workday{Day: next, Started: at(next, 11, 0), Ended: ptr(at(next, 12, 0))}, nil}, // no shift that day
	} {
		if got := shift.Violations(c.d, now); !slices.Equal(got, c.want) {
			t.Errorf("shift %v: %v, want %v", c.d.Started, got, c.want)
		}
	}

	flex := Norm{Company: sched, WS: &WorkSchedule{Kind: "flexible", MinDuration: "04:00"}}
	if p := flex.Plan(sat); !p.Free || p.Work {
		t.Errorf("flexible %+v", p)
	}
	if got := flex.Violations(Workday{Day: sat, Started: at(sat, 23, 0), Ended: ptr(at(sat, 23, 30))}, now); !slices.Equal(got, []string{"short"}) {
		t.Errorf("flexible: %v", got)
	}

	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	rows := BuildSheet([]Person{{ID: "a"}, {ID: "b"}, {ID: "c"}}, nil, nil, nil,
		map[string]Norm{"a": fixed, "b": shift, "c": flex}, sched, month, now)
	a, b, c := rows[0], rows[1], rows[2]
	if a.Cells[15].Kind != "off" || a.Cells[16].Kind != "none" || a.Cells[17].Kind != "off" { // Fri 16 off, Sat 17 works, Sun 18 off
		t.Errorf("fixed kinds %s %s %s", a.Cells[15].Kind, a.Cells[16].Kind, a.Cells[17].Kind)
	}
	if b.Cells[12].Kind != "none" || b.Cells[12].Plan.Shift != "Ночная" || b.Cells[13].Kind != "off" {
		t.Errorf("shift kinds %+v %s", b.Cells[12], b.Cells[13].Kind)
	}
	if c.Cells[12].Kind != "free" {
		t.Errorf("flexible kind %s", c.Cells[12].Kind)
	}
}

// TestWorkSchedules: saving schedules with shifts and members, whose schedule a person has (their own, their nearest
// department's, excluded), the norms with the planned shifts and the plan replaced by a month.
func TestWorkSchedules(t *testing.T) {
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
	boss, anna, olga, ivan := user("boss", "Борис"), user("anna", "Анна"), user("olga", "Ольга"), user("ivan", "Иван")
	var top, store string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Компания', $1) RETURNING id`, boss).Scan(&top))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Склад', $1) RETURNING id`, top).Scan(&store))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $3), ($4, $5)`, anna, olga, store, ivan, top)
	must(err)
	s := NewStore(pool)

	if err := s.SaveWorkSchedule(ctx, boss, &WorkSchedule{Name: "Без смен", Kind: "shift"}); err == nil {
		t.Error("a shift schedule without shifts saved")
	}
	if err := s.SaveWorkSchedule(ctx, boss, &WorkSchedule{Name: "Двойной", Kind: "fixed",
		Times: []ScheduleTime{{Days: []int{1}, Start: "09:00", End: "18:00"}, {Days: []int{1}, Start: "10:00", End: "12:00"}}}); err == nil {
		t.Error("a weekday in two rows saved")
	}
	office := &WorkSchedule{Name: "Офис", Kind: "fixed", Members: []string{"dept:" + top},
		Times: []ScheduleTime{{Days: []int{5, 1, 2, 3, 4}, Start: "09:00", End: "18:00", Break: 60}}, Tolerance: 30}
	must(s.SaveWorkSchedule(ctx, boss, office))
	if office.Tolerance != 0 || !slices.Equal(office.Times[0].Days, []int{1, 2, 3, 4, 5}) {
		t.Errorf("cleaned %+v", office)
	}
	shifts := &WorkSchedule{Name: "Склад", Kind: "shift", Tolerance: 10, Members: []string{"dept:" + store, "nope"}, Excluded: []string{olga},
		Shifts: []Shift{{Name: "Дневная", Start: "08:00", End: "20:00", Break: 60}, {Name: "Ночная", Start: "20:00", End: "08:00"}, {Name: " "}}}
	must(s.SaveWorkSchedule(ctx, boss, shifts))
	ws, err := s.WorkSchedule(ctx, shifts.ID)
	must(err)
	if len(ws.Shifts) != 2 || !slices.Equal(ws.Members, []string{"dept:" + store}) || !slices.Equal(ws.Excluded, []string{olga}) ||
		ws.MemberNames["dept:"+store] != "Склад" {
		t.Fatalf("schedule %+v", ws)
	}
	day, night := ws.Shifts[0], ws.Shifts[1]

	ids, err := s.ScheduleIDs(ctx, []string{boss, anna, olga, ivan})
	must(err)
	if ids[anna] != shifts.ID || ids[olga] != office.ID || ids[ivan] != office.ID || ids[boss] != 0 { // olga is excluded → the company's
		t.Errorf("schedules %v", ids)
	}
	people, err := s.SchedulePeople(ctx, shifts.ID)
	must(err)
	if len(people) != 1 || people[0].ID != anna {
		t.Errorf("people %+v", people)
	}

	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	end := month.AddDate(0, 1, 0)
	must(s.SetShiftPlan(ctx, shifts.ID, []string{anna}, month, end, map[string]int64{
		anna + "|2026-10-05": day.ID, anna + "|2026-10-06": night.ID, olga + "|2026-10-05": day.ID, anna + "|2026-11-01": day.ID, anna + "|2026-10-07": 999999}))
	plan, err := s.ShiftPlan(ctx, []string{anna, olga}, month, end.AddDate(0, 1, 0))
	must(err)
	if len(plan) != 2 || plan[anna+"|2026-10-06"] != night.ID {
		t.Errorf("plan %v", plan)
	}
	norms, err := s.Norms(ctx, []string{anna, olga, boss}, worktime.Default(), month, end)
	must(err)
	if n := norms[anna]; n.WS == nil || n.WS.ID != shifts.ID || n.Shifts["2026-10-06"].Name != "Ночная" {
		t.Errorf("anna's norm %+v", n)
	}
	if norms[boss].WS != nil || !norms[boss].Plan(time.Date(2026, 10, 5, 0, 0, 0, 0, time.Local)).Work {
		t.Errorf("boss's norm %+v", norms[boss])
	}

	// a changed shift keeps its plan, a removed one takes it away; the plan of the month is replaced
	ws.Shifts = []Shift{{ID: day.ID, Name: "День", Start: "09:00", End: "21:00"}}
	must(s.SaveWorkSchedule(ctx, boss, ws))
	plan, err = s.ShiftPlan(ctx, []string{anna}, month, end)
	must(err)
	if len(plan) != 1 || plan[anna+"|2026-10-05"] != day.ID {
		t.Errorf("plan after shifts changed %v", plan)
	}
	must(s.SetShiftPlan(ctx, shifts.ID, []string{anna}, month, end, map[string]int64{anna + "|2026-10-09": day.ID}))
	if plan, _ = s.ShiftPlan(ctx, []string{anna}, month, end); len(plan) != 1 || plan[anna+"|2026-10-09"] != day.ID {
		t.Errorf("plan replaced %v", plan)
	}
	if err := s.SetShiftPlan(ctx, office.ID, []string{anna}, month, end, nil); err == nil {
		t.Error("a plan of a fixed schedule")
	}

	list, err := s.WorkSchedules(ctx)
	must(err)
	if len(list) != 2 || list[1].Name != "Склад" || list[1].Depts != 1 || list[1].People != 0 {
		t.Errorf("list %+v", list)
	}
	must(s.DeleteWorkSchedule(ctx, shifts.ID))
	if ids, _ = s.ScheduleIDs(ctx, []string{anna}); ids[anna] != office.ID {
		t.Errorf("after delete %v", ids)
	}
}
