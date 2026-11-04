package mail

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestPolicies(t *testing.T) {
	for _, p := range Policies {
		if got, ok := PolicyOf(p.Key); !ok || got != p {
			t.Errorf("PolicyOf(%s) = %v %v", p.Key, got, ok)
		}
	}
	if checkPolicy("") != nil || checkPolicy("del_1m") != nil || checkPolicy("del_2d") == nil {
		t.Error("checkPolicy")
	}
	if r := (Retention{Own: "never", Folder: "del_1w"}); r.Policy() != "never" || r.Action() != "" {
		t.Errorf("own policy: %s %s", r.Policy(), r.Action())
	}
	if r := (Retention{Folder: "arch_1y"}); r.Policy() != "arch_1y" || r.Action() != "archive" {
		t.Errorf("folder policy: %s %s", r.Policy(), r.Action())
	}
}

// TestRetention against the test database: a folder's policy moves its old letters and keeps the new ones, a
// letter's own policy is stronger (never — kept; its own — in any folder), archiving leaves «Архив» be, drafts stay;
// another's folder and letters are not found.
func TestRetention(t *testing.T) {
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
	var bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@go24.test') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, bob, Settings{Email: "bob@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folders := map[string]string{}
	for _, role := range []string{"inbox", "trash", "archive", "drafts", ""} {
		var id string
		name := role
		if name == "" {
			name = "Projects"
		}
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, name, role).Scan(&id))
		folders[role] = id
	}
	uid := int64(0)
	letter := func(folder string, days int) string {
		t.Helper()
		uid++
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, subject, body, sent_at)
			VALUES ($1, $2, $3, 'ann@client.test', 'Тема', 'Текст', now() - make_interval(days => $4)) RETURNING id::text`, box, folders[folder], uid, days).Scan(&id))
		return id
	}
	roleOf := func(id string) string {
		t.Helper()
		var role string
		must(pool.QueryRow(ctx, `SELECT f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.id::text = $1`, id).Scan(&role))
		return role
	}
	old, fresh, kept := letter("inbox", 40), letter("inbox", 3), letter("inbox", 400)
	own := letter("", 10)              // its own «Удалить через 1 неделю» in a folder without a policy
	archived := letter("archive", 800) // «Архив» with an archiving policy
	draft := letter("drafts", 100)
	_, err = pool.Exec(ctx, `INSERT INTO mail_drafts (id, user_id) VALUES ($1, $2)`, draft, bob)
	must(err)

	if err := s.SetFolderPolicy(ctx, bob, folders["inbox"], "del_2d"); err == nil {
		t.Error("an unknown policy was set")
	}
	if err := s.SetFolderPolicy(ctx, eve, folders["inbox"], "del_1m"); err != ErrNotFound {
		t.Errorf("another's folder: %v", err)
	}
	if _, err := s.SetLetterPolicy(ctx, eve, []string{old}, "never"); err != ErrNotFound {
		t.Errorf("another's letter: %v", err)
	}
	must(s.SetFolderPolicy(ctx, bob, folders["inbox"], "del_1m"))
	must(s.SetFolderPolicy(ctx, bob, folders["archive"], "arch_1y"))
	must(s.SetFolderPolicy(ctx, bob, folders["drafts"], "del_1w"))
	if n, err := s.SetLetterPolicy(ctx, bob, []string{kept}, "never"); err != nil || n != 1 {
		t.Fatalf("SetLetterPolicy: %d %v", n, err)
	}
	_, err = s.SetLetterPolicy(ctx, bob, []string{own}, "del_1w")
	must(err)
	if f, err := s.Folder(ctx, bob, folders["inbox"]); err != nil || f.Policy != "del_1m" {
		t.Errorf("the folder's policy: %q %v", f.Policy, err)
	}
	r, err := s.LetterRetention(ctx, bob, fresh)
	must(err)
	if r.Policy() != "del_1m" || r.Own != "" || r.Expires == nil || r.Expires.Before(time.Now().AddDate(0, 0, 26)) {
		t.Errorf("fresh: %+v", r)
	}
	if r, _ := s.LetterRetention(ctx, bob, kept); r.Policy() != "never" || r.Expires != nil {
		t.Errorf("kept: %+v", r)
	}
	if _, err := s.LetterRetention(ctx, eve, fresh); err != ErrNotFound {
		t.Errorf("another's letter retention: %v", err)
	}

	n, errs := s.RetentionDue(ctx)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if n != 2 {
		t.Errorf("moved %d, want 2", n)
	}
	for id, want := range map[string]string{old: "trash", fresh: "inbox", kept: "inbox", own: "trash", archived: "archive", draft: "drafts"} {
		if got := roleOf(id); got != want {
			t.Errorf("letter %s in %q, want %q", id, got, want)
		}
	}
	// back to the folder's policy
	_, err = s.SetLetterPolicy(ctx, bob, []string{kept}, "")
	must(err)
	if n, _ := s.RetentionDue(ctx); n != 1 || roleOf(kept) != "trash" {
		t.Errorf("kept, the folder's policy again: %d %s", n, roleOf(kept))
	}
}
