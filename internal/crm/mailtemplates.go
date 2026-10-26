package crm

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Шаблоны писем CRM»: the subject and text of a letter to a client for one kind of card, with the
// card's fields in braces as the document templates have them ({Title}, {Amount}, {Бюджет}); a shared one is the
// whole company's, else its author's only. The card's «Письмо» fills one in (FillMailTemplate).

// MailTemplate is a template of a letter.
type MailTemplate struct {
	ID, Name, Entity, Subject, Body string
	Shared                          bool
	CreatedBy, AuthorName           string
	UpdatedAt                       time.Time
}

// Limits of a template.
const (
	maxMailTemplateName = 200
	maxMailTemplateBody = 200 << 10
)

const mailTemplateCols = `t.id::text, t.name, t.entity, t.subject, t.body, t.shared, coalesce(t.created_by::text, ''),
	coalesce(u.name, ''), t.updated_at FROM crm_mail_templates t LEFT JOIN users u ON u.id = t.created_by`

// MailTemplates are the templates the user may write with: the shared ones and their own — of an entity ("" — all).
func (s *Store) MailTemplates(ctx context.Context, userID, entity string) ([]MailTemplate, error) {
	rows, err := s.db.Query(ctx, `SELECT `+mailTemplateCols+`
		WHERE ($2 = '' OR t.entity = $2) AND (t.shared OR t.created_by::text = $1) ORDER BY t.entity, lower(t.name)`, userID, entity)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[MailTemplate])
}

// MailTemplateOf is one of them (a template of another, not shared, is not found).
func (s *Store) MailTemplateOf(ctx context.Context, userID, id string) (MailTemplate, error) {
	rows, err := s.db.Query(ctx, `SELECT `+mailTemplateCols+` WHERE t.id::text = $2 AND (t.shared OR t.created_by::text = $1)`, userID, id)
	if err != nil {
		return MailTemplate{}, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[MailTemplate])
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return t, err
}

// SaveMailTemplate adds a template (t.ID "") or changes one: its author's or, by an administrator, any.
func (s *Store) SaveMailTemplate(ctx context.Context, userID string, admin bool, t MailTemplate) (string, error) {
	t.Name, t.Subject = strings.TrimSpace(t.Name), strings.TrimSpace(t.Subject)
	if t.Name == "" || len([]rune(t.Name)) > maxMailTemplateName || !Entities[t.Entity] || len([]rune(t.Subject)) > 1000 ||
		len(t.Body) > maxMailTemplateBody || (t.Subject == "" && strings.TrimSpace(t.Body) == "") {
		return "", i18n.E("err.crm_mail_template")
	}
	if t.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO crm_mail_templates (name, entity, subject, body, shared, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, t.Name, t.Entity, t.Subject, t.Body, t.Shared, userID).Scan(&t.ID)
		return t.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_mail_templates SET name = $3, entity = $4, subject = $5, body = $6, shared = $7, updated_at = now()
		WHERE id::text = $1 AND ($8 OR created_by::text = $2)`, t.ID, userID, t.Name, t.Entity, t.Subject, t.Body, t.Shared, admin)
	if err == nil && tag.RowsAffected() == 0 {
		err = s.mailTemplateMissing(ctx, userID, t.ID)
	}
	return t.ID, err
}

// DeleteMailTemplate removes a template: its author's or, by an administrator, any.
func (s *Store) DeleteMailTemplate(ctx context.Context, userID string, admin bool, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_mail_templates WHERE id::text = $1 AND ($3 OR created_by::text = $2)`, id, userID, admin)
	if err == nil && tag.RowsAffected() == 0 {
		err = s.mailTemplateMissing(ctx, userID, id)
	}
	return err
}

// mailTemplateMissing tells why a template was not changed: another's shared one — forbidden, else not found.
func (s *Store) mailTemplateMissing(ctx context.Context, userID, id string) error {
	if _, err := s.MailTemplateOf(ctx, userID, id); err != nil {
		return err
	}
	return ErrForbidden
}

var placeholder = regexp.MustCompile(`\{([^{}\n]{1,100})\}`)

// FillMailTemplate puts the card's values into a template's subject and text: {Name} → values[Name]; in the HTML
// text a value is escaped; a field the card has not stays empty, a brace that is not a field stays as it is.
func FillMailTemplate(t MailTemplate, values map[string]string, known func(string) bool) (subject, body string) {
	fill := func(s string, esc func(string) string) string {
		return placeholder.ReplaceAllStringFunc(s, func(m string) string {
			k := strings.TrimSpace(m[1 : len(m)-1])
			if v, ok := values[k]; ok {
				return esc(v)
			}
			if known != nil && known(k) {
				return ""
			}
			return m
		})
	}
	plain := func(s string) string { return s }
	return fill(t.Subject, plain), fill(t.Body, htmlText)
}

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;", "\n", "<br>")

// htmlText is a value as HTML text: escaped, its lines kept.
func htmlText(s string) string { return htmlEscaper.Replace(s) }

// Addressee is an e-mail of the card's client, as the «Кому» of its letter offers it.
type Addressee struct {
	Email, Name string
}

// ClientEmails are the e-mails of the card's client: a lead's or contact's own; a company's and its contacts'; a
// deal's contacts' (the main one first) and its company's.
func (s *Store) ClientEmails(ctx context.Context, entity, id string) ([]Addressee, error) {
	var q string
	switch entity {
	case "lead":
		q = `SELECT email, coalesce(nullif(name, ''), title), 0 FROM crm_leads WHERE id::text = $1`
	case "contact":
		q = `SELECT email, name, 0 FROM crm_contacts WHERE id::text = $1`
	case "company":
		q = `SELECT email, title, 0 FROM crm_companies WHERE id::text = $1
			UNION ALL SELECT email, name, 1 FROM crm_contacts WHERE company_id::text = $1`
	case "deal":
		q = `SELECT x.email, x.name, CASE WHEN x.id = d.contact_id THEN 0 ELSE 1 END FROM crm_deals d
				JOIN crm_contacts x ON x.id = d.contact_id OR x.id IN (SELECT contact_id FROM crm_deal_contacts WHERE deal_id = d.id)
			WHERE d.id::text = $1
			UNION ALL SELECT c.email, c.title, 2 FROM crm_deals d JOIN crm_companies c ON c.id = d.company_id WHERE d.id::text = $1`
	default:
		return nil, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `SELECT email, name FROM (`+q+`) a(email, name, o) WHERE trim(email) <> '' ORDER BY o, lower(name)`, id)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Addressee])
	seen, out := map[string]bool{}, list[:0]
	for _, a := range list {
		a.Email = strings.TrimSpace(a.Email)
		if k := strings.ToLower(a.Email); !seen[k] {
			seen[k] = true
			out = append(out, a)
		}
	}
	return out, err
}
