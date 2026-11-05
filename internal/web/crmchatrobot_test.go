package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/openlines"
	"go24/internal/tasks"
)

// TestCRMChatRobotHTTP: the robot «Отправить сообщение в открытую линию» — the stage's form offers
// it with «От имени», the text with its placeholders goes into the lead's latest dialogue of the open lines from the
// chosen employee (who takes the dialogue), the timeline says so; a lead with no dialogue gets the robot's failure.
func TestCRMChatRobotHTTP(t *testing.T) {
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
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), I18n: bundle, BaseURL: "https://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.OpenLines = openlines.NewStore(pool, srv.CRM)
	srv.OpenLines.Tr = func(key string, args ...any) string { return key }
	srv.CRM.ClientChat = srv.CRMClientChat

	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Name: "Олег", Responsible: crm.Ref{ID: anna}})
	must(err)
	lonely, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Без чата", Name: "Пётр", Responsible: crm.Ref{ID: anna}})
	must(err)
	wid, err := srv.OpenLines.SaveWidget(ctx, anna, openlines.Widget{Name: "Линия", Active: true})
	must(err)
	sid, _, err := srv.OpenLines.Visit(ctx, wid, "", "")
	must(err)
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET lead_id = $2 WHERE id = $1`, sid, lead)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie)
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}

	stages, err := srv.CRM.LeadStages(ctx)
	must(err)
	if code, body, _ := do("GET", "/crm/robots?entity=lead", nil); code != http.StatusOK || !strings.Contains(body, `value="chat"`) ||
		!strings.Contains(body, bundle.T("ru-RU", "robots.chat_from")) {
		t.Fatalf("the form offers no chat robot: %d", code)
	}
	if _, _, loc := do("POST", "/crm/robots", url.Values{"stage": {stages[1].ID}, "kind": {"chat"}, "to": {bob},
		"text": {"Здравствуйте, {client}! Заявка «{title}» в работе"}, "back": {"/crm/robots"}}); strings.Contains(loc, "msg=") {
		t.Fatalf("the robot is not added: %s", loc)
	}
	if _, _, loc := do("POST", "/crm/robots", url.Values{"stage": {stages[1].ID}, "kind": {"chat"}, "text": {""}, "back": {"/crm/robots"}}); !strings.Contains(loc, "msg=") {
		t.Fatal("a robot with no text is added")
	}

	_, err = srv.CRM.MoveLead(ctx, anna, lead, stages[1].ID)
	must(err)
	var body, author, operator string
	must(pool.QueryRow(ctx, `SELECT m.body, m.author_id::text, s.operator_id::text FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id
		WHERE m.session_id = $1 AND m.author_id IS NOT NULL ORDER BY m.id DESC LIMIT 1`, sid).Scan(&body, &author, &operator))
	if !strings.Contains(body, "Заявка «Заявка» в работе") || strings.Contains(body, "{") || author != bob || operator != bob {
		t.Fatalf("the robot's message: %q from %s, the dialogue's operator %s", body, author, operator)
	}
	if _, page, _ := do("GET", "/crm/leads/"+lead, nil); !strings.Contains(page, bundle.T("ru-RU", "robots.did.chat")) {
		t.Fatal("the timeline does not say the robot wrote")
	}

	_, err = srv.CRM.MoveLead(ctx, anna, lonely, stages[1].ID)
	must(err)
	var fails int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'robot' AND body = 'fail:chat:err.robot_chat'`, lonely).Scan(&fails))
	if fails != 1 {
		t.Fatal("no failure of the robot with no dialogue")
	}
}
