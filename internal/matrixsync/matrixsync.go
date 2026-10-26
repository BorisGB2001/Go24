// Package matrixsync keeps the go24 Matrix server in step with go24 (go24 is the source of truth):
//   - every employee who signed in to go24 gets a Matrix account linked to their Authentik identity;
//     a dismissed employee's account is deactivated (they leave every room at once);
//   - the company is a space; a project whose tasks all members see is a private room in it, and each task
//     is a thread in that room; a task of a "participants only" project gets its own private room;
//   - room members follow the project members / task participants: missing people are added, others removed;
//   - task chat messages cross both ways (go24 posts them under the author's name).
//
// Rooms are created unencrypted so the bridge can read them. People's private chats are not touched:
// the bot is never in them (see tasks.WithAdmin — admin mode never opens private conversations).
package matrixsync

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/feed"
	"go24/internal/matrix"
	"go24/internal/notify"
	"go24/internal/tasks"
)

type Service struct {
	DB         *pgxpool.Pool
	M          *matrix.Client
	Tasks      *tasks.Store
	Feed       *feed.Store
	Log        *slog.Logger
	IDP        string // Synapse oidc_providers idp_id with the "oidc-" prefix: "oidc-authentik"
	ElementURL string // https://…:8443, for links from go24
	AppURL     string // go24 base URL, for links in thread roots
	Company    string // name of the company space
	ThreadHint string // line under a task's thread header, in the installation's language
	FeedName   string // name of the company feed room
	GuestMark  string // after a guest's name in a collab room: «Анна (гость)»
	// notifications repeated in the messenger (nil: not sent there)
	Notify     *notify.Store
	NotifyText func(notify.Item) string // one line of a notification, in the installation's language
	NotifyRoom string                   // name of the bot's chat with notifications
	// a task change as a log line in its thread (nil: not logged)
	HistoryText func(actor, actingFor, field, old, next string, at time.Time) string

	// a bot's event is queued for its application (go24 worker delivers it at once; nil: on its next round)
	BotQueued func()

	// Remote: this process serves pages only; reconciling and bridging run in go24 worker (Wake notifies it)
	Remote bool

	wake chan struct{} // made by New
	mu   sync.Mutex    // one reconcile / one task-room creation at a time

	draftMu sync.Mutex // one change of a chat's drafts at a time (SetDraft)
}

// New prepares the service; start it with Run.
func New(s *Service) *Service {
	s.wake = make(chan struct{}, 1)
	return s
}

// localpartRe: what Matrix allows in user IDs (Authentik usernames are lower-cased first).
var localpartRe = regexp.MustCompile(`^[a-z0-9._=\-/+]+$`)

// Wake asks for a reconcile soon (members or employees changed). Never blocks.
func (s *Service) Wake() {
	if s == nil {
		return
	}
	if s.Remote { // the reconcile runs in go24 worker: tell it through PostgreSQL
		go func() {
			if _, err := s.DB.Exec(context.Background(), `SELECT pg_notify('go24_matrix_wake', '')`); err != nil {
				s.Log.Error("matrix: wake worker", "err", err)
			}
		}()
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run reconciles every few minutes and on Wake, and bridges Matrix messages into go24, until ctx ends.
func (s *Service) Run(ctx context.Context) {
	if bot, err := s.M.WhoAmI(ctx); err == nil {
		if err := s.M.NoRatelimit(ctx, bot); err != nil {
			s.Log.Warn("matrix: bot rate limit stays", "err", err)
		}
	}
	go s.syncLoop(ctx)
	go s.pushLoop(ctx)
	go s.statusLoop(ctx)
	go s.autoDeleteLoop(ctx)
	go s.botLoop(ctx)
	go func() { // go24 web asks for a reconcile (new project, members, employees)
		w := s.listen(ctx, "go24_matrix_wake")
		for {
			select {
			case <-ctx.Done():
				return
			case <-w:
				s.Wake()
			}
		}
	}()
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("matrix reconcile", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
			time.Sleep(2 * time.Second) // gather a burst of changes
		}
	}
}

// ---------- accounts ----------

type person struct {
	ID, Username, Name, Sub string
	Active                  bool
}

func (s *Service) mxidOf(username string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(username))
	if l == "" || !localpartRe.MatchString(l) {
		return "", false
	}
	return s.M.UserID(l), true
}

// accounts creates, updates and deactivates Matrix accounts; it returns go24 user ID → Matrix ID of active people.
func (s *Service) accounts(ctx context.Context) (map[string]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT id, username, name, oidc_sub, active FROM users WHERE username <> ''`)
	if err != nil {
		return nil, err
	}
	people, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (person, error) {
		var p person
		return p, r.Scan(&p.ID, &p.Username, &p.Name, &p.Sub, &p.Active)
	})
	if err != nil {
		return nil, err
	}
	active := map[string]string{}
	for _, p := range people {
		mxid, ok := s.mxidOf(p.Username)
		if !ok {
			continue
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO matrix_users (user_id, mxid) VALUES ($1, $2)
			ON CONFLICT (user_id) DO UPDATE SET mxid = EXCLUDED.mxid,
			  token = CASE WHEN matrix_users.mxid = EXCLUDED.mxid THEN matrix_users.token ELSE '' END`, p.ID, mxid); err != nil {
			return nil, err
		}
		u, err := s.M.GetUser(ctx, mxid)
		if err != nil {
			return nil, err
		}
		switch {
		case u == nil && p.Active:
			err = s.M.PutUser(ctx, mxid, p.Name, s.IDP, p.Sub, false)
		case u != nil && !p.Active && !u.Deactivated:
			s.Log.Info("matrix: deactivating dismissed employee", "user_id", p.ID)
			err = s.M.Deactivate(ctx, mxid)
			if err == nil {
				_, err = s.DB.Exec(ctx, `UPDATE matrix_users SET token = '' WHERE user_id = $1`, p.ID)
			}
		case u != nil && p.Active && (u.Deactivated || u.DisplayName != p.Name):
			err = s.M.PutUser(ctx, mxid, p.Name, s.IDP, p.Sub, false)
		}
		if err != nil {
			return nil, fmt.Errorf("account %s: %w", mxid, err)
		}
		if p.Active {
			active[p.ID] = mxid
		}
	}
	return active, nil
}

// ---------- rooms ----------

func (s *Service) roomOf(ctx context.Context, kind string, ref *string) (string, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT room_id FROM matrix_rooms WHERE kind = $1 AND ref_id IS NOT DISTINCT FROM $2::uuid`, kind, ref).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *Service) ensureRoom(ctx context.Context, kind string, ref *string, name, topic string) (string, bool, error) {
	return s.ensureRoomIn(ctx, kind, ref, name, topic, kind != "space")
}

// ensureRoomIn makes the room when it is not there yet; inSpace links it into the company space (a collab's room
// stays out of it: the collab is not the company's).
func (s *Service) ensureRoomIn(ctx context.Context, kind string, ref *string, name, topic string, inSpace bool) (string, bool, error) {
	id, err := s.roomOf(ctx, kind, ref)
	if err != nil || id != "" {
		return id, false, err
	}
	id, err = s.M.CreateRoom(ctx, name, topic, kind == "space")
	if err != nil {
		return "", false, err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO matrix_rooms (kind, ref_id, room_id) VALUES ($1, $2, $3)`, kind, ref, id); err != nil {
		return "", false, err
	}
	if inSpace {
		if space, _ := s.roomOf(ctx, "space", nil); space != "" {
			if err := s.M.AddToSpace(ctx, space, id); err != nil {
				return "", false, err
			}
		}
	}
	s.Log.Info("matrix: room created", "kind", kind)
	return id, true, nil
}

// members makes the room's people exactly the wanted ones (the bot stays).
func (s *Service) members(ctx context.Context, roomID string, want map[string]bool) error {
	have, err := s.M.Members(ctx, roomID)
	if err != nil {
		return err
	}
	bot := s.M.UserID("go24")
	for mxid := range want {
		if _, ok := have[mxid]; !ok {
			if err := s.M.ForceJoin(ctx, roomID, mxid); err != nil {
				return fmt.Errorf("join %s: %w", mxid, err)
			}
		}
	}
	for mxid := range have {
		if mxid != bot && !want[mxid] {
			if err := s.M.Kick(ctx, roomID, mxid, "go24"); err != nil {
				return fmt.Errorf("kick %s: %w", mxid, err)
			}
		}
	}
	return nil
}

// markGuest gives a guest their name in a collab's room with the mark «(гость)» — every Matrix client (Element too)
// shows it beside their messages. The guest's own member event says it (only they may change it).
func (s *Service) markGuest(ctx context.Context, room, userID, name string) error {
	mxid, tok, err := s.puppet(ctx, userID)
	if err != nil || tok == "" {
		return err
	}
	mark := s.GuestMark
	if mark == "" {
		mark = "guest"
	}
	return s.M.SetStateAs(ctx, tok, room, "m.room.member", mxid, map[string]string{"membership": "join", "displayname": name + " (" + mark + ")"})
}

// collabRoomRules puts the collab's settings on its room: whether new members read what was said before them
// (history_visibility shared / joined), who writes in it (writers — the Matrix IDs of those its «кто может писать в
// чат» lets, nil — everyone: messages then need writerLevel, which they are given; reactions, votes and taking back
// one's own stay open) and, in the archive, that nobody but the bot writes (events_default 100). What was put last is
// kept in matrix_rooms.applied, so the room is touched only on a change.
func (s *Service) collabRoomRules(ctx context.Context, room string, history, readOnly bool, writers []string) error {
	vis := "shared"
	if !history {
		vis = "joined"
	}
	sort.Strings(writers)
	want := "history=" + vis + fmt.Sprintf(";readonly=%t", readOnly)
	if writers != nil {
		h := fnv.New64a()
		h.Write([]byte(strings.Join(writers, ",")))
		want += fmt.Sprintf(";writers=%x", h.Sum64())
	}
	var applied string
	if err := s.DB.QueryRow(ctx, `SELECT applied FROM matrix_rooms WHERE room_id = $1`, room).Scan(&applied); err != nil {
		return err
	}
	if applied == want {
		return nil
	}
	if err := s.M.SetState(ctx, room, "m.room.history_visibility", "", map[string]string{"history_visibility": vis}); err != nil {
		return err
	}
	pl := map[string]any{}
	if err := s.M.State(ctx, room, "m.room.power_levels", "", &pl); err != nil {
		return err
	}
	users, _ := pl["users"].(map[string]any)
	if users == nil {
		users = map[string]any{}
		pl["users"] = users
	}
	for mx, lv := range users { // the writers of before; the bot and others keep their levels
		if v, _ := lv.(float64); v == writerLevel {
			delete(users, mx)
		}
	}
	events, _ := pl["events"].(map[string]any)
	if events == nil {
		events = map[string]any{}
		pl["events"] = events
	}
	pl["events_default"] = 0
	if writers != nil {
		pl["events_default"] = writerLevel
		for _, t := range []string{"m.reaction", "m.room.redaction", pollResponse, "m.poll.response"} {
			events[t] = 0
		}
		for _, mx := range writers {
			if v, _ := users[mx].(float64); v < writerLevel {
				users[mx] = writerLevel
			}
		}
	}
	if readOnly {
		pl["events_default"] = 100
	}
	if err := s.M.SetState(ctx, room, "m.room.power_levels", "", pl); err != nil {
		return err
	}
	_, err := s.DB.Exec(ctx, `UPDATE matrix_rooms SET applied = $2 WHERE room_id = $1`, room, want)
	return err
}

// writerLevel is the power level of those who write in a collab's chat that not everyone writes in: below a
// moderator's, so it takes back nobody else's messages.
const writerLevel = 10

// collabWriterSQL: the member (pm, u) writes in the chat of the collab p, by its «кто может писать в чат».
const collabWriterSQL = `(p.chat_perm = 'members' OR (p.chat_perm = 'employees' AND NOT u.guest) OR pm.role IN ('owner', 'manager'))`

// ProjectRoom is the Matrix room of a project or a collab ("" — not made yet, or no messenger).
func (s *Service) ProjectRoom(ctx context.Context, projectID string) (string, error) {
	if s == nil {
		return "", nil
	}
	return s.roomOf(ctx, "project", &projectID)
}

// ProjectRooms are the rooms of the projects (those that have one).
func (s *Service) ProjectRooms(ctx context.Context, projectIDs []string) ([]string, error) {
	if s == nil || len(projectIDs) == 0 {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT room_id FROM matrix_rooms WHERE kind = 'project' AND ref_id = ANY($1::uuid[])`, projectIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Reconcile brings accounts, the space, project rooms and task rooms in line with go24.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	active, err := s.accounts(ctx)
	if err != nil {
		return err
	}
	guests := map[string]string{} // go24 user → name, of the guests from outside: they are in their collabs' rooms only
	grows, err := s.DB.Query(ctx, `SELECT id::text, name FROM users WHERE guest`)
	if err != nil {
		return err
	}
	for grows.Next() {
		var id, name string
		if err := grows.Scan(&id, &name); err != nil {
			grows.Close()
			return err
		}
		guests[id] = name
	}
	grows.Close()
	all := map[string]bool{}
	for u, mx := range active {
		if _, guest := guests[u]; !guest {
			all[mx] = true
		}
	}
	space, _, err := s.ensureRoom(ctx, "space", nil, s.Company, "go24")
	if err != nil {
		return err
	}
	if err := s.members(ctx, space, all); err != nil {
		return err
	}
	if s.Feed != nil {
		feedRoom, _, err := s.ensureRoom(ctx, "feed", nil, s.FeedName, "go24")
		if err != nil {
			return err
		}
		if err := s.members(ctx, feedRoom, all); err != nil {
			return err
		}
	}

	// project rooms: projects whose tasks every member sees
	rows, err := s.DB.Query(ctx, `SELECT p.id::text, p.key, p.name, p.description, NOT p.archived, p.kind = 'collab', p.chat_history,
			array(SELECT pm.user_id::text FROM project_members pm WHERE pm.project_id = p.id), p.chat_perm <> 'members',
			array(SELECT pm.user_id::text FROM project_members pm JOIN users u ON u.id = pm.user_id WHERE pm.project_id = p.id
				AND `+collabWriterSQL+`),
			p.msg_ttl, coalesce(p.msg_ttl_by::text, (SELECT pm.user_id::text FROM project_members pm WHERE pm.project_id = p.id AND pm.role = 'owner' LIMIT 1), '')
		FROM projects p`)
	if err != nil {
		return err
	}
	type proj struct {
		ID, Key, Name, Desc       string
		Open, Collab, ChatHistory bool
		Members                   []string
		Restricted                bool     // not everyone writes in the collab's chat
		Writers                   []string // who does then
		MsgTTL                    int      // the collab chat's auto-delete
		MsgTTLBy                  string   // who turned it on (the owner when unknown)
	}
	projects, err := pgx.CollectRows(rows, pgx.RowToStructByPos[proj])
	if err != nil {
		return err
	}
	for _, p := range projects {
		id := p.ID
		room, err := s.roomOf(ctx, "project", &id)
		if err != nil {
			return err
		}
		if room == "" && !p.Open {
			continue
		}
		if room == "" {
			topic := p.Key + " · " + p.Desc
			if p.Collab {
				topic = p.Desc
			}
			if room, _, err = s.ensureRoomIn(ctx, "project", &id, p.Name, topic, !p.Collab); err != nil {
				return err
			}
		}
		if p.Collab {
			var writers []string
			if p.Restricted {
				writers = []string{}
				for _, u := range p.Writers {
					if mx, ok := active[u]; ok {
						writers = append(writers, mx)
					}
				}
			}
			if err := s.collabRoomRules(ctx, room, p.ChatHistory, !p.Open, writers); err != nil {
				return err
			}
			if err := s.collabAutoDelete(ctx, room, p.MsgTTL, p.MsgTTLBy); err != nil {
				return err
			}
		}
		want := map[string]bool{}
		if p.Open || p.Collab { // an archived project empties its room; an archived collab's room turns read-only
			for _, u := range p.Members {
				if mx, ok := active[u]; ok {
					want[mx] = true
				}
			}
		}
		var had map[string]string
		if p.Collab {
			if had, err = s.M.Members(ctx, room); err != nil {
				return err
			}
		}
		if err := s.members(ctx, room, want); err != nil {
			return err
		}
		for _, u := range p.Members { // a guest who has just joined a collab's room is marked there
			if name, guest := guests[u]; guest && p.Collab && want[active[u]] {
				if _, ok := had[active[u]]; !ok {
					if err := s.markGuest(ctx, room, u, name); err != nil {
						return err
					}
				}
			}
		}
	}

	// rooms of projects and tasks that are no more (a collab deleted, a task purged): nobody stays in them
	rows, err = s.DB.Query(ctx, `SELECT r.room_id FROM matrix_rooms r WHERE (r.kind = 'project' AND NOT EXISTS (SELECT 1 FROM projects p WHERE p.id = r.ref_id))
		OR (r.kind = 'task' AND NOT EXISTS (SELECT 1 FROM tasks_all t WHERE t.id = r.ref_id))`)
	if err != nil {
		return err
	}
	gone, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, room := range gone {
		if err := s.members(ctx, room, nil); err != nil {
			return err
		}
		if _, err := s.DB.Exec(ctx, `DELETE FROM matrix_rooms WHERE room_id = $1`, room); err != nil {
			return err
		}
		s.Log.Info("matrix: room of a deleted project or task emptied", "room", room)
	}

	// task rooms: the task's participants
	rows, err = s.DB.Query(ctx, `SELECT r.room_id, array_remove(array[t.creator_id::text, t.responsible_id::text] ||
			array(SELECT tp.user_id::text FROM task_participants tp WHERE tp.task_id = t.id), NULL)
		FROM matrix_rooms r JOIN tasks t ON t.id = r.ref_id WHERE r.kind = 'task'`)
	if err != nil {
		return err
	}
	type troom struct {
		Room   string
		People []string
	}
	trooms, err := pgx.CollectRows(rows, pgx.RowToStructByPos[troom])
	if err != nil {
		return err
	}
	for _, tr := range trooms {
		want := map[string]bool{}
		for _, u := range tr.People {
			if mx, ok := active[u]; ok {
				want[mx] = true
			}
		}
		if err := s.members(ctx, tr.Room, want); err != nil {
			return err
		}
	}
	return nil
}

// ---------- task chat ↔ Matrix ----------

// target finds (creating when needed) where a task is discussed: a thread in its project's room, or the
// task's own room when only task participants may see the task.
func (s *Service) target(ctx context.Context, taskID string) (room, root string, err error) {
	room, root, created, err := s.place(ctx, taskID)
	if err == nil && created {
		if err := s.backfill(ctx, taskID, room, root); err != nil {
			s.Log.Error("matrix: move task history", "task_id", taskID, "err", err)
		}
	}
	return room, root, err
}

// place finds or makes the task's room or thread; created reports a new one (its history is still in go24 only).
func (s *Service) place(ctx context.Context, taskID string) (room, root string, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Tasks.TaskByIDAll(ctx, taskID) // «moved to the trash» is logged too
	if err != nil {
		return "", "", false, err
	}
	var open bool
	var thread string
	if err := s.DB.QueryRow(ctx, `SELECT p.visibility = 'members' AND NOT p.archived, t.matrix_thread FROM tasks_all t
		JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, taskID).Scan(&open, &thread); err != nil {
		return "", "", false, err
	}
	link := s.AppURL + "/t/" + t.Key()
	if !open {
		if room, created, err = s.ensureRoom(ctx, "task", &taskID, t.Key()+" "+t.Title, link); err != nil {
			return "", "", false, err
		}
		if created { // first members right away; later changes come with Reconcile
			go s.Wake()
		}
		return room, "", created, nil
	}
	pid := t.ProjectID
	if room, err = s.roomOf(ctx, "project", &pid); err != nil || room == "" {
		if err == nil {
			err = errRoomNotReady
		}
		return "", "", false, err
	}
	if thread == "" {
		thread, err = s.M.Send(ctx, room, map[string]any{"msgtype": "m.notice",
			"body": "📌 " + t.Key() + " " + t.Title + "\n" + link + "\n💬 " + s.ThreadHint}, "")
		if err != nil {
			return "", "", false, err
		}
		if _, err := s.DB.Exec(ctx, `UPDATE tasks_all SET matrix_thread = $2 WHERE id = $1`, taskID, thread); err != nil {
			return "", "", false, err
		}
		created = true
	}
	return room, thread, created, nil
}

// Link opens the task's discussion in Element.
func (s *Service) Link(ctx context.Context, taskID string) (string, error) {
	room, root, err := s.target(ctx, taskID)
	if err != nil {
		return "", err
	}
	return matrix.Permalink(s.ElementURL, room, root), nil
}

func (s *Service) puppet(ctx context.Context, userID string) (mxid, token string, err error) {
	err = s.DB.QueryRow(ctx, `SELECT mxid, token FROM matrix_users WHERE user_id = $1`, userID).Scan(&mxid, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.botPuppet(ctx, userID) // a chat bot writes as itself too
	}
	if err != nil || token != "" {
		return mxid, token, err
	}
	if token, err = s.M.LoginAs(ctx, mxid); err != nil {
		return "", "", err
	}
	_, err = s.DB.Exec(ctx, `UPDATE matrix_users SET token = $2 WHERE user_id = $1`, userID, token)
	return mxid, token, err
}

// OnComment copies a message of the go24 task chat to Matrix, under the author's name.
func (s *Service) OnComment(ctx context.Context, taskID string, c tasks.Comment) {
	if s == nil {
		return
	}
	if err := s.onComment(ctx, taskID, c); err != nil {
		s.Log.Error("matrix: copy comment", "task_id", taskID, "err", err)
	}
}

func (s *Service) onComment(ctx context.Context, taskID string, c tasks.Comment) error {
	room, root, err := s.target(ctx, taskID)
	if err != nil {
		return err
	}
	content := map[string]any{"msgtype": "m.text", "body": c.Body, "go24.comment": c.ID}
	if root != "" {
		content["m.relates_to"] = map[string]any{"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
			"m.in_reply_to": map[string]string{"event_id": root}}
	}
	mxid, token, err := s.puppet(ctx, c.AuthorID)
	if err != nil {
		return err
	}
	if mxid != "" {
		if err := s.M.ForceJoin(ctx, room, mxid); err != nil && !matrix.IsCode(err, "M_FORBIDDEN") {
			return err
		}
	} else { // the author has no Matrix account (never signed in to go24): the bot posts with their name
		content["body"] = c.AuthorName + ": " + c.Body
	}
	ev, err := s.M.Send(ctx, room, content, token)
	if matrix.IsCode(err, "M_UNKNOWN_TOKEN") && mxid != "" { // stale puppet token: log in again once
		if _, err = s.DB.Exec(ctx, `UPDATE matrix_users SET token = '' WHERE user_id = $1`, c.AuthorID); err == nil {
			if _, token, err = s.puppet(ctx, c.AuthorID); err == nil {
				ev, err = s.M.Send(ctx, room, content, token)
			}
		}
	}
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO matrix_events (event_id, comment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, ev, c.ID)
	return err
}

const sinceKey = "matrix.since"

func (s *Service) syncLoop(ctx context.Context) {
	var since string
	_ = s.DB.QueryRow(ctx, `SELECT value #>> '{}' FROM company_settings WHERE key = $1`, sinceKey).Scan(&since)
	for ctx.Err() == nil {
		timeout := 30 * time.Second
		if since == "" {
			timeout = 0 // first start: take the current position, do not replay old history
		}
		res, err := s.M.Sync(ctx, since, timeout)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("matrix sync", "err", err)
				time.Sleep(10 * time.Second)
			}
			continue
		}
		if since != "" {
			for roomID, r := range res.Rooms.Join {
				for _, ev := range r.Timeline.Events {
					if err := s.inbound(ctx, roomID, ev); err != nil {
						s.Log.Error("matrix: inbound message", "err", err)
					}
				}
			}
		}
		since = res.NextBatch
		if _, err := s.DB.Exec(ctx, `INSERT INTO company_settings (key, value) VALUES ($1, to_jsonb($2::text))
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, sinceKey, since); err != nil {
			s.Log.Error("matrix: save sync position", "err", err)
		}
	}
}

// inbound copies a Matrix message into the go24 task chat when it belongs to a task (a thread of a project
// room, or a task room) and its sender is a go24 employee.
func (s *Service) inbound(ctx context.Context, roomID string, ev matrix.Event) error {
	if ev.Sender == s.M.UserID("go24") || mine(ev.Content) {
		return nil
	}
	var seenEv bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM matrix_events WHERE event_id = $1)
		OR EXISTS (SELECT 1 FROM feed_posts WHERE matrix_event = $1) OR EXISTS (SELECT 1 FROM feed_comments WHERE matrix_event = $1)
		OR EXISTS (SELECT 1 FROM feed_likes WHERE matrix_event = $1) OR EXISTS (SELECT 1 FROM feed_comment_likes WHERE matrix_event = $1)
		OR EXISTS (SELECT 1 FROM task_comment_reactions WHERE matrix_event = $1)`, ev.EventID).Scan(&seenEv); err != nil || seenEv {
		return err
	}
	var rkind string
	if err := s.DB.QueryRow(ctx, `SELECT kind FROM matrix_rooms WHERE room_id = $1`, roomID).Scan(&rkind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	var sender string
	if err := s.DB.QueryRow(ctx, `SELECT user_id::text FROM matrix_users WHERE mxid = $1`, ev.Sender).Scan(&sender); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if handled, err := s.inboundReaction(ctx, sender, ev); handled || err != nil {
		return err
	}
	if s.Feed != nil && (rkind == "feed" || rkind == "project") {
		if handled, err := s.inboundFeed(ctx, rkind, roomID, sender, ev); handled || err != nil {
			return err
		}
	}
	if ev.Type != "m.room.message" {
		return nil
	}
	body, _ := ev.Content["body"].(string)
	body = stripReplyFallback(body)
	if strings.TrimSpace(body) == "" {
		return nil
	}
	rel, _ := ev.Content["m.relates_to"].(map[string]any)
	if t, _ := rel["rel_type"].(string); t == "m.replace" { // edits are not copied
		return nil
	}
	var seen bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM matrix_events WHERE event_id = $1)`, ev.EventID).Scan(&seen); err != nil || seen {
		return err
	}
	var kind string
	var ref *string
	err := s.DB.QueryRow(ctx, `SELECT kind, ref_id::text FROM matrix_rooms WHERE room_id = $1`, roomID).Scan(&kind, &ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var taskID string
	switch kind {
	case "task":
		taskID = *ref
	case "project":
		// A message belongs to a task when it is in the task's thread, or when it answers (m.in_reply_to)
		// the task's header or a message of the task. Anything else is the project's general chat.
		var target string
		if t, _ := rel["rel_type"].(string); t == "m.thread" {
			target, _ = rel["event_id"].(string)
		} else if irt, ok := rel["m.in_reply_to"].(map[string]any); ok {
			target, _ = irt["event_id"].(string)
		}
		if target == "" {
			return nil
		}
		err := s.DB.QueryRow(ctx, `SELECT id::text FROM tasks WHERE matrix_thread = $1 AND project_id = $2::uuid
			UNION ALL
			SELECT c.task_id::text FROM matrix_events e JOIN task_comments c ON c.id = e.comment_id
			JOIN tasks t ON t.id = c.task_id WHERE e.event_id = $1 AND t.project_id = $2::uuid
			LIMIT 1`, target, *ref).Scan(&taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	default:
		return nil
	}
	var userID string
	err = s.DB.QueryRow(ctx, `SELECT user_id::text FROM matrix_users WHERE mxid = $1`, ev.Sender).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	mentions, body, err := s.inboundMentions(ctx, ev.Content, body)
	if err != nil {
		return err
	}
	c, err := s.Tasks.PostComment(ctx, userID, taskID, body, mentions...)
	if errors.Is(err, tasks.ErrForbidden) || errors.Is(err, tasks.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO matrix_events (event_id, comment_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, ev.EventID, c.ID)
	return err
}

// stripReplyFallback removes the quoted original ("> <@user:server> text" lines) that older clients put on
// top of a reply's body.
func stripReplyFallback(body string) string {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(lines[i], "> ") {
		i++
	}
	if i > 0 && i < len(lines) && lines[i] == "" {
		i++
	}
	if i >= len(lines) {
		return body
	}
	return strings.Join(lines[i:], "\n")
}

// errRoomNotReady: the project has no room yet (a new project before the next reconcile).
var errRoomNotReady = errors.New("project room not ready yet")

// RoomLink is a messenger room the user takes part in (for the left menu).
type RoomLink struct{ Name, URL string }

// Links returns the company space and the user's project rooms as Element links.
func (s *Service) Links(ctx context.Context, userID string) (space string, rooms []RoomLink, err error) {
	if id, err := s.roomOf(ctx, "space", nil); err == nil && id != "" {
		space = matrix.Permalink(s.ElementURL, id, "")
	}
	rows, err := s.DB.Query(ctx, `SELECT p.name, r.room_id FROM matrix_rooms r JOIN projects p ON p.id = r.ref_id
		JOIN project_members m ON m.project_id = p.id AND m.user_id = $1
		WHERE r.kind = 'project' AND p.visibility = 'members' AND NOT p.archived ORDER BY p.name LIMIT 20`, userID)
	if err != nil {
		return space, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, room string
		if err := rows.Scan(&name, &room); err != nil {
			return space, nil, err
		}
		rooms = append(rooms, RoomLink{Name: name, URL: matrix.Permalink(s.ElementURL, room, "")})
	}
	return space, rooms, rows.Err()
}
