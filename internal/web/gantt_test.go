package web

import (
	"fmt"
	"testing"
	"time"

	"go24/internal/tasks"
)

func TestBuildGantt(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC) }
	a := tasks.GanttTask{Task: tasks.Task{ID: "a", Number: 1}, Start: d(1), End: d(5), Planned: true}
	b := tasks.GanttTask{Task: tasks.Task{ID: "b", Number: 2}, Start: d(6), End: d(8), Planned: true}
	c := tasks.GanttTask{Task: tasks.Task{ID: "c", Number: 3}, Start: d(4), End: d(9), Planned: true}
	v := buildGantt(tasks.GanttData{Tasks: []tasks.GanttTask{b, c, a}, Links: []tasks.Link{
		{FromID: "a", ToID: "b", Type: "finish_start"}, // fine: b starts after a ends
		{FromID: "a", ToID: "c", Type: "finish_start"}, // broken: c starts while a is still running
	}}, "", d(7), nil)
	if v.Scale != "day" || v.DayW != ganttScales["day"] {
		t.Fatalf("short range must use the day scale, got %s", v.Scale)
	}
	if v.Bars[0].ID != "a" || v.Bars[1].ID != "c" || v.Bars[2].ID != "b" {
		t.Fatalf("bars must be sorted by start: %s %s %s", v.Bars[0].ID, v.Bars[1].ID, v.Bars[2].ID)
	}
	if v.Bars[0].W != 5*v.DayW {
		t.Fatalf("5-day bar width = %d", v.Bars[0].W)
	}
	if len(v.Links) != 2 || v.Links[0].Bad || !v.Links[1].Bad {
		t.Fatalf("links bad flags: %+v", v.Links)
	}
	if !v.ShowToday || v.TodayX <= v.Bars[0].X {
		t.Fatalf("today line at %d", v.TodayX)
	}
	long := buildGantt(tasks.GanttData{Tasks: []tasks.GanttTask{{Task: tasks.Task{ID: "x"}, Start: d(1), End: d(1).AddDate(2, 0, 0)}}}, "", d(1), nil)
	if long.Scale != "month" {
		t.Fatalf("two years must use the month scale, got %s", long.Scale)
	}
	if forced := buildGantt(tasks.GanttData{Tasks: []tasks.GanttTask{a}}, "week", d(1), nil); forced.Scale != "week" {
		t.Fatalf("explicit scale ignored: %s", forced.Scale)
	}
	if empty := buildGantt(tasks.GanttData{}, "", d(1), nil); !empty.Empty {
		t.Fatal("no tasks must give an empty view")
	}
}

func TestGanttTree(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC) }
	bar := func(n int, parent string, start int) tasks.GanttTask {
		return tasks.GanttTask{Task: tasks.Task{ID: fmt.Sprint(n), ProjectKey: "GO", Number: n, ParentKey: parent}, Start: d(start), End: d(start + 1)}
	}
	// GO-1 (day 5) with GO-3 (day 1) and GO-4 (day 2); GO-2 (day 3) alone; GO-5 under GO-3; GO-6's parent is elsewhere
	list := []tasks.GanttTask{bar(4, "GO-1", 2), bar(1, "", 5), bar(2, "", 3), bar(3, "GO-1", 1), bar(5, "GO-3", 9), bar(6, "XX-1", 4)}
	order := func(bs []ganttBar) (s string) {
		for _, b := range bs {
			s += fmt.Sprintf("%s:%d ", b.Key(), b.Depth)
		}
		return s
	}
	v := buildGantt(tasks.GanttData{Tasks: list}, "", d(1), nil)
	if got := order(v.Bars); got != "GO-2:0 GO-6:0 GO-1:0 GO-3:1 GO-5:2 GO-4:1 " {
		t.Fatalf("tree order: %s", got)
	}
	if !v.Bars[2].Kids || v.Bars[2].Fold != "GO-1" || v.Bars[0].Kids {
		t.Fatalf("fold of GO-1: %+v", v.Bars[2])
	}
	f := buildGantt(tasks.GanttData{Tasks: list}, "", d(1), map[string]bool{"GO-1": true, "GO-3": true})
	if got := order(f.Bars); got != "GO-2:0 GO-6:0 GO-1:0 " {
		t.Fatalf("folded: %s", got)
	}
	if !f.Bars[2].Folded || f.Bars[2].Fold != "GO-3" {
		t.Fatalf("unfolding GO-1 keeps GO-3 folded: %q", f.Bars[2].Fold)
	}
}

func TestGanttLinkTypes(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC) }
	f := ganttBar{GanttTask: tasks.GanttTask{Start: d(5), End: d(9)}, X: 100, W: 50, Mid: 10}
	t2 := ganttBar{GanttTask: tasks.GanttTask{Start: d(7), End: d(12)}, X: 120, W: 60, Mid: 44}
	for _, c := range []struct {
		typ  string
		bad  bool
		path string
	}{
		{"finish_start", true, "M150 10 H156 V29 H112 V44 H118"},   // t starts before f ends: round
		{"start_start", false, "M100 10 H94 V44 H118"},             // straight into t's start
		{"finish_finish", false, "M150 10 H156 V29 H188 V44 H182"}, // into t's finish from the right
		{"start_finish", false, "M100 10 H94 V29 H188 V44 H182"},
	} {
		path, bad := ganttLinkPath(c.typ, f, t2, 34)
		if path != c.path || bad != c.bad {
			t.Errorf("%s: %s bad=%v, want %s bad=%v", c.typ, path, bad, c.path, c.bad)
		}
	}
	if _, bad := ganttLinkPath("finish_finish", t2, f, 34); !bad {
		t.Error("f finishing before its predecessor must break finish_finish")
	}
}
