package telephony

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestOutLines: an employee calls out by the line they chose while they may call by it, else by the first they may;
// one forbidden to call out has none.
func TestOutLines(t *testing.T) {
	a := Trunk{ID: "11111111-2222-3333-4444-555555555555", Name: "A", Number: "+74950000001", OutAll: true}
	b := Trunk{ID: "22222222-2222-3333-4444-555555555555", Name: "B", Number: "+74950000002", Out: []string{"u1"}}
	nums := map[string]string{"u1": "101", "u2": "102", "u3": "103"}
	out := OutLines([]Trunk{a, b}, nums, map[string]string{"u3": "none"},
		map[string]Prefs{"u1": {OutTrunk: b.ID}, "u2": {OutTrunk: b.ID}})
	if out["101"].ID != b.ID || out["102"].ID != a.ID || out["103"].ID != "" {
		t.Fatalf("%+v", out)
	}
}

// TestRenderForwards: go24-fw has an employee who forwards (with a mobile and an inner number), how, where, after
// how long and their own line out; those who do not forward are not there.
func TestRenderForwards(t *testing.T) {
	a := Trunk{ID: "11111111-2222-3333-4444-555555555555", Name: "A", Number: "+74950000001", OutAll: true}
	prefs := map[string]Prefs{
		"u1": {Forward: ForwardNoAnswer, After: 15, To: "+79001112233"},
		"u2": {Forward: ForwardAlways, To: "+79004445566"},
		"u3": {Forward: ForwardNever, To: "+79007778899"},
		"u4": {Forward: ForwardOffline, To: "+79000000000"}, // no inner number
	}
	plan := RenderForwards(prefs, map[string]string{"u1": "101", "u2": "102", "u3": "103"}, map[string]Trunk{"101": a})
	want := "\n[go24-fw]\n" +
		"exten => 101,1,Set(GO24_FWM=noanswer)\n same => n,Set(GO24_FWTO=+79001112233)\n same => n,Set(GO24_FWS=15)\n" +
		" same => n,Set(GO24_FWOT=" + a.Key() + ")\n same => n,Set(GO24_FWON=+74950000001)\n same => n,Return()\n" +
		"exten => 102,1,Set(GO24_FWM=always)\n same => n,Set(GO24_FWTO=+79004445566)\n same => n,Set(GO24_FWS=0)\n" +
		" same => n,Set(GO24_FWOT=)\n same => n,Set(GO24_FWON=)\n same => n,Return()\n"
	if plan != want {
		t.Fatalf("got\n%s\nwant\n%s", plan, want)
	}
	// RenderTrunks writes them after go24-out
	_, d := RenderTrunks([]Trunk{a}, map[string]string{"u1": "101"}, nil, nil, prefs, false, "")
	if !strings.Contains(d, "[go24-fw]\nexten => 101,1,Set(GO24_FWM=noanswer)") || strings.Index(d, "[go24-fw]") < strings.Index(d, "[go24-out]") {
		t.Fatalf("the dialplan:\n%s", d)
	}
}

// TestPrefs: defaults while the employee has none; the mobile is made dialable, an inner or empty one with
// forwarding is refused; the seconds are kept within bounds; a line the employee may not call by is refused.
func TestPrefs(t *testing.T) {
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
	var ann, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ann', 'ann') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('bob', 'bob') RETURNING id`).Scan(&bob))
	s := NewStore(pool, "secret")
	if p, err := s.PrefsOf(ctx, ann); err != nil || p != (Prefs{After: 20}) {
		t.Fatalf("defaults: %+v %v", p, err)
	}
	var ie *i18n.Error
	for _, bad := range []Prefs{{Forward: ForwardAlways}, {Forward: ForwardOffline, To: "105"}, {To: "abc"}} {
		if err := s.SavePrefs(ctx, ann, bad); !errors.As(err, &ie) || ie.Key != "err.tel_mobile" {
			t.Errorf("%+v saved: %v", bad, err)
		}
	}
	line, err := s.SaveTrunk(ctx, ann, Trunk{Name: "Линия", Host: "sip.example.ru", Username: "go24", Password: "p", Number: "+74950000001", Out: []string{bob}})
	must(err)
	if err := s.SavePrefs(ctx, ann, Prefs{OutTrunk: line}); !errors.As(err, &ie) || ie.Key != "err.tel_out_line" {
		t.Fatalf("a line ann may not call by: %v", err)
	}
	must(s.SavePrefs(ctx, bob, Prefs{OutTrunk: line, Forward: ForwardNoAnswer, After: 200, To: "8 (900) 111-22-33"}))
	must(s.SavePrefs(ctx, ann, Prefs{Forward: "bogus", After: 1}))
	all, err := s.AllPrefs(ctx)
	must(err)
	if p := all[bob]; p.OutTrunk != line || p.Forward != ForwardNoAnswer || p.After != 60 || p.To != Dialable("8 (900) 111-22-33") {
		t.Fatalf("bob: %+v", p)
	}
	if p := all[ann]; p.Forward != ForwardNever || p.After != 5 || p.To != "" {
		t.Fatalf("ann: %+v", p)
	}
}
