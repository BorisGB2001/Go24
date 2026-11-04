package company

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «eNPS»: an administrator or an HR officer with the right asks the employees (the whole company or
// departments with sub-departments) «how likely are you to recommend the company as a place to work», 0–10, with a
// comment. The answer is anonymous: it keeps no person, and the results — the index (the share of promoters, 9–10,
// less the share of detractors, 0–6), the scores, the departments and the comments — show only from EnpsMinGroup
// answers. The employee is notified and reminded in the top bar until they answer.

// EnpsMinGroup is how many answers make a group (the survey, a department) shown: fewer could tell who answered.
const EnpsMinGroup = 3

// EnpsCommentMax limits a comment, in characters.
const EnpsCommentMax = 1000

// Enps is a survey.
type Enps struct {
	ID          int64
	Question    string
	From, To    time.Time
	Departments []Department // the audience: with sub-departments; none — the whole company
	Closed      *time.Time
	ByID        string
	ByName      string
	Created     time.Time
	Audience    int  // employees asked
	Answers     int  // answered
	Answered    bool // the person reading has answered (EnpsFor, Surveys)
	Asked       bool // the person reading is in the audience
}

// Status: "coming" (before its first day), "open" or "closed" (finished early or after its last day).
func (e Enps) Status(day time.Time) string {
	switch {
	case e.Closed != nil || e.To.Before(day):
		return "closed"
	case e.From.After(day):
		return "coming"
	}
	return "open"
}

// Rate is the share of the audience who answered, in per cent.
func (e Enps) Rate() int {
	if e.Audience == 0 {
		return 0
	}
	return e.Answers * 100 / e.Audience
}

// EnpsGroup is a group's results: the survey's or a department's.
type EnpsGroup struct {
	ID, Name                        string
	Answers                         int
	Promoters, Passives, Detractors int
	Index                           int // eNPS: % promoters − % detractors, −100…100
}

func (g *EnpsGroup) add(score int) {
	g.Answers++
	switch {
	case score >= 9:
		g.Promoters++
	case score <= 6:
		g.Detractors++
	default:
		g.Passives++
	}
}

func (g *EnpsGroup) count() {
	if g.Answers > 0 {
		g.Index = (g.Promoters*100 - g.Detractors*100) / g.Answers
	}
}

// Share is a part of the answers in per cent.
func (g EnpsGroup) Share(n int) int {
	if g.Answers == 0 {
		return 0
	}
	return n * 100 / g.Answers
}

// EnpsResults are a survey's results; Shown is false when the answers are too few to show anything but their count.
type EnpsResults struct {
	EnpsGroup
	Shown       bool
	Scores      [11]int // answers by score
	Departments []EnpsGroup
	Hidden      int      // answers of the departments too small to show
	Comments    []string // without their authors, in no telling order
	Prev        *Enps    // the survey before (the index to compare)
	PrevIndex   *int
}

// Compared: the index of the survey before is there to compare; Delta is the change since it.
func (r EnpsResults) Compared() bool { return r.Shown && r.PrevIndex != nil }
func (r EnpsResults) Delta() int     { return r.Index - r.Before() }

// Before is the index of the survey before (0 — none to compare).
func (r EnpsResults) Before() int {
	if !r.Compared() {
		return 0
	}
	return *r.PrevIndex
}

// MaxScore is the most answers of one score (the bars of the chart).
func (r EnpsResults) MaxScore() int {
	m := 0
	for _, n := range r.Scores {
		m = max(m, n)
	}
	return m
}

// CanEnps: administrators and HR officers an administrator let run the surveys.
func (s *Store) CanEnps(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	h, err := s.HROf(ctx, userID)
	return h.Can("enps"), err
}

const enpsSQL = `SELECT e.id, e.question, e.starts_on, e.ends_on, e.departments::text[], e.closed_at, coalesce(e.created_by::text, ''),
		coalesce(b.name, ''), e.created_at,
		(SELECT count(*) FROM enps_audience a WHERE a.survey_id = e.id),
		(SELECT count(*) FROM enps_audience a WHERE a.survey_id = e.id AND a.answered),
		coalesce(me.answered, false), me.user_id IS NOT NULL
	FROM enps_surveys e LEFT JOIN users b ON b.id = e.created_by
	LEFT JOIN enps_audience me ON me.survey_id = e.id AND me.user_id::text = $1`

func (s *Store) scanEnps(ctx context.Context, sql string, args ...any) ([]Enps, error) {
	rows, err := s.db.Query(ctx, enpsSQL+sql, args...)
	if err != nil {
		return nil, err
	}
	var depts [][]string
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Enps, error) {
		var e Enps
		var ids []string
		err := row.Scan(&e.ID, &e.Question, &e.From, &e.To, &ids, &e.Closed, &e.ByID, &e.ByName, &e.Created,
			&e.Audience, &e.Answers, &e.Answered, &e.Asked)
		e.From, e.To = localDay(e.From), localDay(e.To) // a date comes as UTC midnight
		depts = append(depts, ids)
		return e, err
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	names, err := s.deptNames(ctx)
	if err != nil {
		return nil, err
	}
	for i, ids := range depts {
		for _, id := range ids {
			if n, ok := names[id]; ok {
				list[i].Departments = append(list[i].Departments, Department{ID: id, Name: n})
			}
		}
	}
	return list, nil
}

func (s *Store) deptNames(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name FROM departments`)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return nil, err
		}
		m[id] = name
	}
	return m, rows.Err()
}

// Surveys lists the surveys, latest first, as the person ($1) sees them: those keeping the surveys — all, the others —
// those they are asked in.
func (s *Store) Surveys(ctx context.Context, userID string, all bool) ([]Enps, error) {
	where := ""
	if !all {
		where = " WHERE me.user_id IS NOT NULL"
	}
	return s.scanEnps(ctx, where+" ORDER BY e.starts_on DESC, e.id DESC LIMIT 200", userID)
}

// Survey returns a survey as the person sees it.
func (s *Store) Survey(ctx context.Context, userID string, id int64) (Enps, error) {
	list, err := s.scanEnps(ctx, " WHERE e.id = $2", userID, id)
	if err != nil {
		return Enps{}, err
	}
	if len(list) == 0 {
		return Enps{}, ErrNotFound
	}
	return list[0], nil
}

// EnpsDue is the open survey the person has not answered yet (the earliest), nil — none: the top bar reminds of it.
func (s *Store) EnpsDue(ctx context.Context, userID string, now time.Time) (*Enps, error) {
	list, err := s.scanEnps(ctx, ` WHERE me.user_id IS NOT NULL AND NOT me.answered AND e.closed_at IS NULL
		AND $2::date BETWEEN e.starts_on AND e.ends_on ORDER BY e.ends_on, e.id LIMIT 1`, userID, now.Format(time.DateOnly))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// EnpsInput is a new survey.
type EnpsInput struct {
	Question    string
	From, To    time.Time
	Departments []string // with sub-departments; none — the whole company
}

// CreateEnps makes a survey: its audience — the active employees (not guests) of the departments with sub-departments,
// their heads and deputies included, or of the whole company — is fixed and notified. Who may — the caller checks
// (CanEnps).
func (s *Store) CreateEnps(ctx context.Context, actor string, in EnpsInput) (int64, error) {
	in.Question = strings.TrimSpace(in.Question)
	if in.Question == "" || utf8.RuneCountInString(in.Question) > 300 {
		return 0, i18n.E("err.enps_question")
	}
	if in.From.IsZero() || in.To.Before(in.From) {
		return 0, i18n.E("err.enps_dates")
	}
	depts := []string{}
	for _, d := range in.Departments {
		if d = strings.TrimSpace(d); d != "" {
			depts = append(depts, d)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var known int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM departments WHERE id::text = ANY($1)`, depts).Scan(&known); err != nil {
		return 0, err
	}
	if known != len(depts) {
		return 0, ErrNotFound
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO enps_surveys (question, starts_on, ends_on, departments, created_by)
		VALUES ($1, $2, $3, $4::text[]::uuid[], $5) RETURNING id`,
		in.Question, in.From.Format(time.DateOnly), in.To.Format(time.DateOnly), depts, actor).Scan(&id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = ANY($2)
				UNION SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id)
		INSERT INTO enps_audience (survey_id, user_id)
		SELECT $1, u.id FROM users u WHERE u.active AND NOT u.guest AND u.oidc_sub <> 'legacy:0'
			AND (cardinality($2::text[]) = 0
				OR u.id IN (SELECT ud.user_id FROM user_departments ud WHERE ud.department_id IN (SELECT id FROM sub))
				OR u.id IN (SELECT head_id FROM departments WHERE id IN (SELECT id FROM sub))
				OR u.id IN (SELECT user_id FROM department_deputies WHERE department_id IN (SELECT id FROM sub)))`,
		id, depts); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
		SELECT user_id, $2::uuid, 'enps', $3, '/company/enps/' || $1::bigint FROM enps_audience WHERE survey_id = $1 AND user_id <> $2::uuid`,
		id, actor, in.Question); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// AnswerEnps takes the person's answer to an open survey they are asked in, once; the answer is stored apart from
// the person, with the departments they work in, head or are a deputy of.
func (s *Store) AnswerEnps(ctx context.Context, userID string, id int64, score int, comment string, now time.Time) error {
	comment = strings.TrimSpace(comment)
	if score < 0 || score > 10 || utf8.RuneCountInString(comment) > EnpsCommentMax {
		return i18n.E("err.bad_form")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var open bool
	err = tx.QueryRow(ctx, `UPDATE enps_audience a SET answered = true FROM enps_surveys e
		WHERE e.id = a.survey_id AND a.survey_id = $1 AND a.user_id::text = $2 AND NOT a.answered
		RETURNING e.closed_at IS NULL AND $3::date BETWEEN e.starts_on AND e.ends_on`, id, userID, now.Format(time.DateOnly)).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		var asked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM enps_audience WHERE survey_id = $1 AND user_id::text = $2)`,
			id, userID).Scan(&asked); err != nil {
			return err
		}
		if asked {
			return i18n.E("err.enps_answered")
		}
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !open {
		return i18n.E("err.enps_closed")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO enps_answers (survey_id, score, comment, departments)
		VALUES ($1, $2, $3, ARRAY(SELECT department_id FROM user_departments WHERE user_id::text = $4
			UNION SELECT id FROM departments WHERE head_id::text = $4
			UNION SELECT department_id FROM department_deputies WHERE user_id::text = $4))`, id, score, comment, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CloseEnps finishes a survey before its last day: answers are taken no more.
func (s *Store) CloseEnps(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE enps_surveys SET closed_at = coalesce(closed_at, now()) WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DeleteEnps deletes a survey with its answers.
func (s *Store) DeleteEnps(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM enps_surveys WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// EnpsResultsOf counts a survey's results; a department shows from EnpsMinGroup answers, and nothing but the count
// shows while the survey has fewer. Prev is the survey before it (by its first day) with its index, when shown.
func (s *Store) EnpsResultsOf(ctx context.Context, e Enps) (EnpsResults, error) {
	res, err := s.enpsCount(ctx, e.ID, true)
	if err != nil {
		return res, err
	}
	prev, err := s.scanEnps(ctx, ` WHERE (e.starts_on, e.id) < ($2::date, $3) ORDER BY e.starts_on DESC, e.id DESC LIMIT 1`,
		"", e.From.Format(time.DateOnly), e.ID)
	if err != nil {
		return res, err
	}
	if len(prev) > 0 {
		res.Prev = &prev[0]
		if p, err := s.enpsCount(ctx, prev[0].ID, false); err != nil {
			return res, err
		} else if p.Shown {
			res.PrevIndex = &p.Index
		}
	}
	return res, nil
}

// EnpsIndex is a survey's index, nil — too few answers to show it.
func (s *Store) EnpsIndex(ctx context.Context, id int64) (*int, error) {
	r, err := s.enpsCount(ctx, id, false)
	if err != nil || !r.Shown {
		return nil, err
	}
	return &r.Index, nil
}

func (s *Store) enpsCount(ctx context.Context, id int64, full bool) (EnpsResults, error) {
	var res EnpsResults
	// md5 of the id: the comments come in no order telling when (or so who) they were written
	rows, err := s.db.Query(ctx, `SELECT score, comment, departments::text[] FROM enps_answers WHERE survey_id = $1 ORDER BY md5(id::text)`, id)
	if err != nil {
		return res, err
	}
	type answer struct {
		score   int
		comment string
		depts   []string
	}
	var list []answer
	for rows.Next() {
		var a answer
		if err := rows.Scan(&a.score, &a.comment, &a.depts); err != nil {
			rows.Close()
			return res, err
		}
		list = append(list, a)
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	byDept := map[string]*EnpsGroup{}
	for _, a := range list {
		res.add(a.score)
		res.Scores[a.score]++
		if a.comment != "" {
			res.Comments = append(res.Comments, a.comment)
		}
		for _, d := range a.depts {
			if byDept[d] == nil {
				byDept[d] = &EnpsGroup{ID: d}
			}
			byDept[d].add(a.score)
		}
	}
	res.count()
	if res.Shown = res.Answers >= EnpsMinGroup; !res.Shown {
		res.EnpsGroup = EnpsGroup{Answers: res.Answers}
		res.Scores, res.Comments = [11]int{}, nil
		return res, nil
	}
	if !full {
		return res, nil
	}
	names, err := s.deptNames(ctx)
	if err != nil {
		return res, err
	}
	for d, g := range byDept {
		if g.Answers < EnpsMinGroup {
			continue
		}
		g.Name = names[d]
		g.count()
		res.Departments = append(res.Departments, *g)
	}
	for _, a := range list {
		if !slices.ContainsFunc(a.depts, func(d string) bool { return byDept[d].Answers >= EnpsMinGroup }) {
			res.Hidden++
		}
	}
	// the best index first, then by name
	slices.SortFunc(res.Departments, func(a, b EnpsGroup) int {
		return cmp.Or(cmp.Compare(b.Index, a.Index), strings.Compare(a.Name, b.Name))
	})
	return res, nil
}
