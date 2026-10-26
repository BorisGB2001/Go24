package crm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «CRM-формы»: a form clients fill on its page or on a site; each filling makes a card ----------

// FormBuiltin are the fields a form may ask besides the custom fields of the card, in their usual order.
var FormBuiltin = []string{"name", "last_name", "phone", "email", "company", "position", "comment"}

// FormSchemes is what a filling makes: a lead, or a contact (with the company it names) and a deal.
var FormSchemes = []string{"lead", "deal"}

// Form is a «CRM-форма».
type Form struct {
	ID, Token, Name string
	Active          bool
	Scheme          string // lead | deal
	PipelineID      string // the deal's (scheme deal); "" — the first one
	Responsible     Ref    // who gets the cards; "" — the form's creator
	Title, Text     string // above the fields
	Button          string // the button's text; "" — the usual one
	Success         string // shown after sending; "" — the usual one
	Redirect        string // where the client goes after sending instead
	Consent         string // «Согласие на обработку персональных данных»: a required tick with this text; "" — none
	Fields          []FormField
	Results         int
	CreatedBy       string
	CreatedAt       time.Time
}

// FormField is a field of a form: Key — one of FormBuiltin or "f:<custom field id>" of the card the scheme makes.
type FormField struct {
	Key      string `json:"key"`
	Label    string `json:"label,omitempty"` // "" — the field's own name
	Required bool   `json:"required,omitempty"`
	// filled by FormFields: the custom field's name, kind and items
	Name    string   `json:"-"`
	Kind    string   `json:"-"`
	Options []string `json:"-"`
}

// Custom: the id of the custom field the form field is; "" — a built-in one.
func (f FormField) Custom() string {
	if id, ok := strings.CutPrefix(f.Key, "f:"); ok {
		return id
	}
	return ""
}

// FormAnswer is a field of a filling as the client gave it (Label — as the form showed it).
type FormAnswer struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// FormResult is a filling of a form and the cards it made.
type FormResult struct {
	ID                     string
	Answers                []FormAnswer
	LeadID, ContactID      string
	DealID, IP, Page       string
	LeadTitle, ContactName string
	DealTitle              string
	CreatedAt              time.Time
}

var (
	ErrFormRequired = i18n.E("err.crm_form_required")
	ErrFormEmail    = i18n.E("err.crm_form_email")
	ErrFormConsent  = i18n.E("err.crm_form_consent")
)

const formCols = `f.id::text, f.token, f.name, f.active, f.scheme, coalesce(f.pipeline_id::text, ''),
	coalesce(f.responsible_id::text, ''), coalesce(u.name, ''), f.title, f.text, f.button, f.success, f.redirect, f.consent,
	f.fields, (SELECT count(*) FROM crm_form_results r WHERE r.form_id = f.id), coalesce(f.created_by::text, ''), f.created_at
	FROM crm_forms f LEFT JOIN users u ON u.id = f.responsible_id`

func scanForm(row pgx.Row) (Form, error) {
	var f Form
	var fields []byte
	err := row.Scan(&f.ID, &f.Token, &f.Name, &f.Active, &f.Scheme, &f.PipelineID, &f.Responsible.ID, &f.Responsible.Name,
		&f.Title, &f.Text, &f.Button, &f.Success, &f.Redirect, &f.Consent, &fields, &f.Results, &f.CreatedBy, &f.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(fields, &f.Fields)
	}
	return f, err
}

// Forms are all the forms, by name.
func (s *Store) Forms(ctx context.Context) ([]Form, error) {
	rows, err := s.db.Query(ctx, `SELECT `+formCols+` ORDER BY lower(f.name), f.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Form, error) { return scanForm(r) })
}

// Form is one form by its id.
func (s *Store) Form(ctx context.Context, id string) (Form, error) {
	return scanForm(s.db.QueryRow(ctx, `SELECT `+formCols+` WHERE f.id::text = $1`, id))
}

// FormByToken is the form of a public link; a switched off one is not found.
func (s *Store) FormByToken(ctx context.Context, token string) (Form, error) {
	return scanForm(s.db.QueryRow(ctx, `SELECT `+formCols+` WHERE f.token = $1 AND f.active AND $1 <> ''`, token))
}

// FormFieldSet is the set of custom fields a scheme's card has: the lead's, or the deal's.
func FormFieldSet(scheme string) tasks.FieldSet {
	if scheme == "deal" {
		return tasks.CRMFieldSets["deal"]
	}
	return tasks.CRMFieldSets["lead"]
}

func (s *Store) tasks() *tasks.Store {
	if s.Tasks != nil {
		return s.Tasks
	}
	return tasks.NewStore(s.db)
}

// FormFields fills the custom fields of the form with their name, kind and items, and leaves out the fields that are
// gone (a custom field deleted, or one of the other card after the scheme changed).
func (s *Store) FormFields(ctx context.Context, f *Form) error {
	defs, err := s.tasks().FieldsOf(ctx, FormFieldSet(f.Scheme))
	if err != nil {
		return err
	}
	keep := f.Fields[:0]
	for _, x := range f.Fields {
		if id := x.Custom(); id != "" {
			i := slices.IndexFunc(defs, func(d tasks.Field) bool { return d.ID == id })
			if i < 0 {
				continue
			}
			x.Name, x.Kind, x.Options = defs[i].Name, defs[i].Kind, defs[i].Options
		} else if !slices.Contains(FormBuiltin, x.Key) {
			continue
		}
		keep = append(keep, x)
	}
	f.Fields = keep
	return nil
}

// Labeled gives the fields without their own label the field's name: a custom field's, or tr("crm.form_field.<key>").
func (f *Form) Labeled(tr func(key string) string) {
	for i, x := range f.Fields {
		switch {
		case x.Label != "":
		case x.Custom() != "":
			f.Fields[i].Label = x.Name
		default:
			f.Fields[i].Label = tr("crm.form_field." + x.Key)
		}
	}
}

func formToken() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// SaveForm creates (no ID) or changes a form: a name, at least one field of the card and an address of the web to go
// to after sending, if any.
func (s *Store) SaveForm(ctx context.Context, userID string, f Form) (string, error) {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || !slices.Contains(FormSchemes, f.Scheme) {
		return "", i18n.E("err.bad_form")
	}
	if f.Redirect = strings.TrimSpace(f.Redirect); f.Redirect != "" {
		if u, err := url.Parse(f.Redirect); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", i18n.E("err.crm_form_redirect")
		}
	}
	// the fields the card of the scheme has, each once (those of the other card stay behind when the scheme changes)
	probe := f
	probe.Fields = slices.Clone(f.Fields)
	if err := s.FormFields(ctx, &probe); err != nil {
		return "", err
	}
	f.Fields = nil
	for _, x := range probe.Fields {
		if !slices.ContainsFunc(f.Fields, func(o FormField) bool { return o.Key == x.Key }) {
			f.Fields = append(f.Fields, x)
		}
	}
	if len(f.Fields) == 0 {
		return "", i18n.E("err.crm_form_no_fields")
	}
	if f.Scheme != "deal" {
		f.PipelineID = ""
	}
	fields, err := json.Marshal(f.Fields)
	if err != nil {
		return "", err
	}
	args := []any{f.Name, f.Active, f.Scheme, f.PipelineID, f.Responsible.ID, f.Title, f.Text, f.Button, f.Success, f.Redirect,
		f.Consent, fields}
	if f.ID == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO crm_forms (name, active, scheme, pipeline_id, responsible_id, title, text, button,
			success, redirect, consent, fields, token, created_by)
			VALUES ($1, $2, $3, nullif($4, '')::uuid, nullif($5, '')::uuid, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id::text`,
			append(args, formToken(), userID)...).Scan(&f.ID)
		return f.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_forms SET name = $1, active = $2, scheme = $3, pipeline_id = nullif($4, '')::uuid,
		responsible_id = nullif($5, '')::uuid, title = $6, text = $7, button = $8, success = $9, redirect = $10, consent = $11,
		fields = $12, updated_at = now() WHERE id::text = $13`, append(args, f.ID)...)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return f.ID, err
}

// DeleteForm deletes a form with its results (the cards stay).
func (s *Store) DeleteForm(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_forms WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

const (
	formMaxAnswer  = 300
	formMaxComment = 5000
)

// Answer checks a client's answers (key → value) to the form's fields — the required ones filled, at least one, the
// e-mail an address, the custom ones of their kind — and the consent ticked when the form asks it.
func (f Form) Answer(get func(key string) string, consent bool) ([]FormAnswer, error) {
	var out []FormAnswer
	for _, x := range f.Fields {
		v, max := strings.TrimSpace(strings.ReplaceAll(get(x.Key), "\r\n", "\n")), formMaxAnswer
		if x.Key == "comment" {
			max = formMaxComment
		} else {
			v = strings.ReplaceAll(v, "\n", " ")
		}
		if r := []rune(v); len(r) > max {
			v = string(r[:max])
		}
		if v == "" {
			if x.Required {
				return nil, i18n.E("err.crm_form_field_required", x.Label)
			}
			continue
		}
		if x.Key == "email" {
			if a, err := mail.ParseAddress(v); err != nil || a.Address != v {
				return nil, ErrFormEmail
			}
		}
		if x.Custom() != "" {
			n, err := (tasks.Field{Name: x.Label, Kind: x.Kind, Options: x.Options}).Check(v)
			if err != nil {
				return nil, err
			}
			v = n
		}
		out = append(out, FormAnswer{x.Key, x.Label, v})
	}
	if len(out) == 0 {
		return nil, ErrFormRequired
	}
	if f.Consent != "" && !consent {
		return nil, ErrFormConsent
	}
	return out, nil
}

// FormFilling is what the web layer adds to the answers: the cards' title and comment in the portal's language, the
// client's address and the page where the form was filled.
type FormFilling struct {
	Title, Comment, Source string
	IP, Page               string
}

// SubmitForm makes the cards of a filling — a lead, or a contact (a known one with the phone or e-mail; the company
// it names, found by its name or new) with a deal in the form's pipeline — keeps the filling among the results, fires
// the trigger «Заполнение CRM-формы» and tells the responsible. The cards are the responsible's (the form's creator's,
// the first employee's when none of them is active).
func (s *Store) SubmitForm(ctx context.Context, f Form, ans []FormAnswer, x FormFilling) (FormResult, error) {
	var resp string
	if err := s.db.QueryRow(ctx, `SELECT id::text FROM users WHERE active AND NOT guest
		ORDER BY id::text = $1 DESC, id::text = $2 DESC, created_at LIMIT 1`, f.Responsible.ID, f.CreatedBy).Scan(&resp); err != nil {
		return FormResult{}, err
	}
	get, custom := map[string]string{}, map[string]string{}
	for _, a := range ans {
		if id, ok := strings.CutPrefix(a.Key, "f:"); ok {
			custom[id] = a.Value
		} else {
			get[a.Key] = a.Value
		}
	}
	name := strings.TrimSpace(get["name"] + " " + get["last_name"])
	comment := get["comment"]
	if f.Scheme != "deal" { // a lead has no «Должность»: it goes to the comment
		for _, a := range ans {
			if a.Key == "position" {
				comment = strings.TrimSpace(a.Label + ": " + a.Value + "\n" + comment)
			}
		}
	}
	if x.Comment != "" {
		comment = strings.TrimSpace(comment + "\n\n" + x.Comment)
	}
	res := FormResult{Answers: ans, IP: x.IP, Page: x.Page}
	origin := Dict{Code: "WEBFORM"}
	var entity, id string
	var err error
	if f.Scheme == "deal" {
		if res.ContactID, err = s.formContact(ctx, resp, get, name, comment, origin); err != nil {
			return res, err
		}
		var company string
		s.db.QueryRow(ctx, `SELECT coalesce(company_id::text, '') FROM crm_contacts WHERE id::text = $1`, res.ContactID).Scan(&company)
		pipe := f.PipelineID
		if pipe == "" {
			if list, err := s.Pipelines(ctx); err == nil && len(list) > 0 {
				pipe = list[0].ID
			}
		}
		res.DealID, err = s.SaveDeal(ctx, resp, Deal{Title: x.Title, PipelineID: pipe, Comment: comment, Origin: origin,
			Company: Ref{ID: company}, Contacts: []Ref{{ID: res.ContactID}}, Responsible: Ref{ID: resp}})
		entity, id = "deal", res.DealID
	} else {
		res.LeadID, err = s.SaveLead(ctx, resp, Lead{Title: x.Title, Name: name, CompanyTitle: get["company"], Phone: get["phone"],
			Email: get["email"], Comment: comment, Source: x.Source, Origin: origin, Responsible: Ref{ID: resp}})
		entity, id = "lead", res.LeadID
	}
	if err != nil {
		return res, err
	}
	if len(custom) > 0 {
		if err := s.tasks().SetFieldValuesOf(ctx, FormFieldSet(f.Scheme), id, custom); err != nil {
			return res, err
		}
	}
	answers, err := json.Marshal(ans)
	if err != nil {
		return res, err
	}
	if err := s.db.QueryRow(ctx, `INSERT INTO crm_form_results (form_id, answers, lead_id, contact_id, deal_id, ip, page)
		VALUES ($1, $2, nullif($3, '')::uuid, nullif($4, '')::uuid, nullif($5, '')::uuid, $6, left($7, 2000)) RETURNING id::text, created_at`,
		f.ID, answers, res.LeadID, res.ContactID, res.DealID, x.IP, x.Page).Scan(&res.ID, &res.CreatedAt); err != nil {
		return res, err
	}
	if err := s.Fire(ctx, entity, id, "form"); err != nil {
		return res, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'crm_form', left($2, 300), $3)`,
		resp, x.Title, "/crm/"+plural[entity]+"/"+id)
	return res, err
}

// formContact is the contact of a filling: the one with its phone or e-mail, else a new one (with the company it
// names: the one of that name, else a new one).
func (s *Store) formContact(ctx context.Context, resp string, get map[string]string, name, comment string, origin Dict) (string, error) {
	if get["phone"] != "" || get["email"] != "" {
		dups, err := s.Duplicates(ctx, "contact", "", "", get["phone"], get["email"])
		if err != nil {
			return "", err
		}
		for _, d := range dups {
			if d.Entity == "contact" {
				return d.ID, nil
			}
		}
	}
	var company string
	if t := get["company"]; t != "" {
		err := s.db.QueryRow(ctx, `SELECT id::text FROM crm_companies WHERE lower(title) = lower($1) ORDER BY created_at LIMIT 1`, t).Scan(&company)
		if errors.Is(err, pgx.ErrNoRows) {
			company, err = s.SaveCompany(ctx, resp, Company{Title: t, Responsible: Ref{ID: resp}})
		}
		if err != nil {
			return "", err
		}
	}
	if name == "" {
		name = get["email"]
		if name == "" {
			name = get["phone"]
		}
	}
	return s.SaveContact(ctx, resp, Contact{Name: name, Position: get["position"], Phone: get["phone"], Email: get["email"],
		Comment: comment, Origin: origin, Company: Ref{ID: company}, Responsible: Ref{ID: resp}})
}

// FormResults are the fillings of a form, newest first, with the cards they made (those still there).
func (s *Store) FormResults(ctx context.Context, formID string, limit int) ([]FormResult, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id::text, r.answers, coalesce(r.lead_id::text, ''), coalesce(r.contact_id::text, ''),
		coalesce(r.deal_id::text, ''), r.ip, r.page, coalesce(l.title, ''), coalesce(c.name, ''), coalesce(d.title, ''), r.created_at
		FROM crm_form_results r LEFT JOIN crm_leads l ON l.id = r.lead_id LEFT JOIN crm_contacts c ON c.id = r.contact_id
			LEFT JOIN crm_deals d ON d.id = r.deal_id
		WHERE r.form_id::text = $1 ORDER BY r.created_at DESC LIMIT $2`, formID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (FormResult, error) {
		var r FormResult
		var answers []byte
		err := row.Scan(&r.ID, &answers, &r.LeadID, &r.ContactID, &r.DealID, &r.IP, &r.Page, &r.LeadTitle, &r.ContactName,
			&r.DealTitle, &r.CreatedAt)
		if err == nil {
			err = json.Unmarshal(answers, &r.Answers)
		}
		return r, err
	})
}
