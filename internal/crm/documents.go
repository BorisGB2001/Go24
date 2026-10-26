package crm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Генератор документов»: DOCX templates of each kind of card (the files are go24's own, the filling is
// package docgen).

// Entities are the kinds of CRM cards.
var Entities = map[string]bool{"deal": true, "lead": true, "contact": true, "company": true}

// TemplateEmployee is the kind of templates of HR documents («Подпись для сотрудников»), filled from the employee's
// profile, not a card.
const TemplateEmployee = "employee"

// TemplateInvoice is the kind of templates of the quotes and invoices («Счета»): their print forms.
const TemplateInvoice = "invoice"

// DocTemplate is a template and where its file is.
type DocTemplate struct {
	ID, Name, Entity, FileID, FileName string
	Numerator                          string // the number's format: {NUMBER}, {YEAR}, {MONTH}, {DAY} and text
	CreatedAt                          time.Time
}

// DocTemplates of an entity ("" — all).
func (s *Store) DocTemplates(ctx context.Context, entity string) ([]DocTemplate, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, t.entity, t.file_id::text, f.name, t.numerator, t.created_at FROM crm_doc_templates t
		JOIN files f ON f.id = t.file_id WHERE $1 = '' OR t.entity = $1 ORDER BY t.entity, lower(t.name)`, entity)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DocTemplate])
}

// templateFor: the template t may be used by the user $2 — nobody is chosen, or they are, or a department of theirs
// (or one above it) is
const templateFor = `(NOT EXISTS (SELECT 1 FROM crm_doc_template_access a WHERE a.template_id = t.id)
	OR EXISTS (SELECT 1 FROM crm_doc_template_access a WHERE a.template_id = t.id AND (a.user_id = $2 OR a.department_id IN (
		WITH RECURSIVE dep AS (SELECT department_id AS id FROM user_departments WHERE user_id = $2
			UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
		SELECT id FROM dep))))`

// DocTemplatesFor are the templates of an entity the user may make documents by («Кому доступен»).
func (s *Store) DocTemplatesFor(ctx context.Context, entity, userID string) ([]DocTemplate, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, t.entity, t.file_id::text, f.name, t.numerator, t.created_at FROM crm_doc_templates t
		JOIN files f ON f.id = t.file_id WHERE t.entity = $1 AND `+templateFor+` ORDER BY lower(t.name)`, entity, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DocTemplate])
}

// MayUseDocTemplate: the user may make documents by the template.
func (s *Store) MayUseDocTemplate(ctx context.Context, id, userID string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_doc_templates t WHERE t.id::text = $1 AND `+templateFor+`)`, id, userID).Scan(&ok)
	return ok, err
}

// DocTemplateAccess is «Кому доступен» of every template: the people and the departments («dept:ID») by template.
func (s *Store) DocTemplateAccess(ctx context.Context) (map[string][]Ref, error) {
	rows, err := s.db.Query(ctx, `SELECT a.template_id::text, coalesce(a.user_id::text, 'dept:' || a.department_id::text), coalesce(u.name, d.name, '')
		FROM crm_doc_template_access a LEFT JOIN users u ON u.id = a.user_id LEFT JOIN departments d ON d.id = a.department_id
		ORDER BY a.department_id IS NULL, coalesce(d.name, u.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	access := map[string][]Ref{}
	for rows.Next() {
		var tpl string
		var r Ref
		if err := rows.Scan(&tpl, &r.ID, &r.Name); err != nil {
			return nil, err
		}
		access[tpl] = append(access[tpl], r)
	}
	return access, rows.Err()
}

// SetDocTemplateAccess chooses who may use the template: people and departments («dept:ID»); none — everybody.
func (s *Store) SetDocTemplateAccess(ctx context.Context, id string, to []string) error {
	var users, depts []string
	for _, v := range to {
		if d, ok := strings.CutPrefix(v, "dept:"); ok {
			depts = append(depts, d)
		} else if v != "" {
			users = append(users, v)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM crm_doc_template_access WHERE template_id = (SELECT id FROM crm_doc_templates WHERE id::text = $1)`, id)
	if err == nil && tag.RowsAffected() == 0 {
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM crm_doc_templates WHERE id::text = $1`, id).Scan(&n); err == nil && n == 0 {
			err = ErrNotFound
		}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO crm_doc_template_access (template_id, user_id)
			SELECT $1::uuid, u.id FROM users u WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, users)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO crm_doc_template_access (template_id, department_id)
			SELECT $1::uuid, d.id FROM departments d WHERE d.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, depts)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) DocTemplate(ctx context.Context, id string) (DocTemplate, error) {
	var t DocTemplate
	err := s.db.QueryRow(ctx, `SELECT t.id, t.name, t.entity, t.file_id::text, f.name, t.numerator, t.created_at FROM crm_doc_templates t
		JOIN files f ON f.id = t.file_id WHERE t.id::text = $1`, id).Scan(&t.ID, &t.Name, &t.Entity, &t.FileID, &t.FileName, &t.Numerator, &t.CreatedAt)
	if err == pgx.ErrNoRows {
		return t, ErrNotFound
	}
	return t, err
}

// AddDocTemplate keeps an uploaded DOCX as a template (administrators; checked by the web layer).
func (s *Store) AddDocTemplate(ctx context.Context, userID, name, entity, fileID string) error {
	_, err := s.NewDocTemplate(ctx, userID, name, entity, fileID)
	return err
}

// NewDocTemplate is AddDocTemplate giving the template's id.
func (s *Store) NewDocTemplate(ctx context.Context, userID, name, entity, fileID string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 || !(Entities[entity] || entity == TemplateEmployee || entity == TemplateInvoice) {
		return "", i18n.E("err.doc_template")
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO crm_doc_templates (name, entity, file_id, created_by) VALUES ($1, $2, $3, $4)
		RETURNING id::text`, name, entity, fileID, userID).Scan(&id)
	return id, err
}

func (s *Store) DeleteDocTemplate(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM files WHERE id = (SELECT file_id FROM crm_doc_templates WHERE id::text = $1)`, id)
	return err
}

// SetDocTemplateFile makes the file the template's (edited in ONLYOFFICE); the old one goes.
func (s *Store) SetDocTemplateFile(ctx context.Context, id, fileID string) error {
	var old string
	err := s.db.QueryRow(ctx, `WITH o AS (SELECT file_id FROM crm_doc_templates WHERE id::text = $1 FOR UPDATE)
		UPDATE crm_doc_templates t SET file_id = $2 FROM o WHERE t.id::text = $1 RETURNING o.file_id::text`, id, fileID).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM files WHERE id::text = $1`, old)
	return err
}

// SetDocNumerator changes the format of the numbers of a template's documents; the counter goes on.
func (s *Store) SetDocNumerator(ctx context.Context, id, format string) error {
	format = strings.TrimSpace(format)
	if format == "" || len([]rune(format)) > 100 {
		return i18n.E("err.doc_numerator")
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_doc_templates SET numerator = $2 WHERE id::text = $1`, id, format)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// FormatDocNumber is the number n of a document made at t, as the numerator says: «Д-{YEAR}/{NUMBER}» → «Д-2026/7».
func FormatDocNumber(format string, n int64, t time.Time) string {
	return strings.NewReplacer("{NUMBER}", fmt.Sprint(n), "{YEAR}", t.Format("2006"), "{MONTH}", t.Format("01"),
		"{DAY}", t.Format("02")).Replace(format)
}

// NextDocNumber takes the next number of the template's counter (it goes through all cards.
func (s *Store) NextDocNumber(ctx context.Context, templateID string, at time.Time) (string, error) {
	var format string
	var n int64
	err := s.db.QueryRow(ctx, `UPDATE crm_doc_templates SET counter = counter + 1 WHERE id::text = $1 RETURNING numerator, counter`,
		templateID).Scan(&format, &n)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	return FormatDocNumber(format, n, at), err
}

// Document is a document made from a template for a card; Values are what was put in it.
type Document struct {
	ID, TemplateID, TemplateName, Entity, EntityID, Number, FileID, FileName string
	Size                                                                     int64
	Values                                                                   map[string]string
	AuthorID, Author                                                         string
	CreatedAt, UpdatedAt                                                     time.Time
	PDFFileID                                                                string // its PDF, once made
	PublicToken                                                              string // «Публичная ссылка»; "" — off
}

const docCols = `d.id, coalesce(d.template_id::text, ''), d.template_name, d.entity, d.entity_id::text, d.number, d.file_id::text,
	f.name, f.size, d.values, d.created_by::text, u.name, d.created_at, d.updated_at, coalesce(d.pdf_file_id::text, ''),
	coalesce(d.public_token, '')
	FROM crm_documents d JOIN files f ON f.id = d.file_id JOIN users u ON u.id = d.created_by`

// AddDocument keeps a made document at its card and notes it in the card's history.
func (s *Store) AddDocument(ctx context.Context, d Document) (string, error) {
	if !Entities[d.Entity] && d.Entity != TemplateInvoice {
		return "", i18n.E("err.doc_template")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO crm_documents (template_id, template_name, entity, entity_id, number, file_id, values, created_by)
		VALUES (nullif($1, '')::uuid, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		d.TemplateID, d.TemplateName, d.Entity, d.EntityID, d.Number, d.FileID, d.Values, d.AuthorID).Scan(&id)
	if err != nil {
		return "", err
	}
	body := d.TemplateName
	if d.Number != "" {
		body += " № " + d.Number
	}
	entity, card := d.Entity, d.EntityID
	if entity == TemplateInvoice { // the note goes to the deal or the lead of the invoice
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN deal_id IS NULL THEN 'lead' ELSE 'deal' END, coalesce(deal_id, lead_id)::text
			FROM crm_invoices WHERE id::text = $1`, card).Scan(&entity, &card); err != nil {
			return "", err
		}
	}
	if err := s.event(ctx, tx, entity, card, d.AuthorID, "document", body); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// Documents of a card, newest first.
func (s *Store) Documents(ctx context.Context, entity, id string) ([]Document, error) {
	rows, err := s.db.Query(ctx, `SELECT `+docCols+` WHERE d.entity = $1 AND d.entity_id::text = $2 ORDER BY d.created_at DESC`, entity, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Document])
}

// Document of the card (entity, cardID): another card's is not found.
func (s *Store) Document(ctx context.Context, entity, cardID, id string) (Document, error) {
	rows, err := s.db.Query(ctx, `SELECT `+docCols+` WHERE d.id::text = $1 AND d.entity = $2 AND d.entity_id::text = $3`, id, entity, cardID)
	if err != nil {
		return Document{}, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Document])
	if err == pgx.ErrNoRows {
		return d, ErrNotFound
	}
	return d, err
}

// DocumentByID is a document of any card (the caller checks the card).
func (s *Store) DocumentByID(ctx context.Context, id string) (Document, error) {
	rows, err := s.db.Query(ctx, `SELECT `+docCols+` WHERE d.id::text = $1`, id)
	if err != nil {
		return Document{}, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Document])
	if err == pgx.ErrNoRows {
		return d, ErrNotFound
	}
	return d, err
}

// RemakeDocument puts the document made again with other values in place of its file; the number stays.
func (s *Store) RemakeDocument(ctx context.Context, id, fileID string, values map[string]string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var old, pdf string
	if err := tx.QueryRow(ctx, `SELECT file_id::text, coalesce(pdf_file_id::text, '') FROM crm_documents WHERE id::text = $1 FOR UPDATE`,
		id).Scan(&old, &pdf); err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm_documents SET file_id = $2, values = $3, pdf_file_id = NULL, updated_at = now() WHERE id::text = $1`,
		id, fileID, values); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM files WHERE id::text IN ($1, $2)`, old, pdf); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteDocument deletes the document with its files.
func (s *Store) DeleteDocument(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM files WHERE id IN (SELECT file_id FROM crm_documents WHERE id::text = $1
		UNION SELECT pdf_file_id FROM crm_documents WHERE id::text = $1)`, id)
	return err
}

// SetDocumentPDF keeps the PDF made of the document; when the document was remade meanwhile (another file), the PDF
// is of the old one and is not kept (false).
func (s *Store) SetDocumentPDF(ctx context.Context, id, fileID, pdfID string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE crm_documents SET pdf_file_id = $3 WHERE id::text = $1 AND file_id::text = $2 AND pdf_file_id IS NULL`,
		id, fileID, pdfID)
	return err == nil && tag.RowsAffected() == 1, err
}

// SetDocumentPublic switches the document's public link on (a new token each time) or off.
func (s *Store) SetDocumentPublic(ctx context.Context, id string, on bool) (string, error) {
	token := ""
	if on {
		b := make([]byte, 18)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		token = base64.RawURLEncoding.EncodeToString(b)
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_documents SET public_token = nullif($2, '') WHERE id::text = $1`, id, token)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return token, err
}

// PublicDocument is the document of a public link.
func (s *Store) PublicDocument(ctx context.Context, token string) (Document, error) {
	if token == "" {
		return Document{}, ErrNotFound
	}
	rows, err := s.db.Query(ctx, `SELECT `+docCols+` WHERE d.public_token = $1`, token)
	if err != nil {
		return Document{}, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Document])
	if err == pgx.ErrNoRows {
		return d, ErrNotFound
	}
	return d, err
}

// MyCompany is the card of «Моя компания» — the CRM company whose details and requisites the templates have as
// {MyCompany.…} ("" — not chosen).
func (s *Store) MyCompany(ctx context.Context) (string, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = 'doc_my_company'`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	var id string
	if err == nil {
		err = json.Unmarshal(raw, &id)
	}
	return id, err
}

// DocImageKinds are the pictures of «Моя компания» the templates have as {MyCompany.<kind>}: the stamp and the
// facsimile signatures of the director and the chief accountant, as the requisites have them.
var DocImageKinds = []string{"Stamp", "DirectorSignature", "AccountantSignature"}

// DocImages are the files of the pictures of «Моя компания» by their kinds (those uploaded).
func (s *Store) DocImages(ctx context.Context) (map[string]string, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = 'doc_images'`).Scan(&raw)
	out := map[string]string{}
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

// SetDocImage keeps the file of a picture of «Моя компания» (fileID "" — removes it); the file it had before ("" —
// none), to delete.
func (s *Store) SetDocImage(ctx context.Context, userID, kind, fileID string) (string, error) {
	if !slices.Contains(DocImageKinds, kind) {
		return "", ErrNotFound
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = 'doc_images' FOR UPDATE`).Scan(&raw)
	images := map[string]string{}
	switch {
	case err == nil:
		err = json.Unmarshal(raw, &images)
	case errors.Is(err, pgx.ErrNoRows):
		err = nil
	}
	if err != nil {
		return "", err
	}
	old := images[kind]
	if fileID == "" {
		delete(images, kind)
	} else {
		images[kind] = fileID
	}
	raw, _ = json.Marshal(images)
	if _, err := tx.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ('doc_images', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, raw, userID); err != nil {
		return "", err
	}
	return old, tx.Commit(ctx)
}

// SetMyCompany chooses «Моя компания» ("" — none).
func (s *Store) SetMyCompany(ctx context.Context, userID, companyID string) error {
	if companyID != "" {
		if _, err := s.Company(ctx, companyID); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(companyID)
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ('doc_my_company', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, raw, userID)
	return err
}
