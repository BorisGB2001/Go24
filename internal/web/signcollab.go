package web

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"go24/internal/auth"
	"go24/internal/sign"
	"go24/internal/tasks"
)

// ---------- «Документ на подпись» of a collab: its guest signs as the client ----------

// signCollab is the collab a document is sent for signing from: its employee sends (a guest does not), not from the
// archive.
func (s *Server) signCollab(ctx context.Context, sess auth.Session, key string) (tasks.Project, error) {
	if tasks.IsGuestCtx(ctx) {
		return tasks.Project{}, tasks.ErrNotFound
	}
	p, err := s.Tasks.ProjectByKey(ctx, sess.UserID, key)
	if err != nil {
		return p, err
	}
	if !p.Collab() {
		return tasks.Project{}, tasks.ErrNotFound
	}
	if p.Archived {
		return tasks.Project{}, tasks.ErrForbidden
	}
	return p, nil
}

// signCollabGuests are the collab's guests with an e-mail: whom the form offers as the client.
func (s *Server) signCollabGuests(ctx context.Context, p tasks.Project) []tasks.User {
	members, err := s.Tasks.Members(ctx, p.ID)
	if err != nil {
		s.Log.Warn("sign collab guests", "err", err)
		return nil
	}
	var out []tasks.User
	for _, m := range members {
		if m.Guest && strings.Contains(m.Email, "@") {
			out = append(out, m.User)
		}
	}
	return out
}

// signCollabSay writes into the collab's chat as the sender: the document's being sent, signed or refused.
func (s *Server) signCollabSay(ctx context.Context, d sign.Doc, body string) {
	if d.ProjectID == "" || s.Matrix == nil {
		return
	}
	room, err := s.Matrix.ProjectRoom(ctx, d.ProjectID)
	if err == nil && room != "" {
		_, err = s.Matrix.Send(ctx, d.AuthorID, room, body, "")
	}
	if err != nil {
		s.Log.Warn("sign collab chat", "doc", d.ID, "err", err)
	}
}

// signCollabSent tells the collab's chat the document went for signing and to whom.
func (s *Server) signCollabSent(ctx context.Context, lang string, d sign.Doc) {
	var who []string
	for _, p := range d.Parties {
		who = append(who, p.Name)
	}
	s.signCollabSay(ctx, d, s.I18n.T(lang, "sign.collab.sent", d.Title, strings.Join(who, ", "), s.BaseURL+"/sign/"+d.ID))
}

// signCollabDone puts the signed document on the collab's drive and into its chat, as the sender.
func (s *Server) signCollabDone(r *http.Request, d sign.Doc, name, contentType string, data []byte) {
	if d.ProjectID == "" {
		return
	}
	ctx := r.Context()
	s.signCollabSay(ctx, d, s.tr(r, "sign.collab.done", d.Title))
	var key string
	if err := s.DB.QueryRow(ctx, `SELECT key FROM projects WHERE id::text = $1`, d.ProjectID).Scan(&key); err != nil {
		s.Log.Warn("sign collab", "doc", d.ID, "err", err)
		return
	}
	if s.Drive == nil {
		return
	}
	if dr, err := s.Drive.Open(ctx, d.AuthorID, key); err == nil && dr.CanWrite {
		_, err = s.diskStore(r, auth.Session{UserID: d.AuthorID}, dr, "", "", name, contentType, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			s.Log.Warn("sign collab drive", "doc", d.ID, "err", err)
		}
	}
	if s.Matrix == nil {
		return
	}
	room, err := s.Matrix.ProjectRoom(ctx, d.ProjectID)
	if err == nil && room != "" {
		_, err = s.Matrix.SendFile(ctx, d.AuthorID, room, "", name, contentType, bytes.NewReader(data), int64(len(data)), nil)
	}
	if err != nil {
		s.Log.Warn("sign collab chat file", "doc", d.ID, "err", err)
	}
}

// signCollabSees: a document sent from a collab is seen by its employees too — its chat links to it.
func (s *Server) signCollabSees(r *http.Request, d sign.Doc) bool {
	if d.ProjectID == "" || tasks.IsGuestCtx(r.Context()) {
		return false
	}
	var ok bool
	_ = s.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id::text = $1 AND user_id = $2)`,
		d.ProjectID, tasks.Viewer(r.Context())).Scan(&ok)
	return ok
}
