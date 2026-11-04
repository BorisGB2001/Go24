package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
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
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSalary: «Зарплата и отпуск» (GO24DEV-895) — the vacation balance in the profile (the employee, their head, an
// HR officer; not a colleague), the days an HR officer with «absences» changes, the company's default, «Положено» and
// «Остаток» in the vacation report and its CSV; payslips uploaded by an HR officer with «salary» — one, many matched by
// the file names, a non-PDF refused; the employee is notified, opens theirs (noted), a colleague cannot; REST.
func TestSalary(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
	}
	now := time.Now()
	if now.YearDay() < 25 || now.YearDay() > 340 { // the vacations below stay within this year
		t.Skip("the vacations of the test do not fit the year's first and last days")
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
	newUser := func(name, email string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $2) RETURNING id`, name, email).Scan(&id))
		return id
	}
	admin, hr, boss := newUser("Админов Админ", "admin@t"), newUser("Кадрова Ирина", "hr@t"), newUser("Начальников Глеб", "boss@t")
	emp, colleague := newUser("Пётр Иванов", "petr@t"), newUser("Ольга Соседова", "olga@t")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	root, _, err := cs.Tree(ctx)
	must(err)
	sales, err := cs.CreateDepartment(ctx, root.ID, "Продажи", boss)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $3)`, emp, colleague, sales)
	must(err)
	day := func(d int) time.Time { return time.Date(now.Year(), now.Month(), now.Day()+d, 0, 0, 0, 0, time.Local) }
	must(cs.AddAbsence(ctx, admin, emp, "vacation", day(-20), day(-11), "", true, "", nil)) // 10 days taken
	must(cs.AddAbsence(ctx, admin, emp, "vacation", day(10), day(14), "", true, "", nil))   // 5 planned
	must(cs.AddAbsence(ctx, admin, emp, "sick", day(-5), day(-3), "", true, "", nil))       // not a vacation

	keys := apikeys.NewStore(pool)
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	empKey, colleagueKey, hrKey := key(emp), key(colleague), key(hr)
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Company: cs, Notify: notify.NewStore(pool),
		Storages: storages.NewStore(pool, nil, s3),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, hr, boss, emp, colleague} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u, Admin: u == admin}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request) (int, string, http.Header) {
		t.Helper()
		if who != "" {
			req.AddCookie(cookie[who])
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header
	}
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
		st, b, _ := send(who, req)
		return st, b
	}
	pdf := []byte("%PDF-1.4\n1 0 obj << >> endobj\ntrailer << >>\n%%EOF\n")
	upload := func(who, path string, fields map[string]string, files map[string][]byte, field string) (int, string, http.Header) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range fields {
			_ = mw.WriteField(k, v)
		}
		for name, data := range files {
			fw, _ := mw.CreateFormFile(field, name)
			_, _ = fw.Write(data)
		}
		_ = mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+path, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return send(who, req)
	}
	api := func(k, method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		st, b, _ := send("", req)
		return st, b
	}
	left := func(uid string) company.VacationBalance {
		t.Helper()
		b, err := cs.VacationBalanceOf(ctx, uid, now.Year(), now)
		must(err)
		return b
	}

	// the balance: 28 by the company, 10 taken, 5 planned, the sick days aside
	if b := left(emp); b.Days != 28 || b.Own || b.Taken != 10 || b.Planned != 5 || b.Left != 13 || b.LeftNow != 18 {
		t.Fatalf("balance %+v", b)
	}
	for who, sees := range map[string]bool{emp: true, boss: true, hr: true, admin: true, colleague: false} {
		_, body := do(who, "GET", "/u/"+emp, nil)
		if strings.Contains(body, `id="psalary"`) != sees {
			t.Errorf("%s sees the card: %v, want %v", who, !sees, sees)
		}
	}
	_, body := do(emp, "GET", "/u/"+emp, nil)
	if !strings.Contains(body, "<b class=\"\">13</b>") || strings.Contains(body, `action="/u/`+emp+`/vacation"`) {
		t.Error("the employee: want 13 days left and no form")
	}
	if _, body = do(boss, "GET", "/u/"+emp, nil); strings.Contains(body, `class="payslips"`) {
		t.Error("the head sees the payslips")
	}

	// the days: an HR officer with «absences», not without it, not the head
	vac := url.Values{"year": {strconv.Itoa(now.Year())}, "days": {"31"}, "carried": {"2"}}
	for _, who := range []string{hr, boss, emp} {
		if st, _ := do(who, "POST", "/u/"+emp+"/vacation", vac); st != http.StatusForbidden {
			t.Errorf("%s changes the days: %d", who, st)
		}
	}
	must(cs.SetHR(ctx, hr, true, []string{"absences"}))
	if st, _ := do(hr, "POST", "/u/"+emp+"/vacation", vac); st != http.StatusSeeOther {
		t.Fatalf("the HR officer changes the days: %d", st)
	}
	if b := left(emp); b.Days != 31 || !b.Own || b.Carried != 2 || b.Left != 18 {
		t.Fatalf("own days: %+v", b)
	}
	if st, _ := do(hr, "POST", "/u/"+emp+"/vacation", url.Values{"year": {strconv.Itoa(now.Year())}, "days": {"400"}}); st != http.StatusSeeOther || left(emp).Days != 31 {
		t.Error("400 days taken")
	}
	// the company's default: the vacation report's form; the employee's own days stay
	if _, body = do(hr, "GET", "/company/absences/report", nil); !strings.Contains(body, `action="/company/vacation-settings"`) {
		t.Error("the HR officer has no default form")
	}
	if _, body = do(boss, "GET", "/company/absences/report", nil); strings.Contains(body, `action="/company/vacation-settings"`) {
		t.Error("the head has the default form")
	}
	if st, _ := do(boss, "POST", "/company/vacation-settings", url.Values{"days": {"20"}}); st != http.StatusForbidden {
		t.Errorf("the head sets the default: %d", st)
	}
	if st, _ := do(hr, "POST", "/company/vacation-settings", url.Values{"days": {"30"}}); st != http.StatusSeeOther {
		t.Fatalf("the default: %d", st)
	}
	if left(colleague).Days != 30 || left(emp).Days != 31 {
		t.Error("the default: want 30 for the colleague, 31 kept for the employee")
	}
	// the report and its CSV: «Положено» with the days carried, «Остаток»
	st, csvBody, _ := send(admin, func() *http.Request {
		r, _ := http.NewRequest("GET", hs.URL+"/company/absences/report?format=csv", nil)
		return r
	}())
	if st != http.StatusOK {
		t.Fatalf("csv: %d", st)
	}
	cr := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvBody, "\uFEFF")))
	cr.Comma = ';'
	rows, err := cr.ReadAll()
	must(err)
	found := false
	for _, r := range rows {
		if len(r) > 1 && r[1] == "Пётр Иванов" {
			found = r[len(r)-2] == "33" && r[len(r)-1] == "18"
		}
	}
	if !found {
		t.Errorf("the CSV: want 33 entitled and 18 left for the employee, got %q", rows)
	}

	// payslips: «salary» needed; a non-PDF refused
	month := now.AddDate(0, -1, 0).Format("2006-01")
	if st, _, _ := upload(hr, "/u/"+emp+"/payslips", map[string]string{"month": month}, map[string][]byte{"l.pdf": pdf}, "file"); st != http.StatusForbidden {
		t.Fatalf("without «salary»: %d", st)
	}
	must(cs.SetHR(ctx, hr, true, []string{"absences", "salary"}))
	if st, _, h := upload(hr, "/u/"+emp+"/payslips", map[string]string{"month": month}, map[string][]byte{"l.txt": []byte("hello")}, "file"); st != http.StatusSeeOther || !strings.Contains(h.Get("Location"), "msg=") {
		t.Fatalf("a text file: %d %s", st, h.Get("Location"))
	}
	if st, _, _ := upload(hr, "/u/"+emp+"/payslips", map[string]string{"month": month}, map[string][]byte{"листок.pdf": pdf}, "file"); st != http.StatusSeeOther {
		t.Fatalf("a payslip: %d", st)
	}
	list, err := cs.Payslips(ctx, company.PayslipFilter{UserID: emp})
	must(err)
	if len(list) != 1 || list[0].FileName != "листок.pdf" || list[0].Month.Format("2006-01") != month || list[0].Seen != nil {
		t.Fatalf("payslips %+v", list)
	}
	ps := list[0]
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'payslip'`, emp).Scan(&n))
	if n != 1 {
		t.Errorf("notifications: %d", n)
	}
	if _, body = do(emp, "GET", "/u/"+emp, nil); !strings.Contains(body, "/company/payslips/"+ps.ID+"/file") {
		t.Error("the employee does not see their payslip")
	}
	if st, _ := do(colleague, "GET", "/company/payslips/"+ps.ID+"/file", nil); st != http.StatusNotFound {
		t.Errorf("a colleague opens it: %d", st)
	}
	if st, _ := do(boss, "GET", "/company/payslips/"+ps.ID+"/file", nil); st != http.StatusNotFound {
		t.Errorf("the head opens it: %d", st)
	}
	st, b, h := send(emp, func() *http.Request {
		r, _ := http.NewRequest("GET", hs.URL+"/company/payslips/"+ps.ID+"/file", nil)
		return r
	}())
	if st != http.StatusOK || h.Get("Content-Type") != "application/pdf" || b != string(pdf) {
		t.Fatalf("the employee opens it: %d %s", st, h.Get("Content-Type"))
	}
	if ps, _ = cs.Payslip(ctx, ps.ID); ps.Seen == nil {
		t.Error("the opening is not noted")
	}
	// many at once: whose by the file name; the unknown listed back; the same month replaced
	st, _, h = upload(hr, "/company/payslips", map[string]string{"month": month},
		map[string][]byte{"Иванов Петр.pdf": pdf, "olga@t.pdf": pdf, "Неизвестный Человек.pdf": pdf}, "files")
	if st != http.StatusSeeOther {
		t.Fatalf("many: %d", st)
	}
	loc, _ := url.Parse(h.Get("Location"))
	if q := loc.Query(); q.Get("done") != "2" || len(q["unmatched"]) != 1 || q.Get("unmatched") != "Неизвестный Человек.pdf" {
		t.Errorf("many: %s", loc.RawQuery)
	}
	if list, _ = cs.Payslips(ctx, company.PayslipFilter{UserID: emp}); len(list) != 1 || list[0].FileName != "Иванов Петр.pdf" || list[0].Seen != nil {
		t.Errorf("the month's payslip replaced: %+v", list)
	}
	if _, body = do(hr, "GET", "/company/payslips?month="+month, nil); !strings.Contains(body, "Иванов Петр.pdf") || !strings.Contains(body, "olga@t.pdf") {
		t.Error("the page of the month")
	}
	if st, _ := do(boss, "GET", "/company/payslips", nil); st != http.StatusForbidden {
		t.Errorf("the head opens the page: %d", st)
	}

	// REST
	var bal company.VacationBalance
	st, body = api(empKey, "GET", "/api/v1/users/me/vacation", "")
	if st != http.StatusOK || json.Unmarshal([]byte(body), &bal) != nil || bal.Left != 18 {
		t.Errorf("api vacation: %d %s", st, body)
	}
	if st, _ = api(colleagueKey, "GET", "/api/v1/users/"+emp+"/vacation", ""); st != http.StatusNotFound {
		t.Errorf("api: a colleague's vacation: %d", st)
	}
	if st, _ = api(colleagueKey, "PUT", "/api/v1/users/"+emp+"/vacation", `{"days":50}`); st != http.StatusForbidden {
		t.Errorf("api: a colleague sets the days: %d", st)
	}
	if st, body = api(hrKey, "PUT", "/api/v1/users/"+emp+"/vacation", `{"days":null,"carried":0}`); st != http.StatusOK || left(emp).Days != 30 {
		t.Errorf("api: back to the default: %d %s", st, body)
	}
	var mine []company.Payslip
	st, body = api(empKey, "GET", "/api/v1/payslips?user_id="+colleague, "")
	if st != http.StatusOK || json.Unmarshal([]byte(body), &mine) != nil || len(mine) != 1 || mine[0].UserID != emp {
		t.Errorf("api: an employee lists another's: %d %s", st, body)
	}
	if st, _ = api(colleagueKey, "GET", "/api/v1/payslips/"+mine[0].ID+"/file", ""); st != http.StatusNotFound {
		t.Errorf("api: a colleague's file: %d", st)
	}
	if st, _ = api(empKey, "GET", "/api/v1/payslips/"+mine[0].ID+"/file", ""); st != http.StatusOK {
		t.Errorf("api: one's own file: %d", st)
	}
	var req *http.Request
	{
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("user_id", colleague)
		_ = mw.WriteField("month", now.AddDate(0, -2, 0).Format("2006-01"))
		fw, _ := mw.CreateFormFile("file", "o.pdf")
		_, _ = fw.Write(pdf)
		_ = mw.Close()
		req, _ = http.NewRequest("POST", hs.URL+"/api/v1/payslips", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Authorization", "Bearer "+hrKey)
	}
	if st, body, _ = send("", req); st != http.StatusCreated {
		t.Errorf("api upload: %d %s", st, body)
	}
	if st, _ = api(empKey, "DELETE", "/api/v1/payslips/"+mine[0].ID, ""); st != http.StatusForbidden {
		t.Errorf("api: the employee deletes: %d", st)
	}
	// deleting
	if st, _ := do(hr, "POST", "/company/payslips/"+mine[0].ID+"/delete", url.Values{"back": {"/u/" + emp}}); st != http.StatusSeeOther {
		t.Fatalf("delete: %d", st)
	}
	if list, _ = cs.Payslips(ctx, company.PayslipFilter{UserID: emp}); len(list) != 0 {
		t.Error("the payslip stays")
	}
}
