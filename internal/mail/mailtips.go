package mail

import (
	"context"
	"net/mail"
	"strings"
	"time"
)

// «Подсказки по почте», as Outlook's MailTips: while a letter is written, what is worth knowing of its recipients — a
// colleague's automatic replies are on (they are away), some are outside the organization, the audience is large, an
// address is not one. The organization is go24's people and their domains (but mail services' ones, PublicDomain) and
// the domain the letter goes from.

// LargeAudience: a letter to more recipients is a large audience's.
const LargeAudience = 25

// MaxTipAddrs bounds the addresses asked of at once.
const MaxTipAddrs = 1000

// MailTips are the tips of a letter's recipients.
type MailTips struct {
	Recipients  int            `json:"recipients"`
	Large       bool           `json:"large"`    // more than LargeAudience
	External    []string       `json:"external"` // the addresses outside the organization
	Invalid     []string       `json:"invalid"`  // what is not an address
	AutoReplies []AutoReplyTip `json:"auto_replies"`
}

// AutoReplyTip: a recipient of go24's whose automatic replies are on — the reply colleagues get.
type AutoReplyTip struct {
	Email  string     `json:"email"`
	UserID string     `json:"user_id"`
	Name   string     `json:"name"`
	Text   string     `json:"text"`
	Until  *time.Time `json:"until,omitempty"`
}

// publicDomains are mail services' domains: anyone's, not a company's.
var publicDomains = map[string]bool{"gmail.com": true, "googlemail.com": true, "yandex.ru": true, "ya.ru": true, "yandex.com": true,
	"mail.ru": true, "bk.ru": true, "list.ru": true, "inbox.ru": true, "outlook.com": true, "hotmail.com": true, "icloud.com": true,
	"yahoo.com": true, "rambler.ru": true, "proton.me": true, "protonmail.com": true, "live.com": true, "me.com": true}

// PublicDomain: the domain is a mail service's (gmail.com, yandex.ru, …), not a company's.
func PublicDomain(domain string) bool { return publicDomains[strings.ToLower(domain)] }

// MailTips are the tips of the letter the user writes from the address from to addrs (each "Name <a@b>" or a@b).
func (s *Store) MailTips(ctx context.Context, userID, from string, addrs []string, now time.Time) (MailTips, error) {
	t := MailTips{External: []string{}, Invalid: []string{}, AutoReplies: []AutoReplyTip{}}
	if len(addrs) > MaxTipAddrs {
		addrs = addrs[:MaxTipAddrs]
	}
	var emails []string
	seen := map[string]bool{}
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a == "" {
			continue
		}
		p, err := mail.ParseAddress(a)
		if err != nil || !strings.Contains(p.Address[strings.LastIndex(p.Address, "@")+1:], ".") {
			t.Invalid = append(t.Invalid, a)
			continue
		}
		if e := strings.ToLower(p.Address); !seen[e] {
			seen[e] = true
			emails = append(emails, e)
		}
	}
	t.Recipients = len(emails) + len(t.Invalid)
	t.Large = t.Recipients > LargeAudience
	if len(emails) == 0 {
		return t, nil
	}
	// go24's people among them (by their e-mail or a mailbox of theirs)
	rows, err := s.db.Query(ctx, `SELECT a, u.id::text, u.name FROM unnest($1::text[]) a JOIN users u ON u.active AND NOT u.guest
			AND (lower(u.email) = a OR EXISTS (SELECT 1 FROM mailboxes m WHERE m.user_id = u.id AND lower(m.email) = a))`, emails)
	if err != nil {
		return t, err
	}
	people := map[string]AutoReplyTip{}
	for rows.Next() {
		var p AutoReplyTip
		if err := rows.Scan(&p.Email, &p.UserID, &p.Name); err != nil {
			rows.Close()
			return t, err
		}
		people[p.Email] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}
	rows, err = s.db.Query(ctx, `SELECT DISTINCT lower(split_part(email, '@', 2)) FROM users WHERE active AND NOT guest AND email LIKE '%@%'`)
	if err != nil {
		return t, err
	}
	list, err := pgxCollectStrings(rows)
	if err != nil {
		return t, err
	}
	domains := map[string]bool{}
	for _, d := range append(list, strings.ToLower(from[strings.LastIndex(from, "@")+1:])) {
		if d != "" && !PublicDomain(d) {
			domains[d] = true
		}
	}
	asked := map[string]bool{}
	for _, e := range emails {
		p, ok := people[e]
		if !ok {
			if !domains[e[strings.LastIndex(e, "@")+1:]] {
				t.External = append(t.External, e)
			}
			continue
		}
		if p.UserID == userID || asked[p.UserID] {
			continue
		}
		asked[p.UserID] = true
		o, st, err := s.OOONow(ctx, p.UserID, now)
		if err != nil {
			return t, err
		}
		if !st.On {
			continue
		}
		if p.Text = strings.TrimSpace(o.Internal); p.Text == "" {
			p.Text = s.oooDefault(st)
		}
		p.Until = st.Until
		t.AutoReplies = append(t.AutoReplies, p)
	}
	return t, nil
}
