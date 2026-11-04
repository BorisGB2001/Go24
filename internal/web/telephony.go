package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/marketing"
	"go24/internal/openlines"
	"go24/internal/telephony"
)

// ---------- Telephony, as «Телефония»: own Asterisk, inner numbers, SIP accounts ----------

// telephonySettings: «Телефония» of the settings (administrators) — telephony on or off, the employees' inner
// numbers and whether their phones are on.
func (s *Server) telephonySettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		if err := s.Telephony.SetEnabled(ctx, sess.UserID, r.FormValue("enabled") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetRecording(ctx, sess.UserID, r.FormValue("record") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetLeadAuto(ctx, sess.UserID, r.FormValue("lead") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetRecalling(ctx, sess.UserID, r.FormValue("recall") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetRecordDrive(ctx, sess.UserID, r.FormValue("drive") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetPicking(ctx, sess.UserID, r.FormValue("pickup") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		skip := telephony.Skip{DND: r.FormValue("skip_dnd") == "1", Absent: r.FormValue("skip_absent") == "1", Workday: r.FormValue("skip_workday") == "1"}
		if err := s.Telephony.SetSkipping(ctx, sess.UserID, skip); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetTranscribing(ctx, sess.UserID, r.FormValue("transcribe") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetSummarizing(ctx, sess.UserID, r.FormValue("summary") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		if err := s.Telephony.SetFilling(ctx, sess.UserID, r.FormValue("fill") == "1"); err != nil {
			s.taskErr(w, r, err, "/settings/telephony")
			return
		}
		s.Telephony.Kick()
		http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	on, err := s.Telephony.Enabled(ctx)
	if err != nil {
		s.fail(w, "telephony settings", err)
		return
	}
	lines, err := s.Telephony.Lines(ctx)
	if err != nil {
		s.fail(w, "telephony lines", err)
		return
	}
	record, err := s.Telephony.Recording(ctx)
	if err != nil {
		s.fail(w, "telephony recording", err)
		return
	}
	lead, err := s.Telephony.LeadAuto(ctx)
	if err != nil {
		s.fail(w, "telephony leads", err)
		return
	}
	recall, err := s.Telephony.Recalling(ctx)
	if err != nil {
		s.fail(w, "telephony recall", err)
		return
	}
	toDrive, err := s.Telephony.RecordDrive(ctx)
	if err != nil {
		s.fail(w, "telephony record drive", err)
		return
	}
	pickup, err := s.Telephony.Picking(ctx)
	if err != nil {
		s.fail(w, "telephony pickup", err)
		return
	}
	skip, err := s.Telephony.Skipping(ctx)
	if err != nil {
		s.fail(w, "telephony skipping", err)
		return
	}
	away, err := s.Telephony.Unavailable(ctx, time.Now())
	if err != nil {
		s.fail(w, "telephony unavailable", err)
		return
	}
	transcribe, err := s.Telephony.Transcribing(ctx)
	if err != nil {
		s.fail(w, "telephony transcribing", err)
		return
	}
	summary, err := s.Telephony.Summarizing(ctx)
	if err != nil {
		s.fail(w, "telephony summary", err)
		return
	}
	fill, err := s.Telephony.Filling(ctx)
	if err != nil {
		s.fail(w, "telephony fill", err)
		return
	}
	scripts, err := s.Telephony.Scripts(ctx)
	if err != nil {
		s.fail(w, "telephony scripts", err)
		return
	}
	sales, err := s.Telephony.SalesScripts(ctx, "")
	if err != nil {
		s.fail(w, "telephony sales scripts", err)
		return
	}
	trunks, err := s.Telephony.Trunks(ctx)
	if err != nil {
		s.fail(w, "telephony trunks", err)
		return
	}
	menus, err := s.Telephony.Menus(ctx)
	if err != nil {
		s.fail(w, "telephony menus", err)
		return
	}
	black, err := s.Telephony.Blacklist(ctx)
	if err != nil {
		s.fail(w, "telephony black list", err)
		return
	}
	groups, err := s.Telephony.Groups(ctx)
	if err != nil {
		s.fail(w, "telephony groups", err)
		return
	}
	callbacks, err := s.Telephony.Callbacks(ctx)
	if err != nil {
		s.fail(w, "telephony callbacks", err)
		return
	}
	requests := map[string][]telephony.CallbackRequest{}
	for _, c := range callbacks {
		if requests[c.ID], err = s.Telephony.CallbackRequests(ctx, c.ID, 10); err != nil {
			s.fail(w, "telephony callback requests", err)
			return
		}
	}
	var ads []marketing.AdSource // «Колл-трекинг»: the channels a line may be the number of
	if s.Marketing != nil {
		if ads, err = s.Marketing.AdSources(ctx); err != nil {
			s.fail(w, "telephony ad sources", err)
			return
		}
	}
	users, err := s.Tasks.ListUsers(ctx)
	if err != nil {
		s.fail(w, "telephony users", err)
		return
	}
	// Asterisk answers or not; whose phones are registered
	actx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	version, aerr := s.Telephony.ARI.Version(actx)
	online := map[string]bool{}
	if aerr == nil {
		if online, aerr = s.Telephony.ARI.Online(actx); aerr != nil {
			online = map[string]bool{}
		}
	}
	if aerr != nil {
		s.Log.Warn("telephony: asterisk", "err", aerr)
	}
	var holidays string // «Из производственного календаря»: the company's holidays for a line
	if sched, err := s.Tasks.Schedule(ctx); err == nil {
		holidays = strings.Join(openlines.HolidaysDM(sched.Holidays), ",")
	}
	s.render(w, r, "telephony", page{TitleKey: "tel.title", Nav: "telephony", Data: map[string]any{
		"Zones": calendar.Zones(time.Now(), ""), "PortalTZ": portalTZ(), "CompanyHolidays": holidays,
		"Enabled": on, "Record": record, "Lead": lead, "Recall": recall, "Drive": toDrive, "Pickup": pickup, "Skip": skip, "Away": away, "Transcribe": transcribe, "Summary": summary, "Fill": fill, "AI": s.AI != nil, "Scripts": scripts, "NewScript": telephony.Script{Calls: "all", Auto: true, Low: 50}, "Sales": salesViews(sales), "NewSales": telephony.SalesScript{Calls: "all", Active: true}, "Speech": s.Speech != nil, "Trunks": trunks, "Menus": menus, "Blacklist": black,
		"Groups": groups, "NewGroup": telephony.Group{Strategy: "all", Ring: 20, Wait: 120},
		"Callbacks": callbacks, "Requests": requests, "AdSources": ads, "NewCallback": telephony.Callback{RingMode: "all"}, "Root": strings.TrimRight(s.BaseURL, "/"),
		"NewMenu":  telephony.Menu{Wait: 5, Repeats: 2, Direct: true, Timeout: telephony.MenuItem{Act: telephony.ActHangup}},
		"NewTrunk": telephony.Trunk{Register: true, OutAll: true, WorkDays: []int{1, 2, 3, 4, 5}}, "CRM": s.CRM != nil, "Lines": lines, "Users": users, "Online": online, "Version": version, "Down": aerr != nil,
		"SIPHost": s.Telephony.SIPHost}})
}

// blacklistAdd puts a number on telephony's black list (administrators): from the settings or «Детализация звонков».
func (s *Server) blacklistAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.blacklistDo(w, r, sess, func() error {
		return s.Telephony.Block(r.Context(), sess.UserID, r.FormValue("number"), r.FormValue("comment"))
	})
}

// blacklistDelete takes a number off the black list (administrators).
func (s *Server) blacklistDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.blacklistDo(w, r, sess, func() error { return s.Telephony.Unblock(r.Context(), r.PathValue("id")) })
}

func (s *Server) blacklistDo(w http.ResponseWriter, r *http.Request, sess auth.Session, do func() error) {
	back := r.FormValue("back")
	if len(back) < 1 || back[0] != '/' || (len(back) > 1 && (back[1] == '/' || back[1] == '\\')) {
		back = "/settings/telephony#blacklist"
	}
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	msg := s.tr(r, "common.saved")
	if err := do(); err != nil {
		msg = s.errText(r, err)
	} else {
		s.Telephony.Kick()
	}
	path, frag, _ := strings.Cut(back, "#")
	if frag != "" {
		frag = "#" + frag
	}
	http.Redirect(w, r, path+sepFor(path)+"msg="+url.QueryEscape(msg)+frag, http.StatusSeeOther)
}

// telephonyNumber gives an employee an inner number or takes it away (administrators).
func (s *Server) telephonyNumber(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.SetNumber(r.Context(), r.FormValue("user"), r.FormValue("number")); err != nil {
		s.taskErr(w, r, err, "/settings/telephony")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// telephonyPassword gives the employee a new password of their SIP phone (they themselves or administrators).
func (s *Server) telephonyPassword(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/u/" + id
	if r.FormValue("back") == "settings" {
		back = "/settings/telephony"
	}
	if sess.UserID != id && !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), back)
		return
	}
	if _, err := s.Telephony.ResetPassword(r.Context(), id); err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			err = i18n.E("err.telephony_no_number")
		}
		s.taskErr(w, r, err, back)
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "tel.password_changed")), http.StatusSeeOther)
}

// telephonyOf is the block «Телефония» of a profile: for the employee themselves and administrators, while telephony
// is on and the employee has an inner number; nil — none.
func (s *Server) telephonyOf(ctx context.Context, sess auth.Session, userID string) map[string]any {
	if s.Telephony == nil || (sess.UserID != userID && !s.telAdmin(ctx, sess)) {
		return nil
	}
	on, err := s.Telephony.Enabled(ctx)
	if err != nil || !on {
		return nil
	}
	l, ok, err := s.Telephony.LineOf(ctx, userID)
	if err != nil {
		s.Log.Error("profile telephony", "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	out := map[string]any{"Line": l, "SIPHost": s.Telephony.SIPHost, "Realm": telephony.Realm, "Forwards": telephony.Forwards}
	prefs, err := s.Telephony.PrefsOf(ctx, userID)
	if err != nil {
		s.Log.Error("profile telephony", "err", err)
		return out
	}
	out["Prefs"] = prefs
	if lines, err := s.telOutLines(ctx, userID); err == nil {
		out["OutLines"] = lines
	} else {
		s.Log.Error("profile telephony", "err", err)
	}
	return out
}

// telOutLines are the external lines the employee may call out by (none while their calls out are forbidden).
func (s *Server) telOutLines(ctx context.Context, userID string) ([]telephony.Trunk, error) {
	limits, err := s.Telephony.OutLimits(ctx, []string{userID})
	if err != nil || limits[userID] == "none" {
		return nil, err
	}
	trunks, err := s.Telephony.Trunks(ctx)
	if err != nil {
		return nil, err
	}
	return telephony.OutTrunks(trunks, userID), nil
}

// telephonyPrefs keeps the employee's own telephony settings: the line of their calls out and forwarding to their
// mobile (they themselves or administrators); the dialplan follows.
func (s *Server) telephonyPrefs(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/u/" + id
	if sess.UserID != id && !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), back)
		return
	}
	after, _ := strconv.Atoi(r.FormValue("after"))
	p := telephony.Prefs{OutTrunk: r.FormValue("out"), Forward: r.FormValue("forward"), After: after, To: r.FormValue("to")}
	if err := s.Telephony.SavePrefs(r.Context(), id, p); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/u/"+id+"?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#ptel", http.StatusSeeOther)
}

// ---------- The softphone in the browser: a small window of its own, kept open while one works ----------

// phoneCookie names the browser's softphone window: commands of the pages («Позвонить», «Ответить») go to it.
const phoneCookie = "go24_phone"

// phoneLine is the user's line while they may use the softphone; false — no number or telephony off.
func (s *Server) phoneLine(ctx context.Context, userID string) (telephony.Line, bool) {
	if s.Telephony == nil {
		return telephony.Line{}, false
	}
	n, err := s.Telephony.NumberOf(ctx, userID)
	if err != nil {
		s.Log.Error("phone number", "err", err)
	}
	if n == "" {
		return telephony.Line{}, false
	}
	l, ok, err := s.Telephony.LineOf(ctx, userID)
	if err != nil {
		s.Log.Error("phone line", "err", err)
	}
	return l, ok && err == nil
}

// phoneWindow is the softphone (/phone): JsSIP registers the account "w<number>" over /telephony/ws. The microphone
// needs a secure page, so a plain-http visit goes to the https address of go24 first. The colleagues' numbers are
// offered to transfer a call to.
func (s *Server) phoneWindow(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.CallURL != "" && r.Header.Get("X-Forwarded-Proto") != "https" {
		http.Redirect(w, r, s.CallURL+r.URL.RequestURI(), http.StatusFound)
		return
	}
	l, ok := s.phoneLine(r.Context(), sess.UserID)
	if !ok {
		s.taskErr(w, r, i18n.E("err.telephony_no_number"), "/u/"+sess.UserID)
		return
	}
	id := ""
	if c, err := r.Cookie(phoneCookie); err == nil && validPhoneID(c.Value) {
		id = c.Value
	} else {
		id = randomHex(8)
	}
	// not Secure: pages of go24 on plain http (the same host) send it with «Позвонить» too
	http.SetCookie(w, &http.Cookie{Name: phoneCookie, Value: id, Path: "/", MaxAge: 365 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	// colleagues to transfer a call to: their names and inner numbers
	lines, err := s.Telephony.Lines(r.Context())
	if err != nil {
		s.fail(w, "phone colleagues", err)
		return
	}
	people := []telephony.Line{}
	for _, c := range lines {
		if !c.Dup && c.UserID != sess.UserID {
			people = append(people, telephony.Line{Name: c.Name, Number: c.Number})
		}
	}
	pickup, err := s.Telephony.Picking(r.Context())
	if err != nil {
		s.fail(w, "phone pickup", err)
		return
	}
	away, err := s.Telephony.Unavailable(r.Context(), time.Now())
	if err != nil {
		s.fail(w, "phone unavailable", err)
		return
	}
	s.render(w, r, "phone", page{TitleKey: "phone.title", Bare: true, Data: map[string]any{
		"ID": id, "Number": l.Number, "Login": l.WebLogin(), "Password": l.WebPassword, "SIPHost": s.Telephony.SIPHost,
		"Dial": telephony.Dialable(r.URL.Query().Get("dial")), "People": people, "Pickup": pickup, "Away": away[sess.UserID]}})
}

func validPhoneID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// phoneSocket hands the softphone's SIP over WebSocket to Asterisk (its http server, /ws).
func (s *Server) phoneSocket(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, ok := s.phoneLine(r.Context(), sess.UserID); !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	target, err := url.Parse(s.Telephony.ARI.URL)
	if err != nil {
		s.fail(w, "phone socket", err)
		return
	}
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.Out.URL.Path = "/ws"
		pr.Out.URL.RawQuery = ""
		pr.Out.Header.Del("Cookie") // go24's session is not Asterisk's business
		pr.Out.Header.Del("Authorization")
	}}
	proxy.ServeHTTP(w, r)
}

// phoneLive streams the commands of the pages to the softphone window (Server-Sent Events, id = its cookie).
func (s *Server) phoneLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.URL.Query().Get("id")
	if s.Live == nil || !validPhoneID(id) {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	cmds, stop := s.Live.Subscribe("p" + sess.UserID + "." + id)
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case c := <-cmds:
			fmt.Fprintf(w, "event: cmd\ndata: %s\n\n", c)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// phoneCmd is a page's command to the softphone window of this browser: dial (number=), answer, hangup.
// No window — {"open": url}: the page opens it (dial= in the address).
func (s *Server) phoneCmd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, ok := s.phoneLine(r.Context(), sess.UserID); !ok {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": s.tr(r, "err.telephony_no_number")})
		return
	}
	cmd := map[string]string{"cmd": r.FormValue("cmd")}
	switch cmd["cmd"] {
	case "dial":
		if cmd["number"] = telephony.Dialable(r.FormValue("number")); cmd["number"] == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.tr(r, "phone.bad_number")})
			return
		}
	case "answer", "hangup":
	default:
		http.Error(w, "bad command", http.StatusBadRequest)
		return
	}
	id := r.FormValue("phone")
	if c, err := r.Cookie(phoneCookie); id == "" && err == nil {
		id = c.Value
	}
	if !validPhoneID(id) {
		writeJSON(w, http.StatusOK, map[string]string{"open": s.phoneURL()})
		return
	}
	if err := s.Telephony.Notify(r.Context(), "p"+sess.UserID+"."+id, cmd); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sent": id, "open": s.phoneURL()})
}

func (s *Server) phoneURL() string {
	return s.CallURL + "/phone"
}

// phoneState: the softphone window tells what its line does (ringing, dialing, talking, idle; dir in|out) — the
// user's pages show the call (the incoming one with «Ответить»), the window gets who is on the other end.
func (s *Server) phoneState(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, ok := s.phoneLine(r.Context(), sess.UserID); !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	state, dir := r.FormValue("state"), r.FormValue("dir")
	switch state {
	case "ringing", "dialing", "talking", "idle", "online", "offline":
	default:
		http.Error(w, "bad state", http.StatusBadRequest)
		return
	}
	if dir != "in" {
		dir = "out"
	}
	ev := map[string]any{"kind": "phone", "state": state, "dir": dir, "phone": r.FormValue("phone")}
	if n := telephony.Dialable(r.FormValue("number")); n != "" {
		ev["number"], ev["from"] = n, n
		who, ok, err := s.Telephony.WhoIs(r.Context(), n)
		if err != nil {
			s.Log.Error("phone who", "err", err)
		}
		if ok {
			ev["from"], ev["user"], ev["url"] = who.Name, who.UserID, "/u/"+who.UserID
			ev["photo"] = company.PhotoURL(who.UserID, who.PhotoKey)
		} else if entity, id, name, err := s.phoneClient(r.Context(), n); err != nil {
			s.Log.Error("phone client", "err", err)
		} else if entity != "" { // a client of CRM: their card
			ev["from"], ev["url"] = name, telephony.Call{CRMEntity: entity, CRMID: id}.CRMPath()
			ev["crm"] = s.tr(r, "crm."+entity)
		}
	}
	if err := s.Telephony.Notify(r.Context(), "c"+sess.UserID, ev); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ev)
}

// trunkSave adds or changes an external line (administrators): the provider's server, login, password and the line's
// number; whom its calls ring (all at once or by turns) and who may call out by it.
func (s *Server) trunkSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	t := telephony.Trunk{ID: r.PathValue("id"), Name: r.FormValue("name"), Host: r.FormValue("host"), Username: r.FormValue("username"),
		Password: r.FormValue("password"), AuthUser: r.FormValue("auth_user"), Number: r.FormValue("number"), Register: r.FormValue("register") == "1",
		RingMode: r.FormValue("ring_mode"), Ring: r.Form["ring"], OutAll: r.FormValue("out_all") == "1", Out: r.Form["out"],
		WorkOn: r.FormValue("work_on") == "1", WorkFrom: r.FormValue("work_from"), WorkTo: r.FormValue("work_to"), WorkTZ: r.FormValue("work_tz"),
		NoAnswer: r.FormValue("noanswer"), NoAnswerTo: r.FormValue("noanswer_to"), OffHours: r.FormValue("offhours"), OffHoursTo: r.FormValue("offhours_to"),
		DropGreeting: r.FormValue("greeting_drop") == "1", Menu: r.FormValue("menu")}
	var err error
	if t.Holidays, err = openlines.Holidays(strings.FieldsFunc(r.FormValue("holidays"), func(c rune) bool { return c == ',' || c == ';' || unicode.IsSpace(c) })); err != nil {
		s.taskErr(w, r, err, "/settings/telephony#trunks")
		return
	}
	for _, d := range r.Form["work_days"] {
		if n, err := strconv.Atoi(d); err == nil {
			t.WorkDays = append(t.WorkDays, n)
		}
	}
	if f, _, err := r.FormFile("greeting"); err == nil {
		t.Greeting, err = io.ReadAll(io.LimitReader(f, 16<<20))
		f.Close()
		if err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if len(t.Greeting) == 0 {
			t.Greeting = nil
		}
	}
	id, err := s.Telephony.SaveTrunk(r.Context(), sess.UserID, t)
	if err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.taskErr(w, r, err, "/settings/telephony#trunks")
		return
	}
	err = s.Telephony.SetTrunkRating(r.Context(), id, r.FormValue("rate_on") == "1", formWAV(r, "rate_prompt"), r.FormValue("rate_drop") == "1")
	if nerr := s.Telephony.SetTrunkRecordNotice(r.Context(), id, r.FormValue("rec_note_on") == "1", formWAV(r, "rec_note"),
		r.FormValue("rec_note_drop") == "1"); nerr != nil && err == nil {
		err = nerr
	}
	if gerr := s.Telephony.SetTrunkGroup(r.Context(), id, r.FormValue("ring_group")); gerr != nil && err == nil {
		err = gerr
	}
	if oerr := s.Telephony.SetTrunkOwner(r.Context(), id, r.FormValue("to_owner") == "1"); oerr != nil && err == nil {
		err = oerr
	}
	if aerr := s.Telephony.SetTrunkAdSource(r.Context(), id, r.FormValue("ad_source")); aerr != nil && err == nil {
		err = aerr
	}
	if cerr := s.Telephony.SetTrunkCRM(r.Context(), id, r.FormValue("crm_create"), r.FormValue("crm_create_type")); cerr != nil && err == nil {
		err = cerr
	}
	s.Telephony.Kick()
	if err != nil {
		s.taskErr(w, r, err, "/settings/telephony#trunks")
		return
	}
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#trunks", http.StatusSeeOther)
}

// formWAV is the sound file uploaded in the form's field name; nil — none.
func formWAV(r *http.Request, name string) []byte {
	f, _, err := r.FormFile(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil || len(b) == 0 {
		return nil
	}
	return b
}

// trunkGreeting gives the greeting of a line's voicemail (what=rate: the invitation to rate the conversation, what=note:
// the notice of recording) to listen to (administrators).
func (s *Server) trunkGreeting(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		http.NotFound(w, r)
		return
	}
	get := s.Telephony.TrunkGreeting
	switch r.URL.Query().Get("what") {
	case "rate":
		get = s.Telephony.TrunkRatePrompt
	case "note":
		get = s.Telephony.TrunkRecordNotice
	}
	g, err := get(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && g == nil) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony greeting", err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Write(g)
}

// trunkDelete removes an external line (administrators).
func (s *Server) trunkDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.DeleteTrunk(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, telephony.ErrNotFound) {
		s.taskErr(w, r, err, "/settings/telephony#trunks")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony#trunks", http.StatusSeeOther)
}

// ivrSave adds or changes a voice menu (administrators): its name, greeting, how long it waits for a key and how many
// times it is played, the items of the keys, what happens when nothing is pressed and whether the client may dial an
// inner number right away.
func (s *Server) ivrSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	m := telephony.Menu{ID: r.PathValue("id"), Name: r.FormValue("name"), Direct: r.FormValue("direct") == "1",
		DropGreeting: r.FormValue("greeting_drop") == "1", Timeout: ivrItemForm(r, "timeout")}
	m.Wait, _ = strconv.Atoi(r.FormValue("wait"))
	m.Repeats, _ = strconv.Atoi(r.FormValue("repeats"))
	for i := range r.Form["digit"] {
		it := ivrItemForm(r, "item"+strconv.Itoa(i))
		it.Digit = r.Form["digit"][i]
		m.Items = append(m.Items, it)
	}
	if f, _, err := r.FormFile("greeting"); err == nil {
		m.Greeting, err = io.ReadAll(io.LimitReader(f, 16<<20))
		f.Close()
		if err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if len(m.Greeting) == 0 {
			m.Greeting = nil
		}
	}
	if _, err := s.Telephony.SaveMenu(r.Context(), sess.UserID, m); err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.taskErr(w, r, err, "/settings/telephony#menus")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#menus", http.StatusSeeOther)
}

// ivrItemForm is an item of the menu's form: its fields are named by p ("item0_act", "timeout_to"…).
func ivrItemForm(r *http.Request, p string) telephony.MenuItem {
	return telephony.MenuItem{Act: r.FormValue(p + "_act"), Users: r.Form[p+"_users"], Mode: r.FormValue(p + "_mode"),
		To: r.FormValue(p + "_to"), Menu: r.FormValue(p + "_menu"), Group: r.FormValue(p + "_group")}
}

// ivrGreeting gives the greeting of a voice menu to listen to (administrators).
func (s *Server) ivrGreeting(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		http.NotFound(w, r)
		return
	}
	g, err := s.Telephony.MenuGreeting(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && g == nil) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony menu greeting", err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Write(g)
}

// ivrDelete removes a voice menu (administrators).
func (s *Server) ivrDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.DeleteMenu(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, telephony.ErrNotFound) {
		s.taskErr(w, r, err, "/settings/telephony#menus")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony#menus", http.StatusSeeOther)
}

// scriptSave adds or changes a script of CoPilot's call assessment (administrators).
func (s *Server) scriptSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	sc := telephony.Script{ID: r.PathValue("id"), Name: r.FormValue("name"), Body: r.FormValue("body"), Calls: r.FormValue("calls"),
		Auto: r.FormValue("auto") == "1"}
	sc.Low, _ = strconv.Atoi(r.FormValue("low"))
	if _, err := s.Telephony.SaveScript(r.Context(), sess.UserID, sc); err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.taskErr(w, r, err, "/settings/telephony#scripts")
		return
	}
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#scripts", http.StatusSeeOther)
}

// scriptDelete removes a script of the call assessment (administrators); the scores it gave stay.
func (s *Server) scriptDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.DeleteScript(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, telephony.ErrNotFound) {
		s.taskErr(w, r, err, "/settings/telephony#scripts")
		return
	}
	http.Redirect(w, r, "/settings/telephony#scripts", http.StatusSeeOther)
}
