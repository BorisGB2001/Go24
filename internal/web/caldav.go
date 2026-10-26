package web

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// ---------- «Календарь» over CalDAV: two-way sync with Outlook, Thunderbird, iPhone and Mac, Android (DAVx5) ----------
//
// /caldav/ is the person (their principal and calendar home): /caldav/main/ holds everything their calendar shows,
// each of their own calendars is /caldav/{its ID}/. One signs in with an API key of go24 (the password; any user
// name) that may «calendar:read» (and «calendar:write» to change anything); /.well-known/caldav leads there.

const caldavRoot = "/caldav/"

// the namespaces of the answers and their prefixes
const (
	nsDAV  = "DAV:"
	nsCal  = "urn:ietf:params:xml:ns:caldav"
	nsCS   = "http://calendarserver.org/ns/"
	nsICal = "http://apple.com/ns/ical/"
	nsCard = "urn:ietf:params:xml:ns:carddav"
)

var davPrefixes = map[string]string{nsDAV: "d", nsCal: "c", nsCS: "cs", nsICal: "ic", nsCard: "card"}

// davBody is a PROPFIND or REPORT: the properties asked for, the meetings of a multiget, the filter of a query.
type davBody struct {
	XMLName  xml.Name
	AllProp  *struct{} `xml:"DAV: allprop"`
	PropName *struct{} `xml:"DAV: propname"`
	Prop     struct {
		Names []struct{ XMLName xml.Name } `xml:",any"`
	} `xml:"DAV: prop"`
	Hrefs  []string `xml:"DAV: href"`
	Filter struct {
		Comps []davComp `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
	} `xml:"urn:ietf:params:xml:ns:caldav filter"`
}

// davComp is a comp-filter of a calendar-query: the component and the time it is asked for.
type davComp struct {
	Name  string    `xml:"name,attr"`
	Comps []davComp `xml:"urn:ietf:params:xml:ns:caldav comp-filter"`
	Range *struct {
		Start string `xml:"start,attr"`
		End   string `xml:"end,attr"`
	} `xml:"urn:ietf:params:xml:ns:caldav time-range"`
}

// caldavReq is a request of one user to one path: the top (cal ""), a collection (main, or one's calendar's ID) or
// a meeting in it (name).
type caldavReq struct {
	s         *Server
	w         http.ResponseWriter
	r         *http.Request
	user      string
	write     bool
	cal, name string
	sections  []calendar.Section
}

// caldavURL is the address calendar programs are given ("" — none for a guest or without API keys).
func (s *Server) caldavURL(r *http.Request) string {
	if tasks.IsGuestCtx(r.Context()) || s.Keys == nil {
		return ""
	}
	if s.CallURL != "" { // iPhone and Mac want https
		return s.CallURL + caldavRoot
	}
	return s.absURL(r, caldavRoot)
}

func (s *Server) wellKnownCalDAV(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, caldavRoot, http.StatusMovedPermanently)
}

func (s *Server) caldav(w http.ResponseWriter, r *http.Request) {
	p, ok := s.keyUser(w, r, "calendar:read", "err.caldav_key")
	if !ok {
		return
	}
	q := &caldavReq{s: s, w: w, r: r.WithContext(tasks.WithViewer(r.Context(), p.UserID, false)), user: p.UserID, write: p.Can("calendar:write")}
	switch r.Method {
	case "GET", "HEAD", "OPTIONS", "PROPFIND", "REPORT":
	default:
		if !q.write {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/caldav"), "/"), "/")
	if parts[0] == "" {
		parts = nil
	}
	var err error
	if q.sections, err = s.Calendar.Sections(q.r.Context(), p.UserID); err != nil {
		q.fail(err)
		return
	}
	if len(parts) > 2 || len(parts) > 0 && q.calendarID(parts[0]) == nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) > 0 {
		q.cal = parts[0]
	}
	if len(parts) > 1 {
		q.name = parts[1]
	}
	switch r.Method {
	case "OPTIONS":
		w.Header().Set("DAV", "1, 3, calendar-access")
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT")
		w.WriteHeader(http.StatusOK)
	case "PROPFIND":
		q.propfind()
	case "REPORT":
		q.report()
	case "GET", "HEAD":
		q.get()
	case "PUT":
		q.put()
	case "DELETE":
		q.delete()
	case "PROPPATCH": // the name and colour are changed on the calendar's page
		q.proppatch()
	default:
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, REPORT")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	}
}

// calendarID is the ID of a collection's calendar ("" — the main one; nil — no such collection).
func (q *caldavReq) calendarID(cal string) *string {
	if cal == "main" {
		id := ""
		return &id
	}
	for _, x := range q.sections {
		if x.ID == cal {
			return &x.ID
		}
	}
	return nil
}

func (q *caldavReq) fail(err error) {
	var ie *i18n.Error
	switch {
	case errors.Is(err, calendar.ErrNotFound):
		http.Error(q.w, q.s.errText(q.r, err), http.StatusNotFound)
	case errors.As(err, &ie):
		http.Error(q.w, q.s.errText(q.r, err), http.StatusForbidden)
	default:
		q.s.Log.Error("caldav", "method", q.r.Method, "path", q.r.URL.Path, "err", err)
		http.Error(q.w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func (q *caldavReq) body() (davBody, bool) { return davReadBody(q.w, q.r) }

// davReadBody reads the XML of a PROPFIND or REPORT (none — all properties).
func davReadBody(w http.ResponseWriter, r *http.Request) (davBody, bool) {
	var b davBody
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return b, false
	}
	if strings.TrimSpace(string(data)) == "" {
		b.AllProp = &struct{}{}
		return b, true
	}
	if err := xml.Unmarshal(data, &b); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return b, false
	}
	return b, true
}

// davResp is one <response> of a multistatus: the properties found and those not.
type davResp struct {
	href    string
	found   []string
	missing []xml.Name
	status  int // the resource itself is not there (404)
}

// el is an element of the answer with its inner XML.
func el(ns, local, inner string) string {
	p := davPrefixes[ns]
	if inner == "" {
		return "<" + p + ":" + local + "/>"
	}
	return "<" + p + ":" + local + ">" + inner + "</" + p + ":" + local + ">"
}

func hrefEl(href string) string { return el(nsDAV, "href", html.EscapeString(href)) }

// props answers the properties asked for (all of them: those of allprop) with the values a resource has.
func props(href string, b davBody, all []string, have map[xml.Name]string) davResp {
	resp := davResp{href: href}
	if b.AllProp != nil || b.PropName != nil || len(b.Prop.Names) == 0 {
		for _, k := range all {
			for n, v := range have {
				if n.Local == k {
					resp.found = append(resp.found, v)
				}
			}
		}
		return resp
	}
	for _, n := range b.Prop.Names {
		if v, ok := have[n.XMLName]; ok {
			resp.found = append(resp.found, v)
		} else {
			resp.missing = append(resp.missing, n.XMLName)
		}
	}
	return resp
}

func (q *caldavReq) multistatus(list []davResp) { davMultistatus(q.w, list) }

// davMultistatus writes the answer of a PROPFIND or REPORT, of CalDAV and CardDAV alike.
func davMultistatus(w http.ResponseWriter, list []davResp) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<d:multistatus xmlns:d="DAV:" xmlns:c="` + nsCal + `" xmlns:cs="` + nsCS + `" xmlns:ic="` + nsICal + `" xmlns:card="` + nsCard + `">`)
	for _, r := range list {
		b.WriteString("<d:response>" + hrefEl(r.href))
		if r.status != 0 {
			b.WriteString(el(nsDAV, "status", fmt.Sprintf("HTTP/1.1 %d %s", r.status, http.StatusText(r.status))) + "</d:response>")
			continue
		}
		if len(r.found) > 0 || len(r.missing) == 0 {
			b.WriteString("<d:propstat><d:prop>" + strings.Join(r.found, "") + "</d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>")
		}
		if len(r.missing) > 0 {
			b.WriteString("<d:propstat><d:prop>")
			for _, n := range r.missing {
				if p, ok := davPrefixes[n.Space]; ok {
					b.WriteString("<" + p + ":" + n.Local + "/>")
				} else {
					b.WriteString(`<x:` + n.Local + ` xmlns:x="` + html.EscapeString(n.Space) + `"/>`)
				}
			}
			b.WriteString("</d:prop><d:status>HTTP/1.1 404 Not Found</d:status></d:propstat>")
		}
		b.WriteString("</d:response>")
	}
	b.WriteString("</d:multistatus>")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(w, b.String())
}

// the properties allprop answers, by the kind of resource
var (
	davTopProps  = []string{"resourcetype", "displayname", "current-user-principal", "principal-URL", "calendar-home-set", "owner"}
	davCalProps  = []string{"resourcetype", "displayname", "getctag", "calendar-color", "supported-calendar-component-set", "owner"}
	davItemProps = []string{"resourcetype", "getetag", "getcontenttype", "getcontentlength"}
)

// common: what every resource tells of the person.
func (q *caldavReq) common() map[xml.Name]string {
	me := hrefEl(caldavRoot)
	return map[xml.Name]string{
		{Space: nsDAV, Local: "current-user-principal"}:   el(nsDAV, "current-user-principal", me),
		{Space: nsDAV, Local: "principal-URL"}:            el(nsDAV, "principal-URL", me),
		{Space: nsDAV, Local: "owner"}:                    el(nsDAV, "owner", me),
		{Space: nsDAV, Local: "principal-collection-set"}: el(nsDAV, "principal-collection-set", me),
		{Space: nsCal, Local: "calendar-home-set"}:        el(nsCal, "calendar-home-set", me),
	}
}

func (q *caldavReq) topProps() (map[xml.Name]string, error) {
	var name, email string
	if err := q.s.DB.QueryRow(q.r.Context(), `SELECT name, email FROM users WHERE id = $1`, q.user).Scan(&name, &email); err != nil {
		return nil, err
	}
	have := q.common()
	have[xml.Name{Space: nsDAV, Local: "resourcetype"}] = el(nsDAV, "resourcetype", el(nsDAV, "collection", "")+el(nsDAV, "principal", ""))
	have[xml.Name{Space: nsDAV, Local: "displayname"}] = el(nsDAV, "displayname", html.EscapeString(name))
	addr := hrefEl(caldavRoot)
	if email != "" {
		addr = hrefEl("mailto:"+email) + addr
	}
	have[xml.Name{Space: nsCal, Local: "calendar-user-address-set"}] = el(nsCal, "calendar-user-address-set", addr)
	have[xml.Name{Space: nsDAV, Local: "current-user-privilege-set"}] = el(nsDAV, "current-user-privilege-set", el(nsDAV, "privilege", el(nsDAV, "read", "")))
	return have, nil
}

// calProps are the properties of a collection with these meetings.
func (q *caldavReq) calProps(cal string, items []calendar.DavItem, order int) (map[xml.Name]string, error) {
	name, color := q.s.tr(q.r, "calendar.cals.personal"), ""
	if cal == "main" {
		c, err := q.s.Calendar.MainColor(q.r.Context(), q.user)
		if err != nil {
			return nil, err
		}
		color = c
	}
	for _, x := range q.sections {
		if x.ID == cal {
			name, color = x.Name, x.Color
		}
	}
	if color == "" {
		color = calendar.Colors[1]
	}
	privs := el(nsDAV, "privilege", el(nsDAV, "read", ""))
	if q.write {
		for _, p := range []string{"write", "write-content", "bind", "unbind"} {
			privs += el(nsDAV, "privilege", el(nsDAV, p, ""))
		}
	}
	report := func(n string) string { return el(nsDAV, "supported-report", el(nsDAV, "report", el(nsCal, n, ""))) }
	have := q.common()
	for n, v := range map[string]string{
		"resourcetype":               el(nsDAV, "resourcetype", el(nsDAV, "collection", "")+el(nsCal, "calendar", "")),
		"displayname":                el(nsDAV, "displayname", html.EscapeString(name)),
		"current-user-privilege-set": el(nsDAV, "current-user-privilege-set", privs),
		"supported-report-set":       el(nsDAV, "supported-report-set", report("calendar-multiget")+report("calendar-query")),
	} {
		have[xml.Name{Space: nsDAV, Local: n}] = v
	}
	have[xml.Name{Space: nsCS, Local: "getctag"}] = el(nsCS, "getctag", calendar.DavCTag(items))
	have[xml.Name{Space: nsICal, Local: "calendar-color"}] = el(nsICal, "calendar-color", strings.ToUpper(color)+"FF")
	have[xml.Name{Space: nsICal, Local: "calendar-order"}] = el(nsICal, "calendar-order", strconv.Itoa(order))
	have[xml.Name{Space: nsCal, Local: "supported-calendar-component-set"}] = el(nsCal, "supported-calendar-component-set", `<c:comp name="VEVENT"/>`)
	have[xml.Name{Space: nsCal, Local: "supported-calendar-data"}] = el(nsCal, "supported-calendar-data", `<c:calendar-data content-type="text/calendar" version="2.0"/>`)
	return have, nil
}

// itemProps are the properties of a meeting in its collection.
func (q *caldavReq) itemProps(it calendar.DavItem) map[xml.Name]string {
	have := q.common()
	have[xml.Name{Space: nsDAV, Local: "resourcetype"}] = el(nsDAV, "resourcetype", "")
	have[xml.Name{Space: nsDAV, Local: "getetag"}] = el(nsDAV, "getetag", html.EscapeString(it.ETag))
	have[xml.Name{Space: nsDAV, Local: "getcontenttype"}] = el(nsDAV, "getcontenttype", "text/calendar; charset=utf-8; component=vevent")
	have[xml.Name{Space: nsDAV, Local: "getcontentlength"}] = el(nsDAV, "getcontentlength", strconv.Itoa(len(it.ICS)))
	have[xml.Name{Space: nsCal, Local: "calendar-data"}] = el(nsCal, "calendar-data", html.EscapeString(it.ICS))
	return have
}

func calHref(cal string) string { return caldavRoot + cal + "/" }

func itemHref(cal, name string) string { return calHref(cal) + url.PathEscape(name) }

// items are the meetings of the request's collection.
func (q *caldavReq) items() ([]calendar.DavItem, error) {
	return q.s.Calendar.DavResources(q.r.Context(), q.user, *q.calendarID(q.cal))
}

func (q *caldavReq) propfind() {
	b, ok := q.body()
	if !ok {
		return
	}
	deep := q.r.Header.Get("Depth") != "0"
	var out []davResp
	switch {
	case q.cal == "":
		have, err := q.topProps()
		if err != nil {
			q.fail(err)
			return
		}
		out = append(out, props(caldavRoot, b, davTopProps, have))
		if deep {
			for i, cal := range q.collections() {
				q.cal = cal
				items, err := q.items()
				if err != nil {
					q.fail(err)
					return
				}
				have, err := q.calProps(cal, items, i)
				if err != nil {
					q.fail(err)
					return
				}
				out = append(out, props(calHref(cal), b, davCalProps, have))
			}
		}
	case q.name == "":
		items, err := q.items()
		if err != nil {
			q.fail(err)
			return
		}
		have, err := q.calProps(q.cal, items, 0)
		if err != nil {
			q.fail(err)
			return
		}
		out = append(out, props(calHref(q.cal), b, davCalProps, have))
		if deep {
			for _, it := range items {
				out = append(out, props(itemHref(q.cal, it.Name), b, davItemProps, q.itemProps(it)))
			}
		}
	default:
		it, err := q.s.Calendar.DavItemOf(q.r.Context(), q.user, *q.calendarID(q.cal), q.name)
		if err == nil && it == nil {
			err = calendar.ErrNotFound
		}
		if err != nil {
			q.fail(err)
			return
		}
		out = append(out, props(itemHref(q.cal, it.Name), b, davItemProps, q.itemProps(*it)))
	}
	q.multistatus(out)
}

// collections are the person's calendars: the main one and their own.
func (q *caldavReq) collections() []string {
	out := []string{"main"}
	for _, x := range q.sections {
		out = append(out, x.ID)
	}
	return out
}

func (q *caldavReq) report() {
	b, ok := q.body()
	if !ok {
		return
	}
	if q.cal == "" || q.name != "" || b.XMLName.Space != nsCal || b.XMLName.Local != "calendar-multiget" && b.XMLName.Local != "calendar-query" {
		q.w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		q.w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(q.w, `<?xml version="1.0" encoding="utf-8"?><d:error xmlns:d="DAV:"><d:supported-report/></d:error>`)
		return
	}
	items, err := q.items()
	if err != nil {
		q.fail(err)
		return
	}
	var out []davResp
	if b.XMLName.Local == "calendar-multiget" {
		byName := map[string]calendar.DavItem{}
		for _, it := range items {
			byName[it.Name] = it
		}
		for _, h := range b.Hrefs {
			h = strings.TrimSpace(h)
			p := h
			if u, err := url.Parse(h); err == nil {
				p = u.Path
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if it, ok := byName[name]; ok {
				out = append(out, props(itemHref(q.cal, name), b, davItemProps, q.itemProps(it)))
			} else {
				out = append(out, davResp{href: h, status: http.StatusNotFound})
			}
		}
	} else {
		from, to, events := davRange(b.Filter.Comps)
		for _, it := range items {
			if events && overlaps(it.Event, from, to) {
				out = append(out, props(itemHref(q.cal, it.Name), b, davItemProps, q.itemProps(it)))
			}
		}
	}
	q.multistatus(out)
}

// davRange is the time a calendar-query asks for (zero — any) and whether it asks for meetings at all (not tasks).
func davRange(comps []davComp) (from, to time.Time, events bool) {
	if len(comps) == 0 {
		return from, to, true
	}
	for _, c := range comps {
		if !strings.EqualFold(c.Name, "VCALENDAR") {
			continue
		}
		if len(c.Comps) == 0 {
			return from, to, true
		}
		for _, x := range c.Comps {
			if !strings.EqualFold(x.Name, "VEVENT") {
				continue
			}
			if x.Range != nil {
				from, _ = time.Parse("20060102T150405Z", x.Range.Start)
				to, _ = time.Parse("20060102T150405Z", x.Range.End)
			}
			return from, to, true
		}
	}
	return from, to, false
}

// overlaps: the meeting (a series — any of its occurrences) takes some of the time between from and to.
func overlaps(e calendar.Event, from, to time.Time) bool {
	if !to.IsZero() && !e.Start.Before(to) {
		return false
	}
	if from.IsZero() {
		return true
	}
	if e.Repeat != "" {
		return e.Until == nil || !e.Until.AddDate(0, 0, 1).Before(from)
	}
	return e.End.After(from) || e.End.Equal(e.Start) && !e.Start.Before(from)
}

// item is the meeting the request names (nil and a written answer — none).
func (q *caldavReq) item() *calendar.DavItem {
	if q.name == "" {
		http.Error(q.w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return nil
	}
	it, err := q.s.Calendar.DavItemOf(q.r.Context(), q.user, *q.calendarID(q.cal), q.name)
	if err == nil && it == nil {
		err = calendar.ErrNotFound
	}
	if err != nil {
		q.fail(err)
		return nil
	}
	return it
}

func (q *caldavReq) get() {
	it := q.item()
	if it == nil {
		return
	}
	q.w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	q.w.Header().Set("ETag", it.ETag)
	q.w.Header().Set("Content-Length", strconv.Itoa(len(it.ICS)))
	if q.r.Method != "HEAD" {
		_, _ = io.WriteString(q.w, it.ICS)
	}
}

// matches: the precondition If-Match of a change holds for the meeting as it is (nil — none yet).
func (q *caldavReq) matches(it *calendar.DavItem) bool {
	if m := q.r.Header.Get("If-Match"); m != "" && (it == nil || m != "*" && !strings.Contains(m, it.ETag)) {
		return false
	}
	return q.r.Header.Get("If-None-Match") != "*" || it == nil
}

func (q *caldavReq) put() {
	if q.name == "" {
		http.Error(q.w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	ctx, cal := q.r.Context(), *q.calendarID(q.cal)
	it, err := q.s.Calendar.DavItemOf(ctx, q.user, cal, q.name)
	if err != nil {
		q.fail(err)
		return
	}
	if !q.matches(it) {
		http.Error(q.w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
		return
	}
	text, err := io.ReadAll(io.LimitReader(q.r.Body, maxImportICS+1))
	if err != nil || len(text) > maxImportICS {
		http.Error(q.w, q.s.tr(q.r, "err.file_too_big"), http.StatusRequestEntityTooLarge)
		return
	}
	master, overrides := davObject(string(text))
	if master == nil { // a changed occurrence alone: of a meeting go24 does not have
		http.Error(q.w, q.s.tr(q.r, "err.ics_empty"), http.StatusForbidden)
		return
	}
	_, created, err := q.s.Calendar.DavPut(ctx, q.user, cal, q.name, *master, overrides)
	if err != nil {
		q.fail(err)
		return
	}
	// no ETag: go24 keeps the meeting in its own words, the program reads it again
	if created {
		q.w.WriteHeader(http.StatusCreated)
	} else {
		q.w.WriteHeader(http.StatusNoContent)
	}
}

// davObject is the meeting of a calendar object resource and its changed occurrences (nil — none: a task, a journal).
func davObject(text string) (*calendar.Imported, []calendar.Override) {
	var master *calendar.Imported
	var overrides []calendar.Override
	for _, in := range mail.ParseResource(text) {
		switch {
		case in.RecurrenceID == nil && master == nil:
			master = &calendar.Imported{UID: in.UID, Event: eventOfInvite(in)}
		case in.RecurrenceID != nil:
			overrides = append(overrides, calendar.Override{At: *in.RecurrenceID, Cancelled: in.Cancelled, Event: eventOfInvite(in)})
		}
	}
	return master, overrides
}

func (q *caldavReq) delete() {
	it := q.item()
	if it == nil {
		return
	}
	if !q.matches(it) {
		http.Error(q.w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
		return
	}
	if err := q.s.Calendar.DavDelete(q.r.Context(), q.user, *q.calendarID(q.cal), q.name); err != nil {
		q.fail(err)
		return
	}
	q.w.WriteHeader(http.StatusNoContent)
}

// proppatch: nothing is changed by a program — every property asked is answered 403.
func (q *caldavReq) proppatch() {
	var b struct {
		Sets []struct {
			Names []struct{ XMLName xml.Name } `xml:",any"`
		} `xml:"DAV: set>prop"`
		Removes []struct {
			Names []struct{ XMLName xml.Name } `xml:",any"`
		} `xml:"DAV: remove>prop"`
	}
	data, _ := io.ReadAll(io.LimitReader(q.r.Body, 1<<20))
	_ = xml.Unmarshal(data, &b)
	var names []xml.Name
	for _, s := range append(b.Sets, b.Removes...) {
		for _, n := range s.Names {
			names = append(names, n.XMLName)
		}
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n" + `<d:multistatus xmlns:d="DAV:"><d:response>` + hrefEl(q.r.URL.Path) + `<d:propstat><d:prop>`)
	for _, n := range names {
		sb.WriteString(`<x:` + n.Local + ` xmlns:x="` + html.EscapeString(n.Space) + `"/>`)
	}
	sb.WriteString(`</d:prop><d:status>HTTP/1.1 403 Forbidden</d:status></d:propstat></d:response></d:multistatus>`)
	q.w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	q.w.WriteHeader(http.StatusMultiStatus)
	_, _ = io.WriteString(q.w, sb.String())
}
