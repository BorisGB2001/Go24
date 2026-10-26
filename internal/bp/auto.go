package bp

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// The autostart of the templates: «при добавлении» and «при изменении» of a CRM card (of a document of
// «Общий диск» — AutoStartFiles). The worker reads the
// cards' timeline — a card added by a form, REST, a CSV, mail or a robot leaves «created» there, an edit «changed»,
// a move «stage» or «pipeline» — so every way a card changes starts the processes alike.

// cardTables: the table of each kind of card, so a card deleted since its event starts nothing.
var cardTables = map[string]string{"deal": "crm_deals", "lead": "crm_leads", "contact": "crm_contacts", "company": "crm_companies"}

// cardType is the SQL of the smart process a card ($2 of kind $1) belongs to: NULL for deals of the deal pipelines and
// for other cards.
const cardType = `(CASE WHEN $1 = 'deal' THEN (SELECT p.type_id FROM crm_deals d JOIN crm_pipelines p ON p.id = d.pipeline_id
	WHERE d.id::text = $2) END)`

// CardTemplates are the active templates of the card's kind (a smart process's card — of its type).
func (s *Store) CardTemplates(ctx context.Context, entity, id string) ([]Template, error) {
	rows, err := s.db.Query(ctx, templateSQL+` WHERE active AND entity = $1 AND type_id IS NOT DISTINCT FROM `+cardType+`
		ORDER BY lower(name)`, entity, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanTemplate)
}

// autoBatch is how many events one call reads; the rest wait for the next one.
const autoBatch = 500

// autoLag: an event of a transaction still open may get an id before one already read — a few seconds' lag lets it
// commit first.
var autoLag = 3.0

// AutoStart starts the templates set to start by themselves on the cards added or changed since the last call (the
// worker calls it). A template does not start on a card by an event made while a process of it ran there: so a process
// changing its own card (a stage or an «Изменить карточку» step) does not start itself again, even when it is done by
// the time the worker reads the event, and one save leaving several events starts it once.
func (s *Store) AutoStart(ctx context.Context) []error {
	type event struct {
		id                   int64
		entity, card, author string
		created              bool
	}
	var last int64
	if err := s.db.QueryRow(ctx, `SELECT last_id FROM bp_cursor`).Scan(&last); err != nil {
		return []error{err}
	}
	rows, err := s.db.Query(ctx, `SELECT id, entity, entity_id::text, author_id::text, kind = 'created' FROM crm_timeline
		WHERE id > $1 AND kind IN ('created', 'changed', 'stage', 'pipeline') AND created_at <= now() - make_interval(secs => $3)
		ORDER BY id LIMIT $2`, last, autoBatch, autoLag)
	if err != nil {
		return []error{err}
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (event, error) {
		var e event
		return e, r.Scan(&e.id, &e.entity, &e.card, &e.author, &e.created)
	})
	if err != nil || len(events) == 0 {
		return errs(err)
	}
	// the batch is this call's: another worker reading the same events finds the cursor moved
	tag, err := s.db.Exec(ctx, `UPDATE bp_cursor SET last_id = $2 WHERE last_id = $1`, last, events[len(events)-1].id)
	if err != nil || tag.RowsAffected() == 0 {
		return errs(err)
	}
	var out []error
	started := map[[3]string]bool{} // one save leaves «changed» and «stage»: a process done at once still starts once
	for _, e := range events {
		table, ok := cardTables[e.entity]
		if !ok {
			continue
		}
		rows, err := s.db.Query(ctx, templateSQL+` t WHERE active AND entity = $1 AND type_id IS NOT DISTINCT FROM `+cardType+`
			AND (auto_create AND $3 OR auto_change AND NOT $3)
			AND EXISTS (SELECT 1 FROM `+table+` c WHERE c.id::text = $2)
			AND NOT EXISTS (SELECT 1 FROM bp_instances i WHERE i.template_id = t.id AND i.entity_id::text = $2
				AND (i.status = 'running' OR i.finished_at >= (SELECT created_at FROM crm_timeline WHERE id = $4)))
			ORDER BY lower(name)`, e.entity, e.card, e.created, e.id)
		if err != nil {
			out = append(out, err)
			continue
		}
		list, err := pgx.CollectRows(rows, scanTemplate)
		if err != nil {
			out = append(out, err)
			continue
		}
		for _, t := range list {
			key := [3]string{t.ID, e.card, strconv.FormatBool(e.created)}
			if started[key] {
				continue
			}
			started[key] = true
			if _, err := s.start(ctx, e.author, t, e.card, nil, true, ""); err != nil {
				out = append(out, err)
			}
		}
	}
	return out
}

// AutoStartFiles starts the templates of files set to start by themselves on the documents added to «Общий диск» or
// changed there since the last call (the worker calls it), alike: not on an event made while a process of the template
// ran on the document, and once on one document by one batch.
func (s *Store) AutoStartFiles(ctx context.Context) []error {
	type event struct {
		id           int64
		file, author string
		created      bool
	}
	var last int64
	if err := s.db.QueryRow(ctx, `SELECT file_id FROM bp_cursor`).Scan(&last); err != nil {
		return []error{err}
	}
	rows, err := s.db.Query(ctx, `SELECT id, file_id::text, coalesce(author_id::text, ''), created FROM drive_events
		WHERE id > $1 AND created_at <= now() - make_interval(secs => $3) ORDER BY id LIMIT $2`, last, autoBatch, autoLag)
	if err != nil {
		return []error{err}
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (event, error) {
		var e event
		return e, r.Scan(&e.id, &e.file, &e.author, &e.created)
	})
	if err != nil || len(events) == 0 {
		return errs(err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE bp_cursor SET file_id = $2 WHERE file_id = $1`, last, events[len(events)-1].id)
	if err != nil || tag.RowsAffected() == 0 {
		return errs(err)
	}
	var out []error
	started := map[[2]string]bool{} // a file uploaded and at once changed starts a template set to both once
	for _, e := range events {
		if e.author == "" {
			continue
		}
		rows, err := s.db.Query(ctx, templateSQL+` t WHERE active AND entity = $1 AND (auto_create AND $3 OR auto_change AND NOT $3)
			AND EXISTS (SELECT 1 FROM files f WHERE f.id::text = $2 AND f.drive = 'company' AND f.deleted_at IS NULL)
			AND NOT EXISTS (SELECT 1 FROM bp_instances i WHERE i.template_id = t.id AND i.entity_id::text = $2
				AND (i.status = 'running' OR i.finished_at >= (SELECT created_at FROM drive_events WHERE id = $4)))
			ORDER BY lower(name)`, EntityFile, e.file, e.created, e.id)
		if err != nil {
			out = append(out, err)
			continue
		}
		list, err := pgx.CollectRows(rows, scanTemplate)
		if err != nil {
			out = append(out, err)
			continue
		}
		for _, t := range list {
			key := [2]string{t.ID, e.file}
			if started[key] {
				continue
			}
			started[key] = true
			if _, err := s.start(ctx, e.author, t, e.file, nil, true, ""); err != nil {
				out = append(out, err)
			}
		}
	}
	return out
}

func errs(err error) []error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return []error{err}
}
