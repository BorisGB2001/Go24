package crm

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Settings of pipelines and stages: own deal pipelines, stages added, renamed, recolored, moved and
// deleted (their cards go to another stage). Working stages come first, then the success stage, then the failure
// ones. A built-in stage keeps its code (the API and the webhooks give it) when renamed: the name wins.

// newPipelineStages are the stages of a new pipeline, as a new the portal pipeline has them.
var newPipelineStages = []struct{ code, color, semantic string }{
	{"new", "39A8EF", "process"}, {"preparation", "2FC6F6", "process"}, {"prepayment", "55D0E0", "process"},
	{"executing", "47E4C2", "process"}, {"final_invoice", "FFA900", "process"},
	{"won", "7BD500", "success"}, {"lose", "FF5752", "failure"},
}

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func stageName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return "", i18n.E("err.crm_stage_name")
	}
	return name, nil
}

func stageColor(c string) (string, error) {
	c = strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(c), "#"))
	if c == "" {
		return tasks.StageColors[0], nil
	}
	if len(c) != 6 || strings.Trim(c, "0123456789ABCDEF") != "" {
		return "", i18n.E("err.stage_color")
	}
	return c, nil
}

// newTypeStages are the stages of a pipeline of a smart process makes them.
var newTypeStages = []struct{ code, color, semantic string }{
	{"sp_new", "39A8EF", "process"}, {"sp_preparation", "2FC6F6", "process"}, {"sp_client", "FFA900", "process"},
	{"sp_success", "7BD500", "success"}, {"sp_fail", "FF5752", "failure"},
}

// AddPipeline creates a deal pipeline with the stages of a new one.
func (s *Store) AddPipeline(ctx context.Context, name string) (string, error) {
	return s.AddPipelineOf(ctx, "", name)
}

// AddPipelineOf creates a pipeline of deals (typeID "") or of a smart process.
func (s *Store) AddPipelineOf(ctx context.Context, typeID, name string) (string, error) {
	name, err := stageName(name)
	if err != nil {
		return "", err
	}
	var id string
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		id, err = addPipeline(ctx, tx, typeID, "", name)
		return err
	})
	return id, err
}

// addPipeline adds a pipeline (a built-in one by code, or named) with the stages of a new one of its kind.
func addPipeline(ctx context.Context, tx pgx.Tx, typeID, code, name string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO crm_pipelines (type_id, code, name, sort)
		SELECT t.id, nullif($2, ''), $3, (SELECT coalesce(max(sort), 0) + 10 FROM crm_pipelines)
		FROM (SELECT NULL::uuid AS id WHERE $1 = '' UNION ALL SELECT id FROM crm_types WHERE id::text = $1) t
		RETURNING id`, typeID, code, name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	stages := newPipelineStages
	if typeID != "" {
		stages = newTypeStages
	}
	for i, st := range stages {
		if _, err := tx.Exec(ctx, `INSERT INTO crm_stages (entity, pipeline_id, code, color, semantic, sort) VALUES ('deal', $1, $2, $3, $4, $5)`,
			id, st.code, st.color, st.semantic, (i+1)*10); err != nil {
			return "", err
		}
	}
	return id, nil
}

// RenamePipeline names a pipeline (a built-in one too).
func (s *Store) RenamePipeline(ctx context.Context, id, name string) error {
	name, err := stageName(name)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_pipelines SET name = $2 WHERE id::text = $1`, id, name)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// DeletePipeline removes a pipeline without deals; the last one stays.
func (s *Store) DeletePipeline(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var others, deals int // the last pipeline of deals, or of a smart process, stays
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM crm_pipelines o, crm_pipelines p WHERE p.id::text = $1 AND o.id <> p.id
			AND o.type_id IS NOT DISTINCT FROM p.type_id),
			(SELECT count(*) FROM crm_deals WHERE pipeline_id::text = $1)`, id).Scan(&others, &deals); err != nil {
			return err
		}
		if others == 0 {
			return i18n.E("err.crm_pipeline_last")
		}
		if deals > 0 {
			return i18n.E("err.crm_pipeline_not_empty", deals)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM crm_pipelines WHERE id::text = $1`, id)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
		return err
	})
}

// stageRow is a stage with where it belongs, for the settings.
type stageRow struct {
	Stage
	entity, pipeline string
}

func stageOf(ctx context.Context, tx pgx.Tx, id string) (stageRow, error) {
	var st stageRow
	err := tx.QueryRow(ctx, `SELECT `+stageCols+`, st.entity, coalesce(st.pipeline_id::text, '') FROM crm_stages st WHERE st.id::text = $1 FOR UPDATE`, id).
		Scan(&st.ID, &st.Code, &st.Name, &st.Color, &st.Semantic, &st.entity, &st.pipeline)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, i18n.E("err.crm_stage")
	}
	return st, err
}

// siblings are the ids of the stages of the same list (leads, or a pipeline) in their order.
func siblings(ctx context.Context, tx pgx.Tx, entity, pipeline string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM crm_stages WHERE entity = $1 AND coalesce(pipeline_id::text, '') = $2
		ORDER BY CASE semantic WHEN 'process' THEN 0 WHEN 'success' THEN 1 ELSE 2 END, sort FOR UPDATE`, entity, pipeline)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// resort numbers the stages in the given order.
func resort(ctx context.Context, tx pgx.Tx, ids []string) error {
	for i, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE crm_stages SET sort = $2 WHERE id::text = $1`, id, (i+1)*10); err != nil {
			return err
		}
	}
	return nil
}

// AddStage adds a working stage (after the working ones) or a failure stage (at the end) to the leads (entity
// "lead") or to a deal pipeline.
func (s *Store) AddStage(ctx context.Context, entity, pipelineID, name, color, semantic string) error {
	name, err := stageName(name)
	if err != nil {
		return err
	}
	if color, err = stageColor(color); err != nil {
		return err
	}
	if semantic != "failure" {
		semantic = "process"
	}
	if entity == "lead" {
		pipelineID = ""
	} else if entity != "deal" || pipelineID == "" {
		return i18n.E("err.bad_form")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO crm_stages (entity, pipeline_id, name, color, semantic, sort)
			SELECT $1, p.id, $3, $4, $5, 100000 FROM (SELECT NULL::uuid AS id WHERE $2 = '' UNION ALL SELECT id FROM crm_pipelines WHERE id::text = $2) p
			RETURNING id::text`, entity, pipelineID, name, color, semantic).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		ids, err := siblings(ctx, tx, entity, pipelineID)
		if err != nil {
			return err
		}
		return resort(ctx, tx, ids)
	})
}

// UpdateStage renames and recolors a stage.
func (s *Store) UpdateStage(ctx context.Context, id, name, color string) error {
	name, err := stageName(name)
	if err != nil {
		return err
	}
	if color, err = stageColor(color); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_stages SET name = $2, color = $3 WHERE id::text = $1`, id, name, color)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.crm_stage")
	}
	return err
}

// ShiftStage moves a stage one place left (dir < 0) or right among the stages of its kind (working or failure).
func (s *Store) ShiftStage(ctx context.Context, id string, dir int) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		st, err := stageOf(ctx, tx, id)
		if err != nil {
			return err
		}
		ids, err := siblings(ctx, tx, st.entity, st.pipeline)
		if err != nil {
			return err
		}
		sem := map[string]string{}
		rows, err := tx.Query(ctx, `SELECT id::text, semantic FROM crm_stages WHERE id::text = ANY($1)`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return err
			}
			sem[k] = v
		}
		if err := rows.Err(); err != nil {
			return err
		}
		i := -1
		for k, x := range ids {
			if x == id {
				i = k
			}
		}
		j := i + 1
		if dir < 0 {
			j = i - 1
		}
		if i < 0 || j < 0 || j >= len(ids) || sem[ids[j]] != st.Semantic {
			return nil
		}
		ids[i], ids[j] = ids[j], ids[i]
		return resort(ctx, tx, ids)
	})
}

// DeleteStage removes a stage; its cards go to the stage moveTo of the same list. The success stage, the last
// working stage and the last failure stage stay.
func (s *Store) DeleteStage(ctx context.Context, userID, id, moveTo string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		st, err := stageOf(ctx, tx, id)
		if err != nil {
			return err
		}
		if st.Semantic == "success" {
			return i18n.E("err.crm_stage_success")
		}
		var same, used int
		table := "crm_deals"
		if st.entity == "lead" {
			table = "crm_leads"
		}
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM crm_stages WHERE entity = $1 AND coalesce(pipeline_id::text, '') = $2
			AND semantic = $3 AND id::text <> $4), (SELECT count(*) FROM `+table+` WHERE stage_id::text = $4)`,
			st.entity, st.pipeline, st.Semantic, id).Scan(&same, &used); err != nil {
			return err
		}
		if same == 0 {
			return i18n.E("err.crm_stage_last")
		}
		if used > 0 {
			to, err := stageOf(ctx, tx, moveTo)
			if err != nil || to.ID == id || to.entity != st.entity || to.pipeline != st.pipeline {
				return i18n.E("err.crm_stage_move")
			}
			if st.entity == "lead" && to.Semantic == "success" {
				return i18n.E("err.crm_convert_first")
			}
			// the cards move as if moved by hand: a deal is closed or reopened, and the timeline tells
			q := `UPDATE crm_leads SET stage_id = $2, updated_at = now() WHERE stage_id::text = $1 AND $3::bool IS NOT NULL RETURNING id::text`
			if table == "crm_deals" {
				q = `UPDATE crm_deals SET stage_id = $2, updated_at = now(), closed_at = CASE WHEN $3 THEN coalesce(closed_at, now()) END
					WHERE stage_id::text = $1 RETURNING id::text`
			}
			rows, err := tx.Query(ctx, q, id, to.ID, to.Closed())
			if err != nil {
				return err
			}
			moved, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			for _, card := range moved {
				if err := s.event(ctx, tx, st.entity, card, userID, "stage", stageText(st.Stage)+" → "+stageText(to.Stage)); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM crm_stages WHERE id::text = $1`, id)
		return err
	})
}
