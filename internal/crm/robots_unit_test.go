package crm

import (
	"fmt"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestRobotParamsWait_Unit(t *testing.T) {
	tests := []struct {
		name  string
		delay int
		unit  string
		want  time.Duration
	}{
		{"minutes", 15, "min", 15 * time.Minute},
		{"hours", 2, "hour", 2 * time.Hour},
		{"days", 3, "day", 72 * time.Hour},
		{"zero delay", 0, "hour", 0},
		{"unknown unit", 5, "second", 0},
		{"empty unit", 10, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := RobotParams{Delay: tt.delay, Unit: tt.unit}
			if got := p.Wait(); got != tt.want {
				t.Errorf("RobotParams{%d, %q}.Wait() = %v, want %v", tt.delay, tt.unit, got, tt.want)
			}
		})
	}
}

func TestCardFill_Unit(t *testing.T) {
	c := card{
		title:    "Поставка оборудования",
		amount:   1500000.5,
		currency: "RUB",
		client:   "ООО Ромашка",
		link:     "/crm/deals/deal-101",
	}

	template := "Сделка: {title}, Сумма: {amount}, Клиент: {client}, Ответственный: {responsible}, Ссылка: {link}"
	got := c.fill(template, "Анна Иванова")
	want := "Сделка: Поставка оборудования, Сумма: 1 500 000,50 RUB, Клиент: ООО Ромашка, Ответственный: Анна Иванова, Ссылка: /crm/deals/deal-101"

	if got != want {
		t.Errorf("card.fill() =\n%q\nwant:\n%q", got, want)
	}
}

func TestErrKey_Unit(t *testing.T) {
	ie := i18n.E("err.crm_stage")
	if got := errKey(ie); got != "err.crm_stage" {
		t.Errorf("errKey(i18n.Error) = %q, want \"err.crm_stage\"", got)
	}

	stdErr := fmt.Errorf("database connection failed")
	if got := errKey(stdErr); got != "database connection failed" {
		t.Errorf("errKey(stdErr) = %q, want \"database connection failed\"", got)
	}
}

func TestDealAndLeadCard_Unit(t *testing.T) {
	// 1. Deal with contact
	dContact := Deal{
		ID:          "d-1",
		Title:       "Сделка 1",
		Amount:      5000,
		Currency:    "RUB",
		Stage:       Stage{ID: "st-1"},
		Contact:     Ref{Name: "Иван Иванов"},
		Company:     Ref{Name: "ООО Вектор"},
		Responsible: Ref{ID: "u-1"},
		CreatedBy:   "u-2",
	}
	card1 := dealCard(dContact)
	if card1.entity != "deal" || card1.id != "d-1" || card1.client != "Иван Иванов" || card1.link != "/crm/deals/d-1" {
		t.Errorf("dealCard with contact mismatch: %+v", card1)
	}

	// 2. Deal without contact falls back to company
	dCompany := Deal{
		ID:          "d-2",
		Title:       "Сделка 2",
		Company:     Ref{Name: "ООО Вектор"},
		Responsible: Ref{ID: "u-1"},
	}
	card2 := dealCard(dCompany)
	if card2.client != "ООО Вектор" {
		t.Errorf("dealCard fallback client = %q, want \"ООО Вектор\"", card2.client)
	}

	// 3. Lead with name
	lName := Lead{
		ID:           "l-1",
		Title:        "Лид 1",
		Name:         "Петр Петров",
		CompanyTitle: "ЗАО Старт",
		Responsible:  Ref{ID: "u-3"},
	}
	cardL1 := leadCard(lName)
	if cardL1.entity != "lead" || cardL1.client != "Петр Петров" || cardL1.link != "/crm/leads/l-1" {
		t.Errorf("leadCard with name mismatch: %+v", cardL1)
	}

	// 4. Lead without name falls back to CompanyTitle
	lCompany := Lead{
		ID:           "l-2",
		Title:        "Лид 2",
		CompanyTitle: "ЗАО Старт",
	}
	cardL2 := leadCard(lCompany)
	if cardL2.client != "ЗАО Старт" {
		t.Errorf("leadCard fallback client = %q, want \"ЗАО Старт\"", cardL2.client)
	}
}

func TestRobotCondOK_Unit(t *testing.T) {
	values := map[string]string{
		"title":   "Крупная сделка",
		"amount":  "1500.50",
		"empty_f": "   ",
		"city":    "Москва",
	}

	tests := []struct {
		name string
		cond RobotCond
		want bool
	}{
		{"empty operator on empty field", RobotCond{Field: "empty_f", Op: "empty"}, true},
		{"empty operator on absent field", RobotCond{Field: "not_set", Op: "empty"}, true},
		{"empty operator on filled field", RobotCond{Field: "title", Op: "empty"}, false},
		{"filled operator on filled field", RobotCond{Field: "title", Op: "filled"}, true},
		{"filled operator on empty field", RobotCond{Field: "empty_f", Op: "filled"}, false},
		{"gt operator true", RobotCond{Field: "amount", Op: "gt", Value: "1000"}, true},
		{"gt operator false", RobotCond{Field: "amount", Op: "gt", Value: "2000"}, false},
		{"gt operator with formatted value spaces and comma", RobotCond{Field: "amount", Op: "gt", Value: "1 500,00"}, true},
		{"gt operator invalid number returns false", RobotCond{Field: "title", Op: "gt", Value: "100"}, false},
		{"lt operator true", RobotCond{Field: "amount", Op: "lt", Value: "2000"}, true},
		{"lt operator false", RobotCond{Field: "amount", Op: "lt", Value: "1000"}, false},
		{"contains operator case insensitive true", RobotCond{Field: "title", Op: "contains", Value: "сделка"}, true},
		{"contains operator false", RobotCond{Field: "title", Op: "contains", Value: "договор"}, false},
		{"eq operator numeric equal", RobotCond{Field: "amount", Op: "eq", Value: "1500,5"}, true},
		{"eq operator text case insensitive", RobotCond{Field: "city", Op: "eq", Value: "москва"}, true},
		{"eq operator text mismatch", RobotCond{Field: "city", Op: "eq", Value: "СПб"}, false},
		{"neq operator true", RobotCond{Field: "city", Op: "neq", Value: "Казань"}, true},
		{"neq operator false", RobotCond{Field: "city", Op: "neq", Value: "Москва"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := robotCondOK(tt.cond, values); got != tt.want {
				t.Errorf("robotCondOK(%+v) = %v, want %v", tt.cond, got, tt.want)
			}
		})
	}
}
