package crm

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Обязательные поля»: an own field of the cards may be required always or only at some stages of deals
// and leads. The card's form and moving the card to a stage (kanban, stage bar, group action) are refused while such
// a field is empty; robots, REST, CSV, mail and forms fill cards as they can.

// RequiredError: the card lacks the Fields required where it goes.
type RequiredError struct{ Fields []tasks.FieldValue }

func (e *RequiredError) Error() string { return "crm: required fields are empty" }

// I18n is the message for people: «Заполните обязательные поля: …».
func (e *RequiredError) I18n() *i18n.Error {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Name
	}
	return i18n.E("err.crm_required", strings.Join(names, ", "))
}

// CardStage is the stage a card of the entity is at, or comes to: the given one, else the card's (id), else the
// first stage of a new one (of the pipeline, for deals). "" — the entity has no stages.
func (s *Store) CardStage(ctx context.Context, entity, id, pipelineID, stage string) (string, error) {
	if stage != "" || (entity != "deal" && entity != "lead") {
		return stage, nil
	}
	var err error
	switch {
	case id != "" && entity == "deal":
		err = s.db.QueryRow(ctx, `SELECT stage_id::text FROM crm_deals WHERE id::text = $1`, id).Scan(&stage)
	case id != "":
		err = s.db.QueryRow(ctx, `SELECT stage_id::text FROM crm_leads WHERE id::text = $1`, id).Scan(&stage)
	case entity == "deal":
		err = s.db.QueryRow(ctx, `SELECT id::text FROM crm_stages WHERE pipeline_id::text = $1 ORDER BY sort LIMIT 1`, pipelineID).Scan(&stage)
	default:
		err = s.db.QueryRow(ctx, `SELECT id::text FROM crm_stages WHERE entity = 'lead' ORDER BY sort LIMIT 1`).Scan(&stage)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil // the card or pipeline is checked by whoever saves it
	}
	return stage, err
}

// CheckRequired: the card (id; "" — a new one) with the values about to be written (field id → value; absent ones
// keep what the card has) at the stage — a *RequiredError names the required fields left empty.
func (s *Store) CheckRequired(ctx context.Context, entity, id, stage string, values map[string]string) error {
	set, ok := tasks.CRMFieldSets[entity]
	if !ok {
		return nil
	}
	ts := s.Tasks
	if ts == nil {
		ts = tasks.NewStore(s.db)
	}
	var list []tasks.FieldValue
	if id != "" {
		var err error
		if list, err = ts.FieldValuesOf(ctx, set, id); err != nil {
			return err
		}
	} else {
		defs, err := ts.FieldsOf(ctx, set)
		if err != nil {
			return err
		}
		for _, f := range defs {
			list = append(list, tasks.FieldValue{Field: f})
		}
	}
	var missing []tasks.FieldValue
	for _, f := range list {
		if v, ok := values[f.ID]; ok {
			f.Value = v
		}
		if f.RequiredAt(stage) && strings.TrimSpace(f.Value) == "" {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return &RequiredError{Fields: missing}
	}
	return nil
}
