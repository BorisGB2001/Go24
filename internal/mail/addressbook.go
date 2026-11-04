package mail

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Личные контакты», as Outlook's «Контакты»: the user's own address book. In «Кому», «Копия» and «Скрытая» a contact is
// offered beside go24's employees and the CRM (marked «Личный контакт»); the address book of the letter shows them on
// their tab, where they are added, changed and deleted.

// Contact is an entry of the user's address book. A phone's contact may have no address (a phone number only).
type Contact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company"`
	Phone   string `json:"phone"`

	Href string `json:"-"` // the name of its resource in the CardDAV book (its program's; ID.vcf for one made in go24)
	UID  string `json:"-"` // the UID of its card
	Card string `json:"-"` // its card as the program sent it ("" — made from the fields)
}

// MaxContacts bounds the user's address book.
const MaxContacts = 5000

// VCard is the contact's card: the program's one, else made from its fields.
func (c Contact) VCard() string {
	if c.Card != "" {
		return c.Card
	}
	return MakeCard(c)
}

const contactCols = `id::text, name, email, company, phone, coalesce(href, id::text || '.vcf'),
	coalesce(uid, 'go24-contact-' || id::text), card`

func scanContacts(rows pgx.Rows) ([]Contact, error) {
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Company, &c.Phone, &c.Href, &c.UID, &c.Card); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Contacts are the user's contacts matching q (by name, address, company or phone), by name; all — those without an
// address too (recipients are only those with one).
func (s *Store) Contacts(ctx context.Context, userID, q string, limit int, all bool) ([]Contact, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+` FROM mail_contacts
		WHERE user_id::text = $1 AND (name ILIKE $2 OR email ILIKE $2 OR company ILIKE $2 OR phone ILIKE $2) AND ($4 OR email <> '')
		ORDER BY lower(coalesce(nullif(name, ''), nullif(email, ''), phone)) LIMIT $3`, userID, like, limit, all)
	if err != nil {
		return nil, err
	}
	return scanContacts(rows)
}

// ContactCards are all the contacts of the user with their cards, for the CardDAV book and «Экспорт в vCard».
func (s *Store) ContactCards(ctx context.Context, userID string) ([]Contact, error) {
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+` FROM mail_contacts WHERE user_id::text = $1 ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	return scanContacts(rows)
}

// cleanContact checks a contact's fields: one address or none, the lengths; a contact needs a name, an address or
// a phone.
func cleanContact(c Contact) (Contact, error) {
	c.Name, c.Company, c.Phone, c.Email = strings.TrimSpace(c.Name), strings.TrimSpace(c.Company), strings.TrimSpace(c.Phone), strings.TrimSpace(c.Email)
	if c.Email != "" {
		list, err := addresses(c.Email)
		if err != nil || len(list) != 1 {
			return c, i18n.E("err.mail_contact_email")
		}
		c.Email = list[0].Address
		if c.Name == "" {
			c.Name = list[0].Name
		}
	}
	if c.Name == "" && c.Email == "" && c.Phone == "" {
		return c, i18n.E("err.mail_contact_empty")
	}
	if utf8.RuneCountInString(c.Name) > 200 || utf8.RuneCountInString(c.Company) > 200 || utf8.RuneCountInString(c.Phone) > 50 {
		return c, i18n.E("err.mail_contact_long")
	}
	return c, nil
}

// contactRoom tells whether the user's address book has room for one more contact.
func (s *Store) contactRoom(ctx context.Context, userID string) error {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_contacts WHERE user_id::text = $1`, userID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxContacts {
		return i18n.E("err.mail_contact_limit")
	}
	return nil
}

// SaveContact adds a contact to the user's address book (ID "": an address already there is changed instead) or
// changes one. A card a program sent keeps its other fields (PatchCard).
func (s *Store) SaveContact(ctx context.Context, userID string, c Contact) (Contact, error) {
	c, err := cleanContact(c)
	if err != nil {
		return c, err
	}
	if c.ID == "" && c.Email != "" {
		err := s.db.QueryRow(ctx, `SELECT id::text, email FROM mail_contacts WHERE user_id::text = $1 AND email <> '' AND lower(email) = lower($2)
			ORDER BY created_at LIMIT 1`, userID, c.Email).Scan(&c.ID, &c.Email) // the address as it was written first
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return c, err
		}
	}
	if c.ID == "" {
		if err := s.contactRoom(ctx, userID); err != nil {
			return c, err
		}
		err = s.db.QueryRow(ctx, `INSERT INTO mail_contacts (user_id, name, email, company, phone) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, userID, c.Name, c.Email, c.Company, c.Phone).Scan(&c.ID)
		return c, err
	}
	if c.Email != "" {
		var dup bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mail_contacts WHERE user_id::text = $1 AND id::text <> $2
			AND email <> '' AND lower(email) = lower($3))`, userID, c.ID, c.Email).Scan(&dup); err != nil {
			return c, err
		}
		if dup {
			return c, i18n.E("err.mail_contact_dup")
		}
	}
	var card string
	if err := s.db.QueryRow(ctx, `SELECT card FROM mail_contacts WHERE id::text = $1 AND user_id::text = $2`, c.ID, userID).Scan(&card); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrNotFound
		}
		return c, err
	}
	if card != "" {
		card = PatchCard(card, c)
	}
	_, err = s.db.Exec(ctx, `UPDATE mail_contacts SET name = $3, email = $4, company = $5, phone = $6, card = $7, updated_at = now()
		WHERE id::text = $1 AND user_id::text = $2`, c.ID, userID, c.Name, c.Email, c.Company, c.Phone, card)
	return c, err
}

// DeleteContact deletes a contact of the user.
func (s *Store) DeleteContact(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_contacts WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// ---------- the address book over CardDAV: what a phone or a program writes ----------

// ContactByHref is the user's contact of the resource href of the CardDAV book.
func (s *Store) ContactByHref(ctx context.Context, userID, href string) (Contact, error) {
	rows, err := s.db.Query(ctx, `SELECT `+contactCols+` FROM mail_contacts
		WHERE user_id::text = $1 AND (href = $2 OR href IS NULL AND id::text || '.vcf' = $2)`, userID, href)
	if err != nil {
		return Contact{}, err
	}
	list, err := scanContacts(rows)
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Contact{}, err
	}
	return list[0], nil
}

// cardContact is the contact of a card a program sent or a file brought: its fields, too long ones cut, an address
// that is not one dropped (the card keeps it).
func cardContact(card string) (Contact, string, error) {
	c, uid, _ := ParseCard(card)
	if c.Email != "" {
		if list, err := addresses(c.Email); err != nil || len(list) != 1 {
			c.Email = ""
		}
	}
	c.Name, c.Company, c.Phone = cut(c.Name, 200), cut(c.Company, 200), cut(c.Phone, 50)
	if utf8.RuneCountInString(uid) > 300 {
		uid = ""
	}
	c, err := cleanContact(c)
	return c, uid, err
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// PutCard keeps the card a program put at href: it changes the contact there or adds one. created tells which.
func (s *Store) PutCard(ctx context.Context, userID, href, card string) (c Contact, created bool, err error) {
	if utf8.RuneCountInString(href) > 300 || len(SplitCards(card)) != 1 {
		return c, false, i18n.E("err.mail_card_bad")
	}
	card = SplitCards(card)[0]
	c, uid, err := cardContact(card)
	if err != nil {
		return c, false, err
	}
	c.Href, c.UID, c.Card = href, uid, card
	old, err := s.ContactByHref(ctx, userID, href)
	switch {
	case err == nil:
		c.ID = old.ID
		_, err = s.db.Exec(ctx, `UPDATE mail_contacts SET name = $3, email = $4, company = $5, phone = $6, href = $7, uid = nullif($8, ''),
			card = $9, updated_at = now() WHERE id::text = $1 AND user_id::text = $2`,
			c.ID, userID, c.Name, c.Email, c.Company, c.Phone, href, uid, card)
		return c, false, err
	case !errors.Is(err, ErrNotFound):
		return c, false, err
	}
	if err := s.contactRoom(ctx, userID); err != nil {
		return c, false, err
	}
	err = s.db.QueryRow(ctx, `INSERT INTO mail_contacts (user_id, name, email, company, phone, href, uid, card)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8) RETURNING id::text`,
		userID, c.Name, c.Email, c.Company, c.Phone, href, uid, card).Scan(&c.ID)
	return c, err == nil, err
}

// DeleteCard deletes the contact of the resource href.
func (s *Store) DeleteCard(ctx context.Context, userID, href string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_contacts WHERE user_id::text = $1 AND (href = $2 OR href IS NULL AND id::text || '.vcf' = $2)`,
		userID, href)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}
