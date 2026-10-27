package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
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
// must still be in the stage then), may run only for an amount from–to and only when the card meets a condition; a
// fields robot sets fields of the card, a webhook robot posts it to an address (robotfields.go); documents, processes,
// observers and deeds — robotmore.go. Texts may hold {title}, {amount},
// {responsible}, {client}, {link}.

// RobotKinds in the order the settings offer them.
var RobotKinds = []string{"task", "activity", "notify", "mail", "booking", "infocall", "comment", "fields", "webhook", "responsible",
	"observers", "document", "bp", "close", "stage", "copy", "move"}

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
	Text    string     `json:"text"`              // task title, notification, comment, deed's or letter's subject
	Details string     `json:"details,omitempty"` // task or deed description, the letter's text
	Project string     `json:"project,omitempty"` // task: the project key
	To      string     `json:"to,omitempty"`      // "responsible", "creator" or a user id
	Days    int        `json:"days,omitempty"`    // task, deed: deadline in days (0 — a task has none, a deed is in an hour)
	Act     string     `json:"act,omitempty"`     // deed: call | meeting | todo
	Stage   string     `json:"stage,omitempty"`   // stage, copy, move robots: where the card (or its copy) goes
	Delay   int        `json:"delay,omitempty"`   // wait so many units before running
	Unit    string     `json:"unit,omitempty"`    // min | hour | day
	Min     float64    `json:"min,omitempty"`     // run only for an amount from (0 — any)
	Max     float64    `json:"max,omitempty"`     // and up to (0 — any)
	If      *RobotCond `json:"if,omitempty"`      // run only when the card meets it («Условие»)
	Set     []RobotSet `json:"set,omitempty"`     // fields robot: what it sets
	URL     string     `json:"url,omitempty"`     // webhook robot: where it posts the card
	Link    string     `json:"link,omitempty"`    // booking: the booking link's id
	Users   []string   `json:"users,omitempty"`   // responsible robot: the people it gives cards to (one — To of old robots)
	Mode    string     `json:"mode,omitempty"`    // responsible robot among several: queue | even | random
	Skip    bool       `json:"skip,omitempty"`    // responsible robot: passes over the absent («Пропускать отсутствующих»)
	Line    string     `json:"line,omitempty"`    // infocall: the external line it calls through
	Sound   string     `json:"sound,omitempty"`   // infocall: the message's checksum (telephony_sounds)
	Obs     string     `json:"obs,omitempty"`     // observers robot: add | remove | set (its Users)
	Doc     string     `json:"doc,omitempty"`     // document robot: the template's id
	BP      string     `json:"bp,omitempty"`      // process robot: the template's id
}

// ShareModes are how the responsible robot shares cards among several people: in turn, to whom has the fewest open
// cards, at random.
var ShareModes = []string{"queue", "even", "random"}

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
	needText := !slices.Contains([]string{"responsible", "stage", "fields", "webhook", "infocall", "observers", "document", "bp", "close"}, kind) &&
		!slices.Contains(TunnelKinds, kind)
	if !ok || (needText && p.Text == "") || len([]rune(p.Text)) > 500 || len([]rune(p.Details)) > 5000 || p.Days < 0 || p.Days > 365 {
		return i18n.E("err.robot")
	}
	if p.Delay < 0 || p.Delay > 1000 || (p.Delay > 0 && robotUnits[p.Unit] == 0) || p.Min < 0 || p.Max < 0 || (p.Max > 0 && p.Max < p.Min) {
		return i18n.E("err.robot_when")
	}
	if p.Delay == 0 {
		p.Unit = ""
	}
	if err := s.checkRobotExtra(ctx, stageID, kind, &p); err != nil {
		return err
	}
	if err := s.checkRobotMore(ctx, stageID, kind, &p); err != nil {
		return err
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
		if len(p.Users) == 0 && p.To != "" {
			p.Users = []string{p.To}
		}
		var users []string
		for _, u := range p.Users {
			if u = strings.TrimSpace(u); u != "" && !slices.Contains(users, u) {
				users = append(users, u)
			}
		}
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE id::text = ANY($1)`, users).Scan(&n); err != nil || n == 0 ||
			n != len(users) || len(users) > 100 {
			return i18n.E("err.robot_user")
		}
		if p.Users, p.To = users, users[0]; !slices.Contains(ShareModes, p.Mode) {
			p.Mode = "queue"
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
	case "infocall":
		if s.InfoCall == nil || p.Line == "" || p.Sound == "" {
			return i18n.E("err.robot_infocall")
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
	if p.If != nil {
		values, err := s.cardValues(ctx, c)
		if err != nil || !robotCondOK(*p.If, values) {
			return
		}
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
	case "infocall":
		err = s.robotInfoCall(ctx, actorID, c, p)
	case "fields":
		err = s.robotFields(ctx, actorID, c, p.Set, respName)
	case "webhook":
		err = s.robotWebhook(ctx, actorID, c, p.URL)
	case "document":
		err = s.robotDocument(ctx, actorID, c, p.Doc)
	case "bp":
		err = s.robotProcess(ctx, actorID, c, p.BP)
	case "observers":
		err = s.robotObservers(ctx, actorID, c, p)
	case "close":
		err = s.robotClose(ctx, actorID, c, p.Act)
	case "responsible":
		if to, err = s.shareTo(ctx, r, c); err != nil || to == c.responsible {
			break
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

// shareTo is whom the responsible robot gives the card: its only person, or one of its people by its mode — of those
// at work today when it passes over the absent (all absent — any of them). In turn: the one after whom it gave a card
// last time; evenly: the one with the fewest open cards of the kind (in turn among equals).
func (s *Store) shareTo(ctx context.Context, r Robot, c card) (string, error) {
	all := r.Params.Users
	if len(all) == 0 {
		all = []string{r.Params.To}
	}
	free := all
	if r.Params.Skip && len(all) > 1 {
		rows, err := s.db.Query(ctx, `SELECT DISTINCT user_id::text FROM absences WHERE user_id::text = ANY($1)
			AND starts_on <= $2::date AND ends_on >= $2::date`, all, time.Now())
		if err != nil {
			return "", err
		}
		absent, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return "", err
		}
		if free = slices.DeleteFunc(slices.Clone(all), func(u string) bool { return slices.Contains(absent, u) }); len(free) == 0 {
			free = all
		}
	}
	if len(free) == 1 {
		return free[0], nil
	}
	switch r.Params.Mode {
	case "random":
		return free[rand.IntN(len(free))], nil
	case "even":
		rows, err := s.db.Query(ctx, `SELECT c.responsible_id::text, count(*) FROM crm_`+plural[c.entity]+` c JOIN crm_stages st ON st.id = c.stage_id
			WHERE st.semantic = 'process' AND c.responsible_id::text = ANY($1) GROUP BY 1`, free)
		if err != nil {
			return "", err
		}
		load := map[string]int{}
		for rows.Next() {
			var u string
			var n int
			if err := rows.Scan(&u, &n); err != nil {
				rows.Close()
				return "", err
			}
			load[u] = n
		}
		if err := rows.Err(); err != nil {
			return "", err
		}
		least := slices.MinFunc(free, func(a, b string) int { return load[a] - load[b] })
		free = slices.DeleteFunc(slices.Clone(free), func(u string) bool { return load[u] > load[least] })
		if len(free) == 1 {
			return free[0], nil
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var last string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM crm_robot_picks WHERE robot_id::text = $1 FOR UPDATE`, r.ID).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	user, after := free[0], slices.Index(all, last)
	for _, u := range free {
		if slices.Index(all, u) > after {
			user = u
			break
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO crm_robot_picks (robot_id, user_id) VALUES ($1, $2)
		ON CONFLICT (robot_id) DO UPDATE SET user_id = excluded.user_id`, r.ID, user); err != nil {
		return "", err
	}
	return user, tx.Commit(ctx)
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

// robotInfoCall calls the client's phone (the lead's; the deal's contact's, or else its company's) and plays the message.
func (s *Store) robotInfoCall(ctx context.Context, actorID string, c card, p RobotParams) error {
	if s.InfoCall == nil {
		return i18n.E("err.robot_infocall")
	}
	var to string
	q := `SELECT phone FROM crm_leads WHERE id::text = $1`
	if c.entity == "deal" {
		q = `SELECT coalesce(nullif(x.phone, ''), c.phone, '') FROM crm_deals d LEFT JOIN crm_contacts x ON x.id = d.contact_id
			LEFT JOIN crm_companies c ON c.id = d.company_id WHERE d.id::text = $1`
	}
	if err := s.db.QueryRow(ctx, q, c.id).Scan(&to); err != nil {
		return err
	}
	if to = strings.TrimSpace(to); to == "" {
		return i18n.E("err.robot_phone")
	}
	if err := s.InfoCall(ctx, p.Line, to, p.Sound); err != nil {
		return err
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "infocall:"+to)
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
