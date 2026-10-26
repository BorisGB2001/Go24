package legacyimport

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/anon"
	"go24/internal/db"
)

func e(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// TestImport imports a tiny fake export twice and checks that no real personal data reaches the database.
func TestImport(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	dir := t.TempDir()
	write := func(name string, rows ...[]string) {
		var b strings.Builder
		for _, r := range rows {
			b.WriteString(strings.Join(r, "\t") + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("users.tsv",
		[]string{"17", e("Сергей"), e("Кузнецов"), e("Петрович"), "M", e("kuznetsov@firm.ru"), e("kuznetsov")},
		[]string{"25", e("Ольга"), e("Смирнова"), e(""), "F", e("smirnova@firm.ru"), e("olga")})
	write("tasks.tsv",
		[]string{"101", e("Позвонить Кузнецову"), e("[B]Срочно[/B] +7 (914) 123-45-67, [USER=25]Ольга Смирнова[/USER] в курсе. ООО «Байкал-Сервис»"),
			"5", "2", "25", "17", "0", "Y", "2025-03-01 18:00:00", "NULL", "NULL", "2025-02-01 10:00:00", "2025-02-02 10:00:00", "2025-02-03 10:00:00", "25"},
		[]string{"102", e("Подзадача"), e(""), "2", "1", "17", "17", "101", "N", "NULL", "NULL", "NULL", "2025-02-05 09:00:00", "NULL", "NULL", "0"})
	write("members.tsv", []string{"101", "25", "O"}, []string{"101", "17", "R"}, []string{"101", "25", "U"})
	write("comments.tsv",
		[]string{"9001", "101", "17", "2025-02-02 11:00:00", e("Ольга, пишите на smirnova@firm.ru или 8-924-111-22-33")},
		[]string{"9002", "101", "NULL", "2025-02-03 11:00:00", e("Ответ по почте от клиента")})

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
	var owner string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('real', 'Owner', 'o@x') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	msk, _ := time.LoadLocation("Europe/Moscow")
	opts := func() Options {
		return Options{Dir: dir, ProjectKey: "EXT", ProjectName: "Импорт", OwnerID: owner, Location: msk, Anon: anon.New("seed", "portal.import")}
	}
	st, err := Import(ctx, pool, opts())
	if err != nil {
		t.Fatal(err)
	}
	if st.Users != 2 || st.Tasks != 2 || st.Parents != 1 || st.Participants != 1 || st.Comments != 2 {
		t.Fatalf("stats = %v", st)
	}
	if _, err := Import(ctx, pool, opts()); err != nil { // re-runnable
		t.Fatal(err)
	}
	var tasks, comments int
	pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tasks), (SELECT count(*) FROM task_comments)`).Scan(&tasks, &comments)
	if tasks != 2 || comments != 2 {
		t.Fatalf("after re-import: tasks=%d comments=%d", tasks, comments)
	}

	var status, desc, title string
	var closed *time.Time
	var control bool
	pool.QueryRow(ctx, `SELECT status, description, title, closed_at, requires_control FROM tasks WHERE number = 101`).
		Scan(&status, &desc, &title, &closed, &control)
	if status != "completed" || closed == nil || !control || strings.Contains(desc, "[B]") || !strings.Contains(desc, "Байкал-Сервис") {
		t.Fatalf("task 101: status=%s closed=%v control=%v desc=%q", status, closed, control, desc)
	}

	if n, err := WriteAuthentikBlueprint(ctx, pool, filepath.Join(dir, "bp", "users.yaml")); err != nil || n != 2 {
		t.Fatalf("blueprint: n=%d err=%v", n, err)
	}
	bp, err := os.ReadFile(filepath.Join(dir, "bp", "users.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(bp), "model: authentik_core.user") != 2 || !strings.Contains(string(bp), `"user17@portal.import"`) {
		t.Fatalf("blueprint:\n%s", bp)
	}
	for _, leak := range []string{"Кузнецов", "Смирнов", "firm.ru"} {
		if strings.Contains(string(bp), leak) {
			t.Errorf("blueprint leaks %q", leak)
		}
	}

	// attachments: empty placeholders with anonymized names; re-runnable
	write("files.tsv",
		[]string{"501", "101", e("Договор Кузнецов.pdf"), "application/pdf", "17", "task", "2025-02-01 10:00:00"},
		[]string{"502", "101", e("скан.png"), "", "0", "comment", "NULL"},
		[]string{"503", "999", e("чужая.docx"), "", "17", "task", "NULL"}) // task not imported → skipped
	var stored []string
	put := func(_ context.Context, key string, r io.Reader, size int64, _ string) error {
		b, _ := io.ReadAll(r)
		if size != 0 || len(b) != 0 {
			t.Fatalf("placeholder must be empty, got %d bytes", len(b))
		}
		stored = append(stored, key)
		return nil
	}
	n, err := ImportFiles(ctx, pool, put, opts())
	if err != nil || n != 2 || len(stored) != 2 {
		t.Fatalf("files: n=%d stored=%v err=%v", n, stored, err)
	}
	if n, _ := ImportFiles(ctx, pool, put, opts()); n != 0 {
		t.Fatalf("re-import added %d files", n)
	}
	var fname, fct string
	var fsize int64
	pool.QueryRow(ctx, `SELECT name, content_type, size FROM files WHERE legacy_id = 502`).Scan(&fname, &fct, &fsize)
	if fname != "скан.png" || fct != "image/png" || fsize != 0 {
		t.Fatalf("file 502: %q %q %d", fname, fct, fsize)
	}

	// Nothing personal may be left anywhere in text columns.
	var dump string
	pool.QueryRow(ctx, `SELECT string_agg(x, ' | ') FROM (
		SELECT name || ' ' || email AS x FROM users
		UNION ALL SELECT title || ' ' || description FROM tasks
		UNION ALL SELECT body FROM task_comments
		UNION ALL SELECT name || ' ' || object_key FROM files) s`).Scan(&dump)
	for _, leak := range []string{"Кузнецов", "Смирнов", "Сергей", "Ольга", "Петрович", "firm.ru", "123-45-67", "111-22-33"} {
		if strings.Contains(dump, leak) {
			t.Errorf("leak %q in: %s", leak, dump)
		}
	}
}
