package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
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
			BaseURL: r.FormValue("base_url"), Model: r.FormValue("model"), Default: r.FormValue("default") == "1",
			PriceIn: formPrice(r.FormValue("price_in")), PriceOut: formPrice(r.FormValue("price_out"))}
		c.MonthlyLimit, _ = strconv.ParseInt(strings.TrimSpace(r.FormValue("monthly_limit")), 10, 64)
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
	var quota ai.Quota
	if using.ID != "" {
		if quota, err = s.AI.Quota(ctx, sess.UserID, using); err != nil {
			s.fail(w, "ai quota", err)
			return
		}
	}
	data := map[string]any{
		"Company": company, "Mine": mine, "Chosen": chosen, "Using": using.ID, "Providers": ai.Providers, "New": ai.Connection{Provider: ai.Providers[0]},
		"Quota": quota, "Used": tokens(quota.Used), "Limit": tokens(quota.Limit),
	}
	if err := s.AI.Permit(ctx, sess.UserID, ""); err != nil { // «Настройки CoPilot» keep the employee from AI
		data["Denied"] = true
	}
	if sess.Admin {
		pol, err := s.AI.Policy(ctx)
		if err == nil {
			data["Policy"], err = s.aiPolicyData(ctx, pol)
		}
		if err != nil {
			s.fail(w, "ai policy", err)
			return
		}
	}
	s.render(w, r, "aisettings", page{TitleKey: "ai.title", Nav: "aisettings", Data: data})
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

// formPrice reads a price with a dot or a comma ("" or not a number — 0).
func formPrice(v string) float64 {
	f, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(v), ",", "."), 64)
	return f
}

// tokens is a number of tokens with its thousands apart.
func tokens(n int64) string { return crm.Amount(float64(n)) }

// usd is a cost in dollars: cents, or more digits for less than a dollar.
func usd(v float64) string {
	if v != 0 && v < 1 {
		return "$" + strconv.FormatFloat(v, 'f', 4, 64)
	}
	return "$" + crm.Amount(v)
}

// aiSpendRow is a row of the report of the usage.
type aiSpendRow struct {
	ai.Spend
	Tokens, In, Out, Company, Cost string
	Limit                          string // the employee's own limit ("" — as the connection)
}

func aiSpendRows(list []ai.Spend) ([]aiSpendRow, ai.Spend) {
	rows := make([]aiSpendRow, len(list))
	var sum ai.Spend
	for i, u := range list {
		rows[i] = aiSpendRow{Spend: u, Tokens: tokens(u.Tokens()), In: tokens(u.In), Out: tokens(u.Out), Company: tokens(u.Company), Cost: usd(u.Cost)}
		if u.OwnLimit != nil {
			rows[i].Limit = strconv.FormatInt(*u.OwnLimit, 10)
		}
		sum.Calls, sum.In, sum.Out, sum.Company, sum.Cost = sum.Calls+u.Calls, sum.In+u.In, sum.Out+u.Out, sum.Company+u.Company, sum.Cost+u.Cost
	}
	return rows, sum
}

// aiMonth is the month of ?month=YYYY-MM (else this one) in go24's time zone.
func aiMonth(v string) time.Time {
	if t, err := time.ParseInLocation("2006-01", v, time.Local); err == nil {
		return t
	}
	return ai.MonthStart(time.Now())
}

// aiUsage: «Расход ИИ» — the administrators see the employees' usage of a month and set their limits, an employee
// sees their own by months.
func (s *Server) aiUsage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil || tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		if !sess.Admin {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
		var n *int64
		if v := strings.TrimSpace(r.FormValue("limit")); v != "" {
			x, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				x = -1
			}
			n = &x
		}
		if err := s.AI.SetLimit(ctx, r.FormValue("user_id"), n); err != nil {
			s.taskErr(w, r, err, "/settings/ai/usage")
			return
		}
		s.Log.Info("ai limit", "user_id", sess.UserID, "of", r.FormValue("user_id"))
		http.Redirect(w, r, "/settings/ai/usage?month="+url.QueryEscape(r.FormValue("month"))+"&msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	month := aiMonth(r.URL.Query().Get("month"))
	year := month.AddDate(0, -11, 0)
	who := sess.UserID
	if sess.Admin {
		who = ""
	}
	months, err := s.AI.ByMonth(ctx, year, who)
	if err != nil {
		s.fail(w, "ai usage", err)
		return
	}
	data := map[string]any{"Month": month.Format("2006-01"), "Prev": month.AddDate(0, -1, 0).Format("2006-01"),
		"Next": month.AddDate(0, 1, 0).Format("2006-01"), "Admin": sess.Admin}
	data["Months"], _ = aiSpendRows(months)
	if sess.Admin {
		list, err := s.AI.ByUser(ctx, month, month.AddDate(0, 1, 0))
		if err != nil {
			s.fail(w, "ai usage", err)
			return
		}
		rows, sum := aiSpendRows(list)
		data["Users"], data["Sum"] = rows, aiSpendRow{Spend: sum, Tokens: tokens(sum.Tokens()), In: tokens(sum.In), Out: tokens(sum.Out),
			Company: tokens(sum.Company), Cost: usd(sum.Cost)}
	}
	s.render(w, r, "aiusage", page{TitleKey: "ai.usage.title", Nav: "aisettings", Data: data})
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
	mux.HandleFunc("GET /api/v1/ai/usage", s.api("settings:read", s.apiAIUsage))
	mux.HandleFunc("PUT /api/v1/ai/limits/{user}", s.api("settings:write", s.apiAILimit))
	mux.HandleFunc("GET /api/v1/ai/policy", s.api("settings:read", s.apiAIPolicy))
	mux.HandleFunc("PUT /api/v1/ai/policy", s.api("settings:write", s.apiAIPolicy))
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
	PriceIn    float64    `json:"price_in"`
	PriceOut   float64    `json:"price_out"`
	Limit      int64      `json:"monthly_limit"`
}

func toAPIAI(c ai.Connection) apiAIConn {
	return apiAIConn{ID: c.ID, Company: c.Owner == "", Name: c.Name, Provider: c.Provider, BaseURL: c.BaseURL, Model: c.Model,
		KeyTail: c.KeyTail, Default: c.Default, CheckedAt: c.CheckedAt, CheckError: c.CheckError, CreatedAt: c.CreatedAt,
		PriceIn: c.PriceIn, PriceOut: c.PriceOut, Limit: c.MonthlyLimit}
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
		Company  bool     `json:"company"`
		Name     *string  `json:"name"`
		Provider *string  `json:"provider"`
		BaseURL  *string  `json:"base_url"`
		Model    *string  `json:"model"`
		Key      string   `json:"key"`
		Default  *bool    `json:"default"`
		PriceIn  *float64 `json:"price_in"`
		PriceOut *float64 `json:"price_out"`
		Limit    *int64   `json:"monthly_limit"`
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
	for f, v := range map[*float64]*float64{&c.PriceIn: in.PriceIn, &c.PriceOut: in.PriceOut} {
		if v != nil {
			*f = *v
		}
	}
	if in.Limit != nil {
		c.MonthlyLimit = *in.Limit
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

// apiAISpend is a row of the usage as REST gives it.
type apiAISpend struct {
	UserID   string  `json:"user_id,omitempty"`
	Name     string  `json:"name,omitempty"`
	Month    string  `json:"month,omitempty"`
	Calls    int64   `json:"calls"`
	In       int64   `json:"in_tokens"`
	Out      int64   `json:"out_tokens"`
	Company  int64   `json:"company_tokens"`
	Cost     float64 `json:"cost"`
	OwnLimit *int64  `json:"own_limit,omitempty"`
}

func toAPISpend(list []ai.Spend) []apiAISpend {
	out := make([]apiAISpend, len(list))
	for i, u := range list {
		out[i] = apiAISpend{UserID: u.UserID, Name: u.Name, Month: u.Month, Calls: u.Calls, In: u.In, Out: u.Out, Company: u.Company,
			Cost: u.Cost, OwnLimit: u.OwnLimit}
	}
	return out
}

// apiAIUsage: ?month=YYYY-MM — an administrator gets the employees' usage of the month and all of the twelve months up
// to it, an employee — their own months.
func (s *Server) apiAIUsage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	ctx := r.Context()
	month := aiMonth(r.URL.Query().Get("month"))
	admin := s.apiAdmin(ctx, p.UserID)
	who := p.UserID
	if admin {
		who = ""
	}
	months, err := s.AI.ByMonth(ctx, month.AddDate(0, -11, 0), who)
	if err != nil {
		return err
	}
	out := map[string]any{"month": month.Format("2006-01"), "months": toAPISpend(months)}
	if admin {
		users, err := s.AI.ByUser(ctx, month, month.AddDate(0, 1, 0))
		if err != nil {
			return err
		}
		out["users"] = toAPISpend(users)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiAILimit: an administrator sets an employee's own monthly limit ({"monthly_tokens": null} — as the connection).
func (s *Server) apiAILimit(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.apiAIOn(r); err != nil {
		return err
	}
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	var in struct {
		Tokens *int64 `json:"monthly_tokens"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.AI.SetLimit(r.Context(), r.PathValue("user"), in.Tokens); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": r.PathValue("user"), "monthly_tokens": in.Tokens})
	return nil
}
