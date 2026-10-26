package tasks

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// «Коллабы»: a space for work with people outside the company — its chat, tasks, drive and calendar.
// A collab is a project of kind "collab" (so its tasks, kanban, drive and calendar are those of a project) with the
// roles of a collab: the owner, moderators (project managers) and members; guests are members from outside
// (users.guest). Nobody else learns a collab exists: to an outsider its key is not found, its avatar is not served,
// its tasks are not visible (members see every task, as in a group).

// Collab is a collab in the list.
type Collab struct {
	Project
	Members, Guests int
	CollabCounters
}

// CollabCounters are the counters of a collab's card and of «Коллабы» in the menu: the person's
// tasks past the deadline (responsible or accomplice) and the tasks with comments they have not seen.
type CollabCounters struct {
	Overdue, Comments int
}

// collabCountersSQL counts CollabCounters of the project p for the user $1.
const collabCountersSQL = `
		(SELECT count(*) FROM tasks t WHERE t.project_id = p.id AND t.status <> 'completed' AND t.deadline < now()
		   AND (t.responsible_id = $1 OR EXISTS (SELECT 1 FROM task_participants tp
		        WHERE tp.task_id = t.id AND tp.user_id = $1 AND tp.role = 'accomplice')))::int,
		(SELECT count(DISTINCT n.task_id) FROM notifications n JOIN tasks t ON t.id = n.task_id
		 WHERE n.user_id = $1 AND n.read_at IS NULL AND n.kind IN ('comment', 'mention') AND t.project_id = p.id)::int`

// Collab reports whether the project is a collab.
func (p Project) Collab() bool { return p.Kind == "collab" }

// collabRoles are the roles one gives in a collab (the owner is the one who made it).
var collabRoles = map[string]bool{"manager": true, "member": true}

// CreateCollab makes a collab with the actor as its owner and the given people as members; it says the collab's key.
func (s *Store) CreateCollab(ctx context.Context, actorID, name, description string, members []string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("err.collab_name_required")
	}
	var key string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if may, err := mayCreateCollab(ctx, tx, actorID); err != nil {
			return err
		} else if !may { // a guest, or the company's rule (CollabCreators)
			return ErrForbidden
		}
		var id string
		for range 20 { // a key typed by someone for a project may be taken: the next number then
			var n int64
			if err := tx.QueryRow(ctx, `SELECT nextval('collab_key_seq')`).Scan(&n); err != nil {
				return err
			}
			key = "CL" + strconv.FormatInt(n, 10)
			_, err := tx.Exec(ctx, `SAVEPOINT k`)
			if err != nil {
				return err
			}
			err = tx.QueryRow(ctx, `INSERT INTO projects (key, name, description, owner_id, kind) VALUES ($1, $2, $3, $4, 'collab') RETURNING id`,
				key, name, strings.TrimSpace(description), actorID).Scan(&id)
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT k`); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			break
		}
		if id == "" {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, id, actorID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `INSERT INTO project_members (project_id, user_id, role)
			SELECT $1, u.id, 'member' FROM users u WHERE u.id::text = ANY($2) AND u.active AND u.id <> $3
			ON CONFLICT DO NOTHING RETURNING user_id::text`, id, members, actorID)
		if err != nil {
			return err
		}
		added, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if err := logCollab(ctx, tx, id, actorID, "created", "", name); err != nil {
			return err
		}
		for _, u := range added {
			if err := logCollab(ctx, tx, id, actorID, "member_added", u, "member"); err != nil {
				return err
			}
		}
		return nil
	})
	return key, err
}

// ListCollabs are the user's collabs (in admin mode — all of them, as with projects), newest activity first; archived
// — those in the archive; q, when given, — only those with it in the name or the description.
func (s *Store) ListCollabs(ctx context.Context, userID string, archived bool, q string) ([]Collab, error) {
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	return s.collabCards(ctx, `p.archived = $3 AND (p.name ILIKE $4 OR coalesce(p.description, '') ILIKE $4)`,
		userID, IsAdmin(ctx), archived, pattern)
}

// CollabCard is the card of one collab (by its id) the user is a member of (an administrator in admin mode — any):
// what the header of the collab's chat shows. ErrNotFound for another's collab or a project of another kind.
func (s *Store) CollabCard(ctx context.Context, userID, projectID string) (Collab, error) {
	list, err := s.collabCards(ctx, `p.id = $3::uuid`, userID, IsAdmin(ctx), projectID)
	if err != nil {
		return Collab{}, err
	}
	if len(list) == 0 {
		return Collab{}, ErrNotFound
	}
	return list[0], nil
}

// collabCards are the cards of the collabs matching where ($1 — the user, $2 — admin mode, $3 — where's own).
func (s *Store) collabCards(ctx context.Context, where string, args ...any) ([]Collab, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, p.key, p.name, p.description, coalesce(pm.role, 'admin'),
		       (SELECT count(*) FROM tasks t WHERE t.project_id = p.id AND t.status <> 'completed' AND `+visibleSQL("$1", "$2")+`)::int,
		       p.visibility, p.kind, p.photo_key, p.date_start, p.date_finish, p.archived,
		       (SELECT count(*) FROM project_members m WHERE m.project_id = p.id)::int,
		       (SELECT count(*) FROM project_members m JOIN users u ON u.id = m.user_id WHERE m.project_id = p.id AND u.guest)::int,
		       `+collabCountersSQL+`
		FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE p.kind = 'collab' AND `+where+` AND (pm.user_id IS NOT NULL OR $2)
		ORDER BY p.updated_at DESC, p.name`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Collab, error) {
		var c Collab
		p := &c.Project
		err := r.Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.MyRole, &p.OpenTasks, &p.Visibility, &p.Kind,
			&p.PhotoKey, &p.DateStart, &p.DateFinish, &p.Archived, &c.Members, &c.Guests, &c.Overdue, &c.Comments)
		return c, err
	})
}

// CollabNav is «Коллабы» of the menu: the counters of all the person's active collabs (those they are a member of) and
// the collabs, whose chats the page adds up live.
func (s *Store) CollabNav(ctx context.Context, userID string) (CollabCounters, []string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, `+collabCountersSQL+`
		FROM projects p JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE p.kind = 'collab' AND NOT p.archived`, userID)
	if err != nil {
		return CollabCounters{}, nil, err
	}
	defer rows.Close()
	var sum CollabCounters
	var ids []string
	for rows.Next() {
		var id string
		var c CollabCounters
		if err := rows.Scan(&id, &c.Overdue, &c.Comments); err != nil {
			return CollabCounters{}, nil, err
		}
		sum.Overdue += c.Overdue
		sum.Comments += c.Comments
		ids = append(ids, id)
	}
	return sum, ids, rows.Err()
}

// CollabByKey is ProjectByKey for a collab only (a project of another kind is not found).
func (s *Store) CollabByKey(ctx context.Context, userID, key string) (Project, error) {
	p, err := s.ProjectByKey(ctx, userID, key)
	if err == nil && !p.Collab() {
		return Project{}, ErrNotFound
	}
	return p, err
}

// LeaveProject: a member leaves (a collab, as «Покинуть»); the owner hands the collab over first.
func (s *Store) LeaveProject(ctx context.Context, p Project, userID string) error {
	if p.MyRole == "owner" {
		return invalid("err.owner_cannot_leave")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error { return removeMember(ctx, tx, p, userID, "left") })
}

// removeMember takes the person out of the project; out of a collab they take nothing with them: they leave its
// tasks (their open tasks go to the collab's owner), so no counter, deadline or reminder of it reaches them, and
// lose what they had there (see revokeAccess); the log says it (action: member_removed or left).
func removeMember(ctx context.Context, tx pgx.Tx, p Project, userID, action string) error {
	tag, err := tx.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2 AND role <> 'owner'`, p.ID, userID)
	if err != nil || tag.RowsAffected() == 0 || !p.Collab() {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_participants tp USING tasks t WHERE t.id = tp.task_id AND t.project_id = $1 AND tp.user_id = $2`,
		p.ID, userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE tasks t SET responsible_id = p.owner_id, version = version + 1, updated_at = now()
		FROM projects p WHERE p.id = t.project_id AND t.project_id = $1 AND t.responsible_id = $2`, p.ID, userID); err != nil {
		return err
	}
	if err := revokeAccess(ctx, tx, p, userID); err != nil {
		return err
	}
	actor := Viewer(ctx)
	if action == "left" {
		actor = userID
	} else if err := notifyCollab(ctx, tx, userID, actor, "collab_removed", p.ID, "/collabs"); err != nil {
		return err
	}
	return logCollab(ctx, tx, p.ID, actor, action, userID, "")
}

// checkMemberRole: a collab has moderators and members (no «наблюдатель»); a guest is only a member, and only of
// collabs — a guest never joins an ordinary project of the company.
func (s *Store) checkMemberRole(ctx context.Context, p Project, userID, role string) error {
	if p.Collab() && !collabRoles[role] {
		return invalid("err.unknown_role")
	}
	guest, err := s.IsGuest(ctx, userID)
	if err != nil {
		return err
	}
	if guest && !p.Collab() {
		return invalid("err.guest_collab_only")
	}
	if guest && role != "member" {
		return invalid("err.guest_member_only")
	}
	return nil
}

// collabStrangers: in a collab the people of a task (responsible, accomplices, auditors) are its members — a task
// shared with someone outside would show them the collab; and a guest is in tasks of their collabs only — a task of
// the company would show them the company.
func collabStrangers(ctx context.Context, q querier, projectID string, ids ...string) error {
	var strangers, guests bool
	err := q.QueryRow(ctx, `SELECT
		EXISTS (SELECT 1 FROM projects p, unnest($2::text[]) AS i(id)
			WHERE p.id = $1 AND p.kind = 'collab' AND i.id <> ''
			  AND NOT EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id::text = i.id)),
		EXISTS (SELECT 1 FROM projects p, users u
			WHERE p.id = $1 AND p.kind <> 'collab' AND u.id::text = ANY($2::text[]) AND u.guest)`,
		projectID, ids).Scan(&strangers, &guests)
	switch {
	case err != nil:
		return err
	case strangers:
		return invalid("err.collab_task_members")
	case guests:
		return invalid("err.guest_collab_only")
	}
	return nil
}

// IsGuest: the user is a guest from outside the company.
func (s *Store) IsGuest(ctx context.Context, userID string) (bool, error) {
	var g bool
	err := s.db.QueryRow(ctx, `SELECT guest FROM users WHERE id = $1`, userID).Scan(&g)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return g, err
}
