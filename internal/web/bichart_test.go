package web

import (
	"math"
	"strings"
	"testing"

	"go24/internal/bi"
)

func biTr(k string) string {
	switch k {
	case "bi.decimal":
		return ","
	case "bi.fmt.day":
		return "02.01"
	case "bi.fmt.quarter":
		return "{q} кв. {y}"
	case "month.10":
		return "окт"
	}
	return k
}

// biSample is a result of deals by month and stage: a count and a sum.
func biSample() biResult {
	col := func(c bi.Column, name string) biColumn { return biColumn{c, name} }
	return biResult{Columns: []biColumn{col(bi.Column{Field: "created_at", Type: bi.Date, Grain: "month"}, "Месяц"),
		col(bi.Column{Field: "stage", Type: bi.String}, "Стадия"), col(bi.Column{Agg: "count", Type: bi.Number}, "Количество"),
		col(bi.Column{Field: "amount", Agg: "sum", Type: bi.Number}, "Сумма")},
		Rows: [][]any{{"2026-09-01", "Новая", 3.0, 1500.5}, {"2026-09-01", "Успех", 1.0, 1000.0}, {"2026-10-01", "Новая", 2.0, 250.0},
			{"2026-10-01", "", 4.0, -100.0}}}
}

// biSamplePanels are the widgets of every kind over biSample, for the pages' rendering test.
func biSamplePanels() []biPanel {
	var out []biPanel
	for i, k := range bi.Kinds {
		w := bi.Widget{ID: "w" + k, Kind: k, Width: bi.Widths[i%len(bi.Widths)], Title: k, NoPeriod: i == 0,
			Query: bi.Query{Dataset: "crm_deal", Dims: []string{"created_at:month", "stage"}, Measures: []bi.Measure{{Agg: "count"}}}}
		out = append(out, biPanel{W: w, Title: k, Chart: biDraw(w, biSample(), biTr)})
	}
	out = append(out, biPanel{W: bi.Widget{ID: "we", Kind: "bar", Width: 6}, Chart: biChart{Kind: "bar", Err: "нет поля"}},
		biPanel{W: bi.Widget{ID: "wn", Kind: "line", Width: 6}, Chart: biChart{Kind: "line", Empty: true}})
	return out
}

func TestBINum(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 12: "12", 1234567: "1 234 567", 1234.5: "1 234,5", -1500.25: "−1 500,25", 0.004: "0"} {
		if got := biNum(v, ","); got != want {
			t.Errorf("biNum(%v) = %q, want %q", v, got, want)
		}
	}
	if got := biShort(1250000, biTr); got != "1,3bi.n.mn" {
		t.Errorf("biShort = %q", got)
	}
	ticks := biNice(-120, 950)
	if ticks[0] > -120 || ticks[len(ticks)-1] < 950 || len(ticks) > 8 {
		t.Errorf("ticks %v", ticks)
	}
	for _, x := range ticks {
		if math.Mod(x, 100) != 0 && math.Mod(x, 200) != 0 && math.Mod(x, 250) != 0 {
			t.Errorf("tick %v is not round", x)
		}
	}
	if got := biLabel(bi.Column{Type: bi.Date, Grain: "quarter"}, "2026-10-01", biTr); got != "4 кв. 2026" {
		t.Errorf("quarter %q", got)
	}
	if got := biLabel(bi.Column{Type: bi.Date, Grain: "month"}, "2026-10-01", biTr); got != "окт 2026" {
		t.Errorf("month %q", got)
	}
}

func TestBIDraw(t *testing.T) {
	res := biSample()
	kpi := biDraw(bi.Widget{Kind: "kpi", Width: 3}, res, biTr)
	if len(kpi.KPI) != 2 || kpi.KPI[0].Value != "10" || kpi.KPI[1].Value != "2 650,5" {
		t.Errorf("kpi %+v", kpi.KPI)
	}
	// split by the second dimension: a series per stage, the biggest first, by the first measure
	bar := biDraw(bi.Widget{Kind: "bar", Width: 6}, res, biTr)
	if len(bar.Series) != 3 || bar.Series[0].Name != "Новая" || bar.Series[1].Name != "bi.none" {
		t.Fatalf("series %+v", bar.Series)
	}
	if len(bar.XTicks) != 2 || bar.XTicks[1].Text != "окт 2026" {
		t.Errorf("x %+v", bar.XTicks)
	}
	if n := len(bar.Series[0].Bars); n != 2 {
		t.Errorf("bars of «Новая»: %d", n)
	}
	for _, s := range bar.Series {
		for _, b := range s.Bars {
			if b.X < biPadL || b.X+b.W > float64(bar.W-biPadR)+0.01 || b.Y < biPadT-0.01 || b.H <= 0 {
				t.Errorf("bar out of the plot: %+v", b)
			}
		}
	}
	// measures as series when there is one dimension
	one := res
	one.Columns = append([]biColumn{res.Columns[0]}, res.Columns[2:]...)
	one.Rows = [][]any{{"2026-09-01", 4.0, 2500.5}, {"2026-10-01", 6.0, -100.0}}
	line := biDraw(bi.Widget{Kind: "line", Width: 12}, one, biTr)
	if len(line.Series) != 2 || line.Series[1].Name != "Сумма" || strings.Count(line.Series[0].Points, ",") != 2 || len(line.Series[0].Dots) != 2 {
		t.Errorf("line %+v", line.Series)
	}
	if line.Base <= biPadT || line.Base >= biChartH-biPadB { // zero is inside: a sum is negative
		t.Errorf("base %v", line.Base)
	}
	pie := biDraw(bi.Widget{Kind: "pie", Width: 4}, one, biTr)
	if len(pie.Slices) != 2 || pie.Slices[0].Pct != "60%" || pie.Total != "10" {
		t.Errorf("pie %+v %s", pie.Slices, pie.Total)
	}
	whole := biDraw(bi.Widget{Kind: "pie", Width: 4}, biResult{Columns: one.Columns, Rows: one.Rows[:1]}, biTr)
	if len(whole.Slices) != 1 || !strings.Contains(whole.Slices[0].Path, "A100 100 0 1 1") {
		t.Errorf("whole ring %+v", whole.Slices)
	}
	tbl := biDraw(bi.Widget{Kind: "table", Width: 12}, res, biTr)
	if len(tbl.Rows) != 4 || tbl.Rows[3][1].Text != "bi.none" || tbl.Sum[2].Text != "10" || tbl.Sum[3].Text != "2 650,5" {
		t.Errorf("table %+v %+v", tbl.Rows, tbl.Sum)
	}
	if e := biDraw(bi.Widget{Kind: "bar", Width: 6}, biResult{Columns: res.Columns}, biTr); !e.Empty {
		t.Error("no rows is not empty")
	}
}
