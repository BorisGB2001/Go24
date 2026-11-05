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

// fakeGroups is the bridge of go24's messenger: the messages queued by the test come once per position, the posts
// and the chats joined and left are kept.
type fakeGroups struct {
	mu                  sync.Mutex
	news                []ChatMessage
	pos                 int
	posts, joins, lefts []string
}

func (f *fakeGroups) BridgeNews(ctx context.Context, since string) ([]ChatMessage, string, error) {
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

func (f *fakeGroups) BridgePost(ctx context.Context, userID, room, root, name, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, userID+"|"+room+"|"+root+"|"+name+"|"+text)
	return nil
}

func (f *fakeGroups) BridgeJoin(ctx context.Context, userID, room string) error {
	f.joins = append(f.joins, userID+"|"+room)
	return nil
}

func (f *fakeGroups) BridgeLeave(ctx context.Context, room string) error {
	f.lefts = append(f.lefts, room)
	return nil
}

func (f *fakeTelegram) group(chat, from int64, name, text string, to int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := map[string]any{"text": text, "chat": map[string]any{"id": chat, "type": "supergroup", "title": "Команда"},
		"from": map[string]any{"id": from, "first_name": name}}
	if to != 0 {
		m["reply_to_message"] = map[string]any{"message_id": to}
	}
	f.updates = append(f.updates, map[string]any{"update_id": 100 + len(f.updates), "message": m})
}

// TestTelegramGroups: a group chat of go24 is linked to a Telegram group by /link with its code; the chat's new
// messages go to the group, the group's come into the chat — as the employee's when their own chat with the bot is
// linked, a reply into the thread of the message answered; /unlink ends it.
func TestTelegramGroups(t *testing.T) {
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
	groups := &fakeGroups{}
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Telegram{API: srv.URL, HTTP: srv.Client()}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string { return key }

	// no bot ready, no bridge: nothing to link
	if _, ok, err := s.GroupLinkOf(ctx, "!grp"); err != nil || ok {
		t.Fatalf("a link without a bot: %v %v", ok, err)
	}
	s.Groups = groups
	must(s.SaveBot(ctx, "telegram", true, "", "good", false, anna))
	l, err := s.StartGroupLink(ctx, anna, "!grp")
	must(err)
	if l.Code == "" || l.Linked || l.Bot != "go24_test_bot" {
		t.Fatalf("the link started: %+v", l)
	}
	// Анна's own chat with the bot is linked: in the group she writes as herself
	pl, err := s.StartLink(ctx, anna, "telegram")
	must(err)
	tg.write(42, "anna", "/start "+pl.Code)

	// a wrong code is told so; the right one (as a group of several bots sends it) links the group
	tg.group(-500, 42, "Анна", "/link nope", 0)
	tg.group(-500, 42, "Анна", "/link@go24_test_bot "+l.Code, 0)
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{"42: notifyout.bot_linked", "-500: notifyout.group_bad_code", "-500: notifyout.group_linked"}) {
		t.Fatalf("the answers: %q", got)
	}
	if l, _, _ = s.GroupLinkOf(ctx, "!grp"); !l.Linked || l.Title != "Команда" || l.Code != "" {
		t.Fatalf("linked: %+v", l)
	}
	if !slices.Equal(groups.joins, []string{anna + "|!grp"}) {
		t.Fatalf("the bridge joined: %q", groups.joins)
	}

	// the chat's messages go to the group (the first pass only took the position)
	groups.news = []ChatMessage{{Room: "!grp", Event: "$1", Root: "$root", Sender: "Ева", Text: "в треде"},
		{Room: "!other", Event: "$2", Sender: "Боб", Text: "не туда"}}
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{"-500: Ева: в треде"}) {
		t.Fatalf("sent to the group: %q", got)
	}

	// the group's messages: Анна's as hers, a reply into the thread; someone else's with the name; commands — none
	tg.group(-500, 42, "Анна", "ок", 1)
	tg.group(-500, 77, "Пётр", "привет из Telegram", 0)
	tg.group(-500, 77, "Пётр", "/help", 0)
	tg.group(-600, 77, "Пётр", "чужая группа", 0)
	s.Pass(ctx)
	if !slices.Equal(groups.posts, []string{anna + "|!grp|$root|Анна|ок", "|!grp||Пётр|привет из Telegram"}) {
		t.Fatalf("posted: %q", groups.posts)
	}
	if got := tg.took(); len(got) != 0 {
		t.Fatalf("the bot spoke in the groups: %q", got)
	}

	// /unlink in the group ends it, the bridge leaves the chat
	tg.group(-500, 77, "Пётр", "/unlink", 0)
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{"-500: notifyout.group_unlinked"}) {
		t.Fatalf("unlinked: %q", got)
	}
	if l, _, _ = s.GroupLinkOf(ctx, "!grp"); l.Linked || !slices.Equal(groups.lefts, []string{"!grp"}) {
		t.Fatalf("still linked: %+v %q", l, groups.lefts)
	}
}
