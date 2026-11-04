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
	"regexp"
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

// TestMailCopilotHTTP drives CoPilot of the mail against the test database with a fake Anthropic: no AI connection —
// the pane says how to connect one; «Кратко о письме» — the model is told the open letter, reads it with its
// conversation (it stays unread) and answers; a draft reply waits for «Подтвердить» and lands in «Черновики» as a
// reply (Re:, to the sender, the quote); a rejected action changes nothing; the agent does not delete letters, nor
// reads another's; another sees neither the conversation nor the letters.
func TestMailCopilotHTTP(t *testing.T) {
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
	var inbox, first, order string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) SELECT m.id, 'INBOX', 'inbox', true
		FROM mailboxes m WHERE m.email = 'anna@go24.test' RETURNING id::text`).Scan(&inbox))
	letter := func(uid int, subject, body, at string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, thread, message_id)
			SELECT mailbox_id, id, $2::int, 'ivan@client.test', 'Иван', 'anna@go24.test', $3, $4, now() - $5::interval, 'order', '<m' || $2::text || '@client.test>'
			FROM mail_folders WHERE id::text = $1 RETURNING id::text`, inbox, uid, subject, body, at).Scan(&id))
		return id
	}
	first = letter(1, "Заказ", "Нужны коробки, сколько стоят?", "2 days")
	order = letter(2, "Re: Заказ", "Беру 10 коробок, привезите до пятницы.", "1 hour")

	// the fake provider answers in turn; each request is kept
	var asked []string
	replies := []string{
		`{"type":"text","text":"Смотрю письмо"},{"type":"tool_use","id":"r1","name":"read_letter","input":{"id":"` + order + `","conversation":true}}`,
		`{"type":"text","text":"Иван берёт **10 коробок** и просит привезти до пятницы."}`,
		`{"type":"tool_use","id":"d1","name":"draft_reply","input":{"id":"` + order + `","text":"Здравствуйте, Иван!\nПривезём в пятницу."}}`,
		`{"type":"text","text":"Черновик готов."}`,
		`{"type":"tool_use","id":"l1","name":"list_letters","input":{"unread":true}},{"type":"tool_use","id":"m1","name":"mark_letters","input":{"ids":["` + order + `"],"action":"flag"}}`,
		`{"type":"text","text":"Хорошо, оставляю как есть."}`,
		`{"type":"text","text":"Одно непрочитанное."}`,
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

	if page := do(anna, "GET", "/mail", nil, 200); !strings.Contains(page, "data-ai-toggle") || !strings.Contains(page, `id="mcopilot"`) || !strings.Contains(page, "js/mailai.js") {
		t.Fatal("the mail has no CoPilot")
	}
	if pane := do(anna, "GET", "/mail/copilot", nil, 200); !strings.Contains(pane, "подключите нейросеть") || !strings.Contains(pane, `href="/settings/ai"`) || strings.Contains(pane, "data-ai-form") {
		t.Fatalf("CoPilot with no AI connection:\n%s", pane)
	}
	_, err = aiStore.Save(ctx, anna, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)
	pane := do(anna, "GET", "/mail/copilot", nil, 200)
	for _, want := range []string{"Я CoPilot", "data-ai-form", `data-ai-say="Кратко перескажи`, `data-ai-draft="Напиши черновик`, "Разобрать входящие"} {
		if !strings.Contains(pane, want) {
			t.Fatalf("a new CoPilot pane has no %q:\n%s", want, pane)
		}
	}

	// «Кратко о письме»: the open letter is in the system, read with its conversation, still unread after
	pane = do(anna, "POST", "/mail/copilot", url.Values{"letter": {order}, "text": {"Кратко перескажи открытое письмо"}}, 200)
	if len(asked) != 2 || !strings.Contains(asked[0], "Анна Петрова") || !strings.Contains(asked[0], "anna@go24.test") ||
		!strings.Contains(asked[0], `id `+order+` «Re: Заказ» от Иван`) || !strings.Contains(asked[0], `"name":"draft_reply"`) {
		t.Fatalf("asked %d:\n%.2000s", len(asked), asked[0])
	}
	if !strings.Contains(asked[1], `Беру 10 коробок`) || !strings.Contains(asked[1], `сколько стоят`) || !strings.Contains(asked[1], `earlier`) {
		t.Fatalf("the letter read:\n%s", asked[1])
	}
	for _, want := range []string{"Кратко перескажи открытое письмо", "<b>10 коробок</b>", "Прочитал письмо"} {
		if !strings.Contains(pane, want) {
			t.Fatalf("the pane after a summary has no %q:\n%s", want, pane)
		}
	}
	var seen bool
	must(pool.QueryRow(ctx, `SELECT seen FROM mail_messages WHERE id::text = $1`, order).Scan(&seen))
	if seen {
		t.Fatal("CoPilot marked the letter it read as read")
	}
	run := regexp.MustCompile(`data-run="([^"]+)"`).FindStringSubmatch(pane)
	if run == nil || run[1] == "" {
		t.Fatalf("no run in the pane:\n%s", pane)
	}

	// a draft reply waits for «Подтвердить», then lands in «Черновики» as a reply
	pane = do(anna, "POST", "/mail/copilot", url.Values{"run": {run[1]}, "letter": {order}, "text": {"Напиши черновик ответа: привезём в пятницу"}}, 200)
	act := regexp.MustCompile(`data-ai-decide="([^"]+)" data-yes="1"`).FindStringSubmatch(pane)
	if act == nil || !strings.Contains(pane, "Здравствуйте, Иван!<br>Привезём в пятницу.") || !strings.Contains(pane, "Подтвердить") {
		t.Fatalf("the draft does not wait for a confirmation:\n%s", pane)
	}
	var drafts int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_drafts`).Scan(&drafts))
	if drafts != 0 {
		t.Fatal("a draft before «Подтвердить»")
	}
	if p := do(eve, "POST", "/mail/copilot/"+run[1]+"/"+act[1], url.Values{"yes": {"1"}}, 200); strings.Contains(p, "Привезём") {
		t.Fatal("another confirms one's action")
	}
	pane = do(anna, "POST", "/mail/copilot/"+run[1]+"/"+act[1], url.Values{"yes": {"1"}, "letter": {order}}, 200)
	if !strings.Contains(pane, "Черновик готов.") || !strings.Contains(pane, "Открыть черновик") || strings.Contains(pane, `data-yes="1"`) {
		t.Fatalf("the pane after a confirmed draft:\n%s", pane)
	}
	var to, subject, html, replyTo string
	must(pool.QueryRow(ctx, `SELECT x.to_addr, x.subject, x.body_html, d.reply_to::text FROM mail_drafts d JOIN mail_messages x ON x.id = d.id`).Scan(&to, &subject, &html, &replyTo))
	if !strings.Contains(to, "ivan@client.test") || subject != "Re: Заказ" || replyTo != order ||
		!strings.Contains(html, "Здравствуйте, Иван!") || !strings.Contains(html, "data-quote") || strings.Index(html, "Привезём") > strings.Index(html, "data-quote") {
		t.Fatalf("the draft: %q %q %q\n%s", to, subject, replyTo, html)
	}

	// a rejected action changes nothing; the list tool ran by itself
	pane = do(anna, "POST", "/mail/copilot", url.Values{"run": {run[1]}, "text": {"Разбери входящие"}}, 200)
	act = regexp.MustCompile(`data-ai-decide="([^"]+)" data-yes="1" data-reload`).FindStringSubmatch(pane)
	if act == nil || !strings.Contains(pane, "Пометить флажком — писем: 1") || !strings.Contains(pane, "Посмотрел письма") {
		t.Fatalf("the flag does not wait for a confirmation:\n%s", pane)
	}
	if !strings.Contains(asked[4], `"tool_use_id":"l1"`) && !strings.Contains(asked[len(asked)-1], `Беру 10 коробок`) {
		t.Fatal("the list was not given to the model")
	}
	pane = do(anna, "POST", "/mail/copilot/"+run[1]+"/"+act[1], url.Values{"yes": {"0"}}, 200)
	var flagged bool
	must(pool.QueryRow(ctx, `SELECT flagged FROM mail_messages WHERE id::text = $1`, order).Scan(&flagged))
	if flagged || !strings.Contains(pane, "Отклонено") || !strings.Contains(pane, "оставляю как есть") {
		t.Fatalf("a rejected flag: flagged %v\n%s", flagged, pane)
	}
	if !strings.Contains(asked[5], `"tool_use_id":"m1"`) || !strings.Contains(asked[5], `"is_error":true`) {
		t.Fatalf("the model was not told of the rejection:\n%s", asked[5])
	}

	// no deleting; nobody else's letters
	if _, err := srv.mailAIMark(ctx, anna, json.RawMessage(`{"ids":["`+first+`"],"action":"delete"}`)); err == nil {
		t.Fatal("the agent deletes letters")
	}
	if _, err := srv.mailAIRead(ctx, eve, json.RawMessage(`{"id":"`+order+`"}`)); err == nil {
		t.Fatal("the agent of eve reads anna's letter")
	}
	if out, err := srv.mailAILetters(ctx, eve, json.RawMessage(`{}`)); err != nil || strings.Contains(out, "коробок") {
		t.Fatalf("the agent of eve lists anna's letters: %v %s", err, out)
	}
	if _, err := srv.mailAIDraft(ctx, eve, json.RawMessage(`{"id":"`+order+`","text":"Привет"}`)); err == nil {
		t.Fatal("the agent of eve answers anna's letter")
	}

	// REST: a new conversation, the past one with its actions; another's key does not see it
	api := func(who, method, path, body string, want int, out any) {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"mail:read", "mail:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		if out != nil {
			must(json.Unmarshal(b, out))
		}
	}
	var got apiAIRun
	api(anna, "POST", "/api/v1/mail/copilot", `{"text":"Сколько непрочитанных?","letter":"`+order+`"}`, 200, &got)
	if got.ID == "" || got.ID == run[1] || got.Status != ai.RunDone || len(got.Messages) != 2 || got.Messages[1].Text != "Одно непрочитанное." {
		t.Fatalf("REST: a new conversation %+v", got)
	}
	api(anna, "GET", "/api/v1/mail/copilot/"+run[1], "", 200, &got)
	if len(got.Actions) != 4 || got.Actions[1].Tool != "draft_reply" || got.Actions[1].Status != ai.ActDone || got.Actions[3].Status != ai.ActRejected {
		t.Fatalf("REST: the conversation %+v", got)
	}
	var runs []apiAIRun
	api(anna, "GET", "/api/v1/mail/copilot", "", 200, &runs)
	if len(runs) != 2 {
		t.Fatalf("REST: conversations %+v", runs)
	}
	api(eve, "GET", "/api/v1/mail/copilot/"+run[1], "", 404, nil)

	// the past conversations: anna's, not eve's; a deleted one is gone
	if p := do(anna, "GET", "/mail/copilot", nil, 200); !strings.Contains(p, `data-ai-run="`+run[1]+`"`) || !strings.Contains(p, "Кратко перескажи открытое письмо") {
		t.Fatalf("anna's conversations:\n%s", p)
	}
	if p := do(anna, "GET", "/mail/copilot?run="+run[1], nil, 200); !strings.Contains(p, "Черновик готов.") {
		t.Fatal("a past conversation does not open")
	}
	if p := do(eve, "GET", "/mail/copilot?run="+run[1], nil, 200); strings.Contains(p, "коробок") || strings.Contains(p, run[1]) {
		t.Fatalf("eve sees anna's conversation:\n%s", p)
	}
	if p := do(anna, "POST", "/mail/copilot/"+run[1]+"/delete", url.Values{}, 200); strings.Contains(p, run[1]) || !strings.Contains(p, "Я CoPilot") {
		t.Fatalf("a deleted conversation:\n%s", p)
	}
}
