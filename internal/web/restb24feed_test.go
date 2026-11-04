package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
	"go24/internal/worktime"
)

// TestRestPortalFeed (GO24DEV-948): log.blogpost.add to all, to a person and a department, to a group (with a person
// shared) — each seen only by its addressees; log.blogpost.get pages and finds by POST_ID and LOG_RIGHTS;
// update changes the text (only the author) and readdresses; share adds people; getusers.important — who read;
// log.blogcomment.add and delete; delete. With an S3 the FILES of base64 become the post's files.
func TestRestPortalFeed(t *testing.T) {
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
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), I18n: bundle,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if s3ep := os.Getenv("GO24_TEST_S3"); s3ep != "" {
		s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	type person struct {
		id  string
		num int64
		key string
	}
	people := map[string]*person{}
	for _, n := range []string{"anna", "ivan", "olga", "petr"} {
		p := &person{}
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id, num`, n).Scan(&p.id, &p.num))
		p.key, err = srv.Keys.Create(ctx, p.id, "k", []string{"feed:read", "feed:write", "projects:read"}, time.Hour)
		must(err)
		people[n] = p
	}
	anna, ivan, olga, petr := people["anna"], people["ivan"], people["olga"], people["petr"]
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Склад') RETURNING num::text`).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) SELECT $1, id FROM departments WHERE num::text = $2`, olga.id, dept)
	must(err)
	_, err = ts.CreateProject(ctx, anna.id, "SEC", "Секрет", "")
	must(err)
	var group string
	must(pool.QueryRow(ctx, `SELECT num::text FROM projects WHERE key = 'SEC'`).Scan(&group))
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
	ids := func(out map[string]any) string {
		var list []string
		for _, x := range out["result"].([]any) {
			list = append(list, x.(map[string]any)["ID"].(string))
		}
		return strings.Join(list, ",")
	}

	// to all, important; to Ivan and the department; to the group with Petr shared
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	all := portalStr(call(anna, "log.blogpost.add", `{"POST_TITLE": "Собрание", "POST_MESSAGE": "В пятницу", "IMPORTANT": "Y",
		"IMPORTANT_DATE_END": "`+tomorrow+`"}`, 200)["result"])
	some := portalStr(call(anna, "log.blogpost.add", `{"POST_MESSAGE": "Ивану и складу", "DEST": ["U`+num(ivan)+`", "DR`+dept+`"]}`, 200)["result"])
	grp := portalStr(call(anna, "log.blogpost.add", `{"POST_MESSAGE": "Группе", "DEST": ["SG`+group+`", "U`+num(petr)+`"]}`, 200)["result"])
	call(anna, "log.blogpost.add", `{"POST_MESSAGE": "x", "DEST": ["X1"]}`, 400)
	call(anna, "log.blogpost.add", `{"POST_MESSAGE": " "}`, 400)
	call(ivan, "log.blogpost.add", `{"POST_MESSAGE": "x", "DEST": ["SG`+group+`"]}`, 403)

	got := call(anna, "log.blogpost.get", `{}`, 200)
	if ids(got) != grp+","+some+","+all || portalStr(got["total"]) != "3" {
		t.Fatalf("Anna's posts: %v", got)
	}
	row := call(ivan, "log.blogpost.get", `{"POST_ID": `+all+`}`, 200)["result"].([]any)[0].(map[string]any)
	if row["TITLE"] != "Собрание" || row["DETAIL_TEXT"] != "В пятницу" || row["AUTHOR_ID"] != num(anna) || row["UF_BLOG_POST_IMPRTNT"] != "1" ||
		row["HAS_SOCNET_ALL"] != "Y" || row["MICRO"] != "N" || row["UF_IMPRTANT_DATE_END"] == nil {
		t.Fatalf("the post to all: %v", row)
	}
	if s := ids(call(ivan, "log.blogpost.get", `{}`, 200)); s != some+","+all {
		t.Fatalf("Ivan's posts: %s", s)
	}
	if s := ids(call(olga, "log.blogpost.get", `{}`, 200)); s != some+","+all {
		t.Fatalf("Olga's posts (the department): %s", s)
	}
	if s := ids(call(petr, "log.blogpost.get", `{}`, 200)); s != grp+","+all {
		t.Fatalf("Petr's posts (shared): %s", s)
	}
	if s := ids(call(anna, "log.blogpost.get", `{"LOG_RIGHTS": ["SG`+group+`"]}`, 200)); s != grp {
		t.Fatalf("LOG_RIGHTS: %s", s)
	}
	call(olga, "log.blogpost.get", `{"POST_ID": `+grp+`}`, 403)

	// a page of 50: the next one from 50
	for i := 0; i < 52; i++ {
		call(ivan, "log.blogpost.add", `{"POST_MESSAGE": "Пост `+strconv.Itoa(i)+`"}`, 200)
	}
	page := call(olga, "log.blogpost.get", `{}`, 200)
	if len(page["result"].([]any)) != 50 || portalStr(page["next"]) != "50" {
		t.Fatalf("the first page: %d, next %v", len(page["result"].([]any)), page["next"])
	}
	page = call(olga, "log.blogpost.get", `{"start": 50}`, 200)
	if len(page["result"].([]any)) != 4 || page["next"] != nil || portalStr(page["total"]) != "54" {
		t.Fatalf("the second page: %d, next %v, total %v", len(page["result"].([]any)), page["next"], page["total"])
	}

	// update: the author only; readdressed to Olga alone
	call(ivan, "log.blogpost.update", `{"POST_ID": `+some+`, "POST_MESSAGE": "чужое"}`, 403)
	call(anna, "log.blogpost.update", `{"POST_ID": `+some+`, "POST_TITLE": "Важно", "DEST": ["U`+num(olga)+`"]}`, 200)
	row = call(anna, "log.blogpost.get", `{"POST_ID": `+some+`}`, 200)["result"].([]any)[0].(map[string]any)
	if row["TITLE"] != "Важно" || row["DETAIL_TEXT"] != "Ивану и складу" {
		t.Fatalf("updated: %v", row)
	}
	call(ivan, "log.blogpost.get", `{"POST_ID": `+some+`}`, 403)
	// share: Ivan again
	if call(olga, "log.blogpost.share", `{"POST_ID": `+some+`, "DEST": ["U`+num(ivan)+`"]}`, 200)["result"] != true {
		t.Fatal("share")
	}
	call(ivan, "log.blogpost.get", `{"POST_ID": `+some+`}`, 200)
	if call(ivan, "log.blogpost.share", `{"POST_ID": `+all+`, "DEST": ["U`+num(petr)+`"]}`, 200)["result"] != true {
		t.Fatal("share of a post to all")
	}

	// who read the important post
	allID, err := srv.portalNum(ctx, "feed_posts", all)
	must(err)
	_, err = srv.Feed.MarkRead(ctx, ivan.id, allID)
	must(err)
	if r := call(anna, "log.blogpost.getusers.important", `{"POST_ID": `+all+`}`, 200)["result"].([]any); len(r) != 1 || portalStr(r[0]) != num(ivan) {
		t.Fatalf("readers: %v", r)
	}

	// comments
	cm := portalStr(call(ivan, "log.blogcomment.add", `{"POST_ID": `+all+`, "TEXT": "Буду"}`, 200)["result"])
	call(olga, "log.blogcomment.add", `{"POST_ID": `+grp+`, "TEXT": "x"}`, 403)
	if row = call(anna, "log.blogpost.get", `{"POST_ID": `+all+`}`, 200)["result"].([]any)[0].(map[string]any); row["NUM_COMMENTS"] != "1" {
		t.Fatalf("comments: %v", row)
	}
	call(olga, "log.blogcomment.delete", `{"COMMENT_ID": `+cm+`}`, 403)
	call(ivan, "log.blogcomment.delete", `{"COMMENT_ID": `+cm+`}`, 200)
	call(ivan, "log.blogcomment.delete", `{"COMMENT_ID": `+cm+`}`, 400)

	// files of base64 (with a store)
	if os.Getenv("GO24_TEST_S3") != "" {
		b64 := base64.StdEncoding.EncodeToString([]byte("смета"))
		p := portalStr(call(ivan, "log.blogpost.add", `{"POST_MESSAGE": "С файлом", "FILES": [["смета.txt", "`+b64+`"]]}`, 200)["result"])
		row = call(olga, "log.blogpost.get", `{"POST_ID": `+p+`}`, 200)["result"].([]any)[0].(map[string]any)
		if len(row["FILES"].([]any)) != 1 {
			t.Fatalf("files: %v", row)
		}
		portalStr(call(ivan, "log.blogcomment.add", `{"POST_ID": `+p+`, "TEXT": "ещё", "FILES": ["a.txt", "`+b64+`"]}`, 200)["result"])
	}
	call(ivan, "log.blogpost.add", `{"POST_MESSAGE": "x", "FILES": [["a.txt", "не base64"]]}`, 400)

	// delete: the author
	call(ivan, "log.blogpost.delete", `{"POST_ID": `+all+`}`, 403)
	call(anna, "log.blogpost.delete", `{"POST_ID": `+all+`}`, 200)
	call(anna, "log.blogpost.get", `{"POST_ID": `+all+`}`, 400)
}

func TestPortalFilePairs(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		ok   bool
	}{
		{`["a.txt", "QQ=="]`, 1, true},
		{`[["a.txt", "QQ=="], ["b.txt", "Qg=="]]`, 2, true},
		{`{"0": {"0": "a.txt", "1": "QQ=="}, "1": {"0": "b.txt", "1": "Qg=="}}`, 2, true},
		{`{"0": "a.txt", "1": "QQ=="}`, 1, true},
		{`"QQ=="`, 0, false},
		{`[["a.txt"]]`, 0, false},
	} {
		var v any
		if err := json.Unmarshal([]byte(c.in), &v); err != nil {
			t.Fatal(err)
		}
		got, ok := portalFilePairs(v)
		if ok != c.ok || len(got) != c.want {
			t.Errorf("%s: %v %v", c.in, got, ok)
		}
	}
	got, _ := portalFilePairs(map[string]any{"1": []any{"b", "2"}, "0": []any{"a", "1"}, "10": []any{"c", "3"}})
	if len(got) != 3 || got[0][0] != "a" || got[1][0] != "b" || got[2][0] != "c" {
		t.Errorf("the order of a form: %v", got)
	}
}

func TestPortalIPRange(t *testing.T) {
	for in, want := range map[string]string{"192.168.1.0/24": "192.168.1.0-192.168.1.255", "10.0.0.7/32": "10.0.0.7",
		"10.1.0.0/15": "10.0.0.0-10.1.255.255", "2001:db8::/126": "2001:db8::-2001:db8::3"} {
		if got := portalIPRange(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
	if portalWeekDays([]int{1, 2, 3, 4, 5}) != "12345" || portalWeekDays([]int{0, 6}) != "67" || portalClock(3725) != "01:02:05" {
		t.Error("days or clock")
	}
}

// TestRestPortalTimeman (GO24DEV-948): timeman.open starts the day, pause pauses it, open goes on, close finishes it with
// the report, open continues it; timeman.status of another person — to an administrator, not to a colleague; an
// administrator starts another's day; timeman.settings by the company's time; networkrange.get and check.
func TestRestPortalTimeman(t *testing.T) {
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
	for _, n := range []string{"admin", "ivan", "olga"} {
		p := &person{}
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ($1, $1, $1 || '@t', $1 = 'admin') RETURNING id, num`, n).
			Scan(&p.id, &p.num))
		p.key, err = srv.Keys.Create(ctx, p.id, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		people[n] = p
	}
	admin, ivan, olga := people["admin"], people["ivan"], people["olga"]
	sched := worktime.Default()
	sched.LateAfter, sched.MinDuration = "09:15", "08:00"
	sched.Offices = []worktime.Office{{Net: "192.168.1.0/24", Name: "Главный офис"}}
	sched.Geo = true // «Местоположение»: LAT and LON of timeman.open are kept
	must(ts.SetSchedule(ctx, admin.id, sched))
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
	status := func(who *person, body string) map[string]any {
		t.Helper()
		return call(who, "timeman.status", body, 200)["result"].(map[string]any)
	}

	if st := status(ivan, `{}`); st["STATUS"] != "CLOSED" || st["TIME_START"] != nil {
		t.Fatalf("before the day: %v", st)
	}
	call(ivan, "timeman.pause", `{}`, 400)
	if st := call(ivan, "timeman.open", `{"LAT": 55.751244, "LON": "37.618423"}`, 200)["result"].(map[string]any); st["STATUS"] != "OPENED" ||
		st["ACTIVE"] != true || st["TIME_START"] == nil || st["LAT_OPEN"] != 55.751244 || st["LON_OPEN"] != 37.618423 {
		t.Fatalf("opened: %v", st)
	}
	call(ivan, "timeman.open", `{}`, 200) // open already: as it is
	if st := call(ivan, "timeman.pause", `{}`, 200)["result"].(map[string]any); st["STATUS"] != "PAUSED" {
		t.Fatalf("paused: %v", st)
	}
	if st := call(ivan, "timeman.open", `{}`, 200)["result"].(map[string]any); st["STATUS"] != "OPENED" {
		t.Fatalf("resumed: %v", st)
	}
	if st := call(ivan, "timeman.close", `{"REPORT": "Сделал отчёт"}`, 200)["result"].(map[string]any); st["STATUS"] != "CLOSED" || st["TIME_FINISH"] == nil ||
		st["LAT_CLOSE"] != 0.0 {
		t.Fatalf("closed: %v", st)
	}
	var report string
	must(pool.QueryRow(ctx, `SELECT report FROM workdays WHERE user_id = $1`, ivan.id).Scan(&report))
	if report != "Сделал отчёт" {
		t.Fatalf("report: %q", report)
	}
	call(ivan, "timeman.close", `{}`, 400)
	if st := call(ivan, "timeman.open", `{}`, 200)["result"].(map[string]any); st["STATUS"] != "OPENED" || st["TIME_FINISH"] != nil {
		t.Fatalf("continued: %v", st)
	}

	// another person's day
	call(olga, "timeman.status", `{"USER_ID": `+num(ivan)+`}`, 403)
	call(olga, "timeman.open", `{"USER_ID": `+num(ivan)+`}`, 403)
	if st := status(admin, `{"USER_ID": `+num(ivan)+`}`); st["STATUS"] != "OPENED" {
		t.Fatalf("Ivan to the administrator: %v", st)
	}
	call(admin, "timeman.open", `{"USER_ID": `+num(olga)+`}`, 200)
	if st := status(olga, `{}`); st["STATUS"] != "OPENED" {
		t.Fatalf("Olga's day started by the administrator: %v", st)
	}

	// a day of yesterday left open
	_, err = pool.Exec(ctx, `UPDATE workdays SET day = day - 1, started_at = started_at - interval '1 day' WHERE user_id = $1`, olga.id)
	must(err)
	if st := status(olga, `{}`); st["STATUS"] != "EXPIRED" {
		t.Fatalf("expired: %v", st)
	}

	set := call(ivan, "timeman.settings", `{}`, 200)["result"].(map[string]any)
	if set["UF_TM_MAX_START"] != "09:15:00" || set["UF_TM_MIN_DURATION"] != "08:00:00" || set["UF_TM_FREE"] != false || set["ADMIN"] != false {
		t.Fatalf("settings: %v", set)
	}
	if r := call(ivan, "timeman.networkrange.get", `{}`, 200)["result"].([]any); len(r) != 1 ||
		r[0].(map[string]any)["ip_range"] != "192.168.1.0-192.168.1.255" || r[0].(map[string]any)["name"] != "Главный офис" {
		t.Fatalf("ranges: %v", r)
	}
	if r := call(ivan, "timeman.networkrange.check", `{"IP": "192.168.1.20"}`, 200)["result"].(map[string]any); r["range_name"] != "Главный офис" {
		t.Fatalf("check in: %v", r)
	}
	if r := call(ivan, "timeman.networkrange.check", `{"IP": "10.0.0.1"}`, 200)["result"].(map[string]any); r["range_name"] != false {
		t.Fatalf("check out: %v", r)
	}
	call(ivan, "timeman.networkrange.check", `{"IP": "x"}`, 400)
	call(ivan, "timeman.schedule.get", `{"id": 99}`, 400)
}
