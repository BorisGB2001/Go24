package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// maxBoard is the largest scene a board keeps (pictures on it included).
const maxBoard = 50 << 20

// boardBlank is the scene of a new board.
var boardBlank = []byte(`{"type":"excalidraw","version":2,"source":"go24","elements":[],"appState":{},"files":{}}`)

// boardPage is «Доски»: the board in the Excalidraw editor, read only for one who may not change it; ?scene — the
// board's scene for the editor, ?live — what happens on the board while it is open.
func (s *Server) boardPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.URL.Query().Has("scene") {
		s.boardScene(w, r, sess)
		return
	}
	if r.URL.Query().Has("live") {
		s.boardLive(w, r, sess)
		return
	}
	d, m, edit, back, ok := s.boardOf(w, r, sess)
	if !ok {
		return
	}
	id := r.PathValue("file")
	s.render(w, r, "diskboard", page{Title: m.Name, Nav: "disk", Data: map[string]any{"Name": m.Name, "Back": back, "Edit": edit,
		"Scene": d.Path() + "/board/" + id + "?scene", "Download": "/files/" + id, "About": "/files/" + id + "/about",
		"Lang": s.lang(r)}})
}

// boardScene gives the editor the board's scene and the stamp of its state (X-Board-Stamp).
func (s *Server) boardScene(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, m, _, _, ok := s.boardOf(w, r, sess)
	if !ok {
		return
	}
	stamp := drive.BoardStamp(m.Key)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Board-Stamp", stamp)
	if r.Header.Get("If-None-Match") == `"`+stamp+`"` { // the editor asks whether others saved it meanwhile
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data, err := s.readFile(r, m, maxBoard)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if len(data) == 0 {
		data = boardBlank
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", `"`+stamp+`"`)
	w.Write(data)
}

// boardSave stores the scene the editor sends as the board's state: the body is the scene, ?stamp= the state it
// was made from. A board saved by someone else meanwhile — 409, and the editor merges the newer state first.
func (s *Server) boardSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, m, edit, _, ok := s.boardOf(w, r, sess)
	if !ok {
		return
	}
	if !edit {
		s.jsonErr(w, r, drive.ErrForbidden)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBoard+1))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if len(data) > maxBoard {
		s.jsonErr(w, r, i18n.E("err.board_big"))
		return
	}
	var scene struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &scene) != nil || scene.Type != "excalidraw" {
		s.jsonErr(w, r, i18n.E("err.board_bad"))
		return
	}
	id, stamp := r.PathValue("file"), r.URL.Query().Get("stamp")
	if drive.BoardStamp(m.Key) != stamp { // the store checks it again; this spares storing bytes that will not be used
		writeJSON(w, http.StatusConflict, map[string]string{"error": s.tr(r, "err.board_changed"), "code": "err.board_changed"})
		return
	}
	if err := s.Drive.Fits(r.Context(), d, int64(len(data))); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), m.Name)
	storageID := ""
	if m.StorageID != nil {
		storageID = *m.StorageID
	}
	if err := s.Storages.Put(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), storageID, key, bytes.NewReader(data), int64(len(data)), drive.BoardType); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	err = s.Drive.SaveBoard(r.Context(), sess.UserID, d, id, stamp, drive.NewVersion{Key: key, ContentType: drive.BoardType, StorageID: storageID, Size: int64(len(data))})
	if errors.Is(err, drive.ErrBoardChanged) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": s.tr(r, "err.board_changed"), "code": "err.board_changed"})
		return
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	s.boardNotify(r.Context(), id, map[string]any{"t": "saved", "s": drive.BoardStamp(key), "u": sess.UserID, "c": r.URL.Query().Get("c")})
	writeJSON(w, http.StatusOK, map[string]string{"stamp": drive.BoardStamp(key)})
}

// boardNotify tells the open editors of the board what happened (go24_notify, key "b"+the file's id): "join",
// "here" and "leave" of an editor (u — the user, n — the name, c — the editor's connection), "saved" (s — the
// stamp of the new state), "ptr" (x, y — the pointer on the board, b — the button).
func (s *Server) boardNotify(ctx context.Context, id string, ev map[string]any) {
	data, _ := json.Marshal(ev)
	if _, err := s.DB.Exec(ctx, `SELECT pg_notify('go24_notify', 'b' || $1 || ':' || $2)`, id, string(data)); err != nil {
		s.Log.Error("board notify", "err", err)
	}
}

// boardLive streams what happens on the board to an open editor (Server-Sent Events): first "hello" with the
// editor's connection, then "board" events of the others (see boardNotify). The editor says it is here when it
// comes, every 25 seconds and when another comes; "leave" when it goes.
func (s *Server) boardLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, _, _, _, ok := s.boardOf(w, r, sess)
	if !ok {
		return
	}
	flusher, canFlush := w.(http.Flusher)
	if s.Live == nil || !canFlush {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("file")
	events, stop := s.Live.Subscribe("b" + id)
	defer stop()
	name := sess.Name
	if name == "" {
		_ = s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id::text = $1`, sess.UserID).Scan(&name)
	}
	conn := randomHex(8)
	here := func(t string) {
		s.boardNotify(context.WithoutCancel(r.Context()), id, map[string]any{"t": t, "u": sess.UserID, "n": name, "c": conn})
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	hello, _ := json.Marshal(map[string]string{"c": conn, "u": sess.UserID})
	fmt.Fprintf(w, "retry: 3000\n\nevent: hello\ndata: %s\n\n", hello)
	flusher.Flush()
	here("join")
	defer here("leave")
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := s.fileAccess(r, sess, id); err != nil { // the board was closed to the user meanwhile
				return
			}
			here("here")
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case raw := <-events:
			var ev struct{ T, C string }
			if json.Unmarshal([]byte(raw), &ev) != nil || ev.C == conn {
				continue
			}
			if ev.T == "join" {
				here("here")
			}
			fmt.Fprintf(w, "event: board\ndata: %s\n\n", raw)
			flusher.Flush()
		}
	}
}

// boardPointer passes where the editor's pointer is on the board to the others: x, y, b (down or up), c — the
// editor's connection.
func (s *Server) boardPointer(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, _, _, _, ok := s.boardOf(w, r, sess)
	if !ok {
		return
	}
	x, errX := strconv.ParseFloat(r.FormValue("x"), 64)
	y, errY := strconv.ParseFloat(r.FormValue("y"), 64)
	if errX != nil || errY != nil || math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
		s.jsonErr(w, r, i18n.E("err.board_bad"))
		return
	}
	b := "up"
	if r.FormValue("b") == "down" {
		b = "down"
	}
	name := sess.Name
	if name == "" {
		_ = s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id::text = $1`, sess.UserID).Scan(&name)
	}
	s.boardNotify(r.Context(), r.PathValue("file"), map[string]any{"t": "ptr", "u": sess.UserID, "n": name,
		"c": strings.TrimSpace(r.FormValue("c")), "x": x, "y": y, "b": b})
	w.WriteHeader(http.StatusNoContent)
}

// boardOf is the board of the request (its drive, the file, whether the user changes it, where «Назад» leads);
// false — the answer is written.
func (s *Server) boardOf(w http.ResponseWriter, r *http.Request, sess auth.Session) (drive.Drive, storages.FileMeta, bool, string, bool) {
	page := r.Method == http.MethodGet && !r.URL.Query().Has("scene") && !r.URL.Query().Has("live")
	fail := func(err error, back string) {
		if page {
			s.taskErr(w, r, err, back)
		} else {
			s.jsonErr(w, r, err)
		}
	}
	d, err := s.openDrive(r, sess)
	if err != nil {
		fail(err, "/disk/my")
		return d, storages.FileMeta{}, false, "", false
	}
	id := r.PathValue("file")
	back := boardsBack(r, diskBack(d, r.URL.Query().Get("folder")))
	m, err := s.fileAccess(r, sess, id)
	if err == nil && !drive.IsBoard(m.Name) {
		err = drive.ErrNotFound
	}
	if err != nil {
		fail(err, back)
		return d, m, false, back, false
	}
	may, err := s.Drive.MayChange(r.Context(), sess.UserID, d, id)
	if err != nil {
		fail(err, back)
		return d, m, false, back, false
	}
	return d, m, may, back, true
}

// boardNew is «Создать → Доска»: an empty board in the folder or one by a template, opened in the editor.
func (s *Server) boardNew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.openDrive(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	folder := r.FormValue("folder")
	back := boardsBack(r, diskBack(d, folder))
	name := strings.TrimSpace(strings.NewReplacer("/", " ", "\\", " ").Replace(r.FormValue("name")))
	if name == "" {
		name = s.tr(r, "dboard.new")
	}
	if !drive.IsBoard(name) {
		name += drive.BoardExt
	}
	if err := s.Drive.CheckFolder(r.Context(), d, folder); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if same, _ := s.Drive.Same(r.Context(), sess.UserID, d, folder, name); same != "" {
		s.taskErr(w, r, i18n.E("office.exists"), back)
		return
	}
	scene := boardBlank
	if tpl := s.boardTemplateScene(r, r.FormValue("template")); tpl != nil {
		scene = tpl
	}
	id, err := s.diskStore(r, sess, d, folder, "", name, drive.BoardType, bytes.NewReader(scene), int64(len(scene)))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	to := d.Path() + "/board/" + id + "?folder=" + url.QueryEscape(folder)
	if back == "/boards" {
		to += "&from=boards"
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// boardsBack: «Доски» is where the editor and «Создать» go back to, when they were opened from it (?from=boards).
func boardsBack(r *http.Request, back string) string {
	if r.FormValue("from") == "boards" {
		return "/boards"
	}
	return back
}

// boardsPage is «Доски»: the boards of every drive the user works with; ?scope= (mine | shared), ?q= — a part of
// the name.
func (s *Server) boardsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	qs := r.URL.Query()
	f := drive.DocFilter{Scope: qs.Get("scope"), Q: strings.TrimSpace(qs.Get("q"))}
	if f.Scope != "mine" && f.Scope != "shared" {
		f.Scope = ""
	}
	list, err := s.Drive.Documents(r.Context(), sess.UserID, []string{strings.TrimPrefix(drive.BoardExt, ".")}, f, docsMax)
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	s.render(w, r, "boards", page{TitleKey: "dboard.title", Nav: "boards", Data: map[string]any{
		"Boards": list, "Scope": f.Scope, "Q": f.Q, "Scopes": []string{"", "mine", "shared"}}})
}
