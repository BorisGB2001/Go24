package marketing

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Шаблоны писем» as in «Маркетинг»: letters saved to start campaigns from — by anyone who works with
// campaigns, changed by their author or an administrator. The ready-made ones come with go24 (web, by language).

// Template is a saved letter.
type Template struct {
	ID, Name, Subject, Body string
	CreatedBy, AuthorName   string
	UpdatedAt               time.Time
}

const templateCols = `t.id::text, t.name, t.subject, t.body, coalesce(t.created_by::text, ''), coalesce(u.name, ''), t.updated_at
	FROM mkt_templates t LEFT JOIN users u ON u.id = t.created_by`

// Templates are the company's templates by name.
func (s *Store) Templates(ctx context.Context) ([]Template, error) {
	rows, err := s.db.Query(ctx, `SELECT `+templateCols+` ORDER BY lower(t.name), t.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Template])
}

// Template is one of them.
func (s *Store) Template(ctx context.Context, id string) (Template, error) {
	rows, err := s.db.Query(ctx, `SELECT `+templateCols+` WHERE t.id::text = $1`, id)
	if err != nil {
		return Template{}, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Template])
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return t, err
}

// SaveTemplate adds a template (t.ID "") or changes one: the author's or, by an administrator, any.
func (s *Store) SaveTemplate(ctx context.Context, userID string, admin bool, t Template) (string, error) {
	t.Name, t.Subject = strings.TrimSpace(t.Name), strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(t.Subject))
	if t.Name == "" || len([]rune(t.Name)) > maxName || len([]rune(t.Subject)) > maxSubject || len(t.Body) > maxBody {
		return "", i18n.E("err.mkt_template")
	}
	if t.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO mkt_templates (name, subject, body, created_by) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			t.Name, t.Subject, t.Body, userID).Scan(&t.ID)
		return t.ID, err
	}
	if _, err := s.mayChangeTemplate(ctx, userID, admin, t.ID); err != nil {
		return "", err
	}
	_, err := s.db.Exec(ctx, `UPDATE mkt_templates SET name = $2, subject = $3, body = $4, updated_at = now() WHERE id::text = $1`,
		t.ID, t.Name, t.Subject, t.Body)
	return t.ID, err
}

// DeleteTemplate removes a template; the campaigns made from it keep their letters.
func (s *Store) DeleteTemplate(ctx context.Context, userID string, admin bool, id string) error {
	if _, err := s.mayChangeTemplate(ctx, userID, admin, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM mkt_templates WHERE id::text = $1`, id)
	return err
}

func (s *Store) mayChangeTemplate(ctx context.Context, userID string, admin bool, id string) (Template, error) {
	t, err := s.Template(ctx, id)
	if err == nil && !admin && t.CreatedBy != userID {
		err = ErrForbidden
	}
	return t, err
}
