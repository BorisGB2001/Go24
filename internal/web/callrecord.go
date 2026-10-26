package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- Recording of calls: «Начать запись» in the call, the file on the Drive ----------

// maxChatFile is the largest file the messenger takes (Synapse's max_upload_size): a longer recording is only linked.
const maxChatFile = 100 << 20

// recordOn: calls may be recorded here (go24 reaches LiveKit's API and Egress's folder) and the administrators let it.
func (s *Server) recordOn(ctx context.Context) bool {
	if s.Recordings == nil || s.Calls == nil || s.Calls.APIURL == "" || s.RecordDir == "" {
		return false
	}
	on, err := s.Recordings.Enabled(ctx)
	if err != nil {
		s.Log.Warn("call recording setting", "err", err)
	}
	return on
}

// callRecord (the people of the call, ?task=|event=|room=): whether the call may be recorded and is; POST
// action=start starts the recording, action=stop stops it.
func (s *Server) callRecord(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if !s.recordOn(ctx) || tasks.IsGuestCtx(ctx) { // a guest of a collab records nothing
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]bool{"enabled": false, "on": false})
			return
		}
		s.jsonErr(w, r, i18n.E("call.record_off"))
		return
	}
	room, title, _, _, err := s.callRoom(r, sess)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	cur, on, err := s.Recordings.Current(ctx, room)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if r.Method == http.MethodPost {
		switch r.FormValue("action") {
		case "start":
			if on {
				break // someone else started it a moment ago
			}
			audio := r.URL.Query().Get("video") == "0"
			e, err := s.Calls.StartRecording(ctx, room, s.RecordDir, audio) // Egress has the folder at the same path
			if errors.Is(err, calls.ErrNoRoom) {
				s.jsonErr(w, r, i18n.E("call.record_no_room"))
				return
			}
			if err != nil {
				s.Log.Error("call recording start", "room", room, "err", err)
				s.jsonErr(w, r, i18n.E("call.record_failed_start"))
				return
			}
			_, err = s.Recordings.Add(ctx, calls.Recording{Room: room, Title: title, Query: callGuestQuery(r), EgressID: e.ID,
				Audio: audio, StartedBy: sess.UserID})
			if err != nil {
				_ = s.Calls.StopRecording(ctx, e.ID)
				if !errors.Is(err, calls.ErrRecording) {
					s.jsonErr(w, r, err)
					return
				}
			}
			s.Log.Info("call recording started", "user_id", sess.UserID, "room", room, "egress", e.ID)
			on = true
		case "stop":
			if !on {
				break
			}
			if err := s.Calls.StopRecording(ctx, cur.EgressID); err != nil {
				s.Log.Warn("call recording stop", "room", room, "err", err) // the worker sees what Egress did
			}
			if _, err := s.Recordings.Stop(ctx, cur.ID, sess.UserID); err != nil {
				s.jsonErr(w, r, err)
				return
			}
			s.Log.Info("call recording stopped", "user_id", sess.UserID, "room", room)
			on = false
		default:
			s.jsonErr(w, r, i18n.E("err.bad_form"))
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": true, "on": on})
}

// callSettings: «Звонки» of the settings (administrators) — whether calls may be recorded, and the external
// services of calls (callServiceSave keeps them).
func (s *Server) callSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Recordings == nil && s.External == nil {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		if s.Recordings == nil {
			s.taskErr(w, r, i18n.E("err.calls_off"), "/settings/calls")
			return
		}
		if err := s.Recordings.SetEnabled(ctx, sess.UserID, r.FormValue("record") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/calls")
			return
		}
		http.Redirect(w, r, "/settings/calls?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	data := map[string]any{"Recording": s.Recordings != nil, "Ready": s.Calls != nil && s.Calls.APIURL != "" && s.RecordDir != ""}
	if s.Recordings != nil {
		on, err := s.Recordings.Enabled(ctx)
		if err != nil {
			s.fail(w, "call settings", err)
			return
		}
		data["Record"] = on
	}
	if s.External != nil {
		services, err := s.External.Configs(ctx)
		if err != nil {
			s.fail(w, "call services", err)
			return
		}
		data["Services"] = services
	}
	s.render(w, r, "callsettings", page{TitleKey: "call.settings", Nav: "callsettings", Data: data})
}

// RecordingsLoop (go24-worker) follows the recordings of calls: one whose call is over stops, a finished one goes to
// the Drive of the one who started it and to the call's chat or task.
func (s *Server) RecordingsLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.followRecordings(ctx)
		}
	}
}

func (s *Server) followRecordings(ctx context.Context) {
	going, err := s.Recordings.Going(ctx)
	if err != nil {
		s.Log.Warn("call recordings", "err", err)
		return
	}
	for _, rec := range going {
		e, found, err := s.Calls.Recording(ctx, rec.EgressID)
		if err != nil {
			s.Log.Warn("call recording", "egress", rec.EgressID, "err", err)
			continue
		}
		switch {
		case !found:
			if time.Since(rec.Started) > 10*time.Minute { // Egress was restarted and forgot it
				s.recordingFailed(ctx, rec, "egress lost the recording")
			}
		case !e.Over():
			if rec.Stopped == nil && time.Since(rec.Started) > 40*time.Second {
				if in, err := s.Calls.Participants(ctx, rec.Room); err == nil && len(in) == 0 { // everyone left: the recording ends too
					if err := s.Calls.StopRecording(ctx, rec.EgressID); err != nil {
						s.Log.Warn("call recording stop", "room", rec.Room, "err", err)
					}
					_, _ = s.Recordings.Stop(ctx, rec.ID, "")
				}
			}
		case !e.Done():
			s.recordingFailed(ctx, rec, e.Status+" "+e.Error)
		default:
			if err := s.saveRecording(ctx, rec, e); err != nil {
				s.Log.Error("call recording save", "egress", rec.EgressID, "err", err)
				s.recordingFailed(ctx, rec, err.Error())
			}
		}
	}
}

// recordingFailed ends the recording without a file and tells the call so.
func (s *Server) recordingFailed(ctx context.Context, rec calls.Recording, why string) {
	s.Log.Warn("call recording failed", "egress", rec.EgressID, "room", rec.Room, "why", why)
	if err := s.Recordings.Finish(ctx, rec.ID, "", why); err != nil {
		s.Log.Warn("call recording", "err", err)
		return
	}
	s.dropManifest(rec)
	lang := s.I18n.Match("")
	s.tellCall(ctx, rec, s.I18n.T(lang, "call.record_failed"), "", nil)
}

// saveRecording puts the file Egress wrote on the Drive of the one who started the recording («Записи звонков»),
// with the call's task when the call is of a task, and tells the call.
func (s *Server) saveRecording(ctx context.Context, rec calls.Recording, e calls.Egress) error {
	local := filepath.Join(s.RecordDir, filepath.Base(e.File))
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	lang := s.I18n.Match("")
	d, err := s.Drive.Open(ctx, rec.StartedBy, "my")
	if err != nil {
		return err
	}
	if err := s.Drive.Fits(ctx, d, st.Size()); err != nil {
		return errors.New(s.I18n.T(lang, "call.record_no_space"))
	}
	folder, err := s.recordFolder(ctx, rec.StartedBy, d, s.I18n.T(lang, "call.record_folder"))
	if err != nil {
		return err
	}
	ext, ctype := ".mp4", "video/mp4"
	if rec.Audio {
		ext, ctype = ".ogg", "audio/ogg"
	}
	name := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(strings.TrimSpace(rec.Title)) + " " +
		rec.Started.Local().Format("2006-01-02 15-04") + ext
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), "recording"+ext)
	if err := s.Storages.Put(ctx, rec.StartedBy, false, "", key, f, st.Size(), ctype); err != nil {
		return err
	}
	q, _ := url.ParseQuery(rec.Query)
	nf := storages.NewFile{Key: key, Name: name, ContentType: ctype, Size: st.Size(), UploadedBy: rec.StartedBy,
		Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder)}
	var taskID string
	if k := q.Get("task"); k != "" {
		if taskID, err = s.Tasks.TaskIDByKey(ctx, k); err == nil {
			nf.TaskID = taskID // the people of the task see it among its files
		}
	}
	id, err := s.Storages.AddFile(ctx, nf)
	if err != nil {
		return err
	}
	if err := s.Recordings.Finish(ctx, rec.ID, id, ""); err != nil {
		return err
	}
	s.Log.Info("call recording saved", "egress", rec.EgressID, "file", id, "size", st.Size())
	if _, err := f.Seek(0, 0); err == nil {
		s.tellCall(ctx, rec, s.I18n.T(lang, "call.record_saved", name, s.BaseURL+"/files/"+id), name, &chatFile{f: f, size: st.Size(), ctype: ctype})
	}
	_ = f.Close()
	if err := os.Remove(local); err != nil {
		s.Log.Warn("call recording remove", "file", local, "err", err)
	}
	s.dropManifest(rec)
	return nil
}

// dropManifest removes what Egress tells of the recording beside its file (EG_….json): go24 keeps that itself.
func (s *Server) dropManifest(rec calls.Recording) {
	if err := os.Remove(filepath.Join(s.RecordDir, filepath.Base(rec.EgressID)+".json")); err != nil && !os.IsNotExist(err) {
		s.Log.Warn("call recording remove", "egress", rec.EgressID, "err", err)
	}
}

type chatFile struct {
	f     *os.File
	size  int64
	ctype string
}

// recordFolder is «Записи звонков» at the top of the person's own drive, made when missing.
func (s *Server) recordFolder(ctx context.Context, userID string, d drive.Drive, name string) (string, error) {
	folders, _, _, err := s.Drive.List(ctx, d, "")
	if err != nil {
		return "", err
	}
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) {
			return f.ID, nil
		}
	}
	return s.Drive.CreateFolder(ctx, userID, d, "", name)
}

// tellCall writes into the call's chat (with the file itself when the messenger takes it) or task, as the one who
// started the recording; a call of a meeting has no chat.
func (s *Server) tellCall(ctx context.Context, rec calls.Recording, text, name string, file *chatFile) {
	q, _ := url.ParseQuery(rec.Query)
	var err error
	switch {
	case q.Get("task") != "":
		var id string
		if id, err = s.Tasks.TaskIDByKey(ctx, q.Get("task")); err == nil {
			if s.Matrix != nil {
				_, err = s.Matrix.PostTaskComment(ctx, rec.StartedBy, id, text)
			} else {
				_, err = s.Tasks.PostComment(ctx, rec.StartedBy, id, text)
			}
		}
	case q.Get("room") != "" && s.Matrix != nil:
		if file != nil && file.size <= maxChatFile {
			_, err = s.Matrix.SendFile(ctx, rec.StartedBy, q.Get("room"), "", name, file.ctype, file.f, file.size, nil)
		} else {
			_, err = s.Matrix.Send(ctx, rec.StartedBy, q.Get("room"), text, "")
		}
	}
	if err != nil {
		s.Log.Warn("call recording message", "room", rec.Room, "err", err)
	}
}
