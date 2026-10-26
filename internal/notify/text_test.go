package notify

import (
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestText(t *testing.T) {
	tr := func(key string, args ...any) string {
		return map[string]string{"notify.kind.status": "изменил статус задачи на", "status.completed": "Завершена",
			"notify.kind.mention": "упомянул вас", "notify.kind.deadline": "изменил крайний срок:", "notify.no_deadline": "— срок снят",
			"notify.kind.absence_hr.added": "внёс(ла) отсутствие", "absence.kind.vacation": "Отпуск"}[key]
	}
	when := func(t time.Time) string { return t.UTC().Format("02.01 15:04") }
	cases := map[string]Item{
		"Анна изменил статус задачи на «Завершена» · GO-1 Отчёт": {Kind: "status", ActorName: "Анна", Body: "completed", TaskKey: "GO-1", TaskTitle: "Отчёт"},
		"Анна упомянул вас · Пост\n@Боб глянь":                   {Kind: "mention", ActorName: "Анна", PostTitle: "Пост", Body: "@Боб глянь"},
		"Анна изменил крайний срок: 05.11 18:00 · GO-1 Отчёт":    {Kind: "deadline", ActorName: "Анна", Body: "2026-11-05T18:00:00Z", TaskKey: "GO-1", TaskTitle: "Отчёт"},
		"Анна изменил крайний срок: — срок снят · GO-1 Отчёт":    {Kind: "deadline", ActorName: "Анна", TaskKey: "GO-1", TaskTitle: "Отчёт"},
		"Анна внёс(ла) отсутствие «Отпуск» · Пётр, 01.11.2026 — 14.11.2026": {Kind: "absence_hr", ActorName: "Анна",
			Body: "added|vacation|Пётр|01.11.2026|14.11.2026"},
	}
	for want, it := range cases {
		if got := Text(it, tr, when); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

// TestKindLabels: every kind and group of the settings page has its label in each language.
func TestKindLabels(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range b.Langs() {
		for _, g := range Kinds {
			if !b.Has(l.Code, "notify.group."+g.Group) {
				t.Errorf("%s: no notify.group.%s", l.Code, g.Group)
			}
			for _, k := range g.Kinds {
				if !b.Has(l.Code, "notify.set."+k) {
					t.Errorf("%s: no notify.set.%s", l.Code, k)
				}
			}
		}
	}
}
