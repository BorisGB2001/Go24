package mail

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Invitations by mail, as Outlook sends and reads them: iCalendar (RFC 5545) carried by iTIP (RFC 5546) — METHOD
// REQUEST (an invitation or its update, by SEQUENCE), REPLY (an answer to the organizer), CANCEL (the meeting is
// off), COUNTER (an attendee proposes a new time), DECLINECOUNTER (the organizer declines it). go24 reads the first meeting of the calendar (not the changed occurrences of a series) and writes its own.

// Person is an organizer or an attendee of a meeting.
type Person struct {
	Email, Name string
	Status      string // the answer, lower case: needs-action, accepted, tentative, declined
	RSVP        bool   // an answer is asked for
}

// Display is the name, else the address.
func (p Person) Display() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Email
}

// Invite is a meeting carried by a letter.
type Invite struct {
	Method                         string // REQUEST, REPLY, CANCEL, PUBLISH… (upper case; "" — PUBLISH)
	UID                            string
	Sequence                       int
	Summary, Location, Description string
	Comment                        string // an answer's words (REPLY)
	Start, End                     time.Time
	AllDay                         bool
	TZ                             string // the IANA zone of its DTSTART ("" — UTC, a floating time or one go24 does not know)
	Organizer                      Person
	Attendees                      []Person
	Cancelled                      bool   // STATUS:CANCELLED
	Join                           string // the online meeting's link: Google Meet, Teams, Zoom… (https only)
	// repetition, as go24's calendar keeps it: daily | weekly | monthly | yearly ("" — once), every N, until a day
	Repeat  string
	Every   int
	Until   *time.Time
	Days    []int       // weekly: the days of the week (1 — Monday … 7 — Sunday, BYDAY); none — the day of its start
	Count   int         // the series ends after so many meetings (COUNT; Until is then the day of the last one)
	Skip    []time.Time // the deleted occurrences of a series (EXDATE)
	Alarms  []int       // the reminders: minutes before the start (VALARM)
	Private bool        // CLASS:PRIVATE or CONFIDENTIAL
	Free    bool        // TRANSP:TRANSPARENT — does not take one's time
	// a changed occurrence of a series: the original start of the occurrence (RECURRENCE-ID; nil — the meeting itself)
	RecurrenceID *time.Time
}

// Attendee is the attendee with the address (nil — not invited).
func (in Invite) Attendee(email string) *Person {
	for i := range in.Attendees {
		if strings.EqualFold(in.Attendees[i].Email, email) {
			return &in.Attendees[i]
		}
	}
	return nil
}

// icsLine is a content line: its name, parameters and value (still escaped).
type icsLine struct {
	name   string
	params map[string]string
	value  string
}

// unfoldICS splits the text into content lines, joining the folded ones.
func unfoldICS(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

// parseICSLine: NAME;P=V;P="V,V":VALUE — the colon in quotes is not the value's.
func parseICSLine(l string) icsLine {
	quoted, colon := false, -1
	for i, c := range l {
		if c == '"' {
			quoted = !quoted
		} else if c == ':' && !quoted {
			colon = i
			break
		}
	}
	if colon < 0 {
		return icsLine{name: strings.ToUpper(l)}
	}
	head, value := l[:colon], l[colon+1:]
	parts := splitUnquoted(head, ';')
	out := icsLine{name: strings.ToUpper(strings.TrimSpace(parts[0])), params: map[string]string{}, value: value}
	for _, p := range parts[1:] {
		if k, v, ok := strings.Cut(p, "="); ok {
			out.params[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(v, `"`)
		}
	}
	return out
}

func splitUnquoted(s string, sep rune) []string {
	var out []string
	quoted, from := false, 0
	for i, c := range s {
		if c == '"' {
			quoted = !quoted
		} else if c == sep && !quoted {
			out = append(out, s[from:i])
			from = i + 1
		}
	}
	return append(out, s[from:])
}

// unescapeICS: \n, \, \; \\ of a text value.
func unescapeICS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func escapeICS(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", "").Replace(s)
}

// windowsZones are the time zones Outlook and Exchange name their own way (TZID), the usual ones.
var windowsZones = map[string]string{
	"Russian Standard Time": "Europe/Moscow", "Russia Time Zone 3": "Europe/Samara", "Ekaterinburg Standard Time": "Asia/Yekaterinburg",
	"N. Central Asia Standard Time": "Asia/Novosibirsk", "North Asia Standard Time": "Asia/Krasnoyarsk", "North Asia East Standard Time": "Asia/Irkutsk",
	"Yakutsk Standard Time": "Asia/Yakutsk", "Vladivostok Standard Time": "Asia/Vladivostok", "Kaliningrad Standard Time": "Europe/Kaliningrad",
	"Belarus Standard Time": "Europe/Minsk", "W. Europe Standard Time": "Europe/Berlin", "Romance Standard Time": "Europe/Paris",
	"Central Europe Standard Time": "Europe/Budapest", "Central European Standard Time": "Europe/Warsaw", "E. Europe Standard Time": "Europe/Chisinau",
	"FLE Standard Time": "Europe/Kiev", "GTB Standard Time": "Europe/Bucharest", "GMT Standard Time": "Europe/London", "Turkey Standard Time": "Europe/Istanbul",
	"Israel Standard Time": "Asia/Jerusalem", "Arabian Standard Time": "Asia/Dubai", "China Standard Time": "Asia/Shanghai", "Tokyo Standard Time": "Asia/Tokyo",
	"India Standard Time": "Asia/Kolkata", "Eastern Standard Time": "America/New_York", "Central Standard Time": "America/Chicago",
	"Mountain Standard Time": "America/Denver", "Pacific Standard Time": "America/Los_Angeles", "UTC": "UTC", "Coordinated Universal Time": "UTC",
}

// zoneOf: an IANA name, a Windows one, else the calendar's own VTIMEZONE (zones) at that wall time, else go24's own.
func zoneOf(tzid string, zones map[string]*icsZone, wall time.Time) *time.Location {
	tzid = strings.Trim(tzid, `"/ `)
	if tzid == "" {
		return time.Local
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	if name, ok := windowsZones[tzid]; ok {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if z := zones[tzid]; z != nil {
		if loc := z.at(wall); loc != nil {
			return loc
		}
	}
	return time.Local
}

// zoneName is the IANA name of a TZID (an IANA or a Windows one; "" — another).
func zoneName(tzid string) string {
	tzid = strings.Trim(tzid, `"/ `)
	if name, ok := windowsZones[tzid]; ok {
		tzid = name
	}
	if _, err := time.LoadLocation(tzid); err != nil || tzid == "" || tzid == "Local" {
		return ""
	}
	return tzid
}

// icsZone is a VTIMEZONE of a name go24 does not know (Outlook's «Customized Time Zone», Lotus…): its STANDARD and
// DAYLIGHT parts, so that a summer meeting gets the summer offset.
type icsZone struct{ parts []icsObservance }

// icsObservance: an offset from its DTSTART on, every year again by its RRULE (BYMONTH, BYDAY: 2SU, -1SU) or once.
type icsObservance struct {
	offset   int // seconds east of UTC (TZOFFSETTO)
	start    time.Time
	month    time.Month
	week     int // 1…5 — that weekday of the month, -1 — the last one; 0 — no yearly rule
	weekday  time.Weekday
	standard bool
}

// onset: when the observance begins in that year by its rule (as a wall time, read in UTC).
func (o icsObservance) onset(year int) time.Time {
	clock := time.Duration(o.start.Hour())*time.Hour + time.Duration(o.start.Minute())*time.Minute
	var d time.Time
	if o.week > 0 {
		d = time.Date(year, o.month, 1, 0, 0, 0, 0, time.UTC)
		d = d.AddDate(0, 0, (int(o.weekday)-int(d.Weekday())+7)%7+7*(o.week-1))
		for d.Month() != o.month { // «5SU» in a month of four Sundays — the last one
			d = d.AddDate(0, 0, -7)
		}
	} else {
		d = time.Date(year, o.month+1, 0, 0, 0, 0, 0, time.UTC)
		d = d.AddDate(0, 0, -((int(d.Weekday()) - int(o.weekday) + 7) % 7))
	}
	return d.Add(clock)
}

// at: the offset in effect at the wall time — of the part begun last before it.
func (z *icsZone) at(wall time.Time) *time.Location {
	w := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, time.UTC)
	var best *icsObservance
	var bestAt time.Time
	for i := range z.parts {
		o := &z.parts[i]
		onsets := []time.Time{o.start}
		if o.week != 0 && o.month != 0 {
			onsets = []time.Time{o.onset(w.Year() - 1), o.onset(w.Year())}
		}
		for _, t := range onsets {
			if !t.After(w) && (best == nil || t.After(bestAt)) {
				best, bestAt = o, t
			}
		}
	}
	if best == nil { // before any part: the standard one
		for i := range z.parts {
			if best == nil || z.parts[i].standard {
				best = &z.parts[i]
			}
		}
	}
	if best == nil {
		return nil
	}
	return time.FixedZone("", best.offset)
}

// icsObservancePart reads a STANDARD or DAYLIGHT part's lines.
func icsObservancePart(lines []icsLine, standard bool) (icsObservance, bool) {
	o := icsObservance{standard: standard}
	got := false
	for _, l := range lines {
		v := strings.TrimSpace(l.value)
		switch l.name {
		case "TZOFFSETTO":
			if loc := icsOffset(v); loc != nil {
				_, o.offset = time.Date(2000, 1, 1, 0, 0, 0, 0, loc).Zone()
				got = true
			}
		case "DTSTART":
			o.start, _ = time.Parse("20060102T150405", strings.TrimSuffix(v, "Z"))
		case "RRULE":
			for _, part := range strings.Split(strings.ToUpper(v), ";") {
				k, val, _ := strings.Cut(part, "=")
				switch k {
				case "BYMONTH":
					m, _ := strconv.Atoi(val)
					o.month = time.Month(m)
				case "BYDAY":
					if len(val) >= 2 {
						if wd, ok := icsWeekdays[val[len(val)-2:]]; ok {
							o.weekday = wd
							o.week, _ = strconv.Atoi(val[:len(val)-2])
							if o.week == 0 {
								o.week = 1
							}
						}
					}
				}
			}
		}
	}
	return o, got
}

var icsWeekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

// icsTime parses a DATE-TIME or a DATE (all — a whole day).
func icsTime(l icsLine, zones map[string]*icsZone) (t time.Time, day bool, ok bool) {
	v := strings.TrimSpace(l.value)
	if l.params["VALUE"] == "DATE" || len(v) == 8 {
		d, err := time.ParseInLocation("20060102", v, time.Local)
		return d, true, err == nil
	}
	if strings.HasSuffix(v, "Z") {
		d, err := time.Parse("20060102T150405Z", v)
		return d.Local(), false, err == nil
	}
	wall, err := time.Parse("20060102T150405", v)
	if err != nil {
		return wall, false, false
	}
	d, err := time.ParseInLocation("20060102T150405", v, zoneOf(l.params["TZID"], zones, wall))
	return d.Local(), false, err == nil
}

// icsDuration: P1D, PT1H30M, P1W…
func icsDuration(v string) time.Duration {
	v = strings.TrimPrefix(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(v)), "+"), "P")
	var d time.Duration
	num, inTime := 0, false
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9':
			num = num*10 + int(c-'0')
		case c == 'T':
			inTime = true
		case c == 'W':
			d += time.Duration(num) * 7 * 24 * time.Hour
			num = 0
		case c == 'D':
			d += time.Duration(num) * 24 * time.Hour
			num = 0
		case c == 'H':
			d += time.Duration(num) * time.Hour
			num = 0
		case c == 'M' && inTime:
			d += time.Duration(num) * time.Minute
			num = 0
		case c == 'S':
			d += time.Duration(num) * time.Second
			num = 0
		}
	}
	return d
}

// icsOffset: +0300, -0500 as a fixed zone.
func icsOffset(v string) *time.Location {
	v = strings.TrimSpace(v)
	if len(v) < 5 || (v[0] != '+' && v[0] != '-') {
		return nil
	}
	h, err1 := strconv.Atoi(v[1:3])
	m, err2 := strconv.Atoi(v[3:5])
	if err1 != nil || err2 != nil {
		return nil
	}
	sec := h*3600 + m*60
	if v[0] == '-' {
		sec = -sec
	}
	return time.FixedZone(v, sec)
}

func icsPerson(l icsLine) Person {
	addr := strings.TrimSpace(l.value)
	if i := strings.Index(strings.ToLower(addr), "mailto:"); i >= 0 {
		addr = addr[i+len("mailto:"):]
	}
	return Person{Email: strings.ToLower(strings.TrimSpace(addr)), Name: unescapeICS(l.params["CN"]),
		Status: strings.ToLower(l.params["PARTSTAT"]), RSVP: strings.EqualFold(l.params["RSVP"], "TRUE")}
}

// maxICS bounds the calendar text go24 keeps of a letter.
const maxICS = 256 << 10

// ParseInvite reads the meeting of a calendar text; ok — there is one with a UID.
func ParseInvite(text string) (Invite, bool) {
	method, events, zones := readICS(text)
	if len(events) == 0 {
		return Invite{}, false
	}
	ev := events[0] // the meeting itself, not a changed occurrence of its series (RECURRENCE-ID)
	for _, e := range events {
		if isMaster(e) {
			ev = e
			break
		}
	}
	return inviteOf(ev, zones, method)
}

// ParseCalendar reads every meeting of a calendar file (.ics), as an import takes them: the changed occurrences of a
// series (RECURRENCE-ID) are left out, as are the meetings without a UID or a start.
func ParseCalendar(text string) []Invite {
	method, events, zones := readICS(text)
	var out []Invite
	for _, e := range events {
		if !isMaster(e) {
			continue
		}
		if in, ok := inviteOf(e, zones, method); ok {
			out = append(out, in)
		}
	}
	return out
}

// ParseResource reads the meetings of a calendar file with their changed occurrences (RecurrenceID set), as a calendar
// program puts one meeting into a CalDAV collection.
func ParseResource(text string) []Invite {
	method, events, zones := readICS(text)
	var out []Invite
	for _, e := range events {
		if in, ok := inviteOf(e, zones, method); ok {
			out = append(out, in)
		}
	}
	return out
}

func isMaster(ev []icsLine) bool {
	for _, l := range ev {
		if l.name == "RECURRENCE-ID" {
			return false
		}
	}
	return true
}

// readICS splits a calendar text into its METHOD, VEVENTs (the lines of each; the TRIGGER of its VALARMs as
// X-GO24-ALARM) and time zones.
func readICS(text string) (method string, events [][]icsLine, zones map[string]*icsZone) {
	zones = map[string]*icsZone{} // VTIMEZONE: TZID → its parts
	var stack []string
	var tzid string
	var part []icsLine // the STANDARD or DAYLIGHT being read
	for _, raw := range unfoldICS(text) {
		l := parseICSLine(raw)
		switch l.name {
		case "BEGIN":
			c := strings.ToUpper(strings.TrimSpace(l.value))
			stack = append(stack, c)
			switch c {
			case "VEVENT":
				events = append(events, nil)
			case "STANDARD", "DAYLIGHT":
				part = nil
			}
			continue
		case "END":
			if len(stack) > 0 {
				c := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if (c == "STANDARD" || c == "DAYLIGHT") && tzid != "" {
					if o, ok := icsObservancePart(part, c == "STANDARD"); ok {
						if zones[tzid] == nil {
							zones[tzid] = &icsZone{}
						}
						zones[tzid].parts = append(zones[tzid].parts, o)
					}
				}
			}
			continue
		}
		top := ""
		if len(stack) > 0 {
			top = stack[len(stack)-1]
		}
		switch top {
		case "VCALENDAR":
			if l.name == "METHOD" {
				method = strings.ToUpper(strings.TrimSpace(l.value))
			}
		case "VTIMEZONE":
			if l.name == "TZID" {
				tzid = strings.TrimSpace(l.value)
			}
		case "STANDARD", "DAYLIGHT":
			part = append(part, l)
		case "VEVENT":
			events[len(events)-1] = append(events[len(events)-1], l)
		case "VALARM":
			if l.name == "TRIGGER" && len(events) > 0 && slices.Contains(stack, "VEVENT") {
				l.name = "X-GO24-ALARM"
				events[len(events)-1] = append(events[len(events)-1], l)
			}
		}
	}
	return method, events, zones
}

// inviteOf is the meeting of a VEVENT's lines; ok — it has a UID and a start.
func inviteOf(ev []icsLine, zones map[string]*icsZone, method string) (Invite, bool) {
	in := Invite{Method: method}
	count := 0 // RRULE's COUNT: the series ends at its last occurrence
	var dtend *icsLine
	var duration string
	for _, l := range ev {
		v := l.value
		switch l.name {
		case "UID":
			in.UID = strings.TrimSpace(v)
		case "SEQUENCE":
			in.Sequence, _ = strconv.Atoi(strings.TrimSpace(v))
		case "SUMMARY":
			in.Summary = unescapeICS(v)
		case "LOCATION":
			in.Location = unescapeICS(v)
		case "DESCRIPTION":
			in.Description = unescapeICS(v)
		case "COMMENT":
			in.Comment = unescapeICS(v)
		case "STATUS":
			in.Cancelled = strings.EqualFold(strings.TrimSpace(v), "CANCELLED")
		case "DTSTART":
			if t, day, ok := icsTime(l, zones); ok {
				in.Start, in.AllDay, in.TZ = t, day, zoneName(l.params["TZID"])
			}
		case "DTEND":
			x := l
			dtend = &x
		case "RECURRENCE-ID":
			if t, _, ok := icsTime(l, zones); ok {
				in.RecurrenceID = &t
			}
		case "DURATION":
			duration = v
		case "ORGANIZER":
			in.Organizer = icsPerson(l)
		case "ATTENDEE":
			in.Attendees = append(in.Attendees, icsPerson(l))
		case "RRULE":
			in.Repeat, in.Every, in.Until, count, in.Days = parseRRule(v)
		case "EXDATE":
			for _, x := range strings.Split(v, ",") {
				if t, _, ok := icsTime(icsLine{name: l.name, params: l.params, value: x}, zones); ok {
					in.Skip = append(in.Skip, t)
				}
			}
		case "CLASS":
			in.Private = strings.EqualFold(strings.TrimSpace(v), "PRIVATE") || strings.EqualFold(strings.TrimSpace(v), "CONFIDENTIAL")
		case "TRANSP":
			in.Free = strings.EqualFold(strings.TrimSpace(v), "TRANSPARENT")
		case "X-GO24-ALARM": // relative to the start only: -PT15M, PT0S
			if l.params["RELATED"] != "END" && l.params["VALUE"] != "DATE-TIME" {
				if d := strings.TrimSpace(v); strings.HasPrefix(d, "-") || icsDuration(d) == 0 {
					in.Alarms = append(in.Alarms, int(icsDuration(strings.TrimPrefix(d, "-"))/time.Minute))
				}
			}
		case "X-GOOGLE-CONFERENCE", "X-MICROSOFT-SKYPETEAMSMEETINGURL", "URL":
			if in.Join == "" && strings.HasPrefix(strings.TrimSpace(v), "https://") {
				in.Join = strings.TrimSpace(v)
			}
		}
	}
	if in.Join == "" { // Teams, Zoom, Telemost put the link into the place or the text only
		in.Join = joinLink(in.Location + "\n" + in.Description)
	}
	if in.UID == "" || in.Start.IsZero() {
		return in, false
	}
	if count > 0 && in.Repeat != "" && in.Until == nil {
		in.Count = min(count, 999)
		in.Until = lastOccurrence(in)
	}
	switch {
	case dtend != nil:
		if t, _, ok := icsTime(*dtend, zones); ok {
			in.End = t
		}
	case duration != "":
		in.End = in.Start.Add(icsDuration(duration))
	}
	if in.AllDay { // DTEND of a day is the next day's start; go24 ends a day at 23:59
		if in.End.After(in.Start) {
			in.End = in.End.AddDate(0, 0, -1)
		} else {
			in.End = in.Start
		}
		in.End = time.Date(in.End.Year(), in.End.Month(), in.End.Day(), 23, 59, 0, 0, time.Local)
	}
	if in.End.Before(in.Start) {
		in.End = in.Start
	}
	if in.Method == "" {
		in.Method = "PUBLISH"
	}
	return in, true
}

// joinLink: the first link of an online meeting service (Teams, Zoom, Telemost…) in a meeting's place or text.
var joinLink = func() func(string) string {
	re := regexp.MustCompile(`https://(?:[a-z0-9-]+\.)*(?:meet\.google\.com|teams\.microsoft\.com|teams\.live\.com|zoom\.us|telemost\.yandex\.ru|meet\.jit\.si|webex\.com)/[^\s<>"'\]\)]*`)
	return func(text string) string { return re.FindString(text) }
}()

// parseRRule maps the usual rules (FREQ, INTERVAL, UNTIL, COUNT, BYDAY of a week) to go24's repetition; others — once.
// «Every weekday» (DAILY with BYDAY) is a weekly one on those days.
func parseRRule(v string) (string, int, *time.Time, int, []int) {
	var freq string
	every, count := 1, 0
	var until *time.Time
	var days []int
	for _, p := range strings.Split(v, ";") {
		k, val, _ := strings.Cut(p, "=")
		switch strings.ToUpper(k) {
		case "FREQ":
			freq = map[string]string{"DAILY": "daily", "WEEKLY": "weekly", "MONTHLY": "monthly", "YEARLY": "yearly"}[strings.ToUpper(val)]
		case "INTERVAL":
			if n, err := strconv.Atoi(val); err == nil && n > 0 && n <= 365 {
				every = n
			}
		case "COUNT":
			if n, err := strconv.Atoi(val); err == nil && n > 0 && n <= 5000 {
				count = n
			}
		case "UNTIL":
			if t, _, ok := icsTime(icsLine{value: val, params: map[string]string{}}, nil); ok {
				d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
				until = &d
			}
		case "BYDAY":
			for _, code := range strings.Split(strings.ToUpper(val), ",") {
				// a plain day only: a numbered one (2TU — the second Tuesday) is of a month or a year
				if d := slices.Index([]string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}, strings.TrimSpace(code)); d >= 0 && !slices.Contains(days, d+1) {
					days = append(days, d+1)
				}
			}
		}
	}
	if freq == "daily" && len(days) > 0 && every == 1 {
		freq = "weekly"
	}
	if freq != "weekly" {
		days = nil
	}
	slices.Sort(days)
	return freq, every, until, count, days
}

// lastOccurrence is the day of the last meeting of a series that ends after Count meetings.
func lastOccurrence(in Invite) *time.Time {
	every := max(in.Every, 1)
	first := in.Start
	if l, err := time.LoadLocation(in.TZ); err == nil && in.TZ != "" && !in.AllDay {
		first = first.In(l)
	}
	var last time.Time
	for k, n := 0, 0; n < in.Count && k < 5000*max(len(in.Days), 1); k++ {
		var t time.Time
		switch {
		case in.Repeat == "weekly" && len(in.Days) > 0:
			week, day := k/len(in.Days), in.Days[k%len(in.Days)]
			if t = first.AddDate(0, 0, 7*week*every+day-((int(first.Weekday())+6)%7+1)); t.Before(first) {
				continue
			}
		case in.Repeat == "daily":
			t = first.AddDate(0, 0, k*every)
		case in.Repeat == "weekly":
			t = first.AddDate(0, 0, 7*k*every)
		case in.Repeat == "monthly":
			t = first.AddDate(0, k*every, 0)
		default:
			t = first.AddDate(k*every, 0, 0)
		}
		n, last = n+1, t.In(time.Local)
	}
	d := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.Local)
	return &d
}

// ---------- writing ----------

// foldICS folds a content line at 75 octets, not inside a character.
func foldICS(b *strings.Builder, line string) {
	for len(line) > 75 {
		cut := 75
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		b.WriteString(line[:cut] + "\r\n ")
		line = line[cut:]
	}
	b.WriteString(line + "\r\n")
}

func icsUTC(t time.Time) string { return t.UTC().Format("20060102T150405Z") }

func icsParam(v string) string {
	v = strings.NewReplacer(`"`, "'", "\r", "", "\n", " ").Replace(v)
	if strings.ContainsAny(v, ";:,") {
		return `"` + v + `"`
	}
	return v
}

func icsPersonLine(name string, p Person, attendee bool) string {
	var b strings.Builder
	b.WriteString(name)
	if p.Name != "" {
		b.WriteString(";CN=" + icsParam(p.Name))
	}
	if attendee {
		st := strings.ToUpper(p.Status)
		if st == "" || st == "INVITED" {
			st = "NEEDS-ACTION"
		}
		b.WriteString(";ROLE=REQ-PARTICIPANT;PARTSTAT=" + st)
		if p.RSVP {
			b.WriteString(";RSVP=TRUE")
		}
	}
	b.WriteString(":mailto:" + p.Email)
	return b.String()
}

// BuildICS writes a meeting as a calendar of the method (REQUEST, REPLY, CANCEL, COUNTER, DECLINECOUNTER); now is
// its DTSTAMP.
func BuildICS(in Invite, method string, now time.Time) string {
	var b strings.Builder
	w := func(s string) { foldICS(&b, s) }
	w("BEGIN:VCALENDAR")
	w("PRODID:-//go24//go24//EN")
	w("VERSION:2.0")
	w("CALSCALE:GREGORIAN")
	w("METHOD:" + method)
	w("BEGIN:VEVENT")
	w("UID:" + in.UID)
	w("SEQUENCE:" + strconv.Itoa(in.Sequence))
	w("DTSTAMP:" + icsUTC(now))
	if in.AllDay {
		w("DTSTART;VALUE=DATE:" + in.Start.Format("20060102"))
		w("DTEND;VALUE=DATE:" + in.End.AddDate(0, 0, 1).Format("20060102"))
	} else {
		w("DTSTART:" + icsUTC(in.Start))
		w("DTEND:" + icsUTC(in.End))
	}
	if in.Summary != "" {
		w("SUMMARY:" + escapeICS(in.Summary))
	}
	if in.Location != "" {
		w("LOCATION:" + escapeICS(in.Location))
	}
	answer := method == "REPLY" || method == "DECLINECOUNTER"
	if in.Description != "" && !answer {
		w("DESCRIPTION:" + escapeICS(in.Description))
	}
	if in.Comment != "" {
		w("COMMENT:" + escapeICS(in.Comment))
	}
	if in.Repeat != "" && !answer {
		rule := "RRULE:FREQ=" + strings.ToUpper(in.Repeat) + ";INTERVAL=" + strconv.Itoa(max(in.Every, 1))
		if len(in.Days) > 0 && in.Repeat == "weekly" {
			var codes []string
			for _, d := range in.Days {
				if d >= 1 && d <= 7 {
					codes = append(codes, [...]string{"MO", "TU", "WE", "TH", "FR", "SA", "SU"}[d-1])
				}
			}
			rule += ";BYDAY=" + strings.Join(codes, ",")
		}
		if in.Count > 0 {
			rule += ";COUNT=" + strconv.Itoa(in.Count)
		} else if in.Until != nil {
			rule += ";UNTIL=" + icsUTC(time.Date(in.Until.Year(), in.Until.Month(), in.Until.Day(), 23, 59, 59, 0, time.Local))
		}
		w(rule)
	}
	if in.Organizer.Email != "" {
		w(icsPersonLine("ORGANIZER", in.Organizer, false))
	}
	for _, a := range in.Attendees {
		w(icsPersonLine("ATTENDEE", a, true))
	}
	switch {
	case method == "CANCEL" || in.Cancelled:
		w("STATUS:CANCELLED")
	case method == "REQUEST":
		w("STATUS:CONFIRMED")
		w("TRANSP:OPAQUE")
	}
	w("END:VEVENT")
	w("END:VCALENDAR")
	return b.String()
}

// Reply is the answer of an attendee to an invitation (status: accepted, tentative, declined).
func Reply(in Invite, me Person, status, comment string) Invite {
	r := Invite{UID: in.UID, Sequence: in.Sequence, Summary: in.Summary, Location: in.Location, Start: in.Start, End: in.End,
		AllDay: in.AllDay, Organizer: in.Organizer, Comment: comment}
	me.Status, me.RSVP = status, false
	r.Attendees = []Person{me}
	return r
}

// Counter is an attendee's proposal of a new time (start, end) for an invitation, with their answer (tentative,
// declined) and words, as Outlook's «Предложить новое время» sends it.
func Counter(in Invite, me Person, status, comment string, start, end time.Time) Invite {
	c := Reply(in, me, status, comment)
	c.Start, c.End, c.AllDay = start, end, false
	c.Repeat, c.Every, c.Until, c.Days, c.Count = in.Repeat, in.Every, in.Until, in.Days, in.Count
	return c
}

// InviteUID is the UID of a go24 meeting sent by mail.
func InviteUID(eventID, host string) string {
	if host == "" {
		host = "go24"
	}
	return fmt.Sprintf("%s@%s", eventID, host)
}
