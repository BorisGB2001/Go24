package mail

import (
	"strings"

	"go24/internal/i18n"
)

// «Не пересылать», as Outlook's «Разрешения → Не пересылать»: the author restricts a letter — the people it comes to
// may answer it (an answer keeps the restriction), not forward it (as a letter, as an attachment, by a rule, a quick
// step or the mailbox's «Пересылка»), print it, see its source or copy its text. Outlook protects the letter with
// rights management; go24 marks it with X-Go24-Restrict and keeps the restriction for every go24 mailbox it comes to.
// Another mail program does not know the mark: outside go24 the letter is an ordinary one.

const (
	noForwardHeader = "X-Go24-Restrict"
	noForwardValue  = "do-not-forward"
)

// ErrNoForward: the letter is «Не пересылать».
var ErrNoForward = i18n.E("err.mail_no_forward")

// noForwardOf reads the restriction of a letter.
func noForwardOf(h string) bool {
	for _, v := range strings.Split(h, ",") {
		if strings.EqualFold(strings.TrimSpace(v), noForwardValue) {
			return true
		}
	}
	return false
}

// Locked: the letter came «Не пересылать» — it may be read and answered only. The author's own copies are not.
func (m Message) Locked() bool { return m.NoForward && !m.Outgoing() }
