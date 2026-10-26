package payqr

import (
	"bytes"
	"image/png"
	"testing"
)

func TestPayQR(t *testing.T) {
	p := Payee{Name: "ООО «Ромашка» |", Account: "4070 2810 9000 0000 0001", Bank: "ПАО Сбербанк", BIK: "044525225",
		CorrAccount: "30101810400000000225", INN: "7701234567", KPP: "770101001"}
	if !p.Ok() || (Payee{Name: "x", Account: "40702", Bank: "b", BIK: "04x"}).Ok() {
		t.Fatal("Ok")
	}
	got := Text(p, 1234567, "Оплата по счёту № 15 от 26.10.2026")
	want := "ST00012|Name=ООО «Ромашка»|PersonalAcc=40702810900000000001|BankName=ПАО Сбербанк|BIC=044525225|" +
		"CorrespAcc=30101810400000000225|PayeeINN=7701234567|KPP=770101001|Sum=1234567|Purpose=Оплата по счёту № 15 от 26.10.2026"
	if got != want {
		t.Fatalf("text:\n%s\n%s", got, want)
	}
	if Text(Payee{Name: "a", Account: "1", Bank: "b", BIK: "2", CorrAccount: "3"}, 0, "") != "ST00012|Name=a|PersonalAcc=1|BankName=b|BIC=2|CorrespAcc=3" {
		t.Fatal("no sum")
	}
	for in, k := range map[string]int64{"12 345,67": 1234567, "1 000": 100000, "5,5": 550, "0": 0, "abc": 0, "1,234": 0, "−5": 0} {
		if Kopecks(in) != k {
			t.Fatalf("Kopecks(%q) = %d", in, Kopecks(in))
		}
	}
	data, err := PNG(got)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds().Dx() < 200 || img.Bounds().Dx() != img.Bounds().Dy() {
		t.Fatalf("png: %v %v", err, img.Bounds())
	}
}
