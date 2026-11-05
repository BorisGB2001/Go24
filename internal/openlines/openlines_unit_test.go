package openlines

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestWidgetDay(t *testing.T) {
	cases := []struct {
		name string
		w    Widget
		day  int
		want bool
	}{
		{"empty days", Widget{}, 1, false},
		{"matching day", Widget{HoursDays: []int32{1, 3, 5}}, 3, true},
		{"non-matching day", Widget{HoursDays: []int32{1, 3, 5}}, 2, false},
		{"sunday match", Widget{HoursDays: []int32{0, 6}}, 0, true},
		{"saturday match", Widget{HoursDays: []int32{0, 6}}, 6, true},
		{"negative day", Widget{HoursDays: []int32{1, 2}}, -1, false},
		{"out of range day", Widget{HoursDays: []int32{1, 2}}, 7, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.w.Day(tc.day); got != tc.want {
				t.Errorf("Widget.Day(%d) = %v; want %v", tc.day, got, tc.want)
			}
		})
	}
}

func TestWidgetInHours(t *testing.T) {
	parseTime := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatalf("parse time %q: %v", s, err)
		}
		return tm
	}

	// 2026-10-26 is a Monday (weekday 1)
	// 2026-10-27 is a Tuesday (weekday 2)
	// 2026-10-25 is a Sunday (weekday 0)

	t.Run("hours disabled always in hours", func(t *testing.T) {
		w := Widget{HoursOn: false}
		for _, ts := range []string{"2026-10-26 03:00", "2026-10-25 12:00", "2026-10-26 23:59"} {
			if !w.InHours(parseTime(ts)) {
				t.Errorf("expected InHours=true when HoursOn is false for %s", ts)
			}
		}
	})

	t.Run("daytime schedule", func(t *testing.T) {
		w := Widget{
			HoursOn:   true,
			HoursDays: []int32{1, 2}, // Monday, Tuesday
			HoursFrom: "09:00",
			HoursTo:   "18:00",
		}
		cases := []struct {
			time string
			want bool
		}{
			{"2026-10-26 08:59", false}, // minute before start
			{"2026-10-26 09:00", true},  // exact start
			{"2026-10-26 12:30", true},  // mid-day
			{"2026-10-26 17:59", true},  // minute before end
			{"2026-10-26 18:00", false}, // exact end
			{"2026-10-26 20:00", false}, // evening
			{"2026-10-25 12:00", false}, // working hour on non-working day (Sunday)
			{"2026-10-27 10:00", true},  // working hour on working day (Tuesday)
		}
		for _, tc := range cases {
			if got := w.InHours(parseTime(tc.time)); got != tc.want {
				t.Errorf("InHours(%s) = %v; want %v", tc.time, got, tc.want)
			}
		}
	})

	t.Run("overnight schedule", func(t *testing.T) {
		w := Widget{
			HoursOn:   true,
			HoursDays: []int32{1}, // Monday
			HoursFrom: "21:00",
			HoursTo:   "06:00",
		}
		cases := []struct {
			time string
			want bool
		}{
			{"2026-10-26 20:59", false}, // before start on Monday
			{"2026-10-26 21:00", true},  // exact start on Monday
			{"2026-10-26 23:30", true},  // late night on Monday
			{"2026-10-26 00:00", true},  // midnight on Monday (Monday morning < 06:00)
			{"2026-10-26 05:59", true},  // minute before morning end on Monday
			{"2026-10-26 06:00", false}, // exact morning end on Monday
			{"2026-10-26 12:00", false}, // midday on Monday
			{"2026-10-27 02:00", false}, // Tuesday morning is not Monday
		}
		for _, tc := range cases {
			if got := w.InHours(parseTime(tc.time)); got != tc.want {
				t.Errorf("overnight InHours(%s) = %v; want %v", tc.time, got, tc.want)
			}
		}
	})
}

func TestWidgetAllowed(t *testing.T) {
	t.Run("empty origins allows everything", func(t *testing.T) {
		w := Widget{}
		if !w.Allowed("https://example.com") || !w.Allowed("http://localhost:8080") || !w.Allowed("") {
			t.Error("empty Origins must allow any origin")
		}
	})

	t.Run("matches configured origins case-insensitively", func(t *testing.T) {
		w := Widget{Origins: []string{"https://site.ru", "http://localhost:3000"}}
		cases := []struct {
			origin string
			want   bool
		}{
			{"https://site.ru", true},
			{"HTTPS://SITE.RU", true},
			{"http://localhost:3000", true},
			{"HTTP://LOCALHOST:3000", true},
			{"https://site.ru:443", false},
			{"http://site.ru", false},
			{"https://othersite.ru", false},
			{"", false},
		}
		for _, tc := range cases {
			if got := w.Allowed(tc.origin); got != tc.want {
				t.Errorf("Allowed(%q) = %v; want %v", tc.origin, got, tc.want)
			}
		}
	})
}

func TestCleanOrigins(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{"empty list", nil, []string{}, false},
		{"all empty strings", []string{"", "   ", "\t"}, []string{}, false},
		{
			"valid http and https",
			[]string{"https://example.com", "http://local.test:8080"},
			[]string{"https://example.com", "http://local.test:8080"},
			false,
		},
		{
			"normalizes case and path",
			[]string{"  HTTPS://MySite.COM/some/path?q=1#hash  "},
			[]string{"https://mysite.com"},
			false,
		},
		{
			"unsupported scheme ftp",
			[]string{"ftp://example.com"},
			nil,
			true,
		},
		{
			"unsupported scheme javascript",
			[]string{"javascript:alert(1)"},
			nil,
			true,
		},
		{
			"missing host",
			[]string{"https://"},
			nil,
			true,
		},
		{
			"plain word not a url",
			[]string{"localhost"},
			nil,
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanOrigins(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("cleanOrigins(%v) error = %v; wantErr %v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr {
				var ve *i18n.Error
				if !errors.As(err, &ve) {
					t.Errorf("expected i18n.Error, got %T (%v)", err, err)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("cleanOrigins(%v) len = %d; want %d (%v)", tc.in, len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("got[%d] = %q; want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestCleanLine(t *testing.T) {
	t.Run("default values applied", func(t *testing.T) {
		w := Widget{}
		if err := cleanLine(&w); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if w.QueueMode != "all" {
			t.Errorf("QueueMode = %q; want 'all'", w.QueueMode)
		}
		if w.AnswerWait != 3 {
			t.Errorf("AnswerWait = %d; want 3", w.AnswerWait)
		}
		if w.MaxChatsType != "answer" {
			t.Errorf("MaxChatsType = %q; want 'answer'", w.MaxChatsType)
		}
		if w.NoanswerAfter != 5 {
			t.Errorf("NoanswerAfter = %d; want 5", w.NoanswerAfter)
		}
		if w.HoursFrom != "09:00" || w.HoursTo != "18:00" {
			t.Errorf("Hours = %s–%s; want 09:00–18:00", w.HoursFrom, w.HoursTo)
		}
		if w.HoursDays == nil || len(w.HoursDays) != 0 {
			t.Errorf("HoursDays = %v; want non-nil empty", w.HoursDays)
		}
	})

	t.Run("valid queue modes", func(t *testing.T) {
		for _, m := range []string{"all", "evenly", "strict"} {
			w := Widget{QueueMode: m}
			if err := cleanLine(&w); err != nil || w.QueueMode != m {
				t.Errorf("cleanLine(QueueMode=%q) = %v", m, err)
			}
		}
	})

	t.Run("invalid queue mode", func(t *testing.T) {
		w := Widget{QueueMode: "round_robin"}
		if err := cleanLine(&w); err == nil {
			t.Error("expected error for invalid QueueMode")
		}
	})

	t.Run("valid max chats types", func(t *testing.T) {
		for _, ct := range []string{"answer", "close"} {
			w := Widget{MaxChatsType: ct}
			if err := cleanLine(&w); err != nil || w.MaxChatsType != ct {
				t.Errorf("cleanLine(MaxChatsType=%q) = %v", ct, err)
			}
		}
	})

	t.Run("invalid max chats type", func(t *testing.T) {
		w := Widget{MaxChatsType: "never"}
		if err := cleanLine(&w); err == nil {
			t.Error("expected error for invalid MaxChatsType")
		}
	})

	t.Run("invalid day of week", func(t *testing.T) {
		for _, bad := range []int32{-1, 7, 10} {
			w := Widget{HoursDays: []int32{bad}}
			if err := cleanLine(&w); err == nil {
				t.Errorf("expected error for day %d", bad)
			}
		}
	})

	t.Run("hours on requires non-empty days", func(t *testing.T) {
		w := Widget{HoursOn: true, HoursDays: []int32{}}
		if err := cleanLine(&w); err == nil {
			t.Error("expected error when HoursOn=true but HoursDays is empty")
		}
	})

	t.Run("invalid hour formats", func(t *testing.T) {
		badHours := []struct{ from, to string }{
			{"9:00", "18:00"},
			{"09:00", "6:00"},
			{"24:00", "08:00"},
			{"12:60", "18:00"},
			{"ab:cd", "18:00"},
			{"10:00", "10:00"}, // from == to rejected
		}
		for _, bh := range badHours {
			w := Widget{HoursFrom: bh.from, HoursTo: bh.to}
			if err := cleanLine(&w); err == nil {
				t.Errorf("expected error for hours %s–%s", bh.from, bh.to)
			}
		}
	})

	t.Run("boundary checks for integer limits", func(t *testing.T) {
		type fieldCheck struct {
			name string
			mod  func(w *Widget)
			bad  bool
		}
		cases := []fieldCheck{
			{"AnswerWait min ok", func(w *Widget) { w.AnswerWait = 1 }, false},
			{"AnswerWait max ok", func(w *Widget) { w.AnswerWait = 1440 }, false},
			{"AnswerWait too high", func(w *Widget) { w.AnswerWait = 1441 }, true},
			{"AnswerWait negative", func(w *Widget) { w.AnswerWait = -1 }, true},

			{"NoanswerAfter min ok", func(w *Widget) { w.NoanswerAfter = 1 }, false},
			{"NoanswerAfter max ok", func(w *Widget) { w.NoanswerAfter = 1440 }, false},
			{"NoanswerAfter too high", func(w *Widget) { w.NoanswerAfter = 1441 }, true},
			{"NoanswerAfter negative", func(w *Widget) { w.NoanswerAfter = -1 }, true},

			{"Autoclose min ok", func(w *Widget) { w.Autoclose = 0 }, false},
			{"Autoclose max ok", func(w *Widget) { w.Autoclose = 720 }, false},
			{"Autoclose too high", func(w *Widget) { w.Autoclose = 721 }, true},
			{"Autoclose negative", func(w *Widget) { w.Autoclose = -1 }, true},

			{"KPIFirst min ok", func(w *Widget) { w.KPIFirst = 0 }, false},
			{"KPIFirst max ok", func(w *Widget) { w.KPIFirst = 1440 }, false},
			{"KPIFirst too high", func(w *Widget) { w.KPIFirst = 1441 }, true},
			{"KPIFirst negative", func(w *Widget) { w.KPIFirst = -1 }, true},

			{"KPIFurther min ok", func(w *Widget) { w.KPIFurther = 0 }, false},
			{"KPIFurther max ok", func(w *Widget) { w.KPIFurther = 1440 }, false},
			{"KPIFurther too high", func(w *Widget) { w.KPIFurther = 1441 }, true},
			{"KPIFurther negative", func(w *Widget) { w.KPIFurther = -1 }, true},

			{"MaxChats min ok", func(w *Widget) { w.MaxChats = 0 }, false},
			{"MaxChats max ok", func(w *Widget) { w.MaxChats = 1000 }, false},
			{"MaxChats too high", func(w *Widget) { w.MaxChats = 1001 }, true},
			{"MaxChats negative", func(w *Widget) { w.MaxChats = -1 }, true},
		}
		for _, tc := range cases {
			w := Widget{}
			tc.mod(&w)
			err := cleanLine(&w)
			if (err != nil) != tc.bad {
				t.Errorf("%s: err=%v; want bad=%v", tc.name, err, tc.bad)
			}
		}
	})

	t.Run("text length limits", func(t *testing.T) {
		long := strings.Repeat("a", 1001)
		fields := []func(w *Widget, s string){
			func(w *Widget, s string) { w.OfflineText = s },
			func(w *Widget, s string) { w.WelcomeText = s },
			func(w *Widget, s string) { w.NoanswerText = s },
			func(w *Widget, s string) { w.CloseText = s },
			func(w *Widget, s string) { w.VoteText = s },
		}
		for i, set := range fields {
			w := Widget{}
			set(&w, long)
			if err := cleanLine(&w); err == nil {
				t.Errorf("field %d: expected error for text > 1000 runes", i)
			}
		}
	})
}

func TestCleanShow(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		w := Widget{}
		if err := cleanShow(&w); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if w.Position != "bottom-right" {
			t.Errorf("Position = %q; want 'bottom-right'", w.Position)
		}
		if w.PagesMode != "all" {
			t.Errorf("PagesMode = %q; want 'all'", w.PagesMode)
		}
		if len(w.Pages) != 0 {
			t.Errorf("Pages = %v; want empty", w.Pages)
		}
	})

	t.Run("valid positions", func(t *testing.T) {
		for _, pos := range []string{"bottom-right", "bottom-left", "top-right", "top-left"} {
			w := Widget{Position: pos}
			if err := cleanShow(&w); err != nil || w.Position != pos {
				t.Errorf("cleanShow(Position=%q) = %v", pos, err)
			}
		}
	})

	t.Run("invalid position", func(t *testing.T) {
		w := Widget{Position: "center"}
		if err := cleanShow(&w); err == nil {
			t.Error("expected error for invalid position")
		}
	})

	t.Run("valid pages modes", func(t *testing.T) {
		for _, m := range []string{"all", "only", "except"} {
			w := Widget{PagesMode: m, Pages: []string{"https://site.ru/*"}}
			if err := cleanShow(&w); err != nil || w.PagesMode != m {
				t.Errorf("cleanShow(PagesMode=%q) = %v", m, err)
			}
		}
	})

	t.Run("invalid pages mode", func(t *testing.T) {
		w := Widget{PagesMode: "include"}
		if err := cleanShow(&w); err == nil {
			t.Error("expected error for invalid pages mode")
		}
	})

	t.Run("pages trimming and empty filtering", func(t *testing.T) {
		w := Widget{Pages: []string{"  https://a.ru  ", "", "   ", "https://b.ru"}}
		if err := cleanShow(&w); err != nil {
			t.Fatal(err)
		}
		if len(w.Pages) != 2 || w.Pages[0] != "https://a.ru" || w.Pages[1] != "https://b.ru" {
			t.Errorf("Pages = %v; want ['https://a.ru', 'https://b.ru']", w.Pages)
		}
	})

	t.Run("page length over 500 rejected", func(t *testing.T) {
		w := Widget{Pages: []string{strings.Repeat("a", 501)}}
		if err := cleanShow(&w); err == nil {
			t.Error("expected error for page item > 500 chars")
		}
	})

	t.Run("pages count over 100 rejected", func(t *testing.T) {
		list := make([]string, 101)
		for i := range list {
			list[i] = "https://site.ru/page"
		}
		w := Widget{Pages: list}
		if err := cleanShow(&w); err == nil {
			t.Error("expected error for > 100 pages")
		}
	})

	t.Run("empty pages rejected when mode is not all", func(t *testing.T) {
		for _, m := range []string{"only", "except"} {
			w := Widget{PagesMode: m, Pages: []string{"", "  "}}
			if err := cleanShow(&w); err == nil {
				t.Errorf("expected error for PagesMode=%q with no non-empty pages", m)
			}
		}
	})

	t.Run("hello text validation", func(t *testing.T) {
		w := Widget{HelloOn: true, HelloText: "  "}
		if err := cleanShow(&w); err == nil {
			t.Error("expected error when HelloOn=true and HelloText is blank")
		}

		w = Widget{HelloText: strings.Repeat("x", 1001)}
		if err := cleanShow(&w); err == nil {
			t.Error("expected error when HelloText > 1000 runes")
		}

		w = Widget{HelloOn: true, HelloText: "  Hello!  "}
		if err := cleanShow(&w); err != nil || w.HelloText != "Hello!" {
			t.Errorf("cleanShow with HelloText: %v, text=%q", err, w.HelloText)
		}
	})

	t.Run("delay bounds", func(t *testing.T) {
		for _, bad := range []int{-1, 601} {
			w := Widget{ShowDelay: bad}
			if err := cleanShow(&w); err == nil {
				t.Errorf("expected error for ShowDelay=%d", bad)
			}
			w = Widget{HelloDelay: bad}
			if err := cleanShow(&w); err == nil {
				t.Errorf("expected error for HelloDelay=%d", bad)
			}
		}
	})
}

func TestPageMatch(t *testing.T) {
	cases := []struct {
		mask, page string
		want       bool
	}{
		// exact without star
		{"https://example.com/about", "https://example.com/about", true},
		{"https://example.com/about", "HTTPS://EXAMPLE.COM/ABOUT", true},
		{"https://example.com/about", "https://example.com/about/", true},
		{"https://example.com/about/", "https://example.com/about", true},
		{"https://example.com/about", "https://example.com/about/team", false},
		{"https://example.com/about", "https://example.com/other", false},

		// star at end
		{"https://example.com/*", "https://example.com/page", true},
		{"https://example.com/*", "https://example.com/", true},
		{"https://example.com/*", "https://other.com/page", false},

		// star at start
		{"*?utm=1", "https://example.com/?utm=1", true},
		{"*?utm=1", "https://example.com/catalog?utm=1", true},
		{"*?utm=1", "https://example.com/?utm=2", false},

		// stars inside
		{"https://example.com/a*b*c", "https://example.com/a1b2c", true},
		{"https://example.com/a*b*c", "https://example.com/abc", true},
		{"https://example.com/a*b*c", "https://example.com/axb", false},
		{"https://example.com/a*b*c", "https://example.com/a1b2cd", false},

		// universal star
		{"*", "https://anything.com", true},
		{"*", "", true},
	}
	for _, tc := range cases {
		if got := pageMatch(tc.mask, tc.page); got != tc.want {
			t.Errorf("pageMatch(%q, %q) = %v; want %v", tc.mask, tc.page, got, tc.want)
		}
	}
}

func TestWidgetShowsOn(t *testing.T) {
	t.Run("all mode always shows", func(t *testing.T) {
		w := Widget{PagesMode: "all", Pages: []string{"https://site.ru/catalog"}}
		if !w.ShowsOn("https://site.ru/other") {
			t.Error("PagesMode='all' must show on any page")
		}
	})

	t.Run("empty mode defaults to all", func(t *testing.T) {
		w := Widget{PagesMode: ""}
		if !w.ShowsOn("https://site.ru/anywhere") {
			t.Error("empty PagesMode must show on any page")
		}
	})

	t.Run("only mode with hash stripping", func(t *testing.T) {
		w := Widget{
			PagesMode: "only",
			Pages:     []string{"https://site.ru/about"},
		}
		if !w.ShowsOn("https://site.ru/about#contacts") {
			t.Error("expected match with anchor stripped")
		}
		if w.ShowsOn("https://site.ru/news#top") {
			t.Error("must not show on unlisted page")
		}
	})

	t.Run("except mode", func(t *testing.T) {
		w := Widget{
			PagesMode: "except",
			Pages:     []string{"*/cart/*", "*/checkout"},
		}
		if w.ShowsOn("https://site.ru/cart/step1") {
			t.Error("must hide on /cart/*")
		}
		if w.ShowsOn("https://site.ru/checkout") {
			t.Error("must hide on /checkout")
		}
		if !w.ShowsOn("https://site.ru/catalog") {
			t.Error("must show on /catalog")
		}
	})
}

func TestNonNil(t *testing.T) {
	if got := nonNil(nil); got == nil || len(got) != 0 {
		t.Errorf("nonNil(nil) = %v; want non-nil empty", got)
	}
	src := []string{"a", "b"}
	if got := nonNil(src); len(got) != 2 || got[0] != "a" {
		t.Errorf("nonNil(%v) = %v", src, got)
	}
}

func TestHash(t *testing.T) {
	h1 := hash("token1")
	h2 := hash("token1")
	h3 := hash("token2")
	if len(h1) != 32 {
		t.Fatalf("hash length = %d; want 32", len(h1))
	}
	if string(h1) != string(h2) {
		t.Error("hash must be deterministic")
	}
	if string(h1) == string(h3) {
		t.Error("different tokens must produce different hashes")
	}
}

func TestMessageProperties(t *testing.T) {
	t.Run("Image", func(t *testing.T) {
		cases := []struct {
			mime string
			want bool
		}{
			{"image/png", true},
			{"image/jpeg", true},
			{"image/gif", true},
			{"image/webp", true},
			{"image/svg+xml", false},
			{"application/pdf", false},
			{"text/plain", false},
			{"", false},
		}
		for _, tc := range cases {
			if got := (Message{FileType: tc.mime}).Image(); got != tc.want {
				t.Errorf("Message{FileType: %q}.Image() = %v; want %v", tc.mime, got, tc.want)
			}
		}
	})

	t.Run("Text", func(t *testing.T) {
		cases := []struct {
			body, file, want string
		}{
			{"Hello", "", "Hello"},
			{"", "doc.pdf", "📎 doc.pdf"},
			{"Hello", "photo.jpg", "Hello\n📎 photo.jpg"},
			{"", "", ""},
		}
		for _, tc := range cases {
			m := Message{Body: tc.body, FileName: tc.file}
			if got := m.Text(); got != tc.want {
				t.Errorf("Message{%q, %q}.Text() = %q; want %q", tc.body, tc.file, got, tc.want)
			}
		}
	})

	t.Run("Internal", func(t *testing.T) {
		for _, k := range []string{"note", "hidden"} {
			if !(Message{Kind: k}).Internal() {
				t.Errorf("Kind=%q must be Internal", k)
			}
		}
		for _, k := range []string{"", "welcome", "offline", "bot", "form", "booking"} {
			if (Message{Kind: k}).Internal() {
				t.Errorf("Kind=%q must not be Internal", k)
			}
		}
	})

	t.Run("Auto", func(t *testing.T) {
		for _, k := range []string{"welcome", "offline", "noanswer", "close", "bot"} {
			if !(Message{Kind: k}).Auto() {
				t.Errorf("Kind=%q must be Auto", k)
			}
		}
		for _, k := range []string{"", "form", "booking", "note", "hidden"} {
			if (Message{Kind: k}).Auto() {
				t.Errorf("Kind=%q must not be Auto", k)
			}
		}
	})
}

func TestSessionTitle(t *testing.T) {
	cases := []struct {
		id, name, want string
	}{
		{"12345678-abcd", "Иван Иванов", "Иван Иванов"},
		{"abcdef123456", "", "#abcdef"},
		{"998877665544", "", "#998877"},
	}
	for _, tc := range cases {
		s := Session{ID: tc.id, Name: tc.name}
		if got := s.Title(); got != tc.want {
			t.Errorf("Session{ID: %q, Name: %q}.Title() = %q; want %q", tc.id, tc.name, got, tc.want)
		}
	}
}
