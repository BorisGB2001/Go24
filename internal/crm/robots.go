package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Роботы»: what a stage does when a deal or a lead enters it. A task robot makes a task in a project
// (for the card's responsible or a given person, with a deadline in N days), a notify robot sends a notification, a
// comment robot writes into the card's history, an activity robot plans a deed (a call, a meeting, a to-do) on the
// card, a mail robot writes to the client from the responsible's mailbox, a responsible robot gives the card to
// another person, a stage robot moves it on. Sales tunnels («Туннели продаж»): a copy robot makes the deal's copy in
// a stage of another pipeline of the same kind of cards, a move robot moves the deal itself there. A booking robot
// («Отправить ссылку для записи») writes to the client as a mail robot does, with the address of a booking link. A robot may wait («Выполнить через» minutes, hours or days: the card
// must still be in the stage then) and may run only for an amount from–to. Texts may hold {title}, {amount},
// {responsible}, {client}, {link}.

// RobotKinds in the order the settings offer them.
var RobotKinds = []string{"task", "activity", "notify", "mail", "booking", "comment", "responsible", "stage", "copy", "move"}

// TunnelKinds are the robots that lead into another pipeline (of deals and smart processes only).
var TunnelKinds = []string{"copy", "move"}

// robotUnits are the delays a robot may wait, by their name in the settings.
var robotUnits = map[string]time.Duration{"min": time.Minute, "hour": time.Hour, "day": 24 * time.Hour}

// Robot of a stage.
type Robot struct {
	ID, StageID, Kind string
	Params            RobotParams
	Active            bool
}

// RobotParams are a robot's settings (unused ones stay empty).
type RobotParams struct {
	Text    string  `json:"text"`              // task title, notification, comment, deed's or letter's subject
	Details string  `json:"details,omitempty"` // task or deed description, the letter's text
	Project string  `json:"project,omitempty"` // task: the project key
	To      string  `json:"to,omitempty"`      // "responsible", "creator" or a user id
	Days    int     `json:"days,omitempty"`    // task, deed: deadline in days (0 — a task has none, a deed is in an hour)
	Act     string  `json:"act,omitempty"`     // deed: call | meeting | todo
	Stage   string  `json:"stage,omitempty"`   // stage, copy, move robots: where the card (or its copy) goes
	Delay   int     `json:"delay,omitempty"`   // wait so many units before running
	Unit    string  `json:"unit,omitempty"`    // min | hour | day
	Min     float64 `json:"min,omitempty"`     // run only for an amount from (0 — any)
	Max     float64 `json:"max,omitempty"`     // and up to (0 — any)
	Link    string  `json:"link,omitempty"`    // booking: the booking link's id
}

// Wait is how long the robot waits before it runs.
func (p RobotParams) Wait() time.Duration { return time.Duration(p.Delay) * robotUnits[p.Unit] }

// Robots are the robots of the stages given (all of an entity's stages).
func (s *Store) Robots(ctx context.Context, stageIDs []string) ([]Robot, error) {
	rows, err := s.db.Query(ctx, `SELECT id, stage_id::text, kind, params, active FROM crm_robots WHERE stage_id::text = ANY($1) ORDER BY created_at`, stageIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Robot, error) {
		var x Robot
		var raw []byte
		if err := r.Scan(&x.ID, &x.StageID, &x.Kind, &raw, &x.Active); err != nil {
			return x, err
		}
		return x, json.Unmarshal(raw, &x.Params)
	})
}

// AddRobot sets a robot on a stage (administrators; checked by the web layer).
func (s *Store) AddRobot(ctx context.Context, userID, stageID, kind string, p RobotParams) error {
	p.Text, p.Details, p.Project, p.To = strings.TrimSpace(p.Text), strings.TrimSpace(p.Details), strings.ToUpper(strings.TrimSpace(p.Project)), strings.TrimSpace(p.To)
	ok := false
	for _, k := range RobotKinds {
		ok = ok || k == kind
	}
	needText := kind != "responsible" && kind != "stage" && !slices.Contains(TunnelKinds, kind)
	if !ok || (needText && p.Text == "") || len([]rune(p.Text)) > 500 || len([]rune(p.Details)) > 5000 || p.Days < 0 || p.Days > 365 {
		return i18n.E("err.robot")
	}
	if p.Delay < 0 || p.Delay > 1000 || (p.Delay > 0 && robotUnits[p.Unit] == 0) || p.Min < 0 || p.Max < 0 || (p.Max > 0 && p.Max < p.Min) {
		return i18n.E("err.robot_when")
	}
	if p.Delay == 0 {
		p.Unit = ""
	}
	switch kind {
	case "task":
		if p.Project == "" {
			return i18n.E("err.robot_project")
		}
	case "activity":
		if !slices.Contains(ActivityKinds, p.Act) {
			p.Act = "todo"
		}
	case "responsible":
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE id::text = $1`, p.To).Scan(&n); err != nil || n == 0 {
			return i18n.E("err.robot_user")
		}
	case "stage":
		var same bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stages a JOIN crm_stages b ON b.entity = a.entity
			AND b.pipeline_id IS NOT DISTINCT FROM a.pipeline_id AND b.id <> a.id WHERE a.id::text = $1 AND b.id::text = $2)`, stageID, p.Stage).
			Scan(&same); err != nil || !same {
			return i18n.E("err.robot_stage")
		}
	case "booking":
		if s.BookingURL == nil {
			return i18n.E("err.robot_booking")
		}
		if _, err := s.BookingURL(ctx, p.Link); err != nil {
			return i18n.E("err.robot_booking")
		}
	case "copy", "move":
		var other bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stages a JOIN crm_pipelines pa ON pa.id = a.pipeline_id
			JOIN crm_pipelines pb ON pb.type_id IS NOT DISTINCT FROM pa.type_id AND pb.id <> pa.id
			JOIN crm_stages b ON b.pipeline_id = pb.id AND b.entity = 'deal'
			WHERE a.entity = 'deal' AND a.id::text = $1 AND b.id::text = $2)`, stageID, p.Stage).Scan(&other); err != nil || !other {
			return i18n.E("err.robot_tunnel")
		}
	}
	if p.To == "" {
		p.To = "responsible"
	}
	raw, _ := json.Marshal(p)
	tag, err := s.db.Exec(ctx, `INSERT INTO crm_robots (stage_id, kind, params, created_by) SELECT id, $2, $3, $4 FROM crm_stages WHERE id::text = $1`,
		stageID, kind, raw, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.crm_stage")
	}
	return err
}

func (s *Store) DeleteRobot(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM crm_robots WHERE id::text = $1`, id)
	return err
}

// card is what robots know of the card that entered the stage.
type card struct {
	entity, id, stage, title, responsible, creator, client, link string
	amount                                                       float64
	currency                                                     string
}

func (c card) fill(text, responsibleName string) string {
	return strings.NewReplacer("{title}", c.title, "{amount}", Amount(c.amount)+" "+c.currency,
		"{responsible}", responsibleName, "{client}", c.client, "{link}", c.link).Replace(text)
}

// robotDepth counts the stage robots that moved a card in a row: a ring of stage robots stops after a few.
type robotDepth struct{}

const maxRobotDepth = 5

// runRobots does what the stage's robots say; a robot that waits is queued (RunRobotsDue), one that fails is written
// into the history and the others go on.
func (s *Store) runRobots(ctx context.Context, actorID, stageID string, c card) {
	depth, _ := ctx.Value(robotDepth{}).(int)
	if depth >= maxRobotDepth {
		return
	}
	robots, err := s.Robots(ctx, []string{stageID})
	if err != nil {
		return
	}
	for _, r := range robots {
		if !r.Active {
			continue
		}
		if wait := r.Params.Wait(); wait > 0 {
			if _, err := s.db.Exec(ctx, `INSERT INTO crm_robot_jobs (robot_id, entity, entity_id, stage_id, actor_id, run_at)
				VALUES ($1, $2, $3, $4, $5, $6)`, r.ID, c.entity, c.id, stageID, actorID, time.Now().Add(wait)); err != nil {
				_ = s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "fail:"+r.Kind+":"+errKey(err))
			}
			continue
		}
		s.runRobot(ctx, actorID, r, c)
	}
}

// runRobot does one robot's work on the card (if the card's amount is in the robot's range).
func (s *Store) runRobot(ctx context.Context, actorID string, r Robot, c card) {
	p := r.Params
	if (p.Min > 0 && c.amount < p.Min) || (p.Max > 0 && c.amount > p.Max) {
		return
	}
	var respName string
	_ = s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, c.responsible).Scan(&respName)
	to := p.To
	switch to {
	case "responsible", "":
		to = c.responsible
	case "creator":
		to = c.creator
	}
	text := c.fill(p.Text, respName)
	var err error
	switch r.Kind {
	case "comment":
		err = s.event(ctx, s.db, c.entity, c.id, actorID, "robot", text)
	case "notify":
		_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'crm_robot', $3, $4)`,
			to, actorID, text, c.link)
	case "task":
		err = s.robotTask(ctx, actorID, to, p, c, text, respName)
	case "activity":
		deadline := time.Now().Add(time.Hour)
		if p.Days > 0 {
			deadline = time.Now().AddDate(0, 0, p.Days)
		}
		_, err = s.AddActivity(ctx, actorID, Activity{Entity: c.entity, EntityID: c.id, Kind: p.Act, Subject: text,
			Description: c.fill(p.Details, respName), Deadline: deadline, Responsible: Ref{ID: to}})
		if err == nil {
			err = s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "activity:"+p.Act+":"+text)
		}
	case "mail":
		err = s.robotMail(ctx, actorID, c, text, c.fill(p.Details, respName))
	case "booking":
		err = s.robotBooking(ctx, actorID, c, p, text, respName)
	case "responsible":
		if to == c.responsible {
			return
		}
		if err = s.SetResponsible(ctx, actorID, c.entity, c.id, to); err == nil {
			var name string
			_ = s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, to).Scan(&name)
			err = s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "responsible:"+name)
		}
	case "stage", "move":
		depth, _ := ctx.Value(robotDepth{}).(int)
		ctx = context.WithValue(ctx, robotDepth{}, depth+1)
		if c.entity == "deal" {
			_, err = s.MoveDeal(ctx, actorID, c.id, p.Stage)
		} else {
			_, err = s.MoveLead(ctx, actorID, c.id, p.Stage)
		}
	case "copy":
		depth, _ := ctx.Value(robotDepth{}).(int)
		_, err = s.TunnelDeal(context.WithValue(ctx, robotDepth{}, depth+1), actorID, c.id, p.Stage)
	}
	if err != nil {
		_ = s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "fail:"+r.Kind+":"+errKey(err))
	}
}

// errKey is how a robot's failure is kept in the history: a known error by its locale key, another by its text.
func errKey(err error) string {
	var ie *i18n.Error
	if errors.As(err, &ie) {
		return ie.Key
	}
	return err.Error()
}

// robotMail writes to the card's client (a lead's e-mail; a deal's contact's or else company's) from the first
// mailbox of the card's responsible; the letter lands in the card as any letter to a client does.
func (s *Store) robotMail(ctx context.Context, actorID string, c card, subject, text string) error {
	if s.SendMail == nil {
		return i18n.E("err.robot_mailbox")
	}
	var to string
	q := `SELECT email FROM crm_leads WHERE id::text = $1`
	if c.entity == "deal" {
		q = `SELECT coalesce(nullif(x.email, ''), c.email, '') FROM crm_deals d LEFT JOIN crm_contacts x ON x.id = d.contact_id
			LEFT JOIN crm_companies c ON c.id = d.company_id WHERE d.id::text = $1`
	}
	if err := s.db.QueryRow(ctx, q, c.id).Scan(&to); err != nil {
		return err
	}
	if to == "" {
		return i18n.E("err.robot_email")
	}
	if text == "" {
		text = subject
	}
	if err := s.SendMail(ctx, c.responsible, to, subject, text); err != nil {
		return err
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "mail:"+to)
}

// robotBooking writes to the client as robotMail with the address of the booking link: in place of {booking} in the
// text, or else after it; a link removed or switched off since is the robot's failure.
func (s *Store) robotBooking(ctx context.Context, actorID string, c card, p RobotParams, subject, respName string) error {
	if s.BookingURL == nil {
		return i18n.E("err.robot_booking")
	}
	u, err := s.BookingURL(ctx, p.Link)
	if err != nil {
		return i18n.E("err.robot_booking")
	}
	text := c.fill(p.Details, respName)
	if strings.Contains(text, "{booking}") {
		text = strings.ReplaceAll(text, "{booking}", u)
	} else {
		text = strings.TrimSpace(text + "\n\n" + u)
	}
	return s.robotMail(ctx, actorID, c, subject, text)
}

// RunRobotsDue runs the robots whose wait is over, for the cards still in the stage they waited in.
func (s *Store) RunRobotsDue(ctx context.Context, now time.Time) (int, error) {
	type job struct{ robot, entity, id, stage, actor string }
	rows, err := s.db.Query(ctx, `DELETE FROM crm_robot_jobs WHERE id IN (SELECT id FROM crm_robot_jobs WHERE run_at <= $1
		ORDER BY run_at LIMIT 200 FOR UPDATE SKIP LOCKED) RETURNING robot_id::text, entity, entity_id::text, stage_id::text, actor_id::text`, now)
	if err != nil {
		return 0, err
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (job, error) {
		var j job
		return j, r.Scan(&j.robot, &j.entity, &j.id, &j.stage, &j.actor)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range jobs {
		var c card
		if j.entity == "deal" {
			d, err := s.Deal(ctx, j.id)
			if err != nil {
				continue
			}
			c = dealCard(d)
		} else {
			l, err := s.Lead(ctx, j.id)
			if err != nil {
				continue
			}
			c = leadCard(l)
		}
		if c.stage != j.stage {
			continue
		}
		robots, err := s.Robots(ctx, []string{j.stage})
		if err != nil {
			return n, err
		}
		for _, r := range robots {
			if r.ID == j.robot && r.Active {
				s.runRobot(ctx, j.actor, r, c)
				n++
			}
		}
	}
	return n, nil
}

func (s *Store) robotTask(ctx context.Context, actorID, to string, p RobotParams, c card, title, respName string) error {
	if s.Tasks == nil {
		return fmt.Errorf("tasks are off")
	}
	proj, err := s.Tasks.ProjectByKey(tasks.WithAdmin(ctx), actorID, p.Project) // the robot acts for the company
	if err != nil {
		return err
	}
	var deadline *time.Time
	if p.Days > 0 {
		sched, err := s.Tasks.Schedule(ctx)
		if err != nil {
			return err
		}
		d := sched.Deadline(time.Now().AddDate(0, 0, p.Days))
		deadline = &d
	}
	key, err := s.Tasks.CreateTask(tasks.WithAdmin(ctx), actorID, proj, tasks.NewTask{Title: title,
		Description: c.fill(p.Details, respName), ResponsibleID: to, Priority: 1, Deadline: deadline})
	if err != nil {
		return err
	}
	id, err := s.Tasks.TaskIDByKey(ctx, key)
	if err == nil {
		err = s.Tasks.AddCRMLink(tasks.WithAdmin(ctx), actorID, id, c.link)
	}
	if err == nil {
		err = s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "task:"+key)
	}
	return err
}

// dealCard and leadCard: what robots know of a card.
func dealCard(d Deal) card {
	client := d.Contact.Name
	if client == "" {
		client = d.Company.Name
	}
	return card{entity: "deal", id: d.ID, stage: d.Stage.ID, client: client, title: d.Title, responsible: d.Responsible.ID, creator: d.CreatedBy,
		link: "/crm/deals/" + d.ID, amount: d.Amount, currency: d.Currency}
}

func leadCard(l Lead) card {
	client := l.Name
	if client == "" {
		client = l.CompanyTitle
	}
	return card{entity: "lead", id: l.ID, stage: l.Stage.ID, client: client, title: l.Title, responsible: l.Responsible.ID, creator: l.CreatedBy,
		link: "/crm/leads/" + l.ID, amount: l.Amount, currency: l.Currency}
}
