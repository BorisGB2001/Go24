package tasks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Invitations to a project: the invited person becomes a member after accepting it (the portal groups). Who may
// invite is the project's setting: the owner, managers (default) or every member.

var invitePerms = map[string]bool{"owner": true, "managers": true, "members": true}

// Invite is an invitation waiting for an answer.
type Invite struct {
	ProjectKey, ProjectName string
	UserID, UserName        string
	Role                    string
	InvitedBy               string // name
	CreatedAt               time.Time
}

// CanInvite: the project's «кто может приглашать».
func (s *Store) CanInvite(ctx context.Context, p Project) (bool, error) {
	if p.Archived {
		return false, nil
	}
	var perm string
	if err := s.db.QueryRow(ctx, `SELECT invite_perm FROM projects WHERE id = $1`, p.ID).Scan(&perm); err != nil {
		return false, err
	}
	switch perm {
	case "owner":
		return p.MyRole == "owner" || p.MyRole == "admin", nil
	case "members":
		return p.CanWork(), nil
	}
	return p.CanManage(), nil
}

// InvitePerm is the project's setting.
func (s *Store) InvitePerm(ctx context.Context, p Project) (string, error) {
	var perm string
	err := s.db.QueryRow(ctx, `SELECT invite_perm FROM projects WHERE id = $1`, p.ID).Scan(&perm)
	return perm, err
}

// SetInvitePerm changes who may invite (owners and managers).
func (s *Store) SetInvitePerm(ctx context.Context, p Project, perm string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	if !invitePerms[perm] {
		return invalid("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET invite_perm = $2, updated_at = now() WHERE id = $1`, p.ID, perm)
	return err
}

// InviteMember invites a person with a role; the bell tells them. Only managers may invite managers.
func (s *Store) InviteMember(ctx context.Context, p Project, actorID, userID, role string) error {
	ok, err := s.CanInvite(ctx, p)
	if err != nil {
		return err
	}
	if !ok || (role == "manager" && !p.CanManage()) {
		return ErrForbidden
	}
	if role != "manager" && role != "member" && role != "viewer" {
		return invalid("err.unknown_role")
	}
	if err := s.checkMemberRole(ctx, p, userID, role); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var member bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)`,
			p.ID, userID).Scan(&member); err != nil {
			return err
		}
		if member {
			return invalid("err.already_member")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_invites (project_id, user_id, role, invited_by) VALUES ($1, $2, $3, $4)
			ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`, p.ID, userID, role, actorID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, project_id, body)
			SELECT $1::uuid, $2::uuid, 'project_invite', $3, $4 WHERE $1::uuid <> $2::uuid AND NOT go24_quiet()`, userID, actorID, p.ID, role)
		return err
	})
}

// CancelInvite withdraws an invitation (those who may invite).
func (s *Store) CancelInvite(ctx context.Context, p Project, userID string) error {
	ok, err := s.CanInvite(ctx, p)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	_, err = s.db.Exec(ctx, `DELETE FROM project_invites WHERE project_id = $1 AND user_id = $2`, p.ID, userID)
	return err
}

// AnswerInvite: the invited person accepts (becomes a member with the role) or declines.
func (s *Store) AnswerInvite(ctx context.Context, userID, projectKey string, accept bool) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var projectID, role string
		err := tx.QueryRow(ctx, `DELETE FROM project_invites i USING projects p
			WHERE p.id = i.project_id AND p.key = upper($1) AND i.user_id = $2 RETURNING i.project_id, i.role`,
			projectKey, userID).Scan(&projectID, &role)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil || !accept {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			projectID, userID, role)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND project_id = $2 AND read_at IS NULL`,
			userID, projectID)
		return err
	})
}

const inviteSQL = `SELECT p.key, p.name, u.id, u.name, i.role, b.name, i.created_at
	FROM project_invites i JOIN projects p ON p.id = i.project_id JOIN users u ON u.id = i.user_id JOIN users b ON b.id = i.invited_by`

func collectInvites(rows pgx.Rows, err error) ([]Invite, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invite, error) {
		var i Invite
		err := r.Scan(&i.ProjectKey, &i.ProjectName, &i.UserID, &i.UserName, &i.Role, &i.InvitedBy, &i.CreatedAt)
		return i, err
	})
}

// MyInvites are the invitations waiting for the person's answer.
func (s *Store) MyInvites(ctx context.Context, userID string) ([]Invite, error) {
	return collectInvites(s.db.Query(ctx, inviteSQL+` WHERE i.user_id = $1 AND NOT p.archived ORDER BY i.created_at DESC`, userID))
}

// ProjectInvites are the project's invitations nobody has answered yet.
func (s *Store) ProjectInvites(ctx context.Context, projectID string) ([]Invite, error) {
	return collectInvites(s.db.Query(ctx, inviteSQL+` WHERE i.project_id = $1 ORDER BY u.name`, projectID))
}
