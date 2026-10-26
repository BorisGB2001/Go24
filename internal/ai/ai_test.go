package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// A sealed key opens with the same secret only.
func TestSeal(t *testing.T) {
	s, other := NewStore(nil, "secret-a"), NewStore(nil, "secret-b")
	sealed, err := s.seal("sk-ant-12345")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.open(sealed); err != nil || got != "sk-ant-12345" {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := other.open(sealed); err == nil {
		t.Fatal("opened with another secret")
	}
	if got, err := s.open(nil); err != nil || got != "" {
		t.Fatalf("open(nil) = %q, %v", got, err)
	}
}

// TestConnections runs against the test database: the company's and personal connections, the default one, the
// choice of an employee, the key kept on a change and a check against a provider.
func TestConnections(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ai1', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ai2', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))

	var auth string
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization") + r.Header.Get("x-api-key")
		if auth != "Bearer good-key" && auth != "ant-key-9876" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"invalid key"}}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	defer prov.Close()
	s := NewStore(pool, "secret")

	if _, _, err := s.For(ctx, anna); err != ErrNone {
		t.Fatalf("For without connections = %v", err)
	}
	errKey := func(err error) string {
		var e *i18n.Error
		if errors.As(err, &e) {
			return e.Key
		}
		return ""
	}
	if _, err := s.Save(ctx, anna, Connection{Name: "", Provider: "openai"}, "k"); errKey(err) != "err.ai_name_required" {
		t.Fatalf("no name: %v", err)
	}
	if _, err := s.Save(ctx, anna, Connection{Name: "X", Provider: "nope"}, "k"); errKey(err) != "err.ai_provider" {
		t.Fatalf("bad provider: %v", err)
	}
	if _, err := s.Save(ctx, anna, Connection{Name: "X", Provider: "compatible"}, ""); errKey(err) != "err.ai_url_required" {
		t.Fatalf("compatible without URL: %v", err)
	}
	if _, err := s.Save(ctx, anna, Connection{Name: "X", Provider: "openai", BaseURL: "ftp://x"}, "k"); errKey(err) != "err.ai_url_bad" {
		t.Fatalf("bad URL: %v", err)
	}
	if _, err := s.Save(ctx, anna, Connection{Name: "X", Provider: "openai"}, ""); errKey(err) != "err.ai_key_required" {
		t.Fatalf("no key: %v", err)
	}

	// the company's connection: the first is the default one and everyone works through it
	company, err := s.Save(ctx, anna, Connection{Name: "Компания", Provider: "openai", BaseURL: prov.URL, Model: "model-a"}, "good-key")
	must(err)
	c, key, err := s.For(ctx, ivan)
	if err != nil || c.ID != company || key != "good-key" || !c.Default || c.KeyTail != "-key" || c.Owner != "" {
		t.Fatalf("For(ivan) = %+v, %q, %v", c, key, err)
	}
	// Anna's own connection goes before the company's; Ivan does not see it
	mine, err := s.Save(ctx, anna, Connection{Owner: anna, Name: "Мой", Provider: "anthropic", BaseURL: prov.URL}, "ant-key-9876")
	must(err)
	if c, _, _ := s.For(ctx, anna); c.ID != mine {
		t.Fatalf("For(anna) = %s, want own", c.ID)
	}
	if c, _, _ := s.For(ctx, ivan); c.ID != company {
		t.Fatalf("For(ivan) = %s, want company's", c.ID)
	}
	if _, err := s.Get(ctx, ivan, mine); err != ErrNotFound {
		t.Fatalf("Ivan got Anna's connection: %v", err)
	}
	if err := s.Choose(ctx, ivan, mine); err != ErrNotFound {
		t.Fatalf("Ivan chose Anna's connection: %v", err)
	}
	if list, _ := s.List(ctx, anna); len(list) != 2 || list[0].ID != company || list[1].ID != mine {
		t.Fatalf("List(anna) = %+v", list)
	}
	if list, _ := s.List(ctx, ivan); len(list) != 1 {
		t.Fatalf("List(ivan) = %+v", list)
	}
	// the choice: Anna works through the company's one
	must(s.Choose(ctx, anna, company))
	if c, _, _ := s.For(ctx, anna); c.ID != company {
		t.Fatalf("For(anna) after choice = %s", c.ID)
	}
	if id, _ := s.Chosen(ctx, anna); id != company {
		t.Fatalf("Chosen = %s", id)
	}
	must(s.Choose(ctx, anna, ""))
	if c, _, _ := s.For(ctx, anna); c.ID != mine {
		t.Fatalf("For(anna) after clear = %s", c.ID)
	}

	// a second company connection made default unsets the first; an empty key keeps the one it has
	second, err := s.Save(ctx, anna, Connection{Name: "Второй", Provider: "compatible", BaseURL: prov.URL, Default: true}, "")
	must(err)
	if c, _, _ := s.For(ctx, ivan); c.ID != second {
		t.Fatalf("For(ivan) = %s, want second", c.ID)
	}
	_, err = s.Save(ctx, anna, Connection{ID: company, Name: "Компания 2", Provider: "openai", BaseURL: prov.URL, Default: true}, "")
	must(err)
	c, key, _ = s.For(ctx, ivan)
	if c.ID != company || key != "good-key" || c.Name != "Компания 2" {
		t.Fatalf("after change = %+v, %q", c, key)
	}
	// a company connection is not changed as someone's own
	if _, err := s.Save(ctx, anna, Connection{ID: company, Owner: anna, Name: "x", Provider: "openai"}, ""); err != ErrNotFound {
		t.Fatalf("changed as own: %v", err)
	}

	// the check: the models of the provider, a wrong key is noted
	models, err := s.Check(ctx, anna, company)
	if err != nil || len(models) != 2 || models[0] != "model-a" || auth != "Bearer good-key" {
		t.Fatalf("Check = %v, %v (auth %q)", models, err, auth)
	}
	if _, err := s.Check(ctx, anna, mine); err != nil || auth != "ant-key-9876" {
		t.Fatalf("Check anthropic = %v (auth %q)", err, auth)
	}
	if _, err := s.Check(ctx, anna, second); errKey(err) != "err.ai_check" {
		t.Fatalf("Check without key = %v", err)
	}
	if c, _ := s.Get(ctx, anna, second); c.CheckedAt == nil || c.CheckError == "" {
		t.Fatalf("check not noted: %+v", c)
	}
	if c, _ := s.Get(ctx, anna, company); c.CheckedAt == nil || c.CheckError != "" {
		t.Fatalf("good check not noted: %+v", c)
	}

	// deleting the default one makes the oldest left the default
	must(s.Delete(ctx, "", company))
	if c, _ := s.Get(ctx, anna, second); !c.Default {
		t.Fatal("the one left is not the default")
	}
	if err := s.Delete(ctx, "", mine); err != ErrNotFound {
		t.Fatalf("deleted Anna's as the company's: %v", err)
	}
}
