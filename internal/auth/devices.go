package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Device is a signed-in session of an employee — a browser on a computer or a phone, as «Безопасность» of the
// profile: where from and when it was last used.
type Device struct {
	ID      string
	Created time.Time
	Seen    time.Time
	IP      string
	Agent   string
}

// Devices keeps the sessions on the server (user_sessions), so that one can be ended before its cookie expires.
type Devices struct{ db *pgxpool.Pool }

func NewDevices(db *pgxpool.Pool) *Devices { return &Devices{db: db} }

// Start notes a new sign-in and gives its id for the cookie; the employee's ended and expired sessions go away.
func (d *Devices) Start(ctx context.Context, userID, ip, agent string, expires time.Time) (string, error) {
	if _, err := d.db.Exec(ctx, `DELETE FROM user_sessions WHERE user_id = $1 AND (ended_at IS NOT NULL OR expires_at < now())`,
		userID); err != nil {
		return "", err
	}
	var id string
	err := d.db.QueryRow(ctx, `INSERT INTO user_sessions (user_id, expires_at, ip, agent) VALUES ($1, $2, $3, left($4, 500))
		RETURNING id`, userID, expires, ip, agent).Scan(&id)
	return id, err
}

// Touch notes the session used now from ip; expires follows the cookie, which a switch of the admin mode renews.
func (d *Devices) Touch(ctx context.Context, id, ip string, expires time.Time) error {
	_, err := d.db.Exec(ctx, `UPDATE user_sessions SET seen_at = now(), ip = $2, expires_at = greatest(expires_at, $3) WHERE id = $1`,
		id, ip, expires)
	return err
}

// List gives the employee's live sessions, the last used first.
func (d *Devices) List(ctx context.Context, userID string) ([]Device, error) {
	rows, err := d.db.Query(ctx, `SELECT id, created_at, seen_at, ip, agent FROM user_sessions
		WHERE user_id = $1 AND ended_at IS NULL AND expires_at > now() ORDER BY seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Device, error) {
		var v Device
		err := row.Scan(&v.ID, &v.Created, &v.Seen, &v.IP, &v.Agent)
		return v, err
	})
}

// End ends one session of the employee; false — there is no such live session.
func (d *Devices) End(ctx context.Context, userID, id string) (bool, error) {
	tag, err := d.db.Exec(ctx, `UPDATE user_sessions SET ended_at = now()
		WHERE id::text = $2 AND user_id = $1 AND ended_at IS NULL`, userID, id)
	return tag.RowsAffected() > 0, err
}

// EndAll is «Выйти на всех устройствах»: every session of the employee but except ("" — all) ends, and so do
// the cookies made before sessions were kept. It gives how many sessions ended.
func (d *Devices) EndAll(ctx context.Context, userID, except string) (int64, error) {
	tx, err := d.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE user_sessions SET ended_at = now()
		WHERE user_id = $1 AND ended_at IS NULL AND expires_at > now() AND id::text <> $2`, userID, except)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET sessions_after = now() WHERE id = $1`, userID); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
