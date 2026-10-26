// Package hooks is go24's outgoing webhooks (n8n, Make, any HTTP endpoint): an administrator subscribes a URL to
// events, the database queues a delivery for each (see migration 059), and the worker sends it signed and retries.
package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

// Events a webhook may subscribe to.
var Events = []string{"deal.created", "deal.stage", "lead.created", "lead.stage", "task.created", "task.completed", "task.deleted", "openline.started", "openline.message", "openline.closed", "call.ended"}

type Store struct {
	db     *pgxpool.Pool
	client *http.Client
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db, client: &http.Client{Timeout: 15 * time.Second}}
}

// Webhook is a subscription.
type Webhook struct {
	ID, Name, URL, Secret string
	Events                []string
	Active                bool
	LastAt                *time.Time
	LastStatus            *int
	LastError             string
	Pending               int
}

// CheckURL: an http(s) address (a webhook of n8n, for one).
func CheckURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 1000 {
		return i18n.E("err.hook_url")
	}
	return nil
}

// Save creates (ID "": with a new secret) or changes a webhook (administrators; checked by the web layer).
func (s *Store) Save(ctx context.Context, userID string, w Webhook) (string, error) {
	w.Name, w.URL = strings.TrimSpace(w.Name), strings.TrimSpace(w.URL)
	if w.Name == "" || len([]rune(w.Name)) > 100 {
		return "", i18n.E("err.hook_name")
	}
	if err := CheckURL(w.URL); err != nil {
		return "", err
	}
	var events []string
	for _, e := range w.Events {
		for _, known := range Events {
			if e == known {
				events = append(events, e)
			}
		}
	}
	if len(events) == 0 {
		return "", i18n.E("err.hook_events")
	}
	if w.ID == "" {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		err := s.db.QueryRow(ctx, `INSERT INTO webhooks (name, url, events, secret, active, created_by) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			w.Name, w.URL, events, hex.EncodeToString(b), w.Active, userID).Scan(&w.ID)
		return w.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE webhooks SET name = $2, url = $3, events = $4, active = $5 WHERE id::text = $1`, w.ID, w.Name, w.URL, events, w.Active)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.not_found")
	}
	return w.ID, err
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM webhooks WHERE id::text = $1`, id)
	return err
}

// List of the webhooks with what waits to be delivered.
func (s *Store) List(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.Query(ctx, `SELECT w.id, w.name, w.url, w.secret, w.events, w.active, w.last_at, w.last_status, w.last_error,
		(SELECT count(*) FROM webhook_deliveries d WHERE d.webhook_id = w.id AND d.done_at IS NULL)::int FROM webhooks w ORDER BY w.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Webhook])
}

// Ping queues a test event for the webhook («Проверить»).
func (s *Store) Ping(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO webhook_deliveries (webhook_id, event, payload)
		SELECT id, 'ping', jsonb_build_object('event', 'ping', 'at', now(), 'data', jsonb_build_object('webhook', name)) FROM webhooks WHERE id::text = $1`, id)
	return err
}

// Sign is the signature of a body: X-Go24-Signature: sha256=<hex HMAC with the webhook's secret>.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// backoff is when a failed delivery is tried again; after the last one it is given up.
var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Deliver sends the deliveries that are due (the worker calls it).
func (s *Store) Deliver(ctx context.Context, now time.Time) (sent int, errs []error) {
	rows, err := s.db.Query(ctx, `SELECT d.id, d.event, d.payload::text, d.attempts, w.id, w.url, w.secret FROM webhook_deliveries d
		JOIN webhooks w ON w.id = d.webhook_id WHERE d.done_at IS NULL AND d.next_at <= $1 AND w.active ORDER BY d.id LIMIT 50`, now)
	if err != nil {
		return 0, []error{err}
	}
	type job struct {
		id                   int64
		event, payload       string
		attempts             int
		hook, target, secret string
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (job, error) {
		var j job
		err := r.Scan(&j.id, &j.event, &j.payload, &j.attempts, &j.hook, &j.target, &j.secret)
		return j, err
	})
	if err != nil {
		return 0, []error{err}
	}
	for _, j := range jobs {
		status, err := s.post(ctx, j.target, j.secret, j.event, []byte(j.payload))
		msg := ""
		if err != nil {
			msg = err.Error()
		} else if status >= 300 {
			msg = fmt.Sprintf("HTTP %d", status)
		}
		var st *int
		if status > 0 {
			st = &status
		}
		if _, err := s.db.Exec(ctx, `UPDATE webhooks SET last_at = now(), last_status = $2, last_error = left($3, 300) WHERE id::text = $1`, j.hook, st, msg); err != nil {
			errs = append(errs, err)
		}
		if msg == "" {
			sent++
			_, err = s.db.Exec(ctx, `UPDATE webhook_deliveries SET done_at = now(), attempts = attempts + 1 WHERE id = $1`, j.id)
		} else if j.attempts >= len(backoff) {
			errs = append(errs, fmt.Errorf("webhook %s: %s given up: %s", j.hook, j.event, msg))
			_, err = s.db.Exec(ctx, `UPDATE webhook_deliveries SET done_at = now(), attempts = attempts + 1 WHERE id = $1`, j.id)
		} else {
			_, err = s.db.Exec(ctx, `UPDATE webhook_deliveries SET attempts = attempts + 1, next_at = $2 WHERE id = $1`, j.id, now.Add(backoff[j.attempts]))
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return sent, errs
}

func (s *Store) post(ctx context.Context, target, secret, event string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "go24-webhook")
	req.Header.Set("X-Go24-Event", event)
	req.Header.Set("X-Go24-Signature", Sign(secret, body))
	res, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	return res.StatusCode, nil
}
