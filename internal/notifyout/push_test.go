package notifyout

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/notify"
)

// TestPush: the administrators turn push on (go24 makes the key once), an employee's devices subscribe, each gets the
// notifications after the subscription but not those turned off for push or read; a device its push service no
// longer knows is forgotten, the last one gone unlinks the channel; the same browser of another person is theirs.
func TestPush(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ivan', 'Иван') RETURNING id`).Scan(&ivan))
	note := func(user, kind string) int64 {
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, $2, '') RETURNING id`, user, kind).Scan(&id))
		return id
	}

	// the push service: /gone/… it no longer knows; what reached a device is kept by its path
	var mu sync.Mutex
	got := map[string][][]byte{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/gone/") {
			w.WriteHeader(http.StatusGone)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.Path] = append(got[r.URL.Path], b)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	took := func() map[string][][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := got
		got = map[string][][]byte{}
		return out
	}

	p := &Push{Client: srv.Client(), TTL: time.Hour}
	s := New(pool, "go24 secret")
	s.Channels = []Channel{p}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "http://go24.test:8080", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "Борис поставил задачу [" + it.Kind + "]" }
	s.Tr = func(key string, _ ...any) string { return key }

	phone, phoneSub := newBrowser(t, srv.URL+"/fcm/phone")
	laptop, laptopSub := newBrowser(t, srv.URL+"/fcm/laptop")
	_, goneSub := newBrowser(t, srv.URL+"/gone/old")

	if err := s.AddDevice(ctx, anna, phoneSub, "Chrome · Android"); err == nil {
		t.Fatal("a device subscribed while push is off")
	}
	must(s.SaveBot(ctx, "push", false, "", "", false, anna))
	if b, _ := s.Bot(ctx, "push"); b.HasToken {
		t.Fatal("a key made while push is off")
	}
	must(s.SaveBot(ctx, "push", true, "https://evil.example", "pasted", false, anna))
	b, err := s.Bot(ctx, "push")
	must(err)
	if !b.Ready() || len(b.Name) != 87 || b.Server != "mailto:go24@go24.test" {
		t.Fatalf("the channel: %+v", b)
	}
	key := b.Name
	must(s.SaveBot(ctx, "push", true, "", "", false, anna))
	if b, _ := s.Bot(ctx, "push"); b.Name != key {
		t.Fatal("the key changed on saving again")
	}
	if _, err := s.StartLink(ctx, anna, "push"); err == nil {
		t.Fatal("push linked by a code")
	}

	note(anna, "comment") // before the subscription: not sent
	bad := phoneSub
	bad.Endpoint = "http://push.example/x"
	if err := s.AddDevice(ctx, anna, bad, ""); err == nil {
		t.Fatal("an http endpoint was kept")
	}
	must(s.AddDevice(ctx, anna, phoneSub, "Chrome · Android"))
	must(s.AddDevice(ctx, anna, phoneSub, "Chrome · Android")) // renewed: kept once
	must(s.AddDevice(ctx, anna, laptopSub, "Firefox · Linux"))
	must(s.AddDevice(ctx, anna, goneSub, "old"))
	links, err := s.Links(ctx, anna)
	must(err)
	if len(links) != 1 || !links[0].Linked || links[0].Key != key || len(links[0].Devices) != 3 {
		t.Fatalf("the links: %+v", links)
	}
	must(s.SaveOff(ctx, anna, map[string]map[string]bool{"push": {"comment": true}}))

	fresh := note(anna, "comment")
	note(anna, "status") // turned off for push
	read := note(anna, "comment")
	must(s.Notify.MarkRead(ctx, anna, read))
	s.Pass(ctx)
	sent := took()
	if len(sent) != 2 || len(sent["/fcm/phone"]) != 1 || len(sent["/fcm/laptop"]) != 1 {
		t.Fatalf("sent: %v", sent)
	}
	var pl pushPayload
	must(json.Unmarshal(phone.decrypt(t, sent["/fcm/phone"][0]), &pl))
	if pl.Body != "Борис поставил задачу [comment]" || pl.URL != "http://go24.test:8080/notifications/"+itoa(fresh) {
		t.Fatalf("the phone got %+v", pl)
	}
	if laptop.decrypt(t, sent["/fcm/laptop"][0]) == nil {
		t.Fatal("the laptop got nothing")
	}
	devs, err := s.DevicesOf(ctx, anna)
	must(err)
	if len(devs) != 2 || devs[0].Used == nil {
		t.Fatalf("the devices after the pass: %+v", devs)
	}
	s.Pass(ctx)
	if sent := took(); len(sent) != 0 {
		t.Fatalf("sent again: %v", sent)
	}

	n, err := s.TestPush(ctx, anna, Message{Text: "test"})
	if err != nil || n != 2 {
		t.Fatalf("test push: %d %v", n, err)
	}
	took()

	// Ivan signs in on Anna's laptop: the browser's subscription is his now
	must(s.AddDevice(ctx, ivan, laptopSub, "Firefox · Linux"))
	note(anna, "comment")
	s.Pass(ctx)
	if sent := took(); len(sent) != 1 || len(sent["/fcm/phone"]) != 1 {
		t.Fatalf("after the laptop went to Ivan: %v", sent)
	}

	// the phone removed: the last device gone unlinks Anna's push
	must(s.RemoveDevice(ctx, anna, phoneSub.Endpoint))
	var linked bool
	must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notify_links WHERE user_id = $1 AND channel = 'push')`, anna).Scan(&linked))
	if linked {
		t.Fatal("push stays linked without devices")
	}
	must(s.Unlink(ctx, ivan, "push"))
	if devs, _ := s.DevicesOf(ctx, ivan); len(devs) != 0 {
		t.Fatalf("Ivan's devices after unlink: %+v", devs)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
