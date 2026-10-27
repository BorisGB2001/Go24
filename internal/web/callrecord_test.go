package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/speech"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCallRecording: while the administrators do not let calls be recorded, a call has no «Начать запись»; let,
// one of a task's call starts the recording (someone outside the task cannot), Egress records the task's room into
// the shared folder, a second start records nothing more; «Остановить» stops Egress. When Egress has written the
// file, go24-worker puts it on the Drive of the one who started it, in «Записи звонков», among the task's files, and
// says so in the task; the file in the shared folder goes. A recording whose call everyone left stops by itself, and
// one Egress failed is told as failed. With «Расшифровывать записи» on, go24-worker has Whisper make the text of
// the saved recording: a file with the times of speech beside it, among the task's files, told to the task; Whisper
// failing is tried again, the third failure is told.
func TestCallRecording(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, boss, outsider := newUser("Анна"), newUser("Админ"), newUser("Посторонний")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "CR", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "CR")
	must(err)
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Созвон: итоги", ResponsibleID: anna})
	must(err)
	taskID, err := ts.TaskIDByKey(ctx, key)
	must(err)

	// LiveKit: Egress records into dir, says what state the recording is in; the room has whoever is in it
	dir := t.TempDir()
	var mu sync.Mutex
	var started []map[string]any
	var stopped []string
	egress := map[string]string{} // egress id → status
	var people []string
	lk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case strings.HasSuffix(r.URL.Path, "/StartRoomCompositeEgress"):
			started = append(started, in)
			id := "EG_" + string(rune('A'+len(started)-1))
			egress[id] = "EGRESS_STARTING"
			_ = json.NewEncoder(w).Encode(map[string]any{"egress_id": id, "room_name": in["room_name"], "status": 0})
		case strings.HasSuffix(r.URL.Path, "/StopEgress"):
			stopped = append(stopped, in["egress_id"].(string))
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/ListEgress"):
			id := in["egress_id"].(string)
			info := map[string]any{"egress_id": id, "status": egress[id]}
			if egress[id] == "EGRESS_COMPLETE" {
				info["file_results"] = []map[string]any{{"filename": dir + "/task-" + key + "-1.mp4", "size": "11", "duration": "61000000000"}}
			}
			if egress[id] == "EGRESS_FAILED" {
				info["error"] = "pipeline"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{info}})
		case strings.HasSuffix(r.URL.Path, "/ListParticipants"):
			list := []map[string]any{{"identity": "EG_A", "kind": "EGRESS"}}
			for _, id := range people {
				list = append(list, map[string]any{"identity": id, "kind": "STANDARD"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"participants": list})
		default:
			w.WriteHeader(404)
		}
	}))
	defer lk.Close()
	// Whisper: answers with pieces of speech, or fails while whisperDown
	whisperDown := false
	var heard string
	whisper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, h, err := r.FormFile("audio_file")
		if whisperDown || err != nil || r.URL.Query().Get("output") != "json" {
			w.WriteHeader(500)
			return
		}
		b, _ := io.ReadAll(f)
		heard = h.Filename + ":" + string(b)
		_, _ = io.WriteString(w, `{"segments":[{"start":1.2,"end":4,"text":" Добрый день."},{"start":3725,"end":3730,"text":" Договорились."}]}`)
	}))
	defer whisper.Close()
	ffmpegBin = "go24-no-ffmpeg"
	prov := calls.New("wss://lk", "key", "secret")
	prov.APIURL = lk.URL
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Calls: prov, Recordings: &calls.Recordings{DB: pool}, RecordDir: dir, BaseURL: "http://go24", Speech: speech.New(whisper.URL),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Language", "ru")
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who, Admin: who == boss}))
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	q := "?task=" + key + "&video=1"
	state := func() string {
		t.Helper()
		_, body := do(anna, "GET", "/call/record"+q, nil)
		var b bytes.Buffer
		_ = json.Compact(&b, []byte(body))
		return b.String()
	}
	start := url.Values{"action": {"start"}}

	if s := state(); s != `{"enabled":false,"on":false}` {
		t.Fatalf("recording not allowed yet: %s", s)
	}
	if st, _ := do(anna, "POST", "/call/record"+q, start); st == http.StatusOK || len(started) != 0 {
		t.Fatalf("a call was recorded while not allowed: %d", st)
	}
	if st, _ := do(anna, "POST", "/settings/calls", url.Values{"record": {"1"}}); st != http.StatusForbidden {
		t.Fatalf("not an administrator let calls be recorded: %d", st)
	}
	if st, _ := do(boss, "POST", "/settings/calls", url.Values{"record": {"1"}, "transcribe": {"1"}}); st != http.StatusSeeOther {
		t.Fatalf("the administrator's setting: %d", st)
	}
	if _, page := do(boss, "GET", "/settings/calls", nil); !strings.Contains(page, `name="record" value="1" checked`) || !strings.Contains(page, `name="transcribe" value="1" checked`) {
		t.Fatalf("the setting page: %s", page)
	}
	if s := state(); s != `{"enabled":true,"on":false}` {
		t.Fatalf("allowed: %s", s)
	}
	if st, _ := do(outsider, "POST", "/call/record"+q, start); st == http.StatusOK || len(started) != 0 {
		t.Fatalf("someone outside the task recorded its call: %d", st)
	}
	if st, body := do(anna, "POST", "/call/record"+q, start); st != http.StatusOK || !strings.Contains(body, `"on": true`) {
		t.Fatalf("start: %d %s", st, body)
	}
	do(anna, "POST", "/call/record"+q, start)
	if len(started) != 1 || started[0]["room_name"] != "task-"+key || started[0]["audio_only"] != false {
		t.Fatalf("Egress was asked %v", started)
	}
	if out := started[0]["file_outputs"].([]any)[0].(map[string]any); !strings.HasPrefix(out["filepath"].(string), dir+"/task-"+key) || out["file_type"] != "MP4" {
		t.Fatalf("the file Egress writes: %v", out)
	}
	if s := state(); s != `{"enabled":true,"on":true}` {
		t.Fatalf("recording: %s", s)
	}
	if st, body := do(anna, "POST", "/call/record"+q, url.Values{"action": {"stop"}}); st != http.StatusOK || !strings.Contains(body, `"on": false`) || len(stopped) != 1 || stopped[0] != "EG_A" {
		t.Fatalf("stop: %d %s %v", st, body, stopped)
	}

	// Egress is still writing the file: nothing happens; then it is written
	srv.followRecordings(ctx)
	must(os.WriteFile(filepath.Join(dir, "task-"+key+"-1.mp4"), []byte("mp4 bytes!!"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "EG_A.json"), []byte("{}"), 0o644))
	mu.Lock()
	egress["EG_A"] = "EGRESS_COMPLETE"
	mu.Unlock()
	srv.followRecordings(ctx)
	var name, folder, fileTask, by string
	var size int64
	must(pool.QueryRow(ctx, `SELECT f.name, f.size, fo.name, f.task_id::text, f.uploaded_by::text FROM call_recordings c
		JOIN files f ON f.id = c.file_id JOIN drive_folders fo ON fo.id = f.folder_id WHERE c.egress_id = 'EG_A' AND c.state = 'done'`).
		Scan(&name, &size, &folder, &fileTask, &by))
	if !strings.HasPrefix(name, "CR-1 Созвон- итоги ") || !strings.HasSuffix(name, ".mp4") || size != 11 || folder != "Записи звонков" || fileTask != taskID || by != anna {
		t.Fatalf("the recording on the Drive: %q %d %q %s %s", name, size, folder, fileTask, by)
	}
	if _, err := os.Stat(filepath.Join(dir, "task-"+key+"-1.mp4")); !os.IsNotExist(err) {
		t.Fatalf("the file stays in the shared folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "EG_A.json")); !os.IsNotExist(err) {
		t.Fatalf("Egress's manifest stays in the shared folder: %v", err)
	}
	var comment string
	must(pool.QueryRow(ctx, `SELECT body FROM task_comments WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&comment))
	if !strings.Contains(comment, "Запись звонка: "+name) || !strings.Contains(comment, "http://go24/files/") {
		t.Fatalf("the task is told %q", comment)
	}

	// the text of the recording: Whisper hears it, the file goes beside it, the task is told
	var transcript string
	must(pool.QueryRow(ctx, `SELECT transcript FROM call_recordings WHERE egress_id = 'EG_A'`).Scan(&transcript))
	if transcript != "pending" {
		t.Fatalf("the recording waits for its text: %q", transcript)
	}
	if !srv.transcribeNext(ctx) || srv.transcribeNext(ctx) {
		t.Fatal("one recording is to be transcribed")
	}
	if heard != name+":mp4 bytes!!" {
		t.Fatalf("Whisper heard %q", heard)
	}
	var tname, tfolder, ttask, tkey string
	must(pool.QueryRow(ctx, `SELECT f.name, fo.name, f.task_id::text, f.object_key FROM call_recordings c
		JOIN files f ON f.id = c.transcript_file JOIN drive_folders fo ON fo.id = f.folder_id WHERE c.egress_id = 'EG_A' AND c.transcript = 'done'`).
		Scan(&tname, &tfolder, &ttask, &tkey))
	if tname != strings.TrimSuffix(name, ".mp4")+" — расшифровка.txt" || tfolder != "Записи звонков" || ttask != taskID {
		t.Fatalf("the transcript on the Drive: %q %q %s", tname, tfolder, ttask)
	}
	obj, err := srv.Storages.Open(ctx, nil, tkey)
	must(err)
	body, _ := io.ReadAll(obj)
	obj.Close()
	if text := string(body); !strings.HasPrefix(text, "Расшифровка записи звонка «CR-1 Созвон: итоги», ") ||
		!strings.Contains(text, "\n\n[00:01] Добрый день.\n[1:02:05] Договорились.\n") {
		t.Fatalf("the transcript: %q", text)
	}
	must(pool.QueryRow(ctx, `SELECT body FROM task_comments WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&comment))
	if !strings.Contains(comment, "Расшифровка записи звонка: "+tname) {
		t.Fatalf("the task is told %q", comment)
	}
	// Whisper is down: tried again a minute later, the third failure is told
	_, err = pool.Exec(ctx, `UPDATE call_recordings SET transcript = 'pending', transcript_tries = 0 WHERE egress_id = 'EG_A'`)
	must(err)
	whisperDown = true
	for i := 0; i < 2; i++ {
		if srv.transcribeNext(ctx) {
			t.Fatal("a failure is not waited out")
		}
	}
	must(pool.QueryRow(ctx, `SELECT transcript FROM call_recordings WHERE egress_id = 'EG_A'`).Scan(&transcript))
	if transcript != "pending" {
		t.Fatalf("given up too early: %q", transcript)
	}
	if !srv.transcribeNext(ctx) {
		t.Fatal("the third failure")
	}
	must(pool.QueryRow(ctx, `SELECT transcript FROM call_recordings WHERE egress_id = 'EG_A'`).Scan(&transcript))
	must(pool.QueryRow(ctx, `SELECT body FROM task_comments WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&comment))
	if transcript != "failed" || comment != "Не удалось расшифровать запись звонка «CR-1 Созвон: итоги»" {
		t.Fatalf("a failed transcript: %q, told %q", transcript, comment)
	}

	// everyone left the call: the recording stops by itself; Egress fails it
	if st, _ := do(anna, "POST", "/call/record"+q, start); st != http.StatusOK || len(started) != 2 {
		t.Fatalf("a new recording: %d %v", st, started)
	}
	_, err = pool.Exec(ctx, `UPDATE call_recordings SET started_at = now() - interval '1 minute' WHERE egress_id = 'EG_B'`)
	must(err)
	mu.Lock()
	egress["EG_B"], people = "EGRESS_ACTIVE", []string{anna}
	mu.Unlock()
	srv.followRecordings(ctx)
	if len(stopped) != 1 {
		t.Fatalf("stopped while Анна is in the call: %v", stopped)
	}
	mu.Lock()
	people = nil
	mu.Unlock()
	srv.followRecordings(ctx)
	if len(stopped) != 2 || stopped[1] != "EG_B" {
		t.Fatalf("the call everyone left is still recorded: %v", stopped)
	}
	mu.Lock()
	egress["EG_B"] = "EGRESS_FAILED"
	mu.Unlock()
	srv.followRecordings(ctx)
	var st, why string
	must(pool.QueryRow(ctx, `SELECT state, error FROM call_recordings WHERE egress_id = 'EG_B'`).Scan(&st, &why))
	must(pool.QueryRow(ctx, `SELECT body FROM task_comments WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&comment))
	if st != "failed" || !strings.Contains(why, "pipeline") || comment != "Запись звонка не удалась" {
		t.Fatalf("a failed recording: %s %q, told %q", st, why, comment)
	}
}

// TestTranscriptText: the heading, then a line for each piece of speech with its time; nothing heard — said so.
func TestTranscriptText(t *testing.T) {
	got := transcriptText("Звонок", []speech.Segment{{Start: 0.4, Text: "Начнём."}, {Start: 125.9, Text: "Итак."}, {Start: 3600, Text: "Всё."}}, "(тихо)")
	if got != "Звонок\n\n[00:00] Начнём.\n[02:05] Итак.\n[1:00:00] Всё.\n" {
		t.Fatalf("%q", got)
	}
	if got := transcriptText("Звонок", nil, "(тихо)"); got != "Звонок\n\n(тихо)\n" {
		t.Fatalf("%q", got)
	}
}
