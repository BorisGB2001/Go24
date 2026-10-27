package company

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestLearning: «Обучение» — who keeps and assigns courses, who sees them (drafts, «Кто видит» by department with
// sub-departments, assigned), lessons in order, the progress, assignments with notifications and the learners' list.
func TestLearning(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	hr, lead, ann, bob, out := newUser("hr"), newUser("lead"), newUser("ann"), newUser("bob"), newUser("out")
	s := NewStore(pool)
	must(s.EnsureRoot(ctx, "Компания"))
	root, _, err := s.Tree(ctx)
	must(err)
	sales, err := s.CreateDepartment(ctx, root.ID, "Продажи", lead)
	must(err)
	b2b, err := s.CreateDepartment(ctx, sales, "Корпоративные", "")
	must(err)
	other, err := s.CreateDepartment(ctx, root.ID, "Склад", "")
	must(err)
	must(s.SetDepartments(ctx, hr, ann, []string{sales}))
	must(s.SetDepartments(ctx, hr, bob, []string{b2b}))
	must(s.SetDepartments(ctx, hr, out, []string{other}))

	// rights: an HR officer with «learning» keeps the courses; a head assigns; an employee neither
	must(s.SetHR(ctx, hr, true, []string{"learning"}))
	for _, c := range []struct {
		who          string
		keep, assign bool
	}{{hr, true, true}, {lead, false, true}, {ann, false, false}} {
		keep, err := s.CanTeach(ctx, c.who, false)
		must(err)
		assign, err := s.CanAssign(ctx, c.who, false)
		must(err)
		if keep != c.keep || assign != c.assign {
			t.Fatalf("%s: keep %v assign %v", c.who, keep, assign)
		}
	}

	if _, err := s.SaveCourse(ctx, hr, Course{Title: "  "}); err == nil {
		t.Fatal("a course without a title")
	}
	draft, err := s.SaveCourse(ctx, hr, Course{Title: "Черновик", Color: "nonsense"})
	must(err)
	sell, err := s.SaveCourse(ctx, hr, Course{Title: "Продажи B2B", Description: "Как продавать", Color: "green", Published: true})
	must(err)
	must(s.SetCourseAccess(ctx, sell, []string{"dept:" + sales, ""}))
	open, err := s.SaveCourse(ctx, hr, Course{Title: "Для всех", Published: true})
	must(err)
	c, err := s.Course(ctx, draft, hr, true)
	must(err)
	if c.Color != "blue" || c.Published || c.AuthorName != "hr" {
		t.Fatalf("draft: %+v", c)
	}

	// who sees what: the draft — nobody but the keepers; the sales course — the department with b2b, not the warehouse
	sees := func(who string, want ...string) {
		t.Helper()
		list, err := s.Courses(ctx, who, false, CourseFilter{})
		must(err)
		got := map[string]bool{}
		for _, c := range list {
			got[c.ID] = true
		}
		if len(got) != len(want) {
			t.Fatalf("%s sees %v, want %v", who, got, want)
		}
		for _, id := range want {
			if !got[id] {
				t.Fatalf("%s does not see %s", who, id)
			}
		}
	}
	sees(bob, sell, open)
	sees(out, open)
	sees(lead, sell, open) // the head of the department, not one of its employees
	if _, err := s.Course(ctx, sell, out, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("out opens the sales course: %v", err)
	}
	if list, _ := s.Courses(ctx, hr, true, CourseFilter{Query: "продав"}); len(list) != 1 || list[0].ID != sell {
		t.Fatalf("search: %+v", list)
	}

	// lessons in order, moved, the progress
	l1, err := s.SaveLesson(ctx, Lesson{CourseID: sell, Chapter: "Основы", Title: "Клиент", Body: "# Кто клиент"})
	must(err)
	l2, err := s.SaveLesson(ctx, Lesson{CourseID: sell, Chapter: "Основы", Title: "Звонок"})
	must(err)
	l3, err := s.SaveLesson(ctx, Lesson{CourseID: sell, Chapter: "Сделка", Title: "Договор"})
	must(err)
	if _, err := s.SaveLesson(ctx, Lesson{CourseID: "00000000-0000-0000-0000-000000000000", Title: "X"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a lesson of no course: %v", err)
	}
	must(s.MoveLesson(ctx, sell, l2, true))
	must(s.MoveLesson(ctx, sell, l2, true)) // already first
	lessons, err := s.Lessons(ctx, sell, bob)
	must(err)
	if len(lessons) != 3 || lessons[0].ID != l2 || lessons[1].ID != l1 || lessons[2].ID != l3 {
		t.Fatalf("order: %+v", lessons)
	}
	must(s.MarkLesson(ctx, bob, sell, l1, true))
	must(s.MarkLesson(ctx, bob, sell, l1, true))
	must(s.MarkLesson(ctx, bob, open, l2, true)) // a lesson of another course: nothing
	c, err = s.Course(ctx, sell, bob, false)
	must(err)
	if c.Lessons != 3 || c.Done != 1 || c.Percent() != 33 || c.Finished() || c.Assigned {
		t.Fatalf("bob's progress: %+v", c)
	}
	if mine, _ := s.Courses(ctx, bob, false, CourseFilter{Mine: true}); len(mine) != 1 || mine[0].ID != sell {
		t.Fatalf("bob's courses (begun): %+v", mine)
	}

	// assignments: the keeper — a department with sub-departments and a person; the draft becomes visible to them
	due := time.Date(2026, 1, 10, 0, 0, 0, 0, time.Local)
	n, err := s.Assign(ctx, hr, draft, []string{"dept:" + sales, out}, &due, true)
	must(err)
	if n != 3 { // ann, bob (b2b), out; the head is not of the department's employees
		t.Fatalf("assigned to %d", n)
	}
	sees(out, open, draft)
	c, err = s.Course(ctx, draft, out, false)
	must(err)
	if !c.Assigned || c.Due == nil || !c.Due.Equal(due) || c.AssignedBy != "hr" {
		t.Fatalf("out's assignment: %+v", c)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'learn_assign' AND body = 'Черновик'`).Scan(&notes))
	if notes != 3 {
		t.Fatalf("notifications: %d", notes)
	}
	// again with another due day: no new notifications
	later := due.AddDate(0, 1, 0)
	_, err = s.Assign(ctx, hr, draft, []string{out}, &later, true)
	must(err)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'learn_assign'`).Scan(&notes))
	if c, _ = s.Course(ctx, draft, out, false); notes != 3 || !c.Due.Equal(later) {
		t.Fatalf("reassigned: %d notes, due %v", notes, c.Due)
	}

	// the head assigns only to the people of the department: out is skipped
	n, err = s.Assign(ctx, lead, sell, []string{bob, out}, nil, false)
	must(err)
	if n != 1 {
		t.Fatalf("the head assigned %d", n)
	}
	if err := s.Unassign(ctx, lead, draft, out, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the head unassigns a stranger: %v", err)
	}

	// the learners: the keeper sees all, the head — the department; statuses
	all, err := s.Learners(ctx, hr, true, LearnerFilter{})
	must(err)
	if len(all) != 4 {
		t.Fatalf("all learners: %d", len(all))
	}
	mine, err := s.Learners(ctx, lead, false, LearnerFilter{})
	must(err)
	for _, l := range mine {
		if l.ID == out || l.ID == lead {
			t.Fatalf("the head sees %s", l.Name)
		}
	}
	if len(mine) != 3 { // ann and bob in the draft, bob in sales
		t.Fatalf("the head's learners: %d", len(mine))
	}
	for _, id := range []string{l1, l2, l3} {
		must(s.MarkLesson(ctx, bob, sell, id, true))
	}
	done, err := s.Learners(ctx, hr, true, LearnerFilter{CourseID: sell, Status: "finished"})
	must(err)
	if len(done) != 1 || done[0].ID != bob || done[0].Done != 3 || done[0].Last == nil {
		t.Fatalf("finished: %+v", done)
	}
	late, err := s.Learners(ctx, hr, true, LearnerFilter{Status: "overdue", DepartmentID: sales})
	must(err)
	if len(late) != 2 { // the draft has no lessons and the due day is over: ann, bob
		t.Fatalf("overdue in sales: %+v", late)
	}

	must(s.Unassign(ctx, hr, draft, out, true))
	sees(out, open)
	must(s.DeleteLesson(ctx, sell, l3))
	if c, _ = s.Course(ctx, sell, bob, false); c.Lessons != 2 || !c.Finished() {
		t.Fatalf("after a lesson is deleted: %+v", c)
	}
	must(s.DeleteCourse(ctx, sell))
	if err := s.DeleteCourse(ctx, sell); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}

// TestLearningTests: a test of a course — its questions checked when saved, attempts (the order fixed, the limit, the
// one begun goes on), grading of one, several and typed answers, the pass score marking the test passed (the button
// does not), an attempt out of time graded without late answers; the course passed and its certificate's day.
func TestLearningTests(t *testing.T) {
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
	var ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'ann', 'ann@t') RETURNING id`).Scan(&ann))
	s := NewStore(pool)
	course, err := s.SaveCourse(ctx, ann, Course{Title: "Курс", Published: true})
	must(err)
	lesson, err := s.SaveLesson(ctx, Lesson{CourseID: course, Title: "Урок"})
	must(err)
	test, err := s.SaveLesson(ctx, Lesson{CourseID: course, Title: "Итоговый тест", Kind: "test", PassPct: 60, Attempts: 2, Minutes: 5})
	must(err)
	if _, err := s.SaveQuestion(ctx, Question{LessonID: lesson, Kind: "one", Text: "?", Options: OptionsOf("*a\nb")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a question of a lesson: %v", err)
	}
	for _, bad := range []Question{
		{Kind: "one", Text: "Q", Options: OptionsOf("*a\n*b")},
		{Kind: "one", Text: "Q", Options: OptionsOf("*a")},
		{Kind: "many", Text: "Q", Options: OptionsOf("a\nb")},
		{Kind: "text", Text: "Q", Answers: []string{" "}},
		{Kind: "nonsense", Text: "Q"},
	} {
		bad.LessonID = test
		if _, err := s.SaveQuestion(ctx, bad); err == nil {
			t.Fatalf("saved %+v", bad)
		}
	}
	q1, err := s.SaveQuestion(ctx, Question{LessonID: test, Kind: "one", Text: "Столица?", Options: OptionsOf("Париж\n*Москва\n\n"), Points: 2})
	must(err)
	q2, err := s.SaveQuestion(ctx, Question{LessonID: test, Kind: "many", Text: "Чётные?", Options: OptionsOf("*2\n3\n*4")})
	must(err)
	q3, err := s.SaveQuestion(ctx, Question{LessonID: test, Kind: "text", Text: "Цвет неба?", Answers: []string{"Голубой", "синий", ""}})
	must(err)
	must(s.MoveQuestion(ctx, test, q3, true))
	qs, err := s.Questions(ctx, test)
	must(err)
	if len(qs) != 3 || qs[1].ID != q3 || qs[0].Points != 2 || len(qs[0].Options) != 2 || qs[0].OptionLines() != "Париж\n*Москва\n" || len(qs[1].Answers) != 2 {
		t.Fatalf("questions: %+v", qs)
	}

	lessons, err := s.Lessons(ctx, course, ann)
	must(err)
	l := lessons[1]
	if !l.Test() || l.PassPct != 60 || l.Attempts != 2 || l.Minutes != 5 {
		t.Fatalf("the test: %+v", l)
	}
	must(s.MarkLesson(ctx, ann, course, test, true)) // the button does not pass a test
	if c, _ := s.Course(ctx, course, ann, false); c.Done != 0 {
		t.Fatalf("a test passed by the button: %+v", c)
	}

	// the first attempt: one wrong — 2 of 4 points, 50% < 60%
	a, err := s.StartAttempt(ctx, l, ann)
	must(err)
	again, err := s.StartAttempt(ctx, l, ann)
	must(err)
	if again.ID != a.ID || len(a.Questions) != 3 {
		t.Fatalf("the attempt begun goes on: %d, %d", again.ID, a.ID)
	}
	a, err = s.FinishAttempt(ctx, l, ann, a.ID, map[string]any{q1: []int{1}, q2: []int{0}, q3: "  зелёный "})
	must(err)
	if a.Finished == nil || a.Score != 2 || a.Max != 4 || a.Passed || !a.Results[q1] || a.Results[q2] || a.Results[q3] || a.Pct() != 50 {
		t.Fatalf("the first attempt: %+v", a)
	}
	if !a.Chosen(q2, 0) || a.Chosen(q2, 2) || a.Typed(q3) != "зелёный" {
		t.Fatalf("the answers kept: %+v", a.Answers)
	}
	if b, _ := s.FinishAttempt(ctx, l, ann, a.ID, map[string]any{q1: []int{1}, q2: []int{0, 2}, q3: "синий"}); b.Score != 2 {
		t.Fatal("a finished attempt graded again")
	}

	// the second: all right (the typed answer — case and spaces aside); the test is passed
	a, err = s.StartAttempt(ctx, l, ann)
	must(err)
	a, err = s.FinishAttempt(ctx, l, ann, a.ID, map[string]any{q1: []int{1, 7}, q2: []int{2, 0, 2}, q3: " ГОЛУБОЙ  "})
	must(err)
	if a.Score != 4 || !a.Passed {
		t.Fatalf("the second attempt: %+v", a)
	}
	if _, err := s.StartAttempt(ctx, l, ann); err == nil {
		t.Fatal("a third attempt of two")
	}
	tries, err := s.Attempts(ctx, test, ann)
	must(err)
	if len(tries) != 2 || tries[0].ID != a.ID {
		t.Fatalf("attempts: %+v", tries)
	}

	// passed: the test only; with the lesson — the course, its day for the certificate
	if done, _ := s.FinishedCourses(ctx, ann); len(done) != 0 {
		t.Fatalf("the course passed without the lesson: %+v", done)
	}
	must(s.MarkLesson(ctx, ann, course, lesson, true))
	done, err := s.FinishedCourses(ctx, ann)
	must(err)
	if len(done) != 1 || done[0].ID != course || done[0].At.IsZero() {
		t.Fatalf("passed: %+v", done)
	}

	// out of time: the answers sent late are not taken; a begun attempt over time is graded on the next start
	l.Attempts = 0
	a, err = s.StartAttempt(ctx, l, ann)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE learn_attempts SET started_at = now() - interval '7 minutes' WHERE id = $1`, a.ID)
	must(err)
	a, err = s.FinishAttempt(ctx, l, ann, a.ID, map[string]any{q1: []int{1}})
	must(err)
	if a.Score != 0 || a.Passed {
		t.Fatalf("late answers taken: %+v", a)
	}
	a, err = s.StartAttempt(ctx, l, ann)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE learn_attempts SET started_at = now() - interval '7 minutes' WHERE id = $1`, a.ID)
	must(err)
	b, err := s.StartAttempt(ctx, l, ann)
	must(err)
	if b.ID == a.ID {
		t.Fatal("an attempt out of time goes on")
	}
	if old, _ := s.Attempt(ctx, test, ann, a.ID); old.Finished == nil {
		t.Fatal("the attempt out of time is not closed")
	}
	if _, err := s.Attempt(ctx, test, "00000000-0000-0000-0000-000000000000", b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another's attempt: %v", err)
	}
}

// TestLearningReminders: the day before the due day — the employee, once; overdue — the employee and who assigned it,
// once; finished courses and fired people — nothing; a new due day reminds again.
func TestLearningReminders(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, ann, bob, gone := newUser("boss"), newUser("ann"), newUser("bob"), newUser("gone")
	s := NewStore(pool)
	course, err := s.SaveCourse(ctx, boss, Course{Title: "Охрана труда", Published: true})
	must(err)
	lesson, err := s.SaveLesson(ctx, Lesson{CourseID: course, Title: "Урок"})
	must(err)
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.Local)
	tomorrow, yesterday := time.Date(2026, 11, 11, 0, 0, 0, 0, time.Local), time.Date(2026, 11, 9, 0, 0, 0, 0, time.Local)
	_, err = s.Assign(ctx, boss, course, []string{ann, gone}, &tomorrow, true)
	must(err)
	_, err = s.Assign(ctx, boss, course, []string{bob}, &yesterday, true)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, gone)
	must(err)
	count := func(user, kind string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, user, kind).Scan(&n))
		return n
	}
	n, err := s.RemindLearning(ctx, now)
	must(err)
	if n != 3 || count(ann, "learn_due") != 1 || count(bob, "learn_overdue") != 1 || count(boss, "learn_overdue") != 1 || count(gone, "learn_due") != 0 {
		t.Fatalf("sent %d", n)
	}
	var body string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'learn_overdue'`, boss).Scan(&body))
	if body != "bob · Охрана труда" {
		t.Fatalf("to who assigned: %q", body)
	}
	if n, _ = s.RemindLearning(ctx, now); n != 0 {
		t.Fatalf("sent again: %d", n)
	}
	// ann finishes before the due day: no overdue for her; bob gets a new due day — reminded again
	must(s.MarkLesson(ctx, ann, course, lesson, true))
	later := time.Date(2026, 11, 13, 0, 0, 0, 0, time.Local)
	_, err = s.Assign(ctx, boss, course, []string{bob}, &later, true)
	must(err)
	n, err = s.RemindLearning(ctx, time.Date(2026, 11, 12, 9, 0, 0, 0, time.Local))
	must(err)
	if n != 1 || count(bob, "learn_due") != 1 || count(ann, "learn_overdue") != 0 {
		t.Fatalf("after: %d", n)
	}
}
