package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

type sessionKey struct{}

// withSession keeps the session requireUser checked (an administrator made in go24 is known only there) for the
// page's rendering.
func withSession(ctx context.Context, sess auth.Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, sess)
}

// session is the request's session: the one requireUser checked, else the cookie's.
func (s *Server) session(r *http.Request) (auth.Session, error) {
	if sess, ok := r.Context().Value(sessionKey{}).(auth.Session); ok {
		return sess, nil
	}
	return s.Sessions.Get(r)
}

// Admin is a line of «Администраторы»: Group — a member of an administrators' group of Authentik (taken away there
// only), Granted — made in go24 by By at At.
type Admin struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Email   string     `json:"email"`
	Group   bool       `json:"group"`
	Granted bool       `json:"granted"`
	By      string     `json:"by,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

// admins lists the administrators of go24: the working employees of the groups GO24_ADMIN_GROUPS and those made here.
func (s *Server) admins(ctx context.Context) ([]Admin, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT u.id, u.name, u.email, u.groups && $1::text[], u.admin, COALESCE(b.name, ''), u.admin_at
		FROM users u LEFT JOIN users b ON b.id = u.admin_by
		WHERE u.active AND NOT u.guest AND (u.admin OR u.groups && $1::text[])
		ORDER BY u.name, u.email`, s.AdminGroups)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Admin])
	if list == nil {
		list = []Admin{}
	}
	return list, err
}

// grantAdmin makes an employee an administrator (a guest or a dismissed one — not); one already an administrator
// stays as is.
func (s *Server) grantAdmin(r *http.Request, by, userID string) error {
	ctx := r.Context()
	var name string
	err := s.DB.QueryRow(ctx, `
		UPDATE users SET admin = true, admin_by = $2, admin_at = now()
		WHERE id::text = $1 AND active AND NOT guest AND NOT admin AND NOT groups && $3::text[] RETURNING name`,
		userID, by, s.AdminGroups).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		var guest bool
		if err := s.DB.QueryRow(ctx, `SELECT guest FROM users WHERE id::text = $1 AND active`, userID).Scan(&guest); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return tasks.ErrNotFound
			}
			return err
		}
		if guest {
			return i18n.E("admins.err_guest")
		}
		return nil
	}
	if err != nil {
		return err
	}
	s.audit(r, audit.Event{Kind: "admin_granted", UserID: by, Target: name, TargetID: userID})
	return nil
}

// revokeAdmin takes away the administrator made in go24; a member of an administrators' group is one until removed
// from it in Authentik, and nobody takes it from themselves (somebody else must, so that go24 is not left without).
func (s *Server) revokeAdmin(r *http.Request, by, userID string) error {
	ctx := r.Context()
	if userID == by {
		return i18n.E("admins.err_self")
	}
	var name string
	err := s.DB.QueryRow(ctx, `UPDATE users SET admin = false, admin_by = NULL, admin_at = NULL
		WHERE id::text = $1 AND admin RETURNING name`, userID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		var group bool
		if err := s.DB.QueryRow(ctx, `SELECT groups && $2::text[] FROM users WHERE id::text = $1`, userID, s.AdminGroups).Scan(&group); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return tasks.ErrNotFound
			}
			return err
		}
		if group {
			return i18n.E("admins.err_group")
		}
		return nil
	}
	if err != nil {
		return err
	}
	s.audit(r, audit.Event{Kind: "admin_revoked", UserID: by, Target: name, TargetID: userID})
	return nil
}

// adminsPage is «Администраторы» of «Настройки» (administrators), as «Права доступа → Администраторы»:
// who they are, «Добавить администратора» and «Снять».
func (s *Server) adminsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	list, err := s.admins(ctx)
	if err != nil {
		s.fail(w, "admins", err)
		return
	}
	users, err := s.Tasks.ListUsers(tasks.WithAdmin(ctx))
	if err != nil {
		s.fail(w, "admins users", err)
		return
	}
	staff := users[:0:0] // guests are never administrators
	for _, u := range users {
		if !u.Guest {
			staff = append(staff, u)
		}
	}
	s.render(w, r, "admins", page{TitleKey: "admins.title", Nav: "settings", Data: map[string]any{
		"Admins": list, "Users": staff, "Groups": s.AdminGroups}})
}

// adminsAdd: POST /settings/admins (user=ID) — «Добавить администратора».
func (s *Server) adminsAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.grantAdmin(r, sess.UserID, r.FormValue("user")); err != nil {
		s.taskErr(w, r, err, "/settings/admins")
		return
	}
	http.Redirect(w, r, "/settings/admins?msg="+url.QueryEscape(s.tr(r, "admins.added")), http.StatusSeeOther)
}

// adminsRemove: POST /settings/admins/{id}/remove — «Снять».
func (s *Server) adminsRemove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.revokeAdmin(r, sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/settings/admins")
		return
	}
	http.Redirect(w, r, "/settings/admins?msg="+url.QueryEscape(s.tr(r, "admins.removed")), http.StatusSeeOther)
}

// apiAdmins: GET /api/v1/settings/admins — the list; POST {"user_id"} — make one; DELETE …/{id} — take away.
// Administrators' keys only.
func (s *Server) apiAdmins(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	switch r.Method {
	case http.MethodPost:
		var in struct {
			UserID string `json:"user_id"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.grantAdmin(r, p.UserID, in.UserID); err != nil {
			return err
		}
	case http.MethodDelete:
		if err := s.revokeAdmin(r, p.UserID, r.PathValue("id")); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	list, err := s.admins(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"admins": list})
	return nil
}
