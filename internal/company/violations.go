package company

import (
	"context"
	"slices"
	"time"

	"go24/internal/worktime"
)

// «Уведомлять о нарушениях» work schedules: a day started late, finished early or too short is told to
// the person's manager and/or the people the rules name — by the person's schedule, without one by the company's
// working time. Each violation of a day is told once: finishing again after «Продолжить» does not repeat it.

// Notify is who is told of the person's violations: their managers (head) and the people.
func (n Norm) Notify() (head bool, people []string) {
	if n.WS != nil {
		return n.WS.NotifyHead, n.WS.NotifyTo
	}
	return n.Company.NotifyHead, n.Company.NotifyTo
}

// NotifyViolations tells of today's violations of the person not told yet: «late» when the day is started, «early»
// and «short» when it is finished. It returns the violations told.
func (s *Store) NotifyViolations(ctx context.Context, userID string, sched worktime.Schedule, now time.Time) ([]string, error) {
	d, err := s.Today(ctx, userID, now)
	if err != nil || d == nil {
		return nil, err
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	n, err := s.NormOf(ctx, userID, sched, day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	head, people := n.Notify()
	if !head && len(people) == 0 {
		return nil, nil
	}
	found := slices.DeleteFunc(n.Violations(*d, now), func(v string) bool { return v == "open" })
	if len(found) == 0 {
		return nil, nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var told []string
	if err := tx.QueryRow(ctx, `SELECT violations_told FROM workdays WHERE user_id = $1 AND day = $2 FOR UPDATE`,
		userID, now.Format(time.DateOnly)).Scan(&told); err != nil {
		return nil, err
	}
	fresh := slices.DeleteFunc(found, func(v string) bool { return slices.Contains(told, v) })
	if len(fresh) == 0 {
		return nil, nil
	}
	to := slices.Clone(people)
	if head {
		managers, err := s.Managers(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, m := range managers {
			to = append(to, m.ID)
		}
	}
	slices.Sort(to)
	to = slices.DeleteFunc(slices.Compact(to), func(id string) bool { return id == userID })
	if _, err := tx.Exec(ctx, `UPDATE workdays SET violations_told = violations_told || $3::text[] WHERE user_id = $1 AND day = $2`,
		userID, now.Format(time.DateOnly), fresh); err != nil {
		return nil, err
	}
	for _, v := range fresh {
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
			SELECT id, $2, 'workday_violation', $3, $4 FROM users WHERE id = ANY($1::uuid[]) AND active`,
			to, userID, v+"|"+day.Format("02.01.2006"), dayLink(userID, day)); err != nil {
			return nil, err
		}
	}
	return fresh, tx.Commit(ctx)
}
