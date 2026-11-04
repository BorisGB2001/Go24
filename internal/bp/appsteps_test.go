package bp

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apps"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestAppSteps: an application's action of the designer (bizproc.activity.add) posts its properties ({…} filled, an
// employee as user_<number>) and, with an answer, waits for bizproc.event.send — its values become the process's
// fields; without an answer the process goes on; the wait ends by the step's time; a branch waits as well.
func TestAppSteps(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	newUser := func(n string) (string, int64) {
		var id string
		var num int64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id, num`, n).Scan(&id, &num))
		return id, num
	}
	boss, _ := newUser("boss")
	ann, annNum := newUser("ann")
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	as := apps.NewStore(pool)
	s.Apps = as
	var appID string
	must(pool.QueryRow(ctx, `INSERT INTO apps (name, handler_url, client_id, client_secret, application_token, installed_at, created_by)
		VALUES ('Проверка ИНН', 'https://app.example.com/', 'c1', 's1', 't1', now(), $1) RETURNING id`, boss).Scan(&appID))
	app, err := as.Get(ctx, appID)
	must(err)
	if _, err := as.SaveActivity(ctx, app, apps.Activity{Code: "check", Handler: "https://evil.example.com/", Name: "x"}, false); err == nil {
		t.Fatal("a handler off the application's host")
	}
	check, err := as.SaveActivity(ctx, app, apps.Activity{Code: "check", Handler: "https://app.example.com/bp", Subscribe: true, Name: "Проверить ИНН",
		Props: []apps.Prop{{Key: "inn", Name: "ИНН", Type: "string", Required: true}, {Key: "who", Name: "Кто", Type: "user"},
			{Key: "mode", Name: "Режим", Type: "select", Default: "fast", Options: []apps.Option{{Key: "fast", Value: "Быстро"}, {Key: "full", Value: "Полно"}}}},
		Returns: []apps.Prop{{Key: "OK", Name: "Найден", Type: "bool"}, {Key: "score", Name: "Балл", Type: "int"}, {Key: "owner", Name: "Владелец", Type: "user"}}}, false)
	must(err)
	if _, err := as.SaveActivity(ctx, app, apps.Activity{Code: "check", Handler: "https://app.example.com/bp", Name: "Ещё"}, false); !errors.Is(err, apps.ErrActivityExists) {
		t.Fatalf("the same code twice: %v", err)
	}
	ping, err := as.SaveActivity(ctx, app, apps.Activity{Code: "ping", Handler: "https://app.example.com/ping", Name: "Пинг"}, false)
	must(err)

	type call struct {
		code, user, token string
		props             map[string]any
	}
	var calls []call
	s.AppCall = func(_ context.Context, act apps.Activity, userID string, x Instance, token string, props map[string]any) error {
		calls = append(calls, call{act.Code, userID, token, props})
		return nil
	}

	form := []Field{{Key: "inn", Label: "ИНН", Kind: "string", Required: true}}
	var ve *i18n.Error
	for _, bad := range []Step{
		{Kind: "app", App: check.ID}, // ИНН is required
		{Kind: "app", App: check.ID, Sets: []Assign{{Field: "inn", Value: "1"}, {Field: "x", Value: "1"}}}, // no such property
		{Kind: "app", App: "00000000-0000-0000-0000-000000000000", Sets: []Assign{{Field: "inn", Value: "1"}}},
		{Kind: "app"},
		{Kind: "notify", To: "starter", Text: "x", App: check.ID},
	} {
		if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Fields: form, Steps: []Step{bad}}); !errors.As(err, &ve) {
			t.Fatalf("saved %+v: %v", bad, err)
		}
	}
	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Проверка", Active: true, Fields: form, Steps: []Step{
		{Kind: "app", App: check.ID, Timeout: 60, Sets: []Assign{{Field: "inn", Value: "{inn}"}, {Field: "who", Value: "ann"}}},
		{Kind: "if", Field: AppFieldKey("check", "score"), Op: "gt", Value: "40", Goto: 0},
		{Kind: "app", App: ping.ID, Timeout: 5},
	}})
	must(err)
	saved, err := s.Template(ctx, tpl)
	must(err)
	if st := saved.Steps[0]; st.Text != "Проверка ИНН: Проверить ИНН" || len(st.Fields) != 3 || st.Fields[0].Key != "app_check_ok" || st.Fields[1].Kind != "number" ||
		st.Fields[2].Kind != "user" || saved.Steps[2].Timeout != 0 || saved.Steps[2].Fields != nil {
		t.Fatalf("saved steps: %+v", saved.Steps)
	}

	id, err := s.Start(ctx, boss, tpl, "", map[string]string{"inn": "7707083893"})
	must(err)
	if len(calls) != 1 || calls[0].code != "check" || calls[0].user != boss || calls[0].token == "" || calls[0].props["inn"] != "7707083893" ||
		calls[0].props["who"] != "user_"+strconv.FormatInt(annNum, 10) || calls[0].props["mode"] != "fast" {
		t.Fatalf("calls: %+v", calls)
	}
	x, err := s.instance(ctx, id)
	must(err)
	if x.Status != "running" || x.Step != 0 || x.WakeAt == nil {
		t.Fatalf("waits: %+v", x)
	}
	token := calls[0].token
	if err := s.AppLog(ctx, token, "Запрос ушёл в ФНС"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppAnswer(ctx, "00000000-0000-0000-0000-000000000000", token, nil, ""); !errors.Is(err, ErrAppToken) {
		t.Fatalf("another application answers: %v", err)
	}
	must(s.AppAnswer(ctx, appID, token, map[string]string{"ok": "Y", "SCORE": "42", "owner": "user_" + strconv.FormatInt(annNum, 10)}, "Найден"))
	x, err = s.instance(ctx, id)
	must(err)
	if x.Status != "done" || x.Values["app_check_ok"] != "Y" || x.Values["app_check_score"] != "42" || x.Values["app_check_owner"] != ann {
		t.Fatalf("answered: %+v", x)
	}
	if len(calls) != 2 || calls[1].code != "ping" || calls[1].token != "" {
		t.Fatalf("an action without an answer: %+v", calls)
	}
	lines, err := s.Log(ctx, id)
	must(err)
	reports := 0
	for _, l := range lines {
		if l.Kind == "report" {
			reports++
		}
	}
	if reports != 2 {
		t.Fatalf("log: %+v", lines)
	}
	if err := s.AppAnswer(ctx, appID, token, nil, ""); !errors.Is(err, ErrAppToken) {
		t.Fatalf("answered twice: %v", err)
	}

	// the time to wait is over: the process goes on without the values
	id, err = s.Start(ctx, boss, tpl, "", map[string]string{"inn": "1"})
	must(err)
	token = calls[len(calls)-1].token
	for _, err := range s.WakeDue(ctx, time.Now().Add(2*time.Hour)) {
		must(err)
	}
	x, err = s.instance(ctx, id)
	must(err)
	if x.Status != "done" || x.Values["app_check_score"] != "" {
		t.Fatalf("timed out: %+v", x)
	}
	if err := s.AppAnswer(ctx, appID, token, map[string]string{"score": "1"}, ""); !errors.Is(err, ErrAppToken) {
		t.Fatalf("answered late: %v", err)
	}

	// in a branch of a parallel step: the branch waits, the other goes on; the answer joins them
	par, err := s.SaveTemplate(ctx, boss, Template{Name: "Параллельно", Active: true, Fields: form, Steps: []Step{
		{Kind: "parallel", Branches: []int{3}},
		{Kind: "app", App: check.ID, Sets: []Assign{{Field: "inn", Value: "{inn}"}}},
		{Kind: "notify", To: "starter", Text: "Проверяем"},
	}})
	must(err)
	id, err = s.Start(ctx, boss, par, "", map[string]string{"inn": "2"})
	must(err)
	token = calls[len(calls)-1].token
	if x, _ = s.instance(ctx, id); x.Status != "running" {
		t.Fatalf("the branch waits: %+v", x)
	}
	must(s.AppAnswer(ctx, appID, token, map[string]string{"score": "7"}, ""))
	if x, _ = s.instance(ctx, id); x.Status != "done" || x.Values["app_check_score"] != "7" {
		t.Fatalf("joined: %+v", x)
	}

	// the action taken off: a step of it fails, a template with it is not saved
	must(as.DeleteActivity(ctx, app, "check"))
	if _, err := s.SaveTemplate(ctx, boss, saved); !errors.As(err, &ve) {
		t.Fatalf("saved with an action gone: %v", err)
	}
	id, err = s.Start(ctx, boss, tpl, "", map[string]string{"inn": "3"})
	must(err)
	if x, _ = s.instance(ctx, id); x.Status != "failed" {
		t.Fatalf("an action gone: %+v", x)
	}
}
