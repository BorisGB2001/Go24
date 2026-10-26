package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- REST API: CRM and business processes (for n8n and other services) ----------

type apiCRMCard struct {
	ID          string     `json:"id"`
	Kind        string     `json:"kind"`
	Title       string     `json:"title,omitempty"`
	Name        string     `json:"name,omitempty"`
	CompanyName string     `json:"company_title,omitempty"`
	Phone       string     `json:"phone,omitempty"`
	Email       string     `json:"email,omitempty"`
	Source      string     `json:"source,omitempty"`
	Origin      *apiDict   `json:"origin,omitempty"` // «Источник», «Тип …», «Сфера деятельности»: items of the CRM's lists
	DealType    *apiDict   `json:"deal_type,omitempty"`
	ContactType *apiDict   `json:"contact_type,omitempty"`
	CompanyType *apiDict   `json:"company_type,omitempty"`
	Industry    *apiDict   `json:"industry,omitempty"`
	Amount      float64    `json:"amount,omitempty"`
	Currency    string     `json:"currency,omitempty"`
	StageID     string     `json:"stage_id,omitempty"`
	StageCode   string     `json:"stage_code,omitempty"`
	StageName   string     `json:"stage_name,omitempty"`
	PipelineID  string     `json:"pipeline_id,omitempty"`
	TypeID      string     `json:"type_id,omitempty"` // a card of a smart process
	CompanyID   string     `json:"company_id,omitempty"`
	ContactID   string     `json:"contact_id,omitempty"`
	ContactIDs  []string   `json:"contact_ids,omitempty"` // a deal's contacts, the main one first (a single card)
	ParentIDs   []string   `json:"parent_ids,omitempty"`  // «Связи»: the parents of a smart process's element (a single card)
	Responsible apiUser    `json:"responsible"`
	Observers   []apiUser  `json:"observers,omitempty"` // a deal's or a lead's «Наблюдатели» (a single card)
	Comment     string     `json:"comment,omitempty"`
	Closed      *time.Time `json:"closed_at,omitempty"`
	DealID      string     `json:"deal_id,omitempty"`
	Phones      []crm.Comm `json:"phones,omitempty"` // all the phones and e-mails of a lead, contact or company: {value, type}
	Emails      []crm.Comm `json:"emails,omitempty"`
	IsRepeat    bool       `json:"is_repeat,omitempty"` // a lead: «Повторный лид» (contact_id, company_id — its client)
	UTM         *apiUTM    `json:"utm,omitempty"`       // a deal's or a lead's UTM tags (a single card)
	CreatedAt   time.Time  `json:"created_at"`
	URL         string     `json:"url"`
}

// apiDict is an item of a CRM list: its id, the code of a built-in one, its own name (a built-in one may have none).
type apiDict struct {
	ID   string `json:"id"`
	Code string `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
}

func observersJSON(list []crm.Ref) []apiUser {
	var out []apiUser
	for _, o := range list {
		out = append(out, apiUser{o.ID, o.Name})
	}
	return out
}

func dictJSON(d crm.Dict) *apiDict {
	if d.ID == "" {
		return nil
	}
	return &apiDict{d.ID, d.Code, d.Name}
}

// dictIn is an item given to the API: its id, or the code of a built-in one (CALL, CLIENT, IT…).
func dictIn(v string) crm.Dict {
	if v != "" && strings.ToUpper(v) == v && !strings.Contains(v, "-") {
		return crm.Dict{Code: v}
	}
	return crm.Dict{ID: v}
}

func (s *Server) crmCardJSON(kind string, item any) apiCRMCard {
	c := apiCRMCard{Kind: crmEntities[kind]}
	switch x := item.(type) {
	case crm.Deal:
		c.ID, c.Title, c.Amount, c.Currency, c.PipelineID, c.Comment, c.Closed, c.CreatedAt = x.ID, x.Title, x.Amount, x.Currency, x.PipelineID, x.Comment, x.ClosedAt, x.CreatedAt
		c.StageID, c.StageCode, c.StageName, c.CompanyID, c.ContactID = x.Stage.ID, x.Stage.Code, x.Stage.Name, x.Company.ID, x.Contact.ID
		c.Responsible, c.TypeID = apiUser{x.Responsible.ID, x.Responsible.Name}, x.TypeID
		c.Origin, c.DealType = dictJSON(x.Origin), dictJSON(x.DealType)
		for _, ct := range x.Contacts {
			c.ContactIDs = append(c.ContactIDs, ct.ID)
		}
		c.Observers = observersJSON(x.Observers)
	case crm.Lead:
		c.ID, c.Title, c.Name, c.CompanyName, c.Phone, c.Email, c.Source = x.ID, x.Title, x.Name, x.CompanyTitle, x.Phone, x.Email, x.Source
		c.Amount, c.Currency, c.Comment, c.DealID, c.CreatedAt = x.Amount, x.Currency, x.Comment, x.DealID, x.CreatedAt
		c.ContactID, c.CompanyID, c.IsRepeat = x.ContactID, x.CompanyID, x.Repeat
		c.Phones, c.Emails = crm.CommsOf(x.Comms, "phone"), crm.CommsOf(x.Comms, "email")
		c.StageID, c.StageCode, c.StageName = x.Stage.ID, x.Stage.Code, x.Stage.Name
		c.Responsible, c.Origin = apiUser{x.Responsible.ID, x.Responsible.Name}, dictJSON(x.Origin)
		c.Observers = observersJSON(x.Observers)
	case crm.Contact:
		c.ID, c.Name, c.Phone, c.Email, c.CompanyID, c.Comment, c.CreatedAt = x.ID, x.Name, x.Phone, x.Email, x.Company.ID, x.Comment, x.CreatedAt
		c.Responsible, c.Origin, c.ContactType = apiUser{x.Responsible.ID, x.Responsible.Name}, dictJSON(x.Origin), dictJSON(x.ContactType)
		c.Phones, c.Emails = crm.CommsOf(x.Comms, "phone"), crm.CommsOf(x.Comms, "email")
	case crm.Company:
		c.ID, c.Title, c.Phone, c.Email, c.Comment, c.CreatedAt = x.ID, x.Title, x.Phone, x.Email, x.Comment, x.CreatedAt
		c.Responsible, c.CompanyType, c.Industry = apiUser{x.Responsible.ID, x.Responsible.Name}, dictJSON(x.CompanyType), dictJSON(x.Industry)
		c.Phones, c.Emails = crm.CommsOf(x.Comms, "phone"), crm.CommsOf(x.Comms, "email")
	}
	c.URL = s.BaseURL + "/crm/" + kind + "/" + c.ID
	return c
}

// GET /api/v1/crm/{kind}?q=… (leads: &repeat=1|0) — deals, leads, contacts, companies the key's owner may read.
func (s *Server) apiCRMList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	kind, q := r.PathValue("kind"), r.URL.Query().Get("q")
	var items []any
	switch crmEntities[kind] {
	case "deal":
		list, err := s.CRM.Deals(r.Context(), p.UserID, r.URL.Query().Get("pipeline"), q, "", "")
		if err != nil {
			return err
		}
		for _, x := range list {
			items = append(items, x)
		}
	case "lead":
		f := crm.Filter{Q: q}
		if v := r.URL.Query().Get("repeat"); v == "1" || v == "0" { // «Повторный лид» or not
			f.Repeat = v
		}
		list, err := s.CRM.LeadList(r.Context(), p.UserID, f)
		if err != nil {
			return err
		}
		for _, x := range list {
			items = append(items, x)
		}
	case "contact":
		list, err := s.CRM.Contacts(r.Context(), p.UserID, q, "")
		if err != nil {
			return err
		}
		for _, x := range list {
			items = append(items, x)
		}
	case "company":
		list, err := s.CRM.Companies(r.Context(), p.UserID, q)
		if err != nil {
			return err
		}
		for _, x := range list {
			items = append(items, x)
		}
	default:
		return i18n.E("err.not_found")
	}
	out := make([]apiCRMCard, 0, len(items))
	for _, it := range items {
		out = append(out, s.crmCardJSON(kind, it))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiCRMItem(r *http.Request, p apikeys.Principal, action string) (any, error) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	var item any
	var err error
	switch crmEntities[kind] {
	case "deal":
		item, err = s.CRM.Deal(r.Context(), id)
	case "lead":
		item, err = s.CRM.Lead(r.Context(), id)
	case "contact":
		item, err = s.CRM.Contact(r.Context(), id)
	case "company":
		item, err = s.CRM.Company(r.Context(), id)
	default:
		return nil, i18n.E("err.not_found")
	}
	if err != nil {
		return nil, err
	}
	return item, s.CRM.CheckCard(r.Context(), p.UserID, crmEntities[kind], action, id, crmResponsible(item))
}

// GET /api/v1/crm/{kind}/{id}
func (s *Server) apiCRMGet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	item, err := s.apiCRMItem(r, p, "read")
	if err != nil {
		return err
	}
	return s.writeCRMCard(w, r, http.StatusOK, item)
}

type apiCRMInput struct {
	Title         string     `json:"title"`
	Name          string     `json:"name"`
	CompanyTitle  string     `json:"company_title"`
	Position      string     `json:"position"`
	Phone         string     `json:"phone"`
	Email         string     `json:"email"`
	Website       string     `json:"website"`
	Address       string     `json:"address"`
	Source        string     `json:"source"`
	Origin        string     `json:"origin"` // an item of a list: its id or a built-in code
	DealType      string     `json:"deal_type"`
	ContactType   string     `json:"contact_type"`
	CompanyType   string     `json:"company_type"`
	Industry      string     `json:"industry"`
	Amount        float64    `json:"amount"`
	Currency      string     `json:"currency"`
	PipelineID    string     `json:"pipeline_id"`
	StageID       string     `json:"stage_id"`
	CompanyID     string     `json:"company_id"`
	ContactID     string     `json:"contact_id"`
	ContactIDs    []string   `json:"contact_ids"` // a deal's contacts, the main one first (instead of contact_id)
	ResponsibleID string     `json:"responsible_id"`
	ObserverIDs   []string   `json:"observer_ids"` // a deal's or a lead's «Наблюдатели»
	CloseDate     *time.Time `json:"close_date"`
	Comment       string     `json:"comment"`
	UTM           *apiUTM    `json:"utm"`    // a deal's or a lead's UTM tags
	Phones        []crm.Comm `json:"phones"` // several phones and e-mails {value, type}, the main one first (instead of phone, email)
	Emails        []crm.Comm `json:"emails"`
	ParentIDs     []string   `json:"parent_ids"` // «Связи»: the parents of a smart process's element
}

// comms are the input's phones and e-mails; nil — none given (only phone and email).
func (in apiCRMInput) comms() []crm.Comm {
	if in.Phones == nil && in.Emails == nil {
		return nil
	}
	out := []crm.Comm{}
	for kind, list := range map[string][]crm.Comm{"phone": in.Phones, "email": in.Emails} {
		for _, c := range list {
			c.Kind = kind
			out = append(out, c)
		}
	}
	return out
}

// phoneEmail are the main phone and e-mail of the input: the first of phones and emails, if given.
func (in apiCRMInput) phoneEmail() (string, string) {
	phone, email := in.Phone, in.Email
	if in.Phones != nil {
		phone = ""
		if len(in.Phones) > 0 {
			phone = in.Phones[0].Value
		}
	}
	if in.Emails != nil {
		email = ""
		if len(in.Emails) > 0 {
			email = in.Emails[0].Value
		}
	}
	return phone, email
}

// POST /api/v1/crm/{kind} — a new card (a deal goes to the first pipeline unless pipeline_id is given).
func (s *Server) apiCRMCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	kind := r.PathValue("kind")
	var in apiCRMInput
	if err := readJSON(r, &in); err != nil {
		return err
	}
	resp := in.ResponsibleID
	if resp == "" {
		resp = p.UserID
	}
	if err := s.CRM.Check(r.Context(), p.UserID, crmEntities[kind], "write", resp); err != nil {
		return err
	}
	var id string
	var err error
	ref := crm.Ref{ID: resp}
	phone, email := in.phoneEmail()
	switch crmEntities[kind] {
	case "deal":
		if in.PipelineID == "" {
			pipes, err := s.CRM.Pipelines(r.Context())
			if err != nil || len(pipes) == 0 {
				return i18n.E("err.crm_stage")
			}
			in.PipelineID = pipes[0].ID
		}
		d := crm.Deal{Title: in.Title, PipelineID: in.PipelineID, Stage: crm.Stage{ID: in.StageID}, Amount: in.Amount, Currency: in.Currency,
			Company: crm.Ref{ID: in.CompanyID}, Contact: crm.Ref{ID: in.ContactID}, Responsible: ref, CloseDate: in.CloseDate, Comment: in.Comment,
			Origin: dictIn(in.Origin), DealType: dictIn(in.DealType), Observers: observerRefs(in.ObserverIDs)}
		for _, c := range in.ContactIDs {
			d.Contacts = append(d.Contacts, crm.Ref{ID: c})
		}
		for _, pid := range in.ParentIDs {
			if err = s.apiCRMReadable(r, p, pid); err != nil {
				return err
			}
		}
		if id, err = s.CRM.SaveDeal(r.Context(), p.UserID, d); err == nil && len(in.ParentIDs) > 0 {
			err = s.CRM.SetParents(r.Context(), p.UserID, id, in.ParentIDs)
		}
	case "lead":
		id, err = s.CRM.SaveLead(r.Context(), p.UserID, crm.Lead{Title: in.Title, Name: in.Name, CompanyTitle: in.CompanyTitle, Phone: phone,
			Email: email, Comms: in.comms(), Source: in.Source, Amount: in.Amount, Currency: in.Currency, Responsible: ref, Comment: in.Comment, Origin: dictIn(in.Origin),
			Observers: observerRefs(in.ObserverIDs)})
	case "contact":
		id, err = s.CRM.SaveContact(r.Context(), p.UserID, crm.Contact{Name: in.Name, Position: in.Position, Phone: phone, Email: email, Comms: in.comms(),
			Company: crm.Ref{ID: in.CompanyID}, Responsible: ref, Comment: in.Comment, Origin: dictIn(in.Origin), ContactType: dictIn(in.ContactType)})
	case "company":
		id, err = s.CRM.SaveCompany(r.Context(), p.UserID, crm.Company{Title: in.Title, Phone: phone, Email: email, Comms: in.comms(), Website: in.Website,
			Address: in.Address, Responsible: ref, Comment: in.Comment, CompanyType: dictIn(in.CompanyType), Industry: dictIn(in.Industry)})
	default:
		return i18n.E("err.not_found")
	}
	if err == nil && in.UTM != nil {
		err = s.setUTM(r.Context(), crmEntities[kind], id, in.UTM.utm())
	}
	if err != nil {
		return err
	}
	r.SetPathValue("id", id)
	item, err := s.apiCRMItem(r, p, "read")
	if err != nil {
		return err
	}
	return s.writeCRMCard(w, r, http.StatusCreated, item)
}

func observerRefs(ids []string) []crm.Ref {
	var out []crm.Ref
	for _, u := range ids {
		out = append(out, crm.Ref{ID: u})
	}
	return out
}

// PUT /api/v1/crm/{deals|leads}/{id}/observers {"observer_ids": […]} — the card's «Наблюдатели» (all of them).
func (s *Server) apiCRMObservers(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "write"); err != nil {
		return err
	}
	var in struct {
		ObserverIDs []string `json:"observer_ids"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.CRM.SetObservers(r.Context(), p.UserID, crmEntities[r.PathValue("kind")], r.PathValue("id"), in.ObserverIDs); err != nil {
		return err
	}
	item, err := s.apiCRMItem(r, p, "read")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.crmCardJSON(r.PathValue("kind"), item))
	return nil
}

// apiCRMReadable: the key's user may read the deal or element.
func (s *Server) apiCRMReadable(r *http.Request, p apikeys.Principal, id string) error {
	d, err := s.CRM.Deal(r.Context(), id)
	if err != nil {
		return err
	}
	return s.CRM.CheckCard(r.Context(), p.UserID, "deal", "read", id, d.Responsible.ID)
}

// PUT /api/v1/crm/deals/{id}/parents {"parent_ids": [...]} — «Связи»: all the parents of an element ([] unbinds it).
func (s *Server) apiCRMParents(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.SetPathValue("kind", "deals")
	if _, err := s.apiCRMItem(r, p, "write"); err != nil {
		return err
	}
	var in struct {
		ParentIDs []string `json:"parent_ids"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	for _, pid := range in.ParentIDs {
		if err := s.apiCRMReadable(r, p, pid); err != nil {
			return err
		}
	}
	if err := s.CRM.SetParents(r.Context(), p.UserID, r.PathValue("id"), in.ParentIDs); err != nil {
		return err
	}
	item, err := s.apiCRMItem(r, p, "read")
	if err != nil {
		return err
	}
	return s.writeCRMCard(w, r, http.StatusOK, item)
}

// GET /api/v1/crm/deals/{id}/children — «Связи»: the elements bound to a deal or an element.
func (s *Server) apiCRMChildren(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.SetPathValue("kind", "deals")
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	list, err := s.CRM.Children(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	out := []apiCRMCard{}
	for _, d := range list {
		out = append(out, s.crmCardJSON("deals", d))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/crm/{kind}/{id}/stage {"stage_id": …} — deals and leads.
func (s *Server) apiCRMStage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "write"); err != nil {
		return err
	}
	var in struct {
		StageID string `json:"stage_id"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var item any
	var err error
	switch crmEntities[r.PathValue("kind")] {
	case "deal":
		item, err = s.CRM.MoveDeal(r.Context(), p.UserID, r.PathValue("id"), in.StageID)
	case "lead":
		item, err = s.CRM.MoveLead(r.Context(), p.UserID, r.PathValue("id"), in.StageID)
	default:
		return i18n.E("err.bad_form")
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.crmCardJSON(r.PathValue("kind"), item))
	return nil
}

// POST /api/v1/crm/leads/{id}/convert {"deal": true, "pipeline_id": …, "contact": true, "contact_id": …, "company": …,
// "company_id": …} — «Конвертация»: an empty body converts as the card's dialog offers; gives the lead.
func (s *Server) apiCRMConvert(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.SetPathValue("kind", "leads")
	item, err := s.apiCRMItem(r, p, "write")
	if err != nil {
		return err
	}
	var in struct {
		Deal       *bool  `json:"deal"`
		Contact    bool   `json:"contact"`
		Company    bool   `json:"company"`
		PipelineID string `json:"pipeline_id"`
		ContactID  string `json:"contact_id"`
		CompanyID  string `json:"company_id"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	to := crm.ConvertTo{Contact: in.Contact, Company: in.Company, PipelineID: in.PipelineID, ContactID: in.ContactID, CompanyID: in.CompanyID}
	if in.Deal == nil && !in.Contact && !in.Company {
		if to, err = s.CRM.ConvertDefaults(r.Context(), item.(crm.Lead)); err != nil {
			return err
		}
	} else if in.Deal != nil {
		to.Deal = *in.Deal
	}
	for entity, made := range map[string]bool{"deal": to.Deal, "contact": to.Contact && to.ContactID == "", "company": to.Company && to.CompanyID == ""} {
		if made {
			if err := s.CRM.Check(r.Context(), p.UserID, entity, "write", p.UserID); err != nil {
				return err
			}
		}
	}
	if _, err := s.CRM.ConvertLeadTo(r.Context(), p.UserID, r.PathValue("id"), to); err != nil {
		return err
	}
	l, err := s.CRM.Lead(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.crmCardJSON("leads", l))
	return nil
}

// POST /api/v1/crm/{kind}/{id}/comments {"body": …} — a line in the card's history.
func (s *Server) apiCRMComment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.CRM.Comment(r.Context(), p.UserID, crmEntities[r.PathValue("kind")], r.PathValue("id"), in.Body); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// GET /api/v1/crm/{kind}/{id}/timeline — the card's history, newest first (pinned lines are marked).
func (s *Server) apiCRMTimeline(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	list, err := s.CRM.Timeline(r.Context(), crmEntities[r.PathValue("kind")], r.PathValue("id"))
	if err != nil {
		return err
	}
	type ev struct {
		ID       int64     `json:"id"`
		Kind     string    `json:"kind"`
		Body     string    `json:"body"`
		AuthorID string    `json:"author_id"`
		Author   string    `json:"author"`
		At       time.Time `json:"created_at"`
		Pinned   bool      `json:"pinned"`
		Edited   bool      `json:"edited"`
	}
	out := make([]ev, 0, len(list))
	for _, e := range list {
		out = append(out, ev{e.ID, e.Kind, e.Body, e.AuthorID, e.AuthorName, e.At, e.Pinned, e.Edited})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// PATCH /api/v1/crm/{kind}/{id}/timeline/{ev} {"body"?: …, "pinned"?: bool} — edits a comment (its author), pins or
// unpins a line (whoever may change the card); DELETE removes a comment.
func (s *Server) apiCRMEvent(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	ev, err := strconv.ParseInt(r.PathValue("ev"), 10, 64)
	if err != nil {
		return crm.ErrNotFound
	}
	if r.Method == http.MethodDelete {
		if _, err := s.apiCRMItem(r, p, "read"); err != nil {
			return err
		}
		if err := s.CRM.DeleteComment(r.Context(), p.UserID, crmEntities[kind], id, ev); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	var in struct {
		Body   *string `json:"body"`
		Pinned *bool   `json:"pinned"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	need := "read"
	if in.Pinned != nil {
		need = "write"
	}
	if _, err := s.apiCRMItem(r, p, need); err != nil {
		return err
	}
	if in.Body != nil {
		if err := s.CRM.EditComment(r.Context(), p.UserID, crmEntities[kind], id, ev, *in.Body); err != nil {
			return err
		}
	}
	if in.Pinned != nil {
		if err := s.CRM.Pin(r.Context(), crmEntities[kind], id, ev, *in.Pinned); err != nil {
			return err
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// GET /api/v1/crm/pipelines — the deal pipelines with their stages (and the lead stages), for stage ids.
func (s *Server) apiCRMPipelines(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pipes, err := s.CRM.Pipelines(r.Context())
	if err != nil {
		return err
	}
	leads, err := s.CRM.LeadStages(r.Context())
	if err != nil {
		return err
	}
	type st struct {
		ID       string `json:"id"`
		Code     string `json:"code,omitempty"`
		Name     string `json:"name,omitempty"`
		Semantic string `json:"semantic"`
	}
	conv := func(list []crm.Stage) []st {
		out := make([]st, 0, len(list))
		for _, x := range list {
			out = append(out, st{x.ID, x.Code, x.Name, x.Semantic})
		}
		return out
	}
	type pl struct {
		ID     string `json:"id"`
		Code   string `json:"code,omitempty"`
		Name   string `json:"name,omitempty"`
		Stages []st   `json:"stages"`
	}
	out := struct {
		Pipelines  []pl `json:"pipelines"`
		LeadStages []st `json:"lead_stages"`
	}{LeadStages: conv(leads)}
	for _, x := range pipes {
		out.Pipelines = append(out.Pipelines, pl{x.ID, x.Code, x.Name, conv(x.Stages)})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/crm/currencies — the CRM's currencies: code, name, rate to the base one, the base one first.
func (s *Server) apiCRMCurrencies(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.Currencies(r.Context())
	if err != nil {
		return err
	}
	type cur struct {
		Code      string    `json:"code"`
		Name      string    `json:"name,omitempty"`
		Rate      float64   `json:"rate"`
		Base      bool      `json:"base,omitempty"`
		Auto      bool      `json:"auto,omitempty"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	out := make([]cur, 0, len(list))
	for _, c := range list {
		out = append(out, cur{c.Code, c.Name, c.Rate, c.Base, c.Auto, c.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/crm/products?section_id=&q=&all=1 — the catalog (catalog.product.list): the products of a
// section and its subsections, by name; all=1 adds the inactive ones; q finds by name, SKU or barcode, barcode= — the
// product with the barcode only, type=product or type=service — the products or the services only ("type" of a
// product: service — not kept on the warehouses). The products on sale are listed: a product with variations by its variations
// (parent_id — the product's); parent_id= lists a product's variations (the inactive ones too). The purchase price
// is given to those who see it in «Складской учёт» (administrators and the roles with the right).
func (s *Server) apiCRMCatalog(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	var list []crm.Product
	var err error
	if code := q.Get("barcode"); code != "" {
		var x crm.Product
		if x, err = s.CRM.ProductByBarcode(r.Context(), code); err == nil {
			list = []crm.Product{x}
		} else if errors.Is(err, crm.ErrNotFound) {
			err = nil
		}
	} else if parent := q.Get("parent_id"); parent != "" {
		list, err = s.CRM.Variants(r.Context(), parent)
	} else {
		list, err = s.CRM.ProductsOf(r.Context(), q.Get("q"), q.Get("section_id"), q.Get("all") == "1")
	}
	if err != nil {
		return err
	}
	if kind := q.Get("type"); kind != "" {
		list = slices.DeleteFunc(list, func(x crm.Product) bool { return x.Kind != kind })
	}
	rights, err := s.apiStockRights(r, p)
	if err != nil {
		return err
	}
	out := make([]apiProduct, 0, len(list))
	for _, x := range list {
		out = append(out, s.apiProductOut(x, rights.PurchasePrice))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/crm/product-units — the units of measure (catalog.measure.list) in the key's language.
func (s *Server) apiCRMUnits(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.units(r)
	if err != nil {
		return err
	}
	type unit struct {
		ID      string `json:"id"`
		Code    string `json:"code,omitempty"`
		Name    string `json:"name"`
		Symbol  string `json:"symbol"`
		Default bool   `json:"default"`
	}
	out := make([]unit, 0, len(list))
	for _, x := range list {
		out = append(out, unit{x.ID, x.Code, x.ShownName, x.ShownSymbol, x.Default})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/crm/product-props — the products' properties (catalog.productProperty.list); a product's
// "props" are their values by the properties' ids.
func (s *Server) apiCRMProps(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.ProductProps(r.Context())
	if err != nil {
		return err
	}
	type prop struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Options []string `json:"options,omitempty"`
		InList  bool     `json:"in_list"`
		Variant bool     `json:"variant"`
	}
	out := make([]prop, 0, len(list))
	for _, x := range list {
		out = append(out, prop{x.ID, x.Name, x.Kind, x.Options, x.InList, x.Variant})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/crm/product-sections — the sections of the catalog in the tree's order (catalog.section.list).
func (s *Server) apiCRMSections(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.Sections(r.Context())
	if err != nil {
		return err
	}
	type section struct {
		ID       string `json:"id"`
		ParentID string `json:"parent_id,omitempty"`
		Name     string `json:"name"`
		Path     string `json:"path"`
		Sort     int    `json:"sort"`
	}
	out := make([]section, 0, len(list))
	for _, x := range list {
		out = append(out, section{x.ID, x.ParentID, x.Name, x.Path, x.Sort})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiProductRow is a product row of a deal or a lead (crm.deal.productrows).
type apiProductRow struct {
	ProductID   string   `json:"product_id,omitempty"`
	Name        string   `json:"name"`
	Price       float64  `json:"price"`
	Quantity    float64  `json:"quantity"`
	Unit        string   `json:"unit,omitempty"`
	Discount    float64  `json:"discount,omitempty"`
	VATRate     *float64 `json:"vat_rate"`
	VATIncluded bool     `json:"vat_included"`
	Total       float64  `json:"total"`
	// a deal's reserve: the warehouse, how many (none given — the whole quantity), till when (YYYY-MM-DD)
	WarehouseID  string   `json:"warehouse_id,omitempty"`
	Reserve      *float64 `json:"reserve,omitempty"`
	ReserveUntil string   `json:"reserve_until,omitempty"`
}

// GET /api/v1/crm/{deals|leads}/{id}/products — the card's product rows.
func (s *Server) apiCRMProducts(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	rows, err := s.CRM.ProductRows(r.Context(), crmEntities[r.PathValue("kind")], r.PathValue("id"))
	if err != nil {
		return err
	}
	out := make([]apiProductRow, len(rows))
	for i, x := range rows {
		out[i] = apiProductRow{ProductID: x.ProductID, Name: x.Name, Price: x.Price, Quantity: x.Quantity, Unit: x.Unit, Discount: x.Discount,
			VATRate: x.VATRate, VATIncluded: x.VATIncluded, Total: x.Total(), WarehouseID: x.WarehouseID}
		if x.WarehouseID != "" {
			out[i].Reserve = &x.Reserve
		}
		if x.ReserveUntil != nil {
			out[i].ReserveUntil = x.ReserveUntil.Format("2006-01-02")
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// PUT /api/v1/crm/{deals|leads}/{id}/products — replaces the card's product rows (a row of a catalog product
// without a name takes the product's); answers the card's new amount.
func (s *Server) apiCRMSetProducts(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.apiCRMItem(r, p, "write"); err != nil {
		return err
	}
	var in []apiProductRow
	if err := readJSON(r, &in); err != nil {
		return err
	}
	rows := make([]crm.ProductRow, len(in))
	for i, x := range in {
		rows[i] = crm.ProductRow{ProductID: x.ProductID, Name: x.Name, Price: x.Price, Quantity: x.Quantity, Unit: x.Unit,
			Discount: x.Discount, VATRate: x.VATRate, VATIncluded: x.VATIncluded, WarehouseID: x.WarehouseID, Reserve: -1,
			ReserveUntil: formDay(x.ReserveUntil)}
		if x.Reserve != nil {
			rows[i].Reserve = *x.Reserve
		}
	}
	total, err := s.CRM.SetProductRows(r.Context(), p.UserID, crmEntities[r.PathValue("kind")], r.PathValue("id"), rows)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]float64{"amount": total})
	return nil
}

type apiRequisite struct {
	ID          string `json:"id"`
	Preset      string `json:"preset"`
	Name        string `json:"name"`
	INN         string `json:"inn"`
	KPP         string `json:"kpp"`
	OGRN        string `json:"ogrn"`
	Director    string `json:"director"`
	Address     string `json:"address"`
	Bank        string `json:"bank"`
	BIK         string `json:"bik"`
	Account     string `json:"account"`
	CorrAccount string `json:"corr_account"`
	Default     bool   `json:"is_default"`
}

// apiRequisitesOf are the card's requisites (companies and contacts only) once the key may act on the card.
func (s *Server) apiRequisitesOf(r *http.Request, p apikeys.Principal, action string) (string, []crm.Requisite, error) {
	entity := crmEntities[r.PathValue("kind")]
	if entity != "company" && entity != "contact" {
		return "", nil, i18n.E("err.not_found")
	}
	if _, err := s.apiCRMItem(r, p, action); err != nil {
		return "", nil, err
	}
	list, err := s.CRM.Requisites(r.Context(), entity, r.PathValue("id"))
	return entity, list, err
}

// GET /api/v1/crm/{companies|contacts}/{id}/requisites — the card's requisites, the default first.
func (s *Server) apiCRMRequisites(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	_, list, err := s.apiRequisitesOf(r, p, "read")
	if err != nil {
		return err
	}
	out := make([]apiRequisite, len(list))
	for i, q := range list {
		out[i] = apiRequisite{q.ID, q.Preset, q.Name, q.INN, q.KPP, q.OGRN, q.Director, q.Address, q.Bank, q.BIK, q.Account, q.CorrAccount, q.Default}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/crm/{companies|contacts}/{id}/requisites — a new requisite, or a change of one with its "id"
// (preset org, ip or person; org by default); answers {"id"}.
func (s *Server) apiCRMSaveRequisite(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	entity, list, err := s.apiRequisitesOf(r, p, "write")
	if err != nil {
		return err
	}
	var in apiRequisite
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.ID != "" && !slices.ContainsFunc(list, func(q crm.Requisite) bool { return q.ID == in.ID }) {
		return i18n.E("err.not_found")
	}
	if in.Preset == "" {
		in.Preset = crm.RequisitePresets[0]
	}
	id, err := s.CRM.SaveRequisite(r.Context(), crm.Requisite{ID: in.ID, Entity: entity, EntityID: r.PathValue("id"), Preset: in.Preset,
		Name: in.Name, INN: in.INN, KPP: in.KPP, OGRN: in.OGRN, Director: in.Director, Address: in.Address, Bank: in.Bank, BIK: in.BIK,
		Account: in.Account, CorrAccount: in.CorrAccount, Default: in.Default})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
	return nil
}

// DELETE /api/v1/crm/{companies|contacts}/{id}/requisites/{req}
func (s *Server) apiCRMDeleteRequisite(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	_, list, err := s.apiRequisitesOf(r, p, "write")
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(list, func(q crm.Requisite) bool { return q.ID == r.PathValue("req") }) {
		return i18n.E("err.not_found")
	}
	if err := s.CRM.DeleteRequisite(r.Context(), r.PathValue("req")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
