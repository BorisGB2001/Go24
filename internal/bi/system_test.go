package bi

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestSystemWidgets: every widget of the ready dashboards is a valid one.
func TestSystemWidgets(t *testing.T) {
	for _, d := range SystemDashboards {
		if !slices.Contains(Periods, d.Period) || len(d.Widgets) == 0 {
			t.Errorf("%s: period %q, %d widgets", d.Code, d.Period, len(d.Widgets))
		}
		for _, w := range d.Widgets {
			if !slices.Contains(Kinds, w.Kind) || !slices.Contains(Widths, w.Width) || !strings.HasPrefix(w.Title, "bi.sw.") {
				t.Errorf("%s %s: %s %d", d.Code, w.Title, w.Kind, w.Width)
			}
			if err := w.Query.Check(); err != nil {
				t.Errorf("%s %s: %v", d.Code, w.Title, err)
			}
		}
	}
}

// TestSystemDashboards: the ready dashboards are put in once, renewed with a new version, are not changed or
// deleted, and their copies get the widgets' titles in words.
func TestSystemDashboards(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool, crm.NewStore(pool))
	own, err := s.SaveDashboard(ctx, anna, Dashboard{Title: "Моя"})
	must(err)
	must(s.EnsureSystem(ctx))
	must(s.EnsureSystem(ctx))
	me := Viewer{ID: anna}
	list, err := s.Dashboards(ctx, me, "", "")
	must(err)
	var codes []string
	for _, d := range list {
		codes = append(codes, d.Code)
	}
	if strings.Join(codes, ",") != "sales,leads,tasks,timeman," {
		t.Fatalf("codes %v", codes)
	}
	if list, _ := s.Dashboards(ctx, me, "моя", ""); len(list) != 5 { // the ready ones are always there
		t.Errorf("search %d", len(list))
	}
	sales := list[0]
	if sales.Widgets != len(SystemDashboards[0].Widgets) || sales.CanEdit() || sales.CanManage(Viewer{ID: anna, Admin: true}) || sales.Status != "published" || sales.CreatedBy != "" {
		t.Errorf("sales %+v", sales)
	}
	if _, err := s.SaveDashboard(ctx, anna, Dashboard{ID: sales.ID, Title: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a ready one changed: %v", err)
	}
	if err := s.DeleteDashboard(ctx, sales.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a ready one deleted: %v", err)
	}

	// a newer version renews the widgets
	_, err = pool.Exec(ctx, `DELETE FROM bi_widgets WHERE dashboard_id = $1 AND pos > 0`, sales.ID)
	must(err)
	must(s.EnsureSystem(ctx))
	if d, _ := s.Dashboard(ctx, Viewer{}, sales.ID); d.Widgets != 1 {
		t.Errorf("renewed without a new version: %d", d.Widgets)
	}
	_, err = pool.Exec(ctx, `UPDATE bi_dashboards SET version = 0 WHERE code = 'sales'`)
	must(err)
	must(s.EnsureSystem(ctx))
	if d, _ := s.Dashboard(ctx, Viewer{}, sales.ID); d.Widgets != len(SystemDashboards[0].Widgets) {
		t.Errorf("not renewed: %d", d.Widgets)
	}

	cp, err := s.CopyDashboard(ctx, anna, sales.ID, "Продажи", func(k string) string { return "«" + k + "»" })
	must(err)
	d, err := s.Dashboard(ctx, me, cp)
	must(err)
	ws, err := s.Widgets(ctx, cp)
	must(err)
	if d.Code != "" || !d.CanEdit() || d.Status != "draft" || d.Period != "month" || len(ws) != len(SystemDashboards[0].Widgets) ||
		ws[0].Title != "«bi.sw.deals_new»" || ws[4].Query.Dims[0] != "created_at:month" {
		t.Errorf("copy %+v %+v", d, ws)
	}
	must(s.DeleteDashboard(ctx, own))
}
