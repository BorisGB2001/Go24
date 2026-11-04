package ai

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// ---------- The usage of AI: tokens and their cost by employees and months, the monthly limits (GO24DEV-482) ----------

// Cost is the cost of a call by the connection's prices per million tokens.
func (c Connection) Cost(in, out int) float64 {
	return (float64(in)*c.PriceIn + float64(out)*c.PriceOut) / 1e6
}

// MonthStart is the first moment of the month of t in go24's time zone.
func MonthStart(t time.Time) time.Time {
	t = t.In(time.Local)
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.Local)
}

// note writes a call of a model to the usage with its cost (runID "" — not of an agent's run).
func (s *Store) note(ctx context.Context, userID, runID, agent string, c Connection, model string, reply Reply) error {
	var run *string
	if runID != "" {
		run = &runID
	}
	_, err := s.db.Exec(ctx, `INSERT INTO ai_usage (user_id, connection_id, company, run_id, agent, provider, model, in_tokens, out_tokens, cost)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		userID, c.ID, c.Owner == "", run, agent, c.Provider, model, reply.InTokens, reply.OutTokens, c.Cost(reply.InTokens, reply.OutTokens))
	return err
}

// Quota is what an employee may still spend on a connection of the company this month.
type Quota struct {
	Limit int64 // 0 — no limit
	Used  int64 // the tokens of this month on the connection
	Own   bool  // the limit is the employee's own, not the connection's
}

// Quota of the user on c; one's own connection has no limit.
func (s *Store) Quota(ctx context.Context, userID string, c Connection) (Quota, error) {
	q := Quota{Limit: c.MonthlyLimit}
	if c.Owner != "" {
		return Quota{}, nil
	}
	own, err := s.Limit(ctx, userID)
	if err != nil {
		return q, err
	}
	if own != nil {
		q.Limit, q.Own = *own, true
	}
	if q.Limit == 0 {
		return q, nil
	}
	err = s.db.QueryRow(ctx, `SELECT COALESCE(sum(in_tokens + out_tokens), 0) FROM ai_usage
		WHERE user_id = $1 AND connection_id = $2 AND created_at >= $3`, userID, c.ID, MonthStart(time.Now())).Scan(&q.Used)
	return q, err
}

// allow: an error when the policy keeps the user from AI in the tool (the agent), or they spent their month's tokens on
// the company's connection c.
func (s *Store) allow(ctx context.Context, userID, agent string, c Connection) error {
	if err := s.Permit(ctx, userID, agent); err != nil {
		return err
	}
	q, err := s.Quota(ctx, userID, c)
	if err != nil {
		return err
	}
	if q.Limit > 0 && q.Used >= q.Limit {
		return i18n.E("err.ai_limit", q.Used, q.Limit)
	}
	return nil
}

// Limit is the employee's own monthly limit of tokens over the connections' (nil — as the connection says).
func (s *Store) Limit(ctx context.Context, userID string) (*int64, error) {
	var n int64
	err := s.db.QueryRow(ctx, `SELECT monthly_tokens FROM ai_limits WHERE user_id = $1`, userID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// SetLimit sets the employee's own limit (nil — as the connection says, 0 — no limit); the caller checks the right.
func (s *Store) SetLimit(ctx context.Context, userID string, n *int64) error {
	if n == nil {
		_, err := s.db.Exec(ctx, `DELETE FROM ai_limits WHERE user_id = $1`, userID)
		return err
	}
	if *n < 0 {
		return i18n.E("err.ai_negative")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO ai_limits (user_id, monthly_tokens) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET monthly_tokens = EXCLUDED.monthly_tokens`, userID, *n)
	return err
}

// Spend is the usage of one employee, or of a month.
type Spend struct {
	UserID   string
	Name     string
	Month    string // YYYY-MM
	Calls    int64
	In, Out  int64
	Company  int64 // the tokens through the company's connections
	Cost     float64
	OwnLimit *int64 // the employee's own limit
}

// Tokens is the input and the output together.
func (u Spend) Tokens() int64 { return u.In + u.Out }

// ByUser is the usage of the employees in [from, to), the biggest first.
func (s *Store) ByUser(ctx context.Context, from, to time.Time) ([]Spend, error) {
	rows, err := s.db.Query(ctx, `SELECT COALESCE(u.user_id::text, ''), COALESCE(us.name, ''), count(*), sum(u.in_tokens), sum(u.out_tokens),
			COALESCE(sum(u.in_tokens + u.out_tokens) FILTER (WHERE u.company), 0), sum(u.cost)::float8, l.monthly_tokens
		FROM ai_usage u LEFT JOIN users us ON us.id = u.user_id LEFT JOIN ai_limits l ON l.user_id = u.user_id
		WHERE u.created_at >= $1 AND u.created_at < $2
		GROUP BY u.user_id, us.name, l.monthly_tokens ORDER BY sum(u.in_tokens + u.out_tokens) DESC, us.name`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Spend
	for rows.Next() {
		var u Spend
		if err := rows.Scan(&u.UserID, &u.Name, &u.Calls, &u.In, &u.Out, &u.Company, &u.Cost, &u.OwnLimit); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ByMonth is the usage of the months since from (of one employee when userID is given), the latest first.
func (s *Store) ByMonth(ctx context.Context, from time.Time, userID string) ([]Spend, error) {
	rows, err := s.db.Query(ctx, `SELECT to_char(created_at AT TIME ZONE $2, 'YYYY-MM'), count(*), sum(in_tokens), sum(out_tokens),
			COALESCE(sum(in_tokens + out_tokens) FILTER (WHERE company), 0), sum(cost)::float8
		FROM ai_usage WHERE created_at >= $1 AND ($3 = '' OR user_id::text = $3)
		GROUP BY 1 ORDER BY 1 DESC`, from, time.Local.String(), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Spend
	for rows.Next() {
		u := Spend{UserID: userID}
		if err := rows.Scan(&u.Month, &u.Calls, &u.In, &u.Out, &u.Company, &u.Cost); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
