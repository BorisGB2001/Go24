package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/live"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskBoardHTTP: «Создать → Доска» on the company's drive opens the new board in the editor; the scene is saved
// with the stamp of its state, a save from an older state is refused (409) and a scene not changed since is not
// sent again (304); the autosaves of the author within ten minutes are one version, a later one keeps the state
// before; who may not change the board sees it read only and saves nothing.
func TestDiskBoardHTTP(t *testing.T) {
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
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3), I18n: bundle,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Live = live.NewBroker()
	lctx, stopLive := context.WithCancel(ctx)
	defer stopLive()
	go srv.Live.Listen(lctx, pool, "go24_notify", srv.Log)
	time.Sleep(300 * time.Millisecond) // LISTEN is on
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path, ctype, body string, hdr map[string]string, want int) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		req.Header.Set("Accept-Language", "ru-RU")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return res, string(raw)
	}
	form := "application/x-www-form-urlencoded"
	jsonH := map[string]string{"Accept": "application/json"}

	res, _ := do(anna, "POST", "/disk/company/new-board", form, url.Values{"name": {"План работ"}, "folder": {""}}.Encode(), nil, http.StatusSeeOther)
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/disk/company/board/") {
		t.Fatalf("a new board leads to %q", loc)
	}
	id := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/disk/company/board/")
	var name, ct string
	must(pool.QueryRow(ctx, `SELECT name, content_type FROM files WHERE id::text = $1`, id).Scan(&name, &ct))
	if name != "План работ.board" || ct != drive.BoardType {
		t.Fatalf("the new board: %q %q", name, ct)
	}
	_, page := do(anna, "GET", loc, "", "", nil, 200)
	if !strings.Contains(page, `data-scene="/disk/company/board/`+id+`?scene"`) || !strings.Contains(page, `data-edit="true"`) {
		t.Fatalf("the editor page: %.500s", page)
	}
	if _, list := do(anna, "GET", "/disk/company", "", "", nil, 200); !strings.Contains(list, `/disk/company/board/`+id+`?folder=`) {
		t.Fatal("the drive does not open the board in the editor")
	}

	scene := "/disk/company/board/" + id + "?scene"
	res, blank := do(anna, "GET", scene, "", "", nil, 200)
	s0 := res.Header.Get("X-Board-Stamp")
	if s0 == "" || !strings.Contains(blank, `"type":"excalidraw"`) {
		t.Fatalf("the scene of a new board: %q %s", s0, blank)
	}
	drawn := `{"type":"excalidraw","version":2,"elements":[{"id":"r1","type":"rectangle","version":2}],"appState":{},"files":{}}`
	_, out := do(anna, "POST", "/disk/company/board/"+id+"/save?stamp="+s0, "application/json", drawn, jsonH, 200)
	var saved struct{ Stamp string }
	must(json.Unmarshal([]byte(out), &saved))
	if saved.Stamp == "" || saved.Stamp == s0 {
		t.Fatalf("the stamp after a save: %q", out)
	}
	do(anna, "POST", "/disk/company/board/"+id+"/save?stamp="+s0, "application/json", drawn, jsonH, http.StatusConflict)
	do(anna, "GET", scene, "", "", map[string]string{"If-None-Match": `"` + saved.Stamp + `"`}, http.StatusNotModified)
	if _, got := do(anna, "GET", scene, "", "", nil, 200); got != drawn {
		t.Fatalf("the saved scene: %s", got)
	}
	do(anna, "POST", "/disk/company/board/"+id+"/save?stamp="+saved.Stamp, "application/json", `{"type":"other"}`, jsonH, http.StatusUnprocessableEntity)
	versions := func() (n int) {
		must(pool.QueryRow(ctx, `SELECT count(*) FROM drive_file_versions WHERE file_id::text = $1`, id).Scan(&n))
		return
	}
	if n := versions(); n != 0 {
		t.Fatalf("autosaves of the author made %d versions", n)
	}
	// an hour later the author's save keeps the state before as a version
	_, err = pool.Exec(ctx, `UPDATE files SET created_at = now() - interval '1 hour' WHERE id::text = $1`, id)
	must(err)
	_, out = do(anna, "POST", "/disk/company/board/"+id+"/save?stamp="+saved.Stamp, "application/json", strings.Replace(drawn, `"version":2}`, `"version":3}`, 1), jsonH, 200)
	must(json.Unmarshal([]byte(out), &saved))
	if n := versions(); n != 1 {
		t.Fatalf("versions after a later save: %d", n)
	}

	// Пётр may read the company's board, not change it: Анна made it
	_, page = do(petr, "GET", loc, "", "", nil, 200)
	if !strings.Contains(page, `data-edit="false"`) {
		t.Fatal("the board is not read only for another")
	}
	do(petr, "GET", scene, "", "", nil, 200)
	do(petr, "POST", "/disk/company/board/"+id+"/save?stamp="+saved.Stamp, "application/json", drawn, jsonH, http.StatusForbidden)
	// ?live: Анна's editor hears that Пётр came, where his pointer is and that a board was saved
	req, _ := http.NewRequest("GET", hs.URL+"/disk/company/board/"+id+"?live", nil)
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	req.AddCookie(rec.Result().Cookies()[0])
	sctx, stopStream := context.WithTimeout(ctx, 20*time.Second)
	defer stopStream()
	stream, err := http.DefaultClient.Do(req.WithContext(sctx))
	must(err)
	defer stream.Body.Close()
	lines := bufio.NewScanner(stream.Body)
	next := func(event string) map[string]any {
		t.Helper()
		var name string
		for lines.Scan() {
			l := lines.Text()
			if v, ok := strings.CutPrefix(l, "event: "); ok {
				name = v
			}
			if v, ok := strings.CutPrefix(l, "data: "); ok && name == event {
				var ev map[string]any
				must(json.Unmarshal([]byte(v), &ev))
				return ev
			}
		}
		t.Fatalf("no %q event: %v", event, lines.Err())
		return nil
	}
	if hello := next("hello"); hello["c"] == "" || hello["u"] != anna {
		t.Fatalf("hello: %v", hello)
	}
	do(petr, "POST", "/disk/company/board/"+id+"/pointer", form, url.Values{"x": {"12.5"}, "y": {"-3"}, "b": {"down"}, "c": {"pc1"}}.Encode(), nil, http.StatusNoContent)
	if ev := next("board"); ev["t"] != "ptr" || ev["n"] != "Пётр" || ev["x"] != 12.5 || ev["b"] != "down" {
		t.Fatalf("the pointer of another: %v", ev)
	}
	do(petr, "POST", "/disk/company/board/"+id+"/pointer", form, url.Values{"x": {"NaN"}, "y": {"1"}}.Encode(), jsonH, http.StatusUnprocessableEntity)
	_, out = do(anna, "POST", "/disk/company/board/"+id+"/save?c=other&stamp="+saved.Stamp, "application/json", drawn, jsonH, 200)
	must(json.Unmarshal([]byte(out), &saved))
	if ev := next("board"); ev["t"] != "saved" || ev["s"] != saved.Stamp {
		t.Fatalf("a save: %v", ev)
	}

	// «Доски» lists the board for both, and a public link shows it read only; showing it is not a download
	for _, who := range []string{anna, petr} {
		if _, list := do(who, "GET", "/boards", "", "", nil, 200); !strings.Contains(list, `/disk/company/board/`+id+`?folder=&amp;from=boards`) {
			t.Fatalf("«Доски» has no board: %.800s", list)
		}
	}
	if _, list := do(anna, "GET", "/boards?q="+url.QueryEscape("нет такой"), "", "", nil, 200); strings.Contains(list, id) {
		t.Fatal("the search of «Доски» finds what it should not")
	}
	co, err := srv.Drive.Open(ctx, anna, "company")
	must(err)
	token, err := srv.Drive.SetLink(ctx, anna, co, "file", id, "", false, nil)
	must(err)
	pub, err := http.Get(hs.URL + "/s/" + token)
	must(err)
	raw, _ := io.ReadAll(pub.Body)
	pub.Body.Close()
	if !strings.Contains(string(raw), `data-scene="/s/`+token+`/f/`+id+`?scene"`) || !strings.Contains(string(raw), `data-alone="true"`) {
		t.Fatalf("the public link of a board: %.600s", raw)
	}
	pub, err = http.Get(hs.URL + "/s/" + token + "/f/" + id + "?scene")
	must(err)
	raw, _ = io.ReadAll(pub.Body)
	pub.Body.Close()
	var downloads int
	must(pool.QueryRow(ctx, `SELECT downloads FROM drive_links WHERE token = $1`, token).Scan(&downloads))
	if !strings.Contains(string(raw), `"type":"excalidraw"`) || downloads != 0 {
		t.Fatalf("the scene by the link: %d downloads, %.200s", downloads, raw)
	}

	// a file that is not a board does not open as one
	other, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "a.txt", ContentType: "text/plain", UploadedBy: anna, Drive: "company"})
	must(err)
	do(anna, "GET", "/disk/company/board/"+other+"?scene", "", "", nil, http.StatusNotFound)
}
