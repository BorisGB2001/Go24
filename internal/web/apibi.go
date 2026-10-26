package web

import (
	"context"
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/bi"
)

// ---------- REST of «BI-аналитика»: the data sets and the queries over them ----------

// biField is a field of a data set as the constructor and the API show it.
type biField struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Dim     bool   `json:"dim"`
	Measure bool   `json:"measure"`
}

type biDataset struct {
	Code   string    `json:"code"`
	Name   string    `json:"name"`
	Period string    `json:"period"`
	Fields []biField `json:"fields"`
}

// biDatasets are the data sets, those of one's own too, with their names in the language of tr.
func (s *Server) biDatasets(ctx context.Context, tr func(string) string) ([]biDataset, error) {
	sets, err := s.BI.AllDatasets(ctx)
	if err != nil {
		return nil, err
	}
	return biNamedSets(sets, tr), nil
}

func biNamedSets(sets []*bi.Dataset, tr func(string) string) []biDataset {
	out := make([]biDataset, 0, len(sets))
	for _, d := range sets {
		tr := d.Tr(tr)
		x := biDataset{Code: d.Code, Name: tr("bi.ds." + d.Code), Period: d.Period}
		for _, f := range d.Fields {
			x.Fields = append(x.Fields, biField{Code: f.Code, Name: tr("bi.f." + f.Code), Type: f.Type, Dim: f.Dim(), Measure: f.Type == bi.Number})
		}
		out = append(out, x)
	}
	return out
}

// biResult is a result with the names of its columns: «Стадия», «Сумма: Сумма сделки».
type biResult struct {
	Columns []biColumn `json:"columns"`
	Rows    [][]any    `json:"rows"`
	Keys    [][]string `json:"-"` // what the dimensions of a row are filtered by
}

type biColumn struct {
	bi.Column
	Name string `json:"name"`
}

func biNamed(res bi.Result, tr func(string) string) biResult {
	out := biResult{Rows: res.Rows, Keys: res.Keys}
	for _, c := range res.Columns {
		name := tr("bi.f." + c.Field)
		switch {
		case c.Agg == "count":
			name = tr("bi.agg.count")
		case c.Agg != "" && tr("bi.agg."+c.Agg) != name:
			name = tr("bi.agg."+c.Agg) + ": " + name
		case c.Grain != "":
			name += " (" + tr("bi.grain."+c.Grain) + ")"
		}
		out.Columns = append(out.Columns, biColumn{c, name})
	}
	return out
}

func (s *Server) apiBIDatasets(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sets, err := s.biDatasets(r.Context(), s.apiTr(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, sets)
	return nil
}

// apiBIQuery: POST /api/v1/bi/query {dataset, dims, measures, filters, from, to, date_field, sort, limit}.
func (s *Server) apiBIQuery(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var q bi.Query
	if err := readJSON(r, &q); err != nil {
		return err
	}
	tr := s.biTr(r, q.Dataset, s.apiTr(r))
	res, err := s.BI.Run(r.Context(), p.UserID, q, tr)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, biNamed(res, tr))
	return nil
}
