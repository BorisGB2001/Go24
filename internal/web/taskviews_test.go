package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestTaskViewsHTTP: opening the card counts a view; the card shows «Просмотры» with who saw the task, the API
// gives the count with the task and the list at …/views.
func TestTaskViewsHTTP(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna := newUser("Борис"), newUser("Анна")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "AA", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "AA")
	must(err)
	key, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Смета", ResponsibleID: anna})
	must(err)
	keys := apikeys.NewStore(pool)
	token, err := keys.Create(ctx, boss, "ro", []string{"tasks:read"}, time.Hour)
	must(err)

	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	get := func(who, path string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		if who == "" {
			req.Header.Set("Authorization", "Bearer "+token)
		} else {
			rec := httptest.NewRecorder()
			must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
			req.AddCookie(rec.Result().Cookies()[0])
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, res.StatusCode, b)
		}
		return string(b)
	}

	get(anna, "/t/"+key)
	page := get(boss, "/t/"+key)
	for _, w := range []string{"Просмотры", "Кто смотрел задачу", `href="/u/` + anna + `"`, `href="/u/` + boss + `"`} {
		if !strings.Contains(page, w) {
			t.Fatalf("the card lacks %q", w)
		}
	}
	var card struct {
		Views int `json:"views"`
	}
	must(json.Unmarshal([]byte(get("", "/api/v1/tasks/"+key)), &card))
	if card.Views != 2 {
		t.Fatalf("views = %d", card.Views)
	}
	var list []struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Times int `json:"times"`
	}
	must(json.Unmarshal([]byte(get("", "/api/v1/tasks/"+key+"/views")), &list))
	if len(list) != 2 || list[0].User.ID != boss || list[1].User.ID != anna || list[1].Times != 1 {
		t.Fatalf("views list: %+v", list)
	}
}
