package web

import (
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go24/internal/auth"
	"go24/internal/bi"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «BI-аналитика»: dashboards of widgets, as the «BI Конструктор» ----------

const (
	biMeasureN = 3 // measures of a widget in the editor
	biFilterN  = 5 // filters of a widget in the editor
)

// biPanel is a widget of a dashboard with its chart.
type biPanel struct {
	W     bi.Widget
	Chart biChart
	Title string // its own, or what it shows: «Сделки: Количество»
}

// biTitle is a widget's own title or the names of what it counts.
func biTitle(w bi.Widget, tr func(string) string) string {
	if strings.HasPrefix(w.Title, "bi.sw.") { // a widget of a ready dashboard
		return tr(w.Title)
	}
	if w.Title != "" {
		return w.Title
	}
	t := tr("bi.ds." + w.Query.Dataset)
	if len(w.Query.Measures) > 0 {
		t += ": " + biNamed(bi.Result{Columns: []bi.Column{{Field: w.Query.Measures[0].Field, Agg: w.Query.Measures[0].Agg}}}, tr).Columns[0].Name
	}
	return t
}

// biPeriod is the period a dashboard is viewed over: the one picked on the page (?period=, ?from=, ?to=), else its own.
func biPeriod(r *http.Request, d bi.Dashboard) (period string, from, to *time.Time) {
	period, from, to = d.Period, d.DateFrom, d.DateTo
	if p := r.URL.Query().Get("period"); slices.Contains(bi.Periods, p) {
		period, from, to = p, biDate(r.URL.Query().Get("from")), biDate(r.URL.Query().Get("to"))
	}
	return
}

func biDate(v string) *time.Time {
	t, err := time.ParseInLocation("2006-01-02", v, time.Local)
	if err != nil {
		return nil
	}
	return &t
}

// biPanels runs the widgets' queries side by side, each over the period unless it has none of its own.
func (s *Server) biPanels(r *http.Request, viewer string, list []bi.Widget, from, to *time.Time) []biPanel {
	tr := func(k string) string { return s.tr(r, k) }
	out := make([]biPanel, len(list))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, w := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.biPanel(r, viewer, w, from, to, tr)
		}()
	}
	wg.Wait()
	return out
}

func (s *Server) biPanel(r *http.Request, viewer string, w bi.Widget, from, to *time.Time, tr func(string) string) biPanel {
	tr = s.biTr(r, w.Query.Dataset, tr)
	p := biPanel{W: w, Title: biTitle(w, tr)}
	q := w.Query
	if !w.NoPeriod {
		q.From, q.To = from, to
	}
	res, err := s.BI.Run(r.Context(), viewer, q, tr)
	if err != nil {
		p.Chart = biChart{Kind: w.Kind, Err: s.biErr(r, err)}
		return p
	}
	p.Chart = biDraw(w, biNamed(res, tr), tr)
	return p
}

// biTr is tr that knows the names of a set of one's own and of its fields.
func (s *Server) biTr(r *http.Request, code string, tr func(string) string) func(string) string {
	if !bi.IsFile(code) {
		return tr
	}
	ds, _ := s.BI.Dataset(r.Context(), code)
	return ds.Tr(tr)
}

// biErr is the message of an error of a query: its own text, or «Не удалось» for a failure of the database.
func (s *Server) biErr(r *http.Request, err error) string {
	var ie *i18n.Error
	if errors.As(err, &ie) {
		return s.I18n.Err(s.lang(r), ie)
	}
	s.Log.Error("bi", "err", err)
	return s.tr(r, "bi.err.query")
}

// biList: «BI-аналитика» — the dashboards, a new one.
// biSystem gives a ready dashboard its title and description in the viewer's language.
func (s *Server) biSystem(r *http.Request, d *bi.Dashboard) {
	if d.Code != "" {
		d.Title, d.Description = s.tr(r, "bi.sys."+d.Code), s.tr(r, "bi.sysd."+d.Code)
	}
}

// biList: the ready dashboards, then the made ones.
func (s *Server) biList(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q, group := r.URL.Query().Get("q"), r.URL.Query().Get("group")
	list, err := s.BI.Dashboards(r.Context(), biViewer(sess), q, group)
	if err != nil {
		s.fail(w, "bi", err)
		return
	}
	groups, err := s.BI.Groups(r.Context())
	if err != nil {
		s.fail(w, "bi", err)
		return
	}
	var ready, own []bi.Dashboard
	for _, d := range list {
		if d.Code == "" {
			own = append(own, d)
			continue
		}
		if group != "" {
			continue
		}
		s.biSystem(r, &d)
		if strings.Contains(strings.ToLower(d.Title), strings.ToLower(strings.TrimSpace(q))) {
			ready = append(ready, d)
		}
	}
	s.render(w, r, "bi", page{TitleKey: "bi.title", Nav: "bi", Data: map[string]any{
		"Ready": ready, "Dashboards": own, "Q": q, "Group": group, "Groups": groups, "UserID": sess.UserID, "Admin": sess.Admin,
		"Periods": bi.Periods}})
}

// biDashboardForm reads the settings of a dashboard: its title, description and period.
func biDashboardForm(r *http.Request) bi.Dashboard {
	return bi.Dashboard{ID: r.PathValue("id"), Title: r.FormValue("title"), Description: strings.TrimSpace(r.FormValue("description")),
		Period: r.FormValue("period"), DateFrom: biDate(r.FormValue("date_from")), DateTo: biDate(r.FormValue("date_to"))}
}

func (s *Server) biCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.BI.SaveDashboard(r.Context(), sess.UserID, biDashboardForm(r))
	if err != nil {
		s.taskErr(w, r, err, "/bi")
		return
	}
	http.Redirect(w, r, "/bi/"+id+"?edit=1", http.StatusSeeOther)
}

// biViewer is the user as the dashboards see them.
func biViewer(sess auth.Session) bi.Viewer { return bi.Viewer{ID: sess.UserID, Admin: sess.Admin} }

// biEditable is the dashboard of the path when the user may change it.
func (s *Server) biEditable(w http.ResponseWriter, r *http.Request, sess auth.Session, id string) (bi.Dashboard, bool) {
	return s.biAllowed(w, r, sess, id, bi.Dashboard.CanEdit)
}

// biManaged is the dashboard of the path when the user may publish it, give it rights, delete it.
func (s *Server) biManaged(w http.ResponseWriter, r *http.Request, sess auth.Session, id string) (bi.Dashboard, bool) {
	return s.biAllowed(w, r, sess, id, func(d bi.Dashboard) bool { return d.CanManage(biViewer(sess)) })
}

func (s *Server) biAllowed(w http.ResponseWriter, r *http.Request, sess auth.Session, id string, may func(bi.Dashboard) bool) (bi.Dashboard, bool) {
	d, err := s.BI.Dashboard(r.Context(), biViewer(sess), id)
	if err == nil && !may(d) {
		err = tasks.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, "/bi")
		return d, false
	}
	return d, true
}

// biDashboard: a dashboard — its widgets over the period picked; ?edit=1 — with the tools to change them.
func (s *Server) biDashboard(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.BI.Dashboard(r.Context(), biViewer(sess), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/bi")
		return
	}
	data, err := s.biBoard(r, sess, d, "/bi/"+d.ID)
	if err != nil {
		s.fail(w, "bi", err)
		return
	}
	can, manage := d.CanEdit(), d.CanManage(biViewer(sess))
	pin := biCanPin(d, sess)
	data["Can"], data["Manage"], data["CanPin"] = can, manage, pin
	data["Edit"] = can && r.URL.Query().Get("edit") != ""
	data["Settings"] = can && r.URL.Query().Get("settings") != ""
	data["PinForm"] = pin && (data["Settings"] == true || r.URL.Query().Get("pin") != "")
	data["Sections"] = bi.Sections
	if manage && data["Settings"] == true {
		if err := s.biRightsData(r, data, bi.On{Dashboard: d.ID}); err != nil {
			s.fail(w, "bi", err)
			return
		}
	}
	s.render(w, r, "bidash", page{Title: d.Title, Nav: "bi", Data: data})
}

// biBoard is what the widgets of a dashboard show over the period picked on the page at path.
func (s *Server) biBoard(r *http.Request, sess auth.Session, d bi.Dashboard, path string) (map[string]any, error) {
	list, err := s.BI.Widgets(r.Context(), d.ID)
	if err != nil {
		return nil, err
	}
	s.biSystem(r, &d)
	period, pf, pt := biPeriod(r, d)
	from, to := bi.Span(period, pf, pt, time.Now())
	keep := biKeep(r)
	link := "?d=" + d.ID // «Ссылка»: the dashboard with the period picked, where it is opened
	if !strings.HasPrefix(path, "/bi/") {
		if keep != "" {
			link += "&" + keep[1:]
		}
	} else {
		link = keep
	}
	return map[string]any{
		"D": d, "Panels": s.biPanels(r, sess.UserID, list, from, to), "Period": period, "PFrom": pf, "PTo": pt,
		"From": from, "Last": biLastDay(to), "Periods": bi.Periods, "Widths": bi.Widths, "Kinds": bi.Kinds,
		"Path": path, "Keep": keep, "Self": "/bi/" + d.ID + keep, "Link": s.BaseURL + path + link}, nil
}

// biCanPin: who sets the sections a dashboard is shown in — those who manage it; a ready one — the administrators.
func biCanPin(d bi.Dashboard, sess auth.Session) bool {
	return d.CanManage(biViewer(sess)) || d.Code != "" && sess.Admin
}

// biPin: «Показывать в разделах» — the sections the dashboard is shown in (sections=crm&sections=tasks…).
func (s *Server) biPin(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, ok := s.biAllowed(w, r, sess, r.PathValue("id"), func(d bi.Dashboard) bool { return biCanPin(d, sess) })
	if !ok {
		return
	}
	r.ParseForm()
	if err := s.BI.Pin(r.Context(), d.ID, r.Form["sections"]); err != nil {
		s.taskErr(w, r, err, "/bi/"+d.ID)
		return
	}
	http.Redirect(w, r, "/bi/"+d.ID, http.StatusSeeOther)
}

// biKeep is the period picked on the page, to keep in the links of its actions.
func biKeep(r *http.Request) string {
	v := url.Values{}
	for _, k := range []string{"period", "from", "to"} {
		if x := r.URL.Query().Get(k); x != "" {
			v.Set(k, x)
		}
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// biLastDay is the last day of a period ending at to (exclusive).
func biLastDay(to *time.Time) *time.Time {
	if to == nil {
		return nil
	}
	t := to.AddDate(0, 0, -1)
	return &t
}

func (s *Server) biSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, ok := s.biEditable(w, r, sess, r.PathValue("id")); !ok {
		return
	}
	back := "/bi/" + r.PathValue("id")
	if _, err := s.BI.SaveDashboard(r.Context(), sess.UserID, biDashboardForm(r)); err != nil {
		s.taskErr(w, r, err, back+"?settings=1")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// biCopy: «Копировать» — anyone who sees a dashboard may make a copy of their own.
func (s *Server) biCopy(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.BI.Dashboard(r.Context(), biViewer(sess), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/bi")
		return
	}
	title := s.tr(r, "bi.copy_of", d.Title)
	if d.Code != "" {
		s.biSystem(r, &d)
		title = d.Title
	}
	id, err := s.BI.CopyDashboard(r.Context(), sess.UserID, d.ID, title, func(k string) string { return s.tr(r, k) })
	if err != nil {
		s.taskErr(w, r, err, "/bi/"+d.ID)
		return
	}
	http.Redirect(w, r, "/bi/"+id+"?edit=1", http.StatusSeeOther)
}

func (s *Server) biDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, ok := s.biManaged(w, r, sess, r.PathValue("id")); !ok {
		return
	}
	if err := s.BI.DeleteDashboard(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/bi")
		return
	}
	http.Redirect(w, r, "/bi", http.StatusSeeOther)
}

// biOrder: the widgets dragged to a new order (ids=…, in their order).
func (s *Server) biOrder(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, ok := s.biEditable(w, r, sess, r.PathValue("id")); !ok {
		return
	}
	r.ParseForm()
	if err := s.BI.OrderWidgets(r.Context(), r.PathValue("id"), r.Form["ids"]); err != nil {
		s.taskErr(w, r, err, "/bi/"+r.PathValue("id")+"?edit=1")
		return
	}
	if fromForm(r) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	http.Redirect(w, r, "/bi/"+r.PathValue("id")+"?edit=1", http.StatusSeeOther)
}

// biWidgetOf is the widget of the path and its dashboard when the user may change it.
func (s *Server) biWidgetOf(w http.ResponseWriter, r *http.Request, sess auth.Session) (bi.Widget, bool) {
	x, err := s.BI.Widget(r.Context(), r.PathValue("wid"))
	if err != nil {
		s.taskErr(w, r, err, "/bi")
		return x, false
	}
	_, ok := s.biEditable(w, r, sess, x.DashboardID)
	return x, ok
}

// biWidgetAct: the tools of a widget on the dashboard — «Копировать», «Удалить», earlier and later, the width.
func (s *Server) biWidgetAct(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, ok := s.biWidgetOf(w, r, sess)
	if !ok {
		return
	}
	back := "/bi/" + x.DashboardID + "?edit=1"
	var err error
	switch r.PathValue("act") {
	case "copy":
		_, err = s.BI.CopyWidget(r.Context(), x.ID)
	case "delete":
		err = s.BI.DeleteWidget(r.Context(), x.ID)
	case "up":
		err = s.BI.MoveWidget(r.Context(), x.ID, -1)
	case "down":
		err = s.BI.MoveWidget(r.Context(), x.ID, 1)
	case "width":
		x.Width = atoi(r.FormValue("width"))
		_, err = s.BI.SaveWidget(r.Context(), x)
	default:
		err = bi.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#w-"+x.ID, http.StatusSeeOther)
}

// biWidgetForm is a widget as the editor's form has it; the fields the data set has not are left out.
func (s *Server) biWidgetForm(r *http.Request, x bi.Widget) bi.Widget {
	x.Kind = r.FormValue("kind")
	if !slices.Contains(bi.Kinds, x.Kind) {
		x.Kind = "bar"
	}
	x.Title = strings.TrimSpace(r.FormValue("title"))
	x.Width = atoi(r.FormValue("width"))
	if !slices.Contains(bi.Widths, x.Width) {
		x.Width = 6
	}
	x.NoPeriod = r.FormValue("no_period") != ""
	ds, _ := s.BI.Dataset(r.Context(), r.FormValue("dataset"))
	if ds == nil {
		ds = bi.Datasets[0]
	}
	q := bi.Query{Dataset: ds.Code}
	isDim := func(code string) bool { f, ok := ds.Field(code); return ok && f.Dim() }
	dim := func(n string) {
		code := r.FormValue("dim" + n)
		if !isDim(code) {
			return
		}
		if f, _ := ds.Field(code); f.Type == bi.Date {
			g := r.FormValue("grain" + n)
			if !slices.Contains(bi.Grains, g) {
				g = "month"
			}
			code += ":" + g
		}
		q.Dims = append(q.Dims, code)
	}
	if x.Kind != "kpi" {
		dim("")
		if len(q.Dims) > 0 && x.Kind != "pie" {
			dim("2")
		}
	}
	for i := range biMeasureN {
		n := strconv.Itoa(i)
		agg, code := r.FormValue("agg"+n), r.FormValue("mfield"+n)
		f, ok := ds.Field(code)
		if !ok && agg != "count" { // a function just picked: its first field until another is
			for _, x := range ds.Fields {
				if x.Type == bi.Number || agg == "count_distinct" {
					f, code, ok = x, x.Code, true
					break
				}
			}
		}
		switch {
		case agg == "count":
			q.Measures = append(q.Measures, bi.Measure{Agg: agg})
		case agg == "count_distinct" && ok:
			q.Measures = append(q.Measures, bi.Measure{Agg: agg, Field: code})
		case slices.Contains(bi.Aggregates, agg) && ok && f.Type == bi.Number:
			q.Measures = append(q.Measures, bi.Measure{Agg: agg, Field: code})
		}
		if x.Kind == "pie" || (len(q.Dims) > 1 && len(q.Measures) > 0) {
			break // one measure for a pie and a split chart
		}
	}
	if len(q.Measures) == 0 {
		q.Measures = []bi.Measure{{Agg: "count"}}
	}
	for i := range biFilterN {
		n := strconv.Itoa(i)
		code, op, v := r.FormValue("ffield"+n), r.FormValue("fop"+n), strings.TrimSpace(r.FormValue("fvalue"+n))
		if _, ok := ds.Field(code); !ok || !slices.Contains(biOps, op) {
			continue
		}
		if v == "" && !slices.Contains([]string{"set", "empty", "yes", "no"}, op) {
			continue // still being typed
		}
		if op == "in" {
			var vals []string
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					vals = append(vals, p)
				}
			}
			q.Filters = append(q.Filters, bi.Filter{Field: code, Op: op, Values: vals})
			continue
		}
		q.Filters = append(q.Filters, bi.Filter{Field: code, Op: op, Value: v})
	}
	if f, ok := ds.Field(r.FormValue("date_field")); ok && f.Type == bi.Date && f.Code != ds.Period {
		q.DateField = f.Code
	}
	switch r.FormValue("sort") {
	case "m_desc":
		q.Sort = -(len(q.Dims) + 1)
	case "m_asc":
		q.Sort = len(q.Dims) + 1
	case "d_asc":
		q.Sort = 1
	case "d_desc":
		q.Sort = -1
	}
	if len(q.Dims) == 0 {
		q.Sort = 0
	}
	if n := atoi(r.FormValue("limit")); n > 0 {
		q.Limit = n
	}
	x.Query = q
	return x
}

// biFormVals are a widget's query as the editor's form shows it.
type biFormVals struct {
	Dim, Grain, Dim2, Grain2 string
	Measures                 []bi.Measure
	Filters                  []biFilterVal
	Sort                     string
	Limit                    int
	DateField                string
}

type biFilterVal struct {
	bi.Filter
	Text string // the value, the values of «in» joined
	Type string // of the field: a date is picked by a calendar
}

// biFilterRows are the filters as typed in the form, those still without a value too.
func biFilterRows(r *http.Request) []biFilterVal {
	out := make([]biFilterVal, biFilterN)
	for i := range out {
		n := strconv.Itoa(i)
		out[i] = biFilterVal{Filter: bi.Filter{Field: r.FormValue("ffield" + n), Op: r.FormValue("fop" + n)},
			Text: strings.TrimSpace(r.FormValue("fvalue" + n))}
		if out[i].Field == "" {
			out[i] = biFilterVal{}
		}
	}
	return out
}

func biVals(x bi.Widget) biFormVals {
	q := x.Query
	v := biFormVals{Measures: make([]bi.Measure, biMeasureN), Filters: make([]biFilterVal, biFilterN), Limit: q.Limit,
		DateField: q.DateField}
	if len(q.Dims) > 0 {
		v.Dim, v.Grain, _ = strings.Cut(q.Dims[0], ":")
	}
	if len(q.Dims) > 1 {
		v.Dim2, v.Grain2, _ = strings.Cut(q.Dims[1], ":")
	}
	copy(v.Measures, q.Measures)
	for i, f := range q.Filters {
		if i < biFilterN {
			v.Filters[i] = biFilterVal{Filter: f, Text: f.Value}
			if f.Op == "in" {
				v.Filters[i].Text = strings.Join(f.Values, ", ")
			}
		}
	}
	n := len(q.Dims) + 1
	switch q.Sort {
	case -n:
		v.Sort = "m_desc"
	case n:
		v.Sort = "m_asc"
	case 1:
		v.Sort = "d_asc"
	case -1:
		v.Sort = "d_desc"
	}
	return v
}

// biWidgetPage: the widget's editor — the form on the left, the chart as it is now on the right. Each change of the
// form comes back here (GET) and redraws the chart; «Сохранить» posts it.
func (s *Server) biWidgetPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, ok := s.biEditable(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	x := bi.Widget{DashboardID: d.ID, Kind: "bar", Width: 6,
		Query: bi.Query{Dataset: bi.Datasets[0].Code, Dims: []string{"stage"}, Measures: []bi.Measure{{Agg: "count"}}}}
	if id := r.URL.Query().Get("w"); id != "" {
		var err error
		if x, err = s.BI.Widget(r.Context(), id); err != nil || x.DashboardID != d.ID {
			s.taskErr(w, r, bi.ErrNotFound, "/bi/"+d.ID)
			return
		}
	}
	typed := r.URL.Query().Has("dataset")
	if typed {
		x = s.biWidgetForm(r, x)
	} else if k := r.URL.Query().Get("kind"); slices.Contains(bi.Kinds, k) && x.ID == "" {
		x.Kind = k
		if k == "kpi" {
			x.Query.Dims, x.Width = nil, 3
		}
	}
	period, pf, pt := biPeriod(r, d)
	from, to := bi.Span(period, pf, pt, time.Now())
	tr := func(k string) string { return s.tr(r, k) }
	v := biVals(x)
	ds, err := s.BI.Dataset(r.Context(), x.Query.Dataset)
	if err != nil {
		s.fail(w, "bi widget", err)
		return
	}
	if ds == nil { // the set of one's own was deleted: the first one to pick another
		ds = bi.Datasets[0]
	}
	sets, err := s.BI.AllDatasets(r.Context())
	if err != nil {
		s.fail(w, "bi widget", err)
		return
	}
	if typed {
		v.Filters = biFilterRows(r)
	}
	// the rows filled and one empty to add another
	var filters []biFilterVal
	for _, f := range v.Filters {
		if fl, ok := ds.Field(f.Field); ok {
			f.Type = fl.Type
			filters = append(filters, f)
		}
	}
	if len(filters) < biFilterN {
		filters = append(filters, biFilterVal{})
	}
	v.Filters = filters
	n := len(x.Query.Measures)
	if n < biMeasureN {
		n++
	}
	v.Measures = v.Measures[:n]
	isDate := func(code string) bool { f, ok := ds.Field(code); return ok && f.Type == bi.Date }
	s.render(w, r, "biwidget", page{TitleKey: "bi.widget", Nav: "bi", Data: map[string]any{
		"D": d, "W": x, "V": v, "DS": ds, "DimDate": isDate(v.Dim), "Dim2Date": isDate(v.Dim2), "Datasets": sets, "SetSections": bi.SetSections, "HasFiles": len(sets) > len(bi.Datasets),
		"Panel": s.biPanel(r, sess.UserID, x, from, to, tr), "Kinds": bi.Kinds, "Widths": bi.Widths,
		"Grains": bi.Grains, "Aggs": bi.Aggregates, "Ops": biOps, "Period": period}})
}

// biOps are the conditions of a filter in the editor.
var biOps = []string{"eq", "ne", "in", "contains", "gt", "gte", "lt", "lte", "set", "empty", "yes", "no"}

// biWidgetSave: «Сохранить» in the editor — a new widget (no ?w=) at the end of the dashboard, or the one edited.
func (s *Server) biWidgetSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, ok := s.biEditable(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	x := bi.Widget{DashboardID: d.ID}
	if id := r.URL.Query().Get("w"); id != "" {
		var err error
		if x, err = s.BI.Widget(r.Context(), id); err != nil || x.DashboardID != d.ID {
			s.taskErr(w, r, bi.ErrNotFound, "/bi/"+d.ID)
			return
		}
	}
	x = s.biWidgetForm(r, x)
	id, err := s.BI.SaveWidget(r.Context(), x)
	if err != nil {
		s.taskErr(w, r, err, "/bi/"+d.ID+"/widget?w="+x.ID)
		return
	}
	http.Redirect(w, r, "/bi/"+d.ID+"?edit=1#w-"+id, http.StatusSeeOther)
}

// biSection: «BI-аналитика» of a section (crm, tasks, company) — the dashboards pinned to it as tabs, ?d= the one open
// (the first by default), as the BI dashboards in the CRM and the tasks.
func (s *Server) biSection(section string) func(http.ResponseWriter, *http.Request, auth.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess auth.Session) {
		list, err := s.BI.Pinned(r.Context(), biViewer(sess), section)
		if err != nil {
			s.fail(w, "bi", err)
			return
		}
		for i := range list {
			s.biSystem(r, &list[i])
		}
		path := "/" + section + "/bi"
		data := map[string]any{}
		if len(list) > 0 {
			d := list[0]
			for _, x := range list {
				if x.ID == r.URL.Query().Get("d") {
					d = x
				}
			}
			if data, err = s.biBoard(r, sess, d, path); err != nil {
				s.fail(w, "bi", err)
				return
			}
		}
		data["In"], data["Pinned"], data["Kind"] = section, list, "bi"
		if section == "crm" {
			if err := s.crmTabs(r, sess, data); err != nil {
				s.fail(w, "bi", err)
				return
			}
		}
		s.render(w, r, "bisection", page{TitleKey: "bi.title", Nav: map[string]string{"crm": "crm_bi", "tasks": "taskbi", "company": "companybi"}[section], Data: data})
	}
}

// biCSV: «Экспорт в CSV» — the rows of a widget over the period of the page (all of them, not only those shown), as
// a file Excel opens: the dimensions as on the dashboard, the measures as numbers.
func (s *Server) biCSV(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.BI.Widget(r.Context(), r.PathValue("wid"))
	var d bi.Dashboard
	if err == nil {
		d, err = s.BI.Dashboard(r.Context(), biViewer(sess), x.DashboardID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/bi")
		return
	}
	tr := s.biTr(r, x.Query.Dataset, func(k string) string { return s.tr(r, k) })
	q := x.Query
	if !x.NoPeriod {
		period, pf, pt := biPeriod(r, d)
		q.From, q.To = bi.Span(period, pf, pt, time.Now())
	}
	res, err := s.BI.Run(r.Context(), sess.UserID, q, tr)
	if err != nil {
		s.taskErr(w, r, err, "/bi/"+d.ID)
		return
	}
	named := biNamed(res, tr)
	head := make([]string, len(named.Columns))
	for i, c := range named.Columns {
		head[i] = c.Name
	}
	rows := make([][]string, len(res.Rows))
	for n, row := range res.Rows {
		rows[n] = make([]string, len(row))
		for i, v := range row {
			if c := named.Columns[i]; c.Agg != "" {
				rows[n][i] = strings.ReplaceAll(strconv.FormatFloat(biFloat(v), 'f', -1, 64), ".", tr("bi.decimal"))
			} else {
				rows[n][i] = biLabel(c.Column, v, tr)
			}
		}
	}
	s.biSystem(r, &d)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(biFileName(biTitle(x, tr))+".csv"))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(rows)
}

// biFileName is a title fit for a file name.
func biFileName(t string) string {
	t = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < ' ' {
			return '_'
		}
		return r
	}, strings.TrimSpace(t))
	if t == "" {
		return "widget"
	}
	return t
}
