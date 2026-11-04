package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCallConferences: «Видеоконференции» — the organizer makes a broadcast with a password, a
// participant and a presenter; the invited get a notification; any employee joins by /call?conf=, the presenters
// and the organizer speak and lead, the others only watch; an employee signed in who opens the conference's link
// joins as one, a guest gives a name and the password and comes in at once — watching; nobody but the organizer
// changes the conference; without the broadcast everyone speaks; a new link lets nobody in by the old one and puts
// its guests out; deleted, the conference is gone with its link; REST lists and makes conferences.
func TestCallConferences(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, bob, eve, outsider := newUser("Анна"), newUser("Борис"), newUser("Ева"), newUser("Посторонний")

	var mu sync.Mutex
	var removed []string
	lk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.HasSuffix(r.URL.Path, "/RemoveParticipant") {
			mu.Lock()
			removed = append(removed, in["room"]+" "+in["identity"])
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer lk.Close()
	prov := calls.New("wss://lk", "key", "secret")
	prov.APIURL = lk.URL
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calls: prov, CallGuests: &calls.Guests{DB: pool},
		Conferences: &calls.Conferences{DB: pool}, Keys: apikeys.NewStore(pool), BaseURL: "http://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := func(c *http.Client) *http.Client {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	member := noRedirect(&http.Client{})
	do := func(c *http.Client, who, method, path string, form url.Values) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept-Language", "ru")
		if who != "" {
			rec := httptest.NewRecorder()
			must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who}))
			req.AddCookie(rec.Result().Cookies()[0])
		}
		res, err := c.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header
	}
	type grant struct {
		Room       string
		CanPublish bool
	}
	claimsOf := func(page string) (sub, meta string, g grant) {
		t.Helper()
		m := regexp.MustCompile(`data-token="([^"]+)"`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no call on the page: %s", page)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(m[1], ".")[1])
		var c struct {
			Sub, Metadata string
			Video         grant
		}
		must(json.Unmarshal(raw, &c))
		return c.Sub, c.Metadata, c.Video
	}

	st, _, h := do(member, anna, "POST", "/call/conferences", url.Values{"title": {"Планёрка"}, "members": {bob, eve},
		"broadcast": {"1"}, "presenters": {bob}, "password_set": {"1"}, "password": {"тайна"}})
	if st != http.StatusSeeOther || !strings.Contains(h.Get("Location"), "#c-") {
		t.Fatalf("create: %d %v", st, h)
	}
	do(member, anna, "POST", "/call/conferences", url.Values{"title": {"  "}})
	var id string
	var made int
	must(pool.QueryRow(ctx, `SELECT min(id::text), count(*) FROM call_conferences`).Scan(&id, &made))
	if made != 1 {
		t.Fatal("a conference without a title was made")
	}
	c, err := srv.Conferences.Get(ctx, id)
	must(err)
	if !c.Broadcast || !c.Password || len(c.Members) != 2 || len(c.Presenters) != 1 || c.Token == "" {
		t.Fatalf("the conference: %+v", c)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'call_conference' AND url = $1`, "/call/conferences#c-"+id).Scan(&notes))
	if notes != 2 {
		t.Fatalf("notifications of the invited: %d", notes)
	}
	if _, body, _ := do(member, eve, "GET", "/call/conferences", nil); !strings.Contains(body, "Планёрка") || !strings.Contains(body, "/call/g/"+c.Token) || strings.Contains(body, "?edit="+id) {
		t.Fatalf("eve's list: %s", body)
	}
	if _, body, _ := do(member, outsider, "GET", "/call/conferences", nil); strings.Contains(body, `id="c-`+id) {
		t.Fatal("someone not invited sees the conference in their list")
	}

	join := func(who string) (string, grant) {
		t.Helper()
		st, page, _ := do(member, who, "GET", "/call?conf="+id, nil)
		if st != http.StatusOK {
			t.Fatalf("%s joins: %d %s", who, st, page)
		}
		_, meta, g := claimsOf(page)
		if g.Room != calls.ConferenceRoom(id) {
			t.Fatalf("room = %s", g.Room)
		}
		if !g.CanPublish != strings.Contains(page, `data-viewer="true"`) || c.Broadcast != strings.Contains(page, "Трансляция") {
			t.Fatalf("the viewer mark of %s's page: %+v", who, g)
		}
		return meta, g
	}
	if meta, g := join(anna); !g.CanPublish || !strings.Contains(meta, "host") {
		t.Fatalf("the organizer: %s %+v", meta, g)
	}
	if meta, g := join(bob); !g.CanPublish || !strings.Contains(meta, "host") {
		t.Fatalf("the presenter: %s %+v", meta, g)
	}
	if meta, g := join(eve); g.CanPublish || meta != "" {
		t.Fatalf("a participant of the broadcast: %s %+v", meta, g)
	}
	if _, g := join(outsider); g.CanPublish {
		t.Fatal("someone by the link speaks in a broadcast")
	}

	link := "/call/g/" + c.Token
	if st, _, h := do(member, eve, "GET", link, nil); st != http.StatusSeeOther || h.Get("Location") != "/call?conf="+id {
		t.Fatalf("an employee by the conference's link: %d %v", st, h)
	}
	jar, _ := cookiejar.New(nil)
	guest := noRedirect(&http.Client{Jar: jar})
	if _, body, _ := do(guest, "", "GET", link, nil); !strings.Contains(body, `name="password"`) || !strings.Contains(body, "Планёрка") {
		t.Fatalf("the guest page: %s", body)
	}
	if _, body, _ := do(guest, "", "POST", link, url.Values{"name": {"Иван"}, "password": {"не та"}}); !strings.Contains(body, "Неверный пароль") {
		t.Fatal("a wrong password let the guest on")
	}
	do(guest, "", "POST", link, url.Values{"name": {"Иван"}, "password": {"тайна"}})
	_, page, _ := do(guest, "", "GET", link, nil)
	sub, _, g := claimsOf(page)
	if !strings.HasPrefix(sub, "guest-") || g.CanPublish || g.Room != calls.ConferenceRoom(id) || !strings.Contains(page, `data-viewer="true"`) {
		t.Fatalf("the guest of the broadcast: %s %+v", sub, g)
	}

	edit := url.Values{"title": {"Планёрка отдела"}, "members": {bob, eve, outsider}}
	do(member, bob, "POST", "/call/conferences/"+id, edit)
	if c, _ = srv.Conferences.Get(ctx, id); c.Title != "Планёрка" {
		t.Fatal("a presenter changed the conference")
	}
	if st, _, _ := do(member, anna, "POST", "/call/conferences/"+id, edit); st != http.StatusSeeOther {
		t.Fatalf("edit: %d", st)
	}
	if c, _ = srv.Conferences.Get(ctx, id); c.Broadcast || c.Password || c.Title != "Планёрка отдела" || len(c.Members) != 3 {
		t.Fatalf("edited: %+v", c)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'call_conference' AND user_id = $1`, outsider).Scan(&notes))
	if notes != 1 {
		t.Fatal("the newly invited got no notification")
	}
	if _, g := join(eve); !g.CanPublish { // c is the edited one now
		t.Fatal("without the broadcast a participant does not speak")
	}

	if st, _, _ := do(member, anna, "POST", "/call/conferences/"+id, url.Values{"action": {"link"}}); st != http.StatusSeeOther {
		t.Fatalf("new link: %d", st)
	}
	if len(removed) != 1 || removed[0] != calls.ConferenceRoom(id)+" "+sub {
		t.Fatalf("the guest of the old link stays: %v", removed)
	}
	if st, _, _ := do(guest, "", "GET", link, nil); st != http.StatusNotFound {
		t.Fatalf("the old link still opens: %d", st)
	}
	if c, _ = srv.Conferences.Get(ctx, id); c.Token == "" || "/call/g/"+c.Token == link {
		t.Fatalf("no new link: %+v", c)
	}
	other := noRedirect(&http.Client{Jar: func() http.CookieJar { j, _ := cookiejar.New(nil); return j }()})
	do(other, "", "POST", "/call/g/"+c.Token, url.Values{"name": {"Ольга"}})
	if _, page, _ := do(other, "", "GET", "/call/g/"+c.Token, nil); !strings.Contains(page, `data-token="`) {
		t.Fatal("the new link without a password does not let a guest in")
	}

	tok, err := srv.Keys.Create(ctx, eve, "conf", []string{"im:read", "im:write"}, time.Hour)
	must(err)
	api := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, body := api("GET", "/api/v1/calls/conferences", ""); st != http.StatusOK || !strings.Contains(body, "Планёрка отдела") {
		t.Fatalf("REST list: %d %s", st, body)
	}
	if st, _ := api("DELETE", "/api/v1/calls/conferences/"+id, ""); st != http.StatusForbidden {
		t.Fatalf("a participant deleted the conference by REST: %d", st)
	}
	st, body := api("POST", "/api/v1/calls/conferences", `{"title": "Вебинар", "members": ["`+anna+`"], "broadcast": true}`)
	var webinar struct {
		ID, URL   string
		Broadcast bool
	}
	_ = json.Unmarshal([]byte(body), &webinar)
	if st != http.StatusCreated || !webinar.Broadcast || !strings.HasPrefix(webinar.URL, "http://go24/call/g/") {
		t.Fatalf("REST create: %d %s", st, body)
	}
	if st, _ := api("DELETE", "/api/v1/calls/conferences/"+webinar.ID, ""); st != http.StatusNoContent {
		t.Fatalf("REST delete: %d", st)
	}

	if st, _, _ := do(member, anna, "POST", "/call/conferences/"+id, url.Values{"action": {"delete"}}); st != http.StatusSeeOther {
		t.Fatalf("delete: %d", st)
	}
	if _, err := srv.Conferences.Get(ctx, id); err != calls.ErrNoConference {
		t.Fatalf("the deleted conference: %v", err)
	}
	if st, _, _ := do(other, "", "GET", "/call/g/"+c.Token, nil); st != http.StatusNotFound {
		t.Fatalf("the deleted conference's link still opens: %d", st)
	}
	if st, _, _ := do(member, eve, "GET", "/call?conf="+id, nil); st == http.StatusOK {
		t.Fatal("the deleted conference still lets one in")
	}
}
