package web

import (
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestHistoryLine(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 13, 10, 5, 0, 0, time.Local)
	cases := map[string][4]string{
		"Анна: Учёт времени: 1 ч 30 мин · 13.10.2026 · созвон → 45 мин · 12.10.2026": {"Анна", "time", "5400|2026-10-13|созвон", "2700|2026-10-12|"},
		"Анна: Чек-лист: удалено: Позвонить":                                      {"Анна", "checklist", "Позвонить", ""},
		"Анна: Файл добавлен: отчёт.pdf":                                            {"Анна", "file", "", "отчёт.pdf"},
		"Анна: Статус: Ждёт выполнения → Выполняется":                              {"Анна", "status", "pending", "in_progress"},
		"Анна завершил(а) задачу":                                                   {"Анна", "status", "in_progress", "completed"},
	}
	for want, c := range cases {
		got := HistoryLine(b, "ru-RU", c[0], c[1], c[2], c[3], at)
		if !strings.Contains(got, want) || !strings.Contains(got, "13.10.2026 10:05") {
			t.Errorf("got %q, want … %q", got, want)
		}
	}
}
