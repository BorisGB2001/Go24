package web

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/kb"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «База знаний» ----------

// kbHome: the spaces, the search over their pages, a new space.
func (s *Server) kbHome(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	spaces, err := s.KB.Spaces(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	q := r.URL.Query().Get("q")
	hits, err := s.KB.Search(r.Context(), sess.UserID, q, "")
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	templates, err := s.KB.Templates(r.Context(), sess.UserID, s.trFunc(r))
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	favs, err := s.KB.Favorites(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	recent, err := s.KB.Recent(r.Context(), sess.UserID, 10)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "kb", page{TitleKey: "kb.title", Nav: "kb", Data: map[string]any{"Spaces": spaces, "Q": q, "Hits": hits, "Projects": projects,
		"Templates": templates, "Template": r.URL.Query().Get("template"), "Favs": favs, "Recent": recent}})
}

// kbMark stars («В избранное», kind=fav) or watches («Следить», kind=watch) the base or, with page, a page of it;
// on=0 takes it back.
func (s *Server) kbMark(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	spaceID, pageID := r.PathValue("space"), r.FormValue("page")
	back := "/kb/" + spaceID
	if pageID != "" {
		back += "/" + pageID
	}
	if err := s.KB.SetMark(r.Context(), sess.UserID, r.FormValue("kind"), spaceID, pageID, r.FormValue("on") != "0"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// trFunc translates into the language of the request.
func (s *Server) trFunc(r *http.Request) func(string) string {
	return func(key string) string { return s.tr(r, key) }
}

// kbCreateSpace makes a base, empty or from a template («Шаблоны» in the form of a new base).
func (s *Server) kbCreateSpace(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var id string
	var err error
	if tpl := r.FormValue("template"); tpl != "" {
		id, err = s.KB.CreateFromTemplate(r.Context(), sess.UserID, tpl, r.FormValue("name"), r.FormValue("description"), r.FormValue("project"),
			s.trFunc(r), s.Storages.Copy)
	} else {
		id, err = s.KB.CreateSpace(r.Context(), sess.UserID, r.FormValue("name"), r.FormValue("description"), r.FormValue("project"))
	}
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	http.Redirect(w, r, "/kb/"+id, http.StatusSeeOther)
}

func (s *Server) kbDeleteSpace(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.KB.DeleteSpace(r.Context(), sess.UserID, r.PathValue("space")); err != nil {
		s.taskErr(w, r, err, "/kb/"+r.PathValue("space"))
		return
	}
	http.Redirect(w, r, "/kb?msg="+url.QueryEscape(s.tr(r, "kb.space_trashed")), http.StatusSeeOther)
}

// kbSpace shows a space: its tree and its first page (or the form of a new page with ?new=1, the pages found in
// the space with ?q=).
func (s *Server) kbSpace(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sp, err := s.KB.Space(r.Context(), sess.UserID, r.PathValue("space"))
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	tree, err := s.KB.Tree(r.Context(), sp.ID)
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		hits, err := s.KB.Search(r.Context(), sess.UserID, q, sp.ID)
		if err != nil {
			s.taskErr(w, r, err, "/kb/"+sp.ID)
			return
		}
		s.render(w, r, "kbspace", page{Title: sp.Name, Nav: "kb", Data: map[string]any{"Space": sp, "Tree": tree, "Q": q, "Hits": hits}})
		return
	}
	if len(tree) > 0 && r.URL.Query().Get("new") == "" {
		http.Redirect(w, r, "/kb/"+sp.ID+"/"+tree[0].ID, http.StatusFound)
		return
	}
	marks, err := s.KB.MarksAt(r.Context(), sess.UserID, sp.ID, "")
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	parent, under := r.URL.Query().Get("parent"), ""
	if path := kb.Path(tree, parent); len(path) > 0 {
		under = path[len(path)-1].Title
	}
	s.render(w, r, "kbspace", page{Title: sp.Name, Nav: "kb", Data: map[string]any{"Space": sp, "Tree": tree,
		"New": sp.CanEdit, "Parent": parent, "Under": under, "Crumbs": kb.Path(tree, parent), "Marks": marks}})
}

func (s *Server) kbCreatePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/kb/" + r.PathValue("space")
	id, err := s.KB.CreatePage(r.Context(), sess.UserID, r.PathValue("space"), r.FormValue("parent"), r.FormValue("title"), r.FormValue("body"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.feedKBPage(r.Context(), sess.UserID, id)
	http.Redirect(w, r, back+"/"+id, http.StatusSeeOther)
}

// kbPage shows a page (?edit=1 — its editor, ?v=N — an earlier version).
func (s *Server) kbPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, sp, err := s.KB.Page(r.Context(), sess.UserID, r.PathValue("page"))
	if err == nil && sp.ID != r.PathValue("space") {
		err = kb.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	var old *kb.Page
	if v, perr := strconv.ParseInt(r.URL.Query().Get("v"), 10, 64); perr == nil {
		ov, err := s.KB.Version(r.Context(), sess.UserID, p.ID, v)
		if err != nil {
			s.taskErr(w, r, err, "/kb/"+sp.ID+"/"+p.ID)
			return
		}
		old = &ov
	}
	tree, err := s.KB.Tree(r.Context(), sp.ID)
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	versions, err := s.KB.Versions(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	var diff []kb.DiffLine
	if old != nil && r.URL.Query().Get("diff") == "1" { // «Сравнить с текущей»
		diff = kb.Diff("# "+old.Title+"\n"+old.Body, "# "+p.Title+"\n"+p.Body)
	}
	var targets []kb.Target
	if sp.CanEdit {
		if targets, err = s.KB.Targets(r.Context(), sess.UserID, p.ID); err != nil {
			s.taskErr(w, r, err, "/kb")
			return
		}
	}
	var pub string // the outside link of the page, if published
	if sp.CanEdit {
		if pub, err = s.KB.PageLink(r.Context(), sp.ID, p.ID); err != nil {
			s.taskErr(w, r, err, "/kb")
			return
		}
	}
	marks, err := s.KB.MarksAt(r.Context(), sess.UserID, sp.ID, p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	var talk kb.Discussion
	if old == nil { // the discussion under the page, its views with this one
		if err := s.KB.Viewed(r.Context(), sess.UserID, p.ID); err != nil {
			s.Log.Warn("kb view", "err", err)
		}
		if talk, err = s.KB.Discussion(r.Context(), sess.UserID, p, sp); err != nil {
			s.taskErr(w, r, err, "/kb")
			return
		}
	}
	var crumbs, children []*kb.Page // the parents above the page and the pages right under it
	sub := 0
	if path := kb.Path(tree, p.ID); len(path) > 0 {
		crumbs, children, sub = path[:len(path)-1], path[len(path)-1].Children, kb.Descendants(path[len(path)-1])
	}
	s.render(w, r, "kbspace", page{Title: p.Title, Nav: "kb", Data: map[string]any{"Space": sp, "Tree": tree, "Page": p, "Old": old,
		"Diff": diff, "Targets": targets, "Version": r.URL.Query().Get("v"), "Edit": r.URL.Query().Get("edit") == "1" && sp.CanEdit, "Versions": versions,
		"Crumbs": crumbs, "Children": children, "Sub": sub, "TOC": kb.Headings(p.Body), "Pub": pub, "Base": s.BaseURL, "Marks": marks, "Talk": talk}})
}

func (s *Server) kbSavePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/kb/" + r.PathValue("space") + "/" + r.PathValue("page")
	if err := s.KB.SavePage(r.Context(), sess.UserID, r.PathValue("page"), r.FormValue("title"), r.FormValue("body")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "kb.saved")), http.StatusSeeOther)
}

func (s *Server) kbDeletePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	space, err := s.KB.DeletePage(r.Context(), sess.UserID, r.PathValue("page"))
	if err != nil {
		s.taskErr(w, r, err, "/kb/"+r.PathValue("space")+"/"+r.PathValue("page"))
		return
	}
	s.withdraw(r.Context(), feed.SourceKB, r.PathValue("page"))
	http.Redirect(w, r, "/kb/"+space+"?msg="+url.QueryEscape(s.tr(r, "kb.page_trashed")), http.StatusSeeOther)
}

// kbTrash: «Корзина» of the knowledge base — the deleted bases and pages the user may restore, with who and when
// deleted them and when they go for good (the company setting of the trash).
func (s *Server) kbTrash(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	days, err := s.Tasks.TrashDays(r.Context())
	if err != nil {
		s.fail(w, "kb", err)
		return
	}
	list, err := s.KB.Trash(r.Context(), sess.UserID, days)
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	s.render(w, r, "kbtrash", page{TitleKey: "trash.title", Nav: "kb", Data: map[string]any{"Items": list}})
}

// kbTrashAct restores (act=restore) or deletes for good (act=purge) a base in the trash or, with page, a page.
func (s *Server) kbTrashAct(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	space, pg, ctx := r.PathValue("space"), r.FormValue("page"), r.Context()
	var err error
	to := "/kb/trash"
	switch {
	case r.FormValue("act") == "restore" && pg != "":
		space, err = s.KB.RestorePage(ctx, sess.UserID, pg)
		to = "/kb/" + space + "/" + pg
	case r.FormValue("act") == "restore":
		err = s.KB.RestoreSpace(ctx, sess.UserID, space)
		to = "/kb/" + space
	case pg != "":
		err = s.KB.PurgePage(ctx, sess.UserID, pg)
	default:
		err = s.KB.PurgeSpace(ctx, sess.UserID, space)
	}
	if err != nil {
		s.taskErr(w, r, err, "/kb/trash")
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// kbSettings: «Настройки» of a space — its name, description and «Права доступа» (those who manage it).
func (s *Server) kbSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sp, err := s.KB.Space(r.Context(), sess.UserID, r.PathValue("space"))
	if err == nil && !sp.CanManage {
		err = kb.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	rights, err := s.KB.Rights(r.Context(), sp.ID)
	if err != nil {
		s.taskErr(w, r, err, "/kb/"+sp.ID)
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/kb/"+sp.ID)
		return
	}
	users = slices.DeleteFunc(users, func(u tasks.User) bool { return u.Guest })
	depts, err := s.Drive.Departments(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/kb/"+sp.ID)
		return
	}
	links, err := s.KB.PublicLinks(r.Context(), sp.ID)
	if err != nil {
		s.taskErr(w, r, err, "/kb/"+sp.ID)
		return
	}
	s.render(w, r, "kbsettings", page{Title: sp.Name, Nav: "kb", Data: map[string]any{"Space": sp, "Rights": rights, "Links": links, "Base": s.BaseURL,
		"Users": users, "Departments": depts, "Accesses": kb.Accesses}})
}

// kbSaveTemplate: «Сохранить как шаблон» in the settings of a base.
func (s *Server) kbSaveTemplate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/kb/" + r.PathValue("space") + "/settings"
	if _, err := s.KB.SaveTemplate(r.Context(), sess.UserID, r.PathValue("space"), r.FormValue("name"), r.FormValue("description"), s.Storages.Copy); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "kb.template_saved")), http.StatusSeeOther)
}

func (s *Server) kbDeleteTemplate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.KB.DeleteTemplate(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	http.Redirect(w, r, "/kb?msg="+url.QueryEscape(s.tr(r, "kb.template_deleted")), http.StatusSeeOther)
}

func (s *Server) kbSaveSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/kb/" + r.PathValue("space") + "/settings"
	if err := s.KB.UpdateSpace(r.Context(), sess.UserID, r.PathValue("space"), r.FormValue("name"), r.FormValue("description")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "kb.settings_saved")), http.StatusSeeOther)
}

// kbRights changes «Права доступа» of a space: opens it to everyone, an employee or a department
// (whom: all | user:ID | department:ID) with an access (read | edit | full), takes a line away (off: its id) or all
// of them (clear).
func (s *Server) kbRights(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	space, back := r.PathValue("space"), "/kb/"+r.PathValue("space")+"/settings"
	var err error
	switch kind, id, _ := strings.Cut(r.FormValue("whom"), ":"); {
	case r.FormValue("clear") != "":
		err = s.KB.ClearRights(r.Context(), sess.UserID, space)
	case r.FormValue("off") != "":
		err = s.KB.DeleteRight(r.Context(), sess.UserID, space, r.FormValue("off"))
	default:
		err = s.KB.SetRight(r.Context(), sess.UserID, space, kind, id, r.FormValue("access"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if _, err := s.KB.Space(r.Context(), sess.UserID, space); err != nil { // they closed the space to themselves
		back = "/kb"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// kbMovePage puts a page into another place (to: SPACE or SPACE:PAGE — under that page), moves it up or down among
// its neighbours (dir: up | down), puts it where it was dragged in the tree (place: parent, before) or makes a copy
// of it.
func (s *Server) kbMovePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	space, id := r.PathValue("space"), r.PathValue("page")
	back := "/kb/" + space + "/" + id
	var err error
	switch r.PathValue("act") {
	case "move":
		to, parent, _ := strings.Cut(r.FormValue("to"), ":")
		if err = s.KB.MovePage(r.Context(), sess.UserID, id, to, parent); err == nil {
			back = "/kb/" + to + "/" + id
		}
	case "place": // dragged in the tree
		err = s.KB.PlacePage(r.Context(), sess.UserID, id, r.FormValue("parent"), r.FormValue("before"))
		if wantsJSON(r) {
			if err != nil {
				s.jsonErr(w, r, err)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		}
	case "order":
		err = s.KB.MoveUp(r.Context(), sess.UserID, id, r.FormValue("dir") == "up")
	case "copy":
		var p kb.Page
		if p, _, err = s.KB.Page(r.Context(), sess.UserID, id); err == nil {
			var cp string
			if cp, err = s.KB.CopyPage(r.Context(), sess.UserID, id, s.tr(r, "kb.copy_of", p.Title)); err == nil {
				back = "/kb/" + space + "/" + cp
			}
		}
	case "restore":
		v, perr := strconv.ParseInt(r.FormValue("v"), 10, 64)
		if err = kb.ErrNotFound; perr == nil {
			err = s.KB.RestoreVersion(r.Context(), sess.UserID, id, v)
		}
		if err == nil {
			back += "?msg=" + url.QueryEscape(s.tr(r, "kb.restored"))
		}
	default:
		err = kb.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// kbPreview renders the text being written, for «Предпросмотр» of the page's editor: {"html"} — the same safe
// Markdown the page shows once saved.
func (s *Server) kbPreview(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sp, err := s.KB.Space(r.Context(), sess.UserID, r.PathValue("space"))
	if err == nil && !sp.CanEdit {
		err = kb.ErrForbidden
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	writeJSON(w, http.StatusOK, map[string]any{"html": string(kb.Render(r.FormValue("body")))})
}

// kbUpload stores a picture or a file for a page of the space (those who write in it) and answers with what to put
// into the text: {"name", "url", "image", "md"}.
func (s *Server) kbUpload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sp, err := s.KB.Space(r.Context(), sess.UserID, r.PathValue("space"))
	if err == nil && !sp.CanEdit {
		err = kb.ErrForbidden
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.jsonErr(w, r, i18n.E("err.file_required"))
		return
	}
	defer f.Close()
	up, err := s.kbStoreFile(r.Context(), sess.UserID, sp.ID, f, hdr)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, up)
}

// kbFile is a stored picture or file of a base and what to put into a page's text to show it.
type kbFile struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Image bool   `json:"image"`
	MD    string `json:"md"`
}

// kbStoreFile stores an uploaded picture or file as one of the space's.
func (s *Server) kbStoreFile(ctx context.Context, userID, spaceID string, f io.Reader, hdr *multipart.FileHeader) (kbFile, error) {
	name := path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/"))
	ct := hdr.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" { // a REST client may not say: by the name's extension
		if ct = mime.TypeByExtension(path.Ext(name)); ct == "" {
			ct = "application/octet-stream"
		}
	}
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
	if err := s.Storages.Put(ctx, userID, false, "", key, f, hdr.Size, ct); err != nil {
		return kbFile{}, err
	}
	id, err := s.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: name, ContentType: ct, Size: hdr.Size,
		KBSpaceID: spaceID, UploadedBy: userID})
	if err != nil {
		return kbFile{}, err
	}
	image := (feed.File{ContentType: ct}).Image()
	link, label := "/files/"+id, strings.NewReplacer("[", "(", "]", ")").Replace(name)
	md := "[" + label + "](" + link + ")"
	if image {
		link += "?inline=1"
		md = "![" + label + "](" + link + ")"
	}
	return kbFile{ID: id, Name: name, URL: link, Image: image, MD: md}, nil
}

// kbExport gives the base as a ZIP of Markdown files («Экспорт»): who sees the base.
func (s *Server) kbExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sp, err := s.KB.Space(r.Context(), sess.UserID, r.PathValue("space"))
	if err != nil {
		s.taskErr(w, r, err, "/kb")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(sp.Name+".zip"))
	if err := s.KB.Export(r.Context(), sess.UserID, sp.ID, w, s.Storages.Open); err != nil {
		s.Log.Error("kb export", "space", sp.ID, "err", err)
	}
}

// kbImport makes pages of a ZIP of Markdown files or of separate .md files («Импорт»): who writes in the base.
func (s *Server) kbImport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/kb/" + r.PathValue("space")
	if p := r.FormValue("parent"); p != "" {
		back += "/" + p
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.taskErr(w, r, i18n.E("err.file_required"), back)
		return
	}
	files, err := kbImportFiles(r.MultipartForm.File["file"])
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	put := func(ctx context.Context, key string, rd io.Reader, size int64, ct string) error {
		return s.Storages.Put(ctx, sess.UserID, false, "", key, rd, size, ct)
	}
	n, err := s.KB.Import(r.Context(), sess.UserID, r.PathValue("space"), r.FormValue("parent"), files, put)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "kb.imported", n)), http.StatusSeeOther)
}

// kbImportFiles reads the uploaded files for «Импорт»: a ZIP gives the files in it, any other file is taken as it is.
func kbImportFiles(hdrs []*multipart.FileHeader) ([]kb.ImportFile, error) {
	var files []kb.ImportFile
	for _, hdr := range hdrs {
		f, err := hdr.Open()
		if err != nil {
			return nil, err
		}
		name := path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/"))
		if strings.EqualFold(path.Ext(name), ".zip") {
			list, err := kb.ReadArchive(f, hdr.Size)
			f.Close()
			if err != nil {
				return nil, err
			}
			files = append(files, list...)
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		files = append(files, kb.ImportFile{Name: name, Data: data})
	}
	return files, nil
}

// kbDiscuss: the discussion under a page — a comment or a reply to one (…/comment: body, parent), «Нравится»
// (…/like, on=0 takes it back).
func (s *Server) kbDiscuss(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/kb/" + r.PathValue("space") + "/" + r.PathValue("page")
	var err error
	if strings.HasSuffix(r.URL.Path, "/like") {
		err = s.KB.Like(r.Context(), sess.UserID, r.PathValue("page"), r.FormValue("on") != "0")
		back += "#kblikes"
	} else {
		var c kb.Comment
		if c, err = s.KB.AddComment(r.Context(), sess.UserID, r.PathValue("page"), r.FormValue("parent"), r.FormValue("body")); err == nil {
			back += "#c-" + c.ID
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// kbComment changes the user's comment under a page (body) or deletes it (delete=1) with the replies to it.
func (s *Server) kbComment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/kb/" + r.PathValue("space") + "/" + r.PathValue("page")
	id := r.PathValue("id")
	var err error
	if r.FormValue("delete") != "" {
		err = s.KB.DeleteComment(r.Context(), sess.UserID, id)
		back += "#kbcomments"
	} else {
		err = s.KB.EditComment(r.Context(), sess.UserID, id, r.FormValue("body"))
		back += "#c-" + id
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
