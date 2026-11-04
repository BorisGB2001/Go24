package crm

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Robots that make cards of a card or ask about it: a convert robot («Конвертировать лид») converts the
// lead as the dialog offers it (ConvertDefaults), its deal in the robot's pipeline; a create robot («Создать на
// основании») makes a deal, a smart process card or a lead in the robot's stage of the card's title (or its text),
// amount, client and responsible (or whom the robot names) — both cards tell of each other in the history; a request
// robot («Запросить дополнительную информацию») asks an employee to fill fields of the card: a notification and a
// block over the card's form, the stage's later robots wait until the fields are filled (CheckInfoRequests).

// checkRobotBased checks (and tidies) the settings of the robots of this file.
func (s *Store) checkRobotBased(ctx context.Context, stageID, kind string, p *RobotParams) error {
	switch kind {
	case "convert":
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stages WHERE id::text = $1 AND entity = 'lead')
			AND ($2 = '' OR EXISTS (SELECT 1 FROM crm_pipelines WHERE id::text = $2 AND type_id IS NULL))`, stageID, p.Pipe).Scan(&ok); err != nil || !ok {
			return i18n.E("err.robot_convert")
		}
	case "create":
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stages WHERE id::text = $1 AND id::text <> $2
			AND entity IN ('deal', 'lead'))`, p.Stage, stageID).Scan(&ok); err != nil || !ok {
			return i18n.E("err.robot_create")
		}
	case "request":
		var entity string
		if err := s.db.QueryRow(ctx, `SELECT entity FROM crm_stages WHERE id::text = $1`, stageID).Scan(&entity); err != nil {
			return i18n.E("err.crm_stage")
		}
		var fields []string
		for _, f := range p.Fields {
			if f = strings.TrimSpace(f); f != "" && !slices.Contains(fields, f) {
				fields = append(fields, f)
			}
		}
		own, err := s.tasksStore().FieldsOf(ctx, tasks.CRMFieldSets[entity])
		if err != nil {
			return err
		}
		for _, f := range fields {
			id, ownField := strings.CutPrefix(f, "uf:")
			if ownField && !slices.ContainsFunc(own, func(o tasks.Field) bool { return o.ID == id }) ||
				!ownField && (f == "responsible" || !slices.Contains(RobotFieldKeys[entity], f)) {
				return i18n.E("err.robot_request")
			}
		}
		if len(fields) == 0 || len(fields) > 20 {
			return i18n.E("err.robot_request")
		}
		p.Fields = fields
	}
	return nil
}

// robotConvert converts the lead; one converted already (with nothing left to make) is let be.
func (s *Store) robotConvert(ctx context.Context, actorID string, c card, pipeline string) error {
	if c.entity != "lead" {
		return i18n.E("err.robot_convert")
	}
	l, err := s.Lead(ctx, c.id)
	if err != nil {
		return err
	}
	to, err := s.ConvertDefaults(ctx, l)
	if err != nil {
		return err
	}
	if l.DealID != "" {
		return nil
	}
	to.PipelineID = pipeline
	_, err = s.ConvertLeadTo(ctx, actorID, c.id, to)
	return err
}

// robotCreate makes the new card in the stage (its title — the text with the card's fields or the card's title).
func (s *Store) robotCreate(ctx context.Context, actorID, to string, c card, p RobotParams, title string) error {
	var entity, pipeline string
	err := s.db.QueryRow(ctx, `SELECT entity, coalesce(pipeline_id::text, '') FROM crm_stages WHERE id::text = $1`, p.Stage).Scan(&entity, &pipeline)
	if errors.Is(err, pgx.ErrNoRows) {
		return i18n.E("err.robot_create")
	}
	if err != nil {
		return err
	}
	if title == "" {
		title = c.title
	}
	var contact, company Ref
	var name, companyTitle string
	if c.entity == "deal" {
		d, err := s.Deal(ctx, c.id)
		if err != nil {
			return err
		}
		contact, company, name, companyTitle = d.Contact, d.Company, d.Contact.Name, d.Company.Name
	} else {
		l, err := s.Lead(ctx, c.id)
		if err != nil {
			return err
		}
		contact, company, name, companyTitle = Ref{ID: l.ContactID}, Ref{ID: l.CompanyID}, l.Name, l.CompanyTitle
	}
	depth, _ := ctx.Value(robotDepth{}).(int)
	ctx = context.WithValue(ctx, robotDepth{}, depth+1)
	var id string
	if entity == "deal" {
		id, err = s.SaveDeal(ctx, actorID, Deal{Title: title, PipelineID: pipeline, Stage: Stage{ID: p.Stage}, Amount: c.amount,
			Currency: c.currency, Contact: contact, Company: company, Responsible: Ref{ID: to}})
	} else {
		id, err = s.SaveLead(ctx, actorID, Lead{Title: title, Stage: Stage{ID: p.Stage}, Amount: c.amount, Currency: c.currency,
			Name: name, CompanyTitle: companyTitle, ContactID: contact.ID, CompanyID: company.ID, Responsible: Ref{ID: to}})
	}
	if err != nil {
		return err
	}
	if err := s.event(ctx, s.db, entity, id, actorID, "based_from", c.entity+":"+c.id); err != nil {
		return err
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "based_to", entity+":"+id)
}

// InfoRequest is an open request of a request robot: whom it asks to fill which fields of the card.
type InfoRequest struct {
	ID       int64
	UserID   string
	Text     string
	Fields   []string // RobotFieldKeys and «uf:» ones
	Missing  []string // of them, the ones still empty
	AuthorID string
}

// robotRequest asks to fill the fields: a request and a notification, unless they are filled already (then false).
// held are the robots of the stage after this one: they run when the fields are filled.
func (s *Store) robotRequest(ctx context.Context, actorID, to string, r Robot, c card, text string, held []string) (bool, error) {
	values, err := s.cardValues(ctx, c)
	if err != nil {
		return false, err
	}
	if len(missingFields(r.Params.Fields, values)) == 0 {
		return false, nil
	}
	if held == nil {
		held = []string{}
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO crm_info_requests (robot_id, entity, entity_id, stage_id, user_id, actor_id, body, fields, held)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid[])`, r.ID, c.entity, c.id, c.stage, to, actorID, text, r.Params.Fields, held); err != nil {
		return false, err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'crm_robot', $3, $4)`,
		to, actorID, c.title+": "+text, c.link); err != nil {
		return true, err
	}
	return true, s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "request:"+text)
}

func missingFields(fields []string, values map[string]string) []string {
	var out []string
	for _, f := range fields {
		if v := strings.TrimSpace(values[f]); v == "" || (f == "amount" && v == "0") {
			out = append(out, f)
		}
	}
	return out
}

// InfoRequests are the card's open requests (of the stage it is at), with the fields still empty.
func (s *Store) InfoRequests(ctx context.Context, entity, id string) ([]InfoRequest, error) {
	if entity != "deal" && entity != "lead" {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT q.id, q.user_id::text, q.body, q.fields, q.actor_id::text FROM crm_info_requests q
		JOIN crm_`+plural[entity]+` c ON c.id = q.entity_id AND c.stage_id = q.stage_id
		WHERE q.entity = $1 AND q.entity_id::text = $2 AND q.done_at IS NULL ORDER BY q.created_at`, entity, id)
	if err != nil {
		return nil, err
	}
	reqs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (InfoRequest, error) {
		var q InfoRequest
		return q, r.Scan(&q.ID, &q.UserID, &q.Text, &q.Fields, &q.AuthorID)
	})
	if err != nil || len(reqs) == 0 {
		return reqs, err
	}
	values, err := s.cardValues(ctx, card{entity: entity, id: id})
	if err != nil {
		return nil, err
	}
	for i := range reqs {
		reqs[i].Missing = missingFields(reqs[i].Fields, values)
	}
	return reqs, nil
}

// CheckInfoRequests closes the card's requests whose fields are filled now: the history tells of it and the robots
// they held run. A request of a stage the card has left is let be.
func (s *Store) CheckInfoRequests(ctx context.Context, actorID, entity, id string) error {
	reqs, err := s.InfoRequests(ctx, entity, id)
	if err != nil || len(reqs) == 0 {
		return err
	}
	var c card
	if entity == "deal" {
		d, err := s.Deal(ctx, id)
		if err != nil {
			return err
		}
		c = dealCard(d)
	} else {
		l, err := s.Lead(ctx, id)
		if err != nil {
			return err
		}
		c = leadCard(l)
	}
	for _, q := range reqs {
		if len(q.Missing) > 0 {
			continue
		}
		var held []string
		err := s.db.QueryRow(ctx, `UPDATE crm_info_requests SET done_at = now() WHERE id = $1 AND done_at IS NULL RETURNING held::text[]`,
			q.ID).Scan(&held)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // closed by another save just now
		}
		if err != nil {
			return err
		}
		if err := s.event(ctx, s.db, entity, id, actorID, "robot", "request_done:"+q.Text); err != nil {
			return err
		}
		robots, err := s.Robots(ctx, []string{c.stage})
		if err != nil {
			return err
		}
		robots = slices.DeleteFunc(robots, func(r Robot) bool { return !slices.Contains(held, r.ID) })
		s.runRobotList(ctx, q.AuthorID, c.stage, robots, c)
	}
	return nil
}
