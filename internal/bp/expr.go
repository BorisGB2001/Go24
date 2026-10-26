package bp

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go24/internal/i18n"
)

// A value that begins with «=» is a formula, as «Математические операции» and {{=…}}: the fields put in
// ({sum}), it is counted — numbers (a comma or a point), + - * /, brackets, min(), max(), round(x; digits), abs(),
// today() (arguments by «;» or «, »); a date (2026-11-02, with T15:30 a time) plus or minus days is a date, a date minus a date — days.

// value is the text of a step's value as the process holds it: {…} filled, a formula counted.
func (s *Store) value(ctx context.Context, x Instance, text string) (string, error) {
	v := s.fillRaw(ctx, x, text)
	if f, ok := strings.CutPrefix(strings.TrimSpace(text), "="); ok {
		return calc(strings.TrimPrefix(strings.TrimSpace(v), "="), f)
	}
	return v, nil
}

type num struct {
	n            float64
	date, hasTim bool
	t            time.Time
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(T\d{2}:\d{2})?`)

type calcer struct {
	s   string
	pos int
	err bool
}

// calc counts the formula expr (src — as written, for the error).
func calc(expr, src string) (string, error) {
	c := &calcer{s: expr}
	v := c.sum()
	c.space()
	if c.err || c.pos < len(c.s) {
		return "", i18n.E("err.bp_expr", strings.TrimSpace(src))
	}
	if v.date {
		if v.hasTim {
			return v.t.Format("2006-01-02T15:04"), nil
		}
		return v.t.Format("2006-01-02"), nil
	}
	return strconv.FormatFloat(math.Round(v.n*1e9)/1e9, 'f', -1, 64), nil
}

func (c *calcer) space() {
	for c.pos < len(c.s) && c.s[c.pos] == ' ' {
		c.pos++
	}
}

func (c *calcer) peek() byte {
	c.space()
	if c.pos < len(c.s) {
		return c.s[c.pos]
	}
	return 0
}

func (c *calcer) sum() num {
	v := c.product()
	for !c.err {
		op := c.peek()
		if op != '+' && op != '-' {
			return v
		}
		c.pos++
		w := c.product()
		switch {
		case v.date && w.date && op == '-': // days between
			v = num{n: w.t.Sub(v.t).Hours() / -24}
		case v.date && !w.date:
			days := w.n
			if op == '-' {
				days = -days
			}
			v.t = v.t.Add(time.Duration(days * 24 * float64(time.Hour)))
		case !v.date && !w.date:
			if op == '+' {
				v.n += w.n
			} else {
				v.n -= w.n
			}
		default:
			c.err = true
		}
	}
	return v
}

func (c *calcer) product() num {
	v := c.unary()
	for !c.err {
		op := c.peek()
		if op != '*' && op != '/' {
			return v
		}
		c.pos++
		w := c.unary()
		if v.date || w.date || op == '/' && w.n == 0 {
			c.err = true
			return v
		}
		if op == '*' {
			v.n *= w.n
		} else {
			v.n /= w.n
		}
	}
	return v
}

func (c *calcer) unary() num {
	if c.peek() == '-' {
		c.pos++
		v := c.unary()
		if v.date {
			c.err = true
		}
		v.n = -v.n
		return v
	}
	return c.primary()
}

func (c *calcer) primary() num {
	ch := c.peek()
	switch {
	case ch == '(':
		c.pos++
		v := c.sum()
		if c.peek() != ')' {
			c.err = true
		}
		c.pos++
		return v
	case ch >= '0' && ch <= '9' || ch == '.' || ch == ',':
		if m := dateRe.FindString(c.s[c.pos:]); m != "" {
			c.pos += len(m)
			layout := "2006-01-02"
			if len(m) > 10 {
				layout = "2006-01-02T15:04"
			}
			t, err := time.Parse(layout, m)
			c.err = c.err || err != nil
			return num{date: true, hasTim: len(m) > 10, t: t}
		}
		start := c.pos
		digit := func(i int) bool { return i < len(c.s) && c.s[i] >= '0' && c.s[i] <= '9' }
		for digit(c.pos) || c.pos < len(c.s) && (c.s[c.pos] == '.' || c.s[c.pos] == ',' && digit(c.pos+1)) { // «1,5»; «x, 2» — two
			c.pos++
		}
		n, err := strconv.ParseFloat(strings.ReplaceAll(c.s[start:c.pos], ",", "."), 64)
		c.err = c.err || err != nil
		return num{n: n}
	case unicode.IsLetter(rune(ch)):
		start := c.pos
		for c.pos < len(c.s) && unicode.IsLetter(rune(c.s[c.pos])) {
			c.pos++
		}
		name := strings.ToLower(c.s[start:c.pos])
		if c.peek() != '(' {
			c.err = true
			return num{}
		}
		c.pos++
		var args []num
		for c.peek() != ')' && !c.err {
			args = append(args, c.sum())
			if c.peek() == ';' || c.peek() == ',' && len(args) > 0 { // round(x; 2) or round(x, 2)
				c.pos++
			} else if c.peek() != ')' {
				c.err = true
			}
		}
		c.pos++
		return c.call(name, args)
	}
	c.err = true
	return num{}
}

func (c *calcer) call(name string, args []num) num {
	for _, a := range args {
		if a.date {
			c.err = true
			return num{}
		}
	}
	switch {
	case name == "today" && len(args) == 0:
		y, m, d := time.Now().Date()
		return num{date: true, t: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
	case name == "abs" && len(args) == 1:
		return num{n: math.Abs(args[0].n)}
	case name == "round" && (len(args) == 1 || len(args) == 2):
		p := 1.0
		if len(args) == 2 {
			p = math.Pow(10, math.Round(args[1].n))
		}
		return num{n: math.Round(args[0].n*p) / p}
	case (name == "min" || name == "max") && len(args) > 0:
		v := args[0].n
		for _, a := range args[1:] {
			if name == "min" {
				v = math.Min(v, a.n)
			} else {
				v = math.Max(v, a.n)
			}
		}
		return num{n: v}
	}
	c.err = true
	return num{}
}
