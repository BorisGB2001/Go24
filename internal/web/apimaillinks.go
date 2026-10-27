package web

import (
	"net/http"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// The mail's links in the REST API (scopes mail:read, mail:write):
//   GET  /api/v1/mail/messages/{id}/links        the letter's meeting {invite: {method, uid, sequence, summary, location,
//                                                start, end, all_day, organizer, attendees, answer, event_id, conflicts}},
//                                                its tasks [{key, title}] and CRM cards [{entity, id, title, path}]
//                                                — with proposed {start, end} (the new time the user proposed), counter
//                                                {from, status, comment, decision, meeting: {id, start, end}} (a proposal
//                                                that came to the user's meeting), counter_declined
//   POST /api/v1/mail/messages/{id}/invite       {status: accepted|tentative|declined|cancelled, reply: send|none, comment} → {event_id}
//   POST /api/v1/mail/messages/{id}/propose      «Предложить новое время» {status: tentative|declined, start, end, comment}
//   POST /api/v1/mail/messages/{id}/counter      the organizer's decision on a proposal {decision: accepted|declined, comment}
//                                                → {message}; accepted moves the meeting
//   POST /api/v1/mail/messages/{id}/crm          {kind: lead|deal|contact|company} → {entity, id, path}; {target: /crm/deals/ID} — links it
//   GET  /api/v1/mail/messages/{id}/votes        «Отслеживание» of a letter with voting buttons {options, asked, mine,
//                                                count: {choice: n}, answers: [{id, from, name, choice, at}]}
//   POST /api/v1/mail/messages/{id}/vote         {choice, text} — answers its voting button
//   GET  /api/v1/mail/messages/{id}/recall       what came of recalling the user's sent letter [{addr, status, at}]
//   POST /api/v1/mail/messages/{id}/recall       «Отозвать сообщение» → {results: [{addr, status: recalled|read|missing|external|failed, at}]}
//   GET  /api/v1/mail/shared                     the shared mailboxes open to the key's owner [{mailbox_id, email, name, level, open}]
//   GET  /api/v1/mail/shared/{box}/tickets?status=open|new|progress|done|all&who=me|nobody&q=  the requests
//   POST /api/v1/mail/shared/{box}/letters/{letter}/assign  {user: ID|""}
//   POST /api/v1/mail/shared/{box}/letters/{letter}/status  {status: new|progress|done}

func (s *Server) apiMailLinks(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sess := auth.Session{UserID: p.UserID}
	m, err := s.Mail.Message(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	thread, err := s.Mail.Conversation(r.Context(), p.UserID, m.ID)
	if err != nil {
		return err
	}
	data := map[string]any{}
	if err := s.mailExtras(r, sess, m, thread, data); err != nil {
		return err
	}
	out := map[string]any{"invite": nil, "tasks": []any{}, "crm": []any{}}
	if c, _ := data["Invite"].(*invCard); c != nil {
		inv := map[string]any{"method": c.Inv.Method, "uid": c.Inv.UID, "sequence": c.Inv.Sequence, "summary": c.Inv.Summary,
			"location": c.Inv.Location, "start": c.Inv.Start, "end": c.Inv.End, "all_day": c.Inv.AllDay, "cancelled": c.Cancel,
			"organizer": c.Inv.Organizer, "attendees": c.Inv.Attendees, "answer": c.State.Status, "event_id": c.State.EventID, "update": c.Update}
		var conflicts []map[string]any
		for _, e := range c.Conflicts {
			conflicts = append(conflicts, map[string]any{"id": e.ID, "title": e.Title, "start": e.Start, "end": e.End})
		}
		inv["conflicts"] = conflicts
		if c.State.ProposedStart != nil && c.State.ProposedEnd != nil {
			inv["proposed"] = map[string]any{"start": c.State.ProposedStart, "end": c.State.ProposedEnd}
		}
		if c.Counter != nil {
			cn := map[string]any{"from": c.Counter.Email, "name": c.Counter.Name, "status": c.Counter.Status, "comment": c.Inv.Comment,
				"decision": c.Decision, "meeting": nil}
			if e := c.Meeting; e != nil {
				cn["meeting"] = map[string]any{"id": e.ID, "start": e.Start, "end": e.End}
			}
			inv["counter"] = cn
		}
		inv["counter_declined"] = c.Declined
		out["invite"] = inv
	}
	var ts []map[string]any
	for _, t := range data["LetterTasks"].([]mail.TaskRef) {
		ts = append(ts, map[string]any{"key": t.Key, "title": t.Title, "status": t.Status})
	}
	if ts != nil {
		out["tasks"] = ts
	}
	if refs, ok := data["LetterCRM"].([]mail.CRMRef); ok && len(refs) > 0 {
		var cs []map[string]any
		for _, c := range refs {
			cs = append(cs, map[string]any{"entity": c.Entity, "id": c.EntityID, "title": c.Title, "path": c.Path()})
		}
		out["crm"] = cs
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailInvite(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Status, Reply, Comment string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ev, err := s.Mail.Answer(r.Context(), p.UserID, r.PathValue("id"), in.Status, in.Reply, in.Comment)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"event_id": ev})
	return nil
}

func (s *Server) apiMailPropose(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Status, Comment string
		Start, End      time.Time
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Mail.Propose(r.Context(), p.UserID, r.PathValue("id"), in.Status, in.Comment, in.Start, in.End); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	return nil
}

func (s *Server) apiMailCounter(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Decision, Comment string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	msg, err := s.counterDecide(r, auth.Session{UserID: p.UserID}, r.PathValue("id"), in.Decision, in.Comment)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": msg})
	return nil
}

func (s *Server) apiMailVotes(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	m, err := s.Mail.Message(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	t, err := s.Mail.Votes(r.Context(), p.UserID, m.ID)
	if err != nil {
		return err
	}
	answers := []map[string]any{}
	for _, a := range t.Answers {
		answers = append(answers, map[string]any{"id": a.ID, "from": a.From, "name": a.Name, "choice": a.Choice, "at": a.At})
	}
	options := m.Options()
	if options == nil {
		options = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"options": options, "asked": t.Asked, "mine": t.Mine, "count": t.Count, "answers": answers})
	return nil
}

func (s *Server) apiMailVote(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Choice, Text string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Mail.Vote(r.Context(), p.UserID, r.PathValue("id"), in.Choice, in.Text); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

func (s *Server) apiMailRecalls(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	m, err := s.Mail.Message(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	list, err := s.Mail.Recalls(r.Context(), p.UserID, m.MessageID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": recallsJSON(list)})
	return nil
}

func (s *Server) apiMailRecall(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Recall(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": recallsJSON(list)})
	return nil
}

func recallsJSON(list []mail.RecallResult) []map[string]any {
	out := []map[string]any{}
	for _, x := range list {
		out = append(out, map[string]any{"addr": x.Addr, "status": x.Status, "at": x.At})
	}
	return out
}

func (s *Server) apiMailCRM(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Kind, Target string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := auth.Session{UserID: p.UserID}
	m, err := s.Mail.Message(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if in.Target != "" {
		l, perr := tasks.ParseCRM(in.Target)
		parts := strings.Split(in.Target, "/")
		if perr != nil || !l.Own || len(parts) != 4 {
			return i18n.E("err.crm_link")
		}
		if err := s.crmCheck(r, sess, parts[2], parts[3], "write"); err != nil {
			return err
		}
		if _, err := s.Mail.LinkCRM(r.Context(), p.UserID, m.ID, l.Kind, parts[3]); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"entity": l.Kind, "id": parts[3], "path": in.Target})
		return nil
	}
	id, err := s.crmFromLetter(r, sess, m, in.Kind)
	if err == nil {
		_, err = s.Mail.LinkCRM(r.Context(), p.UserID, m.ID, in.Kind, id)
	}
	if err == nil && in.Kind == "lead" {
		err = s.Mail.SetLead(r.Context(), m.ID, id)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"entity": in.Kind, "id": id, "path": "/crm/" + crmPaths[in.Kind] + "/" + id})
	return nil
}

func (s *Server) apiMailShared(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Shares(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, sh := range list {
		out = append(out, map[string]any{"mailbox_id": sh.MailboxID, "email": sh.Email, "name": sh.Title(), "department": sh.DepartmentName,
			"level": sh.Level, "open": sh.Open})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailTickets(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	list, err := s.Mail.Tickets(r.Context(), p.UserID, r.PathValue("box"), mail.TicketFilter{Status: q.Get("status"),
		Mine: q.Get("who") == "me", Nobody: q.Get("who") == "nobody", Q: q.Get("q")})
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, t := range list {
		out = append(out, map[string]any{"thread": t.Thread, "letter_id": t.LetterID, "subject": t.Subject, "from": t.FromAddr, "from_name": t.FromName,
			"last_at": t.LastAt, "count": t.Count, "unread": t.Unread, "status": t.Status, "reopened": t.Reopened, "assignee_id": t.AssigneeID,
			"assignee": t.AssigneeName})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailTicketDo(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ User, Status string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	box, letter := r.PathValue("box"), r.PathValue("letter")
	var err error
	switch r.PathValue("do") {
	case "assign":
		err = s.Mail.Assign(r.Context(), p.UserID, box, letter, in.User)
	case "status":
		err = s.Mail.SetTicketStatus(r.Context(), p.UserID, box, letter, in.Status)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}
