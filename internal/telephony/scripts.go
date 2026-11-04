package telephony

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Оценка звонков» of the CoPilot: a script says what a conversation of the company should have (greet, find
// out the need, offer, agree on the next step…); the AI reads the record's text of a call and scores how much of the
// script the employee did, 0–100 %, with what was done, what was not and advice. A script scores its calls (all,
// incoming or outgoing) by itself once their records are made text; a score below its bar tells the employee's
// managers.

// Script is a script of the assessment.
type Script struct {
	ID, Name string
	Body     string // the script: what the conversation should have
	Calls    string // all | in | out
	Auto     bool   // scores its calls by itself
	Low      int    // a score below it is low, %
}

// Scripts are the scripts by name.
func (s *Store) Scripts(ctx context.Context) ([]Script, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, body, calls, auto, low FROM telephony_scripts ORDER BY lower(name), created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Script, error) {
		var sc Script
		return sc, r.Scan(&sc.ID, &sc.Name, &sc.Body, &sc.Calls, &sc.Auto, &sc.Low)
	})
}

// ScriptByID is a script.
func (s *Store) ScriptByID(ctx context.Context, id string) (Script, error) {
	var sc Script
	err := s.db.QueryRow(ctx, `SELECT id::text, name, body, calls, auto, low FROM telephony_scripts WHERE id::text = $1`, id).
		Scan(&sc.ID, &sc.Name, &sc.Body, &sc.Calls, &sc.Auto, &sc.Low)
	if errors.Is(err, pgx.ErrNoRows) {
		return sc, ErrNotFound
	}
	return sc, err
}

// AutoScript is the script that scores a call of the direction (in, out) by itself: one for its direction before one
// for all calls, then by name; ok false — none.
func (s *Store) AutoScript(ctx context.Context, dir string) (Script, bool, error) {
	var sc Script
	err := s.db.QueryRow(ctx, `SELECT id::text, name, body, calls, auto, low FROM telephony_scripts
		WHERE auto AND (calls = 'all' OR calls = $1) ORDER BY calls = 'all', lower(name), created_at, id LIMIT 1`, dir).
		Scan(&sc.ID, &sc.Name, &sc.Body, &sc.Calls, &sc.Auto, &sc.Low)
	if errors.Is(err, pgx.ErrNoRows) {
		return sc, false, nil
	}
	return sc, err == nil, err
}

// SaveScript adds (ID "") or changes a script.
func (s *Store) SaveScript(ctx context.Context, userID string, sc Script) (string, error) {
	sc.Name = strings.TrimSpace(sc.Name)
	if sc.Name == "" || len([]rune(sc.Name)) > 100 || strings.ContainsAny(sc.Name, "\r\n") {
		return "", i18n.E("err.script_name")
	}
	sc.Body = strings.TrimSpace(sc.Body)
	if sc.Body == "" || len([]rune(sc.Body)) > 10000 {
		return "", i18n.E("err.script_body")
	}
	if sc.Calls != "in" && sc.Calls != "out" {
		sc.Calls = "all"
	}
	sc.Low = min(max(sc.Low, 0), 100)
	if sc.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO telephony_scripts (name, body, calls, auto, low, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id::text`, sc.Name, sc.Body, sc.Calls, sc.Auto, sc.Low, userID).Scan(&sc.ID)
		return sc.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE telephony_scripts SET name = $2, body = $3, calls = $4, auto = $5, low = $6 WHERE id::text = $1`,
		sc.ID, sc.Name, sc.Body, sc.Calls, sc.Auto, sc.Low)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return sc.ID, err
}

// DeleteScript removes a script; the scores it gave stay with its name.
func (s *Store) DeleteScript(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_scripts WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetScore keeps the call's score by the script and the AI's account of it.
func (s *Store) SetScore(ctx context.Context, id string, sc Script, score int, text string) error {
	_, err := s.db.Exec(ctx, `UPDATE telephony_calls SET score = $2, score_script = $3::uuid, score_name = $4, score_text = $5 WHERE id::text = $1`,
		id, score, sc.ID, sc.Name, text)
	return err
}

var scoreRe = regexp.MustCompile(`\d{1,3}`)

// ParseScore takes the AI's answer apart: its first line holds the score (the first number, 0–100), the rest is the
// account; ok false — no score there.
func ParseScore(reply string) (score int, text string, ok bool) {
	first, rest, _ := strings.Cut(strings.TrimSpace(reply), "\n")
	n, err := strconv.Atoi(scoreRe.FindString(first))
	if err != nil || n > 100 {
		return 0, "", false
	}
	return n, strings.TrimSpace(rest), true
}
