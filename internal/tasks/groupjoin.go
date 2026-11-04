package tasks

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Конфиденциальность» of a portal group: an open group is seen by every employee in «Все группы» and joined at
// once («Вступить»); a closed one is seen by everyone and joined on a request its owner or a moderator accepts; a
// secret one is seen by its members only. Guests join none of them; collabs have their own type.

var privacies = map[string]bool{"open": true, "closed": true, "secret": true}

// Group is a card of «Все группы».
type Group struct {
	Project
	Members   int
	Requested bool // the viewer has asked to join it
}

// Listed: the group is in «Все группы» of everyone (not only its members).
func (p Project) Listed() bool { return !p.Collab() && (p.Privacy == "open" || p.Privacy == "closed") }

// JoinRequest is a request to join a closed group, waiting for a moderator.
type JoinRequest struct {
	UserID, UserName string
	Message          string
	CreatedAt        time.Time
}

// SetPrivacy changes the group's type (its owner and moderators).
func (s *Store) SetPrivacy(ctx context.Context, p Project, privacy string) error {
	if !p.CanManage() || p.Collab() {
		return ErrForbidden
	}
	if !privacies[privacy] {
		return invalid("err.bad_form")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE projects SET privacy = $2, updated_at = now() WHERE id = $1`, p.ID, privacy); err != nil {
			return err
		}
		if privacy == "closed" {
			return nil
		}
		// an open group lets those who asked in, a secret one forgets them
		if privacy == "open" {
			if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role)
				SELECT project_id, user_id, 'member' FROM project_join_requests WHERE project_id = $1 ON CONFLICT DO NOTHING`, p.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM project_join_requests WHERE project_id = $1`, p.ID)
		return err
	})
}

// AllGroups is «Все группы»: the open and closed groups and projects of the company, and those the person is in.
// f.Q finds by the name, the key or a keyword; f.SubjectID and f.Keyword narrow to a subject and a keyword.
func (s *Store) AllGroups(ctx context.Context, userID string, f GroupFilter) ([]Group, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, p.key, p.name, p.description, coalesce(pm.role, ''), p.kind, p.photo_key, p.privacy, `+subjectSQL+`, p.tools_off, p.tool_names,
		       (SELECT count(*) FROM project_members m WHERE m.project_id = p.id)::int,
		       EXISTS (SELECT 1 FROM project_join_requests j WHERE j.project_id = p.id AND j.user_id = $1)
		FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE p.kind <> 'collab' AND NOT p.archived AND (pm.user_id IS NOT NULL OR $2 OR (p.privacy <> 'secret' AND NOT `+GuestSQL("$1")+`))
		  AND ($3 = '' OR p.name ILIKE '%' || $3 || '%' OR p.key ILIKE $3 || '%'
		       OR EXISTS (SELECT 1 FROM unnest(p.keywords) k WHERE k ILIKE '%' || $3 || '%'))
		  AND ($4 = '' OR p.subject_id::text = $4)
		  AND ($5 = '' OR EXISTS (SELECT 1 FROM unnest(p.keywords) k WHERE lower(k) = lower($5)))
		ORDER BY p.name`, userID, IsAdmin(ctx), strings.TrimSpace(f.Q), f.SubjectID, strings.TrimSpace(f.Keyword))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Group, error) {
		var g Group
		err := r.Scan(&g.ID, &g.Key, &g.Name, &g.Description, &g.MyRole, &g.Kind, &g.PhotoKey, &g.Privacy,
			&g.SubjectID, &g.Subject, &g.Keywords, &g.ToolsOff, &g.ToolNames, &g.Members, &g.Requested)
		return g, err
	})
}

// GroupForOutsider is the group's card for an employee who is not in it: only an open or closed group, never to a
// guest. ProjectByKey has said ErrForbidden.
func (s *Store) GroupForOutsider(ctx context.Context, p Project, userID string) (Group, error) {
	if !p.Listed() || IsGuestCtx(ctx) {
		return Group{}, ErrForbidden
	}
	g := Group{Project: p}
	err := s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM project_members WHERE project_id = $1)::int,
		EXISTS (SELECT 1 FROM project_join_requests WHERE project_id = $1 AND user_id = $2)`, p.ID, userID).Scan(&g.Members, &g.Requested)
	return g, err
}

// JoinGroup: the person joins an open group at once («Вступить») or asks to join a closed one («Отправить запрос»,
// with a message): its owner and moderators are told. It reports whether they are a member now.
func (s *Store) JoinGroup(ctx context.Context, p Project, userID, message string) (bool, error) {
	if !p.Listed() || p.Archived {
		return false, ErrForbidden
	}
	if p.MyRole != "" && p.MyRole != "admin" {
		return false, invalid("err.already_member")
	}
	if guest, err := s.IsGuest(ctx, userID); err != nil || guest {
		if err == nil {
			err = invalid("err.guest_collab_only")
		}
		return false, err
	}
	message = strings.TrimSpace(message)
	if len([]rune(message)) > 1000 {
		message = string([]rune(message)[:1000])
	}
	joined := p.Privacy == "open"
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if joined {
			if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member')
				ON CONFLICT DO NOTHING`, p.ID, userID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM project_invites WHERE project_id = $1 AND user_id = $2`, p.ID, userID)
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_join_requests (project_id, user_id, message) VALUES ($1, $2, $3)
			ON CONFLICT (project_id, user_id) DO UPDATE SET message = EXCLUDED.message`, p.ID, userID, message)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, project_id, body, url)
			SELECT m.user_id, $2, 'group_join_request', $1, $3, $4 FROM project_members m JOIN users u ON u.id = m.user_id
			WHERE m.project_id = $1 AND m.role IN ('owner', 'manager') AND u.active AND m.user_id <> $2 AND NOT go24_quiet()`,
			p.ID, userID, message, "/p/"+p.Key+"#requests")
		return err
	})
	return joined, err
}

// CancelJoinRequest: the person takes their request back.
func (s *Store) CancelJoinRequest(ctx context.Context, p Project, userID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM project_join_requests WHERE project_id = $1 AND user_id = $2`, p.ID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `DELETE FROM notifications WHERE project_id = $1 AND actor_id = $2 AND kind = 'group_join_request'
			AND read_at IS NULL`, p.ID, userID)
		return err
	})
}

// JoinRequests are the group's «Запросы на вступление» (its owner and moderators), oldest first.
func (s *Store) JoinRequests(ctx context.Context, p Project) ([]JoinRequest, error) {
	if !p.CanManage() || p.Collab() {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name, j.message, j.created_at FROM project_join_requests j
		JOIN users u ON u.id = j.user_id WHERE j.project_id = $1 ORDER BY j.created_at`, p.ID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[JoinRequest])
}

// AnswerJoinRequest: the owner or a moderator accepts the request (the person becomes a member) or rejects it; the
// person is told.
func (s *Store) AnswerJoinRequest(ctx context.Context, p Project, actorID, userID string, accept bool) error {
	if !p.CanManage() || p.Collab() || p.Archived {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `DELETE FROM project_join_requests WHERE project_id = $1 AND user_id::text = $2 RETURNING user_id`,
			p.ID, userID).Scan(&userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		kind := "group_join_rejected"
		if accept {
			kind = "group_join_accepted"
			if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member')
				ON CONFLICT DO NOTHING`, p.ID, userID); err != nil {
				return err
			}
		}
		// the others who could answer it need not any more
		if _, err := tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE project_id = $1 AND actor_id = $2
			AND kind = 'group_join_request' AND read_at IS NULL`, p.ID, userID); err != nil {
			return err
		}
		url := "/projects?tab=all"
		if accept {
			url = "/p/" + p.Key
		}
		return notifyCollab(ctx, tx, userID, actorID, kind, p.ID, url)
	})
}
