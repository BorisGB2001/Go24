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
// CoPilot's summary of it             — POST …/calls/{id}/summary (through the AI of the key's owner)
// CoPilot fills the client's card     — POST …/calls/{id}/fill (the lead or the open deal; the key's owner changes it)
// CoPilot's score by a script         — POST …/calls/{id}/assess {script}; the scripts — GET /api/v1/telephony/scripts
// «Комментарий к звонку»             — PATCH …/calls/{id} {comment}
// «Статистика звонков»               — GET /api/v1/telephony/stats
// voximplant.line.get                — GET /api/v1/telephony/lines (the external lines, ad_source of «Колл-трекинг», record_notice, crm_create)
// voximplant.user.get                — GET /api/v1/telephony/users (the inner numbers)
// «Позвонить» (telephony.externalcall / callback.start) — POST /api/v1/telephony/call {number}: rings the key owner's
//                                       phones, the one who answers is put through to the number
// «Озвучить текст»                   — GET /api/v1/telephony/tts (the voices), POST {text, voice, rate}: the WAV (tts.go)
// «Чёрный список»                    — /api/v1/telephony/blacklist (who may change telephony's settings)
// «Обзвон» of CRM                    — /api/v1/telephony/calllists: the key owner's lists, POST {entity, ids, title},
//                                       GET …/{id} with the clients, PATCH …/{id}/items/{item} {status}
// ONVOXIMPLANTCALLEND                — an event of the applications and the webhook "call.ended" (phoneEvents)
// by the names (/rest/…): telephony.externalcall.*, telephony.externalLine.*, voximplant.statistic.get — restb24tel.go

func (s *Server) apiTelephonyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/telephony/calls", s.api("telephony:read", s.apiTelCalls))
	mux.HandleFunc("GET /api/v1/telephony/calls/{id}", s.api("telephony:read", s.apiTelCall))
	mux.HandleFunc("PATCH /api/v1/telephony/calls/{id}", s.api("telephony:write", s.apiTelComment))
	mux.HandleFunc("GET /api/v1/telephony/calls/{id}/record", s.api("telephony:read", s.apiTelRecord))
	mux.HandleFunc("POST /api/v1/telephony/calls/{id}/transcript", s.api("telephony:write", s.apiTelTranscript))
	mux.HandleFunc("POST /api/v1/telephony/calls/{id}/summary", s.api("telephony:write", s.apiTelSummary))
	mux.HandleFunc("POST /api/v1/telephony/calls/{id}/fill", s.api("telephony:write", s.apiTelFill))
	mux.HandleFunc("POST /api/v1/telephony/calls/{id}/assess", s.api("telephony:write", s.apiTelAssess))
	mux.HandleFunc("GET /api/v1/telephony/scripts", s.api("telephony:read", s.apiTelScripts))
	mux.HandleFunc("GET /api/v1/telephony/salescripts", s.api("telephony:read", s.apiTelSalesScripts))
	mux.HandleFunc("GET /api/v1/telephony/stats", s.api("telephony:read", s.apiTelStats))
	mux.HandleFunc("GET /api/v1/telephony/active", s.api("telephony:read", s.apiTelActive))
	mux.HandleFunc("POST /api/v1/telephony/active/spy", s.api("telephony:write", s.apiTelSpy))
	mux.HandleFunc("POST /api/v1/telephony/active/pick", s.api("telephony:write", s.apiTelPick))
	mux.HandleFunc("GET /api/v1/telephony/lines", s.api("telephony:read", s.apiTelLines))
	mux.HandleFunc("GET /api/v1/telephony/users", s.api("telephony:read", s.apiTelUsers))
	mux.HandleFunc("POST /api/v1/telephony/call", s.api("telephony:write", s.apiTelDial))
	mux.HandleFunc("GET /api/v1/telephony/tts", s.api("telephony:read", s.apiTelVoices))
	mux.HandleFunc("POST /api/v1/telephony/tts", s.api("telephony:write", s.apiTelSpeak))
	mux.HandleFunc("GET /api/v1/telephony/blacklist", s.api("telephony:read", s.apiTelBlacklist))
	mux.HandleFunc("POST /api/v1/telephony/blacklist", s.api("telephony:write", s.apiTelBlock))
	mux.HandleFunc("DELETE /api/v1/telephony/blacklist/{id}", s.api("telephony:write", s.apiTelUnblock))
	mux.HandleFunc("GET /api/v1/telephony/calllists", s.api("telephony:read", s.apiCallLists))
	mux.HandleFunc("POST /api/v1/telephony/calllists", s.api("telephony:write", s.apiCallListMake))
	mux.HandleFunc("GET /api/v1/telephony/calllists/{id}", s.api("telephony:read", s.apiCallList))
	mux.HandleFunc("PATCH /api/v1/telephony/calllists/{id}/items/{item}", s.api("telephony:write", s.apiCallListItem))
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
	Summary      string     `json:"summary,omitempty"`    // CoPilot's summary of the conversation, for them too
	FilledAt     *time.Time `json:"filled_at,omitempty"`  // when CoPilot filled the client's CRM card from it
	Score        *apiScore  `json:"score,omitempty"`      // CoPilot's score by a script, for them too
	DriveFile    string     `json:"drive_file,omitempty"` // the record's file on the company's drive («Записанные звонки»), for them too
}

// apiScore is CoPilot's score of a conversation by a script.
type apiScore struct {
	Percent int    `json:"percent"`
	Script  string `json:"script"` // the script's name
	Text    string `json:"text"`   // what was done of the script and what was not, advice
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
		out.Summary = c.Summary
		out.FilledAt = c.Filled
		out.DriveFile = c.DriveFile
		if c.Score >= 0 {
			out.Score = &apiScore{Percent: c.Score, Script: c.ScoreName, Text: c.ScoreText}
		}
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
// type (in, out, missed), user, q (a part of a number), line (the external line's number), from, to (dates), before (the next page: started_at of the
// last one), limit (up to 200).
func (s *Server) apiTelCalls(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	perms, err := s.apiTelOn(ctx, p, r)
	if err != nil {
		return err
	}
	sess := s.apiSess(r, p)
	q := r.URL.Query()
	f := telephony.CallFilter{Kind: q.Get("type"), Number: q.Get("q"), Line: q.Get("line"), Limit: 50}
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
	w.Header().Set("Content-Type", recordType(name))
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
	Scored      int      `json:"scored"`              // the conversations CoPilot scored by a script
	AvgScore    *int     `json:"avg_score,omitempty"` // their average score, %
}

func apiTelStatOf(r telephony.StatRow) apiTelStatRow {
	out := apiTelStatRow{In: r.In, Answered: r.Answered(), Missed: r.Missed, MissedPct: r.MissedPct(), Out: r.Out, OutAnswered: r.OutOK,
		Seconds: r.Seconds, AvgTalk: r.AvgTalk(), AvgWait: r.AvgWait(), Rated: r.Rated, AvgRating: r.AvgRating(), Scored: r.Scored}
	if r.Scored > 0 {
		avg := r.AvgScore()
		out.AvgScore = &avg
	}
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
	type number struct {
		Number string     `json:"number"`
		Times  int        `json:"times"`
		Last   string     `json:"last"`
		User   *apiUser   `json:"user,omitempty"`
		CRM    *apiTelCRM `json:"crm,omitempty"`
	}
	waiting := make([]number, len(st.Missed.Waiting))
	for i, n := range st.Missed.Waiting {
		waiting[i] = number{Number: n.Number, Times: n.Times, Last: n.Last.Format(time.RFC3339)}
		if n.UserID != "" {
			waiting[i].User = &apiUser{ID: n.UserID, Name: n.Name}
		}
		if n.CRMEntity != "" {
			waiting[i].CRM = &apiTelCRM{Entity: n.CRMEntity, ID: n.CRMID, Name: n.CRMName}
		}
	}
	type hour struct {
		Weekday int `json:"weekday"` // 1 — Monday … 7 — Sunday
		Hour    int `json:"hour"`
		In      int `json:"in"`
		Missed  int `json:"missed"`
	}
	hours := []hour{}
	for d, hs := range st.Hours {
		for h, x := range hs {
			if x.In > 0 {
				hours = append(hours, hour{Weekday: d + 1, Hour: h, In: x.In, Missed: x.Missed})
			}
		}
	}
	m := st.Missed
	pf := from.AddDate(0, 0, -telephony.Days(from, to)-1)
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly),
		"total": apiTelStatOf(st.Total), "users": users, "days": days, "hours": hours,
		"previous": map[string]any{"from": pf.Format(time.DateOnly), "to": from.AddDate(0, 0, -1).Format(time.DateOnly), "total": apiTelStatOf(st.Prev)},
		"missed": map[string]any{"missed": m.Missed, "recalled": m.Recalled, "not_recalled": m.NotRecalled(),
			"recalled_pct": m.RecalledPct(), "avg_recall": m.AvgRecall(), "waiting": waiting}})
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
		// «Колл-трекинг»: the advertising channel whose number the line is (Маркетинг → «Рекламные источники»)
		AdSource string `json:"ad_source,omitempty"`
		// «Предупреждать о записи разговора» (RECORDING_NOTICE): the line plays its notice when recording is on
		RecordNotice bool `json:"record_notice"`
		// «Создавать в CRM» (CRM_CREATE, CRM_CREATE_CALL_TYPE): lead | deal | none ("" — the company's
		// setting) for in | out | all calls with a number unknown to CRM
		CRMCreate     string `json:"crm_create"`
		CRMCreateType string `json:"crm_create_type"`
	}
	out := make([]line, len(trunks))
	for i, t := range trunks {
		out[i] = line{ID: t.ID, Name: t.Name, Number: t.Number, CanCall: outgoing != "none" && (t.OutAll || slices.Contains(t.Out, p.UserID)),
			AdSource: t.AdSource, RecordNotice: t.RecNoteOn && t.RecNoteSum != "", CRMCreate: t.CRMCreate, CRMCreateType: t.CRMCreateType}
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out, "outgoing": outgoing})
	return nil
}

// apiTelUsers is voximplant.user.get: the employees with an inner number and whether a phone of theirs is on (their
// SIP phone or go24's softphone registered; "online" is missing while Asterisk does not answer) and why the clients'
// calls pass them by now («Кому не звонить»; "skip" missing — they ring).
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
	away, err := s.Telephony.Unavailable(ctx, time.Now())
	if err != nil {
		return err
	}
	type user struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Number string `json:"number"`
		Online *bool  `json:"online,omitempty"`
		Skip   string `json:"skip,omitempty"` // the clients' calls pass them by now: dnd | absent | workday
	}
	out := []user{}
	for _, l := range lines {
		if l.Dup {
			continue
		}
		u := user{ID: l.UserID, Name: l.Name, Number: l.Number, Skip: away[l.UserID]}
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

// apiTelSummary has CoPilot sum the conversation up again (through the AI of the key's owner; the record's text is made
// first if there is none) and gives the call with it.
func (s *Server) apiTelSummary(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
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
	if err := s.phoneSummarize(r.Context(), c, p.UserID); err != nil {
		return err
	}
	if c, err = s.apiTelFind(r, p); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiTelCallOf(c, p.UserID, true))
	return nil
}

// apiTelAssess has CoPilot score the conversation by a script ({"script": id}; through the AI of the key's owner, the
// record's text is made first if there is none) and gives the call with the score.
func (s *Server) apiTelAssess(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
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
	var in struct {
		Script string `json:"script"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sc, err := s.Telephony.ScriptByID(r.Context(), in.Script)
	if errors.Is(err, telephony.ErrNotFound) {
		return i18n.E("err.script_none")
	}
	if err != nil {
		return err
	}
	if err := s.phoneScore(r.Context(), c, sc, p.UserID); err != nil {
		return err
	}
	if c, err = s.apiTelFind(r, p); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiTelCallOf(c, p.UserID, true))
	return nil
}

// apiTelScripts are the scripts of CoPilot's call assessment.
func (s *Server) apiTelScripts(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Telephony == nil {
		return i18n.E("err.telephony_off")
	}
	list, err := s.Telephony.Scripts(r.Context())
	if err != nil {
		return err
	}
	type script struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Body  string `json:"body"`
		Calls string `json:"calls"` // all | in | out
		Auto  bool   `json:"auto"`  // scores its calls by itself
		Low   int    `json:"low"`   // a score below it is low, %
	}
	out := make([]script, len(list))
	for i, sc := range list {
		out[i] = script(sc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": out})
	return nil
}

// apiTelActive is «Текущие звонки»: the calls of the employees going on now that the key owner's right «monitor»
// shows, the longest first.
func (s *Server) apiTelActive(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.phoneActive(r.Context(), s.apiSess(r, p))
	if err != nil {
		return err
	}
	type call struct {
		Channel   string    `json:"channel"`
		User      apiUser   `json:"user"`
		Number    string    `json:"number"`
		Peer      string    `json:"peer"`
		PeerName  string    `json:"peer_name,omitempty"`
		Direction string    `json:"direction"`
		State     string    `json:"state"`
		StartedAt time.Time `json:"started_at"`
		Seconds   int       `json:"seconds"`
	}
	now := time.Now()
	out := make([]call, len(list))
	for i, a := range list {
		out[i] = call{Channel: a.Channel, User: apiUser{ID: a.UserID, Name: a.Name}, Number: a.Number, Peer: a.Peer,
			PeerName: a.PeerName, Direction: "in", State: "ringing", StartedAt: a.Since, Seconds: a.Seconds(now)}
		if a.Out {
			out[i].Direction = "out"
		}
		if a.Up {
			out[i].State = "talking"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
	return nil
}

// apiTelSpy joins a call going on: {channel, mode — listen, whisper or barge}; go24 rings the key owner's phones.
func (s *Server) apiTelSpy(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Channel string `json:"channel"`
		Mode    string `json:"mode"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Mode == "" {
		in.Mode = "listen"
	}
	if !slices.Contains(telephony.SpyModes, in.Mode) {
		return i18n.E("err.bad_form")
	}
	if err := s.phoneSpy(r.Context(), s.apiSess(r, p), in.Channel, in.Mode); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"calling": true, "channel": in.Channel, "mode": in.Mode})
	return nil
}

// apiTelPick takes a call ringing for a colleague: {channel}; go24 rings the key owner's phones.
func (s *Server) apiTelPick(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Channel string `json:"channel"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.phonePick(r.Context(), s.apiSess(r, p), in.Channel); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"calling": true, "channel": in.Channel})
	return nil
}

// apiTelFill has CoPilot fill the client's CRM card from the conversation (through the AI of the key's owner, who must
// be able to change the card) and tells what it filled and offered.
func (s *Server) apiTelFill(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	c, err := s.apiTelFind(r, p)
	if err != nil {
		return err
	}
	if c.Record == "" || s.Telephony.Records == "" || s.CRM == nil {
		return i18n.E("err.not_found")
	}
	sess := s.apiSess(r, p)
	if !s.phoneHears(r.Context(), sess, c) {
		return i18n.E("err.forbidden")
	}
	entity, id, err := s.phoneFillCheck(r, sess, c)
	if err != nil {
		return err
	}
	fill, err := s.phoneFillCard(r.Context(), c, p.UserID, entity, id)
	if err != nil {
		return err
	}
	type item struct {
		Field string `json:"field"` // amount, comment, …, «uf:» and the id of a custom field
		Name  string `json:"name,omitempty"`
		Old   string `json:"old"`
		New   string `json:"new"`
		State string `json:"state"` // filled (was empty) | offered (waits in the card's history)
	}
	items := []item{}
	for _, it := range fill.Items {
		items = append(items, item{it.Field, it.Name, it.Old, it.New, it.State})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entity": entity, "id": id, "fields": items})
	return nil
}
