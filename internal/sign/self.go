package sign

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// SelfTemplate is a template «Сотрудник» an employee makes a document by themselves (an application, as «Документы от
// сотрудников»): they sign it first, then the company's representative (GO24DEV-726).
type SelfTemplate struct {
	TemplateID, Name, SignerID, Signer string
}

// SelfTemplates are all such templates, by name.
func (s *Store) SelfTemplates(ctx context.Context) ([]SelfTemplate, error) {
	rows, _ := s.db.Query(ctx, `SELECT t.id::text, t.name, u.id::text, u.name FROM sign_self_templates st
		JOIN crm_doc_templates t ON t.id = st.template_id JOIN users u ON u.id = st.signer_id
		WHERE t.entity = 'employee' ORDER BY lower(t.name)`)
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SelfTemplate])
}

// SelfTemplate is one of them (ErrNotFound — the template is not one).
func (s *Store) SelfTemplate(ctx context.Context, templateID string) (SelfTemplate, error) {
	rows, _ := s.db.Query(ctx, `SELECT t.id::text, t.name, u.id::text, u.name FROM sign_self_templates st
		JOIN crm_doc_templates t ON t.id = st.template_id JOIN users u ON u.id = st.signer_id
		WHERE t.entity = 'employee' AND t.id::text = $1`, templateID)
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[SelfTemplate])
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// SetSelf makes the template «Сотрудник» one employees make documents by, signed after them by the active employee
// signerID; signerID "" — no longer.
func (s *Store) SetSelf(ctx context.Context, templateID, signerID, actorID string) error {
	if signerID == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM sign_self_templates WHERE template_id::text = $1`, templateID)
		return err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO sign_self_templates (template_id, signer_id, updated_by)
		SELECT t.id, u.id, nullif($3, '')::uuid FROM crm_doc_templates t, users u
		WHERE t.id::text = $1 AND t.entity = 'employee' AND u.id::text = $2 AND u.active AND NOT u.guest
		ON CONFLICT (template_id) DO UPDATE SET signer_id = EXCLUDED.signer_id, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		templateID, signerID, actorID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrParties
	}
	return err
}
