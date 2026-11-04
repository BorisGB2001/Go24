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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMFieldSightHTTP: «Видимость поля» — the administrator shows «Маржа» to a department (its sub-departments
// too) and «Секрет» to herself; the others' card, its form and REST leave the hidden fields out, saving the card keeps
// their values, a hidden required field is not asked; the settings show who sees a field.
func TestCRMFieldSightHTTP(t *testing.T) {
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
	var anna, boris, vera, gleb, hq, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('vera', 'Вера', 'vera@t') RETURNING id`).Scan(&vera))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('gleb', 'Глеб', 'gleb@t') RETURNING id`).Scan(&gleb))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Коммерция') RETURNING id`).Scan(&hq))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Продажи', $1, $2) RETURNING id`, hq, gleb).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, boris, sales)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Company: company.NewStore(pool), Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool), I18n: bundle,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookies := map[string]*http.Cookie{}
	for _, u := range []string{anna, boris, vera, gleb} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna}))
		cookies[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookies[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}

	// the sight: Boris — his department and the one above, Gleb — the department he heads, Vera — nobody's
	for _, c := range []struct {
		who  string
		vis  []string
		sees bool
	}{{boris, []string{"dept:" + hq}, true}, {boris, []string{"dept:" + sales}, true}, {gleb, []string{"dept:" + sales}, true},
		{vera, []string{"dept:" + hq}, false}, {vera, []string{vera}, true}, {vera, nil, true}, {anna, []string{boris}, false}} {
		v, err := srv.CRM.FieldSightOf(ctx, c.who, false)
		must(err)
		if v.Sees(tasks.Field{Visible: c.vis}) != c.sees {
			t.Errorf("sight of %s on %v: want %v", c.who, c.vis, c.sees)
		}
	}
	if v, _ := srv.CRM.FieldSightOf(ctx, anna, true); !v.Sees(tasks.Field{Visible: []string{boris}}) {
		t.Error("an administrator does not see a field")
	}

	// the settings: «Маржа» to «Коммерция», «Секрет» (required) to Анна
	set := tasks.CRMFieldSets["deal"]
	must(srv.Tasks.AddFieldTo(ctx, set, tasks.Field{Name: "Маржа", Kind: "string", Sort: 1}))
	must(srv.Tasks.AddFieldTo(ctx, set, tasks.Field{Name: "Секрет", Kind: "string", Sort: 2, Required: true}))
	must(srv.Tasks.AddFieldTo(ctx, set, tasks.Field{Name: "Город", Kind: "string", Sort: 3}))
	var margin, secret, city string
	must(pool.QueryRow(ctx, `SELECT id FROM crm_deal_fields WHERE name = 'Маржа'`).Scan(&margin))
	must(pool.QueryRow(ctx, `SELECT id FROM crm_deal_fields WHERE name = 'Секрет'`).Scan(&secret))
	must(pool.QueryRow(ctx, `SELECT id FROM crm_deal_fields WHERE name = 'Город'`).Scan(&city))
	if code, _, _ := do(anna, "/settings/crm-deal-fields/"+margin+"/visible", url.Values{"who": {"dept:" + hq}}); code != http.StatusSeeOther {
		t.Fatalf("visible: %d", code)
	}
	if code, _, _ := do(anna, "/settings/crm-deal-fields/"+secret+"/visible", url.Values{"who": {anna, anna, " "}}); code != http.StatusSeeOther {
		t.Fatalf("visible: %d", code)
	}
	if code, _, _ := do(boris, "/settings/crm-deal-fields/"+city+"/visible", url.Values{"who": {boris}}); code != http.StatusForbidden {
		t.Fatalf("an employee sets the visibility: %d", code)
	}
	if _, page, _ := do(anna, "/settings/crm-deal-fields", nil); !strings.Contains(page, "Коммерция") || !strings.Contains(page, `value="dept:`+hq+`"`) ||
		!strings.Contains(page, `value="`+anna+`"`) {
		t.Fatal("the settings miss who sees the fields")
	}
	list, err := srv.Tasks.FieldsOf(ctx, set)
	must(err)
	if len(list[1].Visible) != 1 || list[1].Visible[0] != anna || len(list[2].Visible) != 0 {
		t.Fatalf("stored: %+v", list)
	}

	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boris}})
	must(err)
	must(srv.Tasks.SetFieldValuesOf(ctx, set, deal, map[string]string{margin: "MARGIN-30", secret: "SECRET-77", city: "Тверь"}))

	// the card: Анна — all, Борис — «Маржа» and «Город», Вера — «Город»
	card := "/crm/deals/" + deal
	for who, want := range map[string][]bool{anna: {true, true}, boris: {true, false}, vera: {false, false}} {
		code, page, _ := do(who, card, nil)
		if code != http.StatusOK || !strings.Contains(page, "Тверь") {
			t.Fatalf("card of %s: %d", who, code)
		}
		if strings.Contains(page, "MARGIN-30") != want[0] || strings.Contains(page, "SECRET-77") != want[1] {
			t.Errorf("card of %s: margin %v secret %v", who, strings.Contains(page, "MARGIN-30"), strings.Contains(page, "SECRET-77"))
		}
		if _, page, _ = do(who, card+"?edit=1", nil); strings.Contains(page, `name="f_`+secret+`"`) != (who == anna) {
			t.Errorf("the form of %s and «Секрет»", who)
		}
	}

	// Борис saves the card with every field of his form: «Секрет» (required, hidden) is not asked and stays
	edit := url.Values{"title": {"Поставка"}, "responsible": {boris}, "pipeline": {pipes[0].ID}, "with_fields": {"1"}, "f_" + margin: {"MARGIN-31"}, "f_" + city: {"Тверь"}}
	if code, _, loc := do(boris, card, edit); code != http.StatusSeeOther || strings.Contains(loc, "edit=1") {
		t.Fatalf("Борис saves: %d %s", code, loc)
	}
	vals, err := srv.Tasks.ValuesOf(ctx, set, []string{deal})
	must(err)
	if vals[deal][margin] != "MARGIN-31" || vals[deal][secret] != "SECRET-77" {
		t.Fatalf("after Борис: %v", vals[deal])
	}
	// a value of a hidden field sent anyway is ignored
	edit.Set("f_"+secret, "")
	do(vera, card, edit)
	if vals, _ = srv.Tasks.ValuesOf(ctx, set, []string{deal}); vals[deal][secret] != "SECRET-77" || vals[deal][margin] != "MARGIN-31" {
		t.Fatalf("after Вера: %v", vals[deal])
	}

	// REST: Вера's crm.deal.get and userfield.list lack the hidden fields, Анна's have them
	rest := func(who, method, body string) map[string]any {
		t.Helper()
		key, err := srv.Keys.Create(ctx, who, "k", apikeys.AllScopes, 0)
		must(err)
		res, err := http.Post(hs.URL+"/rest/"+method+"?auth="+key, "application/json", strings.NewReader(body))
		must(err)
		defer res.Body.Close()
		var out map[string]any
		must(json.NewDecoder(res.Body).Decode(&out))
		return out
	}
	var dealNum string
	must(pool.QueryRow(ctx, `SELECT num::text FROM crm_deals WHERE id = $1`, deal).Scan(&dealNum))
	for who, want := range map[string]int{anna: 3, vera: 1} {
		got, _ := rest(who, "crm.deal.get", `{"id": `+dealNum+`}`)["result"].(map[string]any)
		n := 0
		for k := range got {
			if strings.HasPrefix(k, "UF_CRM_") {
				n++
			}
		}
		ufs, _ := rest(who, "crm.deal.userfield.list", `{}`)["result"].([]any)
		if n != want || len(ufs) != want {
			t.Errorf("REST of %s: %d fields in the deal, %d user fields; want %d", who, n, len(ufs), want)
		}
	}
}
