package mail

import (
	"context"
	"encoding/json"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"go24/internal/i18n"
)

// Sender checks, as Outlook's: the receiving server's verdict on the From domain (Authentication-Results, RFC 8601:
// DMARC, else SPF and DKIM aligned with From) — a failed one is Outlook's «?» on the sender's picture and «Не
// удалось проверить отправителя»; «через» (via) when only another domain passed; «Вы нечасто получаете письма от …»
// for a first letter; a domain that looks like a familiar one (homoglyphs, one letter changed). «Сообщить о фишинге»
// and «Сообщить о нежелательной почте» move the letter away and send it to the company's reporting address.

// The verdicts of the sender's authentication (Message.SenderAuth).
const (
	AuthPass = "pass"
	AuthFail = "fail"
)

// senderAuth reads the topmost Authentication-Results — the one the user's own server added — for the From
// address: the verdict ("" — not checked) and the domain the letter came «через» when SPF or DKIM passed only for
// another one.
func senderAuth(results, from string) (verdict, via string) {
	dom := domainOf(from)
	if results == "" || dom == "" {
		return "", ""
	}
	parts := strings.Split(stripComments(results), ";")
	var dmarc, compauth string
	aligned, failed := false, false
	var other string // a domain SPF or DKIM passed for, not aligned with From
	for _, p := range parts[1:] {
		f := strings.Fields(p)
		if len(f) == 0 {
			continue
		}
		method, result, _ := strings.Cut(strings.ToLower(f[0]), "=")
		method, _, _ = strings.Cut(method, "/")
		props := map[string]string{}
		for _, kv := range f[1:] {
			if k, v, ok := strings.Cut(kv, "="); ok {
				props[strings.ToLower(k)] = strings.Trim(v, `"`)
			}
		}
		switch method {
		case "dmarc":
			dmarc = result
		case "compauth": // Microsoft's composite verdict
			compauth = result
		case "spf", "dkim":
			d := props["header.d"]
			if method == "spf" {
				d = props["smtp.mailfrom"]
			} else if d == "" {
				d = props["header.i"]
			}
			d = strings.ToLower(strings.TrimPrefix(d[strings.LastIndex(d, "@")+1:], "."))
			switch result {
			case "pass":
				if d == "" || sameOrg(d, dom) {
					aligned = true
				} else if other == "" || method == "dkim" {
					other = d
				}
			case "fail", "softfail", "permerror":
				failed = true
			}
		}
	}
	switch {
	case dmarc == "pass" || compauth == "pass":
		return AuthPass, ""
	case dmarc == "fail" || compauth == "fail":
		return AuthFail, ""
	case aligned:
		return AuthPass, ""
	case other != "":
		return "", other
	case failed:
		return AuthFail, ""
	}
	return "", ""
}

// stripComments drops the (comments) of a header value, quoted strings kept.
func stripComments(v string) string {
	var b strings.Builder
	depth, quoted := 0, false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == '\\' && i+1 < len(v):
			if depth == 0 {
				b.WriteByte(c)
				b.WriteByte(v[i+1])
			}
			i++
			continue
		case c == '"' && depth == 0:
			quoted = !quoted
		case c == '(' && !quoted:
			depth++
			continue
		case c == ')' && !quoted && depth > 0:
			depth--
			b.WriteByte(' ')
			continue
		}
		if depth == 0 {
			b.WriteByte(c)
		}
	}
	return b.String()
}

// domainOf is an address's domain, lower case ("" — none).
func domainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[i+1:]))
}

// orgDomain is the domain one registers (mail.example.co.uk → example.co.uk).
func orgDomain(d string) string {
	if o, err := publicsuffix.EffectiveTLDPlusOne(d); err == nil {
		return o
	}
	return d
}

// sameOrg: are the domains of one organisation (DMARC's relaxed alignment)?
func sameOrg(a, b string) bool { return orgDomain(a) == orgDomain(b) }

// SenderCheck is what the reading pane warns of a letter's sender.
type SenderCheck struct {
	Auth, Via string // Message.SenderAuth, SenderVia
	First     bool   // «Вы нечасто получаете письма от …»: no other letter from the address, never written to
	Like      string // a familiar domain the sender's resembles («похож на …»)
}

// Any: is there something to show?
func (c SenderCheck) Any() bool { return c.Auth == AuthFail || c.First || c.Like != "" }

// CheckSender is SenderCheck of an incoming letter of the user (none of the user's own, a colleague's or a safe one).
func (s *Store) CheckSender(ctx context.Context, userID string, m Message) (SenderCheck, error) {
	c := SenderCheck{Auth: m.SenderAuth, Via: m.SenderVia}
	addr := strings.ToLower(m.FromAddr)
	if m.Outgoing() || domainOf(addr) == "" {
		return SenderCheck{}, nil
	}
	var known, colleague, safe bool
	if err := s.db.QueryRow(ctx, `SELECT
			EXISTS (SELECT 1 FROM mailboxes WHERE user_id::text = $1 AND lower(email) = $2) OR
			EXISTS (SELECT 1 FROM users WHERE lower(email) = $2),
			EXISTS (SELECT 1 FROM mail_senders WHERE user_id::text = $1 AND kind = 'safe' AND sender = ANY($4)),
			EXISTS (SELECT 1 FROM mail_messages x JOIN mailboxes b ON b.id = x.mailbox_id JOIN mail_folders f ON f.id = x.folder_id
				WHERE b.user_id::text = $1 AND x.id::text <> $3 AND (lower(x.from_addr) = $2 AND f.role NOT IN ('junk', 'trash')
					OR f.role = 'sent' AND (x.to_addr || ',' || x.cc_addr) ILIKE '%' || $2 || '%'))`,
		userID, addr, m.ID, senderKeys(addr)).Scan(&colleague, &safe, &known); err != nil {
		return c, err
	}
	if colleague {
		return SenderCheck{}, nil
	}
	if safe {
		return SenderCheck{Via: c.Via}, nil
	}
	c.First = !known
	if !known { // a domain new to the user that looks like one it writes to
		fam, err := s.familiarDomains(ctx, userID)
		if err != nil {
			return c, err
		}
		c.Like = lookalike(domainOf(addr), fam)
	}
	return c, nil
}

// familiarDomains are the user's own domains, the company's and those the user wrote to lately.
func (s *Store) familiarDomains(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT email FROM mailboxes WHERE user_id::text = $1
		UNION ALL (SELECT email FROM users WHERE active AND email LIKE '%@%' LIMIT 500)
		UNION ALL (SELECT x.to_addr || ',' || x.cc_addr FROM mail_messages x JOIN mailboxes b ON b.id = x.mailbox_id JOIN mail_folders f ON f.id = x.folder_id
			WHERE b.user_id::text = $1 AND f.role = 'sent' ORDER BY x.sent_at DESC LIMIT 500)`, userID)
	if err != nil {
		return nil, err
	}
	lists, err := pgxCollectStrings(rows)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		as, err := mail.ParseAddressList(l)
		if err != nil {
			as = nil
			for _, a := range strings.Split(l, ",") {
				as = append(as, &mail.Address{Address: strings.TrimSpace(a)})
			}
		}
		for _, a := range as {
			if d := orgDomain(domainOf(a.Address)); d != "" && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	return out, nil
}

// lookalike is the familiar domain d pretends to be: the same once homoglyphs are read as Latin letters (а → a,
// rn → m, 0 → o…), or one letter off; "" — none.
func lookalike(d string, familiar []string) string {
	d = orgDomain(d)
	if d == "" {
		return ""
	}
	sd := skeleton(d)
	for _, f := range familiar {
		if f == d {
			return ""
		}
	}
	for _, f := range familiar {
		if sd == skeleton(f) || utf8.RuneCountInString(f) >= 6 && oneEdit(d, f) {
			return f
		}
	}
	return ""
}

// homoglyphs are what letters look like: Cyrillic and Greek ones like Latin, digits like letters.
var homoglyphs = strings.NewReplacer("rn", "m", "vv", "w", "0", "o", "1", "l", "i", "l", "|", "l",
	"а", "a", "е", "e", "о", "o", "р", "p", "с", "c", "у", "y", "х", "x", "к", "k", "м", "m", "т", "t", "н", "h", "в", "b",
	"і", "l", "ј", "j", "ѕ", "s", "ԁ", "d", "ο", "o", "α", "a", "ε", "e", "ν", "v", "ρ", "p", "τ", "t", "ι", "l")

// skeleton is a domain as it looks: Unicode (punycode decoded), lower case, homoglyphs as Latin.
func skeleton(d string) string {
	if u, err := idna.ToUnicode(d); err == nil {
		d = u
	}
	return homoglyphs.Replace(strings.ToLower(d))
}

// oneEdit: are a and b one letter apart (changed, added, dropped or two swapped)?
func oneEdit(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	if len(rb)-len(ra) > 1 || a == b {
		return false
	}
	i := 0
	for i < len(ra) && ra[i] == rb[i] {
		i++
	}
	if len(ra) == len(rb) {
		if i+1 < len(ra) && ra[i] == rb[i+1] && ra[i+1] == rb[i] && string(ra[i+2:]) == string(rb[i+2:]) {
			return true
		}
		return string(ra[i+1:]) == string(rb[i+1:])
	}
	return string(ra[i:]) == string(rb[i+1:])
}

// The kinds of «Сообщить».
var ReportKinds = []string{"phishing", "junk"}

// keyReportTo is the company's reporting address (company_settings).
const keyReportTo = "mail_report_to"

// ReportTo is the address reported letters go to ("" — none: they are only moved and recorded).
func (s *Store) ReportTo(ctx context.Context) (string, error) {
	var to string
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT value #>> '{}' FROM company_settings WHERE key = $1), '')`, keyReportTo).Scan(&to)
	return to, err
}

// SetReportTo sets the reporting address (an administrator; "" — none).
func (s *Store) SetReportTo(ctx context.Context, by, to string) error {
	to = strings.TrimSpace(to)
	if to != "" {
		a, err := mail.ParseAddress(to)
		if err != nil {
			return i18n.E("err.mail_to")
		}
		to = a.Address
	}
	v, _ := json.Marshal(to)
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, keyReportTo, v, by)
	return err
}

// Report is a reported letter, for the administrators.
type Report struct {
	ID, Kind, User, FromAddr, Subject, SentTo string
	SentAt                                    *time.Time
	At                                        time.Time
}

// Reports are the latest reported letters.
func (s *Store) Reports(ctx context.Context, limit int) ([]Report, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id::text, r.kind, coalesce(u.name, ''), r.from_addr, r.subject, r.sent_to, r.sent_at, r.created_at
		FROM mail_reports r LEFT JOIN users u ON u.id = r.user_id ORDER BY r.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.ID, &r.Kind, &r.User, &r.FromAddr, &r.Subject, &r.SentTo, &r.SentAt, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReportNote is the text of the letter that carries a reported one to the reporting address.
type ReportNote struct{ Subject, Text string }

// ReportLetters is «Сообщить о фишинге» (phishing: the letters go to «Удалённые») or «Сообщить о нежелательной
// почте» (junk: to «Нежелательная почта»): each is recorded and, with a reporting address set, brought from its
// server; send then mails them there, each attached as .eml from its own mailbox (in the background: the page does
// not wait for the servers).
func (s *Store) ReportLetters(ctx context.Context, userID string, ids []string, kind string, note func(Message) ReportNote) (int, func(context.Context) error, error) {
	if !oneOf(kind, ReportKinds) {
		return 0, nil, i18n.E("err.bad_form")
	}
	to, err := s.ReportTo(ctx)
	if err != nil {
		return 0, nil, err
	}
	type copyOf struct {
		box  string
		note ReportNote
		src  []byte
	}
	var copies []copyOf
	var done []string
	for _, id := range ids {
		m, err := s.message(ctx, userID, id)
		if err != nil {
			return 0, nil, err
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO mail_reports (user_id, kind, from_addr, subject, message_id, sent_at, sent_to) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			userID, kind, m.FromAddr, m.Subject, m.MessageID, m.SentAt, to); err != nil {
			return 0, nil, err
		}
		if to != "" {
			src, err := s.Source(ctx, userID, m.ID)
			if err != nil { // go24 keeps the text, not the source: what it has goes then
				src = []byte("From: " + m.FromAddr + "\r\nSubject: " + m.Subject + "\r\nMessage-ID: <" + m.MessageID + ">\r\n\r\n" + m.Body)
			}
			copies = append(copies, copyOf{m.MailboxID, note(m), src})
		}
		done = append(done, m.ID)
	}
	action := map[string]string{"phishing": "delete", "junk": "junk"}[kind]
	if err := s.Act(ctx, userID, done, action, ""); err != nil {
		return 0, nil, err
	}
	send := func(ctx context.Context) error {
		for _, c := range copies {
			a, err := s.account(ctx, c.box)
			if err != nil {
				return err
			}
			rcpt := &mail.Address{Address: to}
			e := envelope{From: mail.Address{Address: a.Email}, To: []*mail.Address{rcpt}, Host: a.Email[strings.LastIndex(a.Email, "@")+1:], Now: time.Now()}
			l := Letter{Subject: c.note.Subject, Text: c.note.Text, Files: []Attachment{{Name: "message.eml", ContentType: "message/rfc822", Data: c.src}}}
			msg, _ := compose(e, l)
			if err := s.sendSMTP(ctx, a, []string{to}, msg, false); err != nil {
				return i18n.E("err.mail_send", err.Error())
			}
		}
		return nil
	}
	return len(done), send, nil
}
