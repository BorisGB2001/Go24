package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// «Избранное» of the folder pane: Outlook's defaults, add and remove from a menu, the order, what is gone is left out.
func TestMailFavoritesHTTP(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folder := func(name, role string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, name, role).Scan(&id))
		return id
	}
	inbox, sent, drafts, projects := folder("INBOX", "inbox"), folder("Sent", "sent"), folder("Drafts", "drafts"), folder("Проекты", "")
	_, err = pool.Exec(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, subject, body, sent_at) VALUES ($1, $2, 1, 'c@x.test', 'Заказ', 'текст', now())`, box, inbox)
	must(err)

	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, path string, form url.Values, want int) string {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, ts.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	favRef := regexp.MustCompile(`data-fav="([^"]+)"`)
	favs := func(who string) []string {
		t.Helper()
		var refs []string
		for _, m := range favRef.FindAllStringSubmatch(do(who, "/mail", nil, 200), -1) {
			refs = append(refs, m[1])
		}
		return refs
	}
	same := func(got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("favorites = %v, want %v", got, want)
		}
	}

	// Outlook's defaults: Inbox, Sent, Drafts; the inbox's unread counter is there too
	same(favs(anna), inbox, sent, drafts)
	if p := do(anna, "/mail?folder="+inbox, nil, 200); strings.Count(p, `data-folder-id="`+inbox+`"`) != 2 || !strings.Contains(p, "Удалить из избранного") {
		t.Fatal("the inbox in «Избранное»")
	}
	// add a folder, a category and «С флажком»; another user's favorites are his own
	cat, err := srv.Mail.SaveCategory(ctx, anna, "", "Клиенты", "green")
	must(err)
	for _, ref := range []string{projects, mail.CategoryFolder + cat, mail.FlaggedFolder} {
		do(anna, "/mail/favorites", url.Values{"ref": {ref}, "on": {"1"}, "back": {"/mail"}}, 303)
	}
	same(favs(anna), inbox, sent, drafts, projects, mail.CategoryFolder+cat, mail.FlaggedFolder)
	same(favs(eve))
	// the order: up, down, by dragging
	do(anna, "/mail/favorites/move", url.Values{"ref": {projects}, "dir": {"up"}}, 303)
	do(anna, "/mail/favorites/move", url.Values{"ref": {inbox}, "dir": {"up"}}, 303) // the first stays
	same(favs(anna), inbox, sent, projects, drafts, mail.CategoryFolder+cat, mail.FlaggedFolder)
	do(anna, "/mail/favorites/move", url.Values{"order": {strings.Join([]string{mail.FlaggedFolder, inbox, drafts}, ",")}}, 303)
	same(favs(anna), mail.FlaggedFolder, inbox, drafts)
	// removed — and all removed stays so: the defaults come back no more
	do(anna, "/mail/favorites", url.Values{"ref": {drafts}, "back": {"/mail"}}, 303)
	same(favs(anna), mail.FlaggedFolder, inbox)
	do(anna, "/mail/favorites", url.Values{"ref": {mail.FlaggedFolder}}, 303)
	do(anna, "/mail/favorites", url.Values{"ref": {inbox}}, 303)
	same(favs(anna))
	if p := do(anna, "/mail?folder="+inbox, nil, 200); !strings.Contains(p, "mfav-empty") {
		t.Fatal("no hint in an empty «Избранное»")
	}
	// a deleted folder or category is left out
	do(anna, "/mail/favorites", url.Values{"ref": {mail.CategoryFolder + cat}, "on": {"1"}}, 303)
	do(anna, "/mail/favorites", url.Values{"ref": {"no-such-folder"}, "on": {"1"}}, 303)
	must(srv.Mail.DeleteCategory(ctx, anna, cat))
	same(favs(anna))
	do(anna, "/mail/favorites", url.Values{"ref": {""}}, 303) // back with the text of the error

	// a favorite person, as Outlook's: «Добавить отправителя в избранное» of a letter; the name of the latest letter, the
	// unread ones; the folder is the letters from the address in all folders but «Удалённые»
	trash := folder("Trash", "trash")
	var letter string
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, subject, body, sent_at)
		VALUES ($1, $2, 2, 'Boss@X.test', 'Начальник', 'План', 'текст', now()) RETURNING id::text`, box, inbox).Scan(&letter))
	_, err = pool.Exec(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, subject, body, sent_at, seen) VALUES
		($1, $2, 3, 'boss@x.test', 'Отчёт', 'текст', now() - interval '1 day', true), ($1, $3, 4, 'boss@x.test', 'Старое', 'текст', now(), false)`, box, projects, trash)
	must(err)
	if p := do(anna, "/mail?folder="+inbox+"&letter="+letter, nil, 200); !strings.Contains(p, "Добавить отправителя в избранное") ||
		!strings.Contains(p, `value="person:boss@x.test"`) {
		t.Fatal("no «Добавить отправителя в избранное» in the letter's menu")
	}
	do(anna, "/mail/favorites", url.Values{"ref": {"person:Boss@X.test"}, "on": {"1"}}, 303)
	do(anna, "/mail/favorites", url.Values{"ref": {"person:not-an-address"}, "on": {"1"}}, 303)
	same(favs(anna), "person:boss@x.test")
	_, err = pool.Exec(ctx, `UPDATE mail_messages SET seen = false WHERE id::text = $1`, letter) // opened above
	must(err)
	p := do(anna, "/mail?folder=person:boss@x.test", nil, 200)
	if !strings.Contains(p, `mfav-person on`) || !strings.Contains(p, `<span class="mf-name">Начальник</span><span class="cnt">1</span>`) ||
		!strings.Contains(p, "План") || !strings.Contains(p, "Отчёт") || strings.Contains(p, "Старое") || strings.Contains(p, "Заказ") {
		t.Fatalf("the person's folder: %.3000s", p)
	}
	if p := do(anna, "/mail?folder="+inbox+"&letter="+letter, nil, 200); !strings.Contains(p, "Удалить отправителя из избранного") {
		t.Fatal("no «Удалить отправителя из избранного»")
	}
	do(anna, "/mail/favorites", url.Values{"ref": {"person:boss@x.test"}}, 303)
	same(favs(anna))
}
