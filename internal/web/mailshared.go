package web

import (
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- shared mailboxes of a department ----------

// /mail/shared — the shared mailboxes the user may open, and the user's own mailboxes to share; /mail/shared/{box} —
// a shared mailbox as a helpdesk: its conversations as requests (status, who answers it; «Мои», «Без ответственного»),
// the conversation open on the right with the answer as the mailbox, «Назначить», the status; ?tab=access — who may
// read it, send as it, manage it (the department by default and people by name).

type deptRef struct{ ID, Name string }

func (s *Server) shareDepts(r *http.Request) ([]deptRef, error) {
	rows, err := s.DB.Query(r.Context(), `SELECT id::text, name FROM departments ORDER BY parent_id NULLS FIRST, sort, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deptRef
	for rows.Next() {
		var d deptRef
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Server) mailSharedIndex(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	shares, err := s.Mail.Shares(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	shared := map[string]bool{}
	for _, sh := range shares {
		shared[sh.MailboxID] = true
	}
	var own []mail.Mailbox
	for _, b := range boxes {
		if !shared[b.ID] {
			own = append(own, b)
		}
	}
	depts, err := s.shareDepts(r)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	s.render(w, r, "mailshared", page{TitleKey: "mail.shared.title", Nav: "mail", Data: map[string]any{
		"Shares": shares, "Own": own, "Depts": depts, "Levels": mail.ShareLevels}})
}

// mailShareSave makes the user's mailbox shared, or changes a shared one (its name, department, level).
func (s *Server) mailShareSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	box := r.FormValue("box")
	back := "/mail/shared"
	if r.FormValue("back") == "box" {
		back = "/mail/shared/" + url.PathEscape(box) + "?tab=access"
	}
	if err := s.Mail.ShareBox(r.Context(), sess.UserID, box, r.FormValue("name"), r.FormValue("department"), r.FormValue("dept_level")); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mailbox shared", "user_id", sess.UserID, "mailbox", box, "department", r.FormValue("department"), "level", r.FormValue("dept_level"))
	if r.FormValue("back") != "box" {
		back = "/mail/shared/" + url.PathEscape(box) + "?tab=access"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) mailUnshare(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	box := r.PathValue("box")
	if err := s.Mail.UnshareBox(r.Context(), sess.UserID, box); err != nil {
		s.mailErr(w, r, err, "/mail/shared/"+url.PathEscape(box)+"?tab=access")
		return
	}
	s.Log.Info("mailbox unshared", "user_id", sess.UserID, "mailbox", box)
	http.Redirect(w, r, "/mail/shared", http.StatusSeeOther)
}

// mailSharePerson gives a person a level on the mailbox (level "" — takes it away).
func (s *Server) mailSharePerson(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	box := r.PathValue("box")
	back := "/mail/shared/" + url.PathEscape(box) + "?tab=access"
	if err := s.Mail.SetSharePerson(r.Context(), sess.UserID, box, r.FormValue("user"), r.FormValue("level")); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// sharedURL is the address of a shared mailbox's page with its filter kept.
func sharedURL(box string, q url.Values, extra ...string) string {
	v := url.Values{}
	for _, k := range []string{"status", "who", "q"} {
		if x := q.Get(k); x != "" {
			v.Set(k, x)
		}
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if extra[i+1] == "" {
			v.Del(extra[i])
		} else {
			v.Set(extra[i], extra[i+1])
		}
	}
	u := "/mail/shared/" + url.PathEscape(box)
	if len(v) > 0 {
		u += "?" + v.Encode()
	}
	return u
}

func (s *Server) mailSharedPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	box := r.PathValue("box")
	q := r.URL.Query()
	sh, err := s.Mail.SharedBox(ctx, sess.UserID, box, "read")
	if err != nil {
		s.taskErr(w, r, err, "/mail/shared")
		return
	}
	f := mail.TicketFilter{Status: q.Get("status"), Mine: q.Get("who") == "me", Nobody: q.Get("who") == "nobody", Q: q.Get("q")}
	list, err := s.Mail.Tickets(ctx, sess.UserID, box, f)
	if err != nil {
		s.taskErr(w, r, err, "/mail/shared")
		return
	}
	people, err := s.Mail.SharedPeople(ctx, sess.UserID, box)
	if err != nil {
		s.taskErr(w, r, err, "/mail/shared")
		return
	}
	data := map[string]any{"Share": sh, "Tickets": list, "People": people, "Statuses": mail.TicketStatuses, "Levels": mail.ShareLevels,
		"Q": q, "Status": f.Status, "Who": q.Get("who"), "Tab": q.Get("tab"), "Me": sess.UserID,
		"CanSend": mail.LevelAtLeast(sh.Level, "send"), "CanManage": mail.LevelAtLeast(sh.Level, "manage"),
		"Back": sharedURL(box, q), "Owner": sh.OwnerID == sess.UserID}
	data["URL"] = func(extra ...string) string { return sharedURL(box, q, extra...) }
	if q.Get("tab") == "access" && mail.LevelAtLeast(sh.Level, "manage") {
		if data["Depts"], err = s.shareDepts(r); err == nil {
			data["Users"], err = s.Tasks.ListUsers(ctx)
		}
		if err != nil {
			s.taskErr(w, r, err, "/mail/shared")
			return
		}
	}
	if id := q.Get("letter"); id != "" {
		t, err := s.Mail.LetterTicket(ctx, sess.UserID, box, id)
		if err != nil {
			s.taskErr(w, r, err, sharedURL(box, q))
			return
		}
		// the conversation is read as its owner's (the shared mailbox is theirs): the letters, files, pictures
		thread, err := s.Mail.Conversation(ctx, sh.OwnerID, id)
		if err != nil {
			s.taskErr(w, r, err, sharedURL(box, q))
			return
		}
		files := map[string][]mail.File{}
		docs := map[string]string{}
		for _, m := range thread {
			if files[m.ID], err = s.Mail.Files(ctx, sh.OwnerID, m.ID); err != nil {
				s.taskErr(w, r, err, "/mail/shared")
				return
			}
			if m.HTML != "" {
				pics, err := s.Mail.Pictures(ctx, sh.OwnerID, m.ID)
				if err != nil {
					s.taskErr(w, r, err, "/mail/shared")
					return
				}
				docs[m.ID] = mailDoc(mail.WithPictures(m.HTML, pics))
			}
		}
		last := thread[len(thread)-1]
		for _, m := range thread { // the answer goes to the last letter from outside
			if !m.Outgoing() {
				last = m
			}
		}
		data["Ticket"], data["Thread"], data["Files"], data["Docs"], data["Answer"] = t, thread, files, docs, last
		data["ReplySubject"] = replySubject(last.Subject)
	}
	s.render(w, r, "mailshared", page{Title: sh.Title(), Nav: "mail", Data: data})
}

// replySubject is «Re: …» of a subject (once).
func replySubject(subject string) string {
	if reSubjectPrefix.MatchString(subject) {
		return subject
	}
	return "Re: " + subject
}

// mailSharedDo: assign (user — "" nobody), status (new | progress | done), reply (to, cc, subject, body) on the
// request of a letter of the shared mailbox.
func (s *Server) mailSharedDo(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	box, letter := r.PathValue("box"), r.PathValue("letter")
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail/shared/") {
		back = "/mail/shared/" + url.PathEscape(box)
	}
	var err error
	msg := ""
	switch r.PathValue("do") {
	case "assign":
		err = s.Mail.Assign(ctx, sess.UserID, box, letter, r.FormValue("user"))
		msg = s.tr(r, "mail.shared.assigned")
	case "status":
		err = s.Mail.SetTicketStatus(ctx, sess.UserID, box, letter, r.FormValue("status"))
		msg = s.tr(r, "mail.shared.status_set")
	case "reply":
		err = s.Mail.SendShared(ctx, sess.UserID, box, letter, mail.Letter{To: r.FormValue("to"), CC: r.FormValue("cc"),
			Subject: r.FormValue("subject"), Text: r.FormValue("body")})
		msg = s.tr(r, "mail.sent_ok")
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("shared mailbox", "user_id", sess.UserID, "mailbox", box, "letter", letter, "do", r.PathValue("do"))
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
