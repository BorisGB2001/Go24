package web

import (
	"bytes"
	"cmp"
	"crypto/rand"
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
	s.render(w, r, "sign", page{TitleKey: "sign.title", Nav: "sign", Data: map[string]any{"Docs": docs, "Filter": f,
		"Filters": sign.Filters, "Waiting": waiting, "Me": sess.UserID}})
}

// signNew is the form to send a document for signing: a document of a CRM card (?kind=&id=&doc=) or a file to upload.
func (s *Server) signNew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	data := map[string]any{"Me": sess.UserID}
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
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.fail(w, "sign", err)
		return
	}
	data["Users"] = users
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
	if crmEntities[kind] == "" {
		return crm.Document{}, crm.ErrNotFound
	}
	if err := s.crmCheck(r, sess, kind, id, "write"); err != nil {
		return crm.Document{}, err
	}
	return s.CRM.Document(r.Context(), crmEntities[kind], id, docID)
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
	d := sign.Doc{Title: r.FormValue("title"), AuthorID: sess.UserID}
	var data []byte
	var name string
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
		d.Entity, d.EntityID, d.CRMDocID, name = crmEntities[kind], id, doc.ID, m.Name
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
	id, err := s.signStart(r, sess, d, data, name, r.FormValue("signer"), r.FormValue("client_name"), r.FormValue("client_email"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/sign/"+id+"?msg="+url.QueryEscape(s.tr(r, "sign.sent")), http.StatusSeeOther)
}

// signStart sends the Word document (data; name — its file's) for signing by the employee, then by the client when
// given; the pages and the REST API send by it.
func (s *Server) signStart(r *http.Request, sess auth.Session, d sign.Doc, data []byte, name, signerID, clientName, clientEmail string) (string, error) {
	if _, err := docgen.Names(data); err != nil || !strings.EqualFold(path.Ext(name), ".docx") {
		return "", i18n.E("sign.err.file")
	}
	var signer tasks.User
	err := s.DB.QueryRow(r.Context(), `SELECT id::text, name, email, guest FROM users WHERE id::text = $1 AND active`,
		signerID).Scan(&signer.ID, &signer.Name, &signer.Email, &signer.Guest)
	if err != nil || signer.Guest {
		return "", sign.ErrParties
	}
	parties := []sign.Party{{UserID: signer.ID, Name: signer.Name, Email: signer.Email}}
	client := strings.TrimSpace(clientEmail)
	if client != "" || strings.TrimSpace(clientName) != "" {
		parties = append(parties, sign.Party{Name: clientName, Email: client})
		if s.Mail != nil { // the client gets the link and the code from the sender's mailbox
			if boxes, err := s.Mail.Mailboxes(r.Context(), sess.UserID); err != nil || len(boxes) == 0 {
				return "", i18n.E("sign.err.no_mailbox")
			}
		}
	}
	d.AuthorID, d.Hash = sess.UserID, sign.Hash(data)
	name = strings.NewReplacer("/", "-", "\\", "-", "\"", "").Replace(name)
	key := path.Join("go24", "sign", randomHex(16), name)
	err = s.Storages.Put(r.Context(), sess.UserID, false, "", key, bytes.NewReader(data), int64(len(data)), docxType)
	if err == nil {
		d.FileID, err = s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: docxType, Size: int64(len(data)), UploadedBy: sess.UserID})
	}
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

// signTurn tells the signer it is their turn: the employee by a notification (not when they act themselves), the
// client by a letter with the link. false — the letter was not sent.
func (s *Server) signTurn(r *http.Request, d sign.Doc, p *sign.Party, actor string) bool {
	switch {
	case p == nil:
		return true
	case p.Employee():
		if p.UserID != actor {
			if _, err := s.DB.Exec(r.Context(), `INSERT INTO notifications (user_id, actor_id, kind, body, url)
				VALUES ($1, $2, 'sign_request', $3, $4)`, p.UserID, d.AuthorID, d.Title, "/sign/"+d.ID); err != nil {
				s.Log.Warn("sign notify", "err", err)
			}
		}
		return true
	}
	return s.signMail(r, d, p.Email, s.tr(r, "sign.mail.subject", d.Title),
		s.tr(r, "sign.mail.body", p.Name, d.Author, d.Title, s.BaseURL+"/sg/"+p.Token))
}

// signMail sends a letter of «Подпись» from the sender's first mailbox in go24.
func (s *Server) signMail(r *http.Request, d sign.Doc, to, subject, body string, files ...mail.Attachment) bool {
	if s.Mail == nil {
		return false
	}
	boxes, err := s.Mail.Mailboxes(r.Context(), d.AuthorID)
	if err != nil || len(boxes) == 0 {
		return false
	}
	if err := s.Mail.Send(r.Context(), d.AuthorID, boxes[0].ID, to, subject, body, files...); err != nil {
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
	card := ""
	for k, e := range crmEntities {
		if e == d.Entity && d.EntityID != "" {
			card = "/crm/" + k + "/" + d.EntityID + "#documents"
		}
	}
	s.render(w, r, "signdoc", page{Title: d.Title, Nav: "sign", Data: map[string]any{"Doc": d, "Me": sess.UserID,
		"Mine": cur != nil && d.Status == "signing" && cur.UserID == sess.UserID, "Author": d.AuthorID == sess.UserID,
		"PDF": s.Office != nil, "Link": s.BaseURL + "/sg/", "Card": card}})
}

// signOf is the document of the path for those who see it (the sender, the signers; administrators).
func (s *Server) signOf(r *http.Request, sess auth.Session) (sign.Doc, error) {
	d, err := s.Sign.Get(r.Context(), r.PathValue("id"))
	if err == nil && !d.Sees(sess.UserID) && !sess.AdminActive() {
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
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	msg := s.signAct(r, d, cur, "", sess.UserID, r.FormValue("refuse") == "1", r.FormValue("reason"))
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// signAct signs (or refuses) for the signer, then the next one's turn comes or the document is finished; the message
// to show (an error too).
func (s *Server) signAct(r *http.Request, d sign.Doc, p *sign.Party, code, actor string, refuse bool, reason string) string {
	msg, err := s.signDo(r, d, p, code, actor, refuse, reason)
	if err != nil {
		return s.signErr(r, err)
	}
	return msg
}

// signDo is signAct giving the error apart (the REST API).
func (s *Server) signDo(r *http.Request, d sign.Doc, p *sign.Party, code, actor string, refuse bool, reason string) (string, error) {
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
		return s.tr(r, "sign.refused_msg"), nil
	}
	next, done, err := s.Sign.Sign(r.Context(), d.ID, p.ID, code, ip, ua)
	if err != nil {
		return "", err
	}
	s.Log.Info("sign", "doc", d.ID, "party", p.ID, "done", done)
	if done {
		s.signFinish(r, d.ID)
		return s.tr(r, "sign.done_msg"), nil
	}
	if !s.signTurn(r, d, next, actor) {
		return s.tr(r, "sign.signed_msg") + " " + s.tr(r, "sign.not_mailed"), nil
	}
	return s.tr(r, "sign.signed_msg"), nil
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
	for _, p := range d.Parties {
		side, how := s.tr(r, "sign.side.company"), s.tr(r, "sign.how.employee")
		if !p.Employee() {
			side, how = s.tr(r, "sign.side.client"), s.tr(r, "sign.how.code", p.Email)
		}
		who := p.Name
		if p.Email != "" {
			who += "\n" + p.Email
		}
		sh.Rows = append(sh.Rows, []string{fmt.Sprint(p.Ord), who, side, how, when(p.SignedAt), p.IP})
	}
	return sh
}

// signCancel: the sender recalls the document while it is being signed.
func (s *Server) signCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/" + r.PathValue("id")
	d, err := s.signOf(r, sess)
	if err == nil && d.AuthorID != sess.UserID && !sess.AdminActive() {
		err = sign.ErrNotFound
	}
	if err == nil {
		err = s.Sign.Cancel(r.Context(), d.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("sign cancel", "user_id", sess.UserID, "doc", d.ID)
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
	s.render(w, r, "signpub", page{Title: d.Title, Data: map[string]any{"Doc": d, "Party": p, "Token": p.Token,
		"Turn": d.Status == "signing" && p.Status == "ready", "PDF": s.Office != nil, "Email": maskEmail(p.Email),
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
	default:
		msg = s.tr(r, "sign.code_sent", maskEmail(p.Email))
		s.Log.Info("sign code", "doc", d.ID, "party", p.ID)
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
	if r.FormValue("refuse") != "1" && r.FormValue("agree") != "1" {
		msg, more = s.tr(r, "sign.err.agree"), "&code=1"
	} else if msg = s.signAct(r, d, p, r.FormValue("code"), "", r.FormValue("refuse") == "1", r.FormValue("reason")); r.FormValue("refuse") != "1" {
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
