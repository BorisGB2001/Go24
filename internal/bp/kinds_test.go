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

// TestFieldKinds: the parameters — «Да/Нет», «Список», «Сотрудник», «Дата и время», defaults; an
// approval goes to the employee of a user field, the texts show «Да» and the name, a condition checks a bool.
func TestFieldKinds(t *testing.T) {
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
	boss, ann, lawyer := newUser("boss"), newUser("ann"), newUser("lawyer")
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts, crm.NewStore(pool), company.NewStore(pool))
	s.Tr = func(key string, args ...any) string {
		return map[string]string{"bp.bool.Y": "Да", "bp.bool.N": "Нет"}[key]
	}

	var ve *i18n.Error
	bad := []Field{
		{Key: "city", Label: "Город", Kind: "select"},                                       // a list without options
		{Key: "x", Label: "X", Kind: "string", Options: []string{"a"}},                      // options of a string
		{Key: "city", Label: "Город", Kind: "select", Options: []string{"А"}, Default: "Б"}, // a default not among them
		{Key: "urgent", Label: "Срочно", Kind: "bool", Default: "может быть"},               // neither yes nor no
		{Key: "a:b", Label: "A", Kind: "string"},                                            // «:» in a key
		{Key: "who", Label: "Кто", Kind: "user", Default: "not an id"},                      // a user's default not an id
	}
	for _, f := range bad {
		if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Fields: []Field{f},
			Steps: []Step{{Kind: "notify", To: "starter", Text: "x"}}}); !errors.As(err, &ve) {
			t.Fatalf("a wrong field %+v saved: %v", f, err)
		}
	}
	// a step's employee from a field that is not a user field
	if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Fields: []Field{{Key: "who", Label: "Кто", Kind: "string"}},
		Steps: []Step{{Kind: "approve", To: FieldPrefix + "who", Text: "x"}}}); !errors.As(err, &ve) {
		t.Fatalf("a step to a string field: %v", err)
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Договор", Active: true,
		Fields: []Field{{Key: "lawyer", Label: "Юрист", Kind: "user", Required: true, Default: lawyer},
			{Key: "urgent", Label: "Срочно", Kind: "bool", Default: "Y"},
			{Key: "city", Label: "Город", Kind: "select", Options: []string{"Москва", "Казань"}, Default: "Москва"},
			{Key: "at", Label: "Подписание", Kind: "datetime"}},
		Steps: []Step{{Kind: "approve", To: FieldPrefix + "lawyer", Text: "Договор: срочно — {urgent}, {city}, {at}, юрист {lawyer}"},
			{Kind: "if", Field: "urgent", Op: "eq", Value: "yes", Goto: 4},
			{Kind: "notify", To: "starter", Text: "Срочный"},
			{Kind: "notify", To: "starter", Text: "Готово"}}})
	must(err)
	if _, err := s.Start(ctx, ann, tpl, "", map[string]string{"city": "Самара"}); !errors.As(err, &ve) {
		t.Fatalf("an option not of the list: %v", err)
	}
	if _, err := s.Start(ctx, ann, tpl, "", map[string]string{"at": "завтра"}); !errors.As(err, &ve) {
		t.Fatalf("a wrong time: %v", err)
	}
	// nothing sent: the defaults; the approval goes to the lawyer of the field
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"at": "2026-11-02 15:30"})
	must(err)
	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Values["lawyer"] != lawyer || x.Values["urgent"] != "Y" || x.Values["city"] != "Москва" || x.Values["at"] != "2026-11-02T15:30" ||
		len(x.Fields) != 4 {
		t.Fatalf("values %+v, fields %d", x.Values, len(x.Fields))
	}
	mine, _ := s.MyApprovals(ctx, lawyer)
	if len(mine) != 1 || mine[0].Text != "Договор: срочно — Да, Москва, 2026-11-02 15:30, юрист lawyer" {
		t.Fatalf("the lawyer's approvals = %+v", mine)
	}
	must(s.Decide(ctx, lawyer, mine[0].ID, "approve", ""))
	var texts []string
	rows, err := pool.Query(ctx, `SELECT body FROM notifications WHERE kind = 'bp' AND user_id = $1 ORDER BY created_at`, ann)
	must(err)
	for rows.Next() {
		var v string
		must(rows.Scan(&v))
		texts = append(texts, v)
	}
	if len(texts) < 2 || texts[0] != "Срочный" {
		t.Fatalf("ann is told %q: the condition «Срочно = yes» on a bool", texts)
	}

	// the box unchecked (only the hidden «N») — not urgent; another lawyer
	id, err = s.Start(ctx, ann, tpl, "", map[string]string{"urgent": "N", "lawyer": boss})
	must(err)
	x, _ = s.Instance(ctx, ann, id)
	if x.Values["urgent"] != "N" {
		t.Fatalf("urgent = %q", x.Values["urgent"])
	}
	if mine, _ := s.MyApprovals(ctx, boss); len(mine) != 1 {
		t.Fatalf("the boss of the field is not asked: %+v", mine)
	}
	// the template changes: the process keeps its fields
	tp, _ := s.Template(ctx, tpl)
	tp.Fields[2].Options = append(tp.Fields[2].Options, "Самара")
	_, err = s.SaveTemplate(ctx, boss, tp)
	must(err)
	if x, _ := s.Instance(ctx, ann, id); len(x.Fields[2].Options) != 2 {
		t.Fatalf("the process's fields follow the template: %+v", x.Fields[2])
	}

	// a file of the template: a user's default by e-mail; one not found here — no default
	f, err := s.Export(ctx, tpl)
	must(err)
	if f.People[lawyer] != "lawyer@t" {
		t.Fatalf("the file's people %+v", f.People)
	}
}
