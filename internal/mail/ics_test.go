package mail

import (
	"strings"
	"testing"
	"time"
)

// outlookInvite is an invitation as Outlook sends it: a Windows time zone, folded lines, escaped text, a series with
// a changed occurrence before the meeting itself.
const outlookInvite = "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nPRODID:Microsoft Exchange Server 2010\r\nVERSION:2.0\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Russian Standard Time\r\nBEGIN:STANDARD\r\nDTSTART:16010101T000000\r\nTZOFFSETFROM:+0300\r\nTZOFFSETTO:+0300\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nRECURRENCE-ID;TZID=Russian Standard Time:20261027T100000\r\nUID:040000008200E0007\r\nSUMMARY:Перенесённая\r\n" +
	"DTSTART;TZID=Russian Standard Time:20261027T120000\r\nDTEND;TZID=Russian Standard Time:20261027T130000\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nORGANIZER;CN=\"Иванов, Иван\":mailto:Ivan@Client.test\r\n" +
	"ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE;CN=Анна:mailto:anna@go24.test\r\n" +
	"ATTENDEE;ROLE=OPT-PARTICIPANT;PARTSTAT=ACCEPTED;CN=Пётр:mailto:petr@client.test\r\n" +
	"DESCRIPTION;LANGUAGE=ru-RU:Повестка:\\n1. Сроки\\, бюджет\\; риски\\n\r\nRRULE:FREQ=WEEKLY;UNTIL=20261130T070000Z;INTERVAL=2;BYDAY=TU\r\n" +
	"SUMMARY;LANGUAGE=ru-RU:Планёрка по проекту «Склад» — длинное название, чтобы строка была сл\r\n ожена\r\n" +
	"DTSTART;TZID=Russian Standard Time:20261020T100000\r\nDTEND;TZID=Russian Standard Time:20261020T113000\r\n" +
	"UID:040000008200E0007\r\nSEQUENCE:2\r\nLOCATION;LANGUAGE=ru-RU:Переговорная 3\r\nSTATUS:CONFIRMED\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestParseInvite(t *testing.T) {
	in, ok := ParseInvite(outlookInvite)
	if !ok {
		t.Fatal("not parsed")
	}
	msk := time.FixedZone("MSK", 3*3600)
	if in.Method != "REQUEST" || in.UID != "040000008200E0007" || in.Sequence != 2 || in.Location != "Переговорная 3" || in.Cancelled {
		t.Fatalf("invite = %+v", in)
	}
	if !in.Start.Equal(time.Date(2026, 10, 20, 10, 0, 0, 0, msk)) || !in.End.Equal(time.Date(2026, 10, 20, 11, 30, 0, 0, msk)) || in.AllDay || in.TZ != "Europe/Moscow" {
		t.Fatalf("time = %v — %v", in.Start, in.End)
	}
	if !strings.HasSuffix(in.Summary, "сложена") || in.Description != "Повестка:\n1. Сроки, бюджет; риски\n" {
		t.Fatalf("text = %q / %q", in.Summary, in.Description)
	}
	if in.Organizer.Email != "ivan@client.test" || in.Organizer.Name != "Иванов, Иван" || len(in.Attendees) != 2 {
		t.Fatalf("people = %+v %+v", in.Organizer, in.Attendees)
	}
	if a := in.Attendee("ANNA@go24.test"); a == nil || a.Status != "needs-action" || !a.RSVP || a.Name != "Анна" {
		t.Fatalf("me = %+v", a)
	}
	if in.Repeat != "weekly" || in.Every != 2 || in.Until == nil || in.Until.Day() != 30 {
		t.Fatalf("repeat = %s %d %v", in.Repeat, in.Every, in.Until)
	}

	// a whole day (DTEND is the next day), a duration, a cancel, nothing
	day, ok := ParseInvite("BEGIN:VCALENDAR\nMETHOD:CANCEL\nBEGIN:VEVENT\nUID:x\nDTSTART;VALUE=DATE:20261101\nDTEND;VALUE=DATE:20261103\nSTATUS:CANCELLED\nEND:VEVENT\nEND:VCALENDAR")
	if !ok || !day.AllDay || day.Start.Day() != 1 || day.End.Day() != 2 || day.End.Hour() != 23 || !day.Cancelled || day.Method != "CANCEL" {
		t.Fatalf("day = %+v", day)
	}
	dur, ok := ParseInvite("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:y\nDTSTART:20261101T090000Z\nDURATION:PT1H30M\nEND:VEVENT\nEND:VCALENDAR")
	if !ok || dur.End.Sub(dur.Start) != 90*time.Minute || dur.Method != "PUBLISH" {
		t.Fatalf("duration = %+v", dur)
	}
	if _, ok := ParseInvite("BEGIN:VCALENDAR\nBEGIN:VTODO\nUID:z\nEND:VTODO\nEND:VCALENDAR"); ok {
		t.Fatal("a task is not a meeting")
	}
	if _, ok := ParseInvite(""); ok {
		t.Fatal("empty")
	}
}

// customZone is Outlook's «Customized Time Zone» (a name go24 does not know): the summer offset from the last Sunday
// of March, the winter one from the last Sunday of October.
func customZone(start string) string {
	return "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VTIMEZONE\r\nTZID:Customized Time Zone\r\n" +
		"BEGIN:STANDARD\r\nDTSTART:16010101T030000\r\nTZOFFSETFROM:+0200\r\nTZOFFSETTO:+0100\r\nRRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=-1SU;BYMONTH=10\r\nEND:STANDARD\r\n" +
		"BEGIN:DAYLIGHT\r\nDTSTART:16010101T020000\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\nRRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=-1SU;BYMONTH=3\r\nEND:DAYLIGHT\r\n" +
		"END:VTIMEZONE\r\nBEGIN:VEVENT\r\nUID:c1\r\nDTSTART;TZID=Customized Time Zone:" + start + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func TestInviteZones(t *testing.T) {
	for _, c := range []struct {
		start string
		want  time.Time
	}{
		{"20260715T100000", time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)},  // summer: +2
		{"20261215T100000", time.Date(2026, 12, 15, 9, 0, 0, 0, time.UTC)}, // winter: +1
		{"20260329T030000", time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC)},  // just after the change to summer
		{"20261025T040000", time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)}, // just after the change to winter
		{"20270110T090000", time.Date(2027, 1, 10, 8, 0, 0, 0, time.UTC)},  // the next year, winter
	} {
		in, ok := ParseInvite(customZone(c.start))
		if !ok || !in.Start.Equal(c.want) || in.TZ != "" { // a zone of its own: the time only
			t.Errorf("%s: %v, want %v", c.start, in.Start.UTC(), c.want)
		}
	}

	// the southern half: summer from the first Sunday of October to the first Sunday of April (+11 / +10)
	south := "BEGIN:VCALENDAR\nBEGIN:VTIMEZONE\nTZID:Sydney custom\n" +
		"BEGIN:STANDARD\nDTSTART:16010101T030000\nTZOFFSETTO:+1000\nRRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=4\nEND:STANDARD\n" +
		"BEGIN:DAYLIGHT\nDTSTART:16010101T020000\nTZOFFSETTO:+1100\nRRULE:FREQ=YEARLY;BYDAY=1SU;BYMONTH=10\nEND:DAYLIGHT\n" +
		"END:VTIMEZONE\nBEGIN:VEVENT\nUID:s\nDTSTART;TZID=Sydney custom:20270115T110000\n" +
		"DTEND;TZID=Sydney custom:20270615T110000\nEND:VEVENT\nEND:VCALENDAR"
	in, ok := ParseInvite(south)
	if !ok || !in.Start.Equal(time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC)) || !in.End.Equal(time.Date(2027, 6, 15, 1, 0, 0, 0, time.UTC)) {
		t.Errorf("south: %v — %v", in.Start.UTC(), in.End.UTC())
	}

	// Google Calendar: an IANA zone, attendees with extra parameters, a Meet link
	google := "BEGIN:VCALENDAR\r\nPRODID:-//Google Inc//Google Calendar 70.9054//EN\r\nVERSION:2.0\r\nCALSCALE:GREGORIAN\r\nMETHOD:REQUEST\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/Amsterdam\r\nX-LIC-LOCATION:Europe/Amsterdam\r\nBEGIN:DAYLIGHT\r\nTZOFFSETFROM:+0100\r\nTZOFFSETTO:+0200\r\n" +
		"TZNAME:CEST\r\nDTSTART:19700329T020000\r\nRRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU\r\nEND:DAYLIGHT\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nDTSTART;TZID=Europe/Amsterdam:20261022T150000\r\nDTEND;TZID=Europe/Amsterdam:20261022T153000\r\nDTSTAMP:20261020T120000Z\r\n" +
		"ORGANIZER;CN=Boris GB:mailto:boris@gmail.test\r\nUID:3k1h0abc@google.com\r\n" +
		"ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;RSVP=TRUE;CN=anna@go24.test;X-NUM-GUESTS=0:mailto:anna@go24.test\r\n" +
		"ATTENDEE;CUTYPE=INDIVIDUAL;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED;RSVP=TRUE;CN=Boris GB;X-NUM-GUESTS=0:mailto:boris@gmail.test\r\n" +
		"X-GOOGLE-CONFERENCE:https://meet.google.com/abc-defg-hij\r\nDESCRIPTION:Обсудим почту\r\nLOCATION:\r\nSEQUENCE:0\r\nSTATUS:CONFIRMED\r\n" +
		"SUMMARY:Созвон\r\nTRANSP:OPAQUE\r\nBEGIN:VALARM\r\nACTION:EMAIL\r\nDESCRIPTION:This is an event reminder\r\nTRIGGER:-P0DT0H10M0S\r\nEND:VALARM\r\n" +
		"END:VEVENT\r\nEND:VCALENDAR\r\n"
	in, ok = ParseInvite(google)
	if !ok || !in.Start.Equal(time.Date(2026, 10, 22, 13, 0, 0, 0, time.UTC)) || in.End.Sub(in.Start) != 30*time.Minute ||
		in.Summary != "Созвон" || in.Description != "Обсудим почту" || in.Organizer.Name != "Boris GB" {
		t.Fatalf("google = %+v", in)
	}
	if a := in.Attendee("anna@go24.test"); a == nil || a.Status != "needs-action" || !a.RSVP {
		t.Fatalf("google attendee = %+v", in.Attendees)
	}
	if in.Join != "https://meet.google.com/abc-defg-hij" {
		t.Fatalf("google join = %q", in.Join)
	}
	// Teams keeps its link in the text only; a link of another site is no meeting
	teams, _ := ParseInvite("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:t\nDTSTART:20261101T090000Z\nLOCATION:Microsoft Teams Meeting\n" +
		"DESCRIPTION:Join: <https://teams.microsoft.com/l/meetup-join/19%3ameeting_x%40thread.v2/0?context=%7b%7d>\\nhttps://example.com/x\nEND:VEVENT\nEND:VCALENDAR")
	if teams.Join != "https://teams.microsoft.com/l/meetup-join/19%3ameeting_x%40thread.v2/0?context=%7b%7d" {
		t.Fatalf("teams join = %q", teams.Join)
	}
	if other, _ := ParseInvite("BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:o\nDTSTART:20261101T090000Z\nURL:http://example.com/\nDESCRIPTION:https://example.com/x\nEND:VEVENT\nEND:VCALENDAR"); other.Join != "" {
		t.Fatalf("other join = %q", other.Join)
	}
}

func TestBuildICS(t *testing.T) {
	in, _ := ParseInvite(outlookInvite)
	me := Person{Email: "anna@go24.test", Name: "Анна; Смирнова"}
	r := Reply(in, me, "tentative", "Опоздаю, на 10 минут")
	text := BuildICS(r, "REPLY", time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC))
	for _, l := range strings.Split(strings.TrimSuffix(text, "\r\n"), "\r\n") {
		if len(l) > 75 {
			t.Fatalf("line not folded: %q", l)
		}
	}
	back, ok := ParseInvite(text)
	if !ok || back.Method != "REPLY" || back.UID != in.UID || back.Sequence != 2 || back.Comment != "Опоздаю, на 10 минут" || !back.Start.Equal(in.Start) {
		t.Fatalf("reply = %+v", back)
	}
	if len(back.Attendees) != 1 || back.Attendees[0].Status != "tentative" || back.Attendees[0].Name != "Анна; Смирнова" || back.Organizer.Email != "ivan@client.test" {
		t.Fatalf("reply people = %+v", back.Attendees)
	}
	if !strings.Contains(text, "DTSTAMP:20261019T080000Z") || strings.Contains(text, "RRULE") || strings.Contains(text, "DESCRIPTION") {
		t.Fatalf("reply = %s", text)
	}
	until := time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local)
	req := Invite{UID: "e1@go24.test", Summary: "Обзор", Start: time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local), AllDay: true,
		End: time.Date(2026, 11, 2, 23, 59, 0, 0, time.Local), Repeat: "monthly", Every: 1, Until: &until,
		Organizer: me, Attendees: []Person{{Email: "x@y.test", RSVP: true}}}
	text = BuildICS(req, "REQUEST", time.Now())
	back, ok = ParseInvite(text)
	if !ok || !back.AllDay || back.End.Day() != 2 || back.Repeat != "monthly" || back.Until == nil || back.Attendees[0].Status != "needs-action" || !back.Attendees[0].RSVP {
		t.Fatalf("request = %+v\n%s", back, text)
	}
	if !strings.Contains(text, "DTEND;VALUE=DATE:20261103") || !strings.Contains(text, "STATUS:CONFIRMED") {
		t.Fatalf("request = %s", text)
	}
	if c := BuildICS(req, "CANCEL", time.Now()); !strings.Contains(c, "STATUS:CANCELLED") || !strings.Contains(c, "METHOD:CANCEL") {
		t.Fatalf("cancel = %s", c)
	}
	// weekly on Mon and Thu, eight meetings: BYDAY and COUNT go and come back, Until is the day of the last
	req = Invite{UID: "e2@go24.test", Summary: "Йога", Start: time.Date(2026, 10, 19, 18, 0, 0, 0, time.Local),
		End: time.Date(2026, 10, 19, 19, 0, 0, 0, time.Local), Repeat: "weekly", Every: 1, Days: []int{1, 4}, Count: 8, Until: &until}
	text = BuildICS(req, "PUBLISH", time.Now())
	back, ok = ParseInvite(text)
	if !strings.Contains(text, "RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,TH;COUNT=8\r\n") || !ok || back.Count != 8 ||
		len(back.Days) != 2 || back.Days[1] != 4 || back.Until == nil || back.Until.Format("2006-01-02") != "2026-11-12" {
		t.Fatalf("weekly on days = %+v\n%s", back, text)
	}
	// «every weekday» of Google: DAILY with BYDAY is a weekly one; a numbered day (a month's) is no day of a week
	if f, _, _, _, days := parseRRule("FREQ=DAILY;BYDAY=MO,TU,WE,TH,FR"); f != "weekly" || len(days) != 5 {
		t.Fatalf("every weekday: %s %v", f, days)
	}
	if f, _, _, _, days := parseRRule("FREQ=MONTHLY;BYDAY=2TU"); f != "monthly" || days != nil {
		t.Fatalf("second Tuesday: %s %v", f, days)
	}
}

// TestComposeCalendar: a letter with a meeting carries it beside its text, as Outlook's do.
func TestComposeCalendar(t *testing.T) {
	ics := BuildICS(Invite{UID: "u", Summary: "S", Start: time.Now(), End: time.Now()}, "REQUEST", time.Now())
	msg, _ := compose(envelope{Host: "go24.test", Now: time.Now()}, Letter{Subject: "S", Text: "Приходите", calendar: ics,
		Files: []Attachment{{Name: "invite.ics", ContentType: "application/ics", Data: []byte(ics)}}})
	p := parseFull(strings.NewReader(string(msg)))
	if p.body != "Приходите" || p.ics == "" {
		t.Fatalf("parsed = %q %q", p.body, p.ics)
	}
	if in, ok := ParseInvite(p.ics); !ok || in.UID != "u" || in.Method != "REQUEST" {
		t.Fatalf("invite = %+v", in)
	}
	if !strings.Contains(string(msg), "text/calendar; charset=utf-8; method=REQUEST") {
		t.Fatal("no calendar part")
	}
}

// TestParseCalendar: a file exported from a calendar program — every meeting of it, not the changed occurrence of a
// series; the deleted occurrences, the reminders (before the start only), a private and a free meeting.
func TestParseCalendar(t *testing.T) {
	text := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Google Inc//Google Calendar 70.9054//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:a@google.com\r\nSUMMARY:Планёрка\r\nDTSTART:20261026T080000Z\r\nDTEND:20261026T090000Z\r\n" +
		"RRULE:FREQ=WEEKLY\r\nEXDATE:20261102T080000Z,20261109T080000Z\r\nCLASS:PRIVATE\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-P1D\r\nEND:VALARM\r\n" +
		"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER;RELATED=END:PT5M\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:a@google.com\r\nRECURRENCE-ID:20261116T080000Z\r\nSUMMARY:Перенесена\r\nDTSTART:20261116T100000Z\r\nDTEND:20261116T110000Z\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:c@google.com\r\nSUMMARY:Три раза\r\nDTSTART:20261026T080000Z\r\nRRULE:FREQ=WEEKLY;INTERVAL=2;COUNT=3\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:b@google.com\r\nSUMMARY:Отпуск\r\nDTSTART;VALUE=DATE:20261201\r\nDTEND;VALUE=DATE:20261203\r\nTRANSP:TRANSPARENT\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nSUMMARY:Без UID\r\nDTSTART:20261201T080000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	list := ParseCalendar(text)
	if len(list) != 3 || list[0].UID != "a@google.com" || list[2].UID != "b@google.com" {
		t.Fatalf("list = %+v", list)
	}
	a, c, b := list[0], list[1], list[2]
	if c.Repeat != "weekly" || c.Every != 2 || c.Until == nil || c.Until.Format("2006-01-02") != "2026-11-23" { // 26.10, 9.11, 23.11
		t.Fatalf("c = %+v", c)
	}
	if a.Summary != "Планёрка" || a.Repeat != "weekly" || !a.Private || a.Free || len(a.Skip) != 2 ||
		!a.Skip[1].Equal(time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("a = %+v", a)
	}
	if len(a.Alarms) != 2 || a.Alarms[0] != 15 || a.Alarms[1] != 24*60 {
		t.Fatalf("alarms = %v", a.Alarms)
	}
	if !b.AllDay || !b.Free || b.Private || b.End.Day() != 2 {
		t.Fatalf("b = %+v", b)
	}
	if in, ok := ParseInvite(text); !ok || in.Summary != "Планёрка" { // an invitation is still the first meeting
		t.Fatalf("invite = %+v", in)
	}
}
