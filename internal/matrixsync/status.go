package matrixsync

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Task log in the messenger: every change of a task (fields, people, deadlines, status, time, checklist, files) is
// written to its thread as a line from the go24 bot (people cannot delete it there),
// and a completed task gets a ✅ reaction on its thread header — so the messenger shows at a glance what is done.
// Rows are marked once posted; a missed wake-up is caught by the periodic pass.

const doneReaction = "✅"

func (s *Service) statusLoop(ctx context.Context) {
	if s.HistoryText == nil {
		return
	}
	wake := s.listen(ctx, "go24_status")
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.LogStatuses(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("matrix status log", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-t.C:
		}
	}
}

// listen returns a channel that gets a value on every NOTIFY of the PostgreSQL channel (reconnecting on errors).
func (s *Service) listen(ctx context.Context, channel string) <-chan struct{} {
	wake := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			err := func() error {
				conn, err := s.DB.Acquire(ctx)
				if err != nil {
					return err
				}
				defer conn.Release()
				if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
					return err
				}
				for {
					if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
						return err
					}
					select {
					case wake <- struct{}{}:
					default:
					}
				}
			}()
			if ctx.Err() == nil {
				s.Log.Error("matrix: listen", "channel", channel, "err", err)
				time.Sleep(3 * time.Second)
			}
		}
	}()
	return wake
}

// LogStatuses posts the status changes not yet in the messenger (of the last day).
func (s *Service) LogStatuses(ctx context.Context) error {
	for {
		n := 0
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT h.id, h.task_id::text, coalesce(u.name, ''), coalesce(af.name, ''), h.field, h.old_value, h.new_value, h.created_at
				FROM task_history h LEFT JOIN users u ON u.id = h.actor_id LEFT JOIN users af ON af.id = h.acting_for
				WHERE h.field <> 'created' AND h.matrix_event IS NULL AND h.created_at > now() - interval '1 day'
				ORDER BY h.id LIMIT 20 FOR UPDATE OF h SKIP LOCKED`)
			if err != nil {
				return err
			}
			type row struct {
				ID                                    int64
				Task, Actor, Acting, Field, Old, New string
				At                                    time.Time
			}
			list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
			if err != nil {
				return err
			}
			for _, r := range list {
				ev, err := s.logChange(ctx, r.Task, r.Actor, r.Acting, r.Field, r.Old, r.New, r.At)
				if err != nil {
					return err // the messenger is down: the rest waits
				}
				if _, err := tx.Exec(ctx, `UPDATE task_history SET matrix_event = $2 WHERE id = $1`, r.ID, ev); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil {
			return err
		}
		if n < 20 {
			return s.markDone(ctx)
		}
	}
}

// markDone puts ✅ on the threads of tasks completed before the status log existed.
func (s *Service) markDone(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT t.id::text, t.matrix_thread, r.room_id FROM tasks t
		JOIN matrix_rooms r ON r.kind = 'project' AND r.ref_id = t.project_id
		WHERE t.status = 'completed' AND t.matrix_done = '' AND t.matrix_thread <> '' LIMIT 200`)
	if err != nil {
		return err
	}
	type row struct{ Task, Thread, Room string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return err
	}
	for _, r := range list {
		ev, err := s.M.SendEvent(ctx, r.Room, "m.reaction", map[string]any{"m.relates_to": map[string]any{
			"rel_type": "m.annotation", "event_id": r.Thread, "key": doneReaction}}, "")
		if err != nil {
			return err
		}
		if _, err := s.DB.Exec(ctx, `UPDATE tasks SET matrix_done = $2 WHERE id = $1`, r.Task, ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) logChange(ctx context.Context, taskID, actor, acting, field, old, next string, at time.Time) (string, error) {
	room, root, err := s.target(ctx, taskID)
	if err != nil {
		return "", err
	}
	content := map[string]any{"msgtype": "m.notice", "body": s.HistoryText(actor, acting, field, old, next, at), "go24.history": field}
	if root != "" {
		content["m.relates_to"] = threadRel(root)
	}
	ev, err := s.M.Send(ctx, room, content, "")
	if err != nil || root == "" || field != "status" {
		return ev, err
	}
	var done string
	if err := s.DB.QueryRow(ctx, `SELECT matrix_done FROM tasks WHERE id = $1`, taskID).Scan(&done); err != nil {
		return ev, err
	}
	switch {
	case next == "completed" && done == "":
		r, err := s.M.SendEvent(ctx, room, "m.reaction", map[string]any{"m.relates_to": map[string]any{
			"rel_type": "m.annotation", "event_id": root, "key": doneReaction}}, "")
		if err != nil {
			return ev, err
		}
		_, err = s.DB.Exec(ctx, `UPDATE tasks SET matrix_done = $2 WHERE id = $1`, taskID, r)
		return ev, err
	case next != "completed" && done != "": // reopened
		if err := s.M.Redact(ctx, room, done, ""); err != nil {
			return ev, err
		}
		_, err = s.DB.Exec(ctx, `UPDATE tasks SET matrix_done = '' WHERE id = $1`, taskID)
		return ev, err
	}
	return ev, nil
}
