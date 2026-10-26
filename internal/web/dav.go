package web

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/webdav"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/tasks"
)

// ---------- «Диск» over WebDAV: the drives as a network disk (Windows, macOS, office programs) ----------
//
// /dav/ holds «Мой диск», «Общий диск», the drive of each project («Название (KEY)») and «Общие со мной» with the
// folders shared with the user. One signs in with an API key of go24 (the password; any user name) that may
// «disk:read» (and «disk:write» to change anything). A file written again becomes its new version, a deleted one
// goes to the trash, everything as on the drive's page.

// davLocks are the WebDAV locks of each user (every user's /dav/ names their own drives).
var davLocks sync.Map

func (s *Server) dav(w http.ResponseWriter, r *http.Request) {
	p, ok := s.keyUser(w, r, "disk:read", "err.dav_key")
	if !ok {
		return
	}
	switch r.Method {
	case "GET", "HEAD", "OPTIONS", "PROPFIND":
	default:
		if !p.Can("disk:write") {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
	}
	r = r.WithContext(tasks.WithViewer(r.Context(), p.UserID, false))
	ls, _ := davLocks.LoadOrStore(p.UserID, webdav.NewMemLS())
	h := &webdav.Handler{Prefix: "/dav", FileSystem: &davFS{s: s, r: r, user: p.UserID}, LockSystem: ls.(webdav.LockSystem),
		Logger: func(r *http.Request, err error) {
			if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
				s.Log.Warn("dav", "method", r.Method, "path", r.URL.Path, "err", err)
			}
		}}
	h.ServeHTTP(w, r)
}

// keyUser is the employee who signs in to a network disk or calendar with an API key that may scope (the password;
// any user name); else the answer asks for one, saying msg.
func (s *Server) keyUser(w http.ResponseWriter, r *http.Request, scope, msg string) (apikeys.Principal, bool) {
	_, token, ok := r.BasicAuth()
	var p apikeys.Principal
	err := errors.New("no key")
	if ok && s.Keys != nil {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		p, err = s.Keys.Authenticate(r.Context(), strings.TrimSpace(token), ip)
	}
	var active, guest bool
	if err == nil {
		err = s.DB.QueryRow(r.Context(), `SELECT active, guest FROM users WHERE id = $1`, p.UserID).Scan(&active, &guest)
	}
	if err != nil || !active || guest || !p.Can(scope) {
		w.Header().Set("WWW-Authenticate", `Basic realm="go24", charset="UTF-8"`)
		http.Error(w, s.tr(r, msg), http.StatusUnauthorized)
		return p, false
	}
	return p, true
}

// davFS is the drives of one user for one request.
type davFS struct {
	s    *Server
	r    *http.Request
	user string
}

// davNode is what a path names: the top, «Общие со мной», a drive, a folder or a file of it.
type davNode struct {
	name   string
	top    string // "" — the top itself; "shared" — «Общие со мной»; else a drive
	drive  drive.Drive
	folder string // the folder ("" — the drive's top)
	file   *drive.File
}

func (n davNode) isDir() bool { return n.file == nil }

// davName makes a name fit a path segment.
func davName(name string) string {
	return strings.NewReplacer("/", "-", "\\", "-").Replace(strings.TrimSpace(name))
}

// tops are the entries of /dav/: the drive names and how to open each.
func (fs *davFS) tops(ctx context.Context) ([][2]string, error) {
	out := [][2]string{{davName(fs.s.tr(fs.r, "disk.my")), "my"}, {davName(fs.s.tr(fs.r, "disk.company")), "company"}}
	projects, err := fs.s.Tasks.ListProjects(ctx, fs.user)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if !p.Archived {
			out = append(out, [2]string{davName(p.Name + " (" + p.Key + ")"), p.Key})
		}
	}
	return append(out, [2]string{davName(fs.s.tr(fs.r, "disk.shared")), "shared"}), nil
}

// sharedTops are the folders shared with the user, as «Общие со мной» lists them.
func (fs *davFS) sharedTops(ctx context.Context) ([][2]string, error) {
	list, err := fs.s.Drive.SharedWithMe(ctx, fs.user)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, f := range list {
		out = append(out, [2]string{davName(f.Name + " (" + f.Owner + ")"), "sh:" + f.ID})
	}
	return out, nil
}

func davFind(list [][2]string, name string) (string, bool) {
	for _, e := range list {
		if e[0] == name {
			return e[1], true
		}
	}
	return "", false
}

// resolve finds what the path names.
func (fs *davFS) resolve(ctx context.Context, name string) (davNode, error) {
	segs := strings.FieldsFunc(path.Clean("/"+name), func(r rune) bool { return r == '/' })
	n := davNode{name: "/"}
	if len(segs) == 0 {
		return n, nil
	}
	tops, err := fs.tops(ctx)
	if err != nil {
		return n, err
	}
	which, ok := davFind(tops, segs[0])
	if !ok {
		return n, os.ErrNotExist
	}
	n.name, segs = segs[0], segs[1:]
	if which == "shared" {
		n.top = "shared"
		if len(segs) == 0 {
			return n, nil
		}
		shared, err := fs.sharedTops(ctx)
		if err != nil {
			return n, err
		}
		if which, ok = davFind(shared, segs[0]); !ok {
			return n, os.ErrNotExist
		}
		n.name, segs = segs[0], segs[1:]
	}
	n.top = which
	if n.drive, err = fs.s.Drive.Open(ctx, fs.user, which); err != nil {
		return n, os.ErrNotExist
	}
	for i, seg := range segs {
		folders, files, _, err := fs.s.Drive.List(ctx, n.drive, n.folder)
		if err != nil {
			return n, err
		}
		n.name = seg
		found := false
		for _, f := range folders {
			if davName(f.Name) == seg {
				n.folder, found = f.ID, true
				break
			}
		}
		if found {
			continue
		}
		if i == len(segs)-1 {
			for _, f := range files {
				if davName(f.Name) == seg {
					n.file = &f
					return n, nil
				}
			}
		}
		return n, os.ErrNotExist
	}
	return n, nil
}

// parent is the folder the path's last name would be in, and that name.
func (fs *davFS) parent(ctx context.Context, name string) (davNode, string, error) {
	dir, base := path.Split(strings.TrimSuffix(path.Clean("/"+name), "/"))
	p, err := fs.resolve(ctx, dir)
	if err != nil {
		return p, "", err
	}
	if p.top == "" || p.top == "shared" || !p.isDir() || base == "" {
		return p, "", os.ErrPermission // the top and «Общие со мной» are go24's, not folders to write into
	}
	return p, base, nil
}

// davErr turns the drive's refusals into what WebDAV answers.
func davErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, drive.ErrNotFound), errors.Is(err, tasks.ErrNotFound):
		return os.ErrNotExist
	case errors.Is(err, drive.ErrForbidden), errors.Is(err, tasks.ErrForbidden), errors.Is(err, drive.ErrFull), errors.Is(err, drive.ErrLocked):
		return os.ErrPermission
	}
	return err
}

func (fs *davFS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	p, base, err := fs.parent(ctx, name)
	if err != nil {
		return err
	}
	if _, err := fs.resolve(ctx, name); err == nil {
		return os.ErrExist
	}
	_, err = fs.s.Drive.CreateFolder(ctx, fs.user, p.drive, p.folder, base)
	return davErr(err)
}

func (fs *davFS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	n, err := fs.resolve(ctx, name)
	if err != nil {
		return nil, err
	}
	return n.info(), nil
}

func (n davNode) info() davInfo {
	if n.file != nil {
		return davInfo{name: n.file.Name, size: n.file.Size, mod: n.file.CreatedAt, ct: n.file.ContentType}
	}
	return davInfo{name: n.name, dir: true}
}

func (fs *davFS) RemoveAll(ctx context.Context, name string) error {
	n, err := fs.resolve(ctx, name)
	if err != nil {
		return err
	}
	switch {
	case n.file != nil:
		return davErr(fs.s.Drive.DeleteFile(ctx, fs.user, n.drive, n.file.ID))
	case n.folder != "":
		return davErr(fs.s.Drive.DeleteFolder(ctx, fs.user, n.drive, n.folder))
	}
	return os.ErrPermission // a drive itself
}

func (fs *davFS) Rename(ctx context.Context, oldName, newName string) error {
	n, err := fs.resolve(ctx, oldName)
	if err != nil {
		return err
	}
	if n.file == nil && n.folder == "" {
		return os.ErrPermission
	}
	p, base, err := fs.parent(ctx, newName)
	if err != nil {
		return err
	}
	sel, id := drive.Selection{Folders: []string{n.folder}}, n.folder
	if n.file != nil {
		sel, id = drive.Selection{Files: []string{n.file.ID}}, n.file.ID
	}
	if from, _, _ := fs.parent(ctx, oldName); from.top != p.top || from.folder != p.folder {
		if err := fs.s.Drive.Move(ctx, fs.user, n.drive, sel, p.drive, p.folder); err != nil {
			return davErr(err)
		}
	}
	if base == n.name {
		return nil
	}
	if n.file != nil {
		return davErr(fs.s.Drive.RenameFile(ctx, fs.user, p.drive, id, base))
	}
	return davErr(fs.s.Drive.RenameFolder(ctx, fs.user, p.drive, id, base))
}

func (fs *davFS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	n, err := fs.resolve(ctx, name)
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC) == 0 { // reading
		if err != nil {
			return nil, err
		}
		return &davFile{fs: fs, node: n}, nil
	}
	if err == nil && n.isDir() {
		return nil, os.ErrPermission
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	p, base, perr := fs.parent(ctx, name)
	if perr != nil {
		return nil, perr
	}
	if err := fs.s.Drive.CheckFolder(ctx, p.drive, p.folder); err != nil {
		return nil, davErr(err)
	}
	if n.file != nil { // written again: a new version, by those who may change it
		if may, err := fs.s.Drive.MayChange(ctx, fs.user, p.drive, n.file.ID); err != nil || !may {
			return nil, os.ErrPermission
		}
	} else if flag&os.O_CREATE == 0 {
		return nil, os.ErrNotExist
	}
	tmp, err := os.CreateTemp("", "go24-dav-*")
	if err != nil {
		return nil, err
	}
	return &davFile{fs: fs, node: n, into: &p, base: base, tmp: tmp}, nil
}

// davFile is a file or a folder opened over WebDAV: a file is read from its storage into a temporary file when it
// is first read; a file written goes to a temporary file and onto the drive when closed.
type davFile struct {
	fs   *davFS
	node davNode
	tmp  *os.File
	// writing: the folder and the name it goes to
	into *davNode
	base string
	read int // Readdir: how many entries were given
}

func (f *davFile) Close() error {
	if f.tmp == nil {
		return nil
	}
	defer os.Remove(f.tmp.Name())
	defer f.tmp.Close()
	if f.into == nil {
		return nil
	}
	st, err := f.tmp.Stat()
	if err != nil {
		return err
	}
	if _, err := f.tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	ct := mime.TypeByExtension(path.Ext(f.base))
	if ct == "" {
		ct = "application/octet-stream"
	}
	storageID := ""
	if f.node.file != nil { // a new version stays in the storage of the file
		if m, err := f.fs.s.Storages.FileMeta(f.fs.r.Context(), f.node.file.ID); err == nil && m.StorageID != nil {
			storageID = *m.StorageID
		}
	}
	_, err = f.fs.s.diskStore(f.fs.r, auth.Session{UserID: f.fs.user}, f.into.drive, f.into.folder, storageID, f.base, ct, f.tmp, st.Size())
	return davErr(err)
}

func (f *davFile) load() error {
	if f.tmp != nil || f.node.file == nil {
		return nil
	}
	m, err := f.fs.s.Storages.FileMeta(f.fs.r.Context(), f.node.file.ID)
	if err != nil {
		return davErr(err)
	}
	tmp, err := os.CreateTemp("", "go24-dav-*")
	if err != nil {
		return err
	}
	f.tmp = tmp
	if m.Size == 0 {
		return nil
	}
	obj, err := f.fs.s.Storages.Open(f.fs.r.Context(), m.StorageID, m.Key)
	if err != nil {
		return err
	}
	defer obj.Close()
	if _, err := io.Copy(tmp, obj); err != nil {
		return err
	}
	_, err = tmp.Seek(0, io.SeekStart)
	return err
}

func (f *davFile) Read(p []byte) (int, error) {
	if f.node.isDir() {
		return 0, os.ErrInvalid
	}
	if err := f.load(); err != nil {
		return 0, err
	}
	return f.tmp.Read(p)
}

func (f *davFile) Seek(offset int64, whence int) (int64, error) {
	if f.node.isDir() {
		return 0, os.ErrInvalid
	}
	if f.into == nil && whence == io.SeekEnd && offset == 0 && f.tmp == nil { // the size, without fetching the bytes
		return f.node.file.Size, nil
	}
	if err := f.load(); err != nil {
		return 0, err
	}
	return f.tmp.Seek(offset, whence)
}

func (f *davFile) Write(p []byte) (int, error) {
	if f.into == nil {
		return 0, os.ErrPermission
	}
	return f.tmp.Write(p)
}

func (f *davFile) Stat() (fs.FileInfo, error) {
	if f.into != nil {
		return davInfo{name: f.base}, nil
	}
	return f.node.info(), nil
}

func (f *davFile) Readdir(count int) ([]fs.FileInfo, error) {
	if !f.node.isDir() {
		return nil, os.ErrInvalid
	}
	ctx := f.fs.r.Context()
	var all []fs.FileInfo
	switch f.node.top {
	case "", "shared":
		list, err := f.fs.tops(ctx)
		if f.node.top == "shared" {
			list, err = f.fs.sharedTops(ctx)
		}
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			all = append(all, davInfo{name: e[0], dir: true})
		}
	default:
		folders, files, _, err := f.fs.s.Drive.List(ctx, f.node.drive, f.node.folder)
		if err != nil {
			return nil, davErr(err)
		}
		for _, d := range folders {
			all = append(all, davInfo{name: davName(d.Name), dir: true})
		}
		for _, x := range files {
			all = append(all, davInfo{name: davName(x.Name), size: x.Size, mod: x.CreatedAt, ct: x.ContentType})
		}
	}
	if count <= 0 {
		return all[f.read:], nil
	}
	if f.read >= len(all) {
		return nil, io.EOF
	}
	end := min(len(all), f.read+count)
	out := all[f.read:end]
	f.read = end
	return out, nil
}

// davInfo describes a file or a folder.
type davInfo struct {
	name string
	size int64
	mod  time.Time
	dir  bool
	ct   string
}

func (i davInfo) Name() string { return i.name }
func (i davInfo) Size() int64  { return i.size }
func (i davInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i davInfo) ModTime() time.Time { return i.mod }
func (i davInfo) IsDir() bool        { return i.dir }
func (i davInfo) Sys() any           { return nil }

// ContentType spares WebDAV reading a file to guess its type (webdav.ContentTyper).
func (i davInfo) ContentType(context.Context) (string, error) {
	if i.ct == "" {
		return "", webdav.ErrNotImplemented
	}
	return i.ct, nil
}

// davURL is the address of /dav/ people connect to: over https when go24 has it (Windows asks for https), "" for
// guests (they have no keys).
func (s *Server) davURL(r *http.Request) string {
	if tasks.IsGuestCtx(r.Context()) || s.Keys == nil {
		return ""
	}
	if s.CallURL != "" {
		return s.CallURL + "/dav/"
	}
	return s.absURL(r, "/dav/")
}
