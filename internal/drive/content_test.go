package drive

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestDriveContent: «Поиск по содержимому» — a file is found by the words of its text (by the beginning of a word,
// with Russian endings), on the drive and in «Документы», with the place found; a new version is read anew.
func TestDriveContent(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool, tasks.NewStore(pool))
	my, _ := s.Open(ctx, anna, "my")
	add := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner)
			VALUES (gen_random_uuid()::text, $1, 3, $2, 'user', $2) RETURNING id`, name, anna).Scan(&id))
		return id
	}
	plan, deal, photo := add("план.docx"), add("договоры.docx"), add("фото.png")
	add("письмо.txt")

	due, err := s.Unindexed(ctx, 10)
	must(err)
	if len(due) != 4 {
		t.Fatalf("unindexed = %d, want 4", len(due))
	}
	key := map[string]string{}
	for _, u := range due {
		key[u.ID] = u.Key
	}
	must(s.SetContent(ctx, plan, key[plan], "Отчёт за квартал: поставки оборудования\x00 выросли, договоры подписаны."))
	must(s.SetContent(ctx, deal, key[deal], "Шаблон для клиентов"))
	must(s.SetContent(ctx, photo, key[photo], ""))
	if due, _ = s.Unindexed(ctx, 10); len(due) != 1 {
		t.Fatalf("unindexed after = %d, want 1", len(due))
	}

	names := func(files []File) string {
		var n []string
		for _, f := range files {
			n = append(n, f.Name)
		}
		return strings.Join(n, ",")
	}
	// «договор» — the name of one and a word (another ending) of the other; «постав обор» — beginnings of words
	_, files, err := s.Search(ctx, my, "договор")
	must(err)
	if got := names(files); !strings.Contains(got, "план.docx") || !strings.Contains(got, "договоры.docx") {
		t.Fatalf("search договор = %q", got)
	}
	_, files, _ = s.Search(ctx, my, "постав обор")
	if got := names(files); got != "план.docx" {
		t.Fatalf("search by beginnings = %q", got)
	}
	if _, files, _ = s.Search(ctx, my, "самолёт"); len(files) != 0 {
		t.Fatalf("search самолёт = %q", names(files))
	}
	sn, err := s.Snippets(ctx, []string{plan, deal, photo}, "договор")
	must(err)
	if len(sn) != 1 || !strings.Contains(sn[plan], "<mark>договоры</mark>") {
		t.Fatalf("snippets = %v (the found by the name have none)", sn)
	}
	if sn, _ = s.Snippets(ctx, []string{plan}, "<b>"); len(sn) != 0 {
		t.Fatalf("snippets of no words = %v", sn)
	}
	docs, err := s.Documents(ctx, anna, []string{"docx"}, DocFilter{Q: "оборудование"}, 10)
	must(err)
	if len(docs) != 1 || docs[0].ID != plan {
		t.Fatalf("documents by text = %v", docs)
	}

	// a new version: read anew; the text of the old one is not kept for it
	_, err = pool.Exec(ctx, `UPDATE files SET object_key = gen_random_uuid()::text WHERE id = $1`, plan)
	must(err)
	due, _ = s.Unindexed(ctx, 10)
	if len(due) != 2 {
		t.Fatalf("unindexed after a version = %d, want 2", len(due))
	}
	must(s.SetContent(ctx, plan, key[plan], "старое")) // the version read is not the current one: nothing kept
	if due, _ = s.Unindexed(ctx, 10); len(due) != 2 {
		t.Fatalf("an old version's text was kept")
	}
}
