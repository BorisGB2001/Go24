package tasks

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// «Тематика» and «Ключевые слова» of a portal group: the portal administrator keeps the list of subjects, a
// group or project picks one and has its keywords; «Все группы» is filtered by the subject and searched by them.

const (
	maxKeywords   = 20
	maxKeywordLen = 50
)

// subjectSQL selects the project's subject id, its name and the keywords (of projects p).
const subjectSQL = `coalesce(p.subject_id::text, ''), coalesce((SELECT g.name FROM group_subjects g WHERE g.id = p.subject_id), ''), p.keywords`

// Subject is a «Тематика» of groups; Groups counts the groups and projects of it.
type Subject struct {
	ID     string
	Name   string
	Sort   int
	Groups int
}

// GroupFilter narrows «Все группы»: q by the name, the key or a keyword, the subject, one keyword exactly.
type GroupFilter struct {
	Q, SubjectID, Keyword string
}

// Subjects is the list of subjects in their order.
func (s *Store) Subjects(ctx context.Context) ([]Subject, error) {
	rows, err := s.db.Query(ctx, `SELECT g.id, g.name, g.sort,
		(SELECT count(*) FROM projects p WHERE p.subject_id = g.id AND p.kind <> 'collab' AND NOT p.archived)::int
		FROM group_subjects g ORDER BY g.sort, g.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Subject])
}

// SaveSubject adds a subject (id "") or renames one and moves it in the list; only the administrator.
func (s *Store) SaveSubject(ctx context.Context, id, name string, sort int) (string, error) {
	if !IsAdmin(ctx) {
		return "", ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return "", invalid("err.subject_name")
	}
	var err error
	if id == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO group_subjects (name, sort) VALUES ($1, $2) RETURNING id`, name, sort).Scan(&id)
	} else {
		var tag pgconn.CommandTag
		if tag, err = s.db.Exec(ctx, `UPDATE group_subjects SET name = $2, sort = $3 WHERE id = $1`, id, name, sort); err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", invalid("err.subject_exists")
	}
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return "", ErrNotFound
	}
	return id, err
}

// DeleteSubject removes a subject; its groups are left without one.
func (s *Store) DeleteSubject(ctx context.Context, id string) error {
	if !IsAdmin(ctx) {
		return ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM group_subjects WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// ParseKeywords splits «Ключевые слова» typed with commas: trimmed, without repeats (in any case), at most 20.
func ParseKeywords(s string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		w = strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(w), "#")), " ")
		if w == "" || seen[strings.ToLower(w)] {
			continue
		}
		if r := []rune(w); len(r) > maxKeywordLen {
			w = string(r[:maxKeywordLen])
		}
		seen[strings.ToLower(w)] = true
		out = append(out, w)
		if len(out) == maxKeywords {
			break
		}
	}
	return out
}

// SetGroupSubject sets the group's subject ("" — none) and its keywords; those who change the project's card.
func (s *Store) SetGroupSubject(ctx context.Context, p Project, subjectID string, keywords []string) error {
	if !p.CanEdit() || p.Collab() {
		return ErrForbidden
	}
	if keywords == nil {
		keywords = []string{}
	}
	var subject *string
	if subjectID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM group_subjects WHERE id::text = $1)`, subjectID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return invalid("err.subject_unknown")
		}
		subject = &subjectID
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET subject_id = $2::uuid, keywords = $3, updated_at = now() WHERE id = $1`,
		p.ID, subject, keywords)
	return err
}
