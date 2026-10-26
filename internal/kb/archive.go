package kb

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Экспорт» and «Импорт» of a base: a ZIP of Markdown files. Each page is a .md file with its title in the front
// matter; the pages under it are in a folder of the same name, the order kept by a number in front of the names. The
// pictures and files of the base are in _files/. Links between the pages and to the files are relative, so the archive
// reads in any Markdown editor (Obsidian, VS Code) and comes back as the same tree.

// OpenBytes reads the bytes of a stored file.
type OpenBytes func(ctx context.Context, storageID *string, key string) (io.ReadCloser, error)

// PutBytes stores bytes under a key of the built-in storage.
type PutBytes func(ctx context.Context, key string, r io.Reader, size int64, contentType string) error

const (
	importMaxEntries = 2000
	importMaxBytes   = 300 << 20 // unpacked: no ZIP bomb
)

var (
	reFileLink = regexp.MustCompile(`/files/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(\?inline=1)?`)
	reMDLink   = regexp.MustCompile(`\]\((<[^>]*>|[^)\s]+)`)
	reOrder    = regexp.MustCompile(`^\d+[ ._-]+`)
)

// safeName makes a title a file name: no slashes or characters that Windows or macOS refuse.
func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.Trim(s, ". ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100])
	}
	if s == "" {
		s = "_"
	}
	return s
}

// escapePath makes a relative path a link of Markdown: each part escaped, so spaces do not break it.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Export writes the base the user sees as a ZIP.
func (s *Store) Export(ctx context.Context, userID, spaceID string, w io.Writer, open OpenBytes) error {
	if _, err := s.Space(ctx, userID, spaceID); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT id, coalesce(parent_id::text, ''), title, body FROM kb_pages WHERE space_id = $1 ORDER BY sort, title`, spaceID)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Page, error) {
		p := &Page{SpaceID: spaceID}
		return p, r.Scan(&p.ID, &p.ParentID, &p.Title, &p.Body)
	})
	if err != nil {
		return err
	}
	byID := map[string]*Page{}
	for _, p := range list {
		byID[p.ID] = p
	}
	var roots []*Page
	for _, p := range list {
		if parent := byID[p.ParentID]; parent != nil {
			parent.Children = append(parent.Children, p)
		} else {
			roots = append(roots, p)
		}
	}
	// The path of each page, without .md: «01 Title», its subpages in «01 Title/».
	paths := map[string]string{}
	var name func(list []*Page, dir string)
	name = func(list []*Page, dir string) {
		width := max(2, len(strconv.Itoa(len(list))))
		for i, p := range list {
			paths[p.ID] = path.Join(dir, fmt.Sprintf("%0*d %s", width, i+1, safeName(p.Title)))
			name(p.Children, paths[p.ID])
		}
	}
	name(roots, "")

	var ids []string
	for _, p := range list {
		for _, m := range reFileLink.FindAllStringSubmatch(p.Body, -1) {
			ids = append(ids, m[1])
		}
	}
	rows, err = s.db.Query(ctx, `SELECT id::text, object_key, name, content_type, size, storage_id FROM files
		WHERE id::text = ANY($1) AND kb_space_id = $2`, ids, spaceID)
	if err != nil {
		return err
	}
	files, err := pgx.CollectRows(rows, pgx.RowToStructByPos[fileRow])
	if err != nil {
		return err
	}
	filePaths := map[string]string{}
	for _, f := range files {
		filePaths[f.ID] = "_files/" + f.ID[:8] + "-" + safeName(f.Name)
	}

	reSpace := regexp.MustCompile(`/kb/` + regexp.QuoteMeta(spaceID) + `/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)
	z := zip.NewWriter(w)
	for _, p := range list {
		up := strings.Repeat("../", strings.Count(paths[p.ID], "/"))
		body := reFileLink.ReplaceAllStringFunc(p.Body, func(m string) string {
			if fp, ok := filePaths[reFileLink.FindStringSubmatch(m)[1]]; ok {
				return up + escapePath(fp)
			}
			return m
		})
		body = reSpace.ReplaceAllStringFunc(body, func(m string) string {
			if pp, ok := paths[reSpace.FindStringSubmatch(m)[1]]; ok {
				return up + escapePath(pp+".md")
			}
			return m
		})
		f, err := z.CreateHeader(&zip.FileHeader{Name: paths[p.ID] + ".md", Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		if _, err := io.WriteString(f, "---\ntitle: "+strconv.Quote(p.Title)+"\n---\n\n"+body); err != nil {
			return err
		}
	}
	for _, fr := range files {
		r, err := open(ctx, fr.StorageID, fr.Key)
		if err != nil {
			return err
		}
		f, err := z.CreateHeader(&zip.FileHeader{Name: filePaths[fr.ID], Method: zip.Store, Modified: time.Now()})
		if err == nil {
			_, err = io.Copy(f, r)
		}
		r.Close()
		if err != nil {
			return err
		}
	}
	return z.Close()
}

// ImportFile is one file of what is imported: a path in the archive and its bytes.
type ImportFile struct {
	Name string
	Data []byte
}

// ReadArchive unpacks a ZIP for Import, within the limits of the number of files and of their size.
func ReadArchive(r io.ReaderAt, size int64) ([]ImportFile, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return nil, i18n.E("err.kb_import_zip")
	}
	var out []ImportFile
	var total int64
	for _, f := range z.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if f.FileInfo().IsDir() || strings.HasPrefix(name, "__MACOSX/") || strings.HasPrefix(path.Base(name), ".") {
			continue
		}
		if len(out) >= importMaxEntries {
			return nil, i18n.E("err.kb_import_big")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, i18n.E("err.kb_import_zip")
		}
		data, err := io.ReadAll(io.LimitReader(rc, importMaxBytes-total+1))
		rc.Close()
		if err != nil {
			return nil, i18n.E("err.kb_import_zip")
		}
		if total += int64(len(data)); total > importMaxBytes {
			return nil, i18n.E("err.kb_import_big")
		}
		out = append(out, ImportFile{Name: name, Data: data})
	}
	return out, nil
}

// natLess compares names as people do: «2 …» before «10 …».
func natLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da != "" && db != "" {
			na, _ := strconv.ParseUint(strings.TrimLeft(da, "0"), 10, 64)
			nb, _ := strconv.ParseUint(strings.TrimLeft(db, "0"), 10, 64)
			if na != nb {
				return na < nb
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		ra, rb := []rune(a)[0], []rune(b)[0]
		if la, lb := unicode.ToLower(ra), unicode.ToLower(rb); la != lb {
			return la < lb
		}
		a, b = a[len(string(ra)):], b[len(string(rb)):]
	}
	return len(a) < len(b)
}

func digits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

// importPage is a page being imported: a .md file, a folder, or both («a.md» next to «a/»).
type importPage struct {
	key, title, body string
	children         []*importPage
	id               string
}

// parseMD takes the title from the front matter or from a first «# heading»; fallback is the name of the file.
func parseMD(data []byte, fallback string) (title, body string) {
	text := strings.TrimPrefix(strings.ReplaceAll(string(data), "\r\n", "\n"), "\ufeff")
	if rest, ok := strings.CutPrefix(text, "---\n"); ok {
		if fm, after, ok := strings.Cut(rest, "\n---"); ok {
			for _, line := range strings.Split(fm, "\n") {
				if v, ok := strings.CutPrefix(line, "title:"); ok {
					v = strings.TrimSpace(v)
					if u, err := strconv.Unquote(v); err == nil {
						v = u
					} else if len(v) > 1 && v[0] == '\'' && v[len(v)-1] == '\'' {
						v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
					}
					title = v
				}
			}
			after = strings.TrimPrefix(after, "\n")
			text = strings.TrimLeft(after, "\n")
		}
	}
	if title == "" {
		if first, rest, _ := strings.Cut(text, "\n"); strings.HasPrefix(first, "# ") {
			title, text = strings.TrimSpace(first[2:]), strings.TrimLeft(rest, "\n")
		}
	}
	if title == "" {
		title = fallback
	}
	return title, text
}

// Import makes pages of the base from the files (an unpacked archive or separate .md files) under parentID ("" — at
// the top). The other files the pages link to become the base's files. It gives the number of pages made.
func (s *Store) Import(ctx context.Context, userID, spaceID, parentID string, files []ImportFile, put PutBytes) (int, error) {
	sp, err := s.Space(ctx, userID, spaceID)
	if err != nil {
		return 0, err
	}
	if !sp.CanEdit {
		return 0, ErrForbidden
	}
	var parent *string
	if parentID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM kb_pages WHERE id::text = $1 AND space_id = $2)`, parentID, spaceID).Scan(&ok); err != nil {
			return 0, err
		}
		if !ok {
			return 0, ErrNotFound
		}
		parent = &parentID
	}
	// The tree: a node for every .md file and every folder above one.
	nodes := map[string]*importPage{}
	other := map[string][]byte{} // what is not a page, by its path
	root := &importPage{}
	var node func(key string) *importPage
	node = func(key string) *importPage {
		if n := nodes[key]; n != nil {
			return n
		}
		base := path.Base(key)
		n := &importPage{key: key, title: strings.TrimSpace(reOrder.ReplaceAllString(base, ""))}
		if n.title == "" {
			n.title = base
		}
		nodes[key] = n
		up := root
		if dir := path.Dir(key); dir != "." {
			up = node(dir)
		}
		up.children = append(up.children, n)
		return n
	}
	for _, f := range files {
		name := path.Clean(strings.TrimPrefix(strings.ReplaceAll(f.Name, "\\", "/"), "/"))
		if name == "." || strings.HasPrefix(name, "../") {
			continue
		}
		key, isMD := strings.CutSuffix(name, ".md")
		if !isMD {
			key, isMD = strings.CutSuffix(name, ".markdown")
		}
		if !isMD {
			other[name] = f.Data
			continue
		}
		n := node(key)
		n.title, n.body = parseMD(f.Data, n.title)
		if _, err := checkPage(n.title, n.body); err != nil {
			return 0, i18n.E("err.kb_import_page", name)
		}
	}
	if len(nodes) == 0 {
		return 0, i18n.E("err.kb_import_empty")
	}
	var order func(n *importPage)
	order = func(n *importPage) {
		sort.SliceStable(n.children, func(i, j int) bool { return natLess(path.Base(n.children[i].key), path.Base(n.children[j].key)) })
		for _, c := range n.children {
			order(c)
		}
	}
	order(root)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var next int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(sort), 0) FROM kb_pages WHERE space_id = $1`, spaceID).Scan(&next); err != nil {
		return 0, err
	}
	var insert func(list []*importPage, parent *string) error
	insert = func(list []*importPage, parent *string) error {
		for _, p := range list {
			next++
			title, _ := checkPage(p.title, "")
			if err := tx.QueryRow(ctx, `INSERT INTO kb_pages (space_id, parent_id, title, body, sort, created_by, updated_by)
				VALUES ($1, $2, $3, '', $4, $5, $5) RETURNING id`, spaceID, parent, title, next, userID).Scan(&p.id); err != nil {
				return err
			}
			if err := insert(p.children, &p.id); err != nil {
				return err
			}
		}
		return nil
	}
	if err := insert(root.children, parent); err != nil {
		return 0, err
	}
	// Relative links — to the new pages and to the files, each file stored once.
	stored := map[string]string{}
	link := func(dir, target string) (string, error) {
		if strings.HasPrefix(target, "<") {
			target = strings.TrimSuffix(target[1:], ">")
		}
		if target == "" || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "#") || strings.Contains(target, "://") ||
			strings.HasPrefix(target, "mailto:") {
			return "", nil
		}
		target, frag, _ := strings.Cut(target, "#")
		if u, err := url.PathUnescape(target); err == nil {
			target = u
		}
		p := path.Clean(path.Join(dir, target))
		if strings.HasPrefix(p, "../") {
			return "", nil
		}
		if frag != "" {
			frag = "#" + frag
		}
		key := strings.TrimSuffix(strings.TrimSuffix(p, ".md"), ".markdown")
		if n := nodes[key]; n != nil {
			return "/kb/" + spaceID + "/" + n.id + frag, nil
		}
		data, ok := other[p]
		if !ok {
			return "", nil
		}
		id, ok := stored[p]
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = "application/octet-stream"
		}
		if !ok {
			name := path.Base(p)
			fkey := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
			if err := put(ctx, fkey, bytes.NewReader(data), int64(len(data)), ct); err != nil {
				return "", err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, kb_space_id)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, fkey, name, len(data), ct, userID, spaceID).Scan(&id); err != nil {
				return "", err
			}
			stored[p] = id
		}
		if strings.HasPrefix(ct, "image/") && ct != "image/svg+xml" {
			return "/files/" + id + "?inline=1", nil
		}
		return "/files/" + id, nil
	}
	for _, n := range nodes {
		dir := path.Dir(n.key)
		var lerr error
		body := reMDLink.ReplaceAllStringFunc(n.body, func(m string) string {
			to, err := link(dir, m[2:])
			if err != nil {
				lerr = err
			}
			if to == "" {
				return m
			}
			return "](" + to
		})
		if lerr != nil {
			return 0, lerr
		}
		if body != "" {
			if _, err := tx.Exec(ctx, `UPDATE kb_pages SET body = $2 WHERE id = $1`, n.id, body); err != nil {
				return 0, err
			}
		}
	}
	return len(nodes), tx.Commit(ctx)
}
