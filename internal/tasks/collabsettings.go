package tasks

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The settings and the security of a collab «Настройки коллаба»: who may invite members and who may
// invite guests (the owner / the owner and moderators / every member), whether new members see the chat's history,
// what guests may do (make tasks, upload files); who changes the settings, the name and the avatar (the owner / the
// owner and moderators) and who writes in the chat (everyone / all but guests / the owner and moderators), how long the
// chat's messages live (auto-delete); taking a person out takes their access at once; the archive is
// read-only; deleting is the owner's (or an administrator's). Every change is written into the collab's log.

// CollabSettings are the switches of a collab.
type CollabSettings struct {
	InvitePerm      string // who adds members: owner | managers | members
	GuestInvitePerm string // who invites guests: owner | managers | members
	ChatHistory     bool   // new members see the chat's earlier messages
	GuestTasks      bool   // guests make tasks
	GuestFiles      bool   // guests upload files
	EditPerm        string // who changes the settings, the name, the avatar: owner | managers
	ChatPerm        string // who writes in the chat: members | employees (not guests) | managers
	MsgTTL          int    // the chat's messages are deleted after so many seconds (0 — kept), one of MsgTTLs
	// «Уведомления в коллабе»: what of the log the go24 bot tells in the chat — people added, removed, leaving, their
	// roles and the owner; guests joining; invitations; the collab's settings, name and archive
	EventsMembers, EventsGuests, EventsInvites, EventsCollab bool
	StartTool                                                string // «Стартовый инструмент»: one of StartTools
}

// StartTools are the choices of «Стартовый инструмент»: what the collab opens with.
var StartTools = []string{"chat", "tasks", "drive", "calendar", "feed"}

// MsgTTLs are the lifetimes of «Автоудаление сообщений»: an hour, a day, a week, a month (as the messenger's).
var MsgTTLs = []int{3600, 86400, 7 * 86400, 30 * 86400}

// editPerms and chatPerms are the choices of «кто может изменять настройки» and «кто может писать в чат».
var (
	editPerms = map[string]bool{"owner": true, "managers": true}
	chatPerms = map[string]bool{"members": true, "employees": true, "managers": true}
)

// CollabSettings are the collab's settings.
func (s *Store) CollabSettings(ctx context.Context, p Project) (CollabSettings, error) {
	return collabSettings(ctx, s.db, p.ID)
}

func collabSettings(ctx context.Context, q querier, projectID string) (CollabSettings, error) {
	var c CollabSettings
	err := q.QueryRow(ctx, `SELECT invite_perm, guest_invite_perm, chat_history, guests_tasks, guests_files, edit_perm, chat_perm,
		msg_ttl, events_members, events_guests, events_invites, events_collab, start_tool FROM projects WHERE id = $1`, projectID).Scan(
		&c.InvitePerm, &c.GuestInvitePerm, &c.ChatHistory, &c.GuestTasks, &c.GuestFiles, &c.EditPerm, &c.ChatPerm, &c.MsgTTL,
		&c.EventsMembers, &c.EventsGuests, &c.EventsInvites, &c.EventsCollab, &c.StartTool)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// SetCollabSettings changes the settings (those of «кто может изменять настройки»; not in the archive) and logs
// what changed. «Кто может изменять настройки» itself is the owner's (or an administrator's).
func (s *Store) SetCollabSettings(ctx context.Context, p Project, actorID string, in CollabSettings) error {
	if !p.Collab() {
		return ErrNotFound
	}
	if !p.CanEdit() || p.Archived {
		return ErrForbidden
	}
	if !invitePerms[in.InvitePerm] || !invitePerms[in.GuestInvitePerm] || (in.ChatPerm != "" && !chatPerms[in.ChatPerm]) || (in.EditPerm != "" && !editPerms[in.EditPerm]) ||
		(in.MsgTTL != 0 && !slices.Contains(MsgTTLs, in.MsgTTL)) || (in.StartTool != "" && !slices.Contains(StartTools, in.StartTool)) {
		return invalid("err.bad_form")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		old, err := collabSettings(ctx, tx, p.ID)
		if err != nil {
			return err
		}
		if in.EditPerm == "" || (p.MyRole != "owner" && p.MyRole != "admin") { // a moderator keeps the owner's choice
			in.EditPerm = old.EditPerm
		}
		if in.ChatPerm == "" { // a form of before, without the choice
			in.ChatPerm = old.ChatPerm
		}
		if in.StartTool == "" {
			in.StartTool = old.StartTool
		}
		if old == in {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET invite_perm = $2, guest_invite_perm = $3, chat_history = $4, guests_tasks = $5,
			guests_files = $6, edit_perm = $7, chat_perm = $8, msg_ttl = $9,
			msg_ttl_by = CASE WHEN msg_ttl = $9 THEN msg_ttl_by ELSE $10::uuid END, events_members = $11, events_guests = $12,
			events_invites = $13, events_collab = $14, start_tool = $15, updated_at = now() WHERE id = $1`,
			p.ID, in.InvitePerm, in.GuestInvitePerm, in.ChatHistory, in.GuestTasks, in.GuestFiles, in.EditPerm, in.ChatPerm,
			in.MsgTTL, actorID, in.EventsMembers, in.EventsGuests, in.EventsInvites, in.EventsCollab, in.StartTool); err != nil {
			return err
		}
		var diff []string // «invite_perm=members», «chat_history=off»: the log shows them in words
		onOff := func(b bool) string {
			if b {
				return "on"
			}
			return "off"
		}
		for _, d := range []struct{ k, o, n string }{
			{"invite_perm", old.InvitePerm, in.InvitePerm}, {"guest_invite_perm", old.GuestInvitePerm, in.GuestInvitePerm},
			{"chat_history", onOff(old.ChatHistory), onOff(in.ChatHistory)}, {"guests_tasks", onOff(old.GuestTasks), onOff(in.GuestTasks)},
			{"guests_files", onOff(old.GuestFiles), onOff(in.GuestFiles)},
			{"edit_perm", old.EditPerm, in.EditPerm}, {"chat_perm", old.ChatPerm, in.ChatPerm},
			{"msg_ttl", strconv.Itoa(old.MsgTTL), strconv.Itoa(in.MsgTTL)},
			{"events_members", onOff(old.EventsMembers), onOff(in.EventsMembers)}, {"events_guests", onOff(old.EventsGuests), onOff(in.EventsGuests)},
			{"events_invites", onOff(old.EventsInvites), onOff(in.EventsInvites)}, {"events_collab", onOff(old.EventsCollab), onOff(in.EventsCollab)},
			{"start_tool", old.StartTool, in.StartTool},
		} {
			if d.o != d.n {
				diff = append(diff, d.k+"="+d.n)
			}
		}
		return logCollab(ctx, tx, p.ID, actorID, "settings", "", strings.Join(diff, ","))
	})
}

// guestMay: a guest of the collab may do this there ("tasks" — make tasks, "files" — upload files); anyone else
// may (what they may as a member is decided elsewhere).
func guestMay(ctx context.Context, q querier, projectID, userID, what string) error {
	var guest, may bool
	err := q.QueryRow(ctx, `SELECT u.guest, CASE $3 WHEN 'tasks' THEN p.guests_tasks ELSE p.guests_files END OR p.kind <> 'collab'
		FROM users u, projects p WHERE u.id = $2 AND p.id = $1`, projectID, userID, what).Scan(&guest, &may)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil || !guest || may {
		return err
	}
	if what == "tasks" {
		return invalid("err.collab_guest_no_tasks")
	}
	return invalid("err.collab_guest_no_files")
}

// GuestMay: the person may do this in the project (see guestMay).
func (s *Store) GuestMay(ctx context.Context, projectID, userID, what string) error {
	return guestMay(ctx, s.db, projectID, userID, what)
}

// CanAttach: the person may add files to the task — a guest only where the collab lets guests upload; a guest's
// file without a task (they have no drive of their own) is refused.
func (s *Store) CanAttach(ctx context.Context, userID, taskID string) error {
	if taskID == "" {
		if guest, err := s.IsGuest(ctx, userID); err != nil || guest {
			if err == nil {
				err = invalid("err.collab_guest_no_files")
			}
			return err
		}
		return nil
	}
	var projectID string
	var frozen bool
	if err := s.db.QueryRow(ctx, `SELECT t.project_id, p.kind = 'collab' AND p.archived FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, taskID).Scan(&projectID, &frozen); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if frozen { // the archive of a collab takes no files
		return invalid("err.collab_archived")
	}
	return guestMay(ctx, s.db, projectID, userID, "files")
}

// CollabArchived: the project of the key (a project's CL1, or a task's CL1-5) is a collab in the archive the user
// sees (a member, an administrator) — its pages are read-only. To anyone else it says nothing.
func (s *Store) CollabArchived(ctx context.Context, userID, key string) bool {
	if pk, _, err := ParseKey(key); err == nil {
		key = pk
	}
	var yes bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects p WHERE p.key = upper($1) AND p.kind = 'collab' AND p.archived
		AND ($3 OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id::text = $2)))`,
		key, userID, IsAdmin(ctx)).Scan(&yes)
	return yes
}

// ---------- members ----------

// setCollabMember adds a person to the collab or changes their role. Adding is for those the collab lets invite
// (and a guest — for those it lets invite guests); a role is changed by the owner and moderators; a moderator is
// made only by them.
func (s *Store) setCollabMember(ctx context.Context, p Project, actorID, userID, role string) error {
	if p.Archived {
		return ErrForbidden
	}
	if err := s.checkMemberRole(ctx, p, userID, role); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var had string
		err := tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, p.ID, userID).Scan(&had)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch {
		case had == role:
			return nil
		case had == "owner":
			return invalid("err.owner_role_fixed")
		case had != "" || role == "manager":
			if !p.CanManage() {
				return ErrForbidden
			}
		default:
			if err := s.mayInvite(ctx, tx, p, userID); err != nil {
				return err
			}
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT active FROM users WHERE id::text = $1`, userID).Scan(&active); err != nil || !active {
			if err == nil || errors.Is(err, pgx.ErrNoRows) {
				err = ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)
			ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`, p.ID, userID, role); err != nil {
			return err
		}
		action, kind := "member_added", "collab_added"
		if had != "" {
			action, kind = "role_changed", "collab_"+role // collab_manager | collab_member
		}
		if err := notifyCollab(ctx, tx, userID, actorID, kind, p.ID, "/p/"+p.Key); err != nil {
			return err
		}
		return logCollab(ctx, tx, p.ID, actorID, action, userID, role)
	})
}

// mayInvite: the viewer (of p.MyRole) may bring this person into the collab — by «кто может приглашать», and for a
// guest by «кто может приглашать гостей»; a guest invites nobody.
func (s *Store) mayInvite(ctx context.Context, q querier, p Project, userID string) error {
	set, err := collabSettings(ctx, q, p.ID)
	if err != nil {
		return err
	}
	var guest bool
	if err := q.QueryRow(ctx, `SELECT guest FROM users WHERE id::text = $1`, userID).Scan(&guest); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if IsGuestCtx(ctx) || !permits(p, set.InvitePerm) || (guest && !permits(p, set.GuestInvitePerm)) {
		return ErrForbidden
	}
	return nil
}

// permits: the viewer's role in the project passes «кто может» of the setting (an administrator always does).
func permits(p Project, perm string) bool {
	if p.Archived {
		return false
	}
	switch perm {
	case "owner":
		return p.MyRole == "owner" || p.MyRole == "admin"
	case "members":
		return p.CanWork()
	}
	return p.CanManage()
}

// revokeAccess: the person is out of the collab — their invitations of guests stop working, they leave its coming
// meetings, and the meetings they made pass to the owner (removeMember has taken them out of its tasks).
func revokeAccess(ctx context.Context, tx pgx.Tx, p Project, userID string) error {
	if _, err := tx.Exec(ctx, `UPDATE collab_invites SET revoked_at = now() WHERE project_id = $1 AND created_by = $2 AND revoked_at IS NULL`,
		p.ID, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM project_invites WHERE project_id = $1 AND user_id = $2`, p.ID, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM event_attendees a USING events e WHERE e.id = a.event_id AND e.project_id = $1
		AND a.user_id = $2 AND e.ends_at > now()`, p.ID, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events e SET owner_id = p.owner_id FROM projects p WHERE p.id = e.project_id
		AND e.project_id = $1 AND e.owner_id = $2`, p.ID, userID)
	return err
}

// ---------- archive and deleting ----------

// DeleteCollab deletes the collab with its tasks, drive, meetings and log (the owner or an administrator; the
// name typed again confirms it); its chat is emptied by the messenger's next reconcile.
func (s *Store) DeleteCollab(ctx context.Context, p Project, actorID, confirm string) error {
	if !p.Collab() {
		return ErrNotFound
	}
	if p.MyRole != "owner" && p.MyRole != "admin" {
		return ErrForbidden
	}
	if strings.TrimSpace(confirm) != strings.TrimSpace(p.Name) {
		return invalid("err.collab_delete_confirm")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM files WHERE (drive = 'project' AND drive_owner = $1)
			OR task_id IN (SELECT id FROM tasks_all WHERE project_id = $1)`, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE drive_folders SET parent_id = NULL WHERE drive = 'project' AND owner_id = $1`, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM drive_folders WHERE drive = 'project' AND owner_id = $1`, p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM projects WHERE id = $1 AND kind = 'collab'`, p.ID)
		return err
	})
}

// ---------- the log ----------

// CollabLogEntry is a line of the collab's log.
type CollabLogEntry struct {
	At             time.Time
	Actor, Target  string // names ("" — go24 itself / nobody)
	TargetGuest    bool
	Action, Detail string
}

// logCollab writes a line into the log of a collab (of another project — nothing).
func logCollab(ctx context.Context, q querier, projectID, actorID, action, targetID, detail string) error {
	_, err := q.Exec(ctx, `INSERT INTO collab_log (project_id, actor_id, action, target_id, detail)
		SELECT $1, (SELECT id FROM users WHERE id::text = $2), $3, (SELECT id FROM users WHERE id::text = $4), $5
		FROM projects WHERE id = $1 AND kind = 'collab'`, projectID, actorID, action, targetID, detail)
	return err
}

// LogCollabChat writes into the collab's log what was done to its chats: chat_created, chat_added, chat_removed (detail —
// the chat's name); the bot tells it in the collab's chat as its «Уведомления в коллабе» say.
func (s *Store) LogCollabChat(ctx context.Context, projectID, actorID, action, name string) error {
	return logCollab(ctx, s.db, projectID, actorID, action, "", name)
}

// notifyCollab tells the person what was done to them in the collab tells about its groups: added
// (collab_added), made a moderator or a member again (collab_manager, collab_member), excluded (collab_removed), a
// person they invited joined (collab_joined); nobody is told what they did themselves.
func notifyCollab(ctx context.Context, q querier, userID, actorID, kind, projectID, url string) error {
	_, err := q.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, project_id, url)
		SELECT u.id, (SELECT id FROM users WHERE id::text = $2), $3, $4, $5 FROM users u
		WHERE u.id::text = $1 AND u.active AND u.id::text <> $2 AND NOT go24_quiet()`, userID, actorID, kind, projectID, url)
	return err
}

// CollabLog is the collab's log, newest first (the owner, moderators, an administrator).
func (s *Store) CollabLog(ctx context.Context, p Project, limit int) ([]CollabLogEntry, error) {
	if !p.Collab() {
		return nil, ErrNotFound
	}
	if !p.CanManage() {
		return nil, ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT l.created_at, coalesce(a.name, ''), coalesce(t.name, ''), coalesce(t.guest, false), l.action, l.detail
		FROM collab_log l LEFT JOIN users a ON a.id = l.actor_id LEFT JOIN users t ON t.id = l.target_id
		WHERE l.project_id = $1 ORDER BY l.created_at DESC, l.id DESC LIMIT $2`, p.ID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CollabLogEntry])
}

// CanAddMembers: the viewer may add people to the collab (its «кто может приглашать»; a guest never).
func (s *Store) CanAddMembers(ctx context.Context, p Project) bool {
	set, err := collabSettings(ctx, s.db, p.ID)
	return err == nil && p.Collab() && !IsGuestCtx(ctx) && permits(p, set.InvitePerm)
}

// CanRemove: the viewer may take this member out of the collab — the owner and an administrator anyone but the
// owner, a moderator the members (and guests).
func (p Project) CanRemove(m Member) bool {
	switch {
	case m.Role == "owner" || !p.CanManage():
		return false
	case p.Collab() && p.MyRole == "manager":
		return m.Role == "member"
	}
	return true
}

// Changes are the settings a «settings» line changed, as locale suffixes: «invite_perm.members», «chat_history.off».
func (e CollabLogEntry) Changes() []string {
	if e.Action != "settings" || e.Detail == "" {
		return nil
	}
	return strings.Split(strings.ReplaceAll(e.Detail, "=", "."), ",")
}

// TransferCollab hands the collab over, as «Передать права владельца»: the owner (or an administrator)
// makes another employee of the collab its owner, the former owner stays a moderator. A guest owns nothing. The new
// owner is told; the log says it.
func (s *Store) TransferCollab(ctx context.Context, p Project, actorID, userID string) error {
	if !p.Collab() || (p.MyRole != "owner" && p.MyRole != "admin") {
		return ErrForbidden
	}
	if p.Archived {
		return invalid("err.collab_archived")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var role string
		var guest, active bool
		err := tx.QueryRow(ctx, `SELECT m.role, u.guest, u.active FROM project_members m JOIN users u ON u.id = m.user_id
			WHERE m.project_id = $1 AND m.user_id::text = $2`, p.ID, userID).Scan(&role, &guest, &active)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return invalid("err.collab_owner_member")
		case err != nil:
			return err
		case role == "owner":
			return nil
		case guest:
			return invalid("err.collab_owner_guest")
		case !active:
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE project_members SET role = 'manager' WHERE project_id = $1 AND role = 'owner'`, p.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE project_members SET role = 'owner' WHERE project_id = $1 AND user_id::text = $2`, p.ID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET owner_id = $2::uuid WHERE id = $1`, p.ID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, project_id, url)
			SELECT $1::uuid, $2::uuid, 'collab_owner', $3, '/p/' || $4 WHERE $1::uuid <> $2::uuid AND NOT go24_quiet()`,
			userID, actorID, p.ID, p.Key); err != nil {
			return err
		}
		return logCollab(ctx, tx, p.ID, actorID, "owner_changed", userID, "")
	})
}
