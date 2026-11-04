package bp

import (
	"fmt"
	"strings"
	"testing"
)

// shape writes the blocks as «N[col|col]» to compare.
func shape(nodes []Node) string {
	var parts []string
	for _, n := range nodes {
		s := fmt.Sprint(n.N)
		if n.Cols != nil {
			var cols []string
			for _, c := range n.Cols {
				cols = append(cols, shape(c.Nodes))
			}
			s += "[" + strings.Join(cols, "|") + "]"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func note(text string) Step { return Step{Kind: "notify", Text: text} }

// texts are the steps' texts in order, the jumps by «>N».
func texts(steps []Step) string {
	var parts []string
	for _, st := range steps {
		s := st.Kind
		if st.Text != "" {
			s = st.Text
		}
		if st.Goto != 0 {
			s += fmt.Sprintf(">%d", st.Goto)
		}
		for _, b := range st.Branches {
			s += fmt.Sprintf("+%d", b)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestScheme(t *testing.T) {
	// a condition with «иначе», a parallel step, an iterator, a loop and a jump back
	steps := []Step{
		note("a"),
		{Kind: "if", Field: "x", Op: "eq", Value: "1", Goto: 5}, note("yes"), {Kind: "goto", Goto: 6}, note("no"),
		{Kind: "parallel", Goto: 10, Branches: []int{8, 9}}, note("p1"), note("p2"), note("p3"),
		{Kind: "foreach", Field: "m", Item: "i"}, note("each"), {Kind: "goto", Goto: 10},
		note("l1"), note("l2"), {Kind: "loop", Field: "n", Op: "lt", Value: "3", Goto: 13},
		{Kind: "goto", Goto: 1},
	}
	if got, want := shape(Scheme(steps)), "1 2[3|5] 6[7|8|9] 10[11] 15[13 14] 16"; got != want {
		t.Fatalf("scheme %q, want %q", got, want)
	}
	nodes := Scheme(steps)
	if c := nodes[1].Cols[1]; c.Gap != (Gap{At: 5, From: 2}) || !c.Open || c.Label != "bp.scheme.no" {
		t.Errorf("«Нет»: %+v", c)
	}
	if c := nodes[2].Cols[2]; c.Gap != (Gap{At: 9, From: 6, Br: 2}) {
		t.Errorf("the 3rd branch: %+v", c.Gap)
	}
	if n := nodes[4]; n.A != 12 || n.B != 15 || n.Cols[0].Gap != (Gap{At: 13, From: 15}) {
		t.Errorf("the loop: %+v", n)
	}
	// a condition with no «иначе»: «Нет» is empty and closed
	steps = []Step{{Kind: "if", Goto: 3}, note("yes"), note("after")}
	if n := Scheme(steps)[0]; shape(Scheme(steps)) != "1[2|] 3" || n.Cols[1].Open {
		t.Errorf("if: %s %+v", shape(Scheme(steps)), n)
	}
	// a jump back is of no shape — the steps are blocks
	steps = []Step{note("a"), {Kind: "if", Goto: 1}, note("b")}
	if got := shape(Scheme(steps)); got != "1 2 3" {
		t.Errorf("jump back: %q", got)
	}
	// a condition to the end of the process: all the rest is «Да»
	steps = []Step{{Kind: "if"}, note("b"), note("c")}
	if got := shape(Scheme(steps)); got != "1[2 3|]" {
		t.Errorf("if to the end: %q", got)
	}
}

func TestInsert(t *testing.T) {
	steps := func() []Step {
		return []Step{{Kind: "if", Goto: 4}, note("yes"), {Kind: "goto", Goto: 5}, note("no"), note("after")}
	}
	// the top of «Нет»: the condition's jump goes to the new step, the end of «Да» past it
	if got := texts(Insert(steps(), note("new"), Gap{At: 4, From: 1})); got != "if>4 yes goto>6 new no after" {
		t.Errorf("top of «Нет»: %s", got)
	}
	// before the step 4 as before: the jumps keep pointing at it
	if got := texts(Insert(steps(), note("new"), Gap{At: 4})); got != "if>5 yes goto>6 new no after" {
		t.Errorf("before 4: %s", got)
	}
	// the end of «Нет»: «Да» jumps past it
	if got := texts(Insert(steps(), note("new"), Gap{At: 5})); got != "if>4 yes goto>6 no new after" {
		t.Errorf("end of «Нет»: %s", got)
	}
	// the top of a branch of a parallel step
	par := []Step{{Kind: "parallel", Goto: 4, Branches: []int{3}}, note("p1"), note("p2"), note("after")}
	if got := texts(Insert(par, note("new"), Gap{At: 3, From: 1, Br: 1})); got != "parallel>5+3 p1 new p2 after" {
		t.Errorf("branch top: %s", got)
	}
	if got := texts(Insert([]Step{note("a")}, note("new"), Gap{At: 9})); got != "a new" {
		t.Errorf("out of the steps — the end: %s", got)
	}
}

func TestMove(t *testing.T) {
	steps := func() []Step {
		return []Step{note("a"), {Kind: "if", Goto: 5}, note("yes"), {Kind: "goto", Goto: 6}, note("no"), note("b"), note("c")}
	}
	// the condition's block [1, 5) to the end: its jumps inside follow it, the join is past it — the end
	got, ok := Move(steps(), 1, 5, Gap{At: 8})
	if !ok || texts(got) != "a b c if>7 yes goto no" || shape(Scheme(got)) != "1 2 3 4[5|7]" {
		t.Fatalf("to the end: %v %s %s", ok, texts(got), shape(Scheme(got)))
	}
	// and back before «b»: the join of the block at the end (0) is the step after it again
	back, ok := Move(got, 3, 7, Gap{At: 2})
	if !ok || texts(back) != "a if>5 yes goto>6 no b c" {
		t.Fatalf("back: %v %s", ok, texts(back))
	}
	// a step into the top of «Нет»: the condition's jump goes to it
	got, ok = Move(steps(), 6, 7, Gap{At: 5, From: 2})
	if !ok || texts(got) != "a if>5 yes goto>7 c no b" || shape(Scheme(got)) != "1 2[3|5 6] 7" {
		t.Errorf("into «Нет»: %v %s %s", ok, texts(got), shape(Scheme(got)))
	}
	// a jump to a moved step follows it
	jumps := []Step{{Kind: "goto", Goto: 3}, note("x"), note("y")}
	if got, _ := Move(jumps, 2, 3, Gap{At: 2}); texts(got) != "goto>2 y x" {
		t.Errorf("follow: %s", texts(got))
	}
	// into the block itself or right by it — nothing
	for _, g := range []Gap{{At: 3}, {At: 2}, {At: 6}, {At: 9}, {At: 7, From: 2}} {
		if _, ok := Move(steps(), 1, 5, g); ok {
			t.Errorf("moved to %+v", g)
		}
	}
	// a parallel step at the end joining at the end of the process — before the other steps it joins before them
	par := []Step{note("a"), {Kind: "parallel", Branches: []int{4}}, note("p1"), note("p2")}
	if got, ok := Move(par, 1, 4, Gap{At: 1}); !ok || texts(got) != "parallel>4+3 p1 p2 a" || shape(Scheme(got)) != "1[2|3] 4" {
		t.Errorf("parallel: %v %s", ok, texts(got))
	}
}

func TestCopy(t *testing.T) {
	steps := []Step{note("a"), {Kind: "if", Goto: 5}, note("yes"), {Kind: "goto", Goto: 6}, note("no"), {Kind: "goto", Goto: 2}}
	got := Copy(steps, 1, 5)
	if texts(got) != "a if>5 yes goto>6 no if>9 yes goto>10 no goto>2" || shape(Scheme(got)) != "1 2[3|5] 6[7|9] 10" {
		t.Errorf("copy: %s %s", texts(got), shape(Scheme(got)))
	}
	// the copy is of its own: its slices are not the block's
	steps = []Step{{Kind: "parallel", Branches: []int{3}}, note("p1"), note("p2")}
	got = Copy(steps, 0, 3)
	if shape(Scheme(got)) != "1[2|3] 4[5|6]" {
		t.Errorf("copy scheme: %s", shape(Scheme(got)))
	}
	got[3].Branches[0] = 99
	if texts(got[:3]) != "parallel>4+3 p1 p2" || steps[0].Branches[0] != 3 {
		t.Errorf("copy of the end: %s", texts(got))
	}
}
