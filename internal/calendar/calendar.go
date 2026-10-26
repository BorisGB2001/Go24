// Package calendar is «Календарь»: meetings in a personal, project or company calendar, invitations that
// people accept or decline, and the deadlines of the person's tasks shown among them.
package calendar

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

type Store struct {
	db  *pgxpool.Pool
	Key []byte // the key the passwords of CalDAV servers are sealed with (see DavKey); nil — none can be connected
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

type Attendee struct {
	ID, Name, Status string // invited | accepted | tentative | declined
}

// Event is a meeting, or (Kind "task") a task deadline shown in the calendar, or (Kind "crm") the deadline of a CRM
// activity of the viewer, or (Kind "booking") a client's booking of «Онлайн-запись» on the viewer.
type Event struct {
	ID, Kind                  string // event | task
	OwnerID, OwnerName        string
	ProjectID, ProjectKey     string // a project's calendar ("" = personal or company)
	ProjectName               string
	Company                   bool
	Title, Description, Where string
	Start, End                time.Time
	AllDay                    bool
	Attendees                 []Attendee
	MyStatus                  string // the viewer's answer, "" when not invited
	TaskKey                   string // Kind task
	Overdue                   bool
	PersonID, AbsenceKind     string // Kind absence (whose, why) and birthday (whose)
	// repetition: daily | weekly | monthly | yearly ("" = once), every N periods, until a date
	Repeat      string
	Every       int
	Until       *time.Time
	Days        []int     // weekly: the days of the week it repeats on, 1 (Monday) … 7 (Sunday); none — the day of its start
	Count       int       // the series ends after so many meetings (0 — by Until or never); Until is the day of the last
	Reminds     []int     // minutes before the start of each reminder, ascending (none = no reminder)
	SeriesStart time.Time // a repeated meeting: the first occurrence (Start is this occurrence)
	// how the meeting shows the time of its people to others (see Accessibilities); a private meeting is seen in full
	// by its organizer and invited only, others get Hidden (no title, place, description or people)
	Accessibility string
	Private       bool
	Hidden        bool
	Skip          []time.Time // a repeated meeting: the original starts of its deleted or changed occurrences
	SeriesID      string      // a changed occurrence: the series it was taken out of, and its original start
	SeriesAt      *time.Time
	RoomID        string // the meeting room booked ("" — none)
	RoomName      string
	Video         bool   // a video meeting of go24: its call is joined from the meeting
	Color         string // #rrggbb ("" — the colour of its calendar), see Colors
	Important     bool   // «Важная встреча»
	// one of the organizer's own calendars ("" — the main one; shown to the organizer only, see Section)
	CalendarID, CalendarName string
	CalColor                 string // the colour of the calendar it is in for the viewer ("" — that of its kind)
	Of                       string // shown from a colleague's calendar: whose (see PersonRange)
	UID                      string // the UID a calendar program or file gave it ("" — its own, ID@go24); CalDAV only
	// the time zone its time is entered in and its repetitions go by (an IANA name, "" — the portal's)
	TZ string
	// an open event (see OpenEvents): its category ("" — an ordinary meeting)
	CategoryID, CategoryName string
	// Kind crm: the activity's kind (todo | call | meeting) and its card (the card's title is in Where)
	ActKind  string
	Card     *CRMRef
	ChatRoom string // «Чат встречи»: its messenger room ("" — none yet)
	ReadOnly bool   // of a calendar connected by its link: changed there only (see SyncLink)
	Status   string // Kind booking: the booking's status (its client is the Title, the resource — Where)
}

// TimeZones offered in the meeting's form (any other IANA zone is taken too).
var TimeZones = []string{"Europe/Kaliningrad", "Europe/Moscow", "Europe/Samara", "Asia/Yekaterinburg", "Asia/Omsk",
	"Asia/Novosibirsk", "Asia/Krasnoyarsk", "Asia/Irkutsk", "Asia/Yakutsk", "Asia/Vladivostok", "Asia/Magadan",
	"Asia/Kamchatka", "Europe/Minsk", "Europe/Kyiv", "Asia/Almaty", "Asia/Tashkent", "Asia/Tbilisi", "Asia/Yerevan",
	"Asia/Baku", "Europe/London", "Europe/Amsterdam", "Europe/Berlin", "Europe/Paris", "Europe/Istanbul", "Asia/Dubai",
	"Asia/Kolkata", "Asia/Shanghai", "Asia/Tokyo", "Australia/Sydney", "America/New_York", "America/Chicago",
	"America/Denver", "America/Los_Angeles", "UTC"}

// Zone is a time zone of the form: its IANA name and «(UTC+03:00) Europe/Moscow».
type Zone struct{ Name, Label string }

// Zones are TimeZones (and tz, when it is another one) with their offsets at t.
func Zones(t time.Time, tz string) []Zone {
	names := TimeZones
	if tz != "" && !slices.Contains(names, tz) {
		names = append([]string{tz}, names...)
	}
	var out []Zone
	for _, n := range names {
		if l, err := time.LoadLocation(n); err == nil {
			out = append(out, Zone{n, "(UTC" + t.In(l).Format("-07:00") + ") " + n})
		}
	}
	slices.SortStableFunc(out, func(a, b Zone) int {
		_, x := t.In(mustLoc(a.Name)).Zone()
		_, y := t.In(mustLoc(b.Name)).Zone()
		return x - y
	})
	return out
}

func mustLoc(n string) *time.Location {
	l, _ := time.LoadLocation(n)
	return l
}

// InZone is t by the clock of the meeting's zone.
func (e Event) InZone(t time.Time) time.Time { return t.In(e.Loc()) }

// ValidTZ: "" or a time zone Go knows.
func ValidTZ(tz string) bool {
	if tz == "" {
		return true
	}
	_, err := time.LoadLocation(tz)
	return err == nil && tz != "Local"
}

// Loc is the meeting's time zone (the portal's when it has none or an unknown one).
func (e Event) Loc() *time.Location {
	if e.TZ != "" {
		if l, err := time.LoadLocation(e.TZ); err == nil {
			return l
		}
	}
	return time.Local
}

// OtherTZ: the meeting's zone is not the portal's (its time is then shown in both).
func (e Event) OtherTZ() bool {
	return e.TZ != "" && !e.AllDay && !sameZone(e.Loc(), e.Start)
}

// sameZone: l keeps the portal's offsets all year round (another name of the same clock).
func sameZone(l *time.Location, t time.Time) bool {
	for _, m := range []int{0, 3, 6, 9} {
		x := t.AddDate(0, m, 0)
		_, a := x.In(l).Zone()
		_, b := x.In(time.Local).Zone()
		if a != b {
			return false
		}
	}
	return true
}

// Paint is the colour the meeting is drawn with for the viewer: its own, else that of its calendar.
func (e Event) Paint() string {
	if e.Color != "" {
		return e.Color
	}
	return e.CalColor
}

// Accessibilities: the meeting's «Доступность» terms; a free one does not make its people busy.
var Accessibilities = []string{"busy", "tentative", "absent", "free"}

// Colors: the palette of a meeting's colour in the form, that; any other #rrggbb is taken too.
var Colors = []string{"#86b100", "#0092cc", "#00afc7", "#e89b06", "#00b38c", "#de2b24", "#bd7ac9", "#838fa0", "#c3612c", "#e97090"}

var colorRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// Repeats is the meeting's period terms.
var Repeats = []string{"daily", "weekly", "monthly", "yearly"}

// occurrence k of a repeated meeting; ok is false for a day of a weekly meeting's first week before its start.
func (e Event) occurrence(k int) (start, end time.Time, ok bool) {
	first := e.SeriesStart // by the clock of the meeting's zone: 10:00 there stays 10:00 over its daylight saving
	if !e.AllDay {
		first = first.In(e.Loc())
	}
	every := max(e.Every, 1)
	switch {
	case e.Repeat == "weekly" && len(e.Days) > 0: // its days, week by week from the week of the start (from Monday)
		week, day := k/len(e.Days), e.Days[k%len(e.Days)]
		start = first.AddDate(0, 0, 7*week*every+day-WeekDay(first))
	case e.Repeat == "daily":
		start = first.AddDate(0, 0, k*every)
	case e.Repeat == "weekly":
		start = first.AddDate(0, 0, 7*k*every)
	case e.Repeat == "monthly":
		start = first.AddDate(0, k*every, 0)
	case e.Repeat == "yearly":
		start = first.AddDate(k*every, 0, 0)
	default:
		start = first
	}
	start = start.In(time.Local)
	return start, start.Add(e.End.Sub(e.Start)), !start.Before(e.SeriesStart)
}

// OnDay: a weekly meeting repeats on the day of the week d (1 — Monday … 7 — Sunday); with no days chosen — on the
// day of its start.
func (e Event) OnDay(d int) bool {
	if len(e.Days) > 0 {
		return slices.Contains(e.Days, d)
	}
	start := e.Start
	if !e.AllDay {
		start = start.In(e.Loc())
	}
	return WeekDay(start) == d
}

// WeekDays are the days of a week to choose for a weekly meeting, from Monday.
func (Event) WeekDays() []int { return []int{1, 2, 3, 4, 5, 6, 7} }

// WeekDay is the day of the week of t: 1 (Monday) … 7 (Sunday).
func WeekDay(t time.Time) int { return (int(t.Weekday())+6)%7 + 1 }

// expand turns a meeting into its occurrences that touch [from, to).
func (e Event) expand(from, to time.Time) []Event {
	e.SeriesStart = e.Start
	if e.Repeat == "" {
		if e.Start.Before(to) && !e.End.Before(from) {
			return []Event{e}
		}
		return nil
	}
	var out []Event
	n := 0 // the meetings of the series so far: Count ends it (deleted ones count too, as RFC 5545 has it)
	for k := 0; k < 5000*max(len(e.Days), 1); k++ {
		st, en, ok := e.occurrence(k)
		if !ok {
			continue
		}
		if n++; !st.Before(to) || (e.Until != nil && st.After(e.Until.AddDate(0, 0, 1))) || (e.Count > 0 && n > e.Count) {
			break
		}
		if en.Before(from) || slices.ContainsFunc(e.Skip, st.Equal) {
			continue
		}
		o := e
		o.Start, o.End = st, en
		out = append(out, o)
	}
	return out
}

// repetition checks a meeting's repetition and puts it in order: the days of a weekly one (with that of its start;
// its start's day alone is no list), the day of the last meeting of one that ends after Count.
func (e *Event) repetition() error {
	if e.Repeat != "" && !slices.Contains(Repeats, e.Repeat) || e.Count < 0 || e.Count > 999 {
		return i18n.E("err.bad_form")
	}
	if e.Every < 1 || e.Every > 365 {
		e.Every = 1
	}
	if e.Repeat == "" {
		e.Until, e.Count = nil, 0
	}
	var days []int
	if e.Repeat == "weekly" && len(e.Days) > 0 {
		start := e.Start
		if !e.AllDay {
			start = start.In(e.Loc())
		}
		for _, d := range append(e.Days, WeekDay(start)) {
			if d < 1 || d > 7 {
				return i18n.E("err.bad_form")
			}
			if !slices.Contains(days, d) {
				days = append(days, d)
			}
		}
		if slices.Sort(days); len(days) == 1 {
			days = nil
		}
	}
	e.Days = days
	if e.Count > 0 {
		x := *e
		x.Until, x.Skip = nil, nil
		x.SeriesStart = x.Start
		for k, n := 0, 0; n < e.Count && k < 5000*max(len(days), 1); k++ {
			if st, _, ok := x.occurrence(k); ok {
				n++
				d := time.Date(st.Year(), st.Month(), st.Day(), 0, 0, 0, 0, time.Local)
				e.Until = &d
			}
		}
	}
	return nil
}

// Occurrence is the occurrence of a repeated meeting that starts at t (nil — there is none).
func (e Event) Occurrence(t time.Time) *Event {
	for _, o := range e.expand(t, t.Add(time.Second)) {
		if o.Start.Equal(t) {
			return &o
		}
	}
	return nil
}

// Mine: the viewer may change the event.
func (e Event) Mine(viewer string) bool { return e.OwnerID == viewer }

// visible: the owner, the invited, the project's members, everyone for the company calendar (but a guest from
// outside: the company does not exist for them), admins in admin mode.
const visible = `(e.owner_id = $1 OR (e.company AND NOT EXISTS (SELECT 1 FROM users g WHERE g.id = $1 AND g.guest)) OR $2
	OR EXISTS (SELECT 1 FROM event_attendees a WHERE a.event_id = e.id AND a.user_id = $1)
	OR (e.project_id IS NOT NULL AND EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = e.project_id AND m.user_id = $1)))`

const selectSQL = `SELECT e.id::text, e.owner_id::text, u.name, coalesce(e.project_id::text, ''), coalesce(p.key, ''), coalesce(p.name, ''),
		e.company, e.title, e.description, e.location, e.starts_at, e.ends_at, e.all_day,
		e.repeat, e.repeat_every, e.repeat_until, e.repeat_days, e.repeat_count, e.reminds, e.accessibility, e.private,
		coalesce(e.series_id::text, ''), e.series_at, coalesce(e.room_id::text, ''), coalesce(mr.name, ''), e.video, e.color, e.important,
		coalesce(e.calendar_id::text, ''), coalesce(uc.name, ''), coalesce(uc.color, ''), e.timezone,
		coalesce(e.category_id::text, ''), coalesce(ec.name, ''), e.chat_room, coalesce(uc.source_url, '') <> ''
	FROM events e JOIN users u ON u.id = e.owner_id LEFT JOIN projects p ON p.id = e.project_id
		LEFT JOIN meeting_rooms mr ON mr.id = e.room_id LEFT JOIN user_calendars uc ON uc.id = e.calendar_id
		LEFT JOIN event_categories ec ON ec.id = e.category_id`

// scanEvent reads a row of selectSQL.
func scanEvent(r pgx.CollectableRow) (Event, error) {
	e := Event{Kind: "event"}
	err := r.Scan(&e.ID, &e.OwnerID, &e.OwnerName, &e.ProjectID, &e.ProjectKey, &e.ProjectName, &e.Company,
		&e.Title, &e.Description, &e.Where, &e.Start, &e.End, &e.AllDay, &e.Repeat, &e.Every, &e.Until, &e.Days, &e.Count, &e.Reminds,
		&e.Accessibility, &e.Private, &e.SeriesID, &e.SeriesAt, &e.RoomID, &e.RoomName, &e.Video, &e.Color, &e.Important,
		&e.CalendarID, &e.CalendarName, &e.CalColor, &e.TZ, &e.CategoryID, &e.CategoryName, &e.ChatRoom, &e.ReadOnly)
	e.SeriesStart = e.Start
	return e, err
}

func (s *Store) scan(ctx context.Context, viewer string, rows pgx.Rows) ([]Event, error) {
	list, err := pgx.CollectRows(rows, scanEvent)
	if err != nil || len(list) == 0 {
		return list, err
	}
	if err := s.skips(ctx, list); err != nil {
		return nil, err
	}
	ids := make([]string, len(list))
	at := map[string]int{}
	for i, e := range list {
		ids[i], at[e.ID] = e.ID, i
	}
	ar, err := s.db.Query(ctx, `SELECT a.event_id::text, a.user_id::text, u.name, a.status FROM event_attendees a
		JOIN users u ON u.id = a.user_id WHERE a.event_id = ANY($1::uuid[]) ORDER BY u.name`, ids)
	if err != nil {
		return nil, err
	}
	defer ar.Close()
	for ar.Next() {
		var eid string
		var a Attendee
		if err := ar.Scan(&eid, &a.ID, &a.Name, &a.Status); err != nil {
			return nil, err
		}
		e := &list[at[eid]]
		e.Attendees = append(e.Attendees, a)
		if a.ID == viewer {
			e.MyStatus = a.Status
		}
	}
	if err := ar.Err(); err != nil {
		return nil, err
	}
	main, err := s.MainColor(ctx, viewer)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	for i := range list { // a private meeting: others (but admin mode) see only that its people are busy
		if e := &list[i]; e.Private && e.OwnerID != viewer && e.MyStatus == "" && !tasks.IsAdmin(ctx) {
			e.Hidden, e.Title, e.Description, e.Where, e.Attendees, e.ChatRoom = true, "", "", "", nil, ""
		}
		// one's own calendars are one's own: for others the meeting is in their main calendar
		if e := &list[i]; e.OwnerID != viewer {
			e.CalendarID, e.CalendarName, e.CalColor = "", "", ""
		}
		if e := &list[i]; e.CalendarID == "" && e.ProjectID == "" && !e.Company {
			e.CalColor = main
		}
	}
	return list, nil
}

// Range is what the viewer's calendar shows between from and to: meetings and the deadlines of their open tasks.
func (s *Store) Range(ctx context.Context, viewer string, from, to time.Time) ([]Event, error) {
	rows, err := s.db.Query(ctx, selectSQL+` WHERE e.starts_at < $4
		AND ((e.repeat = '' AND e.ends_at >= $3) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= $3::date)))
		AND `+visible+` ORDER BY e.starts_at`, viewer, tasks.IsAdmin(ctx), from, to)
	if err != nil {
		return nil, err
	}
	series, err := s.scan(ctx, viewer, rows)
	if err != nil {
		return nil, err
	}
	var list []Event
	for _, e := range series {
		list = append(list, e.expand(from, to)...)
	}
	trows, err := s.db.Query(ctx, `SELECT p.key || '-' || t.number, t.title, t.deadline FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.status <> 'completed' AND t.deadline >= $2 AND t.deadline < $3
		  AND (t.responsible_id = $1 OR t.creator_id = $1 OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = $1))
		  AND (p.kind <> 'collab' OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1))
		ORDER BY t.deadline`, viewer, from, to)
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	now := time.Now()
	for trows.Next() {
		e := Event{Kind: "task"}
		if err := trows.Scan(&e.TaskKey, &e.Title, &e.Start); err != nil {
			return nil, err
		}
		e.End, e.Overdue = e.Start, e.Start.Before(now)
		list = append(list, e)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	// the open CRM activities the viewer is responsible for, by their deadlines shows calls and meetings
	// of the CRM); one of a card that is gone (the recycle bin) is not shown
	crows, err := s.db.Query(ctx, `SELECT id::text, kind, subject, deadline, entity, entity_id::text, title FROM (
		SELECT c.id, c.kind, c.subject, c.deadline, c.entity, c.entity_id, `+crmTitle+` AS title FROM crm_activities c
		WHERE c.responsible_id = $1 AND c.done_at IS NULL AND c.deadline >= $2 AND c.deadline < $3) x
		WHERE title IS NOT NULL ORDER BY deadline`, viewer, from, to)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		e, card := Event{Kind: "crm"}, CRMRef{}
		if err := crows.Scan(&e.ID, &e.ActKind, &e.Title, &e.Start, &card.Entity, &card.ID, &card.Title); err != nil {
			return nil, err
		}
		e.End, e.Overdue, e.Card, e.Where = e.Start, e.Start.Before(now), &card, card.Title
		list = append(list, e)
	}
	if err := crows.Err(); err != nil {
		return nil, err
	}
	// the bookings of «Онлайн-запись» on the viewer as a resource
	books, err := s.bookingsOf(ctx, []string{viewer}, from, to)
	if err != nil {
		return nil, err
	}
	list = append(list, books...)
	// absences of the company and birthdays: all-day items every employee sees (a guest — none of them)
	var guest bool
	if err := s.db.QueryRow(ctx, `SELECT guest FROM users WHERE id = $1`, viewer).Scan(&guest); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if guest {
		sortEvents(list)
		return list, nil
	}
	arows, err := s.db.Query(ctx, `SELECT a.user_id::text, u.name, a.kind, a.starts_on, a.ends_on FROM absences a JOIN users u ON u.id = a.user_id
		WHERE a.starts_on < $2::date AND a.ends_on >= $1::date`, from, to)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		e := Event{Kind: "absence", AllDay: true}
		var d0, d1 time.Time
		if err := arows.Scan(&e.PersonID, &e.Title, &e.AbsenceKind, &d0, &d1); err != nil {
			arows.Close()
			return nil, err
		}
		e.Start = time.Date(d0.Year(), d0.Month(), d0.Day(), 0, 0, 0, 0, time.Local)
		e.End = time.Date(d1.Year(), d1.Month(), d1.Day(), 23, 59, 0, 0, time.Local)
		list = append(list, e)
	}
	arows.Close()
	brows, err := s.db.Query(ctx, `SELECT id::text, name, birthday FROM users WHERE active AND NOT guest AND birthday IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	for brows.Next() {
		var id, name string
		var born time.Time
		if err := brows.Scan(&id, &name, &born); err != nil {
			brows.Close()
			return nil, err
		}
		for y := from.Year(); y <= to.Year(); y++ {
			d := time.Date(y, born.Month(), born.Day(), 0, 0, 0, 0, time.Local)
			if d.Before(to) && d.AddDate(0, 0, 1).After(from) { // the birthday touches [from, to)
				list = append(list, Event{Kind: "birthday", AllDay: true, PersonID: id, Title: name, Start: d, End: d.Add(23*time.Hour + 59*time.Minute)})
			}
		}
	}
	brows.Close()
	sortEvents(list)
	return list, nil
}

// skips fills Skip of the repeated meetings of the list.
func (s *Store) skips(ctx context.Context, list []Event) error {
	at := map[string][]int{}
	var ids []string
	for i, e := range list {
		if e.Repeat != "" {
			if at[e.ID] == nil {
				ids = append(ids, e.ID)
			}
			at[e.ID] = append(at[e.ID], i)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT event_id::text, at FROM event_exceptions WHERE event_id = ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t time.Time
		if err := rows.Scan(&id, &t); err != nil {
			return err
		}
		for _, i := range at[id] {
			list[i].Skip = append(list[i].Skip, t)
		}
	}
	return rows.Err()
}

// sortEvents: the all-day items first, then by the start.
func sortEvents(list []Event) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].AllDay != list[j].AllDay {
			return list[i].AllDay
		}
		return list[i].Start.Before(list[j].Start)
	})
}

// ProjectRange is the calendar of one project (a collab's calendar): its meetings and the deadlines of its open
// tasks between from and to — nothing personal, nothing of the company. Only the project's members (and admin mode)
// see it; tasks follow the project's visibility.
func (s *Store) ProjectRange(ctx context.Context, viewer, projectID string, from, to time.Time) ([]Event, error) {
	var member bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id::text = $1 AND user_id = $2)`,
		projectID, viewer).Scan(&member); err != nil {
		return nil, err
	}
	if !member && !tasks.IsAdmin(ctx) {
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, selectSQL+` WHERE e.project_id::text = $5 AND e.starts_at < $4
		AND ((e.repeat = '' AND e.ends_at >= $3) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= $3::date)))
		AND `+visible+` ORDER BY e.starts_at`, viewer, tasks.IsAdmin(ctx), from, to, projectID)
	if err != nil {
		return nil, err
	}
	series, err := s.scan(ctx, viewer, rows)
	if err != nil {
		return nil, err
	}
	var list []Event
	for _, e := range series {
		list = append(list, e.expand(from, to)...)
	}
	trows, err := s.db.Query(ctx, `SELECT p.key || '-' || t.number, t.title, t.deadline FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE p.id::text = $4 AND t.status <> 'completed' AND t.deadline >= $2 AND t.deadline < $3
		  AND ($5 OR p.visibility = 'members' OR t.responsible_id = $1 OR t.creator_id = $1
		       OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = $1))
		ORDER BY t.deadline`, viewer, from, to, projectID, tasks.IsAdmin(ctx))
	if err != nil {
		return nil, err
	}
	defer trows.Close()
	now := time.Now()
	for trows.Next() {
		e := Event{Kind: "task"}
		if err := trows.Scan(&e.TaskKey, &e.Title, &e.Start); err != nil {
			return nil, err
		}
		e.End, e.Overdue = e.Start, e.Start.Before(now)
		list = append(list, e)
	}
	if err := trows.Err(); err != nil {
		return nil, err
	}
	sortEvents(list)
	return list, nil
}

// Get loads an event the viewer may see.
func (s *Store) Get(ctx context.Context, viewer, id string) (Event, error) {
	rows, err := s.db.Query(ctx, selectSQL+` WHERE e.id::text = $3 AND (`+visible+` OR `+shared+` OR `+openEvent+`)`, viewer, tasks.IsAdmin(ctx), id)
	if err != nil {
		return Event{}, err
	}
	list, err := s.scan(ctx, viewer, rows)
	if err != nil {
		return Event{}, err
	}
	if len(list) == 0 {
		return Event{}, ErrNotFound
	}
	return list[0], nil
}

// Save creates (empty ID) or changes the viewer's event with exactly these invited people; returns its ID.
// Newly invited people are notified; a moved meeting asks everyone again.
func (s *Store) Save(ctx context.Context, viewer string, e Event, invite []string) (string, error) {
	e.Title = strings.TrimSpace(e.Title)
	if e.Title == "" {
		return "", i18n.E("err.event_title")
	}
	if e.AllDay {
		y, m, d := e.Start.Date()
		e.Start = time.Date(y, m, d, 0, 0, 0, 0, time.Local)
		y, m, d = e.End.Date()
		e.End = time.Date(y, m, d, 23, 59, 0, 0, time.Local)
	}
	if e.End.Before(e.Start) {
		return "", i18n.E("err.event_end")
	}
	e.Reminds = Reminds(e.Reminds)
	if e.Accessibility == "" {
		e.Accessibility = "busy"
	}
	if !slices.Contains(Accessibilities, e.Accessibility) {
		return "", i18n.E("err.bad_form")
	}
	if e.Color = strings.ToLower(strings.TrimSpace(e.Color)); e.Color != "" && !colorRe.MatchString(e.Color) {
		return "", i18n.E("err.bad_form")
	}
	if e.TZ = strings.TrimSpace(e.TZ); !ValidTZ(e.TZ) {
		return "", i18n.E("err.bad_form")
	}
	if e.TZ == time.Local.String() || e.AllDay { // the portal's own zone, whole days: by the portal's clock
		e.TZ = ""
	}
	if err := e.repetition(); err != nil {
		return "", err
	}
	if e.ProjectID != "" || e.Company { // one's own calendars hold personal meetings, and so do open events
		e.CalendarID, e.CategoryID = "", ""
	}
	var category any
	if e.CategoryID != "" {
		if err := s.mayOpen(ctx, viewer, e); err != nil {
			return "", err
		}
		e.Private, category = false, e.CategoryID // everyone sees an open event
	}
	var project any
	if e.ProjectID != "" {
		var member bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id::text = $1 AND user_id = $2)`,
			e.ProjectID, viewer).Scan(&member); err != nil {
			return "", err
		}
		if !member && !tasks.IsAdmin(ctx) {
			return "", ErrForbidden
		}
		project = e.ProjectID
		// a collab's meeting is for its members only: an invitation would show the collab to someone outside
		var strangers, archived bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects p, unnest($2::text[]) AS i(id)
				WHERE p.id::text = $1 AND p.kind = 'collab'
				  AND NOT EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id::text = i.id)),
			EXISTS (SELECT 1 FROM projects WHERE id::text = $1 AND kind = 'collab' AND archived)`,
			e.ProjectID, invite).Scan(&strangers, &archived); err != nil {
			return "", err
		}
		if archived { // a collab in the archive is read-only
			return "", i18n.E("err.collab_archived")
		}
		if strangers {
			return "", i18n.E("err.collab_invite_members")
		}
	}
	// a guest meets people in their collabs only: a guest's meetings are in a collab, and nobody invites a guest to
	// a meeting outside the collabs (it would show them the company)
	var collab, actorGuest, guestInvited bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects WHERE id::text = $3 AND kind = 'collab'),
		EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND guest),
		EXISTS (SELECT 1 FROM users WHERE id::text = ANY($2::text[]) AND guest)`, viewer, invite, e.ProjectID).Scan(&collab, &actorGuest, &guestInvited); err != nil {
		return "", err
	}
	if (!collab && (actorGuest || guestInvited)) || (actorGuest && (e.Company || e.RoomID != "")) {
		return "", i18n.E("err.guest_collab_only")
	}
	var room any
	if e.RoomID != "" {
		room = e.RoomID
	}
	var id string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if e.RoomID != "" { // the room is free at every occurrence
			clash, err := s.roomClash(ctx, tx, e)
			if err != nil {
				return err
			}
			if clash != nil {
				return i18n.E("err.room_busy", clash.Start.Format("02.01.2006 15:04"), clash.End.Format("15:04"))
			}
		}
		moved, opened := false, false // opened: it became an open event, its category hears of it
		// the calendar is one of the organizer's own; someone else (admin mode) does not move it there
		section := func(owner string) (any, error) {
			if e.CalendarID == "" {
				return nil, nil
			}
			var ok bool // a connected calendar takes the meetings of its link only
			err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_calendars WHERE id::text = $1 AND owner_id::text = $2
				AND (source_url = '') <> $3)`, e.CalendarID, owner, syncing(ctx)).Scan(&ok)
			if err == nil && !ok {
				err = i18n.E("err.bad_form")
			}
			return e.CalendarID, err
		}
		if e.ID == "" {
			cal, err := section(viewer)
			if err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO events (owner_id, project_id, company, title, description, location, starts_at, ends_at, all_day,
				repeat, repeat_every, repeat_until, reminds, accessibility, private, room_id, video, color, important, calendar_id, timezone, category_id,
				repeat_days, repeat_count)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, coalesce($23::integer[], '{}'), $24) RETURNING id`, viewer, project, e.Company, e.Title,
				strings.TrimSpace(e.Description), strings.TrimSpace(e.Where), e.Start, e.End, e.AllDay,
				e.Repeat, e.Every, e.Until, e.Reminds, e.Accessibility, e.Private, room, e.Video, e.Color, e.Important, cal, e.TZ, category,
				e.Days, e.Count).Scan(&id); err != nil {
				return err
			}
			opened = category != nil
		} else {
			var owner, was string
			var start, end time.Time
			var linked bool // of a connected calendar
			err := tx.QueryRow(ctx, `SELECT owner_id::text, starts_at, ends_at, coalesce(category_id::text, ''),
				EXISTS (SELECT 1 FROM user_calendars c WHERE c.id = events.calendar_id AND c.source_url <> '')
				FROM events WHERE id::text = $1 FOR UPDATE`, e.ID).Scan(&owner, &start, &end, &was, &linked)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			if owner != viewer && !tasks.IsAdmin(ctx) || linked != syncing(ctx) {
				return ErrForbidden
			}
			moved = !start.Equal(e.Start) || !end.Equal(e.End)
			opened = was == "" && category != nil
			id = e.ID
			cal, err := section(owner)
			if err != nil {
				return err
			}
			// the organizer's calendar stays when someone else changes the meeting (they do not see it)
			if _, err := tx.Exec(ctx, `UPDATE events SET project_id = $2, company = $3, title = $4, description = $5, location = $6,
				starts_at = $7, ends_at = $8, all_day = $9, repeat = $10, repeat_every = $11, repeat_until = $12, reminds = $13,
				accessibility = $14, private = $15, room_id = $16, video = $17, color = $18, important = $19,
				calendar_id = CASE WHEN owner_id::text = $21 OR $2::uuid IS NOT NULL OR $3 THEN $20::uuid ELSE calendar_id END,
				timezone = $22, category_id = $23, repeat_days = coalesce($24::integer[], '{}'), repeat_count = $25, updated_at = now() WHERE id = $1`, id, project, e.Company, e.Title,
				strings.TrimSpace(e.Description), strings.TrimSpace(e.Where), e.Start, e.End, e.AllDay,
				e.Repeat, e.Every, e.Until, e.Reminds, e.Accessibility, e.Private, room, e.Video, e.Color, e.Important, cal, viewer, e.TZ, category,
				e.Days, e.Count); err != nil {
				return err
			}
		}
		var want []string
		for _, u := range invite {
			if u != "" && u != viewer && !slices.Contains(want, u) {
				want = append(want, u)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM event_attendees WHERE event_id = $1 AND NOT (user_id = ANY($2::uuid[]))`, id, want); err != nil {
			return err
		}
		if moved { // a new time: everyone answers again
			if _, err := tx.Exec(ctx, `UPDATE event_attendees SET status = 'invited' WHERE event_id = $1`, id); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `INSERT INTO event_attendees (event_id, user_id)
			SELECT $1, u.id FROM users u WHERE u.id = ANY($2::uuid[]) AND u.active ON CONFLICT DO NOTHING RETURNING user_id::text`, id, want)
		if err != nil {
			return err
		}
		added, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		notifyOf := added
		if moved {
			notifyOf = want
		}
		if _, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body)
			SELECT u, $2, 'event', $1, $4 FROM unnest($3::uuid[]) u WHERE NOT go24_quiet()`, id, viewer, notifyOf, e.Title); err != nil || !opened {
			return err
		}
		return s.announceOpen(ctx, tx, id, viewer, e.CategoryID, e.Title)
	})
	return id, err
}

// Scopes of a change to an occurrence of a repeated meeting asks: only this one, this one and the
// following, the whole series.
const (
	ScopeOne       = "one"
	ScopeFollowing = "following"
	ScopeAll       = "all"
)

// series loads the repeated meeting id the viewer may change and checks that one of its occurrences starts at at.
func (s *Store) series(ctx context.Context, viewer, id string, at time.Time) (Event, error) {
	e, err := s.Get(ctx, viewer, id)
	if err != nil {
		return e, err
	}
	if e.OwnerID != viewer && !tasks.IsAdmin(ctx) {
		return e, ErrForbidden
	}
	if e.Occurrence(at) == nil {
		return e, ErrNotFound
	}
	return e, nil
}

// SaveOccurrence changes the occurrence of the repeated meeting id that starts at at (its original start); e is the
// meeting as it should become, its Start and End are those of the occurrence. ScopeOne makes the occurrence a meeting
// of its own; ScopeFollowing ends the series before it and begins a new one with it; ScopeAll moves the whole series
// by the same shift. Returns the ID of the meeting that now holds the occurrence.
func (s *Store) SaveOccurrence(ctx context.Context, viewer, id string, at time.Time, scope string, e Event, invite []string) (string, error) {
	series, err := s.series(ctx, viewer, id, at)
	if err != nil {
		return "", err
	}
	if scope == ScopeFollowing && at.Equal(series.Start) {
		scope = ScopeAll
	}
	switch scope {
	case ScopeAll:
		length, shift := e.End.Sub(e.Start), e.Start.Sub(at)
		e.ID, e.Start = id, series.Start.Add(shift)
		e.End = e.Start.Add(length)
		if slices.Equal(e.Days, series.Days) { // a weekly series moved to another day: its days move with it
			was, now := series.Start, e.Start
			if !e.AllDay {
				was, now = was.In(e.Loc()), now.In(e.Loc())
			}
			by := WeekDay(now) - WeekDay(was)
			e.Days = slices.Clone(e.Days)
			for i, d := range e.Days {
				e.Days[i] = (d-1+by+7)%7 + 1
			}
		}
		if _, err := s.Save(ctx, viewer, e, invite); err != nil || shift == 0 {
			return id, err
		}
		// the deleted and changed occurrences move with the series
		return id, pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE event_exceptions SET at = at + make_interval(secs => $2) WHERE event_id = $1`, id, shift.Seconds()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE events SET series_at = series_at + make_interval(secs => $2) WHERE series_id = $1`, id, shift.Seconds())
			return err
		})
	case ScopeOne:
		// the occurrence leaves the series first: in its room it must not clash with itself
		tag, err := s.db.Exec(ctx, `INSERT INTO event_exceptions (event_id, at) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, at)
		if err != nil {
			return "", err
		}
		e.ID, e.Repeat, e.Until = "", "", nil
		nid, err := s.Save(ctx, viewer, e, invite)
		if err != nil {
			if tag.RowsAffected() > 0 {
				_, _ = s.db.Exec(ctx, `DELETE FROM event_exceptions WHERE event_id = $1 AND at = $2`, id, at)
			}
			return "", err
		}
		_, err = s.db.Exec(ctx, `UPDATE events SET series_id = $2, series_at = $3 WHERE id = $1`, nid, id, at)
		return nid, err
	case ScopeFollowing:
		if _, err := s.db.Exec(ctx, `UPDATE events SET repeat_until = $2::date, repeat_count = 0, updated_at = now() WHERE id = $1`, id, dayBefore(at)); err != nil {
			return "", err
		}
		if e.Count > 0 && e.Count == series.Count { // the new series ends where the old one did
			e.Count, e.Until = 0, series.Until
		}
		e.ID = ""
		nid, err := s.Save(ctx, viewer, e, invite)
		if err != nil { // the series as it was
			_, _ = s.db.Exec(ctx, `UPDATE events SET repeat_until = $2, repeat_count = $3 WHERE id = $1`, id, series.Until, series.Count)
		}
		return nid, err
	}
	return "", i18n.E("err.bad_form")
}

// dayBefore: the last day of a series that ends before the occurrence at.
func dayBefore(at time.Time) string { return at.AddDate(0, 0, -1).Format("2006-01-02") }

// DeleteOccurrence deletes the occurrence of the repeated meeting id that starts at at: only it (ScopeOne), it and
// the following (ScopeFollowing) or the whole series (ScopeAll).
func (s *Store) DeleteOccurrence(ctx context.Context, viewer, id string, at time.Time, scope string) error {
	series, err := s.series(ctx, viewer, id, at)
	if err != nil {
		return err
	}
	if scope == ScopeFollowing && at.Equal(series.Start) || series.Repeat == "" {
		scope = ScopeAll
	}
	switch scope {
	case ScopeAll:
		return s.Delete(ctx, viewer, id)
	case ScopeOne:
		_, err := s.db.Exec(ctx, `INSERT INTO event_exceptions (event_id, at) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, at)
		return err
	case ScopeFollowing:
		_, err := s.db.Exec(ctx, `UPDATE events SET repeat_until = $2::date, repeat_count = 0, updated_at = now() WHERE id = $1`, id, dayBefore(at))
		return err
	}
	return i18n.E("err.bad_form")
}

// Delete removes the viewer's event.
func (s *Store) Delete(ctx context.Context, viewer, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM events WHERE id::text = $1 AND (owner_id = $2 OR $3)`, id, viewer, tasks.IsAdmin(ctx))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Respond is the invited person's answer (accepted | tentative | declined); the organizer is told.
func (s *Store) Respond(ctx context.Context, viewer, id, status string) error {
	if status != "accepted" && status != "tentative" && status != "declined" {
		return i18n.E("err.bad_form")
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE event_attendees SET status = $3 WHERE event_id::text = $1 AND user_id = $2`, id, viewer, status)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body)
			SELECT e.owner_id, $2, 'event_reply', e.id, $3 FROM events e WHERE e.id::text = $1 AND e.owner_id <> $2 AND NOT go24_quiet()`,
			id, viewer, status)
		return err
	})
}

// RemindDue sends the reminders whose time has come: to the organizer and everyone invited who has not declined.
// Each reminder of an occurrence is sent once; one of an occurrence that has already started (go24 was down) is skipped.
func (s *Store) RemindDue(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.Query(ctx, selectSQL+` WHERE e.reminds <> '{}'
		AND ((e.repeat = '' AND e.starts_at > $1) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= $1::date)))`, now)
	if err != nil {
		return 0, err
	}
	series, err := pgx.CollectRows(rows, scanEvent)
	if err != nil {
		return 0, err
	}
	if err := s.skips(ctx, series); err != nil {
		return 0, err
	}
	sent := 0
	for _, e := range series {
		longest := time.Duration(slices.Max(e.Reminds)) * time.Minute
		for _, o := range e.expand(now, now.Add(longest+time.Minute)) {
			if o.Start.Before(now) {
				continue
			}
			for _, m := range e.Reminds {
				if o.Start.Add(-time.Duration(m) * time.Minute).After(now) {
					continue
				}
				tag, err := s.db.Exec(ctx, `INSERT INTO event_reminders (event_id, at, minutes) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
					o.ID, o.Start, m)
				if err != nil {
					return sent, err
				}
				if tag.RowsAffected() == 0 {
					continue
				}
				if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body)
					SELECT u, $2, 'event_remind', $1, $3 FROM (
						SELECT $2::uuid AS u UNION SELECT user_id FROM event_attendees WHERE event_id = $1 AND status <> 'declined') x
					WHERE NOT go24_quiet()`, o.ID, o.OwnerID, o.Start.UTC().Format(time.RFC3339)); err != nil {
					return sent, err
				}
				sent++
			}
		}
	}
	return sent, nil
}

// RemindPresets are the reminders the meeting form offers, minutes before the start (the list).
var RemindPresets = []int{0, 5, 10, 15, 20, 30, 60, 120, 1440, 2880, 10080}

// Reminds puts reminders in order: ascending, without repeats and negatives, at most ten.
func Reminds(list []int) []int {
	out := []int{}
	for _, m := range list {
		if m >= 0 && m <= 60*24*28 && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	slices.Sort(out)
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// SetChat keeps the meeting's chat room unless it has one already; returns the room it has.
func (s *Store) SetChat(ctx context.Context, id, room string) (string, error) {
	err := s.db.QueryRow(ctx, `UPDATE events SET chat_room = CASE WHEN chat_room = '' THEN $2 ELSE chat_room END
		WHERE id::text = $1 RETURNING chat_room`, id, room).Scan(&room)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return room, err
}
