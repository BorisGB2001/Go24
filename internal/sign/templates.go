package sign

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Template is a template of sending: the document, the company's signers and the reminders, kept once;
// sending by it, only the clients are given.
type Template struct {
	ID, Title, FileID, FileName string
	Signers                     []string
	Reviewers, Observers        []string // approve before the signers; see the document
	RemindDays                  int
	AuthorID, Author            string
	CreatedAt                   time.Time
}

const templateCols = `t.id::text, t.title, t.file_id::text, f.name, t.signers::text[], t.reviewers::text[], t.observers::text[], t.remind_days, t.created_by::text, u.name, t.created_at
	FROM sign_templates t JOIN files f ON f.id = t.file_id JOIN users u ON u.id = t.created_by`

func scanTemplate(r pgx.CollectableRow) (Template, error) {
	var t Template
	return t, r.Scan(&t.ID, &t.Title, &t.FileID, &t.FileName, &t.Signers, &t.Reviewers, &t.Observers, &t.RemindDays, &t.AuthorID, &t.Author, &t.CreatedAt)
}

// Templates are all the templates of sending, by title.
func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+templateCols+` ORDER BY lower(t.title)`)
	return pgx.CollectRows(rows, scanTemplate)
}

// Template is the template of sending by id.
func (s *Store) Template(ctx context.Context, id string) (Template, error) {
	rows, _ := s.db.Query(ctx, `SELECT `+templateCols+` WHERE t.id::text = $1`, id)
	t, err := pgx.CollectExactlyOneRow(rows, scanTemplate)
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, ErrNotFound
	}
	return t, err
}

// NewTemplate keeps a template of sending; its id.
func (s *Store) NewTemplate(ctx context.Context, t Template) (string, error) {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" || len([]rune(t.Title)) > 300 {
		return "", ErrTitle
	}
	for _, ids := range []*[]string{&t.Signers, &t.Reviewers, &t.Observers} {
		if *ids == nil {
			*ids = []string{}
		}
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO sign_templates (title, file_id, signers, reviewers, observers, remind_days, created_by)
		VALUES ($1, $2, $3::uuid[], $4::uuid[], $5::uuid[], $6, $7) RETURNING id::text`,
		t.Title, t.FileID, t.Signers, t.Reviewers, t.Observers, RemindOf(t.RemindDays), t.AuthorID).Scan(&id)
	return id, err
}

// DeleteTemplate removes the template with its file.
func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM files WHERE id = (SELECT file_id FROM sign_templates WHERE id::text = $1)`, id)
	return err
}
