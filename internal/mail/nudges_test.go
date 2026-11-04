package mail

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestAsksAnswer(t *testing.T) {
	for body, want := range map[string]bool{
		"Сможете прислать счёт до пятницы?":                            true,
		"Пришлите, пожалуйста, договор":                                true,
		"Could you check the draft":                                    true,
		"Спасибо, всё получил.":                                        false,
		"Отлично.\n\n> Когда будет готово?":                            false, // the question is the quoted letter's
		"Ок.\nOn Mon, 26 Oct 2026 Ann <a@b.c> wrote:\nWhen is it due?": false,
		"Хорошо\n-----Original Message-----\nFrom: x\nКак дела?":       false,
	} {
		if got := AsksAnswer(body); got != want {
			t.Errorf("AsksAnswer(%q) = %v", body, got)
		}
	}
	if got := splitAddrs(`"Ann B" <Ann@B.test>, c@d.test, nobody`); !slices.Equal(got, []string{"ann@b.test", "c@d.test"}) {
		t.Errorf("splitAddrs = %v", got)
	}
	if got := nameIn(`"Ann B" <ann@b.test>, c@d.test`, "ann@b.test"); got != "Ann B" {
		t.Errorf("nameIn = %q", got)
	}
}

// TestNudges against the test database: which letters of the inbox and «Отправленные» wait for an answer.
func TestNudges(t *testing.T) {
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
	_, err = pool.Exec(ctx, `UPDATE mailboxes SET send_as = 'Продажи <sales@go24.test>' WHERE id = $1`, box)
	must(err)
	var inbox, sent string
	for role, to := range map[string]*string{"inbox": &inbox, "sent": &sent} {
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, role, role).Scan(to))
	}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	uid := int64(0)
	letter := func(folder, from, name, to, subject, body string, daysAgo int, msgID, thread string) string {
		t.Helper()
		uid++
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, message_id, thread)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) RETURNING id::text`, box, folder, uid, from, name, to, subject, body,
			now.Add(-time.Duration(daysAgo)*24*time.Hour), msgID, thread).Scan(&id))
		return id
	}
	ask := letter(inbox, "ann@client.test", "Анна", "bob@go24.test", "Счёт", "Пришлите счёт?", 4, "a1", "a1")
	letter(inbox, "ann@client.test", "Анна", "bob@go24.test", "Свежее", "Как дела?", 1, "a2", "a2")               // too new
	letter(inbox, "ann@client.test", "Анна", "bob@go24.test", "Старое", "Как дела?", 20, "a3", "a3")              // too old
	letter(inbox, "ann@client.test", "Анна", "bob@go24.test", "Спасибо", "Спасибо, получил.", 5, "a4", "a4")      // asks nothing
	letter(inbox, "ann@client.test", "Анна", "team@go24.test", "Всем", "Кто придёт?", 5, "a5", "a5")              // not to the user
	letter(inbox, "noreply@shop.test", "Магазин", "bob@go24.test", "Отзыв", "Оцените покупку?", 5, "a6", "a6")    // a robot
	alias := letter(inbox, "max@client.test", "", "sales@go24.test", "Цена", "Сколько стоит?", 6, "a7", "a7")     // to the alias
	answered := letter(inbox, "ann@client.test", "Анна", "bob@go24.test", "Отчёт", "Готов отчёт?", 7, "a8", "a8") // the user answered it
	letter(sent, "bob@go24.test", "", "ann@client.test", "Re: Отчёт", "Да, готов.", 6, "b8", "a8")
	waiting := letter(sent, "bob@go24.test", "", `"Пётр К" <petr@client.test>`, "Договор", "Подпишете договор?", 5, "b1", "b1")
	got := letter(sent, "bob@go24.test", "", "petr@client.test", "Акт", "Пришлите акт, пожалуйста", 8, "b2", "b2") // Пётр answered
	letter(inbox, "petr@client.test", "Пётр", "bob@go24.test", "Re: Акт", "Вот акт.", 7, "a9", "b2")
	letter(sent, "bob@go24.test", "", "sales@go24.test", "Себе", "Не забыть?", 5, "b3", "b3") // only to the user's own address
	_ = got

	ids := func() []string {
		t.Helper()
		ns, err := s.nudgesAt(ctx, bob, now)
		must(err)
		var out []string
		for _, n := range ns {
			out = append(out, n.ID)
		}
		return out
	}
	if l := ids(); !slices.Equal(l, []string{ask, alias, waiting}) {
		t.Fatalf("nudges = %v, want %v", l, []string{ask, alias, waiting})
	}
	ns, err := s.nudgesAt(ctx, bob, now)
	must(err)
	if ns[0].Who != "Анна" || ns[0].Days != 4 || ns[0].Sent || ns[1].Who != "max@client.test" || ns[2].Who != "Пётр К" || !ns[2].Sent || ns[2].MailboxID != box {
		t.Errorf("nudges: %+v", ns)
	}
	_ = answered

	// at most three; «Закрыть» puts one away for good, the next comes up; another's letter is not to be closed
	more := letter(inbox, "ivan@client.test", "Иван", "bob@go24.test", "Встреча", "Когда удобно?", 3, "a10", "a10")
	if l := ids(); !slices.Equal(l, []string{more, ask, alias}) {
		t.Fatalf("three nudges = %v", l)
	}
	must(s.DismissNudge(ctx, bob, more))
	if err := s.DismissNudge(ctx, eve, ask); err != ErrNotFound {
		t.Fatalf("another's letter closed: %v", err)
	}
	// answered (go24's mark), snoozed, «Другие», or the AI saw it needs no answer — no nudge
	_, err = pool.Exec(ctx, `UPDATE mail_messages SET answered = 'reply' WHERE id = $1`, ask)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO mail_suggestions (message_id, replies) VALUES ($1, '{}')`, alias)
	must(err)
	if l := ids(); !slices.Equal(l, []string{waiting}) {
		t.Fatalf("after the answers = %v", l)
	}
	// the setting is on by default, off when saved so
	h, err := s.Handling(ctx, bob)
	must(err)
	if !h.Nudges {
		t.Fatal("nudges are off by default")
	}
	h.Nudges = false
	must(s.SaveHandling(ctx, bob, h))
	if h, _ = s.Handling(ctx, bob); h.Nudges {
		t.Fatal("nudges stay on")
	}
}
