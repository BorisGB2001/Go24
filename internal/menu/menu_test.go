package menu

import (
	"slices"
	"strings"
	"testing"
)

func ids(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func all() []string {
	var out []string
	for _, b := range Builtin {
		out = append(out, b.ID)
	}
	return out
}

func TestArrange(t *testing.T) {
	// go24's order when nothing is set; the messenger only when it is on
	shown, hidden := Arrange(Config{}, all()[1:])
	if got := ids(shown); !slices.Equal(got, all()[1:]) || hidden != nil {
		t.Errorf("default = %v %v", got, ids(hidden))
	}
	// own order, a hidden item, an own link; items the menu does not name come after their go24 neighbour
	c := Config{Order: []string{"im", "projects", "tasks", "c1", "feed"}, Hidden: []string{"feed"},
		Custom: []Custom{{ID: "c1", Title: "Сделки", URL: "/crm/deals?view=kanban"}}}
	shown, hidden = Arrange(c, all())
	got := ids(shown)
	if !slices.Equal(got[:6], []string{"im", "collabs", "projects", "company", "files", "tasks"}) || shown[6].Custom == nil || shown[6].URL() != "/crm/deals?view=kanban" {
		t.Errorf("shown = %v", got)
	}
	if !slices.Equal(ids(hidden), []string{"feed"}) {
		t.Errorf("hidden = %v", ids(hidden))
	}
	if i, j := slices.Index(got, "feed"), slices.Index(got, "calendar"); i >= 0 || j < 0 {
		t.Errorf("calendar (after the hidden feed) is lost: %v", got)
	}
	if shown[0].URL() != "/im" {
		t.Errorf("start = %s", shown[0].URL())
	}
	// the messenger off: an item that is not available is left out
	shown, _ = Arrange(c, all()[1:])
	if ids(shown)[0] != "collabs" {
		t.Errorf("without the messenger = %v", ids(shown))
	}
}

func TestArrangeApps(t *testing.T) {
	apps := []Custom{{ID: "app.1", Title: "Доставка", URL: "/apps/1"}, {ID: "app.2", Title: "Склад", URL: "/apps/2"}}
	// new applications go last; a placed and a hidden one keep their places; one no longer for the employee is gone
	c := Config{Order: []string{"app.2", "tasks", "app.9"}, Hidden: []string{"app.9"}}
	shown, hidden := Arrange(c, all(), apps...)
	got := ids(shown)
	if got[0] != "app.2" || shown[0].App == nil || shown[0].URL() != "/apps/2" || got[len(got)-1] != "app.1" || hidden != nil {
		t.Errorf("shown = %v, hidden = %v", got, ids(hidden))
	}
	c, err := Check(Config{Order: []string{"app.1", "app.1", "tasks"}, Hidden: []string{"app.2"}})
	if err != nil || !slices.Equal(c.Order, []string{"app.1", "tasks"}) || !slices.Equal(c.Hidden, []string{"app.2"}) {
		t.Errorf("check = %+v %v", c, err)
	}
}

func TestCheck(t *testing.T) {
	c, err := Check(Config{Order: []string{"tasks", "x", "tasks", "c1"}, Hidden: []string{"kb", "nope"},
		Custom: []Custom{{ID: "c1", Title: " Отчёт ", URL: "/tasks/efficiency"}, {Title: "Сайт", URL: "https://example.com", NewTab: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Order) != 3 || c.Order[0] != "tasks" || c.Order[1] != "c1" || !strings.HasPrefix(c.Order[2], "c") || c.Order[2] != c.Custom[1].ID {
		t.Errorf("order = %v", c.Order)
	}
	if !slices.Equal(c.Hidden, []string{"kb"}) || c.Custom[0].Title != "Отчёт" {
		t.Errorf("cleaned = %+v", c)
	}
	for _, bad := range []Custom{{Title: "", URL: "/x"}, {Title: "x", URL: "javascript:alert(1)"}, {Title: "x", URL: "//evil.example"},
		{Title: "x", URL: "/\\evil.example"}, {Title: strings.Repeat("я", 81), URL: "/x"}, {Title: "x", URL: "ftp://h"}} {
		if _, err := Check(Config{Custom: []Custom{bad}}); err == nil {
			t.Errorf("%+v is accepted", bad)
		}
	}
	many := make([]Custom, MaxCustom+1)
	for i := range many {
		many[i] = Custom{Title: "x", URL: "/x"}
	}
	if _, err := Check(Config{Custom: many}); err == nil {
		t.Error("too many links are accepted")
	}
}
