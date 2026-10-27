package openlines

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Онлайн-запись» in the dialogue operator panel: the operator sends an active booking link, the
// client books in the widget (or by the link in Telegram) with the dialogue's name, phone and e-mail filled in; the
// booking gets the dialogue's contact and company, the dialogue gets «Клиент записался» and the contact of the
// booking (if it has none), a cancellation by the client is a service line of it.

// SendBooking sends the booking link to the dialogue's client as the operator's answer; link is the page's address for
// the client by the link's token and the key tying the booking to this dialogue.
func (s *Store) SendBooking(ctx context.Context, userID, id, linkID string, link func(token, secret string) string) error {
	var name, token string
	var active bool
	err := s.db.QueryRow(ctx, `SELECT name, token, active FROM booking_links WHERE id::text = $1`, linkID).Scan(&name, &token, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !active {
		return i18n.E("err.ol_booking_off")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	secret := hex.EncodeToString(b)
	body := s.tr("ol.booking_body", name) + "\n" + link(token, secret)
	return s.answer(ctx, userID, id, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, author_id, body, kind, booking_link_id, form_secret) VALUES ($1::uuid, $2, $3, 'booking', $4, $5)`,
			id, userID, body, linkID, secret)
		return err
	})
}

// BookingClient is the dialogue of a sent booking link's key: its client and cards ("" — none).
type BookingClient struct {
	Session, Name, Phone, Email string
	ContactID, CompanyID        string
}

// BookingFor is the dialogue of the booking link's key (ErrNotFound — not a link sent in a dialogue).
func (s *Store) BookingFor(ctx context.Context, linkID, secret string) (BookingClient, error) {
	var c BookingClient
	if secret == "" {
		return c, ErrNotFound
	}
	err := s.db.QueryRow(ctx, `SELECT s.id::text, s.name, s.phone, s.email, coalesce(s.contact_id::text, ''), coalesce(c.company_id::text, '')
		FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id LEFT JOIN crm_contacts c ON c.id = s.contact_id
		WHERE m.form_secret = $1 AND m.booking_link_id::text = $2 AND m.kind = 'booking' AND NOT s.spam`, secret, linkID).
		Scan(&c.Session, &c.Name, &c.Phone, &c.Email, &c.ContactID, &c.CompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Booked: the client has booked by the link of the key — the booking is the dialogue's, the dialogue gets «Клиент
// записался» with what (the client sees it too), the client's name, phone and e-mail it has none of and the booking's
// contact (if it is not bound yet); the operators are told.
func (s *Store) Booked(ctx context.Context, secret, bookingID, what, name, phone, email, contactID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, linkID, creator string
	if err := tx.QueryRow(ctx, `UPDATE ol_messages m SET form_done = now() FROM ol_sessions s, ol_widgets w
		WHERE m.form_secret = $1 AND m.kind = 'booking' AND s.id = m.session_id AND w.id = s.widget_id
		RETURNING m.session_id::text, coalesce(m.booking_link_id::text, ''), w.created_by::text`, secret).Scan(&id, &linkID, &creator); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	done := s.tr("ol.booking_done", what)
	if _, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind, booking_link_id) VALUES ($1::uuid, $2, 'booking_done', $3)`,
		id, done, nullID(linkID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bookings SET ol_session_id = $2 WHERE id::text = $1`, bookingID, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET name = CASE WHEN name = '' THEN left($2, 200) ELSE name END,
		phone = CASE WHEN phone = '' THEN left($3, 100) ELSE phone END, email = CASE WHEN email = '' THEN left($4, 200) ELSE email END,
		unread = unread + 1, last_at = now() WHERE id::text = $1`, id, name, phone, email); err != nil {
		return err
	}
	if err := notifyOperators(ctx, tx, id, done); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if contactID == "" {
		return nil
	}
	var contact bool
	if err := s.db.QueryRow(ctx, `SELECT contact_id IS NOT NULL FROM ol_sessions WHERE id::text = $1`, id).Scan(&contact); err != nil || contact {
		return err
	}
	return s.bind(ctx, id, "contact", contactID, creator, false)
}

// BookingCancelled: the client has cancelled the booking — a service line of its dialogue, if it was booked in one.
func (s *Store) BookingCancelled(ctx context.Context, bookingID, what string) error {
	var id string
	err := s.db.QueryRow(ctx, `SELECT ol_session_id::text FROM bookings WHERE id::text = $1 AND ol_session_id IS NOT NULL`, bookingID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.note(ctx, s.db, id, "ol.note_booking_cancel", what)
}

func nullID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
