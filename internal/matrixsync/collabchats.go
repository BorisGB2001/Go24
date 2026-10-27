package matrixsync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Chats of a collab, as «Чаты в проекте»: besides its main chat a collab has group chats on their own
// subjects — made in it by any of its people, or a group chat of one's own added to it by its owner (who may bring
// people into the collab). They stay plain group chats with their own rights; collab_chats says whose they are. Their
// people are the collab's: whoever is brought into such a chat joins the collab first (as its «кто может приглашать»
// lets), and whoever leaves the collab leaves its chats (Reconcile). A chat taken out of the collab, or of a collab
// deleted, loses its guests: they are in their collabs' chats only.

// CollabChat is a group chat of a collab as the chat's header lists it.
type CollabChat struct {
	Room, Name string
}

// CollabOf is the collab whose group chat this room is ("" — none).
func (s *Service) CollabOf(ctx context.Context, room string) string {
	var pid string
	_ = s.DB.QueryRow(ctx, `SELECT project_id::text FROM collab_chats WHERE room_id = $1`, room).Scan(&pid)
	return pid
}

// collabFor is the collab as the employee sees it (their role in it); ErrNotFound when it is not a collab of theirs.
func (s *Service) collabFor(ctx context.Context, userID, projectID string) (tasks.Project, error) {
	var key string
	if err := s.DB.QueryRow(ctx, `SELECT key FROM projects WHERE id::text = $1 AND kind = 'collab'`, projectID).Scan(&key); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = tasks.ErrNotFound
		}
		return tasks.Project{}, err
	}
	p, err := s.Tasks.ProjectByKey(ctx, userID, key)
	if err == nil && p.MyRole == "" {
		err = tasks.ErrNotFound
	}
	return p, err
}

// intoCollab brings into the collab the people (go24 users; a chat bot stays a bot) who are not in it yet — as the
// employee may invite them (tasks.SetMember says it, and logs and tells it).
func (s *Service) intoCollab(ctx context.Context, p tasks.Project, ids []string) error {
	rows, err := s.DB.Query(ctx, `SELECT u.id::text FROM users u WHERE u.id::text = ANY($2)
		AND NOT EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = $1 AND m.user_id = u.id)`, p.ID, ids)
	if err != nil {
		return err
	}
	newcomers, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range newcomers {
		if err := s.Tasks.SetMember(ctx, p, id, "member"); err != nil {
			return err
		}
	}
	if len(newcomers) > 0 {
		s.Wake()
	}
	return nil
}

// CreateCollabChat makes a group chat in the collab of the employee (any of its people) and the people named; those
// not in the collab join it.
func (s *Service) CreateCollabChat(ctx context.Context, userID, projectID, name string, ids []string) (string, error) {
	p, err := s.collabFor(ctx, userID, projectID)
	if err != nil {
		return "", err
	}
	if p.Archived {
		return "", tasks.ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", i18n.E("err.collab_chat_name")
	}
	who, err := s.peers(ctx, userID, ids)
	if err != nil {
		return "", err
	}
	delete(who, userID)
	if len(who) == 0 {
		return "", i18n.E("err.im_group_people")
	}
	var people []string
	for id := range who {
		people = append(people, id)
	}
	if err := s.intoCollab(ctx, p, people); err != nil {
		return "", err
	}
	room, err := s.CreateGroup(ctx, userID, name, people, false)
	if err != nil {
		return "", err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO collab_chats (room_id, project_id, added_by) VALUES ($1, $2, $3)`, room, p.ID, userID); err != nil {
		return "", err
	}
	return room, s.Tasks.LogCollabChat(ctx, p.ID, userID, "chat_created", name)
}

// AddChatToCollab: the owner of a group chat who may bring people into the collab (or owns it) adds the chat to it;
// the chat's people join the collab and keep its history.
func (s *Service) AddChatToCollab(ctx context.Context, userID, projectID, room string) error {
	p, err := s.collabFor(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if may, err := s.Tasks.CanInvite(ctx, p); err != nil {
		return err
	} else if p.Archived || tasks.IsGuestCtx(ctx) || (!may && p.MyRole != "owner") {
		return tasks.ErrForbidden
	}
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	if pl.Level(me) < ownerLevel {
		return i18n.E("err.collab_chat_owner")
	}
	if s.CollabOf(ctx, room) != "" {
		return i18n.E("err.collab_chat_taken")
	}
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return forbidden(err)
	}
	var mxids []string
	for mx := range mem {
		mxids = append(mxids, mx)
	}
	who, err := s.people(ctx, mxids)
	if err != nil {
		return err
	}
	var people []string
	for _, w := range who {
		if !w.Bot {
			people = append(people, w.ID)
		}
	}
	if err := s.closeOpen(ctx, userID, tok, room); err != nil { // the collab's people only: an open chat closes
		return err
	}
	if err := s.intoCollab(ctx, p, people); err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO collab_chats (room_id, project_id, added_by) VALUES ($1, $2, $3)`, room, p.ID, userID); err != nil {
		return err
	}
	return s.Tasks.LogCollabChat(ctx, p.ID, userID, "chat_added", s.roomName(ctx, tok, room))
}

// RemoveChatFromCollab takes the group chat out of its collab: the chat's owner, or the collab's owner and moderators.
// The chat stays with its people, but its guests leave it.
func (s *Service) RemoveChatFromCollab(ctx context.Context, userID, room string) error {
	pid := s.CollabOf(ctx, room)
	if pid == "" {
		return tasks.ErrNotFound
	}
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	p, err := s.collabFor(ctx, userID, pid)
	if err != nil {
		return err
	}
	if pl.Level(me) < ownerLevel && !p.CanManage() {
		return tasks.ErrForbidden
	}
	name := s.roomName(ctx, tok, room)
	if _, err := s.DB.Exec(ctx, `DELETE FROM collab_chats WHERE room_id = $1`, room); err != nil {
		return err
	}
	if err := s.sweep(ctx, room, []string{userID}, func(_ string, guest bool) bool { return guest }); err != nil {
		return err
	}
	return s.Tasks.LogCollabChat(ctx, p.ID, userID, "chat_removed", name)
}

// CollabChats are the group chats of the collab the employee is in, by name.
func (s *Service) CollabChats(ctx context.Context, userID, projectID string) ([]CollabChat, error) {
	rooms, err := s.Rooms(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []CollabChat
	for _, r := range rooms {
		if r.CollabID == projectID && r.CollabID != "" {
			out = append(out, CollabChat{Room: r.ID, Name: r.Name})
		}
	}
	return out, nil
}

// roomName is the chat's name ("" — none set).
func (s *Service) roomName(ctx context.Context, tok, room string) string {
	var n struct {
		Name string `json:"name"`
	}
	_ = s.M.StateAs(ctx, tok, room, "m.room.name", "", &n)
	return n.Name
}

// sweep takes out of a group chat the people out() names (go24 users; chat bots stay): each leaves it as themselves —
// the bot is not in people's own chats, and the chat's owner could not remove another owner; the last owner hands the
// chat on (LeaveGroup). via are go24 users who may be in the chat, to see its people through.
func (s *Service) sweep(ctx context.Context, room string, via []string, out func(userID string, guest bool) bool) error {
	var mem map[string]string
	err := errors.New("nobody to look through")
	for _, v := range via {
		var tok string
		if _, tok, err = s.puppet(ctx, v); err == nil && tok != "" {
			if mem, err = s.M.Members(ctx, room, tok); err == nil {
				break
			}
		}
	}
	if err != nil {
		return fmt.Errorf("members of %s: %w", room, err)
	}
	var mxids []string
	for mx := range mem {
		mxids = append(mxids, mx)
	}
	who, err := s.people(ctx, mxids)
	if err != nil {
		return err
	}
	for _, p := range who {
		if !p.Bot && out(p.ID, p.Guest) {
			if err := s.LeaveGroup(ctx, p.ID, room); err != nil {
				s.Log.Warn("matrix: collab chat leave", "room", room, "err", err)
			}
		}
	}
	return nil
}

// collabChats keeps the collabs' group chats to their people: who left the collab leaves them; the chats of a collab
// deleted lose their guests and are its no more.
func (s *Service) collabChats(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT c.room_id, p.id IS NOT NULL, coalesce(c.added_by::text, ''),
			array(SELECT m.user_id::text FROM project_members m WHERE m.project_id = p.id ORDER BY m.role = 'owner' DESC, m.role = 'manager' DESC)
		FROM collab_chats c LEFT JOIN projects p ON p.id = c.project_id AND p.kind = 'collab'`)
	if err != nil {
		return err
	}
	type chat struct {
		Room    string
		Alive   bool
		AddedBy string
		Members []string // the collab's people, its owner and moderators first
	}
	chats, err := pgx.CollectRows(rows, pgx.RowToStructByPos[chat])
	if err != nil {
		return err
	}
	for _, c := range chats {
		in := map[string]bool{}
		for _, u := range c.Members {
			in[u] = true
		}
		out := func(id string, guest bool) bool { return !in[id] && (c.Alive || guest) }
		via := append([]string{c.AddedBy}, c.Members...)
		if err := s.sweep(ctx, c.Room, via, out); err != nil {
			s.Log.Warn("matrix: collab chat", "room", c.Room, "err", err)
			continue
		}
		if !c.Alive {
			if _, err := s.DB.Exec(ctx, `DELETE FROM collab_chats WHERE room_id = $1`, c.Room); err != nil {
				return err
			}
		}
	}
	return nil
}
