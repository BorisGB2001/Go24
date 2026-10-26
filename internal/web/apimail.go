package web

import (
	"net/http"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/mail"
)

// The mail in the REST API (scopes mail:read, mail:write), the key owner's own mailboxes:
//   GET  /api/v1/mail/boxes                      mailboxes with their folders
//   POST /api/v1/mail/boxes                      connect {email, password, login, imap_host, imap_port, smtp_host, smtp_port, tls}
//   POST /api/v1/mail/boxes/{id}/sync            fetch now
//   POST /api/v1/mail/boxes/{id}/folders         {name, parent}
//   POST /api/v1/mail/folders/{id}/rename        {name}
//   DELETE /api/v1/mail/folders/{id}
//   GET  /api/v1/mail/messages?folder=ID|all|flagged|snoozed|cat:ID|search:ID&q=&conv=1  letters of a folder (conv —
//                                                conversations); q — a search (from: to: subject: has:attachment is:unread
//                                                is:flagged is:important after: before:), scope=all — in all folders;
//                                                view=focused|other; filter=unread|flagged|tome|attach|mention|high;
//                                                sort=date|from|size|subject|importance|flag, turn=1 — the other way
//   GET  /api/v1/mail/messages/{id}              a letter (marks it read)
//   GET  /api/v1/mail/messages/{id}/conversation the letter's conversation, the oldest first (marks it read)
//   POST /api/v1/mail/move                       {letters: [ID], folder}
//   POST /api/v1/mail/act                        {letters: [ID], action, arg} — mail.Actions; arg: flag's date or preset,
//                                                category id, snooze time (RFC 3339) or preset
//   GET  /api/v1/mail/categories                 the user's categories
//   GET  /api/v1/mail/searches                   saved searches (search folders) [{id, name, q}]
//   POST /api/v1/mail/searches                   {name, q} → {id}
//   DELETE /api/v1/mail/searches/{id}
//   GET  /api/v1/mail/lists                      «Списки контактов» [{id, name, members: [{name, email}]}]
//   POST /api/v1/mail/lists                      {name, members: [{name, email}]} → {id}; POST /api/v1/mail/lists/{id} — changes
//   DELETE /api/v1/mail/lists/{id}
//   GET  /api/v1/mail/rules                      the rules in their order [{id, name, enabled, stop, pos, if, except, do}]
//   POST /api/v1/mail/rules                      a new rule (as above, without id) → {id}
//   POST /api/v1/mail/rules/{id}                 changes a rule; {move: -1|1} — up or down in the order
//   DELETE /api/v1/mail/rules/{id}
//   POST /api/v1/mail/rules/run                  {folder, rules: [ID]} — «Запустить сейчас» (no rules: those on) → {letters}
//   GET  /api/v1/mail/ooo                        automatic replies {mode, from, to, internal, external, outside, absence, on, until}
//   POST /api/v1/mail/ooo                        {mode: off|on|period, from, to, internal, external, outside: none|contacts|all, absence}
//   POST /api/v1/mail/sweep                      {letters: [ID], mode: all|future|latest|older, target: delete|archive|folder ID, days}
//   GET  /api/v1/mail/sweeps                     the standing «Очистка» [{id, folder_id, sender, mode, target, days}]
//   DELETE /api/v1/mail/sweeps/{id}
// The actions take focus / other (arg "always" — «Всегда перемещать» for the senders).

func toAPIFolder(f mail.Folder) map[string]any {
	return map[string]any{"id": f.ID, "mailbox_id": f.MailboxID, "name": f.Name, "title": f.Title(), "role": f.Role,
		"depth": f.Depth(), "selectable": f.Selectable, "local": f.Local, "unseen": f.Unseen, "total": f.Total}
}

func toAPILetter(m mail.Message, body bool) map[string]any {
	out := map[string]any{"id": m.ID, "mailbox_id": m.MailboxID, "mailbox": m.MailboxEmail, "folder_id": m.FolderID,
		"folder": m.FolderName, "from": m.FromAddr, "from_name": m.FromName, "to": m.To, "subject": m.Subject,
		"sent_at": m.SentAt, "seen": m.Seen, "lead_id": m.LeadID, "cc": m.CC, "message_id": m.MessageID, "thread": m.Thread,
		"flagged": m.Flagged, "flag_done": m.FlagDone, "flag_due": m.FlagDue, "pinned": m.Pinned, "categories": m.Categories,
		"snooze_until": m.SnoozeUntil, "count": m.Count, "unread": m.Unread, "ids": m.IDs, "importance": m.Importance, "bcc": m.BCC, "draft": m.Draft, "other": m.Other,
		"voting": m.Options(), "vote": m.Vote}
	if out["categories"] == nil || len(m.Categories) == 0 {
		out["categories"] = []string{}
	}
	if body {
		out["body"], out["html"] = m.Body, m.HTML
	}
	return out
}

func (s *Server) apiMailBoxes(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	boxes, err := s.Mail.Mailboxes(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	folders, err := s.Mail.Folders(r.Context(), p.UserID, "")
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(boxes))
	for _, b := range boxes {
		fs := []map[string]any{}
		for _, f := range folders {
			if f.MailboxID == b.ID {
				fs = append(fs, toAPIFolder(f))
			}
		}
		out = append(out, map[string]any{"id": b.ID, "email": b.Email, "unseen": b.Unseen, "synced_at": b.SyncedAt, "error": b.Error, "folders": fs})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailConnect(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Login    string `json:"login"`
		IMAPHost string `json:"imap_host"`
		IMAPPort int    `json:"imap_port"`
		SMTPHost string `json:"smtp_host"`
		SMTPPort int    `json:"smtp_port"`
		TLS      *bool  `json:"tls"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	st := mail.Settings{Email: in.Email, Password: in.Password, Login: in.Login, IMAPHost: in.IMAPHost, IMAPPort: in.IMAPPort,
		SMTPHost: in.SMTPHost, SMTPPort: in.SMTPPort, TLS: in.TLS == nil || *in.TLS}
	if err := mail.Probe(st); err != nil {
		return err
	}
	id, err := s.Mail.Add(r.Context(), p.UserID, st)
	if err != nil {
		return err
	}
	if err := s.Mail.Sync(r.Context(), id); err != nil {
		s.Log.Warn("first mail sync", "err", err)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
	return nil
}

func (s *Server) apiMailSync(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	if _, err := s.Mail.Mailbox(r.Context(), p.UserID, id); err != nil {
		return err
	}
	if err := s.Mail.Sync(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

func (s *Server) apiMailFolderAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name   string `json:"name"`
		Parent string `json:"parent"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.Mail.CreateFolder(r.Context(), p.UserID, r.PathValue("id"), in.Parent, in.Name)
	if err != nil {
		return err
	}
	f, err := s.Mail.Folder(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, toAPIFolder(f))
	return nil
}

func (s *Server) apiMailFolderRename(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Mail.RenameFolder(r.Context(), p.UserID, r.PathValue("id"), in.Name); err != nil {
		return err
	}
	f, err := s.Mail.Folder(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIFolder(f))
	return nil
}

func (s *Server) apiMailFolderDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeleteFolder(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

func (s *Server) apiMailMessages(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	folder := r.URL.Query().Get("folder")
	if !mail.Virtual(folder) {
		if _, err := s.Mail.Folder(r.Context(), p.UserID, folder); err != nil {
			return err
		}
	}
	q := r.URL.Query()
	o := mail.ListOpts{Folder: folder, Q: q.Get("q"), Conv: q.Get("conv") == "1", All: q.Get("scope") == "all", Filter: q.Get("filter"),
		Sort: q.Get("sort"), Turn: q.Get("turn") == "1"}
	if v := q.Get("view"); v == mail.ViewFocused || v == mail.ViewOther {
		o.View = v
	}
	list, err := s.Mail.ListBy(r.Context(), p.UserID, o)
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, m := range list {
		out[i] = toAPILetter(m, false)
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(out)))
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailMessage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	m, err := s.Mail.Message(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPILetter(m, true))
	return nil
}

func (s *Server) apiMailMove(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Letters []string `json:"letters"`
		Folder  string   `json:"folder"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Mail.Move(r.Context(), p.UserID, in.Letters, in.Folder); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

func (s *Server) apiMailConversation(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Conversation(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, m := range list {
		out[i] = toAPILetter(m, true)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailAct(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Letters []string `json:"letters"`
		Action  string   `json:"action"`
		Arg     string   `json:"arg"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	arg, err := actArg(in.Action, in.Arg, time.Now())
	if err != nil {
		return err
	}
	if err := s.Mail.Act(r.Context(), p.UserID, in.Letters, in.Action, arg); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

func (s *Server) apiMailSearches(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Searches(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, x := range list {
		out[i] = map[string]any{"id": x.ID, "name": x.Name, "q": x.Q, "folder": mail.SearchFolder + x.ID}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailSearchSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name string `json:"name"`
		Q    string `json:"q"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.Mail.SaveSearch(r.Context(), p.UserID, "", in.Name, in.Q)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "folder": mail.SearchFolder + id})
	return nil
}

func (s *Server) apiMailSearchDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeleteSearch(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

func (s *Server) apiMailCategories(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.mailCategories(r, p.UserID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, c := range list {
		out[i] = map[string]any{"id": c.ID, "name": c.Name, "color": c.Color}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailRules(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Rules(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	if list == nil {
		list = []mail.Rule{}
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

func (s *Server) apiMailRuleSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		mail.Rule
		Move int `json:"move"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if id != "" && in.Move != 0 {
		if err := s.Mail.MoveRule(r.Context(), p.UserID, id, in.Move); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
		return nil
	}
	in.Rule.ID = id
	id, err := s.Mail.SaveRule(r.Context(), p.UserID, in.Rule)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
	return nil
}

func (s *Server) apiMailRuleDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DeleteRule(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

func (s *Server) apiMailRulesRun(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Folder string   `json:"folder"`
		Rules  []string `json:"rules"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	n, err := s.Mail.RunRules(r.Context(), p.UserID, in.Folder, in.Rules)
	if err != nil && n == 0 {
		return err
	}
	out := map[string]any{"letters": n}
	if err != nil {
		out["error"] = s.I18n.Err(s.lang(r), err)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailOOO(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPost {
		var in struct {
			Mode, Internal, External, Outside string
			From, To                          *time.Time
			Absence                           bool
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Mail.SaveOOO(r.Context(), p.UserID, mail.OOO{Mode: in.Mode, From: in.From, To: in.To, Internal: in.Internal,
			External: in.External, Outside: in.Outside, Absence: in.Absence}); err != nil {
			return err
		}
	}
	o, st, err := s.Mail.OOONow(r.Context(), p.UserID, time.Now())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": o.Mode, "from": o.From, "to": o.To, "internal": o.Internal, "external": o.External,
		"outside": o.Outside, "absence": o.Absence, "on": st.On, "until": st.Until, "by_absence": st.Absence})
	return nil
}

// apiMailSenders: the blocked (?kind=blocked, the default) or safe (?kind=safe) senders — addresses and @domains.
func (s *Server) apiMailSenders(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = mail.SenderBlocked
	}
	list, err := s.Mail.Senders(r.Context(), p.UserID, kind)
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, x := range list {
		out[i] = map[string]any{"sender": x.Sender, "kind": x.Kind, "domain": x.Domain(), "created_at": x.CreatedAt}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiMailSenderAdd puts a sender on a list: {"sender": "a@b.c" | "@b.c", "kind": "blocked" | "safe"}, or the senders of
// letters: {"letters": [...], "kind": ..., "domain": true — their whole domains}. Blocked senders' letters go to junk.
func (s *Server) apiMailSenderAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Sender  string   `json:"sender"`
		Kind    string   `json:"kind"`
		Letters []string `json:"letters"`
		Domain  bool     `json:"domain"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = mail.SenderBlocked
	}
	var senders []string
	var moved int
	var err error
	switch {
	case len(in.Letters) > 0 && in.Kind == mail.SenderBlocked:
		senders, moved, err = s.Mail.BlockSenders(r.Context(), p.UserID, in.Letters, in.Domain)
	case len(in.Letters) > 0 && in.Kind == mail.SenderSafe:
		senders, err = s.Mail.TrustSenders(r.Context(), p.UserID, in.Letters)
	default:
		moved, err = s.Mail.AddSender(r.Context(), p.UserID, in.Kind, in.Sender)
		senders = []string{mail.NormSender(in.Sender)}
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"senders": senders, "moved": moved})
	return nil
}

func (s *Server) apiMailSenderDrop(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DropSender(r.Context(), p.UserID, r.PathValue("sender")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}

// apiMailNotify: how new letters are announced ({"mode": "focused" | "all" | "off", "sound", "desktop"}) and the unread.
func (s *Server) apiMailNotify(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPost {
		cur, err := s.Mail.NotifyPrefs(r.Context(), p.UserID)
		if err != nil {
			return err
		}
		in := struct {
			Mode    *string `json:"mode"`
			Sound   *bool   `json:"sound"`
			Desktop *bool   `json:"desktop"`
		}{}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if in.Mode != nil {
			cur.Mode = *in.Mode
		}
		if in.Sound != nil {
			cur.Sound = *in.Sound
		}
		if in.Desktop != nil {
			cur.Desktop = *in.Desktop
		}
		if err := s.Mail.SaveNotifyPrefs(r.Context(), p.UserID, cur); err != nil {
			return err
		}
	}
	np, err := s.Mail.NotifyPrefs(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	unread, err := s.Mail.Unread(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": np.Mode, "sound": np.Sound, "desktop": np.Desktop, "unread": unread})
	return nil
}

func (s *Server) apiMailSweep(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Letters []string `json:"letters"`
		Mode    string   `json:"mode"`
		Target  string   `json:"target"`
		Days    int      `json:"days"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Target == "" {
		in.Target = "delete"
	}
	n, err := s.Mail.SweepNow(r.Context(), p.UserID, in.Letters, in.Mode, in.Target, in.Days)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"letters": n})
	return nil
}

func (s *Server) apiMailSweeps(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.Sweeps(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, x := range list {
		out[i] = map[string]any{"id": x.ID, "folder_id": x.FolderID, "sender": x.Sender, "mode": x.Mode, "target": x.Target, "days": x.Days}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailSweepDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DropSweep(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
	return nil
}
