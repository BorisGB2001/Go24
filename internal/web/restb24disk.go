package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST compatible with the portal (GO24DEV-886): the drive — disk.storage.*, disk.folder.*, disk.file.*. Storages are
// go24's drives (a person's «Мой диск», a project's, «Общий диск»), folders and files go by their numbers (migration
// 772); the rights are those of the drive's pages. DOWNLOAD_URL is a signed link of go24 that works without a key for a
// day; a file is uploaded with fileContent (the name and base64) or, without it, by the uploadUrl the
// method gives — a POST of the form's «file» there.

func init() {
	for name, m := range map[string]portalMethod{
		"disk.storage.getfields":       {Scope: "disk:read", Run: (*Server).portalDiskStorageFields},
		"disk.storage.gettypes":        {Scope: "disk:read", Run: (*Server).portalDiskStorageTypes},
		"disk.storage.getlist":         {Scope: "disk:read", Run: (*Server).portalDiskStorages},
		"disk.storage.get":             {Scope: "disk:read", Run: (*Server).portalDiskStorageGet},
		"disk.storage.getchildren":     {Scope: "disk:read", Run: (*Server).portalDiskChildren},
		"disk.storage.addfolder":       {Scope: "disk:write", Run: (*Server).portalDiskAddFolder},
		"disk.storage.uploadfile":      {Scope: "disk:write", Run: (*Server).portalDiskUpload},
		"disk.folder.getfields":        {Scope: "disk:read", Run: (*Server).portalDiskFolderFields},
		"disk.folder.get":              {Scope: "disk:read", Run: (*Server).portalDiskGet},
		"disk.folder.getchildren":      {Scope: "disk:read", Run: (*Server).portalDiskChildren},
		"disk.folder.addsubfolder":     {Scope: "disk:write", Run: (*Server).portalDiskAddFolder},
		"disk.folder.uploadfile":       {Scope: "disk:write", Run: (*Server).portalDiskUpload},
		"disk.folder.rename":           {Scope: "disk:write", Run: (*Server).portalDiskRename},
		"disk.folder.moveto":           {Scope: "disk:write", Run: (*Server).portalDiskMove},
		"disk.folder.copyto":           {Scope: "disk:write", Run: (*Server).portalDiskCopy},
		"disk.folder.markdeleted":      {Scope: "disk:write", Run: (*Server).portalDiskTrash},
		"disk.folder.restore":          {Scope: "disk:write", Run: (*Server).portalDiskRestore},
		"disk.folder.deletetree":       {Scope: "disk:write", Run: (*Server).portalDiskPurge},
		"disk.file.getfields":          {Scope: "disk:read", Run: (*Server).portalDiskFileFields},
		"disk.file.get":                {Scope: "disk:read", Run: (*Server).portalDiskGet},
		"disk.file.rename":             {Scope: "disk:write", Run: (*Server).portalDiskRename},
		"disk.file.moveto":             {Scope: "disk:write", Run: (*Server).portalDiskMove},
		"disk.file.copyto":             {Scope: "disk:write", Run: (*Server).portalDiskCopy},
		"disk.file.markdeleted":        {Scope: "disk:write", Run: (*Server).portalDiskTrash},
		"disk.file.restore":            {Scope: "disk:write", Run: (*Server).portalDiskRestore},
		"disk.file.delete":             {Scope: "disk:write", Run: (*Server).portalDiskPurge},
		"disk.file.getversions":        {Scope: "disk:read", Run: (*Server).portalDiskVersions},
		"disk.file.uploadversion":      {Scope: "disk:write", Run: (*Server).portalDiskUploadVersion},
		"disk.file.restorefromversion": {Scope: "disk:write", Run: (*Server).portalDiskRestoreVersion},
	} {
		portalMethods[name] = m
	}
}

const portalDiskLinkTTL = 24 * time.Hour // how long DOWNLOAD_URL and uploadUrl work

// portalDiskObj is a folder, a file or the top of a drive (Kind root) found by its number.
type portalDiskObj struct {
	Kind, ID string // file | folder | root; the go24 id ("" — the top)
	Num      int64
	Which    string // the drive as Drive.Open takes it
	Drive    drive.Drive
	Deleted  bool
}

// ---------- storages ----------

// portalDiskWhich is the drive of the kind and owner as Drive.Open takes it: someone else's «Мой диск» — an
// administrator's way into it.
func portalDiskWhich(userID, kind, owner, key string) string {
	switch kind {
	case "company":
		return "company"
	case "project":
		return key
	}
	if owner == userID {
		return "my"
	}
	return "u:" + owner
}

// portalDiskStorage is the number of the drive's storage and of the folder of its top, given when asked for first.
func (s *Server) portalDiskStorage(ctx context.Context, d drive.Drive) (num, root int64, err error) {
	var user, project any
	switch d.Kind {
	case "user":
		user = d.OwnerID
	case "project":
		project = d.OwnerID
	}
	q := `SELECT num, root_num FROM portal_disk_storages WHERE user_id IS NOT DISTINCT FROM $1::uuid
		AND project_id IS NOT DISTINCT FROM $2::uuid AND company = $3`
	err = s.DB.QueryRow(ctx, q, user, project, d.Kind == "company").Scan(&num, &root)
	if errors.Is(err, pgx.ErrNoRows) {
		_, _ = s.DB.Exec(ctx, `INSERT INTO portal_disk_storages (user_id, project_id, company) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, user, project, d.Kind == "company")
		err = s.DB.QueryRow(ctx, q, user, project, d.Kind == "company").Scan(&num, &root)
	}
	return num, root, err
}

// portalDiskStorageOut is a storage gives it: ENTITY_TYPE user (ENTITY_ID — the employee's number), group
// (the project's) or common.
func (s *Server) portalDiskStorageOut(c portalCall, d drive.Drive) (map[string]any, error) {
	ctx := c.r.Context()
	num, root, err := s.portalDiskStorage(ctx, d)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ID": strconv.FormatInt(num, 10), "CODE": nil, "MODULE_ID": "disk",
		"ROOT_OBJECT_ID": strconv.FormatInt(root, 10)}
	switch d.Kind {
	case "company":
		out["ENTITY_TYPE"], out["ENTITY_ID"], out["NAME"] = "common", "shared_files_s1", s.tr(c.r, "disk.company")
	case "project":
		var n int64
		if err := s.DB.QueryRow(ctx, `SELECT num, name FROM projects WHERE id = $1`, d.OwnerID).Scan(&n, &d.Name); err != nil {
			return nil, err
		}
		out["ENTITY_TYPE"], out["ENTITY_ID"], out["NAME"] = "group", strconv.FormatInt(n, 10), d.Name
	default:
		var n int64
		var name string
		if err := s.DB.QueryRow(ctx, `SELECT num, name FROM users WHERE id = $1`, d.OwnerID).Scan(&n, &name); err != nil {
			return nil, err
		}
		out["ENTITY_TYPE"], out["ENTITY_ID"], out["NAME"] = "user", strconv.FormatInt(n, 10), name
	}
	return out, nil
}

// portalDiskStorageOf is the drive of the storage number.
func (s *Server) portalDiskStorageOf(c portalCall, v any) (portalDiskObj, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(v)), 10, 64)
	if err != nil {
		return portalDiskObj{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "id")
	}
	var root int64
	var user, project, key string
	err = s.DB.QueryRow(c.r.Context(), `SELECT st.root_num, coalesce(st.user_id::text, ''), coalesce(st.project_id::text, ''),
			coalesce(p.key, '') FROM portal_disk_storages st LEFT JOIN projects p ON p.id = st.project_id WHERE st.num = $1`, n).
		Scan(&root, &user, &project, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalDiskObj{}, i18n.E("err.not_found")
	}
	if err != nil {
		return portalDiskObj{}, err
	}
	return s.portalDiskRoot(c, root, user, project, key)
}

func (s *Server) portalDiskRoot(c portalCall, root int64, user, project, key string) (portalDiskObj, error) {
	o := portalDiskObj{Kind: "root", Num: root}
	switch {
	case user != "":
		o.Which = portalDiskWhich(c.p.UserID, "user", user, "")
	case project != "":
		o.Which = key
	default:
		o.Which = "company"
	}
	var err error
	o.Drive, err = s.portalDiskDrive(c, o.Which)
	return o, err
}

// portalDiskDrive opens a drive; one closed to the caller is not found.
func (s *Server) portalDiskDrive(c portalCall, which string) (drive.Drive, error) {
	d, err := s.Drive.Open(c.r.Context(), c.p.UserID, which)
	if errors.Is(err, tasks.ErrForbidden) || errors.Is(err, drive.ErrForbidden) {
		err = i18n.E("err.not_found")
	}
	return d, err
}

func (s *Server) portalDiskStorageGet(c portalCall) (portalOut, error) {
	o, err := s.portalDiskStorageOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalDiskStorageOut(c, o.Drive)
	return portalOut{Result: out}, err
}

// portalDiskStorages: the caller's «Мой диск», «Общий диск» and the drives of their projects; filter by ID, NAME,
// ENTITY_TYPE, ENTITY_ID (equals).
func (s *Server) portalDiskStorages(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	var drives []drive.Drive
	for _, which := range []string{"my", "company"} {
		if d, err := s.Drive.Open(ctx, c.p.UserID, which); err == nil {
			drives = append(drives, d)
		}
	}
	projects, err := s.Tasks.ListProjects(ctx, c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	for _, pr := range projects {
		if d, err := s.Drive.Open(ctx, c.p.UserID, pr.Key); err == nil && !pr.Archived {
			drives = append(drives, d)
		}
	}
	filter := portalMap(portalParam(c.in, "filter"))
	list := []map[string]any{}
	for _, d := range drives {
		out, err := s.portalDiskStorageOut(c, d)
		if err != nil {
			return portalOut{}, err
		}
		if portalDiskMatch(out, filter) {
			list = append(list, out)
		}
	}
	start := portalStart(c.in)
	page := list[min(start, len(list)):min(start+portalPage, len(list))]
	return portalPaged(page, start, len(page), len(list)), nil
}

func (s *Server) portalDiskStorageTypes(c portalCall) (portalOut, error) {
	return portalOut{Result: []string{"user", "common", "group"}}, nil
}

func portalDiskFields(names ...string) map[string]any {
	out := map[string]any{}
	for _, n := range names {
		name, typ, _ := strings.Cut(n, ":")
		out[name] = map[string]any{"TYPE": typ, "USE_IN_FILTER": name != "DOWNLOAD_URL" && name != "DETAIL_URL",
			"USE_IN_SHOW": true}
	}
	return out
}

func (s *Server) portalDiskStorageFields(c portalCall) (portalOut, error) {
	return portalOut{Result: portalDiskFields("ID:integer", "NAME:string", "CODE:string", "MODULE_ID:string", "ENTITY_TYPE:string",
		"ENTITY_ID:string", "ROOT_OBJECT_ID:integer")}, nil
}

func (s *Server) portalDiskFolderFields(c portalCall) (portalOut, error) {
	return portalOut{Result: portalDiskFields("ID:integer", "NAME:string", "TYPE:string", "CODE:string", "STORAGE_ID:integer",
		"REAL_OBJECT_ID:integer", "PARENT_ID:integer", "DELETED_TYPE:integer", "CREATE_TIME:datetime", "UPDATE_TIME:datetime",
		"DELETE_TIME:datetime", "CREATED_BY:integer", "UPDATED_BY:integer", "DELETED_BY:integer", "DETAIL_URL:string")}, nil
}

func (s *Server) portalDiskFileFields(c portalCall) (portalOut, error) {
	return portalOut{Result: portalDiskFields("ID:integer", "NAME:string", "TYPE:string", "CODE:string", "STORAGE_ID:integer",
		"REAL_OBJECT_ID:integer", "PARENT_ID:integer", "DELETED_TYPE:integer", "GLOBAL_CONTENT_VERSION:integer",
		"FILE_ID:integer", "SIZE:integer", "CREATE_TIME:datetime", "UPDATE_TIME:datetime", "DELETE_TIME:datetime",
		"CREATED_BY:integer", "UPDATED_BY:integer", "DELETED_BY:integer", "DOWNLOAD_URL:string", "DETAIL_URL:string")}, nil
}

// portalDiskMatch: the record has the values of the filter's fields (a name with % — contains it).
func portalDiskMatch(rec, filter map[string]any) bool {
	for k, v := range filter {
		key := strings.ToUpper(strings.TrimLeft(k, "=%"))
		got, want := portalStr(rec[key]), portalStr(v)
		if strings.HasPrefix(k, "%") {
			if !strings.Contains(strings.ToLower(got), strings.ToLower(want)) {
				return false
			}
		} else if _, ok := rec[key]; ok && !strings.EqualFold(got, want) {
			return false
		}
	}
	return true
}

// ---------- folders and files ----------

// portalDiskRef finds the folder or file of the number (of the kind; "" — either), or the top of a drive by its
// ROOT_OBJECT_ID. One in the trash is found only when deleted is true.
func (s *Server) portalDiskRef(c portalCall, v any, kind string, deleted bool) (portalDiskObj, error) {
	ctx := c.r.Context()
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(v)), 10, 64)
	if err != nil {
		return portalDiskObj{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "id")
	}
	if kind != "file" {
		var user, project, key string
		err := s.DB.QueryRow(ctx, `SELECT coalesce(st.user_id::text, ''), coalesce(st.project_id::text, ''), coalesce(p.key, '')
			FROM portal_disk_storages st LEFT JOIN projects p ON p.id = st.project_id WHERE st.root_num = $1`, n).Scan(&user, &project, &key)
		if err == nil {
			return s.portalDiskRoot(c, n, user, project, key)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return portalDiskObj{}, err
		}
	}
	o := portalDiskObj{Num: n}
	var dk, owner, key string
	err = pgx.ErrNoRows
	if kind != "file" {
		o.Kind = "folder"
		err = s.DB.QueryRow(ctx, `SELECT f.id::text, f.drive, coalesce(f.owner_id::text, ''), coalesce(p.key, ''), f.deleted_at IS NOT NULL
			FROM drive_folders f LEFT JOIN projects p ON f.drive = 'project' AND p.id = f.owner_id WHERE f.num = $1`, n).
			Scan(&o.ID, &dk, &owner, &key, &o.Deleted)
	}
	if errors.Is(err, pgx.ErrNoRows) && kind != "folder" {
		o.Kind = "file"
		err = s.DB.QueryRow(ctx, `SELECT f.id::text, f.drive, coalesce(f.drive_owner::text, ''), coalesce(p.key, ''), f.deleted_at IS NOT NULL
			FROM files f LEFT JOIN projects p ON f.drive = 'project' AND p.id = f.drive_owner WHERE f.num = $1 AND f.drive IS NOT NULL`, n).
			Scan(&o.ID, &dk, &owner, &key, &o.Deleted)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return portalDiskObj{}, i18n.E("err.not_found")
	}
	if err != nil {
		return portalDiskObj{}, err
	}
	o.Which = portalDiskWhich(c.p.UserID, dk, owner, key)
	if o.Drive, err = s.portalDiskDrive(c, o.Which); err != nil {
		return portalDiskObj{}, err
	}
	if o.Deleted != deleted {
		return portalDiskObj{}, i18n.E("err.not_found")
	}
	if !o.Deleted { // seen by the caller: not in a folder closed to them
		if o.Kind == "file" {
			_, err = s.Drive.FileOn(ctx, o.Drive, o.ID)
		} else {
			_, _, _, err = s.Drive.List(ctx, o.Drive, o.ID)
		}
	}
	return o, err
}

// portalDiskTarget is a folder where things go (a folder's number or ROOT_OBJECT_ID): the drive and the folder ("" — the
// top).
func (s *Server) portalDiskTarget(c portalCall, v any) (portalDiskObj, error) {
	return s.portalDiskRef(c, v, "folder", false)
}

func portalDiskNull(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func portalDiskTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.RFC3339)
}

func portalDiskNum(n int64) any {
	if n == 0 {
		return nil
	}
	return strconv.FormatInt(n, 10)
}

// portalDiskOut is a folder or a file of the drive gives it, read with what the drive's numbers say.
func (s *Server) portalDiskOut(c portalCall, d drive.Drive, kind, id string) (map[string]any, error) {
	ctx := c.r.Context()
	storage, root, err := s.portalDiskStorage(ctx, d)
	if err != nil {
		return nil, err
	}
	var num, parent, by, changedBy, deletedBy, size, versions int64
	var name, folder string
	var created, updated time.Time
	var deletedAt *time.Time
	if kind == "folder" {
		err = s.DB.QueryRow(ctx, `SELECT f.num, f.name, coalesce(up.num, 0), coalesce(u.num, 0), f.created_at, f.deleted_at,
				coalesce(du.num, 0)
			FROM drive_folders f LEFT JOIN drive_folders up ON up.id = f.parent_id LEFT JOIN users u ON u.id = f.created_by
				LEFT JOIN users du ON du.id = f.deleted_by WHERE f.id::text = $1`, id).
			Scan(&num, &name, &parent, &by, &created, &deletedAt, &deletedBy)
		changedBy, updated = by, created
	} else {
		err = s.DB.QueryRow(ctx, `SELECT f.num, f.name, coalesce(df.num, 0), coalesce(df.id::text, ''), coalesce(u.num, 0),
				coalesce(cu.num, u.num, 0), f.size, f.created_at, least(f.created_at, (SELECT min(v.created_at) FROM drive_file_versions v
				WHERE v.file_id = f.id)), f.deleted_at, coalesce(du.num, 0),
				1 + (SELECT count(*) FROM drive_file_versions v WHERE v.file_id = f.id)
			FROM files f LEFT JOIN drive_folders df ON df.id = f.folder_id LEFT JOIN users u ON u.id = f.uploaded_by
				LEFT JOIN users cu ON cu.id = f.changed_by LEFT JOIN users du ON du.id = f.deleted_by WHERE f.id::text = $1`, id).
			Scan(&num, &name, &parent, &folder, &by, &changedBy, &size, &updated, &created, &deletedAt, &deletedBy, &versions)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, i18n.E("err.not_found")
	}
	if err != nil {
		return nil, err
	}
	if parent == 0 || folder != "" && folder == d.Root {
		parent = root
	}
	deletedType := "0"
	if deletedAt != nil {
		deletedType = "3" // deleted by a person DELETED_TYPE_ROOT
	}
	out := map[string]any{"ID": strconv.FormatInt(num, 10), "NAME": name, "CODE": nil, "STORAGE_ID": strconv.FormatInt(storage, 10),
		"TYPE": kind, "REAL_OBJECT_ID": strconv.FormatInt(num, 10), "PARENT_ID": strconv.FormatInt(parent, 10),
		"DELETED_TYPE": deletedType, "CREATE_TIME": portalDiskTime(&created), "UPDATE_TIME": portalDiskTime(&updated),
		"DELETE_TIME": portalDiskTime(deletedAt), "CREATED_BY": portalDiskNum(by), "UPDATED_BY": portalDiskNum(changedBy),
		"DELETED_BY": portalDiskNum(deletedBy)}
	if kind == "folder" {
		out["DETAIL_URL"] = s.BaseURL + d.Path() + "?folder=" + id
		return out, nil
	}
	out["GLOBAL_CONTENT_VERSION"] = strconv.FormatInt(versions, 10)
	out["FILE_ID"] = strconv.FormatInt(num, 10)
	out["SIZE"] = strconv.FormatInt(size, 10)
	out["DETAIL_URL"] = s.BaseURL + "/files/" + id + "/about"
	out["DOWNLOAD_URL"] = s.portalDiskLink("download", portalDiskTicket{User: c.p.UserID, File: id})
	return out, nil
}

// portalDiskRootOut is the folder of a drive's top, as disk.folder.get of ROOT_OBJECT_ID gives it.
func (s *Server) portalDiskRootOut(c portalCall, o portalDiskObj) (map[string]any, error) {
	st, err := s.portalDiskStorageOut(c, o.Drive)
	if err != nil {
		return nil, err
	}
	n := strconv.FormatInt(o.Num, 10)
	return map[string]any{"ID": n, "NAME": st["NAME"], "CODE": nil, "STORAGE_ID": st["ID"], "TYPE": "folder", "REAL_OBJECT_ID": n,
		"PARENT_ID": nil, "DELETED_TYPE": "0", "CREATE_TIME": nil, "UPDATE_TIME": nil, "DELETE_TIME": nil, "CREATED_BY": nil,
		"UPDATED_BY": nil, "DELETED_BY": nil, "DETAIL_URL": s.BaseURL + o.Drive.Path()}, nil
}

func (s *Server) portalDiskObjOut(c portalCall, o portalDiskObj) (portalOut, error) {
	if o.Kind == "root" {
		out, err := s.portalDiskRootOut(c, o)
		return portalOut{Result: out}, err
	}
	out, err := s.portalDiskOut(c, o.Drive, o.Kind, o.ID)
	return portalOut{Result: out}, err
}

// portalDiskKind is the kind of object a method is about: disk.file.* — a file, the others — a folder (or the top of a
// storage).
func portalDiskKind(c portalCall) string {
	if strings.HasPrefix(c.method, "disk.file.") {
		return "file"
	}
	return "folder"
}

func (s *Server) portalDiskGet(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), portalDiskKind(c), false)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalDiskObjOut(c, o)
}

// portalDiskChildren: the folders and files of a folder (disk.folder.getchildren) or of a storage's top
// (disk.storage.getchildren), folders first; filter by NAME, TYPE and the other fields (equals; %NAME — contains).
func (s *Server) portalDiskChildren(c portalCall) (portalOut, error) {
	var o portalDiskObj
	var err error
	if strings.HasPrefix(c.method, "disk.storage.") {
		o, err = s.portalDiskStorageOf(c, portalParam(c.in, "id"))
	} else {
		o, err = s.portalDiskRef(c, portalParam(c.in, "id"), "folder", false)
	}
	if err != nil {
		return portalOut{}, err
	}
	folders, files, _, err := s.Drive.List(c.r.Context(), o.Drive, o.ID)
	if err != nil {
		return portalOut{}, err
	}
	filter := portalMap(portalParam(c.in, "filter"))
	list := []map[string]any{}
	for _, f := range folders {
		out, err := s.portalDiskOut(c, o.Drive, "folder", f.ID)
		if err != nil {
			return portalOut{}, err
		}
		if portalDiskMatch(out, filter) {
			list = append(list, out)
		}
	}
	for _, f := range files {
		out, err := s.portalDiskOut(c, o.Drive, "file", f.ID)
		if err != nil {
			return portalOut{}, err
		}
		if portalDiskMatch(out, filter) {
			list = append(list, out)
		}
	}
	start := portalStart(c.in)
	page := list[min(start, len(list)):min(start+portalPage, len(list))]
	return portalPaged(page, start, len(page), len(list)), nil
}

// portalDiskData is the NAME of data[] (or of the parameter name).
func portalDiskData(c portalCall, name string) string {
	if v := portalStr(portalParam(portalMap(portalParam(c.in, "data")), "NAME")); v != "" {
		return v
	}
	return portalStr(portalParam(c.in, name))
}

// portalDiskParent is where a method of a storage or a folder puts a folder or a file.
func (s *Server) portalDiskParent(c portalCall) (portalDiskObj, error) {
	if strings.HasPrefix(c.method, "disk.storage.") {
		return s.portalDiskStorageOf(c, portalParam(c.in, "id"))
	}
	return s.portalDiskTarget(c, portalParam(c.in, "id"))
}

func (s *Server) portalDiskAddFolder(c portalCall) (portalOut, error) {
	o, err := s.portalDiskParent(c)
	if err != nil {
		return portalOut{}, err
	}
	name := strings.TrimSpace(portalDiskData(c, "name"))
	if same, err := s.portalDiskNameTaken(c, o, name, ""); err != nil {
		return portalOut{}, err
	} else if same {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "DISK_OBJ_22000", "err.portal_disk_exists", name)
	}
	id, err := s.Drive.CreateFolder(c.r.Context(), c.p.UserID, o.Drive, o.ID, name)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalDiskOut(c, o.Drive, "folder", id)
	return portalOut{Result: out}, err
}

// portalDiskNameTaken: the folder (of the drive's top: "") has a folder or a file of the name, but the one of id.
func (s *Server) portalDiskNameTaken(c portalCall, o portalDiskObj, name, id string) (bool, error) {
	folders, files, _, err := s.Drive.List(c.r.Context(), o.Drive, o.ID)
	if err != nil {
		return false, err
	}
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) && f.ID != id {
			return true, nil
		}
	}
	for _, f := range files {
		if strings.EqualFold(f.Name, name) && f.ID != id {
			return true, nil
		}
	}
	return false, nil
}

// portalDiskContent is fileContent: [name, base64] (or {"0": name, "1": base64}), or base64 alone with the name in
// data[NAME]; ok false — none was given.
func portalDiskContent(c portalCall) (name string, data []byte, ok bool, err error) {
	v := portalParam(c.in, "fileContent")
	var b64 string
	switch x := v.(type) {
	case nil:
		return portalDiskData(c, "name"), nil, false, nil
	case []any:
		if len(x) > 0 {
			name = portalStr(x[0])
		}
		if len(x) > 1 {
			b64 = portalStr(x[1])
		}
	case map[string]any:
		name, b64 = portalStr(x["0"]), portalStr(x["1"])
	default:
		b64 = portalStr(x)
	}
	if n := portalDiskData(c, "name"); n != "" {
		name = n
	}
	if _, rest, cut := strings.Cut(b64, ";base64,"); cut { // a data: address
		b64 = rest
	}
	data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", nil, true, i18n.E("err.portal_field", "fileContent")
	}
	return name, data, true, nil
}

// portalDiskContentType is the type of a file by its name, or by its first bytes.
func portalDiskContentType(name string, data []byte) string {
	if ct := mime.TypeByExtension(strings.ToLower(path.Ext(name))); ct != "" {
		return ct
	}
	return http.DetectContentType(data)
}

// portalDiskUpload: disk.storage.uploadfile, disk.folder.uploadfile — with fileContent the file is stored at once,
// without it the answer is {field, uploadUrl}, where the form's «file» goes. A name already in the folder is an error,
// , unless generateUniqueName is given: then the file is «name (2)».
func (s *Server) portalDiskUpload(c portalCall) (portalOut, error) {
	o, err := s.portalDiskParent(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Drive.CheckFolder(c.r.Context(), o.Drive, o.ID); err != nil {
		return portalOut{}, err
	}
	name, data, ok, err := portalDiskContent(c)
	if err != nil {
		return portalOut{}, err
	}
	t := portalDiskTicket{User: c.p.UserID, Which: o.Which, Folder: o.ID, Name: name, Unique: formTrue(portalStr(portalParam(c.in, "generateUniqueName")))}
	if !ok {
		return portalOut{Result: map[string]any{"field": "file", "uploadUrl": s.portalDiskLink("upload", t)}}, nil
	}
	id, err := s.portalDiskStore(c.r, o.Drive, t, bytes.NewReader(data), int64(len(data)), portalDiskContentType(name, data))
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalDiskOut(c, o.Drive, "file", id)
	return portalOut{Result: out}, err
}

// portalDiskStore keeps an uploaded file as the ticket says: a new file of the folder, or a new version of the file.
func (s *Server) portalDiskStore(r *http.Request, d drive.Drive, t portalDiskTicket, f io.Reader, size int64, ct string) (string, error) {
	ctx := r.Context()
	sess := auth.Session{UserID: t.User}
	if t.Version != "" {
		if err := s.Drive.CanChange(ctx, t.User, d, t.Version); err != nil {
			return "", err
		}
		if err := s.Drive.Fits(ctx, d, size); err != nil {
			return "", err
		}
		key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), t.Name)
		if err := s.Storages.Put(ctx, t.User, tasks.IsAdmin(ctx), "", key, f, size, ct); err != nil {
			return "", err
		}
		return t.Version, s.Drive.AddVersion(ctx, t.User, d, t.Version, drive.NewVersion{Key: key, ContentType: ct, Size: size})
	}
	name := strings.TrimSpace(path.Base(strings.ReplaceAll(t.Name, "\\", "/")))
	if name == "" || name == "." || name == "/" {
		return "", i18n.E("err.portal_field", "NAME")
	}
	if t.Unique {
		var err error
		if name, err = s.Drive.FreeFileName(ctx, d, t.Folder, name); err != nil {
			return "", err
		}
	} else if same, err := s.portalDiskNameTaken(portalCall{r: r}, portalDiskObj{Drive: d, ID: t.Folder}, name, ""); err != nil {
		return "", err
	} else if same {
		return "", s.portalErr(r, http.StatusBadRequest, "DISK_OBJ_22000", "err.portal_disk_exists", name)
	}
	return s.diskStore(r, sess, d, t.Folder, "", name, ct, f, size)
}

func (s *Server) portalDiskRename(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), portalDiskKind(c), false)
	if err != nil {
		return portalOut{}, err
	}
	if o.Kind == "root" {
		return portalOut{}, tasks.ErrForbidden
	}
	name := strings.TrimSpace(portalStr(portalParam(c.in, "newName")))
	ctx := c.r.Context()
	if o.Kind == "file" {
		err = s.Drive.RenameFile(ctx, c.p.UserID, o.Drive, o.ID, name)
	} else {
		err = s.Drive.RenameFolder(ctx, c.p.UserID, o.Drive, o.ID, name)
	}
	if err != nil {
		return portalOut{}, err
	}
	return s.portalDiskObjOut(c, o)
}

func (o portalDiskObj) selection() drive.Selection {
	if o.Kind == "file" {
		return drive.Selection{Files: []string{o.ID}}
	}
	return drive.Selection{Folders: []string{o.ID}}
}

// portalDiskMove: disk.*.moveto — into targetFolderId, of this drive or another.
func (s *Server) portalDiskMove(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), portalDiskKind(c), false)
	if err != nil {
		return portalOut{}, err
	}
	to, err := s.portalDiskTarget(c, portalParam(c.in, "targetFolderId"))
	if err != nil {
		return portalOut{}, err
	}
	if o.Kind == "root" {
		return portalOut{}, tasks.ErrForbidden
	}
	if err := s.Drive.Move(c.r.Context(), c.p.UserID, o.Drive, o.selection(), to.Drive, to.ID); err != nil {
		return portalOut{}, err
	}
	out, err := s.portalDiskOut(c, to.Drive, o.Kind, o.ID)
	return portalOut{Result: out}, err
}

// portalDiskCopy: disk.*.copyto — a copy in targetFolderId (a name met there is «name (2)»); the answer is the copy.
func (s *Server) portalDiskCopy(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), portalDiskKind(c), false)
	if err != nil {
		return portalOut{}, err
	}
	to, err := s.portalDiskTarget(c, portalParam(c.in, "targetFolderId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalDiskCopyTo(c, o, to)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalDiskOut(c, to.Drive, o.Kind, id)
	return portalOut{Result: out}, err
}

// portalDiskCopyTo copies the folder or file into the folder to; the copy's id.
func (s *Server) portalDiskCopyTo(c portalCall, o, to portalDiskObj) (string, error) {
	if o.Kind == "root" {
		return "", tasks.ErrForbidden
	}
	ctx := c.r.Context()
	var before int64
	if err := s.DB.QueryRow(ctx, `SELECT last_value FROM portal_disk_objects_num`).Scan(&before); err != nil {
		return "", err
	}
	if err := s.Drive.Copy(ctx, c.p.UserID, o.Drive, o.selection(), to.Drive, to.ID, s.Storages.Copy); err != nil {
		return "", err
	}
	// the copy is the newest object of the kind in the target folder
	var id string
	q := `SELECT id::text FROM files WHERE drive = $1 AND drive_owner IS NOT DISTINCT FROM $2::uuid AND folder_id IS NOT DISTINCT FROM $3::uuid
		AND num > $4 AND deleted_at IS NULL ORDER BY num LIMIT 1`
	if o.Kind == "folder" {
		q = `SELECT id::text FROM drive_folders WHERE drive = $1 AND owner_id IS NOT DISTINCT FROM $2::uuid AND parent_id IS NOT DISTINCT FROM $3::uuid
			AND num > $4 AND deleted_at IS NULL ORDER BY num LIMIT 1`
	}
	folder := to.Drive.At(to.ID)
	err := s.DB.QueryRow(ctx, q, to.Drive.Kind, portalDiskNull(to.Drive.OwnerID), portalDiskNull(folder), before).Scan(&id)
	return id, err
}

// portalDiskTrash: disk.*.markdeleted — into the drive's trash; the answer is the object with DELETED_TYPE.
func (s *Server) portalDiskTrash(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), portalDiskKind(c), false)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalDiskDelete(c, o); err != nil {
		return portalOut{}, err
	}
	return s.portalDiskObjOut(c, o)
}

func (s *Server) portalDiskDelete(c portalCall, o portalDiskObj) error {
	switch o.Kind {
	case "file":
		return s.Drive.DeleteFile(c.r.Context(), c.p.UserID, o.Drive, o.ID)
	case "folder":
		return s.Drive.DeleteFolder(c.r.Context(), c.p.UserID, o.Drive, o.ID)
	}
	return tasks.ErrForbidden
}

// portalDiskRestore: disk.*.restore — out of the trash back to where it was.
func (s *Server) portalDiskRestore(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), portalDiskKind(c), true)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Drive.Restore(c.r.Context(), c.p.UserID, o.Drive, o.selection()); err != nil {
		return portalOut{}, err
	}
	return s.portalDiskObjOut(c, o)
}

// portalDiskPurge: disk.file.delete, disk.folder.deletetree — for good, from the drive or from its trash.
func (s *Server) portalDiskPurge(c portalCall) (portalOut, error) {
	kind := portalDiskKind(c)
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), kind, false)
	if err != nil {
		var trashed error
		if o, trashed = s.portalDiskRef(c, portalParam(c.in, "id"), kind, true); trashed != nil {
			return portalOut{}, err
		}
	} else if err := s.portalDiskDelete(c, o); err != nil {
		return portalOut{}, err
	}
	if err := s.Drive.Purge(c.r.Context(), c.p.UserID, o.Drive, o.selection()); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// portalDiskVersions: the file's versions, the newest first; a version's ID is its place in the history from 1.
func (s *Server) portalDiskVersions(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), "file", false)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Drive.Versions(c.r.Context(), o.ID)
	if err != nil {
		return portalOut{}, err
	}
	var name string
	if err := s.DB.QueryRow(c.r.Context(), `SELECT name FROM files WHERE id::text = $1`, o.ID).Scan(&name); err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, v := range list {
		var by int64
		if v.AuthorID != "" {
			by, _ = s.portalUserNum(c, v.AuthorID)
		}
		out = append(out, map[string]any{"ID": strconv.Itoa(v.No), "OBJECT_ID": strconv.FormatInt(o.Num, 10), "SIZE": strconv.FormatInt(v.Size, 10),
			"NAME": name, "CREATE_TIME": portalDiskTime(&v.CreatedAt), "CREATED_BY": portalDiskNum(by), "GLOBAL_CONTENT_VERSION": strconv.Itoa(v.No),
			"DOWNLOAD_URL": s.portalDiskLink("download", portalDiskTicket{User: c.p.UserID, File: o.ID, Version: v.ID})})
	}
	return portalOut{Result: out}, nil
}

// portalDiskUploadVersion: disk.file.uploadversion — fileContent (or the uploadUrl it gives) is the file's new version.
func (s *Server) portalDiskUploadVersion(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), "file", false)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Drive.CanChange(c.r.Context(), c.p.UserID, o.Drive, o.ID); err != nil {
		return portalOut{}, err
	}
	var name string
	if err := s.DB.QueryRow(c.r.Context(), `SELECT name FROM files WHERE id::text = $1`, o.ID).Scan(&name); err != nil {
		return portalOut{}, err
	}
	_, data, ok, err := portalDiskContent(c)
	if err != nil {
		return portalOut{}, err
	}
	t := portalDiskTicket{User: c.p.UserID, Which: o.Which, Name: name, Version: o.ID}
	if !ok {
		return portalOut{Result: map[string]any{"field": "file", "uploadUrl": s.portalDiskLink("upload", t)}}, nil
	}
	if _, err := s.portalDiskStore(c.r, o.Drive, t, bytes.NewReader(data), int64(len(data)), portalDiskContentType(name, data)); err != nil {
		return portalOut{}, err
	}
	return s.portalDiskObjOut(c, o)
}

// portalDiskRestoreVersion: disk.file.restorefromversion — versionId (its place in the history) is the current again.
func (s *Server) portalDiskRestoreVersion(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "id"), "file", false)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Drive.Versions(c.r.Context(), o.ID)
	if err != nil {
		return portalOut{}, err
	}
	no, _ := strconv.Atoi(strings.TrimSpace(portalStr(portalParam(c.in, "versionId"))))
	for _, v := range list {
		if v.No != no {
			continue
		}
		if v.ID != "" { // the current one is restored as it is
			if err := s.Drive.RestoreVersion(c.r.Context(), c.p.UserID, o.Drive, o.ID, v.ID); err != nil {
				return portalOut{}, err
			}
		}
		return s.portalDiskObjOut(c, o)
	}
	return portalOut{}, i18n.E("err.not_found")
}

// ---------- signed links: DOWNLOAD_URL and uploadUrl ----------

// portalDiskTicket is what a signed link lets do, for whom and till when: download File (its Version: "" — the current
// one), or upload into Folder of the drive Which as Name (Unique — «name (2)» when taken), or a new version of the
// file Version.
type portalDiskTicket struct {
	User    string `json:"u"`
	File    string `json:"f,omitempty"`
	Which   string `json:"w,omitempty"`
	Folder  string `json:"d,omitempty"`
	Name    string `json:"n,omitempty"`
	Unique  bool   `json:"q,omitempty"`
	Version string `json:"v,omitempty"`
	Exp     int64  `json:"e"`
}

// portalDiskLink is the signed address of go24 that does what the ticket says (kind download or upload) for a day.
func (s *Server) portalDiskLink(kind string, t portalDiskTicket) string {
	t.Exp = time.Now().Add(portalDiskLinkTTL).Unix()
	raw, _ := json.Marshal(t)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return s.BaseURL + "/rest/" + kind + "?token=" + payload + "." + s.Sessions.Stamp("portal-disk-"+kind, payload)
}

// portalDiskOpen reads the ticket of a signed link and makes the request the employee's, as a REST call is: an active
// employee, access by IP.
func (s *Server) portalDiskOpen(r *http.Request, kind string) (*http.Request, portalDiskTicket, error) {
	var t portalDiskTicket
	payload, stamp, _ := strings.Cut(r.URL.Query().Get("token"), ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || !s.Sessions.Stamped(stamp, "portal-disk-"+kind, payload) || json.Unmarshal(raw, &t) != nil ||
		time.Now().Unix() > t.Exp {
		return r, t, i18n.E("err.portal_disk_link")
	}
	var active, guest bool
	if err := s.DB.QueryRow(r.Context(), `SELECT active, guest FROM users WHERE id::text = $1`, t.User).Scan(&active, &guest); err != nil || !active {
		return r, t, i18n.E("err.portal_disk_link")
	}
	if !s.ipAllowed(r, t.User) {
		return r, t, i18n.E("ipaccess.denied", audit.ClientIP(r))
	}
	return r.WithContext(tasks.WithViewer(r.Context(), t.User, guest)), t, nil
}

// portalDiskDownload: GET /rest/download?token= — the bytes of DOWNLOAD_URL, while the employee still may read the file.
func (s *Server) portalDiskDownload(w http.ResponseWriter, r *http.Request) {
	r, t, err := s.portalDiskOpen(r, "download")
	if err == nil {
		_, err = s.fileAccess(r, auth.Session{UserID: t.User}, t.File)
	}
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	if t.Version == "" {
		if err := s.apiSendFile(w, r, t.File); err != nil {
			s.portalFail(w, r, err)
		}
		return
	}
	e, err := s.Drive.VersionObject(r.Context(), t.File, t.Version)
	var obj io.ReadCloser
	if err == nil {
		obj, err = s.Storages.Open(r.Context(), e.StorageID, e.Key)
	}
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	defer obj.Close()
	var name string
	_ = s.DB.QueryRow(r.Context(), `SELECT name FROM files WHERE id::text = $1`, t.File).Scan(&name)
	w.Header().Set("Content-Type", e.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	_, _ = io.Copy(w, obj)
}

// portalDiskUploaded: POST /rest/upload?token= — the form's «file» of uploadUrl; the answer is the file.
func (s *Server) portalDiskUploaded(w http.ResponseWriter, r *http.Request) {
	r, t, err := s.portalDiskOpen(r, "upload")
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	start := time.Now()
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		s.portalFail(w, r, i18n.E("err.file_required"))
		return
	}
	defer r.MultipartForm.RemoveAll()
	hdr := r.MultipartForm.File["file"]
	if len(hdr) == 0 {
		s.portalFail(w, r, i18n.E("err.file_required"))
		return
	}
	d, err := s.Drive.Open(r.Context(), t.User, t.Which)
	if err == nil {
		err = s.Drive.CheckFolder(r.Context(), d, t.Folder)
	}
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	if t.Name == "" {
		t.Name = hdr[0].Filename
	}
	f, err := hdr[0].Open()
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	defer f.Close()
	ct := hdr[0].Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		ct = portalDiskContentType(t.Name, nil)
	}
	id, err := s.portalDiskStore(r, d, t, f, hdr[0].Size, ct)
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	out, err := s.portalDiskOut(portalCall{r: r, p: apikeys.Principal{UserID: t.User}}, d, "file", id)
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out, "time": portalTime(start)})
}
