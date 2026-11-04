package bp

import (
	"cmp"
	"context"
	"strconv"
	"strings"
	"time"

	"go24/internal/drive"
	"go24/internal/i18n"
)

// The steps of the «Диск» group of the designer (DiskKinds): «Создать папку» (diskfolder) makes the folders
// of a path on a drive, «Копирование и перемещение» (diskcopy) puts a file or a folder into a folder of a drive,
// «Удаление объекта Диска» (diskdelete) puts it into the trash, «Информация об объекте Диска» (diskread) puts its
// name, size, link… into the process. They act of the step's From (the starter by default) with their rights on the
// drives, as «От имени пользователя» of those steps.

// stepObject is the file or folder a step of DiskKinds works on: of its Value ({…} filled — an id or a link), or else
// the process's file, or else the folder the process made last.
func (s *Store) stepObject(ctx context.Context, x Instance, st Step, userID string) (drive.Object, error) {
	v := strings.TrimSpace(s.fillRaw(ctx, x, st.Value))
	if v == "" && st.Value == "" {
		v = x.Values["created_folder"]
		if x.Entity == EntityFile {
			v = x.EntityID
		}
	}
	if v == "" {
		return drive.Object{}, i18n.E("err.bp_disk_object")
	}
	o, err := s.Drive.ObjectOf(ctx, userID, v)
	if err == drive.ErrNotFound {
		return o, i18n.E("err.bp_disk_object")
	}
	return o, err
}

// diskStep does a step of DiskKinds and says what for the process's history.
func (s *Store) diskStep(ctx context.Context, x Instance, st Step) (string, error) {
	if s.Drive == nil {
		return "", i18n.E("err.bp_no_disk")
	}
	userID, err := s.who(ctx, x, cmp.Or(st.From, "starter"))
	if err != nil {
		return "", err
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	if st.Kind == "diskfolder" {
		o, err := s.Drive.MakeFolder(ctx, userID, st.Drive, s.fill(ctx, x, st.Folder))
		if err != nil {
			return "", err
		}
		x.Values["created_folder"], x.Values["created_folder_id"] = o.Link(), o.ID
		return "diskfolder:" + o.ID, s.saveValues(ctx, x)
	}
	o, err := s.stepObject(ctx, x, st, userID)
	if err != nil {
		return "", err
	}
	switch st.Kind {
	case "diskread":
		for k, v := range diskInfo(o) {
			x.Values[DiskInfoPrefix+k] = v
		}
		return "diskread:" + o.ID, s.saveValues(ctx, x)
	case "diskdelete":
		return "diskdelete:" + o.ID, s.Drive.TrashObject(ctx, userID, o)
	}
	to := ""
	if dir := s.fill(ctx, x, st.Folder); strings.Trim(dir, "/ ") != "" {
		f, err := s.Drive.MakeFolder(ctx, userID, st.Drive, dir)
		if err != nil {
			return "", err
		}
		to = f.ID
	}
	return "diskcopy:" + o.ID, s.Drive.CopyObject(ctx, userID, o, st.Drive, to, st.Move, s.CopyBytes)
}

// diskInfo are the fields of DiskInfo of an object (by their keys without the prefix).
func diskInfo(o drive.Object) map[string]string {
	out := map[string]string{"id": o.ID, "name": o.Name, "type": o.Kind, "link": o.Link(), "folder": o.Folder,
		"folder_link": o.FolderLink(), "drive": o.Drive.Path(), "by": o.ByID, "created": o.CreatedAt.In(time.Local).Format("2006-01-02T15:04"),
		"size": "", "content_type": o.ContentType}
	if o.Kind == "file" {
		out["size"] = strconv.FormatInt(o.Size, 10)
	}
	return out
}
