package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMDeedColumns: «Дела» lays the cards out by their nearest deed, the nearest first; a card without deeds is «none».
func TestCRMDeedColumns(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local) // a Wednesday
	cards := []crmCard{{ID: "a", Sum: 10}, {ID: "b", Sum: 5}, {ID: "c", Sum: 1}, {ID: "d", Sum: 2}}
	next := map[string]*crm.Activity{
		"a": {Deadline: now.Add(-time.Hour)},
		"b": {Deadline: now.Add(3 * time.Hour)},
		"c": {Deadline: now.Add(time.Hour)},
	}
	cols := crmDeedColumns(cards, next, now)
	by := map[string]crmDeedCol{}
	for _, c := range cols {
		by[c.Period] = c
	}
	if len(cols) != len(tasks.DeadlinePeriods) || len(by["overdue"].Items) != 1 || by["none"].Items[0].ID != "d" {
		t.Fatalf("columns: %+v", cols)
	}
	if today := by["today"]; len(today.Items) != 2 || today.Items[0].ID != "c" || today.Sum != 6 {
		t.Fatalf("today: %+v", today)
	}
}

// TestCRMMonth: the calendar's month runs in whole weeks from Monday, the cards on their days.
func TestCRMMonth(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	first := crmMonthStart("2026-10", now)
	weeks := crmMonth(first, []crmCard{{ID: "a"}, {ID: "b"}}, func(c crmCard) string {
		if c.ID == "a" {
			return "2026-10-28"
		}
		return ""
	}, now)
	if len(weeks) != 5 || weeks[0][0].Date.Format(time.DateOnly) != "2026-09-28" || !weeks[0][0].Out || weeks[4][6].Date.Format(time.DateOnly) != "2026-11-01" {
		t.Fatalf("weeks: %d from %v", len(weeks), weeks[0][0].Date)
	}
	day := weeks[4][2]
	if !day.Today || len(day.Items) != 1 || day.Items[0].ID != "a" {
		t.Fatalf("the 28th: %+v", day)
	}
	if got := crmMonthStart("bad", now); got.Format(time.DateOnly) != "2026-10-01" {
		t.Fatalf("default month %v", got)
	}
}

// TestCRMViewsHTTP: the views «Дела» and «Календарь» of deals — a card dropped into a column of «Дела»
// moves its nearest deed (or gets a new one), a card dropped onto a day of «Календарь» gets that day as its closing date
// or an own date field.
func TestCRMViewsHTTP(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe := pipes[0].ID

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	do := func(path string, form url.Values) (int, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		req.Header.Set("Accept", "application/json")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	closeAt := time.Now().AddDate(0, 0, 3)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipe, Amount: 700, CloseDate: &closeAt})
	must(err)

	// «Дела»: without deeds the card is in «Нет дел»; a drop into «Сегодня» plans a deed for today
	code, page := do("/crm/deals?view=activities", nil)
	if code != http.StatusOK || !strings.Contains(page, `data-board="crmact"`) || !strings.Contains(page, `data-stage="today"`) || strings.Contains(page, `data-stage="none"`) {
		t.Fatalf("activities view: %d", code)
	}
	code, body := do("/crm/deals/"+deal+"/deed-period", url.Values{"period": {"today"}})
	var res struct{ Card, Error string }
	must(json.Unmarshal([]byte(body), &res))
	if code != http.StatusOK || !strings.Contains(res.Card, "Поставка") {
		t.Fatalf("deed-period: %d %s", code, body)
	}
	next, err := srv.CRM.NextActivities(ctx, "deal", []string{deal})
	must(err)
	a := next[deal]
	if a == nil || a.Kind != "todo" || tasks.DeadlinePeriod(&a.Deadline, time.Now()) != "today" {
		t.Fatalf("the new deed: %+v", a)
	}
	// the same deed moves to next week, no second one
	code, _ = do("/crm/deals/"+deal+"/deed-period", url.Values{"period": {"next_week"}})
	acts, err := srv.CRM.Activities(ctx, "deal", deal)
	must(err)
	if code != http.StatusOK || len(acts) != 1 || tasks.DeadlinePeriod(&acts[0].Deadline, time.Now()) != "next_week" {
		t.Fatalf("moved deed: %d %+v", code, acts)
	}
	if code, _ = do("/crm/deals/"+deal+"/deed-period", url.Values{"period": {"none"}}); code == http.StatusOK {
		t.Fatal("«none» accepted")
	}

	// «Календарь»: deals by their closing date; a drop onto a day changes it
	month := closeAt.Format("2006-01")
	code, page = do("/crm/deals?view=calendar&month="+month, nil)
	if code != http.StatusOK || !strings.Contains(page, `data-crmcal="/crm/deals/"`) || !strings.Contains(page, `data-date="close"`) ||
		!strings.Contains(page, `data-key="`+deal+`" draggable="true"`) {
		t.Fatalf("calendar view: %d", code)
	}
	day := closeAt.AddDate(0, 0, 1).Format(time.DateOnly)
	if code, body = do("/crm/deals/"+deal+"/date", url.Values{"date": {"close"}, "day": {day}}); code != http.StatusNoContent {
		t.Fatalf("date move: %d %s", code, body)
	}
	d, err := srv.CRM.Deal(ctx, deal)
	must(err)
	if d.CloseDate == nil || d.CloseDate.Format(time.DateOnly) != day {
		t.Fatalf("close date %v, want %s", d.CloseDate, day)
	}
	// an own date field: shown when chosen, set by a drop; «Создано» is not movable
	must(srv.Tasks.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Отгрузка", Kind: "date"}))
	fields, err := srv.Tasks.FieldsOf(ctx, tasks.CRMFieldSets["deal"])
	must(err)
	fid := fields[0].ID
	if code, _ = do("/crm/deals/"+deal+"/date", url.Values{"date": {"f:" + fid}, "day": {day}}); code != http.StatusNoContent {
		t.Fatalf("field move: %d", code)
	}
	code, page = do("/crm/deals?view=calendar&month="+day[:7]+"&date=f:"+fid, nil)
	if code != http.StatusOK || !strings.Contains(page, `data-day="`+day+`"`) || !strings.Contains(page, `data-key="`+deal+`"`) || !strings.Contains(page, "Отгрузка") {
		t.Fatalf("calendar by the field: %d", code)
	}
	if code, _ = do("/crm/deals/"+deal+"/date", url.Values{"date": {"created"}, "day": {day}}); code == http.StatusNoContent {
		t.Fatal("«Создано» moved")
	}
	_, page = do("/crm/deals?view=calendar&date=created", nil)
	if !strings.Contains(page, `data-key="`+deal+`"`) || strings.Contains(page, `data-key="`+deal+`" draggable`) {
		t.Fatal("«Создано» draggable")
	}
}
