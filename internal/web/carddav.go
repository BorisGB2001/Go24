package web

import (
	"bytes"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// ---------- «Сотрудники» over CardDAV: the company's address book on iPhone and Mac, Android (DAVx5), Thunderbird ----------
//
// As the «Синхронизация контактов»: /carddav/ is the person (their principal and address book home),
// /carddav/employees/ — the address book of the company's employees, one card each (/carddav/employees/{ID}.vcf).
// It is read only: the cards are changed in go24. One signs in with an API key that may «company:read»;
// /.well-known/carddav leads there.
//
// /carddav/contacts/ — the person's «Личные контакты» of the mail, as Outlook's contacts on a phone (GO24DEV-991): a key
// that may «mail:read» reads them, one that may «mail:write» also creates, changes and deletes them (PUT, DELETE with
// If-Match and If-None-Match); a card is kept as the program sent it.

const (
	carddavRoot     = "/carddav/"
	carddavBook     = "employees"
	carddavContacts = "contacts"
	carddavLimit    = 512 << 10 // a photo larger than this is not put on a card
)

// carddavBooks are the address books, each with the scope a key needs to read it, in their order.
var carddavBooks = []struct{ name, scope string }{{carddavBook, "company:read"}, {carddavContacts, "mail:read"}}

// cardBooks are the books the key may read.
func cardBooks(p apikeys.Principal) []string {
	var out []string
	for _, b := range carddavBooks {
		if p.Can(b.scope) {
			out = append(out, b.name)
		}
	}
	return out
}

// carddavURL is the address contact programs are given ("" — none without API keys).
func (s *Server) carddavURL(r *http.Request) string {
	if tasks.IsGuestCtx(r.Context()) || s.Keys == nil {
		return ""
	}
	if s.CallURL != "" { // iPhone and Mac want https
		return s.CallURL + carddavRoot
	}
	return s.absURL(r, carddavRoot)
}

func (s *Server) wellKnownCardDAV(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, carddavRoot, http.StatusMovedPermanently)
}

// cardOptions are the options of the cards one request makes.
func (s *Server) cardOptions(r *http.Request) company.CardOptions {
	o := company.CardOptions{InnerLabel: s.tr(r, "profile.inner_phone")}
	if s.Portal != nil {
		o.Org = s.Portal.Current().Name
	}
	return o
}

// cardPhoto is the employee's photo made small for a card (nil — none, or one a card cannot take).
func (s *Server) cardPhoto(r *http.Request, p company.Profile) []byte {
	if p.PhotoKey == "" || s.Storages == nil {
		return nil
	}
	obj, err := s.Storages.Open(r.Context(), nil, p.PhotoKey)
	if err != nil {
		s.Log.Warn("card photo", "user", p.ID, "err", err)
		return nil
	}
	defer obj.Close()
	data, ok := company.CardPhoto(obj)
	if !ok || len(data) > carddavLimit {
		return nil
	}
	return data
}

// vcard is the employee's card, with the photo when asked.
func (s *Server) vcard(r *http.Request, p company.Profile, photo bool) string {
	o := s.cardOptions(r)
	if photo {
		o.Photo = s.cardPhoto(r, p)
	}
	return company.VCard(p, o)
}

// cardItem is a card of the address book: its name in the book, version and text (made when asked for).
type cardItem struct {
	name string
	etag string
	text func() string
}

// davBookReq is a request to the address books: the key's person and the book it names ("" — the top).
type davBookReq struct {
	p     apikeys.Principal
	book  string
	write bool // the key may change the book
}

func (s *Server) carddav(w http.ResponseWriter, r *http.Request) {
	p, ok := s.keyUser(w, r, "company:read|mail:read", "err.carddav_key")
	if !ok {
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/carddav"), "/"), "/")
	if parts[0] == "" {
		parts = nil
	}
	if len(parts) > 2 || len(parts) > 0 && !slices.Contains(cardBooks(p), parts[0]) {
		http.NotFound(w, r)
		return
	}
	c := davBookReq{p: p}
	name := ""
	if len(parts) > 0 {
		c.book = parts[0]
		c.write = c.book == carddavContacts && p.Can("mail:write")
	}
	if len(parts) > 1 {
		name = parts[1]
	}
	allow := "OPTIONS, GET, HEAD, PROPFIND, PROPPATCH, REPORT"
	if c.write {
		allow += ", PUT, DELETE"
	}
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("DAV", "1, 3, addressbook")
		w.Header().Set("Allow", allow)
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		s.cardPropfind(w, r, c, name)
	case "REPORT":
		s.cardReport(w, r, c, len(parts) > 0 && name == "")
	case "GET", "HEAD":
		s.cardGet(w, r, c, name)
	case "PROPPATCH": // nothing is changed by a program
		(&caldavReq{s: s, w: w, r: r}).proppatch()
	case "PUT", "DELETE":
		switch {
		case c.book == carddavContacts && !c.write:
			http.Error(w, s.tr(r, "carddav.contacts_read_only"), http.StatusForbidden)
		case !c.write:
			http.Error(w, s.tr(r, "carddav.read_only"), http.StatusForbidden)
		case name == "":
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		case r.Method == "PUT":
			s.cardPut(w, r, c, name)
		default:
			s.cardDelete(w, r, c, name)
		}
	case "MKCOL", "MOVE", "COPY":
		http.Error(w, s.tr(r, "carddav.read_only"), http.StatusForbidden)
	default:
		w.Header().Set("Allow", allow)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

// addressCards are the cards of the address book: the company's working employees, or the person's own contacts.
func (s *Server) addressCards(r *http.Request, c davBookReq) ([]cardItem, error) {
	if c.book == carddavContacts {
		list, err := s.Mail.ContactCards(r.Context(), c.p.UserID)
		if err != nil {
			return nil, err
		}
		out := make([]cardItem, 0, len(list))
		for _, x := range list {
			text := x.VCard()
			out = append(out, cardItem{name: x.Href, etag: mail.CardETag(text), text: func() string { return text }})
		}
		return out, nil
	}
	list, err := s.Company.Employees(r.Context(), company.Filter{})
	if err != nil {
		return nil, err
	}
	o := s.cardOptions(r)
	out := make([]cardItem, 0, len(list))
	for _, p := range list {
		out = append(out, cardItem{name: p.ID + ".vcf", etag: company.CardETag(p, o), text: func() string { return s.vcard(r, p, true) }})
	}
	return out, nil
}

func (s *Server) cardFail(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("carddav", "method", r.Method, "path", r.URL.Path, "err", err)
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
}

// cardCommon: what every resource tells of the person; write — the resource may be changed.
func cardCommon(write bool) map[xml.Name]string {
	priv := el(nsDAV, "privilege", el(nsDAV, "read", ""))
	if write {
		priv += el(nsDAV, "privilege", el(nsDAV, "write", "")) + el(nsDAV, "privilege", el(nsDAV, "write-content", "")) +
			el(nsDAV, "privilege", el(nsDAV, "bind", "")) + el(nsDAV, "privilege", el(nsDAV, "unbind", ""))
	}
	me := hrefEl(carddavRoot)
	return map[xml.Name]string{
		{Space: nsDAV, Local: "current-user-principal"}:     el(nsDAV, "current-user-principal", me),
		{Space: nsDAV, Local: "principal-URL"}:              el(nsDAV, "principal-URL", me),
		{Space: nsDAV, Local: "owner"}:                      el(nsDAV, "owner", me),
		{Space: nsDAV, Local: "principal-collection-set"}:   el(nsDAV, "principal-collection-set", me),
		{Space: nsCard, Local: "addressbook-home-set"}:      el(nsCard, "addressbook-home-set", me),
		{Space: nsDAV, Local: "current-user-privilege-set"}: el(nsDAV, "current-user-privilege-set", priv),
	}
}

// the properties allprop answers, by the kind of resource
var (
	cardTopProps  = []string{"resourcetype", "displayname", "current-user-principal", "principal-URL", "addressbook-home-set", "owner"}
	cardBookProps = []string{"resourcetype", "displayname", "getctag", "owner"}
)

func cardBookHref(book string) string { return carddavRoot + book + "/" }

func cardHref(book, name string) string { return cardBookHref(book) + url.PathEscape(name) }

// cardBookName is the name a program shows for the book.
func (s *Server) cardBookName(r *http.Request, book string) string {
	if book == carddavContacts {
		return s.tr(r, "carddav.contacts_book")
	}
	return s.tr(r, "carddav.book")
}

func (s *Server) cardBookProps(r *http.Request, c davBookReq, items []cardItem) map[xml.Name]string {
	etags := make([]string, len(items))
	for i, it := range items {
		etags[i] = it.etag
	}
	report := func(n string) string { return el(nsDAV, "supported-report", el(nsDAV, "report", el(nsCard, n, ""))) }
	have := cardCommon(c.write)
	have[xml.Name{Space: nsDAV, Local: "resourcetype"}] = el(nsDAV, "resourcetype", el(nsDAV, "collection", "")+el(nsCard, "addressbook", ""))
	have[xml.Name{Space: nsDAV, Local: "displayname"}] = el(nsDAV, "displayname", html.EscapeString(s.cardBookName(r, c.book)))
	have[xml.Name{Space: nsDAV, Local: "supported-report-set"}] = el(nsDAV, "supported-report-set", report("addressbook-multiget")+report("addressbook-query"))
	have[xml.Name{Space: nsCS, Local: "getctag"}] = el(nsCS, "getctag", company.CardsCTag(etags))
	have[xml.Name{Space: nsCard, Local: "supported-address-data"}] = el(nsCard, "supported-address-data",
		`<card:address-data-type content-type="text/vcard" version="3.0"/>`)
	have[xml.Name{Space: nsCard, Local: "max-resource-size"}] = el(nsCard, "max-resource-size", strconv.Itoa(1<<20))
	return have
}

// cardProps are the properties of a card; its text (with the photo) is made only when asked for.
func (s *Server) cardProps(c davBookReq, it cardItem, b davBody) davResp {
	have := cardCommon(c.write)
	have[xml.Name{Space: nsDAV, Local: "resourcetype"}] = el(nsDAV, "resourcetype", "")
	have[xml.Name{Space: nsDAV, Local: "getetag"}] = el(nsDAV, "getetag", html.EscapeString(it.etag))
	have[xml.Name{Space: nsDAV, Local: "getcontenttype"}] = el(nsDAV, "getcontenttype", "text/vcard; charset=utf-8")
	for _, n := range b.Prop.Names {
		if n.XMLName.Space == nsCard && n.XMLName.Local == "address-data" {
			have[n.XMLName] = el(nsCard, "address-data", html.EscapeString(it.text()))
		}
	}
	return props(cardHref(c.book, it.name), b, []string{"resourcetype", "getetag", "getcontenttype"}, have)
}

func (s *Server) cardPropfind(w http.ResponseWriter, r *http.Request, c davBookReq, name string) {
	b, ok := davReadBody(w, r)
	if !ok {
		return
	}
	deep := r.Header.Get("Depth") != "0"
	var out []davResp
	if c.book == "" {
		have := cardCommon(false)
		have[xml.Name{Space: nsDAV, Local: "resourcetype"}] = el(nsDAV, "resourcetype", el(nsDAV, "collection", "")+el(nsDAV, "principal", ""))
		have[xml.Name{Space: nsDAV, Local: "displayname"}] = el(nsDAV, "displayname", html.EscapeString(s.tr(r, "carddav.book")))
		out = append(out, props(carddavRoot, b, cardTopProps, have))
		if deep {
			for _, book := range cardBooks(c.p) {
				bc := davBookReq{p: c.p, book: book, write: book == carddavContacts && c.p.Can("mail:write")}
				items, err := s.addressCards(r, bc)
				if err != nil {
					s.cardFail(w, r, err)
					return
				}
				out = append(out, props(cardBookHref(book), b, cardBookProps, s.cardBookProps(r, bc, items)))
			}
		}
		davMultistatus(w, out)
		return
	}
	items, err := s.addressCards(r, c)
	if err != nil {
		s.cardFail(w, r, err)
		return
	}
	switch {
	case name == "":
		out = append(out, props(cardBookHref(c.book), b, cardBookProps, s.cardBookProps(r, c, items)))
		if deep {
			for _, it := range items {
				out = append(out, s.cardProps(c, it, b))
			}
		}
	default:
		for _, it := range items {
			if it.name == name {
				out = append(out, s.cardProps(c, it, b))
			}
		}
		if out == nil {
			http.NotFound(w, r)
			return
		}
	}
	davMultistatus(w, out)
}

// cardReport answers addressbook-multiget (the cards asked for) and addressbook-query (all the cards: the book is
// small, a program filters them itself).
func (s *Server) cardReport(w http.ResponseWriter, r *http.Request, c davBookReq, book bool) {
	b, ok := davReadBody(w, r)
	if !ok {
		return
	}
	if !book || b.XMLName.Space != nsCard || b.XMLName.Local != "addressbook-multiget" && b.XMLName.Local != "addressbook-query" {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><d:error xmlns:d="DAV:"><d:supported-report/></d:error>`)
		return
	}
	items, err := s.addressCards(r, c)
	if err != nil {
		s.cardFail(w, r, err)
		return
	}
	var out []davResp
	if b.XMLName.Local == "addressbook-multiget" {
		byName := map[string]cardItem{}
		for _, it := range items {
			byName[it.name] = it
		}
		for _, h := range b.Hrefs {
			h = strings.TrimSpace(h)
			p := h
			if u, err := url.Parse(h); err == nil {
				p = u.Path
			}
			if it, ok := byName[p[strings.LastIndex(p, "/")+1:]]; ok {
				out = append(out, s.cardProps(c, it, b))
			} else {
				out = append(out, davResp{href: h, status: http.StatusNotFound})
			}
		}
	} else {
		for _, it := range items {
			out = append(out, s.cardProps(c, it, b))
		}
	}
	davMultistatus(w, out)
}

func (s *Server) cardGet(w http.ResponseWriter, r *http.Request, c davBookReq, name string) {
	if name == "" {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	items, err := s.addressCards(r, c)
	if err != nil {
		s.cardFail(w, r, err)
		return
	}
	for _, it := range items {
		if it.name != name {
			continue
		}
		text := it.text()
		w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
		w.Header().Set("ETag", it.etag)
		w.Header().Set("Content-Length", strconv.Itoa(len(text)))
		if r.Method != "HEAD" {
			_, _ = io.WriteString(w, text)
		}
		return
	}
	http.NotFound(w, r)
}

// ---------- the cards as files: «Экспорт в vCard» of the list and «Скачать визитку» of a profile ----------

// employeesVCF: the employees of the list in one .vcf, which Outlook, Google Contacts and phones import.
func (s *Server) employeesVCF(w http.ResponseWriter, r *http.Request, list []company.Profile) {
	var b bytes.Buffer
	for _, p := range list {
		b.WriteString(s.vcard(r, p, true))
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=employees-"+time.Now().Format(time.DateOnly)+".vcf")
	_, _ = w.Write(b.Bytes())
}

// profileVCF: «Скачать визитку» — the employee's card as a .vcf.
func (s *Server) profileVCF(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if !s.knowsPerson(r, sess.UserID, id) {
		http.NotFound(w, r)
		return
	}
	p, err := s.Company.Profile(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if tasks.IsGuestCtx(r.Context()) {
		p.Departments = nil // the structure of the company is not a guest's business
	}
	w.Header().Set("Content-Type", "text/vcard; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(p.FullName()+".vcf"))
	_, _ = io.WriteString(w, s.vcard(r, p, true))
}

// ---------- «Личные контакты» written by a phone or a program ----------

// cardMatch checks If-Match and If-None-Match of a write against the card there (etag "" — none).
func cardMatch(r *http.Request, etag string) bool {
	if m := r.Header.Get("If-Match"); m != "" && (etag == "" || m != "*" && !slices.Contains(strings.Split(strings.ReplaceAll(m, " ", ""), ","), etag)) {
		return false
	}
	return r.Header.Get("If-None-Match") != "*" || etag == ""
}

// cardThere is the version of the person's card at name ("" — none).
func (s *Server) cardThere(r *http.Request, c davBookReq, name string) (string, error) {
	old, err := s.Mail.ContactByHref(r.Context(), c.p.UserID, name)
	if errors.Is(err, mail.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return mail.CardETag(old.VCard()), nil
}

// cardPut keeps the card a program put: a new contact (201) or the changed one (204), with the card's version.
func (s *Server) cardPut(w http.ResponseWriter, r *http.Request, c davBookReq, name string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil {
		return
	}
	if len(body) > 1<<20 {
		http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
		return
	}
	etag, err := s.cardThere(r, c, name)
	if err != nil {
		s.cardFail(w, r, err)
		return
	}
	if !cardMatch(r, etag) {
		http.Error(w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
		return
	}
	got, created, err := s.Mail.PutCard(r.Context(), c.p.UserID, name, string(body))
	var ie *i18n.Error
	switch {
	case errors.As(err, &ie) && ie.Key == "err.mail_contact_limit":
		http.Error(w, s.I18n.Err(s.lang(r), ie), http.StatusInsufficientStorage)
		return
	case errors.As(err, &ie):
		http.Error(w, s.I18n.Err(s.lang(r), ie), http.StatusBadRequest)
		return
	case err != nil:
		s.cardFail(w, r, err)
		return
	}
	w.Header().Set("ETag", mail.CardETag(got.VCard()))
	if created {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

// cardDelete deletes the contact of the card.
func (s *Server) cardDelete(w http.ResponseWriter, r *http.Request, c davBookReq, name string) {
	etag, err := s.cardThere(r, c, name)
	switch {
	case err != nil:
		s.cardFail(w, r, err)
	case etag == "":
		http.NotFound(w, r)
	case !cardMatch(r, etag):
		http.Error(w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
	default:
		if err := s.Mail.DeleteCard(r.Context(), c.p.UserID, name); err != nil && !errors.Is(err, mail.ErrNotFound) {
			s.cardFail(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
