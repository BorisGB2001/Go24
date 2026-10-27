package kb

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// ErrChanged: someone saved the page since the editor opened it — saving would overwrite their text.
var ErrChanged = i18n.E("err.kb_page_changed")

// EditingFor is how long an editor counts as open after its last sign (the editor gives one every half a minute).
const EditingFor = 90 * time.Second

// Draft is what the user typed over a page and has not saved yet.
type Draft struct {
	Title, Body string
	Base        time.Time // the version of the page it is written over
	Saved       time.Time
	Changed     bool // it differs from the page as it is now
	Stale       bool // the page was saved by someone since Base
}

// SaveDraft keeps the text the user is typing over the page (base — the version of the page the editor opened, zero
// — the current one) and marks the editor open. The title may be empty while being typed.
func (s *Store) SaveDraft(ctx context.Context, userID, pageID, title, body string, base time.Time) error {
	p, sp, err := s.Page(ctx, userID, pageID)
	if err != nil {
		return err
	}
	if !sp.CanEdit {
		return ErrForbidden
	}
	if len([]rune(title)) > 300 || len(body) > 200000 {
		return i18n.E("err.kb_page")
	}
	if base.IsZero() {
		base = p.Updated
	}
	_, err = s.db.Exec(ctx, `INSERT INTO kb_drafts (user_id, page_id, title, body, base, seen_at) VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id, page_id) DO UPDATE SET title = EXCLUDED.title, body = EXCLUDED.body, base = EXCLUDED.base, seen_at = now(),
			saved_at = CASE WHEN kb_drafts.title = EXCLUDED.title AND kb_drafts.body = EXCLUDED.body THEN kb_drafts.saved_at ELSE now() END`,
		userID, p.ID, title, body, base)
	return err
}

// LeaveDraft marks the user's editor of the page closed; a draft that does not differ from the page goes.
func (s *Store) LeaveDraft(ctx context.Context, userID, pageID string) error {
	_, err := s.db.Exec(ctx, `WITH gone AS (DELETE FROM kb_drafts d USING kb_pages_all g WHERE d.user_id = $1 AND d.page_id::text = $2
			AND g.id = d.page_id AND d.title = g.title AND d.body = g.body RETURNING d.page_id)
		UPDATE kb_drafts SET seen_at = NULL WHERE user_id = $1 AND page_id::text = $2 AND page_id NOT IN (SELECT page_id FROM gone)`, userID, pageID)
	return err
}

// DropDraft throws the user's draft of the page away («Отменить черновик»).
func (s *Store) DropDraft(ctx context.Context, userID, pageID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM kb_drafts WHERE user_id = $1 AND page_id::text = $2`, userID, pageID)
	return err
}

// DraftOf is the user's draft of the page; nil — none.
func (s *Store) DraftOf(ctx context.Context, userID, pageID string) (*Draft, error) {
	var d Draft
	err := s.db.QueryRow(ctx, `SELECT d.title, d.body, d.base, d.saved_at, d.title <> g.title OR d.body <> g.body, g.updated_at <> d.base
		FROM kb_drafts d JOIN kb_pages g ON g.id = d.page_id WHERE d.user_id = $1 AND d.page_id::text = $2`, userID, pageID).
		Scan(&d.Title, &d.Body, &d.Base, &d.Saved, &d.Changed, &d.Stale)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Editing are the names of the others whose editor of the page is open now («Сейчас редактирует»).
func (s *Store) Editing(ctx context.Context, userID, pageID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT u.name FROM kb_drafts d JOIN users u ON u.id = d.user_id
		WHERE d.page_id::text = $1 AND d.user_id <> $2 AND d.seen_at > now() - $3::interval ORDER BY d.seen_at DESC`,
		pageID, userID, EditingFor.String())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Drafts are the user's drafts that differ from their pages («Мои черновики»), in the bases they still see, the
// latest first.
func (s *Store) Drafts(ctx context.Context, userID string) ([]Item, error) {
	rows, err := s.db.Query(ctx, `SELECT g.space_id::text, '', g.id::text, CASE WHEN d.title = '' THEN g.title ELSE d.title END, d.saved_at
		FROM kb_drafts d JOIN kb_pages g ON g.id = d.page_id JOIN kb_spaces sp ON sp.id = g.space_id
		WHERE d.user_id = $1 AND (d.title <> g.title OR d.body <> g.body) ORDER BY d.saved_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Item])
	if err != nil {
		return nil, err
	}
	return s.visible(ctx, userID, items)
}
