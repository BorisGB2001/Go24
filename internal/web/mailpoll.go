package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// «Опрос о времени встречи», as Outlook's Scheduling Poll. In the editor of a letter (static/js/mailpoll.js) the
// user picks a few times of the meeting — the employees among the letter's people busy then are shown, «Подобрать
// время» offers times all of them are free — its length, place, video meeting and «Назначать автоматически»; the
// poll is made from the letter's To and Cc, and a block with its times and the link «Проголосовать» goes into the
// text. The people answer on /mp/{token} without signing in («Да», «Возможно», «Нет» for each time); «Опросы о
// времени» (/mail/polls) lists the user's polls, the poll's page shows every answer and «Назначить» makes the meeting:
// in the organizer's calendar, the employees invited in go24, the others by mail from the poll's mailbox.
//
//	GET  /api/v1/mail/polls                 [poll]
//	POST /api/v1/mail/polls                 {title, length, location, note, video, auto, mailbox, people: [addr], starts: [RFC 3339]} → poll
//	GET  /api/v1/mail/polls/{id}            poll
//	POST /api/v1/mail/polls/{id}/schedule   {option} → poll (with event)
//	POST /api/v1/mail/polls/{id}/cancel     → poll
//	DELETE /api/v1/mail/polls/{id}
func (s *Server) mailPollRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mail/polls", s.requireUser(s.mailPollsPage))
	mux.HandleFunc("POST /mail/polls", s.requireUser(s.mailPollCreate))
	mux.HandleFunc("POST /mail/polls/check", s.requireUser(s.mailPollCheck))
	mux.HandleFunc("GET /mail/polls/{id}", s.requireUser(s.mailPollPage))
	mux.HandleFunc("POST /mail/polls/{id}/schedule", s.requireUser(s.mailPollSchedule))
	mux.HandleFunc("POST /mail/polls/{id}/cancel", s.requireUser(s.mailPollCancel))
	mux.HandleFunc("POST /mail/polls/{id}/delete", s.requireUser(s.mailPollDelete))
	mux.HandleFunc("GET /mp/{token}", s.mailPollPublic) // the people's page of the poll: no sign-in
	mux.HandleFunc("POST /mp/{token}", s.mailPollVote)
	mux.HandleFunc("GET /api/v1/mail/polls", s.api("mail:read", s.apiMailPolls))
	mux.HandleFunc("POST /api/v1/mail/polls", s.api("mail:write", s.apiMailPollCreate))
	mux.HandleFunc("GET /api/v1/mail/polls/{id}", s.api("mail:read", s.apiMailPoll))
	mux.HandleFunc("POST /api/v1/mail/polls/{id}/schedule", s.api("mail:write", s.apiMailPollSchedule))
	mux.HandleFunc("POST /api/v1/mail/polls/{id}/cancel", s.api("mail:write", s.apiMailPollCancel))
	mux.HandleFunc("DELETE /api/v1/mail/polls/{id}", s.api("mail:write", s.apiMailPollDelete))
}

// pollURL is the people's link of the poll.
func (s *Server) pollURL(p mail.Poll) string { return s.BaseURL + "/mp/" + p.Token }

// pollWhen is a time of the poll as the letter and the pages show it: «пн, 02.11.2026 10:00–10:30».
func (s *Server) pollWhen(lang string, o mail.PollOption) string {
	st, en := o.Start.In(time.Local), o.End.In(time.Local)
	wd := (int(st.Weekday())+6)%7 + 1
	return s.I18n.T(lang, fmt.Sprintf("wd.%d", wd)) + ", " + st.Format("02.01.2006 15:04") + "–" + en.Format("15:04")
}

// pollShown fills how the poll's times are shown.
func (s *Server) pollShown(lang string, p *mail.Poll) {
	for i := range p.Options {
		p.Options[i].When = s.pollWhen(lang, p.Options[i])
	}
}

// pollBlock is the block of the poll in the letter's text: what it is, its times and the link to answer.
func (s *Server) pollBlock(lang string, p mail.Poll) string {
	esc := template.HTMLEscapeString
	var b strings.Builder
	b.WriteString(`<div style="border:1px solid #c5cfdb;border-radius:8px;padding:12px 16px;margin:12px 0">`)
	b.WriteString(`<p style="margin:0 0 6px"><b>📅 ` + esc(s.I18n.T(lang, "mail.poll.block_head", p.Title)) + `</b></p>`)
	b.WriteString(`<p style="margin:0 0 6px">` + esc(s.I18n.T(lang, "mail.poll.block_text", p.OwnerName, strconv.Itoa(p.Length))) + `</p><ul style="margin:0 0 8px">`)
	for _, o := range p.Options {
		b.WriteString(`<li>` + esc(s.pollWhen(lang, o)) + `</li>`)
	}
	b.WriteString(`</ul><p style="margin:0"><a href="` + esc(s.pollURL(p)) + `"><b>` + esc(s.I18n.T(lang, "mail.poll.block_vote")) + `</b></a></p></div><p></p>`)
	return b.String()
}

// pollIn reads a new poll of the editor's form (title, length, location, note, video, auto, box, to, cc, start…).
func pollIn(r *http.Request) (mail.NewPoll, error) {
	in := mail.NewPoll{Title: r.FormValue("title"), Where: r.FormValue("location"), Note: r.FormValue("note"), MailboxID: r.FormValue("box"),
		Video: r.FormValue("video") != "", Auto: r.FormValue("auto") != "", Length: atoi(r.FormValue("length"))}
	for _, k := range []string{"to", "cc"} {
		in.People = append(in.People, r.Form[k]...)
	}
	for _, v := range r.Form["start"] {
		t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
		if err != nil {
			return in, i18n.E("err.mail_poll_options")
		}
		in.Starts = append(in.Starts, t)
	}
	return in, nil
}

// mailPollCreate makes the poll of the letter being written; the answer has the block for its text.
func (s *Server) mailPollCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	in, err := pollIn(r)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	p, err := s.Mail.CreatePoll(r.Context(), sess.UserID, in, time.Now())
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	s.Log.Info("mail poll created", "user_id", sess.UserID, "poll", p.ID, "options", len(p.Options), "people", len(p.Voters))
	writeJSON(w, http.StatusOK, map[string]any{"id": p.ID, "url": s.pollURL(p), "page": "/mail/polls/" + p.ID, "html": s.pollBlock(s.lang(r), p)})
}

// pollPeopleIDs are the employees of go24 with these addresses (each address as in a letter's To).
func (s *Server) pollPeopleIDs(ctx context.Context, addrs []string) []string {
	var emails []string
	for _, a := range addrs {
		for _, x := range splitAddrs(a) {
			emails = append(emails, strings.ToLower(x.Address))
		}
	}
	var ids []string
	rows, err := s.DB.Query(ctx, `SELECT id::text FROM users WHERE active AND lower(email) = ANY($1::text[])`, emails)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// pollBusy marks each option with the names of the people (employee IDs) busy then.
func (s *Server) pollBusy(ctx context.Context, viewer string, people []string, opts []mail.PollOption) error {
	if len(people) == 0 || len(opts) == 0 || s.Calendar == nil {
		return nil
	}
	from, to := opts[0].Start, opts[0].End
	for _, o := range opts {
		from, to = minTime(from, o.Start), maxTime(to, o.End)
	}
	list, err := s.Calendar.Busy(ctx, viewer, people, from, to, "")
	if err != nil {
		return err
	}
	for i, o := range opts {
		for _, p := range list {
			for _, sl := range p.Slots {
				if sl.Start.Before(o.End) && sl.End.After(o.Start) {
					opts[i].Busy = append(opts[i].Busy, p.Name)
					break
				}
			}
		}
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// mailPollCheck is the busy time of the editor's poll (addr — the letter's people, start — its times, length) and
// «Подобрать время»: up to three times, each on its own working day, all of them and the user are free.
func (s *Server) mailPollCheck(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	ctx := r.Context()
	length := time.Duration(max(15, min(atoi(r.FormValue("length")), 24*60))) * time.Minute
	people := append([]string{sess.UserID}, s.pollPeopleIDs(ctx, r.Form["addr"])...)
	var opts []mail.PollOption
	for _, v := range r.Form["start"] {
		if t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local); err == nil {
			opts = append(opts, mail.PollOption{ID: v, Start: t, End: t.Add(length)})
		}
	}
	if err := s.pollBusy(ctx, sess.UserID, people, opts); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	type item struct {
		Start string   `json:"start"`
		Busy  []string `json:"busy"`
	}
	out := struct {
		Options []item   `json:"options"`
		Suggest []string `json:"suggest"`
		People  int      `json:"people"` // the employees whose time is known (the user among them)
	}{Options: []item{}, Suggest: []string{}, People: len(people)}
	for _, o := range opts {
		out.Options = append(out.Options, item{o.ID, append([]string{}, o.Busy...)})
	}
	if r.FormValue("suggest") != "" && s.Calendar != nil {
		sched, err := s.Tasks.Schedule(ctx)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		now := time.Now()
		busy, err := s.Calendar.Busy(ctx, sess.UserID, people, now, now.AddDate(0, 0, 30), "")
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		from := now.Add(time.Hour)
		for len(out.Suggest) < 3 {
			t, ok := calendar.FirstFree(busy, from, length, sched, 30-int(from.Sub(now).Hours()/24))
			if !ok {
				break
			}
			out.Suggest = append(out.Suggest, t.Format("2006-01-02T15:04"))
			from = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, t.Location())
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) mailPollsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Mail.Polls(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	for i := range list {
		s.pollShown(s.lang(r), &list[i])
	}
	s.render(w, r, "mailpoll", page{TitleKey: "mail.poll.list", Nav: "mail", Data: map[string]any{"Polls": list, "Msg": r.URL.Query().Get("msg")}})
}

// mailPollPage is the organizer's page of the poll: every answer, who of the employees is busy at each time,
// «Назначить», «Отменить опрос», the people's link.
func (s *Server) mailPollPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	p, err := s.Mail.Poll(ctx, sess.UserID, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/mail/polls")
		return
	}
	if p.Open() {
		people := []string{sess.UserID}
		for _, v := range p.Voters {
			if v.UserID != "" {
				people = append(people, v.UserID)
			}
		}
		if err := s.pollBusy(ctx, sess.UserID, people, p.Options); err != nil {
			s.Log.Warn("mail poll busy", "poll", p.ID, "err", err)
		}
	}
	s.pollShown(s.lang(r), &p)
	s.render(w, r, "mailpoll", page{TitleKey: "mail.poll.title", Nav: "mail", Data: map[string]any{"Poll": p, "URL": s.pollURL(p),
		"Msg": r.URL.Query().Get("msg"), "Now": time.Now()}})
}

// schedulePoll makes the meeting of the poll at the option, once: in the organizer's calendar, the employees
// invited in go24, the people from outside by mail from the poll's mailbox; gives the poll as it is then and what to
// tell the organizer about the invitations by mail ("" — nothing).
func (s *Server) schedulePoll(r *http.Request, p mail.Poll, optionID string) (mail.Poll, string, error) {
	ctx := r.Context()
	o, ok := p.Option(optionID)
	if !ok {
		return p, "", mail.ErrNotFound
	}
	if !p.Open() {
		return p, "", mail.ErrPollClosed
	}
	if s.Calendar == nil {
		return p, "", i18n.E("err.internal")
	}
	claimed, err := s.Mail.ClaimPoll(ctx, p.ID, o.ID)
	if err != nil {
		return p, "", err
	}
	if !claimed {
		return p, "", mail.ErrPollClosed
	}
	x, err := s.Calendar.Settings(ctx, p.OwnerID)
	if err != nil {
		_ = s.Mail.ReleasePoll(ctx, p.ID)
		return p, "", err
	}
	reminds := []int{}
	if x.Remind >= 0 {
		reminds = []int{x.Remind}
	}
	e := calendar.Event{Title: p.Title, Description: p.Note, Where: p.Where, Start: o.Start, End: o.End, Video: p.Video,
		Accessibility: "busy", Reminds: reminds}
	var invite []string
	var ext []calendar.External
	for _, v := range p.Voters {
		if v.UserID != "" {
			if v.UserID != p.OwnerID {
				invite = append(invite, v.UserID)
			}
		} else {
			ext = append(ext, calendar.External{Email: v.Email, Name: v.Name})
		}
	}
	id, err := s.Calendar.Save(ctx, p.OwnerID, e, invite)
	if err != nil {
		_ = s.Mail.ReleasePoll(ctx, p.ID)
		return p, "", err
	}
	if err := s.Mail.SetPollEvent(ctx, p.ID, id); err != nil {
		return p, "", err
	}
	msg := ""
	if len(ext) > 0 {
		msg = s.eventMailSendTo(r, p.OwnerID, id, ext, p.MailboxID, "", nil)
	}
	s.Log.Info("mail poll scheduled", "poll", p.ID, "event", id, "invited", len(invite), "external", len(ext))
	after, err := s.Mail.PollByToken(ctx, p.Token)
	return after, msg, err
}

func (s *Server) mailPollSchedule(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/mail/polls/" + url.PathEscape(r.PathValue("id"))
	p, err := s.Mail.Poll(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.mailErr(w, r, err, "/mail/polls")
		return
	}
	p, msg, err := s.schedulePoll(r, p, r.FormValue("option"))
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	done := s.tr(r, "mail.poll.scheduled")
	if msg != "" {
		done += " " + msg
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(done), http.StatusSeeOther)
}

func (s *Server) mailPollCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/mail/polls/" + url.PathEscape(r.PathValue("id"))
	if err := s.Mail.CancelPoll(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "mail.poll.cancelled")), http.StatusSeeOther)
}

func (s *Server) mailPollDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeletePoll(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, "/mail/polls")
		return
	}
	http.Redirect(w, r, "/mail/polls?msg="+url.QueryEscape(s.tr(r, "mail.poll.deleted")), http.StatusSeeOther)
}

// pollSignedVoter is the poll's person who is the signed-in employee ("" — none, or not signed in).
func (s *Server) pollSignedVoter(r *http.Request, p mail.Poll) string {
	sess, err := s.Sessions.Get(r)
	if err != nil {
		return ""
	}
	for _, v := range p.Voters {
		if v.UserID != "" && v.UserID == sess.UserID {
			return v.ID
		}
	}
	return ""
}

// mailPollPublic is the people's page of the poll: who they are (the signed-in employee — at once), and «Да»,
// «Возможно», «Нет» for each time with how many answered what so far; a closed poll says so (and the time chosen).
func (s *Server) mailPollPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	p, err := s.Mail.PollByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.slotGone(w, r)
		return
	}
	q := r.URL.Query()
	signed := s.pollSignedVoter(r, p)
	me := q.Get("v")
	if signed != "" {
		me = signed
	}
	s.pollShown(s.lang(r), &p)
	v, ok := p.Voter(me)
	data := map[string]any{"Poll": p, "Msg": q.Get("msg"), "Signed": signed != "", "Done": q.Get("done") != ""}
	if ok {
		data["Me"] = v
	}
	s.render(w, r, "mailpollpub", page{TitleKey: "mail.poll.title", Data: data})
}

// mailPollVote keeps the answers of the person (voter, a_{option} = yes | maybe | no); when everyone agreed on a
// time and the organizer asked for it, the meeting is made at once.
func (s *Server) mailPollVote(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	p, err := s.Mail.PollByToken(r.Context(), tok)
	if err != nil {
		s.slotGone(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/mp/"+tok, http.StatusSeeOther)
		return
	}
	voter := r.FormValue("voter")
	if signed := s.pollSignedVoter(r, p); signed != "" {
		voter = signed // the employee answers for themself only
	}
	answers := map[string]string{}
	for _, o := range p.Options {
		answers[o.ID] = r.FormValue("a_" + o.ID)
	}
	back := "/mp/" + tok + "?v=" + url.QueryEscape(voter)
	p, err = s.Mail.PollVote(r.Context(), tok, voter, answers)
	if err != nil {
		var ie *i18n.Error
		if !errors.As(err, &ie) {
			s.Log.Warn("mail poll vote", "err", err)
			err = i18n.E("booking.err.public")
		}
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	s.Log.Info("mail poll vote", "poll", p.ID, "voter", voter)
	if o, ok := p.Agreed(time.Now()); ok && p.Auto {
		if after, _, err := s.schedulePoll(r, p, o.ID); err != nil {
			if !errors.Is(err, mail.ErrPollClosed) {
				s.Log.Warn("mail poll auto schedule", "poll", p.ID, "err", err)
			}
		} else if err := s.Mail.NotifyPollScheduled(r.Context(), after, s.pollWhen(s.slotLang(), o)); err != nil {
			s.Log.Warn("mail poll notify", "poll", p.ID, "err", err)
		}
	}
	http.Redirect(w, r, back+"&done=1", http.StatusSeeOther)
}

// ---------- REST ----------

type apiMailPollOption struct {
	ID    string    `json:"id"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Yes   int       `json:"yes"`
	Maybe int       `json:"maybe"`
	No    int       `json:"no"`
}

type apiMailPollVoter struct {
	Email   string            `json:"email"`
	Name    string            `json:"name"`
	UserID  string            `json:"user_id,omitempty"`
	Voted   *time.Time        `json:"voted_at"`
	Answers map[string]string `json:"answers,omitempty"`
}

type apiMailPollOut struct {
	ID       string              `json:"id"`
	URL      string              `json:"url"`
	Title    string              `json:"title"`
	Location string              `json:"location"`
	Note     string              `json:"note"`
	Length   int                 `json:"length"`
	Video    bool                `json:"video"`
	Auto     bool                `json:"auto"`
	Mailbox  string              `json:"mailbox,omitempty"`
	Status   string              `json:"status"`
	Chosen   string              `json:"chosen,omitempty"`
	Event    string              `json:"event,omitempty"`
	Created  time.Time           `json:"created_at"`
	Options  []apiMailPollOption `json:"options"`
	People   []apiMailPollVoter  `json:"people"`
	HTML     string              `json:"html,omitempty"` // a new poll: the block for the letter's text
}

func (s *Server) apiPollOf(p mail.Poll) apiMailPollOut {
	a := apiMailPollOut{ID: p.ID, URL: s.pollURL(p), Title: p.Title, Location: p.Where, Note: p.Note, Length: p.Length, Video: p.Video, Auto: p.Auto,
		Mailbox: p.MailboxID, Status: p.Status, Chosen: p.ChosenID, Event: p.EventID, Created: p.CreatedAt,
		Options: []apiMailPollOption{}, People: []apiMailPollVoter{}}
	for _, o := range p.Options {
		a.Options = append(a.Options, apiMailPollOption{o.ID, o.Start, o.End, o.Yes, o.Maybe, o.No})
	}
	for _, v := range p.Voters {
		a.People = append(a.People, apiMailPollVoter{v.Email, v.Name, v.UserID, v.Voted, v.Answers})
	}
	return a
}

func (s *Server) apiMailPolls(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Polls(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []apiMailPollOut{}
	for _, x := range list {
		out = append(out, s.apiPollOf(x))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailPoll(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	x, err := s.Mail.Poll(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiPollOf(x))
	return nil
}

func (s *Server) apiMailPollCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title    string      `json:"title"`
		Length   int         `json:"length"`
		Location string      `json:"location"`
		Note     string      `json:"note"`
		Video    bool        `json:"video"`
		Auto     bool        `json:"auto"`
		Mailbox  string      `json:"mailbox"`
		People   []string    `json:"people"`
		Starts   []time.Time `json:"starts"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	x, err := s.Mail.CreatePoll(r.Context(), p.UserID, mail.NewPoll{MailboxID: in.Mailbox, Title: in.Title, Where: in.Location, Note: in.Note,
		Length: in.Length, Video: in.Video, Auto: in.Auto, Starts: in.Starts, People: in.People}, time.Now())
	if err != nil {
		return err
	}
	a := s.apiPollOf(x)
	a.HTML = s.pollBlock(s.lang(r), x)
	writeJSON(w, http.StatusCreated, a)
	return nil
}

func (s *Server) apiMailPollSchedule(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Option string `json:"option"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	x, err := s.Mail.Poll(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if x, _, err = s.schedulePoll(r, x, in.Option); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiPollOf(x))
	return nil
}

func (s *Server) apiMailPollCancel(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.CancelPoll(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	return s.apiMailPoll(w, r, p)
}

func (s *Server) apiMailPollDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeletePoll(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
