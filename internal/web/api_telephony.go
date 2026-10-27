package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// ---------- REST of «Телефония» (voximplant.* and telephony.*) ----------
//
// voximplant.statistic.get          — GET /api/v1/telephony/calls, /api/v1/telephony/calls/{id} (by «Права доступа»)
// the record and its text            — GET …/calls/{id}/record, POST …/calls/{id}/transcript
// «Комментарий к звонку»             — PATCH …/calls/{id} {comment}
// «Статистика звонков»               — GET /api/v1/telephony/stats
// voximplant.line.get                — GET /api/v1/telephony/lines (the external lines)
// voximplant.user.get                — GET /api/v1/telephony/users (the inner numbers)
// «Позвонить» (telephony.externalcall / callback.start) — POST /api/v1/telephony/call {number}: rings the key owner's
//                                       phones, the one who answers is put through to the number
// «Чёрный список»                    — /api/v1/telephony/blacklist (who may change telephony's settings)
// ONVOXIMPLANTCALLEND                — an event of the applications and the webhook "call.ended" (phoneEvents)

func (s *Server) apiTelephonyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/telephony/calls", s.api("telephony:read", s.apiTelCalls))
	mux.HandleFunc("GET /api/v1/telephony/calls/{id}", s.api("telephony:read", s.apiTelCall))
	mux.HandleFunc("PATCH /api/v1/telephony/calls/{id}", s.api("telephony:write", s.apiTelComment))
	mux.HandleFunc("GET /api/v1/telephony/calls/{id}/record", s.api("telephony:read", s.apiTelRecord))
	mux.HandleFunc("POST /api/v1/telephony/calls/{id}/transcript", s.api("telephony:write", s.apiTelTranscript))
	mux.HandleFunc("GET /api/v1/telephony/stats", s.api("telephony:read", s.apiTelStats))
	mux.HandleFunc("GET /api/v1/telephony/lines", s.api("telephony:read", s.apiTelLines))
	mux.HandleFunc("GET /api/v1/telephony/users", s.api("telephony:read", s.apiTelUsers))
	mux.HandleFunc("POST /api/v1/telephony/call", s.api("telephony:write", s.apiTelDial))
	mux.HandleFunc("GET /api/v1/telephony/blacklist", s.api("telephony:read", s.apiTelBlacklist))
	mux.HandleFunc("POST /api/v1/telephony/blacklist", s.api("telephony:write", s.apiTelBlock))
	mux.HandleFunc("DELETE /api/v1/telephony/blacklist/{id}", s.api("telephony:write", s.apiTelUnblock))
}

type apiTelCRM struct {
	Entity string `json:"entity"` // deal, lead, contact, company
	ID     string `json:"id"`
	Name   string `json:"name"`
}

// apiTelCall is a call of the log. Type is how the key's owner sees it — out, in, missed (their incoming nobody
// answered) or inner (others' call); Direction is the call's own: in (from outside), out (to outside), inner (between
// employees).
type apiTelCall struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Direction    string     `json:"direction"`
	Status       string     `json:"status"` // answered, busy, noanswer, cancel, failed, voicemail
	From         string     `json:"from"`
	To           string     `json:"to"`
	FromUser     *apiUser   `json:"from_user,omitempty"`
	ToUser       *apiUser   `json:"to_user,omitempty"`
	PhoneNumber  string     `json:"phone_number,omitempty"` // the outside number (PHONE_NUMBER)
	Line         string     `json:"line,omitempty"`         // the external line's number (PORTAL_NUMBER)
	StartedAt    time.Time  `json:"started_at"`
	AnsweredAt   *time.Time `json:"answered_at,omitempty"`
	Seconds      int        `json:"seconds"`
	Rating       int        `json:"rating,omitempty"`
	Comment      string     `json:"comment,omitempty"`
	TransferFrom string     `json:"transfer_from,omitempty"`
	TransferTo   string     `json:"transfer_to,omitempty"`
	Mobile       string     `json:"mobile,omitempty"` // forwarded to the employee's mobile
	CRM          *apiTelCRM `json:"crm,omitempty"`
	HasRecord    bool       `json:"has_record"`
	Record       string     `json:"record,omitempty"`     // the address of the record, for whom «Записи разговоров» lets hear it
	Transcript   string     `json:"transcript,omitempty"` // the record's text, for them too
}

// apiTelCallOf is the call as me sees it; hears — may they listen to the record.
func (s *Server) apiTelCallOf(c telephony.Call, me string, hears bool) apiTelCall {
	out := apiTelCall{ID: c.ID, Type: "inner", Direction: "inner", Status: c.Status, From: c.From, To: c.To, Line: c.Line,
		StartedAt: c.Started, AnsweredAt: c.Answered, Seconds: c.Seconds, Rating: c.Rating, Comment: c.Comment,
		TransferFrom: c.XFrom, TransferTo: c.XTo, Mobile: c.Mobile, HasRecord: c.Record != "" && s.Telephony.Records != ""}
	switch me {
	case c.FromUser.UserID:
		out.Type = "out"
	case c.ToUser.UserID:
		out.Type = "in"
		if c.Missed() {
			out.Type = "missed"
		}
	}
	if c.FromUser.UserID != "" {
		out.FromUser = &apiUser{ID: c.FromUser.UserID, Name: c.FromUser.Name}
	}
	if c.ToUser.UserID != "" {
		out.ToUser = &apiUser{ID: c.ToUser.UserID, Name: c.ToUser.Name}
	}
	var dir string
	if out.PhoneNumber, dir, _ = c.Outside(); dir != "" {
		out.Direction = dir
	}
	if c.CRMEntity != "" {
		out.CRM = &apiTelCRM{Entity: c.CRMEntity, ID: c.CRMID, Name: c.CRMName}
	}
	if out.HasRecord && hears {
		out.Record = "/api/v1/telephony/calls/" + c.ID + "/record"
		out.Transcript = c.Transcript
	}
	return out
}

// apiTelOn: telephony is there and the key's owner may see the log.
func (s *Server) apiTelOn(ctx context.Context, p apikeys.Principal, r *http.Request) (telephony.Perms, error) {
	if s.Telephony == nil {
		return nil, i18n.E("err.telephony_off")
	}
	perms := s.telPerms(ctx, s.apiSess(r, p))
	if !perms.Has("calls", "own") {
		return nil, i18n.E("err.forbidden")
	}
	return perms, nil
}

// apiTelCalls is voximplant.statistic.get: the calls «Права доступа» show the key's owner, newest first; filters
// type (in, out, missed), user, q (a part of a number), from, to (dates), before (the next page: started_at of the
// last one), limit (up to 200).
func (s *Server) apiTelCalls(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	perms, err := s.apiTelOn(ctx, p, r)
	if err != nil {
		return err
	}
	sess := s.apiSess(r, p)
	q := r.URL.Query()
	f := telephony.CallFilter{Kind: q.Get("type"), Number: q.Get("q"), Limit: 50}
	switch f.Kind {
	case "in", "out", "missed", "":
	default:
		return i18n.E("tel.err_param", "type")
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
		f.Limit = min(n, 200)
	}
	for k, t := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(k); v != "" {
			d, err := time.ParseInLocation(time.DateOnly, v, time.Local)
			if err != nil {
				return i18n.E("tel.err_param", k)
			}
			*t = d
		}
	}
	if !f.To.IsZero() {
		f.To = f.To.AddDate(0, 0, 1)
	}
	if v := q.Get("before"); v != "" {
		if f.Before, err = time.Parse(time.RFC3339Nano, v); err != nil {
			return i18n.E("tel.err_param", "before")
		}
	}
	if f.UserID, f.Users, err = s.telScope(ctx, sess, perms["calls"], q.Get("user")); err != nil {
		return err
	}
	list, err := s.Telephony.Calls(ctx, f)
	if err != nil {
		return err
	}
	me := f.UserID
	if me == "" {
		me = sess.UserID
	}
	var team []string
	if perms["records"] == "dept" {
		if team, err = s.Telephony.Team(ctx, sess.UserID); err != nil {
			return err
		}
	}
	out := make([]apiTelCall, len(list))
	for i, c := range list {
		out[i] = s.apiTelCallOf(c, me, phoneHearsBy(perms, sess, c, team, nil))
	}
	res := map[string]any{"calls": out}
	if len(list) == f.Limit {
		res["next_before"] = list[len(list)-1].Started.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, res)
	return nil
}

// apiTelFind is a call of the log the key's owner sees («calls»); not seen — not found.
func (s *Server) apiTelFind(r *http.Request, p apikeys.Principal) (telephony.Call, error) {
	ctx := r.Context()
	if _, err := s.apiTelOn(ctx, p, r); err != nil {
		return telephony.Call{}, err
	}
	c, err := s.Telephony.CallByID(ctx, r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) {
		return c, i18n.E("err.not_found")
	}
	if err != nil {
		return c, err
	}
	if !s.phoneSees(ctx, s.apiSess(r, p), c) {
		return c, i18n.E("err.not_found")
	}
	// the line of the log: the names and the rest (CallByID has the numbers and the employees' ids only)
	list, err := s.Telephony.Calls(ctx, telephony.CallFilter{ID: c.ID, Limit: 1})
	if err != nil || len(list) == 0 {
		return c, err
	}
	return list[0], nil
}

// apiTelCall is one call of the log.
func (s *Server) apiTelCall(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.apiTelFind(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiTelCallOf(c, p.UserID, s.phoneHears(r.Context(), s.apiSess(r, p), c)))
	return nil
}

// apiTelComment is «Комментарий к звонку» ({"comment": ""} clears it): the call's comment in the log and in its
// «Звонок» of the CRM card.
func (s *Server) apiTelComment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.apiTelFind(r, p)
	if err != nil {
		return err
	}
	var in struct {
		Comment *string `json:"comment"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Comment == nil {
		return i18n.E("tel.err_param", "comment")
	}
	text := strings.TrimSpace(*in.Comment)
	if len([]rune(text)) > 2000 {
		text = string([]rune(text)[:2000])
	}
	if err := s.Telephony.SetCallComment(r.Context(), c.ID, text); err != nil {
		return err
	}
	c.Comment = text
	writeJSON(w, http.StatusOK, s.apiTelCallOf(c, p.UserID, s.phoneHears(r.Context(), s.apiSess(r, p), c)))
	return nil
}

// apiTelRecord gives the recorded conversation (WAV) to whom «Записи разговоров» lets hear it.
func (s *Server) apiTelRecord(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.apiTelFind(r, p)
	if err != nil {
		return err
	}
	if c.Record == "" || s.Telephony.Records == "" {
		return i18n.E("err.not_found")
	}
	if !s.phoneHears(r.Context(), s.apiSess(r, p), c) {
		return i18n.E("err.forbidden")
	}
	name := filepath.Base(c.Record)
	if _, err := os.Stat(filepath.Join(s.Telephony.Records, name)); err != nil {
		return i18n.E("err.not_found")
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, filepath.Join(s.Telephony.Records, name))
	return nil
}

// apiTelTranscript makes the record's text by Whisper (once; then it is kept) and gives the call with it.
func (s *Server) apiTelTranscript(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.apiTelFind(r, p)
	if err != nil {
		return err
	}
	if c.Record == "" || s.Telephony.Records == "" {
		return i18n.E("err.not_found")
	}
	if !s.phoneHears(r.Context(), s.apiSess(r, p), c) {
		return i18n.E("err.forbidden")
	}
	if c.Transcript == "" {
		if err := s.phoneTranscribe(r.Context(), c); err != nil {
			return err
		}
		if c, err = s.apiTelFind(r, p); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, s.apiTelCallOf(c, p.UserID, true))
	return nil
}

type apiTelStatRow struct {
	User        *apiUser `json:"user,omitempty"` // none — the calls nobody of go24's took (the total has none too)
	In          int      `json:"in"`
	Answered    int      `json:"answered"`
	Missed      int      `json:"missed"`
	MissedPct   int      `json:"missed_pct"`
	Out         int      `json:"out"`
	OutAnswered int      `json:"out_answered"`
	Seconds     int      `json:"seconds"`
	AvgTalk     int      `json:"avg_talk"`
	AvgWait     int      `json:"avg_wait"`
	Rated       int      `json:"rated"`
	AvgRating   string   `json:"avg_rating,omitempty"`
}

func apiTelStatOf(r telephony.StatRow) apiTelStatRow {
	out := apiTelStatRow{In: r.In, Answered: r.Answered(), Missed: r.Missed, MissedPct: r.MissedPct(), Out: r.Out, OutAnswered: r.OutOK,
		Seconds: r.Seconds, AvgTalk: r.AvgTalk(), AvgWait: r.AvgWait(), Rated: r.Rated, AvgRating: r.AvgRating()}
	if r.UserID != "" {
		out.User = &apiUser{ID: r.UserID, Name: r.Name}
	}
	return out
}

// apiTelStats is «Статистика звонков» of the external lines: from, to (dates; by default this month to today, up to a
// year), line (its number), user — by «Права доступа», as the page.
func (s *Server) apiTelStats(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	perms, err := s.apiTelOn(ctx, p, r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	for k, t := range map[string]*time.Time{"from": &from, "to": &to} {
		if v := q.Get(k); v != "" {
			d, err := time.ParseInLocation(time.DateOnly, v, time.Local)
			if err != nil {
				return i18n.E("tel.err_param", k)
			}
			*t = d
		}
	}
	if to.Before(from) {
		from, to = to, from
	}
	if to.Sub(from) > phoneStatsDays*24*time.Hour {
		from = to.AddDate(0, 0, -phoneStatsDays+1)
	}
	f := telephony.StatFilter{From: from, To: to.AddDate(0, 0, 1), Line: q.Get("line")}
	if f.UserID, f.Users, err = s.telScope(ctx, s.apiSess(r, p), perms["calls"], q.Get("user")); err != nil {
		return err
	}
	st, err := s.Telephony.Stats(ctx, f, time.Local)
	if err != nil {
		return err
	}
	users := make([]apiTelStatRow, len(st.Users))
	for i, u := range st.Users {
		users[i] = apiTelStatOf(u)
	}
	type day struct {
		Day      string `json:"day"`
		In       int    `json:"in"`
		Answered int    `json:"answered"`
		Missed   int    `json:"missed"`
		Out      int    `json:"out"`
	}
	days := make([]day, len(st.Days))
	for i, d := range st.Days {
		days[i] = day{Day: d.Day.Format(time.DateOnly), In: d.In, Answered: d.Answered(), Missed: d.Missed, Out: d.Out}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly),
		"total": apiTelStatOf(st.Total), "users": users, "days": days})
	return nil
}

// apiTelLines is voximplant.line.get: the external lines (without their logins and passwords) and whether the key's
// owner may call out by each.
func (s *Server) apiTelLines(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Telephony == nil {
		return i18n.E("err.telephony_off")
	}
	ctx := r.Context()
	trunks, err := s.Telephony.Trunks(ctx)
	if err != nil {
		return err
	}
	outgoing := s.telPerms(ctx, s.apiSess(r, p))["outgoing"]
	type line struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Number  string `json:"number"`
		CanCall bool   `json:"can_call"`
	}
	out := make([]line, len(trunks))
	for i, t := range trunks {
		out[i] = line{ID: t.ID, Name: t.Name, Number: t.Number, CanCall: outgoing != "none" && (t.OutAll || slices.Contains(t.Out, p.UserID))}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out, "outgoing": outgoing})
	return nil
}

// apiTelUsers is voximplant.user.get: the employees with an inner number and whether a phone of theirs is on (their
// SIP phone or go24's softphone registered; "online" is missing while Asterisk does not answer).
func (s *Server) apiTelUsers(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Telephony == nil {
		return i18n.E("err.telephony_off")
	}
	ctx := r.Context()
	lines, err := s.Telephony.Lines(ctx)
	if err != nil {
		return err
	}
	actx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	online, aerr := s.Telephony.ARI.Online(actx)
	type user struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Number string `json:"number"`
		Online *bool  `json:"online,omitempty"`
	}
	out := []user{}
	for _, l := range lines {
		if l.Dup {
			continue
		}
		u := user{ID: l.UserID, Name: l.Name, Number: l.Number}
		if aerr == nil {
			on := online[l.Login()] || online[l.WebLogin()]
			u.Online = &on
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
	return nil
}

var callableRe = regexp.MustCompile(`^\+?[0-9]{2,20}$`)

// apiTelDial is «Позвонить» from REST, as telephony.externalcall / voximplant.callback.start: Asterisk rings the key
// owner's phones (SIP phone and softphone) showing the number; when they answer, it calls the number as if they
// dialed it — an outside one by their lines and «Исходящие звонки наружу»; the log gets their outgoing call.
func (s *Server) apiTelDial(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Telephony == nil {
		return i18n.E("err.telephony_off")
	}
	ctx := r.Context()
	var in struct {
		Number string `json:"number"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	number := telephony.Dialable(in.Number)
	if !callableRe.MatchString(number) {
		return i18n.E("phone.bad_number")
	}
	if on, err := s.Telephony.Enabled(ctx); err != nil {
		return err
	} else if !on {
		return i18n.E("err.telephony_off")
	}
	l, ok := s.phoneLine(ctx, p.UserID)
	if !ok || l.Dup {
		return i18n.E("err.telephony_no_number")
	}
	if number == l.Number {
		return i18n.E("phone.bad_number")
	}
	if len(number) > 8 || number[0] == '+' { // outside: «Исходящие звонки наружу»
		perms := s.telPerms(ctx, s.apiSess(r, p))
		switch perms["outgoing"] {
		case "none":
			return i18n.E("err.forbidden")
		case "crm":
			if entity, _, _, err := s.phoneClient(ctx, number); err != nil {
				return err
			} else if entity == "" {
				return i18n.E("tel.err_out_crm")
			}
		}
	}
	record, err := s.Telephony.Recording(ctx)
	if err != nil {
		return err
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Telephony.ARI.Call(actx, l.Number, number, record); err != nil {
		s.Log.Error("telephony api call", "err", err)
		return i18n.E("tel.err_asterisk")
	}
	writeJSON(w, http.StatusOK, map[string]any{"calling": true, "from": l.Number, "to": number})
	return nil
}

// apiTelAdmin: telephony is there and the key's owner may change its settings.
func (s *Server) apiTelAdmin(r *http.Request, p apikeys.Principal) error {
	if s.Telephony == nil {
		return i18n.E("err.telephony_off")
	}
	if !s.telAdmin(r.Context(), s.apiSess(r, p)) {
		return i18n.E("err.forbidden")
	}
	return nil
}

type apiBlocked struct {
	ID        string    `json:"id"`
	Number    string    `json:"number"`
	Comment   string    `json:"comment,omitempty"`
	AddedBy   string    `json:"added_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// apiTelBlacklist is «Чёрный список», the newest first.
func (s *Server) apiTelBlacklist(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiTelAdmin(r, p); err != nil {
		return err
	}
	list, err := s.Telephony.Blacklist(r.Context())
	if err != nil {
		return err
	}
	out := make([]apiBlocked, len(list))
	for i, b := range list {
		out[i] = apiBlocked{ID: b.ID, Number: b.Number, Comment: b.Comment, AddedBy: b.ByName, CreatedAt: b.At}
	}
	writeJSON(w, http.StatusOK, map[string]any{"blacklist": out})
	return nil
}

// apiTelBlock puts a number on the black list {number, comment}.
func (s *Server) apiTelBlock(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiTelAdmin(r, p); err != nil {
		return err
	}
	var in struct {
		Number  string `json:"number"`
		Comment string `json:"comment"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Telephony.Block(r.Context(), p.UserID, in.Number, in.Comment); err != nil {
		return err
	}
	s.Telephony.Kick()
	return s.apiTelBlacklist(w, r, p)
}

// apiTelUnblock takes a number off the black list.
func (s *Server) apiTelUnblock(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiTelAdmin(r, p); err != nil {
		return err
	}
	if err := s.Telephony.Unblock(r.Context(), r.PathValue("id")); errors.Is(err, telephony.ErrNotFound) {
		return i18n.E("err.not_found")
	} else if err != nil {
		return err
	}
	s.Telephony.Kick()
	w.WriteHeader(http.StatusNoContent)
	return nil
}
