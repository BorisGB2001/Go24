package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailSgReply: the model's JSON array, in a fence or after words; anything else — no answers.
func TestMailSgReply(t *testing.T) {
	if got := mailSgReply("Sure:\n```json\n[\"Да, подходит\", \"Нет\"]\n```"); !slices.Equal(got, []string{"Да, подходит", "Нет"}) {
		t.Fatalf("fenced: %q", got)
	}
	if got := mailSgReply("No reply is needed."); got != nil {
		t.Fatalf("words: %q", got)
	}
}

// TestMailRepliesHTTP drives «Предлагаемые ответы» against the test database with a fake Anthropic: no AI connection
// — no row; with one the letter asks for them (data-ask), the model gets its text without the quote, the answers are
// kept (trimmed, no repeats, at most three) and a second ask costs no call; the letter then shows them, each opening
// the reply with its text; a mailing is not offered any and costs no call; a letter the model finds needing no
// answer shows no row; the setting off — none; REST gives the kept ones; another gets no letter.
func TestMailRepliesHTTP(t *testing.T) {
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
	user := func(sub, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	anna, eve := user("anna", "Анна Петрова"), user("eve", "Ева")
	bundle, err := i18n.Load("")
	must(err)
	aiStore := ai.NewStore(pool, "secret")
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), I18n: bundle, AI: aiStore, Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, u := range []string{anna, eve} {
		var email string
		must(pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, u).Scan(&email))
		_, err = srv.Mail.Add(ctx, u, mail.Settings{Email: email, IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
		must(err)
	}
	var inbox string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) SELECT m.id, 'INBOX', 'inbox', true
		FROM mailboxes m WHERE m.email = 'anna@go24.test' RETURNING id::text`).Scan(&inbox))
	letter := func(uid int, subject, body, unsub string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, thread, message_id, unsubscribe)
			SELECT mailbox_id, id, $2::int, 'john@client.test', 'John', 'anna@go24.test', $3, $4, now(), 't' || $2::text, '<m' || $2::text || '@client.test>', $5
			FROM mail_folders WHERE id::text = $1 RETURNING id::text`, inbox, uid, subject, body, unsub).Scan(&id))
		return id
	}
	ask := letter(1, "Встреча", "Анна, добрый день!\n\nУдобно ли вам встретиться в четверг в 15:00?\n\nДжон\n> старая цитата", "")
	news := letter(2, "Скидки недели", "Только до воскресенья — скидки на всё!", "<https://shop.test/unsub>")
	info := letter(3, "Счёт оплачен", "Сообщаем: счёт №12 оплачен.", "")

	var asked []string
	replies := []string{
		`{"type":"text","text":"` + "```json\\n[\\\" Да,  четверг подходит \\\", \\\"Давайте в пятницу\\\", \\\"да, четверг подходит\\\", \\\"Пришлите повестку\\\", \\\"Не смогу\\\"]\\n```" + `"}`,
		`{"type":"text","text":"[]"}`,
	}
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, string(b))
		if len(asked) > len(replies) {
			http.Error(w, `{"error":{"message":"no more"}}`, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"content":[`+replies[len(asked)-1]+`],"usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer prov.Close()

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "text/html")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, b)
		}
		return string(b)
	}
	read := func(id string) string { return do(anna, "GET", "/mail/letters/"+id+"?folder="+inbox, nil, 200) }
	row := func(p string) bool { return strings.Contains(p, `class="row msreply"`) }
	suggest := func(id string) []string {
		t.Helper()
		var out struct{ Replies []string }
		must(json.Unmarshal([]byte(do(anna, "POST", "/mail/letters/"+id+"/suggest", url.Values{}, 200)), &out))
		return out.Replies
	}

	// no AI connection: no row
	if p := read(ask); row(p) {
		t.Fatalf("no AI connection:\n%s", p)
	}
	_, err = aiStore.Save(ctx, anna, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	// the letter asks for its answers; the model gets the text without the quote; trimmed, no repeats, three
	if p := read(ask); !row(p) || !strings.Contains(p, `data-sreply="/mail/letters/`+ask+`/suggest"`) || !strings.Contains(p, "data-ask") {
		t.Fatalf("the letter does not ask for its answers:\n%s", p)
	}
	want := []string{"Да, четверг подходит", "Давайте в пятницу", "Пришлите повестку"}
	if got := suggest(ask); !slices.Equal(got, want) {
		t.Fatalf("the answers %q", got)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "up to 3 replies") || !strings.Contains(asked[0], "четверг в 15:00") || strings.Contains(asked[0], "старая цитата") {
		t.Fatalf("asked:\n%s", strings.Join(asked, "\n"))
	}
	if got := suggest(ask); !slices.Equal(got, want) || len(asked) != 1 {
		t.Fatalf("a second ask: %q, %d calls", got, len(asked))
	}

	// kept: the letter shows them, each opening the reply with its text
	p := read(ask)
	if strings.Contains(p, "data-ask") || !strings.Contains(p, ">Давайте в пятницу</a>") ||
		!strings.Contains(p, "&amp;mode=reply&say=%d0%94%d0%b0%d0%b2%d0%b0%d0%b9%d1%82%d0%b5%20%d0%b2%20%d0%bf%d1%8f%d1%82%d0%bd%d0%b8%d1%86%d1%83") {
		t.Fatalf("the kept answers:\n%s", p)
	}
	c := do(anna, "GET", "/mail/compose?"+url.Values{"letter": {ask}, "mode": {"reply"}, "inline": {"1"}, "say": {"Давайте <b>в пятницу</b>"}}.Encode(), nil, 200)
	if !strings.Contains(c, "&lt;p&gt;Давайте &amp;lt;b&amp;gt;в пятницу&amp;lt;/b&amp;gt;&lt;/p&gt;") && !strings.Contains(c, "<p>Давайте &lt;b&gt;в пятницу&lt;/b&gt;</p>") {
		t.Fatalf("the reply has not the answer:\n%s", c)
	}

	// a mailing: no row, no call
	if p := read(news); row(p) {
		t.Fatal("a mailing is offered answers")
	}
	if got := suggest(news); len(got) != 0 || len(asked) != 1 {
		t.Fatalf("a mailing: %q, %d calls", got, len(asked))
	}

	// the model finds no answer needed: none kept, no row, no second call
	if got := suggest(info); len(got) != 0 || len(asked) != 2 {
		t.Fatalf("no answer needed: %q, %d calls", got, len(asked))
	}
	if p := read(info); row(p) {
		t.Fatal("a letter needing no answer shows the row")
	}
	suggest(info)
	if len(asked) != 2 {
		t.Fatal("a kept «no answer» asked the model again")
	}

	// REST gives the kept ones; another's key gets no letter
	api := func(who, path string, want int, out any) {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"mail:read", "mail:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("POST %s: %d, want %d: %s", path, res.StatusCode, want, b)
		}
		if out != nil {
			must(json.Unmarshal(b, out))
		}
	}
	var out struct{ Replies []string }
	api(anna, "/api/v1/mail/messages/"+ask+"/suggest", 200, &out)
	if !slices.Equal(out.Replies, want) {
		t.Fatalf("REST: %q", out.Replies)
	}
	api(eve, "/api/v1/mail/messages/"+ask+"/suggest", 404, nil)
	do(eve, "POST", "/mail/letters/"+ask+"/suggest", url.Values{}, 404)

	// «Показывать предлагаемые ответы» off: no row; the settings show it
	if s := do(anna, "GET", "/mail/settings?tab=compose", nil, 200); !strings.Contains(s, `name="suggest" value="1" checked`) {
		t.Fatalf("the settings:\n%s", s)
	}
	do(anna, "POST", "/mail/settings", url.Values{"undo_seconds": {"5"}}, 200)
	if prefs, _ := srv.Mail.Prefs(ctx, anna); prefs.Suggest || prefs.UndoSeconds != 5 {
		t.Fatalf("prefs %+v", prefs)
	}
	if p := read(ask); row(p) {
		t.Fatal("the setting off still shows the answers")
	}
	if s := do(anna, "GET", "/mail/settings?tab=compose", nil, 200); strings.Contains(s, `name="suggest" value="1" checked`) {
		t.Fatal("the settings show it on")
	}
}
