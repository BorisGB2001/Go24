package anon

import (
	"strings"
	"testing"
)

func setup() (*Anonymizer, *Person, *Person) {
	a := New("test-seed", "portal.import")
	m := a.AddPerson(Person{ID: 17, First: "Сергей", Last: "Кузнецов", Middle: "Петрович", Email: "kuznetsov@firm.ru"})
	f := a.AddPerson(Person{ID: 25, First: "Ольга", Last: "Смирнова", Email: "olga@firm.ru"})
	return a, m, f
}

func TestPeople(t *testing.T) {
	a, m, f := setup()
	if m.FakeLast == "Кузнецов" || m.FakeFirst == "Сергей" || m.FakeEmail != "user17@portal.import" {
		t.Fatalf("male not anonymized: %+v", m)
	}
	if !strings.HasSuffix(m.FakeMiddle, "ич") {
		t.Fatalf("male patronymic = %q", m.FakeMiddle)
	}
	if f.Gender != "F" || !strings.HasSuffix(f.FakeLast, "а") && !strings.HasSuffix(f.FakeLast, "ая") {
		t.Fatalf("female fake surname must be feminine: %+v", f)
	}
	if f.FakeMiddle != "" {
		t.Fatalf("no real patronymic → no fake one, got %q", f.FakeMiddle)
	}
	// deterministic
	b, m2, _ := setup()
	if m2.FullName() != m.FullName() || b.Text("Кузнецов") != a.Text("Кузнецов") {
		t.Fatal("same seed must give same pseudonyms")
	}
}

func TestText(t *testing.T) {
	a, m, f := setup()
	in := "Сергей, передай Кузнецову и Смирновой: звонить +7 (914) 123-45-67 или 8-924-111-22-33, " +
		"городской 62-00-01, моб 9141234567. Почта kuznetsov@firm.ru, клиент ivan@client.ru. " +
		"ООО «Байкал-Сервис» ИНН 2722000000. Сергеем доволен. Левый край, ОЛЬГА."
	out := a.Text(in)
	for _, leak := range []string{"Кузнецов", "Смирнов", "Сергей", "Сергеем", "Ольга", "ОЛЬГА",
		"914) 123-45-67", "924-111-22-33", "62-00-01", "9141234567", "kuznetsov@", "ivan@", "olga"} {
		if strings.Contains(out, leak) {
			t.Errorf("leak %q in %q", leak, out)
		}
	}
	for _, keep := range []string{"ООО «Байкал-Сервис»", "ИНН 2722000000", "client.ru", "Левый край", m.FakeEmail} {
		if !strings.Contains(out, keep) {
			t.Errorf("lost %q in %q", keep, out)
		}
	}
	if !strings.Contains(out, stem(m.FakeLast)+"у") || !strings.Contains(out, stem(f.FakeLast)+"ой") {
		t.Errorf("declension not kept: %q", out)
	}
	if !strings.Contains(out, "+7 (") || !strings.Contains(out, "8-") {
		t.Errorf("phone format not kept: %q", out)
	}
}

// TestNeverOwnName: across many people with the most common real names, no pseudonym part equals the real one.
func TestNeverOwnName(t *testing.T) {
	a := New("leak-check", "x.test")
	real := []Person{{First: "Александр", Last: "Иванов", Middle: "Сергеевич"}, {First: "Елена", Last: "Смирнова", Middle: "Александровна"},
		{First: "Сергей", Last: "Кузнецов", Middle: "Владимирович"}, {First: "Ольга", Last: "Попова", Middle: "Николаевна"}}
	for i := 0; i < 2000; i++ {
		r := real[i%len(real)]
		r.ID = i + 1
		p := a.AddPerson(r)
		if strings.EqualFold(p.FakeFirst, r.First) || strings.EqualFold(p.FakeLast, r.Last) || strings.EqualFold(p.FakeMiddle, r.Middle) {
			t.Fatalf("person %d kept a real name part: %s %s %s", i, p.FakeLast, p.FakeFirst, p.FakeMiddle)
		}
	}
}
