package mail

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The contact card, as Outlook's profile card: a click on the name of a letter's sender or recipient shows who the
// address is — an employee of go24, the user's own contact — and the latest letters with them in the user's mailboxes.

// PersonLetters: so many recent letters with the person the card shows.
const PersonLetters = 5

// PersonLetter is a recent letter with the person: from them, or to them (Outgoing).
type PersonLetter struct {
	ID, FolderID, Subject string
	SentAt                time.Time
	Outgoing, Seen        bool
}

// PersonCard is what the user's mail knows of an address.
type PersonCard struct {
	Email, Name string
	UserID      string   // an employee of go24 with the address ("" — none)
	Contact     *Contact // the user's personal contact with the address
	Letters     []PersonLetter
}

// PersonOf is the contact card of the address (a bare e-mail or «Name <e-mail>») for the user; ErrNotFound when it is
// not an address.
func (s *Store) PersonOf(ctx context.Context, userID, addr string) (PersonCard, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(addr))
	if err != nil {
		return PersonCard{}, ErrNotFound
	}
	p := PersonCard{Email: strings.ToLower(a.Address), Name: strings.TrimSpace(a.Name)}
	err = s.db.QueryRow(ctx, `SELECT id::text, name FROM users WHERE lower(email) = $1 AND active AND NOT guest
		ORDER BY created_at LIMIT 1`, p.Email).Scan(&p.UserID, &p.Name)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	var c Contact
	err = s.db.QueryRow(ctx, `SELECT id::text, name, email, company, phone FROM mail_contacts
		WHERE user_id::text = $1 AND lower(email) = $2 LIMIT 1`, userID, p.Email).Scan(&c.ID, &c.Name, &c.Email, &c.Company, &c.Phone)
	switch {
	case err == nil:
		p.Contact = &c
		if p.UserID == "" && c.Name != "" {
			p.Name = c.Name
		}
	case !errors.Is(err, pgx.ErrNoRows):
		return p, err
	}
	// the letters from the address, or to it among «Кому» and «Копия»: the ILIKE finds the candidates, the exact
	// address is checked here (ann@x.ru is not joann@x.ru)
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.folder_id::text, x.subject, x.sent_at, f.role IN ('sent', 'drafts'), x.seen,
			x.from_addr, x.from_name, x.to_addr, x.cc_addr
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE m.user_id::text = $1 AND f.role NOT IN ('trash', 'junk')
			AND (lower(x.from_addr) = $2 OR x.to_addr ILIKE $3 OR x.cc_addr ILIKE $3)
		ORDER BY x.sent_at DESC LIMIT 50`, userID, p.Email, likeOf(p.Email))
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var l PersonLetter
		var fromAddr, fromName, to, cc string
		if err := rows.Scan(&l.ID, &l.FolderID, &l.Subject, &l.SentAt, &l.Outgoing, &l.Seen, &fromAddr, &fromName, &to, &cc); err != nil {
			return p, err
		}
		from := strings.EqualFold(fromAddr, p.Email)
		if !from && !hasAddr(to, p.Email) && !hasAddr(cc, p.Email) {
			continue
		}
		if from && p.Name == "" {
			p.Name = strings.TrimSpace(fromName)
		}
		if len(p.Letters) < PersonLetters {
			p.Letters = append(p.Letters, l)
		}
	}
	return p, rows.Err()
}

// hasAddr: is the e-mail (lower case) among the addresses of the header?
func hasAddr(header, email string) bool {
	list, err := mail.ParseAddressList(header)
	if err != nil { // a header net/mail cannot read: the address as a word of it
		for _, w := range strings.FieldsFunc(strings.ToLower(header), func(r rune) bool { return strings.ContainsRune(" ,;<>\"'", r) }) {
			if w == email {
				return true
			}
		}
		return false
	}
	for _, a := range list {
		if strings.EqualFold(a.Address, email) {
			return true
		}
	}
	return false
}
