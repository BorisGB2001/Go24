package bi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

func TestTransfer(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	s := NewStore(pool, crm.NewStore(pool))
	adm := Viewer{Admin: true}

	tb, err := ParseCSV([]byte("Город;Сумма\nТверь;10\nМосква;20\n"))
	must(err)
	fid, err := s.CreateFile(ctx, anna, "Продажи", "p.csv", tb)
	must(err)
	file, _ := s.File(ctx, fid)

	id, err := s.SaveDashboard(ctx, anna, Dashboard{Title: "Итоги", Description: "за год", Period: "year", Filters: []string{"stage"}})
	must(err)
	for _, w := range []Widget{
		{Kind: "kpi", Width: 3, Title: "Сделок", Compare: true, Query: Query{Dataset: "crm_deal", Measures: []Measure{{Agg: "count"}}}},
		{Kind: "bar", Width: 6, Query: Query{Dataset: file.Code(), Dims: []string{"c1"}, Measures: []Measure{{Agg: "sum", Field: "c2"}}}},
		{Kind: "text", Width: 12, Body: "# Заметка"},
	} {
		w.DashboardID = id
		_, err := s.SaveWidget(ctx, w)
		must(err)
	}
	d, err := s.Dashboard(ctx, adm, id)
	must(err)
	f, err := s.Export(ctx, d, nil)
	must(err)
	if f.Format != fileFormat || f.Title != "Итоги" || f.Period != "year" || len(f.Widgets) != 3 || f.Files[file.Code()] != "Продажи" ||
		!f.Widgets[0].Compare || f.Widgets[2].Body != "# Заметка" || len(f.Filters) != 1 {
		t.Fatalf("export %+v", f)
	}
	data, err := json.Marshal(f)
	must(err)

	// Here: the same set, all the widgets; a draft of the one who imports.
	nid, skipped, err := s.Import(ctx, petr, data)
	must(err)
	n, err := s.Dashboard(ctx, Viewer{ID: petr}, nid)
	must(err)
	if skipped != 0 || n.Widgets != 3 || n.CreatedBy != petr || n.Status != "draft" || n.Period != "year" || n.Description != "за год" {
		t.Errorf("import %d %+v", skipped, n)
	}
	ws, _ := s.Widgets(ctx, nid)
	if len(ws) != 3 || ws[1].Query.Dataset != file.Code() || ws[0].Title != "Сделок" {
		t.Errorf("widgets %+v", ws)
	}

	// Another portal: the set has another code — found by its name; no such set — its widget is left out.
	f.Files = map[string]string{"file_999": "Продажи"}
	f.Widgets[1].Query.Dataset = "file_999"
	data, _ = json.Marshal(f)
	nid, skipped, err = s.Import(ctx, petr, data)
	must(err)
	if ws, _ := s.Widgets(ctx, nid); skipped != 0 || len(ws) != 3 || ws[1].Query.Dataset != file.Code() {
		t.Errorf("set by name: %d %+v", skipped, ws)
	}
	f.Files = map[string]string{"file_999": "Нет такого"}
	data, _ = json.Marshal(f)
	nid, skipped, err = s.Import(ctx, petr, data)
	must(err)
	if ws, _ := s.Widgets(ctx, nid); skipped != 1 || len(ws) != 2 {
		t.Errorf("no set: %d %d", skipped, len(ws))
	}
	f.Files = nil
	f.Widgets[1].Query.Dataset = "crm_deal" // c1 is no field of deals
	data, _ = json.Marshal(f)
	if _, skipped, _ = s.Import(ctx, petr, data); skipped != 1 {
		t.Errorf("no fields: %d", skipped)
	}

	for _, bad := range []string{`nope`, `{"format":"x","title":"a"}`} {
		if _, _, err := s.Import(ctx, petr, []byte(bad)); !errors.Is(err, ErrImportFile) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, _, err := s.Import(ctx, petr, []byte(`{"format":"go24.bi.dashboard","title":" "}`)); !errors.Is(err, ErrTitle) {
		t.Errorf("no title: %v", err)
	}
}
