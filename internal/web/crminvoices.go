package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Предложения» and «Счета» of the CRM: made from a deal, kept at /crm/invoices/{id}, printed ----------

// crmInvoiceMake makes a quote or an invoice of a deal or a lead (who may change the card).
func (s *Server) crmInvoiceMake(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if kind != "deals" && kind != "leads" {
		http.NotFound(w, r)
		return
	}
	err := s.crmCheck(r, sess, kind, id, "write")
	var inv string
	if err == nil {
		inv, err = s.CRM.MakeCardInvoice(r.Context(), sess.UserID, r.FormValue("kind"), crmEntities[kind], id, 5)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/crm/invoices/"+inv, http.StatusSeeOther)
}

// crmInvoiceOf is a quote or an invoice the person may read (action "read") or change ("write") by the rights on its
// deal or lead.
func (s *Server) crmInvoiceOf(r *http.Request, sess auth.Session, action string) (crm.Invoice, error) {
	v, err := s.CRM.Invoice(r.Context(), r.PathValue("id"))
	if err == nil {
		err = s.invoiceCheck(r, sess, v, action)
	}
	return v, err
}

func (s *Server) invoiceCheck(r *http.Request, sess auth.Session, v crm.Invoice, action string) error {
	entity, id := v.Card()
	return s.crmCheck(r, sess, entity+"s", id, action)
}

// crmInvoicesList is the «Счета» tab: the quotes and invoices of the deals one may read, by kind and status.
func (s *Server) crmInvoicesList(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	kind := q.Get("kind")
	if crm.InvoiceStatuses[kind] == nil {
		kind = ""
	}
	list, err := s.CRM.Invoices(r.Context(), sess.UserID, "", kind, q.Get("status"))
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	data := map[string]any{"Kind": "invoices", "Items": list, "DocKind": kind, "Status": q.Get("status"), "Statuses": crm.InvoiceStatuses}
	if err = s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crminvoices", page{TitleKey: "crm.invoices", Nav: "crm_invoices", Data: data})
}

// crmInvoicePage is a quote or an invoice: its details, status, products; changed by who may change its deal.
func (s *Server) crmInvoicePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data, err := s.crmInvoiceData(r, sess)
	if err == nil {
		data["Users"], err = s.Tasks.ListUsers(r.Context())
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/invoices")
		return
	}
	v := data["Item"].(crm.Invoice)
	if err == nil && data["CanWrite"] == true { // «Отправить» to the client: the deal's letter with the document
		entity, id := v.Card()
		data["MailForm"], err = s.crmMailForm(r, sess, entity, id)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/invoices")
		return
	}
	name := s.tr(r, "crm.doc."+v.Kind) + " № " + v.Code()
	ext := ".html"
	if s.Office != nil {
		ext = ".pdf"
	}
	data["MailInv"] = map[string]string{"ID": v.ID, "File": name + ext,
		"Subject": name + " " + s.tr(r, "crm.inv_from") + " " + v.IssueDate.Format(s.tr(r, "format.date")),
		"Body": "<p>" + html.EscapeString(s.tr(r, "crm.inv_send_text_"+v.Kind, v.Code(), crm.Amount(v.Amount), v.Currency)) + "</p>"}
	s.render(w, r, "crminvoice", page{Title: s.tr(r, "crm.doc."+v.Kind) + " № " + v.Code(), Nav: "crm_invoices", Data: data})
}

func (s *Server) crmInvoiceData(r *http.Request, sess auth.Session) (map[string]any, error) {
	v, err := s.crmInvoiceOf(r, sess, "read")
	if err != nil {
		return nil, err
	}
	data, err := s.invoiceDoc(r, v)
	if err != nil {
		return nil, err
	}
	data["Statuses"], data["Me"], data["Base"] = crm.InvoiceStatuses[v.Kind], sess.UserID, s.BaseURL
	if data["Catalog"], err = s.CRM.ProductsIn(r.Context(), v.Currency); err != nil {
		return nil, err
	}
	if v.Kind == "quote" {
		_, card := v.Card()
		if data["Made"], err = s.CRM.Invoices(r.Context(), sess.UserID, card, "invoice", ""); err != nil {
			return nil, err
		}
	}
	data["CanWrite"] = s.invoiceCheck(r, sess, v, "write") == nil
	if sess.Admin { // «Генератор документов»: the print forms of quotes and invoices
		data["Docs"], err = s.CRM.DocTemplates(r.Context(), crm.TemplateInvoice)
	} else {
		data["Docs"], err = s.CRM.DocTemplatesFor(r.Context(), crm.TemplateInvoice, sess.UserID)
	}
	if err == nil {
		data["Documents"], err = s.CRM.Documents(r.Context(), crm.TemplateInvoice, v.ID)
		data["PDF"] = s.Office != nil
	}
	if err == nil && s.Sign != nil { // «Подпись»: the latest sending of each document
		data["Signs"], err = s.Sign.OfCard(r.Context(), crm.TemplateInvoice, v.ID)
	}
	return data, err
}

// invoiceDoc is what the document for the client shows: its products, payments, the seller and the buyer.
func (s *Server) invoiceDoc(r *http.Request, v crm.Invoice) (map[string]any, error) {
	data := map[string]any{"Item": v}
	rows, err := s.CRM.ProductRows(r.Context(), v.Kind, v.ID)
	if err != nil {
		return nil, err
	}
	data["Rows"] = rows
	data["RowsTotal"], data["RowsVAT"] = crm.RowsTotal(rows)
	if data["Payments"], err = s.CRM.Payments(r.Context(), v.ID); err != nil {
		return nil, err
	}
	if data["Seller"], err = s.CRM.Seller(r.Context()); err != nil {
		return nil, err
	}
	if data["Buyer"], err = s.CRM.Buyer(r.Context(), v.Company.ID, v.Contact.ID); err != nil { // the client's default requisite
		return nil, err
	}
	return data, nil
}

// crmInvoiceAction: "save" the details, set the "status", take a payment ("pay") or remove one ("unpay"), switch the
// client's link ("public"), make an invoice of a quote ("to_invoice"), or "delete" it.
func (s *Server) crmInvoiceAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/invoices/" + r.PathValue("id")
	v, err := s.crmInvoiceOf(r, sess, "write")
	if err == nil {
		switch r.FormValue("op") {
		case "save":
			day := func(k string) *time.Time {
				if t, perr := time.ParseInLocation("2006-01-02", r.FormValue(k), time.Local); perr == nil {
					return &t
				}
				return nil
			}
			v.Title, v.Comment, v.DueDate, v.Responsible.ID = r.FormValue("title"), r.FormValue("comment"), day("due_date"), r.FormValue("responsible")
			if d := day("issue_date"); d != nil {
				v.IssueDate = *d
			}
			err = s.CRM.SaveInvoice(r.Context(), v)
		case "status":
			err = s.CRM.SetInvoiceStatus(r.Context(), sess.UserID, v.ID, r.FormValue("status"))
		case "pay":
			p := crm.Payment{Comment: r.FormValue("comment")}
			if p.Amount, err = strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(r.FormValue("amount"), " ", ""), ",", "."), 64); err != nil {
				err = i18n.E("err.crm_pay_amount", strconv.FormatFloat(v.Due(), 'f', 2, 64))
				break
			}
			if t, perr := time.ParseInLocation("2006-01-02", r.FormValue("paid_on"), time.Local); perr == nil {
				p.PaidOn = t
			}
			err = s.CRM.AddPayment(r.Context(), sess.UserID, v.ID, p)
			back += "#payments"
		case "unpay":
			err = s.CRM.DeletePayment(r.Context(), sess.UserID, v.ID, r.FormValue("payment"))
			back += "#payments"
		case "public":
			_, err = s.CRM.SetInvoicePublic(r.Context(), v.ID, r.FormValue("on") != "")
		case "to_invoice":
			var inv string
			if inv, err = s.CRM.InvoiceOfQuote(r.Context(), sess.UserID, v.ID, 5); err == nil {
				http.Redirect(w, r, "/crm/invoices/"+inv, http.StatusSeeOther)
				return
			}
		case "delete":
			if err = s.CRM.DeleteInvoice(r.Context(), v.ID); err == nil {
				back = v.CardPath()
			}
		default:
			err = i18n.E("err.bad_form")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	path, frag, _ := strings.Cut(back, "#")
	if frag != "" {
		frag = "#" + frag
	}
	http.Redirect(w, r, path+"?msg="+url.QueryEscape(s.tr(r, "crm.saved"))+frag, http.StatusSeeOther)
}

// crmInvoiceRows saves the products of a quote or an invoice (the same table as a deal's).
func (s *Server) crmInvoiceRows(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/invoices/" + r.PathValue("id")
	v, err := s.crmInvoiceOf(r, sess, "write")
	if err == nil {
		err = r.ParseForm()
	}
	if err == nil {
		_, err = s.CRM.SetProductRows(r.Context(), sess.UserID, v.Kind, v.ID, formRows(r))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#products", http.StatusSeeOther)
}

// crmInvoicePrint is the document for the client on a page of its own, printed at once (or saved as PDF).
func (s *Server) crmInvoicePrint(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data, err := s.crmInvoiceData(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/crm/invoices")
		return
	}
	s.invoicePrint(w, r, data)
}

// publicInvoice is a quote or an invoice by the client's link: the printed document, no sign-in.
func (s *Server) publicInvoice(w http.ResponseWriter, r *http.Request) {
	v, err := s.CRM.PublicInvoice(r.Context(), r.PathValue("token"))
	var data map[string]any
	if err == nil {
		data, err = s.invoiceDoc(r, v)
	}
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.invoicePrint(w, r, data)
}

func (s *Server) invoicePrint(w http.ResponseWriter, r *http.Request, data map[string]any) {
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["crminvoice"].ExecuteTemplate(&buf, "invprint", data); err != nil {
		s.fail(w, "crm invoice print", err)
		return
	}
	s.invoiceHTML(w, buf.Bytes())
}

func (s *Server) invoiceHTML(w http.ResponseWriter, page []byte) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// invoiceBare is the document for the client as a file of its own (no print button, no script), in the language.
func (s *Server) invoiceBare(r *http.Request, v crm.Invoice, lang string) ([]byte, error) {
	data, err := s.invoiceDoc(r, v)
	if err != nil {
		return nil, err
	}
	data["Bare"] = true
	var buf bytes.Buffer
	err = s.pages[lang]["crminvoice"].ExecuteTemplate(&buf, "invprint", data)
	return buf.Bytes(), err
}

// invoiceFile is the quote or the invoice the letter to the client carries: in PDF, made by the document server of
// the printed page (ONLYOFFICE ConvertService); without the document server — the page itself, in HTML.
func (s *Server) invoiceFile(r *http.Request, v crm.Invoice) (mail.Attachment, error) {
	lang := s.lang(r)
	page, err := s.invoiceBare(r, v, lang)
	if err != nil {
		return mail.Attachment{}, err
	}
	name := s.tr(r, "crm.doc."+v.Kind) + " № " + v.Code()
	if s.Office == nil {
		return mail.Attachment{Name: name + ".html", ContentType: "text/html; charset=utf-8", Data: page}, nil
	}
	sum := sha256.Sum256(page)
	state := hex.EncodeToString(sum[:8])
	link, err := s.Office.Convert(r.Context(), s.Office.Self+"/onlyoffice/invoice/"+v.ID+"/"+lang+"/"+s.Office.Stamp("invoice", v.ID, lang),
		"html", v.ID+"-"+state, name+".html", "pdf")
	var data []byte
	if err == nil {
		data, err = officeFetch(r, link)
	}
	if err != nil {
		s.Log.Error("invoice pdf", "invoice", v.ID, "err", err)
		return mail.Attachment{}, i18n.E("docs.pdf_failed")
	}
	return mail.Attachment{Name: name + ".pdf", ContentType: "application/pdf", Data: data}, nil
}

// mailInvoice puts a quote or an invoice of the deal or the lead into the card's letter to the client «Отправить»:
// its file («inv_file») and its client's link («inv_link»; switched on when off).
func (s *Server) mailInvoice(r *http.Request, kind, cardID, invID string, l *mail.Letter) (crm.Invoice, error) {
	v, err := s.CRM.Invoice(r.Context(), invID)
	if entity, id := v.Card(); err == nil && (entity != crmEntities[kind] || id != cardID) {
		err = crm.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if r.FormValue("inv_link") != "" {
		token := v.PublicToken
		if token == "" {
			if token, err = s.CRM.SetInvoicePublic(r.Context(), v.ID, true); err != nil {
				return v, err
			}
		}
		link := html.EscapeString(s.BaseURL + "/q/" + token)
		l.HTML += `<p><a href="` + link + `">` + link + `</a></p>`
	}
	if r.FormValue("inv_file") != "" {
		f, err := s.invoiceFile(r, v)
		if err != nil {
			return v, err
		}
		l.Files = append(l.Files, f)
	}
	return v, nil
}

// officeInvoice gives the document server the page of a quote or an invoice it makes a PDF of.
func (s *Server) officeInvoice(w http.ResponseWriter, r *http.Request) {
	id, lang := r.PathValue("id"), r.PathValue("lang")
	if s.Office == nil || s.pages[lang] == nil || !s.Office.Stamped(r.PathValue("stamp"), "invoice", id, lang) {
		http.NotFound(w, r)
		return
	}
	v, err := s.CRM.Invoice(r.Context(), id)
	var page []byte
	if err == nil {
		page, err = s.invoiceBare(r, v, lang)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.invoiceHTML(w, page)
}

// crmSeller keeps the company's details printed on quotes and invoices (administrators).
func (s *Server) crmSeller(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/invoices"
	if b := r.FormValue("back"); len(b) > 14 && b[:14] == "/crm/invoices/" {
		back = b
	}
	var err error = i18n.E("err.forbidden")
	if sess.Admin {
		err = s.CRM.SetSeller(r.Context(), sess.UserID, r.FormValue("seller"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
