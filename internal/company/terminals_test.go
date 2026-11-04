package company

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

func TestValidPIN(t *testing.T) {
	for pin, want := range map[string]bool{"1234": true, "12345678": true, "123": false, "123456789": false, "12a4": false,
		"": false, " 1234": false, "١٢٣٤": false} {
		if ValidPIN(pin) != want {
			t.Errorf("ValidPIN(%q) = %v", pin, !want)
		}
	}
}

// TestTerminals: a terminal by its link (switched off or with a new link — none), PINs unique and kept as an HMAC, a
// PIN finds its active employee, wrong PINs lock the terminal for a minute after 5 in a row, the right one resets the
// count; a day marked at the terminal remembers it.
func TestTerminals(t *testing.T) {
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
	errKey := func(err error, key string) {
		t.Helper()
		if ie, ok := err.(*i18n.Error); !ok || ie.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	anna, ivan, gone := user("anna", "Анна"), user("ivan", "Иван"), user("gone", "Уволен")
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, gone)
	must(err)
	s := NewStore(pool)
	s.Key = []byte("secret")

	_, err = s.AddTerminal(ctx, "  ")
	errKey(err, "err.wterm_name")
	term, err := s.AddTerminal(ctx, " Вход ")
	must(err)
	if term.Name != "Вход" || !term.Active || len(term.Token) < 20 {
		t.Fatalf("terminal %+v", term)
	}
	if got, err := s.TerminalByToken(ctx, term.Token); err != nil || got == nil || got.ID != term.ID {
		t.Fatalf("by token: %v %v", got, err)
	}
	if got, _ := s.TerminalByToken(ctx, ""); got != nil {
		t.Fatal("empty token found a terminal")
	}

	errKey(s.SetWorkPIN(ctx, anna, "12"), "err.wpin_format")
	must(s.SetWorkPIN(ctx, anna, "2468"))
	errKey(s.SetWorkPIN(ctx, ivan, "2468"), "err.wpin_taken")
	must(s.SetWorkPIN(ctx, ivan, "1357"))
	must(s.SetWorkPIN(ctx, gone, "9999"))
	var kept string
	must(pool.QueryRow(ctx, `SELECT pin_hash FROM worktime_pins WHERE user_id = $1`, anna).Scan(&kept))
	if kept == "2468" || len(kept) != 64 {
		t.Fatalf("pin kept as %q", kept)
	}
	if ok, _ := s.HasWorkPIN(ctx, anna); !ok {
		t.Fatal("anna has no PIN")
	}
	if pins, _ := s.WorkPINs(ctx); !pins[anna] || !pins[ivan] || len(pins) != 3 {
		t.Fatalf("pins %v", pins)
	}

	now := time.Now()
	p, err := s.TerminalPerson(ctx, term, "2468", now)
	must(err)
	if p.ID != anna || p.Name != "Анна" {
		t.Fatalf("person %+v", p)
	}
	_, err = s.TerminalPerson(ctx, term, "9999", now) // dismissed
	errKey(err, "err.wterm_pin")
	for range 3 {
		_, err = s.TerminalPerson(ctx, term, "0000", now)
		errKey(err, "err.wterm_pin")
	}
	_, err = s.TerminalPerson(ctx, term, "abc", now) // the fifth wrong in a row locks
	errKey(err, "err.wterm_pin")
	_, err = s.TerminalPerson(ctx, term, "2468", now.Add(30*time.Second))
	errKey(err, "err.wterm_locked")
	_, err = s.TerminalPerson(ctx, term, "2468", now.Add(61*time.Second))
	must(err)
	_, err = s.TerminalPerson(ctx, term, "0000", now.Add(62*time.Second)) // the count started anew
	errKey(err, "err.wterm_pin")
	got, _ := s.Terminal(ctx, term.ID)
	if got.Fails != 1 || got.UsedAt == nil {
		t.Fatalf("after: %+v", got)
	}

	// a day started and finished at the terminal
	at := Place{IP: "10.0.0.5", Terminal: term.Name}
	must(s.WorkAction(ctx, anna, "start", "", now, at))
	must(s.WorkAction(ctx, anna, "finish", "", now.Add(time.Minute), at))
	d, err := s.Today(ctx, anna, now)
	must(err)
	if d.Start.Terminal != "Вход" || d.End.Terminal != "Вход" || d.Start.IP != "10.0.0.5" {
		t.Fatalf("day %+v", d)
	}
	must(s.WorkAction(ctx, anna, "reopen", "", now.Add(2*time.Minute), Place{}))
	if d, _ = s.Today(ctx, anna, now); d.End.Terminal != "" {
		t.Fatalf("reopened day keeps the end terminal: %+v", d.End)
	}

	// switched off, a new link, deleted
	must(s.UpdateTerminal(ctx, term.ID, "Вход", false))
	if got, _ := s.TerminalByToken(ctx, term.Token); got != nil {
		t.Fatal("switched-off terminal works")
	}
	must(s.UpdateTerminal(ctx, term.ID, "Вход, 1 этаж", true))
	must(s.NewTerminalLink(ctx, term.ID))
	if got, _ := s.TerminalByToken(ctx, term.Token); got != nil {
		t.Fatal("the old link works")
	}
	if on, _ := s.TerminalsOn(ctx); !on {
		t.Fatal("no terminal on")
	}
	must(s.DeleteTerminal(ctx, term.ID))
	errKey(s.DeleteTerminal(ctx, term.ID), "err.not_found")
	if on, _ := s.TerminalsOn(ctx); on {
		t.Fatal("a terminal on after delete")
	}
	must(s.SetWorkPIN(ctx, anna, ""))
	if ok, _ := s.HasWorkPIN(ctx, anna); ok {
		t.Fatal("PIN not removed")
	}
}
