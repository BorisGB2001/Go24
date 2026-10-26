package tasks

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// Efficiency (Эффективность): the share of the person's tasks in work during a period that went
// without violations. Counted are the tasks where the person is the responsible, set by someone else and not marked
// «Не учитывать в эффективности», that were open at some moment of the period. A violation is being overdue during
// the period (the deadline passed while the task was open) or being returned for rework (the creator did not accept
// the result). Nothing is stored: the numbers follow the deadlines and statuses as they are now.

// Violation kinds.
const (
	ViolationOverdue = "overdue"
	ViolationRework  = "rework"
)

// Violation is one task's violation in the period.
type Violation struct {
	TaskID, Key, Title, Kind string
	At                       time.Time
}

// DayPoint is the efficiency from the start of the period to the end of the day.
type DayPoint struct {
	Day     time.Time
	Percent int
}

// Efficiency of one person for a period.
type Efficiency struct {
	UserID, Name    string
	Percent         int // 100 when there were no tasks
	Total, Violated int // tasks in work, of them with violations
	Days            []DayPoint
	Violations      []Violation // the latest first
}

type effTask struct {
	ID, Key, Title     string
	Created            time.Time
	Deadline, ClosedAt *time.Time
	reworks            []time.Time
}

// inWork: the task was open at some moment of [from, end).
func (t effTask) inWork(from, end time.Time) bool {
	return t.Created.Before(end) && (t.ClosedAt == nil || !t.ClosedAt.Before(from))
}

// overdueAt: when the task was overdue first within [from, end), if it was.
func (t effTask) overdueAt(from, end time.Time) (time.Time, bool) {
	if t.Deadline == nil || !t.Deadline.Before(end) {
		return time.Time{}, false
	}
	if t.ClosedAt != nil && (!t.ClosedAt.After(*t.Deadline) || !t.ClosedAt.After(from)) {
		return time.Time{}, false
	}
	if t.Deadline.Before(from) {
		return from, true
	}
	return *t.Deadline, true
}

func (t effTask) violations(from, end time.Time) []Violation {
	var out []Violation
	if at, ok := t.overdueAt(from, end); ok {
		out = append(out, Violation{TaskID: t.ID, Key: t.Key, Title: t.Title, Kind: ViolationOverdue, At: at})
	}
	for _, at := range t.reworks {
		if !at.Before(from) && at.Before(end) {
			out = append(out, Violation{TaskID: t.ID, Key: t.Key, Title: t.Title, Kind: ViolationRework, At: at})
		}
	}
	return out
}

func percent(total, violated int) int {
	if total == 0 {
		return 100
	}
	return (total - violated) * 100 / total
}

// score counts the tasks in work during [from, end) and those with violations.
func score(list []effTask, from, end time.Time) (total, violated int, vs []Violation) {
	for _, t := range list {
		if !t.inWork(from, end) {
			continue
		}
		total++
		if v := t.violations(from, end); len(v) > 0 {
			violated++
			vs = append(vs, v...)
		}
	}
	return total, violated, vs
}

// EfficiencyOf computes the person's efficiency for [from, to); the part after now is not counted yet.
func (s *Store) EfficiencyOf(ctx context.Context, userID string, from, to, now time.Time) (Efficiency, error) {
	e := Efficiency{UserID: userID}
	end := to
	if now.Before(end) {
		end = now
	}
	if err := s.db.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&e.Name); err != nil {
		return e, err
	}
	rows, err := s.db.Query(ctx, `SELECT t.id::text, p.key || '-' || t.number, t.title, t.created_at, t.deadline, t.closed_at
		FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.responsible_id = $1 AND t.creator_id <> $1 AND NOT t.no_efficiency
		  AND t.created_at < $3 AND (t.closed_at IS NULL OR t.closed_at >= $2)`, userID, from, end)
	if err != nil {
		return e, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (effTask, error) {
		var t effTask
		err := row.Scan(&t.ID, &t.Key, &t.Title, &t.Created, &t.Deadline, &t.ClosedAt)
		return t, err
	})
	if err != nil {
		return e, err
	}
	byID := make(map[string]*effTask, len(list))
	ids := make([]string, len(list))
	for i := range list {
		byID[list[i].ID], ids[i] = &list[i], list[i].ID
	}
	rows, err = s.db.Query(ctx, `SELECT task_id::text, created_at FROM task_history
		WHERE field = 'status' AND old_value = 'awaiting_control' AND new_value <> 'completed'
		  AND created_at >= $1 AND created_at < $2 AND task_id = ANY($3::uuid[])`, from, end, ids)
	if err != nil {
		return e, err
	}
	var id string
	var at time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &at}, func() error {
		byID[id].reworks = append(byID[id].reworks, at)
		return nil
	}); err != nil {
		return e, err
	}

	e.Total, e.Violated, e.Violations = score(list, from, end)
	e.Percent = percent(e.Total, e.Violated)
	sort.Slice(e.Violations, func(i, j int) bool { return e.Violations[i].At.After(e.Violations[j].At) })
	for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
		dayEnd := d.AddDate(0, 0, 1)
		if end.Before(dayEnd) {
			dayEnd = end
		}
		total, violated, _ := score(list, from, dayEnd)
		e.Days = append(e.Days, DayPoint{Day: d, Percent: percent(total, violated)})
	}
	return e, nil
}

// SetNoEfficiency marks the task «Не учитывать в эффективности» (the creator or project managers).
func (s *Store) SetNoEfficiency(ctx context.Context, userID, taskID string, on bool) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !a.Roles.Creator && !a.Roles.Manager {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE tasks SET no_efficiency = $2, version = version + 1, updated_at = now()
			WHERE id = $1 AND no_efficiency <> $2`, taskID, on)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "no_efficiency", boolText(!on), boolText(on))
	})
}

func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
