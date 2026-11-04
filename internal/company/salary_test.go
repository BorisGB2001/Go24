package company

import (
	"testing"
	"time"
)

func TestVacationSplit(t *testing.T) {
	d := func(m time.Month, day int) time.Time { return time.Date(2026, m, day, 0, 0, 0, 0, time.Local) }
	first, last := d(1, 1), d(12, 31)
	for _, c := range []struct {
		name            string
		from, to, today time.Time
		taken, planned  int
	}{
		{"past", d(3, 1), d(3, 14), d(10, 28), 14, 0},
		{"future", d(11, 2), d(11, 15), d(10, 28), 0, 14},
		{"starts today", d(10, 28), d(10, 30), d(10, 28), 0, 3},
		{"going on", d(10, 26), d(10, 30), d(10, 28), 2, 3},
		{"ends today", d(10, 20), d(10, 28), d(10, 28), 8, 1},
		{"from the last year", d(1, 1).AddDate(0, 0, -5), d(1, 3), d(10, 28), 3, 0},
		{"into the next year", d(12, 29), d(12, 31).AddDate(0, 0, 7), d(10, 28), 0, 3},
	} {
		if tk, pl := vacationSplit(c.from, c.to, first, last, c.today); tk != c.taken || pl != c.planned {
			t.Errorf("%s: taken %d planned %d, want %d %d", c.name, tk, pl, c.taken, c.planned)
		}
	}
}

func TestMatchPayslipFile(t *testing.T) {
	people := []PayslipPerson{{"1", "Пётр Иванов", "petr@corp.ru"}, {"2", "Анна Петрова", "anna@corp.ru"},
		{"3", "Анна Смирнова", "a.smirnova@corp.ru"}, {"4", "Олег Смирнов", "oleg@corp.ru"}, {"5", "Олег Смирнов", "oleg2@corp.ru"},
		{"6", "Мадонна", "a@corp.ru"}}
	for file, want := range map[string]string{
		"Иванов_Петр_10-2026.pdf":   "1", // «ё» as «е», the order and the separators aside
		"ИВАНОВ ПЁТР.PDF":           "1",
		"листок petr@corp.ru.pdf":   "1",
		"Петрова Анна.pdf":          "2",
		"Смирнова-Анна.pdf":         "3", // not Олег Смирнов: «смирнова» is not «смирнов»
		"Смирнов Олег.pdf":          "",  // two namesakes
		"oleg2@corp.ru.pdf":         "5", // the e-mail tells them apart
		"Анна.pdf":                  "",  // a first name alone is no one's
		"Мадонна.pdf":               "",  // a one-word name is not matched by words
		"Иванов.pdf":                "",
		"Сидоров Пётр Иванович.pdf": "",
		"Иванов Пётр Сергеевич.pdf": "1",
		"anna@corp.ru.pdf":          "2", // not Мадонна's a@corp.ru
		"a@corp.ru.pdf":             "6",
	} {
		if got := MatchPayslipFile(file, people); got != want {
			t.Errorf("%s: %q, want %q", file, got, want)
		}
	}
}
