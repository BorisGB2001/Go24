package crm

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Смарт-процессы»: the administrator's own kinds of cards with their own pipelines and stages.
// Their cards are deals of those pipelines (Deal.TypeID): the kanban, the list, the card, deeds, goods, robots,
// triggers and the rights of deals serve them.

// Type is a smart process; Items — how many cards it has.
type Type struct {
	ID, Name, Color string
	Items           int
}

const typeSQL = `SELECT t.id::text, t.name, t.color,
		(SELECT count(*) FROM crm_deals d JOIN crm_pipelines p ON p.id = d.pipeline_id WHERE p.type_id = t.id)::int
	FROM crm_types t`

func scanTypes(rows pgx.Rows, err error) ([]Type, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Type])
}

// Types are the smart processes in their order.
func (s *Store) Types(ctx context.Context) ([]Type, error) {
	return scanTypes(s.db.Query(ctx, typeSQL+` ORDER BY t.sort, t.created_at`))
}

func (s *Store) Type(ctx context.Context, id string) (Type, error) {
	list, err := scanTypes(s.db.Query(ctx, typeSQL+` WHERE t.id::text = $1`, id))
	if err != nil {
		return Type{}, err
	}
	if len(list) == 0 {
		return Type{}, ErrNotFound
	}
	return list[0], nil
}

func typeName(name string) (string, error) {
	name, err := stageName(name)
	if err != nil {
		return "", i18n.E("err.crm_type_name")
	}
	return name, nil
}

// AddType creates a smart process with its pipeline «Общая» and the stages of a new one.
func (s *Store) AddType(ctx context.Context, name, color string) (string, error) {
	name, err := typeName(name)
	if err != nil {
		return "", err
	}
	if color, err = stageColor(color); err != nil {
		return "", err
	}
	var id string
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO crm_types (name, color, sort) VALUES ($1, $2, (SELECT coalesce(max(sort), 0) + 10 FROM crm_types))
			RETURNING id::text`, name, color).Scan(&id); err != nil {
			return err
		}
		_, err := addPipeline(ctx, tx, id, "default", "")
		return err
	})
	return id, err
}

// UpdateType renames and recolors a smart process.
func (s *Store) UpdateType(ctx context.Context, id, name, color string) error {
	name, err := typeName(name)
	if err != nil {
		return err
	}
	if color, err = stageColor(color); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_types SET name = $2, color = $3 WHERE id::text = $1`, id, name, color)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// DeleteType removes a smart process without cards, with its pipelines, stages, robots, triggers and «Связи».
func (s *Store) DeleteType(ctx context.Context, id string) error {
	t, err := s.Type(ctx, id)
	if err != nil {
		return err
	}
	if t.Items > 0 {
		return i18n.E("err.crm_type_not_empty", t.Items)
	}
	_, err = s.db.Exec(ctx, `WITH r AS (DELETE FROM crm_type_relations WHERE parent = $1) DELETE FROM crm_types WHERE id::text = $1`, id)
	return err
}

// PipelineType is the smart process of a pipeline ("" — a deal pipeline).
func (s *Store) PipelineType(ctx context.Context, pipelineID string) (string, error) {
	var t string
	err := s.db.QueryRow(ctx, `SELECT coalesce(type_id::text, '') FROM crm_pipelines WHERE id::text = $1`, pipelineID).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}
