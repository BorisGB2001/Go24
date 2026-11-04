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
	"go24/internal/tasks"
)

// TestAbsenceKindsHTTP: «Типы отсутствий» — an HR officer with «absences» (and an administrator) renames, recolours and
// turns off the built-in reasons and adds own ones; an employee cannot. An own reason is offered and shown everywhere
// with its name and colour; one turned off is not accepted for new absences but stays on the old ones; an own reason
// with absences is turned off instead of deleted; at least one stays on; the REST lists and saves them alike.
func TestAbsenceKindsHTTP(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, hr, adm := user("Анна"), user("Кадры"), user("Админ")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, adm)
	must(err)
	must(company.NewStore(pool).SetHR(ctx, hr, true, []string{"absences"}))
	_, err = pool.Exec(ctx, `INSERT INTO departments (name) VALUES ('Компания')`)
	must(err)

	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Keys: keys, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, hr, adm} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == adm}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	// form: the current reasons as the page sends them, changed by edit (key → name, color, off, del)
	form := func(edit map[string][4]string, newName, newColor string) url.Values {
		list, err := srv.Company.AbsenceKindList(ctx)
		must(err)
		f := url.Values{"new_name": {newName}, "new_color": {newColor}}
		for i, k := range list {
			n := strconv.Itoa(i)
			name, color, off, del := k.Name, k.Color, "", ""
			if k.Off {
				off = "1"
			}
			if e, ok := edit[k.Key]; ok {
				name, color, off, del = e[0], e[1], e[2], e[3]
			}
			f.Set("key."+n, k.Key)
			f.Set("name."+n, name)
			f.Set("color."+n, color)
			if off != "" {
				f.Set("off."+n, off)
			}
			if del != "" {
				f.Set("del."+n, del)
			}
		}
		return f
	}
	kind := func(key string) company.AbsenceKind {
		list, err := srv.Company.AbsenceKindList(ctx)
		must(err)
		for _, k := range list {
			if k.Key == key {
				return k
			}
		}
		return company.AbsenceKind{}
	}

	// an employee neither sees nor saves the reasons
	if st, _ := do(anna, "GET", "/company/absences/kinds", nil); st != http.StatusForbidden {
		t.Fatalf("kinds page for an employee: %d", st)
	}
	if st, _ := do(anna, "POST", "/company/absences/kinds", form(nil, "Учёба", "blue")); st != http.StatusForbidden {
		t.Fatalf("an employee saved the kinds: %d", st)
	}
	if _, body := do(anna, "GET", "/company/absences", nil); strings.Contains(body, "/company/absences/kinds") {
		t.Fatal("the absences page links the kinds for an employee")
	}
	// the HR officer: the built-in ones (with the new «Прогул», «Декретный отпуск»), renames, turns off, adds
	st, body := do(hr, "GET", "/company/absences/kinds", nil)
	if st != http.StatusOK || !strings.Contains(body, `value="maternity"`) || !strings.Contains(body, `value="absent"`) {
		t.Fatalf("kinds page: %d", st)
	}
	if st, body := do(hr, "GET", "/company/absences", nil); !strings.Contains(body, "/company/absences/kinds") {
		t.Fatalf("the absences page does not link the kinds for an HR officer: %d", st)
	}
	st, _ = do(hr, "POST", "/company/absences/kinds", form(map[string][4]string{"vacation": {"Ежегодный отпуск", "green", "", ""},
		"absent": {"", "red", "1", ""}}, "Учёба", "blue"))
	if st != http.StatusSeeOther {
		t.Fatalf("save kinds: %d", st)
	}
	list, err := srv.Company.AbsenceKindList(ctx)
	must(err)
	study := list[len(list)-1]
	if study.Builtin || study.Name != "Учёба" || study.Color != "blue" || !strings.HasPrefix(study.Key, "c") ||
		kind("vacation").Name != "Ежегодный отпуск" || !kind("absent").Off || kind("sick").Color != "red" {
		t.Fatalf("kinds: %+v", list)
	}
	// an own reason for an absence; one turned off is refused
	today := time.Now()
	if st, _ := do(anna, "POST", "/company/absences", url.Values{"kind": {study.Key}, "from": {today.Format(time.DateOnly)},
		"to": {today.AddDate(0, 0, 2).Format(time.DateOnly)}}); st != http.StatusSeeOther {
		t.Fatalf("an absence with an own reason: %d", st)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM absences WHERE user_id = $1 AND kind = $2`, anna, study.Key).Scan(&n))
	if n != 1 {
		t.Fatalf("absences with the own reason: %d", n)
	}
	if _, err := srv.Company.CreateAbsence(ctx, anna, anna, "absent", today, today, "", false, "", nil); err == nil {
		t.Fatal("an absence with a reason turned off")
	}
	if _, err := srv.Company.CreateAbsence(ctx, anna, anna, "holiday", today, today, "", false, "", nil); err == nil {
		t.Fatal("an absence with an unknown reason")
	}
	// shown with the name and the colour; the form offers the own reason, not the one turned off
	_, body = do(anna, "GET", "/company/absences?view=list", nil)
	if !strings.Contains(body, `pill abs-blue">Учёба`) || !strings.Contains(body, `value="`+study.Key+`"`) ||
		strings.Contains(body, `value="absent"`) || !strings.Contains(body, "Ежегодный отпуск") {
		t.Fatal("the list does not show the own reason")
	}
	if _, body = do(anna, "GET", "/company/absences", nil); !strings.Contains(body, `absbar abs-blue`) {
		t.Fatal("the chart does not colour the own reason")
	}
	if !company.IsAbsenceKind(study.Key) || company.IsAbsenceKind("holiday") {
		t.Fatal("IsAbsenceKind (business processes)")
	}
	// deleting the own reason that has an absence turns it off; the absence keeps it
	st, _ = do(hr, "POST", "/company/absences/kinds", form(map[string][4]string{study.Key: {"Учёба", "blue", "", "1"}}, "", ""))
	if st != http.StatusSeeOther || !kind(study.Key).Off {
		t.Fatalf("delete a used own reason: %d %+v", st, kind(study.Key))
	}
	if _, body = do(anna, "GET", "/company/absences?view=list", nil); !strings.Contains(body, `pill abs-blue">Учёба`) {
		t.Fatal("an absence lost its reason turned off")
	}
	// at least one reason stays on; an own one needs a name
	off := map[string][4]string{study.Key: {"Учёба", "blue", "1", ""}}
	for _, k := range company.AbsenceKinds {
		off[k] = [4]string{"", kind(k).Color, "1", ""}
	}
	if do(hr, "POST", "/company/absences/kinds", form(off, "", "")); kind("vacation").Off {
		t.Fatal("every reason was turned off")
	}
	if err := srv.Company.SaveAbsenceKinds(ctx, []company.AbsenceKind{{Name: " ", Color: "red"}}); err == nil {
		t.Fatal("an own reason without a name")
	}
	if err := srv.Company.SaveAbsenceKinds(ctx, []company.AbsenceKind{{Name: "Х", Color: "pink"}}); err == nil {
		t.Fatal("an unknown colour")
	}

	// REST
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	annaKey, admKey := key(anna), key(adm)
	api := func(k, method, path, body string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+k)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		return b
	}
	var got []struct {
		company.AbsenceKind
		Title string `json:"title"`
	}
	must(json.Unmarshal(api(annaKey, "GET", "/api/v1/absences/kinds", "", http.StatusOK), &got))
	if len(got) != len(company.AbsenceKinds)-1 || got[0].Title != "Ежегодный отпуск" || got[1].Title == "" {
		t.Fatalf("REST kinds: %+v", got)
	}
	api(annaKey, "PUT", "/api/v1/absences/kinds", `[{"name":"Донор","color":"red"}]`, http.StatusForbidden)
	must(json.Unmarshal(api(admKey, "PUT", "/api/v1/absences/kinds", `[{"name":"Донор","color":"red"}]`, http.StatusOK), &got))
	if got[len(got)-1].Title != "Донор" {
		t.Fatalf("REST kinds after PUT: %+v", got)
	}
	api(admKey, "PUT", "/api/v1/absences/kinds", `[{"key":"cnope","name":"Х","color":"red"}]`, http.StatusUnprocessableEntity)
	api(annaKey, "POST", "/api/v1/absences", `{"kind":"`+got[len(got)-1].Key+`","from":"2026-12-01","to":"2026-12-02"}`, http.StatusCreated)
}
