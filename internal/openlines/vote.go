package openlines

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// State is what the widget shows besides the messages: whether the dialogue is closed, the visitor's rating and may
// they rate (the line asks it once an operator has answered), may they get the conversation by e-mail.
type State struct {
	Closed           bool
	Vote             int
	CanVote, History bool
	VoteText, Email  string
}

func (s *Store) VisitorState(ctx context.Context, widgetID, token string) (State, error) {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return State{}, err
	}
	var x State
	var answered bool
	err = s.db.QueryRow(ctx, `SELECT s.status = 'closed', s.vote, s.answered_at IS NOT NULL, w.vote_on, w.history_on, w.vote_text, s.email
		FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id WHERE s.id = $1`, id).Scan(&x.Closed, &x.Vote, &answered, &x.CanVote, &x.History, &x.VoteText, &x.Email)
	x.CanVote = x.CanVote && answered
	return x, err
}

func voteMark(v int) string {
	if v > 0 {
		return "👍"
	}
	return "👎"
}

// cleanVote checks a rating (1 or -1; 0 — for the head, to take theirs back) and its comment.
func cleanVote(vote int, comment string, head bool) (string, error) {
	comment = strings.TrimSpace(comment)
	if (vote != 1 && vote != -1 && !(head && vote == 0)) || len([]rune(comment)) > 1000 {
		return "", i18n.E("err.ol_vote")
	}
	return comment, nil
}

// VisitorVote is the visitor's rating of the answer (they may change it): a service line for the operators.
func (s *Store) VisitorVote(ctx context.Context, widgetID, token string, vote int, comment string) error {
	st, err := s.VisitorState(ctx, widgetID, token)
	if err != nil {
		return err
	}
	if !st.CanVote {
		return i18n.E("err.ol_vote")
	}
	if comment, err = cleanVote(vote, comment, false); err != nil {
		return err
	}
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE ol_sessions SET vote = $2, vote_comment = $3, voted_at = now() WHERE id = $1 AND (vote <> $2 OR vote_comment <> $3)`, id, vote, comment)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	key := "ol.note_vote"
	args := []any{voteMark(vote)}
	if comment != "" {
		key, args = "ol.note_vote_comment", append(args, comment)
	}
	if err := s.note(ctx, tx, id, key, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// HeadVote is the head's rating of the operator's work in the dialogue (vote 0 takes it back); who may give it the
// web layer checks.
func (s *Store) HeadVote(ctx context.Context, userID, id string, vote int, comment string) error {
	comment, err := cleanVote(vote, comment, true)
	if err != nil {
		return err
	}
	if vote == 0 {
		comment = ""
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var head *string
	if vote != 0 {
		head = &userID
	}
	tag, err := tx.Exec(ctx, `UPDATE ol_sessions SET head_vote = $2, head_comment = $3, head_by = $4 WHERE id::text = $1`, id, vote, comment, head)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if vote != 0 {
		if err := s.note(ctx, tx, id, "ol.note_head_vote", userName(ctx, tx, userID), voteMark(vote)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SendHistory sends the visitor the conversation from the mailbox of the dialogue's operator (or else of the one who
// made the line); the address stays with the dialogue when it has none.
func (s *Store) SendHistory(ctx context.Context, widgetID, token, email string) error {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return err
	}
	email = strings.TrimSpace(email)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 200 {
		return i18n.E("err.ol_email")
	}
	var x struct {
		on            bool
		line, visitor string
		from          string
	}
	err = s.db.QueryRow(ctx, `UPDATE ol_sessions s SET history_sent_at = now(), email = CASE WHEN s.email = '' THEN $2 ELSE s.email END
		FROM ol_widgets w WHERE w.id = s.widget_id AND s.id = $1 AND (s.history_sent_at IS NULL OR s.history_sent_at < now() - interval '1 minute')
		RETURNING w.history_on, w.name, s.name, coalesce(s.operator_id, w.created_by)::text`, id, email).Scan(&x.on, &x.line, &x.visitor, &x.from)
	if errors.Is(err, pgx.ErrNoRows) {
		return i18n.E("err.ol_flood")
	}
	if err != nil {
		return err
	}
	if !x.on || s.SendMail == nil {
		return i18n.E("err.ol_history")
	}
	msgs, err := s.messages(ctx, id, 0)
	if err != nil {
		return err
	}
	if x.visitor == "" {
		x.visitor = s.tr("ol.history_you")
	}
	var b strings.Builder
	b.WriteString(s.tr("ol.history_intro", x.line) + "\n\n")
	for _, m := range msgs {
		who := x.visitor
		switch {
		case m.Kind == "note":
			continue
		case m.Auto():
			who = x.line
		case m.Operator != "":
			who = m.Operator
		}
		b.WriteString(who + " (" + m.At.Local().Format("02.01.2006 15:04") + "):\n" + m.Body + "\n\n")
	}
	if err := s.SendMail(ctx, x.from, email, s.tr("ol.history_subject", x.line), b.String()); err != nil {
		return i18n.E("err.ol_history")
	}
	return nil
}
