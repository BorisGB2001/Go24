package web

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/linkpreview"
	"go24/internal/telephony"
)

// REST compatible with the portal, telephony (GO24DEV-921): telephony.externalcall.* — an outside PBX (a cloud PBX, a
// SIP provider's) tells go24 of its calls: register when one starts (the client is found in CRM or a lead is made,
// the employee is shown the call), finish when it ends (the call goes into the log, the client's timeline and the
// events, as go24's own calls do), attachRecord; telephony.externalLine.* — its lines; voximplant.statistic.get — the
// log by «Права доступа». A call of the log has a number (migration 840), CALL_ID is its uniqueid. Employees and
// CRM cards are their numbers.

func init() {
	for name, m := range map[string]portalMethod{
		"telephony.externalcall.register":          {Scope: "telephony:write", Run: (*Server).portalTelRegister},
		"telephony.externalcall.finish":            {Scope: "telephony:write", Run: (*Server).portalTelFinish},
		"telephony.externalcall.attachrecord":      {Scope: "telephony:write", Run: (*Server).portalTelAttach},
		"telephony.externalcall.show":              {Scope: "telephony:write", Run: (*Server).portalTelShow},
		"telephony.externalcall.hide":              {Scope: "telephony:write", Run: (*Server).portalTelHide},
		"telephony.externalcall.searchcrmentities": {Scope: "telephony:read", Run: (*Server).portalTelSearch},
		"telephony.externalline.get":               {Scope: "telephony:read", Run: (*Server).portalTelLines},
		"telephony.externalline.add":               {Scope: "telephony:write", Run: (*Server).portalTelLineAdd},
		"telephony.externalline.update":            {Scope: "telephony:write", Run: (*Server).portalTelLineUpdate},
		"telephony.externalline.delete":            {Scope: "telephony:write", Run: (*Server).portalTelLineDelete},
		"voximplant.statistic.get":                 {Scope: "telephony:read", Run: (*Server).portalTelStatistic},
	} {
		portalMethods[name] = m
	}
}

// maxExtRecord is the largest record of an outside call.
const maxExtRecord = 100 << 20

// recordClient loads records of outside calls from the internet only (tests put their own).
var recordClient = &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: linkpreview.PublicOnly}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second,
}}

// portalCRMKinds: a card's kind → go24's.
var portalCRMKinds = map[string]string{"CONTACT": "contact", "COMPANY": "company", "LEAD": "lead", "DEAL": "deal"}

func portalCRMTable(entity string) string {
	return map[string]string{"contact": "crm_contacts", "company": "crm_companies", "lead": "crm_leads", "deal": "crm_deals"}[entity]
}

// portalTelOn: telephony is there; admin — the key's owner may change its settings.
func (s *Server) portalTelOn(c portalCall) (admin bool, err error) {
	if s.Telephony == nil {
		return false, i18n.E("err.telephony_off")
	}
	return s.telAdmin(c.r.Context(), s.apiSess(c.r, c.p)), nil
}

// portalTelUser is the employee of the call: USER_ID (a number) or USER_PHONE_INNER; the key's owner or, for a key of
// who may change telephony's settings, anyone.
func (s *Server) portalTelUser(c portalCall, admin bool, v any) (string, error) {
	var id string
	var err error
	if x := portalStr(v); x != "" {
		id, err = s.portalUserID(c, x)
	} else if inner := portalStr(portalParam(c.in, "user_phone_inner")); inner != "" {
		err = s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM users WHERE inner_phone = $1 AND active AND NOT guest
			ORDER BY created_at, id LIMIT 1`, inner).Scan(&id)
	} else {
		return "", i18n.E("tel.err_param", "USER_ID")
	}
	if err != nil {
		return "", i18n.E("err.not_found")
	}
	if id != c.p.UserID && !admin {
		return "", i18n.E("err.forbidden")
	}
	return id, nil
}

// portalTelCall is the call registered of CALL_ID, for its employee or who may change telephony's settings.
func (s *Server) portalTelCall(c portalCall, admin bool) (telephony.External, error) {
	e, err := s.Telephony.ExternalCall(c.r.Context(), portalStr(portalParam(c.in, "call_id")))
	if errors.Is(err, telephony.ErrNotFound) {
		return e, i18n.E("tel.err_ext_call")
	}
	if err == nil && e.UserID != c.p.UserID && !admin {
		return e, i18n.E("err.forbidden")
	}
	return e, err
}

// portalYes reads a flag (1, Y, true); def — when it is not given.
func portalYes(in map[string]any, name string, def bool) bool {
	v, ok := portalParamOK(in, name)
	if !ok || portalStr(v) == "" {
		return def
	}
	return slices.Contains([]string{"1", "y", "true"}, strings.ToLower(portalStr(v)))
}

// telephony.externalcall.register: USER_ID or USER_PHONE_INNER, PHONE_NUMBER, TYPE (1 outgoing, 2 incoming, 3 incoming
// redirected, 4 callback, 5 info), CALL_START_DATE, LINE_NUMBER, CRM_CREATE (1 — an unknown number makes a lead, as
// the line allows), CRM_SOURCE, CRM_ENTITY_TYPE and CRM_ENTITY_ID (the card, when the PBX knows it), SHOW (1 — the
// employee is shown the call).
func (s *Server) portalTelRegister(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	admin, err := s.portalTelOn(c)
	if err != nil {
		return portalOut{}, err
	}
	user, err := s.portalTelUser(c, admin, portalParam(c.in, "user_id"))
	if err != nil {
		return portalOut{}, err
	}
	e := telephony.External{UserID: user, Number: telephony.Dialable(portalStr(portalParam(c.in, "phone_number"))),
		Line: portalStr(portalParam(c.in, "line_number")), Type: telephony.ExtOut}
	if e.Number == "" {
		return portalOut{}, i18n.E("tel.err_param", "PHONE_NUMBER")
	}
	if t := portalStr(portalParam(c.in, "type")); t != "" {
		if e.Type, err = strconv.Atoi(t); err != nil || e.Type < telephony.ExtOut || e.Type > telephony.ExtInfo {
			return portalOut{}, i18n.E("tel.err_param", "TYPE")
		}
	}
	if t, err := portalDateTime(portalStr(portalParam(c.in, "call_start_date"))); err != nil {
		return portalOut{}, i18n.E("tel.err_param", "CALL_START_DATE")
	} else if t != nil {
		e.Started = *t
	}
	if kind := portalCRMKinds[strings.ToUpper(portalStr(portalParam(c.in, "crm_entity_type")))]; kind != "" {
		if e.CRMID, err = s.portalNum(ctx, portalCRMTable(kind), portalParam(c.in, "crm_entity_id")); err != nil {
			return portalOut{}, err
		}
		if e.CRMID != "" {
			e.CRMEntity = kind
		}
	}
	if e.CRMEntity == "" {
		if e.CRMEntity, e.CRMID, _, err = s.phoneClient(ctx, e.Number); err != nil {
			return portalOut{}, err
		}
	}
	if e.CRMEntity == "" && s.CRM != nil && portalYes(c.in, "crm_create", true) {
		auto, err := s.Telephony.ExtLineAutoCreate(ctx, e.Line)
		if err != nil {
			return portalOut{}, err
		}
		if auto {
			origin := portalStr(portalParam(c.in, "crm_source"))
			if origin == "" {
				origin = "CALL"
			}
			if e.CRMID, err = s.CRM.SaveLead(ctx, user, crm.Lead{Title: s.portalTr("tel.lead_title", e.Number), Phone: e.Number,
				Source: s.portalTr("tel.lead_source"), Origin: crm.Dict{Code: origin}, Responsible: crm.Ref{ID: user}}); err != nil {
				return portalOut{}, err
			}
			e.CRMEntity, e.LeadCreated = "lead", true
		}
	}
	if err := s.Telephony.RegisterExternal(ctx, &e); err != nil {
		return portalOut{}, err
	}
	if portalYes(c.in, "show", true) {
		if err := s.portalTelNotify(ctx, &e, []string{user}); err != nil {
			return portalOut{}, err
		}
	}
	out := map[string]any{"CALL_ID": e.CallID, "CRM_CREATED_LEAD": nil, "CRM_CREATED_ENTITIES": []any{},
		"CRM_ENTITY_TYPE": "", "CRM_ENTITY_ID": nil}
	if e.CRMEntity != "" {
		n, err := s.portalNums(ctx, portalCRMTable(e.CRMEntity), []string{e.CRMID})
		if err != nil {
			return portalOut{}, err
		}
		out["CRM_ENTITY_TYPE"], out["CRM_ENTITY_ID"] = strings.ToUpper(e.CRMEntity), n[e.CRMID]
		if e.LeadCreated {
			out["CRM_CREATED_LEAD"] = n[e.CRMID]
			out["CRM_CREATED_ENTITIES"] = []any{map[string]any{"ENTITY_TYPE": "LEAD", "ENTITY_ID": n[e.CRMID]}}
		}
	}
	return portalOut{Result: out}, nil
}

// portalTelNotify shows the employees the call in the bell (the portal opens its call card): who calls or is called, the
// link to the client's card.
func (s *Server) portalTelNotify(ctx context.Context, e *telephony.External, users []string) error {
	who, link := e.Number, "/telephony/calls?q="+url.QueryEscape(e.Number)
	if e.CRMEntity != "" {
		if _, _, name, err := s.phoneClient(ctx, e.Number); err == nil && name != "" {
			who += " (" + name + ")"
		}
		link = "/crm/" + map[string]string{"contact": "contacts", "company": "companies", "lead": "leads", "deal": "deals"}[e.CRMEntity] + "/" + e.CRMID
	}
	key := "tel.notify.ext_out"
	if e.Incoming() {
		key = "tel.notify.ext_in"
	}
	for _, u := range users {
		var id int64
		err := s.DB.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'phone', left($2, 300), $3) RETURNING id`,
			u, s.portalTr(key, who), link).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) { // the employee's settings turned it off
			return err
		}
		if id != 0 {
			e.Notify = append(e.Notify, id)
		}
	}
	return s.Telephony.SetExternalNotify(ctx, e.CallID, e.Notify)
}

// portalTelUsers are the employees of USER_ID (one or a list; none — the call's own).
func (s *Server) portalTelUsers(c portalCall, admin bool, e telephony.External) ([]string, error) {
	nums := portalList(portalParam(c.in, "user_id"))
	if len(nums) == 0 {
		return []string{e.UserID}, nil
	}
	var out []string
	for _, n := range nums {
		id, err := s.portalTelUser(c, admin, n)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// telephony.externalcall.show: CALL_ID, USER_ID — the call is shown to the employees (again).
func (s *Server) portalTelShow(c portalCall) (portalOut, error) {
	admin, err := s.portalTelOn(c)
	if err != nil {
		return portalOut{}, err
	}
	e, err := s.portalTelCall(c, admin)
	if err != nil {
		return portalOut{}, err
	}
	users, err := s.portalTelUsers(c, admin, e)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalTelHideFrom(c.r.Context(), &e, users); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.portalTelNotify(c.r.Context(), &e, users)
}

// telephony.externalcall.hide: CALL_ID, USER_ID — the call is no longer shown to the employees.
func (s *Server) portalTelHide(c portalCall) (portalOut, error) {
	admin, err := s.portalTelOn(c)
	if err != nil {
		return portalOut{}, err
	}
	e, err := s.portalTelCall(c, admin)
	if err != nil {
		return portalOut{}, err
	}
	users, err := s.portalTelUsers(c, admin, e)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalTelHideFrom(c.r.Context(), &e, users); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Telephony.SetExternalNotify(c.r.Context(), e.CallID, e.Notify)
}

// portalTelHideFrom removes the call's notifications of the employees (nil — of all).
func (s *Server) portalTelHideFrom(ctx context.Context, e *telephony.External, users []string) error {
	rows, err := s.DB.Query(ctx, `DELETE FROM notifications WHERE id = ANY($1) AND ($2::uuid[] IS NULL OR user_id = ANY($2::uuid[])) RETURNING id`,
		e.Notify, users)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		e.Notify = slices.DeleteFunc(e.Notify, func(x int64) bool { return x == id })
	}
	return rows.Err()
}

// telephony.externalcall.finish: CALL_ID, USER_ID (who talked; none — the employee of register), DURATION (seconds),
// STATUS_CODE (CALL_FAILED_CODE: 200 answered, 304 missed, 486 busy, 603 declined, …), FAILED_REASON, COST,
// COST_CURRENCY, RECORD_URL, VOTE (1–5). The call goes into the log, the client's card and the events; the result is
// the call as voximplant.statistic.get gives it.
func (s *Server) portalTelFinish(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	admin, err := s.portalTelOn(c)
	if err != nil {
		return portalOut{}, err
	}
	e, err := s.portalTelCall(c, admin)
	if err != nil {
		return portalOut{}, err
	}
	f := telephony.Finish{Code: portalStr(portalParam(c.in, "status_code")), Reason: portalStr(portalParam(c.in, "failed_reason")),
		Currency: portalStr(portalParam(c.in, "cost_currency"))}
	if v := portalStr(portalParam(c.in, "user_id")); v != "" {
		if f.UserID, err = s.portalTelUser(c, admin, v); err != nil {
			return portalOut{}, err
		}
	}
	for name, dst := range map[string]*int{"duration": &f.Seconds, "vote": &f.Rating} {
		if v := portalStr(portalParam(c.in, name)); v != "" {
			if *dst, err = strconv.Atoi(v); err != nil || *dst < 0 {
				return portalOut{}, i18n.E("tel.err_param", strings.ToUpper(name))
			}
		}
	}
	if v := portalStr(portalParam(c.in, "cost")); v != "" {
		if f.Cost, err = strconv.ParseFloat(v, 64); err != nil || f.Cost < 0 {
			return portalOut{}, i18n.E("tel.err_param", "COST")
		}
	}
	if link := portalStr(portalParam(c.in, "record_url")); link != "" {
		if f.Record, err = s.portalTelLoad(ctx, link, ""); err != nil {
			return portalOut{}, err
		}
	}
	if err := s.portalTelHideFrom(ctx, &e, nil); err != nil {
		return portalOut{}, err
	}
	id, err := s.Telephony.FinishExternal(ctx, e, f)
	if err != nil {
		return portalOut{}, err
	}
	call, err := s.Telephony.CallByID(ctx, id)
	if err != nil {
		return portalOut{}, err
	}
	if call.CRMEntity != "" && s.CRM != nil {
		_, _, emp := call.Outside()
		if err := s.phoneBind(ctx, call, call.CRMEntity, call.CRMID, emp); err != nil {
			return portalOut{}, err
		}
		if err := s.phoneTrigger(ctx, call, call.CRMEntity, call.CRMID); err != nil {
			s.Log.Error("telephony trigger", "err", err, "call", id)
		}
	}
	if err := s.phoneMissed(ctx, id); err != nil {
		s.Log.Error("telephony notify", "err", err, "call", id)
	}
	if err := s.phoneEvents(ctx, id); err != nil {
		s.Log.Error("telephony events", "err", err, "call", id)
	}
	s.portalTelTranscribe(ctx, call)
	rows, err := s.portalTelRows(c, []string{id})
	if err != nil || len(rows) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: rows[0]}, nil
}

// portalTelTranscribe makes the text of a new record when the administrators turned it on.
func (s *Server) portalTelTranscribe(ctx context.Context, call telephony.Call) {
	if call.Record == "" || call.Status != telephony.StatusAnswered || s.Speech == nil {
		return
	}
	if on, err := s.Telephony.Transcribing(ctx); err != nil {
		s.Log.Error("telephony transcribing", "err", err)
	} else if on {
		go s.phoneTranscribeLater(call.ID)
	}
}

// portalTelLoad puts a record into Records: the file's content, or loaded from the link (of the internet only); its
// name there.
func (s *Server) portalTelLoad(ctx context.Context, link, content string, name ...string) (string, error) {
	if s.Telephony.Records == "" {
		return "", i18n.E("tel.err_records")
	}
	ext := ""
	if len(name) > 0 {
		ext = strings.ToLower(filepath.Ext(name[0]))
	}
	var data []byte
	if link != "" {
		u, err := linkpreview.Check(link)
		if err != nil {
			return "", i18n.E("tel.err_param", "RECORD_URL")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", err
		}
		res, err := recordClient.Do(req)
		if err != nil {
			return "", i18n.E("tel.err_record_load", err.Error())
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return "", i18n.E("tel.err_record_load", res.Status)
		}
		if data, err = io.ReadAll(io.LimitReader(res.Body, maxExtRecord+1)); err != nil {
			return "", i18n.E("tel.err_record_load", err.Error())
		}
		if ext == "" {
			ext = strings.ToLower(filepath.Ext(u.Path))
		}
	} else {
		var err error
		if data, err = base64.StdEncoding.DecodeString(content); err != nil || len(data) == 0 {
			return "", i18n.E("tel.err_param", "FILE_CONTENT")
		}
	}
	if len(data) > maxExtRecord {
		return "", i18n.E("tel.err_record_size")
	}
	if !slices.Contains([]string{".mp3", ".wav", ".ogg", ".m4a"}, ext) {
		ext = ".mp3"
	}
	file := "ext-" + randomHex(12) + ext
	if err := os.WriteFile(filepath.Join(s.Telephony.Records, file), data, 0o640); err != nil {
		return "", err
	}
	return file, nil
}

// telephony.externalcall.attachRecord: CALL_ID, FILENAME and FILE_CONTENT (base64), or RECORD_URL — the call's record,
// finished or not yet.
func (s *Server) portalTelAttach(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	admin, err := s.portalTelOn(c)
	if err != nil {
		return portalOut{}, err
	}
	callID := portalStr(portalParam(c.in, "call_id"))
	list, err := s.portalTelLogged(c, callID)
	if err != nil {
		return portalOut{}, err
	}
	if len(list) == 0 { // not finished yet
		if _, err := s.portalTelCall(c, admin); err != nil {
			return portalOut{}, err
		}
	} else if who := list[0]; who.FromUser.UserID != c.p.UserID && who.ToUser.UserID != c.p.UserID && !admin {
		return portalOut{}, i18n.E("err.forbidden")
	}
	link, content := portalStr(portalParam(c.in, "record_url")), portalStr(portalParam(c.in, "file_content"))
	if link == "" && content == "" {
		return portalOut{}, i18n.E("tel.err_param", "FILE_CONTENT")
	}
	file, err := s.portalTelLoad(ctx, link, content, portalStr(portalParam(c.in, "filename")))
	if err != nil {
		return portalOut{}, err
	}
	old, err := s.Telephony.AttachRecord(ctx, callID, file)
	if err != nil {
		os.Remove(filepath.Join(s.Telephony.Records, file))
		if errors.Is(err, telephony.ErrNotFound) {
			return portalOut{}, i18n.E("tel.err_ext_call")
		}
		return portalOut{}, err
	}
	if old != "" && old != file && strings.HasPrefix(old, "ext-") {
		os.Remove(filepath.Join(s.Telephony.Records, filepath.Base(old)))
	}
	out := map[string]any{"CALL_ID": callID, "FILE_NAME": file}
	if len(list) > 0 {
		call, err := s.Telephony.CallByID(ctx, list[0].ID)
		if err != nil {
			return portalOut{}, err
		}
		s.portalTelTranscribe(ctx, call)
		out["CALL_RECORD_URL"] = strings.TrimRight(s.BaseURL, "/") + "/telephony/calls/" + call.ID + "/record"
	}
	return portalOut{Result: out}, nil
}

// portalTelLogged is the outside call of the log with the CALL_ID (none — not finished, or not there).
func (s *Server) portalTelLogged(c portalCall, callID string) ([]telephony.Call, error) {
	var id string
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM telephony_calls WHERE uniqueid = $1 AND external`, callID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || callID == "" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.Telephony.Calls(c.r.Context(), telephony.CallFilter{ID: id, Limit: 1})
}

// telephony.externalCall.searchCrmEntities: PHONE_NUMBER — the CRM cards with the phone the key's owner sees.
func (s *Server) portalTelSearch(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	if _, err := s.portalTelOn(c); err != nil {
		return portalOut{}, err
	}
	number := telephony.Dialable(portalStr(portalParam(c.in, "phone_number")))
	if number == "" {
		return portalOut{}, i18n.E("tel.err_param", "PHONE_NUMBER")
	}
	out := []map[string]any{}
	if s.CRM == nil {
		return portalOut{Result: out}, nil
	}
	list, err := s.CRM.Duplicates(ctx, "lead", "", "", number, "")
	if err != nil {
		return portalOut{}, err
	}
	for _, kind := range []string{"contact", "company", "lead", "deal"} {
		for _, d := range list {
			if d.Entity != kind || d.By != "phone" || !s.crmCanRead(ctx, c.p.UserID, d.Entity, d.ID) {
				continue
			}
			n, err := s.portalNums(ctx, portalCRMTable(d.Entity), []string{d.ID})
			if err != nil {
				return portalOut{}, err
			}
			row := map[string]any{"CRM_ENTITY_TYPE": strings.ToUpper(d.Entity), "CRM_ENTITY_ID": n[d.ID], "NAME": d.Title,
				"ASSIGNED_BY_ID": nil, "ASSIGNED_BY": nil}
			if owner, err := s.CRM.Responsible(ctx, d.Entity, d.ID); err == nil && owner != "" {
				var num int64
				var inner string
				if err := s.DB.QueryRow(ctx, `SELECT num, coalesce(inner_phone, '') FROM users WHERE id = $1`, owner).Scan(&num, &inner); err == nil {
					row["ASSIGNED_BY_ID"] = num
					row["ASSIGNED_BY"] = map[string]any{"ID": num, "USER_PHONE_INNER": inner}
				}
			}
			out = append(out, row)
		}
	}
	return portalOut{Result: out}, nil
}

// telephony.externalLine.get: the lines of the outside PBX.
func (s *Server) portalTelLines(c portalCall) (portalOut, error) {
	if _, err := s.portalTelOn(c); err != nil {
		return portalOut{}, err
	}
	list, err := s.Telephony.ExtLines(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	out := make([]map[string]any, len(list))
	for i, l := range list {
		yn := "N"
		if l.CRMAutoCreate {
			yn = "Y"
		}
		out[i] = map[string]any{"NUMBER": l.Number, "NAME": l.Name, "CRM_AUTO_CREATE": yn}
	}
	return portalOut{Result: out}, nil
}

// portalTelLineSave is telephony.externalLine.add (add) and update: NUMBER, NAME, CRM_AUTO_CREATE (Y/N) — by who may
// change telephony's settings.
func (s *Server) portalTelLineSave(c portalCall, add bool) (portalOut, error) {
	admin, err := s.portalTelOn(c)
	if err != nil {
		return portalOut{}, err
	}
	if !admin {
		return portalOut{}, i18n.E("err.forbidden")
	}
	number := strings.TrimSpace(portalStr(portalParam(c.in, "number")))
	if number == "" || len(number) > 64 {
		return portalOut{}, i18n.E("tel.err_param", "NUMBER")
	}
	var name *string
	if v, ok := portalParamOK(c.in, "name"); ok {
		x := strings.TrimSpace(portalStr(v))
		name = &x
	}
	var auto *bool
	if _, ok := portalParamOK(c.in, "crm_auto_create"); ok {
		x := portalYes(c.in, "crm_auto_create", true)
		auto = &x
	}
	if err := s.Telephony.SaveExtLine(c.r.Context(), number, name, auto, add); errors.Is(err, telephony.ErrNotFound) {
		return portalOut{}, i18n.E("err.not_found")
	} else if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"ID": number}}, nil
}

func (s *Server) portalTelLineAdd(c portalCall) (portalOut, error) {
	return s.portalTelLineSave(c, true)
}
func (s *Server) portalTelLineUpdate(c portalCall) (portalOut, error) {
	return s.portalTelLineSave(c, false)
}

// telephony.externalLine.delete: NUMBER.
func (s *Server) portalTelLineDelete(c portalCall) (portalOut, error) {
	admin, err := s.portalTelOn(c)
	if err != nil {
		return portalOut{}, err
	}
	if !admin {
		return portalOut{}, i18n.E("err.forbidden")
	}
	if err := s.Telephony.DeleteExtLine(c.r.Context(), strings.TrimSpace(portalStr(portalParam(c.in, "number")))); errors.Is(err, telephony.ErrNotFound) {
		return portalOut{}, i18n.E("err.not_found")
	} else if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// portalTelMax is how many calls voximplant.statistic.get looks through, newest first.
const portalTelMax = 5000

// voximplant.statistic.get: the log by «Права доступа» (the calls the key's owner sees) «Детализация
// звонков»; filter by the fields (>=CALL_START_DATE and <=CALL_START_DATE — the period), order, select, start.
func (s *Server) portalTelStatistic(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	perms, err := s.apiTelOn(ctx, c.p, c.r)
	if err != nil {
		return portalOut{}, err
	}
	f := telephony.CallFilter{Limit: portalTelMax}
	filter := portalMap(portalParam(c.in, "filter"))
	for k, v := range filter {
		key := strings.ToUpper(k)
		if strings.TrimLeft(key, "<>=") != "CALL_START_DATE" || key == "CALL_START_DATE" {
			continue
		}
		t, err := portalDateTime(portalStr(v))
		if err != nil || t == nil {
			return portalOut{}, i18n.E("tel.err_param", key)
		}
		if strings.HasPrefix(key, ">") {
			f.From = *t
		} else {
			f.To = t.Add(time.Second)
		}
		delete(filter, k)
	}
	if f.UserID, f.Users, err = s.telScope(ctx, s.apiSess(c.r, c.p), perms["calls"], ""); err != nil {
		return portalOut{}, err
	}
	list, err := s.Telephony.Calls(ctx, f)
	if err != nil {
		return portalOut{}, err
	}
	rows, err := s.portalTelRowsOf(c, list, perms)
	if err != nil {
		return portalOut{}, err
	}
	c.in = portalWith(c.in, "filter", filter)
	return portalRowsPage(c, rows), nil
}

// portalWith is the parameters with one of them replaced.
func portalWith(in map[string]any, name string, v any) map[string]any {
	out := map[string]any{}
	for k, x := range in {
		if !strings.EqualFold(k, name) {
			out[k] = x
		}
	}
	out[name] = v
	return out
}

// portalTelRows are the calls of the log by their ids as voximplant.statistic.get gives them.
func (s *Server) portalTelRows(c portalCall, ids []string) ([]map[string]any, error) {
	var list []telephony.Call
	for _, id := range ids {
		got, err := s.Telephony.Calls(c.r.Context(), telephony.CallFilter{ID: id, Limit: 1})
		if err != nil {
			return nil, err
		}
		list = append(list, got...)
	}
	return s.portalTelRowsOf(c, list, s.telPerms(c.r.Context(), s.apiSess(c.r, c.p)))
}

// portalTelRowsOf are the calls as rows of voximplant.statistic.get; the record's link — for who may listen to it.
func (s *Server) portalTelRowsOf(c portalCall, list []telephony.Call, perms telephony.Perms) ([]map[string]any, error) {
	ctx := c.r.Context()
	sess := s.apiSess(c.r, c.p)
	ids, users, cards := []string{}, []string{}, map[string][]string{}
	for _, x := range list {
		ids = append(ids, x.ID)
		if _, _, emp := x.Outside(); emp != "" {
			users = append(users, emp)
		} else if x.FromUser.UserID != "" {
			users = append(users, x.FromUser.UserID)
		}
		if x.CRMEntity != "" {
			cards[x.CRMEntity] = append(cards[x.CRMEntity], x.CRMID)
		}
	}
	extra, err := s.Telephony.CallExtras(ctx, ids)
	if err != nil {
		return nil, err
	}
	nums, err := s.portalNums(ctx, "users", users)
	if err != nil {
		return nil, err
	}
	cardNums := map[string]int64{}
	for kind, l := range cards {
		got, err := s.portalNums(ctx, portalCRMTable(kind), l)
		if err != nil {
			return nil, err
		}
		for k, v := range got {
			cardNums[k] = v
		}
	}
	var team []string
	if perms["records"] == "dept" {
		if team, err = s.Telephony.Team(ctx, sess.UserID); err != nil {
			return nil, err
		}
	}
	base := strings.TrimRight(s.BaseURL, "/")
	rows := make([]map[string]any, 0, len(list))
	for _, x := range list {
		number, dir, emp := x.Outside()
		if emp == "" {
			emp, number = x.FromUser.UserID, x.To
		}
		typ := 1
		if dir == "in" {
			typ = 2
		}
		ex := extra[x.ID]
		code := ex.Code
		if code == "" {
			code = map[string]string{telephony.StatusAnswered: "200", telephony.StatusBusy: "486", telephony.StatusFailed: "603"}[x.Status]
			if code == "" {
				code = "304"
			}
		}
		row := map[string]any{"ID": ex.Num, "CALL_ID": ex.UniqueID, "PORTAL_USER_ID": nil, "PORTAL_NUMBER": x.Line,
			"PHONE_NUMBER": number, "CALL_TYPE": typ, "CALL_DURATION": x.Seconds, "CALL_START_DATE": x.Started.Format(time.RFC3339),
			"CALL_STATUS": x.Status, "CALL_FAILED_CODE": code, "CALL_FAILED_REASON": ex.Reason, "CALL_VOTE": x.Rating,
			"COST": ex.Cost, "COST_CURRENCY": ex.Currency, "CALL_RECORD_URL": "", "RECORD_FILE_ID": nil,
			"CRM_ENTITY_TYPE": "", "CRM_ENTITY_ID": nil, "CRM_ACTIVITY_ID": nil, "COMMENT": x.Comment, "TRANSCRIPT_PENDING": "N"}
		if n, ok := nums[emp]; ok {
			row["PORTAL_USER_ID"] = n
		}
		if x.CRMEntity != "" {
			row["CRM_ENTITY_TYPE"], row["CRM_ENTITY_ID"] = strings.ToUpper(x.CRMEntity), cardNums[x.CRMID]
		}
		if x.Record != "" && s.Telephony.Records != "" && phoneHearsBy(perms, sess, x, team, nil) {
			row["CALL_RECORD_URL"] = base + "/telephony/calls/" + x.ID + "/record"
		}
		rows = append(rows, row)
	}
	return rows, nil
}
