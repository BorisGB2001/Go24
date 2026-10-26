// Package crm is the CRM of go24: companies, contacts, leads and deals. Deals move along the
// stages of a pipeline («воронка»), leads along their own stages; a lead is converted into a deal (with a contact
// and a company). Every card keeps a timeline of comments and changes. Every employee works with the CRM;
// deleting is for the responsible, the author and administrators.
package crm

import (
	"cmp"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

type Store struct {
	db    *pgxpool.Pool
	Tasks *tasks.Store // robots make tasks with it (nil: task robots fail)
	// SendMail sends a letter from the user's mailbox (mail robots; nil: they fail)
	SendMail func(ctx context.Context, userID, to, subject, text string) error
	// BookingURL is the address of a booking link that is on («Онлайн-запись»; booking robots; nil: they fail)
	BookingURL func(ctx context.Context, linkID string) (string, error)
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Ref is a linked card or person: id and what to show.
type Ref struct{ ID, Name string }

type Company struct {
	ID, Title, Phone, Email, Website, Address, Comment string
	CompanyType, Industry                              Dict // «Тип компании», «Сфера деятельности»
	Responsible                                        Ref
	CreatedBy                                          string
	CreatedAt                                          time.Time
	Contacts, Deals                                    int
}

type Contact struct {
	ID, Name, Position, Phone, Email, Comment string
	ContactType, Origin                       Dict // «Тип контакта», «Источник»
	Company                                   Ref
	Responsible                               Ref
	CreatedBy                                 string
	CreatedAt                                 time.Time
}

// Stage of a pipeline or of leads; built-in ones have a Code (the name is in the locales).
type Stage struct {
	ID, Code, Name, Color, Semantic string // semantic: process | success | failure
}

// Closed: a final stage (won or lost).
func (s Stage) Closed() bool { return s.Semantic != "process" }

type Pipeline struct {
	ID, Code, Name string
	TypeID         string // a pipeline of a smart process; "" — of deals
	Stages         []Stage
}

type Deal struct {
	ID, Title, Currency, Comment string
	PipelineID                   string
	TypeID                       string // a card of a smart process; "" — a deal
	Stage                        Stage
	Amount                       float64
	DealType, Origin             Dict // «Тип сделки», «Источник»
	Company, Contact             Ref
	Contacts                     []Ref // all the contacts of the card, the main one (Contact) first; filled by Deal
	Responsible                  Ref
	Observers                    []Ref // «Наблюдатели», filled by Deal; nil on saving keeps them
	CloseDate                    *time.Time
	ClosedAt                     *time.Time
	CreatedBy                    string
	CreatedAt                    time.Time
}

type Lead struct {
	ID, Title, Name, CompanyTitle, Phone, Email, Currency, Comment string
	Source                                                         string // «Дополнительно об источнике»
	Origin                                                         Dict   // «Источник»
	Amount                                                         float64
	Stage                                                          Stage
	Responsible                                                    Ref
	Observers                                                      []Ref  // «Наблюдатели», as Deal's
	DealID, ContactID, CompanyID                                   string // converted into
	CreatedBy                                                      string
	CreatedAt                                                      time.Time
}

// Event is a line of a card's timeline.
type Event struct {
	ID         int64
	AuthorID   string
	AuthorName string
	Kind       string // comment | created | stage | converted | robot | document | mail | mailing | …
	Body       string
	At         time.Time
	Pinned     bool     // shown above the history too «Закреплённые»
	Edited     bool     // a comment changed after it was written
	Mail       *Mail    // Kind mail: the letter
	Mailing    *Mailing // Kind mailing: the campaign letter; nil — the campaign is deleted
	Booking    *Booking // Kind booking: the client's booking of «Онлайн-запись»; nil — it is deleted
}

// Booking is a booking of «Онлайн-запись» in a card's timeline, as it is now.
type Booking struct {
	ID, Resource, Status string
	Start, End           time.Time
}

// Mailing is a letter of a «Маркетинг» campaign sent to the card: the «E-mail рассылка» in the timeline.
type Mailing struct {
	CampaignID, Title, Subject, Email string
	Status, Error                     string // sent | error
	Opened, Clicked, Unsubscribed     *time.Time
}

// Mail is a letter in a card's timeline (its copy, crm_mail): the e-mail activity.
type Mail struct {
	LinkID, LetterID, OwnerID             string // LetterID "" — gone from the mailbox; OwnerID — whose mailbox
	Outgoing                              bool
	FromAddr, FromName, To, Subject, Body string
	SentAt                                time.Time
}

// From is how the sender is shown.
func (m Mail) From() string {
	if m.FromName != "" {
		return m.FromName
	}
	return m.FromAddr
}

func clean(s *string, max int) error {
	*s = strings.TrimSpace(*s)
	if len([]rune(*s)) > max {
		return i18n.E("err.crm_field")
	}
	return nil
}

// innSQL tells whether a requisite of the card (entity, its table's alias t) has an ИНН like $1.
func innSQL(entity, t string) string {
	return `EXISTS (SELECT 1 FROM crm_requisites rq WHERE rq.entity = '` + entity + `' AND rq.entity_id = ` + t + `.id AND rq.inn ILIKE $1)`
}

func like(q string) string {
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
}

// may: deleting is for the responsible, the author and administrators.
func may(ctx context.Context, userID, responsible, author string) error {
	if userID == responsible || userID == author || tasks.IsAdmin(ctx) {
		return nil
	}
	return ErrForbidden
}

func (s *Store) event(ctx context.Context, q interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, entity, id, author, kind, body string) error {
	_, err := q.Exec(ctx, `INSERT INTO crm_timeline (entity, entity_id, author_id, kind, body) VALUES ($1, $2, $3, $4, $5)`,
		entity, id, author, kind, body)
	return err
}

// AddEvent writes a line in a card's timeline from another section (an open line's dialogue bound to the card).
func (s *Store) AddEvent(ctx context.Context, entity, id, author, kind, body string) error {
	return s.event(ctx, s.db, entity, id, author, kind, body)
}

// Timeline of a card, newest first.
func (s *Store) Timeline(ctx context.Context, entity, id string) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.author_id, u.name, t.kind, t.body, t.created_at, m.id IS NOT NULL, coalesce(m.id::text, ''),
			coalesce(m.message_id::text, ''), coalesce(b.user_id::text, ''), coalesce(m.outgoing, false), coalesce(m.from_addr, ''),
			coalesce(m.from_name, ''), coalesce(m.to_addr, ''), coalesce(m.subject, ''), coalesce(m.body, ''), coalesce(m.sent_at, t.created_at),
			t.pinned_at IS NOT NULL, t.edited_at IS NOT NULL, r.id IS NOT NULL, coalesce(c.id::text, ''), coalesce(c.title, ''),
			coalesce(c.subject, ''), coalesce(r.email, ''), coalesce(r.status, ''), coalesce(r.error, ''), r.opened_at, r.clicked_at, r.unsubscribed_at,
			bk.id IS NOT NULL, coalesce(bk.id::text, ''), coalesce(br.name, ''), coalesce(bk.status, ''), coalesce(bk.starts_at, t.created_at),
			coalesce(bk.ends_at, t.created_at)
		FROM crm_timeline t JOIN users u ON u.id = t.author_id
		LEFT JOIN crm_mail m ON t.kind = 'mail' AND m.id::text = t.body LEFT JOIN mailboxes b ON b.id = m.mailbox_id
		LEFT JOIN mkt_recipients r ON t.kind = 'mailing' AND r.id::text = t.body LEFT JOIN mkt_campaigns c ON c.id = r.campaign_id
		LEFT JOIN bookings bk ON t.kind = 'booking' AND bk.id::text = t.body LEFT JOIN booking_resources br ON br.id = bk.resource_id
		WHERE t.entity = $1 AND t.entity_id::text = $2 ORDER BY t.id DESC LIMIT 200`, entity, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		var e Event
		var m Mail
		var g Mailing
		var bk Booking
		var isMail, isMailing, isBooking bool
		err := r.Scan(&e.ID, &e.AuthorID, &e.AuthorName, &e.Kind, &e.Body, &e.At, &isMail, &m.LinkID, &m.LetterID, &m.OwnerID, &m.Outgoing, &m.FromAddr,
			&m.FromName, &m.To, &m.Subject, &m.Body, &m.SentAt,
			&e.Pinned, &e.Edited, &isMailing, &g.CampaignID, &g.Title, &g.Subject, &g.Email, &g.Status, &g.Error, &g.Opened, &g.Clicked, &g.Unsubscribed,
			&isBooking, &bk.ID, &bk.Resource, &bk.Status, &bk.Start, &bk.End)
		if isBooking {
			e.Booking = &bk
		}
		if isMail {
			e.Mail = &m
		}
		if isMailing {
			e.Mailing = &g
		}
		return e, err
	})
}

// Comment adds a comment to a card's timeline.
func (s *Store) Comment(ctx context.Context, userID, entity, id, body string) error {
	if err := clean(&body, 10000); err != nil || body == "" {
		return i18n.E("err.comment_empty")
	}
	var title string
	err := s.db.QueryRow(ctx, `SELECT `+map[bool]string{true: "title", false: "''"}[watched(entity)]+` FROM crm_`+table(entity)+` WHERE id::text = $1`, id).Scan(&title)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := s.event(ctx, s.db, entity, id, userID, "comment", body); err != nil {
		return err
	}
	return s.tellWatchers(ctx, userID, entity, id, "crm_comment", title, true)
}

// Pin pins a line of a card's timeline (entity, id) above the history or unpins it.
func (s *Store) Pin(ctx context.Context, entity, id string, eventID int64, pin bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE crm_timeline SET pinned_at = CASE WHEN $4 THEN now() END
		WHERE entity = $1 AND entity_id::text = $2 AND id = $3`, entity, id, eventID, pin)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// comment is a comment of a card's timeline that the person may change: its author or an administrator.
func (s *Store) comment(ctx context.Context, userID, entity, id string, eventID int64) error {
	var author string
	err := s.db.QueryRow(ctx, `SELECT author_id::text FROM crm_timeline WHERE entity = $1 AND entity_id::text = $2 AND id = $3 AND kind = 'comment'`,
		entity, id, eventID).Scan(&author)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if author != userID && !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	return nil
}

// EditComment changes the text of a comment; it is marked edited.
func (s *Store) EditComment(ctx context.Context, userID, entity, id string, eventID int64, body string) error {
	if err := clean(&body, 10000); err != nil || body == "" {
		return i18n.E("err.comment_empty")
	}
	if err := s.comment(ctx, userID, entity, id, eventID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE crm_timeline SET body = $2, edited_at = now() WHERE id = $1 AND body <> $2`, eventID, body)
	return err
}

// DeleteComment removes a comment from a card's timeline.
func (s *Store) DeleteComment(ctx context.Context, userID, entity, id string, eventID int64) error {
	if err := s.comment(ctx, userID, entity, id, eventID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM crm_timeline WHERE id = $1`, eventID)
	return err
}

func table(entity string) string {
	switch entity {
	case "company":
		return "companies"
	case "contact":
		return "contacts"
	case "deal":
		return "deals"
	}
	return "leads"
}

// ---------- companies ----------

var companySQL = `SELECT c.id, c.title, c.phone, c.email, c.website, c.address, c.comment, c.responsible_id, r.name,
		c.created_by::text, c.created_at,
		(SELECT count(*) FROM crm_contacts x WHERE x.company_id = c.id)::int, (SELECT count(*) FROM crm_deals d WHERE d.company_id = c.id)::int,
		` + dictCols("jt") + `, ` + dictCols("ji") + `
	FROM crm_companies c JOIN users r ON r.id = c.responsible_id` + dictJoin("jt", "c.company_type_id") + dictJoin("ji", "c.industry_id")

func scanCompanies(rows pgx.Rows, err error) ([]Company, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Company, error) {
		var c Company
		err := r.Scan(&c.ID, &c.Title, &c.Phone, &c.Email, &c.Website, &c.Address, &c.Comment, &c.Responsible.ID, &c.Responsible.Name,
			&c.CreatedBy, &c.CreatedAt, &c.Contacts, &c.Deals, &c.CompanyType.ID, &c.CompanyType.Code, &c.CompanyType.Name,
			&c.Industry.ID, &c.Industry.Code, &c.Industry.Name)
		return c, err
	})
}

// Companies are the companies matching q (title, phone, e-mail, ИНН) the viewer may read, newest first.
func (s *Store) Companies(ctx context.Context, viewer, q string) ([]Company, error) {
	sc, err := s.viewScope(ctx, viewer, "company", "c", 2)
	if err != nil {
		return nil, err
	}
	return scanCompanies(s.db.Query(ctx, companySQL+` WHERE (c.title ILIKE $1 OR c.phone ILIKE $1 OR c.email ILIKE $1 OR `+innSQL("company", "c")+`) AND `+sc+`
		ORDER BY c.created_at DESC LIMIT 200`, like(q), viewer))
}

func (s *Store) Company(ctx context.Context, id string) (Company, error) {
	list, err := scanCompanies(s.db.Query(ctx, companySQL+` WHERE c.id::text = $1`, id))
	if err != nil {
		return Company{}, err
	}
	if len(list) == 0 {
		return Company{}, ErrNotFound
	}
	return list[0], nil
}

// SaveCompany creates (ID "") or changes a company; it gives the id.
func (s *Store) SaveCompany(ctx context.Context, userID string, c Company) (string, error) {
	for _, f := range []*string{&c.Title, &c.Phone, &c.Email, &c.Website, &c.Address} {
		if err := clean(f, 300); err != nil {
			return "", err
		}
	}
	if err := clean(&c.Comment, 10000); err != nil {
		return "", err
	}
	if c.Title == "" {
		return "", i18n.E("err.crm_title")
	}
	if c.Responsible.ID == "" {
		c.Responsible.ID = userID
	}
	typ, err := s.dictItem(ctx, "company_type", c.CompanyType)
	if err != nil {
		return "", err
	}
	industry, err := s.dictItem(ctx, "industry", c.Industry)
	if err != nil {
		return "", err
	}
	if c.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO crm_companies (title, phone, email, website, address, comment, responsible_id, created_by,
				company_type_id, industry_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`, c.Title, c.Phone, c.Email, c.Website, c.Address, c.Comment,
			c.Responsible.ID, userID, typ, industry).Scan(&c.ID)
		if err != nil {
			return "", err
		}
		return c.ID, s.event(ctx, s.db, "company", c.ID, userID, "created", "")
	}
	old, err := s.Company(ctx, c.ID)
	if err != nil {
		return "", err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_companies SET title = $2, phone = $3, email = $4, website = $5, address = $6, comment = $7,
		responsible_id = $8, company_type_id = $9, industry_id = $10, updated_at = now() WHERE id::text = $1`, c.ID, c.Title, c.Phone, c.Email,
		c.Website, c.Address, c.Comment, c.Responsible.ID, typ, industry)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err == nil {
		var nc Company
		if nc, err = s.Company(ctx, c.ID); err == nil {
			err = s.LogChanges(ctx, userID, "company", c.ID, companyChanges(old, nc))
		}
	}
	return c.ID, err
}

func (s *Store) DeleteCompany(ctx context.Context, userID, id string) error {
	c, err := s.Company(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Check(ctx, userID, "company", "delete", c.Responsible.ID); err != nil && may(ctx, userID, c.Responsible.ID, c.CreatedBy) != nil {
		return err
	}
	return s.trash(ctx, userID, "company", id, c.Title, "", c.Responsible.ID, `WITH q AS (DELETE FROM crm_requisites
		WHERE entity = 'company' AND entity_id::text = $1) DELETE FROM crm_companies WHERE id::text = $1`)
}

// ---------- contacts ----------

var contactSQL = `SELECT x.id, x.name, x.position, x.phone, x.email, x.comment, coalesce(x.company_id::text, ''), coalesce(c.title, ''),
		x.responsible_id, r.name, x.created_by::text, x.created_at, ` + dictCols("jt") + `, ` + dictCols("js") + `
	FROM crm_contacts x JOIN users r ON r.id = x.responsible_id LEFT JOIN crm_companies c ON c.id = x.company_id` +
	dictJoin("jt", "x.contact_type_id") + dictJoin("js", "x.source_id")

func scanContacts(rows pgx.Rows, err error) ([]Contact, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Contact, error) {
		var x Contact
		err := r.Scan(&x.ID, &x.Name, &x.Position, &x.Phone, &x.Email, &x.Comment, &x.Company.ID, &x.Company.Name,
			&x.Responsible.ID, &x.Responsible.Name, &x.CreatedBy, &x.CreatedAt, &x.ContactType.ID, &x.ContactType.Code, &x.ContactType.Name,
			&x.Origin.ID, &x.Origin.Code, &x.Origin.Name)
		return x, err
	})
}

// Contacts matching q (name, phone, e-mail, ИНН) the viewer may read, of one company when companyID is given.
func (s *Store) Contacts(ctx context.Context, viewer, q, companyID string) ([]Contact, error) {
	sc, err := s.viewScope(ctx, viewer, "contact", "x", 3)
	if err != nil {
		return nil, err
	}
	return scanContacts(s.db.Query(ctx, contactSQL+` WHERE (x.name ILIKE $1 OR x.phone ILIKE $1 OR x.email ILIKE $1 OR `+innSQL("contact", "x")+`)
		AND ($2 = '' OR x.company_id::text = $2) AND `+sc+` ORDER BY x.created_at DESC LIMIT 200`, like(q), companyID, viewer))
}

func (s *Store) Contact(ctx context.Context, id string) (Contact, error) {
	list, err := scanContacts(s.db.Query(ctx, contactSQL+` WHERE x.id::text = $1`, id))
	if err != nil {
		return Contact{}, err
	}
	if len(list) == 0 {
		return Contact{}, ErrNotFound
	}
	return list[0], nil
}

func optional(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

// ClientContact is the contact of a client who came from another section («Онлайн-запись»): the one with the phone or
// e-mail, else a new one of resp with the origin; made tells it is new.
func (s *Store) ClientContact(ctx context.Context, resp, name, phone, email string, origin Dict) (id string, made bool, err error) {
	if phone != "" || email != "" {
		dups, err := s.Duplicates(ctx, "contact", "", "", phone, email)
		if err != nil {
			return "", false, err
		}
		for _, d := range dups {
			if d.Entity == "contact" {
				return d.ID, false, nil
			}
		}
	}
	if name = strings.TrimSpace(name); name == "" {
		name = cmp.Or(email, phone)
	}
	id, err = s.SaveContact(ctx, resp, Contact{Name: name, Phone: phone, Email: email, Origin: origin, Responsible: Ref{ID: resp}})
	return id, err == nil, err
}

func (s *Store) SaveContact(ctx context.Context, userID string, x Contact) (string, error) {
	for _, f := range []*string{&x.Name, &x.Position, &x.Phone, &x.Email} {
		if err := clean(f, 300); err != nil {
			return "", err
		}
	}
	if err := clean(&x.Comment, 10000); err != nil {
		return "", err
	}
	if x.Name == "" {
		return "", i18n.E("err.crm_name")
	}
	if x.Responsible.ID == "" {
		x.Responsible.ID = userID
	}
	typ, err := s.dictItem(ctx, "contact_type", x.ContactType)
	if err != nil {
		return "", err
	}
	origin, err := s.dictItem(ctx, "source", x.Origin)
	if err != nil {
		return "", err
	}
	if x.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO crm_contacts (name, position, phone, email, company_id, comment, responsible_id, created_by,
				contact_type_id, source_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`, x.Name, x.Position, x.Phone, x.Email, optional(x.Company.ID), x.Comment,
			x.Responsible.ID, userID, typ, origin).Scan(&x.ID)
		if err != nil {
			return "", err
		}
		return x.ID, s.event(ctx, s.db, "contact", x.ID, userID, "created", "")
	}
	old, err := s.Contact(ctx, x.ID)
	if err != nil {
		return "", err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_contacts SET name = $2, position = $3, phone = $4, email = $5, company_id = $6, comment = $7,
		responsible_id = $8, contact_type_id = $9, source_id = $10, updated_at = now() WHERE id::text = $1`, x.ID, x.Name, x.Position, x.Phone,
		x.Email, optional(x.Company.ID), x.Comment, x.Responsible.ID, typ, origin)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err == nil {
		var nx Contact
		if nx, err = s.Contact(ctx, x.ID); err == nil {
			err = s.LogChanges(ctx, userID, "contact", x.ID, contactChanges(old, nx))
		}
	}
	return x.ID, err
}

func (s *Store) DeleteContact(ctx context.Context, userID, id string) error {
	x, err := s.Contact(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Check(ctx, userID, "contact", "delete", x.Responsible.ID); err != nil && may(ctx, userID, x.Responsible.ID, x.CreatedBy) != nil {
		return err
	}
	if err = s.trash(ctx, userID, "contact", id, x.Name, "", x.Responsible.ID, `WITH q AS (DELETE FROM crm_requisites
		WHERE entity = 'contact' AND entity_id::text = $1) DELETE FROM crm_contacts WHERE id::text = $1`); err != nil {
		return err
	}
	return s.promoteContacts(ctx)
}

// promoteContacts: a deal whose main contact is gone gets the next of its contacts as the main one.
func (s *Store) promoteContacts(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `UPDATE crm_deals d SET contact_id = (SELECT dc.contact_id FROM crm_deal_contacts dc
		WHERE dc.deal_id = d.id ORDER BY dc.sort LIMIT 1) WHERE d.contact_id IS NULL AND EXISTS (SELECT 1 FROM crm_deal_contacts dc WHERE dc.deal_id = d.id)`)
	return err
}

// ---------- pipelines and stages ----------

const stageCols = `st.id, coalesce(st.code, ''), st.name, st.color, st.semantic`

// Pipelines are the deal pipelines with their stages.
func (s *Store) Pipelines(ctx context.Context) ([]Pipeline, error) { return s.PipelinesOf(ctx, "") }

// PipelinesOf are the pipelines of a smart process (typeID "": of deals) with their stages.
func (s *Store) PipelinesOf(ctx context.Context, typeID string) ([]Pipeline, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id, coalesce(p.code, ''), p.name, coalesce(p.type_id::text, ''), `+stageCols+`
		FROM crm_pipelines p JOIN crm_stages st ON st.pipeline_id = p.id WHERE coalesce(p.type_id::text, '') = $1
		ORDER BY p.sort, p.created_at, st.sort`, typeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pipeline
	for rows.Next() {
		var p Pipeline
		var st Stage
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.TypeID, &st.ID, &st.Code, &st.Name, &st.Color, &st.Semantic); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != p.ID {
			out = append(out, p)
		}
		out[len(out)-1].Stages = append(out[len(out)-1].Stages, st)
	}
	return out, rows.Err()
}

// LeadStages are the stages of leads.
func (s *Store) LeadStages(ctx context.Context) ([]Stage, error) {
	rows, err := s.db.Query(ctx, `SELECT `+stageCols+` FROM crm_stages st WHERE st.entity = 'lead' ORDER BY st.sort`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Stage])
}

// stage is a stage of the entity (and pipeline, for deals).
func (s *Store) stage(ctx context.Context, entity, pipelineID, id string) (Stage, error) {
	var st Stage
	err := s.db.QueryRow(ctx, `SELECT `+stageCols+` FROM crm_stages st WHERE st.id::text = $1 AND st.entity = $2
		AND ($3 = '' OR st.pipeline_id::text = $3)`, id, entity, pipelineID).Scan(&st.ID, &st.Code, &st.Name, &st.Color, &st.Semantic)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, i18n.E("err.crm_stage")
	}
	return st, err
}

// stageText is what the timeline keeps of a stage: the code of a built-in one not renamed, else the name.
func stageText(st Stage) string {
	if st.Code != "" && st.Name == "" {
		return "code:" + st.Code
	}
	return st.Name
}

// ---------- deals ----------

var dealSQL = `SELECT d.id, d.title, d.currency, d.comment, d.pipeline_id, coalesce(dp.type_id::text, ''), ` + stageCols + `, d.amount::float8,
		coalesce(d.company_id::text, ''), coalesce(c.title, ''), coalesce(d.contact_id::text, ''), coalesce(x.name, ''),
		d.responsible_id, r.name, d.close_date, d.closed_at, d.created_by::text, d.created_at, ` + dictCols("jt") + `, ` + dictCols("js") + `
	FROM crm_deals d JOIN crm_pipelines dp ON dp.id = d.pipeline_id JOIN crm_stages st ON st.id = d.stage_id JOIN users r ON r.id = d.responsible_id
	LEFT JOIN crm_companies c ON c.id = d.company_id LEFT JOIN crm_contacts x ON x.id = d.contact_id` +
	dictJoin("jt", "d.deal_type_id") + dictJoin("js", "d.source_id")

func scanDeals(rows pgx.Rows, err error) ([]Deal, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Deal, error) {
		var d Deal
		err := r.Scan(&d.ID, &d.Title, &d.Currency, &d.Comment, &d.PipelineID, &d.TypeID, &d.Stage.ID, &d.Stage.Code, &d.Stage.Name, &d.Stage.Color,
			&d.Stage.Semantic, &d.Amount, &d.Company.ID, &d.Company.Name, &d.Contact.ID, &d.Contact.Name, &d.Responsible.ID,
			&d.Responsible.Name, &d.CloseDate, &d.ClosedAt, &d.CreatedBy, &d.CreatedAt, &d.DealType.ID, &d.DealType.Code, &d.DealType.Name,
			&d.Origin.ID, &d.Origin.Code, &d.Origin.Name)
		return d, err
	})
}

// Deals of a pipeline matching q the viewer may read; with companyID or contactID — of that card (any pipeline).
func (s *Store) Deals(ctx context.Context, viewer, pipelineID, q, companyID, contactID string) ([]Deal, error) {
	sc, err := s.viewScope(ctx, viewer, "deal", "d", 5)
	if err != nil {
		return nil, err
	}
	return scanDeals(s.db.Query(ctx, dealSQL+` WHERE ($1 = '' OR d.pipeline_id::text = $1) AND d.title ILIKE $2
		AND ($3 = '' OR d.company_id::text = $3) AND ($4 = '' OR d.contact_id::text = $4 OR EXISTS (SELECT 1 FROM crm_deal_contacts dc
			WHERE dc.deal_id = d.id AND dc.contact_id::text = $4)) AND `+sc+`
		ORDER BY d.created_at DESC LIMIT 500`, pipelineID, like(q), companyID, contactID, viewer))
}

func (s *Store) Deal(ctx context.Context, id string) (Deal, error) {
	list, err := scanDeals(s.db.Query(ctx, dealSQL+` WHERE d.id::text = $1`, id))
	if err != nil {
		return Deal{}, err
	}
	if len(list) == 0 {
		return Deal{}, ErrNotFound
	}
	d := list[0]
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.name FROM crm_deal_contacts dc JOIN crm_contacts x ON x.id = dc.contact_id
		WHERE dc.deal_id = $1 ORDER BY x.id <> $2::uuid, dc.sort, x.name`, d.ID, optional(d.Contact.ID))
	if err != nil {
		return Deal{}, err
	}
	if d.Contacts, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ref, error) {
		var c Ref
		return c, r.Scan(&c.ID, &c.Name)
	}); err != nil {
		return Deal{}, err
	}
	d.Observers, err = s.Observers(ctx, "deal", d.ID)
	return d, err
}

// dealContacts: the card's contacts without blanks and repeats; Contact alone when Contacts is not given.
func dealContacts(d *Deal) []string {
	ids := []string{}
	seen := map[string]bool{}
	list := d.Contacts
	if list == nil && d.Contact.ID != "" {
		list = []Ref{d.Contact}
	}
	for _, c := range list {
		if c.ID != "" && !seen[c.ID] {
			seen[c.ID] = true
			ids = append(ids, c.ID)
		}
	}
	d.Contact = Ref{}
	if len(ids) > 0 {
		d.Contact.ID = ids[0]
	}
	return ids
}

// setDealContacts puts the deal's contacts in their order.
func (s *Store) setDealContacts(ctx context.Context, dealID string, ids []string) error {
	_, err := s.db.Exec(ctx, `WITH del AS (DELETE FROM crm_deal_contacts WHERE deal_id = $1::uuid AND NOT (contact_id::text = ANY($2)))
		INSERT INTO crm_deal_contacts (deal_id, contact_id, sort) SELECT $1::uuid, c::uuid, n FROM unnest($2::text[]) WITH ORDINALITY AS u(c, n)
		ON CONFLICT (deal_id, contact_id) DO UPDATE SET sort = excluded.sort`, dealID, ids)
	return err
}

// SaveDeal creates (ID "": in the pipeline's first stage unless one is given) or changes a deal.
func (s *Store) SaveDeal(ctx context.Context, userID string, d Deal) (string, error) {
	if err := clean(&d.Title, 300); err != nil {
		return "", err
	}
	if err := clean(&d.Comment, 10000); err != nil {
		return "", err
	}
	if d.Title == "" {
		return "", i18n.E("err.crm_title")
	}
	if d.Amount < 0 || d.Amount > 1e15 {
		return "", i18n.E("err.crm_amount")
	}
	if err := s.currency(ctx, &d.Currency); err != nil {
		return "", err
	}
	if d.Responsible.ID == "" {
		d.Responsible.ID = userID
	}
	typ, err := s.dictItem(ctx, "deal_type", d.DealType)
	if err != nil {
		return "", err
	}
	origin, err := s.dictItem(ctx, "source", d.Origin)
	if err != nil {
		return "", err
	}
	contacts := dealContacts(&d)
	if d.ID == "" {
		if d.Stage.ID == "" {
			if err := s.db.QueryRow(ctx, `SELECT id FROM crm_stages WHERE pipeline_id::text = $1 ORDER BY sort LIMIT 1`, d.PipelineID).
				Scan(&d.Stage.ID); err != nil {
				return "", i18n.E("err.crm_stage")
			}
		}
		st, err := s.stage(ctx, "deal", d.PipelineID, d.Stage.ID)
		if err != nil {
			return "", err
		}
		var closed *time.Time
		if st.Closed() {
			now := time.Now()
			closed = &now
		}
		err = s.db.QueryRow(ctx, `INSERT INTO crm_deals (title, pipeline_id, stage_id, amount, currency, company_id, contact_id,
				responsible_id, close_date, comment, closed_at, created_by, deal_type_id, source_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`, d.Title, d.PipelineID, st.ID, d.Amount, d.Currency,
			optional(d.Company.ID), optional(d.Contact.ID), d.Responsible.ID, d.CloseDate, d.Comment, closed, userID, typ, origin).Scan(&d.ID)
		if err != nil {
			return "", err
		}
		if err := s.setDealContacts(ctx, d.ID, contacts); err != nil {
			return "", err
		}
		if err := s.setObservers(ctx, userID, "deal", d.ID, d.Title, d.Observers); err != nil {
			return "", err
		}
		if err := s.event(ctx, s.db, "deal", d.ID, userID, "created", ""); err != nil {
			return "", err
		}
		if nd, err := s.Deal(ctx, d.ID); err == nil {
			s.runRobots(ctx, userID, st.ID, dealCard(nd))
		}
		return d.ID, nil
	}
	old, err := s.Deal(ctx, d.ID)
	if err != nil {
		return "", err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_deals SET title = $2, amount = `+rowsAmount("deal", "$3")+`, currency = $4, company_id = $5, contact_id = $6,
		responsible_id = $7, close_date = $8, comment = $9, deal_type_id = $10, source_id = $11, updated_at = now() WHERE id::text = $1`,
		d.ID, d.Title, d.Amount, d.Currency, optional(d.Company.ID), optional(d.Contact.ID), d.Responsible.ID, d.CloseDate, d.Comment, typ, origin)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err == nil {
		err = s.setDealContacts(ctx, d.ID, contacts)
	}
	if err == nil {
		err = s.setObservers(ctx, userID, "deal", d.ID, d.Title, d.Observers)
	}
	if err == nil {
		var nd Deal
		if nd, err = s.Deal(ctx, d.ID); err == nil {
			err = s.LogChanges(ctx, userID, "deal", d.ID, dealChanges(old, nd))
		}
	}
	return d.ID, err
}

// MoveDeal puts the deal into another stage of its pipeline (the kanban); a final stage closes it.
func (s *Store) MoveDeal(ctx context.Context, userID, id, stageID string) (Deal, error) {
	d, err := s.Deal(ctx, id)
	if err != nil {
		return d, err
	}
	st, err := s.stage(ctx, "deal", d.PipelineID, stageID)
	pipeline := d.PipelineID
	if err != nil { // a stage of another pipeline of the same kind of cards: the deal moves there («Сменить воронку»)
		var from, to string
		if e := s.db.QueryRow(ctx, `SELECT st.pipeline_id::text, coalesce(nullif(o.name, ''), 'code:' || o.code), coalesce(nullif(p.name, ''), 'code:' || p.code)
			FROM crm_stages st JOIN crm_pipelines p ON p.id = st.pipeline_id JOIN crm_pipelines o ON o.id::text = $2
			WHERE st.id::text = $1 AND st.entity = 'deal' AND p.type_id IS NOT DISTINCT FROM o.type_id`, stageID, d.PipelineID).Scan(&pipeline, &from, &to); e != nil {
			return d, err
		}
		if st, err = s.stage(ctx, "deal", pipeline, stageID); err != nil {
			return d, err
		}
		if err := s.event(ctx, s.db, "deal", id, userID, "pipeline", from+" → "+to); err != nil {
			return d, err
		}
	}
	if st.ID == d.Stage.ID {
		return d, nil
	}
	if _, err := s.db.Exec(ctx, `UPDATE crm_deals SET stage_id = $2, pipeline_id = $4, updated_at = now(),
		closed_at = CASE WHEN $3 THEN coalesce(closed_at, now()) END WHERE id = $1`, id, st.ID, st.Closed(), pipeline); err != nil {
		return d, err
	}
	if err := s.event(ctx, s.db, "deal", id, userID, "stage", stageText(d.Stage)+" → "+stageText(st)); err != nil {
		return d, err
	}
	if err := s.tellWatchers(ctx, userID, "deal", id, "crm_stage", d.Title, false); err != nil {
		return d, err
	}
	if st.Semantic == "success" && d.Stage.Semantic != "success" {
		_ = s.shipOnWin(ctx, userID, id) // the deal is won anyway; what didn't ship stays to be shipped by hand
	}
	d, err = s.Deal(ctx, id)
	if err == nil {
		s.runRobots(ctx, userID, st.ID, dealCard(d))
	}
	return d, err
}

func (s *Store) DeleteDeal(ctx context.Context, userID, id string) error {
	d, err := s.Deal(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Check(ctx, userID, "deal", "delete", d.Responsible.ID); err != nil && may(ctx, userID, d.Responsible.ID, d.CreatedBy) != nil {
		return err
	}
	return s.trash(ctx, userID, "deal", id, d.Title, d.TypeID, d.Responsible.ID, `DELETE FROM crm_deals WHERE id::text = $1`)
}

// ---------- leads ----------

var leadSQL = `SELECT l.id, l.title, l.name, l.company_title, l.phone, l.email, l.source, l.currency, l.comment, l.amount::float8,
		` + stageCols + `, l.responsible_id, r.name, coalesce(l.deal_id::text, ''), coalesce(l.contact_id::text, ''),
		coalesce(l.company_id::text, ''), l.created_by::text, l.created_at, ` + dictCols("js") + `
	FROM crm_leads l JOIN crm_stages st ON st.id = l.stage_id JOIN users r ON r.id = l.responsible_id` + dictJoin("js", "l.source_id")

func scanLeads(rows pgx.Rows, err error) ([]Lead, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Lead, error) {
		var l Lead
		err := r.Scan(&l.ID, &l.Title, &l.Name, &l.CompanyTitle, &l.Phone, &l.Email, &l.Source, &l.Currency, &l.Comment, &l.Amount,
			&l.Stage.ID, &l.Stage.Code, &l.Stage.Name, &l.Stage.Color, &l.Stage.Semantic, &l.Responsible.ID, &l.Responsible.Name,
			&l.DealID, &l.ContactID, &l.CompanyID, &l.CreatedBy, &l.CreatedAt, &l.Origin.ID, &l.Origin.Code, &l.Origin.Name)
		return l, err
	})
}

// Leads matching q (title, name, phone, e-mail, company) the viewer may read.
func (s *Store) Leads(ctx context.Context, viewer, q string) ([]Lead, error) {
	return s.LeadList(ctx, viewer, Filter{Q: q})
}

func (s *Store) Lead(ctx context.Context, id string) (Lead, error) {
	list, err := scanLeads(s.db.Query(ctx, leadSQL+` WHERE l.id::text = $1`, id))
	if err != nil {
		return Lead{}, err
	}
	if len(list) == 0 {
		return Lead{}, ErrNotFound
	}
	l := list[0]
	l.Observers, err = s.Observers(ctx, "lead", l.ID)
	return l, err
}

func (s *Store) SaveLead(ctx context.Context, userID string, l Lead) (string, error) {
	for _, f := range []*string{&l.Title, &l.Name, &l.CompanyTitle, &l.Phone, &l.Email, &l.Source} {
		if err := clean(f, 300); err != nil {
			return "", err
		}
	}
	if err := clean(&l.Comment, 10000); err != nil {
		return "", err
	}
	if l.Title == "" {
		return "", i18n.E("err.crm_title")
	}
	if l.Amount < 0 || l.Amount > 1e15 {
		return "", i18n.E("err.crm_amount")
	}
	if err := s.currency(ctx, &l.Currency); err != nil {
		return "", err
	}
	if l.Responsible.ID == "" {
		l.Responsible.ID = userID
	}
	origin, err := s.dictItem(ctx, "source", l.Origin)
	if err != nil {
		return "", err
	}
	if l.ID == "" {
		if l.Stage.ID != "" { // the kanban's «+» of a column; a lead becomes «Качественный» only by conversion
			st, err := s.stage(ctx, "lead", "", l.Stage.ID)
			if err != nil {
				return "", err
			}
			if st.Semantic == "success" {
				return "", i18n.E("err.crm_lead_convert")
			}
		}
		err := s.db.QueryRow(ctx, `INSERT INTO crm_leads (title, name, company_title, phone, email, source, amount, currency, stage_id,
				responsible_id, comment, created_by, source_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, coalesce($13::uuid, (SELECT id FROM crm_stages WHERE entity = 'lead' ORDER BY sort LIMIT 1)),
				$9, $10, $11, $12)
			RETURNING id`, l.Title, l.Name, l.CompanyTitle, l.Phone, l.Email, l.Source, l.Amount, l.Currency, l.Responsible.ID, l.Comment, userID,
			origin, optional(l.Stage.ID)).Scan(&l.ID)
		if err != nil {
			return "", err
		}
		if err := s.setObservers(ctx, userID, "lead", l.ID, l.Title, l.Observers); err != nil {
			return "", err
		}
		if err := s.event(ctx, s.db, "lead", l.ID, userID, "created", ""); err != nil {
			return "", err
		}
		if nl, err := s.Lead(ctx, l.ID); err == nil {
			s.runRobots(ctx, userID, nl.Stage.ID, leadCard(nl))
		}
		return l.ID, nil
	}
	old, err := s.Lead(ctx, l.ID)
	if err != nil {
		return "", err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_leads SET title = $2, name = $3, company_title = $4, phone = $5, email = $6, source = $7,
		amount = `+rowsAmount("lead", "$8")+`, currency = $9, responsible_id = $10, comment = $11, source_id = $12, updated_at = now()
		WHERE id::text = $1`, l.ID, l.Title, l.Name, l.CompanyTitle, l.Phone, l.Email, l.Source, l.Amount, l.Currency, l.Responsible.ID, l.Comment, origin)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err == nil {
		err = s.setObservers(ctx, userID, "lead", l.ID, l.Title, l.Observers)
	}
	if err == nil {
		var nl Lead
		if nl, err = s.Lead(ctx, l.ID); err == nil {
			err = s.LogChanges(ctx, userID, "lead", l.ID, leadChanges(old, nl))
		}
	}
	return l.ID, err
}

// MoveLead puts the lead into another stage. The success stage is reached by converting it (ConvertLead).
func (s *Store) MoveLead(ctx context.Context, userID, id, stageID string) (Lead, error) {
	l, err := s.Lead(ctx, id)
	if err != nil {
		return l, err
	}
	st, err := s.stage(ctx, "lead", "", stageID)
	if err != nil || st.ID == l.Stage.ID {
		return l, err
	}
	if st.Semantic == "success" && l.DealID == "" {
		return l, i18n.E("err.crm_convert_first")
	}
	if _, err := s.db.Exec(ctx, `UPDATE crm_leads SET stage_id = $2, updated_at = now() WHERE id = $1`, id, st.ID); err != nil {
		return l, err
	}
	if err := s.event(ctx, s.db, "lead", id, userID, "stage", stageText(l.Stage)+" → "+stageText(st)); err != nil {
		return l, err
	}
	if err := s.tellWatchers(ctx, userID, "lead", id, "crm_stage", l.Title, false); err != nil {
		return l, err
	}
	l, err = s.Lead(ctx, id)
	if err == nil {
		s.runRobots(ctx, userID, st.ID, leadCard(l))
	}
	return l, err
}

func (s *Store) DeleteLead(ctx context.Context, userID, id string) error {
	l, err := s.Lead(ctx, id)
	if err != nil {
		return err
	}
	if err := s.Check(ctx, userID, "lead", "delete", l.Responsible.ID); err != nil && may(ctx, userID, l.Responsible.ID, l.CreatedBy) != nil {
		return err
	}
	return s.trash(ctx, userID, "lead", id, l.Title, "", l.Responsible.ID, `DELETE FROM crm_leads WHERE id::text = $1`)
}

// Amount formats a sum: 1 500 000,50.
func Amount(v float64) string {
	whole := int64(v)
	cents := int64((v-float64(whole))*100 + 0.5)
	s := strconv.FormatInt(whole, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	if cents > 0 {
		b.WriteString("," + strconv.FormatInt(100+cents, 10)[1:])
	}
	return b.String()
}
