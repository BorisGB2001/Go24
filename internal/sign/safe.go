package sign

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- «Сейф» of «Подпись для сотрудников»: the HR documents signed by all, in one archive ----------

// SafeMax: no more documents than this in the list and in an archive.
const SafeMax = 500

// SafeFilter picks the documents of «Сейф». All — everyone's (an HR officer, an administrator), otherwise only those
// UserID is the employee of; signed at From or later and before To (zero — any).
type SafeFilter struct {
	UserID       string
	All          bool
	Employee     string // the employee a document is for
	DepartmentID string // the employee is of the department or of its sub-departments
	From, To     time.Time
	Q            string // in the title
}

// Safe are the HR documents (a sending's copies and the employees' own) signed by all, the newest signed first.
func (s *Store) Safe(ctx context.Context, f SafeFilter) ([]Doc, error) {
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	emp := `p.doc_id = d.id AND p.side = 'employee'`
	where := []string{`d.status = 'done'`}
	if !f.All {
		emp += ` AND p.user_id::text = ` + arg(f.UserID)
	}
	if f.Employee != "" {
		emp += ` AND p.user_id::text = ` + arg(f.Employee)
	}
	if f.DepartmentID != "" {
		emp += ` AND p.user_id IN (WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = ` + arg(f.DepartmentID) + `
			UNION SELECT c.id FROM departments c JOIN sub ON c.parent_id = sub.id)
			SELECT ud.user_id FROM user_departments ud JOIN sub ON sub.id = ud.department_id
			UNION SELECT dp.head_id FROM departments dp JOIN sub ON sub.id = dp.id WHERE dp.head_id IS NOT NULL)`
	}
	where = append(where, `EXISTS (SELECT 1 FROM sign_parties p WHERE `+emp+`)`)
	if !f.From.IsZero() {
		where = append(where, `d.done_at >= `+arg(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, `d.done_at < `+arg(f.To))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		where = append(where, `d.title ILIKE '%' || `+arg(likeEscape(q))+` || '%'`)
	}
	rows, _ := s.db.Query(ctx, `SELECT `+docCols+` WHERE `+strings.Join(where, " AND ")+`
		ORDER BY d.done_at DESC LIMIT `+strconv.Itoa(SafeMax), args...)
	docs, err := pgx.CollectRows(rows, scanDoc)
	if err != nil {
		return nil, err
	}
	return docs, s.parties(ctx, docs)
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
