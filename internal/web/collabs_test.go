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
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCollabsHTTP: collabs over HTTP — pages and REST API. Above all: an employee outside a collab, a member of
// another collab and a guest of another collab learn nothing of it — not its page, tasks, kanban, drive, calendar,
// members, avatar, not by search and not through the API.
func TestCollabsHTTP(t *testing.T) {
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
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@t', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, client := newUser("owner", false), newUser("client", true)
	outsider, other, otherGuest := newUser("outsider", false), newUser("other", false), newUser("otherguest", true)

	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{owner, client, outsider, other, otherGuest} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}

	// the owner makes a collab with the guest in it; it opens on its page
	st, _, loc := do(owner, "POST", "/collabs", url.Values{"name": {"Сайт для Альфы"}, "description": {"Редизайн"}, "members": {client}})
	if st != http.StatusSeeOther || loc != "/p/CL1" {
		t.Fatalf("create: %d %s", st, loc)
	}
	c, err := ts.CollabByKey(ctx, owner, "CL1")
	must(err)
	_, err = ts.SetProjectPhoto(ctx, c, "go24/projects/x.png", "image/png")
	must(err)
	tkey, err := ts.CreateTask(ctx, owner, c, tasks.NewTask{Title: "Макет главной страницы", ResponsibleID: client})
	must(err)
	day := time.Now().Add(24 * time.Hour)
	_, err = srv.Calendar.Save(ctx, owner, calendar.Event{Title: "Созвон с Альфой", Start: day, End: day.Add(time.Hour), ProjectID: c.ID}, []string{client})
	must(err)
	_, err = ts.CreateCollab(ctx, other, "Подрядчик Бета", "", []string{otherGuest})
	must(err)

	// members see it all
	for _, u := range []string{owner, client} {
		for _, p := range []string{"/collabs", "/p/CL1", "/p/CL1/board", "/disk/p/CL1", "/calendar?project=CL1", "/t/" + tkey} {
			if st, body, _ := do(u, "GET", p, nil); st != http.StatusOK || !strings.Contains(body, "Альф") && !strings.Contains(body, "Макет") {
				t.Errorf("a member's %s: %d", p, st)
			}
		}
	}
	// the counters (GO24DEV-404): the guest's overdue task on the card and in the menu; the card leads to exactly it
	past := time.Now().Add(-48 * time.Hour)
	_, err = ts.CreateTask(ctx, owner, c, tasks.NewTask{Title: "Просроченный бриф", ResponsibleID: client, Deadline: &past})
	must(err)
	if _, body, _ := do(client, "GET", "/collabs", nil); !strings.Contains(body, `/my?view=overdue&amp;project=CL1`) ||
		!strings.Contains(body, `data-counter="collabs" data-base="1"`) {
		t.Error("no overdue counter on the collab's card or in the menu")
	}
	if _, body, _ := do(owner, "GET", "/my", nil); !strings.Contains(body, `data-counter="collabs" data-base="0"`) {
		t.Error("the owner's collabs counter")
	}
	if st, body, _ := do(client, "GET", "/my?view=overdue&project=CL1", nil); st != http.StatusOK || !strings.Contains(body, "Просроченный бриф") ||
		strings.Contains(body, "Макет главной страницы") {
		t.Errorf("the overdue tasks of the collab: %d", st)
	}
	if st, _, _ := do(outsider, "GET", "/my?view=overdue&project=CL1", nil); st != http.StatusNotFound {
		t.Errorf("an outsider's view of the collab's tasks: %d", st)
	}
	if _, body, _ := do(client, "GET", "/p/CL1", nil); !strings.Contains(body, "pill guest") || !strings.Contains(body, "/p/CL1/leave") {
		t.Error("the collab page shows no guest mark or no «leave»")
	}
	// the people fields of its task offer the collab's members only (GO24DEV-209)
	if _, body, _ := do(owner, "GET", "/t/"+tkey, nil); !strings.Contains(body, `data-collab="CL1"`) {
		t.Error("the people pickers of a collab's task are not limited to the collab")
	}
	if _, body, _ := do(owner, "GET", "/people?collab=CL1", nil); strings.Contains(body, outsider) || !strings.Contains(body, client) {
		t.Errorf("people of the collab: %s", body)
	}
	if _, body, _ := do(owner, "GET", "/projects", nil); strings.Contains(body, "Сайт для Альфы") {
		t.Error("the collab is among the projects")
	}

	// nobody else: not found (not even «forbidden» — the collab does not exist for them)
	strangers := map[string]string{"outsider": outsider, "member of another collab": other, "guest of another collab": otherGuest}
	for name, u := range strangers {
		for _, p := range []string{"/p/CL1", "/p/CL1/board", "/p/CL1/gantt", "/disk/p/CL1", "/calendar?project=CL1", "/t/" + tkey, "/p/CL1/photo"} {
			if st, _, _ := do(u, "GET", p, nil); st != http.StatusNotFound {
				t.Errorf("%s: GET %s = %d, want 404", name, p, st)
			}
		}
		_, body, _ := do(u, "GET", "/collabs", nil)
		if strings.Contains(body, "Альф") || strings.Contains(body, "CL1") {
			t.Errorf("%s sees the collab in the list", name)
		}
		for _, p := range []string{"/calendar", "/my", "/tasks", "/projects", "/search?q=" + url.QueryEscape("макет")} {
			if _, body, _ := do(u, "GET", p, nil); strings.Contains(body, "Альф") || strings.Contains(body, "Макет главной") {
				t.Errorf("%s sees the collab on %s", name, p)
			}
		}
		if st, _, loc := do(u, "GET", "/search?q="+tkey, nil); st == http.StatusFound || strings.Contains(loc, tkey) {
			t.Errorf("%s: a search by the key led to the task", name)
		}
		// nor can they change anything in it
		for _, p := range []string{"/p/CL1/members", "/p/CL1/tasks", "/p/CL1/info", "/p/CL1/leave", "/disk/p/CL1/folders"} {
			if st, _, _ := do(u, "POST", p, url.Values{"user_id": {u}, "role": {"member"}, "title": {"x"}, "name": {"x"}}); st != http.StatusNotFound {
				t.Errorf("%s: POST %s = %d, want 404", name, p, st)
			}
		}
	}
	if ms, _ := ts.Members(ctx, c.ID); len(ms) != 2 {
		t.Fatalf("members after the strangers tried: %+v", ms)
	}
	// a guest makes no collab
	if st, _, _ := do(client, "POST", "/collabs", url.Values{"name": {"x"}}); st != http.StatusNotFound {
		t.Errorf("a guest made a collab: %d", st)
	}

	// REST API
	token := func(u string, scopes ...string) string {
		k, err := keys.Create(ctx, u, "k", scopes, time.Hour)
		must(err)
		return k
	}
	api := func(tok, method, path string, body any, want int) map[string]any {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		out := map[string]any{}
		_ = json.Unmarshal(raw, &out)
		if strings.HasPrefix(string(raw), "[") {
			var list []any
			_ = json.Unmarshal(raw, &list)
			out["list"] = list
		}
		return out
	}
	full := []string{"collabs:read", "collabs:write", "projects:read", "tasks:read"}
	ot := token(owner, full...)
	api(token(owner, "tasks:read"), "GET", "/api/v1/collabs", nil, http.StatusForbidden)
	got := api(ot, "POST", "/api/v1/collabs", map[string]any{"name": "Через API", "members": []string{outsider}}, http.StatusCreated)
	if col := got["collab"].(map[string]any); col["key"] != "CL3" || col["members"].(float64) != 2 {
		t.Fatalf("created over the API: %v", got)
	}
	got = api(ot, "GET", "/api/v1/collabs/CL1", nil, http.StatusOK)
	if col := got["collab"].(map[string]any); col["guests"].(float64) != 1 || len(got["members"].([]any)) != 2 {
		t.Fatalf("the collab over the API: %v", got)
	}
	api(ot, "POST", "/api/v1/collabs/CL1/members", map[string]any{"user_id": otherGuest, "role": "manager"}, http.StatusUnprocessableEntity)
	for name, u := range strangers {
		tok := token(u, full...)
		for _, p := range []string{"/api/v1/collabs/CL1", "/api/v1/projects/CL1", "/api/v1/projects/CL1/tasks", "/api/v1/tasks/" + tkey} {
			api(tok, "GET", p, nil, http.StatusNotFound)
		}
		api(tok, "POST", "/api/v1/collabs/CL1/members", map[string]any{"user_id": u}, http.StatusNotFound)
		api(tok, "DELETE", "/api/v1/collabs/CL1/members/"+client, nil, http.StatusNotFound)
		for _, l := range [][]any{api(tok, "GET", "/api/v1/collabs", nil, http.StatusOK)["list"].([]any), api(tok, "GET", "/api/v1/projects", nil, http.StatusOK)["list"].([]any)} {
			for _, x := range l {
				if x.(map[string]any)["key"] == "CL1" {
					t.Errorf("%s lists the collab over the API", name)
				}
			}
		}
		if list := api(tok, "GET", "/api/v1/tasks", nil, http.StatusOK)["list"]; list != nil {
			for _, x := range list.([]any) {
				if x.(map[string]any)["key"] == tkey {
					t.Errorf("%s lists the collab's task over the API", name)
				}
			}
		}
	}
	// the guest leaves over the API: the collab is gone for them
	ct := token(client, full...)
	api(ct, "DELETE", "/api/v1/collabs/CL1/members/"+client, nil, http.StatusNoContent)
	api(ct, "GET", "/api/v1/collabs/CL1", nil, http.StatusNotFound)
	if st, _, _ := do(client, "GET", "/t/"+tkey, nil); st != http.StatusNotFound {
		t.Errorf("the left guest opens the collab's task: %d", st)
	}

	// «Передать права владельца»: the owner sees the form; a stranger gets nothing; the API hands CL3 over
	if _, body, _ := do(owner, "GET", "/p/CL3", nil); !strings.Contains(body, `action="/p/CL3/owner"`) {
		t.Error("the owner has no «Передать права владельца»")
	}
	if _, body, _ := do(outsider, "GET", "/p/CL3", nil); strings.Contains(body, `action="/p/CL3/owner"`) {
		t.Error("a member is offered to hand the collab over")
	}
	api(token(other, full...), "POST", "/api/v1/collabs/CL3/owner", map[string]any{"user_id": other}, http.StatusNotFound)
	api(ot, "POST", "/api/v1/collabs/CL3/owner", map[string]any{"user_id": outsider}, http.StatusOK)
	if st, _, _ := do(owner, "POST", "/p/CL3/owner", url.Values{"user_id": {owner}}); st != http.StatusForbidden {
		t.Errorf("the former owner takes it back: %d", st)
	}
	var ownerID string
	must(pool.QueryRow(ctx, `SELECT owner_id::text FROM projects WHERE key = 'CL3'`).Scan(&ownerID))
	if ownerID != outsider {
		t.Error("the collab is not handed over")
	}
}

// TestCollabLogLine: a line of the collab's log as the bot tells it in the chat — worded as on the page, the
// settings in words, an invitation without its address.
func TestCollabLogLine(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		e    tasks.CollabLogEntry
		want string
	}{
		{tasks.CollabLogEntry{Actor: "Иван", Target: "Анна", TargetGuest: true, Action: "member_added", Detail: "member"},
			"Иван: добавлен участник Анна (гость) — Участник"},
		{tasks.CollabLogEntry{Actor: "Анна", Target: "Анна", TargetGuest: true, Action: "joined", Detail: "link"},
			"Анна: вступил(а) в коллаб Анна (гость) — по ссылке"},
		{tasks.CollabLogEntry{Actor: "Иван", Action: "settings", Detail: "chat_history=off,events_members=off"},
			"Иван: изменены настройки: история чата скрыта от новых участников; в чате нет событий участников"},
		{tasks.CollabLogEntry{Actor: "Иван", Action: "invite_created", Detail: "email anna@x.ru"}, "Иван: создано приглашение"},
		{tasks.CollabLogEntry{Actor: "Иван", Action: "info", Detail: "Клиент"}, "Иван: изменены название или описание — Клиент"},
	} {
		if got := CollabLogLine(b, "ru-RU", c.e); got != c.want {
			t.Errorf("%s: %q, want %q", c.e.Action, got, c.want)
		}
	}
}
