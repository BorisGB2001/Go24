package bp

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Commands are the buttons of the «Команды» step the process waits on that the user may press: one of the step's
// people, an administrator (in an administrator's ctx) too. None when the process does not wait on such a step.
func (s *Store) Commands(ctx context.Context, userID string, x Instance) []Command {
	if x.Status != "running" || x.Step >= len(x.Steps) || len(x.Branches) > 0 || x.Steps[x.Step].Kind != "commands" {
		return nil
	}
	st := x.Steps[x.Step]
	if !tasks.IsAdmin(ctx) {
		users, err := s.people(ctx, x, st)
		if err != nil || !slices.Contains(users, userID) {
			return nil
		}
	}
	return st.Commands
}

// Command presses the button n (from 0) of the «Команды» step the process waits on — step, so that a button of a page
// shown before does not press a later step's: the process goes to the command's step and on.
func (s *Store) Command(ctx context.Context, userID, id string, step, n int) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cur int
	err = tx.QueryRow(ctx, `SELECT step FROM bp_instances WHERE id::text = $1 AND status = 'running' FOR NO KEY UPDATE`, id).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	x, err := s.instance(ctx, id)
	if err != nil {
		return err
	}
	list := s.Commands(ctx, userID, x)
	if cur != step || n < 0 || n >= len(list) {
		return i18n.E("err.bp_command")
	}
	next := step + 1
	if list[n].Goto > 0 {
		next = list[n].Goto - 1
	}
	if err := advance(ctx, tx, id, step, next); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bp_log (instance_id, step, actor_id, kind, text) VALUES ($1, $2, $3, 'command', $4)`,
		id, step, userID, list[n].Title); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.run(ctx, id)
}
