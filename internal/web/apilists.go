package web

import (
	"net/http"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/bp"
)

// ---------- «Списки» over REST, as lists.get, lists.field.get and lists.element.* ----------
//
// The rights are those of the pages: a key sees the lists its owner sees and changes the elements where they may;
// the lists themselves are set up on the pages.

func (s *Server) apiListsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/lists", s.api("lists:read", s.apiLists))
	mux.HandleFunc("GET /api/v1/lists/{id}", s.api("lists:read", s.apiList))
	mux.HandleFunc("GET /api/v1/lists/{id}/elements", s.api("lists:read", s.apiListElements))
	mux.HandleFunc("POST /api/v1/lists/{id}/elements", s.api("lists:write", s.apiListSaveElement))
	mux.HandleFunc("GET /api/v1/lists/{id}/elements/{element}", s.api("lists:read", s.apiListElement))
	mux.HandleFunc("PATCH /api/v1/lists/{id}/elements/{element}", s.api("lists:write", s.apiListSaveElement))
	mux.HandleFunc("DELETE /api/v1/lists/{id}/elements/{element}", s.api("lists:write", s.apiListDeleteElement))
}

type apiList struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Fields      []bp.Field `json:"fields"`
	Elements    int        `json:"elements"`
	CanWrite    bool       `json:"can_write"` // the key's owner adds and changes the elements
	CreatedAt   time.Time  `json:"created_at"`
	URL         string     `json:"url"`
}

type apiListElement struct {
	ID        string            `json:"id"`
	ListID    string            `json:"list_id"`
	Name      string            `json:"name"`
	Values    map[string]string `json:"values"` // by the fields' keys: a bool "Y"/"N", an employee's id, a date YYYY-MM-DD
	CreatedBy string            `json:"created_by"`
	CreatedAt time.Time         `json:"created_at"`
	ChangedBy string            `json:"changed_by"`
	ChangedAt time.Time         `json:"changed_at"`
	URL       string            `json:"url"`
}

func (s *Server) apiListOf(r *http.Request, p apikeys.Principal, l bp.List) apiList {
	ctx, _ := s.apiBPCtx(r, p)
	out := apiList{ID: l.ID, Name: l.Name, Description: l.Description, Fields: l.Fields, Elements: l.Count,
		CanWrite: s.BP.CanWriteList(ctx, p.UserID, l.ID), CreatedAt: l.CreatedAt, URL: s.BaseURL + "/bp/lists/" + l.ID}
	if out.Fields == nil {
		out.Fields = []bp.Field{}
	}
	return out
}

func (s *Server) apiElementOf(e bp.Element) apiListElement {
	out := apiListElement{ID: e.ID, ListID: e.ListID, Name: e.Name, Values: e.Values, CreatedBy: e.CreatedBy.ID, CreatedAt: e.CreatedAt,
		ChangedBy: e.ChangedBy.ID, ChangedAt: e.ChangedAt, URL: s.BaseURL + "/bp/el/" + e.ID}
	if out.Values == nil {
		out.Values = map[string]string{}
	}
	return out
}

// apiLists are the lists the key's owner sees (lists.get).
func (s *Server) apiLists(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, _ := s.apiBPCtx(r, p)
	list, err := s.BP.Lists(ctx, p.UserID)
	if err != nil {
		return err
	}
	out := make([]apiList, len(list))
	for i, l := range list {
		out[i] = s.apiListOf(r, p, l)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiList is a list with its fields (lists.field.get).
func (s *Server) apiList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, _ := s.apiBPCtx(r, p)
	l, err := s.BP.List(ctx, p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiListOf(r, p, l))
	return nil
}

// apiListElements are the elements of a list (lists.element.get): ?q= words in the name or the values, ?sort=name|
// created|changed|<a field's key>, ?desc=1, ?limit= (up to 500, 50 by default), ?offset=.
func (s *Server) apiListElements(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, _ := s.apiBPCtx(r, p)
	l, err := s.BP.List(ctx, p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	list, err := s.BP.Elements(ctx, l, bp.ElementFilter{Query: q.Get("q"), Sort: q.Get("sort"), Desc: q.Get("desc") == "1",
		Limit: min(max(limit, 0), 500), Offset: max(offset, 0)})
	if err != nil {
		return err
	}
	out := make([]apiListElement, len(list))
	for i, e := range list {
		out[i] = s.apiElementOf(e)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiElement is the element of the address: of the list {id} the key's owner sees.
func (s *Server) apiElement(r *http.Request, p apikeys.Principal) (bp.Element, error) {
	ctx, _ := s.apiBPCtx(r, p)
	e, _, err := s.BP.Element(ctx, p.UserID, r.PathValue("element"))
	if err == nil && e.ListID != r.PathValue("id") {
		err = bp.ErrNotFound
	}
	return e, err
}

func (s *Server) apiListElement(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	e, err := s.apiElement(r, p)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiElementOf(e))
	return nil
}

// apiListSaveElement adds an element (POST …/lists/{id}/elements, lists.element.add) or changes one (PATCH
// …/lists/{id}/elements/{element}, lists.element.update): {"name", "values": {key: value}}; a value left out is the field's
// default for a new element and stays for one changed. The business processes of the list start as on the page.
func (s *Server) apiListSaveElement(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name   string            `json:"name"`
		Values map[string]string `json:"values"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, _ := s.apiBPCtx(r, p)
	e := bp.Element{Name: in.Name, Values: in.Values}
	status := http.StatusCreated
	if r.Method == http.MethodPatch {
		old, err := s.apiElement(r, p)
		if err != nil {
			return err
		}
		e.ID, status = old.ID, http.StatusOK
	} else {
		e.ListID = r.PathValue("id")
	}
	id, err := s.BP.SaveElement(ctx, p.UserID, e)
	if err != nil {
		return err
	}
	saved, _, err := s.BP.Element(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, status, s.apiElementOf(saved))
	return nil
}

// apiListDeleteElement deletes an element (lists.element.delete).
func (s *Server) apiListDeleteElement(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, _ := s.apiBPCtx(r, p)
	e, err := s.apiElement(r, p)
	if err != nil {
		return err
	}
	if err := s.BP.DeleteElement(ctx, p.UserID, e.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
