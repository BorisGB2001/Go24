package mail

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// «Личные контакты», as Outlook's «Контакты»: the user's own address book. In «Кому», «Копия» and «Скрытая» a contact is
// offered beside go24's employees and the CRM (marked «Личный контакт»); the address book of the letter shows them on
// their tab, where they are added, changed and deleted.

// Contact is an entry of the user's address book.
type Contact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Company string `json:"company"`
	Phone   string `json:"phone"`
}

// MaxContacts bounds the user's address book.
const MaxContacts = 5000

// Contacts are the user's contacts matching q (by name, address or company), by name.
func (s *Store) Contacts(ctx context.Context, userID, q string, limit int) ([]Contact, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(ctx, `SELECT id::text, name, email, company, phone FROM mail_contacts
		WHERE user_id::text = $1 AND (name ILIKE $2 OR email ILIKE $2 OR company ILIKE $2)
		ORDER BY lower(coalesce(nullif(name, ''), email)) LIMIT $3`, userID, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		var c Contact
		if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Company, &c.Phone); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveContact adds a contact to the user's address book (ID "": an address already there is changed instead) or
// changes one.
func (s *Store) SaveContact(ctx context.Context, userID string, c Contact) (Contact, error) {
	c.Name, c.Company, c.Phone = strings.TrimSpace(c.Name), strings.TrimSpace(c.Company), strings.TrimSpace(c.Phone)
	list, err := addresses(c.Email)
	if err != nil || len(list) != 1 {
		return c, i18n.E("err.mail_contact_email")
	}
	c.Email = list[0].Address
	if c.Name == "" {
		c.Name = list[0].Name
	}
	if utf8.RuneCountInString(c.Name) > 200 || utf8.RuneCountInString(c.Company) > 200 || utf8.RuneCountInString(c.Phone) > 50 {
		return c, i18n.E("err.mail_contact_long")
	}
	if c.ID == "" {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_contacts WHERE user_id::text = $1`, userID).Scan(&n); err != nil {
			return c, err
		}
		if n >= MaxContacts {
			return c, i18n.E("err.mail_contact_limit")
		}
		err = s.db.QueryRow(ctx, `INSERT INTO mail_contacts (user_id, name, email, company, phone) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (user_id, lower(email)) DO UPDATE SET name = EXCLUDED.name, company = EXCLUDED.company, phone = EXCLUDED.phone
			RETURNING id::text`, userID, c.Name, c.Email, c.Company, c.Phone).Scan(&c.ID)
		return c, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_contacts SET name = $3, email = $4, company = $5, phone = $6
		WHERE id::text = $1 AND user_id::text = $2`, c.ID, userID, c.Name, c.Email, c.Company, c.Phone)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		err = i18n.E("err.mail_contact_dup")
	case err == nil && tag.RowsAffected() == 0:
		err = ErrNotFound
	}
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
