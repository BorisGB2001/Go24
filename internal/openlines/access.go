package openlines

import (
	"context"
	"strconv"
)

// Who is the one looking at the dialogues, as the roles of the open lines: All — the portal's administrators
// and the lines' heads (every dialogue); otherwise an operator — their own dialogues, the unassigned ones of the lines
// whose queue they are in, and those of Team (the people of the departments they head).
type Who struct {
	UserID string
	All    bool
	Team   []string
}

// cond is the SQL condition on a dialogue (s) and its line (w) of the dialogues the user may see.
func (x Who) cond(arg func(any) string) string {
	if x.All {
		return "true"
	}
	if x.UserID == "" {
		return "false"
	}
	me, team := arg(x.UserID), arg(nonNil(x.Team))
	return `(s.operator_id::text = ` + me + ` OR s.operator_id::text = ANY(` + team + `::text[])
		OR (s.operator_id IS NULL AND (` + me + ` = ANY(w.operators::text[]) OR (cardinality(w.operators) = 0 AND w.created_by::text = ` + me + `))))`
}

// See: may the user see the dialogue (ErrNotFound — no such one or not theirs)?
func (s *Store) See(ctx context.Context, x Who, id string) error {
	args := []any{id}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.id::text = $1 AND `+x.cond(arg)+`)`, args...).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// Heads are the lines' heads: they see every dialogue and everyone's statistics, rate the operators' work.
func (s *Store) Heads(ctx context.Context) ([]string, error) {
	var ids []string
	err := s.db.QueryRow(ctx, `SELECT coalesce(array_agg(user_id::text ORDER BY added_at), '{}') FROM ol_heads`).Scan(&ids)
	return ids, err
}

// IsHead: is the user one of the lines' heads?
func (s *Store) IsHead(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ol_heads WHERE user_id::text = $1)`, userID).Scan(&ok)
	return ok, err
}

// SetHeads makes these users the lines' heads (administrators; checked by the web layer).
func (s *Store) SetHeads(ctx context.Context, by string, ids []string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM ol_heads WHERE NOT (user_id::text = ANY($1::text[]))`, nonNil(ids)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ol_heads (user_id, added_by) SELECT u.id, $2 FROM users u WHERE u.id::text = ANY($1::text[])
		ON CONFLICT DO NOTHING`, nonNil(ids), by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
