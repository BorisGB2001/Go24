package tasks

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Invitations of guests into a collab: a link for anyone (it works until it expires or is revoked),
// or a personal one sent by e-mail or to a phone number (it works once). The people of the collab who may invite
// (the collab's «кто может приглашать», by default the owner and moderators) make and revoke them; a guest invites
// nobody. Whoever opens a link joins the collab as a member; a person new to go24 who signs up by it is a guest.

// GuestInvite is an invitation into a collab.
type GuestInvite struct {
	ID, Token, Kind     string // kind: link | email | phone
	Email, Phone, Name  string
	CreatedBy           string // the name of who made it
	CreatedAt, Expires  time.Time
	Revoked             bool
	Uses                int
	UsedBy              string // the name of the last who joined by it
	ProjectKey, Project string // the collab
}

// Active: the invitation still lets people in.
func (i GuestInvite) Active(now time.Time) bool {
	return !i.Revoked && now.Before(i.Expires) && (i.Kind == "link" || i.Uses == 0)
}

// Guest invitations live from an hour to three months; a personal one — a week by default.
const (
	GuestInviteMinTTL = time.Hour
	GuestInviteMaxTTL = 90 * 24 * time.Hour
	GuestInviteTTL    = 7 * 24 * time.Hour
)

var phoneRe = regexp.MustCompile(`^\+?[0-9]{7,15}$`)

// cleanPhone: «+7 (999) 123-45-67» → «+79991234567».
func cleanPhone(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '(' || r == ')' || r == '.' {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

// canInviteGuests: into a collab, by the people its «кто может приглашать гостей» lets — never by a guest.
func (s *Store) canInviteGuests(ctx context.Context, p Project, actorID string) error {
	if !p.Collab() {
		return ErrNotFound
	}
	guest, err := s.IsGuest(ctx, actorID)
	if err != nil {
		return err
	}
	set, err := collabSettings(ctx, s.db, p.ID)
	if err != nil {
		return err
	}
	if guest || !permits(p, set.GuestInvitePerm) {
		return ErrForbidden
	}
	return nil
}

// CanInviteGuests: the person may invite guests into the collab.
func (s *Store) CanInviteGuests(ctx context.Context, p Project, actorID string) bool {
	return s.canInviteGuests(ctx, p, actorID) == nil
}

// CreateGuestInvite makes an invitation: kind "link" (anyone with it), "email" or "phone" (for one person).
func (s *Store) CreateGuestInvite(ctx context.Context, p Project, actorID, kind, contact, name string, ttl time.Duration) (GuestInvite, error) {
	if err := s.canInviteGuests(ctx, p, actorID); err != nil {
		return GuestInvite{}, err
	}
	var email, phone string
	switch kind {
	case "link":
	case "email":
		a, err := mail.ParseAddress(strings.TrimSpace(contact))
		if err != nil {
			return GuestInvite{}, invalid("err.guest_invite_email")
		}
		email = strings.ToLower(a.Address)
		if name == "" {
			name = a.Name
		}
	case "phone":
		if phone = cleanPhone(contact); !phoneRe.MatchString(phone) {
			return GuestInvite{}, invalid("err.guest_invite_phone")
		}
	default:
		return GuestInvite{}, invalid("err.bad_form")
	}
	if ttl <= 0 {
		ttl = GuestInviteTTL
	}
	ttl = min(max(ttl, GuestInviteMinTTL), GuestInviteMaxTTL)
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return GuestInvite{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO collab_invites (project_id, token, kind, email, phone, name, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
		p.ID, token, kind, email, phone, strings.TrimSpace(name), actorID, time.Now().Add(ttl)).Scan(&id)
	if err != nil {
		return GuestInvite{}, err
	}
	if err := logCollab(ctx, s.db, p.ID, actorID, "invite_created", "", strings.TrimSpace(kind+" "+email+phone)); err != nil {
		return GuestInvite{}, err
	}
	return s.guestInvite(ctx, `i.id::text = $1`, id)
}

const guestInviteSQL = `SELECT i.id::text, i.token, i.kind, i.email, i.phone, i.name, b.name, i.created_at, i.expires_at,
	i.revoked_at IS NOT NULL, i.uses, coalesce(u.name, ''), p.key, p.name
	FROM collab_invites i JOIN projects p ON p.id = i.project_id JOIN users b ON b.id = i.created_by
	LEFT JOIN users u ON u.id = i.used_by`

func scanGuestInvites(rows pgx.Rows, err error) ([]GuestInvite, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (GuestInvite, error) {
		var i GuestInvite
		err := r.Scan(&i.ID, &i.Token, &i.Kind, &i.Email, &i.Phone, &i.Name, &i.CreatedBy, &i.CreatedAt, &i.Expires,
			&i.Revoked, &i.Uses, &i.UsedBy, &i.ProjectKey, &i.Project)
		return i, err
	})
}

func (s *Store) guestInvite(ctx context.Context, where string, args ...any) (GuestInvite, error) {
	list, err := scanGuestInvites(s.db.Query(ctx, guestInviteSQL+` WHERE `+where, args...))
	if err != nil {
		return GuestInvite{}, err
	}
	if len(list) == 0 {
		return GuestInvite{}, ErrNotFound
	}
	return list[0], nil
}

// GuestInvites are the collab's invitations of the last month (the active ones first), for those who may invite.
func (s *Store) GuestInvites(ctx context.Context, p Project, actorID string) ([]GuestInvite, error) {
	if err := s.canInviteGuests(ctx, p, actorID); err != nil {
		return nil, err
	}
	return scanGuestInvites(s.db.Query(ctx, guestInviteSQL+` WHERE i.project_id = $1 AND i.created_at > now() - interval '30 days'
		ORDER BY (i.revoked_at IS NULL AND i.expires_at > now() AND (i.kind = 'link' OR i.uses = 0)) DESC, i.created_at DESC`, p.ID))
}

// RevokeGuestInvite: the link stops working at once (people who joined by it stay in the collab).
func (s *Store) RevokeGuestInvite(ctx context.Context, p Project, actorID, id string) error {
	if err := s.canInviteGuests(ctx, p, actorID); err != nil {
		return err
	}
	var kind, contact string
	err := s.db.QueryRow(ctx, `UPDATE collab_invites SET revoked_at = now() WHERE id::text = $1 AND project_id = $2 AND revoked_at IS NULL
		RETURNING kind, email || phone`, id, p.ID).Scan(&kind, &contact)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return logCollab(ctx, s.db, p.ID, actorID, "invite_revoked", "", strings.TrimSpace(kind+" "+contact))
}

// RenewGuestInvite: «Пригласить повторно» — a personal invitation nobody has used or revoked works ttl more from now
// (an expired one comes back); the caller sends it again.
func (s *Store) RenewGuestInvite(ctx context.Context, p Project, actorID, id string, ttl time.Duration) (GuestInvite, error) {
	if err := s.canInviteGuests(ctx, p, actorID); err != nil {
		return GuestInvite{}, err
	}
	if ttl <= 0 {
		ttl = GuestInviteTTL
	}
	ttl = min(max(ttl, GuestInviteMinTTL), GuestInviteMaxTTL)
	var kind, contact string
	err := s.db.QueryRow(ctx, `UPDATE collab_invites SET expires_at = $3
		WHERE id::text = $1 AND project_id = $2 AND kind <> 'link' AND uses = 0 AND revoked_at IS NULL RETURNING kind, email || phone`,
		id, p.ID, time.Now().Add(ttl)).Scan(&kind, &contact)
	if errors.Is(err, pgx.ErrNoRows) {
		return GuestInvite{}, ErrNotFound
	}
	if err != nil {
		return GuestInvite{}, err
	}
	if err := logCollab(ctx, s.db, p.ID, actorID, "invite_resent", "", kind+" "+contact); err != nil {
		return GuestInvite{}, err
	}
	return s.guestInvite(ctx, `i.id::text = $1`, id)
}

// SplitContacts: the e-mails or phones of several guests at once — one a line, or by commas and semicolons
// («Анна <anna@example.org>, +7 999 123-45-67»); a repeated one once.
func SplitContacts(s string) []string {
	var out []string
	for _, c := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == ';' }) {
		if c = strings.TrimSpace(c); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// GuestInviteByToken is the invitation of a link while it works; a link that expired, was revoked or used (a
// personal one) answers err.guest_invite_gone, an unknown one is not found.
func (s *Store) GuestInviteByToken(ctx context.Context, token string) (GuestInvite, error) {
	if token == "" {
		return GuestInvite{}, ErrNotFound
	}
	i, err := s.guestInvite(ctx, `i.token = $1 AND NOT p.archived`, token)
	if err != nil {
		return i, err
	}
	if !i.Active(time.Now()) {
		return i, invalid("err.guest_invite_gone")
	}
	return i, nil
}

// AcceptGuestInvite: the person joins the collab by the link (as a member; one who is in it already stays as they
// are) and says the collab's key.
func (s *Store) AcceptGuestInvite(ctx context.Context, token, userID string) (string, error) {
	var key string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id, projectID, kind, inviter string
		var expires time.Time
		var revoked bool
		var uses int
		err := tx.QueryRow(ctx, `SELECT i.id, i.project_id, p.key, i.kind, i.expires_at, i.revoked_at IS NOT NULL, i.uses, i.created_by::text
			FROM collab_invites i JOIN projects p ON p.id = i.project_id
			WHERE i.token = $1 AND p.kind = 'collab' AND NOT p.archived FOR UPDATE OF i`, token).
			Scan(&id, &projectID, &key, &kind, &expires, &revoked, &uses, &inviter)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !(GuestInvite{Kind: kind, Expires: expires, Revoked: revoked, Uses: uses}).Active(time.Now()) {
			return invalid("err.guest_invite_gone")
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT active FROM users WHERE id = $1`, userID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member') ON CONFLICT DO NOTHING`,
			projectID, userID)
		if err != nil || tag.RowsAffected() == 0 { // already in: the invitation is not spent
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM project_invites WHERE project_id = $1 AND user_id = $2`, projectID, userID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE collab_invites SET uses = uses + 1, used_by = $2, used_at = now() WHERE id = $1`, id, userID); err != nil {
			return err
		}
		if err := notifyCollab(ctx, tx, inviter, userID, "collab_joined", projectID, "/p/"+key); err != nil {
			return err
		}
		return logCollab(ctx, tx, projectID, userID, "joined", userID, kind)
	})
	return key, err
}
