package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go24/internal/apps"
	"go24/internal/bp"
)

// REST compatible with the portal, the actions of applications for the workflow designer (GO24DEV-903):
// bizproc.activity.add, .update, .delete, .list, .log and bizproc.event.send by their names, for the token of an
// application; adding and changing — of an administrator.

func init() {
	for name, m := range map[string]portalMethod{
		"bizproc.activity.add":    {Scope: "bp:write", Run: (*Server).portalBPActivityAdd},
		"bizproc.activity.update": {Scope: "bp:write", Run: (*Server).portalBPActivityUpdate},
		"bizproc.activity.delete": {Scope: "bp:write", Run: (*Server).portalBPActivityDelete},
		"bizproc.activity.list":   {Scope: "bp:read", Run: (*Server).portalBPActivityList},
		"bizproc.activity.log":    {Scope: "bp:write", Run: (*Server).portalBPActivityLog},
		"bizproc.event.send":      {Scope: "bp:write", Run: (*Server).portalBPEventSend},
	} {
		portalMethods[name] = m
	}
}

// portalLang is a text that is either a string or {"ru": …, "en": …}: Russian, else English, else any.
func portalLang(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return portalStr(v)
	}
	for _, l := range []string{"ru", "en"} {
		if s := portalStr(portalParam(m, l)); s != "" {
			return s
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if s := portalStr(m[k]); s != "" {
			return s
		}
	}
	return ""
}

// portalProps are PROPERTIES (RETURN_PROPERTIES): {key: {Name, Description, Type, Required, Multiple,
// Default, Options {key: text}}}, by their keys.
func portalProps(v any) []apps.Prop {
	m, _ := v.(map[string]any)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := []apps.Prop{}
	for _, k := range keys {
		p, _ := m[k].(map[string]any)
		prop := apps.Prop{Key: k, Name: portalLang(portalParam(p, "Name")), Description: portalLang(portalParam(p, "Description")),
			Type: portalStr(portalParam(p, "Type")), Required: portalStr(portalParam(p, "Required")) == "Y",
			Multiple: portalStr(portalParam(p, "Multiple")) == "Y", Default: portalStr(portalParam(p, "Default"))}
		if prop.Type == "" {
			prop.Type = "string"
		}
		if o, ok := portalParam(p, "Options").(map[string]any); ok {
			ok := make([]string, 0, len(o))
			for k := range o {
				ok = append(ok, k)
			}
			slices.Sort(ok)
			for _, k := range ok {
				prop.Options = append(prop.Options, apps.Option{Key: k, Value: portalStr(o[k])})
			}
		}
		out = append(out, prop)
	}
	return out
}

// portalActivityIn is the action of the parameters (of FIELDS for update).
func (s *Server) portalActivityIn(c portalCall, in map[string]any) (apps.Activity, error) {
	act := apps.Activity{Code: portalStr(portalParam(c.in, "CODE")), Handler: portalStr(portalParam(in, "HANDLER")),
		Subscribe: portalStr(portalParam(in, "USE_SUBSCRIPTION")) == "Y", Name: portalLang(portalParam(in, "NAME")),
		Description: portalLang(portalParam(in, "DESCRIPTION")), Props: portalProps(portalParam(in, "PROPERTIES")),
		Returns: portalProps(portalParam(in, "RETURN_PROPERTIES"))}
	if v := portalParam(in, "AUTH_USER_ID"); portalStr(v) != "" && portalStr(v) != "0" {
		user, err := s.portalNum(c.r.Context(), "users", v)
		if err != nil || user == "" {
			return act, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ACTIVITY_VALIDATION_FAILURE", "err.app_activity_user")
		}
		act.UserID = user
	}
	return act, nil
}

func (s *Server) portalActivityErr(c portalCall, err error) error {
	switch {
	case errors.Is(err, apps.ErrActivityExists):
		return s.portalErr(c.r, http.StatusBadRequest, "ERROR_ACTIVITY_ALREADY_INSTALLED", "err.app_activity_exists")
	case errors.Is(err, apps.ErrActivityMissing):
		return s.portalErr(c.r, http.StatusBadRequest, "ERROR_ACTIVITY_NOT_FOUND", "err.app_activity_missing")
	}
	var be *portalError
	if err != nil && !errors.As(err, &be) {
		be = s.portalErrorOf(c.r, err)
		if be.Code == "ERROR_CORE" {
			be.Code = "ERROR_ACTIVITY_VALIDATION_FAILURE"
		}
		return be
	}
	return err
}

// bizproc.activity.add: CODE, HANDLER, AUTH_USER_ID, USE_SUBSCRIPTION, NAME, DESCRIPTION, PROPERTIES,
// RETURN_PROPERTIES → true.
func (s *Server) portalBPActivityAdd(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	act, err := s.portalActivityIn(c, c.in)
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.Apps.SaveActivity(c.r.Context(), a, act, false); err != nil {
		return portalOut{}, s.portalActivityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// bizproc.activity.update: CODE, FIELDS {the parameters of add} → true; what FIELDS leaves out stays.
func (s *Server) portalBPActivityUpdate(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	fields, _ := portalParam(c.in, "FIELDS").(map[string]any)
	code := portalStr(portalParam(c.in, "CODE"))
	list, err := s.Apps.AppActivities(c.r.Context(), a.ID)
	if err != nil {
		return portalOut{}, err
	}
	i := slices.IndexFunc(list, func(x apps.Activity) bool { return x.Code == code })
	if i < 0 {
		return portalOut{}, s.portalActivityErr(c, apps.ErrActivityMissing)
	}
	act, err := s.portalActivityIn(c, fields)
	if err != nil {
		return portalOut{}, err
	}
	old := list[i]
	if portalParam(fields, "HANDLER") == nil {
		act.Handler = old.Handler
	}
	if portalParam(fields, "AUTH_USER_ID") == nil {
		act.UserID = old.UserID
	}
	if portalParam(fields, "USE_SUBSCRIPTION") == nil {
		act.Subscribe = old.Subscribe
	}
	if portalParam(fields, "NAME") == nil {
		act.Name = old.Name
	}
	if portalParam(fields, "DESCRIPTION") == nil {
		act.Description = old.Description
	}
	if portalParam(fields, "PROPERTIES") == nil {
		act.Props = old.Props
	}
	if portalParam(fields, "RETURN_PROPERTIES") == nil {
		act.Returns = old.Returns
	}
	if _, err := s.Apps.SaveActivity(c.r.Context(), a, act, true); err != nil {
		return portalOut{}, s.portalActivityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// bizproc.activity.delete: CODE → true.
func (s *Server) portalBPActivityDelete(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.DeleteActivity(c.r.Context(), a, portalStr(portalParam(c.in, "CODE"))); err != nil {
		return portalOut{}, s.portalActivityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// bizproc.activity.list: the codes of the application's actions.
func (s *Server) portalBPActivityList(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, false)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Apps.AppActivities(c.r.Context(), a.ID)
	if err != nil {
		return portalOut{}, err
	}
	out := []string{}
	for _, x := range list {
		out = append(out, x.Code)
	}
	return portalOut{Result: out}, nil
}

func (s *Server) portalTokenErr(c portalCall, err error) error {
	if errors.Is(err, bp.ErrAppToken) {
		return s.portalErr(c.r, http.StatusBadRequest, "ERROR_WRONG_EVENT_TOKEN", "err.bp_app_token")
	}
	return err
}

// bizproc.event.send: EVENT_TOKEN, RETURN_VALUES {key: value}, LOG_MESSAGE → true; the process waiting goes on.
func (s *Server) portalBPEventSend(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, false)
	if err != nil {
		return portalOut{}, err
	}
	values := map[string]string{}
	if m, ok := portalParam(c.in, "RETURN_VALUES").(map[string]any); ok {
		for k, v := range m {
			values[k] = portalStr(v)
		}
	}
	err = s.BP.AppAnswer(c.r.Context(), a.ID, portalStr(portalParam(c.in, "EVENT_TOKEN")), values, portalStr(portalParam(c.in, "LOG_MESSAGE")))
	if err != nil {
		return portalOut{}, s.portalTokenErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// bizproc.activity.log: EVENT_TOKEN, LOG_MESSAGE → true; the message goes into the history of the process.
func (s *Server) portalBPActivityLog(c portalCall) (portalOut, error) {
	if _, err := s.portalAppOf(c, false); err != nil {
		return portalOut{}, err
	}
	if err := s.BP.AppLog(c.r.Context(), portalStr(portalParam(c.in, "EVENT_TOKEN")), portalStr(portalParam(c.in, "LOG_MESSAGE"))); err != nil {
		return portalOut{}, s.portalTokenErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// BPAppCall posts a step of an application's action to its handler (bp.Store.AppCall): the
// process (workflow_id), its document (document_id, document_type), event_token, properties and the
// time the answer is waited for (timeout_duration, seconds; 0 — no limit).
func (s *Server) BPAppCall(ctx context.Context, act apps.Activity, userID string, x bp.Instance, token string, props map[string]any) error {
	procs, err := s.portalProcs(ctx, []string{x.ID})
	if err != nil {
		return err
	}
	p := procs[x.ID]
	timeout := 0
	if x.Step < len(x.Steps) {
		timeout = x.Steps[x.Step].Timeout * 60
	}
	entity := x.Entity
	if entity == "none" {
		entity = ""
	}
	dt := portalDocType(entity, "", "")
	if i := strings.LastIndex(p.DocID, "_"); strings.HasPrefix(p.DocID, "DYNAMIC_") && i > 0 { // a smart process's card: DYNAMIC_<type>
		dt[2] = p.DocID[:i]
	}
	data := map[string]any{"workflow_id": x.ID, "event_token": token, "properties": props,
		"document_id": []any{p.Module, p.Entity, p.DocID}, "document_type": []any{dt[0], dt[1], dt[2]},
		"timeout_duration": strconv.Itoa(timeout)}
	return s.Apps.CallActivity(ctx, act, userID, s.BaseURL, data)
}
