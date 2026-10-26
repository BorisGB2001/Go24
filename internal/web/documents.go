package web

import (
	"bytes"
	"cmp"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/docgen"
	"go24/internal/i18n"
	"go24/internal/inwords"
	"go24/internal/mail"
	"go24/internal/office"
	"go24/internal/payqr"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Генератор документов» of the CRM ----------

const maxTemplateSize = 20 << 20

func (s *Server) docTemplatesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.CRM.DocTemplates(r.Context(), "")
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	my, err := s.CRM.MyCompany(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	companies, err := s.CRM.Companies(r.Context(), sess.UserID, "")
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	images, err := s.CRM.DocImages(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	refs, err := s.CRM.DocTemplateAccess(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	access := map[string][]tasks.User{} // the chips of the pickers
	for tpl, list := range refs {
		for _, ref := range list {
			access[tpl] = append(access[tpl], tasks.User{ID: ref.ID, Name: ref.Name})
		}
	}
	s.render(w, r, "documents", page{TitleKey: "docs.templates", Nav: "crm_deals", Data: map[string]any{"Templates": list,
		"Fields": docFields, "PartyFields": partyFields, "ProductFields": productFields, "MyCompany": my, "Companies": companies, "Office": s.Office != nil,
		"Access": access, "Builtins": docBuiltins, "Images": images, "ImageKinds": crm.DocImageKinds}})
}

// docMyCompany chooses «Моя компания» of the templates (administrators).
func (s *Server) docMyCompany(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.CRM.SetMyCompany(r.Context(), sess.UserID, r.FormValue("company")); err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	http.Redirect(w, r, "/crm/documents?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// docImageMax is the biggest picture of «Моя компания».
const docImageMax = 2 << 20

// docImageWidth is how wide each picture of «Моя компания» is in a document, cm: the stamp, a signature.
func docImageWidth(kind string) float64 {
	if kind == "Stamp" {
		return 4
	}
	return 3.5
}

// docImage uploads a picture of «Моя компания» — the stamp or a facsimile signature, PNG or JPEG — or removes it
// (remove=1); administrators.
func (s *Server) docImage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := "/crm/documents"
	r.Body = http.MaxBytesReader(w, r.Body, docImageMax+1<<16)
	kind := r.FormValue("kind")
	if !slices.Contains(crm.DocImageKinds, kind) {
		s.taskErr(w, r, crm.ErrNotFound, back)
		return
	}
	fileID := ""
	if r.FormValue("remove") != "1" {
		f, hdr, err := r.FormFile("file")
		var data []byte
		if err == nil {
			defer f.Close()
			data, err = io.ReadAll(io.LimitReader(f, docImageMax+1))
		}
		var ctype string
		if err == nil {
			_, ctype, err = image.DecodeConfig(bytes.NewReader(data))
		}
		if err != nil || len(data) > docImageMax || (ctype != "png" && ctype != "jpeg") {
			s.taskErr(w, r, i18n.E("docs.err.image"), back)
			return
		}
		name := kind + "." + ctype
		key := path.Join("go24", "doc-images", randomHex(16), name)
		if err = s.Storages.Put(r.Context(), sess.UserID, false, "", key, bytes.NewReader(data), int64(len(data)), "image/"+ctype); err == nil {
			fileID, err = s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/")),
				ContentType: "image/" + ctype, Size: int64(len(data)), UploadedBy: sess.UserID})
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
	}
	old, err := s.CRM.SetDocImage(r.Context(), sess.UserID, kind, fileID)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if old != "" {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, old)
	}
	s.Log.Info("doc image", "user_id", sess.UserID, "kind", kind, "removed", fileID == "")
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#my-company", http.StatusSeeOther)
}

// docImages are the pictures of «Моя компания» for the generator: every kind, those not uploaded empty (their
// placeholders leave nothing).
func (s *Server) docImages(r *http.Request) (map[string]docgen.Image, error) {
	files, err := s.CRM.DocImages(r.Context())
	if err != nil {
		return nil, err
	}
	out := map[string]docgen.Image{}
	for _, kind := range crm.DocImageKinds {
		img := docgen.Image{Width: docgen.Cm(docImageWidth(kind)), Float: true}
		if id := files[kind]; id != "" {
			m, err := s.Storages.FileMeta(r.Context(), id)
			if err == nil {
				img.Data, err = s.readFile(r, m, docImageMax)
			}
			if err != nil {
				s.Log.Warn("doc image", "kind", kind, "err", err)
			}
		}
		out["MyCompany."+kind] = img
	}
	return out, nil
}

// docPayQR is the QR code of the payment of the document ({PaymentQR}), as the invoices have it: to «Моя
// компания»'s account, the document's total in roubles, «Оплата по счёту № … от …»; none with no bank details.
func (s *Server) docPayQR(r *http.Request, v map[string]string) docgen.Image {
	img := docgen.Image{Width: docgen.Cm(3.5)}
	p := payqr.Payee{Name: cmp.Or(v["MyCompany.FullName"], v["MyCompany.Title"]), Account: v["MyCompany.Account"],
		Bank: v["MyCompany.Bank"], BIK: v["MyCompany.BIK"], CorrAccount: v["MyCompany.CorrAccount"], INN: v["MyCompany.INN"],
		KPP: v["MyCompany.KPP"]}
	if !p.Ok() {
		return img
	}
	var sum int64
	if c := v["Currency"]; c == "" || c == "RUB" {
		sum = payqr.Kopecks(v["Total"])
	}
	data, err := payqr.PNG(payqr.Text(p, sum, s.tr(r, "docs.qr_purpose", v["DocumentNumber"], v["DocumentDate"])))
	if err != nil {
		s.Log.Warn("doc payment qr", "err", err)
		return img
	}
	img.Data = data
	return img
}

// docImageField: the placeholder is a picture of «Моя компания» or the QR code of the payment.
func docImageField(n string) bool {
	if n == "PaymentQR" {
		return true
	}
	k, ok := strings.CutPrefix(n, "MyCompany.")
	return ok && slices.Contains(crm.DocImageKinds, k)
}

// docFields are the placeholders of each kind of card, as the settings page lists them.
var docFields = map[string][]string{
	"deal":     {"Title", "Amount", "Currency", "Stage", "Company", "Contact", "Responsible", "CloseDate", "Comment", "Date", "Number"},
	"lead":     {"Title", "Name", "CompanyTitle", "Phone", "Email", "Source", "Amount", "Currency", "Stage", "Responsible", "Comment", "Date"},
	"contact":  {"Name", "Position", "Phone", "Email", "Company", "Responsible", "Comment", "Date"},
	"company":  {"Title", "Phone", "Email", "Website", "Address", "Responsible", "Comment", "Date"},
	"employee": employeeFields,
	"invoice": {"Kind", "Number", "Title", "IssueDate", "DueDate", "Status", "Amount", "Currency", "Company", "Contact", "Responsible",
		"Comment", "Card", "Date"},
}

// storeTemplateFile keeps the DOCX of a template as a file of go24.
func (s *Server) storeTemplateFile(ctx context.Context, userID, name string, data []byte) (string, error) {
	if office.Ext(name) != "docx" {
		name += ".docx"
	}
	key := path.Join("go24", "doc-templates", randomHex(16), name)
	if err := s.Storages.Put(ctx, userID, false, "", key, bytes.NewReader(data), int64(len(data)), docxType); err != nil {
		return "", err
	}
	return s.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: name, ContentType: docxType, Size: int64(len(data)), UploadedBy: userID})
}

// docTemplateEdit opens a template in ONLYOFFICE (administrators): what is saved there becomes the template, as
// «Редактировать» of a template.
func (s *Server) docTemplateEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Office == nil {
		http.NotFound(w, r)
		return
	}
	t, err := s.CRM.DocTemplate(r.Context(), r.PathValue("id"))
	var m storages.FileMeta
	if err == nil {
		m, err = s.Storages.FileMeta(r.Context(), t.FileID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	raw, err := s.officeConfig(r, sess, m.Name, office.Key(t.FileID, m.Key), "/onlyoffice/tpl/"+t.ID+"/"+s.Office.Stamp("tpl", t.ID, m.Key),
		"/onlyoffice/tplsave/"+t.ID+"?"+url.Values{"u": {sess.UserID}, "s": {s.Office.Stamp("tpl", t.ID, sess.UserID)}}.Encode(),
		"/crm/documents", true)
	if err != nil {
		s.fail(w, "office config", err)
		return
	}
	s.render(w, r, "office", page{Title: t.Name, Nav: "crm_deals", Data: map[string]any{"Config": raw, "Name": t.Name,
		"Back": "/crm/documents", "Edit": true, "Download": "/files/" + t.FileID}})
}

// officeTemplateFile gives the document server a template it opens (the link is signed for this state of it).
func (s *Server) officeTemplateFile(w http.ResponseWriter, r *http.Request) {
	t, err := s.CRM.DocTemplate(r.Context(), r.PathValue("id"))
	var m storages.FileMeta
	if err == nil {
		m, err = s.Storages.FileMeta(r.Context(), t.FileID)
	}
	if err != nil || !s.Office.Stamped(r.PathValue("stamp"), "tpl", t.ID, m.Key) {
		http.NotFound(w, r)
		return
	}
	s.sendFile(w, r, m, "attachment")
}

// officeTemplateSave is what the document server says of a template being edited: once saved, the edited file
// becomes the template's (the documents made before keep theirs).
func (s *Server) officeTemplateSave(w http.ResponseWriter, r *http.Request) {
	id, q := r.PathValue("id"), r.URL.Query()
	userID := q.Get("u")
	if !s.Office.Stamped(q.Get("s"), "tpl", id, userID) {
		http.NotFound(w, r)
		return
	}
	var cb office.Callback
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cb)
	if err == nil {
		err = s.officeVerify(r, &cb)
	}
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": 1})
		return
	}
	if cb.Status == office.StatusSave || cb.Status == office.StatusForceSave {
		if err := s.saveTemplate(r.Context(), id, userID, cb.URL); err != nil {
			s.Log.Error("office template save", "id", id, "err", err)
			writeJSON(w, http.StatusOK, map[string]any{"error": 1})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"error": 0})
}

// saveTemplate takes the edited template from the document server.
func (s *Server) saveTemplate(ctx context.Context, id, userID, link string) error {
	t, err := s.CRM.DocTemplate(ctx, id)
	if err != nil {
		return err
	}
	data, err := s.officeDownload(ctx, link)
	if err != nil {
		return err
	}
	if _, err := docgen.Names(data); err != nil {
		return err
	}
	fileID, err := s.storeTemplateFile(ctx, userID, t.FileName, data)
	if err != nil {
		return err
	}
	return s.CRM.SetDocTemplateFile(ctx, id, fileID)
}

func (s *Server) docTemplateAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTemplateSize+1<<16)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.taskErr(w, r, i18n.E("err.file_required"), "/crm/documents")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTemplateSize))
	if err == nil {
		_, err = s.addTemplate(r.Context(), sess.UserID, r.FormValue("name"), r.FormValue("entity"), hdr.Filename, data)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	http.Redirect(w, r, "/crm/documents", http.StatusSeeOther)
}

// addTemplate keeps a Word document as a template of the cards of the entity; its id.
func (s *Server) addTemplate(ctx context.Context, userID, name, entity, filename string, data []byte) (string, error) {
	if _, err := docgen.Fill(data, nil); err != nil { // a Word document at all?
		return "", i18n.E("err.doc_not_docx")
	}
	if !crm.Entities[entity] && entity != crm.TemplateEmployee && entity != crm.TemplateInvoice {
		return "", i18n.E("err.doc_template")
	}
	fileID, err := s.storeTemplateFile(ctx, userID, path.Base(strings.ReplaceAll(filename, "\\", "/")), data)
	if err != nil {
		return "", err
	}
	id, err := s.CRM.NewDocTemplate(ctx, userID, name, entity, fileID)
	if err != nil {
		_, _ = s.DB.Exec(ctx, `DELETE FROM files WHERE id::text = $1`, fileID)
	}
	return id, err
}

// docBuiltinFS are the ready-made templates of a deal by language, made by tools/doctemplates.py: an invoice, a
// contract and an act with «Моя компания», the client and the products' table own templates.
//
//go:embed doctemplates
var docBuiltinFS embed.FS

// docBuiltins are their kinds, in the order offered.
var docBuiltins = []string{"invoice", "contract", "act"}

// docTemplateBuiltin adds a ready-made template in the viewer's language (administrators).
func (s *Server) docTemplateBuiltin(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	name := s.tr(r, "docs.builtin."+r.FormValue("kind"))
	if _, err := s.addBuiltin(r, sess, r.FormValue("kind")); err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	http.Redirect(w, r, "/crm/documents?msg="+url.QueryEscape(s.tr(r, "docs.builtin_added", name)), http.StatusSeeOther)
}

// addBuiltin adds the ready-made template of the kind in the viewer's language; its id.
func (s *Server) addBuiltin(r *http.Request, sess auth.Session, kind string) (string, error) {
	if !slices.Contains(docBuiltins, kind) {
		return "", i18n.E("err.doc_template")
	}
	data, err := docBuiltinFS.ReadFile("doctemplates/" + s.lang(r) + "/" + kind + ".docx")
	if err != nil {
		data, err = docBuiltinFS.ReadFile("doctemplates/en-US/" + kind + ".docx")
	}
	if err != nil {
		return "", err
	}
	name := s.tr(r, "docs.builtin."+kind)
	return s.addTemplate(r.Context(), sess.UserID, name, "deal", name, data)
}

func (s *Server) docTemplateDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.CRM.DeleteDocTemplate(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	http.Redirect(w, r, "/crm/documents", http.StatusSeeOther)
}

// docEntity is the entity of the documents of a kind of cards: the CRM cards and the quotes and invoices.
func docEntity(kind string) string {
	if kind == "invoices" {
		return crm.TemplateInvoice
	}
	return crmEntities[kind]
}

// docCheck: the user may do the action with the card of the documents (an invoice — by its deal or lead).
func (s *Server) docCheck(r *http.Request, sess auth.Session, kind, id, action string) error {
	if kind != "invoices" {
		return s.crmCheck(r, sess, kind, id, action)
	}
	v, err := s.CRM.Invoice(r.Context(), id)
	if err != nil {
		return err
	}
	return s.invoiceCheck(r, sess, v, action)
}

// docValues are the placeholders' values of a card.
func (s *Server) docValues(r *http.Request, kind, id string) (map[string]string, string, error) {
	stage := func(st crm.Stage) string {
		if st.Code != "" && st.Name == "" {
			return s.tr(r, "crm.stage."+st.Code)
		}
		return st.Name
	}
	v := map[string]string{"Date": time.Now().Format(s.tr(r, "format.date"))}
	switch docEntity(kind) {
	case "deal":
		d, err := s.CRM.Deal(r.Context(), id)
		if err != nil {
			return nil, "", err
		}
		v["Title"], v["Amount"], v["Currency"], v["Stage"] = d.Title, crm.Amount(d.Amount), d.Currency, stage(d.Stage)
		v["Company"], v["Contact"], v["Responsible"], v["Comment"], v["Number"] = d.Company.Name, d.Contact.Name, d.Responsible.Name, d.Comment, d.ID[:8]
		v["CloseDate"] = "" // the field is cleared, not left as «{CloseDate}», when the deal has none
		if d.CloseDate != nil {
			v["CloseDate"] = d.CloseDate.Format(s.tr(r, "format.date"))
		}
		if err := s.docParties(r, v, kind, id, d.Company.ID, d.Contact.ID, d.Amount); err != nil {
			return nil, "", err
		}
		return v, d.Title, nil
	case "lead":
		l, err := s.CRM.Lead(r.Context(), id)
		if err != nil {
			return nil, "", err
		}
		v["Title"], v["Name"], v["CompanyTitle"], v["Phone"], v["Email"], v["Source"] = l.Title, l.Name, l.CompanyTitle, l.Phone, l.Email, l.Source
		v["Amount"], v["Currency"], v["Stage"], v["Responsible"], v["Comment"] = crm.Amount(l.Amount), l.Currency, stage(l.Stage), l.Responsible.Name, l.Comment
		if err := s.docParties(r, v, kind, id, "", "", l.Amount); err != nil {
			return nil, "", err
		}
		v["Client.Title"], v["Client.Phone"], v["Client.Email"] = cmp.Or(l.CompanyTitle, l.Name), l.Phone, l.Email
		v["Client.FullName"] = v["Client.Title"]
		return v, l.Title, nil
	case "contact":
		x, err := s.CRM.Contact(r.Context(), id)
		if err != nil {
			return nil, "", err
		}
		v["Name"], v["Position"], v["Phone"], v["Email"], v["Company"], v["Responsible"], v["Comment"] = x.Name, x.Position, x.Phone, x.Email, x.Company.Name, x.Responsible.Name, x.Comment
		if err := s.docParties(r, v, kind, id, "", id, 0); err != nil {
			return nil, "", err
		}
		return v, x.Name, nil
	case "company":
		c, err := s.CRM.Company(r.Context(), id)
		if err != nil {
			return nil, "", err
		}
		v["Title"], v["Phone"], v["Email"], v["Website"], v["Address"], v["Responsible"], v["Comment"] = c.Title, c.Phone, c.Email, c.Website, c.Address, c.Responsible.Name, c.Comment
		if err := s.docParties(r, v, kind, id, id, "", 0); err != nil {
			return nil, "", err
		}
		return v, c.Title, nil
	case crm.TemplateInvoice:
		x, err := s.CRM.Invoice(r.Context(), id)
		if err != nil {
			return nil, "", err
		}
		date := s.tr(r, "format.date")
		v["Kind"], v["Number"], v["Title"], v["Status"] = s.tr(r, "crm.doc."+x.Kind), x.Code(), x.Title, s.tr(r, "crm.inv."+x.Status)
		v["IssueDate"], v["DueDate"], v["Amount"], v["Currency"] = x.IssueDate.Format(date), "", crm.Amount(x.Amount), x.Currency
		v["Company"], v["Contact"], v["Responsible"], v["Comment"], v["Card"] = x.Company.Name, x.Contact.Name, x.Responsible.Name, x.Comment, x.CardTitle
		if x.DueDate != nil {
			v["DueDate"] = x.DueDate.Format(date)
		}
		if err := s.docParties(r, v, kind, id, x.Company.ID, x.Contact.ID, x.Amount); err != nil {
			return nil, "", err
		}
		return v, cmp.Or(x.Title, v["Kind"]+" № "+v["Number"]), nil
	}
	return nil, "", i18n.E("err.bad_form")
}

// partyFields are the placeholders of «Моя компания» and of the client after their prefix: the card's and its
// default requisite's.
var partyFields = []string{"Title", "FullName", "INN", "KPP", "OGRN", "Director", "Address", "Phone", "Email", "Bank", "BIK",
	"Account", "CorrAccount"}

// productFields are those of a line of the card's products, {Product.Name}: its row of a table is made for each.
var productFields = []string{"Number", "Name", "Quantity", "Unit", "Price", "Discount", "TaxRate", "Tax", "Sum"}

// party puts the details of a company or a contact by the prefix (all of partyFields, filled or not).
func (s *Server) party(r *http.Request, v map[string]string, prefix, companyID, contactID string) error {
	var title, phone, email, address string
	switch {
	case companyID != "":
		c, err := s.CRM.Company(r.Context(), companyID)
		if err != nil {
			return err
		}
		title, phone, email, address = c.Title, c.Phone, c.Email, c.Address
	case contactID != "":
		c, err := s.CRM.Contact(r.Context(), contactID)
		if err != nil {
			return err
		}
		title, phone, email = c.Name, c.Phone, c.Email
	}
	q, err := s.CRM.Buyer(r.Context(), companyID, contactID)
	if err != nil {
		return err
	}
	if q == nil {
		q = &crm.Requisite{}
	}
	for k, x := range map[string]string{"Title": title, "FullName": cmp.Or(q.Name, title), "INN": q.INN, "KPP": q.KPP,
		"OGRN": q.OGRN, "Director": q.Director, "Address": cmp.Or(q.Address, address), "Phone": phone, "Email": email,
		"Bank": q.Bank, "BIK": q.BIK, "Account": q.Account, "CorrAccount": q.CorrAccount} {
		v[prefix+"."+k] = x
	}
	return nil
}

// docParties puts «Моя компания» ({MyCompany.…}, {MyCompany.Details} — the seller's text of quotes and invoices), the
// client ({Client.…}) and the totals of the card's products ({Total}, {TaxTotal}; with no products — the amount).
func (s *Server) docParties(r *http.Request, v map[string]string, kind, id, companyID, contactID string, amount float64) error {
	my, err := s.CRM.MyCompany(r.Context())
	if err != nil {
		return err
	}
	if err := s.party(r, v, "MyCompany", my, ""); err != nil && !errors.Is(err, crm.ErrNotFound) {
		return err
	}
	if v["MyCompany.Details"], err = s.CRM.Seller(r.Context()); err != nil {
		return err
	}
	if err := s.party(r, v, "Client", companyID, contactID); err != nil && !errors.Is(err, crm.ErrNotFound) {
		return err
	}
	rows, err := s.docRows(r, kind, id)
	if err != nil {
		return err
	}
	total, vat := amount, 0.0
	if len(rows) > 0 {
		total, vat = crm.RowsTotal(rows)
	}
	v["Total"], v["TaxTotal"], v["TotalNet"] = crm.Amount(total), crm.Amount(vat), crm.Amount(total-vat)
	v["TotalWords"] = inwords.Amount(s.lang(r), int64(math.Round(total*100)), v["Currency"])
	v["ProductsCount"] = strconv.Itoa(len(rows))
	return nil
}

// docRows are the products of a deal, a lead or an invoice (none of the other cards).
func (s *Server) docRows(r *http.Request, kind, id string) ([]crm.ProductRow, error) {
	switch e := docEntity(kind); e {
	case "deal", "lead":
		return s.CRM.ProductRows(r.Context(), e, id)
	case crm.TemplateInvoice:
		x, err := s.CRM.Invoice(r.Context(), id)
		if err != nil {
			return nil, err
		}
		return s.CRM.ProductRows(r.Context(), x.Kind, id)
	}
	return nil, nil
}

// docLists are the lists of the template: {Product.…} — a line of the card's products each.
func (s *Server) docLists(r *http.Request, kind, id string) (docgen.Rows, error) {
	rows, err := s.docRows(r, kind, id)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]string, len(rows))
	for i, x := range rows {
		item := map[string]string{"Number": strconv.Itoa(i + 1), "Name": x.Name, "Quantity": strconv.FormatFloat(x.Quantity, 'f', -1, 64),
			"Unit": cmp.Or(x.Unit, s.tr(r, "crm.unit_default")), "Price": crm.Amount(x.Price), "Discount": "", "TaxRate": "",
			"Tax": crm.Amount(x.VAT()), "Sum": crm.Amount(x.Total())}
		if x.Discount != 0 {
			item["Discount"] = strconv.FormatFloat(x.Discount, 'f', -1, 64) + "%"
		}
		if x.VATRate != nil {
			item["TaxRate"] = strconv.FormatFloat(*x.VATRate, 'f', -1, 64) + "%"
		}
		items[i] = item
	}
	return docgen.Rows{"Product": items}, nil
}

// cardValues are docValues with the card's own fields too, by their names ({Бюджет}); known — every field of the
// kind of card, filled or not.
func (s *Server) cardValues(r *http.Request, kind, id string) (values map[string]string, title string, known map[string]bool, err error) {
	if values, title, err = s.docValues(r, kind, id); err != nil {
		return nil, "", nil, err
	}
	known = map[string]bool{}
	for _, f := range docFields[docEntity(kind)] {
		known[f] = true
	}
	set, ok := tasks.CRMFieldSets[docEntity(kind)]
	if !ok {
		return values, title, known, nil
	}
	fields, err := s.Tasks.FieldValuesOf(r.Context(), set, id)
	if err != nil {
		return nil, "", nil, err
	}
	for _, f := range fields {
		known[f.Name] = true
		if _, taken := values[f.Name]; !taken {
			values[f.Name] = f.Text(s.tr(r, "format.date"))
			if f.Kind == "bool" && f.Value != "" {
				values[f.Name] = s.tr(r, "bool.true")
			}
		}
	}
	return values, title, known, nil
}

// docTemplateAllowed: the template is one the user may make documents by («Кому доступен»; administrators — any).
func (s *Server) docTemplateAllowed(r *http.Request, sess auth.Session, id string) error {
	if sess.Admin {
		return nil
	}
	ok, err := s.CRM.MayUseDocTemplate(r.Context(), id, sess.UserID)
	if err == nil && !ok {
		err = crm.ErrNotFound
	}
	return err
}

// docTemplateFile is the DOCX of a template of the card's kind.
func (s *Server) docTemplateFile(r *http.Request, kind, id string) (crm.DocTemplate, []byte, error) {
	return s.docTemplateOf(r, docEntity(kind), id)
}

// docTemplateOf is the DOCX of a template of the entity (employee — of an HR document).
func (s *Server) docTemplateOf(r *http.Request, entity, id string) (crm.DocTemplate, []byte, error) {
	t, err := s.CRM.DocTemplate(r.Context(), id)
	if err == nil && (entity == "" || t.Entity != entity) {
		err = crm.ErrNotFound
	}
	if err != nil {
		return t, nil, err
	}
	m, err := s.Storages.FileMeta(r.Context(), t.FileID)
	if err != nil {
		return t, nil, err
	}
	obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
	if err != nil {
		return t, nil, err
	}
	defer obj.Close()
	tpl, err := io.ReadAll(io.LimitReader(obj, maxTemplateSize))
	return t, tpl, err
}

// docFormValues are the values «Изменить значения» sent (v.Name), to go over those of the card.
func docFormValues(r *http.Request) map[string]string {
	values := map[string]string{}
	_ = r.ParseForm()
	for k, v := range r.PostForm {
		if n, ok := strings.CutPrefix(k, "v."); ok && n != "" && len(v) > 0 {
			values[n] = v[0]
		}
	}
	return values
}

const docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

// storeDocument fills the template and keeps the document as a file of go24.
func (s *Server) storeDocument(r *http.Request, sess auth.Session, tpl []byte, values map[string]string, lists docgen.Rows, name string) (string, error) {
	images, err := s.docImages(r)
	if err != nil {
		return "", err
	}
	images["PaymentQR"] = s.docPayQR(r, values)
	doc, err := docgen.Make(tpl, values, lists, images)
	if err != nil {
		return "", i18n.E("err.doc_not_docx")
	}
	name = strings.NewReplacer("/", "-", "\\", "-", "\"", "").Replace(name) + ".docx"
	key := path.Join("go24", "crm-documents", randomHex(16), name)
	if err := s.Storages.Put(r.Context(), sess.UserID, false, "", key, bytes.NewReader(doc), int64(len(doc)), docxType); err != nil {
		return "", err
	}
	return s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: docxType, Size: int64(len(doc)), UploadedBy: sess.UserID})
}

// crmDocument makes a document of the card from a template, numbered by its numerator, and keeps it at the card
// («Документы» of the card, a note in its history), as the document generator.
func (s *Server) crmDocument(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.docCheck(r, sess, kind, id, "read"); err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	doc, err := s.makeDocument(r, sess, kind, id, r.FormValue("template"), docFormValues(r))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "docs.made_msg", doc.TemplateName+" № "+doc.Number))+"#documents", http.StatusSeeOther)
}

// makeDocument makes the document of a card the user sees by a template they may use; the values given go over
// those of the card. The pages and the REST API make documents by it.
func (s *Server) makeDocument(r *http.Request, sess auth.Session, kind, id, tplID string, over map[string]string) (crm.Document, error) {
	if err := s.docTemplateAllowed(r, sess, tplID); err != nil {
		return crm.Document{}, err
	}
	return s.makeDocumentBy(r, sess, kind, id, tplID, over)
}

// makeDocumentBy makes the document of the card by the template, whoever may use it: the step «Создание документа» of
// a business process makes it so.
func (s *Server) makeDocumentBy(r *http.Request, sess auth.Session, kind, id, tplID string, over map[string]string) (crm.Document, error) {
	t, tpl, err := s.docTemplateFile(r, kind, tplID)
	if err != nil {
		return crm.Document{}, err
	}
	values, title, _, err := s.cardValues(r, kind, id)
	if err != nil {
		return crm.Document{}, err
	}
	now := time.Now()
	number, err := s.CRM.NextDocNumber(r.Context(), t.ID, now)
	if err != nil {
		return crm.Document{}, err
	}
	values["DocumentNumber"], values["DocumentDate"] = number, now.Format(s.tr(r, "format.date"))
	maps.Copy(values, over)
	lists, err := s.docLists(r, kind, id)
	if err != nil {
		return crm.Document{}, err
	}
	doc := crm.Document{TemplateID: t.ID, TemplateName: t.Name, Entity: docEntity(kind), EntityID: id, Number: number,
		Values: values, AuthorID: sess.UserID}
	doc.FileID, err = s.storeDocument(r, sess, tpl, values, lists, t.Name+" № "+number+" — "+title)
	if err == nil {
		doc.ID, err = s.CRM.AddDocument(r.Context(), doc)
	}
	return doc, err
}

// docField is a placeholder of the template with the value it gets.
type docField struct{ Name, Value string }

// crmDocumentForm is «Изменить значения»: the template's fields with the card's values (?template=) or those the
// document was made with (?doc=), to change before making it.
func (s *Server) crmDocumentForm(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.docCheck(r, sess, kind, id, "read"); err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	var doc crm.Document
	tplID := r.URL.Query().Get("template")
	if docID := r.URL.Query().Get("doc"); docID != "" {
		var err error
		if doc, err = s.CRM.Document(r.Context(), docEntity(kind), id, docID); err == nil && doc.TemplateID == "" {
			err = crm.ErrNotFound
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		tplID = doc.TemplateID
	} else if err := s.docTemplateAllowed(r, sess, tplID); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	t, tpl, err := s.docTemplateFile(r, kind, tplID)
	var names []string
	if err == nil {
		names, err = docgen.Names(tpl)
	}
	values, title, _, err2 := s.cardValues(r, kind, id)
	if err == nil {
		err = err2
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if doc.ID != "" {
		values = doc.Values
	} else {
		values["DocumentDate"] = time.Now().Format(s.tr(r, "format.date"))
	}
	fields := make([]docField, 0, len(names))
	for _, n := range names {
		if n == "DocumentNumber" && doc.ID == "" || strings.HasPrefix(n, "Product.") || docImageField(n) {
			continue // given when it is made; the products are the card's; the pictures are «Моя компания»'s
		}
		fields = append(fields, docField{n, values[n]})
	}
	s.render(w, r, "crmdocvalues", page{Title: t.Name, Nav: "crm_" + kind, Data: map[string]any{"Kind": kind, "ID": id,
		"CardTitle": title, "Template": t, "Doc": doc, "Fields": fields, "Back": back}})
}

// crmDocumentRemake makes the document again with the values sent; its number stays.
func (s *Server) crmDocumentRemake(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.docCheck(r, sess, kind, id, "write"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	doc, err := s.CRM.Document(r.Context(), docEntity(kind), id, r.PathValue("doc"))
	if err == nil {
		err = s.remakeDocument(r, sess, doc, docFormValues(r))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "docs.remade"))+"#documents", http.StatusSeeOther)
}

// crmKind is the path's kind of cards of an entity ("deal" → "deals").
func crmKind(entity string) string {
	if entity == crm.TemplateInvoice {
		return "invoices"
	}
	for k, e := range crmEntities {
		if e == entity {
			return k
		}
	}
	return ""
}

// remakeDocument makes the document again with the values given over those it was made with; its number stays,
// the products are the card's as they are now.
func (s *Server) remakeDocument(r *http.Request, sess auth.Session, doc crm.Document, over map[string]string) error {
	if doc.TemplateID == "" {
		return crm.ErrNotFound
	}
	kind := crmKind(doc.Entity)
	_, tpl, err := s.docTemplateFile(r, kind, doc.TemplateID)
	if err != nil {
		return err
	}
	values := maps.Clone(doc.Values)
	if values == nil {
		values = map[string]string{}
	}
	maps.Copy(values, over)
	values["DocumentNumber"] = doc.Number
	lists, err := s.docLists(r, kind, doc.EntityID)
	if err != nil {
		return err
	}
	fileID, err := s.storeDocument(r, sess, tpl, values, lists, strings.TrimSuffix(doc.FileName, ".docx"))
	if err == nil {
		err = s.CRM.RemakeDocument(r.Context(), doc.ID, fileID, values)
	}
	return err
}

// crmDocumentFile gives the document's DOCX to those who see the card.
func (s *Server) crmDocumentFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.docCheck(r, sess, kind, id, "read"); err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	doc, err := s.CRM.Document(r.Context(), docEntity(kind), id, r.PathValue("doc"))
	var m storages.FileMeta
	if err == nil {
		m, err = s.Storages.FileMeta(r.Context(), doc.FileID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
	if err != nil {
		s.fail(w, "open document", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", docxType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(m.Name)))
	_, _ = io.Copy(w, obj)
}

// crmDocumentDelete deletes a document of the card (those who may change the card).
func (s *Server) crmDocumentDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.docCheck(r, sess, kind, id, "write"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	doc, err := s.CRM.Document(r.Context(), docEntity(kind), id, r.PathValue("doc"))
	if err == nil {
		err = s.CRM.DeleteDocument(r.Context(), doc.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#documents", http.StatusSeeOther)
}

// docTemplateNumerator changes the format of a template's numbers.
func (s *Server) docTemplateNumerator(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.CRM.SetDocNumerator(r.Context(), r.PathValue("id"), r.FormValue("numerator")); err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	http.Redirect(w, r, "/crm/documents?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// docTemplateAccess is «Кому доступен» of a template: people and departments; nobody chosen — everybody.
func (s *Server) docTemplateAccess(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	err := r.ParseForm()
	if err == nil {
		err = s.CRM.SetDocTemplateAccess(r.Context(), r.PathValue("id"), r.Form["to"])
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	http.Redirect(w, r, "/crm/documents?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// ---------- PDF, «Публичная ссылка» and «Отправить» of a card's document ----------

// documentPDF is the PDF of the document: made by the document server the first time (ONLYOFFICE ConvertService)
// and kept until the document is remade.
func (s *Server) documentPDF(r *http.Request, doc crm.Document) (string, error) {
	if doc.PDFFileID != "" {
		return doc.PDFFileID, nil
	}
	pdf, err := s.officePDF(r, doc.FileID, doc.AuthorID, "crm-documents")
	if err != nil {
		return "", err
	}
	if kept, err := s.CRM.SetDocumentPDF(r.Context(), doc.ID, doc.FileID, pdf); err != nil || !kept {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, pdf) // remade or made by another request meanwhile
		if err == nil {
			err = crm.ErrNotFound
		}
		return "", err
	}
	return pdf, nil
}

// officePDF has the document server make a PDF of a kept office file (ONLYOFFICE ConvertService) and keeps it as a
// new file of the owner under go24/{dir}.
func (s *Server) officePDF(r *http.Request, fileID, ownerID, dir string) (string, error) {
	if s.Office == nil {
		return "", crm.ErrNotFound
	}
	m, err := s.Storages.FileMeta(r.Context(), fileID)
	if err != nil {
		return "", err
	}
	link, err := s.Office.Convert(r.Context(), s.Office.Self+"/onlyoffice/convert/"+fileID+"/"+s.Office.Stamp("convert", fileID, m.Key),
		office.Ext(m.Name), office.Key(fileID, m.Key), m.Name, "pdf")
	if err != nil {
		s.Log.Error("office pdf", "file", fileID, "err", err)
		return "", i18n.E("docs.pdf_failed")
	}
	data, err := officeFetch(r, link)
	if err != nil {
		s.Log.Error("office pdf", "file", fileID, "err", err)
		return "", i18n.E("docs.pdf_failed")
	}
	name := strings.TrimSuffix(m.Name, path.Ext(m.Name)) + ".pdf"
	key := path.Join("go24", dir, randomHex(16), name)
	if err := s.Storages.Put(r.Context(), ownerID, false, "", key, bytes.NewReader(data), int64(len(data)), "application/pdf"); err != nil {
		return "", err
	}
	return s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: "application/pdf", Size: int64(len(data)), UploadedBy: ownerID})
}

// officeFetch is the file the document server converted, by its link.
func officeFetch(r *http.Request, link string) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxOfficeFile))
	if err == nil && res.StatusCode != http.StatusOK {
		err = fmt.Errorf("pdf: %s", res.Status)
	}
	return data, err
}

// officeConvertFile gives the document server a file go24 has it convert (a document of a card, not of the drive).
func (s *Server) officeConvertFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.Storages.FileMeta(r.Context(), id)
	if s.Office == nil || err != nil || !s.Office.Stamped(r.PathValue("stamp"), "convert", id, m.Key) {
		http.NotFound(w, r)
		return
	}
	s.sendFile(w, r, m, "attachment")
}

// sendFile gives a kept file (disposition attachment or inline).
func (s *Server) sendFile(w http.ResponseWriter, r *http.Request, m storages.FileMeta, disposition string) {
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if m.Size > 0 {
		var err error
		if obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key); err != nil {
			s.fail(w, "open document", err)
			return
		}
	}
	defer obj.Close()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(m.Name)))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("send file", "key", m.Key, "err", err)
	}
}

// crmDocumentPDF gives the document in PDF to those who see the card.
func (s *Server) crmDocumentPDF(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.docCheck(r, sess, kind, id, "read"); err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	doc, err := s.CRM.Document(r.Context(), docEntity(kind), id, r.PathValue("doc"))
	var pdf string
	if err == nil {
		pdf, err = s.documentPDF(r, doc)
	}
	var m storages.FileMeta
	if err == nil {
		m, err = s.Storages.FileMeta(r.Context(), pdf)
	}
	if err != nil {
		s.taskErr(w, r, err, back+"#documents")
		return
	}
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" {
		disposition = "inline"
	}
	s.sendFile(w, r, m, disposition)
}

// crmDocumentPublic switches the document's public link on (on=1) or off (those who may change the card).
func (s *Server) crmDocumentPublic(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if err := s.docCheck(r, sess, kind, id, "write"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	doc, err := s.CRM.Document(r.Context(), docEntity(kind), id, r.PathValue("doc"))
	if err == nil {
		_, err = s.CRM.SetDocumentPublic(r.Context(), doc.ID, r.FormValue("on") == "1")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("crm document link", "user_id", sess.UserID, "doc", doc.ID, "on", r.FormValue("on") == "1")
	http.Redirect(w, r, back+"#documents", http.StatusSeeOther)
}

// publicDocument is the page of a document's public link: anyone with the link, no sign-in; the document shown in
// PDF (when the document server is there) and downloaded in PDF or DOCX.
func (s *Server) publicDocument(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	doc, err := s.CRM.PublicDocument(r.Context(), r.PathValue("token"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	title := doc.TemplateName
	if doc.Number != "" {
		title += " № " + doc.Number
	}
	s.render(w, r, "pubdoc", page{Title: title, Data: map[string]any{"Doc": doc, "Title": title, "PDF": s.Office != nil}})
}

// publicDocumentFile is the file of a public link: docx, or pdf (inline=1 — shown in the page).
func (s *Server) publicDocumentFile(w http.ResponseWriter, r *http.Request) {
	doc, err := s.CRM.PublicDocument(r.Context(), r.PathValue("token"))
	id := doc.FileID
	if err == nil && r.PathValue("format") == "pdf" {
		id, err = s.documentPDF(r, doc)
	} else if err == nil && r.PathValue("format") != "docx" {
		err = crm.ErrNotFound
	}
	var m storages.FileMeta
	if err == nil {
		m, err = s.Storages.FileMeta(r.Context(), id)
	}
	if err != nil {
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

// documentAttachments are the card's documents «Отправить» puts into the letter: doc=ID:pdf or doc=ID:docx.
func (s *Server) documentAttachments(r *http.Request, entity, id string) ([]mail.Attachment, error) {
	var out []mail.Attachment
	for _, v := range r.Form["doc"] {
		docID, format, _ := strings.Cut(v, ":")
		doc, err := s.CRM.Document(r.Context(), entity, id, docID)
		if err != nil {
			return nil, err
		}
		file := doc.FileID
		if format == "pdf" {
			if file, err = s.documentPDF(r, doc); err != nil {
				return nil, err
			}
		}
		m, err := s.Storages.FileMeta(r.Context(), file)
		if err != nil {
			return nil, err
		}
		data, err := s.readFile(r, m, mail.MaxSendSize)
		if err != nil {
			return nil, err
		}
		out = append(out, mail.Attachment{Name: m.Name, ContentType: m.ContentType, Data: data})
	}
	return out, nil
}

// BPDocument is the hook of business processes (bp.Store.Document): the document of the card by the template, made by
// the user, and its id.
func (s *Server) BPDocument(ctx context.Context, userID, entity, entityID, tplID string, values map[string]string) (string, error) {
	kind := ""
	for k, e := range crmEntities {
		if e == entity {
			kind = k
		}
	}
	if kind == "" {
		return "", crm.ErrNotFound
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	if err != nil {
		return "", err
	}
	doc, err := s.makeDocumentBy(r, auth.Session{UserID: userID}, kind, entityID, tplID, values)
	return doc.ID, err
}
