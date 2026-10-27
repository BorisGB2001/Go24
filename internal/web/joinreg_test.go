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
	"go24/internal/authentik"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

func TestStaffSettingsParts(t *testing.T) {
	d, err := company.CleanDomains("Firm.ru, @other.com\nfirm.ru")
	if err != nil || strings.Join(d, " ") != "firm.ru other.com" {
		t.Fatalf("domains = %v, %v", d, err)
	}
	if _, err := company.CleanDomains("firm"); err == nil {
		t.Fatal("a word without a dot is a domain")
	}
	set := company.StaffSettings{Register: true, Token: "abc", Domains: d}
	if !set.JoinOpen("abc") || set.JoinOpen("abd") || set.JoinOpen("") {
		t.Fatal("JoinOpen")
	}
	if !set.DomainAllowed("Ivan@FIRM.ru") || set.DomainAllowed("ivan@firm.ru.evil") || set.DomainAllowed("ivan@sub.firm.ru") {
		t.Fatal("DomainAllowed")
	}
	if !(company.StaffSettings{}).DomainAllowed("x@any.org") {
		t.Fatal("no domains — any")
	}
	now := time.Now()
	for i := 0; i < joinLimit; i++ {
		if !joinAllowed("192.0.2.7", now) {
			t.Fatalf("request %d refused", i+1)
		}
	}
	if joinAllowed("192.0.2.7", now) || !joinAllowed("192.0.2.8", now) || !joinAllowed("192.0.2.7", now.Add(time.Hour+time.Second)) {
		t.Fatal("joinAllowed: five an hour from an address")
	}
}

// TestStaffSettings: «Настройки → Сотрудники» (GO24DEV-802) — who invites (administrators and HR
// officers, or all employees); «Быстрая регистрация»: the link for anyone, only for the domains, requests waiting in
// «Пригласить сотрудников» (administrators notified), accepted into a department as an invitation or rejected; a new
// link closes the old one; REST /api/v1/settings/employees and /api/v1/users/join-requests.
func TestStaffSettings(t *testing.T) {
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

	made := 0
	ak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v3/flows/instances/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"pk": "flow-1"})
		case r.Method == "POST" && r.URL.Path == "/api/v3/stages/invitation/invitations/":
			made++
			_ = json.NewEncoder(w).Encode(map[string]any{"pk": "inv-" + string(rune('0'+made))})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ak.Close()

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: tasks.NewStore(pool), Company: cs, Keys: keys, I18n: bundle, BaseURL: "https://go24.test",
		Authentik: authentik.New(ak.URL, "tok"), AuthentikPublic: "https://sso.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(uid, name string, adm bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: uid, Name: name, Admin: adm}))
		return rec.Result().Cookies()[0]
	}
	adminCookie, plainCookie := cookieOf(admin, "Админов", true), cookieOf(plain, "Простов", false)
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(c *http.Cookie, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", "198.51.100.1")
		if c != nil {
			req.AddCookie(c)
		}
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}

	// only administrators invite by default; an employee may not change the settings
	if st, _, _ := do(plainCookie, "GET", "/company/invite", nil); st != http.StatusForbidden {
		t.Fatalf("an employee opens «Пригласить» by default: %d", st)
	}
	if st, _, _ := do(plainCookie, "POST", "/settings/employees", url.Values{"invite": {"all"}}); st != http.StatusForbidden {
		t.Fatalf("an employee saves «Сотрудники»: %d", st)
	}
	if st, loc, _ := do(adminCookie, "POST", "/settings/employees", url.Values{"domains": {"firm"}}); st != http.StatusSeeOther ||
		!strings.Contains(loc, "msg=") {
		t.Fatalf("a bad domain: %d %s", st, loc)
	}
	if st, loc, _ := do(adminCookie, "POST", "/settings/employees", url.Values{"invite": {"all"}, "register": {"1"}, "approve": {"1"},
		"domains": {"Firm.test, @other.test"}}); st != http.StatusSeeOther || loc != "/settings/employees?saved=1" {
		t.Fatalf("save: %d %s", st, loc)
	}
	set, err := cs.StaffSettings(ctx)
	must(err)
	if !set.InviteAll || !set.Register || !set.Approve || len(set.Token) < 20 || strings.Join(set.Domains, " ") != "firm.test other.test" || set.By != admin {
		t.Fatalf("settings = %+v", set)
	}
	if st, _, body := do(plainCookie, "GET", "/company/invite", nil); st != http.StatusOK || strings.Contains(body, "/settings/employees") {
		t.Fatalf("all invite: an employee's page %d", st)
	}
	if st, _, body := do(adminCookie, "GET", "/settings/employees", nil); st != http.StatusOK || !strings.Contains(body, "https://go24.test/register/"+set.Token) {
		t.Fatalf("settings page without the link: %d", st)
	}

	// the page of the link: a wrong token is closed; a request of another domain refused; one waits, again — «already»
	if _, _, body := do(nil, "GET", "/register/nope", nil); !strings.Contains(body, "закрыта") {
		t.Fatal("a wrong token is open")
	}
	reg := "/register/" + set.Token
	if st, _, body := do(nil, "GET", reg, nil); st != http.StatusOK || !strings.Contains(body, `name="email"`) {
		t.Fatalf("the registration page: %d", st)
	}
	if _, _, body := do(nil, "POST", reg, url.Values{"name": {"Ева"}, "email": {"eve@gmail.test"}}); !strings.Contains(body, "только для адресов") {
		t.Fatal("another domain accepted")
	}
	if _, _, body := do(nil, "POST", reg, url.Values{"name": {"Ева Петрова"}, "email": {"Eve@Firm.test"}}); !strings.Contains(body, "Заявка отправлена") {
		t.Fatal("the request not taken")
	}
	if _, _, body := do(nil, "POST", reg, url.Values{"name": {"Ева"}, "email": {"eve@firm.test"}}); !strings.Contains(body, "уже есть") {
		t.Fatal("a second request of the e-mail")
	}
	if _, _, body := do(nil, "POST", reg, url.Values{"name": {"Бот"}, "email": {"bot@firm.test"}, "website": {"x"}}); !strings.Contains(body, "Заявка отправлена") {
		t.Fatal("the robot sees a refusal")
	}
	do(nil, "POST", reg, url.Values{"name": {"Макс"}, "email": {"max@other.test"}})
	list, err := cs.JoinRequests(ctx)
	must(err)
	if len(list) != 2 || list[0].State != "pending" || list[0].IP != "198.51.100.1" {
		t.Fatalf("requests = %+v", list)
	}
	var notified int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'join_request' AND user_id = $1`, admin).Scan(&notified))
	if notified != 2 {
		t.Fatalf("the administrator notified %d times", notified)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'join_request' AND user_id = $1`, plain).Scan(&notified))
	if notified != 0 {
		t.Fatal("an employee notified")
	}
	var eve, maxID string
	for _, j := range list {
		if j.Email == "eve@firm.test" {
			eve = j.ID
		} else {
			maxID = j.ID
		}
	}

	// accepted into «Продажи» — an invitation; rejected — nothing; decided once
	if st, loc, _ := do(plainCookie, "POST", "/company/invite/requests/"+eve+"/accept", url.Values{"department": {sales}}); st != http.StatusSeeOther ||
		!strings.Contains(loc, url.QueryEscape("Приглашено: 1")) {
		t.Fatalf("accept: %d %s", st, loc)
	}
	invites, err := cs.Invites(ctx)
	must(err)
	if len(invites) != 1 || invites[0].Email != "eve@firm.test" || invites[0].Name != "Ева Петрова" || invites[0].Department != "Продажи" || made != 1 {
		t.Fatalf("invites = %+v", invites)
	}
	if st, _, _ := do(plainCookie, "POST", "/company/invite/requests/"+eve+"/reject", nil); st != http.StatusNotFound {
		t.Fatalf("a decided request rejected: %d", st)
	}
	if st, _, _ := do(adminCookie, "POST", "/company/invite/requests/"+maxID+"/reject", nil); st != http.StatusSeeOther {
		t.Fatalf("reject: %d", st)
	}
	list, err = cs.JoinRequests(ctx)
	must(err)
	for _, j := range list {
		if j.ID == eve && (j.State != "accepted" || j.InviteID != invites[0].ID || j.DecidedBy != "Простов") || j.ID == maxID && j.State != "rejected" {
			t.Fatalf("decided = %+v", j)
		}
	}
	if _, _, body := do(adminCookie, "GET", "/company/invite", nil); !strings.Contains(body, "Заявки на регистрацию") || !strings.Contains(body, "отклонена") {
		t.Fatal("the requests are not on the page")
	}

	// a new link: the old one is closed
	if st, _, _ := do(adminCookie, "POST", "/settings/employees/link", nil); st != http.StatusSeeOther {
		t.Fatalf("renew: %d", st)
	}
	if _, _, body := do(nil, "GET", reg, nil); !strings.Contains(body, "закрыта") {
		t.Fatal("the old link works")
	}

	// REST
	adminKey, err := keys.Create(ctx, admin, "k", []string{"settings:read", "settings:write", "company:read", "company:write"}, time.Hour)
	must(err)
	plainKey, err := keys.Create(ctx, plain, "k", []string{"settings:read", "settings:write", "company:read", "company:write"}, time.Hour)
	must(err)
	api := func(k, method, path, in string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(in))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, _ := api(plainKey, "GET", "/api/v1/settings/employees", ""); st != http.StatusForbidden {
		t.Fatalf("an employee reads the settings: %d", st)
	}
	if st, b := api(adminKey, "PUT", "/api/v1/settings/employees", `{"invite_all":false,"approve":true,"domains":[]}`); st != http.StatusOK ||
		!strings.Contains(b, `"register": true`) || !strings.Contains(b, `"link": "https://go24.test/register/`) || !strings.Contains(b, `"invite_all": false`) {
		t.Fatalf("PUT: %d %s", st, b)
	}
	set, err = cs.StaffSettings(ctx)
	must(err)
	do(nil, "POST", "/register/"+set.Token, url.Values{"name": {"Юля"}, "email": {"yulia@any.test"}})
	if st, _ := api(plainKey, "GET", "/api/v1/users/join-requests", ""); st != http.StatusForbidden {
		t.Fatalf("not all invite again, an employee lists the requests: %d", st)
	}
	st, b := api(adminKey, "GET", "/api/v1/users/join-requests", "")
	var out []apiJoinRequest
	must(json.Unmarshal([]byte(b), &out))
	if st != http.StatusOK || len(out) != 3 || out[0].Email != "yulia@any.test" || out[0].State != "pending" {
		t.Fatalf("GET requests: %d %s", st, b)
	}
	if st, b := api(adminKey, "POST", "/api/v1/users/join-requests/"+out[0].ID+"/accept", `{"department_id":"`+sales+`"}`); st != http.StatusOK ||
		!strings.Contains(b, `"state": "waiting"`) || !strings.Contains(b, `"department": "Продажи"`) {
		t.Fatalf("accept by REST: %d %s", st, b)
	}
	if st, _ := api(adminKey, "POST", "/api/v1/users/join-requests/"+out[0].ID+"/reject", ""); st != http.StatusNotFound {
		t.Fatalf("a decided request rejected by REST: %d", st)
	}
	if st, b := api(adminKey, "POST", "/api/v1/settings/employees/link", ""); st != http.StatusOK || strings.Contains(b, set.Token) {
		t.Fatalf("a new link by REST: %d %s", st, b)
	}
}
