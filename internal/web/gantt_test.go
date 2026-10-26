package web

import (
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
		{FromID: "a", ToID: "b"}, // fine: b starts after a ends
		{FromID: "a", ToID: "c"}, // broken: c starts while a is still running
	}}, "", d(7))
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
	long := buildGantt(tasks.GanttData{Tasks: []tasks.GanttTask{{Task: tasks.Task{ID: "x"}, Start: d(1), End: d(1).AddDate(2, 0, 0)}}}, "", d(1))
	if long.Scale != "month" {
		t.Fatalf("two years must use the month scale, got %s", long.Scale)
	}
	if forced := buildGantt(tasks.GanttData{Tasks: []tasks.GanttTask{a}}, "week", d(1)); forced.Scale != "week" {
		t.Fatalf("explicit scale ignored: %s", forced.Scale)
	}
	if empty := buildGantt(tasks.GanttData{}, "", d(1)); !empty.Empty {
		t.Fatal("no tasks must give an empty view")
	}
}
