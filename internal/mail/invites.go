package mail

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Invitations in the mail, as Outlook handles them: a letter's meeting is answered «Принять», «Под вопросом» or
// «Отклонить» — with or without a letter to the organizer (iTIP REPLY), with the user's words; the accepted meeting
// is kept in the user's go24 calendar. An update (a later SEQUENCE) moves it there by itself and asks again; a
// cancel marks it cancelled until the user removes it. «Предложить новое время» answers «Под вопросом» or «Отклонить»
// and proposes the organizer another time (iTIP COUNTER); the organizer accepts the proposal (the meeting moves, the
// invited get it again) or declines it (DECLINECOUNTER). Answers to go24's own meetings sent by mail
// (event_externals) are taken in as they come.

// Answers to an invitation; Cancelled — the user removed a cancelled meeting from the calendar.
var Answers = []string{"accepted", "tentative", "declined"}

// InviteState is the user's answer to a meeting: the version answered, the go24 meeting it is kept as.
type InviteState struct {
	Status   string // "" — not answered
	Sequence int
	EventID  string
	// the new time the user proposed (nil — none, or the organizer declined it or sent the meeting again)
	ProposedStart, ProposedEnd *time.Time
}

// calendarPart is the text of a letter with its meeting beside it (multipart/alternative: text, HTML, calendar).
func calendarPart(now time.Time, l Letter) (string, []byte) {
	ct, body := textPart(l.Text)
	parts := []part{{ct: ct, cte: "quoted-printable", body: body}}
	if l.HTML != "" {
		hct, hbody := htmlPart(l.HTML)
		parts = append(parts, part{ct: hct, cte: "quoted-printable", body: hbody})
	}
	method := "REQUEST"
	if in, ok := ParseInvite(l.calendar); ok {
		method = in.Method
	}
	enc := base64.StdEncoding.EncodeToString([]byte(l.calendar))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	parts = append(parts, part{ct: "text/calendar; charset=utf-8; method=" + method, cte: "base64", body: []byte(b.String())})
	return multi("alternative", now, parts, "")
}

// SendCalendar sends a meeting (REQUEST, CANCEL) or an answer (REPLY) from the user's mailbox to the addresses; refs —
// the conversation it joins.
func (s *Store) SendCalendar(ctx context.Context, userID, mailboxID string, to []string, subject, text string, in Invite, method string, refs []string) error {
	if len(to) == 0 {
		return nil
	}
	in.Method = method
	ics := BuildICS(in, method, time.Now())
	l := Letter{To: strings.Join(to, ", "), Subject: subject, Text: text, Refs: refs, calendar: ics,
		Files: []Attachment{{Name: "invite.ics", ContentType: "application/ics", Data: []byte(ics)}}}
	if method == "REPLY" { // an answer carries the calendar beside its text only, as Outlook's
		l.Files = nil
	}
	return s.SendLetter(ctx, userID, mailboxID, l)
}

// Invite is the meeting a letter of the user carries: its calendar part, else a calendar file kept with it.
func (s *Store) Invite(ctx context.Context, userID, letterID string) (Invite, bool, error) {
	var ics string
	err := s.db.QueryRow(ctx, `SELECT x.ics FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2`, letterID, userID).Scan(&ics)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, false, ErrNotFound
	}
	if err != nil {
		return Invite{}, false, err
	}
	if ics == "" { // a letter kept before go24 read calendars: its .ics file
		var fileID string
		err := s.db.QueryRow(ctx, `SELECT id::text FROM files WHERE mail_id::text = $1 AND content_id = ''
			AND (content_type IN ('text/calendar', 'application/ics') OR lower(name) LIKE '%.ics') ORDER BY created_at LIMIT 1`, letterID).Scan(&fileID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Invite{}, false, nil
		}
		if err != nil {
			return Invite{}, false, err
		}
		a, err := s.readFile(ctx, userID, fileID)
		if err != nil {
			return Invite{}, false, nil // the file cannot be read now: shown as a file only
		}
		ics = string(a.Data)
	}
	in, ok := ParseInvite(ics)
	return in, ok, nil
}

// InviteAnswer is the user's answer to the meeting with the UID.
func (s *Store) InviteAnswer(ctx context.Context, userID, uid string) (InviteState, error) {
	var st InviteState
	err := s.db.QueryRow(ctx, `SELECT i.status, i.sequence, coalesce(e.id::text, ''), i.proposed_start, i.proposed_end
		FROM mail_invites i LEFT JOIN events e ON e.id = i.event_id
		WHERE i.user_id::text = $1 AND i.uid = $2`, userID, uid).Scan(&st.Status, &st.Sequence, &st.EventID, &st.ProposedStart, &st.ProposedEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return InviteState{}, nil
	}
	return st, err
}

// inviteTitle is the meeting's name in the calendar.
func inviteTitle(in Invite, subject string) string {
	t := strings.TrimSpace(in.Summary)
	if t == "" {
		t = strings.TrimSpace(subject)
	}
	if t == "" {
		t = "—"
	}
	return clip(t, 300)
}

// inviteText is the meeting's description in the calendar: who organizes it, then its own words.
func (s *Store) inviteText(in Invite) string {
	t := strings.TrimSpace(in.Description)
	if in.Organizer.Email != "" {
		org := in.Organizer.Email
		if in.Organizer.Name != "" {
			org = in.Organizer.Name + " <" + org + ">"
		}
		t = strings.TrimSpace(s.tr("mail.inv.organizer_line", org) + "\n\n" + t)
	}
	if in.Join != "" && !strings.Contains(t, in.Join) {
		t = strings.TrimSpace(t + "\n\n" + s.tr("mail.inv.join_line", in.Join))
	}
	return clip(t, 10000)
}

// keepMeeting puts the meeting into the user's calendar (or changes the one kept already); it gives the id.
func (s *Store) keepMeeting(ctx context.Context, q pgx.Tx, userID, eventID string, in Invite, subject string) (string, error) {
	title, text := inviteTitle(in, subject), s.inviteText(in)
	every := max(in.Every, 1)
	if eventID != "" {
		tag, err := q.Exec(ctx, `UPDATE events SET title = $3, description = $4, location = $5, starts_at = $6, ends_at = $7, all_day = $8,
			repeat = $9, repeat_every = $10, repeat_until = $11, repeat_days = coalesce($12::integer[], '{}'), repeat_count = $13,
			updated_at = now() WHERE id::text = $1 AND owner_id::text = $2`,
			eventID, userID, title, text, clip(in.Location, 1000), in.Start, in.End, in.AllDay, in.Repeat, every, in.Until, in.Days, in.Count)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() > 0 {
			return eventID, nil
		}
	}
	var id string
	err := q.QueryRow(ctx, `INSERT INTO events (owner_id, title, description, location, starts_at, ends_at, all_day, repeat, repeat_every, repeat_until,
		repeat_days, repeat_count)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, coalesce($11::integer[], '{}'), $12) RETURNING id::text`, userID, title, text,
		clip(in.Location, 1000), in.Start, in.End, in.AllDay, in.Repeat, every, in.Until, in.Days, in.Count).Scan(&id)
	return id, err
}

// Answer is the user's answer to the meeting of a letter: accepted, tentative, declined — or cancelled (a cancelled
// meeting is removed from the calendar). reply: "send" — tell the organizer, with the words (comment) when given;
// "none" — do not. It gives the go24 meeting kept ("" — none).
func (s *Store) Answer(ctx context.Context, userID, letterID, status, reply, comment string) (string, error) {
	m, err := s.message(ctx, userID, letterID)
	if err != nil {
		return "", err
	}
	in, ok, err := s.Invite(ctx, userID, letterID)
	if err != nil {
		return "", err
	}
	if !ok || (in.Method != "REQUEST" && in.Method != "PUBLISH" && in.Method != "CANCEL") {
		return "", i18n.E("err.bad_form")
	}
	cancel := in.Method == "CANCEL" || in.Cancelled
	if cancel {
		status = "cancelled"
	} else if status != "accepted" && status != "tentative" && status != "declined" {
		return "", i18n.E("err.bad_form")
	}
	comment = clip(strings.TrimSpace(comment), 2000)
	var eventID string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var kept string
		if err := tx.QueryRow(ctx, `SELECT coalesce(event_id::text, '') FROM mail_invites WHERE user_id::text = $1 AND uid = $2 FOR UPDATE`,
			userID, in.UID).Scan(&kept); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch status {
		case "accepted", "tentative":
			var err error
			if eventID, err = s.keepMeeting(ctx, tx, userID, kept, in, m.Subject); err != nil {
				return err
			}
		default: // declined, or cancelled: not in the calendar
			if kept != "" {
				if _, err := tx.Exec(ctx, `DELETE FROM events WHERE id::text = $1 AND owner_id::text = $2`, kept, userID); err != nil {
					return err
				}
			}
		}
		var ev any
		if eventID != "" {
			ev = eventID
		}
		_, err := tx.Exec(ctx, `INSERT INTO mail_invites (user_id, uid, sequence, status, event_id) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id, uid) DO UPDATE SET sequence = EXCLUDED.sequence, status = EXCLUDED.status, event_id = EXCLUDED.event_id,
			proposed_start = NULL, proposed_end = NULL, updated_at = now()`, userID, in.UID, in.Sequence, status, ev)
		return err
	})
	if err != nil {
		return "", err
	}
	if cancel || reply == "none" || in.Method != "REQUEST" || in.Organizer.Email == "" || strings.EqualFold(in.Organizer.Email, m.MailboxEmail) {
		return eventID, nil
	}
	var name string
	_ = s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, userID).Scan(&name)
	me := Person{Email: m.MailboxEmail, Name: name}
	if a := in.Attendee(m.MailboxEmail); a != nil && a.Name != "" {
		me.Name = a.Name
	}
	text := comment
	if text == "" {
		text = s.tr("mail.inv.text."+status, me.Display(), inviteTitle(in, m.Subject))
	}
	refs, err := s.refsOf(ctx, userID, letterID)
	if err != nil {
		return eventID, err
	}
	err = s.SendCalendar(ctx, userID, m.MailboxID, []string{in.Organizer.Email}, s.tr("mail.inv.subject."+status, inviteTitle(in, m.Subject)),
		text, Reply(in, me, status, comment), "REPLY", refs)
	return eventID, err
}

// Propose answers the letter's meeting «Под вопросом» or «Отклонить» (status tentative | declined) and proposes the
// organizer a new time, start to end, with the words (comment): an iTIP COUNTER from the user's mailbox.
func (s *Store) Propose(ctx context.Context, userID, letterID, status, comment string, start, end time.Time) error {
	if status != "tentative" && status != "declined" {
		return i18n.E("err.bad_form")
	}
	if !end.After(start) {
		return i18n.E("err.event_time")
	}
	m, err := s.message(ctx, userID, letterID)
	if err != nil {
		return err
	}
	in, ok, err := s.Invite(ctx, userID, letterID)
	if err != nil {
		return err
	}
	if !ok || in.Method != "REQUEST" || in.Cancelled || in.Organizer.Email == "" || strings.EqualFold(in.Organizer.Email, m.MailboxEmail) {
		return i18n.E("err.bad_form")
	}
	if _, err := s.Answer(ctx, userID, letterID, status, "none", ""); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE mail_invites SET proposed_start = $3, proposed_end = $4, updated_at = now()
		WHERE user_id::text = $1 AND uid = $2`, userID, in.UID, start, end); err != nil {
		return err
	}
	var name string
	_ = s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, userID).Scan(&name)
	me := Person{Email: m.MailboxEmail, Name: name}
	if a := in.Attendee(m.MailboxEmail); a != nil && a.Name != "" {
		me.Name = a.Name
	}
	comment = clip(strings.TrimSpace(comment), 2000)
	title := inviteTitle(in, m.Subject)
	text := s.tr("mail.counter.text", me.Display(), title, s.span(start, end))
	if comment != "" {
		text += "\n\n" + comment
	}
	refs, err := s.refsOf(ctx, userID, letterID)
	if err != nil {
		return err
	}
	return s.SendCalendar(ctx, userID, m.MailboxID, []string{in.Organizer.Email}, s.tr("mail.counter.subject", title), text,
		Counter(in, me, status, comment, start, end), "COUNTER", refs)
}

// span is a meeting's time in a letter's text: «02.01.2006 15:04 — 16:00».
func (s *Store) span(start, end time.Time) string {
	start, end = start.In(time.Local), end.In(time.Local)
	f := s.tr("format.datetime")
	if start.Format("20060102") == end.Format("20060102") {
		return start.Format(f) + " — " + end.Format("15:04")
	}
	return start.Format(f) + " — " + end.Format(f)
}

// CounterDecision is what the user (the organizer) did with the proposal of a new time a letter carries: accepted,
// declined ("" — nothing yet).
func (s *Store) CounterDecision(ctx context.Context, userID, letterID string) (string, error) {
	var st string
	err := s.db.QueryRow(ctx, `SELECT c.status FROM mail_counters c JOIN mail_messages x ON x.id = c.message_id
		JOIN mailboxes m ON m.id = x.mailbox_id WHERE c.message_id::text = $1 AND m.user_id::text = $2`, letterID, userID).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return st, err
}

// DecideCounter keeps the organizer's decision on the letter's proposal of a new time (accepted — the meeting is
// moved by the caller; declined — the attendee is told so: DECLINECOUNTER, with the words).
func (s *Store) DecideCounter(ctx context.Context, userID, letterID, status, comment string) error {
	if status != "accepted" && status != "declined" {
		return i18n.E("err.bad_form")
	}
	m, err := s.message(ctx, userID, letterID)
	if err != nil {
		return err
	}
	in, ok, err := s.Invite(ctx, userID, letterID)
	if err != nil {
		return err
	}
	if !ok || in.Method != "COUNTER" || len(in.Attendees) == 0 {
		return i18n.E("err.bad_form")
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO mail_counters (message_id, status) VALUES ($1, $2)
		ON CONFLICT (message_id) DO UPDATE SET status = EXCLUDED.status, updated_at = now()`, m.ID, status); err != nil {
		return err
	}
	if status == "accepted" {
		return nil
	}
	who := in.Attendees[0]
	var name string
	_ = s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, userID).Scan(&name)
	d := Invite{UID: in.UID, Sequence: in.Sequence, Summary: in.Summary, Start: in.Start, End: in.End,
		Organizer: Person{Email: m.MailboxEmail, Name: name}, Attendees: []Person{{Email: who.Email, Name: who.Name}},
		Comment: clip(strings.TrimSpace(comment), 2000)}
	title := inviteTitle(in, m.Subject)
	text := s.tr("mail.counter.declined_text", title)
	if d.Comment != "" {
		text += "\n\n" + d.Comment
	}
	refs, err := s.refsOf(ctx, userID, letterID)
	if err != nil {
		return err
	}
	return s.SendCalendar(ctx, userID, m.MailboxID, []string{who.Email}, s.tr("mail.counter.declined_subject", title), text, d,
		"DECLINECOUNTER", refs)
}

// invitesArrived takes in a meeting that came to the user's mailbox: an answer to the user's own meeting (its people
// from outside), an update of a meeting the user accepted (moved in the calendar), a cancel (marked in it).
func (s *Store) invitesArrived(ctx context.Context, a account, ics string) {
	in, ok := ParseInvite(ics)
	if !ok {
		return
	}
	var err error
	switch {
	case in.Method == "REPLY" || in.Method == "COUNTER": // a proposal of a new time answers too
		for _, p := range in.Attendees {
			st := p.Status
			if st != "accepted" && st != "tentative" && st != "declined" {
				continue
			}
			if _, err = s.db.Exec(ctx, `UPDATE event_externals x SET status = $4 FROM events e
				WHERE e.id = x.event_id AND e.ical_uid = $1 AND e.owner_id::text = $2 AND lower(x.email) = lower($3)`,
				in.UID, a.UserID, p.Email, st); err != nil {
				break
			}
		}
	case in.Method == "CANCEL" || in.Cancelled:
		_, err = s.db.Exec(ctx, `UPDATE events e SET title = left($3 || e.title, 300), updated_at = now() FROM mail_invites i
			WHERE i.event_id = e.id AND i.user_id::text = $1 AND i.uid = $2 AND e.owner_id = i.user_id AND i.status <> 'cancelled'
			  AND left(e.title, length($3)) <> $3`, a.UserID, in.UID, s.tr("mail.inv.cancelled_prefix"))
	case in.Method == "DECLINECOUNTER": // the organizer keeps the meeting's time
		_, err = s.db.Exec(ctx, `UPDATE mail_invites SET proposed_start = NULL, proposed_end = NULL WHERE user_id::text = $1 AND uid = $2`,
			a.UserID, in.UID)
	case in.Method == "REQUEST":
		var st InviteState
		if st, err = s.InviteAnswer(ctx, a.UserID, in.UID); err != nil || in.Sequence <= st.Sequence {
			break
		}
		if st.ProposedStart != nil { // sent again: the proposal is answered
			if _, err = s.db.Exec(ctx, `UPDATE mail_invites SET proposed_start = NULL, proposed_end = NULL WHERE user_id::text = $1 AND uid = $2`,
				a.UserID, in.UID); err != nil {
				break
			}
		}
		if st.EventID == "" {
			break
		}
		err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { // the new time is in the calendar; the answer is asked again
			_, err := s.keepMeeting(ctx, tx, a.UserID, st.EventID, in, "")
			return err
		})
	}
	if err != nil && s.Log != nil {
		s.Log("mail: invitation", in.UID, err)
	}
}
