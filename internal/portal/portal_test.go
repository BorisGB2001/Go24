package portal

import (
	"testing"

	"go24/internal/i18n"
)

func TestFormat(t *testing.T) {
	ru := map[string]string{"format.date": "02.01.2006", "format.datetime": "02.01.2006 15:04", "x": "y"}
	for _, c := range []struct {
		set               Settings
		date, when, other string
	}{
		{Settings{}, "02.01.2006", "02.01.2006 15:04", "y"},
		{Settings{DateFormat: "ymd"}, "2006-01-02", "2006-01-02 15:04", "y"},
		{Settings{DateFormat: "mdy", TimeFormat: "12"}, "01/02/2006", "01/02/2006 3:04 PM", "y"},
		{Settings{TimeFormat: "12"}, "02.01.2006", "02.01.2006 3:04 PM", "y"},
	} {
		if d, w, o := c.set.Format("format.date", ru["format.date"]), c.set.Format("format.datetime", ru["format.datetime"]),
			c.set.Format("x", ru["x"]); d != c.date || w != c.when || o != c.other {
			t.Errorf("%+v: %q %q %q", c.set, d, w, o)
		}
	}
}

func TestCheck(t *testing.T) {
	langs := func(l string) bool { return l == "ru-RU" || l == "en-US" }
	for _, ok := range []Settings{{}, {Name: "Ромашка", Lang: "en-US", TZ: "Europe/Moscow", DateFormat: "dmy_slash", TimeFormat: "24"}} {
		if err := Check(ok, langs); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for key, bad := range map[string]Settings{"err.portal_lang": {Lang: "xx"}, "err.portal_tz": {TZ: "Mars/Base"},
		"err.portal_format": {DateFormat: "dd"}, "err.portal_name": {Name: string(make([]rune, 101))}} {
		if err, ok := Check(bad, langs).(*i18n.Error); !ok || err.Key != key {
			t.Errorf("%+v: %v, want %s", bad, err, key)
		}
	}
	if (Settings{}).Title() != "go24" || (Settings{Name: "Ромашка"}).Title() != "Ромашка" {
		t.Error("Title")
	}
}

// TestFilter: the portal's formats come through the bundle for every language, and go away with SetFilter(nil).
func TestFilter(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	b.SetFilter(Settings{DateFormat: "ymd"}.Format)
	if b.T("ru-RU", "format.date") != "2006-01-02" || b.T("en-US", "format.datetime") != "2006-01-02 15:04" {
		t.Error("the portal's format is not used")
	}
	b.SetFilter(nil)
	if b.T("ru-RU", "format.date") != "02.01.2006" {
		t.Error("the filter is not removed")
	}
}
