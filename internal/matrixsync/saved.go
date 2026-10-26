package matrixsync

import (
	"context"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"go24/internal/matrix"
)

// «Избранное» and «Напомнить» of messages: each person's own (go24 keeps them, im_saved); a reminder
// comes as a go24 notification with a link to the message.

// mine checks the employee sees the message (is in its chat).
func (s *Service) mine(ctx context.Context, userID, room, event string) error {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	_, err = s.M.EventAs(ctx, tok, room, event)
	return forbidden(err)
}

// Save puts the message into the employee's «Избранное» (on) or takes it out.
func (s *Service) Save(ctx context.Context, userID, room, event string, on bool) error {
	if err := s.mine(ctx, userID, room, event); err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO im_saved (user_id, room_id, event_id, saved) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, event_id) DO UPDATE SET saved = $4, created_at = CASE WHEN $4 THEN now() ELSE im_saved.created_at END`,
		userID, room, event, on); err != nil {
		return err
	}
	return s.tidySaved(ctx, userID, event)
}

// Remind sets when go24 reminds the employee of the message (zero — no reminder).
func (s *Service) Remind(ctx context.Context, userID, room, event string, at time.Time) error {
	if err := s.mine(ctx, userID, room, event); err != nil {
		return err
	}
	var when *time.Time
	if !at.IsZero() {
		when = &at
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO im_saved (user_id, room_id, event_id, remind_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, event_id) DO UPDATE SET remind_at = $4`, userID, room, event, when); err != nil {
		return err
	}
	return s.tidySaved(ctx, userID, event)
}

// tidySaved forgets a message neither kept nor to be reminded of.
func (s *Service) tidySaved(ctx context.Context, userID, event string) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM im_saved WHERE user_id = $1 AND event_id = $2 AND NOT saved AND remind_at IS NULL`, userID, event)
	return err
}

// markSaved flags the employee's kept messages and those with a reminder among the page's.
func (s *Service) markSaved(ctx context.Context, userID string, msgs []Message) {
	if len(msgs) == 0 {
		return
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	rows, err := s.DB.Query(ctx, `SELECT event_id, saved, remind_at FROM im_saved WHERE user_id = $1 AND event_id = ANY($2)`, userID, ids)
	if err != nil {
		s.Log.Warn("im saved", "err", err)
		return
	}
	defer rows.Close()
	type mark struct {
		saved bool
		at    *time.Time
	}
	marks := map[string]mark{}
	for rows.Next() {
		var id string
		var m mark
		if rows.Scan(&id, &m.saved, &m.at) == nil {
			marks[id] = m
		}
	}
	for i := range msgs {
		if m, ok := marks[msgs[i].ID]; ok {
			msgs[i].Saved = m.saved
			if m.at != nil {
				msgs[i].RemindAt = *m.at
			}
		}
	}
}

// Saved is the employee's «Избранное», the latest kept first (messages of chats they have left are not shown).
func (s *Service) Saved(ctx context.Context, userID string) ([]Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT room_id, event_id FROM im_saved WHERE user_id = $1 AND saved ORDER BY created_at DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	type ref struct{ room, event string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.room, &r.event); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	var out []Message
	for _, r := range refs {
		ev, err := s.M.EventAs(ctx, tok, r.room, r.event)
		if err != nil {
			continue
		}
		if m := s.convert(ctx, []matrix.Event{ev}, r.room, me, false); len(m) == 1 {
			m[0].Saved = true
			out = append(out, m[0])
		}
	}
	return out, nil
}

// RemindLoop (go24-worker) sends the reminders that are due, once a minute.
func (s *Service) RemindLoop(ctx context.Context, title func(Message) string) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.SendReminders(ctx, title); err != nil {
				s.Log.Warn("im reminders", "err", err)
			}
		}
	}
}

// SendReminders turns the due reminders into notifications (im_remind: who wrote what, a link to the message); a
// message no longer to be seen is reminded of without its text.
func (s *Service) SendReminders(ctx context.Context, title func(Message) string) error {
	rows, err := s.DB.Query(ctx, `UPDATE im_saved SET remind_at = NULL WHERE remind_at <= now() RETURNING user_id::text, room_id, event_id`)
	if err != nil {
		return err
	}
	type due struct{ user, room, event string }
	var list []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.user, &d.room, &d.event); err == nil {
			list = append(list, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range list {
		var body string
		if m, _, _, err := s.One(ctx, d.user, d.room, d.event); err == nil {
			body = title(m)
		}
		link := "/im?room=" + url.QueryEscape(d.room) + "&at=" + url.QueryEscape(d.event)
		if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'im_remind', $2, $3)`, d.user, body, link); err != nil {
			s.Log.Warn("im reminder", "err", err)
		}
		_ = s.tidySaved(ctx, d.user, d.event)
	}
	return nil
}

// Excerpt is the start of a message for a line: who wrote it and its first words.
func Excerpt(m Message, n int) string {
	body := m.Body
	if m.File != nil {
		body = m.File.Name
	}
	body = strings.Join(strings.Fields(body), " ")
	if utf8.RuneCountInString(body) > n {
		body = strings.TrimSpace(string([]rune(body)[:n])) + "…"
	}
	return m.SenderName + ": " + body
}
