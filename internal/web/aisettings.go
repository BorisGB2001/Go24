package web

import (
	"net/http"
	"net/url"
	"time"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «ИИ»: connections of AI providers, the company's and one's own (GO24DEV-480) ----------

// aiOwner is the owner of a connection the form changes: "company" — the company's (administrators only).
func aiOwner(scope, userID string, admin bool) (string, error) {
	if scope == "company" {
		if !admin {
			return "", i18n.E("err.forbidden")
		}
		return "", nil
	}
	return userID, nil
}

// aiPage: GET shows the connections and the choice; POST saves a connection.
func (s *Server) aiPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil || tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		owner, err := aiOwner(r.FormValue("scope"), sess.UserID, sess.Admin)
		if err != nil {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
		c := ai.Connection{ID: r.FormValue("id"), Owner: owner, Name: r.FormValue("name"), Provider: r.FormValue("provider"),
			BaseURL: r.FormValue("base_url"), Model: r.FormValue("model"), Default: r.FormValue("default") == "1"}
		id, err := s.AI.Save(ctx, sess.UserID, c, r.FormValue("key"))
		if err != nil {
			s.taskErr(w, r, err, "/settings/ai")
			return
		}
		s.Log.Info("ai connection", "user_id", sess.UserID, "id", id, "company", owner == "")
		http.Redirect(w, r, "/settings/ai?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#ai-"+id, http.StatusSeeOther)
		return
	}
	list, err := s.AI.List(ctx, sess.UserID)
	if err != nil {
		s.fail(w, "ai connections", err)
		return
	}
	chosen, err := s.AI.Chosen(ctx, sess.UserID)
	if err != nil {
		s.fail(w, "ai choice", err)
		return
	}
	var company, mine []ai.Connection
	for _, c := range list {
		if c.Owner == "" {
			company = append(company, c)
		} else {
			mine = append(mine, c)
		}
	}
	using, _, err := s.AI.For(ctx, sess.UserID)
	if err != nil && err != ai.ErrNone {
		s.fail(w, "ai connection", err)
		return
	}
	s.render(w, r, "aisettings", page{TitleKey: "ai.title", Nav: "aisettings", Data: map[string]any{
		"Company": company, "Mine": mine, "Chosen": chosen, "Using": using.ID, "Providers": ai.Providers, "New": ai.Connection{Provider: ai.Providers[0]},
	}})
}

// aiDelete removes a connection: the company's — by an administrator.
func (s *Server) aiDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil || tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	c, err := s.AI.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if err == nil && c.Owner == "" && !sess.Admin {
		err = i18n.E("err.forbidden")
	}
	if err == nil {
		err = s.AI.Delete(r.Context(), c.Owner, c.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/settings/ai")
		return
	}
	http.Redirect(w, r, "/settings/ai?msg="+url.QueryEscape(s.tr(r, "ai.deleted")), http.StatusSeeOther)
}

// aiCheck asks the provider for its models with the connection's key.
func (s *Server) aiCheck(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil || tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("id")
	models, err := s.AI.Check(r.Context(), sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/settings/ai#ai-"+id)
		return
	}
	http.Redirect(w, r, "/settings/ai?msg="+url.QueryEscape(s.tr(r, "ai.check_ok", len(models)))+"#ai-"+id, http.StatusSeeOther)
}

// aiChoose: the connection the employee works through ("" — by default).
func (s *Server) aiChoose(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil || tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if err := s.AI.Choose(r.Context(), sess.UserID, r.FormValue("connection")); err != nil {
		s.taskErr(w, r, err, "/settings/ai")
		return
	}
	http.Redirect(w, r, "/settings/ai?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// ---------- REST: /api/v1/ai ----------

func (s *Server) apiAIRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/ai/connections", s.api("settings:read", s.apiAIList))
	mux.HandleFunc("POST /api/v1/ai/connections", s.api("settings:write", s.apiAISave))
	mux.HandleFunc("PATCH /api/v1/ai/connections/{id}", s.api("settings:write", s.apiAISave))
	mux.HandleFunc("DELETE /api/v1/ai/connections/{id}", s.api("settings:write", s.apiAIDelete))
	mux.HandleFunc("POST /api/v1/ai/connections/{id}/check", s.api("settings:write", s.apiAICheck))
	mux.HandleFunc("GET /api/v1/ai/mine", s.api("settings:read", s.apiAIMine))
	mux.HandleFunc("PUT /api/v1/ai/mine", s.api("settings:write", s.apiAIMine))
}

// apiAIConn is a connection as REST gives it: never the key, only its last characters.
type apiAIConn struct {
	ID         string     `json:"id"`
	Company    bool       `json:"company"`
	Name       string     `json:"name"`
	Provider   string     `json:"provider"`
	BaseURL    string     `json:"base_url"`
	Model      string     `json:"model"`
	KeyTail    string     `json:"key_tail"`
	Default    bool       `json:"default"`
	CheckedAt  *time.Time `json:"checked_at"`
	CheckError string     `json:"check_error"`
	CreatedAt  time.Time  `json:"created_at"`
}

func toAPIAI(c ai.Connection) apiAIConn {
	return apiAIConn{ID: c.ID, Company: c.Owner == "", Name: c.Name, Provider: c.Provider, BaseURL: c.BaseURL, Model: c.Model,
		KeyTail: c.KeyTail, Default: c.Default, CheckedAt: c.CheckedAt, CheckError: c.CheckError, CreatedAt: c.CreatedAt}
}

func (s *Server) apiAIOn(r *http.Request) error {
	if s.AI == nil || tasks.IsGuestCtx(r.Context()) {
		return ai.ErrNotFound
	}
	return nil
}

func (s *Server) apiAIList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	list, err := s.AI.List(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := make([]apiAIConn, 0, len(list))
	for _, c := range list {
		out = append(out, toAPIAI(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
	return nil
}

// apiAISave: POST adds (company: true — the company's, by an administrator), PATCH changes the fields given; key ""
// or none keeps the key.
func (s *Server) apiAISave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	var in struct {
		Company  bool    `json:"company"`
		Name     *string `json:"name"`
		Provider *string `json:"provider"`
		BaseURL  *string `json:"base_url"`
		Model    *string `json:"model"`
		Key      string  `json:"key"`
		Default  *bool   `json:"default"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	var c ai.Connection
	if id := r.PathValue("id"); id != "" {
		var err error
		if c, err = s.AI.Get(ctx, p.UserID, id); err != nil {
			return err
		}
	} else if !in.Company {
		c.Owner = p.UserID
	}
	if c.Owner == "" && !s.apiAdmin(ctx, p.UserID) {
		return i18n.E("err.forbidden")
	}
	for f, v := range map[*string]*string{&c.Name: in.Name, &c.Provider: in.Provider, &c.BaseURL: in.BaseURL, &c.Model: in.Model} {
		if v != nil {
			*f = *v
		}
	}
	if in.Default != nil {
		c.Default = *in.Default
	}
	id, err := s.AI.Save(ctx, p.UserID, c, in.Key)
	if err != nil {
		return err
	}
	if c, err = s.AI.Get(ctx, p.UserID, id); err != nil {
		return err
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, toAPIAI(c))
	return nil
}

func (s *Server) apiAIDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	c, err := s.AI.Get(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if c.Owner == "" && !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	if err := s.AI.Delete(r.Context(), c.Owner, c.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiAICheck: the models of the provider, or 422 with what it answered.
func (s *Server) apiAICheck(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	models, err := s.AI.Check(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	if models == nil {
		models = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
	return nil
}

// apiAIMine: the connection the caller picked (PUT {"connection": ""} — by default) and the one they work through.
func (s *Server) apiAIMine(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	ctx := r.Context()
	if r.Method == http.MethodPut {
		var in struct {
			Connection string `json:"connection"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.AI.Choose(ctx, p.UserID, in.Connection); err != nil {
			return err
		}
	}
	chosen, err := s.AI.Chosen(ctx, p.UserID)
	if err != nil {
		return err
	}
	out := map[string]any{"connection": chosen, "using": nil}
	c, _, err := s.AI.For(ctx, p.UserID)
	switch {
	case err == nil:
		out["using"] = toAPIAI(c)
	case err != ai.ErrNone:
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
