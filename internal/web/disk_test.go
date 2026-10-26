package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
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
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskHTTP: the drive page with its actions; renaming, moving by the dialog's form, copying, deleting several,
// downloading a folder as ZIP, the trash — and someone else's drive is not reached by any of them.
func TestDiskHTTP(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
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
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	my, _ := srv.Drive.Open(ctx, anna, "my")
	docs, err := srv.Drive.CreateFolder(ctx, anna, my, "", "Документы")
	must(err)
	addFile := func(folder, name string) string { // empty files: they have no stored bytes to copy or read
		id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: name, ContentType: "text/plain",
			UploadedBy: anna, Drive: "user", DriveOwner: anna, FolderID: folder})
		must(err)
		return id
	}
	a := addFile(docs, "a.txt")
	b := addFile("", "b.txt")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}

	page := do(anna, "GET", "/disk/my", nil, 200)
	for _, want := range []string{`data-disk="/disk/my"`, `data-item="folder" data-id="` + docs, `data-item="file" data-id="` + b,
		`/disk/my?zip&folder_id=` + docs, `action="/disk/my/rename"`, `data-disk-dlg`, "js/disk.js", `name="file" multiple`} {
		if !strings.Contains(page, want) {
			t.Errorf("the drive page has no %q", want)
		}
	}

	// «История версий» (empty versions: no stored bytes); someone else sees none of it
	must(srv.Drive.AddVersion(ctx, anna, my, a, drive.NewVersion{Key: "k/v2-" + randomHex(4), ContentType: "text/plain"}))
	var vs struct {
		Name     string
		Versions []struct {
			ID string
			No int
		}
	}
	must(json.Unmarshal([]byte(do(anna, "GET", "/files/"+a+"/versions", nil, 200)), &vs))
	if vs.Name != "a.txt" || len(vs.Versions) != 2 || vs.Versions[0].ID != "" || vs.Versions[1].No != 1 {
		t.Fatalf("versions = %+v", vs)
	}
	if !strings.Contains(do(anna, "GET", "/disk/my?folder="+docs, nil, 200), `data-disk-versions="`+a+`" data-mine`) {
		t.Error("the file has no «История версий»")
	}
	do(anna, "GET", "/files/"+a+"/versions/"+vs.Versions[1].ID, nil, 200)
	do(eve, "GET", "/files/"+a+"/versions", nil, 403)
	do(eve, "GET", "/files/"+a+"/versions/"+vs.Versions[1].ID, nil, 403)
	do(eve, "POST", "/disk/my/version-restore", url.Values{"file": {a}, "version": {vs.Versions[1].ID}}, 404)
	do(anna, "POST", "/disk/my/version-restore", url.Values{"folder": {docs}, "file": {a}, "version": {vs.Versions[1].ID}}, 200)
	if list, _ := srv.Drive.Versions(ctx, a); len(list) != 3 {
		t.Errorf("after restoring: %+v", list)
	}

	// search through the whole drive, the filter by type, «Последние»
	if p := do(anna, "GET", "/disk/my?q=A.TXT", nil, 200); !strings.Contains(p, `data-id="`+a+`"`) || !strings.Contains(p, "?folder="+docs) || strings.Contains(p, `data-id="`+b+`"`) {
		t.Errorf("search: %.3000s", p)
	}
	if p := do(anna, "GET", "/disk/my?type=pdf", nil, 200); strings.Contains(p, `data-item="file"`) {
		t.Error("the filter by type shows text files as PDF")
	}
	if p := do(anna, "GET", "/disk/my?type=doc&sort=date&desc=1", nil, 200); !strings.Contains(p, `data-id="`+a+`"`) || !strings.Contains(p, `data-id="`+b+`"`) {
		t.Error("the filter by type has not the text files")
	}
	if p := do(anna, "GET", "/disk/recent", nil, 200); !strings.Contains(p, `data-id="`+a+`"`) || !strings.Contains(p, "/disk/my?folder="+docs) {
		t.Errorf("recent: %.3000s", p)
	}
	if p := do(eve, "GET", "/disk/recent", nil, 200); strings.Contains(p, `data-id="`+a+`"`) {
		t.Error("someone else's recent has anna's file")
	}

	// «Доступ»: anna opens «Документы» to eve for reading; eve finds it in «Общие со мной», sees and downloads what
	// it holds, uploads nothing; closed — gone
	if p := do(anna, "GET", "/disk/my", nil, 200); !strings.Contains(p, `id="sh-`+docs+`"`) || !strings.Contains(p, `value="user:`+eve+`"`) {
		t.Error("the folder has no «Доступ»")
	}
	do(anna, "POST", "/disk/my/share", url.Values{"folder_id": {docs}, "whom": {"user:" + eve}, "access": {"read"}}, 200)
	do(eve, "POST", "/disk/my/share", url.Values{"folder_id": {docs}, "whom": {"user:" + eve}, "access": {"full"}}, 404)
	if p := do(eve, "GET", "/disk/shared", nil, 200); !strings.Contains(p, "/disk/sh/"+docs) {
		t.Errorf("eve's shared: %.2000s", p)
	}
	if p := do(eve, "GET", "/disk/sh/"+docs, nil, 200); !strings.Contains(p, `data-id="`+a+`"`) || strings.Contains(p, "data-disk-upload") || strings.Contains(p, "?trash") {
		t.Errorf("the shared folder for eve: %.3000s", p)
	}
	do(eve, "GET", "/files/"+a+"/preview", nil, 200)
	do(eve, "GET", "/files/"+b+"/preview", nil, 403) // not in the folder
	do(eve, "POST", "/disk/sh/"+docs+"/folders", url.Values{"name": {"Ева"}}, 403)
	do(eve, "GET", "/disk/sh/"+b, nil, 404)
	shares, _ := srv.Drive.Shares(ctx, []string{docs})
	do(anna, "POST", "/disk/my/share", url.Values{"off": {shares[docs][0].ID}}, 200)
	do(eve, "GET", "/disk/sh/"+docs, nil, 404)
	do(eve, "GET", "/files/"+a+"/preview", nil, 403)

	// rename; a bad name is an error the form shows
	do(anna, "POST", "/disk/my/rename", url.Values{"file": {b}, "name": {"бэ.txt"}}, 200)
	if body := do(anna, "POST", "/disk/my/rename", url.Values{"folder_id": {docs}, "name": {"x/y"}}, 422); !strings.Contains(body, "error") {
		t.Errorf("a bad folder name: %s", body)
	}
	// «Заблокировать»: the lock and who has it are shown; locked by another, the file is not renamed
	do(anna, "POST", "/disk/my/lock", url.Values{"file": {b}}, 200)
	if page := do(anna, "GET", "/disk/my", nil, 200); !strings.Contains(page, "data-locked") || !strings.Contains(page, "Заблокировал(а) Анна") ||
		!strings.Contains(page, "Разблокировать") {
		t.Error("the page does not show the lock")
	}
	do(anna, "POST", "/disk/my/rename", url.Values{"file": {b}, "name": {"бэ.txt"}}, 200) // her own lock
	must2(pool.Exec(ctx, `UPDATE files SET locked_by = $2 WHERE id = $1`, b, eve))
	if body := do(anna, "POST", "/disk/my/rename", url.Values{"file": {b}, "name": {"б.txt"}}, 422); !strings.Contains(body, "заблокирован") {
		t.Errorf("renaming a file locked by another: %s", body)
	}
	do(anna, "POST", "/disk/my/lock", url.Values{"file": {b}, "unlock": {"1"}}, 200) // she manages her drive
	if page := do(anna, "GET", "/disk/my", nil, 200); strings.Contains(page, "data-locked") {
		t.Error("the lock stays after unlocking")
	}
	// move b into Документы: the answer leads there
	if body := do(anna, "POST", "/disk/my/move", url.Values{"file": {b}, "to": {"my"}, "to_folder": {docs}}, 200); !strings.Contains(body, "/disk/my?folder="+docs) {
		t.Errorf("after moving: %s", body)
	}
	// copy the folder to the company drive
	do(anna, "POST", "/disk/my/move", url.Values{"folder_id": {docs}, "to": {"company"}, "copy": {"1"}}, 200)
	co, _ := srv.Drive.Open(ctx, anna, "company")
	if folders, _, _, _ := srv.Drive.List(ctx, co, ""); len(folders) != 1 || folders[0].Name != "Документы" {
		t.Fatalf("the company drive after copying: %v", folders)
	}

	// the folder as ZIP
	req, _ := http.NewRequest("GET", hs.URL+"/disk/my?zip&folder_id="+docs, nil)
	req.AddCookie(as[anna])
	res, err := client.Do(req)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Disposition"), url.PathEscape("Документы.zip")) {
		t.Fatalf("zip: %d %s", res.StatusCode, res.Header.Get("Content-Disposition"))
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	must(err)
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, " ") != "Документы/a.txt Документы/бэ.txt" {
		t.Errorf("zip holds %v", names)
	}

	// the viewer: a picture is shown by /raw, a text as HTML; not to someone else
	pic := addFile("", "фото.png")
	if page := do(anna, "GET", "/disk/my", nil, 200); !strings.Contains(page, `data-kind="image"`) || !strings.Contains(page, "/files/"+pic+"/raw") {
		t.Error("the picture has no tile or viewer kind")
	}
	if body := do(anna, "GET", "/files/"+a+"/preview", nil, 200); !strings.Contains(body, `"kind": "html"`) && !strings.Contains(body, `"kind":"html"`) {
		t.Errorf("a text's preview: %s", body)
	}
	do(anna, "GET", "/files/"+pic+"/raw", nil, 200)
	do(eve, "GET", "/files/"+pic+"/raw", nil, 403)
	do(eve, "GET", "/files/"+a+"/preview", nil, 403)
	do(anna, "POST", "/disk/my/purge", url.Values{"file": {pic}}, 404) // not in the trash
	do(anna, "POST", "/disk/my/delete", url.Values{"file": {pic}}, 200)
	do(anna, "POST", "/disk/my/purge", url.Values{"file": {pic}}, 200)

	// a public link of the folder, with a password: anyone with the address, after typing it, sees the folder and
	// downloads its files — nothing outside it; switched off or expired, the link is gone
	do(anna, "POST", "/disk/my/link", url.Values{"folder_id": {docs}, "password": {"секрет"}}, 200)
	links, err := srv.Drive.Links(ctx, []string{docs})
	must(err)
	token := links[docs].Token
	if token == "" || !links[docs].Password {
		t.Fatalf("the link: %+v", links[docs])
	}
	if page := do(anna, "GET", "/disk/my", nil, 200); !strings.Contains(page, "/s/"+token) {
		t.Error("the drive does not show the link")
	}
	jar, _ := cookiejar.New(nil)
	anon := &http.Client{Jar: jar}
	pub := func(method, path string, form url.Values) (int, string) {
		t.Helper()
		var res *http.Response
		var err error
		if method == "POST" {
			res, err = anon.PostForm(hs.URL+path, form)
		} else {
			res, err = anon.Get(hs.URL + path)
		}
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if code, body := pub("GET", "/s/"+token, nil); code != 200 || !strings.Contains(body, `name="password"`) || strings.Contains(body, "a.txt") {
		t.Fatalf("before the password: %d", code)
	}
	if code, _ := pub("GET", "/s/"+token+"/f/"+a, nil); code == 200 {
		t.Fatal("a file given before the password")
	}
	if _, body := pub("POST", "/s/"+token, url.Values{"password": {"не тот"}}); !strings.Contains(body, `name="password"`) {
		t.Fatal("a wrong password let in")
	}
	if code, body := pub("POST", "/s/"+token, url.Values{"password": {"секрет"}}); code != 200 || !strings.Contains(body, "a.txt") {
		t.Fatalf("after the password: %d %.200s", code, body)
	}
	if code, _ := pub("GET", "/s/"+token+"/f/"+a, nil); code != 200 {
		t.Fatalf("a file of the shared folder: %d", code)
	}
	outside := addFile("", "вне.txt")
	if code, _ := pub("GET", "/s/"+token+"/f/"+outside, nil); code != 404 {
		t.Fatalf("a file outside the shared folder: %d", code)
	}
	if code, _ := pub("GET", "/s/"+token+"/zip", nil); code != 200 {
		t.Fatalf("the shared folder as zip: %d", code)
	}
	if links, _ := srv.Drive.Links(ctx, []string{docs}); links[docs].Downloads != 2 {
		t.Errorf("downloads = %d", links[docs].Downloads)
	}
	do(anna, "POST", "/disk/my/link", url.Values{"folder_id": {docs}, "expires": {"2000-01-01"}}, 422)
	must2(pool.Exec(ctx, `UPDATE drive_links SET expires_at = now() - interval '1 minute'`))
	if code, _ := pub("GET", "/s/"+token, nil); code != 404 {
		t.Fatalf("an expired link: %d", code)
	}
	do(eve, "POST", "/disk/my/link", url.Values{"folder_id": {docs}, "off": {"1"}}, 404)
	do(anna, "POST", "/disk/my/link", url.Values{"folder_id": {docs}, "off": {"1"}}, 200)
	if code, _ := pub("GET", "/s/"+token, nil); code != 404 {
		t.Fatalf("a link switched off: %d", code)
	}
	do(anna, "POST", "/disk/my/delete", url.Values{"file": {outside}}, 200)
	do(anna, "POST", "/disk/my/purge", url.Values{"file": {outside}}, 200)

	// someone else: not her drive's folder, not her files
	do(eve, "POST", "/disk/my/rename", url.Values{"file": {a}, "name": {"x"}}, 404)
	do(eve, "POST", "/disk/my/move", url.Values{"file": {a}, "to": {"my"}}, 404)
	do(eve, "GET", "/disk/my?zip&folder_id="+docs, nil, 404)

	// delete several at once: they go to the trash, which lists them; restore one, delete the rest for good
	do(anna, "POST", "/disk/my/delete", url.Values{"file": {a, b}}, 200)
	do(anna, "POST", "/disk/my/delete", url.Values{"folder_id": {docs}}, 200)
	if folders, files, _, _ := srv.Drive.List(ctx, my, ""); len(folders)+len(files) != 0 {
		t.Errorf("left on the drive: %v %v", folders, files)
	}
	trash := do(anna, "GET", "/disk/my?trash", nil, 200)
	for _, want := range []string{`data-item="folder" data-id="` + docs, `data-item="file" data-id="` + a, `action="/disk/my/restore"`, `action="/disk/my/purge"`} {
		if !strings.Contains(trash, want) {
			t.Errorf("the trash page has no %q", want)
		}
	}
	do(eve, "POST", "/disk/my/restore", url.Values{"folder_id": {docs}}, 404)
	if body := do(anna, "POST", "/disk/my/restore", url.Values{"folder_id": {docs}}, 200); !strings.Contains(body, "/disk/my?trash") {
		t.Errorf("after restoring: %s", body)
	}
	do(anna, "POST", "/disk/my/purge", url.Values{"file": {a, b}}, 200)
	if folders, files, _, _ := srv.Drive.List(ctx, my, ""); len(folders) != 1 || len(files) != 0 {
		t.Errorf("after restoring the folder and purging the files: %v %v", folders, files)
	}
	if list, _ := srv.Drive.Trash(ctx, my); len(list) != 0 {
		t.Errorf("the trash is not empty: %v", list)
	}
}

func TestFileKind(t *testing.T) {
	for name, want := range map[string]string{"a.png": "image", "a.PDF": "pdf", "a.docx": "html", "clip.mp4": "video", "song.mp3": "audio",
		"a.exe": "", "a.svg": ""} {
		if got := fileKind(name, ""); got != want {
			t.Errorf("fileKind(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDiskSort(t *testing.T) {
	for _, c := range []struct{ name, ct, want string }{{"а.DOCX", "", "doc"}, {"t.csv", "", "sheet"}, {"s.pptx", "", "slides"},
		{"x.pdf", "", "pdf"}, {"p.jpg", "image/jpeg", "image"}, {"v", "video/mp4", "video"}, {"a.mp3", "", "audio"}, {"z.7z", "", "archive"}, {"b.bin", "", "other"}} {
		if got := fileType(c.name, c.ct); got != c.want {
			t.Errorf("fileType(%s) = %s, want %s", c.name, got, c.want)
		}
	}
	now := time.Now()
	files := []drive.File{{Name: "б", Size: 1, CreatedAt: now}, {Name: "А", Size: 3, CreatedAt: now.Add(-time.Hour)}, {Name: "в", Size: 2, CreatedAt: now.Add(time.Hour)}}
	names := func() string {
		var n []string
		for _, f := range files {
			n = append(n, f.Name)
		}
		return strings.Join(n, "")
	}
	for _, c := range []struct {
		by   string
		desc bool
		want string
	}{{"", false, "Абв"}, {"", true, "вбА"}, {"size", true, "Авб"}, {"date", false, "Абв"}, {"date", true, "вбА"}} {
		if sortFiles(files, c.by, c.desc); names() != c.want {
			t.Errorf("sort %s %v = %s, want %s", c.by, c.desc, names(), c.want)
		}
	}
	u := diskSortURLs(url.Values{"folder": {"f1"}, "q": {"x"}}, "size", true)
	if u["size"] != "?folder=f1&q=x&sort=size" || u["name"] != "?folder=f1&q=x" || u["date"] != "?desc=1&folder=f1&q=x&sort=date" {
		t.Errorf("sort urls = %v", u)
	}
	if u := diskSortURLs(url.Values{}, "", false); u["name"] != "?desc=1" {
		t.Errorf("name again = %v", u)
	}
}
