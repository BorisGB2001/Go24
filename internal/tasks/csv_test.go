package tasks

import "testing"

func TestParseImportDate(t *testing.T) {
	for in, want := range map[string]string{
		"31.12.2026 18:30": "2026-12-31 18:30", "31.12.2026": "2026-12-31 19:00", "2026-12-31": "2026-12-31 19:00",
		"2026-12-31T09:15": "2026-12-31 09:15", "1.2.2027 10:00": "2027-02-01 10:00", "завтра": "",
	} {
		got, err := ParseImportDate(in, 19, 0)
		s := ""
		if err == nil {
			s = got.Format("2006-01-02 15:04")
		}
		if s != want {
			t.Errorf("%q → %q, want %q", in, s, want)
		}
	}
	if got := splitList(" Анна Ли, ;Пётр С "); len(got) != 2 || got[1] != "Пётр С" {
		t.Errorf("splitList = %q", got)
	}
	if nameKey("  Пётр   Семёнов ") != "петр семенов" {
		t.Error("nameKey")
	}
}
