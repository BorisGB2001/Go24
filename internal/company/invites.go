package company

import (
	"context"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Пригласить сотрудника»: go24 keeps the invitations it made; the person has joined once they signed in with
// the invited e-mail.

// Invite is a sent invitation.
type Invite struct {
	ID, Email, Name, Link, InvitedBy string
	Expires, Created                 time.Time
	Joined                           bool // signed in to go24 with this e-mail
}

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

// SaveInvite keeps an invitation Authentik has made.
func (s *Store) SaveInvite(ctx context.Context, by, email, name, link string, expires time.Time) error {
	_, err := s.db.Exec(ctx, `INSERT INTO employee_invites (email, name, link, expires_at, invited_by) VALUES ($1, $2, $3, $4, $5)`,
		email, name, link, expires, by)
	return err
}

// Invites are the latest invitations.
func (s *Store) Invites(ctx context.Context) ([]Invite, error) {
	rows, err := s.db.Query(ctx, `SELECT i.id, i.email, i.name, i.link, b.name, i.expires_at, i.created_at,
			EXISTS (SELECT 1 FROM users u WHERE lower(u.email) = lower(i.email) AND u.oidc_sub NOT LIKE 'legacy:%')
		FROM employee_invites i JOIN users b ON b.id = i.invited_by ORDER BY i.created_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invite, error) {
		var i Invite
		err := r.Scan(&i.ID, &i.Email, &i.Name, &i.Link, &i.InvitedBy, &i.Expires, &i.Created, &i.Joined)
		return i, err
	})
}
