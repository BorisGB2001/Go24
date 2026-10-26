package web

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/bi"
)

// ---------- «BI-аналитика»: the widgets drawn as SVG on the server ----------

const (
	biChartH   = 240 // the height of a chart's viewBox
	biPadL     = 64  // the axis of values
	biPadR     = 12
	biPadT     = 12
	biPadB     = 34 // the labels of the categories
	biColors   = 10 // the palette: classes bi-c0 … bi-c9
	biPieMax   = 9  // slices of a pie; the smaller ones are «Другие»
	biSeries   = 10 // series of a split chart
	biTableMax = 200
	biBarMax   = 56 // the widest column
)

// biChart is a widget ready to draw: a number, an SVG of columns, a line or a pie, or a table.
type biChart struct {
	Kind  string
	Err   string // the query failed: what to show instead
	Empty bool   // no rows
	W, H  int    // the viewBox
	// kpi: the first measure big, the others under it
	KPI []biKPI
	// bar, line
	Series []biSeriesView
	XTicks []biTick
	YTicks []biTick
	Base   float64 // y of zero
	PlotL  int
	PlotR  int
	LabX   int // the right edge of the values' labels
	LabY   int // the baseline of the categories' labels
	// pie
	Slices []biSlice
	Total  string
	// table
	Cols  []biCol
	Rows  [][]biCell
	Sum   []biCell // «Итого», for the counts and the sums
	Cut   bool     // more rows than shown
	Value string   // the measure's name (pie, single-series charts)
}

type biKPI struct{ Name, Value string }

type biTick struct {
	X, Y float64
	Text string
}

type biSeriesView struct {
	Name, Class string
	Bars        []biBar
	Points      string // the line's polyline
	Dots        []biBar
}

type biBar struct {
	X, Y, W, H float64
	Tip        string
}

type biSlice struct {
	Path, Class, Name, Value string
	Pct                      string
}

type biCol struct {
	Name string
	Num  bool
}

type biCell struct {
	Text string
	Num  bool
}

// biNum is a number as the widgets show it: 1 234 567, 1 234,5; dec is the locale's decimal mark.
func biNum(v float64, dec string) string {
	neg := v < 0
	v = math.Abs(v)
	s := strconv.FormatFloat(v, 'f', 2, 64)
	whole, frac, _ := strings.Cut(s, ".")
	frac = strings.TrimRight(frac, "0")
	var b strings.Builder
	if neg {
		b.WriteString("−")
	}
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	if frac != "" {
		b.WriteString(dec + frac)
	}
	return b.String()
}

// biShort is a number for an axis: 1,5 тыс., 12 млн.
func biShort(v float64, tr func(string) string) string {
	dec := tr("bi.decimal")
	a := math.Abs(v)
	switch {
	case a >= 1e9:
		return biNum(math.Round(v/1e8)/10, dec) + tr("bi.n.bn")
	case a >= 1e6:
		return biNum(math.Round(v/1e5)/10, dec) + tr("bi.n.mn")
	case a >= 1e4:
		return biNum(math.Round(v/1e2)/10, dec) + tr("bi.n.k")
	}
	return biNum(v, dec)
}

// biLabel is a value of a dimension as shown: a date by its grain.
func biLabel(c bi.Column, v any, tr func(string) string) string {
	s := fmt.Sprint(v)
	if c.Type == bi.Date && c.Grain != "" {
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return s
		}
		switch c.Grain {
		case "day", "week":
			return t.Format(tr("bi.fmt.day"))
		case "month":
			return tr("month."+strconv.Itoa(int(t.Month()))) + " " + strconv.Itoa(t.Year())
		case "quarter":
			return strings.NewReplacer("{q}", strconv.Itoa((int(t.Month())+2)/3), "{y}", strconv.Itoa(t.Year())).Replace(tr("bi.fmt.quarter"))
		case "year":
			return strconv.Itoa(t.Year())
		}
	}
	if s == "" {
		return tr("bi.none")
	}
	return s
}

func biFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}

// biNice are the ticks of an axis from lo to hi (lo ≤ 0 ≤ hi): about 4 steps of 1, 2 or 5 × 10ⁿ.
func biNice(lo, hi float64) []float64 {
	if hi <= lo {
		hi = lo + 1
	}
	raw := (hi - lo) / 4
	p := math.Pow(10, math.Floor(math.Log10(raw)))
	step := p * 10
	for _, m := range []float64{1, 2, 5} {
		if raw <= m*p {
			step = m * p
			break
		}
	}
	var out []float64
	for v := math.Floor(lo/step) * step; v < hi+step*0.999; v += step {
		out = append(out, math.Round(v/step)*step)
	}
	return out
}

func biClip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// biDraw makes the chart of a widget's result (the columns named, the dimensions first).
func biDraw(w bi.Widget, res biResult, tr func(string) string) biChart {
	ch := biChart{Kind: w.Kind, H: biChartH, W: w.Width*70 + 60}
	dec := tr("bi.decimal")
	var dims, meas []int
	for i, c := range res.Columns {
		if c.Agg != "" {
			meas = append(meas, i)
		} else {
			dims = append(dims, i)
		}
	}
	if len(res.Rows) == 0 && w.Kind != "kpi" {
		ch.Empty = true
		return ch
	}
	if len(meas) > 0 {
		ch.Value = res.Columns[meas[0]].Name
	}
	switch w.Kind {
	case "kpi":
		for _, m := range meas {
			total := 0.0
			for _, r := range res.Rows { // a KPI with dimensions sums its rows
				total += biFloat(r[m])
			}
			ch.KPI = append(ch.KPI, biKPI{res.Columns[m].Name, biNum(total, dec)})
		}
	case "table":
		for _, c := range res.Columns {
			ch.Cols = append(ch.Cols, biCol{c.Name, c.Agg != ""})
		}
		sums := make([]float64, len(res.Columns))
		for n, r := range res.Rows {
			row := make([]biCell, len(r))
			for i, v := range r {
				c := res.Columns[i]
				if c.Agg != "" {
					row[i] = biCell{biNum(biFloat(v), dec), true}
					sums[i] += biFloat(v)
				} else {
					row[i] = biCell{Text: biLabel(c.Column, v, tr)}
				}
			}
			if n < biTableMax {
				ch.Rows = append(ch.Rows, row)
			}
		}
		ch.Cut = len(res.Rows) > biTableMax
		if len(dims) > 0 && len(res.Rows) > 1 {
			ch.Sum = make([]biCell, len(res.Columns))
			ch.Sum[0] = biCell{Text: tr("bi.total")}
			for _, m := range meas {
				if a := res.Columns[m].Agg; a == "count" || a == "sum" {
					ch.Sum[m] = biCell{biNum(sums[m], dec), true}
				}
			}
		}
	case "pie":
		biPie(&ch, res, dims, meas, tr)
	default:
		biAxes(&ch, w.Kind, res, dims, meas, tr)
	}
	return ch
}

// biPie: the slices of the first dimension by the first measure, the biggest first; the rest are «Другие».
func biPie(ch *biChart, res biResult, dims, meas []int, tr func(string) string) {
	ch.W, ch.H = 220, 220
	if len(meas) == 0 {
		ch.Empty = true
		return
	}
	m := meas[0]
	type part struct {
		name string
		v    float64
	}
	var parts []part
	total := 0.0
	for _, r := range res.Rows {
		v := biFloat(r[m])
		if v <= 0 {
			continue
		}
		name := ""
		if len(dims) > 0 {
			name = biLabel(res.Columns[dims[0]].Column, r[dims[0]], tr)
		}
		parts = append(parts, part{name, v})
		total += v
	}
	if total == 0 {
		ch.Empty = true
		return
	}
	// the rows come by the measure, the biggest first, unless the query sorted them otherwise
	slices.SortStableFunc(parts, func(a, b part) int { return cmp.Compare(b.v, a.v) })
	if len(parts) > biPieMax {
		rest := 0.0
		for _, p := range parts[biPieMax-1:] {
			rest += p.v
		}
		parts = append(parts[:biPieMax-1], part{tr("bi.others"), rest})
	}
	dec := tr("bi.decimal")
	const cx, cy, r0, r1 = 110.0, 110.0, 100.0, 58.0
	pt := func(a, r float64) string {
		return strconv.FormatFloat(cx+r*math.Sin(a), 'f', 2, 64) + " " + strconv.FormatFloat(cy-r*math.Cos(a), 'f', 2, 64)
	}
	a := 0.0
	for i, p := range parts {
		da := p.v / total * 2 * math.Pi
		var path string
		if len(parts) == 1 { // a whole ring: two halves
			path = "M" + pt(0, r0) + " A100 100 0 1 1 " + pt(math.Pi, r0) + " A100 100 0 1 1 " + pt(0, r0) +
				" M" + pt(0, r1) + " A58 58 0 1 0 " + pt(math.Pi, r1) + " A58 58 0 1 0 " + pt(0, r1) + "Z"
		} else {
			large := "0"
			if da > math.Pi {
				large = "1"
			}
			path = "M" + pt(a, r0) + " A100 100 0 " + large + " 1 " + pt(a+da, r0) + " L" + pt(a+da, r1) +
				" A58 58 0 " + large + " 0 " + pt(a, r1) + "Z"
		}
		ch.Slices = append(ch.Slices, biSlice{Path: path, Class: "bi-c" + strconv.Itoa(i%biColors), Name: p.name,
			Value: biNum(p.v, dec), Pct: biNum(math.Round(p.v/total*1000)/10, dec) + "%"})
		a += da
	}
	ch.Total = biShort(total, tr)
}

// biAxes: columns or lines over the categories of the first dimension; the series are the measures, or — with a
// second dimension — its values by the first measure.
func biAxes(ch *biChart, kind string, res biResult, dims, meas []int, tr func(string) string) {
	if len(meas) == 0 {
		ch.Empty = true
		return
	}
	dec := tr("bi.decimal")
	var cats []string
	catOf := map[string]int{}
	type series struct {
		name string
		vals map[int]float64
	}
	var ss []*series
	cat := func(r []any) int {
		name := ""
		if len(dims) > 0 {
			name = biLabel(res.Columns[dims[0]].Column, r[dims[0]], tr)
		}
		i, ok := catOf[name]
		if !ok {
			i = len(cats)
			catOf[name] = i
			cats = append(cats, name)
		}
		return i
	}
	if len(dims) > 1 {
		byName := map[string]*series{}
		totals := map[string]float64{}
		for _, r := range res.Rows {
			c := cat(r)
			n := biLabel(res.Columns[dims[1]].Column, r[dims[1]], tr)
			s := byName[n]
			if s == nil {
				s = &series{n, map[int]float64{}}
				byName[n] = s
				ss = append(ss, s)
			}
			s.vals[c] += biFloat(r[meas[0]])
			totals[n] += math.Abs(biFloat(r[meas[0]]))
		}
		// the biggest series are drawn; the others would be colours nobody tells apart
		slices.SortStableFunc(ss, func(a, b *series) int { return cmp.Compare(totals[b.name], totals[a.name]) })
		if len(ss) > biSeries {
			ss = ss[:biSeries]
		}
	} else {
		for _, m := range meas {
			ss = append(ss, &series{res.Columns[m].Name, map[int]float64{}})
		}
		for _, r := range res.Rows {
			c := cat(r)
			for k, m := range meas {
				ss[k].vals[c] = biFloat(r[m])
			}
		}
	}
	lo, hi := 0.0, 0.0
	for _, s := range ss {
		for _, v := range s.vals {
			lo, hi = min(lo, v), max(hi, v)
		}
	}
	ticks := biNice(lo, hi)
	lo, hi = ticks[0], ticks[len(ticks)-1]
	plotW := float64(ch.W - biPadL - biPadR)
	plotH := float64(biChartH - biPadT - biPadB)
	y := func(v float64) float64 { return biPadT + (hi-v)/(hi-lo)*plotH }
	ch.PlotL, ch.PlotR = biPadL, ch.W-biPadR
	ch.LabX, ch.LabY = biPadL-6, biChartH-12
	ch.Base = y(0)
	for _, t := range ticks {
		ch.YTicks = append(ch.YTicks, biTick{Y: y(t), Text: biShort(t, tr)})
	}
	n := float64(len(cats))
	group := plotW / n
	every := int(math.Ceil(n / max(1, plotW/72)))
	for i, c := range cats {
		if i%every == 0 {
			ch.XTicks = append(ch.XTicks, biTick{X: biPadL + group*(float64(i)+0.5), Text: biClip(c, 14)})
		}
	}
	k := float64(len(ss))
	for j, s := range ss {
		sv := biSeriesView{Name: s.name, Class: "bi-c" + strconv.Itoa(j%biColors)}
		var pts []string
		for i, c := range cats {
			v, ok := s.vals[i]
			tip := c + " · " + s.name + ": " + biNum(v, dec)
			if len(ss) == 1 {
				tip = c + ": " + biNum(v, dec)
			}
			if kind == "line" {
				x := biPadL + group*(float64(i)+0.5)
				pts = append(pts, strconv.FormatFloat(x, 'f', 1, 64)+","+strconv.FormatFloat(y(v), 'f', 1, 64))
				if ok {
					sv.Dots = append(sv.Dots, biBar{X: x, Y: y(v), Tip: tip})
				}
				continue
			}
			if !ok {
				continue
			}
			// the columns of a category fill 80% of its place, each at most biBarMax wide, centred
			bw := min(group*0.8/k, biBarMax)
			top, bot := y(max(v, 0)), y(min(v, 0))
			sv.Bars = append(sv.Bars, biBar{X: biPadL + group*float64(i) + (group-bw*k)/2 + bw*float64(j), Y: top, W: max(bw-1, 1),
				H: max(bot-top, 0.5), Tip: tip})
		}
		sv.Points = strings.Join(pts, " ")
		ch.Series = append(ch.Series, sv)
	}
}
