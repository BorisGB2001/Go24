package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Роботы» of the project kanban task automation: when a task enters a stage, the stage's robots
// notify someone, write a comment, hand the task to another responsible, add an accomplice or an auditor, set the
// deadline in N days, change the status, move it to another stage, add a checklist item or make a new task (a subtask).
// A robot may wait («Выполнить через» minutes, hours or days: the task must still be in the stage then). Robots act
// for the person who set them up, with the rights of the project; texts may hold {title}, {responsible}, {deadline},
// {link}. The database queues the robots on every stage change (migration 104); RunRobotsDue runs the queue.

// RobotKinds in the order the settings offer them.
var RobotKinds = []string{"notify", "comment", "responsible", "accomplice", "auditor", "deadline", "status", "stage", "checklist", "task"}

// RobotStatuses are the actions a status robot may take.
var RobotStatuses = []Action{ActionStart, ActionPause, ActionComplete, ActionDefer}

// robotUnits are the delays a robot may wait, by their name in the settings.
var robotUnits = map[string]time.Duration{"min": time.Minute, "hour": time.Hour, "day": 24 * time.Hour}

// Robot of a stage.
type Robot struct {
	ID, StageID, Kind string
	Params            RobotParams
	Active            bool
}

// RobotParams are a robot's settings (unused ones stay empty).
type RobotParams struct {
	Text    string `json:"text,omitempty"`    // notification, comment, checklist item, new task's title
	Details string `json:"details,omitempty"` // new task's description
	To      string `json:"to,omitempty"`      // "responsible", "creator" or a user id
	Days    int    `json:"days,omitempty"`    // deadline robot, new task: deadline in so many days (0 — none)
	Status  Action `json:"status,omitempty"`  // status robot: start | pause | complete | defer
	Stage   string `json:"stage,omitempty"`   // stage robot: where the task goes
	Sub     bool   `json:"sub,omitempty"`     // task robot: the new task is a subtask of this one
	Delay   int    `json:"delay,omitempty"`   // wait so many units before running
	Unit    string `json:"unit,omitempty"`    // min | hour | day
}

// Wait is how long the robot waits before it runs.
func (p RobotParams) Wait() time.Duration { return time.Duration(p.Delay) * robotUnits[p.Unit] }

// Robots are the robots of a project's stages.
func (s *Store) Robots(ctx context.Context, projectID string) ([]Robot, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id, r.stage_id::text, r.kind, r.params, r.active FROM task_robots r
		JOIN project_stages st ON st.id = r.stage_id WHERE st.project_id = $1 ORDER BY r.created_at`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Robot, error) {
		var x Robot
		var raw []byte
		if err := r.Scan(&x.ID, &x.StageID, &x.Kind, &raw, &x.Active); err != nil {
			return x, err
		}
		return x, json.Unmarshal(raw, &x.Params)
	})
}

// AddRobot sets a robot on a stage of the project (its owner or a manager).
func (s *Store) AddRobot(ctx context.Context, userID string, p Project, stageID, kind string, rp RobotParams) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	if _, err := s.stageByID(ctx, s.db, p.ID, stageID); err != nil {
		return err
	}
	rp.Text, rp.Details, rp.To = strings.TrimSpace(rp.Text), strings.TrimSpace(rp.Details), strings.TrimSpace(rp.To)
	needText := kind == "notify" || kind == "comment" || kind == "checklist" || kind == "task"
	if !slices.Contains(RobotKinds, kind) || (needText && rp.Text == "") || len([]rune(rp.Text)) > 500 ||
		len([]rune(rp.Details)) > 5000 || rp.Days < 0 || rp.Days > 365 {
		return invalid("err.robot")
	}
	if rp.Delay < 0 || rp.Delay > 1000 || (rp.Delay > 0 && robotUnits[rp.Unit] == 0) {
		return invalid("err.robot_when")
	}
	if rp.Delay == 0 {
		rp.Unit = ""
	}
	switch kind {
	case "responsible", "accomplice", "auditor":
		if rp.To == "responsible" || rp.To == "creator" {
			break
		}
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE id::text = $1`, rp.To).Scan(&n); err != nil || n == 0 {
			return invalid("err.robot_user")
		}
		if err := collabStrangers(ctx, s.db, p.ID, rp.To); err != nil {
			return err
		}
	case "status":
		if !slices.Contains(RobotStatuses, rp.Status) {
			return invalid("err.robot")
		}
	case "stage":
		if rp.Stage == stageID {
			return invalid("err.robot_stage")
		}
		if _, err := s.stageByID(ctx, s.db, p.ID, rp.Stage); err != nil {
			return invalid("err.robot_stage")
		}
	case "deadline":
		if rp.Days == 0 {
			return invalid("err.robot")
		}
	}
	if rp.To == "" {
		rp.To = "responsible"
	}
	raw, _ := json.Marshal(rp)
	_, err := s.db.Exec(ctx, `INSERT INTO task_robots (stage_id, kind, params, delay_sec, created_by) VALUES ($1, $2, $3, $4, $5)`,
		stageID, kind, raw, int(rp.Wait().Seconds()), userID)
	return err
}

// SetRobotActive turns a robot of the project on or off.
func (s *Store) SetRobotActive(ctx context.Context, p Project, robotID string, active bool) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	_, err := s.db.Exec(ctx, `UPDATE task_robots SET active = $3 WHERE id::text = $1
		AND stage_id IN (SELECT id FROM project_stages WHERE project_id = $2)`, robotID, p.ID, active)
	return err
}

// DeleteRobot removes a robot of the project.
func (s *Store) DeleteRobot(ctx context.Context, p Project, robotID string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	_, err := s.db.Exec(ctx, `DELETE FROM task_robots WHERE id::text = $1
		AND stage_id IN (SELECT id FROM project_stages WHERE project_id = $2)`, robotID, p.ID)
	return err
}

// robotDepth counts the robots that moved a task in a row: the stage change they make queues the next stage's
// robots with the depth one more (inTx hands it to the database), and a ring of stage robots stops after a few.
type robotDepth struct{}

const maxRobotDepth = 5

// RunRobotsDue makes the moves of the triggers, then runs the queued robots whose wait is over, for the tasks still
// in the stage they were queued in.
func (s *Store) RunRobotsDue(ctx context.Context, now time.Time) (int, error) {
	moved, err := s.runTriggersDue(ctx, now)
	if err != nil {
		return moved, err
	}
	type job struct {
		robot, task, stage string
		depth              int
	}
	rows, err := s.db.Query(ctx, `DELETE FROM task_robot_jobs WHERE id IN (SELECT id FROM task_robot_jobs WHERE run_at <= greatest($1, now())
		ORDER BY run_at, id LIMIT 200 FOR UPDATE SKIP LOCKED) RETURNING robot_id::text, task_id::text, stage_id::text, depth`, now)
	if err != nil {
		return 0, err
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (job, error) {
		var j job
		return j, r.Scan(&j.robot, &j.task, &j.stage, &j.depth)
	})
	if err != nil {
		return 0, err
	}
	n := moved
	for _, j := range jobs {
		if j.depth >= maxRobotDepth {
			continue
		}
		var r Robot
		var raw []byte
		var creator string
		var here bool
		err := s.db.QueryRow(ctx, `SELECT r.id, r.stage_id::text, r.kind, r.params, r.active, r.created_by::text,
				coalesce(t.stage_id, (SELECT id FROM project_stages WHERE project_id = t.project_id AND code IS DISTINCT FROM 'finish'
					ORDER BY sort, created_at LIMIT 1))::text = $3
			FROM task_robots r, tasks t WHERE r.id::text = $1 AND t.id::text = $2`, j.robot, j.task, j.stage).
			Scan(&r.ID, &r.StageID, &r.Kind, &raw, &r.Active, &creator, &here)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return n, err
		}
		if !r.Active || !here || json.Unmarshal(raw, &r.Params) != nil {
			continue
		}
		s.runRobot(context.WithValue(ctx, robotDepth{}, j.depth), creator, r, j.task)
		n++
	}
	return n, nil
}

// runRobot does one robot's work on the task; a failure is written into the task's history.
func (s *Store) runRobot(ctx context.Context, actorID string, r Robot, taskID string) {
	ctx = context.WithValue(WithAdmin(ctx), robotKey{}, true) // with the rights of the project, for the person who set it up
	err := s.robotDo(ctx, actorID, r, taskID)
	if err != nil {
		key := err.Error()
		var ie *i18n.Error
		if errors.As(err, &ie) {
			key = ie.Key
		}
		_ = addHistory(ctx, s.db, taskID, actorID, "robot", "", "fail:"+r.Kind+":"+key)
	}
}

func (s *Store) robotDo(ctx context.Context, actorID string, r Robot, taskID string) error {
	t, err := s.GetCard(ctx, actorID, taskID)
	if err != nil {
		return err
	}
	p := r.Params
	to := p.To
	switch to {
	case "responsible", "":
		to = t.ResponsibleID
	case "creator":
		to = t.CreatorID
	}
	deadline := ""
	if t.Deadline != nil {
		deadline = t.Deadline.Local().Format("02.01.2006 15:04")
	}
	link := "/t/" + t.Key()
	text := strings.NewReplacer("{title}", t.Title, "{responsible}", t.ResponsibleName, "{deadline}", deadline,
		"{link}", link).Replace(p.Text)
	switch r.Kind {
	case "notify":
		_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, task_id, body) VALUES ($1, $2, 'task_robot', $3, $4)`,
			to, actorID, taskID, text)
		return err
	case "comment":
		if s.Comment != nil {
			return s.Comment(ctx, actorID, taskID, text)
		}
		_, err = s.PostComment(ctx, actorID, taskID, text)
		return err
	case "responsible":
		return s.SetResponsible(ctx, actorID, taskID, to)
	case "accomplice", "auditor":
		if to == t.ResponsibleID && r.Kind == "accomplice" {
			return nil
		}
		return s.SetParticipant(ctx, actorID, taskID, to, r.Kind, true)
	case "deadline":
		sched, err := s.Schedule(ctx)
		if err != nil {
			return err
		}
		d := sched.Deadline(time.Now().AddDate(0, 0, p.Days))
		return s.SetDeadline(ctx, actorID, taskID, &d)
	case "status":
		next, err := Transition(t.Status, p.Status, Roles{Accomplice: true}, t.RequiresControl)
		if err != nil || next == t.Status {
			return nil // nothing to do: the task is already there or cannot go there from its status
		}
		return s.ApplyAction(ctx, actorID, taskID, p.Status)
	case "stage":
		return s.MoveToStage(context.WithValue(ctx, robotDepth{}, depthOf(ctx)+1), actorID, taskID, p.Stage)
	case "checklist":
		return s.AddChecklistItem(ctx, actorID, taskID, text)
	case "task":
		proj, err := s.ProjectByKey(ctx, actorID, t.ProjectKey)
		if err != nil {
			return err
		}
		nt := NewTask{Title: text, Description: strings.ReplaceAll(p.Details, "{link}", link), ResponsibleID: to, Priority: 1}
		if p.Sub {
			nt.ParentKey = t.Key()
		}
		if p.Days > 0 {
			sched, err := s.Schedule(ctx)
			if err != nil {
				return err
			}
			d := sched.Deadline(time.Now().AddDate(0, 0, p.Days))
			nt.Deadline = &d
		}
		_, err = s.CreateTask(context.WithValue(ctx, robotDepth{}, depthOf(ctx)+1), actorID, proj, nt)
		return err
	}
	return nil
}

func depthOf(ctx context.Context) int { d, _ := ctx.Value(robotDepth{}).(int); return d }

// robotKey marks the work of a robot or a trigger: it may start, complete and move the task as a participant.
type robotKey struct{}

func isRobot(ctx context.Context) bool { v, _ := ctx.Value(robotKey{}).(bool); return v }

// TriggerKinds in the order the settings offer them: what moves a task to the trigger's stage.
var TriggerKinds = []string{"status", "comment", "responsible", "deadline", "checklist", "overdue"}

// TriggerStatuses are the statuses a status trigger may wait for (a finished task goes to the finish stage anyway).
var TriggerStatuses = []Status{StatusInProgress, StatusDeferred, StatusPending}

// Trigger of a stage.
type Trigger struct {
	ID, StageID, Kind string
	Status            Status // a status trigger: the status the task came to
}

// Triggers are the triggers of a project's stages.
func (s *Store) Triggers(ctx context.Context, projectID string) ([]Trigger, error) {
	rows, err := s.db.Query(ctx, `SELECT g.id, g.stage_id::text, g.kind, g.status FROM task_triggers g
		JOIN project_stages st ON st.id = g.stage_id WHERE st.project_id = $1 ORDER BY g.created_at`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Trigger])
}

// AddTrigger sets a trigger on a stage of the project (its owner or a manager).
func (s *Store) AddTrigger(ctx context.Context, userID string, p Project, stageID, kind string, status Status) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	if _, err := s.stageByID(ctx, s.db, p.ID, stageID); err != nil {
		return err
	}
	if !slices.Contains(TriggerKinds, kind) || (kind == "status" && !slices.Contains(TriggerStatuses, status)) {
		return invalid("err.trigger")
	}
	if kind != "status" {
		status = ""
	}
	_, err := s.db.Exec(ctx, `INSERT INTO task_triggers (stage_id, kind, status, created_by) VALUES ($1, $2, $3, $4)`,
		stageID, kind, status, userID)
	return err
}

// DeleteTrigger removes a trigger of the project.
func (s *Store) DeleteTrigger(ctx context.Context, p Project, triggerID string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	_, err := s.db.Exec(ctx, `DELETE FROM task_triggers WHERE id::text = $1
		AND stage_id IN (SELECT id FROM project_stages WHERE project_id = $2)`, triggerID, p.ID)
	return err
}

// runTriggersDue moves the tasks whose events the database has queued (and the overdue ones) to the triggers' stages.
func (s *Store) runTriggersDue(ctx context.Context, now time.Time) (int, error) {
	// «Срок истёк» is no event of the database: the overdue tasks are found here, once per deadline
	if _, err := s.db.Exec(ctx, `WITH due AS (
			INSERT INTO task_trigger_fired (trigger_id, task_id)
			SELECT g.id, t.id FROM task_triggers g JOIN project_stages st ON st.id = g.stage_id JOIN tasks t ON t.project_id = st.project_id
			WHERE g.kind = 'overdue' AND t.deadline < $1 AND t.status IN ('pending', 'in_progress')
			ON CONFLICT DO NOTHING RETURNING trigger_id, task_id)
		INSERT INTO task_trigger_jobs (trigger_id, task_id) SELECT trigger_id, task_id FROM due`, now); err != nil {
		return 0, err
	}
	type job struct {
		trigger, task, stage, creator string
		depth                         int
	}
	rows, err := s.db.Query(ctx, `WITH done AS (DELETE FROM task_trigger_jobs WHERE id IN (SELECT id FROM task_trigger_jobs
			ORDER BY id LIMIT 200 FOR UPDATE SKIP LOCKED) RETURNING id, trigger_id, task_id, depth)
		SELECT d.trigger_id::text, d.task_id::text, g.stage_id::text, g.created_by::text, d.depth
		FROM done d JOIN task_triggers g ON g.id = d.trigger_id ORDER BY d.id`)
	if err != nil {
		return 0, err
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (job, error) {
		var j job
		return j, r.Scan(&j.trigger, &j.task, &j.stage, &j.creator, &j.depth)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range jobs {
		if j.depth >= maxRobotDepth {
			continue
		}
		ctx := context.WithValue(context.WithValue(WithAdmin(ctx), robotKey{}, true), robotDepth{}, j.depth+1)
		err := s.MoveToStage(ctx, j.creator, j.task, j.stage)
		switch {
		case errors.Is(err, ErrNotFound): // the task is gone
		case err != nil:
			key := err.Error()
			var ie *i18n.Error
			if errors.As(err, &ie) {
				key = ie.Key
			}
			_ = addHistory(ctx, s.db, j.task, j.creator, "robot", "", "fail:trigger:"+key)
		default:
			n++
		}
	}
	return n, nil
}
