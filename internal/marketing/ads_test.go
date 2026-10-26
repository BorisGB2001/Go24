package marketing

import (
	"strings"
	"testing"
)

func TestUTM(t *testing.T) {
	u := ParseUTM(params{"utm_source": " yandex ", "utm_campaign": strings.Repeat("я", 300)}.get)
	if u.Source != "yandex" || len([]rune(u.Campaign)) != maxUTM || u.Medium != "" {
		t.Fatalf("ParseUTM: %+v", u)
	}
	if v := (UTM{Source: "vk", Term: "окна"}).Values().Encode(); v != "utm_source=vk&utm_term=%D0%BE%D0%BA%D0%BD%D0%B0" {
		t.Fatalf("Values: %s", v)
	}
	if !(UTM{}).IsZero() || (UTM{Content: "x"}).IsZero() {
		t.Fatal("IsZero")
	}
	if got := strings.Join(SplitUTM("Yandex, ya.direct;yandex\nVK  "), "|"); got != "yandex|ya.direct|vk" {
		t.Fatalf("SplitUTM: %s", got)
	}
}

type params map[string]string

func (p params) get(k string) string { return p[k] }

func TestChannel(t *testing.T) {
	c := Channel{Expenses: 1500, Leads: 3, Won: 2, Revenue: 3000}
	if pct, ok := c.ROI(); !ok || pct != 100 {
		t.Fatalf("ROI: %d %v", pct, ok)
	}
	if c.CostPerLead() != 500 || c.CostPerSale() != 750 {
		t.Fatalf("costs: %v %v", c.CostPerLead(), c.CostPerSale())
	}
	if _, ok := (Channel{Revenue: 10}).ROI(); ok {
		t.Fatal("ROI without expenses")
	}
	if (Channel{Expenses: 10}).CostPerLead() != 0 {
		t.Fatal("cost of no leads")
	}
	m := newMatcher([]AdSource{{ID: "a", UTM: []string{"yandex"}}, {ID: "b", UTM: []string{"vk"}, CRMSource: "adv"}, {ID: "c", CRMSource: "adv"}})
	for _, x := range []struct {
		utm, origin string
		want        int
	}{{"Yandex", "", 0}, {"vk", "", 1}, {"google", "adv", 1}, {"", "adv", 1}, {"google", "", -1}, {"", "", -1}} {
		if got := m.of(x.utm, x.origin); got != x.want {
			t.Errorf("of(%q, %q) = %d, want %d", x.utm, x.origin, got, x.want)
		}
	}
	if d, err := parseDay("25.10.2026"); err != nil || d.Format("2006-01-02") != "2026-10-25" {
		t.Fatalf("parseDay: %v %v", d, err)
	}
}
