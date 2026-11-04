package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
	"go24/internal/worktime"
)

func TestPortalRangePrefixes(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		{"192.168.1.0-192.168.1.255", "192.168.1.0/24"},
		{"10.0.0.1", "10.0.0.1/32"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"192.168.0.1-192.168.0.6", "192.168.0.1/32 192.168.0.2/31 192.168.0.4/31 192.168.0.6/32"},
		{"2001:db8::-2001:db8::ffff", "2001:db8::/112"},
	} {
		p, ok := portalParseRange(c.in)
		var got []string
		for _, x := range p {
			got = append(got, x.String())
		}
		if !ok || strings.Join(got, " ") != c.want {
			t.Errorf("%s: %v %v, want %s", c.in, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "x", "10.0.0.5-10.0.0.1", "10.0.0.1-::1"} {
		if _, ok := portalParseRange(bad); ok {
			t.Errorf("%q taken", bad)
		}
	}
	if portalIPRange(portalRangePrefixes(netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("255.255.255.255"))[0].String()) != "0.0.0.0-255.255.255.255" {
		t.Error("the whole range")
	}
}

// TestRestPortalTimeControl (GO24DEV-978): timeman.timecontrol.settings.get/set — only who manages working time changes
// them; report.add — the caller answers their own absence; reports.get — a month by days, another person's to their
// head, not to a colleague; reports.settings.get and reports.users.get; timeman.networkrange.set replaces the networks.
func TestRestPortalTimeControl(t *testing.T) {
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
	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), Notify: notify.NewStore(pool),
		I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	type person struct {
		id  string
		num int64
		key string
	}
	people := map[string]*person{}
	for _, n := range []string{"admin", "ivan", "olga", "petr"} {
		p := &person{}
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ($1, $1, $1 || '@t', $1 = 'admin') RETURNING id, num`, n).
			Scan(&p.id, &p.num))
		p.key, err = srv.Keys.Create(ctx, p.id, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		people[n] = p
	}
	admin, ivan, olga, petr := people["admin"], people["ivan"], people["olga"], people["petr"]
	var root, sales string
	var salesNum int64
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Продажи', $1, $2) RETURNING id, num`, root, ivan.id).
		Scan(&sales, &salesNum))
	for _, p := range []*person{ivan, olga} {
		_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, p.id, sales)
		must(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, petr.id, root)
	must(err)
	num := func(p *person) string { return strconv.FormatInt(p.num, 10) }
	call := func(who *person, method, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/rest/"+num(who)+"/"+who.key+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, body, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	result := func(who *person, method, body string) map[string]any {
		t.Helper()
		return call(who, method, body, 200)["result"].(map[string]any)
	}

	// the settings
	if st := result(olga, "timeman.timecontrol.settings.get", `{}`); st["active"] != false || st["minimum_idle_for_report"] != float64(worktime.DefaultIdleMinutes) {
		t.Fatalf("settings before: %v", st)
	}
	call(olga, "timeman.timecontrol.settings.set", `{"ACTIVE": "Y"}`, 403)
	call(admin, "timeman.timecontrol.settings.set", `{"ACTIVE": "Y", "MINIMUM_IDLE_FOR_REPORT": 0}`, 400)
	call(admin, "timeman.timecontrol.settings.set", `{"ACTIVE": "Y", "REPORT_REQUEST_TYPE": "x"}`, 400)
	if r := call(admin, "timeman.timecontrol.settings.set", `{"ACTIVE": "Y", "MINIMUM_IDLE_FOR_REPORT": 20}`, 200)["result"]; r != true {
		t.Fatalf("set: %v", r)
	}
	if st := result(olga, "timeman.timecontrol.settings.get", `{}`); st["active"] != true || st["minimum_idle_for_report"] != 20.0 ||
		st["report_request_type"] != "all" {
		t.Fatalf("settings: %v", st)
	}
	call(admin, "timeman.timecontrol.settings.set", `{"REPORT_REQUEST_TYPE": "none"}`, 200)
	if st := result(olga, "timeman.timecontrol.settings.get", `{}`); st["active"] != true || st["report_request_type"] != "none" ||
		st["minimum_idle_for_report"] != 20.0 {
		t.Fatalf("not asked: %v", st)
	}

	// Olga's day with an absence
	now := time.Now()
	start := now.Add(-3 * time.Hour)
	must(srv.Company.WorkAction(ctx, olga.id, "start", "", start, company.Place{}))
	_, err = srv.Company.Alive(ctx, olga.id, start.Add(time.Minute), 20*time.Minute, nil)
	must(err)
	idle, err := srv.Company.Alive(ctx, olga.id, start.Add(time.Hour), 20*time.Minute, nil)
	must(err)
	if idle == nil {
		t.Fatal("no absence")
	}
	id := strconv.FormatInt(idle.ID, 10)
	call(olga, "timeman.timecontrol.report.add", `{"REPORT_ID": "x"}`, 400)
	call(olga, "timeman.timecontrol.report.add", `{"REPORT_ID": `+id+`, "TYPE": "NAP"}`, 400)
	call(petr, "timeman.timecontrol.report.add", `{"REPORT_ID": `+id+`, "TEXT": "чужое"}`, 400) // ERROR_NOT_FOUND answers it
	call(olga, "timeman.timecontrol.report.add", `{"REPORT_ID": `+id+`, "TEXT": "У клиента", "TYPE": "PRIVATE"}`, 200)

	month := `{"USER_ID": ` + num(olga) + `, "YEAR": ` + strconv.Itoa(now.Year()) + `, "MONTH": ` + strconv.Itoa(int(now.Month())) + `}`
	call(petr, "timeman.timecontrol.reports.get", month, 403)
	call(olga, "timeman.timecontrol.reports.get", `{"MONTH": 13}`, 400)
	rep := result(ivan, "timeman.timecontrol.reports.get", month)
	days := rep["report"].(map[string]any)["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("days: %v", rep)
	}
	day := days[0].(map[string]any)
	reports := day["reports"].([]any)
	if day["index"] != float64(start.Day()) || day["workday_date_start"] == nil || day["workday_complete"] != false || len(reports) != 1 {
		t.Fatalf("day: %v", day)
	}
	if r := reports[0].(map[string]any); r["type"] != "PRIVATE" || r["text"] != "У клиента" || r["duration"] != 3540.0 {
		t.Fatalf("report: %v", r)
	}
	if u := rep["user"].(map[string]any); u["id"] != float64(olga.num) || u["name"] != "olga" {
		t.Fatalf("user: %v", u)
	}
	if mine := result(olga, "timeman.timecontrol.reports.get", `{"YEAR": 2001, "MONTH": 1}`); len(mine["report"].(map[string]any)["days"].([]any)) != 0 {
		t.Fatalf("an empty month: %v", mine)
	}

	// whose reports
	if st := result(ivan, "timeman.timecontrol.reports.settings.get", `{}`); st["user_id"] != float64(ivan.num) || st["user_admin"] != false ||
		st["department_manager"] != true || st["report_view_type"] != "full" || st["department_id"] != float64(salesNum) ||
		len(st["departments"].([]any)) != 1 {
		t.Fatalf("Ivan: %v", st)
	}
	if st := result(petr, "timeman.timecontrol.reports.settings.get", `{}`); st["department_manager"] != false || st["report_view_type"] != "simple" {
		t.Fatalf("Petr: %v", st)
	}
	if st := result(admin, "timeman.timecontrol.reports.settings.get", `{}`); st["user_admin"] != true || len(st["departments"].([]any)) != 2 {
		t.Fatalf("admin: %v", st)
	}
	users := call(ivan, "timeman.timecontrol.reports.users.get", `{"DEPARTMENT_ID": `+strconv.FormatInt(salesNum, 10)+`}`, 200)["result"].([]any)
	if len(users) != 2 {
		t.Fatalf("Ivan's department: %v", users)
	}
	if users := call(petr, "timeman.timecontrol.reports.users.get", `{"DEPARTMENT_ID": `+strconv.FormatInt(salesNum, 10)+`}`, 200)["result"].([]any); len(users) != 0 {
		t.Fatalf("Petr sees: %v", users)
	}
	call(ivan, "timeman.timecontrol.reports.users.get", `{"DEPARTMENT_ID": 999}`, 400)

	// the office networks
	ranges := `{"RANGES": [{"ip_range": "192.168.0.0-192.168.0.255", "name": "Офис"}, {"ip_range": "10.1.1.7", "name": "VPN"}]}`
	call(olga, "timeman.networkrange.set", ranges, 403)
	call(admin, "timeman.networkrange.set", `{"RANGES": [{"ip_range": "x"}]}`, 400)
	call(admin, "timeman.networkrange.set", ranges, 200)
	got := call(olga, "timeman.networkrange.get", `{}`, 200)["result"].([]any)
	if len(got) != 2 || got[0].(map[string]any)["ip_range"] != "192.168.0.0-192.168.0.255" || got[1].(map[string]any)["ip_range"] != "10.1.1.7" {
		t.Fatalf("ranges: %v", got)
	}
	if r := result(olga, "timeman.networkrange.check", `{"IP": "10.1.1.7"}`); r["range_name"] != "VPN" {
		t.Fatalf("check: %v", r)
	}
	sched, err := ts.Schedule(ctx)
	must(err)
	if !sched.TimeControl || sched.IdleMinutes != 20 || len(sched.Offices) != 2 {
		t.Fatalf("the schedule: %+v", sched)
	}
	call(admin, "timeman.networkrange.set", `{"RANGES": []}`, 200)
	if got := call(olga, "timeman.networkrange.get", `{}`, 200)["result"].([]any); len(got) != 0 {
		t.Fatalf("cleared: %v", got)
	}
}
