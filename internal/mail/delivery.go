package mail

import (
	"net/mail"
	"strings"
	"time"

	"go24/internal/i18n"
)

// Outlook's «Параметры → Параметры доставки» of a letter: «Направлять ответы на» — the addresses «Ответить» and
// «Ответить всем» go to instead of the sender (Reply-To), «Срок действия истекает» — after that time the letter is
// out of date (Expiry-Date, RFC 4021): Outlook strikes it through in the list and says so over it.

// MaxRepliesTo bounds the addresses of «Направлять ответы на».
const MaxRepliesTo = 10

// repliesTo checks «Направлять ответы на» and makes it the address list kept: "" — none.
func repliesTo(list string) (string, error) {
	addrs, err := addresses(list)
	if err != nil || len(addrs) > MaxRepliesTo {
		return "", i18n.E("err.mail_replies_to")
	}
	return readable(addrs), nil
}

// readable is an address list as a person reads and writes it: «Имя <адрес>», not MIME-encoded.
func readable(addrs []*mail.Address) string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.Address
		if name := a.Name; name != "" {
			if strings.ContainsAny(name, `,;"<>@()`) {
				name = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
			}
			out[i] = name + " <" + a.Address + ">"
		}
	}
	return clip(strings.Join(out, ", "), 2000)
}

// expiryOf reads Expiry-Date (Outlook's) or Expires (RFC 2156) of a letter: nil — none or not a date.
func expiryOf(get func(string) string) *time.Time {
	for _, k := range []string{"Expiry-Date", "Expires"} {
		if v := strings.TrimSpace(get(k)); v != "" {
			if t, err := mail.ParseDate(v); err == nil {
				return &t
			}
		}
	}
	return nil
}

// repliesToOf reads Reply-To of a letter as the address list kept: "" — none, or only the sender itself.
func repliesToOf(h, from string) string {
	addrs, err := mail.ParseAddressList(h)
	if err != nil || len(addrs) == 0 || len(addrs) == 1 && strings.EqualFold(addrs[0].Address, from) {
		return ""
	}
	return readable(addrs)
}

// Expired: the letter's «Срок действия» has passed (the author's own copies too, as Outlook shows them).
func (m Message) Expired() bool { return m.ExpiresAt != nil && m.ExpiresAt.Before(time.Now()) }
