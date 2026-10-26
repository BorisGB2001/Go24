package matrixsync

import (
	"context"
	"html"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// Mentions between go24 and Matrix. go24 writes "@Full Name"; Matrix clients expect a pill (a matrix.to link in
// formatted_body) and the m.mentions list, which is what makes Element highlight and notify. Messages from Element
// carry m.mentions; go24 turns them back into "@Name" and notifies the people.

// mention adds Matrix mentions for the "@Name"s of text to content. rich is the HTML body when content has one
// already ("" = built from the plain body).
func (s *Service) mention(ctx context.Context, content map[string]any, text, rich string) error {
	found, err := s.Tasks.Mentioned(ctx, text)
	if err != nil {
		return err
	}
	bots, err := s.mentionedBots(ctx, text)
	if err != nil || len(found)+len(bots) == 0 {
		return err
	}
	ids := make([]string, len(found))
	for i, m := range found {
		ids[i] = m.ID
	}
	rows, err := s.DB.Query(ctx, `SELECT user_id::text, mxid FROM matrix_users WHERE user_id = ANY($1::uuid[]) AND mxid <> ''`, ids)
	if err != nil {
		return err
	}
	mx := map[string]string{}
	for rows.Next() {
		var id, m string
		if err := rows.Scan(&id, &m); err != nil {
			rows.Close()
			return err
		}
		mx[id] = m
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if rich == "" {
		rich, _ = content["formatted_body"].(string)
	}
	if rich == "" {
		body, _ := content["body"].(string)
		rich = strings.ReplaceAll(html.EscapeString(body), "\n", "<br>")
	}
	var users []string
	for _, m := range found {
		id, ok := mx[m.ID]
		if !ok {
			continue // no Matrix account yet: the plain "@Name" stays
		}
		users = append(users, id)
		rich = strings.ReplaceAll(rich, "@"+html.EscapeString(m.Name),
			`<a href="https://matrix.to/#/`+html.EscapeString(id)+`">`+html.EscapeString(m.Name)+`</a>`)
	}
	for _, m := range bots {
		users = append(users, m.ID)
		rich = strings.ReplaceAll(rich, "@"+html.EscapeString(m.Name),
			`<a href="https://matrix.to/#/`+html.EscapeString(m.ID)+`">`+html.EscapeString(m.Name)+`</a>`)
	}
	if len(users) == 0 {
		return nil
	}
	content["m.mentions"] = map[string]any{"user_ids": users}
	content["format"] = "org.matrix.custom.html"
	content["formatted_body"] = rich
	return nil
}

// mentionedBots are the chat bots the text mentions as "@Name" (ID — the bot's Matrix ID), the longest names first.
func (s *Service) mentionedBots(ctx context.Context, text string) ([]tasks.Mention, error) {
	if !strings.Contains(text, "@") {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT b.mxid, trim(b.name || ' ' || b.last_name) n FROM im_bots b JOIN apps a ON a.id = b.app_id
		WHERE a.installed_at IS NOT NULL AND strpos($1, '@' || trim(b.name || ' ' || b.last_name)) > 0 ORDER BY length(trim(b.name || ' ' || b.last_name)) DESC`, text)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (tasks.Mention, error) {
		var m tasks.Mention
		return m, r.Scan(&m.ID, &m.Name)
	})
}

// inboundMentions reads m.mentions of a message from the messenger: the go24 people and the body with their
// names marked as "@Name".
func (s *Service) inboundMentions(ctx context.Context, content map[string]any, body string) ([]string, string, error) {
	m, _ := content["m.mentions"].(map[string]any)
	list, _ := m["user_ids"].([]any)
	var mxids []string
	for _, v := range list {
		if id, ok := v.(string); ok {
			mxids = append(mxids, id)
		}
	}
	if len(mxids) == 0 {
		return nil, body, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT u.id::text, u.name FROM matrix_users m JOIN users u ON u.id = m.user_id
		WHERE m.mxid = ANY($1) AND u.active`, mxids)
	if err != nil {
		return nil, body, err
	}
	defer rows.Close()
	var ids, names []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, body, err
		}
		ids, names = append(ids, id), append(names, name)
	}
	return ids, tasks.MarkMentions(body, names), rows.Err()
}

// mentions: the message's m.mentions name the user (by MXID), or the whole room (@room).
func mentions(content map[string]any, mxid string) bool {
	m, _ := content["m.mentions"].(map[string]any)
	if m["room"] == true {
		return true
	}
	ids, _ := m["user_ids"].([]any)
	for _, id := range ids {
		if id == mxid {
			return true
		}
	}
	return false
}
