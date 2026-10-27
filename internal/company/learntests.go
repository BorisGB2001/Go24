package company

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Tests of «Обучение»: questions of a test and the attempts to pass it. An attempt fixes the questions' order when it
// begins; it is graded when sent — a question scores its points when answered exactly right (one answer: the right
// option; several: exactly the right options; typed: one of the accepted answers, case and spaces aside). An attempt
// scoring at least the pass percentage passes the test: it counts as passed in the course's progress.

// QuestionKinds are the kinds of questions.
var QuestionKinds = []string{"one", "many", "text"}

// Option is an answer offered by a question.
type Option struct {
	Text  string `json:"text"`
	Right bool   `json:"right"`
}

// Question is a question of a test.
type Question struct {
	ID       string
	LessonID string
	Kind     string
	Text     string
	Options  []Option
	Answers  []string // accepted typed answers
	Points   int
	Sort     int
}

// OptionLines is the options as the form edits them: one per line, the right ones marked with «*» at the start.
func (q Question) OptionLines() string {
	var b strings.Builder
	for _, o := range q.Options {
		if o.Right {
			b.WriteString("*")
		}
		b.WriteString(o.Text + "\n")
	}
	return b.String()
}

// OptionsOf reads the options' lines (see OptionLines).
func OptionsOf(lines string) []Option {
	var out []Option
	for _, l := range strings.Split(lines, "\n") {
		l = strings.TrimSpace(l)
		right := strings.HasPrefix(l, "*")
		if l = strings.TrimSpace(strings.TrimPrefix(l, "*")); l != "" {
			out = append(out, Option{Text: l, Right: right})
		}
	}
	return out
}

// Attempt is a person's attempt to pass a test.
type Attempt struct {
	ID        int64
	LessonID  string
	UserID    string
	Questions []string
	Answers   map[string]any // question id → []float64 (option numbers) | string
	Results   map[string]bool
	Score     int
	Max       int
	Passed    bool
	Started   time.Time
	Finished  *time.Time
}

// Pct is the score in percent.
func (a Attempt) Pct() int { return percent(a.Score, a.Max) }

// Deadline of an attempt limited in time (zero — no limit).
func (a Attempt) Deadline(minutes int) time.Time {
	if minutes == 0 {
		return time.Time{}
	}
	return a.Started.Add(time.Duration(minutes) * time.Minute)
}

// Chosen tells the option number n is among the attempt's answers to the question.
func (a Attempt) Chosen(qid string, n int) bool {
	list, _ := a.Answers[qid].([]any)
	return slices.ContainsFunc(list, func(v any) bool { f, ok := v.(float64); return ok && int(f) == n })
}

// Typed is the attempt's typed answer to the question.
func (a Attempt) Typed(qid string) string { s, _ := a.Answers[qid].(string); return s }

// lateGrace: an answer sent this long after an attempt's time is over is still taken (the page sends it itself).
const lateGrace = time.Minute

const questionSQL = `SELECT id::text, lesson_id::text, kind, text, options, answers, points, sort FROM learn_questions`

func scanQuestions(rows pgx.Rows, err error) ([]Question, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Question, error) {
		var q Question
		var opts []byte
		if err := row.Scan(&q.ID, &q.LessonID, &q.Kind, &q.Text, &opts, &q.Answers, &q.Points, &q.Sort); err != nil {
			return q, err
		}
		return q, json.Unmarshal(opts, &q.Options)
	})
}

// Questions of the test in order.
func (s *Store) Questions(ctx context.Context, lessonID string) ([]Question, error) {
	return scanQuestions(s.db.Query(ctx, questionSQL+` WHERE lesson_id::text = $1 ORDER BY sort, id`, lessonID))
}

// SaveQuestion adds (q.ID "", at the end) or changes a question of a test; returns its id. One answer needs exactly
// one right option of two or more, several — at least one right option, typed — an accepted answer. The caller checks
// CanTeach.
func (s *Store) SaveQuestion(ctx context.Context, q Question) (string, error) {
	q.Text = strings.TrimSpace(q.Text)
	if q.Text == "" || utf8.RuneCountInString(q.Text) > 5000 || !slices.Contains(QuestionKinds, q.Kind) {
		return "", i18n.E("err.bad_form")
	}
	q.Points = max(1, min(q.Points, 100))
	rights := 0
	for _, o := range q.Options {
		if o.Right {
			rights++
		}
	}
	var answers []string
	for _, a := range q.Answers {
		if a = strings.TrimSpace(a); a != "" {
			answers = append(answers, a)
		}
	}
	switch q.Kind {
	case "one":
		if len(q.Options) < 2 || rights != 1 {
			return "", i18n.E("learn.err_one")
		}
	case "many":
		if len(q.Options) < 2 || rights == 0 {
			return "", i18n.E("learn.err_many")
		}
	case "text":
		if len(answers) == 0 {
			return "", i18n.E("learn.err_text")
		}
		q.Options = nil
	}
	if q.Options == nil {
		q.Options = []Option{}
	}
	if answers == nil || q.Kind != "text" {
		answers = []string{}
	}
	opts, _ := json.Marshal(q.Options)
	if q.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO learn_questions (lesson_id, kind, text, options, answers, points, sort)
			SELECT l.id, $2, $3, $4, $5, $6, coalesce((SELECT max(sort) + 1 FROM learn_questions WHERE lesson_id = l.id), 0)
			FROM learn_lessons l WHERE l.id::text = $1 AND l.kind = 'test' RETURNING id::text`,
			q.LessonID, q.Kind, q.Text, opts, answers, q.Points).Scan(&q.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return q.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE learn_questions SET kind = $3, text = $4, options = $5, answers = $6, points = $7
		WHERE id::text = $1 AND lesson_id::text = $2`, q.ID, q.LessonID, q.Kind, q.Text, opts, answers, q.Points)
	if err == nil && tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return q.ID, err
}

// DeleteQuestion removes a question of the test.
func (s *Store) DeleteQuestion(ctx context.Context, lessonID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM learn_questions WHERE id::text = $1 AND lesson_id::text = $2`, id, lessonID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MoveQuestion moves a question of the test one place up (up) or down.
func (s *Store) MoveQuestion(ctx context.Context, lessonID, id string, up bool) error {
	list, err := s.Questions(ctx, lessonID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(q Question) bool { return q.ID == id })
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
	for k, q := range list {
		ids[k] = q.ID
	}
	_, err = s.db.Exec(ctx, `UPDATE learn_questions q SET sort = o.n - 1 FROM unnest($1::text[]) WITH ORDINALITY o(id, n)
		WHERE q.id::text = o.id`, ids)
	return err
}

const attemptSQL = `SELECT id, lesson_id::text, user_id::text, questions, answers, results, score, max_score, passed, started_at,
	finished_at FROM learn_attempts`

func scanAttempts(rows pgx.Rows, err error) ([]Attempt, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Attempt, error) {
		var a Attempt
		var answers, results []byte
		if err := row.Scan(&a.ID, &a.LessonID, &a.UserID, &a.Questions, &answers, &results, &a.Score, &a.Max, &a.Passed,
			&a.Started, &a.Finished); err != nil {
			return a, err
		}
		if err := json.Unmarshal(answers, &a.Answers); err != nil {
			return a, err
		}
		return a, json.Unmarshal(results, &a.Results)
	})
}

// Attempts of the person at the test, the latest first.
func (s *Store) Attempts(ctx context.Context, lessonID, userID string) ([]Attempt, error) {
	return scanAttempts(s.db.Query(ctx, attemptSQL+` WHERE lesson_id::text = $1 AND user_id::text = $2 ORDER BY started_at DESC, id DESC`,
		lessonID, userID))
}

// Attempt returns the person's attempt at the test.
func (s *Store) Attempt(ctx context.Context, lessonID, userID string, id int64) (Attempt, error) {
	list, err := scanAttempts(s.db.Query(ctx, attemptSQL+` WHERE id = $1 AND lesson_id::text = $2 AND user_id::text = $3`, id, lessonID, userID))
	if err != nil {
		return Attempt{}, err
	}
	if len(list) == 0 {
		return Attempt{}, ErrNotFound
	}
	return list[0], nil
}

// StartAttempt begins an attempt at the test l (or gives the one begun and still in time); an attempt whose time is
// over is graded as it is. No attempts left or no questions — an error.
func (s *Store) StartAttempt(ctx context.Context, l Lesson, userID string) (Attempt, error) {
	if !l.Test() {
		return Attempt{}, ErrNotFound
	}
	list, err := s.Attempts(ctx, l.ID, userID)
	if err != nil {
		return Attempt{}, err
	}
	used := 0
	for _, a := range list {
		if a.Finished == nil {
			if d := a.Deadline(l.Minutes); d.IsZero() || time.Now().Before(d) {
				return a, nil
			}
			if _, err := s.grade(ctx, l, a, a.Answers); err != nil {
				return Attempt{}, err
			}
		}
		used++
	}
	if l.Attempts > 0 && used >= l.Attempts {
		return Attempt{}, i18n.E("learn.err_no_attempts")
	}
	qs, err := s.Questions(ctx, l.ID)
	if err != nil {
		return Attempt{}, err
	}
	if len(qs) == 0 {
		return Attempt{}, i18n.E("learn.err_no_questions")
	}
	ids := make([]string, len(qs))
	for i, q := range qs {
		ids[i] = q.ID
	}
	if l.Shuffle {
		rand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	}
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO learn_attempts (lesson_id, user_id, questions) VALUES ($1, $2, $3) RETURNING id`,
		l.ID, userID, ids).Scan(&id); err != nil {
		return Attempt{}, err
	}
	return s.Attempt(ctx, l.ID, userID, id)
}

// FinishAttempt grades the person's attempt at the test l with the answers (question id → option numbers or the typed
// text); sent after the time is over (and the grace) — without them. A passing attempt marks the test passed.
func (s *Store) FinishAttempt(ctx context.Context, l Lesson, userID string, id int64, answers map[string]any) (Attempt, error) {
	a, err := s.Attempt(ctx, l.ID, userID, id)
	if err != nil {
		return a, err
	}
	if a.Finished != nil {
		return a, nil
	}
	if d := a.Deadline(l.Minutes); !d.IsZero() && time.Now().After(d.Add(lateGrace)) {
		answers = a.Answers
	}
	return s.grade(ctx, l, a, answers)
}

// grade scores the attempt by the questions it was given (a question deleted since — not counted) and closes it.
func (s *Store) grade(ctx context.Context, l Lesson, a Attempt, answers map[string]any) (Attempt, error) {
	qs, err := s.Questions(ctx, l.ID)
	if err != nil {
		return a, err
	}
	kept := map[string]any{}
	a.Results, a.Score, a.Max = map[string]bool{}, 0, 0
	for _, qid := range a.Questions {
		i := slices.IndexFunc(qs, func(q Question) bool { return q.ID == qid })
		if i < 0 {
			continue
		}
		q := qs[i]
		a.Max += q.Points
		ok := false
		switch q.Kind {
		case "text":
			typed, _ := answers[qid].(string)
			typed = strings.TrimSpace(typed)
			if typed != "" {
				kept[qid] = typed
			}
			norm := func(s string) string {
				return strings.Join(strings.Fields(strings.ReplaceAll(strings.ToLower(s), "ё", "е")), " ")
			}
			ok = typed != "" && slices.ContainsFunc(q.Answers, func(x string) bool { return norm(x) == norm(typed) })
		default:
			chosen := numbers(answers[qid], len(q.Options))
			if len(chosen) > 0 {
				kept[qid] = chosen
			}
			ok = len(chosen) > 0
			for n, o := range q.Options {
				if o.Right != slices.Contains(chosen, n) {
					ok = false
				}
			}
		}
		a.Results[qid] = ok
		if ok {
			a.Score += q.Points
		}
	}
	a.Passed = a.Max > 0 && a.Score*100 >= l.PassPct*a.Max
	ans, _ := json.Marshal(kept)
	res, _ := json.Marshal(a.Results)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return a, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `UPDATE learn_attempts SET answers = $2, results = $3, score = $4, max_score = $5, passed = $6,
		finished_at = now() WHERE id = $1 AND finished_at IS NULL RETURNING finished_at`, a.ID, ans, res, a.Score, a.Max, a.Passed).Scan(&a.Finished); err != nil {
		if errors.Is(err, pgx.ErrNoRows) { // graded at the same moment by another request
			return s.Attempt(ctx, l.ID, a.UserID, a.ID)
		}
		return a, err
	}
	if a.Passed {
		if _, err := tx.Exec(ctx, `INSERT INTO learn_progress (lesson_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			l.ID, a.UserID); err != nil {
			return a, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return a, err
	}
	a.Answers = map[string]any{}
	_ = json.Unmarshal(ans, &a.Answers)
	return a, nil
}

// numbers are the option numbers of an answer (each once, below n), sorted.
func numbers(v any, n int) []int {
	var out []int
	add := func(f float64) {
		if i := int(f); f == float64(i) && i >= 0 && i < n && !slices.Contains(out, i) {
			out = append(out, i)
		}
	}
	switch x := v.(type) {
	case []int:
		for _, i := range x {
			add(float64(i))
		}
	case []any:
		for _, e := range x {
			if f, ok := e.(float64); ok {
				add(f)
			}
		}
	}
	slices.Sort(out)
	return out
}
