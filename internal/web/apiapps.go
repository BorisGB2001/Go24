package web

import (
	"net/http"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Места встраивания» over REST, as placement.* ----------
//
// Only an application calls them, with its token and the «placement» right: placement.list (the places), placement.get
// (its handlers), placement.bind and placement.unbind — by an administrator.

func (s *Server) apiAppsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/placements", s.api("placement", s.apiPlacements))
	mux.HandleFunc("GET /api/v1/placements/list", s.api("placement", s.apiPlacementList))
	mux.HandleFunc("POST /api/v1/placements", s.api("placement", s.apiPlacementBind))
	mux.HandleFunc("DELETE /api/v1/placements", s.api("placement", s.apiPlacementUnbind))
	mux.HandleFunc("GET /api/v1/events", s.api("", s.apiEvents))
	mux.HandleFunc("GET /api/v1/events/list", s.api("", s.apiEventList))
	mux.HandleFunc("POST /api/v1/events", s.api("", s.apiEventBind))
	mux.HandleFunc("DELETE /api/v1/events", s.api("", s.apiEventUnbind))
}

var errAppOnly = i18n.E("err.api_app_only")

// apiApp is the application the token is of.
func (s *Server) apiApp(r *http.Request, p apikeys.Principal) (apps.App, error) {
	client, ok := strings.CutPrefix(p.KeyID, "app:")
	if !ok || s.Apps == nil {
		return apps.App{}, errAppOnly
	}
	return s.Apps.ByClientID(r.Context(), client)
}

// apiPlacementList is placement.list: the places of go24 to bind to.
func (s *Server) apiPlacementList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiApp(r, p); err != nil {
		return err
	}
	list, err := s.Apps.AllPlaces(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, list)
	return nil
}

// apiPlacements is placement.get: the handlers of the application, with ?placement= — of one place.
func (s *Server) apiPlacements(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiApp(r, p)
	if err != nil {
		return err
	}
	list, err := s.Apps.Placements(r.Context(), a.ID)
	if err != nil {
		return err
	}
	out := []apps.Placement{}
	place := strings.ToUpper(r.URL.Query().Get("placement"))
	for _, x := range list {
		if place == "" || x.Placement == place {
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// placementReq is the body of bind and unbind: {"placement", "handler", "title", "description"}; the query works too.
func placementReq(r *http.Request) (apps.Placement, error) {
	var x apps.Placement
	if r.ContentLength != 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := readJSON(r, &x); err != nil {
			return x, err
		}
	}
	q := r.URL.Query()
	for _, f := range []struct {
		v   *string
		key string
	}{{&x.Placement, "placement"}, {&x.Handler, "handler"}, {&x.Title, "title"}, {&x.Description, "description"}} {
		if *f.v == "" {
			*f.v = q.Get(f.key)
		}
	}
	return x, nil
}

// apiPlacementBind is placement.bind: the handler on the application's host into the place, under the title.
func (s *Server) apiPlacementBind(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiApp(r, p)
	if err != nil {
		return err
	}
	if !s.apiAdmin(r.Context(), p.UserID) {
		return tasks.ErrForbidden
	}
	x, err := placementReq(r)
	if err != nil {
		return err
	}
	if x, err = s.Apps.Bind(r.Context(), a, x); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, x)
	return nil
}

// apiPlacementUnbind is placement.unbind: the handler off the place; without a handler — all of the place.
func (s *Server) apiPlacementUnbind(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiApp(r, p)
	if err != nil {
		return err
	}
	if !s.apiAdmin(r.Context(), p.UserID) {
		return tasks.ErrForbidden
	}
	x, err := placementReq(r)
	if err != nil {
		return err
	}
	if x.Placement == "" {
		return i18n.E("err.app_placement", "")
	}
	n, err := s.Apps.Unbind(r.Context(), a.ID, x.Placement, x.Handler)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": n})
	return nil
}

// ---------- Events over REST, as event.* ----------
//
// Only an application calls them, with its token and any rights: events (the events), event.get (its handlers),
// event.bind and event.unbind — by an administrator.

// apiEventList is «events»: what an application can subscribe to.
func (s *Server) apiEventList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiApp(r, p); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apps.Events)
	return nil
}

// apiEvents is event.get: the handlers of the application, with ?event= — of one event.
func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiApp(r, p)
	if err != nil {
		return err
	}
	list, err := s.Apps.AppEvents(r.Context(), a.ID)
	if err != nil {
		return err
	}
	out := []apps.Event{}
	event := strings.ToUpper(r.URL.Query().Get("event"))
	for _, x := range list {
		if event == "" || x.Event == event {
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// eventReq is the body of bind and unbind: {"event", "handler", "auth_type"}; the query works too.
func eventReq(r *http.Request) (apps.Event, error) {
	var x apps.Event
	if r.ContentLength != 0 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := readJSON(r, &x); err != nil {
			return x, err
		}
	}
	q := r.URL.Query()
	for _, f := range []struct {
		v   *string
		key string
	}{{&x.Event, "event"}, {&x.Handler, "handler"}, {&x.UserID, "auth_type"}} {
		if *f.v == "" {
			*f.v = q.Get(f.key)
		}
	}
	return x, nil
}

// apiEventBind is event.bind: the handler on the application's host to the event; it gets the tokens of the employee
// of auth_type (an active one), by default of the one who binds.
func (s *Server) apiEventBind(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiApp(r, p)
	if err != nil {
		return err
	}
	if !s.apiAdmin(r.Context(), p.UserID) {
		return tasks.ErrForbidden
	}
	x, err := eventReq(r)
	if err != nil {
		return err
	}
	if x.UserID == "" || x.UserID == "0" {
		x.UserID = p.UserID
	}
	if x, err = s.Apps.BindEvent(r.Context(), a, x); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, x)
	return nil
}

// apiEventUnbind is event.unbind: the handler off the event; without a handler — all of the event.
func (s *Server) apiEventUnbind(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	a, err := s.apiApp(r, p)
	if err != nil {
		return err
	}
	if !s.apiAdmin(r.Context(), p.UserID) {
		return tasks.ErrForbidden
	}
	x, err := eventReq(r)
	if err != nil {
		return err
	}
	if x.Event == "" {
		return i18n.E("err.app_event", "")
	}
	n, err := s.Apps.UnbindEvent(r.Context(), a.ID, x.Event, x.Handler)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": n})
	return nil
}
