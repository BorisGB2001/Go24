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
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/authentik"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestEmployeeInvites: «Пригласить сотрудников» (GO24DEV-676) — several people a line each into a
// department, a bad line named; «Пригласить повторно» makes a new Authentik link (the old one deleted), «Отменить»
// deletes it and the invitation cannot be sent again; the invited person signing in goes into the department and to
// «Кадровые изменения» as taken on by the inviter; REST /api/v1/users/invite(s) with the same rights.
func TestEmployeeInvites(t *testing.T) {
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
	admin, plain := newUser("Админов"), newUser("Простов")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	_, depts, err := cs.Tree(ctx)
	must(err)
	sales, err := cs.CreateDepartment(ctx, depts[0].ID, "Продажи", "")
	must(err)

	// Authentik: invitations made and deleted
	var mu sync.Mutex
	made, deleted := 0, []string{}
	ak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v3/flows/instances/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"pk": "flow-1"})
		case r.Method == "POST" && r.URL.Path == "/api/v3/stages/invitation/invitations/":
			made++
			_ = json.NewEncoder(w).Encode(map[string]any{"pk": "inv-" + string(rune('0'+made))})
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v3/stages/invitation/invitations/"):
			deleted = append(deleted, strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v3/stages/invitation/invitations/"), "/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ak.Close()

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: tasks.NewStore(pool), Company: cs, Keys: keys, I18n: bundle,
		Authentik: authentik.New(ak.URL, "tok"), AuthentikPublic: "https://sso.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: admin, Name: "Админов", Admin: true}))
	cookie := rec.Result().Cookies()[0]
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		return res.StatusCode, res.Header.Get("Location")
	}

	// a bad line is named, nobody is invited
	if st, loc := post("/company/invite", url.Values{"people": {"ivan@firm.test Иван Петров\nкто-то без почты"}}); st != http.StatusSeeOther ||
		!strings.Contains(loc, url.QueryEscape("Строка 2")) {
		t.Fatalf("a bad line: %d %s", st, loc)
	}
	// two people (the second as «Имя <e-mail>», the first twice), into «Продажи»
	people := "ivan@firm.test Иван Петров\nМария Сидорова <Maria@Firm.test>\nivan@firm.test Иван Петров"
	if st, loc := post("/company/invite", url.Values{"people": {people}, "department": {sales}}); st != http.StatusSeeOther ||
		!strings.Contains(loc, url.QueryEscape("Приглашено: 2")) {
		t.Fatalf("invite two: %d %s", st, loc)
	}
	list, err := cs.Invites(ctx)
	must(err)
	if len(list) != 2 || list[0].Department != "Продажи" || list[1].Department != "Продажи" || made != 2 {
		t.Fatalf("invites = %+v, made %d", list, made)
	}
	var maria, ivan company.Invite
	for _, i := range list {
		if i.Email == "maria@firm.test" {
			maria = i
		} else {
			ivan = i
		}
	}
	if maria.Name != "Мария Сидорова" || ivan.Name != "Иван Петров" || maria.State(time.Now()) != "waiting" {
		t.Fatalf("parsed: %+v / %+v", maria, ivan)
	}

	// again: the old link deleted, a new one
	if st, _ := post("/company/invite/"+maria.ID+"/resend", url.Values{}); st != http.StatusSeeOther {
		t.Fatalf("resend: %d", st)
	}
	again, err := cs.InviteByID(ctx, maria.ID)
	must(err)
	if again.AuthentikID == maria.AuthentikID || len(deleted) != 1 || deleted[0] != maria.AuthentikID || !strings.Contains(again.Link, again.AuthentikID) {
		t.Fatalf("resend: %+v, deleted %v", again, deleted)
	}
	// cancelled: deleted in Authentik, no more resending
	if st, _ := post("/company/invite/"+ivan.ID+"/cancel", url.Values{}); st != http.StatusSeeOther {
		t.Fatalf("cancel: %d", st)
	}
	ivan, _ = cs.InviteByID(ctx, ivan.ID)
	if ivan.State(time.Now()) != "revoked" || len(deleted) != 2 {
		t.Fatalf("cancelled: %s, deleted %v", ivan.State(time.Now()), deleted)
	}
	if st, loc := post("/company/invite/"+ivan.ID+"/resend", url.Values{}); st != http.StatusSeeOther || !strings.Contains(loc, "msg=") || made != 3 {
		t.Fatalf("a cancelled one sent again: %d %s, made %d", st, loc, made)
	}

	// Мария signs in: into «Продажи», taken on by the administrator
	uid, _, guest, err := srv.signIn(ctx, auth.Claims{Sub: "maria-sub", Email: "maria@firm.test", Name: "Мария Сидорова"}, "")
	must(err)
	if guest {
		t.Fatal("the invited employee became a guest")
	}
	var inSales bool
	must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_departments WHERE user_id = $1 AND department_id = $2)`, uid, sales).Scan(&inSales))
	changes, err := cs.StaffChanges(ctx, company.StaffFilter{Kind: "hired"})
	must(err)
	var by string
	for _, c := range changes {
		if c.ID == uid {
			by = c.ActorID
		}
	}
	if !inSales || by != admin {
		t.Fatalf("joined: in sales %v, hired by %q", inSales, by)
	}
	if again, _ = cs.InviteByID(ctx, maria.ID); again.State(time.Now()) != "joined" {
		t.Fatalf("state after joining: %s", again.State(time.Now()))
	}

	// the page: lists them with their state
	req, _ := http.NewRequest("GET", hs.URL+"/company/invite", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	must(err)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `name="people"`) || !strings.Contains(string(body), "отменено") ||
		!strings.Contains(string(body), `<option value="`+sales+`"`) {
		t.Fatalf("page: %d", res.StatusCode)
	}

	// REST: the administrator invites, lists, resends and cancels; an employee may not
	adminKey, err := keys.Create(ctx, admin, "k", []string{"company:read", "company:write"}, time.Hour)
	must(err)
	plainKey, err := keys.Create(ctx, plain, "k", []string{"company:read", "company:write"}, time.Hour)
	must(err)
	api := func(k, method, path, in string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(in))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	in := `{"people":[{"email":"petr@firm.test","name":"Пётр Иванов"}],"department_id":"` + sales + `"}`
	if st, _ := api(plainKey, "POST", "/api/v1/users/invite", in); st != http.StatusForbidden {
		t.Fatalf("an employee invites by REST: %d", st)
	}
	st, b := api(adminKey, "POST", "/api/v1/users/invite", in)
	var out struct{ Invites []apiInviteOut }
	_ = json.Unmarshal(b, &out)
	if st != http.StatusCreated || len(out.Invites) != 1 || out.Invites[0].Department != "Продажи" || out.Invites[0].State != "waiting" ||
		out.Invites[0].Link == "" {
		t.Fatalf("REST invite: %d %s", st, b)
	}
	petr := out.Invites[0].ID
	if st, b := api(adminKey, "POST", "/api/v1/users/invite", `{"people":[{"email":"maria@firm.test","name":"М"}]}`); st != http.StatusUnprocessableEntity {
		t.Fatalf("REST invites one already in go24: %d %s", st, b)
	}
	if st, b := api(adminKey, "GET", "/api/v1/users/invites", ""); st != http.StatusOK || !strings.Contains(string(b), `"state": "joined"`) {
		t.Fatalf("REST list: %d %s", st, b)
	}
	if st, b := api(adminKey, "POST", "/api/v1/users/invites/"+petr+"/resend", ""); st != http.StatusOK || !strings.Contains(string(b), `"state": "waiting"`) {
		t.Fatalf("REST resend: %d %s", st, b)
	}
	if st, _ := api(adminKey, "DELETE", "/api/v1/users/invites/"+petr, ""); st != http.StatusNoContent {
		t.Fatalf("REST cancel: %d", st)
	}
	if st, _ := api(adminKey, "DELETE", "/api/v1/users/invites/"+petr, ""); st == http.StatusNoContent {
		t.Fatal("REST cancels a cancelled one")
	}
}
