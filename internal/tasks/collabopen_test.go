package tasks

import (
	"context"
	"errors"
	"testing"
)

// TestOpenCollab: an open collab — an employee finds it in the search and by its link and joins it
// themselves; a closed one shows nothing; a guest, an archived one and a member already in it — no «Вступить».
func TestOpenCollab(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@test', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, emp, guest := newUser("owner", false), newUser("emp", false), newUser("guest", true)
	key, err := s.CreateCollab(ctx, owner, "Выставка", "Стенд на выставке", nil)
	must(err)
	c, err := s.CollabByKey(ctx, owner, key)
	must(err)

	// closed (the default): neither the search nor the link shows it
	if l, err := s.OpenCollabs(ctx, emp, "выставка"); err != nil || len(l) != 0 {
		t.Fatalf("a closed collab found: %+v %v", l, err)
	}
	if _, err := s.JoinCollab(ctx, emp, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("joined a closed collab: %v", err)
	}

	set, err := s.CollabSettings(ctx, c)
	must(err)
	set.Open = true
	must(s.SetCollabSettings(ctx, c, owner, set))
	if set, _ = s.CollabSettings(ctx, c); !set.Open {
		t.Fatal("the collab is not open")
	}
	if l, err := s.OpenCollabs(ctx, emp, ""); err != nil || len(l) != 0 {
		t.Fatalf("open collabs without a search: %+v %v", l, err)
	}
	l, err := s.OpenCollabs(ctx, emp, "стенд")
	must(err)
	if len(l) != 1 || l[0].Key != key || l[0].Members != 1 || l[0].MyRole != "" {
		t.Fatalf("found open collabs: %+v", l)
	}
	if mine, _ := s.ListCollabs(ctx, emp, false, "стенд"); len(mine) != 0 {
		t.Fatalf("an open collab among one's own: %+v", mine)
	}
	gctx := WithViewer(ctx, guest, true)
	if l, _ := s.OpenCollabs(gctx, guest, "стенд"); len(l) != 0 {
		t.Fatalf("a guest found an open collab: %+v", l)
	}
	if _, err := s.JoinCollab(gctx, guest, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a guest joined: %v", err)
	}

	// emp joins: a member, the log tells it, the collab is no more «to join» for them
	if _, err := s.OpenCollab(ctx, emp, key); err != nil {
		t.Fatal(err)
	}
	p, err := s.JoinCollab(ctx, emp, key)
	must(err)
	if p.MyRole != "member" {
		t.Fatalf("emp's role: %+v", p)
	}
	if _, err := s.OpenCollab(ctx, emp, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a member asked to join again: %v", err)
	}
	if mine, _ := s.ListCollabs(ctx, emp, false, ""); len(mine) != 1 {
		t.Fatalf("emp's collabs: %+v", mine)
	}
	log, err := s.CollabLog(ctx, c, 10)
	must(err)
	if len(log) == 0 || log[0].Action != "member_joined" || log[0].Actor != "emp" {
		t.Fatalf("the log: %+v", log)
	}

	// archived: nobody joins it
	other := newUser("other", false)
	must(s.SetArchived(ctx, c, true))
	if _, err := s.JoinCollab(ctx, other, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("joined an archived collab: %v", err)
	}
}
