package tasks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Flows (Потоки) tasks.flow: a team's queue of tasks in a project. Whoever may put tasks into
// the flow writes only the title and the text (the form of the flow's template); the flow sets the deadline from its
// planned time and picks the responsible by its distribution (manually — the moderator; queue — the team in turn,
// skipping people away today; himself — the task waits with its creator until someone of the team takes it).
// The flow's administrator, moderator and team see its tasks. The administrator (or admin mode) edits the flow;
// a flow is made by those who manage its project.

// Distributions.
const (
	FlowManually = "manually"
	FlowQueue    = "queue"
	FlowHimself  = "himself"
)

// FlowStatsDays: done tasks, average times and the efficiency of a flow are counted over the last 30 days.
const FlowStatsDays = 30

type Flow struct {
	ID, Name, Description                          string
	ProjectID, ProjectKey, ProjectName             string
	OwnerID, OwnerName, ModeratorID, ModeratorName string
	Distribution                                   string
	PlannedMinutes                                 int
	TemplateID                                     string
	CreatorsAll, MatchWorkTime                     bool
	AllowChangeDeadline, RequiresControl, Active   bool
	Members, Creators                              []User
	Stats                                          FlowStats
	CanManage, CanCreate                           bool // the viewer edits the flow / puts tasks into it
}

// FlowStats: the tasks waiting (not started), in work and done (the last 30 days); the average minutes from
// creation to start and from start to completion; the efficiency (tasks in work without being overdue).
type FlowStats struct {
	Queue, InWork, Done int
	AvgQueue, AvgWork   int // minutes, 0 = no data
	Efficiency          int
}

// Moderator: who gets the tasks of a manual flow.
func (f Flow) Moderator() string {
	if f.ModeratorID != "" {
		return f.ModeratorID
	}
	return f.OwnerID
}

// Member reports whether the user is in the flow's team.
func (f Flow) Member(userID string) bool {
	return slices.ContainsFunc(f.Members, func(u User) bool { return u.ID == userID })
}

// flowPeopleSQL: the user u is the administrator, moderator or in the team of the flow (a SQL expression of its id).
func flowPeopleSQL(flow, u string) string {
	return `EXISTS (SELECT 1 FROM task_flows vf WHERE vf.id = ` + flow + ` AND (vf.owner_id = ` + u + ` OR vf.moderator_id = ` + u + `
		OR EXISTS (SELECT 1 FROM task_flow_members vfm WHERE vfm.flow_id = vf.id AND vfm.user_id = ` + u + `)))`
}

const flowSelect = `SELECT f.id::text, f.name, f.description, f.project_id::text, p.key, p.name, f.owner_id::text, ou.name,
	coalesce(f.moderator_id::text, ''), coalesce(mu.name, ''), f.distribution, f.planned_minutes, coalesce(f.template_id::text, ''),
	f.creators_all, f.match_work_time, f.allow_change_deadline, f.requires_control, f.active
	FROM task_flows f JOIN projects p ON p.id = f.project_id JOIN users ou ON ou.id = f.owner_id
	LEFT JOIN users mu ON mu.id = f.moderator_id`

func scanFlow(row pgx.Row) (Flow, error) {
	var f Flow
	err := row.Scan(&f.ID, &f.Name, &f.Description, &f.ProjectID, &f.ProjectKey, &f.ProjectName, &f.OwnerID, &f.OwnerName,
		&f.ModeratorID, &f.ModeratorName, &f.Distribution, &f.PlannedMinutes, &f.TemplateID,
		&f.CreatorsAll, &f.MatchWorkTime, &f.AllowChangeDeadline, &f.RequiresControl, &f.Active)
	return f, err
}

// fill loads the flow's people and what the viewer may do.
func (s *Store) fillFlow(ctx context.Context, f *Flow, userID string) error {
	for _, x := range []struct {
		table string
		into  *[]User
	}{{"task_flow_members", &f.Members}, {"task_flow_creators", &f.Creators}} {
		rows, err := s.db.Query(ctx, `SELECT u.id, u.name, u.email, u.guest FROM `+x.table+` m JOIN users u ON u.id = m.user_id
			WHERE m.flow_id = $1 ORDER BY u.name, u.id`, f.ID)
		if err != nil {
			return err
		}
		if *x.into, err = pgx.CollectRows(rows, pgx.RowToStructByPos[User]); err != nil {
			return err
		}
	}
	f.CanManage = f.OwnerID == userID || IsAdmin(ctx)
	f.CanCreate = f.Active && (f.CreatorsAll || f.CanManage || f.ModeratorID == userID || f.Member(userID) ||
		slices.ContainsFunc(f.Creators, func(u User) bool { return u.ID == userID }))
	return nil
}

// Flows lists the flows the user may see: own (administrator, moderator, team), those the user may put tasks into,
// all in admin mode.
func (s *Store) Flows(ctx context.Context, userID string) ([]Flow, error) {
	rows, err := s.db.Query(ctx, flowSelect+` WHERE $2 OR f.creators_all OR `+flowPeopleSQL("f.id", "$1")+`
		OR EXISTS (SELECT 1 FROM task_flow_creators c WHERE c.flow_id = f.id AND c.user_id = $1)
		ORDER BY f.active DESC, f.name`, userID, IsAdmin(ctx))
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Flow, error) { return scanFlow(row) })
	if err != nil {
		return nil, err
	}
	for i := range list {
		if err := s.fillFlow(ctx, &list[i], userID); err != nil {
			return nil, err
		}
		if list[i].Stats, err = s.flowStats(ctx, list[i].ID, time.Now()); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// Flow loads one flow for the user (any flow the list would show).
func (s *Store) Flow(ctx context.Context, userID, id string) (Flow, error) {
	f, err := scanFlow(s.db.QueryRow(ctx, flowSelect+` WHERE f.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	if err := s.fillFlow(ctx, &f, userID); err != nil {
		return f, err
	}
	if !f.CanManage && !f.CanCreate && !f.CreatorsAll && f.ModeratorID != userID && !f.Member(userID) {
		return f, ErrForbidden
	}
	f.Stats, err = s.flowStats(ctx, f.ID, time.Now())
	return f, err
}

func (s *Store) flowStats(ctx context.Context, flowID string, now time.Time) (FlowStats, error) {
	var st FlowStats
	since := now.AddDate(0, 0, -FlowStatsDays)
	err := s.db.QueryRow(ctx, `WITH ft AS (
			SELECT t.status, t.created_at, t.closed_at,
			       (SELECT min(h.created_at) FROM task_history h WHERE h.task_id = t.id AND h.field = 'status' AND h.new_value = 'in_progress') AS started
			FROM tasks t WHERE t.flow_id = $1)
		SELECT count(*) FILTER (WHERE status = 'pending'),
		       count(*) FILTER (WHERE status IN ('in_progress', 'awaiting_control', 'deferred')),
		       count(*) FILTER (WHERE status = 'completed' AND closed_at >= $2),
		       coalesce(avg(extract(epoch FROM started - created_at)) FILTER (WHERE started >= $2), 0)::int / 60,
		       coalesce(avg(extract(epoch FROM closed_at - started)) FILTER (WHERE status = 'completed' AND closed_at >= $2 AND started IS NOT NULL), 0)::int / 60
		FROM ft`, flowID, since).Scan(&st.Queue, &st.InWork, &st.Done, &st.AvgQueue, &st.AvgWork)
	if err != nil {
		return st, err
	}
	rows, err := s.db.Query(ctx, `SELECT t.id::text, '', '', t.created_at, t.deadline, t.closed_at FROM tasks t
		WHERE t.flow_id = $1 AND t.created_at < $3 AND (t.closed_at IS NULL OR t.closed_at >= $2)`, flowID, since, now)
	if err != nil {
		return st, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (effTask, error) {
		var t effTask
		err := row.Scan(&t.ID, &t.Key, &t.Title, &t.Created, &t.Deadline, &t.ClosedAt)
		return t, err
	})
	if err != nil {
		return st, err
	}
	total, violated, _ := score(list, since, now)
	st.Efficiency = percent(total, violated)
	return st, nil
}

// FlowInput is the flow's form.
type FlowInput struct {
	ID, Name, Description, ProjectKey  string
	OwnerID, ModeratorID, Distribution string
	PlannedMinutes                     int
	TemplateID                         string
	CreatorsAll, MatchWorkTime         bool
	AllowChangeDeadline                bool
	RequiresControl, Active            bool
	Members, Creators                  []string
}

// SaveFlow makes a flow (those who manage its project, or admin mode) or changes one (its administrator, or admin
// mode) and returns its id.
func (s *Store) SaveFlow(ctx context.Context, actorID string, in FlowInput) (string, error) {
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if in.Name == "" {
		return "", invalid("err.flow_name")
	}
	if in.PlannedMinutes <= 0 {
		return "", invalid("err.flow_planned")
	}
	members := compact(in.Members)
	switch in.Distribution {
	case FlowManually:
	case FlowQueue, FlowHimself:
		if len(members) == 0 {
			return "", invalid("err.flow_team")
		}
	default:
		return "", invalid("err.bad_form")
	}
	if in.OwnerID == "" {
		in.OwnerID = actorID
	}
	p, err := s.ProjectByKey(ctx, actorID, in.ProjectKey)
	if err != nil {
		return "", err
	}
	if in.ID == "" && !p.CanManage() {
		return "", ErrForbidden
	}
	if in.TemplateID != "" { // the template is the owner's
		if _, err := s.Template(ctx, in.OwnerID, in.TemplateID); err != nil {
			return "", invalid("err.flow_template")
		}
	}
	id := in.ID
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if id != "" {
			var owner string
			err := tx.QueryRow(ctx, `SELECT owner_id::text FROM task_flows WHERE id::text = $1 FOR UPDATE`, id).Scan(&owner)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if owner != actorID && !IsAdmin(ctx) {
				return ErrForbidden
			}
		}
		args := []any{in.Name, in.Description, p.ID, in.OwnerID, in.ModeratorID, in.Distribution, in.PlannedMinutes, in.TemplateID,
			in.CreatorsAll, in.MatchWorkTime, in.AllowChangeDeadline, in.RequiresControl, in.Active}
		var err error
		if id == "" {
			err = tx.QueryRow(ctx, `INSERT INTO task_flows (name, description, project_id, owner_id, moderator_id, distribution,
				planned_minutes, template_id, creators_all, match_work_time, allow_change_deadline, requires_control, active, created_by)
				VALUES ($1, $2, $3, $4, nullif($5, '')::uuid, $6, $7, nullif($8, '')::uuid, $9, $10, $11, $12, $13, $14)
				RETURNING id::text`, append(args, actorID)...).Scan(&id)
		} else {
			_, err = tx.Exec(ctx, `UPDATE task_flows SET name = $1, description = $2, project_id = $3, owner_id = $4,
				moderator_id = nullif($5, '')::uuid, distribution = $6, planned_minutes = $7, template_id = nullif($8, '')::uuid,
				creators_all = $9, match_work_time = $10, allow_change_deadline = $11, requires_control = $12, active = $13
				WHERE id::text = $14`, append(args, id)...)
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return invalid("err.flow_name_taken", in.Name)
		}
		if err != nil {
			return err
		}
		for table, ids := range map[string][]string{"task_flow_members": members, "task_flow_creators": compact(in.Creators)} {
			if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE flow_id::text = $1`, id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO `+table+` (flow_id, user_id) SELECT $1::uuid, u.id FROM users u
				WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, ids); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

func compact(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// SetFlowActive turns the flow on or off (its administrator, or admin mode); a flow that is off takes no tasks.
func (s *Store) SetFlowActive(ctx context.Context, actorID, id string, on bool) error {
	return s.flowAdmin(ctx, actorID, id, `UPDATE task_flows SET active = $2 WHERE id::text = $1`, on)
}

// DeleteFlow removes the flow; its tasks stay in the project.
func (s *Store) DeleteFlow(ctx context.Context, actorID, id string) error {
	return s.flowAdmin(ctx, actorID, id, `DELETE FROM task_flows WHERE id::text = $1`)
}

func (s *Store) flowAdmin(ctx context.Context, actorID, id, sql string, args ...any) error {
	var owner string
	err := s.db.QueryRow(ctx, `SELECT owner_id::text FROM task_flows WHERE id::text = $1`, id).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != actorID && !IsAdmin(ctx) {
		return ErrForbidden
	}
	_, err = s.db.Exec(ctx, sql, append([]any{id}, args...)...)
	return err
}

// FlowTask is what the creator writes; the rest comes from the flow and its template.
type FlowTask struct {
	Title, Description string
	Checklist          []string
	HighPriority       bool
}

// CreateFlowTask puts a task into the flow and returns its key.
func (s *Store) CreateFlowTask(ctx context.Context, actorID, flowID string, in FlowTask) (string, error) {
	f, err := s.Flow(ctx, actorID, flowID)
	if err != nil {
		return "", err
	}
	if !f.CanCreate {
		return "", ErrForbidden
	}
	// the flow lets its creators set tasks in its project, members of it or not
	p, err := s.ProjectByKey(WithAdmin(ctx), actorID, f.ProjectKey)
	if err != nil {
		return "", err
	}
	now := time.Now()
	deadline := now.Add(time.Duration(f.PlannedMinutes) * time.Minute)
	nt := NewTask{Title: in.Title, Description: in.Description, Checklist: in.Checklist, Priority: 1, Deadline: &deadline,
		RequiresControl: f.RequiresControl, AllowChangeDeadline: f.AllowChangeDeadline, MatchWorkTime: f.MatchWorkTime, FlowID: f.ID}
	if f.TemplateID != "" {
		if t, err := s.Template(ctx, f.OwnerID, f.TemplateID); err == nil {
			nt.Accomplices, nt.Auditors = t.Accomplices, t.Auditors
			if strings.TrimSpace(nt.Description) == "" {
				nt.Description = t.Description
			}
			if len(compact(nt.Checklist)) == 0 {
				nt.Checklist = t.Checklist
			}
		}
	}
	if in.HighPriority {
		nt.Priority = 2
	}
	if nt.ResponsibleID, err = s.pickResponsible(ctx, f, actorID); err != nil {
		return "", err
	}
	key, err := s.CreateTask(ctx, actorID, p, nt)
	if err != nil || f.Distribution != FlowHimself {
		return key, err
	}
	// himself: the team hears of the task and takes it
	id, err := s.TaskIDByKey(ctx, key)
	if err != nil {
		return key, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, task_id, body)
		SELECT m.user_id, $2, 'flow_task', $3, $4 FROM task_flow_members m WHERE m.flow_id = $1 AND m.user_id <> $2`,
		f.ID, actorID, id, f.Name)
	return key, err
}

// pickResponsible chooses who gets the next task of the flow.
func (s *Store) pickResponsible(ctx context.Context, f Flow, actorID string) (string, error) {
	switch f.Distribution {
	case FlowHimself:
		return actorID, nil
	case FlowQueue:
	default:
		return f.Moderator(), nil
	}
	var who string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var last string
		if err := tx.QueryRow(ctx, `SELECT coalesce(queue_last::text, '') FROM task_flows WHERE id = $1 FOR UPDATE`, f.ID).Scan(&last); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT u.id::text FROM task_flow_members m JOIN users u ON u.id = m.user_id
			WHERE m.flow_id = $1 AND u.active AND NOT EXISTS (SELECT 1 FROM absences a WHERE a.user_id = u.id
			  AND current_date BETWEEN a.starts_on AND a.ends_on)
			ORDER BY u.name, u.id`, f.ID)
		if err != nil {
			return err
		}
		team, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(team) == 0 { // nobody at work: the administrator hands it on, as in a manual flow
			who = f.OwnerID
			return nil
		}
		who = team[0]
		if i := slices.Index(team, last); i >= 0 {
			who = team[(i+1)%len(team)]
		} else if last != "" { // the last one is away or gone: the next by the order of the full team
			rows, err := tx.Query(ctx, `SELECT u.id::text FROM task_flow_members m JOIN users u ON u.id = m.user_id
				WHERE m.flow_id = $1 ORDER BY u.name, u.id`, f.ID)
			if err != nil {
				return err
			}
			all, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			if i := slices.Index(all, last); i >= 0 {
				for k := 1; k <= len(all); k++ {
					if c := all[(i+k)%len(all)]; slices.Contains(team, c) {
						who = c
						break
					}
				}
			}
		}
		_, err = tx.Exec(ctx, `UPDATE task_flows SET queue_last = $2 WHERE id = $1`, f.ID, who)
		return err
	})
	return who, err
}

// CanTake: a task of a «himself» flow still waiting in it, and the user is in its team.
func (s *Store) CanTake(ctx context.Context, userID string, t Task) bool {
	if t.FlowID == "" || t.Status != StatusPending || t.ResponsibleID == userID {
		return false
	}
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_flows f JOIN task_flow_members m ON m.flow_id = f.id
		WHERE f.id = $1 AND f.distribution = 'himself' AND m.user_id = $2) AND
		(SELECT responsible_id = creator_id FROM tasks WHERE id = $3)`, t.FlowID, userID, t.ID).Scan(&ok)
	return ok
}

// TakeFlowTask: someone of the team takes a waiting task of a «himself» flow — they become its responsible and start it.
func (s *Store) TakeFlowTask(ctx context.Context, userID, taskID string) error {
	t, err := s.TaskByID(ctx, taskID)
	if err != nil {
		return err
	}
	if !s.CanTake(ctx, userID, t) {
		return invalid("err.flow_take")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE tasks SET responsible_id = $2, version = version + 1, updated_at = now()
			WHERE id = $1 AND status = 'pending' AND responsible_id = creator_id`, taskID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 { // taken by someone else a moment ago
			return invalid("err.flow_take")
		}
		if err := addHistory(ctx, tx, taskID, userID, "responsible", userName(ctx, tx, t.ResponsibleID), userName(ctx, tx, userID)); err != nil {
			return err
		}
		return setStatus(ctx, tx, taskID, userID, StatusPending, StatusInProgress)
	})
}
