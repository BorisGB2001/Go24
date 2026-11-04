package web

import (
	"cmp"
	"net/http"
	"strings"

	"go24/internal/apps"
	"go24/internal/tasks"
)

// REST compatible with the portal, applications (GO24DEV-835): events, event.get, event.bind, event.unbind and
// placement.list, placement.get, placement.bind, placement.unbind by their names, for the token of an application
// (an incoming webhook gets WRONG_AUTH_TYPE. A handler bound so gets the event posts it:
// a form of PHP arrays, the records by their numbers (migration 664).

func init() {
	for name, m := range map[string]portalMethod{
		"events":           {Run: (*Server).portalEvents},
		"event.get":        {Run: (*Server).portalEventGet},
		"event.bind":       {Run: (*Server).portalEventBind},
		"event.unbind":     {Run: (*Server).portalEventUnbind},
		"placement.list":   {Scope: "placement", Run: (*Server).portalPlacementList},
		"placement.get":    {Scope: "placement", Run: (*Server).portalPlacementGet},
		"placement.bind":   {Scope: "placement", Run: (*Server).portalPlacementBind},
		"placement.unbind": {Scope: "placement", Run: (*Server).portalPlacementUnbind},
	} {
		portalMethods[name] = m
	}
}

// portalAppOf is the application of the caller's token; with admin — the caller is an administrator.
func (s *Server) portalAppOf(c portalCall, admin bool) (apps.App, error) {
	a, err := s.apiApp(c.r, c.p)
	if err == errAppOnly {
		return a, s.portalErr(c.r, http.StatusUnauthorized, "WRONG_AUTH_TYPE", "err.api_app_only")
	}
	if err == nil && admin && !s.apiAdmin(c.r.Context(), c.p.UserID) {
		err = tasks.ErrForbidden
	}
	return a, err
}

// events: the events a handler can be bound to.
func (s *Server) portalEvents(c portalCall) (portalOut, error) {
	if _, err := s.portalAppOf(c, false); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: apps.Events}, nil
}

// event.get: the application's handlers [{event, handler, auth_type, offline}]; auth_type — the employee's number.
func (s *Server) portalEventGet(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, false)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Apps.AppEvents(c.r.Context(), a.ID)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, e := range list {
		num := ""
		if e.UserID != "" {
			n, err := s.portalUserNum(c, e.UserID)
			if err != nil {
				return portalOut{}, err
			}
			num = portalStr(n)
		}
		out = append(out, map[string]any{"event": e.Event, "handler": e.Handler, "auth_type": num, "offline": 0})
	}
	return portalOut{Result: out}, nil
}

// portalEventIn is the event, handler and employee (auth_type — a number; 0 or none — the caller) of bind and unbind.
func (s *Server) portalEventIn(c portalCall) (apps.Event, error) {
	e := apps.Event{Event: portalStr(portalParam(c.in, "event")), Handler: portalStr(portalParam(c.in, "handler")), Portal: true}
	user, err := s.portalNum(c.r.Context(), "users", portalParam(c.in, "auth_type"))
	if e.UserID = user; user == "" {
		e.UserID = c.p.UserID
	}
	return e, err
}

// event.bind: event, handler (on the application's host), auth_type — by an administrator.
func (s *Server) portalEventBind(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	e, err := s.portalEventIn(c)
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.Apps.BindEvent(c.r.Context(), a, e); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// event.unbind: event, handler (none — every handler of the event) → {count}.
func (s *Server) portalEventUnbind(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	e, err := s.portalEventIn(c)
	if err != nil {
		return portalOut{}, err
	}
	n, err := s.Apps.UnbindEvent(c.r.Context(), a.ID, e.Event, e.Handler)
	return portalOut{Result: map[string]int64{"count": n}}, err
}

// placement.list: the places of go24 to bind to.
func (s *Server) portalPlacementList(c portalCall) (portalOut, error) {
	if _, err := s.portalAppOf(c, false); err != nil {
		return portalOut{}, err
	}
	list, err := s.Apps.AllPlaces(c.r.Context())
	return portalOut{Result: list}, err
}

// placement.get: the application's handlers [{placement, handler, title, description}].
func (s *Server) portalPlacementGet(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, false)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Apps.Placements(c.r.Context(), a.ID)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, x := range list {
		out = append(out, map[string]any{"placement": x.Placement, "handler": x.Handler, "title": x.Title, "description": x.Description,
			"options": x.Options})
	}
	return portalOut{Result: out}, nil
}

// portalPlacementIn is PLACEMENT, HANDLER, TITLE, DESCRIPTION and OPTIONS of the call; without TITLE (DESCRIPTION) —
// that of LANG_ALL in the caller's language, else in Russian, English or any.
func (s *Server) portalPlacementIn(c portalCall) apps.Placement {
	x := apps.Placement{Placement: strings.ToUpper(portalStr(portalParam(c.in, "PLACEMENT"))), Handler: portalStr(portalParam(c.in, "HANDLER")),
		Title: portalStr(portalParam(c.in, "TITLE")), Description: portalStr(portalParam(c.in, "DESCRIPTION")), Options: apps.PlaceOptions{}}
	if o, ok := portalParam(c.in, "OPTIONS").(map[string]any); ok {
		for k, v := range o {
			x.Options[k] = portalStr(v)
		}
	}
	if langs, ok := portalParam(c.in, "LANG_ALL").(map[string]any); ok {
		var l map[string]any
		for _, code := range []string{strings.ToLower(strings.SplitN(s.lang(c.r), "-", 2)[0]), "ru", "en"} {
			if l, ok = portalParam(langs, code).(map[string]any); ok {
				break
			}
		}
		for _, v := range langs {
			if l == nil {
				l, _ = v.(map[string]any)
			}
		}
		x.Title = cmp.Or(x.Title, portalStr(portalParam(l, "TITLE")))
		x.Description = cmp.Or(x.Description, portalStr(portalParam(l, "DESCRIPTION")))
	}
	return x
}

// placement.bind: PLACEMENT, HANDLER, TITLE, DESCRIPTION (or LANG_ALL), OPTIONS — by an administrator.
func (s *Server) portalPlacementBind(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.Apps.Bind(c.r.Context(), a, s.portalPlacementIn(c)); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// placement.unbind: PLACEMENT, HANDLER (none — every handler of the place) → {count}.
func (s *Server) portalPlacementUnbind(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	x := s.portalPlacementIn(c)
	if x.Placement == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.app_placement", "")
	}
	n, err := s.Apps.Unbind(c.r.Context(), a.ID, x.Placement, x.Handler)
	return portalOut{Result: map[string]int64{"count": n}}, err
}
