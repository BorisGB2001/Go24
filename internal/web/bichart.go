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
	biPivotMax = 30 // columns of a pivot table; the others are «Другие»
	biSteps    = 20 // steps of a funnel
)

// biChart is a widget ready to draw: a number, an SVG of columns, a line, areas or a pie, a funnel, a table, a pivot
// table or a text.
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
	Cols []biCol
	Rows [][]biCell
	Sum  []biCell // «Итого», for the counts and the sums
	Cut  bool     // more rows than shown
	// funnel
	Steps []biStep
	// pivot: the values of the second dimension across, of the first down
	PCols []biCell
	PRows []biPRow
	PSum  []biCell // the totals of the columns, the last — of all
	// text
	Paras []biPara
	Value string // the measure's name (pie, single-series charts)
	F     string // the dashboard's parameter of the first dimension a click filters by
}

// biKPI is a number; Delta — its change against the period before (Prev): «+12,5%», Trend — up, down or "".
type biKPI struct {
	Name, Value, Prev, Delta, Trend string
	v                               float64
}

// biStep is a step of a funnel: Pct — of the first step, Conv — of the step before; W — its bar's width, %.
type biStep struct {
	Name, Value, Pct, Conv, Class, Key string
	W                                  float64
	Click                              bool
}

// biPRow is a row of a pivot table: its value of the first dimension, the cells by the columns, the row's total.
type biPRow struct {
	Head  biCell
	Cells []biCell
	Total biCell
}

// biPara is a paragraph of a text widget, or its heading.
type biPara struct {
	Text string
	Head bool
}

type biTick struct {
	X, Y float64
	Text string
}

type biSeriesView struct {
	Name, Class string
	Bars        []biBar
	Points      string // the line's polyline
	Area        string // the area's polygon under it
	Dots        []biBar
}

type biBar struct {
	X, Y, W, H float64
	Tip        string
	Key        string
	Click      bool // filters the dashboard by Key
}

type biSlice struct {
	Path, Class, Name, Value string
	Pct                      string
	Key                      string
	Click                    bool
}

type biCol struct {
	Name string
	Num  bool
}

type biCell struct {
	Text string
	Num  bool
	F    string // a dimension's: the parameter and the key a click filters the dashboard by
	Key  string
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
	if w.Kind == "text" {
		ch.Paras = biParas(w.Body)
		return ch
	}
	if len(res.Rows) == 0 && w.Kind != "kpi" {
		ch.Empty = true
		return ch
	}
	if len(meas) > 0 {
		ch.Value = res.Columns[meas[0]].Name
	}
	if len(dims) > 0 && len(res.Keys) == len(res.Rows) {
		ch.F = biParam + res.Columns[dims[0]].Field
	}
	switch w.Kind {
	case "kpi":
		for _, m := range meas {
			total := 0.0
			for _, r := range res.Rows { // a KPI with dimensions sums its rows
				total += biFloat(r[m])
			}
			ch.KPI = append(ch.KPI, biKPI{Name: res.Columns[m].Name, Value: biNum(total, dec), v: total})
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
					row[i] = biCell{Text: biNum(biFloat(v), dec), Num: true}
					sums[i] += biFloat(v)
				} else {
					row[i] = biCell{Text: biLabel(c.Column, v, tr)}
					if ch.F != "" {
						row[i].F, row[i].Key = biParam+c.Field, res.Keys[n][i]
					}
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
					ch.Sum[m] = biCell{Text: biNum(sums[m], dec), Num: true}
				}
			}
		}
	case "pie":
		biPie(&ch, res, dims, meas, tr)
	case "funnel":
		biFunnel(&ch, res, dims, meas, tr)
	case "pivot":
		biPivot(&ch, res, dims, meas, tr)
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
		key  string
		ok   bool // has a key: not «Другие»
	}
	var parts []part
	total := 0.0
	for n, r := range res.Rows {
		v := biFloat(r[m])
		if v <= 0 {
			continue
		}
		p := part{v: v}
		if len(dims) > 0 {
			p.name = biLabel(res.Columns[dims[0]].Column, r[dims[0]], tr)
			if ch.F != "" {
				p.key, p.ok = res.Keys[n][0], true
			}
		}
		parts = append(parts, p)
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
		parts = append(parts[:biPieMax-1], part{name: tr("bi.others"), v: rest})
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
			Value: biNum(p.v, dec), Pct: biNum(math.Round(p.v/total*1000)/10, dec) + "%", Key: p.key, Click: p.ok})
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
	var cats, keys []string
	catOf := map[string]int{}
	click := ch.F != ""
	type series struct {
		name string
		vals map[int]float64
	}
	var ss []*series
	cat := func(n int) int {
		r, name, key := res.Rows[n], "", ""
		if len(dims) > 0 {
			name = biLabel(res.Columns[dims[0]].Column, r[dims[0]], tr)
		}
		if click {
			key = res.Keys[n][0]
		}
		i, ok := catOf[name]
		if !ok {
			i = len(cats)
			catOf[name] = i
			cats, keys = append(cats, name), append(keys, key)
		}
		return i
	}
	if len(dims) > 1 {
		byName := map[string]*series{}
		totals := map[string]float64{}
		for k, r := range res.Rows {
			c := cat(k)
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
		for n, r := range res.Rows {
			c := cat(n)
			for k, m := range meas {
				ss[k].vals[c] = biFloat(r[m])
			}
		}
	}
	// stacked columns and areas lay the series one on another: the axis spans their sums
	stacked := kind == "stack" || kind == "area"
	lo, hi := 0.0, 0.0
	for i := range cats {
		pos, neg := 0.0, 0.0
		for _, s := range ss {
			v := s.vals[i]
			if !stacked {
				lo, hi = min(lo, v), max(hi, v)
			} else if v >= 0 || kind == "area" {
				pos += v
			} else {
				neg += v
			}
		}
		lo, hi = min(lo, neg, pos), max(hi, pos)
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
	xy := func(x, y float64) string {
		return strconv.FormatFloat(x, 'f', 1, 64) + "," + strconv.FormatFloat(y, 'f', 1, 64)
	}
	posAt, negAt := make([]float64, len(cats)), make([]float64, len(cats)) // the stacks so far
	for j, s := range ss {
		sv := biSeriesView{Name: s.name, Class: "bi-c" + strconv.Itoa(j%biColors)}
		var pts, under []string
		for i, c := range cats {
			v, ok := s.vals[i]
			tip := c + " · " + s.name + ": " + biNum(v, dec)
			if len(ss) == 1 {
				tip = c + ": " + biNum(v, dec)
			}
			switch kind {
			case "area":
				x := biPadL + group*(float64(i)+0.5)
				base := posAt[i]
				posAt[i] += v
				pts = append(pts, xy(x, y(posAt[i])))
				under = append(under, xy(x, y(base)))
				if ok {
					sv.Dots = append(sv.Dots, biBar{X: x, Y: y(posAt[i]), Tip: tip, Key: keys[i], Click: click})
				}
				continue
			case "stack":
				if !ok {
					continue
				}
				bw := min(group*0.6, biBarMax)
				var top, bot float64
				if v >= 0 {
					top, bot = y(posAt[i]+v), y(posAt[i])
					posAt[i] += v
				} else {
					top, bot = y(negAt[i]), y(negAt[i]+v)
					negAt[i] += v
				}
				sv.Bars = append(sv.Bars, biBar{X: biPadL + group*float64(i) + (group-bw)/2, Y: top, W: bw,
					H: max(bot-top, 0.5), Tip: tip, Key: keys[i], Click: click})
				continue
			}
			if kind == "line" {
				x := biPadL + group*(float64(i)+0.5)
				pts = append(pts, strconv.FormatFloat(x, 'f', 1, 64)+","+strconv.FormatFloat(y(v), 'f', 1, 64))
				if ok {
					sv.Dots = append(sv.Dots, biBar{X: x, Y: y(v), Tip: tip, Key: keys[i], Click: click})
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
				H: max(bot-top, 0.5), Tip: tip, Key: keys[i], Click: click})
		}
		sv.Points = strings.Join(pts, " ")
		if kind == "area" && len(pts) > 0 {
			slices.Reverse(under)
			sv.Area = sv.Points + " " + strings.Join(under, " ")
		}
		ch.Series = append(ch.Series, sv)
	}
}

// biCompare gives the numbers of a KPI their change against the result of the period before.
func biCompare(ch *biChart, prev biResult, tr func(string) string) {
	dec := tr("bi.decimal")
	var meas []int
	for i, c := range prev.Columns {
		if c.Agg != "" {
			meas = append(meas, i)
		}
	}
	for i := range ch.KPI {
		if i >= len(meas) {
			break
		}
		k, was := &ch.KPI[i], 0.0
		for _, r := range prev.Rows {
			was += biFloat(r[meas[i]])
		}
		k.Prev = biNum(was, dec)
		switch {
		case k.v > was:
			k.Trend = "up"
		case k.v < was:
			k.Trend = "down"
		}
		if was == 0 {
			continue // no share of nothing
		}
		pct := math.Round((k.v-was)/math.Abs(was)*1000) / 10
		k.Delta = biNum(pct, dec) + "%"
		if pct > 0 {
			k.Delta = "+" + k.Delta
		}
	}
}

// biParas are the paragraphs of a text: the blocks between empty lines, «# » starts a heading.
func biParas(body string) []biPara {
	var out []biPara
	for _, b := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n") {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		if h, ok := strings.CutPrefix(b, "#"); ok {
			head, rest, _ := strings.Cut(strings.TrimLeft(h, "# "), "\n")
			out = append(out, biPara{Text: strings.TrimSpace(head), Head: true})
			if rest = strings.TrimSpace(rest); rest != "" {
				out = append(out, biPara{Text: rest})
			}
			continue
		}
		out = append(out, biPara{Text: b})
	}
	return out
}

// biFunnel: the steps of the first dimension in the order of the rows (the stages as their pipeline goes) by the first
// measure, each bar as wide as its share of the biggest, with its share of the first step and of the step before.
func biFunnel(ch *biChart, res biResult, dims, meas []int, tr func(string) string) {
	if len(meas) == 0 || len(dims) == 0 {
		ch.Empty = true
		return
	}
	dec := tr("bi.decimal")
	m, top := meas[0], 0.0
	for _, r := range res.Rows {
		top = max(top, biFloat(r[m]))
	}
	if top <= 0 {
		ch.Empty = true
		return
	}
	pct := func(a, b float64) string {
		if b <= 0 {
			return "—"
		}
		return biNum(math.Round(a/b*1000)/10, dec) + "%"
	}
	first, before := 0.0, 0.0
	for n, r := range res.Rows {
		if n == biSteps {
			ch.Cut = true
			break
		}
		v := biFloat(r[m])
		if n == 0 {
			first, before = v, v
		}
		st := biStep{Name: biLabel(res.Columns[dims[0]].Column, r[dims[0]], tr), Value: biNum(v, dec), Pct: pct(v, first),
			Class: "bi-c" + strconv.Itoa(n%biColors), W: math.Round(max(v, 0)/top*1000) / 10}
		if n > 0 {
			st.Conv = pct(v, before)
		}
		if ch.F != "" {
			st.Key, st.Click = res.Keys[n][0], true
		}
		ch.Steps = append(ch.Steps, st)
		before = v
	}
}

// biPivot: the first dimension down, the second across (the biggest columns first, the rest «Другие»), the cells — the
// first measure; the totals of the rows and the columns for the counts and the sums.
func biPivot(ch *biChart, res biResult, dims, meas []int, tr func(string) string) {
	if len(meas) == 0 || len(dims) == 0 {
		ch.Empty = true
		return
	}
	dec := tr("bi.decimal")
	m := meas[0]
	agg := res.Columns[m].Agg
	summed := agg == "count" || agg == "sum"
	cell := func(v float64, ok bool) biCell {
		if !ok {
			return biCell{Num: true}
		}
		return biCell{Text: biNum(v, dec), Num: true}
	}
	// the columns: the values of the second dimension, the biggest first (dates — in their order)
	type col struct {
		name, key string
		total     float64
	}
	var cols []*col
	colOf := map[string]*col{}
	name2 := func(r []any) string {
		if len(dims) < 2 {
			return res.Columns[m].Name
		}
		return biLabel(res.Columns[dims[1]].Column, r[dims[1]], tr)
	}
	for n, r := range res.Rows {
		c := colOf[name2(r)]
		if c == nil {
			c = &col{name: name2(r)}
			if len(dims) > 1 && ch.F != "" {
				c.key = res.Keys[n][1]
			}
			colOf[c.name] = c
			cols = append(cols, c)
		}
		c.total += math.Abs(biFloat(r[m]))
	}
	if len(dims) > 1 && res.Columns[dims[1]].Type != bi.Date {
		slices.SortStableFunc(cols, func(a, b *col) int { return cmp.Compare(b.total, a.total) })
	}
	// too many columns: the rest are summed in «Другие», or — an average, an extreme — left out
	other := len(cols) > biPivotMax && summed
	at := map[string]int{}
	for i, c := range cols {
		at[c.name] = i
		if other {
			at[c.name] = min(i, biPivotMax-1)
		}
	}
	if other {
		cols = append(cols[:biPivotMax-1], &col{name: tr("bi.others")})
	} else if len(cols) > biPivotMax {
		cols, ch.Cut = cols[:biPivotMax], true
	}
	f2 := ""
	if len(dims) > 1 && ch.F != "" {
		f2 = biParam + res.Columns[dims[1]].Field
	}
	ch.Cols = []biCol{{Name: res.Columns[dims[0]].Name}}
	for i, c := range cols {
		h := biCell{Text: c.name}
		if f2 != "" && !(other && i == len(cols)-1) {
			h.F, h.Key = f2, c.key
		}
		ch.PCols = append(ch.PCols, h)
	}
	// the rows in the order of the query
	type row struct {
		head  biCell
		vals  []float64
		has   []bool
		total float64
	}
	var rows []*row
	rowOf := map[string]*row{}
	sums, sumHas := make([]float64, len(cols)), make([]bool, len(cols))
	all := 0.0
	for n, r := range res.Rows {
		name := biLabel(res.Columns[dims[0]].Column, r[dims[0]], tr)
		x := rowOf[name]
		if x == nil {
			x = &row{head: biCell{Text: name}, vals: make([]float64, len(cols)), has: make([]bool, len(cols))}
			if ch.F != "" {
				x.head.F, x.head.Key = ch.F, res.Keys[n][0]
			}
			rowOf[name] = x
			rows = append(rows, x)
		}
		i, v := at[name2(r)], biFloat(r[m])
		if i >= len(cols) {
			continue
		}
		x.vals[i] += v
		x.has[i] = true
		x.total += v
		sums[i] += v
		sumHas[i] = true
		all += v
	}
	for n, x := range rows {
		if n == biTableMax {
			ch.Cut = true
			break
		}
		pr := biPRow{Head: x.head}
		for i := range cols {
			pr.Cells = append(pr.Cells, cell(x.vals[i], x.has[i]))
		}
		pr.Total = cell(x.total, summed)
		ch.PRows = append(ch.PRows, pr)
	}
	if summed {
		for i := range cols {
			ch.PSum = append(ch.PSum, cell(sums[i], sumHas[i]))
		}
		ch.PSum = append(ch.PSum, cell(all, true))
	}
}
