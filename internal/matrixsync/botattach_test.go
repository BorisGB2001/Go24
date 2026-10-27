package matrixsync

import "testing"

// TestParseAttach: ATTACH has it — an array of blocks or {"COLOR", "BLOCKS"}, keys in any case; a script
// link or a picture without an address is not taken, "N" is an empty card.
func TestParseAttach(t *testing.T) {
	a, err := ParseAttach([]byte(`[{"MESSAGE":"Заказ[BR]готов"},{"link":{"NAME":"Заказ 42","DESC":"Оплачен","LINK":"https://shop.example/42","PREVIEW":"https://shop.example/42.png"}},
		{"IMAGE":{"LINK":"https://shop.example/1.jpg"}},{"FILE":[{"NAME":"Счёт.pdf","LINK":"https://shop.example/1.pdf","SIZE":"1024"}]},
		{"DELIMITER":{"SIZE":200,"COLOR":"#c8c8c8"}},{"GRID":[{"NAME":"Сумма","VALUE":"1 000 ₽","DISPLAY":"column"},{"NAME":"Срок","VALUE":"завтра","DISPLAY":"LINE","COLOR":"red"}]},
		{"USER":{"NAME":"Анна","AVATAR":"https://shop.example/a.png","LINK":"https://shop.example/u/1"}},{"HTML":"<b>x</b>"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Blocks) != 7 {
		t.Fatalf("blocks %+v", a.Blocks)
	}
	b := a.Blocks
	if b[0].Text != "Заказ\nготов" || b[1].Name != "Заказ 42" || b[1].Text != "Оплачен" || b[2].Items[0].Preview != "https://shop.example/1.jpg" ||
		b[3].Items[0].Size != 1024 || b[4].Size != 200 || b[5].Items[0].Display != "ROW" || b[5].Items[1].Color != "" ||
		b[6].Preview != "https://shop.example/a.png" {
		t.Fatalf("blocks %+v", b)
	}
	if got := attachText(a); got != "Заказ\nготов\nЗаказ 42 https://shop.example/42\nhttps://shop.example/1.jpg\nСчёт.pdf https://shop.example/1.pdf\nСумма 1 000 ₽\nСрок завтра\nАнна https://shop.example/u/1" {
		t.Errorf("attachText = %q", got)
	}
	for _, bad := range []string{`[{"LINK":{"NAME":"x","LINK":"javascript:alert(1)"}}]`, `[{"IMAGE":[{"NAME":"x"}]}]`, `[{"MESSAGE":""}]`,
		`[{"USER":{"NAME":"x","AVATAR":"data:image/png;base64,AAAA"}}]`, `[1]`, `{`} {
		if _, err := ParseAttach([]byte(bad)); err == nil {
			t.Errorf("%s is taken", bad)
		}
	}
	if a, err := ParseAttach([]byte(`"N"`)); err != nil || a == nil || len(a.Blocks) != 0 {
		t.Errorf(`"N" = %+v %v`, a, err)
	}
	if a, err := ParseAttach(nil); err != nil || a != nil {
		t.Errorf("none = %+v %v", a, err)
	}
}
