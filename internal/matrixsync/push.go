package matrixsync

import (
	"context"
	"errors"
	"html"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/notify"
)

// Notifications in the messenger (the portal sends them to the «Уведомления» chat): for people who want it, the
// go24 bot repeats each notification in a private chat, so Element on the phone shows it as a push. Rows are
// marked pushed once sent; a missed wake-up is caught by the periodic pass.

// pushLoop waits for go24_push notifications (and checks every minute) until ctx ends.
func (s *Service) pushLoop(ctx context.Context) {
	if s.Notify == nil || s.NotifyText == nil {
		return
	}
	wake := make(chan struct{}, 1)
	go func() {
		for ctx.Err() == nil {
			err := func() error {
				conn, err := s.DB.Acquire(ctx)
				if err != nil {
					return err
				}
				defer conn.Release()
				if _, err := conn.Exec(ctx, "LISTEN go24_push"); err != nil {
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
				s.Log.Error("matrix push: listen", "err", err)
				time.Sleep(3 * time.Second)
			}
		}
	}()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.PushPending(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("matrix push", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-t.C:
		}
	}
}

// PushPending sends the notifications not yet in the messenger (of the last day) of people who want them there.
func (s *Service) PushPending(ctx context.Context) error {
	for {
		n := 0
		err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT n.id, n.user_id::text FROM notifications n JOIN users u ON u.id = n.user_id
				WHERE NOT n.pushed AND u.notify_matrix AND u.active AND n.read_at IS NULL AND n.created_at > now() - interval '1 day'
				ORDER BY n.id LIMIT 20 FOR UPDATE OF n SKIP LOCKED`)
			if err != nil {
				return err
			}
			type row struct {
				ID   int64
				User string
			}
			list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
			if err != nil {
				return err
			}
			for _, r := range list {
				if err := s.push(ctx, r.User, r.ID); err != nil {
					return err // Matrix is down: the rest waits for the next pass
				}
				if _, err := tx.Exec(ctx, `UPDATE notifications SET pushed = true WHERE id = $1`, r.ID); err != nil {
					return err
				}
				n++
			}
			return nil
		})
		if err != nil || n < 20 {
			return err
		}
	}
}

func (s *Service) push(ctx context.Context, userID string, id int64) error {
	it, err := s.Notify.Get(ctx, userID, strconv.FormatInt(id, 10))
	if errors.Is(err, notify.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	room, err := s.notifyRoom(ctx, userID)
	if err != nil || room == "" {
		return err // no Matrix account yet: nothing to send to
	}
	text := s.NotifyText(it)
	link := s.AppURL + "/notifications/" + strconv.FormatInt(id, 10)
	_, err = s.M.Send(ctx, room, map[string]any{"msgtype": "m.notice", "body": text + "\n" + link,
		"format": "org.matrix.custom.html",
		"formatted_body": `<a href="` + html.EscapeString(link) + `">` + html.EscapeString(text) + `</a>`}, "")
	return err
}

// notifyRoom is the bot's private chat with the person for notifications, made on first use ("" when the person
// has no Matrix account).
func (s *Service) notifyRoom(ctx context.Context, userID string) (string, error) {
	ref := userID
	if id, err := s.roomOf(ctx, "notify", &ref); err != nil || id != "" {
		return id, err
	}
	var mxid string
	err := s.DB.QueryRow(ctx, `SELECT mxid FROM matrix_users WHERE user_id = $1 AND mxid <> ''`, userID).Scan(&mxid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, err := s.roomOf(ctx, "notify", &ref); err != nil || id != "" {
		return id, err
	}
	room, err := s.M.CreateDM(ctx, "", mxid)
	if err != nil {
		return "", err
	}
	if err := s.M.ForceJoin(ctx, room, mxid); err != nil {
		return "", err
	}
	if s.NotifyRoom != "" {
		if err := s.M.SetState(ctx, room, "m.room.name", "", map[string]string{"name": s.NotifyRoom}); err != nil {
			return "", err
		}
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO matrix_rooms (kind, ref_id, room_id) VALUES ('notify', $1, $2)`, userID, room); err != nil {
		return "", err
	}
	return room, nil
}
