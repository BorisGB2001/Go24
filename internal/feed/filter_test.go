package feed

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestPeriod: the dates of the filter — a day, the week from Monday, the month, the quarter, «с — по» with «по»
// included.
func TestPeriod(t *testing.T) {
	now := time.Date(2026, 10, 29, 15, 4, 0, 0, time.UTC) // Thursday
	day := func(m time.Month, d int) time.Time { return time.Date(2026, m, d, 0, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		name, from, to string
		a, b           time.Time
	}{
		{"today", "", "", day(10, 29), day(10, 30)},
		{"yesterday", "", "", day(10, 28), day(10, 29)},
		{"week", "", "", day(10, 26), day(11, 2)},
		{"month", "", "", day(10, 1), day(11, 1)},
		{"quarter", "", "", day(10, 1), day(1, 1).AddDate(1, 0, 0)},
		{"range", "2026-10-01", "2026-10-10", day(10, 1), day(10, 11)},
		{"", "2026-10-05", "", day(10, 5), time.Time{}},
		{"range", "", "bad", time.Time{}, time.Time{}},
	} {
		a, b := Period(c.name, c.from, c.to, now)
		if !a.Equal(c.a) || !b.Equal(c.b) {
			t.Errorf("%s %s–%s = %v – %v, want %v – %v", c.name, c.from, c.to, a, b, c.a, c.b)
		}
	}
	// Sunday is the end of the week
	if a, _ := Period("week", "", "", time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)); !a.Equal(day(10, 26)) {
		t.Errorf("week of Sunday from %v", a)
	}
}

// TestFilterMentionsTo: «Упоминания» — the posts where the viewer was mentioned in the text or a comment; «Кому» — the
// posts to a person (by name or through their department) or to a department; the end of a period; the saved
// filters.
func TestFilterMentionsTo(t *testing.T) {
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
	user := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, n).Scan(&id))
		return id
	}
	boss, anna, ivan := user("boss"), user("anna"), user("ivan")
	var sales string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, sales)
	must(err)
	s := NewStore(pool, tasks.NewStore(pool))

	inText, err := s.CreatePost(ctx, boss, "", "", "Анна, посмотри", false, anna)
	must(err)
	inComment, err := s.CreatePost(ctx, boss, "", "", "План", false)
	must(err)
	_, err = s.AddComment(ctx, ivan, inComment, "Анна, твоё мнение?", anna)
	must(err)
	toSales, err := s.CreatePostTo(ctx, boss, "", Audience{Depts: []string{sales}}, "", "Продажам", false)
	must(err)
	toIvan, err := s.CreatePostTo(ctx, boss, "", Audience{Users: []string{ivan}}, "", "Ивану", false)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE feed_posts SET created_at = now() - interval '3 days' WHERE id = $1`, inText)
	must(err)

	ids := func(u string, f Filter) []string {
		t.Helper()
		items, err := s.List(ctx, u, f)
		must(err)
		var out []string
		for _, it := range items {
			if it.Post != nil {
				out = append(out, it.Post.ID)
			}
		}
		slices.Sort(out)
		return out
	}
	want := func(got []string, w ...string) {
		t.Helper()
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("got %v, want %v", got, w)
		}
	}
	want(ids(anna, Filter{Mentions: true}), inText, inComment)
	want(ids(ivan, Filter{Mentions: true}))
	want(ids(boss, Filter{To: []string{anna}}), toSales)
	want(ids(boss, Filter{To: []string{"dept:" + sales}}), toSales)
	want(ids(boss, Filter{To: []string{ivan, "dept:" + sales}}), toSales, toIvan)
	want(ids(anna, Filter{Till: time.Now().Add(-24 * time.Hour)}), inText)
	want(ids(anna, Filter{From: time.Now().Add(-24 * time.Hour), Mentions: true}), inComment)

	must(s.SaveFilter(ctx, anna, SavedFilter{Name: "Мне", Query: "view=mentions"}))
	must(s.SaveFilter(ctx, anna, SavedFilter{Name: "Продажи", Query: "dest=dept%3A" + sales}))
	must(s.SaveFilter(ctx, anna, SavedFilter{Name: "Мне", Query: "view=mentions&period=week"}))
	if err := s.SaveFilter(ctx, anna, SavedFilter{Name: "  ", Query: "q=x"}); err != ErrFilterName {
		t.Fatalf("no name: %v", err)
	}
	list, err := s.SavedFilters(ctx, anna)
	must(err)
	if len(list) != 2 || list[0].Name != "Мне" || list[0].Query != "view=mentions&period=week" || list[1].Name != "Продажи" {
		t.Fatalf("saved = %+v", list)
	}
	must(s.DeleteFilter(ctx, anna, "Мне"))
	must(s.DeleteFilter(ctx, anna, "Продажи"))
	if list, err := s.SavedFilters(ctx, anna); err != nil || len(list) != 0 {
		t.Fatalf("after deleting: %+v %v", list, err)
	}
	if list, err := s.SavedFilters(ctx, boss); err != nil || len(list) != 0 {
		t.Fatalf("another's: %+v %v", list, err)
	}
}
