package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// ---------- presence: «в сети», «был(а) в сети …», «Отошёл», «Не беспокоить» ----------

// onlineFor is how long a person stays «в сети» after go24 last saw them; an open page keeps them seen (the
// messenger counter's stream of every page, see imUnread).
const onlineFor = 3 * time.Minute

// presence is how a person looks to others: State is online | away | dnd | offline; Text says it in words.
type presence struct {
	State string `json:"state"`
	Seen  int64  `json:"seen,omitempty"` // unix seconds; 0 — never seen
	Text  string `json:"text"`
}

// touchSeen marks the person seen now (at most once a minute: one cheap update).
func (s *Server) touchSeen(ctx context.Context, userID string) {
	if s.DB == nil {
		return
	}
	if _, err := s.DB.Exec(ctx, `UPDATE users SET seen_at = now() WHERE id = $1 AND (seen_at IS NULL OR seen_at < now() - interval '1 minute')`, userID); err != nil {
		s.Log.Warn("presence seen", "err", err)
	}
}

// presenceState is the state for a status and the last time seen.
func presenceState(status string, seen *time.Time, now time.Time) string {
	if seen == nil || now.Sub(*seen) > onlineFor {
		return "offline"
	}
	if status != "" {
		return status
	}
	return "online"
}

// presenceText is the state in the reader's words: «В сети», «Был(а) в сети 5 мин назад», «… вчера в 14:20».
func presenceText(tr func(string, ...any) string, state string, seen *time.Time, now time.Time) string {
	if state != "offline" {
		return tr("presence." + state)
	}
	if seen == nil {
		return tr("presence.offline")
	}
	t, now := seen.In(time.Local), now.In(time.Local)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	yesterday := now.AddDate(0, 0, -1)
	switch {
	case now.Sub(t) < time.Hour:
		return tr("presence.seen_min", max(1, int(now.Sub(t).Minutes())))
	case y1 == y2 && m1 == m2 && d1 == d2:
		return tr("presence.seen_today", t.Format("15:04"))
	case t.Year() == yesterday.Year() && t.YearDay() == yesterday.YearDay():
		return tr("presence.seen_yesterday", t.Format("15:04"))
	case y1 == y2:
		return tr("presence.seen_day", t.Format("02.01"))
	}
	return tr("presence.seen_day", t.Format("02.01.2006"))
}

// presenceOf is the presence of the people (unknown ids are left out).
func (s *Server) presenceOf(r *http.Request, ids []string) (map[string]presence, error) {
	out := map[string]presence{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(r.Context(), `SELECT id::text, presence, seen_at FROM users WHERE id::text = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	tr := func(k string, a ...any) string { return s.tr(r, k, a...) }
	for rows.Next() {
		var id, status string
		var seen *time.Time
		if err := rows.Scan(&id, &status, &seen); err != nil {
			return nil, err
		}
		p := presence{State: presenceState(status, seen, now)}
		p.Text = presenceText(tr, p.State, seen, now)
		if seen != nil {
			p.Seen = seen.Unix()
		}
		out[id] = p
	}
	return out, rows.Err()
}

// presenceGet answers GET /presence?user=…&user=…: {id: {state, seen, text}}; a guest learns only of people
// they may know.
func (s *Server) presenceGet(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ids := r.URL.Query()["user"]
	if len(ids) > 300 {
		ids = ids[:300]
	}
	if tasks.IsGuestCtx(r.Context()) {
		known := ids[:0:0]
		for _, id := range ids {
			if id == sess.UserID || s.knowsPerson(r, sess.UserID, id) {
				known = append(known, id)
			}
		}
		ids = known
	}
	out, err := s.presenceOf(r, ids)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// presenceSet stores the person's own status (POST /presence, status = "" | away | dnd) and returns to the page.
func (s *Server) presenceSet(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	status := r.FormValue("status")
	if status != "" && status != "away" && status != "dnd" {
		http.Error(w, s.tr(r, "err.bad_form"), http.StatusBadRequest)
		return
	}
	if _, err := s.DB.Exec(r.Context(), `UPDATE users SET presence = $2, seen_at = now() WHERE id = $1`, sess.UserID, status); err != nil {
		s.fail(w, "presence", err)
		return
	}
	back := "/my"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && ref.Path != "" {
		back = ref.RequestURI()
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// myPresence is the person's own status for the top bar ("" — online).
func (s *Server) myPresence(ctx context.Context, userID string) string {
	var status string
	if s.DB != nil {
		_ = s.DB.QueryRow(ctx, `SELECT presence FROM users WHERE id = $1`, userID).Scan(&status)
	}
	return status
}
