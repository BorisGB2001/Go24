package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// «Копировать» a group or project: a new group made after the source — its settings (type,
// visibility, subject, tools, rights to tasks), the kanban stages with their robots and triggers, a scrum project's
// epics and task types with their Definition of Done; on request its members with their roles and its tasks. The
// copy starts on the date given and the dates of the tasks move with it. The one who copies owns the copy (the
// source's owner becomes a moderator); copied tasks start anew — «Ждёт выполнения», no time, no comments, unticked
// checklists — with their subtasks, checklists, tags, fields and links. Robots do not run on the copied tasks.

// CopyOptions are the choices of the copy form.
type CopyOptions struct {
	Key, Name string
	Start     *time.Time // the copy's start date; nil — the dates stay as they are
	Members   bool       // the members with their roles, the departments
	Tasks     bool
	Closed    bool // the completed tasks too
}

// CopyKey suggests a free key for a copy of the project: its key with the first free number.
func (s *Store) CopyKey(ctx context.Context, key string) (string, error) {
	base := strings.TrimRight(key, "0123456789")
	if len(base) < 1 {
		base = key
	}
	for n := 2; n < 1000; n++ {
		suffix := fmt.Sprint(n)
		k := base
		if len(k)+len(suffix) > 10 {
			k = k[:10-len(suffix)]
		}
		k += suffix
		var taken bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE key = $1)`, k).Scan(&taken); err != nil {
			return "", err
		}
		if !taken && projectKeyRe.MatchString(k) {
			return k, nil
		}
	}
	return "", nil
}

// CopyProject makes the copy and returns its key: the source's owner or a moderator copies a group or project (not
// a collab).
func (s *Store) CopyProject(ctx context.Context, actorID string, p Project, o CopyOptions) (string, error) {
	if p.Collab() || !p.CanEdit() {
		return "", ErrForbidden
	}
	key := strings.ToUpper(strings.TrimSpace(o.Key))
	name := strings.TrimSpace(o.Name)
	if !projectKeyRe.MatchString(key) {
		return "", invalid("err.project_key")
	}
	if name == "" {
		return "", invalid("err.project_name_required")
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// the shift of the dates in days: from the source's start (or its earliest task date, or its creation)
		shift := 0
		if o.Start != nil {
			if err := tx.QueryRow(ctx, `SELECT $2::date - coalesce(p.date_start,
					(SELECT min(least(t.start_plan, t.deadline::date)) FROM tasks t WHERE t.project_id = p.id), p.created_at::date)
				FROM projects p WHERE p.id = $1`, p.ID, o.Start.Format(time.DateOnly)).Scan(&shift); err != nil {
				return err
			}
		}
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO projects (key, name, description, owner_id, visibility, kind,
				date_start, date_finish, invite_perm, chat_history, edit_perm, chat_perm, start_tool, is_open, privacy,
				subject_id, keywords, tools_off, tool_names, task_create, task_edit, task_delete,
				scrum_owner_id, scrum_master_id)
			SELECT $2, $3, description, $4, visibility, kind,
				date_start + $5::int, date_finish + $5::int, invite_perm, chat_history, edit_perm, chat_perm, start_tool, is_open, privacy,
				subject_id, keywords, tools_off, tool_names, task_create, task_edit, task_delete,
				CASE WHEN $6::bool THEN scrum_owner_id END, CASE WHEN $6::bool THEN scrum_master_id END
			FROM projects WHERE id = $1 RETURNING id`, p.ID, key, name, actorID, shift, o.Members).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return invalid("err.project_key_taken", key)
		}
		if err != nil {
			return err
		}
		args := pgx.NamedArgs{"src": p.ID, "dst": id, "actor": actorID, "shift": shift, "closed": o.Closed}
		for _, q := range copyProjectSQL(o) {
			if _, err := tx.Exec(ctx, q, args); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && s.Kick != nil {
		s.Kick()
	}
	return key, err
}

// copyProjectSQL are the steps of the copy; each takes @src the source, @dst the copy, @actor who copies, @shift the shift of
// the dates in days, @closed «the completed tasks too». The temporary tables map the source's rows to the copy's.
func copyProjectSQL(o CopyOptions) []string {
	q := []string{
		`INSERT INTO project_members (project_id, user_id, role) VALUES (@dst, @actor, 'owner')`,
		// the kanban stages, their robots (a robot's «move to the stage» goes to the copy's stage) and triggers
		`CREATE TEMP TABLE copy_stages ON COMMIT DROP AS
			SELECT id AS old, gen_random_uuid() AS new FROM project_stages WHERE project_id = @src`,
		`INSERT INTO project_stages (id, project_id, code, title, color, sort)
			SELECT m.new, @dst, s.code, s.title, s.color, s.sort FROM copy_stages m JOIN project_stages s ON s.id = m.old`,
		`INSERT INTO task_robots (stage_id, kind, params, delay_sec, active, created_by)
			SELECT m.new, r.kind, CASE WHEN r.params ? 'stage' THEN coalesce(jsonb_set(r.params, '{stage}',
				(SELECT to_jsonb(ms.new::text) FROM copy_stages ms WHERE ms.old::text = r.params->>'stage')), r.params - 'stage')
				ELSE r.params END, r.delay_sec, r.active, @actor
			FROM task_robots r JOIN copy_stages m ON m.old = r.stage_id ORDER BY r.created_at`,
		`INSERT INTO task_triggers (stage_id, kind, status, created_by)
			SELECT m.new, g.kind, g.status, @actor FROM task_triggers g JOIN copy_stages m ON m.old = g.stage_id
			ORDER BY g.created_at`,
		// a scrum project's epics, task types and their Definition of Done
		`CREATE TEMP TABLE copy_epics ON COMMIT DROP AS
			SELECT id AS old, gen_random_uuid() AS new FROM epics WHERE project_id = @src`,
		`INSERT INTO epics (id, project_id, name, description, color, created_by)
			SELECT m.new, @dst, e.name, e.description, e.color, @actor FROM copy_epics m JOIN epics e ON e.id = m.old
			ORDER BY e.created_at`,
		`CREATE TEMP TABLE copy_types ON COMMIT DROP AS
			SELECT id AS old, gen_random_uuid() AS new FROM scrum_types WHERE project_id = @src`,
		`INSERT INTO scrum_types (id, project_id, name, dod_required, sort)
			SELECT m.new, @dst, st.name, st.dod_required, st.sort FROM copy_types m JOIN scrum_types st ON st.id = m.old`,
		`INSERT INTO scrum_dod_items (type_id, title, sort)
			SELECT m.new, d.title, d.sort FROM scrum_dod_items d JOIN copy_types m ON m.old = d.type_id`,
	}
	if o.Members {
		q = append(q,
			`INSERT INTO project_members (project_id, user_id, role)
				SELECT @dst, pm.user_id, CASE WHEN pm.role = 'owner' THEN 'manager' ELSE pm.role END
				FROM project_members pm JOIN users u ON u.id = pm.user_id AND u.active
				WHERE pm.project_id = @src AND pm.user_id <> @actor ON CONFLICT DO NOTHING`,
			`INSERT INTO project_departments (project_id, department_id)
				SELECT @dst, department_id FROM project_departments WHERE project_id = @src`,
			`INSERT INTO project_department_people (project_id, user_id)
				SELECT @dst, user_id FROM project_department_people WHERE project_id = @src`)
	}
	if !o.Tasks {
		return q
	}
	return append(q,
		// the tasks keep their order and get the numbers from 1; a responsible who has left — the one who copies
		`CREATE TEMP TABLE copy_tasks ON COMMIT DROP AS
			SELECT id AS old, gen_random_uuid() AS new, row_number() OVER (ORDER BY number)::int AS num
			FROM tasks WHERE project_id = @src AND (@closed OR status <> 'completed')`,
		`UPDATE projects SET task_seq = (SELECT count(*) FROM copy_tasks) WHERE id = @dst`,
		`INSERT INTO tasks (id, project_id, number, title, description, priority, creator_id, responsible_id,
				requires_control, deadline, start_plan, end_plan, estimate_minutes, stage_id, stage_sort,
				allow_change_deadline, match_work_time, epic_id, story_points, backlog_sort,
				no_efficiency, dates_from_subtasks, auto_complete, result_required, scrum_type_id, no_time_tracking)
			SELECT m.new, @dst, m.num, t.title, t.description, t.priority, @actor,
				CASE WHEN u.active THEN t.responsible_id ELSE @actor END,
				t.requires_control, t.deadline + make_interval(days => @shift::int), t.start_plan + @shift::int, t.end_plan + @shift::int, t.estimate_minutes,
				(SELECT ms.new FROM copy_stages ms JOIN project_stages s ON s.id = ms.old
					WHERE ms.old = t.stage_id AND s.code IS DISTINCT FROM 'finish'), t.stage_sort,
				t.allow_change_deadline, t.match_work_time, (SELECT me.new FROM copy_epics me WHERE me.old = t.epic_id),
				t.story_points, t.backlog_sort,
				t.no_efficiency, t.dates_from_subtasks, t.auto_complete, t.result_required,
				(SELECT mt.new FROM copy_types mt WHERE mt.old = t.scrum_type_id), t.no_time_tracking
			FROM copy_tasks m JOIN tasks t ON t.id = m.old JOIN users u ON u.id = t.responsible_id ORDER BY m.num`,
		`UPDATE tasks n SET parent_id = mp.new FROM copy_tasks m JOIN tasks o ON o.id = m.old
			JOIN copy_tasks mp ON mp.old = o.parent_id WHERE n.id = m.new`,
		`INSERT INTO task_participants (task_id, user_id, role)
			SELECT m.new, tp.user_id, tp.role FROM copy_tasks m JOIN task_participants tp ON tp.task_id = m.old
			JOIN users u ON u.id = tp.user_id AND u.active ON CONFLICT DO NOTHING`,
		`INSERT INTO task_tags (task_id, tag) SELECT m.new, g.tag FROM copy_tasks m JOIN task_tags g ON g.task_id = m.old
			ON CONFLICT DO NOTHING`,
		`INSERT INTO task_field_values (task_id, field_id, value)
			SELECT m.new, v.field_id, v.value FROM copy_tasks m JOIN task_field_values v ON v.task_id = m.old`,
		`INSERT INTO task_links (from_task, to_task, type, created_by)
			SELECT mf.new, mt.new, l.type, @actor FROM task_links l
			JOIN copy_tasks mf ON mf.old = l.from_task JOIN copy_tasks mt ON mt.old = l.to_task`,
		// checklists unticked, with the nesting of the items and the people of an item
		`CREATE TEMP TABLE copy_lists ON COMMIT DROP AS
			SELECT c.id AS old, gen_random_uuid() AS new, m.new AS task FROM task_checklists c JOIN copy_tasks m ON m.old = c.task_id`,
		`INSERT INTO task_checklists (id, task_id, title, sort)
			SELECT ml.new, ml.task, c.title, c.sort FROM copy_lists ml JOIN task_checklists c ON c.id = ml.old`,
		`CREATE TEMP TABLE copy_items ON COMMIT DROP AS
			SELECT i.id AS old, gen_random_uuid() AS new FROM task_checklist_items i JOIN copy_lists ml ON ml.old = i.checklist_id`,
		`INSERT INTO task_checklist_items (id, task_id, checklist_id, title, sort, created_by, important, level)
			SELECT mi.new, ml.task, ml.new, i.title, i.sort, @actor, i.important, i.level
			FROM copy_items mi JOIN task_checklist_items i ON i.id = mi.old JOIN copy_lists ml ON ml.old = i.checklist_id`,
		`INSERT INTO task_checklist_item_members (item_id, user_id)
			SELECT mi.new, im.user_id FROM copy_items mi JOIN task_checklist_item_members im ON im.item_id = mi.old
			JOIN users u ON u.id = im.user_id AND u.active ON CONFLICT DO NOTHING`,
		`INSERT INTO task_history (task_id, actor_id, field, old_value, new_value)
			SELECT m.new, @actor, 'created', '', t.title FROM copy_tasks m JOIN tasks t ON t.id = m.new`,
		// a copy is no new piece of work: the robots the new tasks queued do not run
		`DELETE FROM task_robot_jobs WHERE task_id IN (SELECT new FROM copy_tasks)`)
}
