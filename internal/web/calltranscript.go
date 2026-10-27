package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"go24/internal/calls"
	"go24/internal/speech"
	"go24/internal/storages"
)

// ---------- The transcript of a call's recording, as CoPilot's: Whisper makes its text ----------

// transcribeOn: saved recordings are turned into text — Whisper is here and the administrators let it.
func (s *Server) transcribeOn(ctx context.Context) bool {
	if s.Speech == nil || s.Recordings == nil {
		return false
	}
	on, err := s.Recordings.Transcribing(ctx)
	if err != nil {
		s.Log.Warn("call transcript setting", "err", err)
	}
	return on
}

// TranscriptsLoop (go24-worker) makes the text of saved recordings one by one: Whisper takes its time, and the
// recordings go on being saved meanwhile.
func (s *Server) TranscriptsLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for s.transcribeNext(ctx) {
			}
		}
	}
}

// transcribeNext makes the text of the oldest waiting recording: a file beside it in «Записи звонков» (among the
// task's files for a call of a task), told to the call. false — nothing more to do now.
func (s *Server) transcribeNext(ctx context.Context) bool {
	rec, fileID, tries, found, err := s.Recordings.NextTranscript(ctx)
	if err != nil {
		s.Log.Warn("call transcripts", "err", err)
		return false
	}
	if !found {
		return false
	}
	lang := s.portalLang()
	name, id, err := s.transcribeRecording(ctx, rec, fileID)
	if err != nil {
		s.Log.Error("call transcript", "recording", rec.ID, "try", tries, "err", err)
		if tries < calls.TranscriptTries {
			return false // Whisper may be back in a minute
		}
		if err := s.Recordings.Transcribed(ctx, rec.ID, ""); err != nil {
			s.Log.Warn("call transcript", "err", err)
		}
		s.tellCall(ctx, rec, s.I18n.T(lang, "call.transcript_failed", rec.Title), "", nil)
		return true
	}
	if err := s.Recordings.Transcribed(ctx, rec.ID, id); err != nil {
		s.Log.Warn("call transcript", "err", err)
		return false
	}
	s.Log.Info("call transcript saved", "recording", rec.ID, "file", id)
	text := s.I18n.T(lang, "call.transcript_saved", name, s.BaseURL+"/files/"+id)
	if f, size, err := s.storedFile(ctx, id); err == nil {
		s.tellCall(ctx, rec, text, name, &chatFile{f: f, size: size, ctype: "text/plain"})
		f.Close()
		_ = os.Remove(f.Name())
	} else {
		s.tellCall(ctx, rec, text, name, nil)
	}
	return true
}

// transcribeRecording has Whisper make the text of the recording on the Drive and puts it beside it: the name and id of
// the new file.
func (s *Server) transcribeRecording(ctx context.Context, rec calls.Recording, fileID string) (string, string, error) {
	m, err := s.Storages.FileMeta(ctx, fileID)
	if err != nil {
		return "", "", err
	}
	obj, err := s.Storages.Open(ctx, m.StorageID, m.Key)
	if err != nil {
		return "", "", err
	}
	defer obj.Close()
	sound, soundName, done, err := soundOf(ctx, obj, m.Name, rec.Audio)
	if err != nil {
		return "", "", err
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Hour) // a long meeting on Whisper's CPU
	defer cancel()
	lang := s.portalLang()
	code, _, _ := strings.Cut(lang, "-")
	long := &speech.Client{Base: s.Speech.Base, HTTP: &http.Client{}}
	segs, err := long.Segments(ctx, soundName, sound, code)
	if err != nil {
		return "", "", err
	}
	text := transcriptText(s.I18n.T(lang, "call.transcript_head", rec.Title, rec.Started.Local().Format(s.I18n.T(lang, "format.datetime"))),
		segs, s.I18n.T(lang, "im.voice_silent"))

	d, err := s.Drive.Open(ctx, rec.StartedBy, "my")
	if err != nil {
		return "", "", err
	}
	if err := s.Drive.Fits(ctx, d, int64(len(text))); err != nil {
		return "", "", err
	}
	folder, err := s.recordFolder(ctx, rec.StartedBy, d, s.I18n.T(lang, "call.record_folder"))
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSuffix(m.Name, path.Ext(m.Name)) + " — " + s.I18n.T(lang, "call.transcript_file") + ".txt"
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), "transcript.txt")
	if err := s.Storages.Put(ctx, rec.StartedBy, false, "", key, strings.NewReader(text), int64(len(text)), "text/plain; charset=utf-8"); err != nil {
		return "", "", err
	}
	nf := storages.NewFile{Key: key, Name: name, ContentType: "text/plain; charset=utf-8", Size: int64(len(text)),
		UploadedBy: rec.StartedBy, Drive: d.Kind, DriveOwner: d.OwnerID, FolderID: d.At(folder), TaskID: deref(m.TaskID, "")}
	id, err := s.Storages.AddFile(ctx, nf)
	return name, id, err
}

// storedFile copies a stored file into a temporary one (the messenger is sent a file); the caller removes it.
func (s *Server) storedFile(ctx context.Context, id string) (*os.File, int64, error) {
	m, err := s.Storages.FileMeta(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	obj, err := s.Storages.Open(ctx, m.StorageID, m.Key)
	if err != nil {
		return nil, 0, err
	}
	defer obj.Close()
	f, err := os.CreateTemp("", "go24-transcript-*.txt")
	if err != nil {
		return nil, 0, err
	}
	n, err := io.Copy(f, obj)
	if err == nil {
		_, err = f.Seek(0, 0)
	}
	if err != nil {
		f.Close()
		_ = os.Remove(f.Name())
		return nil, 0, err
	}
	return f, n, nil
}

// ffmpegBin is the ffmpeg go24-worker runs (a test has none).
var ffmpegBin = "ffmpeg"

// soundOf is what Whisper is sent: a sound recording as it is; of a video one ffmpeg takes the sound alone (16 kHz
// mono Opus — about 10 MB an hour instead of a gigabyte of video, which Whisper would hold in memory). Without
// ffmpeg the video goes as it is. done removes the temporary files.
func soundOf(ctx context.Context, obj io.Reader, name string, audio bool) (io.Reader, string, func(), error) {
	ffmpeg, err := exec.LookPath(ffmpegBin)
	if audio || err != nil {
		return obj, name, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "go24-transcript-")
	if err != nil {
		return nil, "", nil, err
	}
	done := func() { _ = os.RemoveAll(dir) }
	video := filepath.Join(dir, "call"+path.Ext(name)) // MP4 keeps its index at the end: ffmpeg reads a file, not a pipe
	f, err := os.Create(video)
	if err == nil {
		_, err = io.Copy(f, obj)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		done()
		return nil, "", nil, err
	}
	sound := filepath.Join(dir, "call.ogg")
	out, err := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-loglevel", "error", "-i", video, "-vn", "-ac", "1", "-ar", "16000",
		"-c:a", "libopus", "-b:a", "24k", sound).CombinedOutput()
	if err != nil {
		done()
		return nil, "", nil, fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_ = os.Remove(video)
	s, err := os.Open(sound)
	if err != nil {
		done()
		return nil, "", nil, err
	}
	return s, "call.ogg", func() { s.Close(); done() }, nil
}

// transcriptText is the text of the file: the heading, then a line for each piece of speech with its time from the
// start of the recording ([01:05], [1:02:03]); silent — when nothing was heard.
func transcriptText(head string, segs []speech.Segment, silent string) string {
	var b strings.Builder
	b.WriteString(head + "\n\n")
	if len(segs) == 0 {
		b.WriteString(silent + "\n")
	}
	for _, sg := range segs {
		b.WriteString("[" + offset(sg.Start) + "] " + sg.Text + "\n")
	}
	return b.String()
}

// offset is a time from the start of a recording: 01:05, or 1:02:03 past an hour.
func offset(sec float64) string {
	n := int(sec)
	if n >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", n/3600, n/60%60, n%60)
	}
	return fmt.Sprintf("%02d:%02d", n/60, n%60)
}
