package openlines

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Автоматические сообщения»: a while after a dialogue is closed the client gets the line's messages
// («Остались вопросы?») — in the widget and in their messenger; once they write the dialogue opens as usual and the
// rest are not sent. A spam dialogue and one closed before the line had them get none.

// AutoMsg is one of them: After minutes since the dialogue was closed.
type AutoMsg struct {
	After int    `json:"after_minutes"`
	Text  string `json:"text"`
}

const autoMsgsMax = 3

// Days: is the delay in whole days (the settings show it so)?
func (m AutoMsg) Days() bool { return m.After > 0 && m.After%(24*60) == 0 }

// Count is the delay in its unit: days or hours.
func (m AutoMsg) Count() int {
	if m.Days() {
		return m.After / (24 * 60)
	}
	return m.After / 60
}

// AutoMsgSlots are the line's automatic messages and empty ones (a day by default) up to the most a line has: the rows of
// the settings.
func (w Widget) AutoMsgSlots() []AutoMsg {
	out := slices.Clone(w.AutoMsgs)
	for len(out) < autoMsgsMax {
		out = append(out, AutoMsg{After: 24 * 60})
	}
	return out
}

// cleanAutoMsgs drops the empty messages, checks the rest and sorts them by the delay.
func cleanAutoMsgs(in []AutoMsg) ([]AutoMsg, error) {
	out := []AutoMsg{}
	for _, m := range in {
		if m.Text = strings.TrimSpace(m.Text); m.Text == "" {
			continue
		}
		if len([]rune(m.Text)) > 1000 || m.After < 60 || m.After > 90*24*60 || len(out) >= autoMsgsMax {
			return nil, i18n.E("err.ol_automsg")
		}
		out = append(out, m)
	}
	slices.SortStableFunc(out, func(a, b AutoMsg) int { return a.After - b.After })
	return out, nil
}

// autoMsgs sends the automatic messages due by now (the line's clock): of several due at once (the worker was off) only
// the latest; out of the line's working hours they wait. It returns how many were sent.
func (s *Store) autoMsgs(ctx context.Context, now time.Time, line func(string) (Widget, error)) (int, error) {
	type due struct {
		ID, Widget string
		From       time.Time
		Step       int
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, widget_id::text, automsg_from, automsg_step FROM ol_sessions
		WHERE status = 'closed' AND NOT spam AND automsg_from IS NOT NULL AND automsg_from < $1`, now)
	if err != nil {
		return 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[due])
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range list {
		w, err := line(d.Widget)
		if err != nil {
			return n, err
		}
		next := d.Step
		for next < len(w.AutoMsgs) && !d.From.Add(time.Duration(w.AutoMsgs[next].After)*time.Minute).After(now) {
			next++
		}
		if next < len(w.AutoMsgs) && next == d.Step { // nothing due yet
			continue
		}
		if next > d.Step && !w.InHours(now) {
			continue
		}
		text := ""
		if next > d.Step {
			text = w.AutoMsgs[next-1].Text
		}
		// the last one sent (or the line has fewer now) — nothing more to send; the dialogue reopened meanwhile — none
		tag, err := s.db.Exec(ctx, `WITH u AS (UPDATE ol_sessions SET automsg_step = $3, automsg_from = CASE WHEN $4 THEN NULL ELSE automsg_from END
			WHERE id::text = $1 AND status = 'closed' AND automsg_step = $2 RETURNING id)
			INSERT INTO ol_messages (session_id, body, kind) SELECT id, $5, 'automsg' FROM u WHERE $5 <> ''`,
			d.ID, d.Step, next, next >= len(w.AutoMsgs), text)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
