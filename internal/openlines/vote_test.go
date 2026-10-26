package openlines

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestVote: the visitor rates the answer once an operator has answered (and may change it), the head gives their own
// rating, the visitor gets the conversation by e-mail from the operator's mailbox.
func TestVote(t *testing.T) {
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
	isErr := func(err error, key string) {
		t.Helper()
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, a := newUser("boss"), newUser("a")
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, args ...any) string {
		out := key
		for _, x := range args {
			out += " " + x.(string)
		}
		return out
	}
	type letter struct{ from, to, subject, text string }
	var sent []letter
	s.SendMail = func(_ context.Context, userID, to, subject, text string) error {
		sent = append(sent, letter{userID, to, subject, text})
		return nil
	}
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Линия", Active: true, Operators: []string{a}, VoteOn: true, HistoryOn: true})
	must(err)
	sid, tok, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok, "Где мой заказ?"))

	st, err := s.VisitorState(ctx, wid, tok)
	must(err)
	if st.CanVote || !st.History || st.Closed {
		t.Fatalf("nobody has answered yet: %+v", st)
	}
	isErr(s.VisitorVote(ctx, wid, tok, 1, ""), "err.ol_vote")

	must(s.Reply(ctx, a, sid, "Уже в пути"))
	must(s.SetStatus(ctx, a, sid, false))
	if st, _ = s.VisitorState(ctx, wid, tok); !st.CanVote || !st.Closed {
		t.Fatalf("an answered, closed dialogue is rated: %+v", st)
	}
	isErr(s.VisitorVote(ctx, wid, tok, 2, ""), "err.ol_vote")
	must(s.VisitorVote(ctx, wid, tok, 1, ""))
	must(s.VisitorVote(ctx, wid, tok, 1, "")) // the same again: no second service line
	must(s.VisitorVote(ctx, wid, tok, -1, "  Долго  "))
	x, msgs, err := s.Open(ctx, a, sid)
	must(err)
	if x.Vote != -1 || x.VoteComment != "Долго" {
		t.Fatalf("the visitor changed the rating: %+v", x)
	}
	notes := []string{}
	for _, m := range msgs {
		if m.Kind == "note" && strings.HasPrefix(m.Body, "ol.note_vote") {
			notes = append(notes, m.Body)
		}
	}
	if len(notes) != 2 || notes[0] != "ol.note_vote 👍" || notes[1] != "ol.note_vote_comment 👎 Долго" {
		t.Fatalf("service lines of the rating: %q", notes)
	}
	if st, _ = s.VisitorState(ctx, wid, tok); st.Vote != -1 {
		t.Fatalf("the widget shows the rating: %+v", st)
	}
	list, err := s.Sessions(ctx, Filter{Who: Who{All: true}, Tab: "closed"})
	must(err)
	if len(list) != 1 || list[0].Vote != -1 {
		t.Fatalf("the list shows the rating: %+v", list)
	}

	// the head's rating, and taking it back
	must(s.HeadVote(ctx, boss, sid, 1, "Всё верно"))
	if x, _, _ = s.Open(ctx, a, sid); x.HeadVote != 1 || x.HeadComment != "Всё верно" || x.HeadName != "boss" {
		t.Fatalf("the head's rating: %+v", x)
	}
	must(s.HeadVote(ctx, boss, sid, 0, "x"))
	if x, _, _ = s.Open(ctx, a, sid); x.HeadVote != 0 || x.HeadComment != "" || x.HeadName != "" {
		t.Fatalf("the head's rating taken back: %+v", x)
	}
	isErr(s.HeadVote(ctx, boss, sid, 5, ""), "err.ol_vote")

	// the conversation by e-mail: from the operator, without service lines; the address stays with the dialogue
	isErr(s.SendHistory(ctx, wid, tok, "not an address"), "err.ol_email")
	must(s.SendHistory(ctx, wid, tok, "ivan@shop.ru"))
	if len(sent) != 1 || sent[0].from != a || sent[0].to != "ivan@shop.ru" || sent[0].subject != "ol.history_subject Линия" {
		t.Fatalf("the letter: %+v", sent)
	}
	if !strings.Contains(sent[0].text, "Где мой заказ?") || !strings.Contains(sent[0].text, "a (") || !strings.Contains(sent[0].text, "Уже в пути") ||
		strings.Contains(sent[0].text, "ol.note") {
		t.Fatalf("the letter's text: %s", sent[0].text)
	}
	if st, _ = s.VisitorState(ctx, wid, tok); st.Email != "ivan@shop.ru" {
		t.Fatalf("the address is kept: %+v", st)
	}
	isErr(s.SendHistory(ctx, wid, tok, "ivan@shop.ru"), "err.ol_flood") // not again at once

	// the line asks no rating and sends no conversation
	w, err := s.Widget(ctx, wid)
	must(err)
	w.VoteOn, w.HistoryOn = false, false
	_, err = s.SaveWidget(ctx, boss, w)
	must(err)
	if st, _ = s.VisitorState(ctx, wid, tok); st.CanVote || st.History {
		t.Fatalf("rating and the conversation are off: %+v", st)
	}
	isErr(s.VisitorVote(ctx, wid, tok, 1, ""), "err.ol_vote")
	must(pool.QueryRow(ctx, `UPDATE ol_sessions SET history_sent_at = NULL WHERE id = $1 RETURNING id`, sid).Scan(new(string)))
	isErr(s.SendHistory(ctx, wid, tok, "ivan@shop.ru"), "err.ol_history")
}
