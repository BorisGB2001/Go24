package bi

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/encoding/charmap"

	"go24/internal/i18n"
)

// ---------- «Свои наборы данных»: a data set of one's own, loaded from a CSV file ----------

// Limits of a file.
const (
	MaxFileSize = 20 << 20
	maxFileRows = 100000
	maxFileCols = 100
	filePrefix  = "file_"
)

// FileColumn is a column of a file's set: Code stays, Header is the file's, Name is shown, Type is a field type.
type FileColumn struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Header string `json:"header"`
	Type   string `json:"type"`
}

// File is a set of one's own: its name, the columns and how many rows the file has had loaded.
type File struct {
	ID          int64
	Name        string
	Description string
	Columns     []FileColumn
	FileName    string
	Rows        int
	CreatedBy   string
	AuthorName  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Code is the code of the file's data set.
func (f File) Code() string { return filePrefix + strconv.FormatInt(f.ID, 10) }

// Editable: the author and the administrators change and delete the set; everyone reads it.
func (f File) Editable(v Viewer) bool { return v.Admin || (f.CreatedBy != "" && f.CreatedBy == v.ID) }

// IsFile: the code is of a set of one's own.
func IsFile(code string) bool { return strings.HasPrefix(code, filePrefix) }

// Types are the types a column may have.
var Types = []string{String, Number, Date, Bool}

// Dataset is the file as a data set; the period is its first date.
func (f File) Dataset() *Dataset {
	id := strconv.FormatInt(f.ID, 10)
	ds := &Dataset{Code: f.Code(), Name: f.Name, id: "r.n::text", from: "bi_file_rows r",
		scope: func(*run) (string, error) { return "r.file_id = " + id, nil }}
	for _, c := range f.Columns {
		v := "(r.data->>'" + c.Code + "')"
		expr := map[string]string{String: v, Number: v + "::float8", Date: v + "::timestamptz", Bool: v + "::bool"}[c.Type]
		ds.Fields = append(ds.Fields, Field{Code: c.Code, Name: c.Name, Type: c.Type, Expr: expr})
		if c.Type == Date && ds.Period == "" {
			ds.Period = c.Code
		}
	}
	return ds
}

// Tr reads the names of a set of one's own and of its fields, the locale those of the others.
func (d *Dataset) Tr(tr func(string) string) func(string) string {
	if d == nil || d.Name == "" {
		return tr
	}
	return func(k string) string {
		if k == "bi.ds."+d.Code {
			return d.Name
		}
		if c, ok := strings.CutPrefix(k, "bi.f."); ok {
			if f, ok := d.Field(c); ok && f.Name != "" {
				return f.Name
			}
		}
		return tr(k)
	}
}

// Dataset is the data set by its code: a section's or a file's (nil — none).
func (s *Store) Dataset(ctx context.Context, code string) (*Dataset, error) {
	if !IsFile(code) {
		return DatasetOf(code), nil
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(code, filePrefix), 10, 64)
	if err != nil {
		return nil, nil
	}
	f, err := s.File(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return f.Dataset(), nil
}

// AllDatasets are the sections' data sets and then the files', by their names.
func (s *Store) AllDatasets(ctx context.Context) ([]*Dataset, error) {
	files, err := s.Files(ctx)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(Datasets)
	for _, f := range files {
		out = append(out, f.Dataset())
	}
	return out, nil
}

const fileCols = `f.id, f.name, f.description, f.columns, f.file_name, f.rows, coalesce(f.created_by::text, ''),
	coalesce(u.name, ''), f.created_at, f.updated_at FROM bi_files f LEFT JOIN users u ON u.id = f.created_by`

func scanFile(row pgx.Row) (File, error) {
	var f File
	err := row.Scan(&f.ID, &f.Name, &f.Description, &f.Columns, &f.FileName, &f.Rows, &f.CreatedBy, &f.AuthorName,
		&f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return f, err
}

// Files are the sets of one's own, by their names.
func (s *Store) Files(ctx context.Context) ([]File, error) {
	rows, err := s.db.Query(ctx, `SELECT `+fileCols+` ORDER BY lower(f.name), f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []File{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) File(ctx context.Context, id int64) (File, error) {
	return scanFile(s.db.QueryRow(ctx, `SELECT `+fileCols+` WHERE f.id = $1`, id))
}

// Table is what a CSV file holds: the header and the rows.
type Table struct {
	Head []string
	Rows [][]string
}

// ParseCSV reads a CSV file; the separator (; , or a tab) and the encoding (UTF-8 or Windows-1251, as Excel saves
// it) are guessed. The first line is the header; empty lines are left out.
func ParseCSV(b []byte) (Table, error) {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(b) {
		if d, err := charmap.Windows1251.NewDecoder().Bytes(b); err == nil {
			b = d
		}
	}
	first, _, _ := bytes.Cut(b, []byte("\n"))
	sep, most := ';', -1
	for _, c := range []rune{';', ',', '\t'} {
		if n := bytes.Count(first, []byte(string(c))); n > most {
			sep, most = c, n
		}
	}
	rd := csv.NewReader(bytes.NewReader(b))
	rd.Comma, rd.FieldsPerRecord, rd.LazyQuotes = sep, -1, true
	var t Table
	for {
		rec, err := rd.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return t, i18n.E("bi.files.err.csv")
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		if t.Head == nil {
			t.Head = rec
			continue
		}
		if len(t.Rows) == maxFileRows {
			return t, i18n.E("bi.files.err.rows", maxFileRows)
		}
		t.Rows = append(t.Rows, rec)
	}
	if t.Head == nil {
		return t, i18n.E("bi.files.err.empty")
	}
	if len(t.Head) > maxFileCols {
		return t, i18n.E("bi.files.err.cols", maxFileCols)
	}
	seen := map[string]int{}
	for i, h := range t.Head { // a header for each column, none twice
		h = strings.TrimSpace(h)
		if h == "" {
			h = strconv.Itoa(i + 1)
		}
		if seen[strings.ToLower(h)]++; seen[strings.ToLower(h)] > 1 {
			h += " (" + strconv.Itoa(seen[strings.ToLower(h)]) + ")"
		}
		t.Head[i] = h
	}
	return t, nil
}

var (
	numRe     = regexp.MustCompile(`^[-+]?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
	commaRe   = regexp.MustCompile(`^[-+]?[0-9]+,[0-9]+$`)                     // 1234,5
	dotsRe    = regexp.MustCompile(`^[-+]?[0-9]{1,3}(\.[0-9]{3})+(,[0-9]+)?$`) // 1.234.567,5
	commasRe  = regexp.MustCompile(`^[-+]?[0-9]{1,3}(,[0-9]{3})+(\.[0-9]+)?$`) // 1,234,567.5
	numSpaces = strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "")
)

// parseNum reads a number as people write it: «1 234,5», «1,234.5», «1.234,5», «-12»; a single comma is the decimal
// one, as in Russian.
func parseNum(v string) (float64, bool) {
	v = numSpaces.Replace(strings.TrimSpace(v))
	switch {
	case commaRe.MatchString(v):
		v = strings.Replace(v, ",", ".", 1)
	case dotsRe.MatchString(v):
		v = strings.ReplaceAll(strings.ReplaceAll(v, ".", ""), ",", ".")
	case commasRe.MatchString(v):
		v = strings.ReplaceAll(v, ",", "")
	}
	if !numRe.MatchString(v) {
		return 0, false
	}
	n, err := strconv.ParseFloat(v, 64)
	return n, err == nil
}

var dateLayouts = []string{"2006-01-02", "2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02T15:04:05",
	"02.01.2006", "02.01.2006 15:04", "02.01.2006 15:04:05", "2.1.2006", "2.1.2006 15:04", "2006/01/02", "02.01.06"}

// parseDate reads a date (with its time) as the database takes it in the time zone of the queries; a moment with its
// zone keeps it.
func parseDate(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Format(time.RFC3339), true
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, v); err == nil {
			return t.Format("2006-01-02 15:04:05"), true
		}
	}
	return "", false
}

var boolWords = map[string]bool{"да": true, "нет": false, "yes": true, "no": false, "true": true, "false": false,
	"y": true, "n": false, "д": true, "н": false}

func parseBool(v string) (bool, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "1" || v == "0" {
		return v == "1", true
	}
	b, ok := boolWords[v]
	return b, ok
}

// value is a cell of a column of the type (nil — empty or not of it).
func value(typ, v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	switch typ {
	case Number:
		if n, ok := parseNum(v); ok {
			return n
		}
	case Date:
		if d, ok := parseDate(v); ok {
			return d
		}
	case Bool:
		if b, ok := parseBool(v); ok {
			return b
		}
	default:
		return strings.TrimSpace(v)
	}
	return nil
}

// detect is the type of a column's cells: a flag, a number, a date if every cell filled is one (a number with a
// leading zero or «+» — a code, a phone — is text), text otherwise.
func detect(cells []string) string {
	for _, typ := range []string{Bool, Number, Date} {
		filled, ok := 0, true
		for _, c := range cells {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			filled++
			if typ == Bool && (c == "1" || c == "0") { // ones and noughts are numbers
				ok = false
			}
			if typ == Number && (strings.HasPrefix(c, "+") || len(c) > 1 && c[0] == '0' && c[1] != '.' && c[1] != ',') {
				ok = false
			}
			if ok = ok && value(typ, c) != nil; !ok {
				break
			}
		}
		if ok && filled > 0 {
			return typ
		}
	}
	return String
}

// columnOf is the column of a header: the one with this header or name (the file loaded again), nil — a new one.
func columnOf(cols []FileColumn, h string) *FileColumn {
	for i := range cols {
		if strings.EqualFold(cols[i].Header, h) || strings.EqualFold(cols[i].Name, h) {
			return &cols[i]
		}
	}
	return nil
}

// fileRow is a row of a file: its cells by the columns' codes.
type fileRow map[string]string

// CreateFile makes a set of one's own of a file's table; the types of the columns are guessed.
func (s *Store) CreateFile(ctx context.Context, by, name, fileName string, t Table) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSuffix(fileName, ".csv")
	}
	if name == "" {
		return 0, i18n.E("bi.files.err.name")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO bi_files (name, file_name, created_by) VALUES ($1, $2, $3) RETURNING id`,
		name, fileName, by).Scan(&id); err != nil {
		return 0, err
	}
	if err := s.load(ctx, tx, id, nil, fileName, t, false); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// LoadFile loads a file into the set again: its rows replace those there (or follow them, add); the file's headers
// find their columns, a new header is a new column.
func (s *Store) LoadFile(ctx context.Context, id int64, fileName string, t Table, add bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cols []FileColumn
	if err := tx.QueryRow(ctx, `SELECT columns FROM bi_files WHERE id = $1 FOR UPDATE`, id).Scan(&cols); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if err := s.load(ctx, tx, id, cols, fileName, t, add); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// load puts a table into the file id of the columns cols.
func (s *Store) load(ctx context.Context, tx pgx.Tx, id int64, cols []FileColumn, fileName string, t Table, add bool) error {
	codes := make([]string, len(t.Head)) // the column of each cell
	for i, h := range t.Head {
		c := columnOf(cols, h)
		if c == nil {
			cells := make([]string, len(t.Rows))
			for n, r := range t.Rows {
				if i < len(r) {
					cells[n] = r[i]
				}
			}
			cols = append(cols, FileColumn{Code: "c" + strconv.Itoa(len(cols)+1), Name: h, Header: h, Type: detect(cells)})
			c = &cols[len(cols)-1]
		}
		codes[i] = c.Code
	}
	var rows []fileRow
	if add {
		var err error
		if rows, err = s.rawRows(ctx, tx, id); err != nil {
			return err
		}
	}
	for _, r := range t.Rows {
		row := fileRow{}
		for i, v := range r {
			if i < len(codes) && strings.TrimSpace(v) != "" {
				row[codes[i]] = v
			}
		}
		rows = append(rows, row)
	}
	if len(rows) > maxFileRows {
		return i18n.E("bi.files.err.rows", maxFileRows)
	}
	if _, err := tx.Exec(ctx, `UPDATE bi_files SET columns = $2, file_name = $3, updated_at = now() WHERE id = $1`,
		id, cols, fileName); err != nil {
		return err
	}
	return s.write(ctx, tx, id, cols, rows)
}

// rawRows are the cells of the file's rows, in their order.
func (s *Store) rawRows(ctx context.Context, tx pgx.Tx, id int64) ([]fileRow, error) {
	rows, err := tx.Query(ctx, `SELECT raw FROM bi_file_rows WHERE file_id = $1 ORDER BY n`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (fileRow, error) {
		var x fileRow
		return x, r.Scan(&x)
	})
}

// write puts the rows into the file anew, their values of the columns' types.
func (s *Store) write(ctx context.Context, tx pgx.Tx, id int64, cols []FileColumn, rows []fileRow) error {
	if _, err := tx.Exec(ctx, `DELETE FROM bi_file_rows WHERE file_id = $1`, id); err != nil {
		return err
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"bi_file_rows"}, []string{"file_id", "n", "raw", "data"},
		pgx.CopyFromSlice(len(rows), func(n int) ([]any, error) {
			data := map[string]any{}
			for _, c := range cols {
				if v := value(c.Type, rows[n][c.Code]); v != nil {
					data[c.Code] = v
				}
			}
			return []any{id, n + 1, map[string]string(rows[n]), data}, nil
		})); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE bi_files SET rows = $2 WHERE id = $1`, id, len(rows))
	return err
}

// SaveFile changes the name, the description and the columns' names and types of a set; the values of a column whose
// type changed are read again from the file's cells.
func (s *Store) SaveFile(ctx context.Context, id int64, name, description string, cols []FileColumn) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return i18n.E("bi.files.err.name")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var was []FileColumn
	if err := tx.QueryRow(ctx, `SELECT columns FROM bi_files WHERE id = $1 FOR UPDATE`, id).Scan(&was); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	retype := false
	for i := range was {
		for _, c := range cols {
			if c.Code != was[i].Code {
				continue
			}
			if n := strings.TrimSpace(c.Name); n != "" {
				was[i].Name = n
			}
			if slices.Contains(Types, c.Type) && c.Type != was[i].Type {
				was[i].Type, retype = c.Type, true
			}
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE bi_files SET name = $2, description = $3, columns = $4, updated_at = now() WHERE id = $1`,
		id, name, strings.TrimSpace(description), was); err != nil {
		return err
	}
	if retype {
		rows, err := s.rawRows(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := s.write(ctx, tx, id, was, rows); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteFile(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM bi_files WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Unread counts the cells of each column the type did not read: a column's type to check.
func (s *Store) Unread(ctx context.Context, f File) (map[string]int, error) {
	out := map[string]int{}
	for _, c := range f.Columns {
		if c.Type == String {
			continue
		}
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM bi_file_rows WHERE file_id = $1 AND raw ? $2 AND NOT data ? $2`,
			f.ID, c.Code).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out[c.Code] = n
		}
	}
	return out, nil
}
