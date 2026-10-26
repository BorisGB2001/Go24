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
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// ---------- Telephony, as «Телефония»: own Asterisk, inner numbers, SIP accounts ----------

// telephonySettings: «Телефония» of the settings (administrators) — telephony on or off, the employees' inner
// numbers and whether their phones are on.
func (s *Server) telephonySettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
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
	trunks, err := s.Telephony.Trunks(ctx)
	if err != nil {
		s.fail(w, "telephony trunks", err)
		return
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
	s.render(w, r, "telephony", page{TitleKey: "tel.title", Nav: "telephony", Data: map[string]any{
		"Enabled": on, "Record": record, "Lead": lead, "Trunks": trunks,
		"NewTrunk": telephony.Trunk{Register: true, OutAll: true, WorkDays: []int{1, 2, 3, 4, 5}}, "CRM": s.CRM != nil, "Lines": lines, "Users": users, "Online": online, "Version": version, "Down": aerr != nil,
		"SIPHost": s.Telephony.SIPHost}})
}

// telephonyNumber gives an employee an inner number or takes it away (administrators).
func (s *Server) telephonyNumber(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
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
	if sess.UserID != id && !sess.Admin {
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
	if s.Telephony == nil || (sess.UserID != userID && !sess.Admin) {
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
	return map[string]any{"Line": l, "SIPHost": s.Telephony.SIPHost, "Realm": telephony.Realm}
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
// needs a secure page, so a plain-http visit goes to the https address of go24 first.
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
	s.render(w, r, "phone", page{TitleKey: "phone.title", Bare: true, Data: map[string]any{
		"ID": id, "Number": l.Number, "Login": l.WebLogin(), "Password": l.WebPassword, "SIPHost": s.Telephony.SIPHost,
		"Dial": telephony.Dialable(r.URL.Query().Get("dial"))}})
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
	if !sess.Admin {
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
		WorkOn: r.FormValue("work_on") == "1", WorkFrom: r.FormValue("work_from"), WorkTo: r.FormValue("work_to"),
		NoAnswer: r.FormValue("noanswer"), NoAnswerTo: r.FormValue("noanswer_to"), OffHours: r.FormValue("offhours"), OffHoursTo: r.FormValue("offhours_to"),
		DropGreeting: r.FormValue("greeting_drop") == "1"}
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
	if _, err := s.Telephony.SaveTrunk(r.Context(), sess.UserID, t); err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.taskErr(w, r, err, "/settings/telephony#trunks")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#trunks", http.StatusSeeOther)
}

// trunkGreeting gives the greeting of a line's voicemail to listen to (administrators).
func (s *Server) trunkGreeting(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		http.NotFound(w, r)
		return
	}
	g, err := s.Telephony.TrunkGreeting(r.Context(), r.PathValue("id"))
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
	if !sess.Admin {
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
