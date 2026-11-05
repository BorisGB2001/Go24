package openlines

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanBot(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		w := Widget{
			BotMenu: []BotItem{{Title: "Item 1", Action: "close"}},
		}
		if err := cleanBot(&w); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if w.BotOther != "menu" {
			t.Errorf("BotOther = %q; want 'menu'", w.BotOther)
		}
		if w.BotMenu == nil {
			t.Error("BotMenu must not be nil")
		}
	})

	t.Run("nil bot menu initialized", func(t *testing.T) {
		w := Widget{BotMenu: nil}
		if err := cleanBot(&w); err != nil {
			t.Fatal(err)
		}
		if w.BotMenu == nil || len(w.BotMenu) != 0 {
			t.Errorf("BotMenu = %v; want non-nil empty slice", w.BotMenu)
		}
	})

	t.Run("valid bot other values", func(t *testing.T) {
		for _, o := range []string{"menu", "queue"} {
			w := Widget{BotOther: o}
			if err := cleanBot(&w); err != nil || w.BotOther != o {
				t.Errorf("cleanBot(BotOther=%q) = %v", o, err)
			}
		}
	})

	t.Run("invalid bot other", func(t *testing.T) {
		w := Widget{BotOther: "exit"}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for invalid BotOther")
		}
	})

	t.Run("bot text trimming and length limit", func(t *testing.T) {
		w := Widget{BotText: "  Welcome to bot  "}
		if err := cleanBot(&w); err != nil || w.BotText != "Welcome to bot" {
			t.Errorf("BotText = %q; err = %v", w.BotText, err)
		}
		w = Widget{BotText: strings.Repeat("b", 1001)}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for BotText > 1000 runes")
		}
	})

	t.Run("bot enabled requires non-empty menu", func(t *testing.T) {
		w := Widget{BotOn: true, BotMenu: []BotItem{}}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error when BotOn=true but BotMenu is empty")
		}
	})

	t.Run("action text validation and items clearing", func(t *testing.T) {
		// Action text without text is rejected
		w := Widget{BotMenu: []BotItem{{Title: "F.A.Q.", Action: "text", Text: "  "}}}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for Action='text' with empty Text")
		}

		// Action text with text succeeds and clears sub-items
		w = Widget{BotMenu: []BotItem{{Title: "F.A.Q.", Action: "text", Text: "Answer", Items: []BotItem{{Title: "x"}}}}}
		if err := cleanBot(&w); err != nil {
			t.Fatal(err)
		}
		if w.BotMenu[0].Items != nil {
			t.Errorf("Action='text' must clear Items, got %v", w.BotMenu[0].Items)
		}
	})

	t.Run("action menu validation and nesting", func(t *testing.T) {
		// Action menu without items is rejected
		w := Widget{BotMenu: []BotItem{{Title: "Submenu", Action: "menu", Items: nil}}}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for Action='menu' with empty Items")
		}

		// Valid nested menu
		w = Widget{BotMenu: []BotItem{
			{
				Title:  "Level 1",
				Action: "menu",
				Items: []BotItem{
					{Title: "Level 2", Action: "close"},
				},
			},
		}}
		if err := cleanBot(&w); err != nil {
			t.Fatalf("unexpected error for valid nested menu: %v", err)
		}
	})

	t.Run("action queue and close clear items", func(t *testing.T) {
		for _, action := range []string{"queue", "close"} {
			w := Widget{BotMenu: []BotItem{
				{Title: "Option", Action: action, Items: []BotItem{{Title: "child"}}},
			}}
			if err := cleanBot(&w); err != nil {
				t.Fatalf("cleanBot action %q: %v", action, err)
			}
			if w.BotMenu[0].Items != nil {
				t.Errorf("action %q must clear Items", action)
			}
		}
	})

	t.Run("invalid action rejected", func(t *testing.T) {
		w := Widget{BotMenu: []BotItem{{Title: "Option", Action: "custom"}}}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for invalid action")
		}
	})

	t.Run("max depth exceeded", func(t *testing.T) {
		// botDepth is 4, so depth 5 must fail
		w := Widget{
			BotMenu: []BotItem{
				{
					Title: "L1", Action: "menu", Items: []BotItem{
						{
							Title: "L2", Action: "menu", Items: []BotItem{
								{
									Title: "L3", Action: "menu", Items: []BotItem{
										{
											Title: "L4", Action: "menu", Items: []BotItem{
												{Title: "L5", Action: "close"},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for menu depth > 4")
		}
	})

	t.Run("max items per level exceeded", func(t *testing.T) {
		// botItems is 20, 21 items must fail
		items := make([]BotItem, 21)
		for i := range items {
			items[i] = BotItem{Title: "Item", Action: "close"}
		}
		w := Widget{BotMenu: items}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for > 20 items in a menu")
		}
	})

	t.Run("item title validation", func(t *testing.T) {
		// Empty title
		w := Widget{BotMenu: []BotItem{{Title: "   ", Action: "close"}}}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for empty title")
		}
		// Title > 100 runes
		w = Widget{BotMenu: []BotItem{{Title: strings.Repeat("т", 101), Action: "close"}}}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for title > 100 runes")
		}
	})

	t.Run("item text limit", func(t *testing.T) {
		w := Widget{BotMenu: []BotItem{{Title: "A", Action: "text", Text: strings.Repeat("x", 1001)}}}
		if err := cleanBot(&w); err == nil {
			t.Error("expected error for text > 1000 runes")
		}
	})

	t.Run("json serialization roundtrip", func(t *testing.T) {
		menu := []BotItem{
			{Title: "Payment", Action: "text", Text: "Card or cash"},
			{Title: "Support", Action: "queue"},
			{
				Title:  "Services",
				Action: "menu",
				Items: []BotItem{
					{Title: "Delivery", Action: "text", Text: "1-2 days"},
					{Title: "Exit", Action: "close"},
				},
			},
		}
		data, err := json.Marshal(menu)
		if err != nil {
			t.Fatalf("json.Marshal failed: %v", err)
		}
		var decoded []BotItem
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		if len(decoded) != 3 || decoded[0].Title != "Payment" || decoded[2].Items[0].Title != "Delivery" {
			t.Errorf("decoded menu mismatch: %+v", decoded)
		}
	})
}

func TestWidgetBotNode(t *testing.T) {
	w := Widget{
		BotText: "Main greeting",
		BotMenu: []BotItem{
			{Title: "FAQ", Action: "text", Text: "Some answer"},
			{
				Title:  "Products",
				Action: "menu",
				Text:   "Choose category",
				Items: []BotItem{
					{Title: "Electronics", Action: "text", Text: "Phones, Laptops"},
					{
						Title:  "Clothing",
						Action: "menu",
						// no Text: heading must fall back to Title
						Items: []BotItem{
							{Title: "Shirts", Action: "close"},
						},
					},
				},
			},
			{Title: "Operator", Action: "queue"},
		},
	}

	t.Run("root menu path empty", func(t *testing.T) {
		head, items, ok := w.botNode("")
		if !ok || head != "Main greeting" || len(items) != 3 {
			t.Fatalf("root botNode: ok=%v, head=%q, items len=%d", ok, head, len(items))
		}
	})

	t.Run("first level submenu", func(t *testing.T) {
		head, items, ok := w.botNode("2")
		if !ok || head != "Choose category" || len(items) != 2 || items[0].Title != "Electronics" {
			t.Fatalf("botNode('2'): ok=%v, head=%q, items=%+v", ok, head, items)
		}
	})

	t.Run("second level submenu fallback heading", func(t *testing.T) {
		head, items, ok := w.botNode("2.2")
		if !ok || head != "Clothing" || len(items) != 1 || items[0].Title != "Shirts" {
			t.Fatalf("botNode('2.2'): ok=%v, head=%q, items=%+v", ok, head, items)
		}
	})

	t.Run("invalid paths", func(t *testing.T) {
		badPaths := []string{
			"0",      // < 1
			"4",      // out of range (len is 3)
			"1",      // item 1 is action="text", not "menu"
			"3",      // item 3 is action="queue", not "menu"
			"abc",    // non-numeric
			"2.0",    // submenu index 0
			"2.3",    // submenu index out of range
			"2.1.1",  // item 2.1 is action="text", not "menu"
			"2.2.1",  // item 2.2.1 is action="close", not "menu"
			"2.2.1.", // trailing dot
		}
		for _, p := range badPaths {
			_, _, ok := w.botNode(p)
			if ok {
				t.Errorf("botNode(%q) expected ok=false, got true", p)
			}
		}
	})
}

func TestStoreBotButtons(t *testing.T) {
	items := []BotItem{{Title: "Alpha"}, {Title: "Beta"}}

	t.Run("without translation function", func(t *testing.T) {
		s := &Store{}
		got := s.botButtons(items, false)
		if len(got) != 2 || got[0] != "1. Alpha" || got[1] != "2. Beta" {
			t.Errorf("botButtons(sub=false) = %v", got)
		}

		gotSub := s.botButtons(items, true)
		if len(gotSub) != 3 || gotSub[2] != "0. ol.bot_back" {
			t.Errorf("botButtons(sub=true) = %v", gotSub)
		}
	})

	t.Run("with custom translation function", func(t *testing.T) {
		s := &Store{
			Tr: func(key string, args ...any) string {
				if key == "ol.bot_back" {
					return "Назад"
				}
				return key
			},
		}
		got := s.botButtons(items, true)
		if len(got) != 3 || got[2] != "0. Назад" {
			t.Errorf("botButtons with Tr = %v; want '0. Назад'", got)
		}
	})

	t.Run("empty items list", func(t *testing.T) {
		s := &Store{}
		if got := s.botButtons(nil, false); len(got) != 0 {
			t.Errorf("empty items sub=false: %v", got)
		}
		if got := s.botButtons(nil, true); len(got) != 1 || got[0] != "0. ol.bot_back" {
			t.Errorf("empty items sub=true: %v", got)
		}
	})
}

func TestBotPickComprehensive(t *testing.T) {
	items := []BotItem{
		{Title: "Купить"},
		{Title: "Поддержка"},
		{Title: "О компании"},
	}

	cases := []struct {
		input string
		want  int
	}{
		// numeric picks
		{"1", 1},
		{" 1 ", 1},
		{"1.", 1},
		{"1)", 1},
		{"1. Купить", 1},
		{"1) Купить", 1},
		{"2", 2},
		{"2. Поддержка", 2},
		{"3", 3},
		{"3. О компании", 3},

		// back button
		{"0", 0},
		{" 0 ", 0},
		{"0.", 0},
		{"0)", 0},
		{"0. Назад", 0},
		{"0) Назад", 0},

		// number out of bounds
		{"4", -1},
		{"99", -1},

		// title matches (case-insensitive and trimmed)
		{"купить", 1},
		{"КУПИТЬ", 1},
		{"  поддержка  ", 2},
		{"О КОМПАНИИ", 3},

		// non-matching
		{"привет", -1},
		{"", -1},
		{"   ", -1},
		{"купи", -1},
	}
	for _, tc := range cases {
		if got := botPick(items, tc.input); got != tc.want {
			t.Errorf("botPick(%q) = %d; want %d", tc.input, got, tc.want)
		}
	}
}
