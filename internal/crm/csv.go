package crm

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go24/internal/i18n"
)

// Export and import of CSV: a list goes out as a table Excel opens (UTF-8 with BOM, «;»); contacts,
// companies and leads come in from a table whose first row names the columns (in Russian or English, the case and
// spaces don't matter). Rows like an existing card (by phone or e-mail) may be skipped.

// ImportColumns are the fields a column may fill, by the names people give the columns.
var ImportColumns = map[string]string{
	"name": "name", "имя": "name", "фио": "name", "контакт": "name", "contact": "name",
	"title": "title", "название": "title", "компания": "company", "company": "company", "организация": "company",
	"phone": "phone", "телефон": "phone", "мобильный": "phone", "email": "email", "e-mail": "email", "почта": "email",
	"position": "position", "должность": "position", "website": "website", "сайт": "website", "address": "address", "адрес": "address",
	"source": "origin", "источник": "origin", "source information": "source", "дополнительно об источнике": "source",
	"contact type": "contact_type", "тип контакта": "contact_type", "company type": "company_type", "тип компании": "company_type",
	"industry": "industry", "сфера деятельности": "industry", "amount": "amount", "сумма": "amount", "currency": "currency", "валюта": "currency",
	"comment": "comment", "комментарий": "comment", "примечание": "comment",
}

// ImportResult tells what an import did.
type ImportResult struct {
	Created, Updated, Skipped int
	Errors                    []ImportError // the first 50
}

// ImportError is a row that was not imported (the row's number in the table, from 2) and why.
type ImportError struct {
	Row int
	Err *i18n.Error
}

// ReadCSV reads a table: «;», «,» or a tab, whichever the first line has most of; a BOM is dropped.
func ReadCSV(r io.Reader) ([][]string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 10<<20))
	if err != nil {
		return nil, err
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	if !utf8.ValidString(text) {
		return nil, i18n.E("err.crm_csv_encoding")
	}
	first, _, _ := strings.Cut(text, "\n")
	sep := ';'
	for _, c := range []rune{',', '\t'} {
		if strings.Count(first, string(c)) > strings.Count(first, string(sep)) {
			sep = c
		}
	}
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma, cr.FieldsPerRecord, cr.LazyQuotes = sep, -1, true
	return cr.ReadAll()
}

// Import makes contacts, companies or leads from the table's rows; skipDup skips a row whose phone or e-mail an
// existing contact, company or lead has. A contact's «Компания» is found by title or made. «Источник» and the types
// are the items of the lists with that name, code or (tr: the locale) built-in name; an unknown source of a lead goes
// to «Дополнительно об источнике».
func (s *Store) Import(ctx context.Context, userID, entity string, table [][]string, skipDup bool, tr func(key string) string) (ImportResult, error) {
	var res ImportResult
	if entity != "contact" && entity != "company" && entity != "lead" {
		return res, i18n.E("err.bad_form")
	}
	if len(table) < 2 {
		return res, i18n.E("err.crm_csv_empty")
	}
	if len(table) > 10001 {
		return res, i18n.E("err.crm_csv_big")
	}
	cols := map[int]string{}
	for i, h := range table[0] {
		if f := ImportColumns[strings.ToLower(strings.TrimSpace(h))]; f != "" {
			cols[i] = f
		}
	}
	if len(cols) == 0 {
		return res, i18n.E("err.crm_csv_columns")
	}
	companies := map[string]string{} // title → id, made or found in this import
	dicts, err := s.Dicts(ctx)
	if err != nil {
		return res, err
	}
	item := func(kind, text string) Dict {
		for _, it := range dicts[kind] {
			if text != "" && (strings.EqualFold(text, it.Code) || strings.EqualFold(text, it.Name) || tr != nil && strings.EqualFold(text, it.Text(tr))) {
				return it.Dict
			}
		}
		return Dict{}
	}
	for n, rec := range table[1:] {
		v := map[string]string{}
		for i, f := range cols {
			if i < len(rec) {
				v[f] = strings.TrimSpace(rec[i])
			}
		}
		if strings.Join(rec, "") == "" {
			continue
		}
		if skipDup && (v["phone"] != "" || v["email"] != "") {
			dups, err := s.Duplicates(ctx, entity, "", "", v["phone"], v["email"])
			if err != nil {
				return res, err
			}
			if len(dups) > 0 {
				res.Skipped++
				continue
			}
		}
		var err error
		switch entity {
		case "contact":
			var co string
			if t := v["company"]; t != "" {
				if co = companies[strings.ToLower(t)]; co == "" {
					if err = s.db.QueryRow(ctx, `SELECT id::text FROM crm_companies WHERE lower(title) = lower($1) LIMIT 1`, t).Scan(&co); err != nil {
						co, err = s.SaveCompany(ctx, userID, Company{Title: t})
					}
					companies[strings.ToLower(t)] = co
				}
			}
			if err == nil {
				_, err = s.SaveContact(ctx, userID, Contact{Name: v["name"], Position: v["position"], Phone: v["phone"], Email: v["email"],
					Company: Ref{ID: co}, Comment: v["comment"], Origin: item("source", v["origin"]), ContactType: item("contact_type", v["contact_type"])})
			}
		case "company":
			title := v["title"]
			if title == "" {
				title = v["company"]
			}
			_, err = s.SaveCompany(ctx, userID, Company{Title: title, Phone: v["phone"], Email: v["email"], Website: v["website"],
				Address: v["address"], Comment: v["comment"], CompanyType: item("company_type", v["company_type"]), Industry: item("industry", v["industry"])})
		case "lead":
			title := v["title"]
			if title == "" {
				title = strings.TrimSpace(v["name"] + " " + v["company"])
			}
			origin, source := item("source", v["origin"]), v["source"]
			if origin.ID == "" && source == "" {
				source = v["origin"]
			}
			amount, _ := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(v["amount"], " ", ""), ",", "."), 64)
			_, err = s.SaveLead(ctx, userID, Lead{Title: title, Name: v["name"], CompanyTitle: v["company"], Phone: v["phone"],
				Email: v["email"], Source: source, Origin: origin, Amount: amount, Currency: v["currency"], Comment: v["comment"]})
		}
		if err != nil {
			var ie *i18n.Error
			if !errors.As(err, &ie) {
				return res, err
			}
			if len(res.Errors) < 50 {
				res.Errors = append(res.Errors, ImportError{n + 2, ie})
			}
			continue
		}
		res.Created++
	}
	return res, nil
}

// ExportName is the file's name: crm-deals-2026-10-21.csv.
func ExportName(kind string, now time.Time) string {
	return "crm-" + kind + "-" + now.Format(time.DateOnly) + ".csv"
}
