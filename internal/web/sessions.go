package web

import (
	"net/http"
	"net/url"

	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/tasks"
)

// devices are the employees' sessions kept on the server (user_sessions).
func (s *Server) devices() *auth.Devices { return auth.NewDevices(s.DB) }

// accountURL is the employee's page in Authentik: the password and two-factor authentication are set there.
func (s *Server) accountURL() string {
	if s.OIDC == nil {
		return ""
	}
	return s.OIDC.AccountURL()
}

// deviceRow is a session in the profile's «Безопасность»: the browser and the system, the address, when it began and
// was last used; Current — this very browser.
type deviceRow struct {
	auth.Device
	Browser string
	Current bool
}

// profileDevices are the person's live sessions for «Безопасность» (the person themselves or an administrator).
func (s *Server) profileDevices(r *http.Request, sess auth.Session, id string) ([]deviceRow, error) {
	list, err := s.devices().List(r.Context(), id)
	if err != nil {
		return nil, err
	}
	rows := make([]deviceRow, len(list))
	for i, d := range list {
		rows[i] = deviceRow{Device: d, Browser: audit.Browser(d.Agent), Current: d.ID == sess.SID}
	}
	return rows, nil
}

// endSessions is «Завершить» of a session (sid) or «Выйти на всех устройствах» (no sid) in the profile's
// «Безопасность»: the employee ends their own sessions — all but this one, — an administrator anyone's,
// all of them. Ending this very session is signing out.
func (s *Server) endSessions(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, sid := r.PathValue("id"), r.PathValue("sid")
	me := id == sess.UserID
	if !me && !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := "/u/" + id + "?tab=security"
	var name string
	if err := s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id::text = $1`, id).Scan(&name); err != nil {
		s.taskErr(w, r, tasks.ErrNotFound, "/company")
		return
	}
	msg := "sessions.ended_all"
	if sid != "" {
		ok, err := s.devices().End(r.Context(), id, sid)
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		if !ok {
			s.taskErr(w, r, tasks.ErrNotFound, back)
			return
		}
		s.audit(r, audit.Event{Kind: "session_ended", UserID: sess.UserID, Target: name, TargetID: id})
		if sid == sess.SID { // this very browser: signed out
			s.Sessions.Clear(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		msg = "sessions.ended"
	} else {
		except := ""
		if me {
			except = sess.SID
		}
		if _, err := s.devices().EndAll(r.Context(), id, except); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		s.audit(r, audit.Event{Kind: "sessions_ended", UserID: sess.UserID, Target: name, TargetID: id})
		if me && sess.SID == "" { // a cookie of before the sessions were kept is gone too
			s.Sessions.Clear(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, msg)), http.StatusSeeOther)
}
