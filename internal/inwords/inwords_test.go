package inwords

import "testing"

func TestAmount(t *testing.T) {
	for _, c := range []struct {
		lang     string
		cents    int64
		currency string
		want     string
	}{
		{"ru-RU", 1234567, "RUB", "Двенадцать тысяч триста сорок пять рублей 67 копеек"},
		{"ru-RU", 100, "RUB", "Один рубль 00 копеек"},
		{"ru-RU", 2201, "RUB", "Двадцать два рубля 01 копейка"},
		{"ru-RU", 1100, "RUB", "Одиннадцать рублей 00 копеек"},
		{"ru-RU", 0, "RUB", "Ноль рублей 00 копеек"},
		{"ru-RU", 200000000, "RUB", "Два миллиона рублей 00 копеек"},
		{"ru-RU", 102100000, "RUB", "Один миллион двадцать одна тысяча рублей 00 копеек"},
		{"ru-RU", 200200, "RUB", "Две тысячи два рубля 00 копеек"},
		{"ru-RU", 1414, "RUB", "Четырнадцать рублей 14 копеек"},
		{"ru-RU", 50000, "EUR", "Пятьсот евро 00 евроцентов"},
		{"ru-RU", 200, "UAH", "Две гривны 00 копеек"},
		{"ru-RU", 12345, "XYZ", "Сто двадцать три XYZ 45/100"},
		{"en-US", 1234567, "USD", "Twelve thousand three hundred forty-five dollars 67 cents"},
		{"en-US", 101, "USD", "One dollar 01 cent"},
		{"en-US", 100000000000, "EUR", "One billion euros 00 cents"},
		{"en-US", 7000, "XYZ", "Seventy XYZ 00/100"},
	} {
		if got := Amount(c.lang, c.cents, c.currency); got != c.want {
			t.Errorf("Amount(%s, %d, %s) = %q, want %q", c.lang, c.cents, c.currency, got, c.want)
		}
	}
}
