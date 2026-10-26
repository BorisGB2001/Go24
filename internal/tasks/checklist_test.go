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
