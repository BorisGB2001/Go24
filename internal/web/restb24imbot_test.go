package web

import (
	"testing"
)

// TestPortalBotParams: the bot and its message as the REST gives them — PROPERTIES, EVENT_HANDLER for all
// three handlers, KEYBOARD as a list of a PHP form, as {BUTTONS: […]} or as JSON text, "N" taking the buttons away,
// ATTACH, [BR]; LANG of a command.
func TestPortalBotParams(t *testing.T) {
	b := portalBotFields(map[string]any{"code": "helper", "TYPE": "h", "EVENT_HANDLER": "https://h.example/all",
		"EVENT_WELCOME_MESSAGE": "https://h.example/hi", "PROPERTIES": map[string]any{"NAME": "Помощник", "WORK_POSITION": "Бот"}})
	if b.Code != "helper" || b.Type != "h" || b.Name != "Помощник" || b.WorkPosition != "Бот" || b.MessageHandler != "https://h.example/all" ||
		b.WelcomeHandler != "https://h.example/hi" || b.DeleteHandler != "https://h.example/all" {
		t.Fatalf("the bot %+v", b)
	}

	for name, kb := range map[string]any{
		"a list":     []any{map[string]any{"TEXT": "Да", "COMMAND": "yes"}, map[string]any{"TYPE": "NEWLINE"}, map[string]any{"TEXT": "Нет", "BLOCK": "Y", "COMMAND": "no"}},
		"BUTTONS":    map[string]any{"BUTTONS": []any{map[string]any{"TEXT": "Да", "COMMAND": "yes"}, map[string]any{"TYPE": "NEWLINE"}, map[string]any{"TEXT": "Нет", "BLOCK": "Y", "COMMAND": "no"}}},
		"JSON text":  `[{"TEXT":"Да","COMMAND":"yes"},{"TYPE":"NEWLINE"},{"TEXT":"Нет","BLOCK":"Y","COMMAND":"no"}]`,
		"N":          "N",
		"none given": nil,
	} {
		m := portalBotMsg(map[string]any{"MESSAGE": "Привет[BR]мир", "KEYBOARD": kb})
		p, err := m.parts()
		if err != nil || m.Message != "Привет\nмир" {
			t.Fatalf("%s: %+v %v", name, m, err)
		}
		switch name {
		case "N":
			if p.Keyboard == nil || len(p.Keyboard) != 0 {
				t.Errorf("%s: %+v", name, p.Keyboard)
			}
		case "none given":
			if p.Keyboard != nil {
				t.Errorf("%s: %+v", name, p.Keyboard)
			}
		default:
			if len(p.Keyboard) != 3 || p.Keyboard[0].Command != "yes" || !bool(p.Keyboard[2].Block) {
				t.Errorf("%s: %+v", name, p.Keyboard)
			}
		}
	}
	m := portalBotMsg(map[string]any{"ATTACH": map[string]any{"COLOR": "#29619b", "BLOCKS": []any{map[string]any{"MESSAGE": "Заказ 42"}}}})
	if p, err := m.parts(); err != nil || p.Attach == nil || len(p.Attach.Blocks) != 1 {
		t.Fatalf("ATTACH: %+v %v", p.Attach, err)
	}

	lang := portalCommandLang(map[string]any{"LANG": []any{map[string]any{"LANGUAGE_ID": "ru", "TITLE": "Погода", "PARAMS": "город"},
		map[string]any{"LANGUAGE_ID": "en", "TITLE": "Weather"}}})
	if lang["ru"].Title != "Погода" || lang["ru"].Params != "город" || lang["en"].Title != "Weather" {
		t.Fatalf("LANG %+v", lang)
	}
	if portalCommandLang(map[string]any{}) != nil {
		t.Fatal("no LANG changes the titles")
	}
	if y := portalFlagOf(map[string]any{"HIDDEN": "Y"}, "hidden"); y == nil || !*y {
		t.Fatal("HIDDEN=Y")
	}
	if portalFlagOf(map[string]any{}, "COMMON") != nil {
		t.Fatal("COMMON not given")
	}
}
