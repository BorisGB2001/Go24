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
	TypeID                             string // a template of a smart process's cards (Entity "deal"); "" — of deals
	NumeratorID, NumeratorName         string
	Numerator                          string // its numerator's format: {NUMBER}, {YEAR}, {MONTH}, {DAY} and text
	CreatedAt                          time.Time
	Active                             bool // «Активен»: offered for new documents
	Sort                               int  // «Сортировка»: the order in the lists, then the name
	WithStamps                         bool // «С печатями и подписями» of a new document by it, unless asked otherwise
	CreatedBy, UpdatedBy               string
	UpdatedAt                          *time.Time // the last change of its settings, name or file; nil — none
	Changer                            string     // the name of who changed it last (or added it)
}

// Changed is when the template was changed last (or added).
func (t DocTemplate) Changed() time.Time {
	if t.UpdatedAt != nil {
		return *t.UpdatedAt
	}
	return t.CreatedAt
}

// DocTemplateSettings are «Активен», «Сортировка» and «С печатями и подписями» of a template.
type DocTemplateSettings struct {
	Active     bool
	Sort       int
	WithStamps bool
}

// TypeKind is the kind of templates of a smart process's cards, as DocTemplates and NewDocTemplate take it.
func TypeKind(typeID string) string {
	return "type:" + typeID
}

// DocKind is the kind of templates of a card of the entity (a smart process's — of its type).
func DocKind(entity, typeID string) string {
	if entity == "deal" && typeID != "" {
		return TypeKind(typeID)
	}
	return entity
}

// Kind is the kind of cards the template is of: an entity or TypeKind.
func (t DocTemplate) Kind() string {
	return DocKind(t.Entity, t.TypeID)
}

const tplCols = `t.id, t.name, t.entity, t.file_id::text, f.name, coalesce(t.type_id::text, ''), t.numerator_id::text, n.name,
	n.format, t.created_at, t.active, t.sort, t.with_stamps, t.created_by::text, coalesce(t.updated_by::text, ''), t.updated_at,
	coalesce((SELECT u.name FROM users u WHERE u.id = coalesce(t.updated_by, t.created_by)), '')
	FROM crm_doc_templates t JOIN files f ON f.id = t.file_id JOIN doc_numerators n ON n.id = t.numerator_id`

// tplKind is the template t's kind, as DocKind
const tplKind = `CASE WHEN t.type_id IS NULL THEN t.entity ELSE 'type:' || t.type_id END`

// DocTemplates of a kind ("" — all; TypeKind — of a smart process), active or not.
func (s *Store) DocTemplates(ctx context.Context, kind string) ([]DocTemplate, error) {
	rows, err := s.db.Query(ctx, `SELECT `+tplCols+` WHERE $1 = '' OR `+tplKind+` = $1 ORDER BY t.entity, t.sort, lower(t.name)`, kind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DocTemplate])
}

// ActiveDocTemplates are DocTemplates offered for new documents: «Активен» ones.
func (s *Store) ActiveDocTemplates(ctx context.Context, kind string) ([]DocTemplate, error) {
	rows, err := s.db.Query(ctx, `SELECT `+tplCols+` WHERE ($1 = '' OR `+tplKind+` = $1) AND t.active ORDER BY t.entity, t.sort, lower(t.name)`, kind)
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

// DocTemplatesFor are the active templates of a kind the user may make documents by («Кому доступен»).
func (s *Store) DocTemplatesFor(ctx context.Context, kind, userID string) ([]DocTemplate, error) {
	rows, err := s.db.Query(ctx, `SELECT `+tplCols+` WHERE `+tplKind+` = $1 AND t.active AND `+templateFor+` ORDER BY t.sort, lower(t.name)`, kind, userID)
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
	rows, err := s.db.Query(ctx, `SELECT `+tplCols+` WHERE t.id::text = $1`, id)
	if err != nil {
		return t, err
	}
	t, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[DocTemplate])
	if err == pgx.ErrNoRows {
		return t, ErrNotFound
	}
	return t, err
}

// SetDocTemplateSettings saves «Активен», «Сортировка» and «С печатями и подписями» of the template, changed by the user.
func (s *Store) SetDocTemplateSettings(ctx context.Context, id, userID string, set DocTemplateSettings) error {
	if set.Sort < 0 || set.Sort > 1000000 {
		return i18n.E("err.doc_template_sort")
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_doc_templates SET active = $2, sort = $3, with_stamps = $4, updated_by = $5, updated_at = now()
		WHERE id::text = $1`, id, set.Active, set.Sort, set.WithStamps, optional(userID))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// DocTemplateChanged notes who changed the template (its name, file, numerator or access) and when.
func (s *Store) DocTemplateChanged(ctx context.Context, id, userID string) error {
	_, err := s.db.Exec(ctx, `UPDATE crm_doc_templates SET updated_by = $2, updated_at = now() WHERE id::text = $1`, id, optional(userID))
	return err
}

// AddDocTemplate keeps an uploaded DOCX as a template (administrators; checked by the web layer).
func (s *Store) AddDocTemplate(ctx context.Context, userID, name, entity, fileID string) error {
	_, err := s.NewDocTemplate(ctx, userID, name, entity, fileID)
	return err
}

// NewDocTemplate is AddDocTemplate giving the template's id; the kind is an entity or a smart process's TypeKind.
func (s *Store) NewDocTemplate(ctx context.Context, userID, name, kind, fileID string) (string, error) {
	name = strings.TrimSpace(name)
	entity, typeID := kind, ""
	if t, ok := strings.CutPrefix(kind, "type:"); ok {
		entity, typeID = "deal", t
		if _, err := s.Type(ctx, typeID); err != nil {
			return "", i18n.E("err.doc_template")
		}
	}
	if name == "" || len([]rune(name)) > 200 || !(Entities[entity] || entity == TemplateEmployee || entity == TemplateInvoice) {
		return "", i18n.E("err.doc_template")
	}
	var id string
	err := s.db.QueryRow(ctx, `WITH n AS (INSERT INTO doc_numerators (name) VALUES ($1) RETURNING id)
		INSERT INTO crm_doc_templates (name, entity, type_id, file_id, created_by, numerator_id) SELECT $1, $2, $3, $4, $5, n.id
		FROM n RETURNING id::text`, name, entity, optional(typeID), fileID, userID).Scan(&id)
	return id, err
}

// DeleteDocTemplate deletes the template and its numerator, unless another template numbers by it too.
func (s *Store) DeleteDocTemplate(ctx context.Context, id string) error {
	var file, num string
	err := s.db.QueryRow(ctx, `DELETE FROM crm_doc_templates WHERE id::text = $1 RETURNING file_id::text, numerator_id::text`,
		id).Scan(&file, &num)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = s.db.Exec(ctx, `DELETE FROM files WHERE id::text = $1`, file); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM doc_numerators n WHERE n.id::text = $1
		AND NOT EXISTS (SELECT 1 FROM crm_doc_templates t WHERE t.numerator_id = n.id)`, num)
	return err
}

// RenameDocTemplate gives the template another name.
func (s *Store) RenameDocTemplate(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return i18n.E("err.doc_template")
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_doc_templates SET name = $2 WHERE id::text = $1`, id, name)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
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

// NumeratorResets are how often a numerator starts again from its first number.
var NumeratorResets = []string{"never", "day", "month", "year"}

// Numerator numbers the documents of the templates that chose it («Нумераторы»: one numerator
// may number several templates — their documents then share the numbering.
type Numerator struct {
	ID, Name  string
	Format    string // {NUMBER}, {YEAR}, {MONTH}, {DAY} and text
	Start     int64  // the first number (and again after a reset)
	Step, Pad int    // the step between numbers; the digits of {NUMBER}, with leading zeros (0 — as is)
	Reset     string // never, day, month, year
	Last      *int64 // the last number given; nil — none yet
	Period    string // the period of the last number
	CreatedAt time.Time
	Templates []string // the names of the templates numbered by it
}

const numCols = `n.id::text, n.name, n.format, n.start, n.step, n.pad, n.reset, n.last, n.period, n.created_at,
	coalesce((SELECT array_agg(t.name ORDER BY lower(t.name)) FROM crm_doc_templates t WHERE t.numerator_id = n.id), '{}')
	FROM doc_numerators n`

// Numerators are all the numerators, by name.
func (s *Store) Numerators(ctx context.Context) ([]Numerator, error) {
	rows, err := s.db.Query(ctx, `SELECT `+numCols+` ORDER BY lower(n.name), n.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Numerator])
}

func (s *Store) Numerator(ctx context.Context, id string) (Numerator, error) {
	rows, err := s.db.Query(ctx, `SELECT `+numCols+` WHERE n.id::text = $1`, id)
	if err != nil {
		return Numerator{}, err
	}
	n, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Numerator])
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// SaveNumerator adds the numerator (no ID) or changes its settings; the numbering goes on from the last number
// (a greater start number moves it forward).
func (s *Store) SaveNumerator(ctx context.Context, n Numerator) (string, error) {
	n.Name, n.Format = strings.TrimSpace(n.Name), strings.TrimSpace(n.Format)
	if n.Name == "" || len([]rune(n.Name)) > 200 {
		return "", i18n.E("err.doc_numerator_name")
	}
	if n.Format == "" || len([]rune(n.Format)) > 100 {
		return "", i18n.E("err.doc_numerator")
	}
	if n.Start < 0 || n.Step < 1 || n.Step > 1000 || n.Pad < 0 || n.Pad > 12 || !slices.Contains(NumeratorResets, n.Reset) {
		return "", i18n.E("err.doc_numerator_settings")
	}
	if n.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO doc_numerators (name, format, start, step, pad, reset) VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id::text`, n.Name, n.Format, n.Start, n.Step, n.Pad, n.Reset).Scan(&n.ID)
		return n.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE doc_numerators SET name = $2, format = $3, start = $4, step = $5, pad = $6, reset = $7
		WHERE id::text = $1`, n.ID, n.Name, n.Format, n.Start, n.Step, n.Pad, n.Reset)
	if err == nil && tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return n.ID, err
}

// DeleteNumerator deletes a numerator no template numbers by.
func (s *Store) DeleteNumerator(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM doc_numerators n WHERE n.id::text = $1
		AND NOT EXISTS (SELECT 1 FROM crm_doc_templates t WHERE t.numerator_id = n.id)`, id)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	if _, err := s.Numerator(ctx, id); err != nil {
		return err
	}
	return i18n.E("err.doc_numerator_used")
}

// SetDocTemplateNumerator chooses the numerator of a template's documents.
func (s *Store) SetDocTemplateNumerator(ctx context.Context, id, numeratorID string) error {
	tag, err := s.db.Exec(ctx, `UPDATE crm_doc_templates SET numerator_id = n.id FROM doc_numerators n
		WHERE crm_doc_templates.id::text = $1 AND n.id::text = $2`, id, numeratorID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetDocNumerator changes the format of the numbers of a template's numerator (of every template sharing it);
// the numbering goes on.
func (s *Store) SetDocNumerator(ctx context.Context, id, format string) error {
	format = strings.TrimSpace(format)
	if format == "" || len([]rune(format)) > 100 {
		return i18n.E("err.doc_numerator")
	}
	tag, err := s.db.Exec(ctx, `UPDATE doc_numerators SET format = $2
		WHERE id = (SELECT numerator_id FROM crm_doc_templates WHERE id::text = $1)`, id, format)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// numeratorPeriod is the period of t the numerator counts in: «2026-10» when it starts again every month.
func numeratorPeriod(reset string, t time.Time) string {
	switch reset {
	case "day":
		return t.Format("2006-01-02")
	case "month":
		return t.Format("2006-01")
	case "year":
		return t.Format("2006")
	}
	return ""
}

// next is the number the numerator gives at t: the start number first and in every new period, then by steps.
func (n Numerator) next(t time.Time) int64 {
	if n.Last == nil || n.Period != numeratorPeriod(n.Reset, t) {
		return n.Start
	}
	return max(*n.Last+int64(n.Step), n.Start)
}

// Preview is the number the numerator would give a document made at t.
func (n Numerator) Preview(t time.Time) string {
	return FormatDocNumber(n.Format, n.next(t), n.Pad, t)
}

// FormatDocNumber is the number n of a document made at t, as the numerator says: «Д-{YEAR}/{NUMBER}» → «Д-2026/7»;
// pad — the digits of {NUMBER} with leading zeros (3: «007»).
func FormatDocNumber(format string, n int64, pad int, t time.Time) string {
	return strings.NewReplacer("{NUMBER}", fmt.Sprintf("%0*d", pad, n), "{YEAR}", t.Format("2006"), "{MONTH}", t.Format("01"),
		"{DAY}", t.Format("02")).Replace(format)
}

// NextDocNumber takes the next number of the template's numerator (it goes through all cards and all the templates
// sharing it.
func (s *Store) NextDocNumber(ctx context.Context, templateID string, at time.Time) (string, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var n Numerator
	err = tx.QueryRow(ctx, `SELECT n.id::text, n.format, n.start, n.step, n.pad, n.reset, n.last, n.period FROM doc_numerators n
		JOIN crm_doc_templates t ON t.numerator_id = n.id WHERE t.id::text = $1 FOR UPDATE OF n`, templateID).
		Scan(&n.ID, &n.Format, &n.Start, &n.Step, &n.Pad, &n.Reset, &n.Last, &n.Period)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	num := n.next(at)
	if _, err := tx.Exec(ctx, `UPDATE doc_numerators SET last = $2, period = $3 WHERE id::text = $1`,
		n.ID, num, numeratorPeriod(n.Reset, at)); err != nil {
		return "", err
	}
	return FormatDocNumber(n.Format, num, n.Pad, at), tx.Commit(ctx)
}

// Document is a document made from a template for a card; Values are what was put in it.
type Document struct {
	ID, TemplateID, TemplateName, Entity, EntityID, Number, FileID, FileName string
	Size                                                                     int64
	Values                                                                   map[string]string
	AuthorID, Author                                                         string
	CreatedAt, UpdatedAt                                                     time.Time
	PDFFileID                                                                string     // its PDF, once made
	PublicToken                                                              string     // «Публичная ссылка»; "" — off
	ViewedAt                                                                 *time.Time // the client first opened the public link
	Stamps                                                                   bool       // «С печатями и подписями»
}

const docCols = `d.id, coalesce(d.template_id::text, ''), d.template_name, d.entity, d.entity_id::text, d.number, d.file_id::text,
	f.name, f.size, d.values, d.created_by::text, u.name, d.created_at, d.updated_at, coalesce(d.pdf_file_id::text, ''),
	coalesce(d.public_token, ''), d.viewed_at, d.stamps
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
	err = tx.QueryRow(ctx, `INSERT INTO crm_documents (template_id, template_name, entity, entity_id, number, file_id, values, created_by, stamps)
		VALUES (nullif($1, '')::uuid, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		d.TemplateID, d.TemplateName, d.Entity, d.EntityID, d.Number, d.FileID, d.Values, d.AuthorID, d.Stamps).Scan(&id)
	if err != nil {
		return "", err
	}
	body := d.TemplateName
	if d.Number != "" {
		body += " № " + d.Number
	}
	entity, card, err := docCard(ctx, tx, d.Entity, d.EntityID)
	if err != nil {
		return "", err
	}
	if err := s.event(ctx, tx, entity, card, d.AuthorID, "document", body); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// docCard is the card whose history and triggers a document of (entity, id) has: an invoice's are of its deal or lead.
func docCard(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, entity, id string) (string, string, error) {
	if entity != TemplateInvoice {
		return entity, id, nil
	}
	err := q.QueryRow(ctx, `SELECT CASE WHEN deal_id IS NULL THEN 'lead' ELSE 'deal' END, coalesce(deal_id, lead_id)::text
		FROM crm_invoices WHERE id::text = $1`, id).Scan(&entity, &id)
	return entity, id, err
}

// DocumentViewed notes the client's first opening of a document's public link («Просмотр документа клиентом» of
// the portal): the card's history says so and its triggers fire; later openings change nothing (false).
func (s *Store) DocumentViewed(ctx context.Context, d Document) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE crm_documents SET viewed_at = now() WHERE id::text = $1 AND viewed_at IS NULL`, d.ID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	body := d.TemplateName
	if d.Number != "" {
		body += " № " + d.Number
	}
	return true, s.docEvent(ctx, d.Entity, d.EntityID, d.AuthorID, "doc_viewed", body)
}

// DocumentSigned notes that all the sides signed a card's document sent for signing (title — the signing's), and its
// «Подписание документа» triggers fire.
func (s *Store) DocumentSigned(ctx context.Context, entity, id, author, title string) error {
	return s.docEvent(ctx, entity, id, author, "doc_signed", title)
}

// docEvent writes the document's line (kind: doc_viewed, doc_signed — also the triggers' kind) in its card's history
// and fires the card's triggers of the kind.
func (s *Store) docEvent(ctx context.Context, entity, id, author, kind, body string) error {
	entity, card, err := docCard(ctx, s.db, entity, id)
	if err != nil {
		return err
	}
	if err := s.event(ctx, s.db, entity, card, author, kind, body); err != nil {
		return err
	}
	return s.Fire(ctx, entity, card, kind)
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

// RemakeDocument puts the document made again with other values (with the stamps or not) in place of its file; the
// number stays.
func (s *Store) RemakeDocument(ctx context.Context, id, fileID string, values map[string]string, stamps bool) error {
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
	if _, err := tx.Exec(ctx, `UPDATE crm_documents SET file_id = $2, values = $3, stamps = $4, pdf_file_id = NULL, updated_at = now()
		WHERE id::text = $1`, id, fileID, values, stamps); err != nil {
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
