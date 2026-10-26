package bp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestProcess: «Заявка на отпуск» — the manager approves, the starter is told, HR gets a task; a rejection ends it;
// a pause waits for the worker; a deal's process moves its stage.
func TestProcess(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, ann, hr := newUser("boss"), newUser("ann"), newUser("hr")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Отдел', $1) RETURNING id`, boss).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, ann, dept)
	must(err)
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, hr, "HR", "Кадры", "")
	must(err)
	cs := crm.NewStore(pool)
	s := NewStore(pool, ts, cs, company.NewStore(pool))

	var ve *i18n.Error
	if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Пустой"}); !errors.As(err, &ve) {
		t.Fatalf("a template without steps: %v", err)
	}
	vacation, err := s.SaveTemplate(ctx, boss, Template{Name: "Заявка на отпуск", Active: true,
		Fields: []Field{{Key: "from", Label: "С", Kind: "date", Required: true}, {Key: "days", Label: "Дней", Kind: "number"}},
		Steps: []Step{{Kind: "approve", To: "manager", Text: "Отпуск {starter} с {from} на {days} дн."},
			{Kind: "notify", To: "starter", Text: "Отпуск согласован"},
			{Kind: "task", To: hr, Project: "HR", Text: "Оформить отпуск: {starter}", Days: 2}}})
	must(err)
	if _, err := s.Start(ctx, ann, vacation, "", map[string]string{"days": "14"}); !errors.As(err, &ve) {
		t.Fatalf("a required field missing: %v", err)
	}
	// «Остановить»: only who started it or an administrator; the approval is not asked any more
	stop, err := s.Start(ctx, ann, vacation, "", map[string]string{"from": "2026-12-01", "days": "3"})
	must(err)
	if err := s.Stop(ctx, hr, stop, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("stopped by a stranger: %v", err)
	}
	must(s.Stop(ctx, ann, stop, "Передумала"))
	if err := s.Stop(ctx, ann, stop, ""); !errors.As(err, &ve) {
		t.Fatalf("stopped twice: %v", err)
	}
	if mine, _ := s.MyApprovals(ctx, boss); len(mine) != 0 {
		t.Fatalf("a stopped process asks: %+v", mine)
	}
	if lines, _ := s.Log(ctx, stop); lines[len(lines)-1].Kind != "stopped" || lines[len(lines)-1].Text != "Передумала" {
		t.Fatalf("history of the stopped: %+v", lines)
	}
	if all, _ := s.AllInstances(ctx, Filter{Status: "stopped"}); len(all) != 1 || all[0].ID != stop {
		t.Fatalf("all the stopped: %+v", all)
	}
	if err := s.DeleteInstance(ctx, stop); !errors.Is(err, ErrForbidden) {
		t.Fatalf("deleted not by an administrator: %v", err)
	}
	must(s.DeleteInstance(tasks.WithAdmin(ctx), stop))
	id, err := s.Start(ctx, ann, vacation, "", map[string]string{"from": "2026-11-02", "days": "14"})
	must(err)
	mine, _ := s.MyApprovals(ctx, boss)
	if len(mine) != 1 || mine[0].Text != "Отпуск ann с 2026-11-02 на 14 дн." {
		t.Fatalf("boss's approvals = %+v", mine)
	}
	if _, err := s.Instance(ctx, hr, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("someone outside saw the process: %v", err)
	}
	if err := s.Decide(ctx, ann, mine[0].ID, "approve", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the starter approved her own request: %v", err)
	}
	must(s.Decide(ctx, boss, mine[0].ID, "approve", "Хорошего отдыха"))
	x, err := s.Instance(ctx, ann, id)
	must(err)
	hrTasks, _ := ts.ListTasks(ctx, tasks.Filter{UserID: hr, Role: "responsible"})
	if x.Status != "done" || len(hrTasks) != 1 || hrTasks[0].Title != "Оформить отпуск: ann" || hrTasks[0].Deadline == nil {
		t.Fatalf("after approval: %s, HR tasks %+v", x.Status, hrTasks)
	}
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'bp' AND user_id = $1`, ann).Scan(&told))
	if told != 2 { // «согласован» and the process is done
		t.Fatalf("ann's notifications = %d", told)
	}
	if err := s.Decide(ctx, boss, mine[0].ID, "reject", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("decided twice")
	}

	// a rejection ends the process
	id2, err := s.Start(ctx, ann, vacation, "", map[string]string{"from": "2026-12-01"})
	must(err)
	mine, _ = s.MyApprovals(ctx, boss)
	must(s.Decide(ctx, boss, mine[0].ID, "reject", "Не сейчас"))
	if x, _ := s.Instance(ctx, ann, id2); x.Status != "rejected" {
		t.Fatalf("rejected process = %s", x.Status)
	}

	// a deal's process: a pause, then the stage
	pipes, _ := cs.Pipelines(ctx)
	deal, err := cs.SaveDeal(ctx, ann, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID})
	must(err)
	follow, err := s.SaveTemplate(ctx, boss, Template{Name: "Дожать", Entity: "deal", Active: true,
		Steps: []Step{{Kind: "wait", Days: 3}, {Kind: "stage", Stage: pipes[0].Stages[3].ID}}})
	must(err)
	if _, err := s.Start(ctx, ann, follow, "", nil); err == nil {
		t.Fatal("a deal's process started without a deal")
	}
	id3, err := s.Start(ctx, ann, follow, deal, nil)
	must(err)
	if errs := s.WakeDue(ctx, time.Now().Add(time.Hour)); len(errs) != 0 {
		t.Fatal(errs)
	}
	if x, _ := s.Instance(ctx, ann, id3); x.Status != "running" || x.WakeAt == nil {
		t.Fatalf("the pause ended early: %+v", x)
	}
	if errs := s.WakeDue(ctx, time.Now().AddDate(0, 0, 4)); len(errs) != 0 {
		t.Fatal(errs)
	}
	d, _ := cs.Deal(ctx, deal)
	if x, _ := s.Instance(ctx, ann, id3); x.Status != "done" || d.Stage.ID != pipes[0].Stages[3].ID {
		t.Fatalf("after the pause: %s, stage %s", x.Status, d.Stage.Code)
	}
	if list, _ := s.Instances(ctx, "", "deal", deal); len(list) != 1 {
		t.Fatalf("the deal's processes = %d", len(list))
	}
	// «Вызвать n8n»: the process's data go out, the JSON answer comes back as fields for the next steps
	n8n := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"invoice": "INV-" + in["values"].(map[string]any)["sum"].(string), "ok": true})
	}))
	defer n8n.Close()
	if _, err := s.SaveTemplate(ctx, boss, Template{Name: "x", Active: true, Steps: []Step{{Kind: "webhook", URL: "not a url"}}}); err == nil {
		t.Fatal("a webhook step without a URL")
	}
	bill, err := s.SaveTemplate(ctx, boss, Template{Name: "Счёт через n8n", Active: true, Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "number"}},
		Steps: []Step{{Kind: "webhook", URL: n8n.URL}, {Kind: "notify", To: "starter", Text: "Счёт {invoice} готов ({ok})"}}})
	must(err)
	bid, err := s.Start(ctx, ann, bill, "", map[string]string{"sum": "1500"})
	must(err)
	var said string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE kind = 'bp' AND user_id = $1 ORDER BY id DESC OFFSET 1 LIMIT 1`, ann).Scan(&said))
	if x, _ := s.Instance(ctx, ann, bid); x.Status != "done" || x.Values["invoice"] != "INV-1500" || said != "Счёт INV-1500 готов (true)" {
		t.Fatalf("after n8n: %s %v, told %q", x.Status, x.Values, said)
	}
	if lines, _ := s.Log(ctx, id); len(lines) < 4 {
		t.Fatalf("history = %+v", lines)
	}

	// branches (GO24DEV-291): a condition, a «go to», a field changed, an approval by the majority of three that jumps
	// when rejected, a review everyone must read
	dir := newUser("dir")
	trip := Template{Name: "Командировка", Active: true,
		Fields: []Field{{Key: "city", Label: "Город", Kind: "string"}, {Key: "sum", Label: "Сумма", Kind: "number"}},
		Steps: []Step{{Kind: "if", Field: "sum", Op: "gt", Value: "50000", Goto: 4},
			{Kind: "approve", To: "manager", Users: []string{hr, dir}, Mode: "majority", Text: "Дорогая командировка: {sum}", Goto: 6},
			{Kind: "goto", Goto: 5},
			{Kind: "set", Field: "city", Value: "{city} (эконом)"},
			{Kind: "review", To: hr, Users: []string{boss}, Text: "Командировка {starter} в {city}"},
			{Kind: "notify", To: "starter", Text: "Итог: {city}"}}}
	bad := trip
	bad.Steps = append([]Step{}, trip.Steps...)
	bad.Steps[2] = Step{Kind: "goto", Goto: 2}
	if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
		t.Fatalf("a jump back: %v", err)
	}
	bad.Steps[2] = Step{Kind: "if", Field: "nope", Op: "eq"}
	if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
		t.Fatalf("a condition on no field: %v", err)
	}
	tripID, err := s.SaveTemplate(ctx, boss, trip)
	must(err)
	pending := func(user string) []Approval {
		list, err := s.MyApprovals(ctx, user)
		must(err)
		return list
	}
	// cheap: no approval, the city changed, both read it
	cheap, err := s.Start(ctx, ann, tripID, "", map[string]string{"city": "Казань", "sum": "1000"})
	must(err)
	if len(pending(boss)) != 1 || pending(boss)[0].Kind != "review" || pending(hr)[0].Text != "Командировка ann в Казань (эконом)" {
		t.Fatalf("the review: boss %+v, hr %+v", pending(boss), pending(hr))
	}
	if err := s.Decide(ctx, hr, pending(hr)[0].ID, "reject", ""); err != nil { // a review knows only «seen»
		t.Fatal(err)
	}
	if x, _ := s.Instance(ctx, ann, cheap); x.Status != "running" {
		t.Fatalf("done before everyone read it: %s", x.Status)
	}
	must(s.Decide(ctx, boss, pending(boss)[0].ID, "", ""))
	if x, _ := s.Instance(ctx, ann, cheap); x.Status != "done" || x.Values["city"] != "Казань (эконом)" {
		t.Fatalf("after the review: %s %v", x.Status, x.Values)
	}
	// expensive, approved by two of three: the change of the city is gone round
	dear, err := s.Start(ctx, ann, tripID, "", map[string]string{"city": "Сочи", "sum": "90000"})
	must(err)
	if len(pending(boss)) != 1 || len(pending(hr)) != 1 || len(pending(dir)) != 1 {
		t.Fatalf("approvals: boss %d, hr %d, dir %d", len(pending(boss)), len(pending(hr)), len(pending(dir)))
	}
	var mineDear Approval
	for _, a := range pending(boss) {
		if a.InstanceID == dear {
			mineDear = a
		}
	}
	must(s.Decide(ctx, boss, mineDear.ID, "approve", ""))
	must(s.Decide(ctx, hr, pending(hr)[0].ID, "reject", "Дорого"))
	if x, _ := s.Instance(ctx, ann, dear); x.Step != 1 {
		t.Fatalf("one for, one against of three decided: step %d", x.Step)
	}
	must(s.Decide(ctx, dir, pending(dir)[0].ID, "approve", ""))
	if x, _ := s.Instance(ctx, ann, dear); x.Step != 4 || x.Values["city"] != "Сочи" {
		t.Fatalf("approved by the majority: step %d %v", x.Step, x.Values)
	}
	// expensive, rejected by two of three: straight to the result, the third one is not asked any more
	no, err := s.Start(ctx, ann, tripID, "", map[string]string{"city": "Париж", "sum": "500000"})
	must(err)
	for _, u := range []string{hr, dir} {
		for _, a := range pending(u) {
			if a.InstanceID == no {
				must(s.Decide(ctx, u, a.ID, "reject", ""))
			}
		}
	}
	for _, a := range pending(boss) {
		if a.InstanceID == no {
			t.Fatal("still asks after the majority rejected")
		}
	}
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE kind = 'bp' AND user_id = $1 ORDER BY id DESC OFFSET 1 LIMIT 1`, ann).Scan(&said))
	if x, _ := s.Instance(ctx, ann, no); x.Status != "done" || said != "Итог: Париж" {
		t.Fatalf("rejected by the majority: %s, told %q", x.Status, said)
	}

	// «Запрос дополнительной информации» (GO24DEV-387): any of the people asked fills in the fields, they join the
	// process's form for the next steps; a request that may be rejected jumps as a rejected approval
	of := func(user, instance string) Approval {
		t.Helper()
		for _, a := range pending(user) {
			if a.InstanceID == instance {
				return a
			}
		}
		t.Fatalf("%s is not asked in %s", user, instance)
		return Approval{}
	}
	ask := Template{Name: "Закупка", Active: true, Fields: []Field{{Key: "what", Label: "Что", Kind: "string"}},
		Steps: []Step{{Kind: "request", To: "manager", Users: []string{hr}, Text: "Уточните бюджет: {what}",
			Fields: []Field{{Key: "budget", Label: "Бюджет", Kind: "number", Required: true}, {Key: "note", Label: "Примечание", Kind: "text"}}},
			{Kind: "if", Field: "budget", Op: "gt", Value: "1000", Goto: 5},
			{Kind: "request", To: hr, Text: "Поставщик?", Reject: true, Goto: 4, Fields: []Field{{Key: "vendor", Label: "Поставщик", Kind: "string"}}},
			{Kind: "notify", To: "starter", Text: "Поставщик: {vendor}"},
			{Kind: "notify", To: "starter", Text: "Бюджет {budget}: {note}"}}}
	for _, b := range []Step{{Kind: "request", Text: "?"}, // nothing to ask
		{Kind: "request", Text: "?", Goto: 2, Fields: []Field{{Key: "y", Label: "Y", Kind: "string"}}}, // a jump without «Отклонить»
		{Kind: "request", Text: "?", Fields: []Field{{Key: "what", Label: "Что", Kind: "string"}}},     // the form's key again
		{Kind: "notify", Text: "?", Reject: true}} {
		bad := ask
		bad.Steps = append([]Step{b}, ask.Steps...)
		if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
			t.Fatalf("a bad request %+v: %v", b, err)
		}
	}
	bad = ask
	bad.Steps = append([]Step{{Kind: "if", Field: "budget", Op: "empty"}}, ask.Steps...) // checks a field asked later
	if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
		t.Fatalf("a condition before the request: %v", err)
	}
	askID, err := s.SaveTemplate(ctx, boss, ask)
	must(err)
	buy, err := s.Start(ctx, ann, askID, "", map[string]string{"what": "Стулья"})
	must(err)
	req := of(boss, buy)
	if req.Kind != "request" || req.Text != "Уточните бюджет: Стулья" || len(req.Fields) != 2 || req.CanReject {
		t.Fatalf("the request: %+v", req)
	}
	if err := s.Decide(ctx, boss, req.ID, "approve", ""); !errors.As(err, &ve) {
		t.Fatalf("a request approved: %v", err)
	}
	if err := s.Decide(ctx, boss, req.ID, "reject", ""); !errors.As(err, &ve) {
		t.Fatalf("a request rejected without «Отклонить»: %v", err)
	}
	if err := s.Answer(ctx, boss, req.ID, map[string]string{"note": "x"}, ""); !errors.As(err, &ve) {
		t.Fatalf("a required field missing: %v", err)
	}
	if err := s.Answer(ctx, boss, req.ID, map[string]string{"budget": "много"}, ""); !errors.As(err, &ve) {
		t.Fatalf("not a number: %v", err)
	}
	if err := s.Answer(ctx, ann, req.ID, map[string]string{"budget": "1"}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("answered by someone not asked: %v", err)
	}
	must(s.Answer(ctx, hr, of(hr, buy).ID, map[string]string{"budget": "500", "note": "  до пятницы ", "what": "Столы"}, "Готово"))
	x, err = s.Instance(ctx, ann, buy)
	must(err)
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE kind = 'bp' AND user_id = $1 ORDER BY id DESC OFFSET 1 LIMIT 1`, ann).Scan(&said))
	if x.Status != "done" || x.Values["budget"] != "500" || x.Values["what"] != "Стулья" || said != "Бюджет 500: до пятницы" {
		t.Fatalf("after the answer: %s %v, told %q", x.Status, x.Values, said)
	}
	if err := s.Answer(ctx, boss, req.ID, map[string]string{"budget": "1"}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the second one asked answered too: %v", err)
	}
	if lines, _ := s.Log(ctx, buy); lines[1].Kind != "step" || lines[2].Kind != "answered" || lines[2].Text != "Готово" {
		t.Fatalf("history of the request: %+v", lines)
	}
	// a big budget: on to the vendor; hr rejects it, and the jump goes on to step 4 instead of ending the process
	big, err := s.Start(ctx, ann, askID, "", map[string]string{"what": "Сервер"})
	must(err)
	must(s.Answer(ctx, boss, of(boss, big).ID, map[string]string{"budget": "90000"}, ""))
	vendor := of(hr, big)
	if !vendor.CanReject || vendor.Fields[0].Key != "vendor" {
		t.Fatalf("the second request: %+v", vendor)
	}
	must(s.Decide(ctx, hr, vendor.ID, "reject", "Нет поставщика"))
	if x, _ := s.Instance(ctx, ann, big); x.Status != "done" || x.Values["vendor"] != "" {
		t.Fatalf("after the rejected request: %s %v", x.Status, x.Values)
	}
}

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		v, op, want string
		ok          bool
	}{{"10", "gt", "9", true}, {"10", "gt", "9a", false}, {"1,5", "lt", "2", true}, {"Да", "eq", "да", true},
		{"2026-11-02", "gt", "2026-10-31", true}, {"Москва", "contains", "моск", true}, {" ", "empty", "", true},
		{"x", "filled", "", true}, {"a", "ne", "b", true}, {"a", "nope", "a", false}} {
		if check(c.v, c.op, c.want) != c.ok {
			t.Errorf("check(%q %s %q) != %t", c.v, c.op, c.want, c.ok)
		}
	}
}
