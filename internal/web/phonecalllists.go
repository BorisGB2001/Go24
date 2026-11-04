package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// ---------- «Обзвон» of the CRM: the clients of the chosen cards called one by one from the softphone ----------

// callListLeads are the clients of the cards (kind leads or deals) the person may read, each with its first phone;
// skipped — the cards they may not read or without a phone.
func (s *Server) callListLeads(r *http.Request, sess auth.Session, kind string, ids []string) (leads []telephony.CallLead, skipped int) {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if s.crmCheck(r, sess, kind, id, "read") != nil {
			skipped++
			continue
		}
		phones, err := s.CRM.ClientPhones(r.Context(), crmEntities[kind], id)
		if err != nil || len(phones) == 0 {
			skipped++
			continue
		}
		leads = append(leads, telephony.CallLead{EntityID: id, Name: phones[0].Name, Phone: phones[0].Phone})
	}
	return leads, skipped
}

// makeCallList makes the person's call list of the cards; its title — «Обзвон: Лиды, 29.10.2026 10:15» if not given.
func (s *Server) makeCallList(r *http.Request, sess auth.Session, kind, title string, ids []string) (id string, skipped int, err error) {
	if s.Telephony == nil {
		return "", 0, i18n.E("err.telephony_off")
	}
	entity := crmEntities[kind]
	if entity != "lead" && entity != "deal" {
		return "", 0, i18n.E("err.bad_form")
	}
	leads, skipped := s.callListLeads(r, sess, kind, ids)
	if title == "" {
		title = s.tr(r, "tel.cl_default", s.tr(r, "crm."+kind), time.Now().Format("02.01.2006 15:04"))
	}
	id, err = s.Telephony.MakeCallList(r.Context(), sess.UserID, title, entity, leads)
	return id, skipped, err
}

// crmCallList: «Начать обзвон» of the list of leads or deals — the chosen cards' clients become the person's call list.
func (s *Server) crmCallList(w http.ResponseWriter, r *http.Request, sess auth.Session, kind, back string) {
	id, skipped, err := s.makeCallList(r, sess, kind, "", r.PostForm["id"])
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	u := "/telephony/calllists/" + id
	if skipped > 0 {
		u += "?msg=" + url.QueryEscape(s.tr(r, "tel.cl_skipped", skipped))
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

// callListOf is the call list the person may work with: their own (an administrator of telephony — anyone's).
func (s *Server) callListOf(ctx context.Context, sess auth.Session, id string) (telephony.CallList, []telephony.CallItem, error) {
	if s.Telephony == nil {
		return telephony.CallList{}, nil, i18n.E("err.telephony_off")
	}
	l, items, err := s.Telephony.CallList(ctx, id)
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && l.Owner != sess.UserID && !s.telAdmin(ctx, sess)) {
		return l, nil, i18n.E("err.not_found")
	}
	return l, items, err
}

// callLists is the page «Обзвоны»: the person's call lists (an administrator's — everyone's with ?all=1).
func (s *Server) callLists(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/my")
		return
	}
	owner, all := sess.UserID, r.URL.Query().Get("all") == "1" && s.telAdmin(r.Context(), sess)
	if all {
		owner = ""
	}
	lists, err := s.Telephony.CallLists(r.Context(), owner)
	if err != nil {
		s.fail(w, "call lists", err)
		return
	}
	s.render(w, r, "phonecalllists", page{TitleKey: "tel.cl_title", Nav: "telephony_calls", Flash: r.URL.Query().Get("msg"),
		Data: map[string]any{"Lists": lists, "All": all, "Admin": s.telAdmin(r.Context(), sess), "Me": sess.UserID}})
}

// callListPage is a call list: its clients and the one to call now (?item= — another one), «Позвонить» through the
// softphone, the result.
func (s *Server) callListPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	l, items, err := s.callListOf(r.Context(), sess, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/telephony/calllists")
		return
	}
	cur := telephony.Current(items)
	if it := r.URL.Query().Get("item"); it != "" {
		for i := range items {
			if items[i].ID == it {
				cur = i
			}
		}
	}
	var current *telephony.CallItem
	next := ""
	if cur >= 0 {
		current = &items[cur]
		for _, it := range items[cur+1:] {
			if it.Status == telephony.ItemWait {
				next = it.ID
				break
			}
		}
	}
	_, hasLine := s.phoneLine(r.Context(), sess.UserID)
	s.render(w, r, "phonecalllist", page{Title: l.Title, Nav: "telephony_calls", Flash: r.URL.Query().Get("msg"),
		Data: map[string]any{"List": l, "Items": items, "Current": current, "Next": next, "HasLine": hasLine,
			"Mine": l.Owner == sess.UserID, "Path": map[string]string{"lead": "leads", "deal": "deals"}[l.Entity]}})
}

// callListDial: «Позвонить» — the softphone dials the number, the call log gives the result (JSON).
func (s *Server) callListDial(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	l, _, err := s.callListOf(r.Context(), sess, r.PathValue("id"))
	if err == nil && l.Owner != sess.UserID { // the calls are the owner's: an administrator only looks
		err = i18n.E("err.forbidden")
	}
	if err == nil {
		err = s.Telephony.DialItem(r.Context(), l.ID, r.PathValue("item"))
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// callListItem sets a client's result by hand: «Дозвонились», «Не дозвонились», «Пропустить», «Позвонить ещё раз».
func (s *Server) callListItem(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	l, _, err := s.callListOf(r.Context(), sess, r.PathValue("id"))
	back := "/telephony/calllists/" + r.PathValue("id")
	if err == nil {
		err = s.Telephony.SetItemStatus(r.Context(), l.ID, r.PathValue("item"), r.FormValue("status"))
	}
	if errors.Is(err, telephony.ErrNotFound) {
		err = i18n.E("err.not_found")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if next := r.FormValue("next"); next != "" {
		back += "?item=" + url.QueryEscape(next)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// callListClose: «Завершить обзвон» (closed=0 — resume it); delete=1 — delete it.
func (s *Server) callListClose(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	l, _, err := s.callListOf(r.Context(), sess, r.PathValue("id"))
	back := "/telephony/calllists/" + r.PathValue("id")
	if err == nil && r.FormValue("delete") == "1" {
		if err = s.Telephony.DeleteCallList(r.Context(), l.ID); err == nil {
			http.Redirect(w, r, "/telephony/calllists?msg="+url.QueryEscape(s.tr(r, "tel.cl_deleted")), http.StatusSeeOther)
			return
		}
	} else if err == nil {
		err = s.Telephony.CloseCallList(r.Context(), l.ID, r.FormValue("closed") != "0")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// ---------- REST ----------

type apiCallItem struct {
	ID       string     `json:"id"`
	EntityID string     `json:"entity_id"`
	Title    string     `json:"title"`
	Name     string     `json:"name"`
	Phone    string     `json:"phone"`
	Status   string     `json:"status"` // wait, answered, noanswer, skipped
	DialedAt *time.Time `json:"dialed_at"`
	CallID   string     `json:"call_id,omitempty"`
	Seconds  int        `json:"seconds"`
}

type apiCallList struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Entity    string        `json:"entity"`
	OwnerID   string        `json:"owner_id"`
	Owner     string        `json:"owner"`
	CreatedAt time.Time     `json:"created_at"`
	ClosedAt  *time.Time    `json:"closed_at"`
	Total     int           `json:"total"`
	Done      int           `json:"done"`
	Items     []apiCallItem `json:"items,omitempty"`
}

func apiCallListOf(l telephony.CallList, items []telephony.CallItem) apiCallList {
	out := apiCallList{ID: l.ID, Title: l.Title, Entity: l.Entity, OwnerID: l.Owner, Owner: l.OwnerName, CreatedAt: l.Created,
		ClosedAt: l.Closed, Total: l.Total, Done: l.Done}
	if items != nil {
		out.Items = []apiCallItem{}
	}
	for _, it := range items {
		out.Items = append(out.Items, apiCallItem{ID: it.ID, EntityID: it.EntityID, Title: it.Title, Name: it.Name, Phone: it.Phone,
			Status: it.Status, DialedAt: it.Dialed, CallID: it.CallID, Seconds: it.Seconds})
	}
	return out
}

// apiCallLists are the key owner's call lists.
func (s *Server) apiCallLists(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Telephony == nil {
		return i18n.E("err.telephony_off")
	}
	lists, err := s.Telephony.CallLists(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]apiCallList, len(lists))
	for i, l := range lists {
		out[i] = apiCallListOf(l, nil)
	}
	writeJSON(w, http.StatusOK, map[string]any{"calllists": out})
	return nil
}

// apiCallList is a call list with its clients.
func (s *Server) apiCallList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	l, items, err := s.callListOf(r.Context(), s.apiSess(r, p), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiCallListOf(l, items))
	return nil
}

// apiCallListMake makes a call list {entity: lead|deal, ids, title}; the cards without a phone are skipped.
func (s *Server) apiCallListMake(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Entity string   `json:"entity"`
		IDs    []string `json:"ids"`
		Title  string   `json:"title"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	kind := map[string]string{"lead": "leads", "deal": "deals"}[in.Entity]
	id, skipped, err := s.makeCallList(r, s.apiSess(r, p), kind, in.Title, in.IDs)
	if err != nil {
		return err
	}
	l, items, err := s.Telephony.CallList(r.Context(), id)
	if err != nil {
		return err
	}
	out := apiCallListOf(l, items)
	writeJSON(w, http.StatusCreated, map[string]any{"calllist": out, "skipped": skipped})
	return nil
}

// apiCallListItem sets a client's result {status}.
func (s *Server) apiCallListItem(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Status string `json:"status"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	l, _, err := s.callListOf(r.Context(), s.apiSess(r, p), r.PathValue("id"))
	if err != nil {
		return err
	}
	if err := s.Telephony.SetItemStatus(r.Context(), l.ID, r.PathValue("item"), in.Status); errors.Is(err, telephony.ErrNotFound) {
		return i18n.E("err.not_found")
	} else if err != nil {
		return err
	}
	return s.apiCallList(w, r, p)
}
