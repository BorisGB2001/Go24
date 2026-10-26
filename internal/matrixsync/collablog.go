package matrixsync

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// «Уведомления в коллабе», as «Уведомления в проекте»: the go24 bot tells in a collab's chat what its log
// says — people added, removed, leaving, a new role or owner; guests joining; invitations; the collab's settings, name,
// archive — each kind as the collab's settings turn it on. A line is told once (collab_log.chat_event); a missed
// wake-up is caught by the periodic pass.

// collabLogKinds: which switch of the settings tells an action of the log ("" — never told: the collab's making).
var collabLogKinds = map[string]string{
	"member_added": "members", "member_removed": "members", "left": "members", "role_changed": "members", "owner_changed": "members",
	"joined":         "guests",
	"invite_created": "invites", "invite_resent": "invites", "invite_revoked": "invites",
	"settings": "collab", "info": "collab", "archived": "collab", "restored": "collab",
	"chat_created": "collab", "chat_added": "collab", "chat_removed": "collab",
}

// collabRoomWait: how long a line waits for its collab's chat to be made before it is let go.
const collabRoomWait = 10 * time.Minute

func (s *Service) collabLogLoop(ctx context.Context) {
	if s.CollabLogText == nil {
		return
	}
	wake := s.listen(ctx, "go24_collab_log")
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.TellCollabLogs(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("matrix collab log", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-t.C:
		}
	}
}

// TellCollabLogs posts the log lines not told yet into their collabs' chats.
func (s *Service) TellCollabLogs(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT l.id, coalesce(r.room_id, ''), p.events_members, p.events_guests, p.events_invites,
			p.events_collab, l.created_at, coalesce(a.name, ''), coalesce(t.name, ''), coalesce(t.guest, false), l.action, l.detail
		FROM collab_log l JOIN projects p ON p.id = l.project_id
		LEFT JOIN matrix_rooms r ON r.kind = 'project' AND r.ref_id = p.id
		LEFT JOIN users a ON a.id = l.actor_id LEFT JOIN users t ON t.id = l.target_id
		WHERE l.chat_event IS NULL ORDER BY l.id LIMIT 200`)
	if err != nil {
		return err
	}
	type line struct {
		ID                              int64
		Room                            string
		Members, Guests, Invites, Admin bool
		tasks.CollabLogEntry
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (line, error) {
		var l line
		e := &l.CollabLogEntry
		err := row.Scan(&l.ID, &l.Room, &l.Members, &l.Guests, &l.Invites, &l.Admin, &e.At, &e.Actor, &e.Target, &e.TargetGuest,
			&e.Action, &e.Detail)
		return l, err
	})
	if err != nil {
		return err
	}
	for _, l := range list {
		on := map[string]bool{"members": l.Members, "guests": l.Guests, "invites": l.Invites, "collab": l.Admin}[collabLogKinds[l.Action]]
		ev := ""
		switch {
		case on && l.Room == "" && time.Since(l.At) < collabRoomWait:
			continue // the chat is being made
		case on && l.Room != "":
			if ev, err = s.M.Send(ctx, l.Room, map[string]any{"msgtype": "m.notice", "body": s.CollabLogText(l.CollabLogEntry),
				"go24.collab_log": l.Action}, ""); err != nil {
				return err
			}
		}
		if _, err := s.DB.Exec(ctx, `UPDATE collab_log SET chat_event = $2 WHERE id = $1`, l.ID, ev); err != nil {
			return err
		}
	}
	return nil
}
