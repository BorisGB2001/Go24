package bp

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestLists: an administrator makes a list of fields (not a non-administrator), its rights close it to the others of
// a department and let only some change it; an element is checked by the fields' kinds and the required ones, its
// changes go to its history; a template of the list starts by itself on an element added (approval to who added it,
// {list.…} and {title} filled), its «Изменить элемент» sets the fields as the starter and starts the template of
// changes; a template is of an existing list only, and its «Изменить элемент» sets only the list's fields.
func TestLists(t *testing.T) {
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
	autoLag = 0
	var ann, bob, cat, boss, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@t') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('cat', 'Кэт', 'cat@t') RETURNING id`).Scan(&cat))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $3)`, ann, bob, sales)
	must(err)
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	admin := tasks.WithAdmin(ctx)

	fields := []Field{{Key: "sum", Label: "Сумма", Kind: "number", Required: true}, {Key: "kind", Label: "Вид", Kind: "select", Options: []string{"Аванс", "Расход"}},
		{Key: "who", Label: "Получатель", Kind: "user"}, {Key: "paid", Label: "Оплачено", Kind: "bool"}}
	if _, err := s.SaveList(ctx, ann, List{Name: "Заявки", Fields: fields}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a non-administrator made a list: %v", err)
	}
	if _, err := s.SaveList(admin, boss, List{Name: "Плохой", Fields: []Field{{Key: "name", Label: "Имя", Kind: "string"}}}); err == nil {
		t.Fatal("a field keyed «name» saved")
	}
	list, err := s.SaveList(admin, boss, List{Name: "Заявки на оплату", Fields: fields, Columns: []string{"sum", "gone"}})
	must(err)
	l, err := s.List(ctx, cat, list)
	must(err)
	if len(l.Columns) != 1 || l.Columns[0] != "sum" {
		t.Fatalf("columns: %v", l.Columns)
	}

	// rights: the department sees it, only ann changes it
	must(s.SetListRights(admin, list, "read", []string{"dept:" + sales}))
	must(s.SetListRights(admin, list, "write", []string{ann}))
	if _, err := s.List(ctx, cat, list); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cat sees a list of «Продажи»: %v", err)
	}
	if got, _ := s.Lists(ctx, bob); len(got) != 1 {
		t.Fatalf("bob's lists: %d", len(got))
	}
	if s.CanWriteList(ctx, bob, list) || !s.CanWriteList(ctx, ann, list) || !s.CanWriteList(admin, cat, list) {
		t.Fatal("write rights")
	}
	if _, err := s.SaveElement(ctx, bob, Element{ListID: list, Name: "Бобова", Values: map[string]string{"sum": "1"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob added an element: %v", err)
	}

	// templates: of an existing list; «Изменить элемент» sets its fields only
	if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Без списка", Entity: EntityList, Steps: []Step{{Kind: "notify", To: "starter", Text: "x"}}}); err == nil {
		t.Fatal("a template of lists without a list saved")
	}
	if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Чужое поле", Entity: EntityList, ListID: list,
		Steps: []Step{{Kind: "update", Sets: []Assign{{Field: "title", Value: "x"}}}}}); err == nil {
		t.Fatal("«Изменить элемент» of a field not of the list saved")
	}
	onAdd, err := s.SaveTemplate(ctx, boss, Template{Name: "Согласование заявки", Entity: EntityList, ListID: list, Active: true, AutoCreate: true,
		Steps: []Step{{Kind: "approve", To: "responsible", Text: "{title}: {list.sum} ({list.kind}), {list.who} — {card}"},
			{Kind: "update", Sets: []Assign{{Field: "paid", Value: "Y"}, {Field: "Получатель", Value: "boss@t"}}}}})
	must(err)
	onChange, err := s.SaveTemplate(ctx, boss, Template{Name: "Изменение заявки", Entity: EntityList, ListID: list, Active: true, AutoChange: true,
		Steps: []Step{{Kind: "notify", To: "starter", Text: "{title}"}}})
	must(err)
	runs := func(tpl, id string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM bp_instances WHERE template_id::text = $1 AND entity_id::text = $2`, tpl, id).Scan(&n))
		return n
	}

	// an element: the kinds and the required fields checked
	if _, err := s.SaveElement(ctx, ann, Element{ListID: list, Name: "Без суммы"}); err == nil {
		t.Fatal("an element without a required field saved")
	}
	if _, err := s.SaveElement(ctx, ann, Element{ListID: list, Name: "Не число", Values: map[string]string{"sum": "сто"}}); err == nil {
		t.Fatal("a number field took a word")
	}
	el, err := s.SaveElement(ctx, ann, Element{ListID: list, Name: "Командировка", Values: map[string]string{"sum": "1500", "kind": "Аванс", "who": bob}})
	must(err)
	must(errors.Join(s.AutoStartLists(ctx)...))
	if runs(onAdd, el) != 1 || runs(onChange, el) != 0 {
		t.Fatalf("autostart on add: %d, on change %d", runs(onAdd, el), runs(onChange, el))
	}
	var who, text, approval string
	must(pool.QueryRow(ctx, `SELECT a.id::text, a.user_id::text, a.text FROM bp_approvals a JOIN bp_instances i ON i.id = a.instance_id
		WHERE i.entity_id::text = $1`, el).Scan(&approval, &who, &text))
	if who != ann || text != "Командировка: 1500 (Аванс), Боб — /bp/el/"+el {
		t.Fatalf("approval: %s %q", who, text)
	}
	// approved: «Изменить элемент» sets the fields, as the starter; the template of changes starts on it
	must(s.Decide(ctx, ann, approval, "approve", ""))
	e, _, err := s.Element(ctx, ann, el)
	must(err)
	if e.Values["paid"] != "Y" || e.Values["who"] != boss || e.Values["sum"] != "1500" {
		t.Fatalf("after «Изменить элемент»: %v", e.Values)
	}
	must(errors.Join(s.AutoStartLists(ctx)...))
	if runs(onAdd, el) != 1 || runs(onChange, el) != 1 {
		t.Fatalf("after the step: add %d, change %d", runs(onAdd, el), runs(onChange, el))
	}

	// a change by hand: the history keeps what changed; a save with nothing changed leaves nothing
	_, err = s.SaveElement(ctx, ann, Element{ID: el, Name: "Командировка в Казань", Values: map[string]string{"sum": "2000"}})
	must(err)
	_, err = s.SaveElement(ctx, ann, Element{ID: el, Name: "Командировка в Казань"})
	must(err)
	log, err := s.ElementLog(ctx, el)
	must(err)
	if len(log) != 3 || !log[2].Created || len(log[0].Changes) != 2 || log[0].Changes[0] != (Change{Key: "name", Old: "Командировка", New: "Командировка в Казань"}) ||
		log[0].Changes[1] != (Change{Key: "sum", Old: "1500", New: "2000"}) || log[0].Author.ID != ann {
		t.Fatalf("history: %+v", log)
	}

	// the table: found by a value, ordered by a number field
	_, err = s.SaveElement(ctx, ann, Element{ListID: list, Name: "Канцтовары", Values: map[string]string{"sum": "300", "kind": "Расход"}})
	must(err)
	found, err := s.Elements(ctx, l, ElementFilter{Query: "расход"})
	must(err)
	if len(found) != 1 || found[0].Name != "Канцтовары" {
		t.Fatalf("found: %+v", found)
	}
	sorted, err := s.Elements(ctx, l, ElementFilter{Sort: "sum", Desc: true})
	must(err)
	if len(sorted) != 2 || sorted[0].Name != "Командировка в Казань" {
		t.Fatalf("sorted: %+v", sorted)
	}

	// the card's processes: of one who sees the list; deleting the list deletes its templates and elements
	if _, err := s.CardInstances(ctx, cat, EntityList, el); err == nil {
		t.Fatal("cat sees the processes of an element of a list closed to her")
	}
	if got, err := s.CardInstances(ctx, bob, EntityList, el); err != nil || len(got) != 2 {
		t.Fatalf("bob's card processes: %d %v", len(got), err)
	}
	if err := s.DeleteElement(ctx, bob, el); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob deleted an element: %v", err)
	}
	must(s.DeleteList(admin, list))
	if _, err := s.Template(ctx, onAdd); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the list's template stayed: %v", err)
	}
}
