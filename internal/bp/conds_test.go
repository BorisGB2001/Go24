package bp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestConds: «Условие» and «Цикл» with more conditions joined by «и» / «или» — «и» binds first.
func TestConds(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))

	// sum > 1000 и city = Москва или vip заполнено → «дорого», else «дёшево»
	tpl := Template{Name: "Условия", Active: true,
		Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "number"}, {Key: "city", Label: "Город", Kind: "string"},
			{Key: "vip", Label: "VIP", Kind: "string"}, {Key: "res", Label: "Итог", Kind: "string", Scope: "var"}},
		Steps: []Step{{Kind: "if", Field: "sum", Op: "gt", Value: "1000", Goto: 4,
			More: []Cond{{Field: "city", Op: "eq", Value: "москва"}, {Or: true, Field: "vip", Op: "filled"}}},
			{Kind: "set", Field: "res", Value: "дорого"},
			{Kind: "end"},
			{Kind: "set", Field: "res", Value: "дёшево"}}}
	var ve *i18n.Error
	for _, c := range []Cond{{Field: "nope", Op: "eq"}, {Field: "city", Op: "like"}} {
		bad := tpl
		bad.Steps = append([]Step{}, tpl.Steps...)
		bad.Steps[0].More = []Cond{c}
		if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
			t.Fatalf("saved a condition %+v: %v", c, err)
		}
	}
	bad := tpl
	bad.Steps = append([]Step{}, tpl.Steps...)
	bad.Steps[1].More = []Cond{{Field: "city", Op: "eq"}}
	if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
		t.Fatalf("more conditions of «Изменить переменную»: %v", err)
	}
	id, err := s.SaveTemplate(ctx, boss, tpl)
	must(err)
	if got, _ := s.Template(ctx, id); len(got.Steps[0].More) != 2 || !got.Steps[0].More[1].Or {
		t.Fatalf("saved: %+v", got.Steps[0])
	}
	for _, c := range []struct {
		sum, city, vip, want string
	}{
		{"5000", "Москва", "", "дорого"},   // a и b
		{"5000", "Казань", "", "дёшево"},   // a, not b, not c
		{"10", "Москва", "", "дёшево"},     // b only
		{"10", "Казань", "да", "дорого"},   // c only
		{"5000", "Казань", "да", "дорого"}, // a и not b, но c
		{"", "", "", "дёшево"},
	} {
		pid, err := s.Start(ctx, boss, id, "", map[string]string{"sum": c.sum, "city": c.city, "vip": c.vip})
		must(err)
		if x, _ := s.Instance(ctx, boss, pid); x.Status != "done" || x.Values["res"] != c.want {
			t.Fatalf("%+v: %s %q", c, x.Status, x.Values["res"])
		}
	}

	// «Условие группы пользователей»: the employee is one of the people or a member of a department (a sub-one too,
	// its head as well)
	var sales, east, ann, bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@t') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Восток', $1, $2) RETURNING id`, sales, eve).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, ann, east)
	must(err)
	who := Template{Name: "Кто", Active: true,
		Fields: []Field{{Key: "u", Label: "Сотрудник", Kind: "user"}, {Key: "res", Label: "Итог", Kind: "string", Scope: "var"}},
		Steps: []Step{{Kind: "if", Field: "u", Op: "in", Value: "boss@t; {dept}", Goto: 4},
			{Kind: "set", Field: "res", Value: "свой"},
			{Kind: "end"},
			{Kind: "set", Field: "res", Value: "чужой"}}}
	for _, c := range []struct{ user, dept, want string }{
		{ann, "Продажи", "свой"}, {ann, "dept:" + east, "свой"}, {ann, "dept:" + sales, "свой"}, {ann, "Склад", "чужой"},
		{eve, "Продажи", "свой"}, {bob, "Продажи", "чужой"}, {boss, "Склад", "свой"},
	} {
		w := who
		w.Steps = append([]Step{}, who.Steps...)
		w.Steps[0].Value = strings.ReplaceAll(w.Steps[0].Value, "{dept}", c.dept)
		id, err := s.SaveTemplate(ctx, boss, w)
		must(err)
		pid, err := s.Start(ctx, boss, id, "", map[string]string{"u": c.user})
		must(err)
		if x, _ := s.Instance(ctx, boss, pid); x.Status != "done" || x.Values["res"] != c.want {
			t.Fatalf("%+v: %s %q", c, x.Status, x.Values["res"])
		}
	}
}
