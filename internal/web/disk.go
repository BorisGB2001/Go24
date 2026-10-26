package web

import (
	"cmp"
	"context"
	"fmt"
	"html/template"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/office"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Диск»: /disk/my, /disk/company, /disk/p/{key}, /disk/u/{uid} (?folder=…; ?zip — the download of files and folders; ?trash — its trash) ----------

func (s *Server) openDrive(r *http.Request, sess auth.Session) (drive.Drive, error) {
	which := r.PathValue("which")
	if key := r.PathValue("key"); key != "" {
		which = key
	} else if id := r.PathValue("id"); id != "" {
		which = "sh:" + id // a folder shared with the user
	} else if id := r.PathValue("sf"); id != "" {
		which = "sf:" + id // a file shared with the user
	} else if id := r.PathValue("uid"); id != "" {
		which = "u:" + id // an employee's «Мой диск»: for an administrator
	} else if which != "my" && which != "company" {
		return drive.Drive{}, drive.ErrNotFound
	}
	return s.Drive.Open(r.Context(), sess.UserID, which)
}

func (s *Server) diskPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.URL.Query().Has("zip") {
		s.diskZip(w, r, sess)
		return
	}
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	qs := r.URL.Query()
	folder := qs.Get("folder")
	q, ftype := strings.TrimSpace(qs.Get("q")), qs.Get("type")
	searching := q != "" || ftype != ""
	var folders []drive.Folder
	var found []drive.File // the folders a search found
	var files []drive.File
	var trail []drive.Folder
	var trash []drive.Trashed
	var snippets map[string]template.HTML // the files found by their text: where
	var space drive.Space
	lim, err := s.Drive.Limits(r.Context()) // the former versions kept, what of the drives the company has turned off
	switch {
	case err != nil:
	case qs.Has("space"):
		space, err = s.Drive.Space(r.Context(), d)
	case qs.Has("trash"):
		trash, err = s.Drive.Trash(r.Context(), d)
	case searching: // the whole drive search and filter
		folder = ""
		if found, files, err = s.Drive.Search(r.Context(), d, q); ftype != "" {
			found = nil
			files = slices.DeleteFunc(files, func(f drive.File) bool { return fileType(f.Name, f.ContentType) != ftype })
		}
		if len(files) > searchMax {
			files = files[:searchMax]
		}
		if err == nil {
			snippets, err = s.snippets(r.Context(), files, q)
		}
	default:
		if folders, files, trail, err = s.Drive.List(r.Context(), d, folder); err == nil {
			d, err = s.Drive.Within(r.Context(), d, d.At(folder)) // what the viewer may do here, by the rights of the folders
		}
	}
	if err != nil {
		s.taskErr(w, r, err, d.Path())
		return
	}
	sortBy, desc := qs.Get("sort"), qs.Get("desc") == "1"
	sortFiles(files, sortBy, desc)
	if desc && (sortBy == "" || sortBy == "name") {
		slices.Reverse(folders)
		slices.Reverse(found)
	}
	var ids []string
	for _, f := range folders {
		ids = append(ids, f.ID)
	}
	for _, f := range found {
		ids = append(ids, f.ID)
	}
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	links, err := s.Drive.Links(r.Context(), ids)
	var comments map[string]int // «Обсуждение» of the files: how many comments
	if err == nil {
		comments, err = s.Drive.CommentCounts(r.Context(), ids)
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	// «Доступ» of the folders and the files of one's own «Мой диск»: who has them, and whom to give them to
	var shares map[string][]drive.Share
	var users []tasks.User
	var depts []drive.Department
	if d.Kind == "user" && d.Root == "" && !d.Employee && len(ids) > 0 && !tasks.IsGuestCtx(r.Context()) {
		if shares, err = s.Drive.Shares(r.Context(), ids); err == nil {
			if users, err = s.Tasks.ListUsers(r.Context()); err == nil {
				depts, err = s.Drive.Departments(r.Context())
			}
		}
		if err != nil {
			s.taskErr(w, r, err, "/disk/my")
			return
		}
		users = slices.DeleteFunc(users, func(u tasks.User) bool { return u.ID == sess.UserID || u.Guest })
	}
	// «Права доступа» of the folders of the company's drive or a project's: shown to those who may change them
	var rights map[string][]drive.Right
	var mayRight map[string]bool
	var rightUsers []tasks.User
	if (d.Kind == "company" || d.Kind == "project") && len(folders) > 0 && !tasks.IsGuestCtx(r.Context()) {
		mayRight = map[string]bool{}
		var fids []string
		for _, f := range folders {
			if s.Drive.MayRight(r.Context(), d, f.ID) {
				mayRight[f.ID] = true
			}
			fids = append(fids, f.ID)
		}
		if rights, err = s.Drive.FolderRights(r.Context(), fids); err == nil && len(mayRight) > 0 {
			if rightUsers, err = s.Tasks.ListUsers(r.Context()); err == nil {
				depts, err = s.Drive.Departments(r.Context())
			}
			rightUsers = slices.DeleteFunc(rightUsers, func(u tasks.User) bool { return u.Guest && !d.Collab })
		}
		if err != nil {
			s.taskErr(w, r, err, d.Path())
			return
		}
	}
	stores, err := s.Storages.List(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()))
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	if tasks.IsGuestCtx(r.Context()) {
		stores = nil // the company's storages are not a guest's business: their files go to the built-in one
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	projects = slices.DeleteFunc(projects, tasks.Project.Collab) // a collab's drive is reached from the collab
	usage, err := s.Drive.Usage(r.Context(), d)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	// «Подключить к своему Диску»: the connected at the top of «Мой диск»; a shared folder or a project's drive is connected
	var connected []drive.Connected
	var conn map[string]any
	if d.Kind == "user" && d.Root == "" && d.File == "" && !d.Employee && folder == "" && !searching && !qs.Has("trash") && !qs.Has("space") {
		connected, err = s.Drive.Connections(r.Context(), sess.UserID)
	} else if which := connectWhich(d); which != "" && !tasks.IsGuestCtx(r.Context()) {
		conn = map[string]any{"Which": which, "On": s.Drive.IsConnected(r.Context(), sess.UserID, d)}
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	nav := "disk"
	if d.Collab {
		nav = "collabs"
	}
	s.render(w, r, "disk", page{TitleKey: "disk.title", Nav: nav, Data: map[string]any{
		"Shares": shares, "Rights": rights, "MayRight": mayRight, "RightUsers": rightUsers, "Users": users, "Departments": depts, "Accesses": drive.Accesses, "FileAccesses": drive.FileAccesses, "Office": s.Office != nil, "Usage": usage, "DavURL": s.davURL(r), "Q": q, "Type": ftype, "Types": fileTypes, "Searching": searching, "Sort": sortBy, "Desc": desc, "SortURL": diskSortURLs(qs, sortBy, desc),
		"Drive": d, "Folder": folder, "Folders": folders, "Found": found, "Files": files, "Snippets": snippets, "Trail": trail, "Trash": trash, "Links": links, "Comments": comments, "Base": s.BaseURL, "InTrash": qs.Has("trash"), "InSpace": qs.Has("space"), "Aside": qs.Has("trash") || qs.Has("space"), "Space": space, "KeepVersions": lim.Versions, "LinksOff": lim.NoLinks, "LocksOff": lim.NoLocks, "Connected": connected, "Conn": conn, "Storages": stores, "Projects": projects, "Me": sess.UserID, "Local": !tasks.IsGuestCtx(r.Context())}})
}

// searchMax is how many files a search or a filter of a drive shows.
const searchMax = 500

// fileTypes are the kinds the list of a drive is filtered by (fileType).
var fileTypes = []string{"doc", "sheet", "slides", "pdf", "image", "video", "audio", "archive", "other"}

// fileType is the kind of a file for the filter of a drive: by its extension, then by its content type.
func fileType(name, contentType string) string {
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	switch ext {
	case "doc", "docx", "odt", "rtf", "txt", "md":
		return "doc"
	case "xls", "xlsx", "ods", "csv":
		return "sheet"
	case "ppt", "pptx", "odp":
		return "slides"
	case "pdf":
		return "pdf"
	case "zip", "rar", "7z", "tar", "gz", "tgz", "bz2", "xz":
		return "archive"
	}
	switch k := fileKind(name, contentType); k {
	case "image", "video", "audio":
		return k
	}
	return "other"
}

// sortFiles orders the files by name (""), date or size; desc reverses it.
func sortFiles(files []drive.File, by string, desc bool) {
	slices.SortStableFunc(files, func(a, b drive.File) int {
		var c int
		switch by {
		case "date":
			c = a.CreatedAt.Compare(b.CreatedAt)
		case "size":
			c = cmp.Compare(a.Size, b.Size)
		}
		if c == 0 {
			c = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if desc {
			return -c
		}
		return c
	})
}

// diskSortURLs are the addresses of the list's column headers: the same page sorted by the column, a second click
// on the column sorted the other way.
func diskSortURLs(qs url.Values, by string, desc bool) map[string]string {
	out := map[string]string{}
	for _, col := range []string{"name", "size", "date"} {
		v := url.Values{}
		for _, k := range []string{"folder", "q", "type"} {
			if qs.Get(k) != "" {
				v.Set(k, qs.Get(k))
			}
		}
		if col != "name" {
			v.Set("sort", col)
		}
		if col == by || (col == "name" && by == "") {
			if !desc {
				v.Set("desc", "1")
			}
		} else if col == "date" || col == "size" {
			v.Set("desc", "1") // the newest, the biggest first
		}
		out[col] = "?" + v.Encode()
	}
	return out
}

// diskBack is the folder of the drive the form came from.
func diskBack(d drive.Drive, folder string) string {
	if d.File != "" { // a file shared by itself is in «Общие со мной»
		return "/disk/shared"
	}
	if folder == "" {
		return d.Path()
	}
	return d.Path() + "?folder=" + url.QueryEscape(folder)
}

// diskUpload puts files into a folder of the drive, in the storage chosen. The page's script sends them one by one
// (Accept: application/json — the answer is JSON, to show the progress); the plain form sends any number at once.
func (s *Server) diskUpload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.diskErr(w, r, err, "/disk/my")
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.diskErr(w, r, i18n.E("err.file_required"), d.Path())
		return
	}
	defer r.MultipartForm.RemoveAll()
	folder := r.FormValue("folder")
	back := docsBack(r, diskBack(d, folder))
	if err := s.Drive.CheckFolder(r.Context(), d, folder); err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	hdrs := r.MultipartForm.File["file"]
	if len(hdrs) == 0 {
		s.diskErr(w, r, i18n.E("err.file_required"), back)
		return
	}
	storageID := r.FormValue("storage")
	if tasks.IsGuestCtx(r.Context()) {
		storageID = "" // a guest stores in the built-in storage only
	}
	if dir := r.FormValue("path"); dir != "" { // a file of an uploaded folder: the folders of its path
		if folder, err = s.Drive.EnsurePath(r.Context(), sess.UserID, d, folder, dir); err != nil {
			s.diskErr(w, r, err, back)
			return
		}
	}
	var ids []string
	for _, hdr := range hdrs {
		id, err := s.diskPut(r, sess, d, folder, storageID, hdr)
		if err != nil {
			s.diskErr(w, r, err, back)
			return
		}
		ids = append(ids, id)
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"ids": ids})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) diskPut(r *http.Request, sess auth.Session, d drive.Drive, folder, storageID string, hdr *multipart.FileHeader) (string, error) {
	f, err := hdr.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	ct := hdr.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return s.diskStore(r, sess, d, folder, storageID, path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/")), ct, f, hdr.Size)
}

// diskStore keeps the bytes as a file of the folder; the same name in the folder — a new version of that file.
func (s *Server) diskStore(r *http.Request, sess auth.Session, d drive.Drive, folder, storageID, name, ct string, f io.Reader, size int64) (string, error) {
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
	same, err := s.Drive.Same(r.Context(), sess.UserID, d, folder, name)
	if err != nil {
		return "", err
	}
	if err := s.Drive.Fits(r.Context(), d, size); err != nil { // the drive's limit (a new version counts too)
		return "", err
	}
	if err := s.Storages.Put(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), storageID, key, f, size, ct); err != nil {
		return "", err
	}
	if same != "" {
		return same, s.Drive.AddVersion(r.Context(), sess.UserID, d, same, drive.NewVersion{Key: key, ContentType: ct, StorageID: storageID, Size: size})
	}
	return s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: ct, Size: size,
		StorageID: storageID, UploadedBy: sess.UserID, Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder)})
}

// diskErr: the page's script gets the error as JSON, a form — as a page.
func (s *Server) diskErr(w http.ResponseWriter, r *http.Request, err error, back string) {
	if wantsJSON(r) {
		s.jsonErr(w, r, err)
		return
	}
	s.taskErr(w, r, err, back)
}

func (s *Server) diskFolder(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	parent := r.FormValue("folder")
	id, err := s.Drive.CreateFolder(r.Context(), sess.UserID, d, parent, r.FormValue("name"))
	if err != nil {
		s.taskErr(w, r, err, diskBack(d, parent))
		return
	}
	http.Redirect(w, r, diskBack(d, id), http.StatusSeeOther)
}

// diskSelection is the files (file=ID…) and folders (folder_id=ID…) an action is done with.
func diskSelection(r *http.Request) drive.Selection {
	return drive.Selection{Files: r.Form["file"], Folders: r.Form["folder_id"]}
}

// diskDelete removes files and empty folders and stays in the folder shown (folder).
func (s *Server) diskDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	_ = r.ParseForm()
	back := docsBack(r, diskBack(d, r.FormValue("folder")))
	sel := diskSelection(r)
	for _, id := range sel.Files {
		if err = s.Drive.DeleteFile(r.Context(), sess.UserID, d, id); err != nil {
			break
		}
	}
	for _, id := range sel.Folders {
		if err != nil {
			break
		}
		err = s.Drive.DeleteFolder(r.Context(), sess.UserID, d, id)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskRestore takes the selection out of the drive's trash; diskPurge deletes it for good.
func (s *Server) diskRestore(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.diskTrashAct(w, r, sess, s.Drive.Restore)
}

func (s *Server) diskPurge(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.diskTrashAct(w, r, sess, s.Drive.Purge)
}

func (s *Server) diskTrashAct(w http.ResponseWriter, r *http.Request, sess auth.Session,
	act func(context.Context, string, drive.Drive, drive.Selection) error) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	_ = r.ParseForm()
	back := d.Path() + "?trash"
	if err := act(r.Context(), sess.UserID, d, diskSelection(r)); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// connectWhich is what «Подключить к своему Диску» connects of the drive: a folder shared with the viewer, a
// project's drive (not a collab's); "" — nothing.
func connectWhich(d drive.Drive) string {
	switch {
	case d.Root != "":
		return "sh:" + d.Root
	case d.Kind == "project" && !d.Collab:
		return d.ProjectKey
	}
	return ""
}

// diskConnect connects a folder shared with the user or a project's drive (which) to their «Мой диск», or with
// off=1 disconnects it; back is the page to return to.
func (s *Server) diskConnect(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/disk/") || strings.HasPrefix(back, "//") {
		back = "/disk/my"
	}
	var err error
	if r.FormValue("off") == "1" {
		err = s.Drive.Disconnect(r.Context(), sess.UserID, r.FormValue("which"))
	} else if tasks.IsGuestCtx(r.Context()) {
		err = drive.ErrForbidden
	} else {
		err = s.Drive.Connect(r.Context(), sess.UserID, r.FormValue("which"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskClean is «Очистка» of the drive: what=trash empties its trash, what=versions deletes the former versions of
// its files (of the file=ID only, when given).
func (s *Server) diskClean(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	back := d.Path() + "?space"
	switch r.FormValue("what") {
	case "trash":
		err = s.Drive.EmptyTrash(r.Context(), sess.UserID, d)
	case "versions":
		var files []string
		if id := r.FormValue("file"); id != "" {
			files = []string{id}
		}
		_, err = s.Drive.PurgeVersions(r.Context(), d, files)
	default:
		err = drive.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskRename gives a file (file=ID) or a folder (folder_id=ID) another name.
func (s *Server) diskRename(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	back := docsBack(r, diskBack(d, r.FormValue("folder")))
	if id := r.FormValue("file"); id != "" {
		err = s.Drive.RenameFile(r.Context(), sess.UserID, d, id, r.FormValue("name"))
	} else {
		err = s.Drive.RenameFolder(r.Context(), sess.UserID, d, r.FormValue("folder_id"), r.FormValue("name"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskLock locks a file for the others («Заблокировать») or, with unlock=1, lifts its lock.
func (s *Server) diskLock(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	back := diskBack(d, r.FormValue("folder"))
	if r.FormValue("unlock") != "" {
		err = s.Drive.Unlock(r.Context(), sess.UserID, d, r.FormValue("file"))
	} else {
		err = s.Drive.Lock(r.Context(), sess.UserID, d, r.FormValue("file"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskMove moves (or with copy=1 copies) the selection to a folder (to_folder, "" — the top) of a drive (to: my,
// company or a project's key); the page then shows where they went.
func (s *Server) diskMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.diskErr(w, r, err, "/disk/my")
		return
	}
	_ = r.ParseForm()
	back := diskBack(d, r.FormValue("folder"))
	to := r.FormValue("to")
	if to == "" {
		to = r.PathValue("which") + r.PathValue("key")
		if id := r.PathValue("uid"); id != "" {
			to = "u:" + id
		}
	}
	dst, err := s.Drive.Open(r.Context(), sess.UserID, to)
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	if tasks.IsGuestCtx(r.Context()) && dst.Kind != "project" {
		s.diskErr(w, r, drive.ErrForbidden, back) // a guest's files stay in the collab
		return
	}
	folder, sel := r.FormValue("to_folder"), diskSelection(r)
	if r.FormValue("copy") == "1" {
		err = s.Drive.Copy(r.Context(), sess.UserID, d, sel, dst, folder, s.Storages.Copy)
	} else {
		err = s.Drive.Move(r.Context(), sess.UserID, d, sel, dst, folder)
	}
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, diskBack(dst, folder), http.StatusSeeOther)
}

// diskZip is «Скачать» of a folder or of several files and folders: one ZIP archive, folders with all they hold.
func (s *Server) diskZip(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	_ = r.ParseForm()
	sel := diskSelection(r)
	entries, err := s.Drive.Archive(r.Context(), d, sel)
	if err != nil {
		s.taskErr(w, r, err, d.Path())
		return
	}
	name := "disk"
	if len(sel.Folders) == 1 && len(sel.Files) == 0 && len(entries) > 0 {
		name, _, _ = strings.Cut(entries[0].Path, "/")
	}
	s.writeZip(r.Context(), w, name, entries)
}

// fileKind is how the viewer of «Диск» shows a file: as mail's viewer does (image, pdf, html of a document), and
// video and audio in the browser's player; "" — downloaded.
func fileKind(name, contentType string) string {
	ct, ext := strings.ToLower(contentType), strings.ToLower(path.Ext(name))
	switch {
	case strings.HasPrefix(ct, "video/") || slices.Contains([]string{".mp4", ".webm", ".mov", ".m4v", ".ogv"}, ext):
		return "video"
	case strings.HasPrefix(ct, "audio/") || slices.Contains([]string{".mp3", ".wav", ".m4a", ".ogg", ".oga", ".flac", ".opus"}, ext):
		return "audio"
	}
	return mail.PreviewKind(name, contentType)
}

// filePreview is the viewer's view of a file: kind (see fileKind) and the HTML of a document.
func (s *Server) filePreview(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.fileAccess(r, sess, r.PathValue("id"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := map[string]any{"id": r.PathValue("id"), "name": m.Name, "size_text": s.sizeText(r, m.Size), "kind": fileKind(m.Name, m.ContentType)}
	if out["kind"] == mail.PreviewHTML {
		out["kind"] = mail.PreviewNone
		if data, err := s.readFile(r, m, 20<<20); err == nil {
			if h, ok := mail.Preview(m.Name, m.ContentType, data); ok {
				out["kind"], out["html"] = mail.PreviewHTML, h
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// fileRaw is a picture, a PDF, a video or a sound for the viewer and the tiles: shown in the browser, never run.
func (s *Server) fileRaw(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	m, err := s.fileAccess(r, sess, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/files")
		return
	}
	kind := fileKind(m.Name, m.ContentType)
	ct := m.ContentType
	switch kind {
	case mail.PreviewPDF:
		ct = "application/pdf"
	case mail.PreviewImage, "video", "audio":
		if !strings.HasPrefix(ct, kind+"/") {
			ct = "application/octet-stream" // known by its name only: the browser guesses no further (nosniff)
		}
	default:
		http.Redirect(w, r, "/files/"+r.PathValue("id"), http.StatusSeeOther)
		return
	}
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if m.Size > 0 {
		if obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key); err != nil {
			s.fail(w, "open file", err)
			return
		}
	}
	defer obj.Close()
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprint(m.Size))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'; object-src 'self'")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename*=UTF-8''%s", url.PathEscape(m.Name)))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("file raw", "err", err)
	}
}

// fileVersions is the history of a file of a drive, the current version first.
func (s *Server) fileVersions(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	m, err := s.fileAccess(r, sess, id)
	if err == nil && m.Drive == "" {
		err = drive.ErrNotFound
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	list, err := s.Drive.Versions(r.Context(), id)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, v := range list {
		out = append(out, map[string]any{"id": v.ID, "no": v.No, "size_text": s.sizeText(r, v.Size), "author": v.Author,
			"date": v.CreatedAt.Local().Format(s.tr(r, "format.datetime"))})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"name": m.Name, "versions": out})
}

// fileVersion gives a former version of a file of a drive to those who may read the file.
func (s *Server) fileVersion(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	m, err := s.fileAccess(r, sess, id)
	if err == nil && m.Drive == "" {
		err = drive.ErrNotFound
	}
	var e drive.Entry
	if err == nil {
		e, err = s.Drive.VersionObject(r.Context(), id, r.PathValue("v"))
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if e.Size > 0 {
		if obj, err = s.Storages.Open(r.Context(), e.StorageID, e.Key); err != nil {
			s.fail(w, "open version", err)
			return
		}
	}
	defer obj.Close()
	name := strings.TrimSuffix(m.Name, path.Ext(m.Name)) + " (" + e.ModTime.Local().Format("2006-01-02 15-04") + ")" + path.Ext(m.Name)
	w.Header().Set("Content-Type", e.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("download version", "key", e.Key, "err", err)
	}
}

// diskVersionRestore brings a version (version) of a file (file) back as its current one.
func (s *Server) diskVersionRestore(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.diskErr(w, r, err, "/disk/my")
		return
	}
	back := diskBack(d, r.FormValue("folder"))
	if err := s.Drive.RestoreVersion(r.Context(), sess.UserID, d, r.FormValue("file"), r.FormValue("version")); err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskRecent is «Последние»: the files the user uploaded or changed in the last 30 days, on every drive.
func (s *Server) diskRecent(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Drive.Recent(r.Context(), sess.UserID, 30, 100)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	projects = slices.DeleteFunc(projects, tasks.Project.Collab)
	s.render(w, r, "disk", page{TitleKey: "disk.title", Nav: "disk", Data: map[string]any{
		"RecentPage": true, "Recent": list, "Drive": drive.Drive{}, "Projects": projects, "Me": sess.UserID}})
}

// diskShare opens a folder of one's «Мой диск» (folder_id) to an employee or a department (whom: user:ID |
// department:ID) with an access (read | edit | full), or closes it (off: the share's id).
func (s *Server) diskShare(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.diskErr(w, r, err, "/disk/my")
		return
	}
	back := cardBack(r, diskBack(d, r.FormValue("folder")))
	if off := r.FormValue("off"); off != "" {
		err = s.Drive.DeleteShare(r.Context(), sess.UserID, d, off)
	} else {
		kind, id, _ := strings.Cut(r.FormValue("whom"), ":")
		err = s.Drive.SetShare(r.Context(), sess.UserID, d, r.FormValue("folder_id"), r.FormValue("file"), kind, id, r.FormValue("access"))
	}
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskRights changes «Права доступа» of a folder (folder_id) of the company's drive or a project's: opens it to
// everyone, an employee or a department (whom: all | user:ID | department:ID) with an access (read | edit | full),
// takes a line away (off: its id) or all of them (clear).
func (s *Server) diskRights(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.diskErr(w, r, err, "/disk/company")
		return
	}
	back, folderID := diskBack(d, r.FormValue("folder")), r.FormValue("folder_id")
	switch kind, id, _ := strings.Cut(r.FormValue("whom"), ":"); {
	case r.FormValue("clear") != "":
		err = s.Drive.ClearRights(r.Context(), d, folderID)
	case r.FormValue("off") != "":
		err = s.Drive.DeleteRight(r.Context(), sess.UserID, d, folderID, r.FormValue("off"))
	default:
		err = s.Drive.SetRight(r.Context(), sess.UserID, d, folderID, kind, id, r.FormValue("access"))
	}
	if err != nil {
		s.diskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// diskSharedFile opens a file shared with the user by itself: a document in the editor, any other file as the file.
func (s *Server) diskSharedFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/shared")
		return
	}
	if drive.IsBoard(d.Name) {
		http.Redirect(w, r, d.Path()+"/board/"+d.File, http.StatusFound)
		return
	}
	if s.Office != nil && office.Kind(d.Name) != "" && office.Kind(d.Name) != "pdf" {
		http.Redirect(w, r, d.Path()+"/edit/"+d.File, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/files/"+d.File, http.StatusFound)
}

// diskShared is «Общие со мной»: the folders and the files others shared with the user.
func (s *Server) diskShared(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Drive.SharedWithMe(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	projects = slices.DeleteFunc(projects, tasks.Project.Collab)
	conns, err := s.Drive.Connections(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	set := map[string]bool{}
	for _, c := range conns {
		set[c.Which] = true
	}
	s.render(w, r, "disk", page{TitleKey: "disk.title", Nav: "disk", Data: map[string]any{
		"SharedPage": true, "Shared": list, "ConnectedSet": set, "Drive": drive.Drive{}, "Projects": projects, "Me": sess.UserID}})
}
