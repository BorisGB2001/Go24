package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/sites"
	"go24/internal/telephony"
)

// ---------- «Обратный звонок»: a client leaves their number on a site, go24 calls them through an external line ----------

// callbackHits: one address may ask for a few calls only.
var callbackHits = &sites.Hits{Max: 5, Per: 10 * time.Minute}

// callbackNumber is a number go24 may call back: digits, maybe with «+».
var callbackNumber = regexp.MustCompile(`^\+?[0-9]{6,20}$`)

// callbackSave adds or changes a form of «Обратный звонок» (administrators).
func (s *Server) callbackSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	c := telephony.Callback{ID: r.PathValue("id"), Name: r.FormValue("name"), Trunk: r.FormValue("trunk"), Ring: r.Form["ring"],
		RingMode: r.FormValue("ring_mode"), Title: r.FormValue("title"), Text: r.FormValue("text"), Button: r.FormValue("button")}
	if _, err := s.Telephony.SaveCallback(r.Context(), sess.UserID, c); err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.taskErr(w, r, err, "/settings/telephony#callbacks")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#callbacks", http.StatusSeeOther)
}

// callbackDelete removes a form with its requests (administrators).
func (s *Server) callbackDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.DeleteCallback(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, telephony.ErrNotFound) {
		s.taskErr(w, r, err, "/settings/telephony#callbacks")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony#callbacks", http.StatusSeeOther)
}

// callbackForm is the form as the client sees it (a page of its own; ?embed=1 — inside a site, crmform.js).
func (s *Server) callbackForm(w http.ResponseWriter, r *http.Request, status int, c telephony.Callback, data map[string]any) {
	data["Form"], data["Stamp"] = c, sites.FormStamp(time.Now())
	if t := r.PostFormValue("t"); t != "" { // shown again after a refusal: the time the client began, not now
		data["Stamp"] = t
	}
	data["Embed"] = r.FormValue("embed") == "1"
	data["Page"] = r.FormValue("page")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	s.render(w, r, "callbackpub", page{Data: data})
}

// publicCallback is the form of the address; a refusal is already written.
func (s *Server) publicCallback(w http.ResponseWriter, r *http.Request) (telephony.Callback, bool) {
	var c telephony.Callback
	err := telephony.ErrNotFound
	if s.Telephony != nil {
		c, err = s.Telephony.CallbackByToken(r.Context(), r.PathValue("token"))
	}
	if err != nil {
		if !errors.Is(err, telephony.ErrNotFound) {
			s.fail(w, "callback", err)
			return c, false
		}
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return c, false
	}
	return c, true
}

func (s *Server) callbackPublic(w http.ResponseWriter, r *http.Request) {
	if c, ok := s.publicCallback(w, r); ok {
		s.callbackForm(w, r, http.StatusOK, c, map[string]any{})
	}
}

// callbackSend: the client asks for a call. In the line's working hours go24 calls at once; out of them (or Asterisk
// not answering) the employees are told to call back. A new number makes a lead «Обратный звонок» (the setting of
// the calls' leads). A robot (the trap field filled, the form sent too fast) and a number that asked just now are
// told it went well, and nothing is made.
func (s *Server) callbackSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.publicCallback(w, r)
	if !ok {
		return
	}
	number := telephony.Dialable(r.FormValue("phone"))
	name := strings.TrimSpace(r.FormValue("name"))
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	done := func() { s.callbackForm(w, r, http.StatusOK, c, map[string]any{"Sent": true}) }
	refuse := func(key string, status int) {
		s.callbackForm(w, r, status, c, map[string]any{"Err": s.tr(r, key), "Phone": r.FormValue("phone"), "Name": name})
	}
	now := time.Now()
	if !sites.Human(r.FormValue("website"), r.FormValue("t"), now) {
		done()
		return
	}
	if !callbackNumber.MatchString(number) {
		refuse("err.callback_phone", http.StatusUnprocessableEntity)
		return
	}
	if !callbackHits.Allow(clientIP(r), now) {
		refuse("err.callback_often", http.StatusTooManyRequests)
		return
	}
	trunks, err := s.Telephony.Trunks(ctx)
	if err != nil {
		s.fail(w, "callback trunks", err)
		return
	}
	var line telephony.Trunk
	for _, t := range trunks {
		if t.ID == c.Trunk {
			line = t
		}
	}
	on, err := s.Telephony.Enabled(ctx)
	if err != nil {
		s.fail(w, "callback", err)
		return
	}
	status := telephony.CallbackLater
	if on && s.Telephony.Ready() && line.Working(now) {
		status = telephony.CallbackCalling
	}
	page := r.FormValue("page")
	if len(page) > 500 {
		page = page[:500]
	}
	id, err := s.Telephony.AddCallbackRequest(ctx, c.ID, telephony.CallbackRequest{Number: number, Name: name, Page: page, Status: status})
	if errors.Is(err, telephony.ErrCallbackRecent) {
		done()
		return
	}
	if err != nil {
		s.fail(w, "callback request", err)
		return
	}
	if err := s.callbackLead(ctx, c, number, name); err != nil {
		s.Log.Error("callback lead", "err", err, "form", c.ID)
	}
	if status == telephony.CallbackCalling {
		k := telephony.CallbackKey(c.ID)
		if err := s.Telephony.ARI.Originate(ctx, number, k, k+"-out"); err != nil {
			s.Log.Error("callback originate", "err", err, "form", c.ID)
			status = telephony.CallbackLater
			if err := s.Telephony.SetCallbackStatus(ctx, id, status); err != nil {
				s.Log.Error("callback status", "err", err)
			}
		}
	}
	if status == telephony.CallbackLater {
		if err := s.callbackNotify(ctx, c, number, name); err != nil {
			s.Log.Error("callback notify", "err", err, "form", c.ID)
		}
	}
	s.Log.Info("telephony callback", "form", c.ID, "status", status)
	done()
}

// callbackLead makes a lead «Обратный звонок» of a number no card has (when the calls' leads are on); its responsible
// is the form's first employee.
func (s *Server) callbackLead(ctx context.Context, c telephony.Callback, number, name string) error {
	if s.CRM == nil {
		return nil
	}
	entity, _, _, err := s.phoneClient(ctx, number)
	if err != nil || entity != "" {
		return err
	}
	if on, err := s.Telephony.LeadAuto(ctx); err != nil || !on {
		return err
	}
	emp := ""
	if len(c.Ring) > 0 {
		emp = c.Ring[0]
	}
	_, err = s.CRM.SaveLead(ctx, emp, crm.Lead{Title: s.portalTr("tel.callback_lead", number), Name: name, Phone: number,
		Source: c.Name, Origin: crm.Dict{Code: "CALLBACK"}, Responsible: crm.Ref{ID: emp}})
	return err
}

// callbackNotify tells the form's employees to call the client back.
func (s *Server) callbackNotify(ctx context.Context, c telephony.Callback, number, name string) error {
	who := number
	if name != "" {
		who = name + ", " + number
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
		SELECT u, 'phone', left($2, 300), $3 FROM unnest($1::uuid[]) AS u`,
		c.Ring, s.portalTr("tel.notify.callback", who, c.Name), "/telephony/calls?q="+url.QueryEscape(number))
	return err
}
