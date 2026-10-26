package web

import (
	"context"
	"html"
	"html/template"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	mailpkg "go24/internal/mail"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// ---------- «Маркетинг»: campaigns, segments, the stop list ----------

// As «Маркетинг» (CRM → Маркетинг): e-mail campaigns to segments of CRM cards or of addresses, sent by
// go24-worker from the author's mailbox; each letter ends with «Отписаться» (and has List-Unsubscribe), which puts
// the address into the stop list (/m/u/{token}, no sign-in). Anyone who reads CRM cards works here; a segment takes
// the cards its user may read.

// mktAllowed: the user reads some CRM cards (contacts, companies or leads).
func (s *Server) mktAllowed(r *http.Request, sess auth.Session) error {
	if s.Marketing == nil {
		return crm.ErrNotFound
	}
	p, err := s.CRM.PermsOf(r.Context(), sess.UserID)
	if err != nil {
		return err
	}
	for _, e := range []string{"contact", "company", "lead"} {
		if p.Level(e, "read") != "none" {
			return nil
		}
	}
	return crm.ErrForbidden
}

func (s *Server) mktCampaignsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	list, err := s.Marketing.Campaigns(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "marketing", page{TitleKey: "mkt.campaigns", Nav: "marketing", Flash: r.URL.Query().Get("msg"),
		Data: map[string]any{"Tab": "campaigns", "Campaigns": list}})
}

// mktCampaignPage is a campaign: the editor of a draft, the progress and the addressees of a started one.
func (s *Server) mktCampaignPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	ctx, q, id := r.Context(), r.URL.Query(), r.PathValue("id")
	c := marketing.Campaign{Status: "draft"}
	if id != "new" {
		var err error
		if c, err = s.Marketing.Campaign(ctx, id); err != nil {
			s.taskErr(w, r, err, "/marketing")
			return
		}
	}
	segs, err := s.Marketing.Segments(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	data := map[string]any{"Tab": "campaigns", "C": c, "Segments": segs, "Mine": c.ID == "" || c.CreatedBy == sess.UserID || tasks.IsAdmin(ctx),
		"In": setOf(c.Segments), "Out": setOf(c.Excluded), "Me": sess}
	if s.Mail != nil {
		boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
		if err != nil {
			s.taskErr(w, r, err, "/marketing")
			return
		}
		data["Boxes"] = boxes
	}
	data["Letter"] = template.HTML(mailpkg.Sanitize(c.Body)) // the letter as its addressees see it
	if c.ID != "" && c.Editable() && len(c.Segments) > 0 {
		if list, err := s.Marketing.Addressees(ctx, sess.UserID, c); err == nil {
			data["Count"] = len(list)
		} else {
			data["CountErr"] = s.errText(r, err)
		}
	}
	if !c.Editable() {
		const per = 100
		pg, _ := strconv.Atoi(q.Get("page"))
		pg = max(pg, 1)
		list, n, err := s.Marketing.Recipients(ctx, c.ID, q.Get("status"), per, (pg-1)*per)
		if err != nil {
			s.taskErr(w, r, err, "/marketing")
			return
		}
		var pages []int
		for i := 1; i <= (n+per-1)/per; i++ {
			pages = append(pages, i)
		}
		data["Recipients"], data["Status"], data["Page"], data["Pages"] = list, q.Get("status"), pg, pages
	}
	title := c.Title
	if c.ID == "" {
		title = s.tr(r, "mkt.new_campaign")
	}
	s.render(w, r, "mktcampaign", page{Title: title, Nav: "marketing", Flash: q.Get("msg"), Data: data})
}

func setOf(list []string) map[string]bool {
	out := map[string]bool{}
	for _, v := range list {
		out[v] = true
	}
	return out
}

// mktCampaignForm reads the editor's form.
func (s *Server) mktCampaignForm(r *http.Request) marketing.Campaign {
	c := marketing.Campaign{ID: r.PathValue("id"), Title: r.FormValue("title"), MailboxID: r.FormValue("box"), Subject: r.FormValue("subject"),
		Body: mailpkg.Sanitize(r.FormValue("html")), Segments: r.Form["seg"], Excluded: r.Form["exclude"]}
	if c.ID == "new" {
		c.ID = ""
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", r.FormValue("send_at"), time.Local); err == nil && r.FormValue("planned") == "1" {
		c.SendAt = &t
	}
	return c
}

// mktCampaignSave keeps the draft; «Отправить» (go=start) then starts it, «Тестовое письмо» (go=test) sends it to
// the address given.
func (s *Server) mktCampaignSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	_ = r.ParseForm()
	c := s.mktCampaignForm(r)
	if c.MailboxID != "" && s.Mail != nil {
		if _, err := s.Mail.Mailbox(r.Context(), sess.UserID, c.MailboxID); err != nil {
			s.taskErr(w, r, err, "/marketing")
			return
		}
	}
	id, err := s.Marketing.SaveCampaign(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), c)
	back := "/marketing/campaigns/" + r.PathValue("id")
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	back, msg := "/marketing/campaigns/"+id, s.tr(r, "mkt.saved")
	switch r.FormValue("go") {
	case "test":
		to := strings.TrimSpace(r.FormValue("test_to"))
		if !marketing.ValidEmail(to) {
			s.taskErr(w, r, i18n.E("err.mail_to"), back)
			return
		}
		saved, err := s.Marketing.Campaign(r.Context(), id)
		if err == nil {
			if saved.MailboxID == "" {
				err = i18n.E("err.mkt_incomplete")
			} else {
				err = s.mktSend(r.Context(), sess.UserID, saved, marketing.Addressee{Email: to, Name: sess.Name, Token: "test"})
			}
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		msg = s.tr(r, "mkt.test_sent", to)
	case "template": // «Сохранить как шаблон»: the draft's letter, named as the campaign
		t := marketing.Template{Name: c.Title, Subject: c.Subject, Body: c.Body}
		if _, err := s.Marketing.SaveTemplate(r.Context(), sess.UserID, false, t); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		msg = s.tr(r, "mkt.tpl_made", t.Name)
	case "start":
		n, err := s.Marketing.Start(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), id)
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		s.Log.Info("campaign started", "user_id", sess.UserID, "campaign", id, "recipients", n)
		msg = s.tr(r, "mkt.started", n)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// mktCampaignAction: pause, resume, stop, copy, delete.
func (s *Server) mktCampaignAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	ctx, id, admin := r.Context(), r.PathValue("id"), tasks.IsAdmin(r.Context())
	back := "/marketing/campaigns/" + id
	var err error
	switch a := r.PathValue("action"); a {
	case "pause":
		err = s.Marketing.SetState(ctx, sess.UserID, admin, id, "paused")
	case "resume":
		err = s.Marketing.SetState(ctx, sess.UserID, admin, id, "sending")
	case "stop":
		err = s.Marketing.SetState(ctx, sess.UserID, admin, id, "stopped")
	case "copy":
		var c marketing.Campaign
		if c, err = s.Marketing.Campaign(ctx, id); err == nil {
			var nid string
			if nid, err = s.Marketing.CopyCampaign(ctx, sess.UserID, id, s.tr(r, "mkt.copy_of", c.Title)); err == nil {
				back = "/marketing/campaigns/" + nid
			}
		}
	case "delete":
		if err = s.Marketing.DeleteCampaign(ctx, sess.UserID, admin, id); err == nil {
			back = "/marketing/campaigns"
		}
	default:
		err = crm.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// mktSend sends one letter of a campaign from its mailbox, as its author (go24-worker; «Тестовое письмо» — as the
// user): {Name} and {Email} take the addressee's, the letter ends with «Отписаться».
func (s *Server) mktSend(ctx context.Context, userID string, c marketing.Campaign, a marketing.Addressee) error {
	if s.Mail == nil || userID == "" || c.MailboxID == "" {
		return i18n.E("err.mkt_incomplete")
	}
	unsub := strings.TrimRight(s.BaseURL, "/") + "/m/u/" + a.Token
	name := a.Name
	if name == "" {
		name = a.Email
	}
	fill := strings.NewReplacer("{Name}", name, "{Email}", a.Email, "{Unsubscribe}", unsub)
	fillHTML := strings.NewReplacer("{Name}", html.EscapeString(name), "{Email}", html.EscapeString(a.Email), "{Unsubscribe}", html.EscapeString(unsub))
	body := fillHTML.Replace(c.Body)
	base := strings.TrimRight(s.BaseURL, "/") + "/m/"
	body = mktLink.ReplaceAllStringFunc(body, func(m string) string { // links followed are counted
		u := html.UnescapeString(mktLink.FindStringSubmatch(m)[1])
		if u == unsub {
			return m
		}
		return `href="` + html.EscapeString(base+"c/"+a.Token+"?u="+url.QueryEscape(u)) + `"`
	})
	body += `<img src="` + html.EscapeString(base+"o/"+a.Token+".gif") + `" width="1" height="1" alt="" style="border:0">` // and the letter opened
	if !strings.Contains(c.Body, "{Unsubscribe}") {
		lang := s.I18n.Match("")
		body += `<hr><p style="font-size:12px;color:#888">` + html.EscapeString(s.I18n.T(lang, "mkt.footer")) + ` <a href="` +
			html.EscapeString(unsub) + `">` + html.EscapeString(s.I18n.T(lang, "mkt.unsubscribe")) + `</a></p>`
	}
	to := (&mail.Address{Name: a.Name, Address: a.Email}).String()
	return s.Mail.SendBulk(ctx, userID, c.MailboxID, mailpkg.Letter{To: to, Subject: fill.Replace(c.Subject), HTML: body}, unsub)
}

// mktLink is a link of a letter.
var mktLink = regexp.MustCompile(`href="(https?://[^"]+)"`)

// mktLetterLinks are the links of the campaign's letter to the addressee, as mktSend writes them.
func (s *Server) mktLetterLinks(c marketing.Campaign, a marketing.Addressee) map[string]bool {
	unsub := strings.TrimRight(s.BaseURL, "/") + "/m/u/" + a.Token
	name := a.Name
	if name == "" {
		name = a.Email
	}
	body := strings.NewReplacer("{Name}", html.EscapeString(name), "{Email}", html.EscapeString(a.Email), "{Unsubscribe}", html.EscapeString(unsub)).Replace(c.Body)
	out := map[string]bool{}
	for _, m := range mktLink.FindAllStringSubmatch(body, -1) {
		out[html.UnescapeString(m[1])] = true
	}
	return out
}

// mktPixel is the letter's 1×1 picture: loaded, the letter counts as opened.
var mktPixel = []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")

// mktOpened is the picture of a letter, no sign-in.
func (s *Server) mktOpened(w http.ResponseWriter, r *http.Request) {
	if s.Marketing != nil {
		if err := s.Marketing.Opened(r.Context(), strings.TrimSuffix(r.PathValue("file"), ".gif")); err != nil {
			s.Log.Warn("campaign opened", "err", err)
		}
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(mktPixel)
}

// mktClicked is a link of a letter, no sign-in: counted, it leads where the letter's link does — only to a link
// of that letter.
func (s *Server) mktClicked(w http.ResponseWriter, r *http.Request) {
	if s.Marketing == nil {
		http.NotFound(w, r)
		return
	}
	u := r.URL.Query().Get("u")
	if r.PathValue("token") == "test" && strings.HasPrefix(u, "http") {
		http.Redirect(w, r, u, http.StatusFound) // a test letter: its author follows their own links
		return
	}
	c, a, err := s.Marketing.Clicked(r.Context(), r.PathValue("token"))
	if err != nil || !s.mktLetterLinks(c, a)[u] {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// MarketingSend is the worker's sender of campaigns: as the campaign's author.
func (s *Server) MarketingSend(ctx context.Context, c marketing.Campaign, a marketing.Addressee) error {
	return s.mktSend(ctx, c.CreatedBy, c, a)
}

func (s *Server) mktSegmentsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	ctx, q := r.Context(), r.URL.Query()
	list, err := s.Marketing.Segments(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	counts := map[string]int{}
	for _, g := range list {
		if a, err := s.Marketing.Audience(ctx, sess.UserID, g); err == nil {
			counts[g.ID] = len(a)
		}
	}
	data := map[string]any{"Tab": "segments", "Segments": list, "Counts": counts, "Me": sess.UserID, "Admin": tasks.IsAdmin(ctx),
		"Entities": marketing.Entities}
	if id := q.Get("edit"); id == "new" {
		ent := q.Get("entity")
		if ent == "" {
			ent = "contact"
		}
		data["Edit"] = &marketing.Segment{Entity: ent}
	} else if id != "" {
		for i := range list {
			if list[i].ID == id {
				data["Edit"] = &list[i]
			}
		}
	}
	if data["Edit"] != nil {
		if data["Users"], err = s.Tasks.ListUsers(ctx); err == nil {
			if data["Dicts"], err = s.CRM.Dicts(ctx); err == nil {
				data["Stages"], err = s.CRM.LeadStages(ctx)
			}
		}
		if err != nil {
			s.taskErr(w, r, err, "/marketing")
			return
		}
	}
	s.render(w, r, "marketing", page{TitleKey: "mkt.segments", Nav: "marketing", Flash: q.Get("msg"), Data: data})
}

func (s *Server) mktSegmentSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	g := marketing.Segment{ID: r.PathValue("id"), Name: r.FormValue("name"), Entity: r.FormValue("entity"), Emails: r.FormValue("emails"),
		Filter: marketing.Filter{Q: strings.TrimSpace(r.FormValue("q")), Responsible: r.FormValue("responsible"), Source: r.FormValue("source"),
			Stage: r.FormValue("stage"), Semantic: r.FormValue("semantic"), From: r.FormValue("from"), To: r.FormValue("to")}}
	if g.ID == "new" {
		g.ID = ""
	}
	id, err := s.Marketing.SaveSegment(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), g)
	if err != nil {
		s.taskErr(w, r, err, "/marketing/segments?edit="+r.PathValue("id"))
		return
	}
	http.Redirect(w, r, "/marketing/segments?msg="+url.QueryEscape(s.tr(r, "mkt.segment_saved"))+"#seg-"+id, http.StatusSeeOther)
}

func (s *Server) mktSegmentDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	if err := s.Marketing.DeleteSegment(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/marketing/segments")
		return
	}
	http.Redirect(w, r, "/marketing/segments", http.StatusSeeOther)
}

func (s *Server) mktStopPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	q := r.URL.Query()
	list, err := s.Marketing.StopList(r.Context(), q.Get("q"), 500)
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	s.render(w, r, "marketing", page{TitleKey: "mkt.stoplist", Nav: "marketing", Flash: q.Get("msg"), Query: q.Get("q"),
		Data: map[string]any{"Tab": "stoplist", "Stop": list, "Q": q.Get("q")}})
}

// mktStopChange adds addresses to the stop list or (remove=…) takes one off.
func (s *Server) mktStopChange(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	msg := ""
	if e := r.FormValue("remove"); e != "" {
		if err := s.Marketing.RemoveStop(r.Context(), e); err != nil {
			s.taskErr(w, r, err, "/marketing/stoplist")
			return
		}
	} else {
		n, err := s.Marketing.AddStop(r.Context(), sess.UserID, r.FormValue("emails"))
		if err != nil {
			s.taskErr(w, r, err, "/marketing/stoplist")
			return
		}
		msg = s.tr(r, "mkt.stop_added", n)
	}
	http.Redirect(w, r, "/marketing/stoplist?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// mktUnsubscribe is «Отписаться» of a letter, no sign-in: the page asks, its button (and the mail clients' own
// one-click List-Unsubscribe-Post) puts the address into the stop list.
func (s *Server) mktUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.Marketing == nil {
		http.NotFound(w, r)
		return
	}
	tok := r.PathValue("token")
	data := map[string]any{"Token": tok}
	if tok == "test" {
		data["Test"] = true
	} else if a, title, err := s.Marketing.ByToken(r.Context(), tok); err != nil {
		data["Gone"] = true
	} else {
		data["Email"], data["Campaign"], data["Done"] = a.Email, title, a.Unsub != nil
		if r.Method == http.MethodPost {
			if _, err := s.Marketing.Unsubscribe(r.Context(), tok); err != nil {
				s.fail(w, "unsubscribe", err)
				return
			}
			if r.FormValue("List-Unsubscribe") != "" { // a mail client's one-click: no page
				w.WriteHeader(http.StatusOK)
				return
			}
			data["Done"] = true
		}
	}
	s.render(w, r, "mktunsub", page{TitleKey: "mkt.unsubscribe", Data: data})
}
