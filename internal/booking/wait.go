package booking

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Wait is a client on the waiting list: no time yet; Resource — the one the client wants, if any.
type Wait struct {
	ID, ResourceID, ResourceName string
	ClientName, ClientPhone      string
	ClientEmail, Note            string
	ContactID, ContactName       string
	CompanyID, CompanyName       string
	DealID, DealTitle            string
	CreatedBy, CreatedByName     string
	Created                      time.Time
}

const waitSQL = `SELECT w.id, coalesce(w.resource_id::text, ''), coalesce(r.name, ''), w.client_name, w.client_phone,
		w.client_email, w.note, coalesce(w.contact_id::text, ''), coalesce(x.name, ''), coalesce(w.company_id::text, ''),
		coalesce(c.title, ''), coalesce(w.deal_id::text, ''), coalesce(d.title, ''), coalesce(w.created_by::text, ''),
		coalesce(u.name, ''), w.created_at
	FROM booking_waitlist w LEFT JOIN booking_resources r ON r.id = w.resource_id LEFT JOIN users u ON u.id = w.created_by
	LEFT JOIN crm_contacts x ON x.id = w.contact_id LEFT JOIN crm_companies c ON c.id = w.company_id
	LEFT JOIN crm_deals d ON d.id = w.deal_id`

func scanWait(r pgx.Row) (Wait, error) {
	var w Wait
	err := r.Scan(&w.ID, &w.ResourceID, &w.ResourceName, &w.ClientName, &w.ClientPhone, &w.ClientEmail, &w.Note,
		&w.ContactID, &w.ContactName, &w.CompanyID, &w.CompanyName, &w.DealID, &w.DealTitle, &w.CreatedBy,
		&w.CreatedByName, &w.Created)
	return w, err
}

// Waitlist: the waiting list, the earliest first.
func (s *Store) Waitlist(ctx context.Context) ([]Wait, error) {
	rows, err := s.db.Query(ctx, waitSQL+` ORDER BY w.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Wait, error) { return scanWait(r) })
}

func (s *Store) Wait(ctx context.Context, id string) (Wait, error) {
	w, err := scanWait(s.db.QueryRow(ctx, waitSQL+` WHERE w.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return w, err
}

// SaveWait puts a client on the waiting list (no ID) or changes the entry.
func (s *Store) SaveWait(ctx context.Context, by string, w Wait) (string, error) {
	w.ClientName, w.ClientPhone, w.ClientEmail = strings.TrimSpace(w.ClientName), strings.TrimSpace(w.ClientPhone), strings.TrimSpace(w.ClientEmail)
	w.Note = strings.TrimSpace(w.Note)
	if w.ClientName == "" && w.ClientPhone == "" {
		return "", ErrClient
	}
	if w.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO booking_waitlist (resource_id, client_name, client_phone, client_email, note,
				contact_id, company_id, deal_id, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			nullable(w.ResourceID), w.ClientName, w.ClientPhone, w.ClientEmail, w.Note, nullable(w.ContactID),
			nullable(w.CompanyID), nullable(w.DealID), nullable(by)).Scan(&w.ID)
		return w.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE booking_waitlist SET resource_id = $2, client_name = $3, client_phone = $4,
			client_email = $5, note = $6, contact_id = $7, company_id = $8, deal_id = $9 WHERE id::text = $1`,
		w.ID, nullable(w.ResourceID), w.ClientName, w.ClientPhone, w.ClientEmail, w.Note, nullable(w.ContactID),
		nullable(w.CompanyID), nullable(w.DealID))
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return w.ID, err
}

func (s *Store) DeleteWait(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM booking_waitlist WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// BookWait books the client of a waiting list entry on a resource at start for a slot of the resource («не
// подтверждена») and takes the entry off the list — both or neither.
func (s *Store) BookWait(ctx context.Context, by, id, resourceID string, start time.Time) (Booking, error) {
	w, err := s.Wait(ctx, id)
	if err != nil {
		return Booking{}, err
	}
	r, err := s.Resource(ctx, resourceID)
	if err != nil {
		return Booking{}, err
	}
	b := Booking{ResourceID: r.ID, Start: start, End: start.Add(time.Duration(r.Slot) * time.Minute),
		ClientName: w.ClientName, ClientPhone: w.ClientPhone, ClientEmail: w.ClientEmail, Note: w.Note,
		ContactID: w.ContactID, CompanyID: w.CompanyID, DealID: w.DealID, ResourceName: r.Name}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return b, err
	}
	defer tx.Rollback(ctx)
	var tag pgconn.CommandTag
	if tag, err = tx.Exec(ctx, `DELETE FROM booking_waitlist WHERE id::text = $1`, id); err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound // booked by someone a moment ago
	}
	if err != nil {
		return b, err
	}
	if b.ID, err = saveBooking(ctx, tx, by, b, false); err != nil {
		return b, err
	}
	return b, tx.Commit(ctx)
}

// ToWait puts the client of a booking back on the waiting list (with its resource) and removes the booking.
func (s *Store) ToWait(ctx context.Context, by, bookingID string) (string, error) {
	b, err := s.Booking(ctx, bookingID)
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO booking_waitlist (resource_id, client_name, client_phone, client_email, note,
			contact_id, company_id, deal_id, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		b.ResourceID, b.ClientName, b.ClientPhone, b.ClientEmail, b.Note, nullable(b.ContactID), nullable(b.CompanyID),
		nullable(b.DealID), nullable(by)).Scan(&id); err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM bookings WHERE id = $1`, b.ID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
