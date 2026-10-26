// Package payqr makes the QR code of a payment by GOST R 56042-2014 (ST00012), as the invoices have it: the
// client's bank application reads the payee's details, the sum and the purpose from it and fills the payment.
package payqr

import (
	"strconv"
	"strings"

	"rsc.io/qr"
)

// Payee are the payee's details; Name, Account, Bank, BIK and CorrAccount are required.
type Payee struct {
	Name, Account, Bank, BIK, CorrAccount, INN, KPP string
}

// Ok: the details are enough for a payment.
func (p Payee) Ok() bool {
	return clean(p.Name) != "" && digits(p.Account) != "" && clean(p.Bank) != "" && digits(p.BIK) != "" && digits(p.CorrAccount) != ""
}

// clean drops the separator of the fields and the line breaks.
func clean(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("|", " ").Replace(s)), " ")
}

// digits are the digits of an account or a code, spaces dropped; "" — something else is in it.
func digits(s string) string {
	s = strings.Join(strings.Fields(s), "")
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}

// Text is the payment's text: kopecks — the sum (0 — the payer enters it), purpose — its purpose.
func Text(p Payee, kopecks int64, purpose string) string {
	f := []string{"ST00012", "Name=" + clean(p.Name), "PersonalAcc=" + digits(p.Account), "BankName=" + clean(p.Bank),
		"BIC=" + digits(p.BIK), "CorrespAcc=" + digits(p.CorrAccount)}
	if v := digits(p.INN); v != "" {
		f = append(f, "PayeeINN="+v)
	}
	if v := digits(p.KPP); v != "" {
		f = append(f, "KPP="+v)
	}
	if kopecks > 0 {
		f = append(f, "Sum="+strconv.FormatInt(kopecks, 10))
	}
	if v := clean(purpose); v != "" {
		f = append(f, "Purpose="+v)
	}
	return strings.Join(f, "|")
}

// PNG is the picture of the QR code of the text, with the quiet zone around it.
func PNG(text string) ([]byte, error) {
	c, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	c.Scale = 6
	return c.PNG(), nil
}

// Kopecks reads a sum as the documents write it («12 345,67», «1 000»); 0 — not a sum.
func Kopecks(s string) int64 {
	s = strings.NewReplacer(" ", "", " ", "", " ", "", ",", ".").Replace(strings.TrimSpace(s))
	whole, frac, _ := strings.Cut(s, ".")
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 || len(frac) > 2 {
		return 0
	}
	frac += strings.Repeat("0", 2-len(frac))
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0
	}
	return w*100 + f
}
