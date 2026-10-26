package web

import (
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/openlines"
)

// ---------- «Виджет на сайт»: the public side (no go24 account) ----------

//go:embed widget/widget.js
var widgetJS string

// widgetCORS answers a site's page only when the widget may be shown there.
func (s *Server) widgetCORS(w http.ResponseWriter, r *http.Request) (openlines.Widget, bool) {
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil || !wd.Active {
		http.NotFound(w, r)
		return wd, false
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		self := "http://" + r.Host // go24's own pages (the preview) may always show it
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			self = "https://" + r.Host
		}
		if !wd.Allowed(origin) && !strings.EqualFold(origin, self) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return wd, false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Go24-Visitor")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		w.Header().Set("Access-Control-Max-Age", "600")
	}
	return wd, true
}

// widgetScript is the script a site puts on its pages: <script src=".../w/{id}/widget.js" async></script>.
func (s *Server) widgetScript(w http.ResponseWriter, r *http.Request) {
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil || !wd.Active {
		http.NotFound(w, r)
		return
	}
	lang := s.lang(r)
	t := map[string]string{}
	for _, k := range []string{"open", "chat", "contact", "placeholder", "contact_note", "name", "phone", "email", "question", "send", "thanks",
		"vote", "vote_comment", "vote_thanks", "history", "history_note", "history_sent"} {
		t[k] = s.I18n.T(lang, "widget."+k)
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	cfg, _ := json.Marshal(map[string]any{"id": wd.ID, "base": scheme + "://" + r.Host, "color": wd.Color, "name": wd.Name,
		"greeting": wd.Greeting, "t": t})
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(strings.Replace(widgetJS, "/*CONFIG*/{}", string(cfg), 1)))
}

// widgetAPI: visit | messages (with the dialogue's state) | say | contact | vote | history, for the visitor whose token is in X-Go24-Visitor.
func (s *Server) widgetAPI(w http.ResponseWriter, r *http.Request) {
	wd, ok := s.widgetCORS(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in struct {
		Page, Body, Name, Phone, Email, Text string
		Vote                                 int
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	token := r.Header.Get("X-Go24-Visitor")
	var err error
	out := map[string]any{}
	switch op := r.PathValue("op"); {
	case op == "visit" && r.Method == http.MethodPost:
		_, out["token"], err = s.OpenLines.Visit(r.Context(), wd.ID, token, in.Page)
	case op == "messages" && r.Method == http.MethodGet:
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		var list []openlines.Message
		if list, err = s.OpenLines.VisitorMessages(r.Context(), wd.ID, token, after); err == nil {
			msgs := make([]map[string]any, 0, len(list))
			for _, m := range list {
				msgs = append(msgs, map[string]any{"id": m.ID, "operator": m.Operator, "body": m.Body})
			}
			out["messages"] = msgs
			var st openlines.State
			if st, err = s.OpenLines.VisitorState(r.Context(), wd.ID, token); err == nil {
				if st.VoteText == "" {
					st.VoteText = s.tr(r, "widget.vote")
				}
				out["state"] = map[string]any{"closed": st.Closed, "vote": st.Vote, "can_vote": st.CanVote, "vote_text": st.VoteText,
					"history": st.History, "email": st.Email}
			}
		}
	case op == "say" && r.Method == http.MethodPost:
		err = s.OpenLines.VisitorSay(r.Context(), wd.ID, token, in.Body)
	case op == "contact" && r.Method == http.MethodPost:
		err = s.OpenLines.Contact(r.Context(), wd.ID, token, in.Name, in.Phone, in.Email, in.Text, s.tr(r, "widget.source"))
	case op == "vote" && r.Method == http.MethodPost:
		err = s.OpenLines.VisitorVote(r.Context(), wd.ID, token, in.Vote, in.Text)
	case op == "history" && r.Method == http.MethodPost:
		err = s.OpenLines.SendHistory(r.Context(), wd.ID, token, in.Email)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------- «Открытые линии»: the operators' side ----------

// olTabs are the tabs of the dialogue list.
var olTabs = []string{"mine", "noanswer", "all", "closed"}

func (s *Server) openlinesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	tab := q.Get("tab")
	if q.Get("closed") == "1" { // the old link of «Закрытые»
		tab = "closed"
	}
	if !slices.Contains(olTabs, tab) {
		tab = "all"
	}
	list, err := s.OpenLines.Sessions(r.Context(), openlines.Filter{Tab: tab, Q: q.Get("q"), UserID: sess.UserID})
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	counts, err := s.OpenLines.Counts(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	quick, err := s.OpenLines.Quick(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	// the list's own address: a dialogue opened from it keeps the tab and the search
	keep := url.Values{}
	if tab != "all" {
		keep.Set("tab", tab)
	}
	if v := q.Get("q"); v != "" {
		keep.Set("q", v)
	}
	type tabLink struct {
		Tab, URL string
		Count    int
	}
	tabs := []tabLink{}
	for _, t := range olTabs {
		v := url.Values{}
		if t != "all" {
			v.Set("tab", t)
		}
		if x := q.Get("q"); x != "" {
			v.Set("q", x)
		}
		link := "/openlines"
		if len(v) > 0 {
			link += "?" + v.Encode()
		}
		tabs = append(tabs, tabLink{t, link, counts[t]})
	}
	data := map[string]any{"Sessions": list, "Tab": tab, "Tabs": tabs, "Q": q.Get("q"), "Keep": keep.Encode(), "Quick": quick}
	if id := q.Get("s"); id != "" {
		x, msgs, err := s.OpenLines.Open(r.Context(), sess.UserID, id)
		if err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
		users, err := s.Tasks.ListUsers(r.Context())
		if err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
		data["Current"], data["Messages"], data["Users"] = x, msgs, users
		data["CanRate"] = s.olHead(r, sess, x.OperatorID)
	}
	s.render(w, r, "openlines", page{TitleKey: "ol.title", Nav: "openlines", Data: data})
}

// olHead: may the user rate the operator's work in a dialogue — administrators and the operator's managers, as the
// heads of the open lines.
func (s *Server) olHead(r *http.Request, sess auth.Session, operator string) bool {
	if sess.Admin {
		return true
	}
	if operator == "" || operator == sess.UserID || s.Company == nil {
		return false
	}
	heads, err := s.Company.Managers(r.Context(), operator)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(heads, func(p company.Person) bool { return p.ID == sess.UserID })
}

// openlinesAction: reply (body), close, reopen, lead, transfer (to), spam, rate (vote, comment). back — the list's tab and search.
func (s *Server) openlinesAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	list := "/openlines"
	if v, err := url.ParseQuery(r.FormValue("back")); err == nil && len(v) > 0 {
		list += "?" + url.Values{"tab": v["tab"], "q": v["q"]}.Encode()
	}
	sep := "?"
	if strings.Contains(list, "?") {
		sep = "&"
	}
	back := list + sep + "s=" + url.QueryEscape(id)
	var err error
	switch r.FormValue("op") {
	case "reply":
		err = s.OpenLines.Reply(r.Context(), sess.UserID, id, r.FormValue("body"))
	case "close":
		err = s.OpenLines.SetStatus(r.Context(), sess.UserID, id, false)
		back = list
	case "reopen":
		err = s.OpenLines.SetStatus(r.Context(), sess.UserID, id, true)
	case "transfer":
		err = s.OpenLines.Transfer(r.Context(), sess.UserID, id, r.FormValue("to"))
	case "spam":
		err = s.OpenLines.Spam(r.Context(), sess.UserID, id)
		back = list
	case "rate":
		var x openlines.Session
		if x, _, err = s.OpenLines.Open(r.Context(), sess.UserID, id); err == nil {
			if !s.olHead(r, sess, x.OperatorID) {
				err = i18n.E("err.forbidden")
			} else {
				vote, _ := strconv.Atoi(r.FormValue("vote"))
				err = s.OpenLines.HeadVote(r.Context(), sess.UserID, id, vote, r.FormValue("comment"))
			}
		}
	case "lead":
		if err = s.CRM.Check(r.Context(), sess.UserID, "lead", "write", sess.UserID); err == nil {
			var card string
			if card, err = s.OpenLines.MakeLead(r.Context(), sess.UserID, id, s.tr(r, "widget.source")); err == nil {
				back = card
			}
		}
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		back = list + sep + "s=" + url.QueryEscape(id) + "&msg=" + url.QueryEscape(s.errText(r, err))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// openlinesLive tells an open «Открытые линии» page which dialogue changed (a message, its operator, status): the
// page fetches itself anew.
func (s *Server) openlinesLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Live == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	events, stop := s.Live.Subscribe("ol")
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case id := <-events:
			fmt.Fprintf(w, "event: session\ndata: %s\n\n", id)
			flusher.Flush()
		}
	}
}

// olQuickPage: «Быстрые ответы» — one's own and the company's (administrators change those).
func (s *Server) olQuickPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.OpenLines.Quick(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	s.render(w, r, "olquick", page{TitleKey: "ol.quick", Nav: "openlines", Data: map[string]any{"Quick": list}})
}

func (s *Server) olQuickSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	_, err := s.OpenLines.SaveQuick(r.Context(), sess.UserID, sess.Admin, openlines.Quick{ID: id, Title: r.FormValue("title"), Body: r.FormValue("body"),
		Common: r.FormValue("common") == "on"})
	if err != nil {
		http.Redirect(w, r, "/openlines/quick?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/quick", http.StatusSeeOther)
}

func (s *Server) olQuickDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.OpenLines.DeleteQuick(r.Context(), sess.UserID, sess.Admin, r.PathValue("id")); err != nil {
		http.Redirect(w, r, "/openlines/quick?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/quick", http.StatusSeeOther)
}

// widgetsPage: the widgets and their code to put on a site (administrators).
func (s *Server) widgetsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.OpenLines.Widgets(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	base := s.BaseURL
	if s.CallURL != "" { // the https address: a site on https loads only https scripts
		base = s.CallURL
	}
	fresh := openlines.Widget{QueueMode: "all", AnswerWait: 3, HoursDays: []int32{1, 2, 3, 4, 5}, HoursFrom: "09:00", HoursTo: "18:00", NoanswerAfter: 5,
		VoteOn: true, HistoryOn: true, CRMAuto: true}
	s.render(w, r, "widgets", page{TitleKey: "ol.widgets", Nav: "openlines", Data: map[string]any{"Widgets": list, "Users": users, "Base": base,
		"New": fresh, "Days": []int{1, 2, 3, 4, 5, 6, 0}}})
}

func (s *Server) widgetSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/openlines/widgets")
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	num := func(k string) int { n, _ := strconv.Atoi(r.FormValue(k)); return n }
	days := []int32{}
	for _, d := range r.Form["hours_days"] {
		if n, err := strconv.Atoi(d); err == nil {
			days = append(days, int32(n))
		}
	}
	_, err := s.OpenLines.SaveWidget(r.Context(), sess.UserID, openlines.Widget{ID: id, Name: r.FormValue("name"), Greeting: r.FormValue("greeting"),
		Color: r.FormValue("color"), Origins: strings.Fields(r.FormValue("origins")), Operators: r.Form["operators"], Active: r.FormValue("active") == "on",
		QueueMode: r.FormValue("queue_mode"), AnswerWait: num("answer_wait"), HoursOn: r.FormValue("hours_on") == "on", HoursDays: days,
		HoursFrom: r.FormValue("hours_from"), HoursTo: r.FormValue("hours_to"), OfflineText: r.FormValue("offline_text"),
		WelcomeText: r.FormValue("welcome_text"), NoanswerText: r.FormValue("noanswer_text"), NoanswerAfter: num("noanswer_after"),
		CloseText: r.FormValue("close_text"), Autoclose: num("autoclose"),
		VoteOn: r.FormValue("vote_on") == "on", VoteText: r.FormValue("vote_text"), HistoryOn: r.FormValue("history_on") == "on",
		CRMAuto: r.FormValue("crm_auto") == "on"})
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

func (s *Server) widgetDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.OpenLines.DeleteWidget(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/openlines/widgets")
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetPreview is a page of go24 with the widget on it, to try it before putting it on a site.
func (s *Server) widgetPreview(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/openlines/widgets")
		return
	}
	s.render(w, r, "widgetpreview", page{Title: wd.Name, Nav: "openlines", Data: wd})
}

// ---------- «Статистика» ----------

var olStatsBy = []string{"days", "lines", "operators"}

// openlinesStats is the report of the lines for the days ?from= (the month's first day) to ?to= (today), by days, lines
// or operators (?by=), of a line (?line=) and an operator (?operator=); ?csv=1 gives the table as a CSV file. An
// administrator sees every operator; the others — their own dialogues.
func (s *Server) openlinesStats(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	f := openlines.StatsFilter{From: today.AddDate(0, 0, 1-today.Day()), To: today, Widget: q.Get("line"), OperatorID: q.Get("operator")}
	for k, t := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if d := formDay(q.Get(k)); d != nil {
			*t = *d
		}
	}
	if f.To.Before(f.From) {
		f.From, f.To = f.To, f.From
	}
	if f.To.Sub(f.From) > 366*24*time.Hour { // a year at most: the days' table stays a page
		f.From = f.To.AddDate(-1, 0, 0)
	}
	if !sess.Admin {
		f.OperatorID = sess.UserID
	}
	by := q.Get("by")
	if !slices.Contains(olStatsBy, by) {
		by = "days"
	}
	st, err := s.OpenLines.Stats(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	rows := map[string][]openlines.StatsRow{"days": st.Days, "lines": st.Lines, "operators": st.Operators}[by]
	if q.Get("csv") == "1" {
		s.openlinesStatsCSV(w, r, by, rows, st.Total)
		return
	}
	widgets, err := s.OpenLines.Widgets(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	data := map[string]any{"F": f, "By": by, "BySet": olStatsBy, "Stats": st, "Rows": rows, "Widgets": widgets}
	if sess.Admin {
		if data["Users"], err = s.Tasks.ListUsers(r.Context()); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	link := func(k, v string) string {
		x := r.URL.Query()
		x.Del("csv")
		x.Set(k, v)
		return r.URL.Path + "?" + x.Encode()
	}
	byLinks := map[string]string{}
	for _, b := range olStatsBy {
		byLinks[b] = link("by", b)
	}
	data["ByLinks"], data["CSV"] = byLinks, link("csv", "1")
	s.render(w, r, "olstats", page{TitleKey: "ol.stats", Nav: "openlines", Data: data})
}

// openlinesStatsCSV gives the report's table as the page shows it, with the totals' row, as a CSV file Excel opens.
func (s *Server) openlinesStatsCSV(w http.ResponseWriter, r *http.Request, by string, rows []openlines.StatsRow, total openlines.StatsRow) {
	tr := func(k string) string { return s.tr(r, k) }
	secs := func(d time.Duration) string { return strconv.Itoa(int(d / time.Second)) }
	first := map[string]string{"days": "ol.stats_day", "lines": "ol.stats_line", "operators": "ol.operator"}[by]
	head := []string{tr(first)}
	for _, k := range []string{"ol.stats_new", "ol.stats_closed", "ol.stats_noanswer", "ol.stats_answer_s", "ol.stats_close_s", "ol.stats_up", "ol.stats_down", "ol.stats_spam"} {
		head = append(head, tr(k))
	}
	line := func(name string, x openlines.StatsRow) []string {
		return []string{name, strconv.Itoa(x.New), strconv.Itoa(x.Closed), strconv.Itoa(x.NoAnswer), secs(x.FirstAnswer()), secs(x.ToClose()),
			strconv.Itoa(x.Up), strconv.Itoa(x.Down), strconv.Itoa(x.Spam)}
	}
	out := [][]string{}
	for _, x := range rows {
		name := x.Name
		switch {
		case by == "days":
			name = x.Day.Format(tr("format.date"))
		case name == "":
			name = tr("ol.stats_nobody")
		}
		out = append(out, line(name, x))
	}
	out = append(out, line(tr("stock.total"), total))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+"openlines-"+by+"-"+time.Now().Format(time.DateOnly)+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(out)
}
