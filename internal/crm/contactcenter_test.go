package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestContactCenter: a channel is connected by its active lines, forms, mailboxes…; failing ones are counted apart;
// the requests are only the inbound channels' of the last RecentDays.
func TestContactCenter(t *testing.T) {
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
	s := NewStore(pool)
	now := time.Now()
	get := func() map[string]Channel {
		t.Helper()
		list, err := s.ContactCenter(ctx, now)
		must(err)
		if len(list) != 9 || list[0].Key != "widget" {
			t.Fatalf("channels: %+v", list)
		}
		m := map[string]Channel{}
		for _, c := range list {
			m[c.Key] = c
		}
		return m
	}
	for k, c := range get() {
		if c.Count != 0 || c.Errors != 0 || c.Recent != 0 || c.URL == "" {
			t.Fatalf("empty company, %s: %+v", k, c)
		}
	}

	var boss, w1, w2 string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	// two lines, one with a Telegram bot that fails, and a switched-off one with a bot that does not count
	must(pool.QueryRow(ctx, `INSERT INTO ol_widgets (name, created_by) VALUES ('Сайт', $1) RETURNING id`, boss).Scan(&w1))
	must(pool.QueryRow(ctx, `INSERT INTO ol_widgets (name, created_by, tg_secret, tg_error) VALUES ('Бот', $1, '\x01', 'Unauthorized') RETURNING id`, boss).Scan(&w2))
	_, err = pool.Exec(ctx, `INSERT INTO ol_widgets (name, created_by, active, tg_secret) VALUES ('Старая', $1, false, '\x01')`, boss)
	must(err)
	// chats: two on the site this month, one older; one in Telegram
	_, err = pool.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, channel, created_at) VALUES
		($1, '\x01', 'widget', now()), ($1, '\x02', 'widget', now() - interval '3 days'), ($1, '\x03', 'widget', now() - interval '40 days'),
		($2, '\x04', 'telegram', now())`, w1, w2)
	must(err)
	// a form with a result, a switched-off form; a mailbox that does not sync
	var f string
	must(pool.QueryRow(ctx, `INSERT INTO crm_forms (token, name) VALUES ('f1', 'Заявка') RETURNING id`).Scan(&f))
	_, err = pool.Exec(ctx, `INSERT INTO crm_forms (token, name, active) VALUES ('f2', 'Старая', false)`)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO crm_form_results (form_id, answers) VALUES ($1, '{}')`, f)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO mailboxes (user_id, email, imap_host, imap_port, smtp_host, smtp_port, login, secret, error)
		VALUES ($1, 'a@t.ru', 'imap', 993, 'smtp', 465, 'a', '\x01', 'auth failed'), ($1, 'b@t.ru', 'imap', 993, 'smtp', 465, 'b', '\x01', '')`, boss)
	must(err)

	m := get()
	if c := m["widget"]; c.Count != 2 || c.Recent != 2 || !c.Inbound {
		t.Fatalf("widget: %+v", c) // all the active lines (the bot's one too), the site's chats of the month
	}
	if c := m["telegram"]; c.Count != 1 || c.Errors != 1 || c.Recent != 1 {
		t.Fatalf("telegram: %+v", c)
	}
	if c := m["forms"]; c.Count != 1 || c.Recent != 1 {
		t.Fatalf("forms: %+v", c)
	}
	if c := m["mail"]; c.Count != 2 || c.Errors != 1 || c.Recent != 0 || c.Inbound {
		t.Fatalf("mail: %+v", c)
	}
	if c := m["telephony"]; c.Count != 0 || c.URL != "/settings/telephony#trunks" {
		t.Fatalf("telephony: %+v", c)
	}
}
