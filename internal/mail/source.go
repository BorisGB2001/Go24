package mail

import (
	"context"
	"io"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"go24/internal/i18n"
)

// maxSource bounds a letter's source brought from the server («Показать исходный текст», «Сохранить как»).
const maxSource = 50 << 20

// Source is a letter of the user as its server keeps it (headers and the MIME source): go24 keeps the letter's
// text, HTML and files, not the source, so it is brought from the mail server by the letter's UID. go24's own
// letters (drafts, letters sent before the server had «Отправленные») have none.
func (s *Store) Source(ctx context.Context, userID, letterID string) ([]byte, error) {
	list, err := s.letters(ctx, userID, []string{letterID})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	l := list[0]
	if l.local || l.uid <= 0 || l.uid > maxUID {
		return nil, i18n.E("err.mail_no_source")
	}
	var out []byte
	err = s.remote(ctx, l.mailbox, func(c *client.Client, _ account) error {
		if _, err := c.Select(l.name, true); err != nil {
			return serverErr(err)
		}
		set := new(imap.SeqSet)
		set.AddNum(uint32(l.uid))
		section := &imap.BodySectionName{Peek: true}
		msgs := make(chan *imap.Message, 1)
		done := make(chan error, 1)
		go func() { done <- c.UidFetch(set, []imap.FetchItem{imap.FetchUid, section.FetchItem()}, msgs) }()
		for m := range msgs {
			if r := m.GetBody(section); r != nil && out == nil {
				b, err := io.ReadAll(io.LimitReader(r, maxSource))
				if err != nil {
					return err
				}
				out = b
			}
		}
		return <-done
	})
	if err == nil && out == nil {
		err = i18n.E("err.mail_no_source") // gone from the server since
	}
	return out, err
}
