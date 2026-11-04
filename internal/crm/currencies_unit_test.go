package crm

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

func TestInBase(t *testing.T) {
	rates := map[string]float64{
		"RUB": 1.0,
		"USD": 95.5,
		"EUR": 105.2,
	}

	tests := []struct {
		name     string
		rates    map[string]float64
		amount   float64
		currency string
		want     float64
	}{
		{
			name:     "base currency RUB",
			rates:    rates,
			amount:   100,
			currency: "RUB",
			want:     100,
		},
		{
			name:     "conversion from USD",
			rates:    rates,
			amount:   10,
			currency: "USD",
			want:     955,
		},
		{
			name:     "unknown currency keeps amount as is",
			rates:    rates,
			amount:   250,
			currency: "GBP",
			want:     250,
		},
		{
			name:     "nil rates map keeps amount",
			rates:    nil,
			amount:   500,
			currency: "USD",
			want:     500,
		},
		{
			name:     "zero amount",
			rates:    rates,
			amount:   0,
			currency: "USD",
			want:     0,
		},
		{
			name:     "negative amount converted",
			rates:    rates,
			amount:   -20,
			currency: "EUR",
			want:     -2104,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := InBase(tt.rates, tt.amount, tt.currency)
			if math.Abs(got-tt.want) > 1e-6 {
				t.Errorf("InBase(%v, %v, %q) = %v, want %v", tt.rates, tt.amount, tt.currency, got, tt.want)
			}
		})
	}
}

func TestCurrencyRateText(t *testing.T) {
	tests := []struct {
		name string
		c    Currency
		want string
	}{
		{
			name: "exact 1",
			c:    Currency{Rate: 1},
			want: "1",
		},
		{
			name: "two decimal places",
			c:    Currency{Rate: 95.5},
			want: "95.5",
		},
		{
			name: "four decimal places",
			c:    Currency{Rate: 0.6432},
			want: "0.6432",
		},
		{
			name: "rounds to four decimal places",
			c:    Currency{Rate: 12.34567},
			want: "12.3457",
		},
		{
			name: "very small rate rounds to zero",
			c:    Currency{Rate: 0.00004},
			want: "0",
		},
		{
			name: "zero rate",
			c:    Currency{Rate: 0},
			want: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.RateText(); got != tt.want {
				t.Errorf("Currency{%v}.RateText() = %q, want %q", tt.c.Rate, got, tt.want)
			}
		})
	}
}

func TestParseCBR_Unit(t *testing.T) {
	xmlData := `<?xml version="1.0" encoding="windows-1251"?>
<ValCurs Date="28.10.2026" name="Foreign Currency Market">
    <Valute ID="R01235">
        <NumCode>840</NumCode>
        <CharCode>USD</CharCode>
        <Nominal>1</Nominal>
        <Name>Доллар США</Name>
        <Value>96,5000</Value>
    </Valute>
    <Valute ID="R01820">
        <NumCode>392</NumCode>
        <CharCode>JPY</CharCode>
        <Nominal>100</Nominal>
        <Name>Японских иен</Name>
        <Value>64,3200</Value>
    </Valute>
    <Valute ID="R01999">
        <CharCode>BAD1</CharCode>
        <Nominal>0</Nominal>
        <Value>10,00</Value>
    </Valute>
    <Valute ID="R01998">
        <CharCode>BAD2</CharCode>
        <Nominal>1</Nominal>
        <Value>-5,00</Value>
    </Valute>
</ValCurs>`

	encoder := charmap.Windows1251.NewEncoder()
	win1251Bytes, err := encoder.Bytes([]byte(xmlData))
	if err != nil {
		t.Fatalf("failed to encode windows-1251: %v", err)
	}

	rates, err := ParseCBR(bytes.NewReader(win1251Bytes))
	if err != nil {
		t.Fatalf("ParseCBR failed: %v", err)
	}

	if rates["RUB"] != 1.0 {
		t.Errorf("rates[RUB] = %v, want 1.0", rates["RUB"])
	}
	if math.Abs(rates["USD"]-96.5) > 1e-6 {
		t.Errorf("rates[USD] = %v, want 96.5", rates["USD"])
	}
	if math.Abs(rates["JPY"]-0.6432) > 1e-6 {
		t.Errorf("rates[JPY] = %v, want 0.6432", rates["JPY"])
	}
	if _, ok := rates["BAD1"]; ok {
		t.Errorf("expected zero-nominal currency BAD1 to be skipped")
	}
	if _, ok := rates["BAD2"]; ok {
		t.Errorf("expected negative-value currency BAD2 to be skipped")
	}

	// Error path: invalid XML
	if _, err := ParseCBR(strings.NewReader("<invalid XML")); err == nil {
		t.Error("ParseCBR on invalid XML: expected error, got nil")
	}

	// Error path: no rates found (only RUB default)
	noRatesXML := `<?xml version="1.0" encoding="windows-1251"?><ValCurs></ValCurs>`
	winBytes, _ := encoder.Bytes([]byte(noRatesXML))
	if _, err := ParseCBR(bytes.NewReader(winBytes)); err == nil {
		t.Error("ParseCBR with no valutes: expected error, got nil")
	}
}

func TestInBaseSQL(t *testing.T) {
	sql := inBase("d.amount", "d.currency")
	want := `(d.amount * coalesce((SELECT cr.rate FROM crm_currencies cr WHERE cr.code = d.currency), 1))`
	if sql != want {
		t.Errorf("inBase() = %q, want %q", sql, want)
	}
}
