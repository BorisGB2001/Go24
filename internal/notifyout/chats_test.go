package notifyout

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/notify"
)

// fakeChats is go24's messenger: the messages queued by the test come once per position, the replies are kept.
type fakeChats struct {
	mu      sync.Mutex
	news    []ChatMessage
	pos     int
	replies []string
}

func (f *fakeChats) ChatNews(ctx context.Context, userID, since string) ([]ChatMessage, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if since == "" {
		return nil, "p", nil
	}
	out := f.news
	f.news = nil
	f.pos++
	return out, "p" + string(rune('0'+f.pos)), nil
}

func (f *fakeChats) ChatReply(ctx context.Context, userID, room, root, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, room+"|"+root+"|"+text)
	return nil
}

func (f *fakeTelegram) reply(chat int64, user, text string, to int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_id": 100 + len(f.updates), "message": map[string]any{
		"text": text, "chat": map[string]any{"id": chat, "type": "private"}, "from": map[string]any{"username": user},
		"reply_to_message": map[string]any{"message_id": to}}})
}

// TestTelegramChats: the new messages of a person's chats go to their Telegram while they are not in go24 (not when
// they are, not when im_chat is off there); a reply to one is posted into its chat (its thread), a plain message is
// answered with a hint.
func TestTelegramChats(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))

	tg := &fakeTelegram{}
	srv := httptest.NewServer(tg)
	defer srv.Close()
	chats := &fakeChats{}
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Telegram{API: srv.URL, HTTP: srv.Client()}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string { return key }
	s.Chats = chats
	must(s.SaveBot(ctx, "telegram", true, "", "good", false, anna))
	l, err := s.StartLink(ctx, anna, "telegram")
	must(err)
	tg.write(100, "anna", "/start "+l.Code)
	s.Pass(ctx) // linked; the first pass only takes the position
	tg.took()
	var since string
	must(pool.QueryRow(ctx, `SELECT since FROM notify_chat_sync WHERE user_id = $1`, anna).Scan(&since))
	if since != "p" {
		t.Fatalf("the position: %q", since)
	}

	// not in go24: the messages come, a private chat by the sender, a group with its name
	chats.news = []ChatMessage{{Room: "!dm", Event: "$1", Sender: "Боб", Text: "привет"},
		{Room: "!grp", Event: "$2", Root: "$root", Chat: "Отдел", Sender: "Ева", Text: "в треде"}}
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{`100: <a href="https://go24.test/im?room=%21dm">Боб: привет</a>`,
		`100: <a href="https://go24.test/im?room=%21grp">Ева · Отдел: в треде</a>`}) {
		t.Fatalf("sent: %q", got)
	}

	// the replies: to the message in the thread — into the thread; a plain message — the hint; to an unknown one — the hint
	tg.reply(100, "anna", "ок, смотрю", 2)
	tg.write(100, "anna", "просто текст")
	tg.reply(100, "anna", "куда?", 999)
	s.Pass(ctx)
	if !slices.Equal(chats.replies, []string{"!grp|$root|ок, смотрю"}) {
		t.Fatalf("the replies: %q", chats.replies)
	}
	if got := tg.took(); !slices.Equal(got, []string{"100: notifyout.bot_reply_hint", "100: notifyout.bot_reply_hint"}) {
		t.Fatalf("the answers: %q", got)
	}

	// in go24 now: nothing comes, the position moves on all the same
	_, err = pool.Exec(ctx, `UPDATE users SET seen_at = now() WHERE id = $1`, anna)
	must(err)
	chats.news = []ChatMessage{{Room: "!dm", Event: "$3", Sender: "Боб", Text: "ты тут?"}}
	s.Pass(ctx)
	if got := tg.took(); len(got) != 0 || chats.news != nil {
		t.Fatalf("sent while in go24: %q", got)
	}

	// im_chat off for Telegram: no messages, and the messenger is not asked
	_, err = pool.Exec(ctx, `UPDATE users SET seen_at = NULL WHERE id = $1`, anna)
	must(err)
	must(s.SaveOff(ctx, anna, map[string]map[string]bool{"telegram": {"comment": true}}))
	chats.news = []ChatMessage{{Room: "!dm", Event: "$4", Sender: "Боб", Text: "ау"}}
	s.Pass(ctx)
	if got := tg.took(); len(got) != 0 || chats.news == nil {
		t.Fatalf("sent with im_chat off: %q", got)
	}
}
