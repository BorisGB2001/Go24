package web

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/docgen"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/sign"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Подпись»: documents sent for signing to the company's employee and the client «Подпись» ----------

// signMax is the biggest document sent for signing.
const signMax = 20 << 20

// signPage is the list of «Подпись»: ?f= one of sign.Filters.
func (s *Server) signPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	f := r.URL.Query().Get("f")
	if !slices.Contains(sign.Filters, f) {
		f = ""
	}
	docs, err := s.Sign.List(r.Context(), sess.UserID, f, sess.AdminActive())
	if err != nil {
		s.fail(w, "sign", err)
		return
	}
	waiting, err := s.Sign.Waiting(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "sign", err)
		return
	}
	data := map[string]any{"Docs": docs, "Filter": f, "Filters": sign.Filters, "Waiting": waiting, "Me": sess.UserID,
		"HR": s.signHRAllowed(r, sess)}
	self, err := s.signSelfTemplates(r, sess)
	if err != nil {
		s.fail(w, "sign", err)
		return
	}
	data["Self"] = len(self) > 0
	if f == "hr" {
		if data["Batches"], err = s.Sign.Batches(r.Context(), sess.UserID, sess.AdminActive()); err != nil {
			s.fail(w, "sign", err)
			return
		}
	}
	if f == "templates" {
		if data["Templates"], err = s.Sign.Templates(r.Context()); err != nil {
			s.fail(w, "sign", err)
			return
		}
		data["Admin"] = sess.AdminActive()
	}
	s.render(w, r, "sign", page{TitleKey: "sign.title", Nav: "sign", Data: data})
}

// signNew is the form to send a document for signing: a document of a CRM card (?kind=&id=&doc=) or a file to upload.
func (s *Server) signNew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	data := map[string]any{"Me": sess.UserID, "ClientName": "", "ClientEmail": "", "Signers": []string{sess.UserID}, "Remind": 1}
	if id := q.Get("tpl"); id != "" { // a template of sending: its document, signers and reminders
		t, err := s.Sign.Template(r.Context(), id)
		if err != nil {
			s.taskErr(w, r, err, "/sign?f=templates")
			return
		}
		data["Tpl"], data["Title"], data["Signers"], data["Remind"] = t, t.Title, t.Signers, t.RemindDays
		data["Reviewers"], data["Observers"] = t.Reviewers, t.Observers
	}
	if q.Get("doc") != "" {
		kind, id := q.Get("kind"), q.Get("id")
		doc, err := s.signSource(r, sess, kind, id, q.Get("doc"))
		if err != nil {
			s.taskErr(w, r, err, "/crm/"+kind+"/"+id)
			return
		}
		v := doc.Values
		data["Kind"], data["ID"], data["Doc"] = kind, id, doc
		data["Title"] = signDocTitle(doc)
		data["ClientName"] = cmp.Or(v["Client.Director"], v["Contact"], v["Name"], v["Client.Title"])
		data["ClientEmail"] = cmp.Or(v["Client.Email"], v["Email"])
	}
	if key := q.Get("collab"); key != "" { // from a collab's chat: its guests are offered as the client
		p, err := s.signCollab(r.Context(), sess, key)
		if err != nil {
			s.taskErr(w, r, err, "/collabs")
			return
		}
		guests := s.signCollabGuests(r.Context(), p)
		data["Collab"], data["Guests"] = p, guests
		if len(guests) == 1 {
			data["ClientName"], data["ClientEmail"] = guests[0].Name, guests[0].Email
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.fail(w, "sign", err)
		return
	}
	data["Users"], data["SideMax"] = users, sign.SideMax
	s.render(w, r, "signnew", page{TitleKey: "sign.new", Nav: "sign", Data: data})
}

func signDocTitle(doc crm.Document) string {
	if doc.Number != "" {
		return doc.TemplateName + " № " + doc.Number
	}
	return doc.TemplateName
}

// signSource is the card's document to send for signing (those who may change the card).
func (s *Server) signSource(r *http.Request, sess auth.Session, kind, id, docID string) (crm.Document, error) {
	if docEntity(kind) == "" {
		return crm.Document{}, crm.ErrNotFound
	}
	if err := s.docCheck(r, sess, kind, id, "write"); err != nil {
		return crm.Document{}, err
	}
	return s.CRM.Document(r.Context(), docEntity(kind), id, docID)
}

// signCreate sends the document for signing: a copy of the file is kept with it, the employee is asked to sign first,
// then the client gets the link by e-mail.
func (s *Server) signCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/new"
	if err := r.ParseMultipartForm(signMax); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.taskErr(w, r, i18n.E("sign.err.file"), back)
		return
	}
	kind, id := r.FormValue("kind"), r.FormValue("id")
	d := sign.Doc{Title: r.FormValue("title"), AuthorID: sess.UserID, RemindDays: formRemind(r)}
	var data []byte
	var name string
	if key := r.FormValue("collab"); key != "" {
		back = "/sign/new?collab=" + url.QueryEscape(key)
		p, err := s.signCollab(r.Context(), sess, key)
		if err != nil {
			s.taskErr(w, r, err, "/collabs")
			return
		}
		d.ProjectID = p.ID
	}
	if docID := r.FormValue("doc"); docID != "" {
		back = "/sign/new?" + url.Values{"kind": {kind}, "id": {id}, "doc": {docID}}.Encode()
		doc, err := s.signSource(r, sess, kind, id, docID)
		var m storages.FileMeta
		if err == nil {
			m, err = s.Storages.FileMeta(r.Context(), doc.FileID)
		}
		if err == nil {
			data, err = s.readFile(r, m, signMax)
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		d.Entity, d.EntityID, d.CRMDocID, name = docEntity(kind), id, doc.ID, m.Name
	} else if tplID := r.FormValue("tpl"); tplID != "" {
		back = "/sign/new?tpl=" + url.QueryEscape(tplID)
		t, err := s.Sign.Template(r.Context(), tplID)
		var m storages.FileMeta
		if err == nil {
			m, err = s.Storages.FileMeta(r.Context(), t.FileID)
		}
		if err == nil {
			data, err = s.readFile(r, m, signMax)
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		name, d.Title = m.Name, cmp.Or(strings.TrimSpace(d.Title), t.Title)
	} else {
		f, h, err := r.FormFile("file")
		if err == nil {
			defer f.Close()
			data, err = io.ReadAll(io.LimitReader(f, signMax+1))
			name = h.Filename
		}
		if err != nil || len(data) > signMax {
			s.taskErr(w, r, i18n.E("sign.err.file"), back)
			return
		}
		if strings.TrimSpace(d.Title) == "" {
			d.Title = strings.TrimSuffix(name, path.Ext(name))
		}
	}
	id, err := s.signStart(r, sess, d, data, name, signWho{Signers: r.Form["signer"], Reviewers: r.Form["reviewer"],
		Observers: r.Form["observer"], Clients: signClients(r.Form["client_name"], r.Form["client_email"])})
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if d.ProjectID != "" {
		if doc, err := s.Sign.Get(r.Context(), id); err == nil {
			s.signCollabSent(r.Context(), s.lang(r), doc)
		}
	}
	if r.FormValue("save_template") == "1" && r.FormValue("doc") == "" && r.FormValue("tpl") == "" { // «Сохранить как шаблон»
		t := sign.Template{Title: d.Title, Signers: r.Form["signer"], Reviewers: r.Form["reviewer"], Observers: r.Form["observer"],
			RemindDays: d.RemindDays, AuthorID: sess.UserID}
		if t.FileID, err = s.signKeep(r, sess.UserID, data, name); err == nil {
			_, err = s.Sign.NewTemplate(r.Context(), t)
		}
		if err != nil {
			s.Log.Warn("sign template", "err", err)
		}
	}
	http.Redirect(w, r, "/sign/"+id+"?msg="+url.QueryEscape(s.tr(r, "sign.sent")), http.StatusSeeOther)
}

// signClients are the clients signing of a form: the rows of names and e-mails, the empty ones left out.
func signClients(names, emails []string) []sign.Party {
	var out []sign.Party
	for i := range max(len(names), len(emails)) {
		var p sign.Party
		if i < len(names) {
			p.Name = strings.TrimSpace(names[i])
		}
		if i < len(emails) {
			p.Email = strings.TrimSpace(emails[i])
		}
		if p.Name != "" || p.Email != "" {
			out = append(out, p)
		}
	}
	return out
}

// signWho are those a document is sent to: the employees approving it (Reviewers) and signing it (Signers), user
// ids in turn, the clients signing after them, the employees observing it.
type signWho struct {
	Signers, Reviewers, Observers []string
	Clients                       []sign.Party
}

// signStart sends the Word document (data; name — its file's) for approving by the reviewers and signing by the
// employees in turn, then by the clients when given, in turn; the pages and the REST API send by it.
func (s *Server) signStart(r *http.Request, sess auth.Session, d sign.Doc, data []byte, name string, who signWho) (string, error) {
	if _, err := docgen.Names(data); err != nil || !strings.EqualFold(path.Ext(name), ".docx") {
		return "", i18n.E("sign.err.file")
	}
	user := func(id string) (tasks.User, error) {
		var u tasks.User
		err := s.DB.QueryRow(r.Context(), `SELECT id::text, name, email, guest FROM users WHERE id::text = $1 AND active`,
			id).Scan(&u.ID, &u.Name, &u.Email, &u.Guest)
		if err != nil || u.Guest {
			return u, sign.ErrParties
		}
		return u, nil
	}
	var parties []sign.Party
	seen := map[string]bool{}
	signers := 0
	for _, role := range []string{"reviewer", ""} {
		ids := who.Signers
		if role == "reviewer" {
			ids = who.Reviewers
		}
		n := 0
		for _, id := range ids {
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			u, err := user(id)
			if err != nil {
				return "", err
			}
			parties = append(parties, sign.Party{UserID: u.ID, Name: u.Name, Email: u.Email, Role: role})
			n++
		}
		if n > sign.SideMax {
			return "", sign.ErrParties
		}
		if role == "" {
			signers = n
		}
	}
	clients := who.Clients
	if signers == 0 || len(clients) > sign.SideMax {
		return "", sign.ErrParties
	}
	for _, id := range who.Observers {
		if id == "" || slices.ContainsFunc(d.Observers, func(o sign.Observer) bool { return o.UserID == id }) {
			continue
		}
		u, err := user(id)
		if err != nil {
			return "", err
		}
		d.Observers = append(d.Observers, sign.Observer{UserID: u.ID, Name: u.Name})
	}
	if len(clients) > 0 {
		for _, c := range clients {
			parties = append(parties, sign.Party{Name: c.Name, Email: c.Email})
		}
		if s.Mail != nil { // the clients get the link and the code from the sender's mailbox
			if boxes, err := s.Mail.Mailboxes(r.Context(), sess.UserID); err != nil || len(boxes) == 0 {
				return "", i18n.E("sign.err.no_mailbox")
			}
		}
	}
	var err error
	d.AuthorID, d.Hash = sess.UserID, sign.Hash(data)
	d.FileID, err = s.signKeep(r, sess.UserID, data, name)
	if err == nil {
		d.ID, err = s.Sign.Create(r.Context(), d, parties)
	}
	if err != nil {
		if d.FileID != "" {
			_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, d.FileID)
		}
		return "", err
	}
	s.Log.Info("sign send", "user_id", sess.UserID, "doc", d.ID, "parties", len(parties))
	if doc, err := s.Sign.Get(r.Context(), d.ID); err == nil {
		s.signTurn(r, doc, doc.Current(), sess.UserID)
	}
	return d.ID, nil
}

// signKeep keeps the Word document sent for signing as a file of go24 of its own; its id.
func (s *Server) signKeep(r *http.Request, userID string, data []byte, name string) (string, error) {
	name = strings.NewReplacer("/", "-", "\\", "-", "\"", "").Replace(name)
	key := path.Join("go24", "sign", randomHex(16), name)
	if err := s.Storages.Put(r.Context(), userID, false, "", key, bytes.NewReader(data), int64(len(data)), docxType); err != nil {
		return "", err
	}
	return s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: docxType, Size: int64(len(data)), UploadedBy: userID})
}

// signTurn tells the signer it is their turn: the employee by a notification (not when they act themselves), the
// client by a letter with the link. false — the letter was not sent.
func (s *Server) signTurn(r *http.Request, d sign.Doc, p *sign.Party, actor string) bool {
	return s.signTell(r.Context(), s.lang(r), d, p, actor, false)
}

// signTell is signTurn in the language given; again — a reminder, not the first word.
func (s *Server) signTell(ctx context.Context, lang string, d sign.Doc, p *sign.Party, actor string, again bool) bool {
	switch {
	case p == nil:
		return true
	case p.Employee():
		if p.UserID != actor {
			kind := "sign_request"
			if p.Reviewer() {
				kind = "sign_review"
			}
			if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
				VALUES ($1, $2, $3, $4, $5)`, p.UserID, d.AuthorID, kind, d.Title, "/sign/"+d.ID); err != nil {
				s.Log.Warn("sign notify", "err", err)
			}
			s.signLog(ctx, d, p, again, true)
		}
		return true
	}
	mail := "sign.mail."
	if again {
		mail = "sign.mail.remind_"
	}
	ok := s.signMailTo(ctx, d, p.Email, s.I18n.T(lang, mail+"subject", d.Title),
		s.I18n.T(lang, mail+"body", p.Name, d.Author, d.Title, s.BaseURL+"/sg/"+p.Token))
	s.signLog(ctx, d, p, again, ok)
	return ok
}

// signLog puts the signer's being told into the document's history (not for the reminder of an HR sending's
// representative: it is of many documents).
func (s *Server) signLog(ctx context.Context, d sign.Doc, p *sign.Party, again, ok bool) {
	kind := sign.EvSent
	if again {
		kind = sign.EvReminded
	}
	if strings.HasPrefix(d.ID, "batch/") {
		return
	}
	if err := s.Sign.Log(ctx, d.ID, p.ID, "", kind, ok, "", ""); err != nil {
		s.Log.Warn("sign log", "doc", d.ID, "err", err)
	}
}

// signMail sends a letter of «Подпись» from the sender's first mailbox in go24.
func (s *Server) signMail(r *http.Request, d sign.Doc, to, subject, body string, files ...mail.Attachment) bool {
	return s.signMailTo(r.Context(), d, to, subject, body, files...)
}

func (s *Server) signMailTo(ctx context.Context, d sign.Doc, to, subject, body string, files ...mail.Attachment) bool {
	if s.Mail == nil {
		return false
	}
	boxes, err := s.Mail.Mailboxes(ctx, d.AuthorID)
	if err != nil || len(boxes) == 0 {
		return false
	}
	if err := s.Mail.Send(ctx, d.AuthorID, boxes[0].ID, to, subject, body, files...); err != nil {
		s.Log.Warn("sign mail", "doc", d.ID, "err", err)
		return false
	}
	return true
}

// signDoc is the document's page: the document, the signers with their state, signing for the employee whose turn
// it is, recalling for the sender.
func (s *Server) signDoc(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.signOf(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/sign")
		return
	}
	cur := d.Current()
	mine := cur != nil && d.Status == "signing" && cur.UserID == sess.UserID
	if mine {
		if err := s.Sign.Log(r.Context(), d.ID, cur.ID, sess.UserID, sign.EvViewed, true, clientIP(r), ""); err != nil {
			s.Log.Warn("sign log", "doc", d.ID, "err", err)
		}
	}
	card := ""
	if k := crmKind(d.Entity); k != "" && d.EntityID != "" {
		card = "/crm/" + k + "/" + d.EntityID + "#documents"
	}
	collab := ""
	if d.ProjectID != "" {
		_ = s.DB.QueryRow(r.Context(), `SELECT key FROM projects WHERE id::text = $1`, d.ProjectID).Scan(&collab)
	}
	events, err := s.Sign.Events(r.Context(), d.ID)
	if err != nil {
		s.taskErr(w, r, err, "/sign")
		return
	}
	viewed := map[string]*time.Time{} // a pointer: the signers who have not opened it are none in the template
	for _, e := range events {
		if e.Kind == sign.EvViewed {
			viewed[e.PartyID] = &e.At
		}
	}
	mySig, myName := "", "" // the signature the employee keeps, to sign with
	if mine && !cur.Reviewer() {
		myName = cur.Name
		if img, err := s.Sign.MySignature(r.Context(), sess.UserID); err == nil && img != nil {
			mySig = "data:image/png;base64," + base64.StdEncoding.EncodeToString(img)
		}
	}
	s.render(w, r, "signdoc", page{Title: d.Title, Nav: "sign", Data: map[string]any{"Doc": d, "Me": sess.UserID,
		"MySig": mySig, "MyName": myName, "Mine": mine, "Review": mine && cur.Reviewer(), "Author": d.AuthorID == sess.UserID, "PDF": s.Office != nil, "Link": s.BaseURL + "/sg/", "Card": card,
		"Collab": collab, "Events": events, "Viewed": viewed}})
}

// signOf is the document of the path for those who see it (the sender, the signers; administrators).
func (s *Server) signOf(r *http.Request, sess auth.Session) (sign.Doc, error) {
	d, err := s.Sign.Get(r.Context(), r.PathValue("id"))
	if err == nil && !d.Sees(sess.UserID) && !sess.AdminActive() && !s.signCollabSees(r, d) {
		err = sign.ErrNotFound
	}
	return d, err
}

// signFile gives the document: pdf (to read it), docx (the file as sent) or result (signed); inline=1 — shown.
func (s *Server) signFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.signOf(r, sess)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.signSend(w, r, d)
}

func (s *Server) signSend(w http.ResponseWriter, r *http.Request, d sign.Doc) {
	id := ""
	var err error
	switch r.PathValue("which") {
	case "docx":
		id = d.FileID
	case "result":
		id = d.ResultFile
	case "pdf":
		if id = d.PDFFileID; id == "" {
			if id, err = s.officePDF(r, d.FileID, d.AuthorID, "sign"); err == nil {
				err = s.Sign.SetPDF(r.Context(), d.ID, id)
			}
		}
	}
	var m storages.FileMeta
	if err == nil && id != "" {
		m, err = s.Storages.FileMeta(r.Context(), id)
	}
	if err != nil || id == "" {
		http.NotFound(w, r)
		return
	}
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		disposition = "inline"
	}
	w.Header().Set("Cache-Control", "no-store")
	s.sendFile(w, r, m, disposition)
}

// signSign: the employee whose turn it is signs (having read the document), or refuses with the reason (refuse=1).
func (s *Server) signSign(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/" + r.PathValue("id")
	d, err := s.signOf(r, sess)
	cur := d.Current()
	if err == nil && (cur == nil || cur.UserID != sess.UserID) {
		err = sign.ErrTurn
	}
	if err == nil && r.FormValue("refuse") != "1" && r.FormValue("agree") != "1" {
		err = i18n.E("sign.err.agree")
	}
	refuse := r.FormValue("refuse") == "1"
	var sig []byte
	if err == nil && !refuse && !cur.Reviewer() {
		sig, err = sign.Signature(r.FormValue("signature"))
	}
	if err == nil && !refuse && r.FormValue("keep") == "1" && sig != nil {
		err = s.Sign.KeepMySignature(r.Context(), sess.UserID, sig)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	msg := s.signAct(r, d, cur, "", sess.UserID, refuse, r.FormValue("reason"), sig)
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// signAct signs (or refuses) for the signer, then the next one's turn comes or the document is finished; the message
// to show (an error too).
func (s *Server) signAct(r *http.Request, d sign.Doc, p *sign.Party, code, actor string, refuse bool, reason string, sig []byte) string {
	msg, err := s.signDo(r, d, p, code, actor, refuse, reason, sig)
	if err != nil {
		return s.signErr(r, err)
	}
	return msg
}

// signDo is signAct giving the error apart (the REST API). sig is the handwritten signature (nil — none: an employee
// signs with the one they keep, if any).
func (s *Server) signDo(r *http.Request, d sign.Doc, p *sign.Party, code, actor string, refuse bool, reason string, sig []byte) (string, error) {
	ip, ua := clientIP(r), r.UserAgent()
	if refuse {
		if err := s.Sign.Refuse(r.Context(), d.ID, p.ID, reason, ip, ua); err != nil {
			return "", err
		}
		s.Log.Info("sign refuse", "doc", d.ID, "party", p.ID)
		if _, err := s.DB.Exec(r.Context(), `INSERT INTO notifications (user_id, actor_id, kind, body, url)
			VALUES ($1, nullif($2, '')::uuid, 'sign_refused', $3, $4)`, d.AuthorID, p.UserID, d.Title+" — "+p.Name, "/sign/"+d.ID); err != nil {
			s.Log.Warn("sign notify", "err", err)
		}
		s.signCollabSay(r.Context(), d, s.tr(r, "sign.collab.refused", d.Title, p.Name, reason))
		s.signTellObservers(r.Context(), d, "sign_refused", d.Title+" — "+p.Name, p.UserID)
		if p.Reviewer() {
			return s.tr(r, "sign.rejected_msg"), nil
		}
		return s.tr(r, "sign.refused_msg"), nil
	}
	if p.Reviewer() { // approves with no signature
		sig = nil
	}
	next, done, err := s.Sign.Sign(r.Context(), d.ID, p.ID, code, ip, ua)
	if err != nil {
		return "", err
	}
	s.Log.Info("sign", "doc", d.ID, "party", p.ID, "done", done)
	if sig == nil && p.Employee() && !p.Reviewer() {
		if sig, err = s.Sign.MySignature(r.Context(), p.UserID); err != nil {
			s.Log.Warn("sign my signature", "user_id", p.UserID, "err", err)
		}
	}
	if sig != nil {
		if err := s.Sign.SetSignature(r.Context(), p.ID, sig); err != nil {
			s.Log.Warn("sign signature", "doc", d.ID, "party", p.ID, "err", err)
		}
	}
	if done {
		s.signFinish(r, d.ID)
		return s.tr(r, "sign.done_msg"), nil
	}
	msg := s.tr(r, "sign.signed_msg")
	if p.Reviewer() {
		msg = s.tr(r, "sign.approved_msg")
	}
	if !s.signTurn(r, d, next, actor) {
		return msg + " " + s.tr(r, "sign.not_mailed"), nil
	}
	return msg, nil
}

// signTellObservers tells the document's observers how it ended (kind: sign_done, sign_refused, sign_cancelled), not
// the one who did it (actor).
func (s *Server) signTellObservers(ctx context.Context, d sign.Doc, kind, body, actor string) {
	for _, o := range d.Observers {
		if o.UserID == actor || o.UserID == d.AuthorID {
			continue
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
			VALUES ($1, nullif($2, '')::uuid, $3, $4, $5)`, o.UserID, actor, kind, body, "/sign/"+d.ID); err != nil {
			s.Log.Warn("sign notify", "err", err)
		}
	}
}

// signErr is the error as a message to show.
func (s *Server) signErr(r *http.Request, err error) string {
	var ie *i18n.Error
	if errors.As(err, &ie) {
		return s.I18n.Err(s.lang(r), ie)
	}
	s.Log.Error("sign", "err", err)
	return s.tr(r, "err.internal")
}

// signFinish makes the signed document — the file with the signing sheet at its end, in PDF when the document
// server is there — and tells the sender; the client gets the signed document by e-mail.
func (s *Server) signFinish(r *http.Request, id string) {
	d, err := s.Sign.Get(r.Context(), id)
	if err != nil {
		s.Log.Error("sign finish", "doc", id, "err", err)
		return
	}
	result, err := s.signResult(r, d)
	if err != nil {
		s.Log.Error("sign result", "doc", id, "err", err)
	}
	if _, err := s.DB.Exec(r.Context(), `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'sign_done', $2, $3)`,
		d.AuthorID, d.Title, "/sign/"+d.ID); err != nil {
		s.Log.Warn("sign notify", "err", err)
	}
	s.signTellObservers(r.Context(), d, "sign_done", d.Title, "")
	if result == "" {
		return
	}
	m, err := s.Storages.FileMeta(r.Context(), result)
	var data []byte
	if err == nil {
		data, err = s.readFile(r, m, mail.MaxSendSize)
	}
	if err != nil {
		return
	}
	s.signCollabDone(r, d, m.Name, m.ContentType, data)
	for _, p := range d.Parties {
		if !p.Employee() {
			s.signMail(r, d, p.Email, s.tr(r, "sign.mail.done_subject", d.Title), s.tr(r, "sign.mail.done_body", p.Name, d.Title),
				mail.Attachment{Name: m.Name, ContentType: m.ContentType, Data: data})
		}
	}
}

// signResult keeps the signed document: the sent file with the signing sheet, in PDF (DOCX with no document server).
func (s *Server) signResult(r *http.Request, d sign.Doc) (string, error) {
	m, err := s.Storages.FileMeta(r.Context(), d.FileID)
	if err != nil {
		return "", err
	}
	src, err := s.readFile(r, m, signMax)
	if err != nil {
		return "", err
	}
	out, err := docgen.AppendSheet(src, s.signSheet(r, d))
	if err != nil {
		return "", err
	}
	name := strings.TrimSuffix(m.Name, path.Ext(m.Name)) + " — " + s.tr(r, "sign.signed_suffix") + ".docx"
	key := path.Join("go24", "sign", randomHex(16), name)
	if err := s.Storages.Put(r.Context(), d.AuthorID, false, "", key, bytes.NewReader(out), int64(len(out)), docxType); err != nil {
		return "", err
	}
	file, err := s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: docxType, Size: int64(len(out)), UploadedBy: d.AuthorID})
	if err != nil {
		return "", err
	}
	if s.Office != nil {
		if pdf, err := s.officePDF(r, file, d.AuthorID, "sign"); err == nil {
			_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, file)
			file = pdf
		} else {
			s.Log.Warn("sign result pdf", "doc", d.ID, "err", err)
		}
	}
	return file, s.Sign.SetResult(r.Context(), d.ID, file)
}

// signSheet is «Лист подписания»: the document, its checksum and who signed it, when, how and from where.
func (s *Server) signSheet(r *http.Request, d sign.Doc) docgen.Sheet {
	when := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.In(time.Local).Format("02.01.2006 15:04:05 MST")
	}
	sh := docgen.Sheet{Title: s.tr(r, "sign.sheet.title"), Lines: []string{
		s.tr(r, "sign.sheet.doc", d.Title),
		s.tr(r, "sign.sheet.sent", d.Author, when(&d.CreatedAt)),
		s.tr(r, "sign.sheet.hash", d.Hash),
		s.tr(r, "sign.sheet.about"),
	}, Header: []string{"№", s.tr(r, "sign.sheet.signer"), s.tr(r, "sign.sheet.side"), s.tr(r, "sign.sheet.how"),
		s.tr(r, "sign.sheet.when"), s.tr(r, "sign.sheet.ip")}}
	pics := slices.ContainsFunc(d.Parties, func(p sign.Party) bool { return p.HasSignature })
	if pics {
		sh.Header, sh.Images = append(sh.Header, s.tr(r, "sign.sheet.signature")), map[int]docgen.Image{}
	}
	for i, p := range d.Parties {
		side, how := s.tr(r, "sign.side.company"), s.tr(r, "sign.how.employee")
		if p.Side == "employee" {
			side = s.tr(r, "sign.side.employee")
		} else if p.Reviewer() {
			how = s.tr(r, "sign.how.reviewer")
		} else if !p.Employee() {
			side, how = s.tr(r, "sign.side.client"), s.tr(r, "sign.how.code", p.Email)
		}
		who := p.Name
		if p.Email != "" {
			who += "\n" + p.Email
		}
		row := []string{fmt.Sprint(p.Ord), who, side, how, when(p.SignedAt), p.IP}
		if pics {
			row = append(row, "")
			if img, err := s.Sign.PartySignature(r.Context(), p.ID); err == nil && img != nil {
				sh.Images[i] = docgen.Image{Data: img, Width: docgen.Cm(3)}
			}
		}
		sh.Rows = append(sh.Rows, row)
	}
	return sh
}

// signSignature gives a signer's handwritten signature (PNG) to those who see the document.
func (s *Server) signSignature(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.signOf(r, sess)
	s.signSignatureSend(w, r, d, err)
}

// signSignatureSend gives the signature of the path's signer of the document (err — the document is not seen).
func (s *Server) signSignatureSend(w http.ResponseWriter, r *http.Request, d sign.Doc, err error) {
	p := d.Party(r.PathValue("party"))
	var img []byte
	if err == nil && p != nil {
		img, err = s.Sign.PartySignature(r.Context(), p.ID)
	}
	if err != nil || p == nil || img == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(img)
}

// signForget: the employee forgets the signature they keep to sign with.
func (s *Server) signForget(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/sign/") || strings.HasPrefix(back, "//") {
		back = "/sign"
	}
	if err := s.Sign.KeepMySignature(r.Context(), sess.UserID, nil); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// signCancel: the sender recalls the document while it is being signed.
func (s *Server) signCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/" + r.PathValue("id")
	d, err := s.signOf(r, sess)
	if err == nil && d.AuthorID != sess.UserID && !sess.AdminActive() {
		err = sign.ErrNotFound
	}
	if err == nil {
		err = s.Sign.Cancel(r.Context(), d.ID, sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("sign cancel", "user_id", sess.UserID, "doc", d.ID)
	s.signTellObservers(r.Context(), d, "sign_cancelled", d.Title, sess.UserID)
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "sign.cancelled_msg")), http.StatusSeeOther)
}

// signResend sends the client the letter with the link again.
func (s *Server) signResend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/" + r.PathValue("id")
	d, err := s.signOf(r, sess)
	cur := d.Current()
	if err == nil && (d.AuthorID != sess.UserID || cur == nil || cur.Employee() || d.Status != "signing") {
		err = sign.ErrTurn
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	msg := s.tr(r, "sign.resent", cur.Email)
	if !s.signTurn(r, d, cur, sess.UserID) {
		msg = s.tr(r, "sign.not_mailed")
	} else if err := s.Sign.Reminded(r.Context(), cur.ID); err != nil {
		s.Log.Warn("sign reminded", "err", err)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// ---------- the client's page: no sign-in, the personal link /sg/{token} ----------

// signPublic is the client's page: the document, «Получить код» to the client's e-mail, signing with the code or
// refusing.
func (s *Server) signPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	d, p, err := s.Sign.ByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	turn := d.Status == "signing" && p.Status == "ready"
	if turn {
		if err := s.Sign.Log(r.Context(), d.ID, p.ID, "", sign.EvViewed, true, clientIP(r), ""); err != nil {
			s.Log.Warn("sign log", "doc", d.ID, "err", err)
		}
	}
	s.render(w, r, "signpub", page{Title: d.Title, Data: map[string]any{"Doc": d, "Party": p, "Token": p.Token,
		"Turn": turn, "PDF": s.Office != nil, "Email": maskEmail(p.Email),
		"Msg": r.URL.Query().Get("msg"), "Code": r.URL.Query().Get("code") == "1"}})
}

// maskEmail hides the most of the address: a***@example.com.
func maskEmail(e string) string {
	user, host, ok := strings.Cut(e, "@")
	if !ok || user == "" {
		return e
	}
	r := []rune(user)
	return string(r[:1]) + "***@" + host
}

func (s *Server) signPublicFile(w http.ResponseWriter, r *http.Request) {
	d, _, err := s.Sign.ByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.signSend(w, r, d)
}

// signCode sends the client a code to sign with (good for sign.CodeTTL).
func (s *Server) signCode(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	d, p, err := s.Sign.ByToken(r.Context(), tok)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	code := fmt.Sprintf("%06d", n.Int64())
	if err == nil && d.Status != "signing" {
		err = sign.ErrTurn
	}
	if err == nil {
		err = s.Sign.SetCode(r.Context(), p.ID, code)
	}
	msg, more := "", "&code=1"
	switch {
	case err != nil:
		msg, more = s.signErr(r, err), ""
	case !s.signMail(r, d, p.Email, s.tr(r, "sign.mail.code_subject", code), s.tr(r, "sign.mail.code_body", code, d.Title, int(sign.CodeTTL.Minutes()))):
		msg, more = s.tr(r, "sign.err.code_mail"), ""
		if err := s.Sign.ClearCode(r.Context(), p.ID); err != nil {
			s.Log.Warn("sign code", "err", err)
		}
		if err := s.Sign.Log(r.Context(), d.ID, p.ID, "", sign.EvCode, false, clientIP(r), ""); err != nil {
			s.Log.Warn("sign log", "doc", d.ID, "err", err)
		}
	default:
		msg = s.tr(r, "sign.code_sent", maskEmail(p.Email))
		s.Log.Info("sign code", "doc", d.ID, "party", p.ID)
		if err := s.Sign.Log(r.Context(), d.ID, p.ID, "", sign.EvCode, true, clientIP(r), ""); err != nil {
			s.Log.Warn("sign log", "doc", d.ID, "err", err)
		}
	}
	http.Redirect(w, r, "/sg/"+tok+"?msg="+url.QueryEscape(msg)+more, http.StatusSeeOther)
}

// signPublicSign: the client signs with the code (or refuses, refuse=1).
func (s *Server) signPublicSign(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	d, p, err := s.Sign.ByToken(r.Context(), tok)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	more := ""
	var msg string
	refuse := r.FormValue("refuse") == "1"
	var sig []byte
	var err2 error
	if !refuse {
		sig, err2 = sign.Signature(r.FormValue("signature"))
	}
	if !refuse && r.FormValue("agree") != "1" {
		msg, more = s.tr(r, "sign.err.agree"), "&code=1"
	} else if err2 != nil {
		msg, more = s.signErr(r, err2), "&code=1"
	} else if msg = s.signAct(r, d, p, r.FormValue("code"), "", refuse, r.FormValue("reason"), sig); !refuse {
		if after, _, _ := s.Sign.ByToken(r.Context(), tok); after.Party(p.ID) != nil && after.Party(p.ID).Status == "ready" {
			more = "&code=1" // a wrong code: the field stays
			time.Sleep(time.Second)
		}
	}
	http.Redirect(w, r, "/sg/"+tok+"?msg="+url.QueryEscape(msg)+more, http.StatusSeeOther)
}

// clientIP is the address the request came from (the first of X-Forwarded-For behind the proxy).
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		first, _, _ := strings.Cut(f, ",")
		return strings.TrimSpace(first)
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
