package telephony

import (
	"errors"
	"testing"

	"go24/internal/i18n"
)

func TestParseSalesScript(t *testing.T) {
	blocks, err := ParseSalesScript("\n# Привет\r\nЗдравствуйте!\n\nКак дела?\n- Хорошо -> пока\n- Плохо\n#Помощь\nЧем помочь?\n- Ничем → Пока\n# Пока\nДо свидания.\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 || blocks[0].Title != "Привет" || blocks[0].Text != "Здравствуйте!\n\nКак дела?" || blocks[1].Title != "Помощь" {
		t.Fatalf("blocks: %+v", blocks)
	}
	if a := blocks[0].Answers; len(a) != 2 || a[0] != (ScriptAnswer{"Хорошо", 2}) || a[1] != (ScriptAnswer{"Плохо", 1}) {
		t.Fatalf("answers: %+v", a)
	}
	if a := blocks[1].Answers; len(a) != 1 || a[0] != (ScriptAnswer{"Ничем", 2}) || len(blocks[2].Answers) != 0 {
		t.Fatalf("answers of the 2nd and 3rd: %+v %+v", a, blocks[2].Answers)
	}
	if b, err := ParseSalesScript("# Один\nТекст\n- Дальше"); err != nil || b[0].Answers[0].Next != -1 {
		t.Fatalf("an answer of the last block without the arrow: %+v %v", b, err)
	}
	for body, key := range map[string]string{
		"":                        "err.salescript_empty",
		"Текст\n# А":              "err.salescript_start",
		"# А\n# а":                "err.salescript_twice",
		"#\nТекст":                "err.salescript_title",
		"# А\n- -> А":             "err.salescript_answer",
		"# А\n- Да -> Нет такого": "err.salescript_target",
	} {
		_, err := ParseSalesScript(body)
		var e *i18n.Error
		if !errors.As(err, &e) || e.Key != key {
			t.Errorf("%q: %v, want %s", body, err, key)
		}
	}
}
