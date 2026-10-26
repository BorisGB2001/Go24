package tasks

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestMentionText(t *testing.T) {
	if MentionAt("привет @Анна Лиина", "Анна Ли") != -1 {
		t.Error("@Анна Ли matched inside @Анна Лиина")
	}
	if MentionAt("@Анна Ли, привет", "Анна Ли") != 0 || MentionAt("ок @Анна Ли", "Анна Ли") != len("ок ") {
		t.Error("whole-name mentions not found")
	}
	if got := MarkMentions("Анна Ли, глянь", []string{"Анна Ли"}); got != "@Анна Ли, глянь" {
		t.Errorf("MarkMentions = %q", got)
	}
	if got := MarkMentions("@Анна Ли, глянь", []string{"Анна Ли"}); got != "@Анна Ли, глянь" {
		t.Errorf("an existing mention is marked twice: %q", got)
	}
}

// TestMentionComment: a mentioned colleague outside the task becomes its auditor and gets a «mention»
// notification; members mentioned get it instead of the usual one; extra mentions (from the messenger) count too.
func TestMentionComment(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, worker, anna, annaL, petr := user("Борис Г"), user("Иван Петров"), user("Анна Ли"), user("Анна Лиина"), user("Пётр С")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	must(s.SetMember(ctx, p, worker, "member"))
	key, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Отчёт", ResponsibleID: worker})
	must(err)
	id, err := s.TaskIDByKey(ctx, key)
	must(err)

	_, err = s.PostComment(ctx, boss, id, "@Анна Ли и @Иван Петров, посмотрите", petr)
	must(err)
	kind := func(u string) string {
		var k string
		_ = pool.QueryRow(ctx, `SELECT kind FROM notifications WHERE user_id = $1 ORDER BY id DESC LIMIT 1`, u).Scan(&k)
		return k
	}
	if k := kind(anna); k != "mention" {
		t.Errorf("Анна Ли: %q, want mention", k)
	}
	if k := kind(worker); k != "mention" {
		t.Errorf("the responsible person mentioned: %q, want mention", k)
	}
	if k := kind(petr); k != "mention" {
		t.Errorf("a messenger mention: %q, want mention", k)
	}
	if k := kind(annaL); k != "" {
		t.Errorf("Анна Лиина was not mentioned but got %q", k)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1`, anna).Scan(&n))
	if n != 1 {
		t.Errorf("Анна Ли got %d notifications, want 1", n)
	}
	must(s.CheckVisible(ctx, anna, id)) // she watches the task now
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_participants WHERE task_id = $1 AND role = 'auditor'`, id).Scan(&n))
	if n != 2 {
		t.Errorf("auditors = %d, want 2 (Анна Ли, Пётр С)", n)
	}
	if _, err := s.PostComment(ctx, worker, id, "спасибо"); err != nil {
		t.Fatal(err)
	}
	if k := kind(anna); k != "comment" {
		t.Errorf("an auditor now gets the task's messages: %q", k)
	}

	// the green counter: the task has unread messages for Анна (lists, the «новые комментарии» view, the sidebar)
	list, err := s.ListTasks(ctx, Filter{Viewer: anna, ProjectID: p.ID})
	must(err)
	if len(list) != 1 || list[0].Unread != 2 {
		t.Fatalf("unread in the list = %+v", list)
	}
	nc, err := s.ListTasks(ctx, Filter{UserID: anna, NewComments: true})
	must(err)
	cn, err := s.Counters(ctx, anna)
	must(err)
	if len(nc) != 1 || cn.Comments != 1 {
		t.Fatalf("new comments view %d, counter %d", len(nc), cn.Comments)
	}
	_, err = pool.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND task_id = $2`, anna, id) // she opened the task
	must(err)
	if list, _ = s.ListTasks(ctx, Filter{Viewer: anna, ProjectID: p.ID}); list[0].Unread != 0 {
		t.Fatal("still unread after opening")
	}

	// marks: a tag for everyone, favourite and pin for Анна; muted — only mentions reach her
	must(s.SetTag(ctx, worker, id, "#срочно", true))
	if err := s.SetTag(ctx, annaL, id, "x", true); err == nil {
		t.Error("someone outside the task tagged it")
	}
	key2, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Вторая", ResponsibleID: worker, Auditors: []string{anna}})
	must(err)
	id2, _ := s.TaskIDByKey(ctx, key2)
	must(s.SetMark(ctx, anna, id2, "pin", true))
	must(s.SetMark(ctx, anna, id, "favorite", true))
	list, err = s.ListTasks(ctx, Filter{Viewer: anna, ProjectID: p.ID})
	must(err)
	if list[0].ID != id2 || !list[0].Pinned || fmt.Sprint(list[1].Tags) != "[срочно]" || !list[1].Favorite {
		t.Fatalf("marks in the list = %+v", list)
	}
	if fav, _ := s.ListTasks(ctx, Filter{UserID: anna, Favorites: true}); len(fav) != 1 || fav[0].ID != id {
		t.Fatalf("favourites = %+v", fav)
	}
	if tagged, _ := s.ListTasks(ctx, Filter{Viewer: boss, Tag: "СРОЧНО"}); len(tagged) != 1 {
		t.Fatalf("by tag = %d tasks", len(tagged))
	}
	must(s.SetMark(ctx, anna, id, "mute", true))
	var before, after int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1`, anna).Scan(&before))
	_, err = s.PostComment(ctx, worker, id, "без упоминания")
	must(err)
	_, err = s.PostComment(ctx, worker, id, "@Анна Ли, а это тебе")
	must(err)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1`, anna).Scan(&after))
	if after != before+1 || kind(anna) != "mention" {
		t.Fatalf("muted: %d → %d notifications, last %q", before, after, kind(anna))
	}

	// «Оценка» by the creator, «Результат» marked by the responsible
	if err := s.Rate(ctx, worker, id, "positive"); err != ErrForbidden {
		t.Fatalf("the responsible rated the task: %v", err)
	}
	must(s.Rate(ctx, boss, id, "negative"))
	var cid string
	must(pool.QueryRow(ctx, `SELECT id FROM task_comments WHERE task_id = $1 AND body = 'спасибо'`, id).Scan(&cid))
	must(s.SetResult(ctx, worker, id, cid, true))
	res, err := s.Results(ctx, id)
	must(err)
	tk, _ := s.ListTasks(ctx, Filter{Viewer: boss, ProjectID: p.ID})
	var rating string
	for _, x := range tk {
		if x.ID == id {
			rating = x.Rating
		}
	}
	if len(res) != 1 || res[0].Body != "спасибо" || rating != "negative" {
		t.Fatalf("results %+v, rating %q", res, rating)
	}
	must(s.SetResult(ctx, worker, id, cid, false))
	if res, _ = s.Results(ctx, id); len(res) != 0 {
		t.Fatal("result not removed")
	}

	// «исполняющий обязанности»: while Иван is away, Анна Лиина works for him with the powers given — no more
	key3, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Третья", ResponsibleID: worker})
	must(err)
	id3, _ := s.TaskIDByKey(ctx, key3)
	if err := s.CheckVisible(ctx, annaL, id3); err != ErrForbidden {
		t.Fatalf("before the absence: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by, acting_id, acting_rights)
		VALUES ($1, 'vacation', current_date - 1, current_date + 1, $1, $2, '{view,work,notify}')`, worker, annaL)
	must(err)
	must(s.CheckVisible(ctx, annaL, id3))
	must(s.ApplyAction(ctx, annaL, id3, ActionStart)) // as the responsible
	var signed string
	must(pool.QueryRow(ctx, `SELECT coalesce(acting_for::text, '') FROM task_history WHERE task_id = $1 AND field = 'status' ORDER BY id DESC LIMIT 1`, id3).Scan(&signed))
	if signed != worker {
		t.Fatalf("the change is not signed «ИО за Иван»: %q", signed)
	}
	if err := s.Rate(ctx, annaL, id3, "positive"); err != ErrForbidden {
		t.Fatalf("acting without «control» rated: %v", err)
	}
	_, err = s.PostComment(ctx, boss, id3, "как дела?")
	must(err)
	var copies int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND for_user = $2 AND task_id = $3`, annaL, worker, id3).Scan(&copies))
	if copies == 0 {
		t.Fatal("the acting person got no copy of the notifications")
	}
	_, err = pool.Exec(ctx, `UPDATE absences SET ends_on = current_date - 1, starts_on = current_date - 2 WHERE user_id = $1`, worker)
	must(err)
	if err := s.CheckVisible(ctx, annaL, id3); err != ErrForbidden {
		t.Fatalf("after the absence: %v", err)
	}

	// the task form saves the whole lists: Пётр becomes an accomplice (no longer an auditor), Анна Лиина is added
	must(s.SetParticipants(ctx, boss, id, []string{petr, ""}, []string{anna, annaL}))
	rows, err := pool.Query(ctx, `SELECT u.name || ':' || p.role FROM task_participants p JOIN users u ON u.id = p.user_id
		WHERE p.task_id = $1 ORDER BY 1`, id)
	must(err)
	var got []string
	for rows.Next() {
		var x string
		must(rows.Scan(&x))
		got = append(got, x)
	}
	if fmt.Sprint(got) != "[Анна Ли:auditor Анна Лиина:auditor Пётр С:accomplice]" {
		t.Errorf("participants = %v", got)
	}
	if k := kind(annaL); k != "participant" {
		t.Errorf("a newcomer from the form: %q, want participant", k)
	}
	if err := s.SetParticipants(ctx, annaL, id, nil, nil); err != ErrForbidden {
		t.Errorf("an auditor changed the lists: %v", err)
	}
}
