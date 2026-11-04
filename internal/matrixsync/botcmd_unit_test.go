package matrixsync

import (
	"encoding/json"
	"strings"
	"testing"

	"go24/internal/matrix"
)

// TestYNUnmarshalJSON checks boolean, string, and number representations YN flags.
func TestYNUnmarshalJSON(t *testing.T) {
	tests := []struct {
		input   string
		want    YN
		wantErr bool
	}{
		{input: `true`, want: true},
		{input: `false`, want: false},
		{input: `"Y"`, want: true},
		{input: `"y"`, want: true},
		{input: `"1"`, want: true},
		{input: `"true"`, want: true},
		{input: `"TRUE"`, want: true},
		{input: `"N"`, want: false},
		{input: `"n"`, want: false},
		{input: `"0"`, want: false},
		{input: `"false"`, want: false},
		{input: `"random"`, want: false},
		{input: `1`, want: true},
		{input: `42.5`, want: true},
		{input: `0`, want: false},
		{input: `0.0`, want: false},
		{input: `invalid`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var got YN
			err := json.Unmarshal([]byte(tt.input), &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalJSON(%s) err = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("UnmarshalJSON(%s) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestCommandText checks language fallback in Command.Text().
func TestCommandText(t *testing.T) {
	c := Command{
		Lang: map[string]CommandText{
			"ru": {Title: "Справка", Params: "[команда]"},
			"en": {Title: "Help", Params: "[command]"},
			"de": {Title: "Hilfe", Params: "[befehl]"},
		},
	}

	// Exact prefix match: ru-RU -> ru
	if got := c.Text("ru-RU"); got.Title != "Справка" || got.Params != "[команда]" {
		t.Errorf("c.Text(ru-RU) = %+v, want ru", got)
	}

	// Case insensitive prefix: RU -> ru
	if got := c.Text("RU"); got.Title != "Справка" {
		t.Errorf("c.Text(RU) = %+v, want ru", got)
	}

	// Fallback to en when requested lang is missing
	if got := c.Text("fr"); got.Title != "Help" {
		t.Errorf("c.Text(fr) = %+v, want en fallback", got)
	}

	// Fallback to first alphabetically sorted key when neither requested nor en exists
	cNoEn := Command{
		Lang: map[string]CommandText{
			"fr": {Title: "Aide"},
			"de": {Title: "Hilfe"},
		},
	}
	if got := cNoEn.Text("es"); got.Title != "Hilfe" { // "de" comes before "fr"
		t.Errorf("cNoEn.Text(es) = %+v, want de (first alphabetical)", got)
	}

	// Empty Lang map
	cEmpty := Command{}
	if got := cEmpty.Text("en"); got != (CommandText{}) {
		t.Errorf("cEmpty.Text(en) = %+v, want empty", got)
	}
}

// TestNormCommand verifies validation and normalization of bot command names.
func TestNormCommand(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{input: "/help", want: "help"},
		{input: "help", want: "help"},
		{input: "  /ping  ", want: "ping"},
		{input: "/команда_123", want: "команда_123"},
		{input: "/my-bot.cmd_1", want: "my-bot.cmd_1"},
		{input: "", wantErr: true},
		{input: "/", wantErr: true},
		{input: "   ", wantErr: true},
		{input: "/bad name", wantErr: true},
		{input: "/cmd!", wantErr: true},
		{input: "/cmd@bot", wantErr: true},
		{input: "/" + strings.Repeat("a", 50), want: strings.Repeat("a", 50)},
		{input: "/" + strings.Repeat("a", 51), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := normCommand(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("normCommand(%q) err = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("normCommand(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestCommandOf checks parsing of bot commands and parameters from message text.
func TestCommandOf(t *testing.T) {
	tests := []struct {
		body       string
		wantName   string
		wantParams string
		wantOK     bool
	}{
		{body: "/help", wantName: "help", wantParams: "", wantOK: true},
		{body: "/echo hello world", wantName: "echo", wantParams: "hello world", wantOK: true},
		{body: "  /calc  1 + 2  ", wantName: "calc", wantParams: "1 + 2", wantOK: true},
		{body: "/task\nNew task title", wantName: "task", wantParams: "New task title", wantOK: true},
		{body: "/cmd\nline1 line2", wantName: "cmd", wantParams: "line1 line2", wantOK: true},
		{body: "regular message", wantOK: false},
		{body: "", wantOK: false},
		{body: "   ", wantOK: false},
		{body: "//", wantOK: false},
		{body: "/ badcmd", wantOK: false},
		{body: "/bad!name arg", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			name, params, ok := commandOf(tt.body)
			if ok != tt.wantOK {
				t.Fatalf("commandOf(%q) ok = %v, want %v", tt.body, ok, tt.wantOK)
			}
			if tt.wantOK {
				if name != tt.wantName || params != tt.wantParams {
					t.Errorf("commandOf(%q) = (%q, %q), want (%q, %q)", tt.body, name, params, tt.wantName, tt.wantParams)
				}
			}
		})
	}
}

// TestNormKeyboard tests validation and normalization of keyboard buttons.
func TestNormKeyboard(t *testing.T) {
	// Exceeding 100 buttons
	many := make([]Button, 101)
	for i := range many {
		many[i] = Button{Text: "btn"}
	}
	if _, err := NormKeyboard(many); err == nil {
		t.Error("NormKeyboard with 101 buttons expected error")
	}

	// Normal valid keyboard
	kb := []Button{
		{Text: "  Open Link  ", Link: "https://example.com/page", Display: "block", Action: "send"},
		{Type: "NEWLINE"},
		{Text: "Run", Command: "/run", BgColor: "#abcdef", TextColor: "#12345678", Display: "line"},
		{Text: "Call", Action: "call", BgColor: "invalid-color"},
	}
	got, err := NormKeyboard(kb)
	if err != nil {
		t.Fatalf("NormKeyboard valid: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4", len(got))
	}

	// First button
	if got[0].Text != "Open Link" || got[0].Display != "BLOCK" || got[0].Action != "SEND" {
		t.Errorf("got[0] = %+v", got[0])
	}
	// Second button: NEWLINE
	if got[1].Type != "NEWLINE" {
		t.Errorf("got[1] = %+v, want NEWLINE", got[1])
	}
	// Third button: Command prefix stripped, color preserved
	if got[2].Command != "run" || got[2].BgColor != "#abcdef" || got[2].TextColor != "#12345678" {
		t.Errorf("got[2] = %+v", got[2])
	}
	// Fourth button: invalid color cleared, display defaulted to LINE
	if got[3].BgColor != "" || got[3].Display != "LINE" {
		t.Errorf("got[3] = %+v", got[3])
	}

	// Validation failures
	badCases := []struct {
		name string
		kb   []Button
	}{
		{name: "empty text", kb: []Button{{Text: "  "}}},
		{name: "text > 100 runes", kb: []Button{{Text: strings.Repeat("ё", 101)}}},
		{name: "invalid link scheme", kb: []Button{{Text: "x", Link: "javascript:void(0)"}}},
		{name: "link without host", kb: []Button{{Text: "x", Link: "https://"}}},
		{name: "invalid command name", kb: []Button{{Text: "x", Command: "/bad command"}}},
		{name: "invalid action", kb: []Button{{Text: "x", Action: "UNKNOWN"}}},
	}
	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			if _, err := NormKeyboard(bc.kb); err == nil {
				t.Errorf("NormKeyboard(%s) expected error, got nil", bc.name)
			}
		})
	}
}

// TestKeyboardRows tests splitting keyboard buttons into rows by NEWLINE and DISPLAY BLOCK.
func TestKeyboardRows(t *testing.T) {
	b1 := Button{Text: "B1", Display: "LINE"}
	b2 := Button{Text: "B2", Display: "LINE"}
	nl := Button{Type: "NEWLINE"}
	block := Button{Text: "Block", Display: "BLOCK"}
	b3 := Button{Text: "B3", Display: "LINE"}

	input := []Button{b1, b2, nl, block, b3}
	rows := keyboardRows(input)

	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	// Row 0: [b1, b2]
	if len(rows[0]) != 2 || rows[0][0].Text != "B1" || rows[0][1].Text != "B2" {
		t.Errorf("rows[0] = %+v", rows[0])
	}
	// Row 1: [block]
	if len(rows[1]) != 1 || rows[1][0].Text != "Block" {
		t.Errorf("rows[1] = %+v", rows[1])
	}
	// Row 2: [b3]
	if len(rows[2]) != 1 || rows[2][0].Text != "B3" {
		t.Errorf("rows[2] = %+v", rows[2])
	}

	// Empty input
	if got := keyboardRows(nil); len(got) != 0 {
		t.Errorf("keyboardRows(nil) = %+v, want empty", got)
	}

	// Multiple consecutive NEWLINEs
	if got := keyboardRows([]Button{nl, nl}); len(got) != 0 {
		t.Errorf("keyboardRows(multiple newlines) = %+v, want empty", got)
	}
}

// TestKeyboardOf tests extracting keyboard buttons from message content map.
func TestKeyboardOf(t *testing.T) {
	// Missing
	if got := keyboardOf(map[string]any{}); got != nil {
		t.Errorf("keyboardOf(empty) = %+v, want nil", got)
	}

	// Invalid json structure
	if got := keyboardOf(map[string]any{"go24.keyboard": "invalid"}); got != nil {
		t.Errorf("keyboardOf(invalid) = %+v, want nil", got)
	}

	// Valid
	validContent := map[string]any{
		"go24.keyboard": []any{
			map[string]any{"text": "Btn1", "action": "PUT"},
		},
	}
	got := keyboardOf(validContent)
	if len(got) != 1 || got[0].Text != "Btn1" || got[0].Action != "PUT" {
		t.Fatalf("keyboardOf(valid) = %+v", got)
	}
}

// TestLastContent verifies extracting replaced content from matrix.Event.
func TestLastContent(t *testing.T) {
	// Original content when not replaced
	evOriginal := matrix.Event{
		Content: map[string]any{"body": "original"},
	}
	if got := lastContent(evOriginal); got["body"] != "original" {
		t.Errorf("lastContent(original) = %+v", got)
	}

	// Replaced content when unsigned relation exists
	evReplaced := matrix.Event{
		Content: map[string]any{"body": "old"},
	}
	evReplaced.Unsigned.Relations.Replace = &matrix.Event{
		Content: map[string]any{
			"m.new_content": map[string]any{"body": "new text"},
		},
	}
	if got := lastContent(evReplaced); got["body"] != "new text" {
		t.Errorf("lastContent(replaced) = %+v, want 'new text'", got)
	}
}

// TestBotParams verifies parameter formatting for bot events in group vs private chats.
func TestBotParams(t *testing.T) {
	bot := Bot{ID: "bot123"}
	from := imPerson{ID: "user456"}

	// Group chat (private == false)
	gp := botParams(bot, "!room:example.com", false, from, "$ev1", "test body")
	if gp["DIALOG_ID"] != "!room:example.com" || gp["CHAT_TYPE"] != "C" || gp["MESSAGE_TYPE"] != "C" {
		t.Errorf("group DIALOG_ID/CHAT_TYPE mismatch: %+v", gp)
	}
	if gp["TO_CHAT_ID"] != "!room:example.com" || gp["FROM_USER_ID"] != "user456" {
		t.Errorf("group TO_CHAT_ID/FROM_USER_ID mismatch: %+v", gp)
	}

	// Private chat (private == true)
	pp := botParams(bot, "!room:example.com", true, from, "$ev2", "private msg")
	if pp["DIALOG_ID"] != "user456" || pp["CHAT_TYPE"] != "P" || pp["MESSAGE_TYPE"] != "P" {
		t.Errorf("private DIALOG_ID/CHAT_TYPE mismatch: %+v", pp)
	}
	if pp["TO_USER_ID"] != "bot123" {
		t.Errorf("private TO_USER_ID mismatch: %+v", pp)
	}
	if _, has := pp["TO_CHAT_ID"]; has {
		t.Errorf("private chat should not have TO_CHAT_ID: %+v", pp)
	}
}
