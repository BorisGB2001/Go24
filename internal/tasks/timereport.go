package tasks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// TimeRow is a line of «Затраченное время»: the time a person logged on tasks in a period.
type TimeRow struct {
	UserID  string
	Name    string
	Tasks   int // tasks the time was logged on
	Seconds int
	Over    int // of them, tasks whose whole logged time passed «Плановые трудозатраты»
}

// TimeTask is a task of a person's line: the time they logged in the period and the task's totals.
type TimeTask struct {
	Task
	Seconds         int  // the person's, in the period
	TotalSeconds    int  // everyone's, all time
	EstimateMinutes *int // «Плановые трудозатраты»
}

func (t TimeTask) Over() bool {
	return t.EstimateMinutes != nil && *t.EstimateMinutes > 0 && t.TotalSeconds > *t.EstimateMinutes*60
}

// timeReportCTE: the entries of the period (days [from, to)) of the given people, optionally of one project's tasks;
// tasks in the trash are left out.
const timeReportCTE = `WITH e AS (
	SELECT te.user_id, te.task_id, sum(te.seconds)::int AS seconds FROM time_entries te JOIN tasks t ON t.id = te.task_id
	WHERE te.user_id::text = ANY($1) AND te.spent_on >= $2::date AND te.spent_on < $3::date AND ($4 = '' OR t.project_id::text = $4)
	GROUP BY te.user_id, te.task_id),
tot AS (SELECT task_id, sum(seconds)::int AS seconds FROM time_entries WHERE task_id IN (SELECT task_id FROM e) GROUP BY task_id)
`

// TimeReport sums the logged time of the people per person, the busiest first; people without time are left out.
func (s *Store) TimeReport(ctx context.Context, users []string, from, to time.Time, projectID string) ([]TimeRow, error) {
	rows, err := s.db.Query(ctx, timeReportCTE+`
		SELECT u.id::text, u.name, count(*)::int, sum(e.seconds)::int,
			count(*) FILTER (WHERE t.estimate_minutes > 0 AND tot.seconds > t.estimate_minutes * 60)::int
		FROM e JOIN users u ON u.id = e.user_id JOIN tasks t ON t.id = e.task_id JOIN tot ON tot.task_id = e.task_id
		GROUP BY u.id, u.name ORDER BY sum(e.seconds) DESC, u.name, u.id`,
		users, from.Format(time.DateOnly), to.Format(time.DateOnly), projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TimeRow, error) {
		var r TimeRow
		return r, row.Scan(&r.UserID, &r.Name, &r.Tasks, &r.Seconds, &r.Over)
	})
}

// TimeReportTasks are the tasks a person logged time on in the period, the most time first.
func (s *Store) TimeReportTasks(ctx context.Context, userID string, from, to time.Time, projectID string) ([]TimeTask, error) {
	rows, err := s.db.Query(ctx, timeReportCTE+`
		SELECT t.id::text, p.key, t.number, t.title, t.status, e.seconds, tot.seconds, t.estimate_minutes
		FROM e JOIN tasks t ON t.id = e.task_id JOIN projects p ON p.id = t.project_id JOIN tot ON tot.task_id = e.task_id
		ORDER BY e.seconds DESC, p.key, t.number`,
		[]string{userID}, from.Format(time.DateOnly), to.Format(time.DateOnly), projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TimeTask, error) {
		var t TimeTask
		return t, row.Scan(&t.ID, &t.ProjectKey, &t.Number, &t.Title, &t.Status, &t.Seconds, &t.TotalSeconds, &t.EstimateMinutes)
	})
}
