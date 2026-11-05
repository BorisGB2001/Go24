package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/sysinfo"
	"go24/internal/update"
)

// updateState is «Обновление» as the page and the API show it.
type updateState struct {
	Version  string               `json:"version"`
	Enabled  bool                 `json:"enabled"` // false — a development build: updates come from git
	Latest   *update.Release      `json:"latest,omitempty"`
	Newer    bool                 `json:"newer"` // the latest release is newer than this one
	Busy     bool                 `json:"busy"`
	Status   *update.Status       `json:"status,omitempty"`
	Rollback *update.RollbackMeta `json:"rollback,omitempty"`
}

func (s *Server) updateState() updateState {
	st := updateState{Version: sysinfo.Version}
	u := s.Update
	if u == nil {
		return st
	}
	st.Enabled = u.Enabled()
	st.Latest, _ = u.Latest()
	st.Newer = st.Latest != nil && update.Newer(st.Latest.Version, sysinfo.Version)
	st.Busy = u.Busy()
	if j, err := update.ReadStatus(u.Dir); err == nil && j.ID != "" {
		st.Status = &j
	}
	st.Rollback, _ = update.ReadRollback(u.Dir)
	return st
}

// updateSteps are the steps of go24-updater (Status.Step).
var updateSteps = []string{"verify", "unpack", "backup", "files", "env", "matrix", "start", "health", "rollback"}

// updatePage: GET /settings/update — «Обновление» (administrators): this version, the latest on GitHub, its changes,
// «Проверить», «Обновить», «Откатить» and what go24-updater does, live.
func (s *Server) updatePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	states, steps := map[string]string{}, map[string]string{}
	for _, k := range []string{update.Installing, update.OK, update.RolledBack, update.Failed} {
		states[k] = s.tr(r, "update.state."+k)
	}
	for _, k := range updateSteps {
		steps[k] = s.tr(r, "update.step."+k)
	}
	s.render(w, r, "update", page{TitleKey: "update.title", Nav: "settings", Data: map[string]any{
		"S": s.updateState(), "Msg": r.URL.Query().Get("msg"), "Err": r.URL.Query().Get("err"), "States": states, "Steps": steps}})
}

// updateStatus: GET /settings/update/status — the state for the page's live view.
func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, s.updateState())
}

// updateAction: POST /settings/update/{action} — check, install, rollback.
func (s *Server) updateAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Update == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	msg := ""
	switch r.PathValue("action") {
	case "check":
		if !s.Update.Enabled() {
			err = update.ErrDev
			break
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		_, err = s.Update.Check(ctx)
		cancel()
		msg = "update.checked"
	case "install":
		var req update.Request
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		req, err = s.Update.Update(ctx, who(sess))
		cancel()
		if err == nil {
			s.audit(r, audit.Event{Kind: "update_started", UserID: sess.UserID, Target: req.Version})
			msg = "update.started"
		}
	case "rollback":
		var req update.Request
		req, err = s.Update.Rollback(who(sess))
		if err == nil {
			s.audit(r, audit.Event{Kind: "update_rollback", UserID: sess.UserID, Target: req.Version})
			msg = "update.rollback_started"
		}
	default:
		http.NotFound(w, r)
		return
	}
	q := url.Values{}
	if err != nil {
		q.Set("err", s.updateErr(r, err))
	} else {
		q.Set("msg", s.tr(r, msg))
	}
	http.Redirect(w, r, "/settings/update?"+q.Encode(), http.StatusSeeOther)
}

// updateErr is an error of the page's actions in the reader's language.
func (s *Server) updateErr(r *http.Request, err error) string {
	for _, e := range []error{update.ErrDev, update.ErrBusy, update.ErrNotNewer, update.ErrNoRelease, update.ErrNoRollback} {
		if errors.Is(err, e) {
			return s.tr(r, e.Error())
		}
	}
	switch {
	case errors.Is(err, update.ErrSignature):
		return s.tr(r, "update.err.signature")
	case errors.Is(err, update.ErrNoKey):
		return s.tr(r, "update.err.no_key")
	}
	return s.tr(r, "update.err.other", err.Error())
}

// who is the person for go24-updater's log.
func who(sess auth.Session) string {
	if sess.Name != "" {
		return sess.Name
	}
	return sess.UserID
}

// apiUpdate: GET /api/v1/settings/update — «Обновление» (administrators).
func (s *Server) apiUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	writeJSON(w, http.StatusOK, s.updateState())
	return nil
}

// AnnounceUpdates tells the administrators (the bell, and the messenger by their settings) how a job of go24-updater
// ended: updated to X.Y.Z, rolled back and why, failed. It looks once a minute, until ctx is done.
func (s *Server) AnnounceUpdates(ctx context.Context) {
	for t := time.NewTicker(time.Minute); ; {
		if st, ok, err := s.Update.Announce(); err != nil {
			s.Log.Warn("update announce", "err", err)
		} else if ok {
			kind, body := "update_done", st.To
			switch {
			case st.State == update.RolledBack:
				kind, body = "update_rolled_back", st.To+" · "+st.Error
			case st.State == update.Failed:
				kind, body = "update_failed", st.Error
			case st.Kind == "rollback":
				kind = "update_reverted"
			}
			if r := []rune(body); len(r) > 300 {
				body = string(r[:300]) + "…"
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
				SELECT id, $1, $2, '/settings/update' FROM users WHERE active AND NOT guest AND (admin OR groups && $3::text[])`,
				kind, body, s.AdminGroups); err != nil {
				s.Log.Warn("update announce", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
