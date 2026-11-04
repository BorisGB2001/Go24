package openlines

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// The AI bot on the knowledge base: a new dialogue of a line with the AI bot (and without the menu
// bot) goes to it first. go24 worker takes the client's messages that wait for it (AIBotDue), the AI answers from the
// pages of the line's knowledge base space (Knowledge) and the answer goes to the client (AIBotAnswer); an answer with
// AIHandoff — the pages have no answer or the client asks for a person — hands the dialogue to the operators.

// AIHandoff is what the AI writes instead of an answer to hand the dialogue to the operators.
const AIHandoff = "[OPERATOR]"

const (
	aiTurns     = 10    // answers of the bot in a dialogue, then the operators
	aiKnowledge = 40000 // characters of the knowledge base pages the AI reads
)

// AIAsk is a dialogue whose client waits for the AI bot's answer.
type AIAsk struct {
	SessionID, WidgetID string
	Space               string // the knowledge base space of the line
	UserID              string // whose AI connection answers: the line's first operator (else its author), or the company's
	Question            string // the client's last message
}

// AIBotDue takes the dialogues whose client's message waits for the AI bot's answer; each is taken once.
func (s *Store) AIBotDue(ctx context.Context) ([]AIAsk, error) {
	rows, err := s.db.Query(ctx, `UPDATE ol_sessions s SET ai_wait = NULL FROM ol_widgets w
		WHERE s.id IN (SELECT id FROM ol_sessions WHERE ai_wait IS NOT NULL AND ai_bot ORDER BY ai_wait LIMIT 20 FOR UPDATE SKIP LOCKED)
			AND w.id = s.widget_id
		RETURNING s.id::text, w.id::text, coalesce(w.ai_space::text, ''), coalesce(w.operators[1], w.created_by)::text,
			coalesce((SELECT m.body FROM ol_messages m WHERE m.session_id = s.id AND m.author_id IS NULL AND m.kind = '' ORDER BY m.id DESC LIMIT 1), '')`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[AIAsk])
}

// Knowledge is what the AI bot reads to answer the question: the pages of the knowledge base space, those with more of
// the question's words first, as long as aiKnowledge characters allow ("" — the space has no pages).
func (s *Store) Knowledge(ctx context.Context, spaceID, question string) (string, error) {
	rows, err := s.db.Query(ctx, `SELECT title, body FROM kb_pages WHERE space_id::text = $1 ORDER BY sort, title`, spaceID)
	if err != nil {
		return "", err
	}
	type page struct {
		Title, Body string
		score       int
	}
	pages, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (page, error) {
		var p page
		return p, r.Scan(&p.Title, &p.Body)
	})
	if err != nil {
		return "", err
	}
	words := stems(question)
	for i := range pages {
		title, body := strings.ToLower(pages[i].Title), strings.ToLower(pages[i].Body)
		for _, w := range words {
			if strings.Contains(title, w) {
				pages[i].score += 3
			}
			if strings.Contains(body, w) {
				pages[i].score++
			}
		}
	}
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].score > pages[j].score })
	var b strings.Builder
	left := aiKnowledge
	for _, p := range pages {
		text := []rune("## " + p.Title + "\n" + strings.TrimSpace(p.Body) + "\n\n")
		if len(text) > left {
			if b.Len() > 0 {
				break
			}
			text = text[:left] // one page longer than all the room: its beginning
		}
		b.WriteString(string(text))
		left -= len(text)
	}
	return strings.TrimSpace(b.String()), nil
}

// stems are the question's words to look for in the pages: lowercased, without short ones, cut to their first six
// letters (a crude stem: «доставки» and «доставка» meet).
func stems(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		r := []rune(w)
		if len(r) < 3 {
			continue
		}
		if len(r) > 6 {
			r = r[:6]
		}
		if w = string(r); !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// AIBotAnswer gives the client the AI bot's answer; with AIHandoff in it (or after aiTurns answers) the dialogue goes to
// the operators, the rest of the answer — if any — said before. A dialogue an operator has taken meanwhile is left be.
func (s *Store) AIBotAnswer(ctx context.Context, sessionID, answer string) error {
	handoff := strings.Contains(answer, AIHandoff)
	answer = strings.TrimSpace(strings.ReplaceAll(answer, AIHandoff, ""))
	return s.aiBot(ctx, sessionID, answer, handoff, "ol.note_aibot_queue")
}

// AIBotFail hands the dialogue to the operators when the AI could not answer (no connection, an error of the service).
func (s *Store) AIBotFail(ctx context.Context, sessionID string) error {
	return s.aiBot(ctx, sessionID, "", true, "ol.note_aibot_error")
}

func (s *Store) aiBot(ctx context.Context, id, answer string, handoff bool, note string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var on bool
	var turns int
	var widgetID, last string
	var operator, queue *string
	if err := tx.QueryRow(ctx, `SELECT ai_bot AND status = 'open', ai_turns, widget_id::text, coalesce(queue_id, widget_id)::text, operator_id::text,
		coalesce((SELECT m.body FROM ol_messages m WHERE m.session_id = s.id AND m.author_id IS NULL AND m.kind = '' ORDER BY m.id DESC LIMIT 1), '')
		FROM ol_sessions s WHERE id::text = $1 FOR UPDATE`, id).Scan(&on, &turns, &widgetID, &queue, &operator, &last); err != nil {
		return err
	}
	if !on {
		return nil
	}
	if answer == "" && !handoff { // the AI said nothing: the operators will
		handoff, note = true, "ol.note_aibot_error"
	}
	if answer != "" {
		if err := s.botSay(ctx, tx, id, answer, nil); err != nil {
			return err
		}
		if turns+1 >= aiTurns && !handoff {
			handoff, note = true, "ol.note_aibot_turns"
		}
	}
	if !handoff {
		if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET ai_turns = ai_turns + 1, last_at = now() WHERE id::text = $1`, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if answer == "" {
		if err := s.botSay(ctx, tx, id, s.tr("ol.bot_to_queue"), nil); err != nil {
			return err
		}
	}
	if err := s.note(ctx, tx, id, note); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET ai_bot = false, ai_wait = NULL, ai_turns = ai_turns + 1, unread = unread + 1,
		waiting_since = coalesce(waiting_since, now()), last_at = now() WHERE id::text = $1`, id); err != nil {
		return err
	}
	qw, err := s.Widget(ctx, *queue)
	if err != nil {
		return err
	}
	if qw.QueueMode != "all" {
		ops, err := s.queue(ctx, tx, qw.ID)
		if err != nil {
			return err
		}
		keep, err := keeper(ctx, tx, qw, id, ops, operator)
		if err != nil {
			return err
		}
		if _, err := s.assign(ctx, tx, qw, id, keep, nil); err != nil {
			return err
		}
	}
	if w, err := s.Widget(ctx, widgetID); err != nil {
		return err
	} else if !w.InHours(time.Now()) && w.OfflineText != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind) SELECT $1::uuid, $2, 'offline'
			WHERE NOT EXISTS (SELECT 1 FROM ol_messages WHERE session_id = $1::uuid AND kind = 'offline' AND at > now() - interval '12 hours')`, id, w.OfflineText); err != nil {
			return err
		}
	}
	if err := notifyOperators(ctx, tx, id, last); err != nil {
		return err
	}
	if err := notifyInvited(ctx, tx, id, last); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
