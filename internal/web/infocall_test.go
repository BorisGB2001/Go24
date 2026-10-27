package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestInfoCall: the CRM robot «Информационный звонок» — added with a line and a WAV message (a bad file is refused),
// listened to in the settings, written for Asterisk by the sync; a lead entering the stage is called through the line
// (go24-info plays the message), the history says so; a lead without a phone is the robot's warning.
func TestInfoCall(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('boss', 'Босс', 'boss@go24.test', true) RETURNING id`).Scan(&boss))
	store := telephony.NewStore(pool, "secret")
	must(store.SetEnabled(ctx, boss, true))
	line, err := store.SaveTrunk(ctx, boss, telephony.Trunk{Name: "Москва", Host: "sip.example.org", Username: "login", Password: "pw",
		Number: "+74950000000", RingMode: "all", OutAll: true})
	must(err)

	type origin struct {
		q    url.Values
		body string
	}
	got := make(chan origin, 4)
	ast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/ari/channels" {
			b, _ := io.ReadAll(r.Body)
			got <- origin{r.URL.Query(), string(b)}
		}
		io.WriteString(w, `{}`)
	}))
	defer ast.Close()
	bundle, err := i18n.Load("")
	must(err)
	dir := t.TempDir()
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), I18n: bundle,
		Telephony: &telephony.PBX{Store: store, Dir: dir, ARI: &telephony.ARI{URL: ast.URL, User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.CRM.InfoCall = srv.Telephony.InfoCall
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	stages, err := srv.CRM.LeadStages(ctx)
	must(err)
	wav := telephony.Beep() // any WAV Asterisk plays
	add := func(sound []byte) string {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for k, v := range map[string]string{"stage": stages[1].ID, "kind": "infocall", "line": line, "back": "/crm/robots?entity=lead"} {
			mw.WriteField(k, v)
		}
		fw, _ := mw.CreateFormFile("sound", "hello.wav")
		fw.Write(sound)
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/crm/robots", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(cookie)
		res, err := client.Do(req)
		must(err)
		res.Body.Close()
		if res.StatusCode != http.StatusSeeOther {
			t.Fatalf("add robot: %d", res.StatusCode)
		}
		return res.Header.Get("Location")
	}
	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(cookie)
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if loc := add([]byte("not a wav")); !strings.Contains(loc, "msg=") {
		t.Fatalf("a bad file is taken: %s", loc)
	}
	if loc := add(wav); strings.Contains(loc, "msg=") {
		t.Fatalf("robot not added: %s", loc)
	}
	var sum string
	must(pool.QueryRow(ctx, `SELECT params->>'sound' FROM crm_robots WHERE kind = 'infocall'`).Scan(&sum))
	if code, page := get("/crm/robots?entity=lead"); code != 200 || !strings.Contains(page, "/telephony/sounds/"+sum) || !strings.Contains(page, "«Москва»") {
		t.Fatalf("robots page: %d %s", code, page)
	}
	kept, err := telephony.Greeting(wav)
	must(err)
	if code, b := get("/telephony/sounds/" + sum); code != 200 || b != string(kept) {
		t.Fatalf("sound: %d", code)
	}
	// an old sound no robot plays is dropped by the sync; the robot's one is written for Asterisk
	_, err = pool.Exec(ctx, `INSERT INTO telephony_sounds (sum, data, created_at) VALUES ('old', 'x', now() - interval '2 hours')`)
	must(err)
	must(srv.Telephony.Sync(ctx))
	if _, err := os.Stat(filepath.Join(dir, "greet-"+sum+".wav")); err != nil {
		t.Fatal(err)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM telephony_sounds`).Scan(&n))
	if n != 1 {
		t.Fatalf("sounds kept: %d", n)
	}

	history := func(id string) string {
		var b string
		must(pool.QueryRow(ctx, `SELECT coalesce(string_agg(body, '|'), '') FROM crm_timeline WHERE entity_id::text = $1 AND kind = 'robot'`, id).Scan(&b))
		return b
	}
	lead, err := srv.CRM.SaveLead(ctx, boss, crm.Lead{Title: "Звонок", Phone: "+7 (900) 111-22-33", Responsible: crm.Ref{ID: boss}})
	must(err)
	_, err = srv.CRM.MoveLead(ctx, boss, lead, stages[1].ID)
	must(err)
	o := <-got
	if o.q.Get("endpoint") != "PJSIP/+79001112233@trunk-"+strings.ReplaceAll(line, "-", "")[:12] || o.q.Get("context") != "go24-info" ||
		o.q.Get("callerId") != "+74950000000 <+74950000000>" {
		t.Fatalf("origin: %v", o.q)
	}
	var body struct{ Variables map[string]string }
	must(json.Unmarshal([]byte(o.body), &body))
	if body.Variables["GO24_SOUND"] != telephony.GreetingFile(sum) || body.Variables["GO24_TO"] != "+79001112233" || body.Variables["GO24_LINE"] != "+74950000000" {
		t.Fatalf("variables: %v", body.Variables)
	}
	if h := history(lead); h != "infocall:+7 (900) 111-22-33" {
		t.Fatalf("history: %q", h)
	}
	silent, err := srv.CRM.SaveLead(ctx, boss, crm.Lead{Title: "Без телефона", Responsible: crm.Ref{ID: boss}})
	must(err)
	_, err = srv.CRM.MoveLead(ctx, boss, silent, stages[1].ID)
	must(err)
	if h := history(silent); h != "fail:infocall:err.robot_phone" {
		t.Fatalf("history without a phone: %q", h)
	}
	select {
	case o := <-got:
		t.Fatalf("called without a phone: %v", o.q)
	default:
	}
}
