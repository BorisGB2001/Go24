package bp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCommands: a process with statuses state-machine one — «Установить статус» shows the status,
// «Команды» waits till one of its people presses a button, which goes to its step, back to a status before as well.
func TestCommands(t *testing.T) {
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
	boss, ann, bob, eve := newUser("boss"), newUser("ann"), newUser("bob"), newUser("eve")
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	var ve *i18n.Error
	is := func(err error, key string) {
		t.Helper()
		if !errors.As(err, &ve) || ve.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	note := Step{Kind: "notify", To: "starter", Text: "x"}
	for key, steps := range map[string][][]Step{
		"err.bp_step": {
			{{Kind: "commands", To: boss}}, // no buttons
			{note, {Kind: "notify", To: "starter", Text: "x", Commands: []Command{{Title: "A"}}}}, // buttons of another step
			{{Kind: "commands", To: boss, Commands: []Command{{Title: "A"}, {Title: "A"}}}},       // the same title twice
			{{Kind: "commands", To: boss, Commands: []Command{{Title: " "}}}},
			{{Kind: "status"}}, // no status
			{{Kind: "commands", To: boss, Goto: 1, Commands: []Command{{Title: "A"}}}, note}, // where to without the time to wait
		},
		"err.bp_goto": {
			{note, {Kind: "commands", To: boss, Commands: []Command{{Title: "A", Goto: 2}}}}, // to itself
			{note, {Kind: "commands", To: boss, Commands: []Command{{Title: "A", Goto: 3}}}}, // nowhere
			{{Kind: "parallel", Branches: []int{3}, Goto: 4}, note, note, {Kind: "commands", To: boss, Commands: []Command{{Title: "A", Goto: 1}}}},
			{note, {Kind: "commands", To: boss, Timeout: 60, Goto: 2, Commands: []Command{{Title: "A"}}}}, // no one pressed — to itself
		},
		"err.bp_parallel": {
			{{Kind: "parallel", Branches: []int{3}, Goto: 4}, {Kind: "commands", To: boss, Commands: []Command{{Title: "A"}}}, note, note}, // in a branch
			{{Kind: "commands", To: boss, Commands: []Command{{Title: "A", Goto: 3}}}, {Kind: "parallel", Branches: []int{4}, Goto: 5}, note, note, note},
		},
	} {
		for _, st := range steps {
			_, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Steps: st})
			is(err, key)
		}
	}

	// Черновик → На согласовании: boss approves (→ Утверждено) or sends it back to the draft, where ann sends it again
	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Договор", Active: true, Steps: []Step{
		{Kind: "status", Text: "Черновик"},
		{Kind: "commands", To: "starter", Text: "Отправить?", Commands: []Command{{Title: " На согласование "}}},
		{Kind: "status", Text: "На согласовании у {starter}"},
		{Kind: "commands", To: boss, Users: []string{bob}, Commands: []Command{{Title: "Утвердить", Goto: 6}, {Title: "Вернуть", Goto: 1}}},
		{Kind: "notify", To: "starter", Text: "не доходит"},
		{Kind: "status", Text: "Утверждено"}}})
	must(err)
	if st, _ := s.Template(ctx, tpl); st.Steps[1].Commands[0].Title != "На согласование" {
		t.Fatalf("title %q", st.Steps[1].Commands[0].Title)
	}
	id, err := s.Start(ctx, ann, tpl, "", nil)
	must(err)
	x, _ := s.Instance(ctx, ann, id)
	if x.Label != "Черновик" || x.Step != 1 || x.Status != "running" {
		t.Fatalf("label %q, step %d, %s", x.Label, x.Step, x.Status)
	}
	if c := s.Commands(ctx, boss, x); c != nil {
		t.Fatalf("boss has %v", c)
	}
	if c := s.Commands(ctx, ann, x); len(c) != 1 {
		t.Fatalf("ann has %v", c)
	}
	is(s.Command(ctx, boss, id, 1, 0), "err.bp_command") // not his
	is(s.Command(ctx, ann, id, 3, 0), "err.bp_command")  // not the step it waits on
	is(s.Command(ctx, ann, id, 1, 1), "err.bp_command")  // no such button
	press := func(user string, step, n int, label string) {
		t.Helper()
		must(s.Command(ctx, user, id, step, n))
		if x, _ = s.Instance(ctx, ann, id); x.Label != label {
			t.Fatalf("label %q, want %q", x.Label, label)
		}
	}
	press(ann, 1, 0, "На согласовании у ann")
	if x.Step != 3 || len(s.Commands(ctx, bob, x)) != 2 || s.Commands(ctx, ann, x) != nil || len(s.Commands(tasks.WithAdmin(ctx), ann, x)) != 2 {
		t.Fatalf("step %d", x.Step)
	}
	if _, err := s.Instance(ctx, bob, id); err != nil { // the page with his buttons
		t.Fatal(err)
	}
	if _, err := s.Instance(ctx, eve, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("eve sees it: %v", err)
	}
	press(bob, 3, 1, "Черновик")
	if _, err := s.Instance(ctx, bob, id); err != nil { // who pressed a button
		t.Fatal(err)
	}
	is(s.Command(ctx, boss, id, 3, 0), "err.bp_command") // the page of before
	press(ann, 1, 0, "На согласовании у ann")
	press(boss, 3, 0, "Утверждено")
	if x.Status != "done" {
		t.Fatalf("status %s", x.Status)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE body LIKE '%не доходит%'`).Scan(&notes))
	if notes != 0 {
		t.Fatal("the step past the jump is done")
	}
	lines, _ := s.Log(ctx, id)
	var cmds []string
	for _, l := range lines {
		if l.Kind == "command" {
			cmds = append(cmds, l.Actor+":"+l.Text)
		}
	}
	if len(cmds) != 4 || cmds[1] != "bob:Вернуть" || cmds[3] != "boss:Утвердить" {
		t.Fatalf("history %v", cmds)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND body LIKE '🔘%'`, bob).Scan(&notes))
	if notes != 2 {
		t.Fatalf("bob is told %d times", notes)
	}
}

// TestCommandsTimeout: «Ожидать не дольше» of «Команды» «Обработчик событий» — a command or the time,
// whichever comes first: no one pressed — the process goes to the step's own one; pressed in time — the time is off.
func TestCommandsTimeout(t *testing.T) {
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
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Звонок", Active: true, Steps: []Step{
		{Kind: "status", Text: "Ждём"},
		{Kind: "commands", To: boss, Timeout: 90, Goto: 5, Commands: []Command{{Title: "Перезвонил"}}},
		{Kind: "status", Text: "Перезвонил"},
		{Kind: "end"},
		{Kind: "status", Text: "Не перезвонил"}}})
	must(err)
	start := func() string {
		t.Helper()
		id, err := s.Start(ctx, boss, tpl, "", nil)
		must(err)
		return id
	}
	label := func(id string) string {
		t.Helper()
		x, err := s.Instance(ctx, boss, id)
		must(err)
		return x.Label + "/" + x.Status
	}
	late, early := start(), start()
	if errs := s.WakeDue(ctx, time.Now().Add(80*time.Minute)); len(errs) > 0 {
		t.Fatal(errs)
	}
	if l := label(late); l != "Ждём/running" {
		t.Fatalf("too early: %s", l)
	}
	must(s.Command(ctx, boss, early, 1, 0))
	if errs := s.WakeDue(ctx, time.Now().Add(100*time.Minute)); len(errs) > 0 {
		t.Fatal(errs)
	}
	if l := label(late); l != "Не перезвонил/done" {
		t.Fatalf("no one pressed: %s", l)
	}
	if l := label(early); l != "Перезвонил/done" {
		t.Fatalf("pressed in time: %s", l)
	}
	lines, _ := s.Log(ctx, late)
	var timeouts int
	for _, l := range lines {
		if l.Kind == "timeout_commands" && l.Step == 1 {
			timeouts++
		}
	}
	if timeouts != 1 {
		t.Fatalf("history %v", lines)
	}
}
