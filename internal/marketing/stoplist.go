package marketing

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Стоп-лист»: the addresses no campaign goes to — those who followed «Отписаться» of a letter (or
// its List-Unsubscribe) and those an employee added.

// Stopped is an address of the stop list.
type Stopped struct {
	Email, Campaign, CampaignTitle, AddedBy string
	At                                      time.Time
}

// StopList is the stop list, the latest first; q — a part of the address.
func (s *Store) StopList(ctx context.Context, q string, limit int) ([]Stopped, error) {
	rows, err := s.db.Query(ctx, `SELECT x.email, coalesce(x.campaign_id::text, ''), coalesce(c.title, ''), coalesce(u.name, ''), x.created_at
		FROM mkt_stoplist x LEFT JOIN mkt_campaigns c ON c.id = x.campaign_id LEFT JOIN users u ON u.id = x.added_by
		WHERE x.email LIKE '%' || lower($1) || '%' ORDER BY x.created_at DESC, x.email LIMIT $2`, strings.TrimSpace(q), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Stopped])
}

func (s *Store) stopSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT email FROM mkt_stoplist`)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := make(map[string]bool, len(list))
	for _, e := range list {
		out[e] = true
	}
	return out, err
}

// AddStop puts addresses (one a line, as list segments have them) into the stop list; it gives how many were new.
func (s *Store) AddStop(ctx context.Context, userID, text string) (int, error) {
	list := ParseList(text)
	if len(list) == 0 {
		return 0, i18n.E("err.mkt_list")
	}
	n := 0
	for _, r := range list {
		tag, err := s.db.Exec(ctx, `INSERT INTO mkt_stoplist (email, added_by) VALUES (lower($1), $2) ON CONFLICT DO NOTHING`, r.Email, userID)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// RemoveStop takes an address off the stop list: campaigns go to it again.
func (s *Store) RemoveStop(ctx context.Context, email string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM mkt_stoplist WHERE email = lower($1)`, strings.TrimSpace(email))
	return err
}

// ByToken is the addressee of a letter's links and the campaign's title.
func (s *Store) ByToken(ctx context.Context, tok string) (Addressee, string, error) {
	var a Addressee
	var title string
	err := s.db.QueryRow(ctx, `SELECT r.id, r.email, r.name, r.entity, coalesce(r.entity_id::text, ''), r.token, r.status, r.error,
		r.sent_at, r.opened_at, r.clicked_at, r.unsubscribed_at, c.title FROM mkt_recipients r JOIN mkt_campaigns c ON c.id = r.campaign_id
		WHERE r.token = $1`, tok).Scan(&a.ID, &a.Email, &a.Name, &a.Entity, &a.EntityID, &a.Token, &a.Status, &a.Error,
		&a.SentAt, &a.OpenedAt, &a.ClickedAt, &a.Unsub, &title)
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return a, title, err
}

// Unsubscribe: the addressee of the letter's link leaves — their address goes into the stop list and the campaign
// counts them; it gives the address.
func (s *Store) Unsubscribe(ctx context.Context, tok string) (string, error) {
	var email, campaign string
	err := s.db.QueryRow(ctx, `UPDATE mkt_recipients SET unsubscribed_at = coalesce(unsubscribed_at, now()) WHERE token = $1
		RETURNING email, campaign_id::text`, tok).Scan(&email, &campaign)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO mkt_stoplist (email, campaign_id) VALUES (lower($1), $2) ON CONFLICT DO NOTHING`, email, campaign)
	return email, err
}
