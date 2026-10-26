package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// ---------- «Маркетинг»: advertising channels, their expenses, «Сквозная аналитика» ----------

// As «Рекламные источники» and «Сквозная аналитика»: an administrator keeps the list of channels (the
// utm_source values and the «Источник» of the CRM that are a channel's); anyone of «Маркетинг» writes down what was
// spent on them, by hand or from a CSV; the report sets the expenses against the leads and deals the viewer reads.

// mktPeriod is the period of the query (from, to — days, both in), by default this month; to is the day after it.
func mktPeriod(q url.Values) (from, to time.Time) {
	now := time.Now()
	from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	to = from.AddDate(0, 1, 0)
	if t, err := time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local); err == nil {
		from = t
	}
	if t, err := time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local); err == nil {
		to = t.AddDate(0, 0, 1)
	}
	if !to.After(from) {
		to = from.AddDate(0, 0, 1)
	}
	return from, to
}

func (s *Server) mktAdsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	ctx, q := r.Context(), r.URL.Query()
	srcs, err := s.Marketing.AdSources(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	from, to := mktPeriod(q)
	if q.Get("from") == "" && q.Get("to") == "" { // the expenses of all days, the latest first
		from, to = time.Time{}, time.Time{}
	}
	last := to
	if !to.IsZero() {
		last = to.AddDate(0, 0, -1)
	}
	list, err := s.Marketing.Expenses(ctx, q.Get("source"), from, last, 500)
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	data := map[string]any{"Tab": "ads", "Sources": srcs, "Expenses": list, "Source": q.Get("source"), "From": from, "To": last,
		"Admin": tasks.IsAdmin(ctx), "Me": sess.UserID, "Today": time.Now()}
	if data["Dicts"], err = s.CRM.Dicts(ctx); err == nil {
		data["Currencies"], err = s.CRM.Currencies(ctx)
	}
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	if id := q.Get("edit"); id == "new" {
		data["Edit"] = &marketing.AdSource{}
	} else if id != "" {
		for i := range srcs {
			if srcs[i].ID == id {
				data["Edit"] = &srcs[i]
			}
		}
	}
	s.render(w, r, "marketing", page{TitleKey: "mkt.ads", Nav: "marketing", Flash: q.Get("msg"), Data: data})
}

// mktAdSourceSave adds or changes a channel; delete=1 removes it. Administrators only.
func (s *Server) mktAdSourceSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	if !tasks.IsAdmin(ctx) {
		s.taskErr(w, r, crm.ErrForbidden, "/marketing/ads")
		return
	}
	var err error
	if r.FormValue("delete") != "" {
		err = s.Marketing.DeleteAdSource(ctx, id)
	} else {
		if id == "new" {
			id = ""
		}
		id, err = s.Marketing.SaveAdSource(ctx, marketing.AdSource{ID: id, Name: r.FormValue("name"), UTM: marketing.SplitUTM(r.FormValue("utm")),
			CRMSource: r.FormValue("crm_source"), Color: r.FormValue("color")})
	}
	if err != nil {
		s.taskErr(w, r, err, "/marketing/ads?edit="+r.PathValue("id"))
		return
	}
	http.Redirect(w, r, "/marketing/ads?msg="+url.QueryEscape(s.tr(r, "mkt.ads_saved")), http.StatusSeeOther)
}

// mktExpenseAdd writes down an expense, or (a file) imports them from a CSV.
func (s *Server) mktExpenseAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	ctx := r.Context()
	if err := r.ParseMultipartForm(4 << 20); err != nil && err != http.ErrNotMultipart {
		s.taskErr(w, r, err, "/marketing/ads")
		return
	}
	msg := s.tr(r, "mkt.expense_added")
	if f, _, err := r.FormFile("file"); err == nil {
		defer f.Close()
		n, err := s.Marketing.ImportExpenses(ctx, sess.UserID, f)
		if err != nil {
			s.taskErr(w, r, err, "/marketing/ads")
			return
		}
		msg = s.tr(r, "mkt.expenses_imported", n)
	} else {
		day, _ := time.ParseInLocation(time.DateOnly, r.FormValue("day"), time.Local)
		amount, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(r.FormValue("amount")), " ", ""), ",", "."), 64)
		if err != nil {
			amount = -1
		}
		if _, err := s.Marketing.AddExpense(ctx, sess.UserID, marketing.Expense{SourceID: r.FormValue("source"), Day: day, Amount: amount,
			Currency: r.FormValue("currency"), Campaign: r.FormValue("campaign"), Comment: r.FormValue("comment")}); err != nil {
			s.taskErr(w, r, err, "/marketing/ads")
			return
		}
	}
	http.Redirect(w, r, "/marketing/ads?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) mktExpenseDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	if err := s.Marketing.DeleteExpense(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/marketing/ads")
		return
	}
	http.Redirect(w, r, "/marketing/ads", http.StatusSeeOther)
}

// mktAnalytics is «Сквозная аналитика» of a period.
func (s *Server) mktAnalytics(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	from, to := mktPeriod(r.URL.Query())
	rep, err := s.Marketing.Through(r.Context(), sess.UserID, from, to)
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	last := to.AddDate(0, 0, -1)
	s.render(w, r, "marketing", page{TitleKey: "mkt.analytics", Nav: "marketing",
		Data: map[string]any{"Tab": "analytics", "Report": rep, "From": &from, "To": &last}})
}

// mktROI is the ROI of a line as the report shows it: «+120%», or "" without expenses.
func mktROI(c marketing.Channel) string {
	pct, ok := c.ROI()
	if !ok {
		return ""
	}
	if pct > 0 {
		return "+" + strconv.Itoa(pct) + "%"
	}
	return strconv.Itoa(pct) + "%"
}

// ---------- UTM tags of the cards ----------

// utmCookie keeps the UTM tags of the address a visitor came to a go24 site by, for its forms (30 days).
const utmCookie = "go24_utm"

// keepUTM remembers the tags of a site page's address; an address without tags keeps those of before.
func keepUTM(w http.ResponseWriter, r *http.Request) {
	u := marketing.ParseUTM(r.URL.Query().Get)
	if u.Source == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: utmCookie, Value: u.Values().Encode(), Path: "/", MaxAge: 30 * 86400, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
}

// visitorUTM are the tags a form brings: its own utm_* fields or those the visitor came to the site with.
func visitorUTM(r *http.Request) marketing.UTM {
	if u := marketing.ParseUTM(r.FormValue); u.Source != "" {
		return u
	}
	if c, err := r.Cookie(utmCookie); err == nil {
		if v, err := url.ParseQuery(c.Value); err == nil {
			return marketing.ParseUTM(v.Get)
		}
	}
	return marketing.UTM{}
}

// setUTM keeps the tags of a lead or a deal (other cards have none).
func (s *Server) setUTM(ctx context.Context, entity, id string, u marketing.UTM) error {
	if s.Marketing == nil || (entity != "lead" && entity != "deal") {
		return nil
	}
	return s.Marketing.SetUTM(ctx, entity, id, u)
}

// cardUTM are the tags of the card of the page: its own or, a deal's, those of its lead.
func (s *Server) cardUTM(ctx context.Context, entity, id string, data map[string]any) error {
	if s.Marketing == nil || (entity != "lead" && entity != "deal") {
		return nil
	}
	u, inherited, err := s.Marketing.UTMOf(ctx, entity, id)
	data["UTM"], data["UTMInherited"] = u, inherited
	return err
}

// apiUTM are the UTM tags in the API.
type apiUTM struct {
	Source   string `json:"utm_source,omitempty"`
	Medium   string `json:"utm_medium,omitempty"`
	Campaign string `json:"utm_campaign,omitempty"`
	Content  string `json:"utm_content,omitempty"`
	Term     string `json:"utm_term,omitempty"`
}

func (a apiUTM) utm() marketing.UTM { return marketing.UTM(a) }

// writeCRMCard writes a card, a deal's or a lead's with its UTM tags.
func (s *Server) writeCRMCard(w http.ResponseWriter, r *http.Request, status int, item any) error {
	c := s.crmCardJSON(r.PathValue("kind"), item)
	if s.Marketing != nil && (c.Kind == "lead" || c.Kind == "deal") {
		u, _, err := s.Marketing.UTMOf(r.Context(), c.Kind, c.ID)
		if err != nil {
			return err
		}
		if !u.IsZero() {
			c.UTM = &apiUTM{u.Source, u.Medium, u.Campaign, u.Content, u.Term}
		}
	}
	writeJSON(w, status, c)
	return nil
}

// PUT /api/v1/crm/{deals|leads}/{id}/utm {"utm_source": …, …} — the card's UTM tags (all of them; {} removes them).
func (s *Server) apiCRMUTM(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	kind := r.PathValue("kind")
	if e := crmEntities[kind]; e != "lead" && e != "deal" || s.Marketing == nil {
		return i18n.E("err.not_found")
	}
	if _, err := s.apiCRMItem(r, p, "write"); err != nil {
		return err
	}
	var in apiUTM
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.setUTM(r.Context(), crmEntities[kind], r.PathValue("id"), in.utm()); err != nil {
		return err
	}
	item, err := s.apiCRMItem(r, p, "read")
	if err != nil {
		return err
	}
	return s.writeCRMCard(w, r, http.StatusOK, item)
}

type apiChannel struct {
	ID          string       `json:"id,omitempty"` // "" — the cards of no channel (or a campaign)
	Name        string       `json:"name"`
	Expenses    float64      `json:"expenses"`
	Leads       int          `json:"leads"`
	Deals       int          `json:"deals"`
	Won         int          `json:"won"`
	Revenue     float64      `json:"revenue"`
	CostPerLead float64      `json:"cost_per_lead"`
	CostPerSale float64      `json:"cost_per_sale"`
	ROI         *int         `json:"roi,omitempty"` // percent; none without expenses
	Campaigns   []apiChannel `json:"campaigns,omitempty"`
}

func channelJSON(c marketing.Channel) apiChannel {
	out := apiChannel{ID: c.ID, Name: c.Name, Expenses: c.Expenses, Leads: c.Leads, Deals: c.Deals, Won: c.Won, Revenue: c.Revenue,
		CostPerLead: c.CostPerLead(), CostPerSale: c.CostPerSale()}
	if pct, ok := c.ROI(); ok {
		out.ROI = &pct
	}
	for _, x := range c.Campaigns {
		out.Campaigns = append(out.Campaigns, channelJSON(*x))
	}
	return out
}

// GET /api/v1/marketing/analytics?from=2026-10-01&to=2026-10-31 — «Сквозная аналитика» (by default this month).
func (s *Server) apiMktAnalytics(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Marketing == nil {
		return i18n.E("err.not_found")
	}
	from, to := mktPeriod(r.URL.Query())
	rep, err := s.Marketing.Through(r.Context(), p.UserID, from, to)
	if err != nil {
		return err
	}
	out := struct {
		From     string       `json:"from"`
		To       string       `json:"to"`
		Currency string       `json:"currency"`
		Channels []apiChannel `json:"channels"`
		Total    apiChannel   `json:"total"`
	}{From: from.Format(time.DateOnly), To: to.AddDate(0, 0, -1).Format(time.DateOnly), Currency: rep.Currency, Channels: []apiChannel{},
		Total: channelJSON(rep.Total)}
	for _, c := range rep.Channels {
		out.Channels = append(out.Channels, channelJSON(*c))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiExpense struct {
	ID       string  `json:"id,omitempty"`
	SourceID string  `json:"source_id"`
	Source   string  `json:"source,omitempty"`
	Day      string  `json:"day"`
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency,omitempty"`
	Campaign string  `json:"campaign,omitempty"`
	Comment  string  `json:"comment,omitempty"`
}

// GET /api/v1/marketing/sources — the advertising channels; GET /api/v1/marketing/expenses?source=&from=&to= — what
// was spent (the latest first, up to 1000).
func (s *Server) apiMktSources(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Marketing == nil {
		return i18n.E("err.not_found")
	}
	list, err := s.Marketing.AdSources(r.Context())
	if err != nil {
		return err
	}
	type src struct {
		ID        string   `json:"id"`
		Name      string   `json:"name"`
		UTM       []string `json:"utm_source"`
		CRMSource string   `json:"crm_source_id,omitempty"`
	}
	out := []src{}
	for _, a := range list {
		out = append(out, src{a.ID, a.Name, a.UTM, a.CRMSource})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMktExpenses(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Marketing == nil {
		return i18n.E("err.not_found")
	}
	q := r.URL.Query()
	var from, to time.Time
	if q.Get("from") != "" || q.Get("to") != "" {
		from, to = mktPeriod(q)
		to = to.AddDate(0, 0, -1)
	}
	list, err := s.Marketing.Expenses(r.Context(), q.Get("source"), from, to, 1000)
	if err != nil {
		return err
	}
	out := []apiExpense{}
	for _, e := range list {
		out = append(out, apiExpense{e.ID, e.SourceID, e.SourceName, e.Day.Format(time.DateOnly), e.Amount, e.Currency, e.Campaign, e.Comment})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/marketing/expenses {"source_id": …, "day": "2026-10-25", "amount": 1500, "currency": "RUB",
// "campaign": "autumn", "comment": …} — an expense (from an advertising account's script, say).
func (s *Server) apiMktAddExpense(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Marketing == nil {
		return i18n.E("err.not_found")
	}
	var in apiExpense
	if err := readJSON(r, &in); err != nil {
		return err
	}
	day, err := time.Parse(time.DateOnly, in.Day)
	if err != nil {
		return i18n.E("err.mkt_expense")
	}
	if in.ID, err = s.Marketing.AddExpense(r.Context(), p.UserID, marketing.Expense{SourceID: in.SourceID, Day: day, Amount: in.Amount,
		Currency: in.Currency, Campaign: in.Campaign, Comment: in.Comment}); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, in)
	return nil
}

// DELETE /api/v1/marketing/expenses/{id} — the key owner's expense (an administrator's — any).
func (s *Server) apiMktDeleteExpense(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Marketing == nil {
		return i18n.E("err.not_found")
	}
	if err := s.Marketing.DeleteExpense(r.Context(), p.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
