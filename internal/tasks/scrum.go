package tasks

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Scrum mirrors the portal scrum groups: tasks of a scrum project live in the backlog or in a sprint;
// one sprint at a time is active and shown as a kanban; a completed sprint returns its unfinished tasks
// to the backlog (or to a planned sprint) and keeps its statistics.

type Epic struct {
	ID          string
	Name        string
	Description string
	Color       string
	Tasks       int
	Points      float64
}

type Sprint struct {
	ID          string
	Name        string
	Goal        string
	Status      string // planned | active | completed
	DateStart   *time.Time
	DateEnd     *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	PointsStart *float64
	PointsDone  *float64
	Tasks       []Task
	Points      float64 // story points of all tasks in the sprint
	Done        float64 // of completed tasks
	DoneCount   int
}

// Percent of story points done (by task count when nothing is estimated), as the sprint header shows it.
func (s Sprint) Percent() int {
	if s.Points > 0 {
		return int(s.Done * 100 / s.Points)
	}
	if len(s.Tasks) == 0 {
		return 0
	}
	return s.DoneCount * 100 / len(s.Tasks)
}

// Overdue: the sprint is active and its end date has passed.
func (s Sprint) Overdue() bool {
	return s.Status == "active" && s.DateEnd != nil && s.DateEnd.Before(time.Now().Truncate(24*time.Hour))
}

type ScrumPlan struct {
	Backlog []Task
	Points  float64 // story points in the backlog
	Sprints []Sprint
	Epics   []Epic
}

// SprintLength is the default sprint duration (two weeks).
const SprintLength = 14 * 24 * time.Hour

func points(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// SetProjectKind turns a project into a scrum team or back into an ordinary group.
func (s *Store) SetProjectKind(ctx context.Context, p Project, kind string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	if (kind != "group" && kind != "project" && kind != "scrum") || p.Collab() { // a collab stays a collab
		return invalid("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET kind = $2, updated_at = now() WHERE id = $1`, p.ID, kind)
	return err
}

func (s *Store) sprints(ctx context.Context, projectID string, statuses ...string) ([]Sprint, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, goal, status, date_start, date_end, started_at, completed_at,
			points_start::float8, points_done::float8
		FROM sprints WHERE project_id = $1 AND status = ANY($2)
		ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'planned' THEN 1 ELSE 2 END, sort, coalesce(completed_at, created_at) DESC`,
		projectID, statuses)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Sprint, error) {
		var sp Sprint
		err := r.Scan(&sp.ID, &sp.Name, &sp.Goal, &sp.Status, &sp.DateStart, &sp.DateEnd, &sp.StartedAt, &sp.CompletedAt,
			&sp.PointsStart, &sp.PointsDone)
		return sp, err
	})
}

// ActiveSprint returns the running sprint of the project, if any.
func (s *Store) ActiveSprint(ctx context.Context, projectID string) (*Sprint, error) {
	list, err := s.sprints(ctx, projectID, "active")
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// ScrumPlanFor returns the planning view: backlog, active and planned sprints with their tasks, epics.
func (s *Store) ScrumPlanFor(ctx context.Context, userID string, p Project) (ScrumPlan, error) {
	var out ScrumPlan
	sprints, err := s.sprints(ctx, p.ID, "active", "planned")
	if err != nil {
		return out, err
	}
	idx := map[string]int{}
	for i, sp := range sprints {
		idx[sp.ID] = i
	}
	list, err := s.listOrdered(ctx, Filter{ProjectID: p.ID, Viewer: userID}, `t.backlog_sort, t.created_at DESC`)
	if err != nil {
		return out, err
	}
	for _, t := range list {
		if i, ok := idx[t.SprintID]; ok {
			sp := &sprints[i]
			sp.Tasks = append(sp.Tasks, t)
			sp.Points += points(t.StoryPoints)
			if t.Status == StatusCompleted {
				sp.Done += points(t.StoryPoints)
				sp.DoneCount++
			}
			continue
		}
		if t.SprintID == "" && t.Status != StatusCompleted {
			out.Backlog = append(out.Backlog, t)
			out.Points += points(t.StoryPoints)
		}
	}
	out.Sprints = sprints
	out.Epics, err = s.Epics(ctx, p.ID)
	return out, err
}

// CompletedSprints lists finished sprints with their frozen statistics, newest first.
func (s *Store) CompletedSprints(ctx context.Context, p Project) ([]Sprint, error) {
	list, err := s.sprints(ctx, p.ID, "completed")
	if err != nil {
		return nil, err
	}
	for i := range list {
		if err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'completed'), count(*) FROM tasks WHERE sprint_id = $1`,
			list[i].ID).Scan(&list[i].DoneCount, new(int)); err != nil {
			return nil, err
		}
		list[i].Points, list[i].Done = points(list[i].PointsStart), points(list[i].PointsDone)
	}
	return list, nil
}

func (s *Store) sprintOf(ctx context.Context, q querier, projectID, sprintID string) (Sprint, error) {
	var sp Sprint
	err := q.QueryRow(ctx, `SELECT id, name, status, date_start, date_end FROM sprints WHERE id = $1 AND project_id = $2`,
		sprintID, projectID).Scan(&sp.ID, &sp.Name, &sp.Status, &sp.DateStart, &sp.DateEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return sp, ErrNotFound
	}
	return sp, err
}

// canPlan: project members plan the backlog and sprints (viewers only look), administrators too.
func canPlan(p Project) bool { return p.CanWork() }

// CreateSprint adds a planned sprint after the last one: it starts when the previous one ends and lasts two weeks.
func (s *Store) CreateSprint(ctx context.Context, p Project, name string) error {
	if !p.Scrum() || !canPlan(p) {
		return ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return invalid("err.sprint_name")
	}
	var last *time.Time
	if err := s.db.QueryRow(ctx, `SELECT max(date_end) FROM sprints WHERE project_id = $1 AND status <> 'completed'`, p.ID).Scan(&last); err != nil {
		return err
	}
	start := time.Now().Truncate(24 * time.Hour)
	if last != nil && last.After(start) {
		start = last.AddDate(0, 0, 1)
	}
	end := start.Add(SprintLength - 24*time.Hour)
	_, err := s.db.Exec(ctx, `INSERT INTO sprints (project_id, name, date_start, date_end, sort)
		VALUES ($1, $2, $3, $4, (SELECT coalesce(max(sort), 0) + 10 FROM sprints WHERE project_id = $1))`, p.ID, name, start, end)
	return err
}

// UpdateSprint renames a sprint, sets its goal and dates (not for completed sprints).
func (s *Store) UpdateSprint(ctx context.Context, p Project, id, name, goal string, start, end *time.Time) error {
	if !canPlan(p) {
		return ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return invalid("err.sprint_name")
	}
	if start != nil && end != nil && end.Before(*start) {
		return invalid("err.plan_dates")
	}
	sp, err := s.sprintOf(ctx, s.db, p.ID, id)
	if err != nil {
		return err
	}
	if sp.Status == "completed" {
		return invalid("err.sprint_completed")
	}
	_, err = s.db.Exec(ctx, `UPDATE sprints SET name = $2, goal = $3, date_start = $4, date_end = $5 WHERE id = $1`,
		id, name, strings.TrimSpace(goal), start, end)
	return err
}

// DeleteSprint removes a planned sprint; its tasks go back to the backlog.
func (s *Store) DeleteSprint(ctx context.Context, p Project, id string) error {
	if !canPlan(p) {
		return ErrForbidden
	}
	sp, err := s.sprintOf(ctx, s.db, p.ID, id)
	if err != nil {
		return err
	}
	if sp.Status != "planned" {
		return invalid("err.sprint_not_planned")
	}
	_, err = s.db.Exec(ctx, `DELETE FROM sprints WHERE id = $1`, id) // tasks.sprint_id → NULL
	return err
}

// StartSprint makes a planned sprint active (only one at a time). Its tasks appear on the kanban.
func (s *Store) StartSprint(ctx context.Context, p Project, id string) error {
	if !p.CanLead() {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		sp, err := s.sprintOf(ctx, tx, p.ID, id)
		if err != nil {
			return err
		}
		if sp.Status != "planned" {
			return invalid("err.sprint_not_planned")
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sprints WHERE project_id = $1 AND status = 'active')`, p.ID).Scan(&active); err != nil {
			return err
		}
		if active {
			return invalid("err.sprint_active_exists")
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE sprint_id = $1`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return invalid("err.sprint_empty")
		}
		today := time.Now().Truncate(24 * time.Hour)
		if _, err := tx.Exec(ctx, `UPDATE sprints SET status = 'active', started_at = now(),
				date_start = coalesce(date_start, $2), date_end = coalesce(date_end, $3),
				points_start = (SELECT coalesce(sum(story_points), 0) FROM tasks WHERE sprint_id = $1)
			WHERE id = $1`, id, today, today.Add(SprintLength-24*time.Hour)); err != nil {
			return err
		}
		return ensureProjectStages(ctx, tx, p.ID)
	})
}

// CompleteSprint finishes the active sprint. Unfinished tasks go to the backlog or to a planned sprint (moveTo).
func (s *Store) CompleteSprint(ctx context.Context, p Project, id, moveTo string) error {
	if !p.CanLead() {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		sp, err := s.sprintOf(ctx, tx, p.ID, id)
		if err != nil {
			return err
		}
		if sp.Status != "active" {
			return invalid("err.sprint_not_active")
		}
		var target any
		if moveTo != "" {
			to, err := s.sprintOf(ctx, tx, p.ID, moveTo)
			if err != nil {
				return err
			}
			if to.Status != "planned" {
				return invalid("err.sprint_not_planned")
			}
			target = to.ID
		}
		if _, err := tx.Exec(ctx, `UPDATE sprints SET status = 'completed', completed_at = now(),
				points_done = (SELECT coalesce(sum(story_points), 0) FROM tasks WHERE sprint_id = $1 AND status = 'completed')
			WHERE id = $1`, id); err != nil {
			return err
		}
		// unfinished tasks leave the sprint; the kanban starts over for them
		_, err = tx.Exec(ctx, `UPDATE tasks SET sprint_id = $2, stage_id = NULL WHERE sprint_id = $1 AND status <> 'completed'`, id, target)
		return err
	})
}

// MoveToSprint puts a task into a sprint (sprintID "" = back to the backlog). Moving into the active sprint
// puts the task on the first stage of its kanban.
func (s *Store) MoveToSprint(ctx context.Context, userID, taskID, sprintID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Worker() || a.Roles.Manager || a.MemberRole == "member") {
			return ErrForbidden
		}
		var kind string
		if err := tx.QueryRow(ctx, `SELECT kind FROM projects WHERE id = $1`, a.ProjectID).Scan(&kind); err != nil {
			return err
		}
		if kind != "scrum" {
			return invalid("err.not_scrum")
		}
		var target any
		if sprintID != "" {
			sp, err := s.sprintOf(ctx, tx, a.ProjectID, sprintID)
			if err != nil {
				return err
			}
			if sp.Status == "completed" {
				return invalid("err.sprint_completed")
			}
			target = sp.ID
		}
		var old string
		if err := tx.QueryRow(ctx, `SELECT coalesce(sprint_id::text, '') FROM tasks WHERE id = $1`, taskID).Scan(&old); err != nil {
			return err
		}
		if old == sprintID {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET sprint_id = $2, stage_id = NULL, updated_at = now(),
			backlog_sort = (SELECT coalesce(min(backlog_sort), 0) - 1 FROM tasks WHERE project_id = $3 AND sprint_id IS NOT DISTINCT FROM $2::uuid)
			WHERE id = $1`, taskID, target, a.ProjectID); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "sprint", sprintName(ctx, tx, old), sprintName(ctx, tx, sprintID))
	})
}

func sprintName(ctx context.Context, q querier, id string) string {
	if id == "" {
		return "sprint.backlog"
	}
	var n string
	_ = q.QueryRow(ctx, `SELECT name FROM sprints WHERE id = $1`, id).Scan(&n)
	return n
}

// SetStoryPoints estimates a task (nil clears the estimate).
func (s *Store) SetStoryPoints(ctx context.Context, userID, taskID string, sp *float64) error {
	if sp != nil && (*sp < 0 || *sp > 1000) {
		return invalid("err.story_points")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Worker() || a.Roles.Manager || a.MemberRole == "member") {
			return ErrForbidden
		}
		var old *float64
		if err := tx.QueryRow(ctx, `SELECT story_points::float8 FROM tasks WHERE id = $1`, taskID).Scan(&old); err != nil {
			return err
		}
		if fmtPoints(old) == fmtPoints(sp) {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET story_points = $2, updated_at = now() WHERE id = $1`, taskID, sp); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "story_points", fmtPoints(old), fmtPoints(sp))
	})
}

// SetEpic links a task to an epic of its project ("" unlinks).
func (s *Store) SetEpic(ctx context.Context, userID, taskID, epicID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Worker() || a.Roles.Manager || a.MemberRole == "member") {
			return ErrForbidden
		}
		var target any
		name := ""
		if epicID != "" {
			if err := tx.QueryRow(ctx, `SELECT name FROM epics WHERE id = $1 AND project_id = $2`, epicID, a.ProjectID).Scan(&name); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			target = epicID
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks SET epic_id = $2, updated_at = now() WHERE id = $1`, taskID, target); err != nil {
			return err
		}
		return addHistory(ctx, tx, taskID, userID, "epic", "", name)
	})
}

func fmtPoints(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'f', -1, 64)
}

// ---------- epics ----------

var epicColorRe = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

func (s *Store) Epics(ctx context.Context, projectID string) ([]Epic, error) {
	rows, err := s.db.Query(ctx, `SELECT e.id, e.name, e.description, e.color,
			(SELECT count(*) FROM tasks t WHERE t.epic_id = e.id)::int,
			(SELECT coalesce(sum(story_points), 0) FROM tasks t WHERE t.epic_id = e.id)::float8
		FROM epics e WHERE e.project_id = $1 ORDER BY e.created_at`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Epic])
}

func cleanEpic(name, color string) (string, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return "", "", invalid("err.epic_name")
	}
	color = strings.TrimSpace(color)
	if color == "" {
		color = "69DAFF"
	}
	if !epicColorRe.MatchString(color) {
		return "", "", invalid("err.stage_color")
	}
	return name, strings.ToUpper(strings.TrimPrefix(color, "#")), nil
}

func (s *Store) CreateEpic(ctx context.Context, actorID string, p Project, name, color, description string) error {
	if !p.Scrum() || !canPlan(p) {
		return ErrForbidden
	}
	name, color, err := cleanEpic(name, color)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO epics (project_id, name, color, description, created_by) VALUES ($1, $2, $3, $4, $5)`,
		p.ID, name, color, strings.TrimSpace(description), actorID)
	return err
}

func (s *Store) UpdateEpic(ctx context.Context, p Project, id, name, color, description string) error {
	if !canPlan(p) {
		return ErrForbidden
	}
	name, color, err := cleanEpic(name, color)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE epics SET name = $3, color = $4, description = $5 WHERE id = $1 AND project_id = $2`,
		id, p.ID, name, color, strings.TrimSpace(description))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteEpic(ctx context.Context, p Project, id string) error {
	if !canPlan(p) {
		return ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM epics WHERE id = $1 AND project_id = $2`, id, p.ID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------- burndown ----------

// BurnPoint is one day of the burndown chart: the ideal remaining work and the real one.
type BurnPoint struct {
	Day       time.Time
	Ideal     float64
	Remaining float64
	Future    bool // after today: no real value yet
}

// Burndown computes the chart of a started sprint: story points (or tasks, when nothing is estimated)
// remaining at the end of each day, against the ideal straight line.
func (s *Store) Burndown(ctx context.Context, sprintID string) ([]BurnPoint, error) {
	var start, end *time.Time
	if err := s.db.QueryRow(ctx, `SELECT date_start, date_end FROM sprints WHERE id = $1`, sprintID).Scan(&start, &end); err != nil {
		return nil, err
	}
	if start == nil || end == nil {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT coalesce(story_points::float8, 0), closed_at FROM tasks WHERE sprint_id = $1`, sprintID)
	if err != nil {
		return nil, err
	}
	type item struct {
		sp     float64
		closed *time.Time
	}
	items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (item, error) {
		var it item
		return it, r.Scan(&it.sp, &it.closed)
	})
	if err != nil {
		return nil, err
	}
	total, estimated := 0.0, false
	for _, it := range items {
		total += it.sp
		estimated = estimated || it.sp > 0
	}
	weight := func(it item) float64 {
		if estimated {
			return it.sp
		}
		return 1
	}
	if !estimated {
		total = float64(len(items))
	}
	days := int(end.Sub(*start).Hours()/24) + 1
	if days < 1 || days > 366 {
		return nil, nil
	}
	today := time.Now()
	out := make([]BurnPoint, 0, days)
	for d := 0; d < days; d++ {
		day := start.AddDate(0, 0, d)
		dayEnd := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, time.Local)
		bp := BurnPoint{Day: day, Ideal: total * float64(days-1-d) / float64(max(days-1, 1))}
		if day.After(today) {
			bp.Future = true
		} else {
			rem := total
			for _, it := range items {
				if it.closed != nil && !it.closed.After(dayEnd) {
					rem -= weight(it)
				}
			}
			bp.Remaining = rem
		}
		out = append(out, bp)
	}
	return out, nil
}
