package paysys

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestTBankToken: the plain fields and the password, ordered by the names; nested objects and the token are not signed.
func TestTBankToken(t *testing.T) {
	fields := map[string]any{"TerminalKey": "MerchantTerminalKey", "Amount": int64(19200), "OrderId": "21090",
		"Description": "Подарочная карта", "Receipt": map[string]any{"Email": "a@b"}, "Token": "x", "Success": true}
	sum := sha256.Sum256([]byte("19200Подарочная карта21090usaf8fw8fsw21gtrueMerchantTerminalKey"))
	if got := tbankToken(fields, "usaf8fw8fsw21g"); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("token %s", got)
	}
}

// TestPayments: a payment is started at ЮKassa and at Т-Банк as each wants it, a refusal is the error and a failed
// payment; the check sees a payment paid once only, a sum other than asked is no payment; the notices name the
// payment, a Т-Банк notice with a wrong token is none; the secrets are sealed.
func TestPayments(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	c := crm.NewStore(pool)
	pipes, err := c.Pipelines(ctx)
	must(err)
	deal, err := c.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID})
	must(err)
	_, err = c.SetProductRows(ctx, anna, "deal", deal, []crm.ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 3}})
	must(err)
	inv, err := c.MakeInvoice(ctx, anna, "invoice", deal, 5)
	must(err)

	s := NewStore(pool, "secret")
	var calls []Request
	answer := map[string]string{} // URL → body
	s.Call = func(_ context.Context, r Request) (Response, error) {
		calls = append(calls, r)
		for u, body := range answer {
			if strings.HasSuffix(r.URL, u) {
				return Response{200, []byte(body)}, nil
			}
		}
		return Response{500, []byte("no answer")}, nil
	}
	if _, err := s.SaveSystem(ctx, System{Kind: "yookassa", Name: "ЮKassa", Login: "shop1"}, ""); err != errSystem {
		t.Fatalf("a new service without its key: %v", err)
	}
	yoo, err := s.SaveSystem(ctx, System{Kind: "yookassa", Name: "ЮKassa", Login: "shop1", Active: true}, "live_KEY")
	must(err)
	tb, err := s.SaveSystem(ctx, System{Kind: "tbank", Name: "Т-Банк", Login: "TERM", Active: true}, "PASS")
	must(err)
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT secret FROM pay_systems WHERE id = $1`, yoo).Scan(&sealed))
	if strings.Contains(string(sealed), "live_KEY") {
		t.Fatal("the key is sealed")
	}
	list, err := s.Active(ctx)
	must(err)
	if len(list) != 2 || list[0].Name != "ЮKassa" || !list[0].HasKey {
		t.Fatalf("active %+v", list)
	}

	// ЮKassa: the payment with the sum as a string, the shop's authorization and the payment's own idempotence key
	answer[YooKassaURL] = `{"id":"y-1","status":"pending","confirmation":{"type":"redirect","confirmation_url":"https://yoomoney.ru/checkout?id=y-1"}}`
	p, err := s.Start(ctx, yoo, Start{InvoiceID: inv, Amount: 2999.999, Currency: "RUB", Description: "Счёт № 1", ReturnURL: "https://go24/q/T?pay={id}"})
	must(err)
	last := calls[len(calls)-1]
	body, _ := json.Marshal(last.JSON)
	if p.URL != "https://yoomoney.ru/checkout?id=y-1" || p.ExtID != "y-1" || last.User != "shop1" || last.Pass != "live_KEY" ||
		last.Header["Idempotence-Key"] != p.ID || !strings.Contains(string(body), `"value":"3000.00"`) || !strings.Contains(string(body), "https://go24/q/T?pay="+p.ID) {
		t.Fatalf("ЮKassa payment %+v %+v %s", p, last, body)
	}
	answer[YooKassaURL+"/y-1"] = `{"id":"y-1","status":"pending","amount":{"value":"3000.00","currency":"RUB"}}`
	if q, paid, err := s.Check(ctx, p.ID); err != nil || paid || q.Status != "pending" {
		t.Fatalf("pending: %+v %v %v", q, paid, err)
	}
	answer[YooKassaURL+"/y-1"] = `{"id":"y-1","status":"succeeded","paid":true,"amount":{"value":"3000.00","currency":"RUB"}}`
	if q, paid, err := s.Check(ctx, p.ID); err != nil || !paid || q.Status != "succeeded" || q.PaidAt == nil {
		t.Fatalf("paid: %+v %v %v", q, paid, err)
	}
	if _, paid, err := s.Check(ctx, p.ID); err != nil || paid {
		t.Fatalf("paid once only: %v %v", paid, err)
	}
	if ext, err := s.Notice(ctx, yoo, []byte(`{"type":"notification","event":"payment.succeeded","object":{"id":"y-1"}}`)); err != nil || ext != "y-1" {
		t.Fatalf("ЮKassa notice %q %v", ext, err)
	}
	if q, err := s.ByExternal(ctx, yoo, "y-1"); err != nil || q.ID != p.ID {
		t.Fatalf("by the service's id %+v %v", q, err)
	}

	// a refusal: the service's words, the payment failed with them
	delete(answer, YooKassaURL)
	if _, err := s.Start(ctx, yoo, Start{InvoiceID: inv, Amount: 10, Currency: "RUB"}); err == nil || err.(*i18n.Error).Key != "err.pay_failed" {
		t.Fatalf("refused: %v", err)
	}
	ps, err := s.Payments(ctx, inv)
	must(err)
	if len(ps) != 2 || ps[0].Status != "canceled" || !strings.Contains(ps[0].Error, "no answer") {
		t.Fatalf("the refused payment %+v", ps)
	}

	// Т-Банк: the sum in kopecks, the order is the payment, the request signed; another sum paid is no payment
	answer["Init"] = `{"Success":true,"ErrorCode":"0","PaymentId":7001,"PaymentURL":"https://securepay.tinkoff.ru/new/abc"}`
	p, err = s.Start(ctx, tb, Start{InvoiceID: inv, Amount: 3000, Currency: "RUB", ReturnURL: "https://go24/q/T?pay={id}", NotifyURL: "https://go24/pay/x/notify"})
	must(err)
	init := calls[len(calls)-1].JSON.(map[string]any)
	want := tbankToken(map[string]any{"TerminalKey": "TERM", "Amount": int64(300000), "OrderId": p.ID, "Description": "",
		"SuccessURL": "https://go24/q/T?pay=" + p.ID, "FailURL": "https://go24/q/T?pay=" + p.ID, "NotificationURL": "https://go24/pay/x/notify"}, "PASS")
	if p.ExtID != "7001" || p.URL != "https://securepay.tinkoff.ru/new/abc" || init["Amount"] != int64(300000) || init["Token"] != want {
		t.Fatalf("Т-Банк payment %+v %+v", p, init)
	}
	answer["GetState"] = `{"Success":true,"ErrorCode":"0","Status":"CONFIRMED","PaymentId":"7001","Amount":100}`
	if q, paid, err := s.Check(ctx, p.ID); err != nil || paid || q.Status != "canceled" {
		t.Fatalf("another sum: %+v %v %v", q, paid, err)
	}
	notice := map[string]any{"TerminalKey": "TERM", "OrderId": p.ID, "Success": true, "Status": "CONFIRMED", "PaymentId": json.Number("7001"), "Amount": json.Number("300000")}
	notice["Token"] = tbankToken(notice, "PASS")
	b, _ := json.Marshal(notice)
	if ext, err := s.Notice(ctx, tb, b); err != nil || ext != "7001" {
		t.Fatalf("Т-Банк notice %q %v", ext, err)
	}
	notice["Token"] = tbankToken(notice, "WRONG")
	b, _ = json.Marshal(notice)
	if _, err := s.Notice(ctx, tb, b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a forged notice: %v", err)
	}

	// switched off — no payments through it
	_, err = s.SaveSystem(ctx, System{ID: tb, Kind: "tbank", Name: "Т-Банк", Login: "TERM"}, "")
	must(err)
	if _, err := s.Start(ctx, tb, Start{InvoiceID: inv, Amount: 1, Currency: "RUB"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an inactive service: %v", err)
	}
	must(s.DeleteSystem(ctx, tb))
	if list, _ := s.Systems(ctx); len(list) != 1 {
		t.Fatalf("deleted: %+v", list)
	}
}
