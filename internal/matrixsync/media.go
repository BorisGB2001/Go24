package matrixsync

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	"go24/internal/matrix"
)

// mediaMax and linksMax are how many of a chat's latest files and text messages the chat's panel looks through.
const (
	mediaMax = 1000
	linksMax = 2000
)

// Link is a link written in a chat message, as the chat's «Ссылки» list shows it.
type Link struct {
	URL, MessageID, ThreadRoot, SenderName string
	At                                     time.Time
}

// linkRe finds the web links in a message's text.
var linkRe = regexp.MustCompile(`https?://[^\s<>"'«»]+`)

// Media is what was sent to the chat, newest first chat panel shows it: the messages with a file
// (pictures, videos, files, voice messages — threads' replies too) and the links written in messages.
func (s *Service) Media(ctx context.Context, userID, roomID string) ([]Message, []Link, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	page := func(filter string, max int) ([]matrix.Event, error) {
		var evs []matrix.Event
		from := ""
		for len(evs) < max {
			p, end, err := s.M.MessagesFiltered(ctx, tok, roomID, from, 500, filter)
			if err != nil {
				return nil, forbidden(err)
			}
			evs = append(evs, p...)
			if len(p) == 0 || end == "" || end == from {
				break
			}
			from = end
		}
		return evs, nil
	}
	evs, err := page(`{"types":["m.room.message"],"contains_url":true}`, mediaMax)
	if err != nil {
		return nil, nil, err
	}
	var files []Message
	for _, m := range s.convert(ctx, evs, roomID, me, false) {
		if m.File != nil {
			files = append(files, m)
		}
	}
	slices.Reverse(files)
	if evs, err = page(`{"types":["m.room.message"],"contains_url":false}`, linksMax); err != nil {
		return nil, nil, err
	}
	msgs := s.convert(ctx, evs, roomID, me, false)
	slices.Reverse(msgs)
	return files, messageLinks(msgs), nil
}

// messageLinks are the links of the messages (newest first), each link once — where it was written last.
func messageLinks(msgs []Message) []Link {
	seen := map[string]bool{}
	var out []Link
	for _, m := range msgs {
		if m.File != nil || m.Encrypted || m.Event != "" || m.Call != nil || m.Poll != nil {
			continue
		}
		for _, u := range linkRe.FindAllString(stripReplyFallback(m.Body), -1) {
			u = strings.TrimRight(u, ".,;:!?)]}")
			if seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, Link{URL: u, MessageID: m.ID, ThreadRoot: m.ThreadRoot, SenderName: m.SenderName, At: m.At})
		}
	}
	return out
}
