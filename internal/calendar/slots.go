package calendar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/worktime"
)

// «Поделиться слотами»: a link anyone opens without signing in to pick a free time of one's calendar
// and so make a meeting with them. Each person has a personal link; a joint one is with colleagues too, and offers
// the time all of them are free.

var (
	ErrSlotTaken   = i18n.E("calslots.err.taken")
	ErrSlotGuest   = i18n.E("calslots.err.guest")
	ErrSlotTooMany = i18n.E("calslots.err.too_many")
)

// SlotLengths are the meeting lengths a link offers, in minutes.
var SlotLengths = []int{15, 30, 45, 60, 90, 120, 180}

// SlotsPerHour: the meetings a guest may make by links from one address within an hour.
const SlotsPerHour = 5

// SlotRule is when the link offers meetings: on these weekdays (1 Monday … 7 Sunday), from and to minutes since
// midnight.
type SlotRule struct {
	Days []int `json:"days"`
	From int   `json:"from"`
	To   int   `json:"to"`
}

// SlotLink is a link to one's free time; Members (colleagues) make it a joint one.
type SlotLink struct {
	ID, Token          string
	OwnerID, OwnerName string
	Members            []string
	MemberNames        []string
	Length             int // of a meeting, minutes
	Rules              []SlotRule
	DaysAhead          int
	Active             bool
	CreatedAt          time.Time
	Meetings           int      // made by the link and not cancelled
	Holidays           []string // the company's, when no meetings are offered (see Offers)
}

// Joint tells a link with colleagues.
func (l SlotLink) Joint() bool { return len(l.Members) > 0 }

// SlotMeeting is a meeting a guest made by a link; it is cancelled when the guest cancelled it or its meeting is gone.
type SlotMeeting struct {
	ID, LinkID, OwnerID, OwnerName string
	EventID, Title                 string
	Token                          string
	GuestName, GuestContact, Note  string
	Start, End                     time.Time
	Video                          bool
	Cancelled                      bool
	People                         []string // the organizer and colleagues of the meeting, by name
}

func slotToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

const slotLinkSQL = `SELECT l.id::text, l.token, l.owner_id::text, u.name, l.members::text[],
		coalesce((SELECT array_agg(m.name ORDER BY i.n) FROM unnest(l.members) WITH ORDINALITY AS i(id, n) JOIN users m ON m.id = i.id), '{}'),
		l.length, l.rules, l.days_ahead, l.active, l.created_at,
		(SELECT count(*) FROM calendar_slot_meetings x WHERE x.link_id = l.id AND NOT x.cancelled AND x.event_id IS NOT NULL)::int
	FROM calendar_slot_links l JOIN users u ON u.id = l.owner_id`

func scanSlotLink(r pgx.Row) (SlotLink, error) {
	var l SlotLink
	var rules []byte
	err := r.Scan(&l.ID, &l.Token, &l.OwnerID, &l.OwnerName, &l.Members, &l.MemberNames, &l.Length, &rules, &l.DaysAhead,
		&l.Active, &l.CreatedAt, &l.Meetings)
	if err == nil {
		err = json.Unmarshal(rules, &l.Rules)
	}
	return l, err
}

// DefaultSlotRules: the working days and hours of one's calendar settings.
func DefaultSlotRules(x Settings) []SlotRule {
	r := SlotRule{From: x.WorkFrom * 60, To: x.WorkTo * 60}
	for d := 1; d <= 7; d++ {
		if !slices.Contains(x.DaysOff, d) {
			r.Days = append(r.Days, d)
		}
	}
	return []SlotRule{r}
}

// SlotLinks are the owner's links: the personal one first (made on the first visit), then the joint ones, newest
// first.
func (s *Store) SlotLinks(ctx context.Context, owner string) ([]SlotLink, error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM calendar_slot_links WHERE owner_id = $1 AND members = '{}'`, owner).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		x, err := s.Settings(ctx, owner)
		if err != nil {
			return nil, err
		}
		rules, _ := json.Marshal(DefaultSlotRules(x))
		if _, err := s.db.Exec(ctx, `INSERT INTO calendar_slot_links (token, owner_id, rules) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			slotToken(), owner, rules); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.Query(ctx, slotLinkSQL+` WHERE l.owner_id = $1 ORDER BY l.members <> '{}', l.created_at DESC`, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SlotLink, error) { return scanSlotLink(r) })
}

// SlotLink is one of the owner's links.
func (s *Store) SlotLink(ctx context.Context, owner, id string) (SlotLink, error) {
	l, err := scanSlotLink(s.db.QueryRow(ctx, slotLinkSQL+` WHERE l.id::text = $1 AND l.owner_id = $2`, id, owner))
	l.Holidays = s.companySchedule(ctx).Holidays
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return l, err
}

// SlotLinkByToken is the link of a public page while it is on and its owner works here.
func (s *Store) SlotLinkByToken(ctx context.Context, tok string) (SlotLink, error) {
	l, err := scanSlotLink(s.db.QueryRow(ctx, slotLinkSQL+` WHERE l.token = $1 AND l.active AND u.active AND NOT u.guest`, tok))
	l.Holidays = s.companySchedule(ctx).Holidays
	if errors.Is(err, pgx.ErrNoRows) || tok == "" {
		err = ErrNotFound
	}
	return l, err
}

// cleanRules checks the rules of a form: known days, a span of at least one meeting within the day.
func cleanRules(list []SlotRule, length int) ([]SlotRule, error) {
	var out []SlotRule
	for _, r := range list {
		days := []int{}
		for _, d := range r.Days {
			if d < 1 || d > 7 {
				return nil, i18n.E("err.bad_form")
			}
			if !slices.Contains(days, d) {
				days = append(days, d)
			}
		}
		if len(days) == 0 {
			continue
		}
		if r.From < 0 || r.To > 24*60 || r.From+length > r.To {
			return nil, i18n.E("calslots.err.hours")
		}
		slices.Sort(days)
		out = append(out, SlotRule{Days: days, From: r.From, To: r.To})
	}
	if len(out) == 0 {
		return nil, i18n.E("calslots.err.rules")
	}
	return out, nil
}

// SaveSlotLink adds a joint link (no ID; members are needed) or changes one of the owner's: the length, the rules,
// the days ahead, on or off, and the members of a joint one. The members are colleagues the owner may know.
func (s *Store) SaveSlotLink(ctx context.Context, owner string, l SlotLink) (string, error) {
	if !slices.Contains(SlotLengths, l.Length) {
		return "", i18n.E("err.bad_form")
	}
	rules, err := cleanRules(l.Rules, l.Length)
	if err != nil {
		return "", err
	}
	raw, _ := json.Marshal(rules)
	l.DaysAhead = max(1, min(l.DaysAhead, 365))
	var members []string
	for _, m := range l.Members {
		if m != "" && m != owner && !slices.Contains(members, m) {
			members = append(members, m)
		}
	}
	if len(members) > 0 {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users u WHERE u.id::text = ANY($2::text[]) AND u.active AND NOT u.guest
			AND `+tasks.PeerSQL("$1", "u"), owner, members).Scan(&n); err != nil {
			return "", err
		}
		if n != len(members) {
			return "", i18n.E("err.bad_form")
		}
	}
	if l.ID == "" {
		if len(members) == 0 { // the personal link is one, made by SlotLinks
			return "", i18n.E("calslots.err.members")
		}
		err := s.db.QueryRow(ctx, `INSERT INTO calendar_slot_links (token, owner_id, members, length, rules, days_ahead, active)
			VALUES ($1, $2, $3::uuid[], $4, $5, $6, $7) RETURNING id::text`,
			slotToken(), owner, members, l.Length, raw, l.DaysAhead, l.Active).Scan(&l.ID)
		return l.ID, err
	}
	// a joint link keeps at least one colleague; the personal one has none
	tag, err := s.db.Exec(ctx, `UPDATE calendar_slot_links SET length = $3, rules = $4, days_ahead = $5, active = $6,
			members = CASE WHEN members = '{}' THEN members ELSE $7::uuid[] END
		WHERE id::text = $1 AND owner_id = $2 AND (members = '{}' OR cardinality($7::uuid[]) > 0)`,
		l.ID, owner, l.Length, raw, l.DaysAhead, l.Active, members)
	if err == nil && tag.RowsAffected() == 0 {
		if _, e := s.SlotLink(ctx, owner, l.ID); e != nil {
			return "", e
		}
		err = i18n.E("calslots.err.members")
	}
	return l.ID, err
}

// DeleteSlotLink removes a joint link of the owner; its meetings stay. The personal one is only switched off.
func (s *Store) DeleteSlotLink(ctx context.Context, owner, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM calendar_slot_links WHERE id::text = $1 AND owner_id = $2 AND members <> '{}'`, id, owner)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SlotDays are the first day a link offers (today) and its last.
func (l SlotLink) SlotDays(now time.Time) (first, last time.Time) {
	first = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return first, first.AddDate(0, 0, l.DaysAhead-1)
}

// Offers tells a day the rules of the link offer meetings on: one of their weekdays, not a holiday of the company.
func (l SlotLink) Offers(day time.Time) bool {
	return !(worktime.Schedule{Holidays: l.Holidays}).Holiday(day) && slices.ContainsFunc(l.Rules, func(r SlotRule) bool { return slices.Contains(r.Days, Weekday(day)) })
}

// SlotFree are the starts of the meetings the link offers on day: by its rules, on its length's grid from the start
// of each span, after now, within its days, when the owner and the members are free (their meetings and absences,
// as the planner sees them).
func (s *Store) SlotFree(ctx context.Context, l SlotLink, day, now time.Time) ([]time.Time, error) {
	loc := now.Location()
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
	first, last := l.SlotDays(now)
	if day.Before(first) || day.After(last) || !l.Offers(day) {
		return nil, nil
	}
	length := time.Duration(l.Length) * time.Minute
	var starts []time.Time
	for _, r := range l.Rules {
		if !slices.Contains(r.Days, Weekday(day)) {
			continue
		}
		for m := r.From; m+l.Length <= r.To; m += l.Length {
			t := day.Add(time.Duration(m) * time.Minute) // the clock of the day, as on the guest's page
			if t.After(now) && !slices.ContainsFunc(starts, t.Equal) {
				starts = append(starts, t)
			}
		}
	}
	if len(starts) == 0 {
		return nil, nil
	}
	sort.Slice(starts, func(a, b int) bool { return starts[a].Before(starts[b]) })
	people, err := s.Busy(ctx, l.OwnerID, append([]string{l.OwnerID}, l.Members...), day, day.AddDate(0, 0, 1), "")
	if err != nil {
		return nil, err
	}
	var free []time.Time
	for _, t := range starts {
		clash := false
		for _, p := range people {
			for _, b := range p.Slots {
				if b.Start.Before(t.Add(length)) && b.End.After(t) {
					clash = true
				}
			}
		}
		if !clash {
			free = append(free, t)
		}
	}
	return free, nil
}

// SlotBooking is what a guest gives to make a meeting; Title and Description are those of the meeting in the
// owner's calendar.
type SlotBooking struct {
	Start                         time.Time
	GuestName, GuestContact, Note string
	ClientIP                      string
	Title, Description            string
}

// BookSlot makes the meeting of a guest: the start must be one the link offers and still free (checked under a lock
// of the owner, so two guests do not take one time), the guest — a name and a phone or e-mail, the address — under
// SlotsPerHour meetings this hour. The meeting is the owner's, a video meeting with the members invited (they are
// free, so they accept), reminded as the owner's settings say; the owner and the members are told.
func (s *Store) BookSlot(ctx context.Context, l SlotLink, b SlotBooking, now time.Time) (SlotMeeting, error) {
	b.GuestName, b.GuestContact, b.Note = strings.TrimSpace(b.GuestName), strings.TrimSpace(b.GuestContact), strings.TrimSpace(b.Note)
	if b.GuestName == "" || b.GuestContact == "" {
		return SlotMeeting{}, ErrSlotGuest
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM calendar_slot_meetings WHERE client_ip = $1 AND created_at > now() - interval '1 hour'`,
		b.ClientIP).Scan(&n); err != nil {
		return SlotMeeting{}, err
	}
	if b.ClientIP != "" && n >= SlotsPerHour {
		return SlotMeeting{}, ErrSlotTooMany
	}
	x, err := s.Settings(ctx, l.OwnerID)
	if err != nil {
		return SlotMeeting{}, err
	}
	reminds := []int{}
	if x.Remind >= 0 {
		reminds = []int{x.Remind}
	}
	m := SlotMeeting{LinkID: l.ID, OwnerID: l.OwnerID, OwnerName: l.OwnerName, Title: b.Title, Token: slotToken(),
		GuestName: b.GuestName, GuestContact: b.GuestContact, Note: b.Note, Start: b.Start, End: b.Start.Add(time.Duration(l.Length) * time.Minute), Video: true}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		// the owner's lock: the people of the link are checked free by one guest at a time
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('calendar_slots:' || $1::text))`, l.OwnerID); err != nil {
			return err
		}
		free, err := s.SlotFree(ctx, l, b.Start.In(now.Location()), now)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(free, b.Start.Equal) {
			return ErrSlotTaken
		}
		if err := tx.QueryRow(ctx, `INSERT INTO events (owner_id, title, description, starts_at, ends_at, reminds, accessibility, video)
			VALUES ($1, $2, $3, $4, $5, $6, 'busy', true) RETURNING id::text`, l.OwnerID, b.Title, b.Description, m.Start, m.End, reminds).Scan(&m.EventID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO event_attendees (event_id, user_id, status)
			SELECT $1, u.id, 'accepted' FROM users u WHERE u.id = ANY($2::uuid[]) AND u.active ON CONFLICT DO NOTHING`, m.EventID, l.Members); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO calendar_slot_meetings (link_id, owner_id, event_id, token, guest_name, guest_contact, note,
				starts_at, ends_at, client_ip) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id::text`,
			l.ID, l.OwnerID, m.EventID, m.Token, m.GuestName, m.GuestContact, m.Note, m.Start, m.End, b.ClientIP).Scan(&m.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, kind, event_id, body)
			SELECT u, 'event_slot', $1, $3 FROM unnest($2::uuid[]) u`, m.EventID, append([]string{l.OwnerID}, l.Members...),
			m.GuestName+" · "+m.Start.In(now.Location()).Format("02.01.2006 15:04"))
		return err
	})
	return m, err
}

// SlotMeetingByToken is the guest's meeting: its time and people as the meeting has them now (the organizer may
// have moved it or invited others).
func (s *Store) SlotMeetingByToken(ctx context.Context, tok string) (SlotMeeting, error) {
	var m SlotMeeting
	err := s.db.QueryRow(ctx, `SELECT x.id::text, coalesce(x.link_id::text, ''), x.owner_id::text, u.name, coalesce(x.event_id::text, ''),
			coalesce(e.title, ''), x.token, x.guest_name, x.guest_contact, x.note, coalesce(e.starts_at, x.starts_at), coalesce(e.ends_at, x.ends_at),
			coalesce(e.video, false), x.cancelled OR e.id IS NULL,
			coalesce((SELECT array_agg(p.name ORDER BY p.name) FROM event_attendees a JOIN users p ON p.id = a.user_id
				WHERE a.event_id = e.id AND a.status <> 'declined'), '{}')
		FROM calendar_slot_meetings x JOIN users u ON u.id = x.owner_id LEFT JOIN events e ON e.id = x.event_id
		WHERE x.token = $1`, tok).Scan(&m.ID, &m.LinkID, &m.OwnerID, &m.OwnerName, &m.EventID, &m.Title, &m.Token, &m.GuestName,
		&m.GuestContact, &m.Note, &m.Start, &m.End, &m.Video, &m.Cancelled, &m.People)
	if errors.Is(err, pgx.ErrNoRows) || tok == "" {
		return m, ErrNotFound
	}
	m.People = append([]string{m.OwnerName}, m.People...)
	return m, err
}

// CancelSlotMeeting is the guest cancelling the meeting before it starts: the meeting is removed from the calendar,
// its organizer and people are told.
func (s *Store) CancelSlotMeeting(ctx context.Context, tok string, now time.Time) (SlotMeeting, error) {
	m, err := s.SlotMeetingByToken(ctx, tok)
	if err != nil {
		return m, err
	}
	if m.Cancelled || !m.Start.After(now) {
		return m, ErrForbidden
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
			SELECT u, 'event_slot_cancel', $2, $3 FROM (SELECT owner_id AS u FROM events WHERE id::text = $1
				UNION SELECT user_id FROM event_attendees WHERE event_id::text = $1 AND status <> 'declined') x`,
			m.EventID, m.GuestName+" · "+m.Start.In(now.Location()).Format("02.01.2006 15:04"),
			"/calendar?view=day&date="+m.Start.In(now.Location()).Format("2006-01-02")); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE calendar_slot_meetings SET cancelled = true WHERE id::text = $1`, m.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM events WHERE id::text = $1`, m.EventID)
		return err
	})
	m.Cancelled = err == nil
	return m, err
}
