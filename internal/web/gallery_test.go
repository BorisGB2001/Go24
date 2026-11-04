package web

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestGallery: «Фотогалерея» (GO24DEV-929) — an HR officer with «gallery» makes an album for a department, an employee
// cannot make one at the top; the department sees it, an outsider does not (nor its album inside, nor its photos);
// photos uploaded in a batch get thumbnails and the album's cover, a non-picture is refused, the feed is told for the
// department; «Добавлять фото могут все» lets a member add photos; likes, a comment telling the photo's author,
// the neighbours on the photo's page; moving, the cover after a delete; removing the album with all inside.
func TestGallery(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	hr, emp, mate, outsider := newUser("Кадрова"), newUser("Пётр"), newUser("Ольга"), newUser("Чужой")
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	root, _, err := cs.Tree(ctx)
	must(err)
	sales, err := cs.CreateDepartment(ctx, root.ID, "Продажи", "")
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $3)`, emp, mate, sales)
	must(err)
	must(cs.SetHR(ctx, hr, true, []string{"gallery"}))

	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, I18n: bundle, Company: cs, Notify: notify.NewStore(pool), Feed: feed.NewStore(pool, ts),
		Storages: storages.NewStore(pool, nil, s3),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{hr, emp, mate, outsider} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request) (int, string, http.Header) {
		t.Helper()
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header
	}
	do := func(who, method, path string, form url.Values) (int, string, http.Header) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return send(who, req)
	}
	upload := func(who, album string, files map[string][]byte) (int, http.Header) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("caption", "Корпоратив")
		for name, data := range files {
			fw, _ := mw.CreateFormFile("files", name)
			_, _ = fw.Write(data)
		}
		_ = mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/company/gallery/"+album+"/photos", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		st, _, h := send(who, req)
		return st, h
	}
	picture := func(c color.Color) []byte {
		img := image.NewRGBA(image.Rect(0, 0, 640, 480))
		for x := range 640 {
			for y := range 480 {
				img.Set(x, y, c)
			}
		}
		var b bytes.Buffer
		must(png.Encode(&b, img))
		return b.Bytes()
	}
	idIn := func(h http.Header, prefix string) string {
		t.Helper()
		loc := h.Get("Location")
		id, ok := strings.CutPrefix(strings.SplitN(loc, "?", 2)[0], prefix)
		if !ok || id == "" {
			t.Fatalf("redirected to %q, want %s…", loc, prefix)
		}
		return id
	}

	// only a keeper makes an album at the top
	if st, _, h := do(emp, "POST", "/company/gallery", url.Values{"title": {"Моё"}}); st != http.StatusSeeOther ||
		strings.HasPrefix(h.Get("Location"), "/company/gallery/") {
		t.Fatalf("an employee made an album at the top: %d %s", st, h.Get("Location"))
	}
	st, _, h := do(hr, "POST", "/company/gallery", url.Values{"title": {"Новый год"}, "access": {"dept:" + sales}})
	if st != http.StatusSeeOther {
		t.Fatalf("album: %d", st)
	}
	album := idIn(h, "/company/gallery/")
	for who, sees := range map[string]bool{hr: true, emp: true, mate: true, outsider: false} {
		_, body, _ := do(who, "GET", "/company/gallery", nil)
		if strings.Contains(body, "Новый год") != sees {
			t.Errorf("%s sees the album: %v, want %v", who, !sees, sees)
		}
	}
	if st, _, _ := do(outsider, "GET", "/company/gallery/"+album, nil); st == http.StatusOK {
		t.Error("an outsider opens a closed album")
	}

	// photos: two pictures and a text — the pictures before the text are kept
	if st, h := upload(emp, album, map[string][]byte{"a.png": picture(color.White)}); st != http.StatusSeeOther ||
		strings.Contains(h.Get("Location"), "added=") {
		t.Errorf("an employee added photos to another's album: %d %s", st, h.Get("Location"))
	}
	st, h = upload(hr, album, map[string][]byte{"a.png": picture(color.White), "b.png": picture(color.Black)})
	if st != http.StatusSeeOther || !strings.Contains(h.Get("Location"), "added=2") {
		t.Fatalf("upload: %d %s", st, h.Get("Location"))
	}
	if _, h := upload(hr, album, map[string][]byte{"c.txt": []byte("not a picture")}); strings.Contains(h.Get("Location"), "added=") {
		t.Error("a text was taken for a photo")
	}
	photos, err := cs.Photos(ctx, hr, album)
	must(err)
	if len(photos) != 2 || photos[0].Width != 640 || photos[0].ThumbKey == "" || photos[0].Caption != "Корпоратив" || photos[0].Mime != "image/png" {
		t.Fatalf("photos %+v", photos)
	}
	a, err := cs.Album(ctx, album)
	must(err)
	if a.CoverID != photos[0].ID || a.Photos != 2 {
		t.Errorf("album %+v", a)
	}
	var posts int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM feed_posts p JOIN feed_post_recipients r ON r.post_id = p.id
		WHERE p.author_id = $1 AND r.department_id = $2 AND p.body LIKE '%/company/gallery/' || $3 || '%'`, hr, sales, album).Scan(&posts))
	if posts != 1 {
		t.Errorf("feed posts for the department: %d", posts)
	}
	for _, path := range []string{"/company/photos/" + photos[0].ID + "/file", "/company/photos/" + photos[0].ID + "/thumb"} {
		st, body, h := do(mate, "GET", path, nil)
		if st != http.StatusOK || !strings.HasPrefix(h.Get("Content-Type"), "image/") || len(body) == 0 {
			t.Errorf("%s: %d %s", path, st, h.Get("Content-Type"))
		}
		if st, _, _ := do(outsider, "GET", path, nil); st == http.StatusOK {
			t.Errorf("an outsider gets %s", path)
		}
	}

	// the photo's page: neighbours, a like, a comment the author is told of
	_, body, _ := do(mate, "GET", "/company/photos/"+photos[0].ID, nil)
	if !strings.Contains(body, `data-next="/company/photos/`+photos[1].ID+`"`) || strings.Contains(body, "data-prev=") {
		t.Error("the first photo's neighbours")
	}
	do(mate, "POST", "/company/photos/"+photos[0].ID+"/like", url.Values{})
	do(emp, "POST", "/company/photos/"+photos[0].ID+"/like", url.Values{})
	do(emp, "POST", "/company/photos/"+photos[0].ID+"/like", url.Values{}) // off again
	do(mate, "POST", "/company/photos/"+photos[0].ID+"/comment", url.Values{"body": {"Отличное фото"}})
	p, err := cs.Photo(ctx, mate, photos[0].ID)
	must(err)
	if p.Likes != 1 || !p.Liked || p.Comments != 1 {
		t.Errorf("photo %+v", p)
	}
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'gallery_comment' AND actor_id = $2`, hr, mate).Scan(&told))
	if told != 1 {
		t.Errorf("the author was told %d times", told)
	}
	comments, err := cs.PhotoComments(ctx, photos[0].ID)
	must(err)
	if st, _, _ := do(emp, "POST", "/company/photo-comments/"+comments[0].ID+"/delete", url.Values{}); st != http.StatusSeeOther {
		t.Fatal(st)
	}
	if c, _ := cs.PhotoComments(ctx, photos[0].ID); len(c) != 1 {
		t.Error("another's comment was removed")
	}

	// «Добавлять фото могут все»: the member adds; an album inside — seen by the department only
	do(hr, "POST", "/company/gallery/"+album, url.Values{"title": {"Новый год"}, "access": {"dept:" + sales}, "anyone_adds": {"1"}})
	if _, h := upload(emp, album, map[string][]byte{"e.png": picture(color.Gray{Y: 128})}); !strings.Contains(h.Get("Location"), "added=1") {
		t.Errorf("a member could not add with «anyone adds»: %s", h.Get("Location"))
	}
	_, _, h = do(hr, "POST", "/company/gallery", url.Values{"title": {"Внутри"}, "parent": {album}})
	inner := idIn(h, "/company/gallery/")
	if ok, err := cs.SeesAlbum(ctx, outsider, inner, false); err != nil || ok {
		t.Errorf("an outsider sees the album inside a closed one: %v %v", ok, err)
	}
	if ok, err := cs.SeesAlbum(ctx, mate, inner, false); err != nil || !ok {
		t.Errorf("the department does not see the album inside: %v %v", ok, err)
	}

	// moving the cover photo: the old album's cover is its next photo; then a delete
	do(hr, "POST", "/company/photos/"+photos[0].ID, url.Values{"album": {inner}})
	if a, _ := cs.Album(ctx, album); a.CoverID != photos[1].ID {
		t.Errorf("cover after the move: %s", a.CoverID)
	}
	if a, _ := cs.Album(ctx, inner); a.CoverID != photos[0].ID {
		t.Errorf("the inner album's cover: %s", a.CoverID)
	}
	if st, _, _ := do(mate, "POST", "/company/photos/"+photos[1].ID+"/delete", url.Values{}); st != http.StatusSeeOther {
		t.Fatal(st)
	}
	if _, err := cs.Photo(ctx, hr, photos[1].ID); err != nil {
		t.Error("a member removed another's photo")
	}
	must(cs.DeletePhoto(ctx, photos[1].ID))
	if a, _ := cs.Album(ctx, album); a.CoverID == "" || a.CoverID == photos[1].ID {
		t.Errorf("cover after the delete: %q", a.CoverID)
	}

	// REST: the member lists the album and its photos, the outsider does not see it
	api := func(uid, method, path string) (int, string) {
		t.Helper()
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, body := api(mate, "GET", "/api/v1/gallery/albums"); st != http.StatusOK || !strings.Contains(body, `"title":"Новый год"`) {
		t.Errorf("REST albums: %d %s", st, body)
	}
	if st, body := api(mate, "GET", "/api/v1/gallery/albums/"+album+"/photos"); st != http.StatusOK || !strings.Contains(body, `"likes":`) ||
		strings.Contains(body, "file_key") {
		t.Errorf("REST photos: %d %s", st, body)
	}
	if st, body := api(outsider, "GET", "/api/v1/gallery/albums"); st != http.StatusOK || strings.Contains(body, "Новый год") {
		t.Errorf("REST albums of an outsider: %d %s", st, body)
	}
	if st, _ := api(outsider, "GET", "/api/v1/gallery/albums/"+album+"/photos"); st != http.StatusNotFound {
		t.Errorf("REST photos of an outsider: %d", st)
	}

	// the album with all inside
	do(hr, "POST", "/company/gallery/"+album+"/delete", url.Values{})
	var left int
	must(pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM gallery_albums) + (SELECT count(*) FROM gallery_photos)`).Scan(&left))
	if left != 0 {
		t.Errorf("%d albums and photos left", left)
	}
}
