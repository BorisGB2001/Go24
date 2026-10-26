package openlines

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestWidget: a visitor starts a dialogue, the queue is notified, an operator answers and takes it, the contact
// form makes one lead; another widget's token is not this one's.
func TestWidget(t *testing.T) {
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
	boss, op1, op2 := newUser("boss"), newUser("op1"), newUser("op2")
	s := NewStore(pool, crm.NewStore(pool))
	var ve *i18n.Error
	if _, err := s.SaveWidget(ctx, boss, Widget{Name: "Сайт", Origins: []string{"javascript:x"}}); !errors.As(err, &ve) {
		t.Fatalf("a bad origin: %v", err)
	}
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Сайт", Color: "#00AA00", Origins: []string{"https://shop.example/"}, Operators: []string{op1, op2}, Active: true})
	must(err)
	w, _ := s.Widget(ctx, wid)
	if !w.Allowed("https://shop.example") || w.Allowed("https://evil.example") || w.Color != "00AA00" || len(w.Operators) != 2 {
		t.Fatalf("widget = %+v", w)
	}

	sess, token, err := s.Visit(ctx, wid, "", "https://shop.example/catalog")
	must(err)
	again, same, err := s.Visit(ctx, wid, token, "")
	must(err)
	if again != sess || same != token || len(token) < 32 {
		t.Fatal("the token must bring the visitor back to their dialogue")
	}
	must(s.VisitorSay(ctx, wid, token, "Есть доставка?"))
	must(s.VisitorSay(ctx, wid, token, "И самовывоз?"))
	note := func(user string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'openline' AND user_id = $1`, user).Scan(&n))
		return n
	}
	if note(op1) != 1 || note(op2) != 1 || note(boss) != 0 { // once per unread dialogue, to the queue
		t.Fatalf("notified: op1 %d, op2 %d, boss %d", note(op1), note(op2), note(boss))
	}
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Unread != 2 || list[0].Last != "И самовывоз?" {
		t.Fatalf("sessions = %+v", list)
	}
	must(s.Reply(ctx, op2, sess, "Да, бесплатно от 3000 ₽"))
	msgs, _ := s.VisitorMessages(ctx, wid, token, 2)
	if len(msgs) != 1 || msgs[0].Operator != "op2" {
		t.Fatalf("the visitor sees = %+v", msgs)
	}
	must(s.VisitorSay(ctx, wid, token, "Спасибо"))
	if note(op2) != 2 || note(op1) != 1 { // now only the operator who took it
		t.Fatalf("after taking: op1 %d, op2 %d", note(op1), note(op2))
	}
	if err := s.Contact(ctx, wid, token, "Иван", "", "", "", "Сайт"); !errors.As(err, &ve) {
		t.Fatalf("a contact without phone or e-mail: %v", err)
	}
	must(s.Contact(ctx, wid, token, "Иван", "+7 900 000-00-00", "", "Нужен счёт", "Сайт"))
	must(s.Contact(ctx, wid, token, "Иван", "+7 900 000-00-00", "", "ещё раз", "Сайт"))
	var leads int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_leads WHERE source = 'Сайт' AND phone = '+7 900 000-00-00'`).Scan(&leads))
	if leads != 1 {
		t.Fatalf("leads = %d", leads)
	}
	other, _ := s.SaveWidget(ctx, boss, Widget{Name: "Другой", Active: true})
	if _, err := s.VisitorMessages(ctx, other, token, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a token used on another widget: %v", err)
	}
	must(s.SetStatus(ctx, op1, sess, false))
	if list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}, Tab: "closed"}); len(list) != 1 {
		t.Fatal("closed dialogue")
	}
}
