package tasks

import (
	"context"
	"testing"
)

// TestMatchPeople: words start a word of the name, position, department or email; «ё», case and the wrong
// keyboard layout do not matter; name matches come first.
func TestMatchPeople(t *testing.T) {
	all := []PersonHit{
		{ID: "1", Name: "Пётр Иванов", Position: "Бухгалтер", Department: "Финансы", Email: "petr@x.ru"},
		{ID: "2", Name: "Анна Бухова", Position: "Юрист", Email: "anna@x.ru"},
		{ID: "3", Name: "John Smith", Position: "Developer", Email: "js@x.ru", recent: 1},
	}
	ids := func(list []PersonHit) (s string) {
		for _, p := range list {
			s += p.ID
		}
		return s
	}
	for q, want := range map[string]string{
		"":          "123",
		"петр":      "1",
		"ПЁТР ив":   "1",
		"бух":       "21", // Бухова by name before the Бухгалтер by position
		"финан":     "1",
		"anna":      "2",
		"gtnh":      "1", // «петр» typed in QWERTY
		"ощрт":      "3", // «john» typed in ЙЦУКЕН
		"етр":       "",  // not from the first letter
		"иванов пе": "1",
		"юрист бух": "2",
	} {
		if got := ids(MatchPeople(all, q, false)); got != want {
			t.Errorf("%q: %q, want %q", q, got, want)
		}
	}
	if got := ids(MatchPeople(all, "", true)); got != "3" {
		t.Errorf("recent without a query: %q", got)
	}
	if got := ids(MatchPeople(all, "x", true)); got != "312" { // everybody by the email, the recent first
		t.Errorf("recent, x: %q", got)
	}
}

// TestPeople: fired people are not offered (but named by IDs), the viewer only with Self, «Последние» are the
// people of the viewer's tasks with the viewer first, a department lists its people (Deep — with sub-departments),
// its head first, and is searched within.
func TestPeople(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name, position string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ($1, $1, $1 || '@test', $2) RETURNING id`,
			name, position).Scan(&id))
		return id
	}
	me, anna, boris, fired := newUser("Я Сам", ""), newUser("Анна Ёлкина", "Юрист"), newUser("Борис Петров", "Бухгалтер"), newUser("Уволенный", "")
	_, err := pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, fired)
	must(err)
	names := func(list []PersonHit, err error) (out []string) {
		t.Helper()
		must(err)
		for _, p := range list {
			out = append(out, p.Name)
		}
		return out
	}
	eq := func(what string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", what, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, want %v", what, got, want)
			}
		}
	}
	eq("all", names(s.People(ctx, me, PeopleQuery{})), "Анна Ёлкина", "Борис Петров")
	eq("self", names(s.People(ctx, me, PeopleQuery{Self: true})), "Анна Ёлкина", "Борис Петров", "Я Сам")
	eq("search", names(s.People(ctx, me, PeopleQuery{Q: "елк"})), "Анна Ёлкина")
	eq("position", names(s.People(ctx, me, PeopleQuery{Q: "бух"})), "Борис Петров")
	eq("fired", names(s.People(ctx, me, PeopleQuery{Q: "увол"})))
	eq("ids", names(s.People(ctx, me, PeopleQuery{IDs: []string{fired, anna}})), "Анна Ёлкина", "Уволенный")
	eq("limit", names(s.People(ctx, me, PeopleQuery{Self: true, Limit: 1, Offset: 1})), "Борис Петров")

	key, err := s.CreateProject(ctx, me, "PPL", "Люди", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, me, key)
	must(err)
	_, err = s.CreateTask(ctx, me, proj, NewTask{Title: "С Борисом", ResponsibleID: boris})
	must(err)
	eq("recent", names(s.People(ctx, me, PeopleQuery{Recent: true, Self: true})), "Я Сам", "Борис Петров")
	eq("recent search", names(s.People(ctx, me, PeopleQuery{Recent: true, Q: "а"})), "Анна Ёлкина")

	var root, sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Бухгалтерия', $1) RETURNING id`, root).Scan(&sub))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $4)`, anna, boris, root, sub)
	must(err)
	eq("department", names(s.People(ctx, me, PeopleQuery{Department: root})), "Анна Ёлкина")
	eq("deep", names(s.People(ctx, me, PeopleQuery{Department: root, Deep: true})), "Анна Ёлкина", "Борис Петров")
	_, err = pool.Exec(ctx, `UPDATE departments SET head_id = $1 WHERE id = $2`, boris, root)
	must(err)
	eq("head first", names(s.People(ctx, me, PeopleQuery{Department: root, Deep: true})), "Борис Петров", "Анна Ёлкина")
	eq("within", names(s.People(ctx, me, PeopleQuery{Department: sub, Deep: true, Q: "ан"})))
	list, err := s.People(ctx, me, PeopleQuery{Q: "бухгалтерия"})
	must(err)
	if len(list) != 1 || list[0].Department != "Бухгалтерия" {
		t.Fatalf("by department: %+v", list)
	}
}
