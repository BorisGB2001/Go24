package kb

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Page templates, as in Confluence and корпоративного портала: «Новая страница» starts from a ready text — one of go24's own (builtin
// pages, their texts in the locales) or a page of the company saved as a template (kb_templates of kind 'page', with
// copies of its pictures and files). {{date}}, {{user}} and {{title}} in the text are filled in when the page is made.

// pageBuiltins are go24's page templates: «kb.ptpl.ID» is the name, «….desc», «….title» and «….body» the rest.
var pageBuiltins = []string{"meeting", "howto", "requirements", "decision", "retro", "faq"}

// PageTemplates are go24's page templates (tr gives their texts) and the company's, newest first; Pages is 1.
func (s *Store) PageTemplates(ctx context.Context, userID string, tr func(string) string) ([]Template, error) {
	var list []Template
	for _, id := range pageBuiltins {
		list = append(list, Template{ID: "p:" + id, Name: tr("kb.ptpl." + id), Description: tr("kb.ptpl." + id + ".desc"),
			Builtin: true, Pages: 1})
	}
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, t.description, coalesce(u.name, ''), t.created_at,
			$2 OR t.created_by IS NOT DISTINCT FROM $1::uuid
		FROM kb_templates t LEFT JOIN users u ON u.id = t.created_by WHERE t.kind = 'page' ORDER BY t.created_at DESC`, userID, tasks.IsAdmin(ctx))
	if err != nil {
		return nil, err
	}
	own, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Template, error) {
		t := Template{Pages: 1}
		err := r.Scan(&t.ID, &t.Name, &t.Description, &t.Author, &t.Created, &t.CanDelete)
		return t, err
	})
	return append(list, own...), err
}

// SavePageTemplate saves the text of a page as a template of the company («Сохранить как шаблон страницы»): who may
// edit its base. The name is the page's title when left empty.
func (s *Store) SavePageTemplate(ctx context.Context, userID, pageID, name, description string, copyBytes CopyBytes) (string, error) {
	p, sp, err := s.Page(ctx, userID, pageID)
	if err != nil {
		return "", err
	}
	if !sp.CanEdit {
		return "", ErrForbidden
	}
	if name = strings.TrimSpace(name); name == "" {
		name = p.Title
	}
	if len([]rune(name)) > 200 {
		return "", i18n.E("err.kb_name")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO kb_templates (kind, name, description, pages, created_by) VALUES ('page', $1, $2, '[]', $3) RETURNING id`,
		name, strings.TrimSpace(description), userID).Scan(&id); err != nil {
		return "", err
	}
	texts, err := copyFiles(ctx, tx, copyBytes, userID, []string{p.Body}, "kb_space_id", sp.ID, "kb_template_id", id)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal([]TemplatePage{{Ref: p.ID, Title: p.Title, Body: texts[0]}})
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE kb_templates SET pages = $2 WHERE id = $1`, id, data); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// PageVars are what the text of a template is filled in with: {{date}} — today, {{user}} — who makes the page (left
// empty — their name in go24).
type PageVars struct{ Date, User string }

// fillVars puts the values in place of {{date}}, {{user}} and {{title}}.
func fillVars(text, title string, v PageVars) string {
	return strings.NewReplacer("{{date}}", v.Date, "{{user}}", v.User, "{{title}}", title).Replace(text)
}

// CreatePageFromTemplate writes a new page (as CreatePage does) with the text of the template; an empty title is the
// template's own. tr gives the texts of go24's templates.
func (s *Store) CreatePageFromTemplate(ctx context.Context, userID, spaceID, parentID, templateID, title string, vars PageVars,
	tr func(string) string, copyBytes CopyBytes) (string, error) {
	sp, err := s.Space(ctx, userID, spaceID)
	if err != nil {
		return "", err
	}
	if !sp.CanEdit {
		return "", ErrForbidden
	}
	var tp TemplatePage
	own := !strings.HasPrefix(templateID, "p:")
	if own {
		var pages []TemplatePage
		err := s.db.QueryRow(ctx, `SELECT pages FROM kb_templates WHERE id::text = $1 AND kind = 'page'`, templateID).Scan(&pages)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && len(pages) == 0) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
		tp = pages[0]
	} else {
		key := strings.TrimPrefix(templateID, "p:")
		if !slices.Contains(pageBuiltins, key) {
			return "", ErrNotFound
		}
		tp = TemplatePage{Title: tr("kb.ptpl." + key + ".title"), Body: tr("kb.ptpl." + key + ".body")}
	}
	if vars.User == "" {
		if err := s.db.QueryRow(ctx, `SELECT coalesce(name, '') FROM users WHERE id = $1`, userID).Scan(&vars.User); err != nil {
			return "", err
		}
	}
	if title = strings.TrimSpace(title); title == "" {
		title = fillVars(tp.Title, "", vars)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	body := tp.Body
	if own { // the page gets copies of the template's pictures and files
		texts, err := copyFiles(ctx, tx, copyBytes, userID, []string{body}, "kb_template_id", templateID, "kb_space_id", sp.ID)
		if err != nil {
			return "", err
		}
		body = texts[0]
	}
	id, err := s.CreatePage(ctx, userID, sp.ID, parentID, title, fillVars(body, title, vars))
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
