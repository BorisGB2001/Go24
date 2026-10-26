package web

import (
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"go24/internal/bi"
)

// ---------- «BI-аналитика»: the filters of a dashboard besides its period and the cross-filters of its charts ----------

const biParam = "f_" // ?f_responsible=…&f_created_at=month:2026-10-01 — the values picked on a dashboard

// biPicked are the values picked on the page: a field → its keys (an empty one too — the empty value).
func biPicked(r *http.Request) map[string][]string {
	out := map[string][]string{}
	for k, v := range r.URL.Query() {
		if code, ok := strings.CutPrefix(k, biParam); ok && code != "" {
			out[code] = v
		}
	}
	return out
}

// biFilters are the filters of a dashboard as its page shows them: those of its settings with their values to pick
// from, then the others something was clicked on.
type biFilters struct {
	List   []biFilterView
	Picked map[string][]string
	Names  map[string]string
	Clear  string // the page without them
}

type biFilterView struct {
	Code, Param, Name string
	Select            bool       // one of the dashboard's: picked from Options
	Options           []bi.Value // what the viewer may pick
	Value             string     // the one picked
	Picked            []biPick
}

// biPick is a value picked: what it reads as and the page without it.
type biPick struct{ Label, Off string }

// biFilterBar makes the filters of a dashboard with the widgets list for the page of r.
func (s *Server) biFilterBar(r *http.Request, viewer string, d bi.Dashboard, list []bi.Widget) biFilters {
	tr := func(k string) string { return s.tr(r, k) }
	fl := biFilters{Picked: biPicked(r), Names: map[string]string{}}
	codes := slices.Clone(d.Filters)
	for _, k := range slices.Sorted(maps.Keys(fl.Picked)) {
		if !slices.Contains(codes, k) {
			codes = append(codes, k)
		}
	}
	sets := biSets(r, s, list)
	q := r.URL.Query()
	without := func(param, key string) string {
		v := url.Values{}
		for k, xs := range q {
			for _, x := range xs {
				if k != param || x != key {
					v.Add(k, x)
				}
			}
		}
		if len(v) == 0 {
			return r.URL.Path
		}
		return r.URL.Path + "?" + v.Encode()
	}
	for _, code := range codes {
		x := biFilterView{Code: code, Param: biParam + code, Select: slices.Contains(d.Filters, code)}
		var field bi.Field
		seen := map[string]bool{}
		for _, ds := range sets {
			f, ok := ds.Field(code)
			if !ok || !f.Dim() {
				continue
			}
			tr := ds.Tr(tr)
			if x.Name == "" {
				x.Name, field = tr("bi.f."+code), f
			}
			if f.Type == bi.Date {
				continue
			}
			vals, err := s.BI.Values(r.Context(), viewer, ds.Code, code, tr)
			if err != nil {
				s.Log.Error("bi filter", "err", err)
				continue
			}
			for _, v := range vals {
				if !seen[v.Key] {
					seen[v.Key] = true
					x.Options = append(x.Options, v)
				}
			}
		}
		if x.Name == "" { // no widget has it
			continue
		}
		fl.Names[code] = x.Name
		x.Select = x.Select && field.Type != bi.Date
		for _, key := range fl.Picked[code] {
			label := key
			if field.Type == bi.Date {
				grain, day, _ := strings.Cut(key, ":")
				label = biLabel(bi.Column{Type: bi.Date, Grain: grain}, day, tr)
			} else if i := slices.IndexFunc(x.Options, func(v bi.Value) bool { return v.Key == key }); i >= 0 {
				label = x.Options[i].Label
			} else if key == "" {
				label = tr("bi.none")
			}
			x.Picked = append(x.Picked, biPick{label, without(x.Param, key)})
		}
		if len(fl.Picked[code]) == 1 {
			x.Value = fl.Picked[code][0]
		}
		if x.Select || len(x.Picked) > 0 {
			fl.List = append(fl.List, x)
		}
	}
	if slices.ContainsFunc(fl.List, func(x biFilterView) bool { return len(x.Picked) > 0 }) {
		v := url.Values{}
		for k, xs := range q {
			if !strings.HasPrefix(k, biParam) {
				v[k] = xs
			}
		}
		fl.Clear = r.URL.Path
		if len(v) > 0 {
			fl.Clear += "?" + v.Encode()
		}
	}
	return fl
}

// biSets are the data sets of the widgets, each once, in their order.
func biSets(r *http.Request, s *Server, list []bi.Widget) []*bi.Dataset {
	var out []*bi.Dataset
	for _, w := range list {
		if slices.ContainsFunc(out, func(d *bi.Dataset) bool { return d.Code == w.Query.Dataset }) {
			continue
		}
		if ds, err := s.BI.Dataset(r.Context(), w.Query.Dataset); err == nil && ds != nil {
			out = append(out, ds)
		}
	}
	return out
}

// biFilterFields are the fields a dashboard of these widgets may be filtered by: those of their data sets one
// groups by, no dates (the period is the dashboard's), each once.
func (s *Server) biFilterFields(r *http.Request, list []bi.Widget) []biField {
	tr := func(k string) string { return s.tr(r, k) }
	var out []biField
	for _, ds := range biSets(r, s, list) {
		tr := ds.Tr(tr)
		for _, f := range ds.Fields {
			if f.Dim() && f.Type != bi.Date && !slices.ContainsFunc(out, func(x biField) bool { return x.Code == f.Code }) {
				out = append(out, biField{Code: f.Code, Name: tr("bi.f." + f.Code), Type: f.Type, Dim: true})
			}
		}
	}
	return out
}
