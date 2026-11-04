package company

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/calendar"
)

// «График отсутствий» in Outlook and on a phone «Подключить к Outlook» (GO24DEV-944): a person's
// secret link to an iCalendar feed of the absences of the company (or of a department) — each one a whole-day event
// «Name — reason». The link stops working when the person leaves or gets a new one.

// AbsenceFeedToken is the token of the person's link ("" — none issued).
func (s *Store) AbsenceFeedToken(ctx context.Context, userID string) (string, error) {
	var token string
	err := s.db.QueryRow(ctx, `SELECT token FROM absence_feeds WHERE user_id = $1`, userID).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return token, err
}

// NewAbsenceFeed issues the person a new link; the old one stops working.
func (s *Store) NewAbsenceFeed(ctx context.Context, userID string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	_, err := s.db.Exec(ctx, `INSERT INTO absence_feeds (user_id, token) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET token = EXCLUDED.token, created_at = now()`, userID, token)
	return token, err
}

// AbsenceFeedUser is whose link the token is: ErrNotFound for an unknown token, a person who left or a guest.
func (s *Store) AbsenceFeedUser(ctx context.Context, token string) (string, error) {
	var userID string
	err := s.db.QueryRow(ctx, `SELECT f.user_id::text FROM absence_feeds f JOIN users u ON u.id = f.user_id
		WHERE f.token = $1 AND u.active AND NOT u.guest`, token).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) || token == "" {
		return "", ErrNotFound
	}
	return userID, err
}

// AbsencesICS is the iCalendar of the absences: whole days to the day after the last one, free time (the reader is
// not busy with others' vacations), the person acting and the comment in the description.
func AbsencesICS(list []Absence, title string, kind func(string) string, acting string, now time.Time) string {
	var b strings.Builder
	line := func(l string) { calendar.ICSLine(&b, l) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//go24//absences//RU")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:" + calendar.ICSText(title))
	line("REFRESH-INTERVAL;VALUE=DURATION:PT1H")
	line("X-PUBLISHED-TTL:PT1H")
	stamp := now.UTC().Format("20060102T150405Z")
	for _, a := range list {
		line("BEGIN:VEVENT")
		line("UID:absence-" + a.ID + "@go24")
		line("DTSTAMP:" + stamp)
		line("DTSTART;VALUE=DATE:" + a.From.Format("20060102"))
		line("DTEND;VALUE=DATE:" + a.To.AddDate(0, 0, 1).Format("20060102"))
		line("SUMMARY:" + calendar.ICSText(a.UserName+" — "+kind(a.Kind)))
		var desc []string
		if a.ActingName != "" {
			desc = append(desc, acting+": "+a.ActingName)
		}
		if c := strings.TrimSpace(a.Comment); c != "" {
			desc = append(desc, c)
		}
		if len(desc) > 0 {
			line("DESCRIPTION:" + calendar.ICSText(strings.Join(desc, "\n")))
		}
		line("CATEGORIES:" + calendar.ICSText(kind(a.Kind)))
		line("TRANSP:TRANSPARENT")
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}
