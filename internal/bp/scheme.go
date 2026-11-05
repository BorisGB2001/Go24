package bp

import "encoding/json"

// The scheme of a template, as the graphical designer: the steps are blocks top-down, a condition with its
// «Да» and «Нет» side by side, the branches of a parallel step as columns, the steps of an iterator and a loop inside
// their block. The steps stay a list with jumps by numbers; Scheme finds these shapes in it, and what is of no shape
// (a jump back, out of a branch) is a block with its «→ шаг N».

// Node is a block of the scheme: the step numbered N from 1 and the steps [A, B) (indexes) it holds with its columns.
type Node struct {
	N    int
	A, B int
	Step Step
	Cols []Col // if: «Да», «Нет»; parallel: the branches; foreach, loop: the steps repeated
}

// Col is a column of blocks: the steps [A, B) (indexes) of a branch, Label — its title key ("" — none), Gap — the place
// at its top; Open — steps can be put at its top (not the «Нет» of a condition with no «иначе»).
type Col struct {
	Label string
	A, B  int
	Gap   Gap
	Open  bool
	Nodes []Node
}

// Gap is a place in the scheme a step is put in: before the step numbered At from 1 (the number past the last one — at
// the end); the jump of the step numbered From to At (its Goto, or its branch Br from 1 of a parallel step) goes to
// what is put there — the top of a branch. From 0 — the jumps keep pointing at the step At.
type Gap struct {
	At, From, Br int
}

// Scheme: the blocks of the steps.
func Scheme(steps []Step) []Node {
	return scheme(steps, 0, len(steps))
}

// target is the index of the step numbered n from 1; 0 and past the last one — len(steps).
func target(steps []Step, n int) int {
	if n <= 0 || n > len(steps) {
		return len(steps)
	}
	return n - 1
}

func scheme(steps []Step, a, b int) []Node {
	var out []Node
	col := func(label string, a, b int, g Gap) Col {
		return Col{Label: label, A: a, B: b, Gap: g, Open: true, Nodes: scheme(steps, a, b)}
	}
	for i := a; i < b; {
		// a loop goes back to the step the block starts with: the outermost one is the block
		loop := -1
		for k := b - 1; k >= i; k-- {
			if steps[k].Kind == "loop" && steps[k].Goto == i+1 {
				loop = k
				break
			}
		}
		if loop >= 0 {
			out = append(out, Node{N: loop + 1, A: i, B: loop + 1, Step: steps[loop],
				Cols: []Col{col("", i, loop, Gap{At: i + 1, From: loop + 1})}})
			i = loop + 1
			continue
		}
		st, n := steps[i], Node{N: i + 1, A: i, B: i + 1, Step: steps[i]}
		switch st.Kind {
		case "foreach":
			if e := EachEnd(steps, i); e > 0 && e <= b {
				n.B, n.Cols = e, []Col{col("", i+1, e-1, Gap{At: i + 2})}
			}
		case "parallel":
			join, rs, ok := target(steps, st.Goto), Ranges(steps, i), true
			for _, r := range rs {
				ok = ok && r[0] > i && r[0] <= r[1] && r[1] <= join
			}
			if ok && join <= b {
				n.B = join
				for k, r := range rs {
					g := Gap{At: r[0] + 1}
					if k > 0 {
						g = Gap{At: r[0] + 1, From: i + 1, Br: k}
					}
					n.Cols = append(n.Cols, col("", r[0], r[1], g))
				}
			}
		case "if":
			if t := target(steps, st.Goto); t > i+1 && t <= b {
				yes, no := col("bp.scheme.yes", i+1, t, Gap{At: i + 2}), Col{Label: "bp.scheme.no", A: t, B: t, Gap: Gap{At: t + 1, From: i + 1}}
				n.B = t
				// «иначе»: «Да» ends with a jump forward past the steps of «Нет» — they join there
				if g := steps[t-1]; g.Kind == "goto" && t-1 > i {
					if u := target(steps, g.Goto); u >= t && u <= b {
						yes = col("bp.scheme.yes", i+1, t-1, Gap{At: i + 2})
						no = col("bp.scheme.no", t, u, Gap{At: t + 1, From: i + 1})
						n.B = u
					}
				}
				n.Cols = []Col{yes, no}
			}
		}
		out = append(out, n)
		i = n.B
	}
	return out
}

// Skip is the index of the step a process goes to past the switched off step i («Выключить»): a condition, a parallel
// step and an iterator go with the steps of their blocks, any other step (a loop too: its steps are done once) alone.
func Skip(steps []Step, i int) int {
	if k := steps[i].Kind; k == "if" || k == "parallel" || k == "foreach" {
		if n, ok := nodeAt(Scheme(steps), i); ok && n.B > i+1 {
			return n.B
		}
	}
	return i + 1
}

// nodeAt is the block of the step of the index i, in the blocks or inside them.
func nodeAt(nodes []Node, i int) (Node, bool) {
	for _, n := range nodes {
		if n.N == i+1 {
			return n, true
		}
		for _, c := range n.Cols {
			if m, ok := nodeAt(c.Nodes, i); ok {
				return m, true
			}
		}
	}
	return Node{}, false
}

// Renumber changes the step's jumps to the steps by f (the number from 1 → the new one); 0 — the end — stays.
func Renumber(st *Step, f func(int) int) {
	if st.Goto != 0 {
		st.Goto = f(st.Goto)
	}
	for k := range st.Branches {
		st.Branches[k] = f(st.Branches[k])
	}
	for k := range st.Commands {
		if st.Commands[k].Goto != 0 {
			st.Commands[k].Goto = f(st.Commands[k].Goto)
		}
	}
}

// valid: the gap is of the steps.
func (g Gap) valid(steps []Step) bool {
	return g.At >= 1 && g.At <= len(steps)+1 && g.From >= 0 && g.From <= len(steps) && g.Br >= 0
}

// jumpOf is the jump of the gap's step to its top (nil — it has none).
func (g Gap) jumpOf(st *Step) *int {
	if g.Br == 0 && st.Goto == g.At {
		return &st.Goto
	}
	if g.Br > 0 && g.Br <= len(st.Branches) && st.Branches[g.Br-1] == g.At {
		return &st.Branches[g.Br-1]
	}
	return nil
}

// toEnd: the step's Goto 0 is the step after the block that ends the steps — where a condition's «Да» and «Нет», a
// parallel step's branches join (not the end of the process when the block is put before other steps).
func toEnd(st Step) bool {
	return st.Goto == 0 && (st.Kind == "if" || st.Kind == "goto" || st.Kind == "parallel")
}

// Insert puts the step st into the gap; the jumps past it keep pointing at their steps (st's own are of the new numbers).
func Insert(steps []Step, st Step, g Gap) []Step {
	if !g.valid(steps) {
		g = Gap{At: len(steps) + 1}
	}
	for i := range steps {
		var top *int // the top of the branch is the new step
		if i == g.From-1 {
			top = g.jumpOf(&steps[i])
		}
		Renumber(&steps[i], func(m int) int {
			if m >= g.At {
				return m + 1
			}
			return m
		})
		if top != nil {
			*top = g.At
		}
	}
	return append(steps[:g.At-1], append([]Step{st}, steps[g.At-1:]...)...)
}

// Move puts the steps [a, b) (indexes) — a block of the scheme — into the gap. The jumps to its steps follow them; its
// jumps past its end (the join of its branches) go past it in the new place; the others keep pointing at their steps.
// False: the gap is in the block or by it — nothing to move.
func Move(steps []Step, a, b int, g Gap) ([]Step, bool) {
	l := len(steps)
	if a < 0 || b <= a || b > l || !g.valid(steps) || g.At-1 >= a && g.At-1 <= b || g.From-1 >= a && g.From-1 < b {
		return steps, false
	}
	var order []int // the old indexes in the new order
	for i := 0; i < l; i++ {
		if i == g.At-1 {
			for j := a; j < b; j++ {
				order = append(order, j)
			}
		}
		if i < a || i >= b {
			order = append(order, i)
		}
	}
	if g.At == l+1 {
		for j := a; j < b; j++ {
			order = append(order, j)
		}
	}
	num := make([]int, l) // old index → new number
	for p, i := range order {
		num[i] = p + 1
	}
	after := 0 // the step after the block in the new place
	if g.At <= l {
		after = num[g.At-1]
	}
	out := make([]Step, l)
	for p, i := range order {
		st := clone(steps[i])
		inside := i >= a && i < b
		var top *int
		if i == g.From-1 && g.At <= l {
			top = g.jumpOf(&st)
		}
		end := inside && b == l && toEnd(st)
		Renumber(&st, func(m int) int {
			switch {
			case inside && m == b+1:
				return after
			case m < 1 || m > l:
				return m
			}
			return num[m-1]
		})
		if end {
			st.Goto = after
		}
		if top != nil { // the block is the top of the branch
			*top = num[a]
		}
		out[p] = st
	}
	return out, true
}

// Copy puts a copy of the steps [a, b) (indexes) — a block of the scheme — right after them: the jumps of the copy
// inside it are of the copy, the block's jumps to its end — to the copy, the copy's — past it.
func Copy(steps []Step, a, b int) []Step {
	l, n := len(steps), b-a
	if a < 0 || b <= a || b > l {
		return steps
	}
	out := make([]Step, 0, l+n)
	for i := 0; i < l; i++ {
		st := clone(steps[i])
		inside := i >= a && i < b
		Renumber(&st, func(m int) int {
			if m > b+1 || m == b+1 && !inside {
				return m + n
			}
			return m
		})
		if inside && b == l && toEnd(st) { // the block joins the copy
			st.Goto = b + 1
		}
		out = append(out, st)
		if i == b-1 {
			for j := a; j < b; j++ {
				c := clone(steps[j])
				Renumber(&c, func(m int) int {
					if m > a {
						return m + n
					}
					return m
				})
				out = append(out, c)
			}
		}
	}
	return out
}

// clone is a deep copy of the step.
func clone(st Step) Step {
	var c Step
	b, _ := json.Marshal(st)
	_ = json.Unmarshal(b, &c)
	return c
}
