package crm

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestLinkTriggers: «Переход по ссылке» — the trigger's address in a robot's letter becomes the card's tracked link
// (other addresses and the punctuation after it stay), the client's following it moves the deal and writes the
// history once; the link of another address moves nothing; a trigger without an address of the web is refused.
func TestLinkTriggers(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	s := NewStore(pool)
	var sent []string
	s.SendMail = func(_ context.Context, _, _, _, text string) error {
		sent = append(sent, text)
		return nil
	}
	s.LinkURL = func(token string) string { return "https://go24.test/cl/" + token }
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages
	for _, bad := range []string{"", "javascript:alert(1)", "ftp://x.test/f", "https://"} {
		if err := s.AddTrigger(ctx, boss, st[3].ID, "link", TriggerParams{URL: bad}); err == nil {
			t.Fatalf("a link trigger with %q", bad)
		}
	}
	must(s.AddTrigger(ctx, boss, st[3].ID, "link", TriggerParams{URL: " https://shop.test/offer "}))
	must(s.AddTrigger(ctx, boss, st[4].ID, "link", TriggerParams{URL: "https://shop.test/pay"}))
	if trs, _ := s.Triggers(ctx, []string{st[3].ID}); len(trs) != 1 || trs[0].Params.URL != "https://shop.test/offer" {
		t.Fatalf("the trigger = %+v", trs)
	}
	must(s.AddRobot(ctx, boss, st[1].ID, "mail", RobotParams{Text: "Предложение",
		Details: "Смотрите https://shop.test/offer. Каталог: https://shop.test/offer/all, сайт https://shop.test"}))
	contact, err := s.SaveContact(ctx, boss, Contact{Name: "Иван", Email: "ivan@client.test"})
	must(err)
	deal, err := s.SaveDeal(ctx, boss, Deal{Title: "Д", PipelineID: pipes[0].ID, Contact: Ref{ID: contact}, Responsible: Ref{ID: boss}})
	must(err)
	_, err = s.MoveDeal(ctx, boss, deal, st[1].ID)
	must(err)
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "Смотрите https://go24.test/cl/") ||
		!strings.HasSuffix(sent[0], ". Каталог: https://shop.test/offer/all, сайт https://shop.test") {
		t.Fatalf("the letter = %q", sent)
	}
	token := strings.TrimPrefix(strings.Fields(sent[0])[1], "https://go24.test/cl/")
	token = strings.TrimSuffix(token, ".")

	// once more: the same link of the card
	_, err = s.MoveDeal(ctx, boss, deal, st[0].ID)
	must(err)
	_, err = s.MoveDeal(ctx, boss, deal, st[1].ID)
	must(err)
	if len(sent) != 2 || sent[1] != sent[0] {
		t.Fatalf("the second letter = %q", sent)
	}

	if u, err := s.LinkPeek(ctx, token); err != nil || u != "https://shop.test/offer" {
		t.Fatalf("peek = %q, %v", u, err)
	}
	if d, _ := s.Deal(ctx, deal); d.Stage.ID != st[1].ID {
		t.Fatalf("a peek moved the deal")
	}
	if _, err := s.FollowLink(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("an unknown link: %v", err)
	}
	for range 2 {
		u, err := s.FollowLink(ctx, token)
		must(err)
		if u != "https://shop.test/offer" {
			t.Fatalf("goes to %q", u)
		}
	}
	if d, _ := s.Deal(ctx, deal); d.Stage.ID != st[3].ID {
		t.Fatalf("the deal is on %s, not on the trigger's stage", d.Stage.Name)
	}
	var lines, clicks int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'link_clicked' AND body = 'https://shop.test/offer'`, deal).Scan(&lines))
	must(pool.QueryRow(ctx, `SELECT clicks FROM crm_trigger_links WHERE token = $1`, token).Scan(&clicks))
	if lines != 1 || clicks != 2 {
		t.Fatalf("history lines %d, clicks %d", lines, clicks)
	}
}
