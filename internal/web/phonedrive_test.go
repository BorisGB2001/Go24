package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestPhoneRecordDrive: «Записанные звонки» — the record of an answered call goes to the company's drive, into the
// month's folder of «Записанные звонки», which only the administrators see; once, and not when turned off.
func TestPhoneRecordDrive(t *testing.T) {
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
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, bob, "102"))
	must(store.SetEnabled(ctx, anna, true))
	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	ts := tasks.NewStore(pool)
	records := t.TempDir()
	srv := &Server{DB: pool, Tasks: ts, I18n: bundle, pages: pages, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Telephony: &telephony.PBX{Store: store, Key: "astkey", Records: records, ARI: &telephony.ARI{URL: "http://127.0.0.1:1", User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	wav := append([]byte("RIFF"), make([]byte, 20000)...)
	cdr := func(id, from, dial string) string {
		t.Helper()
		must(os.WriteFile(filepath.Join(records, id+".wav"), wav, 0o644))
		res, err := http.PostForm(hs.URL+"/telephony/cdr", url.Values{"key": {"astkey"}, "id": {id}, "from": {from}, "to": {"102"},
			"rec": {id + ".wav"}, "start": {"1761401000"}, "end": {"1761401090"}, "answer": {"1761401010"}, "dial": {dial}})
		must(err)
		res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("cdr %s: %d", id, res.StatusCode)
		}
		var call string
		must(pool.QueryRow(ctx, `SELECT id::text FROM telephony_calls WHERE uniqueid = $1`, id).Scan(&call))
		return call
	}
	saved := func(call string) string {
		t.Helper()
		for i := 0; i < 50; i++ {
			c, err := store.CallByID(ctx, call)
			must(err)
			if c.DriveFile != "" {
				return c.DriveFile
			}
			time.Sleep(100 * time.Millisecond)
		}
		return ""
	}

	if on, err := store.RecordDrive(ctx); err != nil || !on {
		t.Fatalf("on by default: %v %v", on, err)
	}
	first := cdr("d.1", "+79001110001", "ANSWER")
	fid := saved(first)
	if fid == "" {
		t.Fatal("the record is not on the drive")
	}
	var name, folder, month, top string
	var size int64
	must(pool.QueryRow(ctx, `SELECT f.name, f.size, f.drive, m.name, p.name FROM files f JOIN drive_folders m ON m.id = f.folder_id
		JOIN drive_folders p ON p.id = m.parent_id WHERE f.id::text = $1 AND p.parent_id IS NULL`, fid).Scan(&name, &size, &folder, &month, &top))
	at := time.Unix(1761401000, 0).Local()
	if name != at.Format("2006-01-02 15-04-05")+" +79001110001.wav" || size != int64(len(wav)) || folder != "company" ||
		month != at.Format("2006-01") || top != "Записанные звонки" {
		t.Fatalf("the file: %q %d %q %q %q", name, size, folder, month, top)
	}
	// the folder is the administrators': an employee does not see it on the company's drive
	for who, sees := range map[string]bool{anna: true, bob: false} {
		d := drive.Drive{Kind: "company", CanWrite: true, CanManage: true}
		if who == bob {
			d, err = srv.Drive.Open(ctx, bob, "company")
			must(err)
		}
		folders, _, _, err := srv.Drive.List(ctx, d, "")
		must(err)
		found := false
		for _, f := range folders {
			found = found || f.Name == "Записанные звонки"
		}
		if found != sees {
			t.Fatalf("%s sees the folder: %v", who, found)
		}
	}
	// once: another call goes into the same folder, the first is not copied again
	second := cdr("d.2", "+79001110002", "ANSWER")
	if saved(second) == "" {
		t.Fatal("the second record")
	}
	must(srv.phoneToDrive(ctx, first))
	var files, tops int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE drive = 'company'`).Scan(&files))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM drive_folders WHERE drive = 'company' AND parent_id IS NULL`).Scan(&tops))
	if files != 2 || tops != 1 {
		t.Fatalf("files %d, top folders %d", files, tops)
	}
	// turned off — the record stays in the telephony only
	must(store.SetRecordDrive(ctx, anna, false))
	third := cdr("d.3", "+79001110003", "ANSWER")
	time.Sleep(500 * time.Millisecond)
	if c, err := store.CallByID(ctx, third); err != nil || c.DriveFile != "" {
		t.Fatalf("turned off: %q %v", c.DriveFile, err)
	}
}
