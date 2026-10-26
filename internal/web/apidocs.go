package web

import (
	"cmp"
	"errors"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/docgen"
	"go24/internal/i18n"
	"go24/internal/sign"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Генератор документов» and «Подпись» over REST, as documentgenerator.* and sign.* ----------
//
// The templates are the CRM's (their changes — administrators'); a document belongs to its card and is seen by those
// who see the card, made and changed by those who may change it, as on the card's page. A card is named by its kind
// as in the paths of the pages (deals, leads, contacts, companies).

func (s *Server) apiDocsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/document-templates", s.api("documents:read", s.apiDocTemplates))
	mux.HandleFunc("POST /api/v1/document-templates", s.api("documents:write", s.apiDocTemplateAdd))
	mux.HandleFunc("GET /api/v1/document-templates/{id}", s.api("documents:read", s.apiDocTemplate))
	mux.HandleFunc("PATCH /api/v1/document-templates/{id}", s.api("documents:write", s.apiDocTemplateChange))
	mux.HandleFunc("PUT /api/v1/document-templates/{id}/file", s.api("documents:write", s.apiDocTemplateFile))
	mux.HandleFunc("DELETE /api/v1/document-templates/{id}", s.api("documents:write", s.apiDocTemplateDelete))
	mux.HandleFunc("GET /api/v1/documents", s.api("documents:read", s.apiDocList))
	mux.HandleFunc("POST /api/v1/documents", s.api("documents:write", s.apiDocMake))
	mux.HandleFunc("GET /api/v1/documents/{id}", s.api("documents:read", s.apiDocGet))
	mux.HandleFunc("PATCH /api/v1/documents/{id}", s.api("documents:write", s.apiDocRemake))
	mux.HandleFunc("DELETE /api/v1/documents/{id}", s.api("documents:write", s.apiDocDelete))
	mux.HandleFunc("GET /api/v1/documents/{id}/file", s.api("documents:read", s.apiDocFile))
	mux.HandleFunc("POST /api/v1/documents/{id}/public", s.api("documents:write", s.apiDocPublic))
	mux.HandleFunc("GET /api/v1/sign", s.api("documents:read", s.apiSignList))
	mux.HandleFunc("POST /api/v1/sign", s.api("documents:write", s.apiSignCreate))
	mux.HandleFunc("GET /api/v1/sign/{id}", s.api("documents:read", s.apiSignGet))
	mux.HandleFunc("GET /api/v1/sign/{id}/file/{which}", s.api("documents:read", s.apiSignFile))
	mux.HandleFunc("POST /api/v1/sign/{id}/sign", s.api("documents:write", s.apiSignSign))
	mux.HandleFunc("POST /api/v1/sign/{id}/cancel", s.api("documents:write", s.apiSignCancel))
	mux.HandleFunc("POST /api/v1/sign/{id}/resend", s.api("documents:write", s.apiSignResend))
}

// apiSess is the key's owner as a session of the pages: an administrator, in admin mode with X-Go24-Admin.
func (s *Server) apiSess(r *http.Request, p apikeys.Principal) auth.Session {
	admin := s.apiAdmin(r.Context(), p.UserID)
	return auth.Session{UserID: p.UserID, Admin: admin, AdminMode: admin && tasks.IsAdmin(r.Context())}
}

type apiDocTemplate struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Entity    string    `json:"entity"` // the kind of cards: deals, leads, contacts, companies
	FileName  string    `json:"file_name"`
	Numerator string    `json:"numerator"`
	CreatedAt time.Time `json:"created_at"`
	Fields    []string  `json:"fields,omitempty"` // its placeholders (a single template)
	Access    []apiUser `json:"access,omitempty"` // «Кому доступен»: people and departments («dept:ID»); none — all
}

func apiDocTemplateOf(t crm.DocTemplate) apiDocTemplate {
	return apiDocTemplate{ID: t.ID, Name: t.Name, Entity: crmKind(t.Entity), FileName: t.FileName, Numerator: t.Numerator,
		CreatedAt: t.CreatedAt}
}

// apiDocTemplates are the templates the key's owner may make documents by (?entity=deals — of a kind of cards);
// an administrator's — all.
func (s *Server) apiDocTemplates(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sess, kind := s.apiSess(r, p), r.URL.Query().Get("entity")
	if kind != "" && crmEntities[kind] == "" {
		return i18n.E("err.doc_template")
	}
	var list []crm.DocTemplate
	var err error
	if sess.Admin {
		list, err = s.CRM.DocTemplates(r.Context(), crmEntities[kind])
	} else {
		for k, e := range crmEntities {
			if kind != "" && k != kind {
				continue
			}
			some, err := s.CRM.DocTemplatesFor(r.Context(), e, p.UserID)
			if err != nil {
				return err
			}
			list = append(list, some...)
		}
		slices.SortFunc(list, func(a, b crm.DocTemplate) int {
			return cmp.Or(cmp.Compare(a.Entity, b.Entity), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
		})
	}
	if err != nil {
		return err
	}
	out := make([]apiDocTemplate, 0, len(list))
	for _, t := range list {
		out = append(out, apiDocTemplateOf(t))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDocTemplate is a template with its placeholders and «Кому доступен».
func (s *Server) apiDocTemplate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sess := s.apiSess(r, p)
	id := r.PathValue("id")
	if err := s.docTemplateAllowed(r, sess, id); err != nil {
		return err
	}
	t, err := s.CRM.DocTemplate(r.Context(), id)
	if err != nil {
		return err
	}
	_, tpl, err := s.docTemplateFile(r, crmKind(t.Entity), id)
	if err != nil {
		return err
	}
	out := apiDocTemplateOf(t)
	if out.Fields, err = docgen.Names(tpl); err != nil {
		return i18n.E("err.doc_not_docx")
	}
	refs, err := s.CRM.DocTemplateAccess(r.Context())
	if err != nil {
		return err
	}
	for _, ref := range refs[t.ID] {
		out.Access = append(out.Access, apiUser{ref.ID, ref.Name})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDocTemplateAdd adds a template (administrators): multipart with the DOCX «file», «name» and «entity», or a
// ready-made one — «builtin»: invoice, contract, act (of deals, in the caller's language).
func (s *Server) apiDocTemplateAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sess := s.apiSess(r, p)
	if !sess.Admin {
		return tasks.ErrForbidden
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateSize+1<<16)
	if err := r.ParseMultipartForm(maxTemplateSize); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return i18n.E("err.file_required")
	}
	var id string
	var err error
	if kind := r.FormValue("builtin"); kind != "" {
		id, err = s.addBuiltin(r, sess, kind)
	} else {
		f, hdr, ferr := r.FormFile("file")
		if ferr != nil {
			return i18n.E("err.file_required")
		}
		defer f.Close()
		data, rerr := io.ReadAll(io.LimitReader(f, maxTemplateSize))
		if rerr != nil {
			return rerr
		}
		id, err = s.addTemplate(r.Context(), p.UserID, r.FormValue("name"), crmEntities[r.FormValue("entity")], hdr.Filename, data)
	}
	if err != nil {
		return err
	}
	t, err := s.CRM.DocTemplate(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, apiDocTemplateOf(t))
	return nil
}

// apiDocTemplateChange changes the template's numerator and «Кому доступен» (administrators):
// {"numerator": "{NUMBER}/{YEAR}", "access": ["user id", "dept:ID"]} — what is given; access [] — everybody.
func (s *Server) apiDocTemplateChange(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiSess(r, p).Admin {
		return tasks.ErrForbidden
	}
	var in struct {
		Numerator *string   `json:"numerator"`
		Access    *[]string `json:"access"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	t, err := s.CRM.DocTemplate(r.Context(), id)
	if err != nil {
		return err
	}
	if in.Numerator != nil {
		if err := s.CRM.SetDocNumerator(r.Context(), id, *in.Numerator); err != nil {
			return err
		}
	}
	if in.Access != nil {
		if err := s.CRM.SetDocTemplateAccess(r.Context(), id, *in.Access); err != nil {
			return err
		}
	}
	if t, err = s.CRM.DocTemplate(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiDocTemplateOf(t))
	return nil
}

// apiDocTemplateFile puts another DOCX in place of the template's (administrators): the body is the file.
func (s *Server) apiDocTemplateFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiSess(r, p).Admin {
		return tasks.ErrForbidden
	}
	id := r.PathValue("id")
	t, err := s.CRM.DocTemplate(r.Context(), id)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxTemplateSize+1))
	if err != nil || len(data) > maxTemplateSize {
		return i18n.E("err.file_required")
	}
	if _, err := docgen.Fill(data, nil); err != nil {
		return i18n.E("err.doc_not_docx")
	}
	fileID, err := s.storeTemplateFile(r.Context(), p.UserID, t.FileName, data)
	if err == nil {
		err = s.CRM.SetDocTemplateFile(r.Context(), id, fileID)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiDocTemplateOf(t))
	return nil
}

func (s *Server) apiDocTemplateDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiSess(r, p).Admin {
		return tasks.ErrForbidden
	}
	if _, err := s.CRM.DocTemplate(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	if err := s.CRM.DeleteDocTemplate(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiDocument struct {
	ID         string            `json:"id"`
	Entity     string            `json:"entity"`
	EntityID   string            `json:"entity_id"`
	TemplateID string            `json:"template_id,omitempty"`
	Template   string            `json:"template"`
	Number     string            `json:"number"`
	FileName   string            `json:"file_name"`
	Size       int64             `json:"size"`
	Values     map[string]string `json:"values,omitempty"` // the fields it was made with (a single document)
	Author     apiUser           `json:"author"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	PublicURL  string            `json:"public_url,omitempty"`
	Sign       *apiSignBrief     `json:"sign,omitempty"` // the last sending for signing
	URL        string            `json:"url"`            // the DOCX: GET with the same key; ?format=pdf — in PDF
}

type apiSignBrief struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (s *Server) apiDocumentOf(d crm.Document) apiDocument {
	out := apiDocument{ID: d.ID, Entity: crmKind(d.Entity), EntityID: d.EntityID, TemplateID: d.TemplateID,
		Template: d.TemplateName, Number: d.Number, FileName: d.FileName, Size: d.Size, Author: apiUser{d.AuthorID, d.Author},
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, URL: "/api/v1/documents/" + d.ID + "/file"}
	if d.PublicToken != "" {
		out.PublicURL = s.BaseURL + "/d/" + d.PublicToken
	}
	return out
}

// apiDocList are the documents of a card: ?entity=deals&entity_id=ID.
func (s *Server) apiDocList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	kind, id := q.Get("entity"), q.Get("entity_id")
	if err := s.crmCheck(r, s.apiSess(r, p), kind, id, "read"); err != nil {
		return err
	}
	list, err := s.CRM.Documents(r.Context(), crmEntities[kind], id)
	if err != nil {
		return err
	}
	signs, err := s.Sign.OfCard(r.Context(), crmEntities[kind], id)
	if err != nil {
		return err
	}
	out := make([]apiDocument, 0, len(list))
	for _, d := range list {
		a := s.apiDocumentOf(d)
		if sd, ok := signs[d.ID]; ok {
			a.Sign = &apiSignBrief{sd.ID, sd.Status}
		}
		out = append(out, a)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDocMake makes a document of the card by a template: {"entity": "deals", "entity_id": "…", "template": "…",
// "values": {"Field": "value"}} — the values go over those of the card.
func (s *Server) apiDocMake(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Entity   string            `json:"entity"`
		EntityID string            `json:"entity_id"`
		Template string            `json:"template"`
		Values   map[string]string `json:"values"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sess := s.apiSess(r, p)
	if err := s.crmCheck(r, sess, in.Entity, in.EntityID, "read"); err != nil {
		return err
	}
	doc, err := s.makeDocument(r, sess, in.Entity, in.EntityID, in.Template, in.Values)
	if err != nil {
		return err
	}
	if doc, err = s.CRM.DocumentByID(r.Context(), doc.ID); err != nil {
		return err
	}
	out := s.apiDocumentOf(doc)
	out.Values = doc.Values
	writeJSON(w, http.StatusCreated, out)
	return nil
}

// apiDoc is the document of the path for those who may do the action with its card (read, write).
func (s *Server) apiDoc(r *http.Request, p apikeys.Principal, action string) (crm.Document, error) {
	d, err := s.CRM.DocumentByID(r.Context(), r.PathValue("id"))
	if err == nil {
		err = s.crmCheck(r, s.apiSess(r, p), crmKind(d.Entity), d.EntityID, action)
	}
	return d, err
}

func (s *Server) apiDocGet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiDoc(r, p, "read")
	if err != nil {
		return err
	}
	out := s.apiDocumentOf(d)
	out.Values = d.Values
	if signs, err := s.Sign.OfCard(r.Context(), d.Entity, d.EntityID); err == nil {
		if sd, ok := signs[d.ID]; ok {
			out.Sign = &apiSignBrief{sd.ID, sd.Status}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiDocRemake is «Изменить значения»: the document made again, {"values": {"Field": "value"}}; the number stays.
func (s *Server) apiDocRemake(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Values map[string]string `json:"values"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, err := s.apiDoc(r, p, "write")
	if err == nil {
		err = s.remakeDocument(r, s.apiSess(r, p), d, in.Values)
	}
	if err == nil {
		d, err = s.CRM.DocumentByID(r.Context(), d.ID)
	}
	if err != nil {
		return err
	}
	out := s.apiDocumentOf(d)
	out.Values = d.Values
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiDocDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiDoc(r, p, "write")
	if err == nil {
		err = s.CRM.DeleteDocument(r.Context(), d.ID)
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiDocFile gives the document: the DOCX, or ?format=pdf — in PDF (made by the document server).
func (s *Server) apiDocFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, err := s.apiDoc(r, p, "read")
	if err != nil {
		return err
	}
	id := d.FileID
	if r.URL.Query().Get("format") == "pdf" {
		if id, err = s.documentPDF(r, d); err != nil {
			return err
		}
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		return err
	}
	s.sendFile(w, r, m, "attachment")
	return nil
}

// apiDocPublic switches the public link on or off: {"on": true}; the document with its link.
func (s *Server) apiDocPublic(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, err := s.apiDoc(r, p, "write")
	if err == nil {
		_, err = s.CRM.SetDocumentPublic(r.Context(), d.ID, in.On)
	}
	if err == nil {
		d, err = s.CRM.DocumentByID(r.Context(), d.ID)
	}
	if err != nil {
		return err
	}
	s.Log.Info("crm document link", "user_id", p.UserID, "doc", d.ID, "on", in.On, "api", true)
	writeJSON(w, http.StatusOK, s.apiDocumentOf(d))
	return nil
}

// ---------- «Подпись» ----------

type apiSignDoc struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Status     string            `json:"status"` // signing | done | refused | cancelled
	Entity     string            `json:"entity,omitempty"`
	EntityID   string            `json:"entity_id,omitempty"`
	DocumentID string            `json:"document_id,omitempty"` // the card's document sent
	Hash       string            `json:"sha256"`
	Author     apiUser           `json:"author"`
	CreatedAt  time.Time         `json:"created_at"`
	DoneAt     *time.Time        `json:"done_at,omitempty"`
	Parties    []apiSignParty    `json:"parties"`
	Files      map[string]string `json:"files"`             // docx, pdf, result (when signed): GET with the same key
	History    []apiSignEvent    `json:"history,omitempty"` // GET /api/v1/sign/{id} only
}

// apiSignEvent is a line of the document's history: created, sent, reminded, viewed, code, signed, refused,
// cancelled, done.
type apiSignEvent struct {
	Kind   string    `json:"kind"`
	At     time.Time `json:"at"`
	Party  string    `json:"party,omitempty"` // the signer's name
	Actor  *apiUser  `json:"actor,omitempty"` // the employee who did it
	Sent   bool      `json:"sent"`            // false — the letter was not sent
	IP     string    `json:"ip,omitempty"`    // to the sender
	Reason string    `json:"reason,omitempty"`
}

type apiSignParty struct {
	UserID   string     `json:"user_id,omitempty"` // an employee; the client has none
	Name     string     `json:"name"`
	Email    string     `json:"email,omitempty"`
	Status   string     `json:"status"` // wait | ready | signed | refused
	SignedAt *time.Time `json:"signed_at,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Link     string     `json:"link,omitempty"` // the client's personal link (to the sender)
}

func (s *Server) apiSignOf(d sign.Doc, viewer string) apiSignDoc {
	out := apiSignDoc{ID: d.ID, Title: d.Title, Status: d.Status, Entity: crmKind(d.Entity), EntityID: d.EntityID,
		DocumentID: d.CRMDocID, Hash: d.Hash, Author: apiUser{d.AuthorID, d.Author}, CreatedAt: d.CreatedAt, DoneAt: d.DoneAt,
		Parties: []apiSignParty{}, Files: map[string]string{}}
	for _, which := range []string{"docx", "pdf"} {
		out.Files[which] = "/api/v1/sign/" + d.ID + "/file/" + which
	}
	if d.ResultFile != "" {
		out.Files["result"] = "/api/v1/sign/" + d.ID + "/file/result"
	}
	for _, p := range d.Parties {
		a := apiSignParty{UserID: p.UserID, Name: p.Name, Email: p.Email, Status: p.Status, SignedAt: p.SignedAt, Reason: p.Reason}
		if !p.Employee() && viewer == d.AuthorID && d.Status == "signing" {
			a.Link = s.BaseURL + "/sg/" + p.Token
		}
		out.Parties = append(out.Parties, a)
	}
	return out
}

// apiSignList are the documents of «Подпись» the key's owner sent or signs: ?filter=me (waiting for my signature),
// signing, done, closed (refused or cancelled).
func (s *Server) apiSignList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	f := r.URL.Query().Get("filter")
	if !slices.Contains(sign.Filters, f) {
		f = ""
	}
	docs, err := s.Sign.List(r.Context(), p.UserID, f, s.apiSess(r, p).AdminActive())
	if err != nil {
		return err
	}
	out := make([]apiSignDoc, 0, len(docs))
	for _, d := range docs {
		out = append(out, s.apiSignOf(d, p.UserID))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiSignDocOf is the sending of the path for those who see it.
func (s *Server) apiSignDocOf(r *http.Request, p apikeys.Principal) (sign.Doc, auth.Session, error) {
	sess := s.apiSess(r, p)
	d, err := s.signOf(r, sess)
	return d, sess, err
}

func (s *Server) apiSignGet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, _, err := s.apiSignDocOf(r, p)
	if err != nil {
		return err
	}
	events, err := s.Sign.Events(r.Context(), d.ID)
	if err != nil {
		return err
	}
	out := s.apiSignOf(d, p.UserID)
	for _, e := range events {
		a := apiSignEvent{Kind: e.Kind, At: e.At, Party: e.Party, Sent: e.OK, Reason: e.Note}
		if e.ActorID != "" {
			a.Actor = &apiUser{e.ActorID, e.Actor}
		}
		if p.UserID == d.AuthorID {
			a.IP = e.IP
		}
		out.History = append(out.History, a)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiSignCreate sends a document for signing: the card's document — JSON {"document": "…", "title": "…",
// "signer": "user id" or "signers": ["user id", …], "client_name": "…", "client_email": "…" or "clients": [{"name",
// "email"}, …], "remind_days": 1}, or a Word file — multipart with «file» and the same fields (signer, client_name and
// client_email repeated). The employees sign first in turn, then the clients get the link by e-mail in turn; the one
// whose turn it is is reminded every remind_days days (0, 1, 2, 3, 7; 0 — never; by default every day).
func (s *Server) apiSignCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Document    string                         `json:"document"`
		Title       string                         `json:"title"`
		Signer      string                         `json:"signer"`
		ClientName  string                         `json:"client_name"`
		ClientEmail string                         `json:"client_email"`
		Signers     []string                       `json:"signers"`
		Clients     []struct{ Name, Email string } `json:"clients"`
		RemindDays  *int                           `json:"remind_days"`
	}
	sess := s.apiSess(r, p)
	var data []byte
	var name string
	var clients []sign.Party
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, signMax+1<<16)
		f, h, err := r.FormFile("file")
		if err == nil {
			defer f.Close()
			data, err = io.ReadAll(io.LimitReader(f, signMax+1))
		}
		if err != nil || len(data) > signMax {
			return i18n.E("sign.err.file")
		}
		name = h.Filename
		in.Title, in.Signers = r.FormValue("title"), r.Form["signer"]
		clients = signClients(r.Form["client_name"], r.Form["client_email"])
		if v := r.FormValue("remind_days"); v != "" {
			days := atoi(v)
			in.RemindDays = &days
		}
		if strings.TrimSpace(in.Title) == "" {
			in.Title = strings.TrimSuffix(name, path.Ext(name))
		}
	} else if err := readJSON(r, &in); err != nil {
		return err
	} else {
		in.Signers = append([]string{in.Signer}, in.Signers...)
		clients = signClients([]string{in.ClientName}, []string{in.ClientEmail})
		for _, c := range in.Clients {
			clients = append(clients, signClients([]string{c.Name}, []string{c.Email})...)
		}
	}
	d := sign.Doc{Title: in.Title, RemindDays: 1}
	if in.RemindDays != nil {
		d.RemindDays = *in.RemindDays
	}
	if data == nil {
		doc, err := s.CRM.DocumentByID(r.Context(), in.Document)
		if err == nil {
			doc, err = s.signSource(r, sess, crmKind(doc.Entity), doc.EntityID, doc.ID)
		}
		var m storages.FileMeta
		if err == nil {
			m, err = s.Storages.FileMeta(r.Context(), doc.FileID)
		}
		if err == nil {
			data, err = s.readFile(r, m, signMax)
		}
		if err != nil {
			return err
		}
		d.Entity, d.EntityID, d.CRMDocID, name = doc.Entity, doc.EntityID, doc.ID, m.Name
		d.Title = cmp.Or(strings.TrimSpace(d.Title), signDocTitle(doc))
	}
	id, err := s.signStart(r, sess, d, data, name, in.Signers, clients)
	if err != nil {
		return err
	}
	sd, err := s.Sign.Get(r.Context(), id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.apiSignOf(sd, p.UserID))
	return nil
}

// apiSignFile gives the document: docx (as sent), pdf (to read it) or result (signed, with the signing sheet).
func (s *Server) apiSignFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, _, err := s.apiSignDocOf(r, p)
	if err != nil {
		return err
	}
	s.signSend(w, r, d)
	return nil
}

// apiSignSign: the employee whose turn it is signs — {"agree": true} (has read the document) — or refuses:
// {"refuse": true, "reason": "…"}.
func (s *Server) apiSignSign(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Agree  bool   `json:"agree"`
		Refuse bool   `json:"refuse"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	d, _, err := s.apiSignDocOf(r, p)
	cur := d.Current()
	if err == nil && (cur == nil || cur.UserID != p.UserID) {
		err = sign.ErrTurn
	}
	if err == nil && !in.Refuse && !in.Agree {
		err = i18n.E("sign.err.agree")
	}
	if err == nil {
		_, err = s.signDo(r, d, cur, "", p.UserID, in.Refuse, in.Reason)
	}
	if err == nil {
		d, err = s.Sign.Get(r.Context(), d.ID)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiSignOf(d, p.UserID))
	return nil
}

// apiSignCancel is «Отозвать» (the sender; administrators in admin mode).
func (s *Server) apiSignCancel(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, sess, err := s.apiSignDocOf(r, p)
	if err == nil && d.AuthorID != p.UserID && !sess.AdminActive() {
		err = sign.ErrNotFound
	}
	if err == nil {
		err = s.Sign.Cancel(r.Context(), d.ID, p.UserID)
	}
	if err == nil {
		d, err = s.Sign.Get(r.Context(), d.ID)
	}
	if err != nil {
		return err
	}
	s.Log.Info("sign cancel", "user_id", p.UserID, "doc", d.ID, "api", true)
	writeJSON(w, http.StatusOK, s.apiSignOf(d, p.UserID))
	return nil
}

// apiSignResend sends the client the letter with the link again (the sender).
func (s *Server) apiSignResend(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, _, err := s.apiSignDocOf(r, p)
	cur := d.Current()
	if err == nil && (d.AuthorID != p.UserID || cur == nil || cur.Employee() || d.Status != "signing") {
		err = sign.ErrTurn
	}
	if err != nil {
		return err
	}
	if !s.signTurn(r, d, cur, p.UserID) {
		return i18n.E("sign.not_mailed")
	}
	writeJSON(w, http.StatusOK, s.apiSignOf(d, p.UserID))
	return nil
}
