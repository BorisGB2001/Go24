package mail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	netmail "net/mail"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Опрос о времени встречи», as Outlook's Scheduling Poll: the organizer offers a few times of a meeting in a letter
// (a block with the link «Проголосовать»), its people — go24's employees and people from outside, without signing
// in — answer «Да», «Возможно» or «Нет» for each time; the organizer is told of every vote, sees them all on the
// poll's page and makes the meeting at the time chosen (with «Назначать автоматически» the meeting is made by itself
// as soon as everyone answered and said «Да» to one time). The meeting itself is made by the web layer (calendar and
// invitations by mail).

// Answers of a vote.
const (
	PollYes   = "yes"
	PollMaybe = "maybe"
	PollNo    = "no"
)

// Statuses of a poll.
const (
	PollOpen      = "open"
	PollScheduled = "scheduled"
	PollCancelled = "cancelled"
)

// PollLengths are the meeting lengths the poll offers, minutes.
var PollLengths = []int{15, 30, 45, 60, 90, 120, 180, 240}

// Limits of a poll.
const (
	PollMaxOptions = 20
	PollMaxVoters  = 100
)

var (
	errPollTitle   = i18n.E("err.mail_poll_title")
	errPollOptions = i18n.E("err.mail_poll_options")
	errPollVoters  = i18n.E("err.mail_poll_voters")
	errPollLength  = i18n.E("err.mail_poll_length")
	// ErrPollClosed: the poll is scheduled or cancelled, votes and a second meeting are not taken.
	ErrPollClosed = i18n.E("err.mail_poll_closed")
	errPollVoter  = i18n.E("err.mail_poll_voter")
)

// PollOption is one time of the poll and how many answered what.
type PollOption struct {
	ID             string
	Start, End     time.Time
	Yes, Maybe, No int
	Busy           []string // the employees busy then (filled by the web layer)
	When           string   // how the time is shown (filled by the web layer)
	Best           bool     // the most «Да» (then «Возможно») of the poll's options, with any answers
}

// PollVoter is one of the people asked, and what they answered (option ID → answer; nil — not voted yet).
type PollVoter struct {
	ID, Email, Name, UserID string
	Voted                   *time.Time
	Answers                 map[string]string
}

// Label is how the voter is shown: the name, else the address.
func (v PollVoter) Label() string {
	if v.Name != "" {
		return v.Name
	}
	return v.Email
}

// Poll is a poll with its times and people.
type Poll struct {
	ID, OwnerID, OwnerName string
	MailboxID              string
	Title, Where, Note     string
	Length                 int // minutes
	Video, Auto            bool
	Token                  string
	Status                 string
	ChosenID, EventID      string
	CreatedAt              time.Time
	ClosedAt               *time.Time
	Options                []PollOption
	Voters                 []PollVoter
	Votes                  int // how many of the people answered
}

// Open: votes are taken.
func (p Poll) Open() bool { return p.Status == PollOpen }

// Option is the poll's option by its ID.
func (p Poll) Option(id string) (PollOption, bool) {
	for _, o := range p.Options {
		if o.ID == id {
			return o, true
		}
	}
	return PollOption{}, false
}

// Chosen is the option the meeting was made at (nil — none).
func (p Poll) Chosen() *PollOption {
	if o, ok := p.Option(p.ChosenID); ok {
		return &o
	}
	return nil
}

// Voter is the poll's person by ID.
func (p Poll) Voter(id string) (PollVoter, bool) {
	for _, v := range p.Voters {
		if v.ID == id {
			return v, true
		}
	}
	return PollVoter{}, false
}

// Agreed is the time of «Назначать автоматически»: when everyone asked has answered, the earliest option all of them
// said «Да» to (ok false — none, or not everyone has answered yet). Options already past are not taken.
func (p Poll) Agreed(now time.Time) (PollOption, bool) {
	if len(p.Voters) == 0 {
		return PollOption{}, false
	}
	for _, v := range p.Voters {
		if v.Voted == nil {
			return PollOption{}, false
		}
	}
	for _, o := range p.Options { // in the order of their starts
		if o.Yes == len(p.Voters) && o.Start.After(now) {
			return o, true
		}
	}
	return PollOption{}, false
}

// NewPoll is what the organizer gives: the times (starts) and the people asked (addresses as in a letter's To).
type NewPoll struct {
	MailboxID          string
	Title, Where, Note string
	Length             int
	Video, Auto        bool
	Starts             []time.Time
	People             []string
}

func pollToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// pollStarts are the times of a new poll: in the future, without repeats, in order.
func pollStarts(list []time.Time, now time.Time) ([]time.Time, error) {
	var out []time.Time
	for _, t := range list {
		t = t.Truncate(time.Minute)
		if !t.After(now) {
			return nil, errPollOptions
		}
		if !slices.ContainsFunc(out, t.Equal) {
			out = append(out, t)
		}
	}
	if len(out) == 0 || len(out) > PollMaxOptions {
		return nil, errPollOptions
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Before(out[b]) })
	return out, nil
}

// pollPeople are the people of a new poll by address (the organizer's own addresses left out), with their names.
func pollPeople(list []string, own []string) ([]*netmail.Address, error) {
	var out []*netmail.Address
	seen := map[string]bool{}
	for _, a := range own {
		seen[strings.ToLower(a)] = true
	}
	for _, s := range list {
		for _, part := range splitList(s) {
			a, err := netmail.ParseAddress(part)
			if err != nil {
				return nil, errPollVoters
			}
			a.Address = strings.ToLower(a.Address)
			if seen[a.Address] {
				continue
			}
			seen[a.Address] = true
			out = append(out, a)
		}
	}
	if len(out) == 0 || len(out) > PollMaxVoters {
		return nil, errPollVoters
	}
	return out, nil
}

// splitList splits addresses by commas and semicolons outside quotes.
func splitList(s string) []string {
	var out []string
	quoted, from := false, 0
	for i, c := range s {
		switch {
		case c == '"':
			quoted = !quoted
		case (c == ',' || c == ';') && !quoted:
			out = append(out, s[from:i])
			from = i + 1
		}
	}
	out = append(out, s[from:])
	var list []string
	for _, p := range out {
		if p = strings.TrimSpace(p); p != "" {
			list = append(list, p)
		}
	}
	return list
}

// CreatePoll makes the user's poll; the mailbox ("" — the user's first) is the one the invitations go from.
func (s *Store) CreatePoll(ctx context.Context, userID string, in NewPoll, now time.Time) (Poll, error) {
	in.Title, in.Where, in.Note = strings.TrimSpace(in.Title), strings.TrimSpace(in.Where), strings.TrimSpace(in.Note)
	if in.Title == "" || len([]rune(in.Title)) > 500 {
		return Poll{}, errPollTitle
	}
	if !slices.Contains(PollLengths, in.Length) {
		return Poll{}, errPollLength
	}
	starts, err := pollStarts(in.Starts, now)
	if err != nil {
		return Poll{}, err
	}
	boxes, err := s.Mailboxes(ctx, userID)
	if err != nil {
		return Poll{}, err
	}
	var own []string
	box := ""
	for _, b := range boxes {
		own = append(own, b.Email)
		if b.ID == in.MailboxID || (in.MailboxID == "" && box == "") {
			box = b.ID
		}
	}
	if in.MailboxID != "" && box != in.MailboxID {
		return Poll{}, ErrNotFound
	}
	var me string
	if err := s.db.QueryRow(ctx, `SELECT email FROM users WHERE id::text = $1`, userID).Scan(&me); err != nil {
		return Poll{}, err
	}
	people, err := pollPeople(in.People, append(own, me))
	if err != nil {
		return Poll{}, err
	}
	var boxArg any
	if box != "" {
		boxArg = box
	}
	id := ""
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO mail_polls (owner_id, mailbox_id, title, location, note, length_min, video, auto, token)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id::text`,
			userID, boxArg, in.Title, in.Where, in.Note, in.Length, in.Video, in.Auto, pollToken()).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mail_poll_options (poll_id, starts_at) SELECT $1, unnest($2::timestamptz[])`, id, starts); err != nil {
			return err
		}
		for _, a := range people {
			// an employee of go24 with this address answers as themself, and is invited to the meeting in go24
			if _, err := tx.Exec(ctx, `INSERT INTO mail_poll_voters (poll_id, email, name, user_id)
				VALUES ($1, $2, coalesce(nullif($3, ''), (SELECT name FROM users WHERE lower(email) = $2 AND active LIMIT 1), ''),
					(SELECT id FROM users WHERE lower(email) = $2 AND active LIMIT 1))`, id, a.Address, strings.TrimSpace(a.Name)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Poll{}, err
	}
	return s.Poll(ctx, userID, id)
}

const pollSQL = `SELECT p.id::text, p.owner_id::text, u.name, coalesce(p.mailbox_id::text, ''), p.title, p.location, p.note, p.length_min,
		p.video, p.auto, p.token, p.status, coalesce(p.chosen_id::text, ''), coalesce(p.event_id::text, ''), p.created_at, p.closed_at
	FROM mail_polls p JOIN users u ON u.id = p.owner_id`

func scanPoll(r pgx.Row) (Poll, error) {
	var p Poll
	err := r.Scan(&p.ID, &p.OwnerID, &p.OwnerName, &p.MailboxID, &p.Title, &p.Where, &p.Note, &p.Length,
		&p.Video, &p.Auto, &p.Token, &p.Status, &p.ChosenID, &p.EventID, &p.CreatedAt, &p.ClosedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return p, err
}

// fill reads the poll's times, people and answers.
func (s *Store) fillPoll(ctx context.Context, p *Poll) error {
	rows, err := s.db.Query(ctx, `SELECT id::text, starts_at FROM mail_poll_options WHERE poll_id = $1 ORDER BY starts_at`, p.ID)
	if err != nil {
		return err
	}
	at := map[string]int{}
	for rows.Next() {
		var o PollOption
		if err := rows.Scan(&o.ID, &o.Start); err != nil {
			rows.Close()
			return err
		}
		o.End = o.Start.Add(time.Duration(p.Length) * time.Minute)
		at[o.ID] = len(p.Options)
		p.Options = append(p.Options, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.Query(ctx, `SELECT id::text, email, name, coalesce(user_id::text, ''), voted_at FROM mail_poll_voters
		WHERE poll_id = $1 ORDER BY lower(coalesce(nullif(name, ''), email))`, p.ID)
	if err != nil {
		return err
	}
	who := map[string]int{}
	for rows.Next() {
		var v PollVoter
		if err := rows.Scan(&v.ID, &v.Email, &v.Name, &v.UserID, &v.Voted); err != nil {
			rows.Close()
			return err
		}
		if v.Voted != nil {
			p.Votes++
			v.Answers = map[string]string{}
		}
		who[v.ID] = len(p.Voters)
		p.Voters = append(p.Voters, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = s.db.Query(ctx, `SELECT x.voter_id::text, x.option_id::text, x.answer FROM mail_poll_votes x
		JOIN mail_poll_voters v ON v.id = x.voter_id WHERE v.poll_id = $1 AND v.voted_at IS NOT NULL`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v, o, a string
		if err := rows.Scan(&v, &o, &a); err != nil {
			return err
		}
		i, ok := at[o]
		j, ok2 := who[v]
		if !ok || !ok2 {
			continue
		}
		p.Voters[j].Answers[o] = a
		switch a {
		case PollYes:
			p.Options[i].Yes++
		case PollMaybe:
			p.Options[i].Maybe++
		default:
			p.Options[i].No++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	markBest(p.Options)
	return nil
}

// markBest marks the options with the most «Да», then «Возможно» (none when nobody said either).
func markBest(list []PollOption) {
	by, bm := 0, 0
	for _, o := range list {
		if o.Yes > by || o.Yes == by && o.Maybe > bm {
			by, bm = o.Yes, o.Maybe
		}
	}
	for i := range list {
		list[i].Best = (by > 0 || bm > 0) && list[i].Yes == by && list[i].Maybe == bm
	}
}

// Poll is the user's poll.
func (s *Store) Poll(ctx context.Context, userID, id string) (Poll, error) {
	p, err := scanPoll(s.db.QueryRow(ctx, pollSQL+` WHERE p.id::text = $1 AND p.owner_id::text = $2`, id, userID))
	if err != nil {
		return p, err
	}
	return p, s.fillPoll(ctx, &p)
}

// PollByToken is the poll of a link.
func (s *Store) PollByToken(ctx context.Context, tok string) (Poll, error) {
	if tok == "" {
		return Poll{}, ErrNotFound
	}
	p, err := scanPoll(s.db.QueryRow(ctx, pollSQL+` WHERE p.token = $1`, tok))
	if err != nil {
		return p, err
	}
	return p, s.fillPoll(ctx, &p)
}

// Polls are the user's polls, the newest first (with their times and answers).
func (s *Store) Polls(ctx context.Context, userID string) ([]Poll, error) {
	rows, err := s.db.Query(ctx, pollSQL+` WHERE p.owner_id::text = $1 ORDER BY p.created_at DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	var list []Poll
	for rows.Next() {
		p, err := scanPoll(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		list = append(list, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range list {
		if err := s.fillPoll(ctx, &list[i]); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// PollVote keeps the answers of one of the poll's people (option ID → yes | maybe | no; an option left out is «Нет»),
// replacing what they answered before; the organizer is told. The poll must be open.
func (s *Store) PollVote(ctx context.Context, tok, voterID string, answers map[string]string) (Poll, error) {
	p, err := s.PollByToken(ctx, tok)
	if err != nil {
		return p, err
	}
	if !p.Open() {
		return p, ErrPollClosed
	}
	v, ok := p.Voter(voterID)
	if !ok {
		return p, errPollVoter
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE mail_poll_voters SET voted_at = now() WHERE id = $1
			AND (SELECT status FROM mail_polls WHERE id = poll_id) = 'open'`, v.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrPollClosed
		}
		for _, o := range p.Options {
			a := answers[o.ID]
			if a != PollYes && a != PollMaybe {
				a = PollNo
			}
			if _, err := tx.Exec(ctx, `INSERT INTO mail_poll_votes (voter_id, option_id, answer) VALUES ($1, $2, $3)
				ON CONFLICT (voter_id, option_id) DO UPDATE SET answer = excluded.answer`, v.ID, o.ID, a); err != nil {
				return err
			}
		}
		var actor any
		if v.UserID != "" {
			actor = v.UserID
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'mail_poll', left($3, 300), $4)`,
			p.OwnerID, actor, v.Label()+" · "+p.Title, "/mail/polls/"+p.ID)
		return err
	})
	if err != nil {
		return p, err
	}
	return s.PollByToken(ctx, tok)
}

// ClaimPoll takes the open poll for its meeting at the option (so it is made once): false — the poll is not open
// any more. ReleasePoll gives it back when the meeting could not be made; SetPollEvent keeps the meeting made.
func (s *Store) ClaimPoll(ctx context.Context, pollID, optionID string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE mail_polls SET status = 'scheduled', chosen_id = $2::uuid, closed_at = now()
		WHERE id::text = $1 AND status = 'open' AND EXISTS (SELECT 1 FROM mail_poll_options o WHERE o.id::text = $2 AND o.poll_id = mail_polls.id)`,
		pollID, optionID)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Store) ReleasePoll(ctx context.Context, pollID string) error {
	_, err := s.db.Exec(ctx, `UPDATE mail_polls SET status = 'open', chosen_id = NULL, closed_at = NULL WHERE id::text = $1 AND event_id IS NULL`, pollID)
	return err
}

func (s *Store) SetPollEvent(ctx context.Context, pollID, eventID string) error {
	_, err := s.db.Exec(ctx, `UPDATE mail_polls SET event_id = $2::uuid WHERE id::text = $1`, pollID, eventID)
	return err
}

// NotifyPollScheduled tells the organizer the meeting of the poll was made by itself (everyone agreed).
func (s *Store) NotifyPollScheduled(ctx context.Context, p Poll, when string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'mail_poll_done', left($2, 300), $3)`,
		p.OwnerID, p.Title+" · "+when, "/mail/polls/"+p.ID)
	return err
}

// CancelPoll closes the user's open poll without a meeting.
func (s *Store) CancelPoll(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_polls SET status = 'cancelled', closed_at = now() WHERE id::text = $1 AND owner_id::text = $2 AND status = 'open'`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		if _, err = s.Poll(ctx, userID, id); err == nil {
			err = ErrPollClosed
		}
	}
	return err
}

// DeletePoll forgets the user's poll (a meeting made stays in the calendar).
func (s *Store) DeletePoll(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_polls WHERE id::text = $1 AND owner_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}
