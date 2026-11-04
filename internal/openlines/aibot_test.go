package openlines

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestStems: the question's words, lowercased and cut, without short ones and repeats.
func TestStems(t *testing.T) {
	if got := stems("Сколько стоит ДОСТАВКА в Москву? доставка, 2 дня"); !slices.Equal(got, []string{"скольк", "стоит", "достав", "москву", "дня"}) {
		t.Fatalf("stems = %q", got)
	}
}

// TestAIBot: a new dialogue of a line with the AI bot waits for its answer, not for the operators; the answer goes to
// the client and the bot goes on; [OPERATOR] hands the dialogue to the operators (told, waiting); an operator's answer
// and a failed AI end the bot's part; the knowledge base pages are ranked by the question's words.
func TestAIBot(t *testing.T) {
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
	boss, a := newUser("boss"), newUser("a")
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, args ...any) string { return key }
	var space string
	must(pool.QueryRow(ctx, `INSERT INTO kb_spaces (name, created_by) VALUES ('Справка', $1) RETURNING id`, boss).Scan(&space))
	addPage := func(title, body string) {
		_, err := pool.Exec(ctx, `INSERT INTO kb_pages (space_id, title, body, created_by, updated_by) VALUES ($1, $2, $3, $4, $4)`, space, title, body, boss)
		must(err)
	}
	addPage("Оплата", "Картой на сайте или по счёту.")
	addPage("Доставка", "Доставка по Москве — 300 ₽, 1–2 дня.")

	if _, err := s.SaveWidget(ctx, boss, Widget{Name: "ИИ", Active: true, AIBot: true}); err == nil {
		t.Fatal("an AI bot without a space is taken")
	}
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "ИИ", Active: true, Operators: []string{a}, AIBot: true, AISpace: space, WelcomeText: "welcome"})
	must(err)
	if w, err := s.Widget(ctx, wid); err != nil || !w.AIBot || w.AISpace != space {
		t.Fatalf("line = %+v, %v", w, err)
	}
	k, err := s.Knowledge(ctx, space, "Сколько стоит доставка?")
	must(err)
	if !strings.HasPrefix(k, "## Доставка\n") || !strings.Contains(k, "## Оплата") {
		t.Fatalf("knowledge = %q", k)
	}

	told := func() int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'openline'`, a).Scan(&n))
		return n
	}
	var last int64
	said := func(tok string) []Message {
		t.Helper()
		list, err := s.VisitorMessages(ctx, wid, tok, last)
		must(err)
		out := []Message{}
		for _, m := range list {
			last = m.ID
			if m.AuthorID == "" && m.Kind != "" {
				out = append(out, m)
			}
		}
		return out
	}
	state := func(id string) (ai, waiting bool, unread int) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT ai_bot, waiting_since IS NOT NULL, unread FROM ol_sessions WHERE id = $1`, id).Scan(&ai, &waiting, &unread))
		return
	}

	// a new dialogue: no welcome, nobody waits or is told; worker takes it once, with the question and whose AI
	sid, tok, err := s.Visit(ctx, wid, "", "")
	must(err)
	must(s.VisitorSay(ctx, wid, tok, "Сколько стоит доставка?"))
	if got := said(tok); len(got) != 0 {
		t.Fatalf("before the bot's answer the client got %+v", got)
	}
	if ai, waiting, unread := state(sid); !ai || waiting || unread != 0 || told() != 0 {
		t.Fatalf("with the AI bot: ai %v, waiting %v, unread %d, told %d", ai, waiting, unread, told())
	}
	due, err := s.AIBotDue(ctx)
	must(err)
	if len(due) != 1 || due[0].SessionID != sid || due[0].Space != space || due[0].UserID != a || due[0].Question != "Сколько стоит доставка?" {
		t.Fatalf("due = %+v", due)
	}
	if due, _ := s.AIBotDue(ctx); len(due) != 0 {
		t.Fatalf("taken twice: %+v", due)
	}
	if x, _, err := s.Open(ctx, boss, sid); err != nil || !x.Bot || x.Waiting {
		t.Fatalf("the dialogue = %+v, %v", x, err)
	}

	// the answer goes to the client, the bot goes on
	must(s.AIBotAnswer(ctx, sid, "300 ₽, доставим за 1–2 дня."))
	if got := said(tok); len(got) != 1 || got[0].Kind != "bot" || got[0].Body != "300 ₽, доставим за 1–2 дня." {
		t.Fatalf("the bot's answer = %+v", got)
	}
	if ai, waiting, _ := state(sid); !ai || waiting || told() != 0 {
		t.Fatal("the bot let go after an answer")
	}

	// [OPERATOR]: to the queue, told, a service line
	must(s.VisitorSay(ctx, wid, tok, "Хочу поговорить с человеком"))
	if due, _ := s.AIBotDue(ctx); len(due) != 1 {
		t.Fatalf("the next question is not due: %+v", due)
	}
	must(s.AIBotAnswer(ctx, sid, " [OPERATOR] "))
	if got := said(tok); len(got) != 1 || got[0].Body != "ol.bot_to_queue" {
		t.Fatalf("the handoff = %+v", got)
	}
	if ai, waiting, unread := state(sid); ai || !waiting || unread != 1 || told() != 1 {
		t.Fatalf("handed: ai %v, waiting %v, unread %d, told %d", ai, waiting, unread, told())
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ol_messages WHERE session_id = $1 AND kind = 'note' AND body = 'ol.note_aibot_queue'`, sid).Scan(&notes))
	if notes != 1 {
		t.Fatal("no service line of the handoff")
	}
	// the client goes on with the operators, not the bot
	must(s.VisitorSay(ctx, wid, tok, "Алло?"))
	if due, _ := s.AIBotDue(ctx); len(due) != 0 {
		t.Fatal("the bot is back after the handoff")
	}

	// a second client: an operator's answer ends the bot's part, the late AI answer is dropped
	sid2, tok2, err := s.Visit(ctx, wid, "", "")
	must(err)
	must(s.VisitorSay(ctx, wid, tok2, "Как оплатить?"))
	must(s.Reply(ctx, a, sid2, "Здравствуйте, я помогу"))
	if ai, _, _ := state(sid2); ai {
		t.Fatal("the operator's answer left the bot on")
	}
	if due, _ := s.AIBotDue(ctx); len(due) != 0 {
		t.Fatalf("due after the operator's answer: %+v", due)
	}
	last = 0
	must(s.AIBotAnswer(ctx, sid2, "Картой."))
	for _, m := range said(tok2) {
		if m.Kind == "bot" {
			t.Fatalf("the bot answered after the operator: %+v", m)
		}
	}

	// a third: the AI fails — to the operators with its service line
	sid3, tok3, err := s.Visit(ctx, wid, "", "")
	must(err)
	must(s.VisitorSay(ctx, wid, tok3, "Вопрос"))
	_, _ = s.AIBotDue(ctx)
	must(s.AIBotFail(ctx, sid3))
	if ai, waiting, _ := state(sid3); ai || !waiting {
		t.Fatal("a failed AI kept the dialogue")
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ol_messages WHERE session_id = $1 AND kind = 'note' AND body = 'ol.note_aibot_error'`, sid3).Scan(&notes))
	if notes != 1 {
		t.Fatal("no service line of the failure")
	}

	// a fourth: after aiTurns answers the operators take over
	sid4, tok4, err := s.Visit(ctx, wid, "", "")
	must(err)
	for i := 0; i < aiTurns; i++ {
		must(s.VisitorSay(ctx, wid, tok4, "Ещё вопрос"))
		must(s.AIBotAnswer(ctx, sid4, "Ответ"))
	}
	if ai, waiting, _ := state(sid4); ai || !waiting {
		t.Fatalf("after %d answers the bot still leads", aiTurns)
	}
}
