package bi

import (
	"context"
	"encoding/json"
	"time"

	"go24/internal/i18n"
)

// ---------- «Экспорт» and «Импорт» of a dashboard: a file to load here or on another portal ----------

var ErrImportFile = i18n.E("bi.err.import_file")

// fileFormat marks a dashboard's file.
const fileFormat = "go24.bi.dashboard"

// DashboardFile is a dashboard as a file: its settings, its widgets and the sets of one's own they read — by their
// names, for the codes of those sets differ from portal to portal.
type DashboardFile struct {
	Format      string            `json:"format"`
	Version     int               `json:"version"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Period      string            `json:"period"`
	DateFrom    *time.Time        `json:"date_from,omitempty"`
	DateTo      *time.Time        `json:"date_to,omitempty"`
	Filters     []string          `json:"filters,omitempty"`
	Widgets     []WidgetFile      `json:"widgets"`
	Files       map[string]string `json:"files,omitempty"` // a set of one's own: its code → its name
}

// WidgetFile is a widget of a dashboard's file.
type WidgetFile struct {
	Kind     string `json:"kind"`
	Title    string `json:"title,omitempty"`
	Width    int    `json:"width"`
	Query    Query  `json:"query"`
	NoPeriod bool   `json:"no_period,omitempty"`
	Compare  bool   `json:"compare,omitempty"`
	Body     string `json:"body,omitempty"`
}

// Export is the dashboard as a file; name gives the titles of a ready dashboard's widgets (locale keys), as in a copy.
func (s *Store) Export(ctx context.Context, d Dashboard, name func(string) string) (DashboardFile, error) {
	f := DashboardFile{Format: fileFormat, Version: 1, Title: d.Title, Description: d.Description, Period: d.Period,
		DateFrom: d.DateFrom, DateTo: d.DateTo, Filters: d.Filters, Widgets: []WidgetFile{}}
	ws, err := s.Widgets(ctx, d.ID)
	if err != nil {
		return f, err
	}
	for _, w := range ws {
		if d.Code != "" && name != nil {
			w.Title = name(w.Title)
		}
		f.Widgets = append(f.Widgets, WidgetFile{Kind: w.Kind, Title: w.Title, Width: w.Width, Query: w.Query,
			NoPeriod: w.NoPeriod, Compare: w.Compare, Body: w.Body})
		if code := w.Query.Dataset; IsFile(code) && f.Files[code] == "" {
			ds, err := s.Dataset(ctx, code)
			if err != nil {
				return f, err
			}
			if ds != nil {
				if f.Files == nil {
					f.Files = map[string]string{}
				}
				f.Files[code] = ds.Name
			}
		}
	}
	return f, nil
}

// Import makes a new dashboard of a file, a draft of the user: a set of one's own is the one of this portal with the
// code and the name of the file's, else the first by its name; a widget whose set or fields are not here is left out
// — skipped is how many.
func (s *Store) Import(ctx context.Context, by string, data []byte) (id string, skipped int, err error) {
	var f DashboardFile
	if json.Unmarshal(data, &f) != nil || f.Format != fileFormat {
		return "", 0, ErrImportFile
	}
	sets := map[string]string{} // the file's code → this portal's
	if len(f.Files) > 0 {
		files, err := s.Files(ctx)
		if err != nil {
			return "", 0, err
		}
		for code, name := range f.Files {
			for _, x := range files {
				if x.Name == name && (x.Code() == code || sets[code] == "") {
					sets[code] = x.Code()
				}
			}
		}
	}
	var ws []Widget
	for _, x := range f.Widgets {
		w := Widget{Kind: x.Kind, Title: x.Title, Width: x.Width, Query: x.Query, NoPeriod: x.NoPeriod, Compare: x.Compare,
			Body: x.Body}
		if IsFile(w.Query.Dataset) {
			w.Query.Dataset = sets[w.Query.Dataset]
		}
		if w.Kind != "text" && w.Query.Dataset == "" || s.CheckWidget(ctx, w) != nil {
			skipped++
			continue
		}
		ws = append(ws, w)
	}
	id, err = s.SaveDashboard(ctx, by, Dashboard{Title: f.Title, Description: f.Description, Period: f.Period,
		DateFrom: f.DateFrom, DateTo: f.DateTo, Filters: f.Filters})
	if err != nil {
		return "", 0, err
	}
	for _, w := range ws {
		w.DashboardID = id
		if _, err := s.SaveWidget(ctx, w); err != nil {
			return id, skipped, err
		}
	}
	return id, skipped, nil
}
