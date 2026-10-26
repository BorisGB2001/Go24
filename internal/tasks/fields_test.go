package tasks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestFields: an administrator's fields, values checked by type, who may fill them, the history, deleting a field.
func TestFields(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	creator, resp := newUser("creator"), newUser("resp")
	p, err := s.CreateProject(ctx, creator, "uf", "Поля", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, creator, p)
	must(err)
	key, err := s.CreateTask(ctx, creator, proj, NewTask{Title: "Договор", ResponsibleID: resp})
	must(err)
	id, err := s.TaskIDByKey(ctx, key)
	must(err)

	var ve *i18n.Error
	if err := s.AddField(ctx, Field{Name: "Этап", Kind: "list"}); !errors.As(err, &ve) {
		t.Fatalf("a list without values must fail, got %v", err)
	}
	must(s.AddField(ctx, Field{Name: "Сумма", Kind: "number", Sort: 1}))
	must(s.AddField(ctx, Field{Name: "Этап", Kind: "list", Options: []string{"Черновик", " Подписан ", ""}, Sort: 2}))
	must(s.AddField(ctx, Field{Name: "Срочно", Kind: "bool", Sort: 3}))
	if err := s.AddField(ctx, Field{Name: "сумма", Kind: "string"}); !errors.As(err, &ve) {
		t.Fatalf("a duplicate name must fail, got %v", err)
	}
	fields, err := s.Fields(ctx)
	must(err)
	if len(fields) != 3 || fields[1].Options[1] != "Подписан" {
		t.Fatalf("fields = %+v", fields)
	}
	sum, stage, urgent := fields[0].ID, fields[1].ID, fields[2].ID

	if err := s.SetFieldValues(ctx, resp, id, map[string]string{sum: "1"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the responsible must not fill the fields, got %v", err)
	}
	if err := s.SetFieldValues(ctx, creator, id, map[string]string{sum: "много"}); !errors.As(err, &ve) {
		t.Fatalf("a bad number must fail, got %v", err)
	}
	if err := s.SetFieldValues(ctx, creator, id, map[string]string{stage: "Отменён"}); !errors.As(err, &ve) {
		t.Fatalf("a value out of the list must fail, got %v", err)
	}
	must(s.SetFieldValues(ctx, creator, id, map[string]string{sum: "1 500,50", stage: "Подписан", urgent: "1"}))
	must(s.SetFieldValues(ctx, creator, id, map[string]string{sum: "1500.5", urgent: ""}))
	vals, err := s.FieldValues(ctx, id)
	must(err)
	if vals[0].Value != "1500.5" || vals[1].Value != "Подписан" || vals[2].Value != "" {
		t.Fatalf("values = %+v", vals)
	}
	var changes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'field'`, id).Scan(&changes))
	if changes != 4 { // sum, stage, urgent set; urgent cleared (the same sum is no change)
		t.Fatalf("history changes = %d", changes)
	}
	must(s.DeleteField(ctx, stage))
	if vals, _ = s.FieldValues(ctx, id); len(vals) != 2 {
		t.Fatalf("after delete = %+v", vals)
	}
}

// TestListFilter: the list filter of «Задачи» — search, statuses, people, deadline columns, priority — and presets.
func TestListFilter(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna, petr := newUser("boss"), newUser("anna"), newUser("petr")
	p, err := s.CreateProject(ctx, boss, "fl", "Фильтр", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, boss, p)
	must(err)
	past, soon := time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour*30)
	k1, err := s.CreateTask(ctx, boss, proj, NewTask{Title: "Отчёт за квартал", ResponsibleID: anna, Deadline: &past, Priority: 2})
	must(err)
	_, err = s.CreateTask(ctx, boss, proj, NewTask{Title: "Договор поставки", ResponsibleID: petr, Deadline: &soon, Auditors: []string{anna}})
	must(err)
	_, err = s.CreateTask(ctx, anna, proj, NewTask{Title: "Отчёт по продажам", ResponsibleID: anna})
	must(err)
	id1, _ := s.TaskIDByKey(ctx, k1)
	must(s.ApplyAction(ctx, anna, id1, ActionStart))

	keys := func(f Filter) string {
		t.Helper()
		f.Viewer = boss
		list, err := s.ListTasks(ctx, f)
		must(err)
		var out []string
		for _, x := range list {
			out = append(out, x.Key())
		}
		slices.Sort(out)
		return strings.Join(out, " ")
	}
	for name, c := range map[string]struct {
		f    Filter
		want string
	}{
		"search":      {Filter{Search: "отчёт"}, "FL-1 FL-3"},
		"key":         {Filter{Search: "fl-2"}, "FL-2"},
		"status":      {Filter{Statuses: []Status{StatusInProgress}}, "FL-1"},
		"responsible": {Filter{ResponsibleID: anna}, "FL-1 FL-3"},
		"member":      {Filter{MemberID: anna}, "FL-1 FL-2 FL-3"},
		"overdue":     {Filter{Period: "overdue"}, "FL-1"},
		"later":       {Filter{Period: "later"}, "FL-2"},
		"none":        {Filter{Period: "none"}, "FL-3"},
		"important":   {Filter{HighPriority: true}, "FL-1"},
		"together":    {Filter{Search: "отчёт", CreatorID: anna}, "FL-3"},
	} {
		if got := keys(c.f); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}

	must(s.SavePreset(ctx, anna, "Мои", "member=me"))
	must(s.SavePreset(ctx, anna, "мои", "member=me&prio=high")) // the same name replaces it
	var ve *i18n.Error
	if err := s.SavePreset(ctx, anna, " ", "q=x"); !errors.As(err, &ve) {
		t.Fatalf("a preset without a name: %v", err)
	}
	ps, err := s.Presets(ctx, anna)
	must(err)
	if len(ps) != 1 || ps[0].Query != "member=me&prio=high" {
		t.Fatalf("presets = %+v", ps)
	}
	if err := s.DeletePreset(ctx, boss, ps[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's preset deleted: %v", err)
	}
	must(s.DeletePreset(ctx, anna, ps[0].ID))
}

// TestUserFields: the employee profile's own fields are kept apart from the task fields.
func TestUserFields(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	var anna string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@test') RETURNING id`).Scan(&anna); err != nil {
		t.Fatal(err)
	}
	if err := s.AddFieldTo(ctx, UserFieldSet, Field{Name: "Разряд", Kind: "list", Options: []string{"1", "2"}}); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.Fields(ctx); len(list) != 0 {
		t.Fatalf("a profile field among task fields: %+v", list)
	}
	list, _ := s.FieldsOf(ctx, UserFieldSet)
	var ve *i18n.Error
	if err := s.SetUserFieldValues(ctx, anna, map[string]string{list[0].ID: "3"}); !errors.As(err, &ve) {
		t.Fatalf("a value out of the list: %v", err)
	}
	if err := s.SetUserFieldValues(ctx, anna, map[string]string{list[0].ID: "2"}); err != nil {
		t.Fatal(err)
	}
	if vals, _ := s.FieldValuesOf(ctx, UserFieldSet, anna); len(vals) != 1 || vals[0].Value != "2" {
		t.Fatalf("anna's fields = %+v", vals)
	}
	if err := s.DeleteFieldOf(ctx, UserFieldSet, list[0].ID); err != nil {
		t.Fatal(err)
	}
}

// TestCRMFields: each kind of CRM card has its own fields; the values go with their card.
func TestCRMFields(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	var anna, deal string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@test') RETURNING id`).Scan(&anna); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO crm_deals (title, pipeline_id, stage_id, responsible_id, created_by)
		SELECT 'Поставка', pipeline_id, id, $1, $1 FROM crm_stages WHERE entity = 'deal' ORDER BY sort LIMIT 1 RETURNING id`, anna).Scan(&deal); err != nil {
		t.Fatal(err)
	}
	set := CRMFieldSets["deal"]
	for _, f := range []Field{{Name: "Бюджет", Kind: "number"}, {Name: "VIP", Kind: "bool"}} {
		if err := s.AddFieldTo(ctx, set, f); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := s.FieldsOf(ctx, CRMFieldSets["lead"]); len(list) != 0 {
		t.Fatalf("a deal field among lead fields: %+v", list)
	}
	list, _ := s.FieldsOf(ctx, set)
	values := map[string]string{}
	for _, f := range list {
		values[f.ID] = map[string]string{"Бюджет": "1500", "VIP": "1"}[f.Name]
	}
	if err := s.SetFieldValuesOf(ctx, set, deal, values); err != nil {
		t.Fatal(err)
	}
	if vals, _ := s.FieldValuesOf(ctx, set, deal); len(vals) != 2 || vals[0].Value == "" || vals[1].Value == "" {
		t.Fatalf("deal's fields = %+v", vals)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM crm_deals WHERE id = $1`, deal); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM crm_deal_field_values`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d values outlived their deal (%v)", left, err)
	}
}
