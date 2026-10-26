package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestRecurrenceDays: the days of the copies — months keep the start day or take the month's last one.
func TestRecurrenceDays(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	for _, c := range []struct {
		r    Recurrence
		n    int
		want string
	}{
		{Recurrence{Unit: "day", Every: 3, Start: d("2026-10-30")}, 1, "2026-11-02"},
		{Recurrence{Unit: "week", Every: 2, Start: d("2026-10-22")}, 2, "2026-11-19"},
		{Recurrence{Unit: "month", Every: 1, Start: d("2026-01-31")}, 1, "2026-02-28"},
		{Recurrence{Unit: "month", Every: 1, Start: d("2026-01-31")}, 2, "2026-03-31"},
		{Recurrence{Unit: "month", Every: 3, Start: d("2026-11-30")}, 1, "2027-02-28"},
	} {
		if got := c.r.nth(c.n).Format(time.DateOnly); got != c.want {
			t.Errorf("%+v #%d: %s, want %s", c.r, c.n, got, c.want)
		}
	}
}

// TestRecurringDeals: a deal repeated monthly twice makes on its days copies in the first stage with the client,
// products and own fields; the timelines point to each other, the responsible is notified; a late schedule makes one
// copy, not a pile; after the last copy the schedule is gone; bad schedules are refused; stopping removes it.
func TestRecurringDeals(t *testing.T) {
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
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'petr', 'petr@t') RETURNING id`).Scan(&petr))
	s := NewStore(pool)
	s.Tasks = tasks.NewStore(pool)
	set := tasks.CRMFieldSets["deal"]
	must(s.Tasks.AddFieldTo(ctx, set, tasks.Field{Name: "Договор", Kind: "string"}))
	fields, err := s.Tasks.FieldsOf(ctx, set)
	must(err)

	pipes, err := s.Pipelines(ctx)
	must(err)
	p := pipes[0]
	co, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка"})
	must(err)
	c1, err := s.SaveContact(ctx, anna, Contact{Name: "Иван"})
	must(err)
	c2, err := s.SaveContact(ctx, anna, Contact{Name: "Мария"})
	must(err)
	src, err := s.SaveDeal(ctx, anna, Deal{Title: "Абонентское обслуживание", PipelineID: p.ID, Company: Ref{ID: co},
		Contacts: []Ref{{ID: c1}, {ID: c2}}, Responsible: Ref{ID: petr}, Comment: "раз в месяц"})
	must(err)
	_, err = s.MoveDeal(ctx, anna, src, p.Stages[1].ID)
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", src, []ProductRow{{Name: "Обслуживание", Price: 5000, Quantity: 1}})
	must(err)
	must(s.Tasks.SetFieldValuesOf(ctx, set, src, map[string]string{fields[0].ID: "Д-7"}))

	day := func(s string) time.Time { v, _ := time.ParseInLocation(time.DateOnly, s, time.Local); return v }
	today := day("2026-10-22")
	for _, bad := range []Recurrence{
		{DealID: src, Unit: "year", Every: 1, Start: today},
		{DealID: src, Unit: "month", Every: 0, Start: today},
		{DealID: src, Unit: "month", Every: 1, Start: day("2026-10-21")},
		{DealID: src, Unit: "month", Every: 1, Start: today, Until: ptrTime(day("2026-10-01"))},
	} {
		if err := s.SetRecurrence(ctx, anna, bad, today); err == nil {
			t.Fatalf("set %+v", bad)
		}
	}
	if err := s.SetRecurrence(ctx, anna, Recurrence{DealID: "00000000-0000-0000-0000-000000000000", Unit: "day", Every: 1, Start: today}, today); err != ErrNotFound {
		t.Fatalf("no deal: %v", err)
	}
	must(s.SetRecurrence(ctx, anna, Recurrence{DealID: src, Unit: "month", Every: 1, Start: day("2026-10-31"), Limit: 2}, today))

	// not yet
	made, errs := s.RunRecurringDue(ctx, day("2026-10-30"))
	if len(made) != 0 || len(errs) != 0 {
		t.Fatalf("early: %v %v", made, errs)
	}
	made, errs = s.RunRecurringDue(ctx, day("2026-10-31").Add(9*time.Hour))
	if len(made) != 1 || len(errs) != 0 {
		t.Fatalf("made %v %v", made, errs)
	}
	cp, err := s.Deal(ctx, made[0])
	must(err)
	if cp.Title != "Абонентское обслуживание" || cp.Stage.ID != p.Stages[0].ID || cp.Company.ID != co || len(cp.Contacts) != 2 ||
		cp.Contacts[0].ID != c1 || cp.Responsible.ID != petr || cp.Amount != 5000 || cp.Comment != "раз в месяц" {
		t.Fatalf("copy %+v", cp)
	}
	if rows, _ := s.ProductRows(ctx, "deal", cp.ID); len(rows) != 1 || rows[0].Name != "Обслуживание" {
		t.Fatalf("copy rows %+v", rows)
	}
	if vals, _ := s.Tasks.FieldValuesOf(ctx, set, cp.ID); len(vals) != 1 || vals[0].Value != "Д-7" {
		t.Fatalf("copy fields %+v", vals)
	}
	hasEvent := func(id, kind, body string) bool {
		tl, err := s.Timeline(ctx, "deal", id)
		must(err)
		for _, e := range tl {
			if e.Kind == kind && e.Body == body {
				return true
			}
		}
		return false
	}
	if !hasEvent(cp.ID, "recurred_from", src+"|Абонентское обслуживание") || !hasEvent(src, "recurred", cp.ID+"|Абонентское обслуживание") {
		t.Fatal("timelines do not point to each other")
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'crm_recurring'`, petr).Scan(&notes))
	if notes != 1 {
		t.Fatalf("notifications %d", notes)
	}
	rc, err := s.Recurrence(ctx, src)
	must(err)
	if rc == nil || rc.Count != 1 || rc.Next.Format(time.DateOnly) != "2026-11-30" {
		t.Fatalf("schedule %+v", rc)
	}
	// late by months: one copy, and the schedule is over (2 of 2)
	made, _ = s.RunRecurringDue(ctx, day("2027-02-10"))
	if len(made) != 1 {
		t.Fatalf("late: %v", made)
	}
	if rc, _ = s.Recurrence(ctx, src); rc != nil {
		t.Fatalf("schedule after the last copy: %+v", rc)
	}

	// until a date: weekly from today until in 10 days — 2 copies; stopping removes the schedule
	must(s.SetRecurrence(ctx, anna, Recurrence{DealID: src, Unit: "week", Every: 1, Start: today, Until: ptrTime(day("2026-11-01"))}, today))
	made, _ = s.RunRecurringDue(ctx, today)
	made2, _ := s.RunRecurringDue(ctx, day("2026-10-29"))
	if len(made) != 1 || len(made2) != 1 {
		t.Fatalf("weekly %v %v", made, made2)
	}
	if rc, _ = s.Recurrence(ctx, src); rc != nil {
		t.Fatalf("schedule past its end: %+v", rc)
	}
	must(s.SetRecurrence(ctx, anna, Recurrence{DealID: src, Unit: "day", Every: 1, Start: today}, today))
	must(s.StopRecurrence(ctx, src))
	if rc, _ = s.Recurrence(ctx, src); rc != nil {
		t.Fatal("stopped schedule stays")
	}
	// deleting the deal deletes its schedule
	must(s.SetRecurrence(ctx, anna, Recurrence{DealID: src, Unit: "day", Every: 1, Start: today}, today))
	must(s.DeleteDeal(ctx, anna, src))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_deal_recurrence`).Scan(&left))
	if left != 0 {
		t.Fatal("schedule of a deleted deal")
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
