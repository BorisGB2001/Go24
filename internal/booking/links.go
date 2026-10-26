package booking

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

var (
	ErrLinkOff = i18n.E("booking.err.link_off")
	ErrSlot    = i18n.E("booking.err.slot")
	ErrPhone   = i18n.E("booking.err.phone")
	ErrTooMany = i18n.E("booking.err.too_many")
)

// PerHour: the bookings a client may make by links from one address within an hour.
const PerHour = 5

// Link is a page for clients to book themselves: the resources it offers (none — all), the days ahead it shows,
// who is told of a new booking (none — the resource's employee, else who made the link).
type Link struct {
	ID, Token, Name, Description string
	Resources                    []string
	DaysAhead                    int
	ResponsibleID, Responsible   string
	Active                       bool
	CreatedBy                    string
	Bookings                     int // made by the link, not cancelled
}

func token() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

const linkSQL = `SELECT l.id, l.token, l.name, l.description, l.resource_ids::text[], l.days_ahead,
		coalesce(l.responsible_id::text, ''), coalesce(u.name, ''), l.active, coalesce(l.created_by::text, ''),
		(SELECT count(*) FROM bookings b WHERE b.link_id = l.id AND b.status <> 'cancelled')::int
	FROM booking_links l LEFT JOIN users u ON u.id = l.responsible_id`

func scanLink(r pgx.Row) (Link, error) {
	var l Link
	err := r.Scan(&l.ID, &l.Token, &l.Name, &l.Description, &l.Resources, &l.DaysAhead, &l.ResponsibleID,
		&l.Responsible, &l.Active, &l.CreatedBy, &l.Bookings)
	return l, err
}

func (s *Store) Links(ctx context.Context) ([]Link, error) {
	rows, err := s.db.Query(ctx, linkSQL+` ORDER BY l.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Link, error) { return scanLink(r) })
}

func (s *Store) Link(ctx context.Context, id string) (Link, error) {
	l, err := scanLink(s.db.QueryRow(ctx, linkSQL+` WHERE l.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return l, err
}

// LinkByToken: the link of a public page; a switched off one is ErrLinkOff.
func (s *Store) LinkByToken(ctx context.Context, tok string) (Link, error) {
	l, err := scanLink(s.db.QueryRow(ctx, linkSQL+` WHERE l.token = $1`, tok))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil && !l.Active {
		err = ErrLinkOff
	}
	return l, err
}

// SaveLink adds a link (no ID, with a new token) or changes one.
func (s *Store) SaveLink(ctx context.Context, by string, l Link) (string, error) {
	l.Name = strings.TrimSpace(l.Name)
	if l.Name == "" {
		return "", ErrName
	}
	l.DaysAhead = max(1, min(l.DaysAhead, 365))
	res := []string{}
	for _, id := range l.Resources {
		if id != "" && !slices.Contains(res, id) {
			res = append(res, id)
		}
	}
	if l.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO booking_links (token, name, description, resource_ids, days_ahead,
				responsible_id, active, created_by) VALUES ($1, $2, $3, $4::uuid[], $5, $6, $7, $8) RETURNING id`,
			token(), l.Name, l.Description, res, l.DaysAhead, nullable(l.ResponsibleID), l.Active, nullable(by)).Scan(&l.ID)
		return l.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE booking_links SET name = $2, description = $3, resource_ids = $4::uuid[],
			days_ahead = $5, responsible_id = $6, active = $7 WHERE id::text = $1`,
		l.ID, l.Name, l.Description, res, l.DaysAhead, nullable(l.ResponsibleID), l.Active)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return l.ID, err
}

// DeleteLink removes a link; the bookings made by it stay.
func (s *Store) DeleteLink(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM booking_links WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// LinkResources: the resources a link offers, by type and name.
func (s *Store) LinkResources(ctx context.Context, l Link) ([]Resource, error) {
	all, err := s.Resources(ctx, "")
	if err != nil || len(l.Resources) == 0 {
		return all, err
	}
	return slices.DeleteFunc(all, func(r Resource) bool { return !slices.Contains(l.Resources, r.ID) }), nil
}

// BookByLink is a client booking a slot of a resource by a link: the resource must be the link's, the time — a free
// slot within the link's days, the client — a name and a phone, the address — under PerHour bookings this hour. The
// booking is «не подтверждена», with a token of its own for the client's page.
func (s *Store) BookByLink(ctx context.Context, l Link, b Booking, loc *time.Location) (Booking, error) {
	b.ClientName, b.ClientPhone = strings.TrimSpace(b.ClientName), strings.TrimSpace(b.ClientPhone)
	if b.ClientName == "" {
		return b, ErrClient
	}
	if len(strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, b.ClientPhone)) < 5 {
		return b, ErrPhone
	}
	if len(l.Resources) > 0 && !slices.Contains(l.Resources, b.ResourceID) {
		return b, ErrNotFound
	}
	r, err := s.Resource(ctx, b.ResourceID)
	if err != nil {
		return b, err
	}
	now := time.Now().In(loc)
	last := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, l.DaysAhead)
	if !b.Start.Before(last) {
		return b, ErrSlot
	}
	free, err := s.Free(ctx, r, b.Start.In(loc), loc)
	if err != nil {
		return b, err
	}
	if !slices.ContainsFunc(free, b.Start.Equal) {
		return b, ErrSlot
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM bookings WHERE client_ip = $1 AND link_id IS NOT NULL
			AND created_at > now() - interval '1 hour'`, b.ClientIP).Scan(&n); err != nil {
		return b, err
	}
	if b.ClientIP != "" && n >= PerHour {
		return b, ErrTooMany
	}
	b.ID, b.End, b.Status, b.LinkID, b.ClientToken = "", b.Start.Add(time.Duration(r.Slot)*time.Minute), "unconfirmed", l.ID, token()
	b.ClientEmail, b.Note = strings.TrimSpace(b.ClientEmail), strings.TrimSpace(b.Note)
	if b.ID, err = s.save(ctx, "", b, true); errors.Is(err, ErrOverlap) {
		err = ErrSlot // taken by someone a moment ago
	}
	return b, err
}

// ByClientToken: the booking of the client's page.
func (s *Store) ByClientToken(ctx context.Context, tok string) (Booking, error) {
	if tok == "" {
		return Booking{}, ErrNotFound
	}
	b, err := scanBooking(s.db.QueryRow(ctx, bookingSQL+` WHERE b.client_token = $1`, tok))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return b, err
}
