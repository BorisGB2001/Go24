package web

import (
	"bytes"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/office"
	"go24/internal/tasks"
)

// ---------- «Прикрепить» of the messenger: a file of the drive, a new document ----------

// imDisk is «Файл с Диска»: a file of a drive the person reads goes into the chat (into a thread, with thread) as
// their message.
func (s *Server) imDisk(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if tasks.IsGuestCtx(r.Context()) {
		s.jsonErr(w, r, i18n.E("err.forbidden"))
		return
	}
	m, err := s.sendToChat(r, sess, r.FormValue("file"), r.FormValue("room"), r.FormValue("thread"))
	s.imAnswer(w, r, m, err, http.StatusCreated)
}

// imDoc is «Документ», «Таблица», «Презентация»: an empty document (kind: docx, xlsx, pptx) is made where the chat's
// people reach it, and goes into the chat as a link to the editor; the answer also has that link for its maker to open.
// A chat of a project (a collab's, a task's) keeps it on the project's drive; any other chat — in «Сохранённые файлы»
// of the maker's «Мой диск», opened to the chat's people for editing.
func (s *Server) imDoc(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	kind, room := r.FormValue("kind"), r.FormValue("room")
	if tasks.IsGuestCtx(ctx) {
		s.jsonErr(w, r, i18n.E("err.forbidden"))
		return
	}
	if s.Office == nil || officeTypes[kind] == "" {
		s.jsonErr(w, r, drive.ErrNotFound)
		return
	}
	if err := s.needMatrix(); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	people, err := s.Matrix.ChatMembers(ctx, sess.UserID, room) // the person is in the chat
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	var d drive.Drive
	if key := s.Matrix.ChatProjectKey(ctx, room); key != "" {
		if pd, err := s.Drive.Open(ctx, sess.UserID, key); err == nil && pd.CanWrite {
			d = pd
		}
	}
	if d.Kind == "" {
		if d, err = s.Drive.Open(ctx, sess.UserID, "my"); err != nil {
			s.jsonErr(w, r, err)
			return
		}
	}
	folder, err := s.driveFolderNamed(r, sess, d, s.tr(r, "im.drive_folder"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	data, err := officeBlank(s.lang(r), kind)
	if err != nil {
		s.fail(w, "office blank", err)
		return
	}
	name, err := s.freeName(r, sess, d, folder, s.tr(r, "office.new_"+kind), kind)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	id, err := s.diskStore(r, sess, d, folder, "", name, officeTypes[kind], bytes.NewReader(data), int64(len(data)))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if d.Kind == "user" {
		var ids []string
		for _, p := range people {
			if p.ID != "" && !p.Me && !p.Guest && !p.Bot {
				ids = append(ids, p.ID)
			}
		}
		if err := s.Drive.ShareFileWith(ctx, sess.UserID, d, id, ids, "edit"); err != nil {
			s.jsonErr(w, r, err)
			return
		}
	}
	open := "/files/" + id + "/open"
	m, err := s.Matrix.Reply(ctx, sess.UserID, room, s.tr(r, "im.doc_made", strings.TrimRight(s.BaseURL, "/")+open, name), r.FormValue("thread"), "")
	var html string
	if err == nil {
		html, err = s.imFragment(r, "immsg", m)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": m.ID, "html": html, "url": open})
}

// freeName is base.ext, or «base 2.ext», «base 3.ext»… when the folder has that name already.
func (s *Server) freeName(r *http.Request, sess auth.Session, d drive.Drive, folder, base, ext string) (string, error) {
	for n := 1; ; n++ {
		name := base + "." + ext
		if n > 1 {
			name = base + " " + strconv.Itoa(n) + "." + ext
		}
		same, err := s.Drive.Same(r.Context(), sess.UserID, d, folder, name)
		if err != nil || same == "" {
			return name, err
		}
	}
}

// fileOpen opens a file of a drive as the person reaches it: a document — in the editor of the drive it lies on, or
// as a file shared with them; anything else, or a document they reach otherwise, — its card.
func (s *Server) fileOpen(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	m, err := s.fileAccess(r, sess, id)
	if err == nil && m.Drive == "" {
		err = drive.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	to := "/files/" + id + "/about"
	if k := office.Kind(m.Name); s.Office != nil && k != "" && k != "pdf" {
		c, err := s.Drive.FileCard(r.Context(), id)
		if err != nil && !errors.Is(err, drive.ErrNotFound) {
			s.taskErr(w, r, err, "/disk/my")
			return
		}
		switch which := cardDrive(c, sess.UserID); {
		case err != nil:
		case which == "my" || which == "company":
			to = "/disk/" + which + "/edit/" + id
		case c.Drive == "project":
			to = "/disk/p/" + which + "/edit/" + id
		default:
			if _, err := s.Drive.Open(r.Context(), sess.UserID, "sf:"+id); err == nil { // shared with them by itself
				to = "/disk/sf/" + id + "/edit/" + id
			}
		}
	}
	http.Redirect(w, r, to, http.StatusFound)
}
