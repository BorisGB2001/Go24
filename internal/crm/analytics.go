package crm

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Аналитика» of the CRM, as the portal sales reports: the funnel of a pipeline (deals created in the period by
// their stage now), the deals closed in the period (won, lost, win rate, average deal and cycle), the managers'
// results, and the leads (by stage, conversion into deals, by source). Only the cards the viewer may read count.

// StageCount is a stage with its cards' count and sum.
type StageCount struct {
	Stage
	Count int
	Sum   float64
}

// Manager is a person's results in the period.
type Manager struct {
	Ref
	InWork, Won, Lost int
	InWorkSum, WonSum float64
	OverdueActivities int
}

// WinRate is the share of won among the closed, in percent.
func (m Manager) WinRate() int {
	if m.Won+m.Lost == 0 {
		return 0
	}
	return m.Won * 100 / (m.Won + m.Lost)
}

// Source is a lead source with its leads and converted ones: the item of «Источник», or the text of a lead without one.
type Source struct {
	Dict
	Count, Converted int
}

// Report is the analytics of a pipeline over a period [From, To).
type Report struct {
	From, To            time.Time
	Funnel              []StageCount
	Won, Lost           int
	WonSum, LostSum     float64
	CycleDays           float64 // average days from creating to winning
	Managers            []Manager
	Leads               []StageCount
	LeadsNew, Converted int
	Sources             []Source
	Currency            string // the base one: the sums are in it
}

// WinRate is the share of won among the deals closed in the period, in percent.
func (r Report) WinRate() int {
	if r.Won+r.Lost == 0 {
		return 0
	}
	return r.Won * 100 / (r.Won + r.Lost)
}

// AverageWon is the average won deal.
func (r Report) AverageWon() float64 {
	if r.Won == 0 {
		return 0
	}
	return round2(r.WonSum / float64(r.Won))
}

// Conversion is the share of the period's leads converted into deals, in percent.
func (r Report) Conversion() int {
	if r.LeadsNew == 0 {
		return 0
	}
	return r.Converted * 100 / r.LeadsNew
}

// MaxCount is the largest stage count of the funnel (the bars' scale).
func (r Report) MaxCount() int {
	m := 1
	for _, s := range append(append([]StageCount{}, r.Funnel...), r.Leads...) {
		m = max(m, s.Count)
	}
	return m
}

// Analytics makes the report of a pipeline over [from, to) for the viewer.
func (s *Store) Analytics(ctx context.Context, viewer, pipelineID string, from, to time.Time) (Report, error) {
	rep := Report{From: from, To: to}
	ds, err := s.viewScope(ctx, viewer, "deal", "d", 4)
	if err != nil {
		return rep, err
	}
	ls, err := s.viewScope(ctx, viewer, "lead", "l", 3)
	if err != nil {
		return rep, err
	}
	if rep.Currency, err = s.BaseCurrency(ctx); err != nil {
		return rep, err
	}
	stageRows := func(rows pgx.Rows, err error) ([]StageCount, error) {
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StageCount, error) {
			var c StageCount
			err := r.Scan(&c.ID, &c.Code, &c.Name, &c.Color, &c.Semantic, &c.Count, &c.Sum)
			return c, err
		})
	}
	// the funnel: the stages of the pipeline with the deals created in the period now in them
	if rep.Funnel, err = stageRows(s.db.Query(ctx, `SELECT `+stageCols+`, count(d.id)::int, coalesce(sum(`+inBase("d.amount", "d.currency")+`), 0)::float8
		FROM crm_stages st LEFT JOIN crm_deals d ON d.stage_id = st.id AND d.created_at >= $2 AND d.created_at < $3 AND (`+ds+`)
		WHERE st.pipeline_id::text = $1 GROUP BY st.id ORDER BY st.sort`, pipelineID, from, to, viewer)); err != nil {
		return rep, err
	}
	// closed in the period
	if err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE st.semantic = 'success')::int, count(*) FILTER (WHERE st.semantic = 'failure')::int,
			coalesce(sum(`+inBase("d.amount", "d.currency")+`) FILTER (WHERE st.semantic = 'success'), 0)::float8, coalesce(sum(`+inBase("d.amount", "d.currency")+`) FILTER (WHERE st.semantic = 'failure'), 0)::float8,
			coalesce(avg(extract(epoch FROM d.closed_at - d.created_at) / 86400) FILTER (WHERE st.semantic = 'success'), 0)::float8
		FROM crm_deals d JOIN crm_stages st ON st.id = d.stage_id
		WHERE d.pipeline_id::text = $1 AND d.closed_at >= $2 AND d.closed_at < $3 AND (`+ds+`)`, pipelineID, from, to, viewer).
		Scan(&rep.Won, &rep.Lost, &rep.WonSum, &rep.LostSum, &rep.CycleDays); err != nil {
		return rep, err
	}
	rep.CycleDays = round2(rep.CycleDays)
	// the managers: in work now, closed in the period, overdue deeds
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name,
			count(*) FILTER (WHERE st.semantic = 'process')::int, coalesce(sum(`+inBase("d.amount", "d.currency")+`) FILTER (WHERE st.semantic = 'process'), 0)::float8,
			count(*) FILTER (WHERE st.semantic = 'success' AND d.closed_at >= $2 AND d.closed_at < $3)::int,
			coalesce(sum(`+inBase("d.amount", "d.currency")+`) FILTER (WHERE st.semantic = 'success' AND d.closed_at >= $2 AND d.closed_at < $3), 0)::float8,
			count(*) FILTER (WHERE st.semantic = 'failure' AND d.closed_at >= $2 AND d.closed_at < $3)::int,
			(SELECT count(*) FROM crm_activities a WHERE a.responsible_id = u.id AND a.done_at IS NULL AND a.deadline < now())::int
		FROM crm_deals d JOIN crm_stages st ON st.id = d.stage_id JOIN users u ON u.id = d.responsible_id
		WHERE d.pipeline_id::text = $1 AND (`+ds+`) GROUP BY u.id ORDER BY 6 DESC, 4 DESC, u.name`, pipelineID, from, to, viewer)
	if err != nil {
		return rep, err
	}
	if rep.Managers, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Manager, error) {
		var m Manager
		err := r.Scan(&m.ID, &m.Name, &m.InWork, &m.InWorkSum, &m.Won, &m.WonSum, &m.Lost, &m.OverdueActivities)
		return m, err
	}); err != nil {
		return rep, err
	}
	// leads created in the period: by stage, converted, by source
	if rep.Leads, err = stageRows(s.db.Query(ctx, `SELECT `+stageCols+`, count(l.id)::int, coalesce(sum(`+inBase("l.amount", "l.currency")+`), 0)::float8
		FROM crm_stages st LEFT JOIN crm_leads l ON l.stage_id = st.id AND l.created_at >= $1 AND l.created_at < $2 AND (`+ls+`)
		WHERE st.entity = 'lead' GROUP BY st.id ORDER BY st.sort`, from, to, viewer)); err != nil {
		return rep, err
	}
	rows, err = s.db.Query(ctx, `SELECT coalesce(j.id::text, ''), coalesce(j.code, ''), coalesce(j.name, trim(l.source), ''), count(*)::int, count(l.deal_id)::int
		FROM crm_leads l`+dictJoin("j", "l.source_id")+` WHERE l.created_at >= $1 AND l.created_at < $2 AND (`+ls+`)
		GROUP BY 1, 2, 3 ORDER BY 4 DESC, 3 LIMIT 20`, from, to, viewer)
	if err != nil {
		return rep, err
	}
	if rep.Sources, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Source, error) {
		var x Source
		err := r.Scan(&x.ID, &x.Code, &x.Name, &x.Count, &x.Converted)
		return x, err
	}); err != nil {
		return rep, err
	}
	for _, x := range rep.Leads {
		rep.LeadsNew += x.Count
	}
	err = s.db.QueryRow(ctx, `SELECT count(*)::int FROM crm_leads l WHERE l.deal_id IS NOT NULL AND l.created_at >= $1 AND l.created_at < $2
		AND (`+ls+`)`, from, to, viewer).Scan(&rep.Converted)
	return rep, err
}
