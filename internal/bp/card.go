package bp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// The steps of the designer that act on the CRM and write to people: «Изменить карточку» (update) sets fields of
// the process's card, «Создать карточку» (create) adds a lead, a deal, a contact or a company, «Сообщение в чат» (chat)
// writes to people in the messenger and «Письмо» (mail) sends a letter from someone's mailbox. «Добавить комментарий»
// (comment) writes in the card's timeline, «Дело CRM» (activity) plans a call, a meeting or a to-do on the card,
// «Создание документа» (document) makes a document of the card by a template of the document generator.

// CardFields are the built-in fields of each kind of card a step may set (the locales crm.f.<key>); own fields go by
// their names.
var CardFields = map[string][]string{
	"deal":    {"title", "amount", "close_date", "responsible", "comment"},
	"lead":    {"title", "name", "company_title", "phone", "email", "amount", "responsible", "comment"},
	"contact": {"name", "position", "phone", "email", "responsible", "comment"},
	"company": {"title", "phone", "email", "website", "address", "responsible", "comment"},
}

// checkCardStep checks what a step of these kinds needs; entity is the template's card.
func checkCardStep(entity string, st Step) error {
	if st.Kind != "update" && st.Kind != "create" && st.Kind != "start" && st.Kind != "document" && len(st.Sets) > 0 || st.Kind != "create" && st.Entity != "" ||
		st.Kind != "chat" && st.Kind != "mail" && st.Kind != "post" && st.Kind != "comment" && st.Kind != "document" && st.From != "" || st.Kind != "mail" && st.Email != "" ||
		st.Kind != "mail" && st.Kind != "post" && st.Subject != "" || st.Kind == "post" && len([]rune(st.Subject)) > 300 ||
		st.Kind != "activity" && (st.Deed != "" || st.Note != "") || len([]rune(st.Note)) > 1000 ||
		(st.Kind == "document") != (st.Doc != "") {
		return i18n.E("err.bp_step")
	}
	for _, a := range st.Sets {
		if a.Field == "" || len([]rune(a.Value)) > 1000 {
			return i18n.E("err.bp_step")
		}
	}
	switch st.Kind {
	case "update":
		if !crm.Entities[entity] || len(st.Sets) == 0 {
			return i18n.E("err.bp_step")
		}
	case "comment", "document":
		if !crm.Entities[entity] || len(st.Sets) > 50 {
			return i18n.E("err.bp_step")
		}
	case "activity":
		if !crm.Entities[entity] || !slices.Contains(crm.ActivityKinds, st.Deed) {
			return i18n.E("err.bp_step")
		}
	case "create":
		if !crm.Entities[st.Entity] || len(st.Sets) > 50 {
			return i18n.E("err.bp_step")
		}
	case "mail":
		// the card's client is there only when the process is of a CRM card
		if strings.TrimSpace(st.Subject) == "" || len([]rune(st.Subject)) > 300 || len(st.Email) > 1000 ||
			st.To == "client" && !crm.Entities[entity] || st.To == "" && strings.TrimSpace(st.Email) == "" {
			return i18n.E("err.bp_step")
		}
	}
	return nil
}

// cardField is the key a step sets: a built-in one, or "uf:<id>" of the own field of that name.
func (s *Store) cardField(ctx context.Context, entity, name string) (string, error) {
	if contains(CardFields[entity], name) {
		return name, nil
	}
	set, ok := tasks.CRMFieldSets[entity]
	if !ok {
		return "", ErrNotFound
	}
	list, err := s.Tasks.FieldsOf(ctx, set)
	if err != nil {
		return "", err
	}
	for _, f := range list {
		if strings.EqualFold(f.Name, strings.TrimSpace(name)) {
			return "uf:" + f.ID, nil
		}
	}
	return "", ErrNotFound
}

// person is the employee a value means: starter, manager, responsible, an id, a name or an e-mail.
func (s *Store) person(ctx context.Context, x Instance, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "starter" || v == "manager" || v == "responsible" {
		return s.who(ctx, x, v)
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM users WHERE active AND (id::text = $1 OR lower(name) = lower($1) OR lower(email) = lower($1))
		ORDER BY id::text = $1 DESC LIMIT 1`, v).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("no employee %q", v)
	}
	return id, err
}

// card is a CRM card being changed or made: its built-in fields by key, and own ones apart.
type card struct {
	set func(key, v string) error
	own map[string]string
}

func number(v string) (float64, error) {
	if v == "" {
		return 0, nil
	}
	return strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(v, " ", ""), ",", "."), 64)
}

// apply sets the fields of the step on the card, the values filled.
func (s *Store) apply(ctx context.Context, x Instance, entity string, c card, sets []Assign) error {
	for _, a := range sets {
		key, err := s.cardField(ctx, entity, a.Field)
		if err != nil {
			return fmt.Errorf("no field %q", a.Field)
		}
		v := strings.TrimSpace(s.fillRaw(ctx, x, a.Value))
		if id, ok := strings.CutPrefix(key, "uf:"); ok {
			c.own[id] = v
			continue
		}
		if key == "responsible" {
			if v, err = s.person(ctx, x, v); err != nil {
				return err
			}
		}
		if err := c.set(key, v); err != nil {
			return fmt.Errorf("%s: %w", a.Field, err)
		}
	}
	return nil
}

// dealCard and the others open a card's built-in fields to a step.
func dealCard(d *crm.Deal) card {
	return card{own: map[string]string{}, set: func(key, v string) (err error) {
		switch key {
		case "title":
			d.Title = v
		case "amount":
			d.Amount, err = number(v)
		case "close_date":
			d.CloseDate = nil
			if v != "" {
				var t time.Time
				t, err = time.Parse(time.DateOnly, v)
				d.CloseDate = &t
			}
		case "responsible":
			d.Responsible = crm.Ref{ID: v}
		case "comment":
			d.Comment = v
		}
		return err
	}}
}

func leadCard(l *crm.Lead) card {
	return card{own: map[string]string{}, set: func(key, v string) (err error) {
		switch key {
		case "title":
			l.Title = v
		case "name":
			l.Name = v
		case "company_title":
			l.CompanyTitle = v
		case "phone":
			l.Phone = v
		case "email":
			l.Email = v
		case "amount":
			l.Amount, err = number(v)
		case "responsible":
			l.Responsible = crm.Ref{ID: v}
		case "comment":
			l.Comment = v
		}
		return err
	}}
}

func contactCard(c *crm.Contact) card {
	return card{own: map[string]string{}, set: func(key, v string) error {
		switch key {
		case "name":
			c.Name = v
		case "position":
			c.Position = v
		case "phone":
			c.Phone = v
		case "email":
			c.Email = v
		case "responsible":
			c.Responsible = crm.Ref{ID: v}
		case "comment":
			c.Comment = v
		}
		return nil
	}}
}

func companyCard(c *crm.Company) card {
	return card{own: map[string]string{}, set: func(key, v string) error {
		switch key {
		case "title":
			c.Title = v
		case "phone":
			c.Phone = v
		case "email":
			c.Email = v
		case "website":
			c.Website = v
		case "address":
			c.Address = v
		case "responsible":
			c.Responsible = crm.Ref{ID: v}
		case "comment":
			c.Comment = v
		}
		return nil
	}}
}

// updateCard sets the fields of the process's card, as its starter: the card's timeline shows what changed.
func (s *Store) updateCard(ctx context.Context, x Instance, sets []Assign) error {
	actor := x.StartedBy.ID
	var c card
	var save func() error
	switch x.Entity {
	case "deal":
		d, err := s.CRM.Deal(ctx, x.EntityID)
		if err != nil {
			return err
		}
		c, save = dealCard(&d), func() error { _, err := s.CRM.SaveDeal(ctx, actor, d); return err }
	case "lead":
		l, err := s.CRM.Lead(ctx, x.EntityID)
		if err != nil {
			return err
		}
		c, save = leadCard(&l), func() error { _, err := s.CRM.SaveLead(ctx, actor, l); return err }
	case "contact":
		k, err := s.CRM.Contact(ctx, x.EntityID)
		if err != nil {
			return err
		}
		c, save = contactCard(&k), func() error { _, err := s.CRM.SaveContact(ctx, actor, k); return err }
	case "company":
		k, err := s.CRM.Company(ctx, x.EntityID)
		if err != nil {
			return err
		}
		c, save = companyCard(&k), func() error { _, err := s.CRM.SaveCompany(ctx, actor, k); return err }
	default:
		return fmt.Errorf("no CRM card")
	}
	if err := s.apply(ctx, x, x.Entity, c, sets); err != nil {
		return err
	}
	if err := save(); err != nil {
		return err
	}
	if len(c.own) == 0 {
		return nil
	}
	return s.CRM.SetFields(ctx, actor, x.Entity, x.EntityID, c.own)
}

// createCard adds a card of the step's kind: the text is its title (a contact's name), To its responsible. A deal made
// by a process of a contact, a company or another deal gets their contact and company, a contact — the company. The
// new card's link goes into the process's values as {created_card}.
func (s *Store) createCard(ctx context.Context, x Instance, st Step, title string) (string, error) {
	actor := x.StartedBy.ID
	resp, err := s.who(ctx, x, st.To)
	if err != nil {
		return "", err
	}
	var contact, company crm.Ref // of the process's card
	switch x.Entity {
	case "deal":
		if d, err := s.CRM.Deal(ctx, x.EntityID); err == nil {
			contact, company = d.Contact, d.Company
		}
	case "contact":
		contact = crm.Ref{ID: x.EntityID}
		if k, err := s.CRM.Contact(ctx, x.EntityID); err == nil {
			company = k.Company
		}
	case "company":
		company = crm.Ref{ID: x.EntityID}
	}
	var c card
	var save func() (string, error)
	switch st.Entity {
	case "deal":
		pipes, err := s.CRM.Pipelines(ctx)
		if err != nil {
			return "", err
		}
		if len(pipes) == 0 {
			return "", fmt.Errorf("no pipeline")
		}
		d := crm.Deal{Title: title, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: resp}, Contact: contact, Company: company}
		c, save = dealCard(&d), func() (string, error) { return s.CRM.SaveDeal(ctx, actor, d) }
	case "lead":
		l := crm.Lead{Title: title, Responsible: crm.Ref{ID: resp}}
		c, save = leadCard(&l), func() (string, error) { return s.CRM.SaveLead(ctx, actor, l) }
	case "contact":
		k := crm.Contact{Name: title, Responsible: crm.Ref{ID: resp}, Company: company}
		c, save = contactCard(&k), func() (string, error) { return s.CRM.SaveContact(ctx, actor, k) }
	case "company":
		k := crm.Company{Title: title, Responsible: crm.Ref{ID: resp}}
		c, save = companyCard(&k), func() (string, error) { return s.CRM.SaveCompany(ctx, actor, k) }
	default:
		return "", fmt.Errorf("no CRM card")
	}
	if err := s.apply(ctx, x, st.Entity, c, st.Sets); err != nil {
		return "", err
	}
	id, err := save()
	if err != nil {
		return "", err
	}
	if len(c.own) > 0 {
		if err := s.CRM.SetFields(ctx, actor, st.Entity, id, c.own); err != nil {
			return id, err
		}
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	x.Values["created_card"] = CardPath(st.Entity, id)
	return id, s.saveValues(ctx, x)
}

func (s *Store) saveValues(ctx context.Context, x Instance) error {
	vals, _ := json.Marshal(x.Values)
	_, err := s.db.Exec(ctx, `UPDATE bp_instances SET values = $2 WHERE id = $1`, x.ID, vals)
	return err
}

// clientEmail is the e-mail of the card's client: a lead's, a contact's or a company's, a deal's contact's or else company's.
func (s *Store) clientEmail(ctx context.Context, x Instance) (string, error) {
	var to string
	q := map[string]string{
		"lead":    `SELECT email FROM crm_leads WHERE id::text = $1`,
		"contact": `SELECT email FROM crm_contacts WHERE id::text = $1`,
		"company": `SELECT email FROM crm_companies WHERE id::text = $1`,
		"deal": `SELECT coalesce(nullif(x.email, ''), c.email, '') FROM crm_deals d LEFT JOIN crm_contacts x ON x.id = d.contact_id
			LEFT JOIN crm_companies c ON c.id = d.company_id WHERE d.id::text = $1`,
	}[x.Entity]
	if q == "" {
		return "", fmt.Errorf("no CRM card")
	}
	if err := s.db.QueryRow(ctx, q, x.EntityID).Scan(&to); err != nil {
		return "", err
	}
	if to == "" {
		return "", i18n.E("err.robot_email")
	}
	return to, nil
}

// mail sends the step's letter from the mailbox of From: to To (the card's client or an employee) and the addresses.
func (s *Store) mail(ctx context.Context, x Instance, st Step, text string) (string, error) {
	if s.CRM.SendMail == nil {
		return "", i18n.E("err.robot_mailbox")
	}
	from, err := s.who(ctx, x, st.From)
	if err != nil {
		return "", err
	}
	var to []string
	switch st.To {
	case "":
	case "client":
		addr, err := s.clientEmail(ctx, x)
		if err != nil {
			return "", err
		}
		to = append(to, addr)
	default:
		user, err := s.who(ctx, x, st.To)
		if err != nil {
			return "", err
		}
		var addr string
		if err := s.db.QueryRow(ctx, `SELECT email FROM users WHERE id::text = $1`, user).Scan(&addr); err != nil {
			return "", err
		}
		if addr == "" {
			return "", i18n.E("err.robot_email")
		}
		to = append(to, addr)
	}
	for _, a := range strings.FieldsFunc(s.fill(ctx, x, st.Email), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		to = append(to, a)
	}
	if len(to) == 0 {
		return "", i18n.E("err.robot_email")
	}
	if text == "" {
		text = s.fill(ctx, x, st.Subject)
	}
	addr := strings.Join(to, ", ")
	return addr, s.CRM.SendMail(ctx, from, addr, s.fill(ctx, x, st.Subject), text)
}

// chat writes the text from From to each person of the step in the messenger; to oneself it is a notification.
func (s *Store) chat(ctx context.Context, x Instance, st Step, text string) ([]string, error) {
	if s.Chat == nil {
		return nil, i18n.E("err.bp_no_messenger")
	}
	from, err := s.who(ctx, x, st.From)
	if err != nil {
		return nil, err
	}
	users, err := s.people(ctx, x, st)
	if err != nil {
		return nil, err
	}
	for _, user := range users {
		if user == from {
			s.notify(ctx, user, from, x.ID, text)
			continue
		}
		if err := s.Chat(ctx, from, user, text); err != nil {
			return nil, err
		}
	}
	return users, nil
}

// activity plans a deed of the step's kind on the process's card, by its starter: the text is its subject, To its
// responsible; the deadline is the end of the working day Days days on.
func (s *Store) activity(ctx context.Context, x Instance, st Step, text string) (string, error) {
	user, err := s.who(ctx, x, st.To)
	if err != nil {
		return "", err
	}
	sched, err := s.Tasks.Schedule(ctx)
	if err != nil {
		return "", err
	}
	day := time.Now().AddDate(0, 0, st.Days)
	deadline := sched.Deadline(time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 0, 0, day.Location()))
	return s.CRM.AddActivity(ctx, x.StartedBy.ID, crm.Activity{Entity: x.Entity, EntityID: x.EntityID, Kind: st.Deed,
		Subject: text, Description: s.fill(ctx, x, st.Note), Deadline: deadline, Responsible: crm.Ref{ID: user}})
}

// document makes a document of the process's card by the step's template, of From: the placeholders of Sets ({…}
// filled) go over the card's values. The document's link goes into the process's values as {created_document}.
func (s *Store) document(ctx context.Context, x Instance, st Step) (string, error) {
	if s.Document == nil {
		return "", i18n.E("err.bp_no_docs")
	}
	author, err := s.who(ctx, x, cmp.Or(st.From, "starter"))
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	for _, a := range st.Sets {
		values[a.Field] = s.fill(ctx, x, a.Value)
	}
	id, err := s.Document(ctx, author, x.Entity, x.EntityID, st.Doc, values)
	if err != nil {
		return "", err
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	x.Values["created_document"] = CardPath(x.Entity, x.EntityID) + "/documents/" + id
	return id, s.saveValues(ctx, x)
}
