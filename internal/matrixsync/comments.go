package matrixsync

import (
	"context"
	"html"
	"maps"
	"strings"

	"go24/internal/i18n"
	"go24/internal/matrix"
)

// Comments on a channel's posts: anyone of the channel comments on a post in its thread, while only
// its owner and admins write in the channel itself. Matrix lets a reader send no message there, so go24's bot sends
// the reader's comment for them: the content names its author (commentBy) and keeps the text as written (commentBody);
// the body itself starts with the author's name, so Element shows whose comment it is. go24 shows the author.
const (
	commentBy   = "go24.comment_by"
	commentBody = "go24.comment_body"
)

// commentAuthor: whom go24's comment on a channel post is from ("" — not such a comment).
func (s *Service) commentAuthor(ev matrix.Event) string {
	if ev.Sender != s.M.UserID("go24") || ev.Type != "m.room.message" {
		return ""
	}
	by, _ := ev.Content[commentBy].(string)
	return by
}

// commentText is the text of a message as its author wrote it (a comment's without the name go24 put before it).
func commentText(content map[string]any) string {
	if b, _ := content[commentBody].(string); b != "" {
		return b
	}
	b, _ := content["body"].(string)
	return b
}

// asComment makes a message's content go24's comment for the author named (name — their name before a text).
func asComment(content map[string]any, me, name string) map[string]any {
	c := maps.Clone(content)
	body, _ := content["body"].(string)
	c[commentBy], c[commentBody] = me, body
	if mt, _ := content["msgtype"].(string); name != "" && (mt == "m.text" || mt == "m.emote") { // a file keeps its name
		c["body"] = name + ": " + body
		if f, ok := content["formatted_body"].(string); ok {
			c["formatted_body"] = "<b>" + html.EscapeString(name) + "</b>: " + f
		}
	}
	return c
}

// comment sends the employee's message in a thread of a channel they only read, as go24's comment; err — what Matrix
// said to the employee's own try (the room is not such a channel, or they are not in it).
func (s *Service) comment(ctx context.Context, me, tok, roomID string, content map[string]any, err error) (string, error) {
	if !matrix.IsCode(err, "M_FORBIDDEN") {
		return "", err
	}
	pl := matrix.PowerLevels{}
	if s.M.StateAs(ctx, tok, roomID, "m.room.power_levels", "", &pl) != nil || !isChannel(pl) {
		return "", err
	}
	if mem, merr := s.M.Members(ctx, roomID, tok); merr != nil || mem[me] != "join" {
		return "", err
	}
	if berr := s.commentBot(ctx, roomID, pl); berr != nil {
		s.Log.Warn("matrix: channel comment bot", "room", roomID, "err", berr)
		return "", err
	}
	who, _ := s.people(ctx, []string{me})
	return s.M.Send(ctx, roomID, asComment(content, me, who[me].Name), "")
}

// commentBot puts go24's bot into the channel as one who writes there (its owner invites it), once.
func (s *Service) commentBot(ctx context.Context, roomID string, pl matrix.PowerLevels) error {
	bot := s.M.UserID("go24")
	if pl.Level(bot) >= plLevel(pl, "events_default", 0) {
		return nil
	}
	var owner string
	if users, ok := pl["users"].(map[string]any); ok {
		for mx, v := range users {
			if l, _ := v.(float64); int(l) >= ownerLevel && strings.HasPrefix(mx, "@") {
				var tok string
				if s.DB.QueryRow(ctx, `SELECT token FROM matrix_users WHERE mxid = $1`, mx).Scan(&tok) == nil && tok != "" {
					owner = tok
					break
				}
			}
		}
	}
	if owner == "" {
		return i18n.E("err.forbidden")
	}
	mem, err := s.M.Members(ctx, roomID, owner)
	if err != nil {
		return err
	}
	if mem[bot] != "join" {
		if mem[bot] != "invite" {
			if err := s.M.InviteAs(ctx, owner, roomID, bot); err != nil {
				return err
			}
		}
		if err := s.M.JoinAs(ctx, "", roomID); err != nil {
			return err
		}
	}
	pl.SetLevel(bot, adminLevel)
	return s.M.SetStateAs(ctx, owner, roomID, "m.room.power_levels", "", pl)
}
