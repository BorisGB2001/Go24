package company

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Обучение»: courses of lessons (Markdown, in chapters) kept by administrators and HR officers with the
// right «learning». A published course is seen by everybody or by the people and departments (with sub-departments)
// chosen; a course is assigned to employees — by its keepers to anyone, by heads and deputies to the people of their
// departments — with a due day, the employee is notified. Each employee marks the lessons passed: the progress.

// Course is a course of lessons.
type Course struct {
	ID          string
	Title       string
	Description string
	Color       string
	Published   bool
	AuthorID    string
	AuthorName  string
	Created     time.Time
	Changed     time.Time
	Lessons     int // how many
	// of the viewer
	Done       int        // lessons passed
	Assigned   bool       // the course is assigned to the viewer
	Due        *time.Time // the due day of the assignment
	AssignedBy string     // who assigned it (a name)
}

// Percent of the lessons passed.
func (c Course) Percent() int { return percent(c.Done, c.Lessons) }

// Finished: every lesson is passed.
func (c Course) Finished() bool { return c.Lessons > 0 && c.Done >= c.Lessons }

// Overdue: assigned, not finished and the due day is over.
func (c Course) Overdue(today time.Time) bool { return overdue(c.Due, c.Finished(), today) }

func percent(done, total int) int {
	if total == 0 {
		return 0
	}
	return min(100, done*100/total)
}

func overdue(due *time.Time, finished bool, today time.Time) bool {
	return due != nil && !finished && due.Before(today)
}

// Lesson is an item of a course's plan: a lesson or a test (Kind "test": Body is its introduction, passed by an attempt
// scoring at least PassPct percent).
type Lesson struct {
	ID       string
	CourseID string
	Chapter  string
	Title    string
	Body     string
	Sort     int
	Kind     string // "lesson" | "test"
	PassPct  int
	Attempts int  // how many a person has, 0 — any number
	Minutes  int  // the time of an attempt, 0 — no limit
	Shuffle  bool // the questions in another order each attempt
	Done     bool // passed by the viewer
}

// Test: the item is a test.
func (l Lesson) Test() bool { return l.Kind == "test" }

// Learner is an employee a course is assigned to, with the progress.
type Learner struct {
	Person
	CourseID    string
	CourseTitle string
	Due         *time.Time
	ByName      string
	Assigned    time.Time
	Done, Total int
	Last        *time.Time // the last lesson passed
	Best        *int       // the best result of the course's tests, in percent (nil — no attempt)
}

func (l Learner) Percent() int                 { return percent(l.Done, l.Total) }
func (l Learner) Finished() bool               { return l.Total > 0 && l.Done >= l.Total }
func (l Learner) Overdue(today time.Time) bool { return overdue(l.Due, l.Finished(), today) }

// CourseColors are the covers a course may have.
var CourseColors = []string{"blue", "green", "orange", "purple", "red", "teal", "gray"}

// Limits of a course and a lesson, in characters.
const (
	CourseTitleMax = 200
	CourseDescMax  = 5000
	LessonBodyMax  = 200000
)

// CanTeach: administrators and HR officers an administrator let keep the courses.
func (s *Store) CanTeach(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	h, err := s.HROf(ctx, userID)
	return h.Can("learning"), err
}

// CanAssign: who may assign courses — the keepers (to anyone) and heads and deputies of departments (to the people of
// their departments with sub-departments).
func (s *Store) CanAssign(ctx context.Context, userID string, admin bool) (bool, error) {
	if ok, err := s.CanTeach(ctx, userID, admin); ok || err != nil {
		return ok, err
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM departments WHERE head_id::text = $1)
		OR EXISTS (SELECT 1 FROM department_deputies WHERE user_id::text = $1)`, userID).Scan(&ok)
	return ok, err
}

// underSQL: the active employees (not guests) of the departments the user ($N) heads or is a deputy of, with
// sub-departments, the user excepted.
func underSQL(arg string) string {
	return `(WITH RECURSIVE sub AS (
			SELECT id FROM departments WHERE head_id::text = ` + arg + `
			UNION SELECT department_id FROM department_deputies WHERE user_id::text = ` + arg + `
			UNION SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id)
		SELECT ud.user_id FROM user_departments ud JOIN users u ON u.id = ud.user_id
		WHERE ud.department_id IN (SELECT id FROM sub) AND u.active AND NOT u.guest AND u.id::text <> ` + arg + `)`
}

// seesSQL: the course c is seen by the user ($N): published and for everybody or for the user or a department of
// theirs — where they work, head it or are a deputy (or a department above it); or assigned to the user.
func seesSQL(arg string) string {
	return `((c.published AND (NOT EXISTS (SELECT 1 FROM learn_access a WHERE a.course_id = c.id)
			OR EXISTS (SELECT 1 FROM learn_access a WHERE a.course_id = c.id AND (a.user_id::text = ` + arg + ` OR a.department_id IN (
				WITH RECURSIVE up AS (SELECT d.id, d.parent_id FROM departments d WHERE d.id IN (
						SELECT department_id FROM user_departments WHERE user_id::text = ` + arg + `
						UNION SELECT id FROM departments WHERE head_id::text = ` + arg + `
						UNION SELECT department_id FROM department_deputies WHERE user_id::text = ` + arg + `)
					UNION SELECT p.id, p.parent_id FROM departments p JOIN up ON p.id = up.parent_id) SELECT id FROM up)))))
		OR EXISTS (SELECT 1 FROM learn_assignments x WHERE x.course_id = c.id AND x.user_id::text = ` + arg + `))`
}

const courseSQL = `SELECT c.id::text, c.title, c.description, c.color, c.published, coalesce(c.created_by::text, ''),
		coalesce(au.name, ''), c.created_at, c.changed_at,
		(SELECT count(*) FROM learn_lessons l WHERE l.course_id = c.id),
		(SELECT count(*) FROM learn_progress p JOIN learn_lessons l ON l.id = p.lesson_id WHERE l.course_id = c.id AND p.user_id::text = $1),
		x.user_id IS NOT NULL, x.due_on, coalesce(bu.name, '')
	FROM learn_courses c LEFT JOIN users au ON au.id = c.created_by
	LEFT JOIN learn_assignments x ON x.course_id = c.id AND x.user_id::text = $1 LEFT JOIN users bu ON bu.id = x.assigned_by`

func scanCourses(rows pgx.Rows, err error) ([]Course, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Course, error) {
		var c Course
		err := row.Scan(&c.ID, &c.Title, &c.Description, &c.Color, &c.Published, &c.AuthorID, &c.AuthorName, &c.Created,
			&c.Changed, &c.Lessons, &c.Done, &c.Assigned, &c.Due, &c.AssignedBy)
		if c.Due != nil {
			d := localDay(*c.Due)
			c.Due = &d
		}
		return c, err
	})
}

// CourseFilter narrows the courses.
type CourseFilter struct {
	Mine  bool   // assigned to the viewer or begun
	Query string // words of the title or description
}

// Courses the viewer sees (keep — all, drafts too), with the viewer's progress: the assigned ones first, by the due
// day, then by title.
func (s *Store) Courses(ctx context.Context, viewer string, keep bool, f CourseFilter) ([]Course, error) {
	args := []any{viewer}
	where := []string{"true"}
	if !keep {
		where = append(where, seesSQL("$1"))
	}
	if f.Mine {
		where = append(where, `(x.user_id IS NOT NULL OR EXISTS (SELECT 1 FROM learn_progress p JOIN learn_lessons l ON l.id = p.lesson_id
			WHERE l.course_id = c.id AND p.user_id::text = $1))`)
	}
	for _, w := range strings.Fields(f.Query) {
		args = append(args, "%"+likeEscape(w)+"%")
		n := "$" + strconv.Itoa(len(args))
		where = append(where, "(c.title ILIKE "+n+" OR c.description ILIKE "+n+")")
	}
	return scanCourses(s.db.Query(ctx, courseSQL+" WHERE "+strings.Join(where, " AND ")+
		" ORDER BY x.user_id IS NULL, x.due_on NULLS LAST, lower(c.title) LIMIT 500", args...))
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Course returns a course the viewer sees (keep — any).
func (s *Store) Course(ctx context.Context, id, viewer string, keep bool) (Course, error) {
	where := " WHERE c.id::text = $2"
	if !keep {
		where += " AND " + seesSQL("$1")
	}
	list, err := scanCourses(s.db.Query(ctx, courseSQL+where, viewer, id))
	if err != nil {
		return Course{}, err
	}
	if len(list) == 0 {
		return Course{}, ErrNotFound
	}
	return list[0], nil
}

// SaveCourse creates (c.ID "") or changes a course; returns its id. The caller checks CanTeach.
func (s *Store) SaveCourse(ctx context.Context, actor string, c Course) (string, error) {
	c.Title, c.Description = strings.TrimSpace(c.Title), strings.TrimSpace(c.Description)
	if c.Title == "" || utf8.RuneCountInString(c.Title) > CourseTitleMax || utf8.RuneCountInString(c.Description) > CourseDescMax {
		return "", i18n.E("err.bad_form")
	}
	if !slices.Contains(CourseColors, c.Color) {
		c.Color = CourseColors[0]
	}
	if c.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO learn_courses (title, description, color, published, created_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, c.Title, c.Description, c.Color, c.Published, actor).Scan(&c.ID)
		return c.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE learn_courses SET title = $2, description = $3, color = $4, published = $5, changed_at = now()
		WHERE id::text = $1`, c.ID, c.Title, c.Description, c.Color, c.Published)
	if err == nil && tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return c.ID, err
}

// DeleteCourse removes a course with its lessons, assignments and progress.
func (s *Store) DeleteCourse(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM learn_courses WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// CourseAccess is «Кто видит» of the course: people (ID) and departments («dept:ID»); none — everybody.
func (s *Store) CourseAccess(ctx context.Context, id string) ([]DeptRef, error) {
	rows, err := s.db.Query(ctx, `SELECT coalesce(a.user_id::text, 'dept:' || a.department_id::text), coalesce(u.name, d.name, '')
		FROM learn_access a LEFT JOIN users u ON u.id = a.user_id LEFT JOIN departments d ON d.id = a.department_id
		WHERE a.course_id::text = $1 ORDER BY a.department_id IS NULL, coalesce(d.name, u.name)`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DeptRef])
}

// splitTo parts a picker's choice into people and departments («dept:ID»).
func splitTo(to []string) (users, depts []string) {
	for _, v := range to {
		if d, ok := strings.CutPrefix(v, "dept:"); ok {
			depts = append(depts, d)
		} else if v != "" {
			users = append(users, v)
		}
	}
	return users, depts
}

// SetCourseAccess chooses who sees the course: people and departments («dept:ID»); none — everybody.
func (s *Store) SetCourseAccess(ctx context.Context, id string, to []string) error {
	users, depts := splitTo(to)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var course string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM learn_courses WHERE id::text = $1 FOR UPDATE`, id).Scan(&course); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM learn_access WHERE course_id = $1`, course); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learn_access (course_id, user_id)
		SELECT $1::uuid, u.id FROM users u WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, course, users); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO learn_access (course_id, department_id)
		SELECT $1::uuid, d.id FROM departments d WHERE d.id::text = ANY($2) ON CONFLICT DO NOTHING`, course, depts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Lessons of the course in order, Done — passed by the user.
func (s *Store) Lessons(ctx context.Context, courseID, userID string) ([]Lesson, error) {
	rows, err := s.db.Query(ctx, `SELECT l.id::text, l.course_id::text, l.chapter, l.title, l.body, l.sort, l.kind,
			l.pass_pct, l.attempts, l.minutes, l.shuffle, EXISTS (SELECT 1 FROM learn_progress p WHERE p.lesson_id = l.id AND p.user_id::text = $2)
		FROM learn_lessons l WHERE l.course_id::text = $1 ORDER BY l.sort, l.title`, courseID, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Lesson])
}

// SaveLesson adds (l.ID "", at the end of the course) or changes a lesson or a test (its kind stays); returns its id.
// The caller checks CanTeach.
func (s *Store) SaveLesson(ctx context.Context, l Lesson) (string, error) {
	l.Title, l.Chapter = strings.TrimSpace(l.Title), strings.TrimSpace(l.Chapter)
	if l.Title == "" || utf8.RuneCountInString(l.Title) > CourseTitleMax || utf8.RuneCountInString(l.Chapter) > CourseTitleMax ||
		len(l.Body) > LessonBodyMax {
		return "", i18n.E("err.bad_form")
	}
	if l.Kind != "test" {
		l.Kind = "lesson"
	}
	if l.PassPct < 1 || l.PassPct > 100 {
		l.PassPct = 70
	}
	l.Attempts, l.Minutes = max(0, min(l.Attempts, 100)), max(0, min(l.Minutes, 600))
	if l.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO learn_lessons (course_id, chapter, title, body, kind, pass_pct, attempts, minutes, shuffle, sort)
			SELECT c.id, $2, $3, $4, $5, $6, $7, $8, $9, coalesce((SELECT max(sort) + 1 FROM learn_lessons WHERE course_id = c.id), 0)
			FROM learn_courses c WHERE c.id::text = $1 RETURNING id::text`, l.CourseID, l.Chapter, l.Title, l.Body, l.Kind,
			l.PassPct, l.Attempts, l.Minutes, l.Shuffle).Scan(&l.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		if err == nil {
			_, err = s.db.Exec(ctx, `UPDATE learn_courses SET changed_at = now() WHERE id::text = $1`, l.CourseID)
		}
		return l.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE learn_lessons SET chapter = $3, title = $4, body = $5, pass_pct = $6, attempts = $7, minutes = $8,
			shuffle = $9 WHERE id::text = $1 AND course_id::text = $2`,
		l.ID, l.CourseID, l.Chapter, l.Title, l.Body, l.PassPct, l.Attempts, l.Minutes, l.Shuffle)
	if err == nil && tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	if err == nil {
		_, err = s.db.Exec(ctx, `UPDATE learn_courses SET changed_at = now() WHERE id::text = $1`, l.CourseID)
	}
	return l.ID, err
}

// DeleteLesson removes a lesson of the course.
func (s *Store) DeleteLesson(ctx context.Context, courseID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM learn_lessons WHERE id::text = $1 AND course_id::text = $2`, id, courseID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MoveLesson moves a lesson of the course one place up (up) or down.
func (s *Store) MoveLesson(ctx context.Context, courseID, id string, up bool) error {
	list, err := s.Lessons(ctx, courseID, "")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(l Lesson) bool { return l.ID == id })
	if i < 0 {
		return ErrNotFound
	}
	j := i + 1
	if up {
		j = i - 1
	}
	if j < 0 || j >= len(list) {
		return nil
	}
	list[i], list[j] = list[j], list[i]
	ids := make([]string, len(list))
	for k, l := range list {
		ids[k] = l.ID
	}
	_, err = s.db.Exec(ctx, `UPDATE learn_lessons l SET sort = o.n - 1 FROM unnest($1::text[]) WITH ORDINALITY o(id, n)
		WHERE l.id::text = o.id`, ids)
	return err
}

// MarkLesson marks the lesson passed by the user (done) or not; a test is passed only by an attempt. The caller checks
// the user sees the course.
func (s *Store) MarkLesson(ctx context.Context, userID, courseID, lessonID string, done bool) error {
	var err error
	if done {
		_, err = s.db.Exec(ctx, `INSERT INTO learn_progress (lesson_id, user_id)
			SELECT l.id, $2 FROM learn_lessons l WHERE l.id::text = $1 AND l.course_id::text = $3 AND l.kind = 'lesson'
			ON CONFLICT DO NOTHING`, lessonID, userID, courseID)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM learn_progress p USING learn_lessons l
			WHERE p.lesson_id = l.id AND l.kind = 'lesson' AND l.id::text = $1 AND p.user_id = $2`, lessonID, userID)
	}
	return err
}

// Assign assigns the course to people and departments («dept:ID», their active employees now, with sub-departments)
// with a due day (nil — none): a keeper — to anyone, otherwise only to the people of the actor's departments (the
// others are skipped). Those already assigned get the new due day. The newly assigned are notified (not the actor).
// Returns how many people the course is assigned to now.
func (s *Store) Assign(ctx context.Context, actor, courseID string, to []string, due *time.Time, keep bool) (int, error) {
	users, depts := splitTo(to)
	var dueOn any
	if due != nil {
		dueOn = due.Format(time.DateOnly)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var title string
	if err := tx.QueryRow(ctx, `SELECT title FROM learn_courses WHERE id::text = $1`, courseID).Scan(&title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	limit := ""
	if !keep {
		limit = " AND u.id IN " + underSQL("$5")
	}
	rows, err := tx.Query(ctx, `WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = ANY($3)
				UNION SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id),
			who AS (SELECT u.id FROM users u WHERE u.active AND NOT u.guest AND u.oidc_sub <> 'legacy:0'
				AND (u.id::text = ANY($2) OR u.id IN (SELECT ud.user_id FROM user_departments ud WHERE ud.department_id IN (SELECT id FROM sub)))`+limit+`)
		INSERT INTO learn_assignments (course_id, user_id, assigned_by, due_on)
		SELECT $1::uuid, who.id, $5::text::uuid, $4::date FROM who
		ON CONFLICT (course_id, user_id) DO UPDATE SET due_on = EXCLUDED.due_on, due_reminded = NULL, overdue_sent = NULL
		RETURNING user_id::text, xmax = 0`, courseID, users, depts, dueOn, actor)
	if err != nil {
		return 0, err
	}
	var fresh []string
	n := 0
	for rows.Next() {
		var id string
		var isNew bool
		if err := rows.Scan(&id, &isNew); err != nil {
			return 0, err
		}
		n++
		if isNew && id != actor {
			fresh = append(fresh, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
		SELECT u::uuid, $2, 'learn_assign', $3, $4 FROM unnest($1::text[]) u`, fresh, actor, title, "/company/learning/"+courseID); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// Unassign takes the course from the person (the progress stays): a keeper — anyone's, otherwise only a person of
// the actor's departments.
func (s *Store) Unassign(ctx context.Context, actor, courseID, userID string, keep bool) error {
	q := `DELETE FROM learn_assignments WHERE course_id::text = $1 AND user_id::text = $2`
	args := []any{courseID, userID}
	if !keep {
		q += " AND user_id IN " + underSQL("$3")
		args = append(args, actor)
	}
	tag, err := s.db.Exec(ctx, q, args...)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// LearnerFilter narrows the learners.
type LearnerFilter struct {
	CourseID     string
	DepartmentID string // with sub-departments
	Status       string // "" | "active" | "finished" | "overdue"
}

// Learners are the assignments the viewer may look at — a keeper: all, otherwise those of the people of the viewer's
// departments — with the progress, the overdue first, then by the due day and name.
func (s *Store) Learners(ctx context.Context, viewer string, keep bool, f LearnerFilter) ([]Learner, error) {
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{"u.active"}
	if !keep {
		where = append(where, "x.user_id IN "+underSQL(arg(viewer)))
	}
	if f.CourseID != "" {
		where = append(where, "x.course_id::text = "+arg(f.CourseID))
	}
	if f.DepartmentID != "" {
		where = append(where, `x.user_id IN (SELECT ud.user_id FROM user_departments ud WHERE ud.department_id IN (
			WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = `+arg(f.DepartmentID)+`
				UNION ALL SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id) SELECT id FROM sub))`)
	}
	rows, err := s.db.Query(ctx, `SELECT * FROM (SELECT u.id::text, u.name, u.position, u.photo_key, c.id::text, c.title, x.due_on,
			coalesce(b.name, ''), x.created_at,
			(SELECT count(*) FROM learn_progress p JOIN learn_lessons l ON l.id = p.lesson_id WHERE l.course_id = c.id AND p.user_id = u.id) done,
			(SELECT count(*) FROM learn_lessons l WHERE l.course_id = c.id) total,
			(SELECT max(p.done_at) FROM learn_progress p JOIN learn_lessons l ON l.id = p.lesson_id WHERE l.course_id = c.id AND p.user_id = u.id),
			(SELECT max(a.score * 100 / a.max_score) FROM learn_attempts a JOIN learn_lessons l ON l.id = a.lesson_id
				WHERE l.course_id = c.id AND a.user_id = u.id AND a.finished_at IS NOT NULL AND a.max_score > 0)
		FROM learn_assignments x JOIN users u ON u.id = x.user_id JOIN learn_courses c ON c.id = x.course_id
		LEFT JOIN users b ON b.id = x.assigned_by WHERE `+strings.Join(where, " AND ")+`) r
		ORDER BY (r.due_on < current_date AND (r.total = 0 OR r.done < r.total)) DESC, r.due_on NULLS LAST, r.name, r.title LIMIT 2000`, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Learner, error) {
		var l Learner
		err := row.Scan(&l.ID, &l.Name, &l.Position, &l.PhotoKey, &l.CourseID, &l.CourseTitle, &l.Due, &l.ByName, &l.Assigned,
			&l.Done, &l.Total, &l.Last, &l.Best)
		if l.Due != nil {
			d := localDay(*l.Due)
			l.Due = &d
		}
		return l, err
	})
	if err != nil || f.Status == "" {
		return list, err
	}
	today := localDay(time.Now())
	return slices.DeleteFunc(list, func(l Learner) bool {
		switch f.Status {
		case "finished":
			return !l.Finished()
		case "overdue":
			return !l.Overdue(today)
		}
		return l.Finished() // active
	}), nil
}

// CourseDone is a course the person has passed: every lesson and test, the day of the last one.
type CourseDone struct {
	ID, Title, Color string
	At               time.Time
}

// FinishedCourses are the courses the person has passed (of those with lessons), the latest first.
func (s *Store) FinishedCourses(ctx context.Context, userID string) ([]CourseDone, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.title, c.color, max(p.done_at)
		FROM learn_courses c JOIN learn_lessons l ON l.course_id = c.id
		LEFT JOIN learn_progress p ON p.lesson_id = l.id AND p.user_id::text = $1
		GROUP BY c.id HAVING count(*) = count(p.lesson_id) ORDER BY 4 DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CourseDone])
}

// unfinishedSQL: the assignment x is not finished — the course has no lessons or some are not passed.
const unfinishedSQL = `(SELECT count(*) FROM learn_lessons l WHERE l.course_id = x.course_id) = 0 OR EXISTS (
	SELECT 1 FROM learn_lessons l WHERE l.course_id = x.course_id AND NOT EXISTS (
		SELECT 1 FROM learn_progress p WHERE p.lesson_id = l.id AND p.user_id = x.user_id))`

// RemindLearning sends the reminders of the assignments not finished, each once: the due day is tomorrow — to the
// employee; the due day is over — to the employee and to who assigned the course. Returns how many were sent.
func (s *Store) RemindLearning(ctx context.Context, now time.Time) (int, error) {
	today := localDay(now).Format(time.DateOnly)
	n := 0
	tag, err := s.db.Exec(ctx, `WITH due AS (UPDATE learn_assignments x SET due_reminded = now()
			WHERE x.due_reminded IS NULL AND x.due_on = $1::date + 1 AND (`+unfinishedSQL+`)
			AND x.user_id IN (SELECT id FROM users WHERE active) RETURNING x.course_id, x.user_id, x.due_on)
		INSERT INTO notifications (user_id, kind, body, url)
		SELECT due.user_id, 'learn_due', c.title, '/company/learning/' || c.id FROM due JOIN learn_courses c ON c.id = due.course_id`, today)
	if err != nil {
		return n, err
	}
	n += int(tag.RowsAffected())
	tag, err = s.db.Exec(ctx, `WITH late AS (UPDATE learn_assignments x SET overdue_sent = now()
			WHERE x.overdue_sent IS NULL AND x.due_on < $1::date AND (`+unfinishedSQL+`)
			AND x.user_id IN (SELECT id FROM users WHERE active) RETURNING x.course_id, x.user_id, x.assigned_by)
		INSERT INTO notifications (user_id, kind, body, url)
		SELECT late.user_id, 'learn_overdue', c.title, '/company/learning/' || c.id FROM late JOIN learn_courses c ON c.id = late.course_id
		UNION ALL
		SELECT late.assigned_by, 'learn_overdue', u.name || ' · ' || c.title, '/company/learning/' || c.id
		FROM late JOIN learn_courses c ON c.id = late.course_id JOIN users u ON u.id = late.user_id
		JOIN users b ON b.id = late.assigned_by AND b.active
		WHERE late.assigned_by <> late.user_id`, today)
	if err != nil {
		return n, err
	}
	return n + int(tag.RowsAffected()), nil
}
