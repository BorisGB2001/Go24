package tasks

import (
	"context"
	"time"
)

// «План на день» of the working day (task.planner): the person picks tasks for today in the working
// day window. A task stays in the plan until taken out; a completed one is shown till the end of the day it was
// completed and leaves the plan the day after.

// DayPlanItem is a task of the plan with the time the person spent on it today (a running timer included).
type DayPlanItem struct {
	Task
	TodaySecs int
	Timer     bool // the person's timer runs on this task
}

// dayStart is local midnight of the day of now.
func dayStart(now time.Time) time.Time {
	y, m, d := now.Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// DayPlan returns the person's plan in the order the tasks were added; tasks completed before today leave it.
func (s *Store) DayPlan(ctx context.Context, userID string, now time.Time) ([]DayPlanItem, error) {
	today := dayStart(now)
	if _, err := s.db.Exec(ctx, `DELETE FROM task_day_plan dp USING tasks_all t WHERE dp.user_id = $1 AND t.id = dp.task_id
		AND t.status = 'completed' AND t.closed_at < $2`, userID, today); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, taskSelect+`
		JOIN task_day_plan dp ON dp.task_id = t.id AND dp.user_id = $1
		WHERE `+visibleSQL("$1", "false")+`
		ORDER BY dp.added_at, t.number`, userID)
	if err != nil {
		return nil, err
	}
	var list []DayPlanItem
	at := map[string]int{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		at[t.ID] = len(list)
		list = append(list, DayPlanItem{Task: t})
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(list) == 0 {
		return list, err
	}
	rows, err = s.db.Query(ctx, `
		SELECT task_id::text, sum(seconds)::int, false FROM time_entries
		WHERE user_id = $1 AND spent_on = $2::date AND task_id IN (SELECT task_id FROM task_day_plan WHERE user_id = $1)
		GROUP BY task_id
		UNION ALL
		SELECT task_id::text, greatest(0, extract(epoch FROM $3::timestamptz - greatest(started_at, $4::timestamptz)))::int, true
		FROM task_timers WHERE user_id = $1`, userID, today.Format(time.DateOnly), now, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var secs int
		var timer bool
		if err := rows.Scan(&id, &secs, &timer); err != nil {
			return nil, err
		}
		if i, ok := at[id]; ok {
			list[i].TodaySecs += secs
			list[i].Timer = list[i].Timer || timer
		}
	}
	return list, rows.Err()
}

// DayPlanChoices are the person's open tasks (responsible or accomplice) not in the plan yet, nearest deadline first.
func (s *Store) DayPlanChoices(ctx context.Context, userID string) ([]Task, error) {
	rows, err := s.db.Query(ctx, taskSelect+`
		WHERE t.status <> 'completed'
		  AND (t.responsible_id = $1 OR EXISTS (SELECT 1 FROM task_participants tp
		       WHERE tp.task_id = t.id AND tp.user_id = $1 AND tp.role = 'accomplice'))
		  AND NOT EXISTS (SELECT 1 FROM task_day_plan dp WHERE dp.task_id = t.id AND dp.user_id = $1)
		ORDER BY (t.deadline IS NULL), t.deadline, t.priority DESC, t.updated_at DESC
		LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, t)
	}
	return list, rows.Err()
}

// SetDayPlan puts a task the person sees into the plan or takes it out.
func (s *Store) SetDayPlan(ctx context.Context, userID, taskID string, on bool) error {
	if !on {
		_, err := s.db.Exec(ctx, `DELETE FROM task_day_plan WHERE user_id = $1 AND task_id = $2`, userID, taskID)
		return err
	}
	if err := s.CheckVisible(ctx, userID, taskID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO task_day_plan (user_id, task_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, taskID)
	return err
}

// InDayPlan reports whether the task is in the person's plan.
func (s *Store) InDayPlan(ctx context.Context, userID, taskID string) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_day_plan WHERE user_id = $1 AND task_id = $2)`, userID, taskID).Scan(&on)
	return on, err
}

// DayTask is a task someone spent time on during a day.
type DayTask struct {
	Key   string
	Title string
	Secs  int
}

// DayTasks are the tasks userID logged time on during day (as the working day report lists them),
// only those the viewer sees; most time first.
func (s *Store) DayTasks(ctx context.Context, viewer, userID string, day time.Time) ([]DayTask, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.key || '-' || t.number, t.title, sum(e.seconds)::int FROM time_entries e
		JOIN tasks t ON t.id = e.task_id JOIN projects p ON p.id = t.project_id
		WHERE e.user_id = $2 AND e.spent_on = $3::date AND `+visibleSQL("$1", "$4")+`
		GROUP BY p.key, t.number, t.title ORDER BY 3 DESC, 1`, viewer, userID, day.Format(time.DateOnly), IsAdmin(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []DayTask
	for rows.Next() {
		var d DayTask
		if err := rows.Scan(&d.Key, &d.Title, &d.Secs); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}
