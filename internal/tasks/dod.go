package tasks

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Scrum team and Definition of Done scrum: the product owner (by default the project's owner) and
// the scrum master run the sprints with the moderators; every task of a scrum project has a type («Задача»,
// «Ошибка» and the project's own), each type its DoD checklist, ticked in the task before it is completed — a
// type may make that compulsory for the tasks of a sprint.

type ScrumTeam struct {
	Owner  *User // «Владелец продукта»
	Master *User // «Скрам-мастер»
	Team   []Member
}

// ScrumTeamOf returns the team of a scrum project: its product owner, scrum master and members.
func (s *Store) ScrumTeamOf(ctx context.Context, p Project) (ScrumTeam, error) {
	var t ScrumTeam
	var owner, master string
	if err := s.db.QueryRow(ctx, `SELECT coalesce(p.scrum_owner_id, (SELECT o.user_id FROM project_members o
			WHERE o.project_id = p.id AND o.role = 'owner' LIMIT 1))::text, coalesce(p.scrum_master_id::text, '')
		FROM projects p WHERE p.id = $1`, p.ID).Scan(&owner, &master); err != nil {
		return t, err
	}
	members, err := s.Members(ctx, p.ID)
	if err != nil {
		return t, err
	}
	for i, m := range members {
		switch m.ID {
		case owner:
			t.Owner = &members[i].User
		case master:
			t.Master = &members[i].User
		}
		if m.Role != "viewer" {
			t.Team = append(t.Team, m)
		}
	}
	return t, nil
}

// SetScrumTeam names the product owner and the scrum master ("" — none; the product owner then is the project's
// owner). Both are members of the project; the moderators change them.
func (s *Store) SetScrumTeam(ctx context.Context, p Project, ownerID, masterID string) error {
	if !p.Scrum() || !p.CanManage() || p.Archived {
		return ErrForbidden
	}
	ids := []any{nil, nil}
	for i, id := range []string{ownerID, masterID} {
		if id == "" {
			continue
		}
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members
			WHERE project_id = $1 AND user_id::text = $2 AND role IN ('owner', 'manager', 'member'))`, p.ID, id).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return invalid("err.scrum_not_member")
		}
		ids[i] = id
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET scrum_owner_id = $2, scrum_master_id = $3, updated_at = now() WHERE id = $1`,
		p.ID, ids[0], ids[1])
	return err
}

type DodItem struct {
	ID    string
	Title string
	Done  bool // ticked in the task
}

type ScrumType struct {
	ID       string
	Name     string
	Required bool // the tasks of a sprint are completed only with all the items ticked
	Items    []DodItem
}

// ScrumTypes lists the task types of a project with their DoD items, in order; the first is the default type.
func (s *Store) ScrumTypes(ctx context.Context, projectID string) ([]ScrumType, error) {
	rows, err := s.db.Query(ctx, `SELECT st.id, st.name, st.dod_required, coalesce(i.id::text, ''), coalesce(i.title, '')
		FROM scrum_types st LEFT JOIN scrum_dod_items i ON i.type_id = st.id
		WHERE st.project_id = $1 ORDER BY st.sort, st.created_at, i.sort`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScrumType
	for rows.Next() {
		var t ScrumType
		var item DodItem
		if err := rows.Scan(&t.ID, &t.Name, &t.Required, &item.ID, &item.Title); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != t.ID {
			out = append(out, t)
		}
		if item.ID != "" {
			out[len(out)-1].Items = append(out[len(out)-1].Items, item)
		}
	}
	return out, rows.Err()
}

// EnsureScrumTypes gives a scrum project without types the default ones (the names come translated).
func (s *Store) EnsureScrumTypes(ctx context.Context, p Project, names ...string) error {
	if !p.Scrum() {
		return nil
	}
	_, err := s.db.Exec(ctx, `INSERT INTO scrum_types (project_id, name, sort)
		SELECT $1, n, o * 10 FROM unnest($2::text[]) WITH ORDINALITY AS x(n, o)
		WHERE NOT EXISTS (SELECT 1 FROM scrum_types WHERE project_id = $1)`, p.ID, names)
	return err
}

// SaveScrumType creates a type (id "") or changes one: its name, whether DoD is compulsory and the checklist, an
// item a line. Items kept by their text stay ticked in the tasks.
func (s *Store) SaveScrumType(ctx context.Context, p Project, id, name string, required bool, items []string) error {
	if !p.Scrum() || !p.CanLead() {
		return ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return invalid("err.scrum_type_name")
	}
	var clean []string
	for _, it := range items {
		if it = strings.TrimSpace(it); it != "" {
			if len([]rune(it)) > 500 {
				return invalid("err.dod_item")
			}
			clean = append(clean, it)
		}
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if id == "" {
			if err := tx.QueryRow(ctx, `INSERT INTO scrum_types (project_id, name, dod_required, sort)
				VALUES ($1, $2, $3, (SELECT coalesce(max(sort), 0) + 10 FROM scrum_types WHERE project_id = $1)) RETURNING id`,
				p.ID, name, required).Scan(&id); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE scrum_types SET name = $3, dod_required = $4 WHERE id::text = $1 AND project_id = $2`,
				id, p.ID, name, required)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrNotFound
			}
		}
		rows, err := tx.Query(ctx, `SELECT id, title, false FROM scrum_dod_items WHERE type_id = $1`, id)
		if err != nil {
			return err
		}
		old, err := pgx.CollectRows(rows, pgx.RowToStructByPos[DodItem])
		if err != nil {
			return err
		}
		used := map[string]bool{}
		for i, title := range clean {
			found := ""
			for _, o := range old {
				if o.Title == title && !used[o.ID] {
					found = o.ID
					break
				}
			}
			if found != "" {
				used[found] = true
				_, err = tx.Exec(ctx, `UPDATE scrum_dod_items SET sort = $2 WHERE id = $1`, found, i)
			} else {
				_, err = tx.Exec(ctx, `INSERT INTO scrum_dod_items (type_id, title, sort) VALUES ($1, $2, $3)`, id, title, i)
			}
			if err != nil {
				return err
			}
		}
		for _, o := range old {
			if !used[o.ID] {
				if _, err := tx.Exec(ctx, `DELETE FROM scrum_dod_items WHERE id = $1`, o.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// DeleteScrumType removes a type; its tasks take the default one. The last type stays.
func (s *Store) DeleteScrumType(ctx context.Context, p Project, id string) error {
	if !p.Scrum() || !p.CanLead() {
		return ErrForbidden
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM scrum_types WHERE project_id = $1`, p.ID).Scan(&n); err != nil {
		return err
	}
	if n <= 1 {
		return invalid("err.scrum_type_last")
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM scrum_types WHERE id::text = $1 AND project_id = $2`, id, p.ID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// typeOfSQL is the type of task t: its own when it belongs to the task's project, otherwise the project's first.
const typeOfSQL = `(SELECT st.id FROM scrum_types st WHERE st.project_id = t.project_id
	ORDER BY st.id = t.scrum_type_id DESC NULLS LAST, st.sort, st.created_at LIMIT 1)`

// TaskDod is the Definition of Done block of a task card.
type TaskDod struct {
	Types    []ScrumType
	Type     ScrumType // the task's, items with Done
	InSprint bool
	Done     bool // every item is ticked
	CanEdit  bool
}

// Blocks: the task cannot be completed until its DoD is ticked.
func (d *TaskDod) Blocks() bool { return d != nil && d.InSprint && d.Type.Required && !d.Done }

// TaskDodOf returns the DoD of a task of a scrum project (nil for other tasks and projects without types).
func (s *Store) TaskDodOf(ctx context.Context, userID, taskID string) (*TaskDod, error) {
	var projectID, typeID string
	var inSprint bool
	err := s.db.QueryRow(ctx, `SELECT t.project_id::text, coalesce(`+typeOfSQL+`::text, ''), t.sprint_id IS NOT NULL
		FROM tasks t JOIN projects p ON p.id = t.project_id AND p.kind = 'scrum' WHERE t.id = $1`, taskID).Scan(&projectID, &typeID, &inSprint)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && typeID == "") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d := &TaskDod{InSprint: inSprint, Done: true}
	if d.Types, err = s.ScrumTypes(ctx, projectID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT item_id::text FROM task_dod WHERE task_id = $1`, taskID)
	if err != nil {
		return nil, err
	}
	ticked, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, t := range d.Types {
		if t.ID != typeID {
			continue
		}
		d.Type = t
		d.Type.Items = append([]DodItem(nil), t.Items...)
		for i := range d.Type.Items {
			for _, id := range ticked {
				d.Type.Items[i].Done = d.Type.Items[i].Done || id == d.Type.Items[i].ID
			}
			d.Done = d.Done && d.Type.Items[i].Done
		}
	}
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return nil, err
	}
	d.CanEdit = a.Roles.Worker() || a.Roles.Manager || a.MemberRole == "member"
	return d, nil
}

// SetTaskDod sets the type of a task ("" keeps it) and the DoD items ticked in it (of its type; the rest are
// unticked).
func (s *Store) SetTaskDod(ctx context.Context, userID, taskID, typeID string, checked []string) error {
	if checked == nil {
		checked = []string{}
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Worker() || a.Roles.Manager || a.MemberRole == "member") {
			return ErrForbidden
		}
		var oldType, oldName string
		if err := tx.QueryRow(ctx, `SELECT coalesce(`+typeOfSQL+`::text, ''), coalesce((SELECT name FROM scrum_types WHERE id = `+typeOfSQL+`), '')
			FROM tasks t JOIN projects p ON p.id = t.project_id AND p.kind = 'scrum' WHERE t.id = $1`, taskID).Scan(&oldType, &oldName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("err.not_scrum")
			}
			return err
		}
		if typeID != "" && typeID != oldType {
			var name string
			if err := tx.QueryRow(ctx, `SELECT name FROM scrum_types WHERE id::text = $1 AND project_id = $2`, typeID, a.ProjectID).Scan(&name); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE tasks SET scrum_type_id = $2, updated_at = now() WHERE id = $1`, taskID, typeID); err != nil {
				return err
			}
			if err := addHistory(ctx, tx, taskID, userID, "scrum_type", oldName, name); err != nil {
				return err
			}
			oldType = typeID
		}
		if _, err := tx.Exec(ctx, `DELETE FROM task_dod WHERE task_id = $1 AND NOT (item_id::text = ANY($2))`, taskID, checked); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO task_dod (task_id, item_id, checked_by)
			SELECT $1, i.id, $3 FROM scrum_dod_items i WHERE i.type_id = $2 AND i.id::text = ANY($4)
			ON CONFLICT DO NOTHING`, taskID, oldType, userID, checked)
		return err
	})
}

// dodDone: a task of a sprint whose type makes DoD compulsory has all its items ticked (other tasks always pass).
func dodDone(ctx context.Context, q querier, taskID string) (bool, error) {
	var missing bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tasks t
		JOIN projects p ON p.id = t.project_id AND p.kind = 'scrum'
		JOIN scrum_types st ON st.id = `+typeOfSQL+` AND st.dod_required
		JOIN scrum_dod_items i ON i.type_id = st.id
		WHERE t.id = $1 AND t.sprint_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM task_dod d WHERE d.task_id = t.id AND d.item_id = i.id))`, taskID).Scan(&missing)
	return !missing, err
}
