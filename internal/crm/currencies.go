package crm

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/encoding/charmap"

	"go24/internal/i18n"
)

// «Валюты» of the portal CRM: the currencies the company works in, one of them the base one. A card, a product
// and an invoice have a currency of the list; kanban sums and the analytics count in the base currency by the rates.
// The rates of the currencies marked «auto» come daily from the Central Bank of Russia; a rate set by hand stays.

// Currency of the list.
type Currency struct {
	Code, Name string
	Rate       float64 // what one unit costs in the base currency
	Base, Auto bool
	UpdatedAt  time.Time
}

// CBRURL is the daily rates of the Central Bank of Russia (roubles for a «Nominal» of units).
var CBRURL = "https://www.cbr.ru/scripts/XML_daily.asp"

// inBase is the SQL of an amount in the base currency.
func inBase(amount, currency string) string {
	return `(` + amount + ` * coalesce((SELECT cr.rate FROM crm_currencies cr WHERE cr.code = ` + currency + `), 1))`
}

func (s *Store) Currencies(ctx context.Context) ([]Currency, error) {
	rows, err := s.db.Query(ctx, `SELECT code, name, rate::float8, base, auto, updated_at FROM crm_currencies ORDER BY NOT base, sort, code`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Currency, error) {
		var c Currency
		err := r.Scan(&c.Code, &c.Name, &c.Rate, &c.Base, &c.Auto, &c.UpdatedAt)
		return c, err
	})
}

// BaseCurrency is the code of the base currency.
func (s *Store) BaseCurrency(ctx context.Context) (string, error) {
	var code string
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT code FROM crm_currencies WHERE base), 'RUB')`).Scan(&code)
	return code, err
}

// Rates are the rates of the currencies to the base one.
func (s *Store) Rates(ctx context.Context) (map[string]float64, error) {
	list, err := s.Currencies(ctx)
	rates := map[string]float64{}
	for _, c := range list {
		rates[c.Code] = c.Rate
	}
	return rates, err
}

// currency checks the currency of a card or a product: one of the list; empty is the base one.
func (s *Store) currency(ctx context.Context, code *string) error {
	if *code = strings.ToUpper(strings.TrimSpace(*code)); *code == "" {
		var err error
		*code, err = s.BaseCurrency(ctx)
		return err
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_currencies WHERE code = $1)`, *code).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return i18n.E("err.crm_currency")
	}
	return nil
}

// SaveCurrency adds a currency or changes its name and rate; a rate given by hand (auto off) stays as it is.
func (s *Store) SaveCurrency(ctx context.Context, c Currency) error {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	if len(c.Code) != 3 || strings.Trim(c.Code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return i18n.E("err.crm_currency_code")
	}
	if err := clean(&c.Name, 100); err != nil {
		return err
	}
	if !(c.Rate > 0 && c.Rate < 1e12) {
		return i18n.E("err.crm_currency_rate")
	}
	// the base currency's rate is always 1
	_, err := s.db.Exec(ctx, `INSERT INTO crm_currencies (code, name, rate, auto) VALUES ($1, $2, $3, $4)
		ON CONFLICT (code) DO UPDATE SET name = $2, rate = CASE WHEN crm_currencies.base THEN 1 ELSE $3 END, auto = $4, updated_at = now()`,
		c.Code, c.Name, c.Rate, c.Auto)
	return err
}

// SetBaseCurrency makes a currency the base one: the rates of all are counted anew to it.
func (s *Store) SetBaseCurrency(ctx context.Context, code string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var rate float64
		if err := tx.QueryRow(ctx, `SELECT rate::float8 FROM crm_currencies WHERE code = $1`, code).Scan(&rate); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE crm_currencies SET base = false WHERE base`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE crm_currencies SET rate = CASE WHEN code = $1 THEN 1 ELSE rate / $2::numeric END, base = (code = $1),
			updated_at = now()`, code, rate)
		return err
	})
}

// DeleteCurrency deletes a currency nothing has: not the base one, no card, product or invoice in it.
func (s *Store) DeleteCurrency(ctx context.Context, code string) error {
	var used bool
	if err := s.db.QueryRow(ctx, `SELECT base OR EXISTS (SELECT 1 FROM crm_deals WHERE currency = $1) OR EXISTS (SELECT 1 FROM crm_leads WHERE currency = $1)
		OR EXISTS (SELECT 1 FROM crm_products WHERE currency = $1) OR EXISTS (SELECT 1 FROM crm_invoices WHERE currency = $1)
		FROM crm_currencies WHERE code = $1`, code).Scan(&used); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if used {
		return i18n.E("err.crm_currency_used")
	}
	_, err := s.db.Exec(ctx, `DELETE FROM crm_currencies WHERE code = $1`, code)
	return err
}

// ParseCBR reads the daily rates of the Central Bank: roubles for one unit of each currency.
func ParseCBR(r io.Reader) (map[string]float64, error) {
	var doc struct {
		Valutes []struct {
			CharCode, Nominal, Value string
		} `xml:"Valute"`
	}
	d := xml.NewDecoder(r)
	d.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return charmap.Windows1251.NewDecoder().Reader(in), nil }
	if err := d.Decode(&doc); err != nil {
		return nil, err
	}
	rub := map[string]float64{"RUB": 1}
	for _, v := range doc.Valutes {
		value, err1 := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(v.Value), ",", "."), 64)
		nominal, err2 := strconv.ParseFloat(strings.TrimSpace(v.Nominal), 64)
		if err1 == nil && err2 == nil && value > 0 && nominal > 0 {
			rub[strings.TrimSpace(v.CharCode)] = value / nominal
		}
	}
	if len(rub) == 1 {
		return nil, fmt.Errorf("cbr: no rates")
	}
	return rub, nil
}

// UpdateRates sets the rates of the «auto» currencies from roubles per unit: counted to the base currency (it must be
// among them). Returns how many currencies got their rate.
func (s *Store) UpdateRates(ctx context.Context, rub map[string]float64) (int, error) {
	base, err := s.BaseCurrency(ctx)
	if err != nil {
		return 0, err
	}
	b, ok := rub[base]
	if !ok {
		return 0, nil
	}
	list, err := s.Currencies(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range list {
		r, ok := rub[c.Code]
		if !ok || !c.Auto || c.Base {
			continue
		}
		if _, err := s.db.Exec(ctx, `UPDATE crm_currencies SET rate = $2, updated_at = now() WHERE code = $1`, c.Code, r/b); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// FetchRates takes today's rates of the Central Bank, when the last ones are older than a day (or force).
func (s *Store) FetchRates(ctx context.Context, force bool) (int, error) {
	if !force {
		var due bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_currencies WHERE auto AND NOT base AND updated_at < now() - interval '1 day')`).Scan(&due); err != nil || !due {
			return 0, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CBRURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "go24") // the CBR answers 403 to Go's own
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("cbr: %s", resp.Status)
	}
	rub, err := ParseCBR(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	return s.UpdateRates(ctx, rub)
}

// ProductsIn is the catalog on sale with the prices in a card's currency, by the rates adds a product
// to a deal in another currency), by sections (the root's first) and names, for the picker.
func (s *Store) ProductsIn(ctx context.Context, currency string) ([]Product, error) {
	list, err := s.Products(ctx, "", false)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].SectionPath < list[j].SectionPath })
	rates, err := s.Rates(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		p := &list[i]
		if from, to := rates[p.Currency], rates[currency]; p.Currency != currency && from > 0 && to > 0 {
			p.Price, p.Currency = round2(p.Price*from/to), currency
		}
	}
	return list, nil
}

// InBase is an amount in the base currency by the rates (as it is when the currency has no rate).
func InBase(rates map[string]float64, amount float64, currency string) float64 {
	if r, ok := rates[currency]; ok {
		return amount * r
	}
	return amount
}

// RateText is the rate as people read it: up to four decimals.
func (c Currency) RateText() string { return strconv.FormatFloat(math.Round(c.Rate*10000)/10000, 'f', -1, 64) }
