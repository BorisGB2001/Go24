package calls

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestHistory: «Звонки» of each person — the calls they started or joined, with how long they went on; the rings
// they missed or declined, unless they came to that call later; an outgoing call nobody came to; the count of missed
// calls not looked at yet, gone once the history is looked at.
func TestHistory(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	ann, bob, eve, kim := user("ann"), user("bob"), user("eve"), user("kim")
	rs, ss := &Rings{DB: pool}, &Sessions{DB: pool}

	// ann calls the group: bob comes, eve declines, kim misses it but joins the call a bit later
	r, err := rs.Start(ctx, ann, "!g:x", true, []string{bob, eve, kim})
	must(err)
	cs, _, err := ss.Join(ctx, "lk-g", "!g:x", ann, true)
	must(err)
	_, err = rs.Answer(ctx, bob, r.ID, "accepted")
	must(err)
	_, _, err = ss.Join(ctx, "lk-g", "!g:x", bob, true)
	must(err)
	_, err = rs.Answer(ctx, eve, r.ID, "declined")
	must(err)
	_, err = rs.End(ctx, r.ID)
	must(err)
	_, _, err = ss.Join(ctx, "lk-g", "!g:x", kim, true)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE call_sessions SET started_at = now() - interval '10 minutes' WHERE id = $1`, cs.ID)
	must(err)
	if list, _ := rs.History(ctx, ann, false, 50); len(list) != 1 || list[0].Kind != "out" || !list[0].Live || list[0].Secs != 0 {
		t.Fatalf("ann's history while the call goes on: %+v", list)
	}
	_, _, err = ss.Finish(ctx, cs.ID)
	must(err)

	// bob calls eve in private: nobody answers
	r2, err := rs.Start(ctx, bob, "!d:x", false, []string{eve})
	must(err)
	cs2, _, err := ss.Join(ctx, "lk-d", "!d:x", bob, false)
	must(err)
	_, err = rs.End(ctx, r2.ID)
	must(err)
	_, _, err = ss.Finish(ctx, cs2.ID)
	must(err)

	ah, _ := rs.History(ctx, ann, false, 50)
	if len(ah) != 1 || ah[0].Kind != "out" || !ah[0].Video || ah[0].Live || ah[0].Secs < 590 || len(ah[0].People) != 3 || ah[0].Chat != "!g:x" {
		t.Fatalf("ann's history: %+v", ah)
	}
	bh, _ := rs.History(ctx, bob, false, 50)
	if len(bh) != 2 || bh[0].Kind != "out" || !bh[0].Alone() || bh[0].Secs != 0 || bh[1].Kind != "in" || bh[1].By != ann {
		t.Fatalf("bob's history: %+v", bh)
	}
	eh, _ := rs.History(ctx, eve, false, 50)
	if len(eh) != 2 || eh[0].Kind != "missed" || eh[0].By != bob || eh[0].Chat != "!d:x" || eh[1].Kind != "declined" || eh[1].By != ann {
		t.Fatalf("eve's history: %+v", eh)
	}
	if kh, _ := rs.History(ctx, kim, false, 50); len(kh) != 1 || kh[0].Kind != "in" {
		t.Fatalf("kim missed the ring but came to the call: %+v", kh)
	}
	if em, _ := rs.History(ctx, eve, true, 50); len(em) != 2 {
		t.Fatalf("eve's missed: %+v", em)
	}
	if bm, _ := rs.History(ctx, bob, true, 50); len(bm) != 0 {
		t.Fatalf("bob missed nothing: %+v", bm)
	}
	if n, err := rs.Unseen(ctx, eve); err != nil || n != 2 {
		t.Fatalf("eve's unseen: %d %v", n, err)
	}
	if n, _ := rs.Unseen(ctx, kim); n != 0 {
		t.Fatalf("kim's unseen: %d", n)
	}
	must(rs.SeeAll(ctx, eve))
	if n, _ := rs.Unseen(ctx, eve); n != 0 {
		t.Fatalf("eve's unseen after looking: %d", n)
	}
	if eh, _ := rs.History(ctx, eve, false, 50); len(eh) != 2 {
		t.Fatal("looking at the history removed it")
	}
}
