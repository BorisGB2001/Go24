package web

import (
	"context"
	"errors"
	"net/http"
	netmail "net/mail"
	"net/url"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// ---------- «Почта» and «Календарь» ----------

// A letter's meeting shows as Outlook shows it: a card above the text — when, where, who organizes it and who is
// invited, what of the user's go24 calendar it overlaps — with «Принять», «Под вопросом», «Отклонить» (with words to
// the organizer, without them, or not telling them at all); an update asks again, a cancel offers «Удалить из
// календаря»; an answer to the user's own meeting says who answered what. «Создать встречу» and «Ответить
// приглашением на собрание» open the meeting's form filled from the letter: its people (go24's own among the invited,
// the others invited by mail from the user's mailbox — iCalendar REQUEST, CANCEL when the meeting is off).

// invCard is the meeting card of a letter.
type invCard struct {
	Inv       mail.Invite
	State     mail.InviteState
	Me        *mail.Person // the user among the invited (their mailbox's address)
	Update    bool         // the user answered an older version
	Cancel    bool
	Reply     *mail.Person // an answer to the user's meeting: who, and what
	EventID   string       // the user's meeting this answer is about
	Conflicts []calendar.Event
	Answers   []string
}

// mailInviteCard is the card of the letter's meeting (nil — none).
func (s *Server) mailInviteCard(r *http.Request, sess auth.Session, m mail.Message) (*invCard, error) {
	if m.Outgoing() {
		return nil, nil
	}
	in, ok, err := s.Mail.Invite(r.Context(), sess.UserID, m.ID)
	if err != nil || !ok {
		return nil, err
	}
	c := &invCard{Inv: in, Me: in.Attendee(m.MailboxEmail), Cancel: in.Method == "CANCEL" || in.Cancelled, Answers: mail.Answers}
	if in.Method == "REPLY" {
		if len(in.Attendees) > 0 {
			c.Reply = &in.Attendees[0]
		}
		_ = s.DB.QueryRow(r.Context(), `SELECT id::text FROM events WHERE ical_uid = $1 AND owner_id = $2`, in.UID, sess.UserID).Scan(&c.EventID)
		return c, nil
	}
	if c.State, err = s.Mail.InviteAnswer(r.Context(), sess.UserID, in.UID); err != nil {
		return nil, err
	}
	c.Update = c.State.Status != "" && c.State.Status != "cancelled" && in.Sequence > c.State.Sequence
	if c.Cancel || s.Calendar == nil {
		return c, nil
	}
	list, err := s.Calendar.Range(r.Context(), sess.UserID, in.Start, in.End)
	if err != nil {
		return nil, err
	}
	for _, e := range list { // the meetings it overlaps, as Outlook's «Конфликтует с …»
		if e.Kind == "event" && e.ID != c.State.EventID && e.Start.Before(in.End) && e.End.After(in.Start) && e.MyStatus != "declined" && !e.AllDay {
			c.Conflicts = append(c.Conflicts, e)
		}
	}
	return c, nil
}

// mailInviteAnswer answers the letter's meeting: status accepted | tentative | declined (cancelled — removed from the
// calendar); reply send | none; comment — the words to the organizer.
func (s *Server) mailInviteAnswer(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("letter")
	back := mailURL(r.FormValue("folder"), id)
	status := r.FormValue("status")
	ev, err := s.Mail.Answer(r.Context(), sess.UserID, id, status, r.FormValue("reply"), r.FormValue("comment"))
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mail invitation answered", "user_id", sess.UserID, "letter", id, "status", status, "event", ev)
	key := "mail.inv.done." + status
	if status != "accepted" && status != "tentative" && status != "declined" {
		key = "mail.inv.done.cancelled"
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, key)), http.StatusSeeOther)
}

// letterPeople are the people of a letter besides the user: go24's employees (by address) and the others.
func (s *Server) letterPeople(ctx context.Context, m mail.Message, own []string) ([]tasks.User, []calendar.External, error) {
	var users []tasks.User
	var others []calendar.External
	seen := map[string]bool{}
	for _, a := range own {
		seen[strings.ToLower(a)] = true
	}
	add := func(addr, name string) error {
		addr = strings.ToLower(strings.TrimSpace(addr))
		if addr == "" || seen[addr] {
			return nil
		}
		seen[addr] = true
		var u tasks.User
		err := s.DB.QueryRow(ctx, `SELECT id::text, name FROM users WHERE lower(email) = $1 AND active AND NOT guest`, addr).Scan(&u.ID, &u.Name)
		switch {
		case err == nil:
			users = append(users, u)
		case errors.Is(err, pgx.ErrNoRows):
			others = append(others, calendar.External{Email: addr, Name: name})
		default:
			return err
		}
		return nil
	}
	if err := add(m.FromAddr, m.FromName); err != nil {
		return nil, nil, err
	}
	for _, list := range []string{m.To, m.CC} {
		for _, a := range splitAddrs(list) {
			if err := add(a.Address, a.Name); err != nil {
				return nil, nil, err
			}
		}
	}
	return users, others, nil
}

// splitAddrs reads an address list as people type or keep it (commas, semicolons, spaces).
func splitAddrs(list string) []*netmail.Address {
	if l, err := netmail.ParseAddressList(list); err == nil {
		return l
	}
	var out []*netmail.Address
	for _, p := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' }) {
		if a, err := netmail.ParseAddress(strings.Trim(p, "<>")); err == nil {
			out = append(out, a)
		}
	}
	return out
}

var reSubjectPrefix = regexp.MustCompile(`(?i)^\s*((re|fwd?|aw|wg|отв|пересл|ответ)(\[\d+\])?\s*:\s*)+`)

// eventMailData fills the meeting's form for the mail: the user's mailboxes the invitations go from, the people
// from outside; from a letter (?mail=ID, &reply=1 — an answer in its conversation) — its subject, text and people.
func (s *Server) eventMailData(r *http.Request, sess auth.Session, data map[string]any) {
	if s.Mail == nil {
		return
	}
	ctx := r.Context()
	e := data["E"].(calendar.Event)
	boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
	if err != nil || len(boxes) == 0 {
		return
	}
	data["MailBoxes"] = boxes
	if e.ID != "" {
		ext, err := s.Calendar.Externals(ctx, e.ID)
		if err != nil {
			s.Log.Warn("event externals", "err", err)
			return
		}
		data["Externals"] = ext
		var addrs []string
		for _, x := range ext {
			addrs = append(addrs, x.Email)
		}
		data["ExternalText"] = strings.Join(addrs, ", ")
		if mi, err := s.Calendar.Mail(ctx, e.ID); err == nil {
			data["MailboxID"] = mi.MailboxID
		}
		return
	}
	id := r.URL.Query().Get("mail")
	if id == "" {
		return
	}
	m, err := s.Mail.Message(ctx, sess.UserID, id)
	if err != nil {
		return
	}
	var own []string
	for _, b := range boxes {
		own = append(own, b.Email)
	}
	users, others, err := s.letterPeople(ctx, m, own)
	if err != nil {
		s.Log.Warn("letter people", "err", err)
		return
	}
	e.Title = clipText(strings.TrimSpace(reSubjectPrefix.ReplaceAllString(m.Subject, "")), 300)
	if e.Title == "" {
		e.Title = m.From()
	}
	e.Description = clipText(m.Body, 5000)
	data["E"], data["Invited"], data["MailboxID"], data["FromLetter"] = e, users, m.MailboxID, m
	var addrs []string
	for _, x := range others {
		addrs = append(addrs, x.Email)
	}
	data["ExternalText"] = strings.Join(addrs, ", ")
	if r.URL.Query().Get("reply") == "1" {
		data["ReplyTo"] = m.ID
	}
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// eventInvite is the meeting as an invitation by mail.
func (s *Server) eventInvite(ctx context.Context, e calendar.Event, mi calendar.MailInfo, box mail.Mailbox, organizer string, ext []calendar.External) mail.Invite {
	in := mail.Invite{UID: mi.UID, Sequence: mi.Sequence, Summary: e.Title, Location: e.Where, Description: e.Description,
		Start: e.Start, End: e.End, AllDay: e.AllDay, Repeat: e.Repeat, Every: e.Every, Until: e.Until, Days: e.Days, Count: e.Count,
		Organizer: mail.Person{Email: box.Email, Name: organizer}}
	for _, a := range e.Attendees { // go24's own invited, with their answers
		var email string
		if err := s.DB.QueryRow(ctx, `SELECT email FROM users WHERE id::text = $1`, a.ID).Scan(&email); err != nil || email == "" {
			continue
		}
		st := a.Status
		if st == "invited" {
			st = "needs-action"
		}
		in.Attendees = append(in.Attendees, mail.Person{Email: email, Name: a.Name, Status: st})
	}
	for _, x := range ext {
		st := x.Status
		if st == "invited" {
			st = "needs-action"
		}
		in.Attendees = append(in.Attendees, mail.Person{Email: x.Email, Name: x.Name, Status: st, RSVP: true})
	}
	return in
}

// eventMailSend sends the saved meeting to its people from outside (field external, from the mailbox chosen):
// the invitation to the new ones, an update to all when the meeting changed (before — as it was), a cancel to
// those taken off. It gives what to tell the user ("" — nothing).
func (s *Server) eventMailSend(r *http.Request, sess auth.Session, id string, before *calendar.Event) string {
	if s.Mail == nil {
		return ""
	}
	ctx := r.Context()
	var want []calendar.External
	for _, a := range splitAddrs(r.FormValue("external")) {
		want = append(want, calendar.External{Email: a.Address, Name: a.Name})
	}
	if before == nil && len(want) == 0 {
		return ""
	}
	e, err := s.Calendar.Get(ctx, sess.UserID, id)
	if err != nil || e.OwnerID != sess.UserID {
		return ""
	}
	mi, err := s.Calendar.Mail(ctx, id)
	if err != nil {
		return s.errText(r, err)
	}
	if mi.UID == "" && len(want) == 0 {
		return ""
	}
	added, removed, err := s.Calendar.SetExternals(ctx, sess.UserID, id, want)
	if err != nil {
		return s.errText(r, err)
	}
	boxID := r.FormValue("mailbox")
	if boxID == "" {
		boxID = mi.MailboxID
	}
	box, err := s.Mail.Mailbox(ctx, sess.UserID, boxID)
	if err != nil {
		boxes, _ := s.Mail.Mailboxes(ctx, sess.UserID)
		if len(boxes) == 0 {
			return s.tr(r, "mail.inv.no_box")
		}
		box = boxes[0]
	}
	changed := before == nil || !before.Start.Equal(e.Start) || !before.End.Equal(e.End) || before.Title != e.Title ||
		before.Where != e.Where || before.Description != e.Description || before.AllDay != e.AllDay || before.Repeat != e.Repeat
	host := box.Email[strings.LastIndex(box.Email, "@")+1:]
	if mi.UID == "" {
		mi.UID = mail.InviteUID(id, host)
	} else if changed {
		mi.Sequence++
	}
	moved := before != nil && (!before.Start.Equal(e.Start) || !before.End.Equal(e.End))
	if moved {
		if err := s.Calendar.ResetExternals(ctx, id); err != nil {
			return s.errText(r, err)
		}
	}
	mi.MailboxID = box.ID
	if err := s.Calendar.SetMail(ctx, sess.UserID, id, mi); err != nil {
		return s.errText(r, err)
	}
	ext, err := s.Calendar.Externals(ctx, id)
	if err != nil {
		return s.errText(r, err)
	}
	var name string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, sess.UserID).Scan(&name)
	in := s.eventInvite(ctx, e, mi, box, name, ext)
	var refs []string
	if reply := r.FormValue("reply_to"); reply != "" {
		refs, _ = s.Mail.Refs(ctx, sess.UserID, reply)
	}
	var to []string
	if changed {
		for _, x := range ext {
			to = append(to, x.Email)
		}
	} else {
		for _, x := range added {
			to = append(to, x.Email)
		}
	}
	text := s.eventMailText(r, e)
	sent := 0
	if len(to) > 0 {
		if err := s.Mail.SendCalendar(ctx, sess.UserID, box.ID, to, e.Title, text, in, "REQUEST", refs); err != nil {
			s.Log.Warn("meeting invitations", "event", id, "err", err)
			return s.errText(r, err)
		}
		sent = len(to)
	}
	if len(removed) > 0 {
		var gone []string
		for _, x := range removed {
			gone = append(gone, x.Email)
		}
		c := in
		c.Attendees = nil
		for _, x := range removed {
			c.Attendees = append(c.Attendees, mail.Person{Email: x.Email, Name: x.Name})
		}
		if err := s.Mail.SendCalendar(ctx, sess.UserID, box.ID, gone, s.tr(r, "mail.inv.subject.cancel", e.Title), text, c, "CANCEL", nil); err != nil {
			s.Log.Warn("meeting cancel", "event", id, "err", err)
			return s.errText(r, err)
		}
	}
	s.Log.Info("meeting sent by mail", "user_id", sess.UserID, "event", id, "to", sent, "removed", len(removed), "sequence", mi.Sequence)
	if sent > 0 {
		return s.tr(r, "mail.inv.sent", sent)
	}
	return ""
}

// eventMailText is the text of an invitation by mail: when, where, the meeting's words.
func (s *Server) eventMailText(r *http.Request, e calendar.Event) string {
	when := e.Start.Format(s.tr(r, "format.datetime")) + " — " + e.End.Format("15:04")
	if e.AllDay {
		when = e.Start.Format(s.tr(r, "format.date"))
	}
	t := s.tr(r, "mail.inv.when") + ": " + when
	if e.Where != "" {
		t += "\n" + s.tr(r, "mail.inv.where") + ": " + e.Where
	}
	if e.Description != "" {
		t += "\n\n" + e.Description
	}
	return t
}

// eventMailCancel tells the people from outside that the meeting is off (before it is deleted).
func (s *Server) eventMailCancel(r *http.Request, sess auth.Session, id string) {
	if s.Mail == nil {
		return
	}
	ctx := r.Context()
	e, err := s.Calendar.Get(ctx, sess.UserID, id)
	if err != nil || e.OwnerID != sess.UserID {
		return
	}
	mi, err := s.Calendar.Mail(ctx, id)
	if err != nil || mi.UID == "" || mi.MailboxID == "" {
		return
	}
	ext, err := s.Calendar.Externals(ctx, id)
	if err != nil || len(ext) == 0 {
		return
	}
	box, err := s.Mail.Mailbox(ctx, sess.UserID, mi.MailboxID)
	if err != nil {
		return
	}
	var name string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, sess.UserID).Scan(&name)
	mi.Sequence++
	in := s.eventInvite(ctx, e, mi, box, name, ext)
	var to []string
	for _, x := range ext {
		to = append(to, x.Email)
	}
	if err := s.Mail.SendCalendar(ctx, sess.UserID, box.ID, to, s.tr(r, "mail.inv.subject.cancel", e.Title), s.eventMailText(r, e), in, "CANCEL", nil); err != nil {
		s.Log.Warn("meeting cancel", "event", id, "err", err)
	}
}
