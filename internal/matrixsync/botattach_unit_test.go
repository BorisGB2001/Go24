package matrixsync

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestNormMenu tests validation and normalization of bot menu items.
func TestNormMenu(t *testing.T) {
	// Limit of 30 items
	many := make([]Button, 31)
	for i := range many {
		many[i] = Button{Text: "Item"}
	}
	if _, err := NormMenu(many); err == nil {
		t.Error("NormMenu with 31 items expected error")
	}

	// NEWLINE items filtered out, styles stripped (BgColor, TextColor, Display, Block)
	input := []Button{
		{Text: "Action 1", BgColor: "#ffffff", TextColor: "#000000", Display: "BLOCK", Block: true, Action: "PUT"},
		{Type: "NEWLINE"},
		{Text: "Action 2", Command: "/help", Action: "SEND"},
	}
	got, err := NormMenu(input)
	if err != nil {
		t.Fatalf("NormMenu valid: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	// Action 1: NEWLINE skipped, formatting stripped
	if got[0].Text != "Action 1" || got[0].BgColor != "" || got[0].TextColor != "" || got[0].Display != "" || got[0].Block != false {
		t.Errorf("got[0] styles not cleared: %+v", got[0])
	}
	if got[0].Action != "PUT" {
		t.Errorf("got[0].Action = %q, want PUT", got[0].Action)
	}
	// Action 2
	if got[1].Text != "Action 2" || got[1].Command != "help" || got[1].Action != "SEND" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

// TestAttachOfAndMenuOf tests extracting attach cards and menus from message content maps.
func TestAttachOfAndMenuOf(t *testing.T) {
	// Missing attach / menu
	if got := attachOf(map[string]any{}); got != nil {
		t.Errorf("attachOf(empty) = %+v, want nil", got)
	}
	if got := menuOf(map[string]any{}); got != nil {
		t.Errorf("menuOf(empty) = %+v, want nil", got)
	}

	// Malformed JSON structures
	if got := attachOf(map[string]any{"go24.attach": "not a map"}); got != nil {
		t.Errorf("attachOf(invalid) = %+v, want nil", got)
	}
	if got := menuOf(map[string]any{"go24.menu": "not an array"}); got != nil {
		t.Errorf("menuOf(invalid) = %+v, want nil", got)
	}

	// Empty attach blocks
	if got := attachOf(map[string]any{"go24.attach": map[string]any{"blocks": []any{}}}); got != nil {
		t.Errorf("attachOf(empty blocks) = %+v, want nil", got)
	}

	// Valid attach and menu
	content := map[string]any{
		"go24.attach": map[string]any{
			"color": "#123456",
			"blocks": []any{
				map[string]any{"kind": "message", "text": "hello card"},
			},
		},
		"go24.menu": []any{
			map[string]any{"text": "Menu 1", "action": "PUT"},
		},
	}

	att := attachOf(content)
	if att == nil || len(att.Blocks) != 1 || att.Blocks[0].Text != "hello card" || att.Color != "#123456" {
		t.Errorf("attachOf(valid) = %+v", att)
	}

	menu := menuOf(content)
	if len(menu) != 1 || menu[0].Text != "Menu 1" || menu[0].Action != "PUT" {
		t.Errorf("menuOf(valid) = %+v", menu)
	}
}

// TestBotPartsPut verifies merging and populating BotParts into message content.
func TestBotPartsPut(t *testing.T) {
	oldContent := map[string]any{
		"go24.keyboard": []any{map[string]any{"text": "Old KB"}},
		"go24.menu":     []any{map[string]any{"text": "Old Menu"}},
		"go24.attach": map[string]any{
			"blocks": []any{map[string]any{"kind": "message", "text": "Old Card"}},
		},
	}

	// Scenario 1: BotParts are nil -> inherited from oldContent
	pNil := BotParts{}
	target1 := map[string]any{}
	pNil.put(target1, oldContent)

	if _, ok := target1["go24.keyboard"]; !ok {
		t.Error("target1 missing inherited go24.keyboard")
	}
	if _, ok := target1["go24.menu"]; !ok {
		t.Error("target1 missing inherited go24.menu")
	}
	if _, ok := target1["go24.attach"]; !ok {
		t.Error("target1 missing inherited go24.attach")
	}

	// Scenario 2: BotParts explicitly provided -> overrides oldContent
	pExplicit := BotParts{
		Keyboard: []Button{{Text: "New KB"}},
		Menu:     []Button{{Text: "New Menu"}},
		Attach:   &Attach{Blocks: []AttachBlock{{Kind: "message", Text: "New Card"}}},
	}
	target2 := map[string]any{}
	pExplicit.put(target2, oldContent)

	kb, ok := target2["go24.keyboard"].([]Button)
	if !ok || len(kb) != 1 || kb[0].Text != "New KB" {
		t.Errorf("target2 keyboard = %+v", target2["go24.keyboard"])
	}
	menu, ok := target2["go24.menu"].([]Button)
	if !ok || len(menu) != 1 || menu[0].Text != "New Menu" {
		t.Errorf("target2 menu = %+v", target2["go24.menu"])
	}
	att, ok := target2["go24.attach"].(*Attach)
	if !ok || len(att.Blocks) != 1 || att.Blocks[0].Text != "New Card" {
		t.Errorf("target2 attach = %+v", target2["go24.attach"])
	}
}

// TestAttachHelpers tests attachText formatting, only flag, and ParseAttach edge cases.
func TestAttachHelpers(t *testing.T) {
	// a.only() helper
	var aNil *Attach
	if aNil.only() {
		t.Error("aNil.only() should be false")
	}
	aFalse := &Attach{Only: false}
	if aFalse.only() {
		t.Error("aFalse.only() should be false")
	}
	aTrue := &Attach{Only: true}
	if !aTrue.only() {
		t.Error("aTrue.only() should be true")
	}

	// attachText formatting across different block kinds
	att := &Attach{
		Blocks: []AttachBlock{
			{Kind: "message", Text: "Message line"},
			{Kind: "link", Name: "Website", Link: "https://example.com"},
			{Kind: "user", Name: "Alice", Link: "https://example.com/u/alice"},
			{Kind: "image", Items: []AttachItem{{Link: "https://example.com/pic.png"}}},
			{Kind: "file", Items: []AttachItem{{Name: "Doc.pdf", Link: "https://example.com/doc.pdf"}}},
			{Kind: "grid", Items: []AttachItem{{Name: "Key", Value: "Value", Link: "https://example.com/item"}}},
			{Kind: "unknown", Text: "ignored"},
		},
	}
	gotText := attachText(att)
	wantParts := []string{
		"Message line",
		"Website https://example.com",
		"Alice https://example.com/u/alice",
		"https://example.com/pic.png",
		"Doc.pdf https://example.com/doc.pdf",
		"Key Value https://example.com/item",
	}
	wantText := strings.Join(wantParts, "\n")
	if gotText != wantText {
		t.Errorf("attachText =\n%q\nwant:\n%q", gotText, wantText)
	}

	// ParseAttach: null or empty
	if a, err := ParseAttach(json.RawMessage(`null`)); err != nil || a != nil {
		t.Errorf("ParseAttach(null) = (%+v, %v), want (nil, nil)", a, err)
	}
	if a, err := ParseAttach(json.RawMessage(``)); err != nil || a != nil {
		t.Errorf("ParseAttach(empty) = (%+v, %v), want (nil, nil)", a, err)
	}

	// ParseAttach: delimiter with size bounds and color
	aDelim, err := ParseAttach(json.RawMessage(`[{"DELIMITER":{"size": 5000, "color": "#123456"}}]`))
	if err != nil {
		t.Fatalf("ParseAttach delimiter: %v", err)
	}
	if len(aDelim.Blocks) != 1 || aDelim.Blocks[0].Size != 2000 || aDelim.Blocks[0].Color != "#123456" {
		t.Errorf("aDelim.Blocks[0] = %+v, want size clamped to 2000", aDelim.Blocks[0])
	}

	// ParseAttach: >50 blocks error
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 51; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"MESSAGE":"text"}`)
	}
	sb.WriteString("]")
	if _, err := ParseAttach(json.RawMessage(sb.String())); err == nil {
		t.Error("ParseAttach > 50 blocks expected error")
	}

	// ParseAttach: grid with display LINE, ROW, BLOCK
	aGrid, err := ParseAttach(json.RawMessage(`[{"GRID":[{"NAME":"A","VALUE":"1","DISPLAY":"line"},{"NAME":"B","VALUE":"2","DISPLAY":"row"},{"NAME":"C","VALUE":"3","DISPLAY":"other"}]}]`))
	if err != nil {
		t.Fatalf("ParseAttach grid: %v", err)
	}
	if len(aGrid.Blocks) != 1 || len(aGrid.Blocks[0].Items) != 3 {
		t.Fatalf("aGrid items: %+v", aGrid.Blocks)
	}
	if aGrid.Blocks[0].Items[0].Display != "LINE" {
		t.Errorf("item 0 display = %q, want LINE", aGrid.Blocks[0].Items[0].Display)
	}
	if aGrid.Blocks[0].Items[1].Display != "ROW" {
		t.Errorf("item 1 display = %q, want ROW", aGrid.Blocks[0].Items[1].Display)
	}
	if aGrid.Blocks[0].Items[2].Display != "BLOCK" {
		t.Errorf("item 2 display = %q, want BLOCK", aGrid.Blocks[0].Items[2].Display)
	}
}
