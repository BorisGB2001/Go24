package kb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Шаблоны» of a base: a new base is made from a ready set of pages — one of go24's own (builtins, their
// texts in the locales) or a company base saved as a template (kb_templates). A saved template keeps copies of the
// pictures and files of its pages, and a base made from it gets copies of its own, so either lives on when the other
// is deleted. Links between the pages of the base are kept as links between the pages of the copy.

// Template is a template a base is made from.
type Template struct {
	ID, Name, Description string // the ID of a builtin is "b:…"
	Builtin               bool
	Pages                 int
	Author                string
	Created               time.Time
	CanDelete             bool
}

// TemplatePage is a page of a template with the pages under it; Ref is the ID of the page it was saved from.
type TemplatePage struct {
	Ref      string         `json:"ref,omitempty"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Children []TemplatePage `json:"children,omitempty"`
}

// CopyBytes stores the bytes of a stored file once more under another key of the same storage.
type CopyBytes func(ctx context.Context, storageID *string, from, to string, size int64, contentType string) error

// builtin is a template of go24: the pages are locale keys, «KEY.title» and «KEY.body» being their texts.
type builtin struct {
	id    string
	pages []builtinPage
}

type builtinPage struct {
	key      string
	children []builtinPage
}

var builtins = []builtin{
	{"onboarding", []builtinPage{{key: "kb.tpl.onboarding.welcome", children: []builtinPage{
		{key: "kb.tpl.onboarding.first_day"}, {key: "kb.tpl.onboarding.first_week"}}},
		{key: "kb.tpl.onboarding.company"}, {key: "kb.tpl.onboarding.tools"}, {key: "kb.tpl.onboarding.contacts"}}},
	{"rules", []builtinPage{{key: "kb.tpl.rules.about", children: []builtinPage{
		{key: "kb.tpl.rules.worktime"}, {key: "kb.tpl.rules.vacation"}, {key: "kb.tpl.rules.trips"}}},
		{key: "kb.tpl.rules.security"}, {key: "kb.tpl.rules.docs"}}},
	{"faq", []builtinPage{{key: "kb.tpl.faq.about", children: []builtinPage{
		{key: "kb.tpl.faq.access"}, {key: "kb.tpl.faq.money"}, {key: "kb.tpl.faq.it"}}}}},
	{"product", []builtinPage{{key: "kb.tpl.product.overview", children: []builtinPage{
		{key: "kb.tpl.product.start"}, {key: "kb.tpl.product.guide"}, {key: "kb.tpl.product.faq"}}},
		{key: "kb.tpl.product.releases"}}},
}

func (b builtin) tree(tr func(string) string) []TemplatePage {
	var walk func([]builtinPage) []TemplatePage
	walk = func(list []builtinPage) []TemplatePage {
		var out []TemplatePage
		for _, p := range list {
			out = append(out, TemplatePage{Title: tr(p.key + ".title"), Body: tr(p.key + ".body"), Children: walk(p.children)})
		}
		return out
	}
	return walk(b.pages)
}

func countPages(list []TemplatePage) int {
	n := len(list)
	for _, p := range list {
		n += countPages(p.Children)
	}
	return n
}

// Templates are go24's templates (tr gives their texts) and the company's, newest first.
func (s *Store) Templates(ctx context.Context, userID string, tr func(string) string) ([]Template, error) {
	var list []Template
	for _, b := range builtins {
		list = append(list, Template{ID: "b:" + b.id, Name: tr("kb.tpl." + b.id), Description: tr("kb.tpl." + b.id + ".desc"),
			Builtin: true, Pages: countPages(b.tree(tr))})
	}
	rows, err := s.db.Query(ctx, `SELECT t.id, t.name, t.description, t.pages, coalesce(u.name, ''), t.created_at,
			$2 OR t.created_by IS NOT DISTINCT FROM $1::uuid
		FROM kb_templates t LEFT JOIN users u ON u.id = t.created_by WHERE t.kind = 'space' ORDER BY t.created_at DESC`, userID, tasks.IsAdmin(ctx))
	if err != nil {
		return nil, err
	}
	own, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Template, error) {
		var t Template
		var pages []TemplatePage
		err := r.Scan(&t.ID, &t.Name, &t.Description, &pages, &t.Author, &t.Created, &t.CanDelete)
		t.Pages = countPages(pages)
		return t, err
	})
	return append(list, own...), err
}

// reFile finds the files of a page's text: /files/{id}.
var reFile = regexp.MustCompile(`/files/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

type fileRow struct {
	ID, Key, Name, ContentType string
	Size                       int64
	StorageID                  *string
}

// copyFiles copies the files of owner (a column of files and its value) the texts link to; set is the column and
// value of the copies. It gives the texts with the links to the copies.
func copyFiles(ctx context.Context, tx pgx.Tx, copyBytes CopyBytes, userID string, texts []string, owner, ownerID, set, setID string) ([]string, error) {
	var ids []string
	for _, t := range texts {
		for _, m := range reFile.FindAllStringSubmatch(t, -1) {
			ids = append(ids, m[1])
		}
	}
	if len(ids) == 0 {
		return texts, nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, object_key, name, content_type, size, storage_id FROM files
		WHERE id::text = ANY($1) AND `+owner+` = $2`, ids, ownerID)
	if err != nil {
		return nil, err
	}
	files, err := pgx.CollectRows(rows, pgx.RowToStructByPos[fileRow])
	if err != nil {
		return nil, err
	}
	var pairs []string
	for _, f := range files {
		key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), f.Name)
		if err := copyBytes(ctx, f.StorageID, f.Key, key, f.Size, f.ContentType); err != nil {
			return nil, err
		}
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, storage_id, `+set+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, key, f.Name, f.Size, f.ContentType, userID, f.StorageID, setID).Scan(&id); err != nil {
			return nil, err
		}
		pairs = append(pairs, "/files/"+f.ID, "/files/"+id)
	}
	if len(pairs) == 0 {
		return texts, nil
	}
	r := strings.NewReplacer(pairs...)
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = r.Replace(t)
	}
	return out, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// flatten lists the texts of the pages in their order, and fill puts them back.
func flatten(list []TemplatePage, out *[]string) {
	for _, p := range list {
		*out = append(*out, p.Body)
		flatten(p.Children, out)
	}
}

func fill(list []TemplatePage, texts *[]string) {
	for i := range list {
		list[i].Body, *texts = (*texts)[0], (*texts)[1:]
		fill(list[i].Children, texts)
	}
}

// SaveTemplate saves the base as a template of the company («Сохранить как шаблон»): who manages the base.
func (s *Store) SaveTemplate(ctx context.Context, userID, spaceID, name, description string, copyBytes CopyBytes) (string, error) {
	sp, err := s.managed(ctx, userID, spaceID)
	if err != nil {
		return "", err
	}
	if name = strings.TrimSpace(name); name == "" {
		name = sp.Name
	}
	if len([]rune(name)) > 200 {
		return "", i18n.E("err.kb_name")
	}
	if description = strings.TrimSpace(description); description == "" {
		description = sp.Description
	}
	rows, err := s.db.Query(ctx, `SELECT id, coalesce(parent_id::text, ''), title, body FROM kb_pages WHERE space_id = $1 ORDER BY sort, title`, sp.ID)
	if err != nil {
		return "", err
	}
	type row struct{ ID, ParentID, Title, Body string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[row])
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", i18n.E("err.kb_template_empty")
	}
	// The tree, links to the base's own pages made relative («/kb/~/{ref}») to be pointed at the copy's pages.
	local := strings.NewReplacer("/kb/"+sp.ID+"/", "/kb/~/", "/kb/"+sp.ID+")", "/kb/~)")
	nodes := map[string]*TemplatePage{}
	for _, p := range list {
		nodes[p.ID] = &TemplatePage{Ref: p.ID, Title: p.Title, Body: local.Replace(p.Body)}
	}
	var build func(parent string) []TemplatePage
	build = func(parent string) []TemplatePage {
		var out []TemplatePage
		for _, p := range list {
			if p.ParentID == parent || (parent == "" && nodes[p.ParentID] == nil && p.ParentID != "") {
				n := *nodes[p.ID]
				n.Children = build(p.ID)
				out = append(out, n)
			}
		}
		return out
	}
	tree := build("")
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO kb_templates (name, description, pages, created_by) VALUES ($1, $2, '[]', $3) RETURNING id`,
		name, description, userID).Scan(&id); err != nil {
		return "", err
	}
	var texts []string
	flatten(tree, &texts)
	if texts, err = copyFiles(ctx, tx, copyBytes, userID, texts, "kb_space_id", sp.ID, "kb_template_id", id); err != nil {
		return "", err
	}
	fill(tree, &texts)
	data, err := json.Marshal(tree)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE kb_templates SET pages = $2 WHERE id = $1`, id, data); err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

// DeleteTemplate deletes a template of the company (of a base or a page) with its files: who saved it, or an administrator.
func (s *Store) DeleteTemplate(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM kb_templates WHERE id::text = $1 AND ($3 OR created_by = $2)`, id, userID, tasks.IsAdmin(ctx))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateFromTemplate makes a base (as CreateSpace does) with the pages of the template; tr gives the texts of go24's
// templates.
func (s *Store) CreateFromTemplate(ctx context.Context, userID, templateID, name, description, projectID string,
	tr func(string) string, copyBytes CopyBytes) (_ string, err error) {
	var tree []TemplatePage
	own := !strings.HasPrefix(templateID, "b:")
	if own {
		err := s.db.QueryRow(ctx, `SELECT pages FROM kb_templates WHERE id::text = $1 AND kind = 'space'`, templateID).Scan(&tree)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		if err != nil {
			return "", err
		}
	} else {
		i := len(builtins)
		for j, b := range builtins {
			if "b:"+b.id == templateID {
				i = j
			}
		}
		if i == len(builtins) {
			return "", ErrNotFound
		}
		tree = builtins[i].tree(tr)
	}
	id, err := s.CreateSpace(ctx, userID, name, description, projectID)
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil { // no half-made base
			s.db.Exec(context.WithoutCancel(ctx), `DELETE FROM kb_spaces_all WHERE id = $1`, id)
		}
	}()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if own {
		var texts []string
		flatten(tree, &texts)
		if texts, err = copyFiles(ctx, tx, copyBytes, userID, texts, "kb_template_id", templateID, "kb_space_id", id); err != nil {
			return "", err
		}
		fill(tree, &texts)
	}
	refs, sort := map[string]string{}, 0
	var made []string // the new pages, in the order of the texts
	var insert func(list []TemplatePage, parent *string) error
	insert = func(list []TemplatePage, parent *string) error {
		for _, p := range list {
			sort++
			var pid string
			if err := tx.QueryRow(ctx, `INSERT INTO kb_pages (space_id, parent_id, title, body, sort, created_by, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id`, id, parent, p.Title, p.Body, sort, userID).Scan(&pid); err != nil {
				return err
			}
			made = append(made, pid)
			if p.Ref != "" {
				refs[p.Ref] = pid
			}
			if err := insert(p.Children, &pid); err != nil {
				return err
			}
		}
		return nil
	}
	if err := insert(tree, nil); err != nil {
		return "", err
	}
	// Links between the pages of the template — to the pages of the new base.
	pairs := []string{}
	for ref, pid := range refs {
		pairs = append(pairs, "/kb/~/"+ref, "/kb/"+id+"/"+pid)
	}
	pairs = append(pairs, "/kb/~/", "/kb/"+id+"/", "/kb/~)", "/kb/"+id+")")
	r := strings.NewReplacer(pairs...)
	var texts []string
	flatten(tree, &texts)
	for i, pid := range made {
		if t := r.Replace(texts[i]); t != texts[i] {
			if _, err := tx.Exec(ctx, `UPDATE kb_pages SET body = $2 WHERE id = $1`, pid, t); err != nil {
				return "", err
			}
		}
	}
	return id, tx.Commit(ctx)
}
