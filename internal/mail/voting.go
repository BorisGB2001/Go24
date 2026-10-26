package mail

import (
	"context"
	"html"
	"strings"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Voting buttons, as Outlook's: a letter asks its people to choose one of its choices («Утвердить;Отклонить»). In
// go24 the choices are buttons over the letter; one sends an answer «Утвердить: тема» that carries the choice. The
// sender's «Отслеживание» counts the answers (the last one of each person). Outlook keeps the choices in a property
// of its own; go24 — in X-Go24-Voting and X-Go24-Vote, and an answer without them (from another mail program) counts
// by its subject. The letter's text also lists the choices for those who see no buttons.

const (
	hdrVoting = "X-Go24-Voting"
	hdrVote   = "X-Go24-Vote"
	maxChoice = 100 // runes of a choice
	maxVotes  = 10  // choices of a letter
)

// VotingPresets are the voting buttons offered, as Outlook's (the keys of their choices in the locales).
var VotingPresets = []string{"approve", "yesno", "yesnomaybe"}

// Choices is a list of voting buttons as kept: «;»-separated, trimmed, without repeats and empty ones.
func Choices(s string) string {
	var out []string
	seen := map[string]bool{}
	for _, c := range strings.Split(strings.NewReplacer("\r", " ", "\n", " ").Replace(s), ";") {
		c = clip(strings.TrimSpace(c), maxChoice)
		if c == "" || seen[strings.ToLower(c)] || len(out) == maxVotes {
			continue
		}
		seen[strings.ToLower(c)] = true
		out = append(out, c)
	}
	return strings.Join(out, ";")
}

// Options are the voting buttons of the letter (none — it asks for no vote).
func (m Message) Options() []string {
	if m.Voting == "" {
		return nil
	}
	return strings.Split(m.Voting, ";")
}

// votingOf reads the voting headers of a letter.
func votingOf(get func(string) (string, error)) (voting, vote string) {
	v, _ := get(hdrVoting)
	c, _ := get(hdrVote)
	return Choices(v), clip(strings.TrimSpace(c), maxChoice)
}

// choiceOf is the choice an answer votes for: its own, else the one its subject begins with («Утвердить: тема»);
// "" — not one of the choices.
func choiceOf(options []string, vote, subject string) string {
	for _, o := range options {
		if vote != "" && strings.EqualFold(vote, o) {
			return o
		}
	}
	if vote != "" {
		return ""
	}
	for _, o := range options {
		if len(subject) > len(o) && strings.EqualFold(subject[:len(o)], o) && subject[len(o)] == ':' {
			return o
		}
	}
	return ""
}

// votingText is what the letter's HTML says of its voting buttons, for those who see none.
func (s *Store) votingText(voting string) string {
	return "<p><i>" + html.EscapeString(s.tr("mail.vote.text", strings.ReplaceAll(voting, ";", " / "))) + "</i></p>"
}

// VoteAnswer is an answer to the user's voting buttons.
type VoteAnswer struct {
	ID, From, Name, Choice string
	At                     time.Time
}

// Who is how the one who voted is shown.
func (v VoteAnswer) Who() string {
	if v.Name != "" {
		return v.Name
	}
	return v.From
}

// Tracking is «Отслеживание» of a letter with voting buttons: the answers (the last of each person, the newest
// first), the count of each choice, the user's own vote ("" — none yet).
type Tracking struct {
	Answers []VoteAnswer
	Count   map[string]int
	Asked   int // the people the letter asks (To, Cc)
	Mine    string
}

// Votes is the tracking of the user's letter with voting buttons: the answers in its conversation.
func (s *Store) Votes(ctx context.Context, userID, id string) (Tracking, error) {
	t := Tracking{Count: map[string]int{}}
	m, err := s.message(ctx, userID, id)
	if err != nil || m.Voting == "" {
		return t, err
	}
	for _, a := range strings.Split(strings.ReplaceAll(m.To+","+m.CC, ";", ","), ",") {
		if a = strings.TrimSpace(a); a != "" && !hasAddress(a, m.MailboxEmail) {
			t.Asked++
		}
	}
	thread := m.Thread
	if thread == "" {
		thread = m.MessageID
	}
	rows, err := s.db.Query(ctx, `SELECT r.id::text, r.from_addr, r.from_name, r.vote, r.subject, r.sent_at, f.role = 'sent' OR lower(r.from_addr) = lower(b.email)
		FROM mail_messages r JOIN mail_folders f ON f.id = r.folder_id JOIN mailboxes b ON b.id = r.mailbox_id
		WHERE r.mailbox_id::text = $1 AND r.thread = $2 AND r.id::text <> $3 AND f.role <> 'drafts' AND r.sent_at >= $4
		AND NOT EXISTS (SELECT 1 FROM mail_drafts d WHERE d.id = r.id) ORDER BY r.sent_at DESC, r.uid DESC`, m.MailboxID, thread, m.ID, m.SentAt.Add(-time.Minute))
	if err != nil {
		return t, err
	}
	type row struct {
		VoteAnswer
		vote, subject string
		own           bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.ID, &x.From, &x.Name, &x.vote, &x.subject, &x.At, &x.own)
		return x, err
	})
	if err != nil {
		return t, err
	}
	seen := map[string]bool{}
	for _, x := range list {
		c := choiceOf(m.Options(), x.vote, x.subject)
		if c == "" {
			continue
		}
		if x.own {
			if t.Mine == "" {
				t.Mine = c
			}
			continue
		}
		who := strings.ToLower(x.From)
		if seen[who] { // only the person's last answer counts
			continue
		}
		seen[who] = true
		x.Choice = c
		t.Answers = append(t.Answers, x.VoteAnswer)
		t.Count[c]++
	}
	return t, nil
}

// Vote answers the letter's voting buttons: an answer «Выбор: тема» to its sender with the user's words (none — just
// the choice), in its conversation; the letter is marked answered.
func (s *Store) Vote(ctx context.Context, userID, id, choice, text string) error {
	m, err := s.message(ctx, userID, id)
	if err != nil {
		return err
	}
	c := choiceOf(m.Options(), choice, "")
	if c == "" || m.Outgoing() || m.FromAddr == "" {
		return i18n.E("err.bad_form")
	}
	refs, err := s.refsOf(ctx, userID, id)
	if err != nil {
		return err
	}
	from := "" // the answer goes from the address the letter came to
	if !hasAddress(m.To+","+m.CC, m.MailboxEmail) {
		froms, err := s.FromAddresses(ctx, userID)
		if err != nil {
			return err
		}
		for _, f := range froms {
			if f.MailboxID == m.MailboxID && hasAddress(m.To+","+m.CC, f.Email) {
				from = f.Email
			}
		}
	}
	l := Letter{From: from, To: m.FromAddr, Subject: c + ": " + m.Subject, Text: strings.TrimSpace(text), Vote: c, Refs: refs}
	if err := s.SendLetter(ctx, userID, m.MailboxID, l); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET answered = 'reply', answered_at = now() WHERE id::text = $1`, id); err != nil {
		return err
	}
	if list, err := s.letters(ctx, userID, []string{id}); err == nil && len(list) > 0 {
		if err := s.store(ctx, list, imap.AnsweredFlag, true); err != nil && s.Log != nil {
			s.Log("mail: mark answered", id, err)
		}
	}
	return nil
}

// hasAddress: is the address in the list?
func hasAddress(list, addr string) bool {
	for _, a := range strings.Split(strings.ReplaceAll(list, ";", ","), ",") {
		a = strings.TrimSpace(a)
		if i := strings.LastIndex(a, "<"); i >= 0 {
			a = strings.TrimSuffix(a[i+1:], ">")
		}
		if addr != "" && strings.EqualFold(a, addr) {
			return true
		}
	}
	return false
}
