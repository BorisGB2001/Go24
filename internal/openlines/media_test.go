package openlines

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestMedia: files both ways (the visitor's link carries the file's secret), «печатает…» and «прочитано» both ways,
// the operator's photo only to the widget of a line they answered in.
func TestMedia(t *testing.T) {
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
	var boss, op string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, photo_key, photo_type) VALUES ('op', 'Оля', 'op@t', 'photos/op', 'image/png')
		RETURNING id`).Scan(&op))
	s := NewStore(pool, nil)
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Сайт", Operators: []string{op}, Active: true})
	must(err)
	other, err := s.SaveWidget(ctx, boss, Widget{Name: "Другой", Active: true})
	must(err)
	sess, token, err := s.Visit(ctx, wid, "", "")
	must(err)

	if err := s.VisitorSend(ctx, wid, token, "", nil); err == nil {
		t.Fatal("an empty message without a file")
	}
	if err := s.VisitorSend(ctx, wid, token, "", &File{Key: "k", Name: "big.zip", Size: MaxFile + 1}); err == nil {
		t.Fatal("a file over the limit")
	}
	must(s.VisitorSend(ctx, wid, token, "", &File{Key: "openlines/a/photo.png", Name: "photo.png", Type: "image/png", Size: 100}))
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Last != "" || list[0].Unread != 1 {
		t.Fatalf("sessions = %+v", list)
	}
	msgs, err := s.VisitorMessages(ctx, wid, token, 0)
	must(err)
	if len(msgs) != 1 || msgs[0].FileName != "photo.png" || !msgs[0].Image() || len(msgs[0].FileSecret) != 32 || msgs[0].Text() != "📎 photo.png" {
		t.Fatalf("the visitor's file = %+v", msgs)
	}
	f, err := s.FileOf(ctx, msgs[0].ID)
	must(err)
	if f.Key != "openlines/a/photo.png" || f.WidgetID != wid || f.SessionID != sess || !f.Secret(msgs[0].FileSecret) || f.Secret("") || f.Secret("x") {
		t.Fatalf("file = %+v", f)
	}

	// the operator opens the dialogue: the visitor sees «прочитано»; types — the visitor sees «печатает…»
	seen, err := s.VisitorSeenState(ctx, wid, token)
	must(err)
	if seen.Read != 0 || seen.Typing || seen.Operator != "" {
		t.Fatalf("before the operator = %+v", seen)
	}
	_, _, err = s.Open(ctx, op, sess)
	must(err)
	must(s.Typing(ctx, op, sess))
	seen, _ = s.VisitorSeenState(ctx, wid, token)
	if seen.Read != msgs[0].ID || !seen.Typing {
		t.Fatalf("after reading = %+v", seen)
	}
	must(s.ReplyWith(ctx, op, sess, "Вот счёт", &File{Key: "openlines/b/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 2000}))
	seen, _ = s.VisitorSeenState(ctx, wid, token)
	if seen.Typing || seen.Operator != "Оля" || !seen.Photo || seen.OperatorID != op {
		t.Fatalf("after the answer = %+v", seen)
	}
	got, _ := s.VisitorMessages(ctx, wid, token, msgs[0].ID)
	if len(got) != 1 || got[0].FileName != "bill.pdf" || got[0].Image() || got[0].Body != "Вот счёт" {
		t.Fatalf("the operator's file = %+v", got)
	}

	// the visitor reads it (the widget is open) — the operators see it; types — the page is told
	x, _, _ := s.Open(ctx, op, sess)
	if x.VisRead != 0 {
		t.Fatalf("vis_read = %d", x.VisRead)
	}
	must(s.VisitorSeen(ctx, wid, token))
	must(s.VisitorTyping(ctx, wid, token))
	x, _, _ = s.Open(ctx, op, sess)
	if x.VisRead != got[0].ID {
		t.Fatalf("vis_read = %d, want %d", x.VisRead, got[0].ID)
	}

	// the photo: to the line the operator answered in, not to another one
	key, _, err := s.OperatorPhoto(ctx, wid, op)
	must(err)
	if key != "photos/op" {
		t.Fatalf("photo = %q", key)
	}
	if key, _, _ := s.OperatorPhoto(ctx, other, op); key != "" {
		t.Fatal("the photo of an operator of another line")
	}
	if key, _, _ := s.OperatorPhoto(ctx, wid, boss); key != "" {
		t.Fatal("the photo of someone who has not answered")
	}
	if CleanName(`C:\docs\a.pdf`) != "a.pdf" || CleanName("../") != "file" {
		t.Fatal("CleanName")
	}
}
