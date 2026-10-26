package web

import (
	"net/http"
	"net/url"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Корзина» of the CRM (/crm/recycle): the deleted cards, restored or deleted for good ----------

func (s *Server) crmRecycle(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	entity, q := r.URL.Query().Get("entity"), r.URL.Query().Get("q")
	data := map[string]any{"Kind": "recycle", "Entity": entity, "Q": q, "Days": crm.RecycleDays}
	err := s.crmTabs(r, sess, data)
	if err == nil {
		data["Items"], err = s.CRM.Recycle(r.Context(), sess.UserID, entity, q)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmrecycle", page{TitleKey: "crm.recycle", Nav: "crm", Data: data})
}

// crmRecycleAction: «Восстановить» (op=restore — then to the card) or «Удалить навсегда» (op=delete) a card of the
// bin; «Очистить корзину» (no card, op=empty).
func (s *Server) crmRecycleAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/recycle"
	id := r.PathValue("id")
	var err error
	switch op := r.FormValue("op"); {
	case op == "restore" && id != "":
		var entity, card string
		if entity, card, err = s.CRM.Restore(r.Context(), sess.UserID, id); err == nil {
			http.Redirect(w, r, "/crm/"+crmPaths[entity]+"/"+card, http.StatusSeeOther)
			return
		}
	case op == "delete" && id != "":
		err = s.CRM.PurgeRecycled(r.Context(), sess.UserID, id)
	case op == "empty" && id == "":
		err = s.CRM.EmptyRecycle(r.Context(), sess.UserID)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.recycle_deleted")), http.StatusSeeOther)
}

// GET /api/v1/crm/recycle[?entity=deal|lead|contact|company] — the bin: the key owner's deleted cards (an
// administrator's key — all), newest first.
func (s *Server) apiCRMRecycle(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.Recycle(r.Context(), p.UserID, r.URL.Query().Get("entity"), r.URL.Query().Get("q"))
	if err != nil {
		return err
	}
	type item struct {
		ID        string    `json:"id"`
		Entity    string    `json:"entity"`
		EntityID  string    `json:"entity_id"`
		TypeID    string    `json:"type_id,omitempty"`
		Title     string    `json:"title"`
		DeletedBy string    `json:"deleted_by,omitempty"`
		DeletedAt time.Time `json:"deleted_at"`
		PurgeAt   time.Time `json:"purge_at"`
	}
	out := make([]item, 0, len(list))
	for _, it := range list {
		out = append(out, item{it.ID, it.Entity, it.EntityID, it.TypeID, it.Title, it.DeletedBy.ID, it.DeletedAt, it.PurgeAt()})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/crm/recycle/{id}/restore — puts the card back: {"entity": …, "id": …}.
func (s *Server) apiCRMRestore(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	entity, id, err := s.CRM.Restore(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"entity": entity, "id": id})
	return nil
}

// DELETE /api/v1/crm/recycle/{id} — deletes the card for good.
func (s *Server) apiCRMPurge(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.CRM.PurgeRecycled(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
