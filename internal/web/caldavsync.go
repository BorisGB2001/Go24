package web

import (
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
)

// A calendar of a CalDAV server connected two ways (see calendar.DavResources): «Подключить CalDAV» on the page
// «Синхронизация» — the server's address (Яндекс, iCloud, Nextcloud, any other), the login and the password of an
// application; each calendar of meetings of the account becomes one of the person's calendars. go24-worker syncs them
// every five minutes, and within a minute after a meeting of one was changed in go24.

// davClient talks to one CalDAV account.
type davClient struct {
	login, password string
	hc              *http.Client
}

// davHTTP reaches CalDAV servers of the internet only, its redirects followed by do (tests put their own).
var davHTTP = &http.Client{Timeout: 60 * time.Second, Transport: linkClient.Transport,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

const maxDavAnswer = 20 << 20 // the largest answer of a CalDAV server read

// do sends a request with the account's password, following the server's redirects (iCloud sends one to the
// person's own server) with it.
func (c *davClient) do(ctx context.Context, method, target string, hdr map[string]string, body string) (*http.Response, []byte, error) {
	for range 5 {
		req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
		if err != nil {
			return nil, nil, i18n.E("err.calendar_dav_url")
		}
		req.SetBasicAuth(c.login, c.password)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := c.hc.Do(req)
		if err != nil {
			return nil, nil, i18n.E("err.calendar_dav_load", err.Error())
		}
		data, err := io.ReadAll(io.LimitReader(res.Body, maxDavAnswer))
		res.Body.Close()
		if err != nil {
			return nil, nil, i18n.E("err.calendar_dav_load", err.Error())
		}
		switch res.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			loc, err := req.URL.Parse(res.Header.Get("Location"))
			if err != nil || loc.Scheme != "https" && loc.Scheme != req.URL.Scheme {
				return nil, nil, i18n.E("err.calendar_dav_load", res.Status)
			}
			target = loc.String()
			continue
		case http.StatusUnauthorized:
			return nil, nil, i18n.E("err.calendar_dav_auth")
		}
		res.Request = req
		return res, data, nil
	}
	return nil, nil, i18n.E("err.calendar_dav_load", "redirects")
}

// davProps are the properties of a resource a multistatus answers (those found).
type davProps struct {
	Collection *struct{} `xml:"resourcetype>collection"`
	Calendar   *struct{} `xml:"resourcetype>calendar"`
	Name       string    `xml:"displayname"`
	ETag       string    `xml:"getetag"`
	Principal  string    `xml:"current-user-principal>href"`
	Home       string    `xml:"calendar-home-set>href"`
	Comps      []struct {
		Name string `xml:"name,attr"`
	} `xml:"supported-calendar-component-set>comp"`
	Color string `xml:"calendar-color"`
	Data  string `xml:"calendar-data"`
}

type davFound struct {
	URL   string // absolute
	Props davProps
}

// multistatus asks target for props (a PROPFIND of depth, or the REPORT body) and reads the answer.
func (c *davClient) multistatus(ctx context.Context, method, target, depth, body string) ([]davFound, error) {
	res, data, err := c.do(ctx, method, target, map[string]string{"Depth": depth, "Content-Type": "application/xml; charset=utf-8"},
		`<?xml version="1.0" encoding="utf-8"?>`+body)
	if err != nil {
		return nil, err
	}
	switch {
	case res.StatusCode == http.StatusForbidden && method == "PROPFIND": // the account may not see it: not its login
		return nil, i18n.E("err.calendar_dav_auth")
	case res.StatusCode != http.StatusMultiStatus:
		return nil, i18n.E("err.calendar_dav_load", res.Status)
	}
	var ms struct {
		Responses []struct {
			Href      string `xml:"href"`
			Propstats []struct {
				Prop   davProps `xml:"prop"`
				Status string   `xml:"status"`
			} `xml:"propstat"`
		} `xml:"response"`
	}
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, i18n.E("err.calendar_dav_load", err.Error())
	}
	var out []davFound
	for _, r := range ms.Responses {
		u, err := res.Request.URL.Parse(strings.TrimSpace(r.Href))
		if err != nil {
			continue
		}
		f := davFound{URL: u.String()}
		for _, ps := range r.Propstats {
			if ps.Status != "" && !strings.Contains(ps.Status, " 200") {
				continue
			}
			p := ps.Prop
			f.Props.Collection, f.Props.Calendar = cmp.Or(f.Props.Collection, p.Collection), cmp.Or(f.Props.Calendar, p.Calendar)
			f.Props.Name, f.Props.ETag = cmp.Or(f.Props.Name, strings.TrimSpace(p.Name)), cmp.Or(f.Props.ETag, strings.TrimSpace(p.ETag))
			f.Props.Principal, f.Props.Home = cmp.Or(f.Props.Principal, strings.TrimSpace(p.Principal)), cmp.Or(f.Props.Home, strings.TrimSpace(p.Home))
			f.Props.Color, f.Props.Data = cmp.Or(f.Props.Color, strings.TrimSpace(p.Color)), cmp.Or(f.Props.Data, strings.TrimSpace(p.Data))
			f.Props.Comps = append(f.Props.Comps, p.Comps...)
		}
		out = append(out, f)
	}
	return out, nil
}

const davFindProps = `<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ic="http://apple.com/ns/ical/"><d:prop>
<d:resourcetype/><d:displayname/><d:current-user-principal/><c:calendar-home-set/><c:supported-calendar-component-set/>
<ic:calendar-color/></d:prop></d:propfind>`

// davCal is a calendar of meetings of the account.
type davCal struct{ URL, Name, Color string }

// discover finds the calendars of meetings of the account at raw: a calendar's own address, or the server's (its
// principal and calendar home are found as RFC 6764 tells).
func (c *davClient) discover(ctx context.Context, raw string) ([]davCal, error) {
	raw = strings.TrimSpace(raw)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || len(raw) > 2000 {
		return nil, i18n.E("err.calendar_dav_url")
	}
	u.Fragment = ""
	var found []davFound
	if u.Path == "" || u.Path == "/" { // the server's name: its well-known address, else its top (Yandex has none)
		u.Path = "/.well-known/caldav"
		if found, err = c.multistatus(ctx, "PROPFIND", u.String(), "0", davFindProps); err != nil && !isKey(err, "err.calendar_dav_auth") {
			u.Path = "/"
			found, err = c.multistatus(ctx, "PROPFIND", u.String(), "0", davFindProps)
		}
	} else {
		found, err = c.multistatus(ctx, "PROPFIND", u.String(), "0", davFindProps)
	}
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, i18n.E("err.calendar_dav_none")
	}
	if f := found[0]; f.Props.Calendar != nil {
		return calsOf(found[:1]), nil
	}
	home := found[0].Props.Home
	if home == "" {
		p := found[0].Props.Principal
		if p == "" {
			return nil, i18n.E("err.calendar_dav_none")
		}
		pu, err := url.Parse(found[0].URL)
		if err != nil {
			return nil, i18n.E("err.calendar_dav_none")
		}
		if pu, err = pu.Parse(p); err != nil {
			return nil, i18n.E("err.calendar_dav_none")
		}
		pf, err := c.multistatus(ctx, "PROPFIND", pu.String(), "0", davFindProps)
		if err != nil {
			return nil, err
		}
		if len(pf) == 0 || pf[0].Props.Home == "" {
			return nil, i18n.E("err.calendar_dav_none")
		}
		found, home = pf, pf[0].Props.Home
	}
	hu, err := url.Parse(found[0].URL)
	if err == nil {
		hu, err = hu.Parse(home)
	}
	if err != nil {
		return nil, i18n.E("err.calendar_dav_none")
	}
	list, err := c.multistatus(ctx, "PROPFIND", hu.String(), "1", davFindProps)
	if err != nil {
		return nil, err
	}
	cals := calsOf(list)
	if len(cals) == 0 {
		return nil, i18n.E("err.calendar_dav_none")
	}
	return cals, nil
}

func isKey(err error, key string) bool {
	var ie *i18n.Error
	return errors.As(err, &ie) && ie.Key == key
}

// calsOf are the calendars of meetings among found (one without a set of components holds everything).
func calsOf(found []davFound) []davCal {
	var out []davCal
	for _, f := range found {
		if f.Props.Calendar == nil {
			continue
		}
		events := len(f.Props.Comps) == 0
		for _, c := range f.Props.Comps {
			events = events || strings.EqualFold(c.Name, "VEVENT")
		}
		if !events {
			continue
		}
		name := f.Props.Name
		if name == "" {
			name = path.Base(strings.TrimSuffix(f.URL, "/"))
			if n, err := url.PathUnescape(name); err == nil {
				name = n
			}
		}
		color := strings.ToLower(f.Props.Color)
		if len(color) == 9 { // #rrggbbaa of Apple
			color = color[:7]
		}
		out = append(out, davCal{URL: f.URL, Name: name, Color: color})
	}
	return out
}

// davRemote is a resource of the server's collection: its address and tag.
type davRemote struct{ URL, ETag string }

// resources are the resources of the collection by name (its last segment of the path).
func (c *davClient) resources(ctx context.Context, coll string) (map[string]davRemote, error) {
	found, err := c.multistatus(ctx, "PROPFIND", coll, "1",
		`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getetag/></d:prop></d:propfind>`)
	if err != nil {
		return nil, err
	}
	out := map[string]davRemote{}
	for _, f := range found {
		if f.Props.Collection != nil || strings.HasSuffix(f.URL, "/") {
			continue
		}
		out[davResName(f.URL)] = davRemote{URL: f.URL, ETag: f.Props.ETag}
	}
	return out, nil
}

// davResName is the name of a resource by its address.
func davResName(u string) string {
	p := u
	if x, err := url.Parse(u); err == nil {
		p = x.EscapedPath()
	}
	name := path.Base(p)
	if n, err := url.PathUnescape(name); err == nil {
		return n
	}
	return name
}

// fetch loads the calendar files of the resources (a calendar-multiget of 50 at a time; a server without it — one by
// one), by name.
func (c *davClient) fetch(ctx context.Context, coll string, list []davRemote) (map[string]davRemote, map[string]string, error) {
	files, tags := map[string]string{}, map[string]davRemote{}
	for i := 0; i < len(list); i += 50 {
		part := list[i:min(i+50, len(list))]
		var b strings.Builder
		b.WriteString(`<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><c:calendar-data/></d:prop>`)
		for _, r := range part {
			u, _ := url.Parse(r.URL)
			b.WriteString("<d:href>" + xmlText(u.EscapedPath()) + "</d:href>")
		}
		b.WriteString(`</c:calendar-multiget>`)
		found, err := c.multistatus(ctx, "REPORT", coll, "1", b.String())
		if err == nil {
			for _, f := range found {
				if f.Props.Data != "" {
					files[davResName(f.URL)] = f.Props.Data
					tags[davResName(f.URL)] = davRemote{URL: f.URL, ETag: f.Props.ETag}
				}
			}
			continue
		}
		for _, r := range part {
			res, data, err := c.do(ctx, http.MethodGet, r.URL, map[string]string{"Accept": "text/calendar"}, "")
			if err != nil {
				return nil, nil, err
			}
			if res.StatusCode == http.StatusOK {
				files[davResName(r.URL)] = string(data)
				tags[davResName(r.URL)] = davRemote{URL: r.URL, ETag: cmp.Or(res.Header.Get("ETag"), strings.TrimSpace(r.ETag))}
			}
		}
	}
	return tags, files, nil
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// errDavChanged: the resource was changed on the server since (If-Match failed).
var errDavChanged = errors.New("caldav: changed on the server")

// put writes the calendar file of a meeting to the server (etag "" — a new resource); returns its new tag ("" — the
// server did not tell it).
func (c *davClient) put(ctx context.Context, target, etag, ics string) (string, error) {
	hdr := map[string]string{"Content-Type": "text/calendar; charset=utf-8"}
	if etag != "" {
		hdr["If-Match"] = etag
	} else {
		hdr["If-None-Match"] = "*"
	}
	ics = strings.Replace(ics, "METHOD:PUBLISH\r\n", "", 1) // a resource of a collection has no METHOD (RFC 4791 4.1)
	res, _, err := c.do(ctx, http.MethodPut, target, hdr, ics)
	if err != nil {
		return "", err
	}
	switch {
	case res.StatusCode == http.StatusPreconditionFailed:
		return "", errDavChanged
	case res.StatusCode/100 != 2:
		return "", i18n.E("err.calendar_dav_put", res.Status)
	}
	return res.Header.Get("ETag"), nil
}

// remove deletes the resource from the server; one gone already is fine.
func (c *davClient) remove(ctx context.Context, target, etag string) error {
	hdr := map[string]string{}
	if etag != "" {
		hdr["If-Match"] = etag
	}
	res, _, err := c.do(ctx, http.MethodDelete, target, hdr, "")
	if err != nil {
		return err
	}
	switch {
	case res.StatusCode == http.StatusPreconditionFailed:
		return errDavChanged
	case res.StatusCode/100 != 2 && res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusGone:
		return i18n.E("err.calendar_dav_put", res.Status)
	}
	return nil
}

func (s *Server) davClientOf(a calendar.DavAccount) *davClient {
	return &davClient{login: a.Login, password: a.Password, hc: davHTTP}
}

// syncDav makes the connected calendar x and the server's collection the same: what changed on the server since the
// last sync comes into go24 (the server's version wins when both changed), what changed in go24 goes to the server.
// It tells how many meetings there are; a failure is kept on x.
func (s *Server) syncDav(ctx context.Context, x calendar.Section) (int, error) {
	n, err := s.syncDavOnce(ctx, x)
	if err != nil && ctx.Err() == nil {
		why := s.I18n.Err(s.portalLang(), err)
		if ferr := s.Calendar.DavSynced(ctx, x.ID, why); ferr != nil {
			return 0, ferr
		}
	}
	return n, err
}

func (s *Server) syncDavOnce(ctx context.Context, x calendar.Section) (int, error) {
	acc, err := s.Calendar.DavAccountOf(ctx, x.ID)
	if err != nil {
		return 0, err
	}
	if err := s.Calendar.DavBegin(ctx, x.ID); err != nil {
		return 0, err
	}
	c := s.davClientOf(acc)
	remote, err := c.resources(ctx, acc.URL)
	if err != nil {
		return 0, err
	}
	states, err := s.Calendar.DavStates(ctx, x.ID)
	if err != nil {
		return 0, err
	}
	// the server's: new and changed resources are loaded and saved in go24
	var changed []davRemote
	for name, r := range remote {
		if st, ok := states[name]; !ok || st.ETag != r.ETag || r.ETag == "" {
			changed = append(changed, r)
		}
	}
	tags, files, err := c.fetch(ctx, acc.URL, changed)
	if err != nil {
		return 0, err
	}
	var failed []string // the server's meetings go24 could not save
	pulled := map[string]string{}
	for name, ics := range files {
		master, overrides := davObject(ics)
		if master == nil { // a task, a journal: go24 does not take it
			if err := s.Calendar.DavKeep(ctx, x.ID, name, calendar.DavState{ETag: tags[name].ETag}); err != nil {
				return 0, err
			}
			continue
		}
		if _, _, err := s.Calendar.DavPut(ctx, x.OwnerID, x.ID, name, *master, overrides); err != nil {
			if ctx.Err() != nil {
				return 0, err
			}
			failed = append(failed, master.Event.Title)
			continue
		}
		pulled[name] = tags[name].ETag
	}
	// gone from the server: gone from go24 too
	for name, st := range states {
		if _, ok := remote[name]; ok {
			continue
		}
		if st.Tag != "" {
			if err := s.Calendar.DavDelete(ctx, x.OwnerID, x.ID, name); err != nil && !errors.Is(err, calendar.ErrNotFound) {
				return 0, err
			}
		}
		if err := s.Calendar.DavForget(ctx, x.ID, name); err != nil {
			return 0, err
		}
	}
	local, err := s.Calendar.DavResources(ctx, x.OwnerID, x.ID)
	if err != nil {
		return 0, err
	}
	mine := map[string]calendar.DavItem{}
	for _, it := range local {
		mine[it.Name] = it
	}
	for name, etag := range pulled {
		if err := s.Calendar.DavKeep(ctx, x.ID, name, calendar.DavState{ETag: etag, Tag: mine[name].ETag}); err != nil {
			return 0, err
		}
	}
	// go24's: changed and deleted meetings go to the server, new ones are put there
	coll := strings.TrimSuffix(acc.URL, "/") + "/"
	for name, r := range remote {
		st, ok := states[name]
		if _, now := pulled[name]; now || !ok || st.Tag == "" || st.ETag != r.ETag {
			continue
		}
		it, here := mine[name]
		switch {
		case !here:
			err = c.remove(ctx, r.URL, r.ETag)
			if err == nil {
				err = s.Calendar.DavForget(ctx, x.ID, name)
			}
		case it.ETag != st.Tag:
			var etag string
			if etag, err = c.put(ctx, r.URL, r.ETag, it.ICS); err == nil {
				err = s.Calendar.DavKeep(ctx, x.ID, name, calendar.DavState{ETag: etag, Tag: it.ETag})
			}
		}
		if errors.Is(err, errDavChanged) { // changed on the server meanwhile: the next sync loads it
			err = s.Calendar.DavKeep(ctx, x.ID, name, calendar.DavState{Tag: st.Tag})
		}
		if err != nil {
			return 0, err
		}
	}
	for name, it := range mine {
		if _, ok := remote[name]; ok {
			continue
		}
		if _, ok := states[name]; ok { // deleted on the server just now
			continue
		}
		etag, err := c.put(ctx, coll+url.PathEscape(name), "", it.ICS)
		if errors.Is(err, errDavChanged) { // the server has one of the name already: the next sync loads it
			continue
		}
		if err != nil {
			return 0, err
		}
		if err := s.Calendar.DavKeep(ctx, x.ID, name, calendar.DavState{ETag: etag, Tag: it.ETag}); err != nil {
			return 0, err
		}
	}
	why := ""
	if len(failed) > 0 {
		why = s.I18n.T(s.portalLang(), "calsync.dav_skipped", len(failed), strings.Join(failed[:min(len(failed), 3)], ", "))
	}
	if err := s.Calendar.DavSynced(ctx, x.ID, why); err != nil {
		return 0, err
	}
	return len(mine), nil
}

// SyncCalendarDav is go24-worker's round of the connected CalDAV calendars due.
func (s *Server) SyncCalendarDav(ctx context.Context, now time.Time) error {
	list, err := s.Calendar.DavDue(ctx, now)
	if err != nil {
		return err
	}
	for _, x := range list {
		if _, err := s.syncDav(ctx, x); err != nil && ctx.Err() == nil {
			s.Log.Info("calendar caldav", "calendar", x.ID, "err", err)
		}
	}
	return ctx.Err()
}

// connectDav connects the calendars of meetings of the account at raw (all of them, or the one of the address
// given) and syncs them at once; it tells the names of those connected.
func (s *Server) connectDav(ctx context.Context, viewer, raw, login, password string) ([]calendar.Section, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return nil, i18n.E("err.calendar_dav_auth")
	}
	c := &davClient{login: login, password: password, hc: davHTTP}
	cals, err := c.discover(ctx, raw)
	if err != nil {
		return nil, err
	}
	var out []calendar.Section
	for _, dc := range cals {
		id, err := s.Calendar.AddDav(ctx, viewer, dc.Name, dc.Color, calendar.DavAccount{URL: dc.URL, Login: login, Password: password})
		if err != nil {
			if len(cals) > 1 && isKey(err, "err.calendar_dav_dup") {
				continue // connected before
			}
			return out, err
		}
		x, err := s.Calendar.Dav(ctx, viewer, id)
		if err != nil {
			return out, err
		}
		_, _ = s.syncDav(ctx, x) // a failure is seen on the page
		if x, err = s.Calendar.Dav(ctx, viewer, id); err != nil {
			return out, err
		}
		out = append(out, x)
	}
	if len(out) == 0 {
		return nil, i18n.E("err.calendar_dav_dup")
	}
	return out, nil
}

// calendarDavAdd: «Подключить» of «Календарь CalDAV».
func (s *Server) calendarDavAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.connectDav(r.Context(), sess.UserID, r.FormValue("url"), r.FormValue("login"), r.FormValue("password"))
	if err != nil && len(list) == 0 {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	names := make([]string, len(list))
	for i, x := range list {
		names[i] = x.Name
	}
	msg := s.tr(r, "calsync.dav_connected", strings.Join(names, ", "))
	http.Redirect(w, r, "/calendar/sync?msg="+url.QueryEscape(msg)+"#dav", http.StatusSeeOther)
}

// calendarDavSync: «Синхронизировать сейчас».
func (s *Server) calendarDavSync(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.Calendar.Dav(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	msg := ""
	if n, err := s.syncDav(r.Context(), x); err != nil {
		msg = s.tr(r, "calsync.link_failed", x.Name) + ": " + s.errText(r, err)
	} else {
		msg = s.tr(r, "calsync.dav_synced_now", x.Name, n)
	}
	http.Redirect(w, r, "/calendar/sync?msg="+url.QueryEscape(msg)+"#dav", http.StatusSeeOther)
}

// calendarDavPassword: a new login and password of the account (the old one stopped working).
func (s *Server) calendarDavPassword(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if r.FormValue("login") == "" || r.FormValue("password") == "" {
		s.taskErr(w, r, i18n.E("err.calendar_dav_auth"), "/calendar/sync")
		return
	}
	if err := s.Calendar.SetDavPassword(r.Context(), sess.UserID, id, r.FormValue("login"), r.FormValue("password")); err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	s.calendarDavSync(w, r, sess)
}

// apiCalDavAdd connects the calendars of a CalDAV account ({"url", "login", "password"}) and syncs them: the
// calendars as /calendar/sections shows them.
func (s *Server) apiCalDavAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		URL      string `json:"url"`
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	list, err := s.connectDav(r.Context(), p.UserID, in.URL, in.Login, in.Password)
	if err != nil && len(list) == 0 {
		return err
	}
	out := make([]apiSection, len(list))
	for i, x := range list {
		out[i] = apiSectionOf(x)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"calendars": out})
	return nil
}

// apiCalDavSync: «Синхронизировать сейчас».
func (s *Server) apiCalDavSync(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	x, err := s.Calendar.Dav(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	n, serr := s.syncDav(r.Context(), x)
	if x, err = s.Calendar.Dav(r.Context(), p.UserID, x.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"calendar": apiSectionOf(x), "events": n, "ok": serr == nil})
	return nil
}
