package tasks

import (
	"context"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The project's own card: name, description, the dates of a «проект», the avatar and the archive. Owners and
// managers of the project change them (of a collab — by its «кто может изменять настройки»).

// PhotoURL is where the project's avatar is served ("" — none); a new avatar gets a new URL.
func (p Project) PhotoURL() string {
	if p.PhotoKey == "" {
		return ""
	}
	h := fnv.New32a()
	h.Write([]byte(p.PhotoKey))
	return "/p/" + p.Key + "/photo?v=" + strconv.FormatUint(uint64(h.Sum32()), 36)
}

// SaveProjectInfo changes the name, the description and the dates.
func (s *Store) SaveProjectInfo(ctx context.Context, p Project, name, description string, start, finish *time.Time) error {
	if !p.CanEdit() {
		return ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return invalid("err.project_name_required")
	}
	if start != nil && finish != nil && finish.Before(*start) {
		return invalid("err.plan_dates")
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET name = $2, description = $3, date_start = $4, date_finish = $5, updated_at = now()
		WHERE id = $1`, p.ID, name, strings.TrimSpace(description), start, finish)
	if err == nil && p.Collab() && (name != p.Name || strings.TrimSpace(description) != p.Description) {
		err = logCollab(ctx, s.db, p.ID, Viewer(ctx), "info", "", name)
	}
	return err
}

// SetProjectPhoto keeps a new avatar ("" removes it) and says the old one's key.
func (s *Store) SetProjectPhoto(ctx context.Context, p Project, key, contentType string) (old string, err error) {
	if !p.CanEdit() {
		return "", ErrForbidden
	}
	err = s.db.QueryRow(ctx, `UPDATE projects n SET photo_key = $2, photo_type = $3, updated_at = now() FROM projects o
		WHERE n.id = $1 AND o.id = n.id RETURNING o.photo_key`, p.ID, key, contentType).Scan(&old)
	return old, err
}

// ProjectPhoto is the avatar's storage key and type (any signed-in person sees project avatars, as in lists; a
// collab's avatar — only its members).
func (s *Store) ProjectPhoto(ctx context.Context, userID, key string) (photoKey, contentType string, err error) {
	err = s.db.QueryRow(ctx, `SELECT photo_key, photo_type FROM projects p WHERE key = $1 AND (kind <> 'collab' OR $3
			OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $2))`,
		strings.ToUpper(key), userID, IsAdmin(ctx)).Scan(&photoKey, &contentType)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && photoKey == "") {
		return "", "", ErrNotFound
	}
	return photoKey, contentType, err
}

// SetArchived moves the project to the archive or back. An archived project leaves the menus and lists and takes
// no new tasks; its tasks stay as they are. A collab in the archive is read-only (its tasks, drive, chat), its
// invitations do not let anyone in.
func (s *Store) SetArchived(ctx context.Context, p Project, on bool) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `UPDATE projects SET archived = $2, updated_at = now() WHERE id = $1 AND archived <> $2`, p.ID, on)
	if err != nil || tag.RowsAffected() == 0 || !p.Collab() {
		return err
	}
	action := "archived"
	if !on {
		action = "restored"
	}
	return logCollab(ctx, s.db, p.ID, Viewer(ctx), action, "", "")
}

// TransferProject hands the group or project over, as «Сменить владельца» (GO24DEV-926): the owner (or
// an administrator) makes another employee among its members the owner, the former owner stays a moderator; the new
// owner is told. A collab goes by TransferCollab.
func (s *Store) TransferProject(ctx context.Context, p Project, actorID, userID string) error {
	if p.Collab() {
		return s.TransferCollab(ctx, p, actorID, userID)
	}
	if p.MyRole != "owner" && p.MyRole != "admin" {
		return ErrForbidden
	}
	if p.Archived {
		return invalid("err.project_archived")
	}
	return s.transferOwner(ctx, p, actorID, userID, "project_owner", "err.project_owner_member", "err.project_owner_guest")
}

// DeleteProject deletes the group or project for good, as «Удалить группу» (GO24DEV-926): the owner or
// an administrator, the name typed again. As there, a group with tasks is not deleted — they are moved or deleted
// first (those in the trash go with it). A collab goes by DeleteCollab, its tasks with it.
func (s *Store) DeleteProject(ctx context.Context, p Project, actorID, confirm string) error {
	if p.Collab() {
		return s.DeleteCollab(ctx, p, actorID, confirm)
	}
	if p.MyRole != "owner" && p.MyRole != "admin" {
		return ErrForbidden
	}
	if strings.TrimSpace(confirm) != strings.TrimSpace(p.Name) {
		return invalid("err.project_delete_confirm")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id = $1`, p.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return invalid("err.project_has_tasks", n)
		}
		return deleteProjectTx(ctx, tx, p.ID)
	})
}
