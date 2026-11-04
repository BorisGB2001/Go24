package web

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math/big"
	"mime"
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
	"go24/internal/payqr"
	"go24/internal/sign"
	"go24/internal/sms"
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
	lf := signListFilter(r)
	all, err := s.signListScope(r.Context(), sess, &lf)
	var docs []sign.Doc
	if err == nil {
		docs, err = s.Sign.Find(r.Context(), sess.UserID, f, all, lf)
	}
	if err != nil {
		s.fail(w, "sign", err)
		return
	}
	waiting, err := s.Sign.Waiting(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "sign", err)
		return
	}
	view := r.URL.Query().Get("view")
	if view != "kanban" {
		view = "list"
	}
	data := map[string]any{"Docs": docs, "Filter": f, "Filters": sign.Filters, "Waiting": waiting, "Me": sess.UserID,
		"HR": s.signHRAllowed(r, sess), "LF": lf, "View": view, "Query": signListQuery(lf, view),
		"Base": signListQuery(lf, ""), "To": ""}
	if !lf.To.IsZero() {
		data["To"] = lf.To.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	if view == "kanban" {
		data["Columns"] = signColumns(docs)
	}
	if !slices.Contains([]string{"hr", "safe", "templates"}, f) {
		if data["Users"], err = s.Tasks.ListUsers(r.Context()); err != nil {
			s.fail(w, "sign", err)
			return
		}
	}
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
	if f == "safe" {
		if err := s.signSafeData(r, sess, data); err != nil {
			s.fail(w, "sign", err)
			return
		}
	}
	if f == "templates" {
		if data["Templates"], err = s.Sign.Templates(r.Context()); err != nil {
			s.fail(w, "sign", err)
			return
		}
		data["TplAll"] = s.signPerms(r.Context(), sess).Has("templates", "all")
	}
	data["Admin"], data["Send"] = sess.Admin, s.signPerms(r.Context(), sess).Has("send", "all")
	s.render(w, r, "sign", page{TitleKey: "sign.title", Nav: "sign", Data: data})
}

// signListFilter is the filter of the list of the query: q, author, from, to (YYYY-MM-DD, local days).
func signListFilter(r *http.Request) sign.ListFilter {
	q := r.URL.Query()
	lf := sign.ListFilter{Q: strings.TrimSpace(q.Get("q")), Author: q.Get("author")}
	lf.From, _ = time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local)
	if to, err := time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local); err == nil {
		lf.To = to.AddDate(0, 0, 1)
	}
	return lf
}

// signListQuery is the filter and the view as the query of the list, kept by its tabs.
func signListQuery(lf sign.ListFilter, view string) template.URL {
	v := url.Values{}
	for k, x := range map[string]string{"q": lf.Q, "author": lf.Author} {
		if x != "" {
			v.Set(k, x)
		}
	}
	if !lf.From.IsZero() {
		v.Set("from", lf.From.Format(time.DateOnly))
	}
	if !lf.To.IsZero() {
		v.Set("to", lf.To.AddDate(0, 0, -1).Format(time.DateOnly))
	}
	if view == "kanban" {
		v.Set("view", view)
	}
	return template.URL(v.Encode())
}

// signColumn is a column of the kanban of «Подпись»: the documents of a status.
type signColumn struct {
	Status string
	Docs   []sign.Doc
}

// signStatuses are the kanban's columns, as the stages of the documents: being signed, signed, then how it failed.
var signStatuses = []string{"signing", "done", "refused", "expired", "cancelled"}

// signColumns groups the documents by status into the kanban's columns.
func signColumns(docs []sign.Doc) []signColumn {
	cols := make([]signColumn, len(signStatuses))
	for i, st := range signStatuses {
		cols[i].Status = st
	}
	for _, d := range docs {
		if i := slices.Index(signStatuses, d.Status); i >= 0 {
			cols[i].Docs = append(cols[i].Docs, d)
		}
	}
	return cols
}

// signNew is the form to send a document for signing: a document of a CRM card (?kind=&id=&doc=) or a file to upload.
func (s *Server) signNew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.signPerms(r.Context(), sess).Has("send", "all") {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	data := map[string]any{"Me": sess.UserID, "ClientName": "", "ClientEmail": "", "ClientPhone": "", "Chat": "", "InChat": "", "Signers": []string{sess.UserID}, "Remind": 1}
	if id := q.Get("tpl"); id != "" { // a template of sending: its document, signers and reminders
		t, err := s.Sign.Template(r.Context(), id)
		if err != nil {
			s.taskErr(w, r, err, "/sign?f=templates")
			return
		}
		data["Tpl"], data["Title"], data["Signers"], data["Remind"] = t, t.Title, t.Signers, t.RemindDays
		if dl := sign.DeadlineIn(time.Now(), t.DeadlineDays); dl != nil {
			data["Deadline"] = dl.Format(time.DateOnly)
		}
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
		data["ClientPhone"] = cmp.Or(v["Client.Phone"], v["Phone"])
		if s.SignChatReady(r.Context(), docEntity(kind), id) { // «В чат», chosen when the client has no other way
			data["Chat"] = "1"
			if data["ClientEmail"] == "" && data["ClientPhone"] == "" {
				data["InChat"] = "1"
			}
		}
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
	data["Users"], data["SideMax"], data["Today"] = users, sign.SideMax, time.Now().Format(time.DateOnly)
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

// SignRobot is the hook of the CRM robot «Отправить на подпись» (crm.Store.SendSign): the card's document sent by the
// user for signing by the employees, then by the client; says the signing's title.
func (s *Server) SignRobot(ctx context.Context, userID, entity, entityID, docID string, signers []string, client, email, phone string) (string, error) {
	doc, err := s.CRM.Document(ctx, entity, entityID, docID)
	if err != nil {
		return "", err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	if err != nil {
		return "", err
	}
	m, err := s.Storages.FileMeta(ctx, doc.FileID)
	var data []byte
	if err == nil {
		data, err = s.readFile(r, m, signMax)
	}
	if err != nil {
		return "", err
	}
	d := sign.Doc{Title: signDocTitle(doc), AuthorID: userID, RemindDays: 1, Entity: entity, EntityID: entityID, CRMDocID: doc.ID}
	_, err = s.signStart(r, auth.Session{UserID: userID}, d, data, m.Name, signWho{Signers: signers,
		Clients: []sign.Party{{Name: client, Email: email, Phone: phone}}})
	return d.Title, err
}

// signCreate sends the document for signing: a copy of the file is kept with it, the employee is asked to sign first,
// then the client gets the link by e-mail.
func (s *Server) signCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/new"
	if !s.signPerms(r.Context(), sess).Has("send", "all") {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
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
		var err error
		if data, name, err = s.signUploads(r, sess.UserID); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		if strings.TrimSpace(d.Title) == "" {
			d.Title = strings.TrimSuffix(name, path.Ext(name))
		}
	}
	var err error
	if d.Deadline, err = formDeadline(r); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	id, err = s.signStart(r, sess, d, data, name, signWho{Signers: r.Form["signer"], Reviewers: r.Form["reviewer"],
		Observers: r.Form["observer"], Clients: signClients(r.Form["client_name"], r.Form["client_email"], r.Form["client_phone"], r.Form["client_via"]...), QES: r.FormValue("qes")})
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
			RemindDays: d.RemindDays, DeadlineDays: sign.Days(time.Now(), d.Deadline), AuthorID: sess.UserID}
		if t.FileID, err = s.signKeep(r, sess.UserID, data, name); err == nil {
			_, err = s.Sign.NewTemplate(r.Context(), t)
		}
		if err != nil {
			s.Log.Warn("sign template", "err", err)
		}
	}
	http.Redirect(w, r, "/sign/"+id+"?msg="+url.QueryEscape(s.tr(r, "sign.sent")), http.StatusSeeOther)
}

// signClients are the clients signing of a form: the rows of names, e-mails and phones, the empty ones left out; vias
// (when given) are how each gets the link: "chat" — in the card's chat of the open lines (signStart finds it).
func signClients(names, emails, phones []string, vias ...string) []sign.Party {
	var out []sign.Party
	at := func(list []string, i int) string {
		if i < len(list) {
			return strings.TrimSpace(list[i])
		}
		return ""
	}
	for i := range max(len(names), len(emails), len(phones)) {
		p := sign.Party{Name: at(names, i), Email: at(emails, i), Phone: at(phones, i)}
		if at(vias, i) == "chat" {
			p.Chat = "chat"
		}
		if p.Name != "" || p.Email != "" || p.Phone != "" {
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
	QES                           string // who signs with КЭП: "" — none (a simple signature), company — the employees, all — the clients too
}

// signStart sends the document (data; name — its file's: Word, PDF, a picture or another office document, see
// signPrepare) for approving by the reviewers and signing by the employees in turn, then by the clients when given, in
// turn; the pages and the REST API send by it.
func (s *Server) signStart(r *http.Request, sess auth.Session, d sign.Doc, data []byte, name string, who signWho) (string, error) {
	data, name, err := s.signPrepare(r, sess.UserID, data, name)
	if err != nil {
		return "", err
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
			parties = append(parties, sign.Party{UserID: u.ID, Name: u.Name, Email: u.Email, Role: role, Method: signMethod(who.QES != "")})
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
		mailed := false
		for _, c := range clients {
			if c.Chat, err = s.signChatOf(r.Context(), d, c); err != nil {
				return "", err
			}
			if c.Phone != "" { // the phone: for the link and the code by SMS, the only way when there is no e-mail
				if c.Phone = sms.Normalize(c.Phone); c.Phone == "" {
					return "", i18n.E("err.sms_phone")
				}
				if c.Email == "" && !s.signSMSReady(r.Context()) {
					return "", i18n.E("sign.err.no_sms")
				}
			}
			mailed = mailed || c.Email != ""
			parties = append(parties, sign.Party{Name: c.Name, Email: c.Email, Phone: c.Phone, Chat: c.Chat, Method: signMethod(who.QES == "all")})
		}
		if s.Mail != nil && mailed { // the clients get the link and the code from the sender's mailbox
			if boxes, err := s.Mail.Mailboxes(r.Context(), sess.UserID); err != nil || len(boxes) == 0 {
				return "", i18n.E("sign.err.no_mailbox")
			}
		}
	}
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

// signMethod is the method of a signer: qes — КЭП, "" — a simple signature.
func signMethod(qualified bool) string {
	if qualified {
		return "qes"
	}
	return ""
}

// signPrepare is the document to sign of the file sent: Word (.docx) and PDF as they are, a picture
// (JPEG, PNG) as a page of PDF, DOC, ODT and RTF as the PDF the document server makes of them.
func (s *Server) signPrepare(r *http.Request, userID string, data []byte, name string) ([]byte, string, error) {
	ext := strings.ToLower(path.Ext(name))
	pdfName := strings.TrimSuffix(name, path.Ext(name)) + ".pdf"
	switch ext {
	case ".docx":
		if _, err := docgen.Names(data); err == nil {
			return data, name, nil
		}
	case ".pdf":
		if docgen.IsPDF(data) {
			return data, name, nil
		}
	case ".jpg", ".jpeg", ".png":
		if pdf, err := docgen.ImagePDF(data); err == nil {
			return pdf, pdfName, nil
		}
	case ".doc", ".odt", ".rtf":
		if s.Office == nil {
			break
		}
		pdf, err := s.signOfficePDF(r, userID, data, name)
		if err != nil {
			return nil, "", err
		}
		return pdf, pdfName, nil
	}
	return nil, "", i18n.E("sign.err.file")
}

// signOfficePDF is the office document (name — its file's) made a PDF by the document server.
func (s *Server) signOfficePDF(r *http.Request, userID string, data []byte, name string) ([]byte, error) {
	if s.Office == nil {
		return nil, i18n.E("docs.pdf_failed")
	}
	src, err := s.signKeep(r, userID, data, name)
	if err != nil {
		return nil, err
	}
	pdf, err := s.officePDF(r, src, userID, "sign")
	defer func() {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = ANY($1)`, []string{src, pdf})
	}()
	if err != nil {
		return nil, err
	}
	var m storages.FileMeta
	if m, err = s.Storages.FileMeta(r.Context(), pdf); err == nil {
		data, err = s.readFile(r, m, signMax)
	}
	if err != nil || !docgen.IsPDF(data) {
		return nil, i18n.E("docs.pdf_failed")
	}
	return data, nil
}

// signFilesMax is how many files are sent for signing at once (joined into one document).
const signFilesMax = 50

// signUploads is the document of the form's «file» fields: one file as it is, or several (scanned pages, say) joined
// into one PDF in their order by signJoin; signMax in all.
func (s *Server) signUploads(r *http.Request, userID string) ([]byte, string, error) {
	if r.MultipartForm == nil {
		if _, _, err := r.FormFile("file"); err != nil {
			return nil, "", i18n.E("sign.err.file")
		}
	}
	hdrs := r.MultipartForm.File["file"]
	if len(hdrs) == 0 || len(hdrs) > signFilesMax {
		return nil, "", i18n.E("sign.err.file")
	}
	var parts [][]byte
	var names []string
	total := 0
	for _, h := range hdrs {
		f, err := h.Open()
		if err != nil {
			return nil, "", i18n.E("sign.err.file")
		}
		data, err := io.ReadAll(io.LimitReader(f, int64(signMax-total+1)))
		f.Close()
		if total += len(data); err != nil || total > signMax {
			return nil, "", i18n.E("sign.err.file")
		}
		parts, names = append(parts, data), append(names, h.Filename)
	}
	if len(parts) == 1 {
		return parts[0], names[0], nil
	}
	data, err := s.signJoin(r, userID, parts, names)
	if err != nil {
		return nil, "", err
	}
	return data, strings.TrimSuffix(names[0], path.Ext(names[0])) + ".pdf", nil
}

// signJoin makes one PDF of the files (see signPrepare): the pictures in a row become pages of their own, a PDF goes
// in as it is, an office document — as the document server makes it a PDF.
func (s *Server) signJoin(r *http.Request, userID string, parts [][]byte, names []string) ([]byte, error) {
	var pdfs, pics [][]byte
	flush := func() error {
		if len(pics) == 0 {
			return nil
		}
		pdf, err := docgen.ImagesPDF(pics...)
		pdfs, pics = append(pdfs, pdf), nil
		return err
	}
	for i, data := range parts {
		switch strings.ToLower(path.Ext(names[i])) {
		case ".jpg", ".jpeg", ".png":
			pics = append(pics, data)
			continue
		}
		if err := flush(); err != nil {
			return nil, i18n.E("sign.err.file")
		}
		data, name, err := s.signPrepare(r, userID, data, names[i])
		if err == nil && !strings.EqualFold(path.Ext(name), ".pdf") { // Word
			data, err = s.signOfficePDF(r, userID, data, name)
		}
		if err != nil {
			return nil, err
		}
		pdfs = append(pdfs, data)
	}
	if err := flush(); err != nil {
		return nil, i18n.E("sign.err.file")
	}
	if len(pdfs) == 1 {
		return pdfs[0], nil
	}
	out, err := docgen.JoinPDF(r.Context(), pdfs...)
	if err != nil {
		s.Log.Error("sign join", "err", err)
		return nil, i18n.E("docs.pdf_failed")
	}
	return out, nil
}

// signKeep keeps the document sent for signing as a file of go24 of its own; its id.
func (s *Server) signKeep(r *http.Request, userID string, data []byte, name string) (string, error) {
	name = strings.NewReplacer("/", "-", "\\", "-", "\"", "").Replace(name)
	key := path.Join("go24", "sign", randomHex(16), name)
	ctype := docxType
	if ext := strings.ToLower(path.Ext(name)); ext != ".docx" {
		ctype = cmp.Or(mime.TypeByExtension(ext), "application/octet-stream")
	}
	if err := s.Storages.Put(r.Context(), userID, false, "", key, bytes.NewReader(data), int64(len(data)), ctype); err != nil {
		return "", err
	}
	return s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: ctype, Size: int64(len(data)), UploadedBy: userID})
}

// signIsPDF tells the document sent as a PDF (a PDF, a picture, an office document but Word) from a Word one.
func signIsPDF(m storages.FileMeta) bool {
	return strings.EqualFold(path.Ext(m.Name), ".pdf")
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
	link := s.BaseURL + "/sg/" + p.Token
	if p.Chat != "" && s.signChat(ctx, d, p.Chat, s.I18n.T(lang, "sign.chat.link", d.Author, d.Title, link)) {
		s.signLog(ctx, d, p, again, true, "chat")
		return true
	}
	ok := p.Email != "" && s.signMailTo(ctx, d, p.Email, s.I18n.T(lang, mail+"subject", d.Title),
		s.I18n.T(lang, mail+"body", p.Name, d.Author, d.Title, link))
	if !ok && p.Phone != "" { // no e-mail or the letter did not go: the link by SMS
		ok = s.signSMS(ctx, d, p.Phone, s.I18n.T(lang, "sign.sms.link", d.Author, d.Title, link))
	}
	s.signLog(ctx, d, p, again, ok)
	return ok
}

// signSMSReady: the clients may get the link and the code by SMS — a provider of SMS is set up in the CRM.
func (s *Server) signSMSReady(ctx context.Context) bool {
	return s.SMS != nil && s.SMS.Ready(ctx)
}

// signSMS sends an SMS of «Подпись» from the document's sender (kept in the log of SMS, not on the CRM card: a code
// must not be seen there); false — it did not go.
func (s *Server) signSMS(ctx context.Context, d sign.Doc, phone, text string) bool {
	if !s.signSMSReady(ctx) {
		return false
	}
	if _, err := s.SMS.Send(ctx, sms.Out{Phone: phone, Text: text, AuthorID: d.AuthorID}); err != nil {
		s.Log.Warn("sign sms", "doc", d.ID, "err", err)
		return false
	}
	return true
}

// signLog puts the signer's being told into the document's history (not for the reminder of an HR sending's
// representative: it is of many documents); via — how the client was told, when not the usual way ("chat").
func (s *Server) signLog(ctx context.Context, d sign.Doc, p *sign.Party, again, ok bool, via ...string) {
	kind := sign.EvSent
	if again {
		kind = sign.EvReminded
	}
	if strings.HasPrefix(d.ID, "batch/") {
		return
	}
	if err := s.Sign.Log(ctx, d.ID, p.ID, "", kind, ok, "", strings.Join(via, "")); err != nil {
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
	blocks, err := s.Sign.Blocks(r.Context(), d.ID)
	if err != nil {
		s.taskErr(w, r, err, "/sign")
		return
	}
	var fields, filled []sign.Block
	if mine {
		fields = s.signMyFields(r, d, cur)
	}
	for _, b := range blocks {
		if b.Kind == "field" && b.Value != "" {
			filled = append(filled, b)
		}
	}
	s.render(w, r, "signdoc", page{Title: d.Title, Nav: "sign", Data: map[string]any{"Doc": d, "Fields": fields, "Filled": filled, "CheckCode": sign.ShowCode(d.VerifyCode), "Me": sess.UserID, "Today": time.Now().Format(time.DateOnly),
		"MySig": mySig, "MyName": myName, "Mine": mine, "Review": mine && cur.Reviewer(), "QES": mine && cur.Qualified(), "Author": s.signEdits(r.Context(), sess, d), "PDF": s.Office != nil, "Link": s.BaseURL + "/sg/", "Card": card,
		"Collab": collab, "Events": events, "Viewed": viewed, "Blocks": len(blocks), "BlocksEdit": signBlocksEditable(d) && s.signEdits(r.Context(), sess, d)}})
}

// signOf is the document of the path for those who see it (the sender, the signers, the observers; by the rights
// «docs» and «edit»; administrators).
func (s *Server) signOf(r *http.Request, sess auth.Session) (sign.Doc, error) {
	d, err := s.Sign.Get(r.Context(), r.PathValue("id"))
	if err == nil && !d.Sees(sess.UserID) && !s.signLevel(r.Context(), sess, "docs", d.AuthorID) && !s.signEdits(r.Context(), sess, d) &&
		!s.signCollabSees(r, d) && !s.signSafeSees(r, sess, d) {
		err = sign.ErrNotFound
	}
	return d, err
}

// signFile gives the document: pdf (to read it), docx (the file as sent: Word or PDF) or result (signed); inline=1 —
// shown.
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
		id, err = s.signPDFID(r, d)
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

// signPDFID is the file of the document in PDF, to read it: the file sent, when a PDF, or its PDF made by the
// document server (once).
func (s *Server) signPDFID(r *http.Request, d sign.Doc) (string, error) {
	if d.PDFFileID != "" {
		return d.PDFFileID, nil
	}
	src, err := s.Storages.FileMeta(r.Context(), d.FileID)
	if err == nil && signIsPDF(src) {
		return d.FileID, nil
	}
	id, err := s.officePDF(r, d.FileID, d.AuthorID, "sign")
	if err == nil {
		err = s.Sign.SetPDF(r.Context(), d.ID, id)
	}
	return id, err
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
	if err == nil && !refuse && !cur.Reviewer() && !cur.Qualified() {
		sig, err = sign.Signature(r.FormValue("signature"))
	}
	if err == nil && !refuse {
		err = s.signFields(r, d, cur, signFieldsForm(r))
	}
	if err == nil && !refuse && cur.Qualified() {
		err = s.signQES(r, d, cur, []byte(r.FormValue("cms")))
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
	if sig == nil && p.Employee() && !p.Reviewer() && !p.Qualified() {
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
	if d.Entity != "" && d.EntityID != "" {
		if err := s.CRM.DocumentSigned(r.Context(), d.Entity, d.EntityID, d.AuthorID, d.Title); err != nil {
			s.Log.Warn("sign crm", "doc", d.ID, "err", err)
		}
	}
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
		switch {
		case p.Employee():
		case p.Chat != "" && s.signChat(r.Context(), d, p.Chat, s.tr(r, "sign.chat.done", d.Title, s.BaseURL+"/sg/"+p.Token)):
		case p.Email != "":
			s.signMail(r, d, p.Email, s.tr(r, "sign.mail.done_subject", d.Title), s.tr(r, "sign.mail.done_body", p.Name, d.Title),
				mail.Attachment{Name: m.Name, ContentType: m.ContentType, Data: data})
		case p.Phone != "": // with no e-mail: the link to the signed document by SMS
			s.signSMS(r.Context(), d, p.Phone, s.tr(r, "sign.sms.done", d.Title, s.BaseURL+"/sg/"+p.Token))
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
	if signIsPDF(m) {
		return s.signResultPDF(r, d, m.Name, src)
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
	hash := sign.Hash(out)
	if s.Office != nil {
		if pdf, err := s.officePDF(r, file, d.AuthorID, "sign"); err == nil {
			_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, file)
			file, hash = pdf, ""
			if pm, err := s.Storages.FileMeta(r.Context(), pdf); err == nil {
				if data, err := s.readFile(r, pm, signMax); err == nil {
					hash = sign.Hash(data)
					if out, ok := s.signStamp(r, d, data); ok {
						if f, err := s.signKeep(r, d.AuthorID, out, pm.Name); err == nil {
							_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, pdf)
							file, hash = f, sign.Hash(out)
						}
					}
				}
			}
		} else {
			s.Log.Warn("sign result pdf", "doc", d.ID, "err", err)
		}
	}
	return file, s.Sign.SetResult(r.Context(), d.ID, file, hash)
}

// signResultPDF is signResult of a document sent as a PDF: the signing sheet is made a PDF of its own by the
// document server and put after the document's pages.
func (s *Server) signResultPDF(r *http.Request, d sign.Doc, name string, src []byte) (string, error) {
	sheet, err := docgen.SheetDoc(s.signSheet(r, d))
	if err != nil {
		return "", err
	}
	if s.Office == nil {
		return "", i18n.E("docs.pdf_failed")
	}
	tmp, err := s.signKeep(r, d.AuthorID, sheet, s.tr(r, "sign.sheet.title")+".docx")
	if err != nil {
		return "", err
	}
	pdf, err := s.officePDF(r, tmp, d.AuthorID, "sign")
	defer func() {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = ANY($1)`, []string{tmp, pdf})
	}()
	var page []byte
	if err == nil {
		var m storages.FileMeta
		if m, err = s.Storages.FileMeta(r.Context(), pdf); err == nil {
			page, err = s.readFile(r, m, signMax)
		}
	}
	var out []byte
	if err == nil {
		if stamped, ok := s.signStamp(r, d, src); ok {
			src = stamped
		}
		out, err = docgen.JoinPDF(r.Context(), src, page)
	}
	if err != nil {
		return "", err
	}
	file, err := s.signKeep(r, d.AuthorID, out, strings.TrimSuffix(name, path.Ext(name))+" — "+s.tr(r, "sign.signed_suffix")+".pdf")
	if err != nil {
		return "", err
	}
	return file, s.Sign.SetResult(r.Context(), d.ID, file, sign.Hash(out))
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
	if slices.ContainsFunc(d.Parties, func(p sign.Party) bool { return p.QES != nil }) {
		sh.Lines = append(sh.Lines, s.tr(r, "sign.sheet.about_qes"))
	}
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
			switch p.CodeVia {
			case "sms":
				how = s.tr(r, "sign.how.sms", p.Phone)
			case "chat":
				how = s.tr(r, "sign.how.chat")
			}
		}
		if p.QES != nil {
			how = s.signQESLine(r, p.QES)
		}
		who := p.Name
		for _, c := range []string{p.Email, p.Phone} {
			if c != "" {
				who += "\n" + c
			}
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
	link := s.signCheckURL(d)
	sh.Check = []string{s.tr(r, "sign.sheet.check", sign.ShowCode(d.VerifyCode)), s.tr(r, "sign.sheet.check_at", link)}
	if png, err := payqr.PNG(link); err == nil {
		sh.CheckPic = &docgen.Image{Data: png, Width: docgen.Cm(2.5)}
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
	if err == nil && !s.signEdits(r.Context(), sess, d) {
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
	if err == nil && (!s.signEdits(r.Context(), sess, d) || cur == nil || cur.Employee() || d.Status != "signing") {
		err = sign.ErrTurn
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	to := cmp.Or(cur.Email, cur.Phone)
	if cur.Chat != "" {
		to = s.tr(r, "sign.chat.to")
	}
	msg := s.tr(r, "sign.resent", to)
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
	var fields []sign.Block
	if turn {
		fields = s.signMyFields(r, d, p)
	}
	s.render(w, r, "signpub", page{Title: d.Title, Data: map[string]any{"Doc": d, "Party": p, "Token": p.Token, "Fields": fields,
		"Turn": turn, "PDF": s.Office != nil, "Email": maskEmail(p.Email), "Phone": maskPhone(p.Phone),
		"SMS": p.Phone != "" && s.signSMSReady(r.Context()), "Chat": p.Chat != "" && s.OpenLines != nil,
		"Msg": r.URL.Query().Get("msg"), "Code": r.URL.Query().Get("code") == "1"}})
}

// signCodeVia is how the client's code goes: as asked for when the client has the way (mail, sms, chat), else in the
// chat the link went to, by e-mail, by SMS.
func signCodeVia(p sign.Party, asked string) string {
	has := map[string]bool{"mail": p.Email != "", "sms": p.Phone != "", "chat": p.Chat != ""}
	if has[asked] {
		return asked
	}
	for _, via := range []string{"chat", "mail", "sms"} {
		if has[via] {
			return via
		}
	}
	return "mail"
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

// maskPhone hides the most of the number: +7 *** ***-45-67.
func maskPhone(p string) string {
	if len(p) < 8 {
		return p
	}
	return p[:max(1, len(p)-10)] + " *** ***-" + p[len(p)-4:len(p)-2] + "-" + p[len(p)-2:]
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
	via := signCodeVia(*p, r.FormValue("via"))
	if err == nil {
		err = s.Sign.SetCodeVia(r.Context(), p.ID, code, via)
	}
	sent := func() bool {
		switch via {
		case "sms":
			return s.signSMS(r.Context(), d, p.Phone, s.tr(r, "sign.sms.code", code, int(sign.CodeTTL.Minutes())))
		case "chat":
			return s.signChat(r.Context(), d, p.Chat, s.tr(r, "sign.chat.code", code, int(sign.CodeTTL.Minutes())))
		}
		return s.signMail(r, d, p.Email, s.tr(r, "sign.mail.code_subject", code), s.tr(r, "sign.mail.code_body", code, d.Title, int(sign.CodeTTL.Minutes())))
	}
	to := maskEmail(p.Email)
	switch via {
	case "sms":
		to = maskPhone(p.Phone)
	case "chat":
		to = s.tr(r, "sign.chat.to")
	}
	msg, more := "", "&code=1"
	switch {
	case err != nil:
		msg, more = s.signErr(r, err), ""
	case !sent():
		msg, more = s.tr(r, "sign.err.code_mail"), ""
		if err := s.Sign.ClearCode(r.Context(), p.ID); err != nil {
			s.Log.Warn("sign code", "err", err)
		}
		if err := s.Sign.Log(r.Context(), d.ID, p.ID, "", sign.EvCode, false, clientIP(r), via); err != nil {
			s.Log.Warn("sign log", "doc", d.ID, "err", err)
		}
	default:
		msg = s.tr(r, "sign.code_sent", to)
		s.Log.Info("sign code", "doc", d.ID, "party", p.ID, "via", via)
		if err := s.Sign.Log(r.Context(), d.ID, p.ID, "", sign.EvCode, true, clientIP(r), via); err != nil {
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
	if !refuse && !p.Qualified() {
		sig, err2 = sign.Signature(r.FormValue("signature"))
	}
	if !refuse && err2 == nil && r.FormValue("agree") == "1" {
		err2 = s.signFields(r, d, p, signFieldsForm(r))
	}
	if !refuse && err2 == nil && r.FormValue("agree") == "1" && p.Qualified() {
		err2 = s.signQES(r, d, p, []byte(r.FormValue("cms")))
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
