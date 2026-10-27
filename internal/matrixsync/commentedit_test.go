package matrixsync

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestTaskChatEdits: an answer goes to Matrix as a reply to the message within the task's thread, an edit as
// m.replace by the author, a deletion as a redaction; the same from the messenger comes back to go24 (an answer
// quotes, an edit of someone else's message is ignored, a redaction removes the message).
func TestTaskChatEdits(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	var ann, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-ann', 'ann', 'ann') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-bob', 'Боб', 'bob') RETURNING id`).Scan(&bob))
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ts := tasks.NewStore(pool)
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: ts,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	_, err = ts.CreateProject(ctx, ann, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, ann, "GO")
	must(ts.SetMember(ctx, p, bob, "member"))
	must(s.Reconcile(ctx))
	key, err := ts.CreateTask(ctx, ann, p, tasks.NewTask{Title: "Отчёт", ResponsibleID: bob})
	must(err)
	tid, _ := ts.TaskIDByKey(ctx, key)
	gid := p.ID
	proom, _ := s.roomOf(ctx, "project", &gid)
	var root string
	must(pool.QueryRow(ctx, `SELECT matrix_thread FROM tasks WHERE id = $1`, tid).Scan(&root))

	q, err := s.PostTaskComment(ctx, ann, tid, "Когда?")
	must(err)
	qev := fake.sent[len(fake.sent)-1].ID
	if root == "" {
		must(pool.QueryRow(ctx, `SELECT matrix_thread FROM tasks WHERE id = $1`, tid).Scan(&root))
	}
	a, err := s.PostTaskReply(ctx, bob, tid, q.ID, "Завтра")
	must(err)
	sent := fake.sent[len(fake.sent)-1]
	rel, _ := sent.Content["m.relates_to"].(map[string]any)
	irt, _ := rel["m.in_reply_to"].(map[string]any)
	if a.ReplyTo != q.ID || rel["rel_type"] != "m.thread" || rel["event_id"] != root || irt["event_id"] != qev || rel["is_falling_back"] != false {
		t.Fatalf("reply sent as %+v (root %s, q %s)", sent.Content, root, qev)
	}
	aev := sent.ID

	// edit: m.replace from the author; someone else may not
	if _, err := s.EditTaskComment(ctx, ann, tid, a.ID, "чужое"); err != tasks.ErrForbidden {
		t.Fatalf("someone else edits: %v", err)
	}
	e, err := s.EditTaskComment(ctx, bob, tid, a.ID, "Сегодня")
	must(err)
	sent = fake.sent[len(fake.sent)-1]
	rel, _ = sent.Content["m.relates_to"].(map[string]any)
	if nc, _ := sent.Content["m.new_content"].(map[string]any); rel["rel_type"] != "m.replace" || rel["event_id"] != aev || nc["body"] != "Сегодня" ||
		sent.Sender != "@bob:t.lan" || e.Body != "Сегодня" || e.EditedAt == nil {
		t.Fatalf("edit sent as %+v", sent)
	}
	// … and go24's own edit coming back from the sync changes nothing
	must(s.inbound(ctx, proom, matrix.Event{EventID: sent.ID, Type: "m.room.message", Sender: "@bob:t.lan", Content: sent.Content}))

	// from the messenger: an answer with a quote, an edit by the author (someone else's is ignored), a redaction
	must(s.inbound(ctx, proom, matrix.Event{EventID: "$in1", Type: "m.room.message", Sender: "@ann:t.lan", Content: map[string]any{
		"msgtype": "m.text", "body": "Хорошо", "m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": root,
			"is_falling_back": false, "m.in_reply_to": map[string]any{"event_id": aev}}}}))
	_, inID, err := ts.CommentByEvent(ctx, "$in1")
	must(err)
	in, err := ts.CommentByID(ctx, tid, inID)
	must(err)
	if in.ReplyTo != a.ID || in.Body != "Хорошо" {
		t.Fatalf("an answer from the messenger: %+v", in)
	}
	edit := func(ev, sender, target, body string) {
		must(s.inbound(ctx, proom, matrix.Event{EventID: ev, Type: "m.room.message", Sender: sender, Content: map[string]any{
			"msgtype": "m.text", "body": "* " + body, "m.new_content": map[string]any{"msgtype": "m.text", "body": body},
			"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": target}}}))
	}
	edit("$in2", "@bob:t.lan", "$in1", "взлом")
	edit("$in3", "@ann:t.lan", "$in1", "Хорошо, жду")
	if in, _ = ts.CommentByID(ctx, tid, inID); in.Body != "Хорошо, жду" || in.EditedAt == nil {
		t.Fatalf("edits from the messenger: %+v", in)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_comments WHERE task_id = $1`, tid).Scan(&n))
	if n != 3 {
		t.Fatalf("an edit made a new message: %d", n)
	}
	must(s.inbound(ctx, proom, matrix.Event{EventID: "$in4", Type: "m.room.redaction", Sender: "@ann:t.lan", Redacts: "$in1"}))
	if _, err := ts.CommentByID(ctx, tid, inID); err != tasks.ErrNotFound {
		t.Fatalf("a redaction from the messenger: %v", err)
	}

	// delete in go24: the bot redacts the message
	if err := s.DeleteTaskComment(ctx, ann, tid, a.ID); err != tasks.ErrForbidden {
		t.Fatalf("someone else deletes: %v", err)
	}
	must(s.DeleteTaskComment(ctx, bob, tid, a.ID))
	if fake.redacted[len(fake.redacted)-1] != aev {
		t.Fatalf("redacted = %v", fake.redacted)
	}
	if _, err := ts.CommentByID(ctx, tid, a.ID); err != tasks.ErrNotFound {
		t.Fatalf("deleted: %v", err)
	}
}
