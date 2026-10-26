package bi

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
)

// maxValues are the values a filter of a dashboard offers.
const maxValues = 500

// Within is the span [from, to) of a date's key "grain:2006-01-02", as dates.
func Within(key string) (from, to string, ok bool) {
	grain, day, _ := strings.Cut(key, ":")
	t, err := time.Parse("2006-01-02", day)
	if err != nil || !slices.Contains(Grains, grain) {
		return "", "", false
	}
	end := map[string]time.Time{"day": t.AddDate(0, 0, 1), "week": t.AddDate(0, 0, 7), "month": t.AddDate(0, 1, 0),
		"quarter": t.AddDate(0, 3, 0), "year": t.AddDate(1, 0, 0)}[grain]
	return t.Format("2006-01-02"), end.Format("2006-01-02"), true
}

// Narrow adds what is picked on a dashboard (a field → its keys) to a widget's query: the fields its data set has —
// a date by its last key, the others by any of theirs; skipped are the fields the set has not.
func (s *Store) Narrow(ctx context.Context, q Query, picked map[string][]string) (Query, []string, error) {
	if len(picked) == 0 {
		return q, nil, nil
	}
	ds, err := s.Dataset(ctx, q.Dataset)
	if err != nil || ds == nil {
		return q, nil, err
	}
	var skipped []string
	q.Filters = slices.Clip(q.Filters) // the widget's own are not touched
	for _, code := range slices.Sorted(maps.Keys(picked)) {
		keys := picked[code]
		f, ok := ds.Field(code)
		switch {
		case len(keys) == 0:
		case !ok || !f.Dim():
			skipped = append(skipped, code)
		case f.Type == Date:
			q.Filters = append(q.Filters, Filter{Field: code, Op: "within", Value: keys[len(keys)-1]})
		default:
			q.Filters = append(q.Filters, Filter{Field: code, Op: "in", Values: keys})
		}
	}
	return q, skipped, nil
}

// Value is a value a dashboard's filter offers: its key and what it reads as.
type Value struct{ Key, Label string }

// Values are the values of a field in a data set the viewer reads, over all time, the most frequent first.
func (s *Store) Values(ctx context.Context, viewer, dataset, field string, tr func(string) string) ([]Value, error) {
	res, err := s.Run(ctx, viewer, Query{Dataset: dataset, Dims: []string{field}, Measures: []Measure{{Agg: "count"}},
		Limit: maxValues}, tr)
	if err != nil {
		return nil, err
	}
	out := make([]Value, len(res.Rows))
	for i, r := range res.Rows {
		out[i] = Value{res.Keys[i][0], r[0].(string)}
	}
	return out, nil
}
