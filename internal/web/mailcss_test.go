package web

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// TestMailListLayers: the head of the letter list (Фильтр, Сортировка, the search suggestions open from it) stays
// above the sticky group headers («Сегодня», «Закреплено»), or a group header covers its menus (GO24DEV-528).
func TestMailListLayers(t *testing.T) {
	css, err := os.ReadFile("static/css/app.css")
	if err != nil {
		t.Fatal(err)
	}
	z := func(sel string) int {
		m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(sel) + ` \{[^}]*position: sticky[^}]*z-index: (\d+)`).FindSubmatch(css)
		if m == nil {
			t.Fatalf("%s: no sticky rule with z-index", sel)
		}
		n, _ := strconv.Atoi(string(m[1]))
		return n
	}
	if head, group := z(".maillist-head"), z(".mgroup"); head <= group {
		t.Fatalf(".maillist-head z-index %d must be above .mgroup %d", head, group)
	}
}
