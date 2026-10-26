package company

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Пригласить сотрудников»: go24 keeps the invitations it made; the person has joined once they signed in with
// the invited e-mail. As, several people go at once, into a department, by e-mail; an invitation not
// used yet is sent again or cancelled.

// MaxInvites is how many people one form invites.
const MaxInvites = 50

// Invite is a sent invitation.
type Invite struct {
	ID, Email, Name, Link, InvitedBy string
	InvitedByID, AuthentikID         string
	DepartmentID, Department         string
	Expires, Created                 time.Time
	Mailed                           bool
	Revoked                          *time.Time
	Joined                           bool // signed in to go24 with this e-mail
}

// State is «joined», «revoked», «expired» or «waiting».
func (i Invite) State(now time.Time) string {
	switch {
	case i.Joined:
		return "joined"
	case i.Revoked != nil:
		return "revoked"
	case i.Expires.Before(now):
		return "expired"
	}
	return "waiting"
}

// Invitee is one person of the form.
type Invitee struct{ Email, Name string }

// CheckInvite cleans and checks what the form gives; a person already in go24 is not invited again.
func (s *Store) CheckInvite(ctx context.Context, email, name string) (string, string, error) {
	email, name = strings.ToLower(strings.TrimSpace(email)), strings.TrimSpace(name)
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
		return "", "", i18n.E("err.invite_email")
	}
	if name == "" || len([]rune(name)) > 200 {
		return "", "", i18n.E("err.invite_name")
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = $1 AND active AND oidc_sub NOT LIKE 'legacy:%')`,
		email).Scan(&exists); err != nil {
		return "", "", err
	}
	if exists {
		return "", "", i18n.E("err.invite_exists")
	}
	return email, name, nil
}

// ParseInvitees reads the people of the form, one a line: «ivan@firm.ru Иван Петров», «Иван Петров <ivan@firm.ru>»
// or with a comma or a semicolon between. Each is checked as CheckInvite does; the error names the line.
func (s *Store) ParseInvitees(ctx context.Context, text string) ([]Invitee, error) {
	var out []Invitee
	seen := map[string]bool{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		email, name := splitInvitee(line)
		email, name, err := s.CheckInvite(ctx, email, name)
		if err != nil {
			var e *i18n.Error
			if errors.As(err, &e) {
				return nil, i18n.E("err.invite_line", n+1, i18n.Ref(e.Key))
			}
			return nil, err
		}
		if seen[email] {
			continue
		}
		seen[email] = true
		out = append(out, Invitee{email, name})
	}
	if len(out) == 0 {
		return nil, i18n.E("err.invite_email")
	}
	if len(out) > MaxInvites {
		return nil, i18n.E("err.invite_many", MaxInvites)
	}
	return out, nil
}

// splitInvitee: the word with «@» is the e-mail, the rest — the name.
func splitInvitee(line string) (email, name string) {
	if a, err := mail.ParseAddress(line); err == nil && a.Name != "" {
		return a.Address, a.Name
	}
	var rest []string
	for _, w := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == ',' || r == ';' }) {
		if email == "" && strings.Contains(w, "@") {
			email = strings.Trim(w, "<>")
			continue
		}
		rest = append(rest, w)
	}
	return email, strings.Join(rest, " ")
}

// CheckDepartment: the department chosen exists ("" — none).
func (s *Store) CheckDepartment(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM departments WHERE id::text = $1)`, id).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return i18n.E("err.invite_department")
	}
	return nil
}

// SaveInvite keeps an invitation Authentik has made.
func (s *Store) SaveInvite(ctx context.Context, by, email, name, departmentID, link, authentikID string, expires time.Time) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO employee_invites (email, name, link, expires_at, invited_by, department_id, authentik_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
		email, name, link, expires, by, nullable(departmentID), authentikID).Scan(&id)
	return id, err
}

// SetInviteMailed marks the invitation sent by e-mail.
func (s *Store) SetInviteMailed(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE employee_invites SET mailed = true WHERE id::text = $1`, id)
	return err
}

const inviteCols = `SELECT i.id::text, i.email, i.name, i.link, b.name, b.id::text, i.authentik_id, coalesce(i.department_id::text, ''),
		coalesce(d.name, ''), i.expires_at, i.created_at, i.mailed, i.revoked_at,
		EXISTS (SELECT 1 FROM users u WHERE lower(u.email) = lower(i.email) AND u.oidc_sub NOT LIKE 'legacy:%')
	FROM employee_invites i JOIN users b ON b.id = i.invited_by LEFT JOIN departments d ON d.id = i.department_id`

func scanInvite(r pgx.Row) (Invite, error) {
	var i Invite
	err := r.Scan(&i.ID, &i.Email, &i.Name, &i.Link, &i.InvitedBy, &i.InvitedByID, &i.AuthentikID, &i.DepartmentID, &i.Department,
		&i.Expires, &i.Created, &i.Mailed, &i.Revoked, &i.Joined)
	return i, err
}

// Invites are the latest invitations.
func (s *Store) Invites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.Query(ctx, inviteCols+` ORDER BY i.created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invite, error) { return scanInvite(r) })
}

// InviteByID is one invitation.
func (s *Store) InviteByID(ctx context.Context, id string) (Invite, error) {
	i, err := scanInvite(s.db.QueryRow(ctx, inviteCols+` WHERE i.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// Pending: the invitation can still be sent again or cancelled (the person has not joined, not cancelled).
func (s *Store) Pending(ctx context.Context, id string) (Invite, error) {
	i, err := s.InviteByID(ctx, id)
	if err != nil {
		return i, err
	}
	if i.Joined || i.Revoked != nil {
		return i, i18n.E("err.invite_closed")
	}
	return i, nil
}

// RenewInvite puts the new Authentik link of «Пригласить повторно» in place of the old one.
func (s *Store) RenewInvite(ctx context.Context, id, link, authentikID string, expires time.Time) error {
	_, err := s.db.Exec(ctx, `UPDATE employee_invites SET link = $2, authentik_id = $3, expires_at = $4, mailed = false
		WHERE id::text = $1`, id, link, authentikID, expires)
	return err
}

// RevokeInvite marks the invitation cancelled.
func (s *Store) RevokeInvite(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE employee_invites SET revoked_at = now() WHERE id::text = $1 AND revoked_at IS NULL`, id)
	return err
}

// Joined: a person new to go24 signed in. The latest invitation of their e-mail (not cancelled) puts them into its
// department and goes to «Кадровые изменения» as taken on by the one who invited.
func (s *Store) Joined(ctx context.Context, userID, email string) error {
	if email == "" {
		return nil
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var by string
		var dept *string
		err := tx.QueryRow(ctx, `SELECT invited_by::text, department_id::text FROM employee_invites
			WHERE lower(email) = lower($1) AND revoked_at IS NULL ORDER BY created_at DESC LIMIT 1`, email).Scan(&by, &dept)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if dept != nil {
			if _, err := tx.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
				userID, *dept); err != nil {
				return err
			}
		}
		return logStaff(ctx, tx, userID, "hired", by, nil)
	})
}
