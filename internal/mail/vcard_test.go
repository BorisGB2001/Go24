package mail

import (
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// ParseCard reads the cards of iPhone (groups, types, folded lines), Android (vCard 4.0, no FN) and old Outlook
// (vCard 2.1 in quoted-printable, a code page): the name, the preferred address, a mobile phone first, the company,
// the UID.
func TestParseCard(t *testing.T) {
	iphone := "BEGIN:VCARD\r\nVERSION:3.0\r\nPRODID:-//Apple Inc.//iPhone OS 18.0//EN\r\nN:Сидоров;Пётр;Ильич;;\r\n" +
		"FN:Пётр Ильич Сидоров\r\nORG:ИП Сидоров\\, и партнёры;Отдел\r\nitem1.EMAIL;type=INTERNET;type=HOME:home@ip.test\r\n" +
		"item2.EMAIL;type=INTERNET;type=WORK;type=pref:work@ip.test\r\nTEL;type=WORK;type=VOICE:+7 495 000-00-00\r\n" +
		"TEL;type=CELL;type=VOICE:+7 900 111-2\r\n 2-33\r\nUID:ABC-1\r\nPHOTO;ENCODING=b;TYPE=JPEG:AAAA\r\nEND:VCARD\r\n"
	c, uid, v := ParseCard(iphone)
	if c.Name != "Пётр Ильич Сидоров" || c.Email != "work@ip.test" || c.Phone != "+7 900 111-22-33" || c.Company != "ИП Сидоров, и партнёры" ||
		uid != "ABC-1" || v != "3.0" {
		t.Fatalf("iphone = %+v %q %q", c, uid, v)
	}
	android := "BEGIN:VCARD\nVERSION:4.0\nN:Иванова;Анна;;;\nEMAIL:mailto:anna@x.test\nTEL;VALUE=uri;TYPE=\"voice,home\":tel:+7-1\nEND:VCARD\n"
	if c, _, v := ParseCard(android); c.Name != "Анна Иванова" || c.Email != "anna@x.test" || c.Phone != "+7-1" || v != "4.0" {
		t.Fatalf("android = %+v %q", c, v)
	}
	qp, _ := charmap.Windows1251.NewEncoder().String("Михаил Петров")
	enc := ""
	for _, b := range []byte(qp) {
		enc += "=" + strings.ToUpper(string("0123456789abcdef"[b>>4])+string("0123456789abcdef"[b&15]))
	}
	outlook := "BEGIN:VCARD\r\nVERSION:2.1\r\nFN;CHARSET=windows-1251;ENCODING=QUOTED-PRINTABLE:" + enc[:30] + "=\r\n" + enc[30:] +
		"\r\nTEL;WORK;VOICE:111\r\nTEL;CELL;VOICE:222\r\nEMAIL;PREF;INTERNET:m@x.test\r\nEND:VCARD\r\n"
	if c, _, v := ParseCard(outlook); c.Name != "Михаил Петров" || c.Phone != "222" || c.Email != "m@x.test" || v != "2.1" {
		t.Fatalf("outlook = %+v %q", c, v)
	}
	if got := SplitCards("\ufeff" + iphone + "junk\n" + android + outlook); len(got) != 3 || !strings.Contains(got[0], "+7 900 111-22-33") {
		t.Fatalf("split = %d %q", len(got), got)
	}
}

// MakeCard writes a card every program reads back the same; PatchCard puts the fields changed in go24 into a
// program's card and keeps the rest.
func TestMakeAndPatchCard(t *testing.T) {
	c := Contact{UID: "go24-contact-1", Name: "Пётр Сидоров", Email: "sid@ip.test", Company: "ИП; Сидоров", Phone: "+7 900"}
	card := MakeCard(c)
	if !strings.Contains(card, "N:Сидоров;Пётр;;;\r\n") || !strings.Contains(card, "ORG:ИП\\; Сидоров\r\n") {
		t.Fatalf("card = %q", card)
	}
	if got, uid, _ := ParseCard(card); got.Name != c.Name || got.Email != c.Email || got.Company != c.Company || got.Phone != c.Phone || uid != c.UID {
		t.Fatalf("read back = %+v %q", got, uid)
	}
	if MakeCard(Contact{Phone: "+1"}) == "" || !strings.Contains(MakeCard(Contact{Phone: "+1"}), "FN:+1\r\n") {
		t.Fatal("a contact of a phone only has no FN")
	}

	phone := "BEGIN:VCARD\r\nVERSION:3.0\r\nN:Сидоров;Пётр;;;\r\nFN:Пётр Сидоров\r\nitem1.EMAIL;type=INTERNET;type=pref:sid@ip.test\r\n" +
		"EMAIL;type=HOME:other@ip.test\r\nTEL;type=CELL:+7 900\r\nTEL;type=HOME:+7 495\r\nPHOTO;ENCODING=b;TYPE=JPEG:QUFB\r\nUID:U1\r\nEND:VCARD\r\n"
	got := PatchCard(phone, Contact{Name: "Пётр Сидоров", Email: "new@ip.test", Company: "ООО Ромашка", Phone: "+7 901"})
	for _, w := range []string{"FN:Пётр Сидоров", "item1.EMAIL;type=INTERNET;type=pref:new@ip.test", "EMAIL;type=HOME:other@ip.test",
		"TEL;type=CELL:+7 901", "TEL;type=HOME:+7 495", "PHOTO;ENCODING=b;TYPE=JPEG:QUFB", "UID:U1", "ORG:ООО Ромашка\r\nEND:VCARD"} {
		if !strings.Contains(got, w) {
			t.Errorf("patched has no %q: %q", w, got)
		}
	}
	if strings.Contains(got, "sid@ip.test") || strings.Contains(got, "+7 900\r") {
		t.Errorf("old values stay: %q", got)
	}
	renamed := PatchCard(phone, Contact{Name: "Пётр Ильич", Email: "sid@ip.test", Phone: ""})
	if !strings.Contains(renamed, "N:Ильич;Пётр;;;\r\nFN:Пётр Ильич") || strings.Count(renamed, "FN:") != 1 || strings.Contains(renamed, "TEL;type=CELL") {
		t.Errorf("renamed = %q", renamed)
	}
	if c, _, _ := ParseCard(renamed); c.Phone != "+7 495" {
		t.Errorf("the other phone is not left: %+v", c)
	}
}

// csvContacts reads Outlook's CSV (Russian, semicolons, the names apart) and Google's.
func TestCSVContacts(t *testing.T) {
	outlook := "\"Имя\";\"Отчество\";\"Фамилия\";\"Организация\";\"Мобильный телефон\";\"Адрес эл. почты\"\r\n" +
		"\"Анна\";\"\";\"Иванова\";\"ООО Ромашка\";\"+7 900\";\"anna@x.test\"\r\n\"\";\"\";\"\";\"\";\"\";\"\"\r\n" +
		"\"Борис\";\"\";\"\";\"\";\"+7 901\";\"not mail\"\r\n"
	list, skipped, err := csvContacts([]byte(outlook))
	if err != nil || skipped != 1 || len(list) != 2 || list[0] != (Contact{Name: "Анна Иванова", Email: "anna@x.test", Company: "ООО Ромашка", Phone: "+7 900"}) ||
		list[1].Email != "" || list[1].Phone != "+7 901" {
		t.Fatalf("outlook = %+v %d %v", list, skipped, err)
	}
	google := "First Name,Last Name,Organization Name,E-mail 1 - Value,Phone 1 - Value\nJohn,Smith,Acme,john@acme.test,+1 555\n"
	if list, _, err := csvContacts([]byte(google)); err != nil || len(list) != 1 || list[0].Name != "John Smith" || list[0].Company != "Acme" {
		t.Fatalf("google = %+v %v", list, err)
	}
	if _, _, err := csvContacts([]byte("a,b\n1,2\n")); err == nil {
		t.Fatal("a CSV without contacts passed")
	}
}
