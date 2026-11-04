package mail

import (
	"context"
	"errors"
	"html"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Слияние», as Outlook's Mail Merge: a draft goes to each of its «Кому» as a letter of its own, and the fields in
// its subject and text — {Имя}, {Компания}… (the field's name in go24's language, or its code: {first}) — become
// the recipient's: an employee's from the profile, else the user's own contact, else a CRM contact, else the name
// in the address. Each letter is kept in «Отправленные»; the addresses it went to are kept with the draft, so a send
// that failed (or ran out of time) goes on with the others and nobody gets it twice.

// MergeFields are the fields of a recipient, by their codes.
var MergeFields = []string{"first", "middle", "last", "name", "company", "position", "email"}

// MaxMerge bounds the recipients of a merge: a whole contact list.
const MaxMerge = MaxMembers

// MergeRcpt is whom a merged letter goes to, with the fields it fills.
type MergeRcpt struct {
	Email                                        string
	First, Middle, Last, Name, Company, Position string
}

// Field is the recipient's field by its code.
func (r MergeRcpt) Field(code string) string {
	switch code {
	case "first":
		return r.First
	case "middle":
		return r.Middle
	case "last":
		return r.Last
	case "name":
		return r.Name
	case "company":
		return r.Company
	case "position":
		return r.Position
	case "email":
		return r.Email
	}
	return ""
}

// patronymic: does the word look like a Russian patronymic (Иванович, Петровна, Ильинична, Кузьмич)?
func patronymic(w string) bool {
	w = strings.ToLower(w)
	for _, end := range []string{"вич", "вна", "чна", "ична", "кызы", "оглы"} {
		if strings.HasSuffix(w, end) {
			return true
		}
	}
	return false
}

// splitName parts a full name: «Имя Фамилия», «Фамилия Имя Отчество», «Имя Отчество», «Имя Отчество Фамилия».
func splitName(name string) (first, middle, last string) {
	w := strings.Fields(name)
	switch {
	case len(w) == 1:
		return w[0], "", ""
	case len(w) == 2 && patronymic(w[1]):
		return w[0], w[1], ""
	case len(w) == 2:
		return w[0], "", w[1]
	case len(w) == 3 && patronymic(w[2]):
		return w[1], w[2], w[0]
	case len(w) >= 3:
		return w[0], strings.Join(w[1:len(w)-1], " "), w[len(w)-1]
	}
	return "", "", ""
}

// MergeFor is the fields of an address for the user's merged letter.
func (s *Store) MergeFor(ctx context.Context, userID string, a *mail.Address) (MergeRcpt, error) {
	r := MergeRcpt{Email: a.Address}
	err := s.db.QueryRow(ctx, `SELECT first_name, second_name, last_name, name, position FROM users
		WHERE lower(email) = lower($1) AND active AND NOT guest ORDER BY created_at LIMIT 1`, a.Address).
		Scan(&r.First, &r.Middle, &r.Last, &r.Name, &r.Position)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return r, err
	}
	if err == nil && r.First+r.Last != "" {
		if r.Name == "" {
			r.Name = strings.Join(strings.Fields(r.First+" "+r.Middle+" "+r.Last), " ")
		}
		return r, nil
	}
	err = s.db.QueryRow(ctx, `SELECT name, company FROM mail_contacts WHERE user_id::text = $1 AND lower(email) = lower($2) LIMIT 1`,
		userID, a.Address).Scan(&r.Name, &r.Company)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.db.QueryRow(ctx, `SELECT c.name, coalesce(co.title, ''), c.position FROM crm_contacts c
			LEFT JOIN crm_companies co ON co.id = c.company_id WHERE lower(trim(c.email)) = lower($1) ORDER BY c.updated_at DESC LIMIT 1`,
			a.Address).Scan(&r.Name, &r.Company, &r.Position)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return r, err
	}
	if r.Name == "" {
		r.Name = strings.TrimSpace(a.Name)
	}
	r.First, r.Middle, r.Last = splitName(r.Name)
	return r, nil
}

var reField = regexp.MustCompile(`\{([^{}<>]{1,40})\}`)

// mergeCodes are the names of the fields a text may have: their codes and their names in go24's language.
func (s *Store) mergeCodes() map[string]string {
	out := map[string]string{}
	for _, c := range MergeFields {
		out[c] = c
		out[strings.ToLower(s.tr("mail.merge.f."+c))] = c
	}
	return out
}

// mergeText puts the recipient's fields into a text (escaped in HTML); a name not of a field stays as it is.
func mergeText(text string, r MergeRcpt, codes map[string]string, isHTML bool) string {
	return reField.ReplaceAllStringFunc(text, func(m string) string {
		name := strings.ToLower(strings.TrimSpace(m[1 : len(m)-1]))
		if isHTML {
			name = html.UnescapeString(name)
		}
		code, ok := codes[name]
		if !ok {
			return m
		}
		v := r.Field(code)
		if isHTML {
			return html.EscapeString(v)
		}
		return v
	})
}

// Merged is the letter one recipient of a merge gets.
func (s *Store) Merged(l Letter, r MergeRcpt) Letter {
	codes := s.mergeCodes()
	l.To, l.CC, l.BCC = Member{Name: r.Name, Email: r.Email}.String(), "", ""
	l.Subject = mergeText(l.Subject, r, codes, false)
	l.HTML = mergeText(l.HTML, r, codes, true)
	if l.Text != "" {
		l.Text = mergeText(l.Text, r, codes, false)
	}
	return l
}

// MergeCheck: can a merge go — its addresses right, none in «Копия» and «СК», not too many?
func MergeCheck(to, cc, bcc string) error {
	list, err := addresses(to)
	if err != nil || len(list) == 0 {
		return i18n.E("err.mail_to")
	}
	if strings.TrimSpace(cc) != "" || strings.TrimSpace(bcc) != "" {
		return i18n.E("err.mail_merge_cc")
	}
	if len(list) > MaxMerge {
		return i18n.E("err.mail_merge_many", MaxMerge)
	}
	return nil
}

// MergePreview is the letter the i-th recipient of a merge gets (i is taken round the list), with how many there are.
func (s *Store) MergePreview(ctx context.Context, userID, to, subject, body string, i int) (Letter, int, error) {
	list, err := addresses(to)
	if err != nil || len(list) == 0 {
		return Letter{}, 0, i18n.E("err.mail_to")
	}
	i = ((i % len(list)) + len(list)) % len(list)
	r, err := s.MergeFor(ctx, userID, list[i])
	if err != nil {
		return Letter{}, 0, err
	}
	return s.Merged(Letter{Subject: subject, HTML: Sanitize(body)}, r), len(list), nil
}

// sendMerge sends a taken merge draft: a letter to each of its recipients it did not go to yet. It stops a little
// before ctx ends and gives done false: the draft goes back to wait (the worker takes it again at once).
func (s *Store) sendMerge(ctx context.Context, userID string, d Draft, l Letter) (done bool, _ error) {
	list, err := addresses(l.To)
	if err != nil {
		return false, i18n.E("err.mail_to")
	}
	var sent []string
	if err := s.db.QueryRow(ctx, `SELECT merge_done FROM mail_drafts WHERE id::text = $1`, d.ID).Scan(&sent); err != nil {
		return false, err
	}
	seen := map[string]bool{}
	for _, a := range sent {
		seen[strings.ToLower(a)] = true
	}
	for _, a := range list {
		k := strings.ToLower(a.Address)
		if seen[k] {
			continue
		}
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < 20*time.Second {
			return false, nil
		}
		r, err := s.MergeFor(ctx, userID, a)
		if err != nil {
			return false, err
		}
		if err := s.SendLetter(ctx, userID, d.MailboxID, s.Merged(l, r)); err != nil {
			return false, err
		}
		seen[k] = true
		if _, err := s.db.Exec(ctx, `UPDATE mail_drafts SET merge_done = array_append(merge_done, $2) WHERE id::text = $1`, d.ID, k); err != nil {
			return false, err
		}
	}
	return true, nil
}
