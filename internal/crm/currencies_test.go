package crm

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/encoding/charmap"

	"go24/internal/db"
)

// TestParseCBR: the daily XML of the Central Bank gives roubles per unit, the nominal counted in.
func TestParseCBR(t *testing.T) {
	xml := `<?xml version="1.0" encoding="windows-1251"?>
<ValCurs Date="22.10.2026" name="Foreign Currency Market">
<Valute ID="R01235"><NumCode>840</NumCode><CharCode>USD</CharCode><Nominal>1</Nominal><Name>Доллар США</Name><Value>90,5000</Value></Valute>
<Valute ID="R01335"><NumCode>398</NumCode><CharCode>KZT</CharCode><Nominal>100</Nominal><Name>Тенге</Name><Value>18,2000</Value></Valute>
</ValCurs>`
	cp, err := charmap.Windows1251.NewEncoder().String(xml) // as the CBR sends it
	if err != nil {
		t.Fatal(err)
	}
	rub, err := ParseCBR(strings.NewReader(cp))
	if err != nil {
		t.Fatal(err)
	}
	if rub["RUB"] != 1 || rub["USD"] != 90.5 || math.Abs(rub["KZT"]-0.182) > 1e-9 {
		t.Fatalf("rates %v", rub)
	}
	if _, err := ParseCBR(strings.NewReader(`<ValCurs></ValCurs>`)); err == nil {
		t.Fatal("no rates parsed")
	}
}

// TestCurrencies: the list has the base RUB; a card's currency must be of the list (empty — the base one); the CBR
// rates go to the «auto» currencies only; the analytics counts in the base currency; making another currency the
// base one counts the rates anew; the base and a used currency are not deleted; catalog prices come in a card's
// currency.
func TestCurrencies(t *testing.T) {
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
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool)

	base, err := s.BaseCurrency(ctx)
	must(err)
	if base != "RUB" {
		t.Fatalf("base %q", base)
	}
	for _, bad := range []Currency{{Code: "US", Rate: 1}, {Code: "U$D", Rate: 1}, {Code: "GBP", Rate: 0}, {Code: "GBP", Rate: -2}} {
		if err := s.SaveCurrency(ctx, bad); err == nil {
			t.Fatalf("saved %+v", bad)
		}
	}
	must(s.SaveCurrency(ctx, Currency{Code: "usd", Rate: 90, Auto: true}))
	must(s.SaveCurrency(ctx, Currency{Code: "EUR", Rate: 100})) // set by hand: the CBR does not touch it
	must(s.SaveCurrency(ctx, Currency{Code: "RUB", Name: "Рубль", Rate: 5, Auto: true}))
	rates, err := s.Rates(ctx)
	must(err)
	if rates["RUB"] != 1 || rates["USD"] != 90 || rates["EUR"] != 100 {
		t.Fatalf("rates %v", rates)
	}

	pipes, err := s.Pipelines(ctx)
	must(err)
	p := pipes[0]
	won := p.Stages[len(p.Stages)-2].ID
	if _, err := s.SaveDeal(ctx, anna, Deal{Title: "x", PipelineID: p.ID, Amount: 1, Currency: "XYZ"}); err == nil {
		t.Fatal("deal in a currency not in the list")
	}
	if _, err := s.SaveLead(ctx, anna, Lead{Title: "x", Currency: "XYZ"}); err == nil {
		t.Fatal("lead in a currency not in the list")
	}
	if _, err := s.SaveProduct(ctx, anna, Product{Name: "x", Price: 1, Currency: "XYZ"}); err == nil {
		t.Fatal("product in a currency not in the list")
	}
	rubDeal, err := s.SaveDeal(ctx, anna, Deal{Title: "rub", PipelineID: p.ID, Amount: 1000})
	must(err)
	d, err := s.Deal(ctx, rubDeal)
	must(err)
	if d.Currency != "RUB" {
		t.Fatalf("default currency %q", d.Currency)
	}
	usdDeal, err := s.SaveDeal(ctx, anna, Deal{Title: "usd", PipelineID: p.ID, Amount: 10, Currency: "usd"})
	must(err)
	for _, id := range []string{rubDeal, usdDeal} {
		_, err = s.MoveDeal(ctx, anna, id, won)
		must(err)
	}

	// the CBR: USD is auto, EUR set by hand, KZT is not in the list
	n, err := s.UpdateRates(ctx, map[string]float64{"RUB": 1, "USD": 95, "EUR": 105, "KZT": 0.2})
	must(err)
	if rates, _ = s.Rates(ctx); n != 1 || rates["USD"] != 95 || rates["EUR"] != 100 {
		t.Fatalf("CBR: %d %v", n, rates)
	}
	rep, err := s.Analytics(ctx, "", p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	must(err)
	if rep.Currency != "RUB" || rep.Won != 2 || !near(rep.WonSum, 1000+10*95) {
		t.Fatalf("analytics in the base currency: %+v", rep)
	}
	if InBase(rates, 10, "USD") != 950 || InBase(rates, 10, "ABC") != 10 {
		t.Fatal("InBase")
	}

	// catalog prices in a deal's currency
	_, err = s.SaveProduct(ctx, anna, Product{Name: "Лицензия", Price: 9500})
	must(err)
	list, err := s.ProductsIn(ctx, "USD")
	must(err)
	if len(list) != 1 || list[0].Currency != "USD" || list[0].Price != 100 {
		t.Fatalf("catalog in USD: %+v", list)
	}

	// USD becomes the base: the rates are counted to it; the CBR rates too
	must(s.SetBaseCurrency(ctx, "USD"))
	rates, err = s.Rates(ctx)
	must(err)
	if base, _ = s.BaseCurrency(ctx); base != "USD" || rates["USD"] != 1 || !near(rates["RUB"], 1.0/95) || !near(rates["EUR"], 100.0/95) {
		t.Fatalf("new base %s: %v", base, rates)
	}
	_, err = s.UpdateRates(ctx, map[string]float64{"RUB": 1, "USD": 100, "EUR": 110})
	must(err)
	if rates, _ = s.Rates(ctx); !near(rates["RUB"], 0.01) || rates["USD"] != 1 {
		t.Fatalf("CBR to USD: %v", rates)
	}
	if rep, err = s.Analytics(ctx, "", p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); err != nil || rep.Currency != "USD" || !near(rep.WonSum, 10+1000*0.01) {
		t.Fatalf("analytics in USD: %+v %v", rep, err)
	}

	// deleting: not the base one, not a used one; an unused one goes
	if err := s.DeleteCurrency(ctx, "USD"); err == nil {
		t.Fatal("deleted the base currency")
	}
	if err := s.DeleteCurrency(ctx, "RUB"); err == nil {
		t.Fatal("deleted a used currency")
	}
	must(s.DeleteCurrency(ctx, "CNY"))
	if err := s.DeleteCurrency(ctx, "CNY"); err != ErrNotFound {
		t.Fatalf("deleted twice: %v", err)
	}
}
