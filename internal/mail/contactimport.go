package mail

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/encoding/charmap"

	"go24/internal/i18n"
)

// ---------- «Импорт» and «Экспорт» of the personal contacts, as Outlook's «Импорт и экспорт» of contacts ----------

// ContactsImported is what an import did.
type ContactsImported struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

// ImportContacts takes a file of contacts — a .vcf of a phone, Outlook, Google or iCloud, or a .csv Outlook or
// Google exported — into the user's address book. A contact whose address (or, without one, name and phone) is
// there already is changed, as Outlook's «Заменять дубликаты при импорте». A vCard 3.0 or 4.0 is kept whole, so the
// phone gets its other fields back.
func (s *Store) ImportContacts(ctx context.Context, userID, filename string, data []byte) (ContactsImported, error) {
	var res ContactsImported
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) { // Outlook writes its CSV in the system's code page
		if b, err := charmap.Windows1251.NewDecoder().Bytes(data); err == nil {
			data = b
		}
	}
	var list []Contact
	if bytes.Contains(bytes.ToUpper(data[:min(len(data), 4096)]), []byte("BEGIN:VCARD")) {
		for _, card := range SplitCards(string(data)) {
			c, uid, err := cardContact(card)
			if err != nil {
				res.Skipped++
				continue
			}
			if _, _, v := ParseCard(card); v == "3.0" || v == "4.0" {
				c.UID, c.Card = uid, card
			}
			list = append(list, c)
		}
	} else if strings.HasSuffix(strings.ToLower(filename), ".csv") || bytes.ContainsAny(data[:min(len(data), 4096)], ",;\t") {
		rows, skipped, err := csvContacts(data)
		if err != nil {
			return res, err
		}
		list, res.Skipped = rows, skipped
	}
	if len(list) == 0 && res.Skipped == 0 {
		return res, i18n.E("err.mail_contacts_file")
	}
	for _, c := range list {
		added, err := s.importContact(ctx, userID, c)
		switch {
		case err != nil && errors.As(err, new(*i18n.Error)):
			if res.Added+res.Updated > 0 || res.Skipped > 0 {
				res.Skipped++
				continue
			}
			return res, err
		case err != nil:
			return res, err
		case added:
			res.Added++
		default:
			res.Updated++
		}
	}
	return res, nil
}

// importContact adds the contact or changes the one it repeats.
func (s *Store) importContact(ctx context.Context, userID string, c Contact) (bool, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM mail_contacts WHERE user_id::text = $1
		AND CASE WHEN $2 <> '' THEN email <> '' AND lower(email) = lower($2) ELSE email = '' AND name = $3 AND phone = $4 END
		ORDER BY created_at LIMIT 1`, userID, c.Email, c.Name, c.Phone).Scan(&id)
	switch {
	case err == nil:
		_, err = s.db.Exec(ctx, `UPDATE mail_contacts SET name = $3, company = $4, phone = $5,
			card = CASE WHEN $6 <> '' THEN $6 ELSE card END, uid = CASE WHEN $6 <> '' THEN nullif($7, '') ELSE uid END, updated_at = now()
			WHERE id::text = $1 AND user_id::text = $2`, id, userID, c.Name, c.Company, c.Phone, c.Card, c.UID)
		return false, err
	case !errors.Is(err, pgx.ErrNoRows):
		return false, err
	}
	if err := s.contactRoom(ctx, userID); err != nil {
		return false, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO mail_contacts (user_id, name, email, company, phone, uid, card)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7)`, userID, c.Name, c.Email, c.Company, c.Phone, c.UID, c.Card)
	return err == nil, err
}

// csvFields are the columns of the contacts CSV of Outlook (English and Russian) and Google, by the field they fill,
// the better ones first.
var csvFields = map[string][]string{
	"name":    {"name", "full name", "display name", "полное имя", "имя для отображения", "отображаемое имя"},
	"first":   {"first name", "given name", "имя"},
	"middle":  {"middle name", "additional name", "отчество", "второе имя"},
	"last":    {"last name", "family name", "фамилия"},
	"email":   {"e-mail address", "email address", "e-mail 1 - value", "email", "e-mail", "адрес эл. почты", "электронная почта", "адрес электронной почты"},
	"company": {"company", "organization name", "organization 1 - name", "organization", "организация", "компания"},
	"phone": {"mobile phone", "phone 1 - value", "primary phone", "business phone", "home phone", "мобильный телефон",
		"основной телефон", "рабочий телефон", "домашний телефон", "телефон"},
}

// csvContacts reads the rows of a contacts CSV; skipped — rows that are no contact.
func csvContacts(data []byte) ([]Contact, int, error) {
	head, _, _ := bytes.Cut(data, []byte("\n"))
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	switch {
	case bytes.Count(head, []byte(";")) > bytes.Count(head, []byte(",")):
		r.Comma = ';'
	case bytes.Count(head, []byte("\t")) > bytes.Count(head, []byte(",")):
		r.Comma = '\t'
	}
	rows, err := r.ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, 0, i18n.E("err.mail_contacts_file")
	}
	col := map[string]int{}
	for f, names := range csvFields {
		for _, n := range names {
			for i, h := range rows[0] {
				if _, ok := col[f]; !ok && strings.EqualFold(strings.TrimSpace(h), n) {
					col[f] = i
				}
			}
		}
	}
	if _, ok := col["email"]; !ok {
		if _, ok := col["phone"]; !ok {
			return nil, 0, i18n.E("err.mail_contacts_file")
		}
	}
	var out []Contact
	skipped := 0
	for _, row := range rows[1:] {
		get := func(f string) string {
			if i, ok := col[f]; ok && i < len(row) {
				return strings.TrimSpace(row[i])
			}
			return ""
		}
		c := Contact{Name: get("name"), Email: get("email"), Company: get("company"), Phone: get("phone")}
		if c.Name == "" {
			c.Name = strings.Join(strings.Fields(get("first")+" "+get("middle")+" "+get("last")), " ")
		}
		if c.Email != "" {
			if list, err := addresses(c.Email); err != nil || len(list) != 1 {
				c.Email = ""
			}
		}
		c.Name, c.Company, c.Phone = cut(c.Name, 200), cut(c.Company, 200), cut(c.Phone, 50)
		c, err := cleanContact(c)
		if err != nil {
			skipped++
			continue
		}
		out = append(out, c)
	}
	return out, skipped, nil
}
