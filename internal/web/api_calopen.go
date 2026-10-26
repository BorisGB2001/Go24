package web

import (
	"net/http"
	"slices"
	"time"

	"go24/internal/apikeys"
	"go24/internal/calendar"
)

// REST of «Открытые события»: the categories, the coming open events, joining one.

type apiCategory struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`
	Closed      bool   `json:"closed,omitempty"` // only its creator (and admins) add events
	Muted       bool   `json:"muted,omitempty"`  // the caller does not hear of its new events
	Upcoming    int    `json:"upcoming"`
}

func (s *Server) apiCalCategories(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Calendar.Categories(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []apiCategory{}
	for _, c := range list {
		out = append(out, apiCategory{ID: c.ID, Name: c.Name, Description: c.Description, CreatedBy: c.CreatedBy,
			Closed: c.Closed, Muted: c.Muted, Upcoming: c.Upcoming})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCalCategorySave adds (POST) or changes (PATCH) a category; muted turns its notifications off or on for the caller.
func (s *Server) apiCalCategorySave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Closed      *bool   `json:"closed"`
		Muted       *bool   `json:"muted"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var c calendar.Category
	if id := r.PathValue("id"); id != "" {
		list, err := s.Calendar.Categories(r.Context(), p.UserID)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(list, func(c calendar.Category) bool { return c.ID == id })
		if i < 0 {
			return calendar.ErrNotFound
		}
		c = list[i]
	}
	if in.Name != nil {
		c.Name = *in.Name
	}
	if in.Description != nil {
		c.Description = *in.Description
	}
	if in.Closed != nil {
		c.Closed = *in.Closed
	}
	status := http.StatusOK
	if c.ID == "" {
		status = http.StatusCreated
	}
	if c.ID == "" || in.Name != nil || in.Description != nil || in.Closed != nil {
		id, err := s.Calendar.SaveCategory(r.Context(), p.UserID, c)
		if err != nil {
			return err
		}
		c.ID = id
	}
	if in.Muted != nil {
		if err := s.Calendar.MuteCategory(r.Context(), p.UserID, c.ID, *in.Muted); err != nil {
			return err
		}
	}
	list, err := s.Calendar.Categories(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.ID == c.ID {
			writeJSON(w, status, apiCategory{ID: x.ID, Name: x.Name, Description: x.Description, CreatedBy: x.CreatedBy,
				Closed: x.Closed, Muted: x.Muted, Upcoming: x.Upcoming})
			return nil
		}
	}
	return calendar.ErrNotFound
}

func (s *Server) apiCalCategoryDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Calendar.DeleteCategory(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiCalOpen: the open events (?category=) between ?from and ?to (by default: the next two months).
func (s *Server) apiCalOpen(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	from, to := time.Now(), time.Now().AddDate(0, 0, openDays)
	if q := r.URL.Query(); q.Has("from") || q.Has("to") {
		var err error
		if from, to, err = apiRange(r); err != nil {
			return err
		}
	}
	list, err := s.Calendar.OpenEvents(r.Context(), p.UserID, r.URL.Query().Get("category"), from, to)
	if err != nil {
		return err
	}
	out := []apiEvent{}
	for _, e := range list {
		out = append(out, toAPIEvent(e))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiCalJoin: {"join": true} — the caller comes to the open event, false — does not come any more.
func (s *Server) apiCalJoin(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Join *bool `json:"join"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	join := in.Join == nil || *in.Join
	id := r.PathValue("id")
	if err := s.Calendar.Join(r.Context(), p.UserID, id, join); err != nil {
		return err
	}
	s.meetingChatSync(r.Context(), p.UserID, id)
	e, err := s.Calendar.Get(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, toAPIEvent(e))
	return nil
}
