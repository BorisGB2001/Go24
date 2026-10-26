// Package inwords writes a sum of money in words, as «Сумма прописью» of the documents: «Двенадцать тысяч
// триста сорок пять рублей 67 копеек», «Twelve thousand three hundred forty-five dollars 67 cents».
package inwords

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Amount is the sum (in hundredths: kopecks, cents) in words in the language (ru… — Russian, else English); the
// whole part in words, the hundredths in figures. Roubles, dollars and euros are named, another currency by its code.
func Amount(lang string, cents int64, currency string) string {
	if cents < 0 {
		cents = 0
	}
	whole, frac := cents/100, cents%100
	var s string
	if strings.HasPrefix(lang, "ru") {
		if c, ok := ruCurrencies[currency]; ok {
			s = fmt.Sprintf("%s %s %02d %s", ruNumber(whole, c.fem), plural(whole, c.forms), frac, plural(frac, c.cents))
		} else {
			s = fmt.Sprintf("%s %s %02d/100", ruNumber(whole, false), currency, frac)
		}
	} else {
		if c, ok := enCurrencies[currency]; ok {
			s = fmt.Sprintf("%s %s %02d %s", enNumber(whole), c[enMany(whole)], frac, c[2+enMany(frac)])
		} else {
			s = fmt.Sprintf("%s %s %02d/100", enNumber(whole), currency, frac)
		}
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

type ruCurrency struct {
	fem          bool      // «одна гривна»
	forms, cents [3]string // one, few, many
}

var ruCurrencies = map[string]ruCurrency{
	"RUB": {forms: [3]string{"рубль", "рубля", "рублей"}, cents: [3]string{"копейка", "копейки", "копеек"}},
	"USD": {forms: [3]string{"доллар США", "доллара США", "долларов США"}, cents: [3]string{"цент", "цента", "центов"}},
	"EUR": {forms: [3]string{"евро", "евро", "евро"}, cents: [3]string{"евроцент", "евроцента", "евроцентов"}},
	"BYN": {forms: [3]string{"белорусский рубль", "белорусских рубля", "белорусских рублей"}, cents: [3]string{"копейка", "копейки", "копеек"}},
	"KZT": {forms: [3]string{"тенге", "тенге", "тенге"}, cents: [3]string{"тиын", "тиына", "тиынов"}},
	"UAH": {fem: true, forms: [3]string{"гривна", "гривны", "гривен"}, cents: [3]string{"копейка", "копейки", "копеек"}},
	"CNY": {forms: [3]string{"юань", "юаня", "юаней"}, cents: [3]string{"фэнь", "фэня", "фэней"}},
}

// plural is the form of the word for the number: one (21), few (22–24), many (25–30, 11–14).
func plural(n int64, f [3]string) string {
	switch n10, n100 := n%10, n%100; {
	case n100 >= 11 && n100 <= 14:
		return f[2]
	case n10 == 1:
		return f[0]
	case n10 >= 2 && n10 <= 4:
		return f[1]
	}
	return f[2]
}

var (
	ruOnes     = []string{"", "один", "два", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять"}
	ruOnesFem  = []string{"", "одна", "две"}
	ruTeens    = []string{"десять", "одиннадцать", "двенадцать", "тринадцать", "четырнадцать", "пятнадцать", "шестнадцать", "семнадцать", "восемнадцать", "девятнадцать"}
	ruTens     = []string{"", "", "двадцать", "тридцать", "сорок", "пятьдесят", "шестьдесят", "семьдесят", "восемьдесят", "девяносто"}
	ruHundreds = []string{"", "сто", "двести", "триста", "четыреста", "пятьсот", "шестьсот", "семьсот", "восемьсот", "девятьсот"}
	ruScales   = []struct {
		fem   bool
		forms [3]string
	}{
		{true, [3]string{"тысяча", "тысячи", "тысяч"}},
		{false, [3]string{"миллион", "миллиона", "миллионов"}},
		{false, [3]string{"миллиард", "миллиарда", "миллиардов"}},
		{false, [3]string{"триллион", "триллиона", "триллионов"}},
	}
)

// ruTriple is 1–999 in words.
func ruTriple(n int64, fem bool) []string {
	var w []string
	if h := n / 100; h > 0 {
		w = append(w, ruHundreds[h])
	}
	switch t, o := n%100/10, n%10; {
	case t == 1:
		w = append(w, ruTeens[o])
	default:
		if t > 1 {
			w = append(w, ruTens[t])
		}
		if o > 0 {
			if fem && o <= 2 {
				w = append(w, ruOnesFem[o])
			} else {
				w = append(w, ruOnes[o])
			}
		}
	}
	return w
}

// ruNumber is the whole number in words, the ones of the gender given.
func ruNumber(n int64, fem bool) string {
	if n == 0 {
		return "ноль"
	}
	var w []string
	if low := n % 1000; low > 0 {
		w = ruTriple(low, fem)
	}
	n /= 1000
	for i := 0; n > 0 && i < len(ruScales); i++ {
		if t := n % 1000; t > 0 {
			w = append(append(ruTriple(t, ruScales[i].fem), plural(t, ruScales[i].forms)), w...)
		}
		n /= 1000
	}
	return strings.Join(w, " ")
}

// enMany: 0 — one, 1 — many.
func enMany(n int64) int {
	if n == 1 {
		return 0
	}
	return 1
}

// enCurrencies: one, many; of the hundredths one, many.
var enCurrencies = map[string][4]string{
	"RUB": {"rouble", "roubles", "kopeck", "kopecks"},
	"USD": {"dollar", "dollars", "cent", "cents"},
	"EUR": {"euro", "euros", "cent", "cents"},
	"GBP": {"pound", "pounds", "penny", "pence"},
}

var (
	enOnes = []string{"", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve",
		"thirteen", "fourteen", "fifteen", "sixteen", "seventeen", "eighteen", "nineteen"}
	enTens   = []string{"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy", "eighty", "ninety"}
	enScales = []string{"thousand", "million", "billion", "trillion"}
)

func enTriple(n int64) []string {
	var w []string
	if h := n / 100; h > 0 {
		w = append(w, enOnes[h], "hundred")
	}
	switch r := n % 100; {
	case r >= 20 && r%10 > 0:
		w = append(w, enTens[r/10]+"-"+enOnes[r%10])
	case r >= 20:
		w = append(w, enTens[r/10])
	case r > 0:
		w = append(w, enOnes[r])
	}
	return w
}

// enNumber is the whole number in English words.
func enNumber(n int64) string {
	if n == 0 {
		return "zero"
	}
	var w []string
	if low := n % 1000; low > 0 {
		w = enTriple(low)
	}
	n /= 1000
	for i := 0; n > 0 && i < len(enScales); i++ {
		if t := n % 1000; t > 0 {
			w = append(append(enTriple(t), enScales[i]), w...)
		}
		n /= 1000
	}
	return strings.Join(w, " ")
}
