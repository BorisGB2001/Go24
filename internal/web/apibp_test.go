package web

import (
	"bytes"
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
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBPAPI: «Бизнес-процессы» through REST API v1 — a template made, changed and switched off by an administrator
// (the others neither change it nor see it switched off or its steps); a process started, its request answered and
// approval decided by the person asked, as «Мои задания» list them; seen by who takes part only; another stopped by its
// starter, found by an administrator among everybody's and deleted; the template deleted with its processes.
func TestBPAPI(t *testing.T) {
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
	user := func(name string, admin bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, admin) VALUES ($1, $1, $2) RETURNING id`, name, admin).Scan(&id))
		return id
	}
	anna, boris, eve, oleg := user("Анна", true), user("Борис", false), user("Ева", false), user("Олег", false)
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	annaKey, borisKey, eveKey, olegKey := key(anna, "bp:read", "bp:write"), key(boris, "bp:read", "bp:write"),
		key(eve, "bp:read", "bp:write"), key(oleg, "bp:read", "bp:write")
	readOnly, noBP := key(eve, "bp:read"), key(eve, "crm:read")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, CRM: crm.NewStore(pool), Company: company.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BP = bp.NewStore(pool, srv.Tasks, srv.CRM, srv.Company)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(method, path, token string, body any, want int, out any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}

	// the template: an administrator's only
	tpl := map[string]any{"name": "Закупка", "description": "через API",
		"fields": []map[string]any{{"key": "sum", "label": "Сумма", "kind": "number", "required": true}},
		"steps": []map[string]any{
			{"kind": "request", "text": "Уточните поставщика", "to": boris, "fields": []map[string]any{{"key": "vendor", "label": "Поставщик", "kind": "string"}}},
			{"kind": "approve", "text": "Закупка на {sum} у {vendor}", "to": boris},
			{"kind": "notify", "text": "Закупка утверждена", "to": "starter"}}}
	call("POST", "/api/v1/bp/templates", eveKey, tpl, 403, nil)
	call("POST", "/api/v1/bp/templates", readOnly, tpl, 403, nil)
	call("GET", "/api/v1/bp/templates", noBP, nil, 403, nil)
	call("POST", "/api/v1/bp/templates", annaKey, map[string]any{"name": "Пусто"}, 422, nil) // no steps
	var made apiBPTemplate
	call("POST", "/api/v1/bp/templates", annaKey, tpl, 201, &made)
	if made.ID == "" || made.Entity != "none" || !made.Active || len(made.Steps) != 3 || made.Steps[0].Fields[0].Key != "vendor" {
		t.Fatalf("made %+v", made)
	}
	var list []apiBPTemplate
	call("GET", "/api/v1/bp/templates", eveKey, nil, 200, &list)
	if len(list) != 1 || list[0].Steps != nil || len(list[0].Fields) != 1 || !strings.HasSuffix(list[0].URL, "/bp/start/"+made.ID) {
		t.Fatalf("templates for an employee %+v", list)
	}
	call("PATCH", "/api/v1/bp/templates/"+made.ID, eveKey, map[string]any{"active": false}, 403, nil)
	call("PATCH", "/api/v1/bp/templates/"+made.ID, annaKey, map[string]any{"steps": []map[string]any{{"kind": "goto", "goto": 1}}}, 422, nil)
	call("PATCH", "/api/v1/bp/templates/"+made.ID, annaKey, map[string]any{"active": false}, 200, &made)
	if made.Active || made.Name != "Закупка" || len(made.Steps) != 3 {
		t.Fatalf("switched off %+v", made)
	}
	call("GET", "/api/v1/bp/templates/"+made.ID, eveKey, nil, 404, nil)
	call("GET", "/api/v1/bp/templates", eveKey, nil, 200, &list)
	call("POST", "/api/v1/bp/templates/"+made.ID+"/start", eveKey, map[string]any{"values": map[string]string{"sum": "100"}}, 422, nil)
	if call("GET", "/api/v1/bp/templates", annaKey, nil, 200, &list); len(list) != 1 {
		t.Fatalf("an administrator sees the inactive ones: %+v", list)
	}
	call("PATCH", "/api/v1/bp/templates/"+made.ID, annaKey, map[string]any{"active": true, "name": "Закупка товара"}, 200, &made)
	var seen apiBPTemplate
	call("GET", "/api/v1/bp/templates/"+made.ID, eveKey, nil, 200, &seen)
	if seen.Name != "Закупка товара" || seen.Steps != nil {
		t.Fatalf("for an employee %+v", seen)
	}

	// a process: started, the request answered, the approval decided
	call("POST", "/api/v1/bp/templates/"+made.ID+"/start", readOnly, map[string]any{"values": map[string]string{"sum": "100"}}, 403, nil)
	call("POST", "/api/v1/bp/templates/"+made.ID+"/start", eveKey, map[string]any{"values": map[string]string{"sum": "сто"}}, 422, nil)
	var x apiBPInstance
	call("POST", "/api/v1/bp/templates/"+made.ID+"/start", eveKey, map[string]any{"values": map[string]string{"sum": "100"}}, 201, &x)
	if x.Status != "running" || x.Step != 1 || x.StartedBy.ID != eve || len(x.Steps) != 3 || x.Steps[0].State != "now" ||
		len(x.Tasks) != 1 || x.Tasks[0].Kind != "request" || x.Tasks[0].User.ID != boris || !x.CanStop || len(x.Log) == 0 {
		t.Fatalf("started %+v", x)
	}
	call("GET", "/api/v1/bp/instances/"+x.ID, olegKey, nil, 403, nil)
	var mine []*apiBPTask
	call("GET", "/api/v1/bp/tasks", eveKey, nil, 200, &mine)
	if len(mine) != 0 {
		t.Fatalf("the starter has no tasks: %+v", mine)
	}
	call("GET", "/api/v1/bp/tasks", borisKey, nil, 200, &mine)
	if len(mine) != 1 || mine[0].ProcessID != x.ID || len(mine[0].Fields) != 1 || mine[0].Step != 1 {
		t.Fatalf("Boris's tasks %+v", mine)
	}
	call("POST", "/api/v1/bp/tasks/"+mine[0].ID+"/complete", eveKey, map[string]any{"decision": "approve"}, 404, nil)
	call("POST", "/api/v1/bp/tasks/"+mine[0].ID+"/complete", borisKey, map[string]any{"decision": "answer", "values": map[string]string{"vendor": "Ромашка"}}, 200, &x)
	if x.Step != 2 || x.Values["vendor"] != "Ромашка" || x.Steps[0].State != "done" || len(x.Fields) != 2 {
		t.Fatalf("answered %+v", x)
	}
	call("GET", "/api/v1/bp/tasks", borisKey, nil, 200, &mine)
	if len(mine) != 1 || mine[0].Kind != "approve" || mine[0].Text != "Закупка на 100 у Ромашка" {
		t.Fatalf("the approval %+v", mine)
	}
	var done apiBPInstance
	call("POST", "/api/v1/bp/tasks/"+mine[0].ID+"/complete", borisKey, map[string]any{"decision": "maybe"}, 422, nil)
	call("POST", "/api/v1/bp/tasks/"+mine[0].ID+"/complete", borisKey, map[string]any{"decision": "approve", "comment": "ок"}, 200, &done)
	if x = done; x.Status != "done" || x.Step != 0 || x.FinishedAt == nil || x.CanStop || x.Tasks[1].Decision != "approve" || x.Tasks[1].Comment != "ок" {
		t.Fatalf("approved %+v", x)
	}
	call("POST", "/api/v1/bp/tasks/"+mine[0].ID+"/complete", borisKey, map[string]any{"decision": "approve"}, 404, nil)
	call("GET", "/api/v1/bp/instances/"+x.ID, borisKey, nil, 200, &x) // who took part sees it

	// another: stopped by its starter, deleted by an administrator
	var y apiBPInstance
	call("POST", "/api/v1/bp/"+made.ID+"/start", eveKey, map[string]any{"values": map[string]string{"sum": "5"}}, 201, &y) // the first address
	call("POST", "/api/v1/bp/instances/"+y.ID+"/stop", olegKey, map[string]string{"reason": "чужой"}, 403, nil)
	call("POST", "/api/v1/bp/instances/"+y.ID+"/stop", eveKey, map[string]string{"reason": "передумала"}, 200, &y)
	if y.Status != "stopped" || y.Log[len(y.Log)-1].Kind != "stopped" || y.Log[len(y.Log)-1].Text != "передумала" {
		t.Fatalf("stopped %+v", y)
	}
	call("POST", "/api/v1/bp/instances/"+y.ID+"/stop", eveKey, map[string]string{}, 422, nil)
	var all []apiBPInstance
	call("GET", "/api/v1/bp/instances", eveKey, nil, 200, &all)
	if len(all) != 2 || all[0].ID != y.ID || all[0].Steps != nil {
		t.Fatalf("Eve's processes %+v", all)
	}
	call("GET", "/api/v1/bp/instances?status=done", eveKey, nil, 200, &all)
	if len(all) != 1 || all[0].ID != x.ID {
		t.Fatalf("Eve's done processes %+v", all)
	}
	call("GET", "/api/v1/bp/instances?all=1", eveKey, nil, 403, nil)
	call("GET", "/api/v1/bp/instances?all=1&status=stopped", annaKey, nil, 200, &all)
	if len(all) != 1 || all[0].ID != y.ID {
		t.Fatalf("the stopped ones %+v", all)
	}
	call("GET", "/api/v1/bp/instances?entity=bogus&entity_id=1", eveKey, nil, 422, nil)
	call("DELETE", "/api/v1/bp/instances/"+y.ID, eveKey, nil, 403, nil)
	call("DELETE", "/api/v1/bp/instances/"+y.ID, annaKey, nil, 204, nil)
	call("GET", "/api/v1/bp/instances/"+y.ID, annaKey, nil, 404, nil)

	call("DELETE", "/api/v1/bp/templates/"+made.ID, eveKey, nil, 403, nil)
	call("DELETE", "/api/v1/bp/templates/"+made.ID, annaKey, nil, 204, nil)
	call("DELETE", "/api/v1/bp/templates/"+made.ID, annaKey, nil, 404, nil)
	call("GET", "/api/v1/bp/instances/"+x.ID, eveKey, nil, 404, nil) // its processes go with it, as on the page
}
