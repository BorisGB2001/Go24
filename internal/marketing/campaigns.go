package marketing

import (
	"context"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// E-mail campaigns as «Рассылки»: a letter from a go24 mailbox to the addressees of the chosen segments
// but those of the excluded ones and of the stop list. Started, the campaign fixes its addressees; go24-worker sends
// them a hundred letters a minute (SendDue), from its planned time; the campaign may be paused, resumed, stopped.
//
// A/B test: variant B — its subject and/or letter, an empty one is A's — goes with A, half and half, to
// a share of the addressees (chosen at random); the rest are held till the test's hours are over, then the variant
// with more letters opened (or links followed) goes to them. The author may choose the winner sooner.

// Campaign is a campaign with its counts.
type Campaign struct {
	ID, Title, MailboxID, Subject, Body, Status string
	SendAt, StartedAt, FinishedAt               *time.Time
	CreatedBy, AuthorName                       string
	UpdatedAt                                   time.Time
	Total, Sent, Errors, Skipped, Opened        int
	Clicked, Unsubscribed                       int
	Segments, Excluded                          []string // the segments' ids
	ABSubject, ABBody                           string   // variant B
	ABShare, ABHours                            int      // the test's share of the addressees, %, (0 — no test) and hours
	ABMetric, ABWinner                          string   // opened or clicked; a or b when chosen
}

// AB: the campaign tests variant B.
func (c Campaign) AB() bool {
	return c.ABShare > 0 && (c.ABSubject != "" || strings.TrimSpace(c.ABBody) != "")
}

// Variant is the campaign as variant v ("b" — B's subject and letter where given) goes.
func (c Campaign) Variant(v string) Campaign {
	if v == "b" {
		if c.ABSubject != "" {
			c.Subject = c.ABSubject
		}
		if strings.TrimSpace(c.ABBody) != "" {
			c.Body = c.ABBody
		}
	}
	return c
}

// ABDue is when the test of a started campaign is over.
func (c Campaign) ABDue() time.Time {
	from := time.Now()
	if c.SendAt != nil {
		from = *c.SendAt
	} else if c.StartedAt != nil {
		from = *c.StartedAt
	}
	return from.Add(time.Duration(c.ABHours) * time.Hour)
}

// Testing: the test of the started campaign goes on, the rest held.
func (c Campaign) Testing() bool {
	return c.AB() && c.ABWinner == "" && (c.Status == "sending" || c.Status == "paused")
}

// Pending: letters still to send.
func (c Campaign) Pending() int { return c.Total - c.Sent - c.Errors - c.Skipped }

// Planned: started, waiting for its time.
func (c Campaign) Planned() bool {
	return c.Status == "sending" && c.SendAt != nil && c.SendAt.After(time.Now())
}

// Editable: a draft, not started yet.
func (c Campaign) Editable() bool { return c.Status == "draft" }

const campaignCols = `c.id::text, c.title, coalesce(c.mailbox_id::text, ''), c.subject, c.body, c.status, c.send_at, c.started_at,
	c.finished_at, coalesce(c.created_by::text, ''), coalesce(u.name, ''), c.updated_at,
	count(r.id)::int, count(*) FILTER (WHERE r.status = 'sent')::int, count(*) FILTER (WHERE r.status = 'error')::int,
	count(*) FILTER (WHERE r.status = 'skipped')::int, count(r.opened_at)::int, count(r.clicked_at)::int, count(r.unsubscribed_at)::int,
	coalesce((SELECT array_agg(segment_id::text) FROM mkt_campaign_segments WHERE campaign_id = c.id AND NOT exclude), '{}'),
	coalesce((SELECT array_agg(segment_id::text) FROM mkt_campaign_segments WHERE campaign_id = c.id AND exclude), '{}'),
	c.ab_subject, c.ab_body, c.ab_share, c.ab_hours, c.ab_metric, c.ab_winner
	FROM mkt_campaigns c LEFT JOIN users u ON u.id = c.created_by LEFT JOIN mkt_recipients r ON r.campaign_id = c.id`

// Campaigns are the company's campaigns, the latest first.
func (s *Store) Campaigns(ctx context.Context) ([]Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignCols+` GROUP BY c.id, u.name ORDER BY c.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Campaign])
}

// Campaign is one of them.
func (s *Store) Campaign(ctx context.Context, id string) (Campaign, error) {
	rows, err := s.db.Query(ctx, `SELECT `+campaignCols+` WHERE c.id::text = $1 GROUP BY c.id, u.name`, id)
	if err != nil {
		return Campaign{}, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Campaign])
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return c, err
}

// mayChange: the campaign's author or an administrator.
func (s *Store) mayChange(ctx context.Context, userID string, admin bool, id string) (Campaign, error) {
	c, err := s.Campaign(ctx, id)
	if err == nil && !admin && c.CreatedBy != userID {
		err = ErrForbidden
	}
	return c, err
}

// SaveCampaign adds a campaign (c.ID "") or changes a draft one: the author's or, by an administrator, any.
func (s *Store) SaveCampaign(ctx context.Context, userID string, admin bool, c Campaign) (string, error) {
	c.Title, c.Subject = strings.TrimSpace(c.Title), strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(c.Subject))
	c.ABSubject = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(c.ABSubject))
	if c.Title == "" || len([]rune(c.Title)) > maxName || len([]rune(c.Subject)) > maxSubject || len(c.Body) > maxBody ||
		len([]rune(c.ABSubject)) > maxSubject || len(c.ABBody) > maxBody {
		return "", i18n.E("err.mkt_campaign")
	}
	if c.ABHours == 0 {
		c.ABHours = 4
	}
	if c.ABMetric == "" {
		c.ABMetric = "opened"
	}
	if c.ABShare != 0 && (c.ABShare < 10 || c.ABShare > 100) || c.ABHours < 1 || c.ABHours > 72 || c.ABMetric != "opened" && c.ABMetric != "clicked" {
		return "", i18n.E("err.mkt_ab")
	}
	var box any
	if c.MailboxID != "" {
		box = c.MailboxID
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if c.ID == "" {
		if err := tx.QueryRow(ctx, `INSERT INTO mkt_campaigns (title, mailbox_id, subject, body, send_at, created_by,
			ab_subject, ab_body, ab_share, ab_hours, ab_metric) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING id::text`, c.Title, box, c.Subject, c.Body, c.SendAt, userID,
			c.ABSubject, c.ABBody, c.ABShare, c.ABHours, c.ABMetric).Scan(&c.ID); err != nil {
			return "", err
		}
	} else {
		old, err := s.mayChange(ctx, userID, admin, c.ID)
		if err != nil {
			return "", err
		}
		if !old.Editable() {
			return "", i18n.E("err.mkt_started")
		}
		if _, err := tx.Exec(ctx, `UPDATE mkt_campaigns SET title = $2, mailbox_id = $3, subject = $4, body = $5, send_at = $6,
			ab_subject = $7, ab_body = $8, ab_share = $9, ab_hours = $10, ab_metric = $11, updated_at = now()
			WHERE id::text = $1`, c.ID, c.Title, box, c.Subject, c.Body, c.SendAt,
			c.ABSubject, c.ABBody, c.ABShare, c.ABHours, c.ABMetric); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mkt_campaign_segments WHERE campaign_id::text = $1`, c.ID); err != nil {
			return "", err
		}
	}
	for i, list := range [][]string{c.Segments, c.Excluded} {
		for _, g := range list {
			if _, err := tx.Exec(ctx, `INSERT INTO mkt_campaign_segments (campaign_id, segment_id, exclude) SELECT $1, id, $3
				FROM mkt_segments WHERE id::text = $2 ON CONFLICT (campaign_id, segment_id) DO UPDATE SET exclude = EXCLUDED.exclude`,
				c.ID, g, i == 1); err != nil {
				return "", err
			}
		}
	}
	return c.ID, tx.Commit(ctx)
}

// DeleteCampaign removes a campaign that is not being sent (a draft, finished, stopped) with its statistics.
func (s *Store) DeleteCampaign(ctx context.Context, userID string, admin bool, id string) error {
	c, err := s.mayChange(ctx, userID, admin, id)
	if err != nil {
		return err
	}
	if c.Status == "sending" || c.Status == "paused" {
		return i18n.E("err.mkt_running")
	}
	_, err = s.db.Exec(ctx, `DELETE FROM mkt_campaigns WHERE id::text = $1`, id)
	return err
}

// CopyCampaign makes a draft of the campaign's letter and segments («Копировать»), the user's.
func (s *Store) CopyCampaign(ctx context.Context, userID, id, title string) (string, error) {
	c, err := s.Campaign(ctx, id)
	if err != nil {
		return "", err
	}
	c.ID, c.Title, c.SendAt = "", title, nil
	return s.SaveCampaign(ctx, userID, false, c)
}

// Addressees are the campaign's addressees now, as the viewer sees the segments: those of its segments but those of
// the excluded ones and of the stop list, each address once.
func (s *Store) Addressees(ctx context.Context, viewer string, c Campaign) ([]Recipient, error) {
	stop, err := s.stopSet(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range c.Excluded {
		g, err := s.Segment(ctx, id)
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		list, err := s.Audience(ctx, viewer, g)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			stop[strings.ToLower(r.Email)] = true
		}
	}
	var out []Recipient
	for _, id := range c.Segments {
		g, err := s.Segment(ctx, id)
		if err == ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		list, err := s.Audience(ctx, viewer, g)
		if err != nil {
			return nil, err
		}
		for _, r := range list {
			if k := strings.ToLower(r.Email); !stop[k] {
				stop[k] = true
				out = append(out, r)
			}
		}
	}
	if len(out) > MaxAudience {
		return nil, i18n.E("err.mkt_too_many", MaxAudience)
	}
	return out, nil
}

// Start starts a draft: fixes its addressees (as its starter sees them) and gives it to the worker, from its
// planned time if any.
func (s *Store) Start(ctx context.Context, userID string, admin bool, id string) (int, error) {
	c, err := s.mayChange(ctx, userID, admin, id)
	if err != nil {
		return 0, err
	}
	if !c.Editable() {
		return 0, i18n.E("err.mkt_started")
	}
	if c.MailboxID == "" || c.Subject == "" || strings.TrimSpace(c.Body) == "" {
		return 0, i18n.E("err.mkt_incomplete")
	}
	list, err := s.Addressees(ctx, userID, c)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, i18n.E("err.mkt_nobody")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mkt_campaigns SET status = 'sending', started_at = now(), updated_at = now()
		WHERE id::text = $1 AND status = 'draft'`, id)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, i18n.E("err.mkt_started")
	}
	// the test: a share at random, A and B half and half; the rest held for the winner
	test := len(list)
	if c.AB() {
		rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
		test = max(2, (len(list)*c.ABShare+50)/100)
	}
	rows := make([][]any, len(list))
	for i, r := range list {
		var ent any
		if r.EntityID != "" {
			ent = r.EntityID
		}
		status, variant := "pending", ""
		if c.AB() {
			switch {
			case i >= test:
				status = "held"
			case i < (test+1)/2:
				variant = "a"
			default:
				variant = "b"
			}
		}
		rows[i] = []any{c.ID, r.Email, r.Name, r.Entity, ent, token(), status, variant}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"mkt_recipients"}, []string{"campaign_id", "email", "name", "entity", "entity_id", "token",
		"status", "variant"},
		pgx.CopyFromRows(rows)); err != nil {
		return 0, err
	}
	return len(list), tx.Commit(ctx)
}

// SetState pauses (paused), resumes (sending) or stops (stopped) a started campaign.
func (s *Store) SetState(ctx context.Context, userID string, admin bool, id, state string) error {
	c, err := s.mayChange(ctx, userID, admin, id)
	if err != nil {
		return err
	}
	from := map[string]string{"paused": "sending", "sending": "paused"}[state]
	if state == "stopped" && (c.Status == "sending" || c.Status == "paused") {
		from = c.Status
	}
	if from == "" || c.Status != from {
		return i18n.E("err.mkt_state")
	}
	_, err = s.db.Exec(ctx, `UPDATE mkt_campaigns SET status = $2, updated_at = now(),
		finished_at = CASE WHEN $2 = 'stopped' THEN now() END WHERE id::text = $1 AND status = $3`, id, state, from)
	return err
}

// Addressee is a recipient of a started campaign and what came of the letter.
type Addressee struct {
	ID                                   int64
	Email, Name, Entity, EntityID, Token string
	Status, Error                        string
	SentAt, OpenedAt, ClickedAt, Unsub   *time.Time
	Variant                              string // of an A/B test: a or b; "" — no test
}

const addresseeCols = `id, email, name, entity, coalesce(entity_id::text, ''), token, status, error, sent_at, opened_at, clicked_at, unsubscribed_at,
	variant`

// Recipients are the campaign's addressees of a status ("" — all; opened, clicked, unsubscribed too) by the order
// they go in, a page of them, and how many there are.
func (s *Store) Recipients(ctx context.Context, id, status string, limit, offset int) ([]Addressee, int, error) {
	cond := map[string]string{"": "true", "pending": "status IN ('pending', 'held')", "sent": "status = 'sent'", "error": "status = 'error'",
		"skipped": "status = 'skipped'", "opened": "opened_at IS NOT NULL", "clicked": "clicked_at IS NOT NULL",
		"unsubscribed": "unsubscribed_at IS NOT NULL"}[status]
	if cond == "" {
		cond = "true"
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mkt_recipients WHERE campaign_id::text = $1 AND `+cond, id).Scan(&n); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+addresseeCols+` FROM mkt_recipients WHERE campaign_id::text = $1 AND `+cond+`
		ORDER BY id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Addressee])
	return list, n, err
}

// Sender sends one letter of a campaign; an error is the recipient's (a wrong address, the server refused it).
type Sender func(ctx context.Context, c Campaign, r Addressee) error

// SendDue sends the next letters of the campaigns whose time has come, sendPerRound of each, and finishes those
// with none left; the addresses unsubscribed meanwhile are skipped. It gives the number of letters sent.
func (s *Store) SendDue(ctx context.Context, now time.Time, send Sender) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM mkt_campaigns WHERE status = 'sending' AND (send_at IS NULL OR send_at <= $1)`, now)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	if err := s.decideTests(ctx, now); err != nil {
		return 0, err
	}
	sent := 0
	for _, id := range ids {
		n, err := s.sendSome(ctx, id, send)
		sent += n
		if err != nil {
			return sent, err
		}
	}
	return sent, nil
}

func (s *Store) sendSome(ctx context.Context, id string, send Sender) (int, error) {
	c, err := s.Campaign(ctx, id)
	if err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `UPDATE mkt_recipients r SET status = 'skipped', error = 'stoplist' WHERE r.campaign_id::text = $1
		AND r.status = 'pending' AND lower(r.email) IN (SELECT email FROM mkt_stoplist)`, id); err != nil {
		return 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+addresseeCols+` FROM mkt_recipients WHERE campaign_id::text = $1 AND status = 'pending'
		ORDER BY id LIMIT $2`, id, sendPerRound)
	if err != nil {
		return 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Addressee])
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, r := range list {
		if ctx.Err() != nil {
			return sent, ctx.Err()
		}
		var st string // the campaign may have been paused or stopped meanwhile
		if err := s.db.QueryRow(ctx, `SELECT status FROM mkt_campaigns WHERE id::text = $1`, id).Scan(&st); err != nil || st != "sending" {
			return sent, err
		}
		status, msg := "sent", ""
		v := r.Variant
		if v == "" {
			v = c.ABWinner
		}
		if err := send(ctx, c.Variant(v), r); err != nil {
			status, msg = "error", err.Error()
		} else {
			sent++
		}
		if _, err := s.db.Exec(ctx, `UPDATE mkt_recipients SET status = $2, error = $3, sent_at = now() WHERE id = $1`, r.ID, status, msg); err != nil {
			return sent, err
		}
		if err := s.toTimeline(ctx, c, r); err != nil {
			return sent, err
		}
	}
	_, err = s.db.Exec(ctx, `UPDATE mkt_campaigns c SET status = 'done', finished_at = now(), updated_at = now() WHERE c.id::text = $1
		AND c.status = 'sending' AND NOT EXISTS (SELECT 1 FROM mkt_recipients WHERE campaign_id = c.id AND status IN ('pending', 'held'))`, id)
	return sent, err
}

// toTimeline writes the letter to the timeline of the addressee's CRM card, from the campaign's author (a timeline
// line has one); the addresses of a list have no card.
func (s *Store) toTimeline(ctx context.Context, c Campaign, r Addressee) error {
	if s.CRM == nil || r.EntityID == "" || r.Entity == "list" || r.Entity == "" || c.CreatedBy == "" {
		return nil
	}
	return s.CRM.AddEvent(ctx, r.Entity, r.EntityID, c.CreatedBy, "mailing", strconv.FormatInt(r.ID, 10))
}

// Opened counts the letter as opened (its picture loaded); a test letter's token is no one's.
func (s *Store) Opened(ctx context.Context, tok string) error {
	_, err := s.db.Exec(ctx, `UPDATE mkt_recipients SET opened_at = now() WHERE token = $1 AND opened_at IS NULL`, tok)
	return err
}

// Clicked counts a link of the letter followed (and so the letter opened); it gives the campaign, whose letter the
// link must be of.
func (s *Store) Clicked(ctx context.Context, tok string) (Campaign, Addressee, error) {
	a, _, err := s.ByToken(ctx, tok)
	if err != nil {
		return Campaign{}, a, err
	}
	var id string
	if err := s.db.QueryRow(ctx, `UPDATE mkt_recipients SET clicked_at = coalesce(clicked_at, now()), opened_at = coalesce(opened_at, now())
		WHERE id = $1 RETURNING campaign_id::text`, a.ID).Scan(&id); err != nil {
		return Campaign{}, a, err
	}
	c, err := s.Campaign(ctx, id)
	return c, a, err
}

// ABResult is how a variant of the test did.
type ABResult struct {
	Sent, Opened, Clicked int
}

// ABResults are the test's results by variant (a, b).
func (s *Store) ABResults(ctx context.Context, id string) (map[string]ABResult, error) {
	rows, err := s.db.Query(ctx, `SELECT variant, count(*) FILTER (WHERE status = 'sent')::int, count(opened_at)::int, count(clicked_at)::int
		FROM mkt_recipients WHERE campaign_id::text = $1 AND variant <> '' GROUP BY variant`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ABResult{"a": {}, "b": {}}
	for rows.Next() {
		var v string
		var x ABResult
		if err := rows.Scan(&v, &x.Sent, &x.Opened, &x.Clicked); err != nil {
			return nil, err
		}
		out[v] = x
	}
	return out, rows.Err()
}

// ChooseWinner ends the test of a started campaign with variant v (a, b): the held addressees get it.
func (s *Store) ChooseWinner(ctx context.Context, userID string, admin bool, id, v string) error {
	c, err := s.mayChange(ctx, userID, admin, id)
	if err != nil {
		return err
	}
	if !c.Testing() || v != "a" && v != "b" {
		return i18n.E("err.mkt_state")
	}
	return s.setWinner(ctx, id, v)
}

func (s *Store) setWinner(ctx context.Context, id, v string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mkt_campaigns SET ab_winner = $2, updated_at = now() WHERE id::text = $1 AND ab_winner = ''`, id, v)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE mkt_recipients SET status = 'pending' WHERE campaign_id::text = $1 AND status = 'held'`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// decideTests ends the tests whose hours are over: the variant with more letters opened (links followed) wins, A
// on a tie.
func (s *Store) decideTests(ctx context.Context, now time.Time) error {
	rows, err := s.db.Query(ctx, `SELECT `+campaignCols+` WHERE c.status = 'sending' AND c.ab_share > 0 AND c.ab_winner = ''
		GROUP BY c.id, u.name`)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Campaign])
	if err != nil {
		return err
	}
	for _, c := range list {
		if !c.AB() || c.ABDue().After(now) {
			continue
		}
		res, err := s.ABResults(ctx, c.ID)
		if err != nil {
			return err
		}
		v := "a"
		if c.ABMetric == "clicked" && res["b"].Clicked > res["a"].Clicked || c.ABMetric != "clicked" && res["b"].Opened > res["a"].Opened {
			v = "b"
		}
		if err := s.setWinner(ctx, c.ID, v); err != nil {
			return err
		}
	}
	return nil
}
