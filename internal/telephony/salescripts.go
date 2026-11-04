package telephony

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Скрипты продаж»: a conversation's scenario of blocks — what the employee says and what the client may
// answer, each answer leading to another block. During a call the softphone's card shows the script of the call's
// direction: the block, buttons of the answers, «Назад» and «Сначала».
//
// A script is written as text: a line «# Title» starts a block, the lines after it are what the employee says, a line
// «- Answer -> Title» is the client's answer leading to the block of that title; «- Answer» without the arrow leads to
// the next block (from the last one — to the end). A block without answers ends the script.

// SalesScript is a sales script.
type SalesScript struct {
	ID, Name string
	Body     string // the blocks in the markup
	Calls    string // all | in | out
	Active   bool
}

// ScriptBlock is a block of a sales script.
type ScriptBlock struct {
	Title, Text string
	Answers     []ScriptAnswer
}

// ScriptAnswer is the client's answer of a block; Next is the index of the block it leads to, -1 — the script's end.
type ScriptAnswer struct {
	Label string
	Next  int
}

// Limits of a sales script.
const (
	salesBlocks  = 100
	salesAnswers = 10
)

// ParseSalesScript takes the markup apart into blocks; the error tells the line at fault.
func ParseSalesScript(body string) ([]ScriptBlock, error) {
	type answer struct {
		label, to string
		line      int
	}
	var blocks []ScriptBlock
	var answers [][]answer
	var text []string
	flush := func() {
		if len(blocks) > 0 {
			blocks[len(blocks)-1].Text = strings.TrimSpace(strings.Join(text, "\n"))
		}
		text = nil
	}
	index := map[string]int{}
	for n, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "#"):
			flush()
			title := strings.TrimSpace(strings.TrimLeft(trim, "#"))
			if title == "" || len([]rune(title)) > 100 {
				return nil, i18n.E("err.salescript_title", n+1)
			}
			key := strings.ToLower(title)
			if _, ok := index[key]; ok {
				return nil, i18n.E("err.salescript_twice", n+1, title)
			}
			if len(blocks) == salesBlocks {
				return nil, i18n.E("err.salescript_blocks", salesBlocks)
			}
			index[key] = len(blocks)
			blocks = append(blocks, ScriptBlock{Title: title})
			answers = append(answers, nil)
		case trim == "" && len(blocks) == 0:
		case len(blocks) == 0:
			return nil, i18n.E("err.salescript_start", n+1)
		case strings.HasPrefix(trim, "- ") || trim == "-":
			label, to, _ := strings.Cut(strings.TrimSpace(trim[1:]), "->")
			if !strings.Contains(trim, "->") {
				label, to, _ = strings.Cut(strings.TrimSpace(trim[1:]), "→")
			}
			label = strings.TrimSpace(label)
			if label == "" || len([]rune(label)) > 200 {
				return nil, i18n.E("err.salescript_answer", n+1)
			}
			a := &answers[len(answers)-1]
			if len(*a) == salesAnswers {
				return nil, i18n.E("err.salescript_answers", n+1, salesAnswers)
			}
			*a = append(*a, answer{label, strings.TrimSpace(to), n + 1})
		default:
			text = append(text, strings.TrimRight(line, " \t"))
		}
	}
	flush()
	if len(blocks) == 0 {
		return nil, i18n.E("err.salescript_empty")
	}
	for i, list := range answers {
		for _, a := range list {
			next := i + 1
			if a.to != "" {
				j, ok := index[strings.ToLower(a.to)]
				if !ok {
					return nil, i18n.E("err.salescript_target", a.line, a.to)
				}
				next = j
			} else if next == len(blocks) {
				next = -1
			}
			blocks[i].Answers = append(blocks[i].Answers, ScriptAnswer{Label: a.label, Next: next})
		}
	}
	return blocks, nil
}

const salesCols = `id::text, name, body, calls, active`

func scanSales(r pgx.CollectableRow) (SalesScript, error) {
	var sc SalesScript
	return sc, r.Scan(&sc.ID, &sc.Name, &sc.Body, &sc.Calls, &sc.Active)
}

// SalesScripts are the sales scripts by name; dir (in, out) keeps the active ones for calls of the direction, "" — all.
func (s *Store) SalesScripts(ctx context.Context, dir string) ([]SalesScript, error) {
	q := `SELECT ` + salesCols + ` FROM telephony_sales_scripts`
	var args []any
	if dir != "" {
		q += ` WHERE active AND (calls = 'all' OR calls = $1)`
		args = append(args, dir)
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY lower(name), created_at, id`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSales)
}

// SalesScriptByID is a sales script.
func (s *Store) SalesScriptByID(ctx context.Context, id string) (SalesScript, error) {
	rows, err := s.db.Query(ctx, `SELECT `+salesCols+` FROM telephony_sales_scripts WHERE id::text = $1`, id)
	if err != nil {
		return SalesScript{}, err
	}
	sc, err := pgx.CollectExactlyOneRow(rows, scanSales)
	if errors.Is(err, pgx.ErrNoRows) {
		return sc, ErrNotFound
	}
	return sc, err
}

// SaveSalesScript adds (ID "") or changes a sales script; the markup must parse.
func (s *Store) SaveSalesScript(ctx context.Context, userID string, sc SalesScript) (string, error) {
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Name == "" || len([]rune(sc.Name)) > 100 || strings.ContainsAny(sc.Name, "\r\n") {
		return "", i18n.E("err.script_name")
	}
	sc.Body = strings.TrimSpace(sc.Body)
	if len([]rune(sc.Body)) > 50000 {
		return "", i18n.E("err.salescript_long")
	}
	if _, err := ParseSalesScript(sc.Body); err != nil {
		return "", err
	}
	if sc.Calls != "in" && sc.Calls != "out" {
		sc.Calls = "all"
	}
	if sc.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO telephony_sales_scripts (name, body, calls, active, created_by)
			VALUES ($1, $2, $3, $4, $5) RETURNING id::text`, sc.Name, sc.Body, sc.Calls, sc.Active, userID).Scan(&sc.ID)
		return sc.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE telephony_sales_scripts SET name = $2, body = $3, calls = $4, active = $5 WHERE id::text = $1`,
		sc.ID, sc.Name, sc.Body, sc.Calls, sc.Active)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return sc.ID, err
}

// DeleteSalesScript removes a sales script.
func (s *Store) DeleteSalesScript(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_sales_scripts WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}
