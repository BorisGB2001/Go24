package telephony

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Информационный звонок», as the CRM robot: go24 calls the client through an external line (the client
// sees the line's number) and plays them a recorded message (go24-info); the log has the call from the line to the
// client. Not answered — Asterisk drops it, nothing is logged.

// soundKeep is how long a sound no robot plays stays (it was just uploaded for a robot being saved).
const soundKeep = time.Hour

// SaveSound checks an uploaded message (a WAV, as a greeting) and keeps it: its checksum names it.
func (s *Store) SaveSound(ctx context.Context, data []byte) (string, error) {
	wav, err := Greeting(data)
	if err != nil {
		return "", err
	}
	sum := fmt.Sprintf("%x", md5.Sum(wav))
	_, err = s.db.Exec(ctx, `INSERT INTO telephony_sounds (sum, data) VALUES ($1, $2) ON CONFLICT (sum) DO UPDATE SET created_at = now()`, sum, wav)
	return sum, err
}

// Sound is a kept message.
func (s *Store) Sound(ctx context.Context, sum string) ([]byte, error) {
	var b []byte
	err := s.db.QueryRow(ctx, `SELECT data FROM telephony_sounds WHERE sum = $1`, sum).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// robotSounds are the checksums of the messages the CRM robots play; the others, older than soundKeep, are dropped.
func (s *Store) robotSounds(ctx context.Context) ([]string, error) {
	const used = `SELECT params->>'sound' FROM crm_robots WHERE kind = 'infocall' AND params->>'sound' IS NOT NULL`
	if _, err := s.db.Exec(ctx, `DELETE FROM telephony_sounds WHERE created_at < $1 AND sum NOT IN (`+used+`)`, time.Now().Add(-soundKeep)); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT sum FROM telephony_sounds WHERE sum IN (`+used+`)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// InfoCall calls the number through the line and plays it the message sum (the robot «Информационный звонок»).
func (p *PBX) InfoCall(ctx context.Context, lineID, number, sum string) error {
	if !p.Ready() {
		return ErrOff
	}
	if on, err := p.Enabled(ctx); err != nil || !on {
		return i18n.E("err.robot_infocall")
	}
	if number = Dialable(number); number == "" {
		return i18n.E("err.robot_phone")
	}
	trunks, err := p.Trunks(ctx)
	if err != nil {
		return err
	}
	for _, t := range trunks {
		if t.ID == lineID {
			return p.ARI.InfoCall(ctx, t, number, GreetingFile(sum))
		}
	}
	return i18n.E("err.robot_infocall")
}

// InfoCall asks Asterisk to call the number through the line; answered, go24-info plays the sound (a file without
// its extension) and hangs up.
func (a *ARI) InfoCall(ctx context.Context, t Trunk, number, sound string) error {
	v := url.Values{"endpoint": {"PJSIP/" + number + "@" + t.Key()}, "extension": {"s"}, "context": {"go24-info"}, "priority": {"1"},
		"callerId": {t.Number + " <" + t.Number + ">"}, "timeout": {"60"}}
	vars := map[string]string{"GO24_SOUND": sound, "GO24_FROM": t.Number, "GO24_TO": number, "GO24_LINE": t.Number}
	return a.send(ctx, http.MethodPost, "/channels?"+v.Encode(), map[string]any{"variables": vars}, nil)
}
