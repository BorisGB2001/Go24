package web

import (
	"slices"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// crmWaitAnchors are what «Ожидание» counts from: "now" (after today), "close" (before a deal's closing date) and
// "f:"+an own date field of the card (before it).
func (s *Server) crmWaitAnchors(r *http.Request, entity string) ([]crmDateField, error) {
	fields, err := s.crmFieldsOf(r, tasks.CRMFieldSets[entity])
	if err != nil {
		return nil, err
	}
	out := []crmDateField{{Key: "now"}}
	if entity == "deal" {
		out = append(out, crmDateField{Key: "close"})
	}
	for _, f := range fields {
		if f.Kind == "date" {
			out = append(out, crmDateField{Key: "f:" + f.ID, Name: f.Name})
		}
	}
	return out, nil
}

// crmWaitAnchor is the card's date of an anchor: nil — "now"; an empty date is an error.
func (s *Server) crmWaitAnchor(r *http.Request, entity, id, key string) (*time.Time, error) {
	switch {
	case key == "now" || key == "":
		return nil, nil
	case key == "close" && entity == "deal":
		d, err := s.CRM.Deal(r.Context(), id)
		if err != nil {
			return nil, err
		}
		if d.CloseDate != nil {
			t := time.Date(d.CloseDate.Year(), d.CloseDate.Month(), d.CloseDate.Day(), 0, 0, 0, 0, time.Local)
			return &t, nil
		}
	case strings.HasPrefix(key, "f:"):
		fields, err := s.crmFieldsOf(r, tasks.CRMFieldSets[entity]) // a field hidden from the viewer is no anchor
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(fields, func(f tasks.Field) bool { return "f:"+f.ID == key }) {
			return nil, i18n.E("err.bad_form")
		}
		vals, err := s.Tasks.ValuesOf(r.Context(), tasks.CRMFieldSets[entity], []string{id})
		if err != nil {
			return nil, err
		}
		if t, err := time.ParseInLocation(time.DateOnly, vals[id][strings.TrimPrefix(key, "f:")], time.Local); err == nil {
			return &t, nil
		}
	default:
		return nil, i18n.E("err.bad_form")
	}
	return nil, i18n.E("err.crm_wait_no_date")
}

// crmWait: op "set" makes the card wait («Ждать n unit после текущей даты / до даты»), "end" ends its wait.
func (s *Server) crmWait(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back, entity := "/crm/"+kind+"/"+id, crmEntities[kind]
	err := s.crmCheck(r, sess, kind, id, "write")
	if err == nil {
		switch r.FormValue("op") {
		case "set":
			var anchor *time.Time
			if anchor, err = s.crmWaitAnchor(r, entity, id, r.FormValue("anchor")); err == nil {
				n, _ := strconv.Atoi(r.FormValue("n"))
				var until time.Time
				if until, err = crm.WaitUntil(n, r.FormValue("unit"), anchor, time.Now()); err == nil {
					_, err = s.CRM.AddWait(r.Context(), sess.UserID, crm.Wait{Entity: entity, EntityID: id, Until: until,
						Description: r.FormValue("description")})
				}
			}
		case "end":
			err = s.CRM.EndWait(r.Context(), sess.UserID, entity, id)
		default:
			err = i18n.E("err.bad_form")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#activities", http.StatusSeeOther)
}

// waitJSON is a wait in REST.
func waitJSON(w *crm.Wait) any {
	if w == nil {
		return nil
	}
	return map[string]any{"id": w.ID, "until": w.Until, "description": w.Description, "created_by": w.By.ID,
		"created_by_name": w.By.Name, "created_at": w.CreatedAt}
}

// GET /api/v1/crm/{kind}/{id}/wait — the card's open «Ожидание» (null — none).
func (s *Server) apiCRMWait(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	wt, err := s.CRM.WaitOf(r.Context(), crmEntities[r.PathValue("kind")], r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, waitJSON(wt))
	return nil
}

// POST /api/v1/crm/{kind}/{id}/wait {"until": RFC 3339} or {"n": 3, "unit": "day|week", "anchor": "now|close|f:<field>"},
// "description" — the card waits (its open wait ends); DELETE ends the wait («Завершить»).
func (s *Server) apiCRMWaitSet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "write"); err != nil {
		return err
	}
	entity, id := crmEntities[r.PathValue("kind")], r.PathValue("id")
	if r.Method == http.MethodDelete {
		if err := s.CRM.EndWait(r.Context(), p.UserID, entity, id); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	var in struct {
		Until       *time.Time `json:"until"`
		N           int        `json:"n"`
		Unit        string     `json:"unit"`
		Anchor      string     `json:"anchor"`
		Description string     `json:"description"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var until time.Time
	if in.Until != nil {
		until = *in.Until
	} else {
		anchor, err := s.crmWaitAnchor(r, entity, id, in.Anchor)
		if err != nil {
			return err
		}
		if until, err = crm.WaitUntil(in.N, in.Unit, anchor, time.Now()); err != nil {
			return err
		}
	}
	if _, err := s.CRM.AddWait(r.Context(), p.UserID, crm.Wait{Entity: entity, EntityID: id, Until: until, Description: in.Description}); err != nil {
		return err
	}
	wt, err := s.CRM.WaitOf(r.Context(), entity, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, waitJSON(wt))
	return nil
}
