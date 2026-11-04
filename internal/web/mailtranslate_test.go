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

// TestMailTrReply: the model's JSON gives the subject, the text and the language; an answer of the text alone keeps
// the subject, an unknown language — the one go24 told.
func TestMailTrReply(t *testing.T) {
	if s, x, f := mailTrReply("Sure:\n```json\n{\"from\":\"EN\",\"subject\":\"Заказ\",\"text\":\" Привет \"}\n```", "Order", "de"); s != "Заказ" || x != "Привет" || f != "en" {
		t.Fatalf("json: %q %q %q", s, x, f)
	}
	if s, x, f := mailTrReply("{\"from\":\"xx\",\"text\":\"Привет\"}", "Order", "de"); s != "Order" || x != "Привет" || f != "de" {
		t.Fatalf("no subject: %q %q %q", s, x, f)
	}
	if s, x, f := mailTrReply(" Hallo Anna ", "Order", "en"); s != "Order" || x != "Hallo Anna" || f != "en" {
		t.Fatalf("text alone: %q %q %q", s, x, f)
	}
}

// TestMailTranslateHTTP drives «Перевести» against the test database with a fake Anthropic: no AI connection — no
// banner and «Перевести» tells how to connect one; a letter in English offers «Перевести на русский», a Russian one
// does not; the translation (made of the text without its quote) is kept and shown in place of the letter's subject
// and text, a second one costs no call; «Всегда переводить» shows the kept one by itself and asks for a new one
// (data-auto), ?tr=0 — the original; «Никогда не переводить: английский» stops the offers; REST translates into another
// language; another sees neither the letter nor its translation.
func TestMailTranslateHTTP(t *testing.T) {
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
	letter := func(uid int, subject, body string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, thread, message_id)
			SELECT mailbox_id, id, $2::int, 'john@client.test', 'John', 'anna@go24.test', $3, $4, now(), 't' || $2::text, '<m' || $2::text || '@client.test>'
			FROM mail_folders WHERE id::text = $1 RETURNING id::text`, inbox, uid, subject, body).Scan(&id))
		return id
	}
	en := letter(1, "Order of boxes", "Hello Anna,\n\nWe would like to order ten boxes. Please send them to our office by Friday.\n\nThank you,\nJohn\n> an earlier quoted line")
	ru := letter(2, "Заказ", "Здравствуйте! Спасибо за письмо, заказ готов, отправим его завтра курьером на адрес вашего офиса.")
	en2 := letter(3, "Invoice", "Hello Anna, the invoice for the order is attached. Please let us know when you have paid it. Thank you!")

	var asked []string
	replies := []string{
		`{"type":"text","text":"{\"from\":\"en\",\"subject\":\"Заказ коробок\",\"text\":\"Здравствуйте, Анна!\\n\\nМы хотим заказать десять коробок.\"}"}`,
		`{"type":"text","text":"Hallo Anna,\nwir möchten zehn Kartons bestellen."}`,
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
	read := func(id, q string) string { return do(anna, "GET", "/mail/letters/"+id+"?folder="+inbox+q, nil, 200) }
	banner := func(p string) bool { return strings.Contains(p, `class="row mc-banner mtr"`) }

	// no AI connection: no banner; «Ещё → Перевести» is there and says how to connect one
	if p := read(en, ""); banner(p) || !strings.Contains(p, `action="/mail/letters/`+en+`/translate"`) {
		t.Fatalf("no AI connection:\n%s", p)
	}
	if e := do(anna, "POST", "/mail/letters/"+en+"/translate", url.Values{"lang": {"ru"}}, 422); !strings.Contains(e, "Подключения ИИ") {
		t.Fatalf("translating with no AI connection: %s", e)
	}
	_, err = aiStore.Save(ctx, anna, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	// the English letter offers its translation, the Russian one does not
	p := read(en, "")
	for _, want := range []string{"Письмо на языке: английский.", "Перевести на русский", `name="lang" value="ru"`, `name="from" value="en"`, "Никогда не переводить: английский"} {
		if !strings.Contains(p, want) {
			t.Fatalf("the English letter has no %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "data-auto") {
		t.Fatal("«Предлагать перевод» translates by itself")
	}
	if p := read(ru, ""); banner(p) {
		t.Fatal("a Russian letter offers a translation into Russian")
	}

	// «Перевести на русский»: the model gets the text without its quote; the translation is kept and costs no second call
	var tr mail.Translation
	must(json.Unmarshal([]byte(do(anna, "POST", "/mail/letters/"+en+"/translate", url.Values{"lang": {"ru"}, "folder": {inbox}}, 200)), &tr))
	if tr.Subject != "Заказ коробок" || tr.From != "en" || tr.Lang != "ru" || !strings.HasPrefix(tr.Text, "Здравствуйте, Анна!") {
		t.Fatalf("the translation %+v", tr)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "into Russian") || !strings.Contains(asked[0], "ten boxes") || strings.Contains(asked[0], "earlier quoted") {
		t.Fatalf("asked:\n%s", strings.Join(asked, "\n"))
	}
	var used int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage WHERE agent = 'mail' AND user_id = $1`, anna).Scan(&used))
	if used != 1 {
		t.Fatalf("the usage has %d calls", used)
	}
	do(anna, "POST", "/mail/letters/"+en+"/translate", url.Values{"lang": {"ru"}}, 200)
	if len(asked) != 1 {
		t.Fatal("a kept translation asked the model again")
	}

	// ?tr=ru: the translation in place of the letter; «Предлагать перевод» opens the letter as it is next time
	p = read(en, "&tr=ru")
	for _, want := range []string{"<h2>Заказ коробок", "Здравствуйте, Анна!<br><br>Мы хотим заказать десять коробок.", "Переведено: английский → русский.", "Показать оригинал", `data-mtr-show="0"`} {
		if !strings.Contains(p, want) {
			t.Fatalf("the translated letter has no %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "We would like to order") {
		t.Fatal("the translated letter shows the original text")
	}
	if p := read(en, ""); !strings.Contains(p, "We would like to order") || !strings.Contains(p, "Перевести на русский") {
		t.Fatal("«Предлагать перевод» shows the kept translation by itself")
	}

	// «Всегда переводить»: the kept translation by itself, a letter with none asks for it (data-auto); ?tr=0 — the original
	if s := do(anna, "GET", "/mail/settings?tab=translate", nil, 200); !strings.Contains(s, `action="/mail/settings/translate"`) || !strings.Contains(s, `value="ask" checked`) || !strings.Contains(s, "Язык интерфейса (русский)") {
		t.Fatalf("the settings:\n%s", s)
	}
	do(anna, "POST", "/mail/settings/translate", url.Values{"mode": {"loud"}}, 422)
	do(anna, "POST", "/mail/settings/translate", url.Values{"mode": {"always"}, "to": {""}}, 200)
	if p := read(en, ""); !strings.Contains(p, "<h2>Заказ коробок") || strings.Contains(p, "data-auto") {
		t.Fatalf("«Всегда переводить» with a kept translation:\n%s", p)
	}
	if p := read(en2, ""); !strings.Contains(p, "data-auto") || !strings.Contains(p, "Перевести на русский") {
		t.Fatalf("«Всегда переводить» does not ask for a new translation:\n%s", p)
	}
	if p := read(en2, "&tr=0"); strings.Contains(p, "data-auto") || !banner(p) {
		t.Fatalf("?tr=0 with «Всегда переводить»:\n%s", p)
	}
	if p := read(en, "&tr=0"); !strings.Contains(p, "<h2>Order of boxes") {
		t.Fatal("?tr=0 does not show the original")
	}

	// «Никогда не переводить: английский»
	do(anna, "POST", "/mail/letters/"+en2+"/translate", url.Values{"do": {"never"}, "from": {"en"}}, 200)
	if prefs, _ := srv.Mail.TranslatePrefs(ctx, anna); !slices.Equal(prefs.Skip, []string{"en"}) || prefs.Mode != "always" {
		t.Fatalf("prefs %+v", prefs)
	}
	if p := read(en2, ""); banner(p) {
		t.Fatal("a skipped language is still offered")
	}
	if s := do(anna, "GET", "/mail/settings?tab=translate", nil, 200); !strings.Contains(s, `name="skip" value="en" checked`) {
		t.Fatal("the settings do not show the skipped language")
	}

	// REST: into German; the model answering with the text alone keeps the subject; another's key gets no letter
	api := func(who, path, body string, want int, out any) {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"mail:read", "mail:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
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
	api(anna, "/api/v1/mail/messages/"+en+"/translate", `{"lang":"de"}`, 200, &tr)
	if tr.Lang != "de" || tr.Subject != "Order of boxes" || tr.From != "en" || !strings.Contains(tr.Text, "zehn Kartons") || len(asked) != 2 || !strings.Contains(asked[1], "into German") {
		t.Fatalf("REST: %+v", tr)
	}
	api(anna, "/api/v1/mail/messages/"+en+"/translate", `{}`, 200, &tr)
	if tr.Lang != "ru" || len(asked) != 2 {
		t.Fatalf("REST into the user's language: %+v", tr)
	}
	api(anna, "/api/v1/mail/messages/"+en+"/translate", `{"lang":"xx"}`, 422, nil)
	api(eve, "/api/v1/mail/messages/"+en+"/translate", `{"lang":"ru"}`, 404, nil)
	do(eve, "POST", "/mail/letters/"+en+"/translate", url.Values{"lang": {"ru"}}, 404)
	if _, ok, _ := srv.Mail.Translated(ctx, eve, en, "ru"); ok {
		t.Fatal("eve gets anna's translation")
	}
}
