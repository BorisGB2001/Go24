// Package anon replaces personal data in imported records: people's names get stable fake names,
// phone numbers and e-mail local parts are scrambled. Company names are left as is.
// The mapping is deterministic for a given seed, so re-running an import produces the same pseudonyms.
package anon

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Person is a real person from the source system.
type Person struct {
	ID         int
	First      string
	Last       string
	Middle     string
	Gender     string // "M", "F" or "" (guessed from the names)
	Email      string
	Login      string
	FakeFirst  string
	FakeLast   string
	FakeMiddle string
	FakeEmail  string
}

// FullName is "Фамилия Имя" — how go24 shows people.
func (p Person) FullName() string { return strings.TrimSpace(p.FakeLast + " " + p.FakeFirst) }

type Anonymizer struct {
	seed    string
	people  map[int]*Person
	words   map[string]string // lower-case stem (4+ letters) of a real name → fake stem
	exact   map[string]string // lower-case whole real name → fake name (for short names)
	emails  map[string]string // lower-case real e-mail → fake e-mail
	domain  string
	usedFIO map[string]bool
}

// New creates an anonymizer; fakeDomain is used for people's fake e-mails (e.g. "portal.import").
func New(seed, fakeDomain string) *Anonymizer {
	return &Anonymizer{seed: seed, people: map[int]*Person{}, words: map[string]string{}, exact: map[string]string{},
		emails: map[string]string{}, domain: fakeDomain, usedFIO: map[string]bool{}}
}

func (a *Anonymizer) rng(parts ...string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(a.seed))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	s := h.Sum64()
	return rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15))
}

// AddPerson registers a real person and assigns a fake identity of the same gender.
func (a *Anonymizer) AddPerson(p Person) *Person {
	if p.Gender == "" {
		p.Gender = guessGender(p.First, p.Last, p.Middle)
	}
	r := a.rng("person", fmt.Sprint(p.ID))
	same := func(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }
	for attempt := 0; ; attempt++ {
		if p.Gender == "F" {
			p.FakeFirst = pick(r, femaleNames, p.First)
			p.FakeLast = feminine(pick(r, surnames, ""))
			p.FakeMiddle = patronymics[r.IntN(len(patronymics))][1]
		} else {
			p.FakeFirst = pick(r, maleNames, p.First)
			p.FakeLast = pick(r, surnames, "")
			p.FakeMiddle = patronymics[r.IntN(len(patronymics))][0]
		}
		// a pseudonym part equal to the real one would leak it: draw again
		if (same(p.FakeLast, p.Last) || same(p.FakeMiddle, p.Middle)) && attempt < 50 {
			continue
		}
		key := p.FakeLast + " " + p.FakeFirst
		if !a.usedFIO[key] || attempt > 20 {
			a.usedFIO[key] = true
			break
		}
	}
	p.FakeEmail = fmt.Sprintf("user%d@%s", p.ID, a.domain)
	if p.Middle == "" {
		p.FakeMiddle = ""
	}
	a.people[p.ID] = &p

	a.addWord(p.Last, p.FakeLast)
	a.addWord(p.First, p.FakeFirst)
	a.addWord(p.Middle, p.FakeMiddle)
	if p.Email != "" {
		a.emails[strings.ToLower(p.Email)] = p.FakeEmail
	}
	return a.people[p.ID]
}

func (a *Anonymizer) Person(id int) (*Person, bool) {
	p, ok := a.people[id]
	return p, ok
}

// addWord remembers a real name so it is replaced inside free text, including Russian case endings.
func (a *Anonymizer) addWord(real, fake string) {
	real, fake = strings.TrimSpace(real), strings.TrimSpace(fake)
	if utf8.RuneCountInString(real) < 3 || fake == "" {
		return
	}
	a.exact[strings.ToLower(real)] = fake
	// Short stems ("Лев", "Анн") would hit ordinary words with endings ("левый"), so only 4+ letters.
	if st := stem(real); utf8.RuneCountInString(st) >= 4 {
		a.words[strings.ToLower(st)] = stem(fake)
	}
}

// stem cuts the inflectable ending: "Иванова" → "Иванов", "Сергей" → "Серге", "Ольга" → "Ольг".
func stem(w string) string {
	r := []rune(w)
	lower := strings.ToLower(w)
	for _, end := range []string{"ова", "ева", "ина", "ына"} {
		if strings.HasSuffix(lower, end) && len(r) > 4 {
			return string(r[:len(r)-1])
		}
	}
	if len(r) > 3 && strings.ContainsRune("аяйьоеиыу", unicode.ToLower(r[len(r)-1])) {
		return string(r[:len(r)-1])
	}
	return w
}

var (
	wordRe  = regexp.MustCompile(`\p{L}+`)
	emailRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@([a-z0-9\-]+\.)+[a-z]{2,}`)
	// Phone-like numbers: +7/8 and 10 digits with optional separators, bare 9xxxxxxxxx, local xx-xx-xx.
	phoneRe = regexp.MustCompile(`(?:\+7|\b8)[\s\-]?\(?\d{3}\)?[\s\-]?\d{3}[\s\-]?\d{2}[\s\-]?\d{2}\b|\b9\d{9}\b|\b\d{2}-\d{2}-\d{2}\b`)
)

// Text anonymizes free text: e-mails, phones and known people's names.
func (a *Anonymizer) Text(s string) string {
	s = emailRe.ReplaceAllStringFunc(s, a.Email)
	s = phoneRe.ReplaceAllStringFunc(s, a.Phone)
	return wordRe.ReplaceAllStringFunc(s, a.word)
}

func (a *Anonymizer) word(w string) string {
	lw := strings.ToLower(w)
	if fake, ok := a.exact[lw]; ok {
		return matchCase(w, fake)
	}
	r := []rune(lw)
	// try the longest known stem that leaves an ending of at most 3 letters
	for cut := 0; cut <= 3 && cut < len(r); cut++ {
		if fake, ok := a.words[string(r[:len(r)-cut])]; ok {
			ending := string([]rune(w)[len(r)-cut:])
			return matchCase(w, fake+ending)
		}
	}
	return w
}

// Email maps a known person's e-mail to their fake one, otherwise scrambles the local part and keeps the domain.
func (a *Anonymizer) Email(e string) string {
	if fake, ok := a.emails[strings.ToLower(e)]; ok {
		return fake
	}
	_, domain, _ := strings.Cut(e, "@")
	h := fnv.New32a()
	h.Write([]byte(a.seed + strings.ToLower(e)))
	return fmt.Sprintf("contact%06x@%s", h.Sum32()&0xffffff, domain)
}

// Phone keeps the format (separators, +7/8 prefix) and replaces the digits deterministically.
func (a *Anonymizer) Phone(p string) string {
	r := a.rng("phone", p)
	var b strings.Builder
	digits := 0
	keepPrefix := strings.HasPrefix(p, "+7") || strings.HasPrefix(p, "8")
	for _, c := range p {
		if c >= '0' && c <= '9' {
			digits++
			if keepPrefix && digits == 1 {
				b.WriteRune(c)
				continue
			}
			b.WriteRune(rune('0' + r.IntN(10)))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

func guessGender(first, last, middle string) string {
	l, m, f := strings.ToLower(last), strings.ToLower(middle), strings.ToLower(first)
	switch {
	case strings.HasSuffix(m, "вна") || strings.HasSuffix(m, "чна"):
		return "F"
	case strings.HasSuffix(m, "вич") || strings.HasSuffix(m, "ич"):
		return "M"
	case strings.HasSuffix(l, "ова") || strings.HasSuffix(l, "ева") || strings.HasSuffix(l, "ина") || strings.HasSuffix(l, "ая"):
		return "F"
	case strings.HasSuffix(f, "а") || strings.HasSuffix(f, "я"):
		return "F"
	}
	return "M"
}

// pick chooses a fake value; when like is given it prefers values with the same last letter, so declensions fit.
func pick(r *rand.Rand, list []string, like string) string {
	// never the real name itself: a pseudonym equal to the original would leak it
	real := strings.ToLower(strings.TrimSpace(like))
	var pool []string
	for _, v := range list {
		if strings.ToLower(v) != real {
			pool = append(pool, v)
		}
	}
	if lr := []rune(real); len(lr) > 0 {
		last := lr[len(lr)-1]
		var same []string
		for _, v := range pool {
			vr := []rune(v)
			if vr[len(vr)-1] == last {
				same = append(same, v)
			}
		}
		if len(same) > 0 {
			return same[r.IntN(len(same))]
		}
	}
	return pool[r.IntN(len(pool))]
}

func feminine(surname string) string {
	switch {
	case strings.HasSuffix(surname, "ский"):
		return strings.TrimSuffix(surname, "ий") + "ая"
	case strings.HasSuffix(surname, "ов"), strings.HasSuffix(surname, "ев"), strings.HasSuffix(surname, "ин"):
		return surname + "а"
	}
	return surname
}

// matchCase gives out the letter case of the original word: ИВАНОВ → БЕЛОВ, Иванов → Белов.
func matchCase(orig, out string) string {
	if utf8.RuneCountInString(orig) > 1 && strings.ToUpper(orig) == orig {
		return strings.ToUpper(out)
	}
	if r := []rune(orig); len(r) > 0 && unicode.IsLower(r[0]) {
		return strings.ToLower(out)
	}
	return out
}

var maleNames = []string{"Александр", "Алексей", "Андрей", "Антон", "Артём", "Борис", "Вадим", "Валерий", "Василий",
	"Виктор", "Виталий", "Владимир", "Глеб", "Григорий", "Денис", "Дмитрий", "Евгений", "Егор", "Иван", "Игорь", "Илья",
	"Кирилл", "Константин", "Леонид", "Максим", "Михаил", "Никита", "Николай", "Олег", "Павел", "Пётр", "Роман",
	"Сергей", "Станислав", "Степан", "Тимофей", "Фёдор", "Юрий", "Ярослав"}

var femaleNames = []string{"Алина", "Алла", "Анастасия", "Анна", "Валентина", "Вера", "Виктория", "Галина", "Дарья",
	"Евгения", "Екатерина", "Елена", "Жанна", "Зоя", "Ирина", "Карина", "Ксения", "Лариса", "Любовь", "Людмила",
	"Маргарита", "Марина", "Мария", "Надежда", "Наталья", "Нина", "Оксана", "Ольга", "Полина", "Светлана",
	"Софья", "Тамара", "Татьяна", "Юлия", "Яна"}

var surnames = []string{"Абрамов", "Белов", "Волков", "Воробьёв", "Гаврилов", "Голубев", "Громов", "Данилов", "Дроздов",
	"Ершов", "Жуков", "Зайцев", "Захаров", "Зимин", "Калинин", "Карпов", "Киселёв", "Ковалёв", "Комаров", "Крылов",
	"Кудрявцев", "Лебедев", "Макаров", "Медведев", "Миронов", "Морозов", "Никитин", "Орлов", "Осипов", "Панов",
	"Полянский", "Родионов", "Рябов", "Савельев", "Сафонов", "Соболев", "Субботин", "Тарасов", "Тихонов", "Уваров",
	"Филиппов", "Фомин", "Хохлов", "Цветков", "Чернов", "Шаров", "Щукин", "Яковлев"}

// patronymics: male / female forms.
var patronymics = [][2]string{{"Александрович", "Александровна"}, {"Алексеевич", "Алексеевна"}, {"Андреевич", "Андреевна"},
	{"Борисович", "Борисовна"}, {"Васильевич", "Васильевна"}, {"Викторович", "Викторовна"}, {"Владимирович", "Владимировна"},
	{"Дмитриевич", "Дмитриевна"}, {"Евгеньевич", "Евгеньевна"}, {"Иванович", "Ивановна"}, {"Игоревич", "Игоревна"},
	{"Константинович", "Константиновна"}, {"Михайлович", "Михайловна"}, {"Николаевич", "Николаевна"},
	{"Олегович", "Олеговна"}, {"Павлович", "Павловна"}, {"Петрович", "Петровна"}, {"Романович", "Романовна"},
	{"Сергеевич", "Сергеевна"}, {"Юрьевич", "Юрьевна"}}
