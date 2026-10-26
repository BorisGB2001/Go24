package web

import (
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

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// TestIMSettings: «Настройки» of the messenger — the sound is on until the person turns it off; the form and the REST
// API keep the settings of the person only; an unknown background is refused; the page shows the background, the
// alignment and the sending key chosen.
func TestIMSettings(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ($1, $1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	ann, bob := user("ann"), user("bob")
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	must(err)
	srv := &Server{DB: pool, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), I18n: bundle, pages: pages,
		Keys: apikeys.NewStore(pool), Tasks: tasks.NewStore(pool), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	if set, err := srv.imSettingsOf(ctx, ann); err != nil || set != defaultIMSettings || !set.Sound {
		t.Fatalf("settings before any change: %+v %v", set, err)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := func(who string, v url.Values) *http.Response {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who}))
		req, _ := http.NewRequest("POST", hs.URL+"/im/settings", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := cl.Do(req)
		must(err)
		res.Body.Close()
		return res
	}
	res := form(ann, url.Values{"background": {"mint"}, "align": {"left"}, "send": {"ctrl"}, "room": {"!r:x"}})
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != imRoomURL("!r:x") {
		t.Fatalf("the form: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	want := imSettings{Background: "mint", AlignLeft: true, CtrlEnter: true} // the sound unticked
	if set, _ := srv.imSettingsOf(ctx, ann); set != want {
		t.Fatalf("ann's settings: %+v", set)
	}
	if set, _ := srv.imSettingsOf(ctx, bob); set != defaultIMSettings {
		t.Fatalf("bob's settings changed with ann's: %+v", set)
	}
	form(ann, url.Values{"background": {"<script>"}, "sound": {"1"}})
	if set, _ := srv.imSettingsOf(ctx, ann); set != want {
		t.Fatalf("an unknown background kept: %+v", set)
	}

	tok, err := srv.Keys.Create(ctx, bob, "im", []string{"im:read", "im:write"}, time.Hour)
	must(err)
	api := func(method, body string) (int, imSettings) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/im/settings", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var set imSettings
		_ = json.NewDecoder(res.Body).Decode(&set)
		return res.StatusCode, set
	}
	if st, set := api("GET", ""); st != http.StatusOK || set != defaultIMSettings {
		t.Fatalf("GET: %d %+v", st, set)
	}
	if st, set := api("PUT", `{"background": "night"}`); st != http.StatusOK || set != (imSettings{Background: "night", Sound: true}) {
		t.Fatalf("PUT of one field: %d %+v", st, set)
	}
	if st, _ := api("PUT", `{"background": "plaid"}`); st == http.StatusOK {
		t.Fatal("PUT of an unknown background")
	}
	if st, _ := api("PUT", `{"colour": "red"}`); st == http.StatusOK {
		t.Fatal("PUT of an unknown field")
	}

	var b strings.Builder
	r := matrixsync.Room{ID: "!r:x", Name: "Чат", Kind: "group"}
	set, _ := srv.imSettingsOf(ctx, ann)
	must(pages["ru-RU"]["im"].ExecuteTemplate(&b, "content", page{Data: map[string]any{"Rooms": []matrixsync.Room{r}, "Room": r.ID,
		"Current": r, "Tab": "all", "Settings": set, "Backgrounds": imBackgrounds}}))
	out := b.String()
	for _, want := range []string{`class="im im-bg-mint im-left" data-im data-ctrl-enter`, "data-settings-open", "Настройки мессенджера",
		`name="background" value="mint" checked`, `name="align" value="left" checked`, `name="send" value="ctrl" checked`, `title="Сирень"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(out, `name="sound" value="1" checked`) {
		t.Error("the sound shown on")
	}

	rec := httptest.NewRecorder()
	writeUnread(rec, map[string]int{"!r:x": 2}, nil, nil, true)
	if !strings.Contains(rec.Body.String(), `"sound":true`) || !strings.Contains(rec.Body.String(), `"total":2`) {
		t.Errorf("the unread event: %s", rec.Body.String())
	}
}
