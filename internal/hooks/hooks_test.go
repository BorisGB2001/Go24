package hooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestDeliver: a new deal is queued by the database trigger, sent signed to the subscriber, a failing subscriber
// is retried later, and an event nobody subscribed to is not queued.
func TestDeliver(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'b@t') RETURNING id`).Scan(&boss))

	var mu sync.Mutex
	var got []map[string]any
	var sigOK bool
	fail := true
	s := NewStore(pool)
	var secret string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/flaky" && fail {
			w.WriteHeader(500)
			return
		}
		sigOK = r.Header.Get("X-Go24-Signature") == Sign(secret, body) && r.Header.Get("X-Go24-Event") != ""
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		got = append(got, m)
	}))
	defer srv.Close()

	if _, err := s.Save(ctx, boss, Webhook{Name: "x", URL: "ftp://x", Events: Events, Active: true}); err == nil {
		t.Fatal("a non-http URL")
	}
	_, err = s.Save(ctx, boss, Webhook{Name: "n8n", URL: srv.URL + "/ok", Events: []string{"deal.created", "nope"}, Active: true})
	must(err)
	_, err = s.Save(ctx, boss, Webhook{Name: "flaky", URL: srv.URL + "/flaky", Events: []string{"deal.created"}, Active: true})
	must(err)
	list, _ := s.List(ctx)
	secret = list[0].Secret
	if len(list[0].Events) != 1 || len(secret) < 32 {
		t.Fatalf("webhook = %+v", list[0])
	}

	c := crm.NewStore(pool)
	pipes, _ := c.Pipelines(ctx)
	_, err = c.SaveDeal(ctx, boss, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Amount: 5000})
	must(err)
	_, err = c.SaveLead(ctx, boss, crm.Lead{Title: "Не подписан никто"})
	must(err)
	now := time.Now()
	sent, errs := s.Deliver(ctx, now)
	if sent != 1 || len(errs) != 0 {
		t.Fatalf("sent %d, errors %v", sent, errs)
	}
	mu.Lock()
	if len(got) != 1 || got[0]["event"] != "deal.created" || got[0]["data"].(map[string]any)["title"] != "Поставка" || !sigOK {
		t.Fatalf("received %+v, signature ok %v", got, sigOK)
	}
	fail = false
	mu.Unlock()
	if sent, _ := s.Deliver(ctx, now.Add(10*time.Second)); sent != 0 {
		t.Fatal("a failed delivery is retried only after its pause")
	}
	if sent, _ := s.Deliver(ctx, now.Add(2*time.Minute)); sent != 1 {
		t.Fatal("the failed delivery was not retried")
	}
	must(s.Ping(ctx, list[0].ID))
	if sent, _ := s.Deliver(ctx, now.Add(3*time.Minute)); sent != 1 {
		t.Fatal("ping")
	}
	if l, _ := s.List(ctx); l[1].LastStatus == nil || *l[1].LastStatus != 200 || l[1].LastError != "" || l[0].Pending != 0 {
		t.Fatalf("after deliveries: %+v", l)
	}
}
