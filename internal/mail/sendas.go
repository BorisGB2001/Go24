package mail

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Адреса отправителя», as Outlook's aliases with «Показать поле «От»» and the «Имя отправителя»: a mailbox
// sends with a name of its own (the user's by default) and may send from other addresses the server lets it use —
// its aliases, each with a name or not. One of them is the address a new letter is from; an answer goes from the
// address the letter came to. The SMTP envelope stays the mailbox's: the server checks it may send as the alias.

// MaxSendAs is how many other addresses a mailbox sends from.
const MaxSendAs = 10

// BoxSenders is a mailbox's «Адреса отправителя».
type BoxSenders struct {
	Name    string // «Имя отправителя» ("" — the user's)
	SendAs  string // the other addresses, "Имя <адрес>", comma-separated
	Default string // the address of a new letter ("" — the mailbox's own)
}

// FromAddress is an address a letter can go from.
type FromAddress struct {
	MailboxID, Email, Name string
	Default                bool // a new letter of its mailbox is from it
}

// MailboxSenders: the mailbox's «Адреса отправителя» (the user's own only).
func (s *Store) MailboxSenders(ctx context.Context, userID, mailboxID string) (BoxSenders, error) {
	var b BoxSenders
	err := s.db.QueryRow(ctx, `SELECT sender_name, send_as, send_default FROM mailboxes WHERE id::text = $1 AND user_id::text = $2`, mailboxID, userID).
		Scan(&b.Name, &b.SendAs, &b.Default)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return b, err
}

// SetMailboxSenders sets them: the addresses are checked (not the mailbox's own, none twice), the default is the
// mailbox's own or one of them.
func (s *Store) SetMailboxSenders(ctx context.Context, userID, mailboxID string, b BoxSenders) error {
	box, err := s.Mailbox(ctx, userID, mailboxID)
	if err != nil {
		return err
	}
	b.Name = clip(strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(b.Name)), 100)
	list, err := addresses(b.SendAs)
	if err != nil || len(list) > MaxSendAs {
		return i18n.E("err.mail_send_as", MaxSendAs)
	}
	seen := map[string]bool{strings.ToLower(box.Email): true}
	for _, a := range list {
		if seen[strings.ToLower(a.Address)] {
			return i18n.E("err.mail_send_as_twice", a.Address)
		}
		seen[strings.ToLower(a.Address)] = true
	}
	b.Default = strings.TrimSpace(b.Default)
	if strings.EqualFold(b.Default, box.Email) {
		b.Default = ""
	}
	if !seen[strings.ToLower(b.Default)] { // an address taken away: new letters are from the mailbox's own again
		b.Default = ""
	}
	tag, err := s.db.Exec(ctx, `UPDATE mailboxes SET sender_name = $3, send_as = $4, send_default = $5 WHERE id::text = $1 AND user_id::text = $2`,
		mailboxID, userID, b.Name, namedAddrs(list), strings.ToLower(b.Default))
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// namedAddrs writes addresses with their names as the user typed them (not encoded as a header).
func namedAddrs(list []*mail.Address) string {
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Address
		if name := strings.TrimSpace(a.Name); name != "" {
			if strings.ContainsAny(name, `,;"<>@()`) {
				name = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
			}
			out[i] = name + " <" + a.Address + ">"
		}
	}
	return strings.Join(out, ", ")
}

// FromAddresses are the addresses the user's letters can go from: each mailbox's own, then its aliases.
func (s *Store) FromAddresses(ctx context.Context, userID string) ([]FromAddress, error) {
	rows, err := s.db.Query(ctx, `SELECT m.id::text, m.email, m.sender_name, m.send_as, m.send_default, u.name
		FROM mailboxes m JOIN users u ON u.id = m.user_id WHERE m.user_id::text = $1 ORDER BY m.email`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FromAddress
	for rows.Next() {
		var id, email, name, sendAs, def, user string
		if err := rows.Scan(&id, &email, &name, &sendAs, &def, &user); err != nil {
			return nil, err
		}
		if name == "" {
			name = user
		}
		out = append(out, FromAddress{MailboxID: id, Email: email, Name: name, Default: def == ""})
		list, _ := addresses(sendAs)
		for _, a := range list {
			n := strings.TrimSpace(a.Name)
			if n == "" {
				n = name
			}
			out = append(out, FromAddress{MailboxID: id, Email: a.Address, Name: n, Default: strings.EqualFold(a.Address, def)})
		}
	}
	return out, rows.Err()
}

// sender is the address a letter of the mailbox goes from: the one given ("" — the mailbox's own) if the mailbox
// may send from it, with its name.
func (s *Store) sender(ctx context.Context, userID, mailboxID, addr string) (mail.Address, error) {
	list, err := s.FromAddresses(ctx, userID)
	if err != nil {
		return mail.Address{}, err
	}
	for _, x := range list { // the mailbox's own comes first of its addresses
		if x.MailboxID == mailboxID && (addr == "" || strings.EqualFold(x.Email, addr)) {
			return mail.Address{Name: x.Name, Address: x.Email}, nil
		}
	}
	return mail.Address{}, i18n.E("err.mail_from")
}
