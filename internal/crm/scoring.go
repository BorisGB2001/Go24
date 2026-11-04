package crm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Скоринг сделок»: a pipeline of deals learns from its closed deals (won — the stage «успех», lost —
// «провал») how likely an open deal is to be won. The model is naive Bayes over what a deal is like — its source, type,
// responsible, amount, client, whether the client has bought before, its deeds and messages, its closing date — and is
// learned again daily by the go24 worker; the probability is counted when a deal is shown, so it follows its changes.

// ScoreFeatures are what the model looks at, in the order «Почему так» names them.
var ScoreFeatures = []string{"origin", "deal_type", "responsible", "amount", "client", "returning", "activities", "comms", "close_date"}

// MinScoreDeals is how many closed deals a pipeline needs to learn from, of them at least MinScoreEach won and lost.
const (
	MinScoreDeals = 20
	MinScoreEach  = 3
	maxScoreDeals = 5000 // the latest closed deals it learns from
)

// ScoreModel is a pipeline's model: the closed deals and, for every feature, its values in the won and the lost ones.
type ScoreModel struct {
	Won, Lost int
	Bounds    []float64                    // the quartiles of the amounts (in the base currency) of the closed deals
	Counts    map[string]map[string][2]int // feature → value → [won, lost]
}

// Scoring is how a pipeline of deals is scored («Настройка скоринга»).
type Scoring struct {
	PipelineID, Code, Name string
	Enabled                bool
	Samples, Won           int     // closed deals of the last training, of them won
	Accuracy               float64 // 0–1
	TrainedAt              *time.Time
	Closed, ClosedWon      int // closed deals now
}

// Ready: the pipeline has enough closed deals to learn from.
func (s Scoring) Ready() bool {
	return s.Closed >= MinScoreDeals && s.ClosedWon >= MinScoreEach && s.Closed-s.ClosedWon >= MinScoreEach
}

// AccuracyPercent is the accuracy in percent, rounded.
func (s Scoring) AccuracyPercent() int { return int(math.Round(s.Accuracy * 100)) }

// Score is the probability of an open deal to be won and why.
type Score struct {
	Percent int // 1–99
	Factors []ScoreFactor
}

// ScoreFactor is what raises (Up) or lowers the probability of a deal.
type ScoreFactor struct {
	Feature, Value string
	Dict           Dict   // origin, deal_type: the item of the dictionary
	Name           string // responsible: the employee
	Up             bool
	weight         float64
}

// scoreDeal is a deal as the model sees it.
type scoreDeal struct {
	id, pipeline, semantic string
	amount                 float64
	values                 map[string]string
	dicts                  map[string]Dict
	responsible            string
}

// scoreSQL takes what the model looks at for the deals of the condition (with $1, $2 …).
const scoreSQL = `SELECT d.id::text, d.pipeline_id::text, st.semantic, coalesce(d.source_id::text, ''), coalesce(js.code, ''), coalesce(js.name, ''),
		coalesce(d.deal_type_id::text, ''), coalesce(jt.code, ''), coalesce(jt.name, ''), d.responsible_id::text, r.name,
		(d.amount * coalesce((SELECT cr.rate FROM crm_currencies cr WHERE cr.code = d.currency), 1))::float8,
		CASE WHEN d.company_id IS NOT NULL THEN 'company' WHEN d.contact_id IS NOT NULL THEN 'contact' ELSE 'none' END,
		EXISTS (SELECT 1 FROM crm_deals o JOIN crm_stages os ON os.id = o.stage_id WHERE os.semantic = 'success' AND o.id <> d.id
			AND o.closed_at < d.created_at AND (o.company_id = d.company_id OR o.contact_id = d.contact_id)),
		(SELECT count(*) FROM crm_activities a WHERE a.entity = 'deal' AND a.entity_id = d.id)::int,
		(SELECT count(*) FROM crm_timeline t WHERE t.entity = 'deal' AND t.entity_id = d.id
			AND t.kind IN ('comment', 'mail', 'call', 'sms', 'openline'))::int,
		d.close_date IS NOT NULL
	FROM crm_deals d JOIN crm_pipelines p ON p.id = d.pipeline_id AND p.type_id IS NULL JOIN crm_stages st ON st.id = d.stage_id
	JOIN users r ON r.id = d.responsible_id LEFT JOIN crm_dict js ON js.id = d.source_id LEFT JOIN crm_dict jt ON jt.id = d.deal_type_id`

func (s *Store) scoreDeals(ctx context.Context, where string, args ...any) ([]scoreDeal, error) {
	rows, err := s.db.Query(ctx, scoreSQL+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (scoreDeal, error) {
		var d scoreDeal
		var src, typ Dict
		var client string
		var returning, closeDate bool
		var acts, comms int
		var responsible string
		if err := r.Scan(&d.id, &d.pipeline, &d.semantic, &src.ID, &src.Code, &src.Name, &typ.ID, &typ.Code, &typ.Name,
			&responsible, &d.responsible, &d.amount, &client, &returning, &acts, &comms, &closeDate); err != nil {
			return d, err
		}
		d.values = map[string]string{"origin": orNone(src.ID), "deal_type": orNone(typ.ID), "responsible": responsible,
			"client": client, "returning": yesNo(returning), "activities": countBucket(acts), "comms": countBucket(comms),
			"close_date": yesNo(closeDate)}
		d.dicts = map[string]Dict{"origin": src, "deal_type": typ}
		return d, nil
	})
}

// countBucket groups a count of deeds or messages.
func countBucket(n int) string {
	switch {
	case n == 0:
		return "0"
	case n <= 2:
		return "1-2"
	case n <= 5:
		return "3-5"
	}
	return "6+"
}

// orNone is the value of a dictionary feature: the item, or "none" without one.
func orNone(id string) string {
	if id == "" {
		return "none"
	}
	return id
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// amountBucket is the quartile of an amount among the closed deals; no amount — "zero".
func amountBucket(amount float64, bounds []float64) string {
	if amount <= 0 {
		return "zero"
	}
	return []string{"q1", "q2", "q3", "q4"}[sort.SearchFloat64s(bounds, amount)]
}

// quartiles are the bounds of the four quarters of the positive amounts.
func quartiles(amounts []float64) []float64 {
	var pos []float64
	for _, a := range amounts {
		if a > 0 {
			pos = append(pos, a)
		}
	}
	if len(pos) == 0 {
		return []float64{0, 0, 0}
	}
	sort.Float64s(pos)
	q := func(p float64) float64 { return pos[int(p*float64(len(pos)-1))] }
	return []float64{q(0.25), q(0.5), q(0.75)}
}

// train learns a model from closed deals (won: true).
func train(values []map[string]string, won []bool) *ScoreModel {
	m := &ScoreModel{Counts: map[string]map[string][2]int{}}
	for i, v := range values {
		k := 1
		if won[i] {
			m.Won++
			k = 0
		} else {
			m.Lost++
		}
		for _, f := range ScoreFeatures {
			if m.Counts[f] == nil {
				m.Counts[f] = map[string][2]int{}
			}
			c := m.Counts[f][v[f]]
			c[k]++
			m.Counts[f][v[f]] = c
		}
	}
	return m
}

// weights are the log-odds of a deal to be won: the prior and the part of each feature (Laplace-smoothed); without
// (a closed deal of the model itself, as v, won) the deal is taken out of the counts.
func (m *ScoreModel) weights(v map[string]string, without *bool) (float64, map[string]float64) {
	won, lost := float64(m.Won), float64(m.Lost)
	var self [2]float64
	if without != nil {
		if *without {
			self[0] = 1
		} else {
			self[1] = 1
		}
		won, lost = won-self[0], lost-self[1]
	}
	prior := math.Log((won + 1) / (lost + 1))
	parts := make(map[string]float64, len(ScoreFeatures))
	for _, f := range ScoreFeatures {
		k := float64(len(m.Counts[f]) + 1) // the values seen and an unseen one
		c := m.Counts[f][v[f]]
		cw, cl := float64(c[0])-self[0], float64(c[1])-self[1]
		parts[f] = math.Log((cw+1)/(won+k)) - math.Log((cl+1)/(lost+k))
	}
	return prior, parts
}

// probability of a deal (as v) to be won, 0–1.
func (m *ScoreModel) probability(v map[string]string, without *bool) (float64, map[string]float64) {
	logit, parts := m.weights(v, without)
	for _, w := range parts {
		logit += w
	}
	return 1 / (1 + math.Exp(-logit)), parts
}

// accuracy is the share of the closed deals the model guesses right, each counted without itself (leave-one-out).
func (m *ScoreModel) accuracy(values []map[string]string, won []bool) float64 {
	if len(values) == 0 {
		return 0
	}
	right := 0
	for i, v := range values {
		p, _ := m.probability(v, &won[i])
		if (p >= 0.5) == won[i] {
			right++
		}
	}
	return float64(right) / float64(len(values))
}

// fill sets the values of a deal from what scoreSQL took; the amount is bucketed by the model's bounds.
func (d *scoreDeal) fill(bounds []float64) {
	d.values["amount"] = amountBucket(d.amount, bounds)
}

// TrainScoring learns the model of a pipeline of deals again; with too few closed deals the model is dropped.
func (s *Store) TrainScoring(ctx context.Context, pipelineID string) error {
	deals, err := s.scoreDeals(ctx, `d.pipeline_id::text = $1 AND st.semantic IN ('success', 'failure')
		ORDER BY d.closed_at DESC NULLS LAST LIMIT $2`, pipelineID, maxScoreDeals)
	if err != nil {
		return err
	}
	amounts := make([]float64, len(deals))
	for i, d := range deals {
		amounts[i] = d.amount
	}
	bounds := quartiles(amounts)
	values, won := make([]map[string]string, len(deals)), make([]bool, len(deals))
	nWon := 0
	for i := range deals {
		deals[i].fill(bounds)
		values[i], won[i] = deals[i].values, deals[i].semantic == "success"
		if won[i] {
			nWon++
		}
	}
	var model []byte
	acc := 0.0
	if len(deals) >= MinScoreDeals && nWon >= MinScoreEach && len(deals)-nWon >= MinScoreEach {
		m := train(values, won)
		m.Bounds = bounds
		acc = m.accuracy(values, won)
		if model, err = json.Marshal(m); err != nil {
			return err
		}
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO crm_scoring (pipeline_id, model, samples, won, accuracy, trained_at)
		SELECT id, $2, $3, $4, $5, now() FROM crm_pipelines WHERE id::text = $1 AND type_id IS NULL
		ON CONFLICT (pipeline_id) DO UPDATE SET model = $2, samples = $3, won = $4, accuracy = $5, trained_at = now()`,
		pipelineID, model, len(deals), nWon, acc)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetScoring turns the scoring of a pipeline of deals on or off; turned on, it learns at once.
func (s *Store) SetScoring(ctx context.Context, userID, pipelineID string, enabled bool) error {
	tag, err := s.db.Exec(ctx, `INSERT INTO crm_scoring (pipeline_id, enabled, updated_by)
		SELECT id, $2, $3 FROM crm_pipelines WHERE id::text = $1 AND type_id IS NULL
		ON CONFLICT (pipeline_id) DO UPDATE SET enabled = $2, updated_by = $3`, pipelineID, enabled, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if enabled {
		return s.TrainScoring(ctx, pipelineID)
	}
	return nil
}

// ScoringList is the scoring of every pipeline of deals, with how many closed deals each has now.
func (s *Store) ScoringList(ctx context.Context) ([]Scoring, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id::text, coalesce(p.code, ''), p.name, coalesce(sc.enabled, false), coalesce(sc.samples, 0),
			coalesce(sc.won, 0), coalesce(sc.accuracy, 0), sc.trained_at,
			(SELECT count(*) FROM crm_deals d JOIN crm_stages st ON st.id = d.stage_id WHERE d.pipeline_id = p.id AND st.semantic <> 'process')::int,
			(SELECT count(*) FROM crm_deals d JOIN crm_stages st ON st.id = d.stage_id WHERE d.pipeline_id = p.id AND st.semantic = 'success')::int
		FROM crm_pipelines p LEFT JOIN crm_scoring sc ON sc.pipeline_id = p.id WHERE p.type_id IS NULL ORDER BY p.sort, p.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Scoring, error) {
		var c Scoring
		err := r.Scan(&c.PipelineID, &c.Code, &c.Name, &c.Enabled, &c.Samples, &c.Won, &c.Accuracy, &c.TrainedAt, &c.Closed, &c.ClosedWon)
		return c, err
	})
}

// TrainScoringDue learns again the models of the pipelines scored that were learned more than a day ago (go24 worker).
func (s *Store) TrainScoringDue(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT pipeline_id::text FROM crm_scoring WHERE enabled AND (trained_at IS NULL OR trained_at < $1)`,
		now.Add(-24*time.Hour))
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := s.TrainScoring(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			return 0, err
		}
	}
	return len(ids), nil
}

// Scores are the probabilities of the open deals (of ids) of the pipelines scored, by deal; others are absent.
func (s *Store) Scores(ctx context.Context, ids []string) (map[string]*Score, error) {
	out := map[string]*Score{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT pipeline_id::text, model FROM crm_scoring WHERE enabled AND model IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	models := map[string]*ScoreModel{}
	var pipes []string
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		var m ScoreModel
		if json.Unmarshal(raw, &m) == nil {
			models[id], pipes = &m, append(pipes, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(pipes) == 0 {
		return out, err
	}
	deals, err := s.scoreDeals(ctx, `d.id::text = ANY($1) AND st.semantic = 'process' AND d.pipeline_id::text = ANY($2)`, ids, pipes)
	if err != nil {
		return nil, err
	}
	for _, d := range deals {
		m := models[d.pipeline]
		d.fill(m.Bounds)
		p, parts := m.probability(d.values, nil)
		out[d.id] = &Score{Percent: min(99, max(1, int(math.Round(p*100)))), Factors: d.factors(parts)}
	}
	return out, nil
}

// factors are the features that move the probability most: up to three up, then up to three down.
func (d scoreDeal) factors(parts map[string]float64) []ScoreFactor {
	var up, down []ScoreFactor
	for _, f := range ScoreFeatures {
		w := parts[f]
		if math.Abs(w) < 0.15 { // hardly moves it
			continue
		}
		x := ScoreFactor{Feature: f, Value: d.values[f], Dict: d.dicts[f], Up: w > 0, weight: math.Abs(w)}
		if f == "responsible" {
			x.Name = d.responsible
		}
		if x.Up {
			up = append(up, x)
		} else {
			down = append(down, x)
		}
	}
	byWeight := func(l []ScoreFactor) []ScoreFactor {
		sort.SliceStable(l, func(i, j int) bool { return l[i].weight > l[j].weight })
		return l[:min(3, len(l))]
	}
	return append(byWeight(up), byWeight(down)...)
}

// ScoreOf is the probability of a deal, nil — it is closed or its pipeline is not scored.
func (s *Store) ScoreOf(ctx context.Context, id string) (*Score, error) {
	scores, err := s.Scores(ctx, []string{id})
	return scores[id], err
}
