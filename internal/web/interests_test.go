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
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestInterests: «Интересы» of the profile — the person adds (several at once, the same in another case once) and
// removes their own, an administrator — anyone's, nobody else; the popular ones are offered to add without the person's
// own; an interest leads to «Сотрудники» with it, the search finds by it; REST interests, ?interest=, PUT and
// /api/v1/interests with the same rights.
func TestInterests(t *testing.T) {
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
	admin, ann, bob, fired := newUser("Админов"), newUser("Анна"), newUser("Борис"), newUser("Уволенов")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	cs := company.NewStore(pool)
	keys := apikeys.NewStore(pool)
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	adminKey, bobKey := key(admin), key(bob)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Company: cs, Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, ann, bob} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u, Admin: u == admin}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
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
		return res.StatusCode, string(b)
	}
	api := func(k, method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	interests := func(uid string) string {
		t.Helper()
		list, err := cs.Interests(ctx, uid)
		must(err)
		return strings.Join(list, "|")
	}

	// the person adds several at once, the same in another case once; another employee may not
	if st, _ := do(ann, "POST", "/u/"+ann+"/interests", url.Values{"op": {"add"}, "tag": {"#Фотография #Футбол, футбол"}}); st != http.StatusSeeOther {
		t.Fatalf("add: %d", st)
	}
	if got := interests(ann); got != "Фотография|Футбол" {
		t.Fatalf("Анна: %q", got)
	}
	if st, _ := do(bob, "POST", "/u/"+ann+"/interests", url.Values{"op": {"add"}, "tag": {"спам"}}); st != http.StatusForbidden {
		t.Fatalf("another employee adds: %d", st)
	}
	do(ann, "POST", "/u/"+ann+"/interests", url.Values{"op": {"remove"}, "tag": {"фотография"}})
	if got := interests(ann); got != "Футбол" {
		t.Fatalf("after removing: %q", got)
	}
	do(admin, "POST", "/u/"+bob+"/interests", url.Values{"op": {"add"}, "tag": {"футбол, шахматы"}})
	if got := interests(bob); got != "футбол|шахматы" {
		t.Fatalf("Борис by the administrator: %q", got)
	}
	must(cs.SetInterests(ctx, fired, []string{"Футбол", "Рыбалка"}))
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, fired)
	must(err)

	// the popular: the active employees only, one spelling, the most common first
	pop, err := cs.PopularInterests(ctx, "", 10)
	must(err)
	if len(pop) != 2 || !strings.EqualFold(pop[0].Tag, "футбол") || pop[0].Count != 2 || pop[1].Tag != "шахматы" {
		t.Fatalf("popular: %+v", pop)
	}
	if pop, _ := cs.PopularInterests(ctx, "шах", 10); len(pop) != 1 {
		t.Fatalf("by the prefix: %+v", pop)
	}

	// the profile: the chips lead to the list, the person's own are not offered again
	_, body := do(ann, "GET", "/u/"+ann, nil)
	if !strings.Contains(body, `id="pinterests"`) || !strings.Contains(body, `/company?interest=%d0%a4%d1%83%d1%82%d0%b1%d0%be%d0%bb`) {
		t.Fatal("the profile has no interests linked to the list")
	}
	if !strings.Contains(body, `value="шахматы"`) || strings.Count(body, `class="tag ghost"`) != 1 {
		t.Fatal("the popular ones offered wrong")
	}
	if _, body := do(bob, "GET", "/u/"+ann, nil); strings.Contains(body, `action="/u/`+ann+`/interests"`) {
		t.Fatal("another employee is given the form")
	}

	// «Сотрудники» with the interest; the search finds by it
	_, body = do(bob, "GET", "/company?interest=ФУТБОЛ", nil)
	if !strings.Contains(body, "Анна") || !strings.Contains(body, "Борис") || strings.Contains(body, "Админов") || !strings.Contains(body, "#ФУТБОЛ ×") {
		t.Fatal("the list by the interest")
	}
	if _, body := do(bob, "GET", "/company?q=шахм", nil); !strings.Contains(body, "Борис") || strings.Contains(body, "Анна") {
		t.Fatal("the search by the interest")
	}

	// REST
	st, out := api(bobKey, "GET", "/api/v1/users?interest=шахматы", "")
	var users []struct {
		ID        string   `json:"id"`
		Interests []string `json:"interests"`
	}
	must(json.Unmarshal([]byte(out), &users))
	if st != http.StatusOK || len(users) != 1 || users[0].ID != bob || strings.Join(users[0].Interests, "|") != "футбол|шахматы" {
		t.Fatalf("GET users?interest: %d %s", st, out)
	}
	if st, _ := api(bobKey, "PUT", "/api/v1/users/"+ann+"/interests", `{"interests":["x"]}`); st != http.StatusForbidden {
		t.Fatalf("PUT another's: %d", st)
	}
	if st, out := api(bobKey, "PUT", "/api/v1/users/me/interests", `{"interests":["Кино", "#кино", "Шахматы"]}`); st != http.StatusOK ||
		!strings.Contains(strings.Join(strings.Fields(out), ""), `"interests":["Кино","Шахматы"]`) {
		t.Fatalf("PUT own: %d %s", st, out)
	}
	if st, _ := api(adminKey, "PUT", "/api/v1/users/"+ann+"/interests", `{"interests":[]}`); st != http.StatusOK || interests(ann) != "" {
		t.Fatalf("PUT by the administrator: %d %q", st, interests(ann))
	}
	if st, out := api(bobKey, "GET", "/api/v1/interests?q=ки", ""); st != http.StatusOK || strings.Join(strings.Fields(out), "") != `[{"tag":"Кино","count":1}]` {
		t.Fatalf("GET interests: %d %q", st, out)
	}
}
