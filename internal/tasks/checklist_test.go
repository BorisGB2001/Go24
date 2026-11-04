package tasks

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestChecklists: several lists from the line form, items pasted as a list, renamed, marked important, dragged to
// another list; «@Name» makes an accomplice; a list goes with its items; the line form comes back the same.
func TestChecklists(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, worker, anna, stranger := user("Борис Г"), user("Иван Петров"), user("Анна Ли"), user("Чужой")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	must(s.SetMember(ctx, p, worker, "member"))
	key, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Переезд", ResponsibleID: worker,
		Checklist: []string{"Коробки", "", "# Офис", "Ключи", "#", "Мебель"}})
	must(err)
	id, err := s.TaskIDByKey(ctx, key)
	must(err)
	card := func() TaskCard {
		t.Helper()
		c, err := s.GetCard(ctx, boss, id)
		must(err)
		return c
	}
	c := card()
	names := func(l Checklist) string {
		var out []string
		for _, i := range l.Items {
			out = append(out, i.Title)
		}
		return l.Title + ":" + strings.Join(out, ",")
	}
	if len(c.Checklists) != 3 || names(c.Checklists[0]) != ":Коробки" || names(c.Checklists[1]) != "Офис:Ключи" ||
		names(c.Checklists[2]) != ":Мебель" || c.Checklists[2].N != 3 || len(c.Checklist) != 3 {
		t.Fatalf("lists from lines: %+v", c.Checklists)
	}
	if got := strings.Join(c.ChecklistLines(), "|"); got != "Коробки|# Офис|Ключи|# |Мебель" {
		t.Fatalf("lines = %q", got)
	}

	// a pasted list: every line an item; "#" here is just a character; @Анна Ли becomes an accomplice
	office := c.Checklists[1].ID
	must(s.AddChecklistItems(ctx, worker, id, office, "Пропуск\n\n#2 Стол @Анна Ли\r\n"))
	if err := s.AddChecklistItems(ctx, stranger, id, office, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger adds: %v", err)
	}
	if err := s.AddChecklistItems(ctx, worker, id, office, " \n "); err == nil {
		t.Fatal("empty lines added")
	}
	c = card()
	if names(c.Checklists[1]) != "Офис:Ключи,Пропуск,#2 Стол @Анна Ли" {
		t.Fatalf("pasted: %s", names(c.Checklists[1]))
	}
	if len(c.Accomplices) != 1 || c.Accomplices[0].ID != anna {
		t.Fatalf("mention: accomplices %+v", c.Accomplices)
	}
	// no list given — the first one; robots and the API use this
	must(s.AddChecklistItem(ctx, worker, id, "Скотч"))

	// rename, mark, edit, move to another list before an item, delete a list with its items
	must(s.RenameChecklist(ctx, worker, id, office, "  Новый офис "))
	c = card()
	first, keys := c.Checklists[0], c.Checklists[1].Items[0]
	must(s.MarkChecklistItem(ctx, worker, id, keys.ID, true))
	must(s.EditChecklistItem(ctx, worker, id, keys.ID, "Ключи от офиса"))
	must(s.MoveChecklistItem(ctx, worker, id, keys.ID, first.ID, first.Items[1].ID)) // Коробки, [Ключи], Скотч
	if err := s.MoveChecklistItem(ctx, worker, id, keys.ID, "00000000-0000-0000-0000-000000000000", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("move to a list of no task: %v", err)
	}
	c = card()
	if names(c.Checklists[0]) != ":Коробки,Ключи от офиса,Скотч" || !c.Checklists[0].Items[1].Important ||
		names(c.Checklists[1]) != "Новый офис:Пропуск,#2 Стол @Анна Ли" {
		t.Fatalf("after move: %s / %s", names(c.Checklists[0]), names(c.Checklists[1]))
	}
	// within its list: to the end
	must(s.MoveChecklistItem(ctx, worker, id, c.Checklists[0].Items[0].ID, "", ""))
	if c = card(); names(c.Checklists[0]) != ":Ключи от офиса,Скотч,Коробки" {
		t.Fatalf("within the list: %s", names(c.Checklists[0]))
	}
	must(s.DeleteChecklist(ctx, worker, id, c.Checklists[1].ID))
	if c = card(); len(c.Checklists) != 2 || len(c.Checklist) != 4 {
		t.Fatalf("after deleting a list: %+v", c.Checklists)
	}
	// «Добавить чек-лист»: an empty list at the end
	newID, err := s.AddChecklist(ctx, worker, id, "")
	must(err)
	if c = card(); len(c.Checklists) != 3 || c.Checklists[2].ID != newID || len(c.Checklists[2].Items) != 0 {
		t.Fatalf("empty list: %+v", c.Checklists)
	}
}

// TestNestedChecklist: an indented list becomes nested items (no deeper than one level under the item above), the
// line form keeps the indents; a parent counts its items, marks them with it, an opened item opens its parents;
// items shift right and left with their nested ones, move and go away with them; «@Name» is a member of the item.
func TestNestedChecklist(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna := user("Борис Г"), user("Анна Ли")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	key, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Переезд", ResponsibleID: boss,
		Checklist: []string{"Подготовка", "  - Коробки", "    Скотч", "\tКлючи", "Переезд", "      Глубоко"}})
	must(err)
	id, err := s.TaskIDByKey(ctx, key)
	must(err)
	items := func() []ChecklistItem {
		t.Helper()
		c, err := s.GetCard(ctx, boss, id)
		must(err)
		return c.Checklist
	}
	tree := func() string {
		var out []string
		for _, i := range items() {
			mark := ""
			if i.Done {
				mark = "+"
			}
			out = append(out, strings.Repeat(">", i.Level)+i.Title+mark)
		}
		return strings.Join(out, ",")
	}
	if got := tree(); got != "Подготовка,>Коробки,>>Скотч,>Ключи,Переезд,>Глубоко" {
		t.Fatalf("from lines: %s", got)
	}
	c, err := s.GetCard(ctx, boss, id)
	must(err)
	if got := strings.Join(c.ChecklistLines(), "|"); got != "Подготовка|  Коробки|    Скотч|  Ключи|Переезд|  Глубоко" {
		t.Fatalf("lines = %q", got)
	}
	it := items()
	if it[0].Sub != 2 || it[1].Parent != it[0].ID || it[2].Parent != it[1].ID || it[3].Parent != it[0].ID || it[4].Parent != "" {
		t.Fatalf("parents: %+v", it)
	}

	// a parent marks its items; an opened item opens the items it is in, not its siblings
	must(s.ChangeChecklistItem(ctx, boss, id, it[0].ID, false))
	if got := tree(); got != "Подготовка+,>Коробки+,>>Скотч+,>Ключи+,Переезд,>Глубоко" {
		t.Fatalf("parent done: %s", got)
	}
	must(s.ChangeChecklistItem(ctx, boss, id, it[2].ID, false))
	if got := tree(); got != "Подготовка,>Коробки,>>Скотч,>Ключи+,Переезд,>Глубоко" {
		t.Fatalf("child opened: %s", got)
	}
	if it = items(); it[0].Sub != 2 || it[0].SubDone != 1 {
		t.Fatalf("counts: %d of %d", it[0].SubDone, it[0].Sub)
	}

	// right under the item above (with its own items), back left; the first item and the deepest stay
	must(s.IndentChecklistItem(ctx, boss, id, it[4].ID, true))
	if got := tree(); got != "Подготовка,>Коробки,>>Скотч,>Ключи+,>Переезд,>>Глубоко" {
		t.Fatalf("right: %s", got)
	}
	must(s.IndentChecklistItem(ctx, boss, id, it[4].ID, false))
	must(s.IndentChecklistItem(ctx, boss, id, it[0].ID, true))
	for range 3 { // under Коробки, under Скотч, no deeper than the item above + 1
		must(s.IndentChecklistItem(ctx, boss, id, it[3].ID, true))
	}
	if got := tree(); got != "Подготовка,>Коробки,>>Скотч,>>>Ключи+,Переезд,>Глубоко" {
		t.Fatalf("limits: %s", got)
	}

	// Коробки with its items to the end: its level stays, it fits under the last item
	must(s.MoveChecklistItem(ctx, boss, id, it[1].ID, "", ""))
	if got := tree(); got != "Подготовка,Переезд,>Глубоко,>Коробки,>>Скотч,>>>Ключи+" {
		t.Fatalf("moved: %s", got)
	}
	// to the top: it can't be nested there
	must(s.MoveChecklistItem(ctx, boss, id, it[1].ID, "", it[0].ID))
	if got := tree(); got != "Коробки,>Скотч,>>Ключи+,Подготовка,Переезд,>Глубоко" {
		t.Fatalf("moved up: %s", got)
	}
	// deleted with its items
	must(s.ChangeChecklistItem(ctx, boss, id, it[1].ID, true))
	if got := tree(); got != "Подготовка,Переезд,>Глубоко" {
		t.Fatalf("deleted: %s", got)
	}

	// «@Анна Ли»: a member of the item and an accomplice; edited away — no longer a member
	must(s.AddChecklistItems(ctx, boss, id, "", "Позвонить @Анна Ли"))
	it = items()
	call := it[len(it)-1]
	if len(call.Members) != 1 || call.Members[0].ID != anna || call.Level != 0 {
		t.Fatalf("members: %+v", call)
	}
	must(s.EditChecklistItem(ctx, boss, id, call.ID, "Позвонить"))
	if it = items(); len(it[len(it)-1].Members) != 0 {
		t.Fatalf("members after edit: %+v", it[len(it)-1].Members)
	}
	if c, err = s.GetCard(ctx, boss, id); err != nil || len(c.Accomplices) != 1 {
		t.Fatalf("accomplice stays: %+v %v", c.Accomplices, err)
	}
}
