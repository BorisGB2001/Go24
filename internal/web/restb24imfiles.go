package web

import (
	"net/http"
	"path"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST compatible with the portal, the messenger part 6 (GO24DEV-937): a chat's files — im.disk.folder.get,
// im.disk.file.commit, im.disk.file.save, im.disk.file.delete — and im.chat.updateColor. A chat's folder is where
// «Прикрепить → Документ» keeps its documents: «Сохранённые файлы» of the project's drive for a chat of a project
// (a collab's, a task's) when the caller may write there, of the caller's «Мой диск» for any other; an application
// uploads there with disk.folder.uploadfile and sends the files into the chat with im.disk.file.commit.

func init() {
	for name, m := range map[string]portalMethod{
		"im.disk.folder.get":  {Scope: "im:read", Run: (*Server).portalChatFolder},
		"im.disk.file.commit": {Scope: "im:write", Run: (*Server).portalChatFileCommit},
		"im.disk.file.save":   {Scope: "im:write", Run: (*Server).portalChatFileSave},
		"im.disk.file.delete": {Scope: "im:write", Run: (*Server).portalChatFileDelete},
		"im.chat.updateColor": {Scope: "im:write", Run: (*Server).portalChatColor},
	} {
		portalMethods[strings.ToLower(name)] = m
	}
}

// portalChatDialog is the room of CHAT_ID or DIALOG_ID.
func (s *Server) portalChatDialog(c portalCall) (string, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return "", err
	}
	d := portalParam(c.in, "DIALOG_ID")
	if chat := strings.TrimSpace(portalStr(portalParam(c.in, "CHAT_ID"))); chat != "" && chat != "0" && portalStr(d) == "" {
		d = "chat" + strings.TrimPrefix(strings.ToLower(chat), "chat")
	}
	return s.portalDialog(c, d)
}

// chatFolder is the drive and folder of the chat's files (see above); the person must be in the chat.
func (s *Server) chatFolder(r *http.Request, sess auth.Session, room string) (drive.Drive, string, error) {
	ctx := r.Context()
	if tasks.IsGuestCtx(ctx) {
		return drive.Drive{}, "", i18n.E("err.forbidden")
	}
	if _, err := s.Matrix.ChatMembers(ctx, sess.UserID, room); err != nil {
		return drive.Drive{}, "", err
	}
	var d drive.Drive
	if key := s.Matrix.ChatProjectKey(ctx, room); key != "" {
		if pd, err := s.Drive.Open(ctx, sess.UserID, key); err == nil && pd.CanWrite {
			d = pd
		}
	}
	if d.Kind == "" {
		var err error
		if d, err = s.Drive.Open(ctx, sess.UserID, "my"); err != nil {
			return d, "", err
		}
	}
	folder, err := s.driveFolderNamed(r, sess, d, s.tr(r, "im.drive_folder"))
	return d, folder, err
}

// im.disk.folder.get: CHAT_ID or DIALOG_ID → {ID}, the number of the chat's folder (disk.folder.* take it).
func (s *Server) portalChatFolder(c portalCall) (portalOut, error) {
	room, err := s.portalChatDialog(c)
	if err != nil {
		return portalOut{}, err
	}
	d, folder, err := s.chatFolder(c.r, auth.Session{UserID: c.p.UserID}, room)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalDiskOut(c, d, "folder", folder)
	if err != nil {
		return portalOut{}, err
	}
	n, _ := strconv.ParseInt(portalStr(out["ID"]), 10, 64)
	return portalOut{Result: map[string]any{"ID": n}}, nil
}

// portalFileNums are the numbers of drive files of FILE_ID, UPLOAD_ID or DISK_ID (one or a list).
func portalFileNums(in map[string]any) []string {
	for _, k := range []string{"FILE_ID", "UPLOAD_ID", "DISK_ID"} {
		if v, ok := portalParamOK(in, k); ok {
			return portalList(v)
		}
	}
	return nil
}

// im.disk.file.commit: CHAT_ID or DIALOG_ID, FILE_ID (UPLOAD_ID, DISK_ID) — files of a drive the caller sees, one or
// a list — and MESSAGE, the text sent before them. Each file goes into the chat as its own message of the caller
// → {FILES: {uploadN: …}, DISK_ID, FILE_MODELS, MESSAGE_ID — the first file's message}.
func (s *Server) portalChatFileCommit(c portalCall) (portalOut, error) {
	room, err := s.portalChatDialog(c)
	if err != nil {
		return portalOut{}, err
	}
	if tasks.IsGuestCtx(c.r.Context()) {
		return portalOut{}, i18n.E("err.forbidden")
	}
	nums := portalFileNums(c.in)
	if len(nums) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "FILES_ERROR", "err.file_required")
	}
	var objs []portalDiskObj
	for _, n := range nums {
		o, err := s.portalDiskRef(c, n, "file", false)
		if err != nil {
			return portalOut{}, err
		}
		objs = append(objs, o)
	}
	ctx := c.r.Context()
	sess := auth.Session{UserID: c.p.UserID}
	if m := strings.TrimSpace(botText(portalStr(portalParam(c.in, "MESSAGE")))); m != "" {
		if _, err := s.Matrix.Send(ctx, c.p.UserID, room, m, ""); err != nil {
			return portalOut{}, err
		}
	}
	chat, err := s.portalChatNum(ctx, room)
	if err != nil {
		return portalOut{}, err
	}
	me, _ := s.portalUserNum(c, c.p.UserID)
	var name string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, c.p.UserID).Scan(&name)
	files, models, disk := map[string]any{}, map[string]any{}, []string{}
	var first int64
	for _, o := range objs {
		fm, err := s.fileAccess(c.r, sess, o.ID)
		if err != nil {
			return portalOut{}, err
		}
		msg, err := s.fileToChat(c.r, sess, fm, room, "")
		if err != nil {
			return portalOut{}, err
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO portal_chat_files (room_id, event_id, file_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			room, msg.ID, o.ID); err != nil {
			return portalOut{}, err
		}
		if first == 0 {
			if first, err = s.portalMessageNum(ctx, room, msg.ID); err != nil {
				return portalOut{}, err
			}
		}
		model, err := s.portalDiskOut(c, o.Drive, "file", o.ID)
		if err != nil {
			return portalOut{}, err
		}
		key := "upload" + strconv.FormatInt(o.Num, 10)
		typ := "file"
		if strings.HasPrefix(fm.ContentType, "image/") {
			typ = "image"
		}
		files[key] = map[string]any{"id": o.Num, "chatId": chat, "date": msg.At.Format("2006-01-02T15:04:05-07:00"), "type": typ,
			"name": fm.Name, "extension": strings.TrimPrefix(path.Ext(fm.Name), "."), "size": fm.Size, "image": false,
			"status": "done", "progress": 100, "authorId": me, "authorName": name, "urlPreview": "", "urlShow": model["DETAIL_URL"],
			"urlDownload": model["DOWNLOAD_URL"]}
		models[key] = model
		disk = append(disk, strconv.FormatInt(o.Num, 10))
	}
	return portalOut{Result: map[string]any{"FILES": files, "DISK_ID": disk, "FILE_MODELS": models, "MESSAGE_ID": first}}, nil
}

// im.disk.file.save: FILE_ID, a file of a chat (any drive file the caller sees) → a copy in «Сохранённые файлы» of
// the caller's «Мой диск»: {folder: {id, name}, file: {id, name}}.
func (s *Server) portalChatFileSave(c portalCall) (portalOut, error) {
	o, err := s.portalDiskRef(c, portalParam(c.in, "FILE_ID"), "file", false)
	if err != nil {
		return portalOut{}, err
	}
	sess := auth.Session{UserID: c.p.UserID}
	if tasks.IsGuestCtx(c.r.Context()) {
		return portalOut{}, i18n.E("err.forbidden")
	}
	my, err := s.Drive.Open(c.r.Context(), c.p.UserID, "my")
	if err != nil {
		return portalOut{}, err
	}
	name := s.tr(c.r, "im.drive_folder")
	folder, err := s.driveFolderNamed(c.r, sess, my, name)
	if err != nil {
		return portalOut{}, err
	}
	to := portalDiskObj{Kind: "folder", ID: folder, Drive: my}
	id, err := s.portalDiskCopyTo(c, o, to)
	if err != nil {
		return portalOut{}, err
	}
	fo, err := s.portalDiskOut(c, my, "folder", folder)
	if err != nil {
		return portalOut{}, err
	}
	fi, err := s.portalDiskOut(c, my, "file", id)
	if err != nil {
		return portalOut{}, err
	}
	num := func(v any) int64 { n, _ := strconv.ParseInt(portalStr(v), 10, 64); return n }
	return portalOut{Result: map[string]any{"folder": map[string]any{"id": num(fo["ID"]), "name": fo["NAME"]},
		"file": map[string]any{"id": num(fi["ID"]), "name": fi["NAME"]}}}, nil
}

// im.disk.file.delete: CHAT_ID, FILE_ID → true; the messages im.disk.file.commit sent the file with leave the chat
// (one's own; the chat's owner or an admin — anyone's, as the messenger lets them). The file stays on its drive.
func (s *Server) portalChatFileDelete(c portalCall) (portalOut, error) {
	room, err := s.portalChatDialog(c)
	if err != nil {
		return portalOut{}, err
	}
	o, err := s.portalDiskRef(c, portalParam(c.in, "FILE_ID"), "file", false)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	rows, err := s.DB.Query(ctx, `SELECT event_id FROM portal_chat_files WHERE room_id = $1 AND file_id::text = $2`, room, o.ID)
	if err != nil {
		return portalOut{}, err
	}
	var events []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return portalOut{}, err
		}
		events = append(events, e)
	}
	rows.Close()
	if len(events) == 0 {
		return portalOut{}, i18n.E("err.not_found")
	}
	for _, e := range events {
		if err := s.Matrix.Delete(ctx, c.p.UserID, room, e); err != nil {
			return portalOut{}, err
		}
		if _, err := s.DB.Exec(ctx, `DELETE FROM portal_chat_files WHERE room_id = $1 AND event_id = $2`, room, e); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, nil
}

// portalColors are the chat colours and their shades.
var portalColors = map[string]string{
	"RED": "#df532d", "GREEN": "#64a513", "MINT": "#4ba984", "LIGHT_BLUE": "#4ba5c3", "DARK_BLUE": "#3e99ce",
	"PURPLE": "#8474c8", "AQUA": "#1eb4aa", "PINK": "#f76187", "LIME": "#58cc47", "BROWN": "#ab7761", "AZURE": "#29619b",
	"KHAKI": "#728f7a", "SAND": "#ba9c7b", "MARENGO": "#556574", "GRAY": "#909090", "GRAPHITE": "#738f99",
}

// portalSetColor keeps COLOR of the group chat in the room for those who may change the chat (its name, description, picture).
func (s *Server) portalSetColor(c portalCall, room string) error {
	color := strings.ToUpper(strings.TrimSpace(portalStr(portalParam(c.in, "COLOR"))))
	if portalColors[color] == "" {
		return s.portalErr(c.r, http.StatusBadRequest, "WRONG_COLOR", "err.portal_field", "COLOR")
	}
	ctx := c.r.Context()
	g, err := s.Matrix.GroupInfo(ctx, c.p.UserID, room)
	if err != nil {
		return err
	}
	if g == nil {
		return s.portalErr(c.r, http.StatusBadRequest, "DIALOG_ID_EMPTY", "err.portal_dialog")
	}
	if !g.CanEdit {
		return tasks.ErrForbidden
	}
	if _, err := s.portalChatNum(ctx, room); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE portal_chats SET color = $2 WHERE room_id = $1`, room, color)
	return err
}

// im.chat.updateColor: CHAT_ID (DIALOG_ID), COLOR — RED, GREEN, MINT… → true; im.dialog.get gives it back.
func (s *Server) portalChatColor(c portalCall) (portalOut, error) {
	room, err := s.portalChatDialog(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.portalSetColor(c, room)
	return portalOut{Result: err == nil}, err
}
