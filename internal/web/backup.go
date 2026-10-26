package web

import (
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/backup"
	"go24/internal/i18n"
)

// backupPage is «Резервное копирование» of «Настройки» (administrators): what is copied and where to, the backups'
// password, «Сделать сейчас» and the journal of the copies.
func (s *Server) backupPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Backup == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	st, err := s.Backup.Settings(ctx)
	if err != nil {
		s.fail(w, "backup settings", err)
		return
	}
	runs, err := s.Backup.Runs(ctx, 50)
	if err != nil {
		s.fail(w, "backup runs", err)
		return
	}
	busy := false
	for _, run := range runs {
		busy = busy || run.Status == "queued" || run.Status == "running"
	}
	var next *time.Time
	if n := st.Next(time.Now()); !n.IsZero() {
		next = &n
	}
	s.render(w, r, "backup", page{TitleKey: "backup.title", Nav: "settings", Data: map[string]any{
		"Set": st, "Parts": backup.Parts, "Runs": runs, "Busy": busy, "Host": s.BackupHostDir, "Next": next,
		"Weekdays": []int{1, 2, 3, 4, 5, 6, 7}, "Types": backup.TargetTypes, "RC": s.BackupRC != nil}})
}

// backupSave: POST /settings/backup (dir, parts, password, nopassword) — the settings.
func (s *Server) backupSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Backup == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/settings/backup")
		return
	}
	st := backup.Settings{Dir: r.FormValue("dir"), Parts: r.Form["parts"], Schedule: r.FormValue("schedule"), At: r.FormValue("at"),
		Target: backup.Target{Type: r.FormValue("t_type"), Host: r.FormValue("t_host"), Port: r.FormValue("t_port"), User: r.FormValue("t_user"),
			Domain: r.FormValue("t_domain"), Endpoint: r.FormValue("t_endpoint"), Region: r.FormValue("t_region"), URL: r.FormValue("t_url"),
			Path: r.FormValue("t_path")}}
	st.Weekday, _ = strconv.Atoi(r.FormValue("weekday"))
	if keep := strings.TrimSpace(r.FormValue("keep")); keep == "" {
		st.Keep = backup.DefaultKeep
	} else if n, err := strconv.Atoi(keep); err == nil {
		st.Keep = n
	} else {
		st.Keep = -1 // refused as not a number of copies
	}
	sec := backup.Secrets{Password: r.FormValue("password"), Clear: r.FormValue("nopassword") == "1", TargetPassword: r.FormValue("t_password")}
	if err := s.Backup.SaveSettings(r.Context(), sess.UserID, st, sec); err != nil {
		s.taskErr(w, r, err, "/settings/backup")
		return
	}
	s.audit(r, audit.Event{Kind: "backup_saved", UserID: sess.UserID, Target: strings.Join(r.Form["parts"], ", ")})
	http.Redirect(w, r, "/settings/backup?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// backupCheck: POST /settings/backup/check — «Проверить подключение» to the outside place of the saved settings.
func (s *Server) backupCheck(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Backup == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Backup.Check(r.Context(), s.BackupRC); err != nil {
		s.taskErr(w, r, err, "/settings/backup")
		return
	}
	http.Redirect(w, r, "/settings/backup?msg="+url.QueryEscape(s.tr(r, "backup.check_ok")), http.StatusSeeOther)
}

// backupRun: POST /settings/backup/run — «Сделать сейчас».
func (s *Server) backupRun(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Backup == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if _, err := s.Backup.Queue(r.Context(), sess.UserID); err != nil {
		s.taskErr(w, r, err, "/settings/backup")
		return
	}
	s.audit(r, audit.Event{Kind: "backup_started", UserID: sess.UserID})
	http.Redirect(w, r, "/settings/backup?msg="+url.QueryEscape(s.tr(r, "backup.queued")), http.StatusSeeOther)
}

// backupRestorePage: GET /settings/backup/{id}/restore — what of the copy to put back, its password, the confirmation.
func (s *Server) backupRestorePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Backup == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c, err := s.Backup.Copy(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/settings/backup")
		return
	}
	var parts []string
	for _, p := range backup.Restorable {
		if slices.Contains(c.Parts, p) {
			parts = append(parts, p)
		}
	}
	s.render(w, r, "backup_restore", page{TitleKey: "backup.restore.title", Nav: "settings", Data: map[string]any{
		"Copy": c, "Base": path.Base(c.Name), "Parts": parts, "Secrets": slices.Contains(c.Parts, "secrets"),
		"Media": slices.Contains(c.Parts, "media"), "Host": s.BackupHostDir}})
}

// backupRestore: POST /settings/backup/{id}/restore (parts, password, safety, confirm — the copy's folder typed again)
// — go24-worker restores from the copy.
func (s *Server) backupRestore(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Backup == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	back := "/settings/backup/" + strconv.FormatInt(id, 10) + "/restore"
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	ctx := r.Context()
	c, err := s.Backup.Copy(ctx, id)
	if err != nil {
		s.taskErr(w, r, err, "/settings/backup")
		return
	}
	if strings.TrimSpace(r.FormValue("confirm")) != path.Base(c.Name) {
		s.taskErr(w, r, i18n.E("backup.err_confirm"), back)
		return
	}
	if _, err := s.Backup.QueueRestore(ctx, sess.UserID, id, r.Form["parts"], r.FormValue("password"), r.FormValue("safety") == "1"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.audit(r, audit.Event{Kind: "backup_restore", UserID: sess.UserID, Target: path.Base(c.Name) + ": " + strings.Join(r.Form["parts"], ", ")})
	http.Redirect(w, r, "/settings/backup?msg="+url.QueryEscape(s.tr(r, "backup.restore.queued")), http.StatusSeeOther)
}

// apiBackup: GET /api/v1/settings/backup — the settings (without the password) and the journal; POST …/run — a copy
// now (202 with its id). Administrators' keys only.
func (s *Server) apiBackup(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) || s.Backup == nil {
		return i18n.E("err.forbidden")
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		id, err := s.Backup.Queue(ctx, p.UserID)
		if err != nil {
			return err
		}
		s.audit(r, audit.Event{Kind: "backup_started", UserID: p.UserID})
		writeJSON(w, http.StatusAccepted, map[string]any{"id": id})
		return nil
	}
	st, err := s.Backup.Settings(ctx)
	if err != nil {
		return err
	}
	runs, err := s.Backup.Runs(ctx, 50)
	if err != nil {
		return err
	}
	out := map[string]any{"dir": st.Dir, "parts": st.Parts, "password": st.HasPassword(), "folder": s.BackupHostDir,
		"schedule": st.Schedule, "at": st.At, "weekday": st.Weekday, "keep": st.Keep, "runs": runs, "target": st.Target.Type}
	if n := st.Next(time.Now()); !n.IsZero() {
		out["next"] = n
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
