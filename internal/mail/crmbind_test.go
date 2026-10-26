package mail

import (
	"context"
	"net/smtp"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCRMBind runs against GreenMail and the test database (as TestMailbox): with the mailbox's settings off nothing
// is bound; on, a letter from a contact goes into the contact and its open deal (not a won one), one from a new
// address makes a lead and the next one of that address goes into it, a mailing makes nothing; a letter sent goes
// into the cards of its recipient.
func TestCRMBind(t *testing.T) {
	url, host := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_MAIL_HOST")
	if url == "" || host == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_MAIL_HOST not set")
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
	const me = "crmbind@go24.test"
	send := func(from, subject string) {
		t.Helper()
		must(smtp.SendMail(host+":3025", nil, from, []string{me},
			[]byte("From: "+from+"\r\nTo: "+me+"\r\nSubject: "+subject+"\r\n\r\nText\r\n")))
	}
	var anna, contact, open, won string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna5', 'Анна', $1) RETURNING id`, me).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO crm_contacts (name, email, responsible_id, created_by) VALUES ('Client', 'Client@Buyer.test', $1, $1)
		RETURNING id`, anna).Scan(&contact))
	for _, d := range []struct {
		semantic string
		id       *string
	}{{"process", &open}, {"success", &won}} {
		must(pool.QueryRow(ctx, `INSERT INTO crm_deals (title, pipeline_id, stage_id, contact_id, responsible_id, created_by)
			SELECT 'Deal', pipeline_id, id, $2, $1, $1 FROM crm_stages WHERE entity = 'deal' AND semantic = $3 ORDER BY sort LIMIT 1 RETURNING id`,
			anna, contact, d.semantic).Scan(d.id))
	}
	s := NewStore(pool, "test-secret")
	var leads []NewLead
	s.Lead = func(ctx context.Context, owner string, l NewLead) (id string, err error) {
		leads = append(leads, l)
		err = pool.QueryRow(ctx, `INSERT INTO crm_leads (title, name, email, stage_id, responsible_id, created_by)
			SELECT $1, $2, $3, id, $4, $4 FROM crm_stages WHERE code = 'lead_new' RETURNING id`, l.Title, l.Name, l.Email, owner).Scan(&id)
		return id, err
	}
	send("client@buyer.test", "Before")
	box, err := s.Add(ctx, anna, Settings{Email: me, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	cards := func(subject string) map[string]string { // entity → id of the letter's cards
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT c.entity, c.entity_id::text FROM crm_mail c JOIN mail_messages x ON x.id = c.message_id
			WHERE x.subject = $1`, subject)
		must(err)
		out := map[string]string{}
		for rows.Next() {
			var e, id string
			must(rows.Scan(&e, &id))
			out[e] = id
		}
		must(rows.Err())
		return out
	}

	send("client@buyer.test", "Off")
	must(s.Sync(ctx, box))
	if c := cards("Off"); len(c) != 0 || len(leads) != 0 {
		t.Fatalf("bound with the settings off: %v, leads %v", c, leads)
	}
	if err := s.SetMailboxCRM(ctx, "00000000-0000-0000-0000-000000000000", box, BoxCRM{Bind: true}); err != ErrNotFound {
		t.Fatalf("someone else's mailbox: %v", err)
	}
	must(s.SetMailboxCRM(ctx, anna, box, BoxCRM{Bind: true, Leads: true}))
	if b, err := s.MailboxCRM(ctx, anna, box); err != nil || !b.Bind || !b.Leads {
		t.Fatalf("settings = %+v, %v", b, err)
	}

	send("client@buyer.test", "Price")
	send("Stranger <stranger@new.test>", "Offer")
	send("noreply@shop.test", "Sale")
	must(s.Sync(ctx, box))
	if c := cards("Price"); len(c) != 2 || c["contact"] != contact || c["deal"] != open {
		t.Fatalf("a contact's letter is in %v (the contact %s, the open deal %s, not the won %s)", c, contact, open, won)
	}
	if len(leads) != 1 || leads[0].Email != "stranger@new.test" || leads[0].Name != "Stranger" || leads[0].Title != "Offer" {
		t.Fatalf("leads = %+v", leads)
	}
	lead := cards("Offer")["lead"]
	var leadOf string
	must(pool.QueryRow(ctx, `SELECT coalesce(lead_id::text, '') FROM mail_messages WHERE subject = 'Offer'`).Scan(&leadOf))
	if lead == "" || leadOf != lead {
		t.Fatalf("the new address's letter: lead %q, the letter's lead %q", lead, leadOf)
	}
	if c := cards("Sale"); len(c) != 0 {
		t.Fatalf("a mailing is in %v", c)
	}
	send("stranger@new.test", "Again")
	must(s.Sync(ctx, box))
	if c := cards("Again"); len(leads) != 1 || c["lead"] != lead {
		t.Fatalf("the next letter of the address: %v, leads %d", c, len(leads))
	}

	// a letter sent goes into the cards of its recipient
	id, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, To: `"Client" <client@buyer.test>`, Subject: "Invoice", HTML: "<p>Invoice</p>"})
	must(err)
	must(s.SendDraft(ctx, anna, id))
	must(s.Sync(ctx, box))
	if c := cards("Invoice"); c["contact"] != contact || c["deal"] != open {
		t.Fatalf("the sent letter is in %v", c)
	}
}
