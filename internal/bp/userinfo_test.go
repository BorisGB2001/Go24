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
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestUserInfoLog: «Информация о сотруднике» puts the card of the employee of a field into the process — the texts
// show it, a condition checks it, the next step goes to their manager; «Запись в отчёт» writes into the history.
func TestUserInfoLog(t *testing.T) {
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
	ann, ivan, head := newUser("ann"), newUser("ivan"), newUser("head")
	must2 := func(_ any, err error) { must(err) }
	must2(pool.Exec(ctx, `UPDATE users SET position = 'Юрист', mobile_phone = '+7 900 000-00-01' WHERE id = $1`, ivan))
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	root, _, err := cs.Tree(ctx)
	must(err)
	dept, err := cs.CreateDepartment(ctx, root.ID, "Юристы", head)
	must(err)
	must(cs.SetDepartments(ctx, "", ivan, []string{dept}))
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), cs)

	var ve *i18n.Error
	// a field of the form named as one of the employee's — taken
	if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Fields: []Field{{Key: "user_email", Label: "Почта", Kind: "string"}},
		Steps: []Step{{Kind: "userinfo", To: "starter"}}}); !errors.As(err, &ve) {
		t.Fatalf("a field user_email beside «Информация о сотруднике»: %v", err)
	}
	// a condition on the employee's fields before the step — not yet there
	if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Steps: []Step{{Kind: "if", Field: "user_position", Op: "filled"},
		{Kind: "userinfo", To: "starter"}}}); !errors.As(err, &ve) {
		t.Fatalf("a condition before «Информация о сотруднике»: %v", err)
	}
	if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Steps: []Step{{Kind: "log"}}}); !errors.As(err, &ve) {
		t.Fatalf("«Запись в отчёт» without a text: %v", err)
	}

	tpl, err := s.SaveTemplate(ctx, ann, Template{Name: "Доверенность", Active: true,
		Fields: []Field{{Key: "who", Label: "Кому", Kind: "user", Required: true}},
		Steps: []Step{{Kind: "userinfo", To: FieldPrefix + "who"},
			{Kind: "log", Text: "{user_name}, {user_position}, {user_department}, {user_mobile}, руководитель {user_manager}"},
			{Kind: "if", Field: "user_position", Op: "eq", Value: "юрист", Goto: 5},
			{Kind: "approve", To: FieldPrefix + "user_manager", Text: "Доверенность для {user_name}"},
			{Kind: "notify", To: "starter", Text: "Готово"}}})
	must(err)
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"who": ivan})
	must(err)
	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Values["user_email"] != "ivan@t" || x.Values["user_manager"] != head || x.Values["user_department"] != "Юристы" {
		t.Fatalf("values %+v", x.Values)
	}
	lines, err := s.Log(ctx, id)
	must(err)
	report := ""
	for _, l := range lines {
		if l.Kind == "report" {
			report = l.Text
		}
	}
	if report != "ivan, Юрист, Юристы, +7 900 000-00-01, руководитель head" {
		t.Fatalf("the report: %q", report)
	}
	if mine, _ := s.MyApprovals(ctx, head); len(mine) != 1 || mine[0].Text != "Доверенность для ivan" {
		t.Fatalf("the manager of the field is not asked: %+v", mine)
	}
	if f, ok := x.Field("user_manager"); !ok || f.Kind != "user" || UserInfoLabel(f.Key) != "bp.userinfo.manager" {
		t.Fatalf("the field user_manager: %+v", f)
	}
}
