package mail

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestParseQuery(t *testing.T) {
	q := ParseQuery(`счёт from:boss@client.test to:"Анна Иванова" subject:Заказ has:attachment is:unread is:flagged after:2026-10-01 before:12.10.2026 "два слова" от:Пётр`)
	if strings.Join(q.Words, "|") != "счёт|два слова" || strings.Join(q.From, "|") != "boss@client.test|Пётр" || strings.Join(q.To, "|") != "Анна Иванова" ||
		strings.Join(q.Subject, "|") != "Заказ" || !q.Attach || !q.Unread || !q.Flagged || q.Read || q.High {
		t.Fatalf("parsed = %+v", q)
	}
	if q.After == nil || q.After.Format("2006-01-02") != "2026-10-01" || q.Before == nil || q.Before.Format("2006-01-02") != "2026-10-12" {
		t.Fatalf("dates = %v %v", q.After, q.Before)
	}
	// what is not an operator stays a word: an unknown one, a bad date, an empty value
	q = ParseQuery("http://x.test is:nothing before:завтра from:")
	if strings.Join(q.Words, "|") != "http://x.test|is:nothing|before:завтра|from:" || q.Before != nil {
		t.Fatalf("words = %q", q.Words)
	}
	if !ParseQuery("  ").Empty() || ParseQuery("is:important").Empty() || !ParseQuery("is:important").High {
		t.Fatal("Empty")
	}
	if got := strings.Join(ParseQuery(`from:a "b c" subject:d`).Terms(), "|"); got != "b c|a|d" {
		t.Fatalf("terms = %q", got)
	}
	// the advanced form makes a search, and a search fills the form back
	a := Advanced{From: "boss", To: "Анна Иванова", Subject: "Заказ", Words: "срочно", After: "2026-10-01", Attach: true, Unread: true}
	s := a.String()
	if s != `from:boss to:"Анна Иванова" subject:Заказ has:attachment is:unread after:2026-10-01 срочно` {
		t.Fatalf("advanced = %q", s)
	}
	if back := ParseQuery(s).Advanced(); back != a {
		t.Fatalf("back = %+v", back)
	}
}

func TestBulk(t *testing.T) {
	h := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i+1 < len(kv); i += 2 {
				if strings.EqualFold(kv[i], k) {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	for _, c := range []struct {
		from string
		h    func(string) string
		want bool
	}{
		{"ivan@client.test", h(), false},
		{"noreply@shop.test", nil, true},
		{"no-reply+42@shop.test", nil, true},
		{"newsletter@shop.test", nil, true},
		{"ivan@client.test", h("List-Unsubscribe", "<mailto:x@y>"), true},
		{"ivan@client.test", h("Precedence", "Bulk"), true},
		{"ivan@client.test", h("Auto-Submitted", "auto-generated"), true},
		{"ivan@client.test", h("Auto-Submitted", "no"), false},
	} {
		if got := Bulk(c.from, c.h); got != c.want {
			t.Errorf("Bulk(%q) = %v", c.from, got)
		}
	}
	p := parseFull(strings.NewReader("From: shop@x.test\r\nList-Unsubscribe: <https://x.test/u>\r\nContent-Type: text/plain\r\n\r\nSale\r\n"))
	if !p.bulk {
		t.Fatal("a newsletter is not bulk")
	}
}

// TestFocusAndSearch runs against the test database: a new inbox letter goes under «Другие» when it is bulk —
// unless the user wrote to the sender, moved such letters to «Отсортированные» or set «Всегда перемещать»; the
// tabs, the filters, the orders and the searches (in a folder, in all folders, a search folder) list what they
// should; the recent and saved searches; nobody else sees any of it.
func TestFocusAndSearch(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис Петров', 'bob@go24.test') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, bob, Settings{Email: "bob@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox, sent, archive string
	for role, to := range map[string]*string{"inbox": &inbox, "sent": &sent, "archive": &archive} {
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, role, role).Scan(to))
	}
	uid := int64(0)
	letter := func(folder, from, name, to, subject, body string, at time.Time, bulk bool) string {
		t.Helper()
		uid++
		other := false
		if folder == inbox {
			var err error
			other, err = s.otherFor(ctx, bob, from, bulk || Bulk(from, nil))
			must(err)
		}
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, other)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`, box, folder, uid, from, name, to, subject, body, at, other).Scan(&id))
		return id
	}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 10, 0, 0, 0, time.Local) }
	// Ivan wrote first; the user answered him, so his newsletter-like letter stays focused
	ivan := letter(inbox, "ivan@client.test", "Иван", "bob@go24.test", "Заказ", "Десять коробок, @Борис посмотри", day(1), false)
	letter(sent, "bob@go24.test", "", "ivan@client.test", "Re: Заказ", "Хорошо", day(2), false)
	ivan2 := letter(inbox, "ivan@client.test", "Иван", "list@client.test", "Прайс", "Отписаться: ...", day(3), true)
	sale := letter(inbox, "news@shop.test", "Магазин", "bob@go24.test", "Скидки", "-50%", day(4), true)
	robot := letter(inbox, "noreply@bank.test", "Банк", "bob@go24.test", "Выписка", "Ваша выписка", day(5), false)
	big := letter(archive, "anna@partner.test", "Анна", "bob@go24.test", "Договор", "Договор во вложении", day(6), false)
	must2 := func(id string, other bool) {
		t.Helper()
		var got bool
		must(pool.QueryRow(ctx, `SELECT other FROM mail_messages WHERE id::text = $1`, id).Scan(&got))
		if got != other {
			t.Fatalf("letter %s other = %v, want %v", id, got, other)
		}
	}
	must2(ivan, false)
	must2(ivan2, false)
	must2(sale, true)
	must2(robot, true)
	ids := func(list []Message, err error) string {
		t.Helper()
		must(err)
		var out []string
		for _, m := range list {
			out = append(out, m.Subject)
		}
		return strings.Join(out, ",")
	}
	list := func(o ListOpts) string {
		t.Helper()
		return ids(s.ListBy(ctx, bob, o))
	}
	if got := list(ListOpts{Folder: inbox, View: ViewFocused}); got != "Прайс,Заказ" {
		t.Fatalf("focused = %q", got)
	}
	if got := list(ListOpts{Folder: AllInboxes, View: ViewOther}); got != "Выписка,Скидки" {
		t.Fatalf("other = %q", got)
	}
	if n, names, err := s.OtherNews(ctx, bob, inbox); err != nil || n != 2 || strings.Join(names, ",") != "Банк,Магазин" {
		t.Fatalf("other news = %d %q %v", n, names, err)
	}

	// moving the bank's letter to «Отсортированные» teaches go24: its next letter is focused; «Всегда перемещать» of
	// the shop moves its letters now and later
	must(s.Act(ctx, bob, []string{robot}, "focus", ""))
	must2(robot, false)
	robot2 := letter(inbox, "noreply@bank.test", "Банк", "bob@go24.test", "Выписка 2", "", day(7), false)
	must2(robot2, false)
	must(s.Act(ctx, bob, []string{ivan2}, "other", "always"))
	must2(ivan, true) // all of Ivan's inbox letters
	ivan3 := letter(inbox, "ivan@client.test", "Иван", "bob@go24.test", "Ещё", "", day(8), false)
	must2(ivan3, true)
	rules, err := s.FocusRules(ctx, bob)
	must(err)
	if len(rules) != 1 || rules[0] != (FocusRule{"ivan@client.test", ViewOther}) {
		t.Fatalf("rules = %+v", rules)
	}
	must(s.DropFocusRule(ctx, bob, "IVAN@client.test"))
	if err := s.DropFocusRule(ctx, eve, "ivan@client.test"); err != ErrNotFound {
		t.Fatalf("eve drops bob's rule: %v", err)
	}
	must(s.SetFocusRule(ctx, bob, "Ivan@Client.test", ViewFocused))
	must2(ivan3, false)
	if err := s.SetFocusRule(ctx, bob, "not an address", ViewFocused); err == nil {
		t.Fatal("a rule for no address")
	}
	if err := s.Act(ctx, eve, []string{sale}, "focus", ""); err != ErrNotFound {
		t.Fatalf("eve moves bob's letter: %v", err)
	}

	// filters
	must(s.Act(ctx, bob, []string{sale}, "flag", ""))
	must(s.Act(ctx, bob, []string{ivan}, "read", ""))
	if _, err := pool.Exec(ctx, `UPDATE mail_messages SET importance = 1 WHERE id::text = $1`, robot); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO files (object_key, name, size, mail_id) VALUES ('k1', 'big.pdf', 5000000, $1)`, big); err != nil {
		t.Fatal(err)
	}
	for filter, want := range map[string]string{"unread": "Ещё,Выписка 2,Выписка,Скидки,Прайс", "flagged": "Скидки", "tome": "Ещё,Выписка 2,Выписка,Скидки,Заказ",
		"mention": "Заказ", "high": "Выписка", "attach": ""} {
		if got := list(ListOpts{Folder: inbox, Filter: filter}); got != want {
			t.Errorf("filter %s = %q, want %q", filter, got, want)
		}
	}
	// orders
	for _, c := range []struct {
		sort string
		turn bool
		want string
	}{
		{"date", true, "Заказ,Прайс,Скидки,Выписка,Выписка 2,Ещё"},
		{"from", false, "Выписка 2,Выписка,Ещё,Прайс,Заказ,Скидки"}, // Банк, Иван, Магазин; each the newest first
		{"subject", false, "Выписка,Выписка 2,Ещё,Заказ,Прайс,Скидки"},
		{"importance", false, "Выписка,Ещё,Выписка 2,Скидки,Прайс,Заказ"},
		{"flag", false, "Скидки,Ещё,Выписка 2,Выписка,Прайс,Заказ"},
	} {
		if got := list(ListOpts{Folder: inbox, Sort: c.sort, Turn: c.turn}); got != c.want {
			t.Errorf("sort %s %v = %q, want %q", c.sort, c.turn, got, c.want)
		}
	}
	if got := list(ListOpts{Folder: AllInboxes, Q: "subject:Договор", All: true, Sort: "size"}); got != "Договор" {
		t.Fatalf("size = %q", got)
	}
	// searches: in the folder, in all folders (snoozed letters too), with operators
	if got := list(ListOpts{Folder: inbox, Q: "Договор"}); got != "" {
		t.Fatalf("the folder's search = %q", got)
	}
	if _, err := pool.Exec(ctx, `UPDATE mail_messages SET snooze_until = now() + interval '1 day' WHERE id::text = $1`, sale); err != nil {
		t.Fatal(err)
	}
	if got := list(ListOpts{Folder: inbox, Q: "from:anna has:attachment", All: true}); got != "Договор" {
		t.Fatalf("all folders = %q", got)
	}
	if got := list(ListOpts{Folder: inbox, Q: "is:flagged", All: true}); got != "Скидки" {
		t.Fatalf("a snoozed letter found = %q", got)
	}
	if got := list(ListOpts{Folder: inbox, Q: "after:2026-10-07 before:2026-10-08"}); got != "Выписка 2" {
		t.Fatalf("dates = %q", got)
	}
	if got := list(ListOpts{Folder: inbox, Q: "to:list@client.test", Conv: true}); got != "Прайс" {
		t.Fatalf("to = %q", got)
	}
	if got := list(ListOpts{Folder: inbox, Q: "is:unread", Sort: "size", Conv: true}); got == "" {
		t.Fatal("conversations by size")
	}
	if got := ids(s.ListBy(ctx, eve, ListOpts{Folder: inbox, Q: "Договор", All: true})); got != "" {
		t.Fatalf("eve finds bob's letters: %q", got)
	}

	// recent and saved searches
	for _, q := range []string{"Договор", "from:anna", "Договор"} {
		must(s.Remember(ctx, bob, q))
	}
	recent, err := s.Recent(ctx, bob, "", 5)
	must(err)
	if len(recent) != 2 || recent[0].Q != "Договор" {
		t.Fatalf("recent = %+v", recent)
	}
	for i := 0; i < maxRecent+3; i++ {
		must(s.Remember(ctx, bob, "q"+strings.Repeat("x", i)))
	}
	if recent, _ = s.Recent(ctx, bob, "", 50); len(recent) != maxRecent {
		t.Fatalf("recent kept = %d", len(recent))
	}
	if recent, _ = s.Recent(ctx, eve, "", 50); len(recent) != 0 {
		t.Fatal("eve sees bob's searches")
	}
	id, err := s.SaveSearch(ctx, bob, "", "Договоры", "subject:Договор")
	must(err)
	if _, err := s.SaveSearch(ctx, bob, "", "договоры", "x"); err == nil {
		t.Fatal("a second search folder of the name")
	}
	if _, err := s.SaveSearch(ctx, bob, "", "Пусто", "   "); err == nil {
		t.Fatal("a search folder of nothing")
	}
	if got := list(ListOpts{Folder: SearchFolder + id}); got != "Договор" {
		t.Fatalf("search folder = %q", got)
	}
	if _, err := s.ListBy(ctx, eve, ListOpts{Folder: SearchFolder + id}); err != ErrNotFound {
		t.Fatalf("eve opens bob's search folder: %v", err)
	}
	_, err = s.SaveSearch(ctx, bob, id, "От Анны", "from:anna")
	must(err)
	saved, err := s.Searches(ctx, bob)
	must(err)
	if len(saved) != 1 || saved[0].Name != "От Анны" || saved[0].Q != "from:anna" {
		t.Fatalf("saved = %+v", saved)
	}
	if err := s.DeleteSearch(ctx, eve, id); err != ErrNotFound {
		t.Fatalf("eve deletes bob's search: %v", err)
	}
	must(s.DeleteSearch(ctx, bob, id))
	must(s.ForgetRecent(ctx, bob))
	if recent, _ = s.Recent(ctx, bob, "", 50); len(recent) != 0 {
		t.Fatal("recent searches kept")
	}

	// the setting
	p, err := s.Prefs(ctx, bob)
	must(err)
	if !p.Focused {
		t.Fatal("focused is off by default")
	}
	must(s.SetFocused(ctx, bob, false))
	must(s.SavePrefs(ctx, bob, Prefs{UndoSeconds: 10}))
	if p, _ = s.Prefs(ctx, bob); p.Focused || p.UndoSeconds != 10 {
		t.Fatalf("prefs = %+v", p)
	}
}
