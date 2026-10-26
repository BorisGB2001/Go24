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
)

// TestHonorBoard: «Доска почёта» — an administrator and an HR officer with the right put employees on the board, the
// others neither see the form nor may post it; the employee is notified, their profile shows the mark; a place whose
// days are over goes to the archive, one to come shows as «скоро»; the department filter takes sub-departments;
// taking off; REST /api/v1/honors with the same rights.
func TestHonorBoard(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.FixedZone("UTC+2", 2*3600) // dates from Postgres are UTC midnights: «today» must stay today
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ($1, $1, $1 || '@t', 'Менеджер') RETURNING id`,
			name).Scan(&id))
		return id
	}
	admin, hr, plain, star := newUser("Админов"), newUser("Кадрова"), newUser("Простов"), newUser("Лучшева")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	root, _, err := cs.Tree(ctx)
	must(err)
	sales, err := cs.CreateDepartment(ctx, root.ID, "Продажи", "")
	must(err)
	b2b, err := cs.CreateDepartment(ctx, sales, "Корпоративные", "")
	must(err)
	other, err := cs.CreateDepartment(ctx, root.ID, "Склад", "")
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, star, b2b, plain, other)
	must(err)
	keys := apikeys.NewStore(pool)
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	adminKey, plainKey := key(admin), key(plain)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Company: cs, Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, hr, plain, star} {
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
	day := func(d int) string { return time.Now().AddDate(0, 0, d).Format(time.DateOnly) }

	// who keeps the board: an administrator; an employee neither sees the form nor may post
	if _, body := do(admin, "GET", "/company/honor", nil); !strings.Contains(body, `method="post" action="/company/honor"`) {
		t.Fatal("the administrator has no form")
	}
	if st, body := do(plain, "GET", "/company/honor", nil); st != http.StatusOK || strings.Contains(body, `method="post" action="/company/honor"`) {
		t.Fatalf("an employee: %d, the form shown = %v", st, strings.Contains(body, `method="post" action="/company/honor"`))
	}
	if st, _ := do(plain, "POST", "/company/honor", url.Values{"user": {plain}, "from": {day(0)}}); st != http.StatusForbidden {
		t.Fatalf("an employee puts themselves: %d", st)
	}
	if st, _ := do(hr, "POST", "/company/honor", url.Values{"user": {star}, "from": {day(0)}}); st != http.StatusForbidden {
		t.Fatalf("an HR officer without the right: %d", st)
	}
	must(cs.SetHR(ctx, hr, true, []string{"honor"}))
	if st, _ := do(hr, "POST", "/company/honor", url.Values{"user": {plain}, "reason": {"Порядок на складе"}, "from": {day(-30)}, "to": {day(-1)}}); st != http.StatusSeeOther {
		t.Fatalf("the HR officer with the right: %d", st)
	}
	// dates: the end before the start, a guest, nobody
	do(admin, "POST", "/company/honor", url.Values{"user": {star}, "from": {day(0)}, "to": {day(-1)}})
	if st, _ := do(admin, "POST", "/company/honor", url.Values{"user": {"00000000-0000-0000-0000-000000000000"}, "from": {day(0)}}); st != http.StatusNotFound {
		t.Errorf("nobody put on the board: %d", st)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM honors`).Scan(&n))
	if n != 1 {
		t.Fatalf("the end before the start was taken: %d places", n)
	}
	if st, _ := do(admin, "POST", "/company/honor", url.Values{"user": {star}, "reason": {"Лучший продавец квартала"}, "from": {day(0)}}); st != http.StatusSeeOther {
		t.Fatalf("the administrator puts: %d", st)
	}

	// the board now, the archive, the department with sub-departments
	_, body := do(plain, "GET", "/company/honor", nil)
	if !strings.Contains(body, "Лучший продавец квартала") || strings.Contains(body, "Порядок на складе") {
		t.Error("the board now: want the current place only")
	}
	if strings.Contains(body, "honortile coming") {
		t.Error("today's place is marked «скоро»")
	}
	if !strings.Contains(body, "Корпоративные") {
		t.Error("the department is not shown")
	}
	if _, body := do(plain, "GET", "/company/honor?archive=1", nil); !strings.Contains(body, "Порядок на складе") || strings.Contains(body, "Лучший продавец") {
		t.Error("the archive: want the ended place only")
	}
	if _, body := do(plain, "GET", "/company/honor?dept="+sales, nil); !strings.Contains(body, "Лучший продавец") {
		t.Error("«Продажи» lost the sub-department's place")
	}
	if _, body := do(plain, "GET", "/company/honor?dept="+other, nil); strings.Contains(body, "Лучший продавец") {
		t.Error("«Склад» shows another department's place")
	}

	// the employee is told; the profile shows the mark, and the button to the keepers of the board
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'honor' AND body = 'Лучший продавец квартала'`, star).Scan(&n))
	if n != 1 {
		t.Errorf("notifications to the employee: %d", n)
	}
	if _, body := do(plain, "GET", "/u/"+star, nil); !strings.Contains(body, "honorpill") || strings.Contains(body, "/company/honor?add=") {
		t.Error("the profile: want the mark and no button for an employee")
	}
	if _, body := do(admin, "GET", "/u/"+plain, nil); strings.Contains(body, "honorpill") || !strings.Contains(body, "/company/honor?add="+plain) {
		t.Error("the profile of one off the board: want the button for the administrator and no mark")
	}
	if _, body := do(admin, "GET", "/company/honor?add="+plain, nil); !strings.Contains(body, `<option value="`+plain+`" selected>`) {
		t.Error("?add= does not pick the person")
	}

	// one to come
	if st, _ := do(admin, "POST", "/company/honor", url.Values{"user": {plain}, "reason": {"Скоро"}, "from": {day(3)}}); st != http.StatusSeeOther {
		t.Fatalf("a place to come: %d", st)
	}
	if _, body := do(plain, "GET", "/company/honor", nil); !strings.Contains(body, "honortile coming") {
		t.Error("a place to come is not marked")
	}
	if _, body := do(admin, "GET", "/u/"+plain, nil); strings.Contains(body, "honorpill") {
		t.Error("a place to come marks the profile already")
	}

	// REST: the board, putting on (an employee — 403), taking off
	st, out := api(plainKey, "GET", "/api/v1/honors", "")
	var list []apiHonorOut
	must(json.Unmarshal([]byte(out), &list))
	if st != http.StatusOK || len(list) != 2 || list[0].UserID != star || list[0].Departments[0] != "Корпоративные" || list[0].To != "" {
		t.Fatalf("GET /honors: %d %s", st, out)
	}
	if st, _ := api(plainKey, "POST", "/api/v1/honors", `{"user_id":"`+plain+`"}`); st != http.StatusForbidden {
		t.Errorf("an employee puts through REST: %d", st)
	}
	st, out = api(adminKey, "POST", "/api/v1/honors", `{"user_id":"`+star+`","reason":"Год","from":"`+day(-400)+`","to":"`+day(-35)+`"}`)
	var h apiHonorOut
	must(json.Unmarshal([]byte(out), &h))
	if st != http.StatusCreated || h.Reason != "Год" || h.By.ID != admin {
		t.Fatalf("POST /honors: %d %s", st, out)
	}
	if _, out := api(plainKey, "GET", "/api/v1/honors?archive=1&user_id="+star, ""); !strings.Contains(out, `"Год"`) {
		t.Errorf("the archive through REST: %s", out)
	}
	if st, _ := api(plainKey, "DELETE", "/api/v1/honors/"+strconv.FormatInt(h.ID, 10), ""); st != http.StatusForbidden {
		t.Errorf("an employee takes off: %d", st)
	}
	if st, _ := api(adminKey, "DELETE", "/api/v1/honors/"+strconv.FormatInt(h.ID, 10), ""); st != http.StatusNoContent {
		t.Errorf("DELETE /honors: %d", st)
	}
	if st, _ := api(adminKey, "DELETE", "/api/v1/honors/"+strconv.FormatInt(h.ID, 10), ""); st != http.StatusNotFound {
		t.Errorf("DELETE again: %d", st)
	}

	// taking off on the page
	if st, _ := do(plain, "POST", "/company/honor/"+strconv.FormatInt(list[0].ID, 10)+"/delete", url.Values{}); st != http.StatusForbidden {
		t.Errorf("an employee takes off: %d", st)
	}
	if st, _ := do(admin, "POST", "/company/honor/"+strconv.FormatInt(list[0].ID, 10)+"/delete", url.Values{}); st != http.StatusSeeOther {
		t.Errorf("the administrator takes off: %d", st)
	}
	if _, body := do(plain, "GET", "/company/honor", nil); strings.Contains(body, "Лучший продавец") {
		t.Error("the place is still on the board")
	}
	// a dismissed employee leaves the board
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, plain)
	must(err)
	if _, body := do(admin, "GET", "/company/honor", nil); strings.Contains(body, "honortile") {
		t.Error("a dismissed employee is on the board")
	}
}

