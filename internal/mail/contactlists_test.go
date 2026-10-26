package mail

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// Members reads the addresses as typed: names with commas, lines and semicolons, the same address once; a list
// written back out reads the same.
func TestMembers(t *testing.T) {
	got, err := Members("\"Петров, И.\" <p@x.ru>\nanna@x.ru; Анна <ANNA@x.ru>, b@x.ru")
	if err != nil || len(got) != 3 || got[0] != (Member{"Петров, И.", "p@x.ru"}) || got[1].Email != "anna@x.ru" || got[2].Email != "b@x.ru" {
		t.Fatalf("Members = %v, %v", got, err)
	}
	again, err := Members(ContactList{Members: got}.String())
	if err != nil || len(again) != 3 || again[0] != got[0] {
		t.Fatalf("again = %v, %v", again, err)
	}
	if _, err := Members("not an address"); err == nil {
		t.Fatal("a wrong address passed")
	}
}

// TestContactLists runs against the test database: a user's lists are made, changed, found by name and deleted, and
// nobody else reaches them; a department is offered with its people and those of its subdepartments.
func TestContactLists(t *testing.T) {
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
	var anna, ivan, gone, root, sales, east string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('cl1', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('cl2', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, active) VALUES ('cl3', 'Уволен', 'gone@go24.test', false) RETURNING id`).Scan(&gone))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Отдел продаж', $1) RETURNING id`, root).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Восток', $1) RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $4), ($5, $3)`, anna, ivan, sales, east, gone)
	must(err)
	s := NewStore(pool, "test-secret")

	if _, err := s.SaveContactList(ctx, anna, ContactList{Name: " ", Members: []Member{{Email: "a@x.ru"}}}); err == nil {
		t.Fatal("a list with no name")
	}
	if _, err := s.SaveContactList(ctx, anna, ContactList{Name: "Пусто"}); err == nil {
		t.Fatal("an empty list")
	}
	id, err := s.SaveContactList(ctx, anna, ContactList{Name: "Команда проекта", Members: []Member{{"Клиент", "c@x.ru"}}})
	must(err)
	_, err = s.SaveContactList(ctx, anna, ContactList{ID: id, Name: "Команда", Members: []Member{{"Клиент", "c@x.ru"}, {"", "d@x.ru"}}})
	must(err)
	if _, err := s.SaveContactList(ctx, ivan, ContactList{ID: id, Name: "Чужой", Members: []Member{{"", "e@x.ru"}}}); err != ErrNotFound {
		t.Fatalf("another's list changed: %v", err)
	}
	if _, err := s.ContactListOf(ctx, ivan, id); err != ErrNotFound {
		t.Fatalf("another's list read: %v", err)
	}
	c, err := s.ContactListOf(ctx, anna, id)
	must(err)
	if c.Name != "Команда" || len(c.Members) != 2 {
		t.Fatalf("list = %+v", c)
	}

	g, err := s.Groups(ctx, anna, "команд", 4)
	must(err)
	if len(g) != 1 || g[0].Kind != "list" || len(g[0].Members) != 2 {
		t.Fatalf("groups = %+v", g)
	}
	if g, err = s.Groups(ctx, ivan, "команд", 4); err != nil || len(g) != 0 {
		t.Fatalf("another's lists offered: %+v, %v", g, err)
	}
	g, err = s.Groups(ctx, ivan, "продаж", 4)
	must(err)
	if len(g) != 1 || g[0].Kind != "dept" || len(g[0].Members) != 2 {
		t.Fatalf("department = %+v (its people and the subdepartment's, the dismissed left out)", g)
	}
	if g, err = s.Groups(ctx, ivan, "нет такого", 4); err != nil || len(g) != 0 {
		t.Fatalf("groups = %+v, %v", g, err)
	}

	if err := s.DeleteContactList(ctx, ivan, id); err != ErrNotFound {
		t.Fatalf("another's list deleted: %v", err)
	}
	must(s.DeleteContactList(ctx, anna, id))
	if list, err := s.ContactLists(ctx, anna); err != nil || len(list) != 0 {
		t.Fatalf("lists = %v, %v", list, err)
	}
}
