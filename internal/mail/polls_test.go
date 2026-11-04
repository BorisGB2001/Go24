package mail

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestPollPure(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	h := func(n int) time.Time { return now.Add(time.Duration(n) * time.Hour) }
	got, err := pollStarts([]time.Time{h(5), h(2), h(5).Add(20 * time.Second)}, now)
	if err != nil || len(got) != 2 || !got[0].Equal(h(2)) || !got[1].Equal(h(5)) {
		t.Errorf("pollStarts: %v %v", got, err)
	}
	if _, err := pollStarts([]time.Time{h(-1)}, now); err == nil {
		t.Error("a past time is taken")
	}
	if _, err := pollStarts(nil, now); err == nil {
		t.Error("no times are taken")
	}
	if l := splitList(`"Иванов, Иван" <ivan@x.ru>; anna@x.ru, `); len(l) != 2 || l[0] != `"Иванов, Иван" <ivan@x.ru>` {
		t.Errorf("splitList: %q", l)
	}
	people, err := pollPeople([]string{`Иван <IVAN@x.ru>, me@x.ru`, "ivan@x.ru", "anna@x.ru"}, []string{"ME@x.ru"})
	if err != nil || len(people) != 2 || people[0].Address != "ivan@x.ru" || people[0].Name != "Иван" || people[1].Address != "anna@x.ru" {
		t.Errorf("pollPeople: %v %v", people, err)
	}
	if _, err := pollPeople([]string{"me@x.ru"}, []string{"me@x.ru"}); err == nil {
		t.Error("only the organizer is taken")
	}
	if _, err := pollPeople([]string{"not an address"}, nil); err == nil {
		t.Error("a broken address is taken")
	}

	voted := now
	p := Poll{Options: []PollOption{{ID: "a", Start: h(1), Yes: 1, Maybe: 1}, {ID: "b", Start: h(2), Yes: 2}, {ID: "c", Start: h(3), Yes: 2}},
		Voters: []PollVoter{{ID: "1", Voted: &voted}, {ID: "2"}}}
	if _, ok := p.Agreed(now); ok {
		t.Error("agreed before everyone answered")
	}
	p.Voters[1].Voted = &voted
	if o, ok := p.Agreed(now); !ok || o.ID != "b" {
		t.Errorf("agreed: %v %v", o, ok)
	}
	if o, ok := p.Agreed(h(2)); !ok || o.ID != "c" { // b has started
		t.Errorf("agreed later: %v %v", o, ok)
	}
	markBest(p.Options)
	if p.Options[0].Best || !p.Options[1].Best || !p.Options[2].Best {
		t.Errorf("best: %+v", p.Options)
	}
	none := []PollOption{{No: 2}}
	if markBest(none); none[0].Best {
		t.Error("a time nobody wants is the best")
	}
	if (PollVoter{Email: "a@x.ru"}).Label() != "a@x.ru" || (PollVoter{Email: "a@x.ru", Name: "Анна"}).Label() != "Анна" {
		t.Error("Label")
	}
	if p.Chosen() != nil {
		t.Error("chosen without a choice")
	}
	p.ChosenID = "c"
	if c := p.Chosen(); c == nil || c.ID != "c" {
		t.Error("Chosen")
	}
}

// TestPolls against the test database: a poll of the letter's people (the organizer and repeats left out, an
// employee known by the address), votes replaced and told to the organizer, the poll taken for its meeting once,
// given back when the meeting fails, closed polls take no votes; another's poll is not found.
func TestPolls(t *testing.T) {
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
	var bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@go24.test') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'Eve@go24.test') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, bob, Settings{Email: "boris@work.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	now := time.Now()
	t1, t2 := now.Add(24*time.Hour).Truncate(time.Minute), now.Add(48*time.Hour).Truncate(time.Minute)
	in := NewPoll{Title: " Планёрка ", Length: 30, Auto: true, Starts: []time.Time{t2, t1},
		People: []string{"eve@go24.test, Клиент <client@out.test>", "boris@work.test", "bob@go24.test", "CLIENT@out.test"}}
	if _, err := s.CreatePoll(ctx, bob, NewPoll{Title: "x", Length: 31, Starts: in.Starts, People: in.People}, now); err == nil {
		t.Error("a length not offered is taken")
	}
	if _, err := s.CreatePoll(ctx, bob, NewPoll{Title: "x", Length: 30, Starts: in.Starts, People: in.People, MailboxID: "00000000-0000-0000-0000-000000000000"}, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("another's mailbox: %v", err)
	}
	p, err := s.CreatePoll(ctx, bob, in, now)
	must(err)
	if p.Title != "Планёрка" || p.MailboxID != box || p.Status != PollOpen || len(p.Options) != 2 || !p.Options[0].Start.Equal(t1) ||
		!p.Options[0].End.Equal(t1.Add(30*time.Minute)) || len(p.Voters) != 2 {
		t.Fatalf("poll: %+v", p)
	}
	var ev, cl PollVoter
	for _, v := range p.Voters {
		switch v.Email {
		case "eve@go24.test":
			ev = v
		case "client@out.test":
			cl = v
		}
	}
	if ev.UserID != eve || ev.Name != "Ева" || cl.UserID != "" || cl.Name != "Клиент" {
		t.Fatalf("voters: %+v", p.Voters)
	}
	if _, err := s.Poll(ctx, eve, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another's poll: %v", err)
	}
	if _, err := s.PollVote(ctx, p.Token, "00000000-0000-0000-0000-000000000000", nil); err == nil {
		t.Error("a stranger votes")
	}
	o1, o2 := p.Options[0].ID, p.Options[1].ID
	p, err = s.PollVote(ctx, p.Token, ev.ID, map[string]string{o1: PollYes, o2: "bogus"})
	must(err)
	if p.Votes != 1 || p.Options[0].Yes != 1 || p.Options[1].No != 1 {
		t.Fatalf("after a vote: %+v", p.Options)
	}
	if _, ok := p.Agreed(now); ok {
		t.Error("agreed with one of two")
	}
	p, err = s.PollVote(ctx, p.Token, ev.ID, map[string]string{o1: PollYes, o2: PollMaybe}) // changed
	must(err)
	p, err = s.PollVote(ctx, p.Token, cl.ID, map[string]string{o1: PollYes})
	must(err)
	if p.Votes != 2 || p.Options[0].Yes != 2 || p.Options[1].Maybe != 1 || p.Options[1].No != 1 || !p.Options[0].Best {
		t.Fatalf("after two votes: %+v", p.Options)
	}
	if v, _ := p.Voter(ev.ID); v.Answers[o2] != PollMaybe {
		t.Errorf("answers: %v", v.Answers)
	}
	if o, ok := p.Agreed(now); !ok || o.ID != o1 {
		t.Errorf("agreed: %v %v", o, ok)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'mail_poll' AND url = $2`, bob, "/mail/polls/"+p.ID).Scan(&n))
	if n != 3 {
		t.Errorf("%d notifications", n)
	}
	list, err := s.Polls(ctx, bob)
	must(err)
	if len(list) != 1 || list[0].Votes != 2 {
		t.Errorf("polls: %+v", list)
	}
	// taken once, given back, taken again
	ok, err := s.ClaimPoll(ctx, p.ID, o1)
	must(err)
	if !ok {
		t.Fatal("not claimed")
	}
	if ok, _ := s.ClaimPoll(ctx, p.ID, o2); ok {
		t.Error("claimed twice")
	}
	if _, err := s.PollVote(ctx, p.Token, cl.ID, nil); !errors.Is(err, ErrPollClosed) {
		t.Errorf("a vote in a closed poll: %v", err)
	}
	must(s.ReleasePoll(ctx, p.ID))
	if ok, _ := s.ClaimPoll(ctx, p.ID, o2); !ok {
		t.Error("not claimed after release")
	}
	var ev1 string
	must(pool.QueryRow(ctx, `INSERT INTO events (owner_id, title, starts_at, ends_at) VALUES ($1, 'x', $2, $3) RETURNING id::text`, bob, t2, t2.Add(time.Hour)).Scan(&ev1))
	must(s.SetPollEvent(ctx, p.ID, ev1))
	must(s.ReleasePoll(ctx, p.ID)) // a meeting made: it stays scheduled
	p, err = s.PollByToken(ctx, p.Token)
	must(err)
	if p.Status != PollScheduled || p.EventID != ev1 || p.Chosen() == nil || p.Chosen().ID != o2 || p.ClosedAt == nil {
		t.Errorf("scheduled: %+v", p)
	}
	if err := s.CancelPoll(ctx, bob, p.ID); !errors.Is(err, ErrPollClosed) {
		t.Errorf("cancel a scheduled poll: %v", err)
	}
	must(s.NotifyPollScheduled(ctx, p, "завтра"))
	// another poll: cancelled, then deleted
	q, err := s.CreatePoll(ctx, bob, NewPoll{Title: "Ещё", Length: 60, Starts: []time.Time{t1}, People: []string{"eve@go24.test"}}, now)
	must(err)
	if err := s.CancelPoll(ctx, eve, q.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("cancel another's poll: %v", err)
	}
	must(s.CancelPoll(ctx, bob, q.ID))
	if _, err := s.PollVote(ctx, q.Token, q.Voters[0].ID, nil); !errors.Is(err, ErrPollClosed) {
		t.Errorf("a vote in a cancelled poll: %v", err)
	}
	must(s.DeletePoll(ctx, bob, q.ID))
	if _, err := s.PollByToken(ctx, q.Token); !errors.Is(err, ErrNotFound) {
		t.Errorf("a deleted poll: %v", err)
	}
	if _, err := s.PollByToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Error("an empty token")
	}
}
