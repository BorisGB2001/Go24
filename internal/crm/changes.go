package crm

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"go24/internal/tasks"
)

// The history of a card, as in the portal timeline: an edit leaves «Изменено поле: Сумма 100 → 200» for each changed
// field. Values are kept as they read then; a built-in item of a dictionary or a stage is kept by its code ("code:…",
// translated when shown), a date as "date:2006-01-02".

// Change is a field changed by an edit: its key (the locale crm.f.<key>, or "uf:Name" — an own field) and the values.
type Change struct {
	Field string `json:"f"`
	Old   string `json:"o"`
	New   string `json:"n"`
}

// changeClip is how much of a long value (a comment) the history keeps.
const changeClip = 300

// Changes reads the body of a "changed" event.
func Changes(body string) []Change {
	var list []Change
	_ = json.Unmarshal([]byte(body), &list)
	return list
}

// LogChanges writes the changes of a card into its timeline as one event (none — nothing).
func (s *Store) LogChanges(ctx context.Context, userID, entity, id string, list []Change) error {
	if len(list) == 0 {
		return nil
	}
	for i := range list {
		list[i].Old, list[i].New = clip(list[i].Old), clip(list[i].New)
	}
	body, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.event(ctx, s.db, entity, id, userID, "changed", string(body))
}

func clip(v string) string {
	if utf8.RuneCountInString(v) <= changeClip {
		return v
	}
	return string([]rune(v)[:changeClip]) + "…"
}

// differ collects the changed fields.
type differ []Change

func (d *differ) add(field, old, next string) {
	if old != next {
		*d = append(*d, Change{field, old, next})
	}
}

func dictValue(v Dict) string {
	if v.Name == "" && v.Code != "" {
		return "code:" + v.Code
	}
	return v.Name
}

func dateValue(t *time.Time) string {
	if t == nil {
		return ""
	}
	return "date:" + t.Format(time.DateOnly)
}

func refNames(list []Ref) string {
	names := make([]string, len(list))
	for i, r := range list {
		names[i] = r.Name
	}
	return strings.Join(names, ", ")
}

func dealChanges(a, b Deal) []Change {
	var d differ
	d.add("title", a.Title, b.Title)
	d.add("amount", Amount(a.Amount), Amount(b.Amount))
	d.add("currency", a.Currency, b.Currency)
	d.add("stage", stageText(a.Stage), stageText(b.Stage))
	d.add("deal_type", dictValue(a.DealType), dictValue(b.DealType))
	d.add("origin", dictValue(a.Origin), dictValue(b.Origin))
	d.add("company", a.Company.Name, b.Company.Name)
	d.add("contacts", refNames(a.Contacts), refNames(b.Contacts))
	d.add("responsible", a.Responsible.Name, b.Responsible.Name)
	d.add("observers", refNames(a.Observers), refNames(b.Observers))
	d.add("close_date", dateValue(a.CloseDate), dateValue(b.CloseDate))
	d.add("comment", a.Comment, b.Comment)
	return d
}

func leadChanges(a, b Lead) []Change {
	var d differ
	d.add("title", a.Title, b.Title)
	d.add("name", a.Name, b.Name)
	d.add("company_title", a.CompanyTitle, b.CompanyTitle)
	d.add("phone", commsText(a.Comms, "phone", a.Phone), commsText(b.Comms, "phone", b.Phone))
	d.add("email", commsText(a.Comms, "email", a.Email), commsText(b.Comms, "email", b.Email))
	d.add("origin", dictValue(a.Origin), dictValue(b.Origin))
	d.add("source_info", a.Source, b.Source)
	d.add("amount", Amount(a.Amount), Amount(b.Amount))
	d.add("currency", a.Currency, b.Currency)
	d.add("responsible", a.Responsible.Name, b.Responsible.Name)
	d.add("observers", refNames(a.Observers), refNames(b.Observers))
	d.add("comment", a.Comment, b.Comment)
	return d
}

func contactChanges(a, b Contact) []Change {
	var d differ
	d.add("name", a.Name, b.Name)
	d.add("position", a.Position, b.Position)
	d.add("phone", commsText(a.Comms, "phone", a.Phone), commsText(b.Comms, "phone", b.Phone))
	d.add("email", commsText(a.Comms, "email", a.Email), commsText(b.Comms, "email", b.Email))
	d.add("company", a.Company.Name, b.Company.Name)
	d.add("contact_type", dictValue(a.ContactType), dictValue(b.ContactType))
	d.add("origin", dictValue(a.Origin), dictValue(b.Origin))
	d.add("responsible", a.Responsible.Name, b.Responsible.Name)
	d.add("comment", a.Comment, b.Comment)
	return d
}

func companyChanges(a, b Company) []Change {
	var d differ
	d.add("title", a.Title, b.Title)
	d.add("phone", commsText(a.Comms, "phone", a.Phone), commsText(b.Comms, "phone", b.Phone))
	d.add("email", commsText(a.Comms, "email", a.Email), commsText(b.Comms, "email", b.Email))
	d.add("website", a.Website, b.Website)
	d.add("address", a.Address, b.Address)
	d.add("company_type", dictValue(a.CompanyType), dictValue(b.CompanyType))
	d.add("industry", dictValue(a.Industry), dictValue(b.Industry))
	d.add("responsible", a.Responsible.Name, b.Responsible.Name)
	d.add("comment", a.Comment, b.Comment)
	return d
}

// SetFields writes a card's own fields (field id → value; "" clears, absent ones stay) and logs what changed.
func (s *Store) SetFields(ctx context.Context, userID, entity, id string, values map[string]string) error {
	set, ts := tasks.CRMFieldSets[entity], s.Tasks
	if ts == nil {
		ts = tasks.NewStore(s.db)
	}
	before, err := ts.FieldValuesOf(ctx, set, id)
	if err != nil {
		return err
	}
	if err := ts.SetFieldValuesOf(ctx, set, id, values); err != nil {
		return err
	}
	after, err := ts.FieldValuesOf(ctx, set, id)
	if err != nil {
		return err
	}
	old := map[string]string{}
	for _, v := range before {
		old[v.ID] = fieldValue(v)
	}
	var d differ
	for _, v := range after {
		d.add("uf:"+v.Name, old[v.ID], fieldValue(v))
	}
	return s.LogChanges(ctx, userID, entity, id, d)
}

func fieldValue(v tasks.FieldValue) string {
	if v.Kind == "date" && v.Value != "" {
		return "date:" + v.Value
	}
	return v.Value
}
