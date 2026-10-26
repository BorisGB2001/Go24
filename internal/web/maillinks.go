package web

import (
	"bytes"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Почта» with tasks and the CRM ----------

// As in the mail: «Создать задачу» of a letter (its subject, text and files, a link back, «связано с задачей»
// on the letter), the letter to the CRM (a new lead, deal or contact of its sender, or a card it is linked to — the
// card's timeline shows the correspondence), and the sender's card in the reading pane: go24's employee or the CRM's
// contact, company, lead, their open deals and tasks, the last letters from them.

// crmPaths are a card's addresses by its kind.
var crmPaths = map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}

// senderCard is who wrote the letter (or whom it went to), as go24 knows them.
type senderCard struct {
	Addr, Name string
	Employee   *senderEmployee
	Contacts   []crm.Contact
	Companies  []crm.Company
	Leads      []crm.Lead
	Deals      []crm.Deal
	Tasks      []tasks.Task
	Recent     []mail.Message
}

type senderEmployee struct {
	ID, Name, Position, PhotoKey string
	Departments                  []string
}

// Empty: go24 knows nothing of them but their letters.
func (c senderCard) Empty() bool {
	return c.Employee == nil && len(c.Contacts) == 0 && len(c.Companies) == 0 && len(c.Leads) == 0
}

// publicDomains are mail services' domains: a company is not found by them.
var publicDomains = map[string]bool{"gmail.com": true, "googlemail.com": true, "yandex.ru": true, "ya.ru": true, "yandex.com": true,
	"mail.ru": true, "bk.ru": true, "list.ru": true, "inbox.ru": true, "outlook.com": true, "hotmail.com": true, "icloud.com": true,
	"yahoo.com": true, "rambler.ru": true, "proton.me": true, "protonmail.com": true, "live.com": true, "me.com": true}

// mailSender is the card of the letter's person.
func (s *Server) mailSender(r *http.Request, sess auth.Session, m mail.Message, thread []mail.Message) (*senderCard, error) {
	ctx := r.Context()
	c := &senderCard{Addr: strings.ToLower(m.FromAddr), Name: m.FromName}
	if m.Outgoing() { // a letter of the user: whom it went to
		l := splitAddrs(m.To)
		if len(l) == 0 {
			return nil, nil
		}
		c.Addr, c.Name = strings.ToLower(l[0].Address), l[0].Name
	}
	if c.Addr == "" {
		return nil, nil
	}
	var e senderEmployee
	if err := s.DB.QueryRow(ctx, `SELECT id::text, name, position, photo_key FROM users WHERE lower(email) = $1 AND active AND NOT guest`, c.Addr).
		Scan(&e.ID, &e.Name, &e.Position, &e.PhotoKey); err == nil {
		rows, err := s.DB.Query(ctx, `SELECT d.name FROM user_departments u JOIN departments d ON d.id = u.department_id
			WHERE u.user_id::text = $1 AND d.parent_id IS NOT NULL ORDER BY d.name`, e.ID)
		if err == nil {
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					e.Departments = append(e.Departments, n)
				}
			}
			rows.Close()
		}
		c.Employee = &e
	}
	if s.CRM != nil && c.Employee == nil {
		contacts, err := s.CRM.Contacts(ctx, sess.UserID, c.Addr, "")
		if err != nil {
			return nil, err
		}
		companyIDs := map[string]bool{}
		for _, x := range contacts {
			if strings.EqualFold(x.Email, c.Addr) {
				c.Contacts = append(c.Contacts, x)
				if x.Company.ID != "" && !companyIDs[x.Company.ID] {
					companyIDs[x.Company.ID] = true
					if co, err := s.CRM.Company(ctx, x.Company.ID); err == nil && s.CRM.Check(ctx, sess.UserID, "company", "read", co.Responsible.ID) == nil {
						c.Companies = append(c.Companies, co)
					}
				}
			}
		}
		domain := c.Addr[strings.LastIndex(c.Addr, "@")+1:]
		q := c.Addr
		if !publicDomains[domain] {
			q = "@" + domain // a company of the sender's domain
		}
		companies, err := s.CRM.Companies(ctx, sess.UserID, q)
		if err != nil {
			return nil, err
		}
		for _, co := range companies {
			if !companyIDs[co.ID] && (strings.EqualFold(co.Email, c.Addr) || (!publicDomains[domain] && strings.HasSuffix(strings.ToLower(co.Email), "@"+domain))) {
				companyIDs[co.ID] = true
				c.Companies = append(c.Companies, co)
			}
		}
		leads, err := s.CRM.Leads(ctx, sess.UserID, c.Addr)
		if err != nil {
			return nil, err
		}
		for _, l := range leads {
			if strings.EqualFold(l.Email, c.Addr) && l.DealID == "" && !l.Stage.Closed() {
				c.Leads = append(c.Leads, l)
			}
		}
		deals := map[string]bool{}
		addDeals := func(companyID, contactID string) error {
			list, err := s.CRM.Deals(ctx, sess.UserID, "", "", companyID, contactID)
			if err != nil {
				return err
			}
			for _, d := range list {
				if !deals[d.ID] && !d.Stage.Closed() {
					deals[d.ID] = true
					c.Deals = append(c.Deals, d)
				}
			}
			return nil
		}
		for _, x := range c.Contacts {
			if err := addDeals("", x.ID); err != nil {
				return nil, err
			}
		}
		for _, co := range c.Companies {
			if err := addDeals(co.ID, ""); err != nil {
				return nil, err
			}
		}
		// the open tasks of these cards
		var paths []string
		for _, x := range c.Contacts {
			paths = append(paths, "/crm/contacts/"+x.ID)
		}
		for _, co := range c.Companies {
			paths = append(paths, "/crm/companies/"+co.ID)
		}
		for _, d := range c.Deals {
			paths = append(paths, "/crm/deals/"+d.ID)
		}
		for _, l := range c.Leads {
			paths = append(paths, "/crm/leads/"+l.ID)
		}
		seen := map[string]bool{}
		for i, p := range paths {
			if i >= 8 {
				break
			}
			list, err := s.Tasks.ListTasks(ctx, tasks.Filter{Viewer: sess.UserID, CRMLink: p})
			if err != nil {
				return nil, err
			}
			for _, t := range list {
				if !seen[t.ID] && t.Status != tasks.StatusCompleted && len(c.Tasks) < 10 {
					seen[t.ID] = true
					c.Tasks = append(c.Tasks, t)
				}
			}
		}
	}
	// the last letters from them, besides this conversation
	inThread := map[string]bool{}
	for _, t := range thread {
		inThread[t.ID] = true
	}
	recent, err := s.Mail.ListBy(ctx, sess.UserID, mail.ListOpts{Q: "from:" + c.Addr, All: true})
	if err != nil {
		return nil, err
	}
	for _, x := range recent {
		if !inThread[x.ID] && (m.Thread == "" || x.Thread != m.Thread) && len(c.Recent) < 5 {
			c.Recent = append(c.Recent, x)
		}
	}
	return c, nil
}

// mailExtras is what the reading pane shows of a letter besides itself: its meeting, its tasks and CRM cards, the
// sender's card, what may be made of it.
func (s *Server) mailExtras(r *http.Request, sess auth.Session, m mail.Message, thread []mail.Message, data map[string]any) error {
	ctx := r.Context()
	inv, err := s.mailInviteCard(r, sess, m)
	if err != nil {
		return err
	}
	data["Invite"] = inv
	ids := make([]string, len(thread))
	for i, t := range thread {
		ids[i] = t.ID
	}
	list, err := s.Mail.LetterTasks(ctx, sess.UserID, ids)
	if err != nil {
		return err
	}
	var visible []mail.TaskRef
	for _, t := range list {
		if s.Tasks.CheckVisible(ctx, sess.UserID, t.ID) == nil {
			visible = append(visible, t)
		}
	}
	data["LetterTasks"] = visible
	if s.CRM != nil {
		refs, err := s.Mail.LetterCRM(ctx, sess.UserID, ids)
		if err != nil {
			return err
		}
		data["LetterCRM"] = refs
		can := map[string]bool{}
		for _, e := range []string{"lead", "deal", "contact", "company"} {
			if can[e], err = s.CRM.Can(ctx, sess.UserID, e, "write", sess.UserID); err != nil {
				return err
			}
		}
		data["CRMCan"] = can
	}
	if data["Sender"], err = s.mailSender(r, sess, m, thread); err != nil {
		return err
	}
	return s.mailShareExtras(r, sess, ids, thread, data)
}

// ---------- a task of a letter ----------

// taskFromLetter fills a new task's form from the user's letter: its subject, its text with who wrote it and when;
// a CRM card the letter belongs to is the task's too.
func (s *Server) taskFromLetter(r *http.Request, sess auth.Session, id string, f *taskForm) {
	m, err := s.Mail.Message(r.Context(), sess.UserID, id)
	if err != nil {
		return
	}
	files, err := s.Mail.Files(r.Context(), sess.UserID, m.ID)
	if err != nil {
		return
	}
	f.Mail, f.MailFiles = &m, len(files)
	f.Title = clipText(strings.TrimSpace(reSubjectPrefix.ReplaceAllString(m.Subject, "")), 500)
	if f.Title == "" {
		f.Title = s.tr(r, "task.letter_from", m.From())
	}
	head := s.tr(r, "task.letter_head", m.From()+" <"+m.FromAddr+">", m.SentAt.Format(s.tr(r, "format.datetime")))
	f.Description = clipText(head+"\n\n"+m.Body, 20000)
	if f.CRM == "" {
		if refs, err := s.Mail.LetterCRM(r.Context(), sess.UserID, []string{m.ID}); err == nil && len(refs) == 1 {
			f.CRM = "/crm/" + crmPaths[refs[0].Entity] + "/" + refs[0].EntityID
		}
	}
}

// taskLetterDone links the new task to the letter and copies the letter's files to it.
func (s *Server) taskLetterDone(r *http.Request, sess auth.Session, letterID, key string) {
	ctx := r.Context()
	taskID, err := s.Tasks.TaskIDByKey(ctx, key)
	if err == nil {
		err = s.Mail.LinkTask(ctx, sess.UserID, letterID, taskID)
	}
	if err != nil {
		s.Log.Warn("task of a letter", "task", key, "err", err)
		return
	}
	files, err := s.Mail.Files(ctx, sess.UserID, letterID)
	if err != nil || len(files) == 0 || s.Tasks.CanAttach(ctx, sess.UserID, taskID) != nil {
		return
	}
	for _, f := range files {
		a, err := s.Mail.ReadFile(ctx, sess.UserID, f.ID)
		if err != nil {
			s.Log.Warn("letter file to a task", "file", f.ID, "err", err)
			continue
		}
		name := path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
		k := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
		if err := s.Storages.Put(ctx, sess.UserID, false, "", k, bytes.NewReader(a.Data), int64(len(a.Data)), a.ContentType); err != nil {
			s.Log.Warn("letter file to a task", "file", f.ID, "err", err)
			continue
		}
		if _, err := s.Storages.AddFile(ctx, storages.NewFile{Key: k, Name: name, ContentType: a.ContentType, Size: int64(len(a.Data)),
			TaskID: taskID, UploadedBy: sess.UserID}); err != nil {
			s.Log.Warn("letter file to a task", "file", f.ID, "err", err)
			continue
		}
		if err := s.Tasks.FileAdded(ctx, sess.UserID, taskID, name); err != nil {
			s.Log.Warn("file history", "err", err)
		}
	}
}

// ---------- a letter to the CRM ----------

// mailCRM makes a CRM card of the letter (kind lead | deal | contact | company) with the sender's data, or links the
// letter to a card (target — its path, /crm/deals/ID); the card's timeline shows it.
func (s *Server) mailCRM(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	m, err := s.Mail.Message(ctx, sess.UserID, r.PathValue("letter"))
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	back := mailURL(r.FormValue("folder"), m.ID)
	if target := strings.TrimSpace(r.FormValue("target")); target != "" {
		l, perr := tasks.ParseCRM(target)
		parts := strings.Split(target, "/")
		if perr != nil || !l.Own || len(parts) != 4 {
			s.mailErr(w, r, i18n.E("err.crm_link"), back)
			return
		}
		if err := s.crmCheck(r, sess, parts[2], parts[3], "write"); err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		if _, err := s.Mail.LinkCRM(ctx, sess.UserID, m.ID, l.Kind, parts[3]); err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		s.Log.Info("letter linked to crm", "user_id", sess.UserID, "letter", m.ID, "card", target)
		http.Redirect(w, r, back+"&msg="+url.QueryEscape(s.tr(r, "mail.crm.linked")), http.StatusSeeOther)
		return
	}
	kind := r.FormValue("kind")
	id, err := s.crmFromLetter(r, sess, m, kind)
	if err == nil {
		_, err = s.Mail.LinkCRM(ctx, sess.UserID, m.ID, kind, id)
	}
	if err == nil && kind == "lead" {
		err = s.Mail.SetLead(ctx, m.ID, id)
	}
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("crm card of a letter", "user_id", sess.UserID, "letter", m.ID, "kind", kind, "id", id)
	dest := "/crm/" + crmPaths[kind] + "/" + id
	if kind == "deal" {
		dest += "?edit=1" // the amount and the stage are the user's to fill
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// crmFromLetter makes the card: a lead (the subject, the sender, the text), a contact (the sender; their company
// when the CRM knows one of the address's domain), a deal (the subject, with the sender's contact — found or made —
// and its company), a company (of the sender's domain).
func (s *Server) crmFromLetter(r *http.Request, sess auth.Session, m mail.Message, kind string) (string, error) {
	ctx := r.Context()
	if _, ok := crmPaths[kind]; !ok {
		return "", i18n.E("err.bad_form")
	}
	if err := s.CRM.Check(ctx, sess.UserID, kind, "write", sess.UserID); err != nil {
		return "", err
	}
	addr, name := m.FromAddr, m.FromName
	if m.Outgoing() {
		if l := splitAddrs(m.To); len(l) > 0 {
			addr, name = l[0].Address, l[0].Name
		}
	}
	if name == "" {
		name = addr
		if at := strings.Index(addr, "@"); at > 0 {
			name = addr[:at]
		}
	}
	title := strings.TrimSpace(reSubjectPrefix.ReplaceAllString(m.Subject, ""))
	if title == "" {
		title = name
	}
	title = clipText(title, 300)
	body := clipText(m.Body, 9000)
	domain := strings.ToLower(addr[strings.LastIndex(addr, "@")+1:])
	companyOf := func() string { // a company of the CRM with an address of the domain
		if publicDomains[domain] || domain == "" {
			return ""
		}
		list, err := s.CRM.Companies(ctx, sess.UserID, "@"+domain)
		if err != nil {
			return ""
		}
		for _, c := range list {
			if strings.HasSuffix(strings.ToLower(c.Email), "@"+domain) {
				return c.ID
			}
		}
		return ""
	}
	switch kind {
	case "lead":
		return s.CRM.SaveLead(ctx, sess.UserID, crm.Lead{Title: title, Name: clipText(name, 300), Email: clipText(addr, 300),
			Origin: crm.Dict{Code: "EMAIL"}, Comment: body})
	case "contact":
		return s.CRM.SaveContact(ctx, sess.UserID, crm.Contact{Name: clipText(name, 300), Email: clipText(addr, 300), Company: crm.Ref{ID: companyOf()},
			Origin: crm.Dict{Code: "EMAIL"}})
	case "company":
		t := domain
		if publicDomains[domain] {
			t = name
		}
		return s.CRM.SaveCompany(ctx, sess.UserID, crm.Company{Title: clipText(t, 300), Email: clipText(addr, 300)})
	}
	// a deal: with the sender's contact
	var contact, company string
	if list, err := s.CRM.Contacts(ctx, sess.UserID, addr, ""); err == nil {
		for _, x := range list {
			if strings.EqualFold(x.Email, addr) {
				contact, company = x.ID, x.Company.ID
				break
			}
		}
	}
	if contact == "" && s.CRM.Check(ctx, sess.UserID, "contact", "write", sess.UserID) == nil {
		company = companyOf()
		id, err := s.CRM.SaveContact(ctx, sess.UserID, crm.Contact{Name: clipText(name, 300), Email: clipText(addr, 300), Company: crm.Ref{ID: company},
			Origin: crm.Dict{Code: "EMAIL"}})
		if err != nil {
			return "", err
		}
		contact = id
		if _, err := s.Mail.LinkCRM(ctx, sess.UserID, m.ID, "contact", id); err != nil {
			return "", err
		}
	}
	pipes, err := s.CRM.Pipelines(ctx) // the first pipeline, its first stage
	if err != nil || len(pipes) == 0 {
		return "", i18n.E("err.crm_stage")
	}
	return s.CRM.SaveDeal(ctx, sess.UserID, crm.Deal{Title: title, PipelineID: pipes[0].ID, Contact: crm.Ref{ID: contact}, Company: crm.Ref{ID: company},
		Origin: crm.Dict{Code: "EMAIL"}, Comment: body})
}

// mailCRMSearch finds CRM cards to link a letter to: JSON [{path, kind, title, sub}].
func (s *Server) mailCRMSearch(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := []map[string]string{}
	if len([]rune(q)) < 2 || s.CRM == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	add := func(kind, id, title, sub string) {
		if len(out) < 30 {
			out = append(out, map[string]string{"path": "/crm/" + crmPaths[kind] + "/" + id, "kind": kind, "kind_name": s.tr(r, "crm."+crmPaths[kind]),
				"title": title, "sub": sub})
		}
	}
	if list, err := s.CRM.Deals(ctx, sess.UserID, "", q, "", ""); err == nil {
		for _, d := range list {
			if !d.Stage.Closed() {
				add("deal", d.ID, d.Title, d.Company.Name)
			}
		}
	}
	if list, err := s.CRM.Leads(ctx, sess.UserID, q); err == nil {
		for _, l := range list {
			add("lead", l.ID, l.Title, l.Email)
		}
	}
	if list, err := s.CRM.Contacts(ctx, sess.UserID, q, ""); err == nil {
		for _, x := range list {
			add("contact", x.ID, x.Name, x.Email)
		}
	}
	if list, err := s.CRM.Companies(ctx, sess.UserID, q); err == nil {
		for _, c := range list {
			add("company", c.ID, c.Title, c.Email)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// crmMailUnlink takes a letter's conversation out of a card's timeline (those who may change the card).
func (s *Server) crmMailUnlink(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	err := s.crmCheck(r, sess, kind, id, "write")
	if err == nil {
		err = s.Mail.UnlinkCRM(r.Context(), crmEntities[kind], id, r.PathValue("link"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#timeline", http.StatusSeeOther)
}
