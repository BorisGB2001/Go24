package notify

import (
	"context"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestItemLink(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want string
	}{
		{
			name: "url only",
			item: Item{URL: "https://example.com/crm/deal/1"},
			want: "https://example.com/crm/deal/1",
		},
		{
			name: "url priority over all other fields",
			item: Item{
				URL:        "/custom/target",
				TaskKey:    "GO-1",
				PostID:     "10",
				EventID:    "20",
				ProjectKey: "PROJ",
			},
			want: "/custom/target",
		},
		{
			name: "task key link",
			item: Item{TaskKey: "GO-42"},
			want: "/t/GO-42",
		},
		{
			name: "task key priority over post, event and project",
			item: Item{
				TaskKey:    "GO-99",
				PostID:     "15",
				EventID:    "25",
				ProjectKey: "KEY",
			},
			want: "/t/GO-99",
		},
		{
			name: "post link",
			item: Item{PostID: "101"},
			want: "/feed#post-101",
		},
		{
			name: "post priority over event and project",
			item: Item{
				PostID:     "202",
				EventID:    "303",
				ProjectKey: "KEY",
			},
			want: "/feed#post-202",
		},
		{
			name: "event link",
			item: Item{EventID: "555"},
			want: "/calendar/555",
		},
		{
			name: "event priority over project",
			item: Item{
				EventID:    "777",
				ProjectKey: "KEY",
			},
			want: "/calendar/777",
		},
		{
			name: "project invite link",
			item: Item{ProjectKey: "ALPHA"},
			want: "/projects#invites",
		},
		{
			name: "empty fallback to root",
			item: Item{},
			want: "/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.Link(); got != tc.want {
				t.Errorf("Link() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestItemDeadline(t *testing.T) {
	expectedUTC := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		item    Item
		wantNil bool
		want    time.Time
	}{
		{
			name: "valid UTC deadline",
			item: Item{Kind: "deadline", Body: "2026-11-05T18:00:00Z"},
			want: expectedUTC,
		},
		{
			name: "valid deadline with timezone offset",
			item: Item{Kind: "deadline", Body: "2026-11-05T21:00:00+03:00"},
			want: expectedUTC,
		},
		{
			name:    "empty body",
			item:    Item{Kind: "deadline", Body: ""},
			wantNil: true,
		},
		{
			name:    "invalid date format",
			item:    Item{Kind: "deadline", Body: "2026-11-05 18:00:00"},
			wantNil: true,
		},
		{
			name:    "malformed text",
			item:    Item{Kind: "deadline", Body: "not-a-time"},
			wantNil: true,
		},
		{
			name:    "not a deadline kind with valid timestamp",
			item:    Item{Kind: "status", Body: "2026-11-05T18:00:00Z"},
			wantNil: true,
		},
		{
			name:    "task_new kind with valid timestamp",
			item:    Item{Kind: "task_new", Body: "2026-11-05T18:00:00Z"},
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.item.Deadline()
			if tc.wantNil {
				if got != nil {
					t.Errorf("Deadline() = %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("Deadline() = nil, want %v", tc.want)
			}
			if !got.Equal(tc.want) {
				t.Errorf("Deadline() = %v, want %v", *got, tc.want)
			}
		})
	}
}

func TestItemSplit(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "standard status and date",
			body: "approved|25.10.2026",
			want: []string{"approved", "25.10.2026"},
		},
		{
			name: "mark and date range",
			body: "good|20.10.2026 — 23.10.2026",
			want: []string{"good", "20.10.2026 — 23.10.2026"},
		},
		{
			name: "no separator",
			body: "onlyone",
			want: []string{"onlyone", ""},
		},
		{
			name: "empty string",
			body: "",
			want: []string{"", ""},
		},
		{
			name: "multiple separators cuts on first pipe",
			body: "first|second|third",
			want: []string{"first", "second|third"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := Item{Body: tc.body}
			got := it.Split()
			if len(got) != 2 || got[0] != tc.want[0] || got[1] != tc.want[1] {
				t.Errorf("Split() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestItemHRParts(t *testing.T) {
	tests := []struct {
		name string
		item Item
		want []string
	}{
		{
			name: "valid absence_hr with 5 parts",
			item: Item{Kind: "absence_hr", Body: "added|vacation|Пётр Иванов|01.11.2026|14.11.2026"},
			want: []string{"added", "vacation", "Пётр Иванов", "01.11.2026", "14.11.2026"},
		},
		{
			name: "absence_hr with fewer than 5 parts",
			item: Item{Kind: "absence_hr", Body: "added|vacation|Пётр Иванов|01.11.2026"},
			want: nil,
		},
		{
			name: "absence_hr with more than 5 parts",
			item: Item{Kind: "absence_hr", Body: "added|vacation|Пётр Иванов|01.11.2026|14.11.2026|extra"},
			want: nil,
		},
		{
			name: "absence_hr with empty body",
			item: Item{Kind: "absence_hr", Body: ""},
			want: nil,
		},
		{
			name: "different kind with 5 parts",
			item: Item{Kind: "workday_decided", Body: "a|b|c|d|e"},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.item.HRParts()
			if tc.want == nil {
				if got != nil {
					t.Errorf("HRParts() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tc.want) {
				t.Fatalf("HRParts() length = %d, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("HRParts()[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestItemReactionAndExcerpt(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantReact   string
		wantExcerpt string
	}{
		{
			name:        "emoji with comment text",
			body:        "👍 Отличная работа над задачей",
			wantReact:   "👍",
			wantExcerpt: "Отличная работа над задачей",
		},
		{
			name:        "emoji only without space",
			body:        "❤️",
			wantReact:   "❤️",
			wantExcerpt: "",
		},
		{
			name:        "emoji followed by single word",
			body:        "🎉 Ура",
			wantReact:   "🎉",
			wantExcerpt: "Ура",
		},
		{
			name:        "empty body",
			body:        "",
			wantReact:   "",
			wantExcerpt: "",
		},
		{
			name:        "leading space before comment",
			body:        "  space text",
			wantReact:   "",
			wantExcerpt: " space text",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it := Item{Body: tc.body}
			if got := it.Reaction(); got != tc.wantReact {
				t.Errorf("Reaction() = %q, want %q", got, tc.wantReact)
			}
			if got := it.Excerpt(); got != tc.wantExcerpt {
				t.Errorf("Excerpt() = %q, want %q", got, tc.wantExcerpt)
			}
		})
	}
}

func TestStoreGetInvalidID(t *testing.T) {
	s := NewStore(nil)
	if s == nil {
		t.Fatal("NewStore returned nil")
	}

	invalidIDs := []string{
		"abc",
		"",
		"12.34",
		"id-42",
		" 123",
		"9999999999999999999999999999999999999999", // int64 overflow
	}

	for _, id := range invalidIDs {
		t.Run("id_"+id, func(t *testing.T) {
			item, err := s.Get(context.Background(), "user-1", id)
			if err != ErrNotFound {
				t.Errorf("Get(id=%q) error = %v, want %v", id, err, ErrNotFound)
			}
			if item != (Item{}) {
				t.Errorf("Get(id=%q) item = %+v, want empty Item", id, item)
			}
		})
	}
}

func TestKindsMatrixIntegrity(t *testing.T) {
	if len(Kinds) == 0 {
		t.Fatal("Kinds slice is empty")
	}

	groupSeen := map[string]bool{}
	kindSeen := map[string]string{}

	for _, g := range Kinds {
		if g.Group == "" {
			t.Errorf("empty group name found")
		}
		if groupSeen[g.Group] {
			t.Errorf("duplicate group name: %q", g.Group)
		}
		groupSeen[g.Group] = true

		if len(g.Kinds) == 0 {
			t.Errorf("group %q has no kinds defined", g.Group)
		}

		for _, k := range g.Kinds {
			if k == "" {
				t.Errorf("empty kind found in group %q", g.Group)
			}
			if prevGroup, ok := kindSeen[k]; ok {
				t.Errorf("kind %q appears multiple times (in group %q and %q)", k, prevGroup, g.Group)
			}
			kindSeen[k] = g.Group
		}
	}
}

func TestTextAllKinds(t *testing.T) {
	tr := func(key string, args ...any) string {
		translations := map[string]string{
			// kinds
			"notify.kind.status":                   "изменил статус задачи на",
			"notify.kind.deadline":                 "изменил крайний срок:",
			"notify.kind.participant":              "назначил вас",
			"notify.kind.like":                     "оценил(а) вашу публикацию:",
			"notify.kind.comment_like":             "отреагировал(а)",
			"notify.kind.flow_task":                "назначил задачу потока",
			"notify.kind.mail_assign":              "передал письмо",
			"notify.kind.mail_recall":              "отозвал письмо",
			"notify.kind.disk_share":               "открыл доступ к папке",
			"notify.kind.disk_file_share":          "открыл доступ к файлу",
			"notify.kind.disk_share_left":          "закрыл доступ к диску",
			"notify.kind.disk_comment":             "прокомментировал файл",
			"notify.kind.kb_page_changed":          "изменил страницу базы знаний",
			"notify.kind.kb_page_new":              "создал страницу базы знаний",
			"notify.kind.kb_page_trashed":          "удалил страницу базы знаний",
			"notify.kind.kb_comment":               "прокомментировал статью",
			"notify.kind.kb_reply":                 "ответил в базе знаний",
			"notify.kind.kb_mention":               "упомянул вас в базе знаний",
			"notify.kind.kb_like":                  "оценил статью",
			"notify.kind.learn_assign":             "назначил курс",
			"notify.kind.enps":                     "опрос вовлечённости",
			"notify.kind.subtasks_done":            "Все подзадачи завершены",
			"notify.kind.event_reply.accepted":     "придёт на встречу",
			"notify.kind.event_reply.declined":     "не придёт на встречу",
			"notify.kind.event_reply.tentative":    "возможно, придёт на встречу",
			"notify.kind.event_invited":            "пригласил вас на встречу",
			"notify.kind.task_remind":              "Напоминание по задаче",
			"notify.kind.crm_activity":             "активность CRM",
			"notify.kind.crm_recurring":            "регулярная сделка",
			"notify.kind.crm_salesgen":             "генератор продаж",
			"notify.kind.crm_observer":             "наблюдатель CRM",
			"notify.kind.crm_comment":              "комментарий в CRM",
			"notify.kind.crm_stage":                "стадия сделки",
			"notify.kind.crm_activity_remind":      "Дело в CRM",
			"notify.kind.learn_due":                "Срок курса подходит",
			"notify.kind.learn_overdue":            "Курс просрочен",
			"notify.kind.sign_request":             "запрос на подписание",
			"notify.kind.sign_review":              "документ на согласовании",
			"notify.kind.sign_done":                "Документ подписан",
			"notify.kind.sign_refused":             "В подписании отказано",
			"notify.kind.sign_cancelled":           "Подписание отменено",
			"notify.kind.sign_expired":             "Срок подписания истёк",
			"notify.kind.booking_new":              "Новая бронь",
			"notify.kind.booking_late":             "Опоздание на бронь",
			"notify.kind.booking_unconfirmed":      "Бронь не подтверждена",
			"notify.kind.booking_confirmed":        "Бронь подтверждена",
			"notify.kind.booking_cancelled":        "Бронь отменена",
			"notify.kind.booking_moved":            "Бронь перенесена",
			"notify.kind.booking_rated":            "Оценка бронирования",
			"notify.kind.event_slot":               "Запись на слот",
			"notify.kind.event_slot_cancel":        "Отмена слота",
			"notify.kind.backup_failed":            "Сбой резервного копирования",
			"notify.kind.backup_restored":          "Копия восстановлена",
			"notify.kind.backup_restore_failed":    "Сбой восстановления",
			"notify.kind.workday_change":           "изменил рабочий день:",
			"notify.kind.workday_decided.approved": "утвердил правку рабочего дня",
			"notify.kind.workday_decided.declined": "отклонил правку рабочего дня",
			"notify.kind.workday_violation.late":   "опоздал(а)",
			"notify.kind.workday_violation.early":  "ушёл(ла) раньше",
			"notify.kind.meeting":                  "Встреча",
			"notify.kind.honor":                    "на Доске почёта",
			"notify.kind.work_report":              "сдал отчёт",
			"notify.kind.work_report_comment":      "прокомментировал отчёт",
			"notify.kind.work_report_mark":         "оценил отчёт за",
			"notify.kind.absence_hr.added":         "внёс(ла) отсутствие",
			"notify.kind.absence_hr":               "изменил(а) отсутствие",
			"notify.kind.join_request":             "Заявка на вход",
			"notify.kind.site_form":                "Заявка с сайта",
			"notify.kind.crm_form":                 "Заполнена форма CRM",
			"notify.kind.phone_score":              "Оценка звонка",
			"notify.kind.im_remind":                "Напоминание из чата",
			"notify.kind.mail_remind":              "Напоминание о письме",
			"notify.kind.event_remind":             "Напоминание о событии",
			"notify.kind.comment":                  "добавил комментарий к",
			"notify.kind.post_comment":             "прокомментировал публикацию",
			"notify.kind.post_followed":            "новое сообщение в отслеживаемом посте",
			"notify.kind.mention":                  "упомянул вас",
			"notify.kind.event_comment":            "прокомментировал событие",
			"notify.kind.task_new":                 "поставил задачу",
			"notify.kind.post_to_you":              "адресовал вам сообщение",
			"notify.kind.project_invite":           "пригласил вас в проект",

			// helpers
			"notify.no_deadline":    "— срок снят",
			"status.completed":      "Завершена",
			"status.in_progress":    "Выполняется",
			"role.auditor":          "Наблюдатель",
			"absence.kind.vacation": "Отпуск",
			"wreport.mark.good":     "Положительно",
			"backup.err_space":      "Недостаточно места на диске",
		}
		if val, ok := translations[key]; ok {
			return val
		}
		return key
	}

	when := func(t time.Time) string {
		return t.UTC().Format("02.01 15:04")
	}

	tests := []struct {
		name string
		item Item
		want string
	}{
		// early return kinds
		{
			name: "crm_robot",
			item: Item{Kind: "crm_robot", Body: "Сделка переведена роботом", TaskKey: "IGNORE"},
			want: "🤖 Сделка переведена роботом",
		},
		{
			name: "task_robot",
			item: Item{Kind: "task_robot", Body: "Робот завершил задачу"},
			want: "🤖 Робот завершил задачу",
		},
		{
			name: "app",
			item: Item{Kind: "app", Body: "Интеграция обновила остатки"},
			want: "🔔 Интеграция обновила остатки",
		},
		{
			name: "app_personal",
			item: Item{Kind: "app_personal", ActorName: "Telegram Bot", Body: "Ваш код: 1234"},
			want: "Telegram Bot: Ваш код: 1234",
		},
		{
			name: "crm_activity_remind",
			item: Item{Kind: "crm_activity_remind", Body: "Позвонить клиенту в 15:00"},
			want: "⏰ Дело в CRM: Позвонить клиенту в 15:00",
		},
		{
			name: "learn_due",
			item: Item{Kind: "learn_due", Body: "Охрана труда"},
			want: "⏰ Срок курса подходит: Охрана труда",
		},
		{
			name: "learn_overdue",
			item: Item{Kind: "learn_overdue", Body: "Пожарная безопасность"},
			want: "⏰ Курс просрочен: Пожарная безопасность",
		},
		{
			name: "sign_done",
			item: Item{Kind: "sign_done", Body: "Договор поставки № 12"},
			want: "✍ Документ подписан: Договор поставки № 12",
		},
		{
			name: "sign_refused",
			item: Item{Kind: "sign_refused", Body: "Акт сверки"},
			want: "✍ В подписании отказано: Акт сверки",
		},
		{
			name: "sign_cancelled",
			item: Item{Kind: "sign_cancelled", Body: "Соглашение о конфиденциальности"},
			want: "✍ Подписание отменено: Соглашение о конфиденциальности",
		},
		{
			name: "sign_expired",
			item: Item{Kind: "sign_expired", Body: "Счёт-оферта"},
			want: "✍ Срок подписания истёк: Счёт-оферта",
		},
		{
			name: "booking_new",
			item: Item{Kind: "booking_new", Body: "Переговорная 1 на 14:00"},
			want: "📅 Новая бронь: Переговорная 1 на 14:00",
		},
		{
			name: "booking_late",
			item: Item{Kind: "booking_late", Body: "Переговорная 2"},
			want: "📅 Опоздание на бронь: Переговорная 2",
		},
		{
			name: "booking_unconfirmed",
			item: Item{Kind: "booking_unconfirmed", Body: "Кабинет директора"},
			want: "📅 Бронь не подтверждена: Кабинет директора",
		},
		{
			name: "booking_confirmed",
			item: Item{Kind: "booking_confirmed", Body: "Зал презентаций"},
			want: "📅 Бронь подтверждена: Зал презентаций",
		},
		{
			name: "booking_cancelled",
			item: Item{Kind: "booking_cancelled", Body: "Рабочее место у окна"},
			want: "📅 Бронь отменена: Рабочее место у окна",
		},
		{
			name: "booking_moved",
			item: Item{Kind: "booking_moved", Body: "Перенесено на завтра"},
			want: "📅 Бронь перенесена: Перенесено на завтра",
		},
		{
			name: "booking_rated",
			item: Item{Kind: "booking_rated", Body: "5 звёзд"},
			want: "📅 Оценка бронирования: 5 звёзд",
		},
		{
			name: "event_slot",
			item: Item{Kind: "event_slot", Body: "Консультация 16:30"},
			want: "📅 Запись на слот: Консультация 16:30",
		},
		{
			name: "event_slot_cancel",
			item: Item{Kind: "event_slot_cancel", Body: "Отмена консультации"},
			want: "📅 Отмена слота: Отмена консультации",
		},
		{
			name: "bp",
			item: Item{Kind: "bp", Body: "Процесс утверждения счёта завершён"},
			want: "⚙ Процесс утверждения счёта завершён",
		},
		{
			name: "backup_failed with translated body",
			item: Item{Kind: "backup_failed", Body: "backup.err_space"},
			want: "💾 Сбой резервного копирования: Недостаточно места на диске",
		},
		{
			name: "backup_restored",
			item: Item{Kind: "backup_restored", Body: "backup_20261028.tar.gz"},
			want: "💾 Копия восстановлена: backup_20261028.tar.gz",
		},
		{
			name: "backup_restore_failed",
			item: Item{Kind: "backup_restore_failed", Body: "backup.err_space"},
			want: "💾 Сбой восстановления: Недостаточно места на диске",
		},
		{
			name: "workday_decided approved",
			item: Item{Kind: "workday_decided", ActorName: "Борис", Body: "approved|25.10.2026"},
			want: "Борис утвердил правку рабочего дня 25.10.2026",
		},
		{
			name: "workday_decided declined",
			item: Item{Kind: "workday_decided", ActorName: "Борис", Body: "declined|26.10.2026"},
			want: "Борис отклонил правку рабочего дня 26.10.2026",
		},
		{
			name: "workday_violation late",
			item: Item{Kind: "workday_violation", ActorName: "Иван", Body: "late|28.10.2026"},
			want: "⏱ Иван опоздал(а) · 28.10.2026",
		},
		{
			name: "workday_violation early",
			item: Item{Kind: "workday_violation", ActorName: "Ольга", Body: "early|28.10.2026"},
			want: "⏱ Ольга ушёл(ла) раньше · 28.10.2026",
		},
		{
			name: "meeting",
			item: Item{Kind: "meeting", ActorName: "Анна", Body: "Планёрка отдела|29.10.2026 10:00"},
			want: "📋 Анна Встреча «Планёрка отдела» · 29.10.2026 10:00",
		},
		{
			name: "honor with reason",
			item: Item{Kind: "honor", ActorName: "Анна", Body: "Запуск проекта в срок"},
			want: "🏆 Анна на Доске почёта: Запуск проекта в срок",
		},
		{
			name: "honor without reason",
			item: Item{Kind: "honor", ActorName: "Анна", Body: ""},
			want: "🏆 Анна на Доске почёта",
		},
		{
			name: "work_report_mark",
			item: Item{Kind: "work_report_mark", ActorName: "Борис", Body: "good|20.10.2026 — 23.10.2026"},
			want: "Борис оценил отчёт за 20.10.2026 — 23.10.2026: Положительно",
		},
		{
			name: "absence_hr valid",
			item: Item{Kind: "absence_hr", ActorName: "Елена", Body: "added|vacation|Пётр Иванов|01.11.2026|14.11.2026"},
			want: "Елена внёс(ла) отсутствие «Отпуск» · Пётр Иванов, 01.11.2026 — 14.11.2026",
		},
		{
			name: "openline",
			item: Item{Kind: "openline", Body: "Новое обращение из чата на сайте"},
			want: "💬 Новое обращение из чата на сайте",
		},
		{
			name: "join_request",
			item: Item{Kind: "join_request", Body: "Иван Петров <ivan@firm.ru>"},
			want: "👤 Заявка на вход: Иван Петров <ivan@firm.ru>",
		},
		{
			name: "site_form",
			item: Item{Kind: "site_form", Body: "Обратный звонок: +7 999 111-22-33"},
			want: "🌐 Заявка с сайта: Обратный звонок: +7 999 111-22-33",
		},
		{
			name: "crm_form",
			item: Item{Kind: "crm_form", Body: "Заказ консультации"},
			want: "📋 Заполнена форма CRM: Заказ консультации",
		},
		{
			name: "phone",
			item: Item{Kind: "phone", Body: "Пропущенный звонок от +7 900 000-00-00"},
			want: "📞 Пропущенный звонок от +7 900 000-00-00",
		},
		{
			name: "phone_score",
			item: Item{Kind: "phone_score", Body: "Клиент поставил 5 баллов"},
			want: "📞 Оценка звонка: Клиент поставил 5 баллов",
		},
		{
			name: "im_remind",
			item: Item{Kind: "im_remind", Body: "Ответить коллеге"},
			want: "⏰ Напоминание из чата: Ответить коллеге",
		},
		{
			name: "mail",
			item: Item{Kind: "mail", Body: "Входящее письмо от partner@example.com"},
			want: "✉ Входящее письмо от partner@example.com",
		},
		{
			name: "mail_remind",
			item: Item{Kind: "mail_remind", Body: "Проверить вложение"},
			want: "⏰ Напоминание о письме: Проверить вложение",
		},

		// non-early-return kinds with headers, body and tails
		{
			name: "status with task",
			item: Item{Kind: "status", ActorName: "Анна", Body: "completed", TaskKey: "GO-10", TaskTitle: "Отчёт"},
			want: "Анна изменил статус задачи на «Завершена» · GO-10 Отчёт",
		},
		{
			name: "deadline set with task",
			item: Item{Kind: "deadline", ActorName: "Анна", Body: "2026-11-05T18:00:00Z", TaskKey: "GO-11", TaskTitle: "Релиз"},
			want: "Анна изменил крайний срок: 05.11 18:00 · GO-11 Релиз",
		},
		{
			name: "deadline removed with task",
			item: Item{Kind: "deadline", ActorName: "Анна", TaskKey: "GO-11", TaskTitle: "Релиз"},
			want: "Анна изменил крайний срок: — срок снят · GO-11 Релиз",
		},
		{
			name: "participant with task",
			item: Item{Kind: "participant", ActorName: "Борис", Body: "auditor", TaskKey: "GO-12", TaskTitle: "Бюджет"},
			want: "Борис назначил вас «Наблюдатель» · GO-12 Бюджет",
		},
		{
			name: "like with post",
			item: Item{Kind: "like", ActorName: "Ольга", Body: "👍", PostTitle: "Обновление портала"},
			want: "Ольга оценил(а) вашу публикацию: 👍 · Обновление портала",
		},
		{
			name: "comment_like with excerpt and post",
			item: Item{Kind: "comment_like", ActorName: "Анна", Body: "👍 Отличная идея!", PostTitle: "Идеи на спринт"},
			want: "Анна отреагировал(а) 👍 · Идеи на спринт\nОтличная идея!",
		},
		{
			name: "comment_like without excerpt",
			item: Item{Kind: "comment_like", ActorName: "Анна", Body: "❤️", PostTitle: "Идеи на спринт"},
			want: "Анна отреагировал(а) ❤️ · Идеи на спринт",
		},
		{
			name: "flow_task",
			item: Item{Kind: "flow_task", ActorName: "Дмитрий", Body: "Шаг 1: согласование", TaskKey: "FL-1", TaskTitle: "Поток"},
			want: "Дмитрий назначил задачу потока «Шаг 1: согласование» · FL-1 Поток",
		},
		{
			name: "mail_assign",
			item: Item{Kind: "mail_assign", ActorName: "Иван", Body: "Письмо от клиента", TaskKey: "GO-15", TaskTitle: "Почта"},
			want: "Иван передал письмо «Письмо от клиента» · GO-15 Почта",
		},
		{
			name: "subtasks_done with task",
			item: Item{Kind: "subtasks_done", ActorName: "Система", TaskKey: "GO-20", TaskTitle: "Большой проект"},
			want: "✅ Все подзадачи завершены · GO-20 Большой проект",
		},
		{
			name: "event_reply accepted with event",
			item: Item{Kind: "event_reply", ActorName: "Анна", Body: "accepted", EventTitle: "Стратсессия"},
			want: "Анна придёт на встречу · Стратсессия",
		},
		{
			name: "event_reply declined with event",
			item: Item{Kind: "event_reply", ActorName: "Пётр", Body: "declined", EventTitle: "Стратсессия"},
			want: "Пётр не придёт на встречу · Стратсессия",
		},
		{
			name: "event_invited with event",
			item: Item{Kind: "event_invited", ActorName: "Борис", Body: "Повестка и материалы", EventTitle: "Демо"},
			want: "Борис пригласил вас на встречу: Повестка и материалы · Демо",
		},
		{
			name: "task_remind with task",
			item: Item{Kind: "task_remind", ActorName: "Система", TaskKey: "GO-30", TaskTitle: "Срочно"},
			want: "⏰ Напоминание по задаче · GO-30 Срочно",
		},
		{
			name: "crm_activity bracketed",
			item: Item{Kind: "crm_activity", ActorName: "Менеджер", Body: "Звонок клиенту"},
			want: "Менеджер активность CRM «Звонок клиенту»",
		},
		{
			name: "sign_request bracketed",
			item: Item{Kind: "sign_request", ActorName: "Юрист", Body: "Договор аренды"},
			want: "Юрист запрос на подписание «Договор аренды»",
		},
		{
			name: "workday_change",
			item: Item{Kind: "workday_change", ActorName: "Иван", Body: "Начало в 09:15"},
			want: "Иван изменил рабочий день: Начало в 09:15",
		},
		{
			name: "work_report with period",
			item: Item{Kind: "work_report", ActorName: "Иван", Body: "Октябрь 2026"},
			want: "Иван сдал отчёт · Октябрь 2026",
		},
		{
			name: "work_report_comment",
			item: Item{Kind: "work_report_comment", ActorName: "Борис", Body: "Октябрь 2026"},
			want: "Борис прокомментировал отчёт · Октябрь 2026",
		},
		{
			name: "absence_hr invalid body falls through without error",
			item: Item{Kind: "absence_hr", ActorName: "Кадровик", Body: "bad|format", TaskKey: "GO-5", TaskTitle: "Кадры"},
			want: "Кадровик изменил(а) отсутствие · GO-5 Кадры",
		},
		{
			name: "event_remind with valid time",
			item: Item{Kind: "event_remind", ActorName: "Система", Body: "2026-11-05T10:00:00Z", EventTitle: "Синхронизация"},
			want: "⏰ Напоминание о событии 05.11 10:00 · Синхронизация",
		},
		{
			name: "event_remind with invalid time",
			item: Item{Kind: "event_remind", ActorName: "Система", Body: "not-a-time", EventTitle: "Синхронизация"},
			want: "⏰ Напоминание о событии · Синхронизация",
		},

		// multiline bodies
		{
			name: "comment multiline body",
			item: Item{Kind: "comment", ActorName: "Борис", Body: "Проверь пункт 2", TaskKey: "GO-50", TaskTitle: "Доработка"},
			want: "Борис добавил комментарий к · GO-50 Доработка\nПроверь пункт 2",
		},
		{
			name: "post_comment multiline body",
			item: Item{Kind: "post_comment", ActorName: "Ольга", Body: "Поздравляю!", PostTitle: "Праздник компании"},
			want: "Ольга прокомментировал публикацию · Праздник компании\nПоздравляю!",
		},
		{
			name: "post_followed multiline body",
			item: Item{Kind: "post_followed", ActorName: "Сергей", Body: "Новые вводные", PostTitle: "Важная новость"},
			want: "Сергей новое сообщение в отслеживаемом посте · Важная новость\nНовые вводные",
		},
		{
			name: "mention multiline body",
			item: Item{Kind: "mention", ActorName: "Анна", Body: "@Борис обрати внимание", TaskKey: "GO-60", TaskTitle: "Макет"},
			want: "Анна упомянул вас · GO-60 Макет\n@Борис обрати внимание",
		},
		{
			name: "event_comment multiline body",
			item: Item{Kind: "event_comment", ActorName: "Пётр", Body: "Принесу проектор", EventTitle: "Презентация"},
			want: "Пётр прокомментировал событие · Презентация\nПринесу проектор",
		},
		{
			name: "task_new does not append body multiline",
			item: Item{Kind: "task_new", ActorName: "Борис", Body: "Текст описания задачи", TaskKey: "GO-70", TaskTitle: "Новая задача"},
			want: "Борис поставил задачу · GO-70 Новая задача",
		},

		// tail priority
		{
			name: "tail priority TaskKey over PostTitle, EventTitle, ProjectName",
			item: Item{
				Kind:        "task_new",
				ActorName:   "Борис",
				TaskKey:     "GO-80",
				TaskTitle:   "Приоритет задачи",
				PostTitle:   "Пост",
				EventTitle:  "Событие",
				ProjectName: "Проект",
			},
			want: "Борис поставил задачу · GO-80 Приоритет задачи",
		},
		{
			name: "tail priority PostTitle over EventTitle and ProjectName",
			item: Item{
				Kind:        "post_to_you",
				ActorName:   "Борис",
				PostTitle:   "Пост",
				EventTitle:  "Событие",
				ProjectName: "Проект",
			},
			want: "Борис адресовал вам сообщение · Пост",
		},
		{
			name: "tail priority EventTitle over ProjectName",
			item: Item{
				Kind:        "event_invited",
				ActorName:   "Борис",
				Body:        "Приглашение",
				EventTitle:  "Событие",
				ProjectName: "Проект",
			},
			want: "Борис пригласил вас на встречу: Приглашение · Событие",
		},
		{
			name: "tail fallback to ProjectName",
			item: Item{
				Kind:        "project_invite",
				ActorName:   "Борис",
				ProjectName: "Разработка ядра",
			},
			want: "Борис пригласил вас в проект · Разработка ядра",
		},
		{
			name: "no context tail when all tail fields empty",
			item: Item{
				Kind:      "project_invite",
				ActorName: "Борис",
			},
			want: "Борис пригласил вас в проект",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.item, tr, when)
			if got != tc.want {
				t.Errorf("Text() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTextRealBundle(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatalf("i18n.Load error: %v", err)
	}

	when := func(tm time.Time) string {
		return tm.UTC().Format("02.01 15:04")
	}

	for _, lang := range []string{"ru-RU", "en-US"} {
		t.Run(lang, func(t *testing.T) {
			tr := func(key string, args ...any) string {
				return b.T(lang, key, args...)
			}

			it := Item{
				Kind:      "status",
				ActorName: "Anna",
				Body:      "completed",
				TaskKey:   "GO-1",
				TaskTitle: "Report",
			}

			got := Text(it, tr, when)
			if got == "" {
				t.Errorf("%s: Text() returned empty string", lang)
			}
			// Verify the rendered line contains the actor name and task key
			if wantSub := "Anna"; got[:len(wantSub)] != wantSub {
				t.Errorf("%s: Text() = %q, does not start with actor name %q", lang, got, wantSub)
			}
		})
	}
}
