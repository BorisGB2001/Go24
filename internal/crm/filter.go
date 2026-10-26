package crm

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// The filter of the deal and lead lists (and kanbans), as the portal filter has it: search, stage or its kind
// (in work, won, lost), responsible, created between dates, amount between sums; and the order of the list.

// Filter of a list of deals or leads; zero fields don't filter.
type Filter struct {
	Q, Stage, Responsible string
	Source                string // the id of the «Источник» item
	Observer              string // the cards this person observes («Я наблюдатель»)
	Semantic              string // process | success | failure | closed (success or failure)
	From, To              *time.Time
	AmountMin, AmountMax  *float64
	Sort                  string // created (default) | title | amount | stage | responsible
	Asc                   bool
}

// sortCols: the list's columns one may sort by.
var sortCols = map[string]string{"title": "%s.title", "amount": "%s.amount", "stage": "st.sort", "responsible": "r.name", "created": "%s.created_at"}

// where adds the filter's conditions (beyond the search) to the arguments; alias is the table's alias of the entity.
func (f Filter) where(entity, alias string, args *[]any) string {
	arg := func(v any) string {
		*args = append(*args, v)
		return "$" + strconv.Itoa(len(*args))
	}
	w := ""
	if f.Stage != "" {
		w += ` AND ` + alias + `.stage_id::text = ` + arg(f.Stage)
	}
	if f.Semantic == "closed" { // won or lost
		w += ` AND st.semantic <> 'process'`
	} else if f.Semantic != "" {
		w += ` AND st.semantic = ` + arg(f.Semantic)
	}
	if f.Responsible != "" {
		w += ` AND ` + alias + `.responsible_id::text = ` + arg(f.Responsible)
	}
	if f.Source != "" {
		w += ` AND ` + alias + `.source_id::text = ` + arg(f.Source)
	}
	if f.Observer != "" {
		w += ` AND ` + observerSQL(entity, alias, arg(f.Observer))
	}
	if f.From != nil {
		w += ` AND ` + alias + `.created_at >= ` + arg(*f.From)
	}
	if f.To != nil {
		w += ` AND ` + alias + `.created_at < ` + arg(f.To.AddDate(0, 0, 1))
	}
	if f.AmountMin != nil {
		w += ` AND ` + alias + `.amount >= ` + arg(*f.AmountMin)
	}
	if f.AmountMax != nil {
		w += ` AND ` + alias + `.amount <= ` + arg(*f.AmountMax)
	}
	return w
}

// order is the ORDER BY of the filter's sort.
func (f Filter) order(alias string) string {
	col, ok := sortCols[f.Sort]
	if !ok {
		col = sortCols["created"]
	}
	dir := " DESC"
	if f.Asc {
		dir = " ASC"
	}
	if col[0] == '%' {
		col = alias + col[2:]
	}
	return " ORDER BY " + col + dir + ", " + alias + ".created_at DESC"
}

// DealList: the deals of a pipeline (all, with pipelineID "") the viewer may read, filtered and ordered.
func (s *Store) DealList(ctx context.Context, viewer, pipelineID string, f Filter) ([]Deal, error) {
	sc, err := s.viewScope(ctx, viewer, "deal", "d", 3)
	if err != nil {
		return nil, err
	}
	args := []any{pipelineID, like(f.Q), viewer}
	cond := f.where("deal", "d", &args)
	return scanDeals(s.db.Query(ctx, dealSQL+` WHERE ($1 = '' OR d.pipeline_id::text = $1)
		AND (d.title ILIKE $2 OR coalesce(c.title, '') ILIKE $2 OR coalesce(x.name, '') ILIKE $2) AND (`+sc+`)`+cond+f.order("d")+` LIMIT 500`, args...))
}

// LeadList: the leads the viewer may read, filtered and ordered.
func (s *Store) LeadList(ctx context.Context, viewer string, f Filter) ([]Lead, error) {
	sc, err := s.viewScope(ctx, viewer, "lead", "l", 2)
	if err != nil {
		return nil, err
	}
	args := []any{like(f.Q), viewer}
	cond := f.where("lead", "l", &args)
	return scanLeads(s.db.Query(ctx, leadSQL+` WHERE (l.title ILIKE $1 OR l.name ILIKE $1 OR l.phone ILIKE $1 OR l.email ILIKE $1
		OR l.company_title ILIKE $1) AND (`+sc+`)`+cond+f.order("l")+` LIMIT 500`, args...))
}

// SetResponsible gives a deal or a lead another responsible.
func (s *Store) SetResponsible(ctx context.Context, userID, entity, id, responsibleID string) error {
	if entity != "deal" && entity != "lead" {
		return ErrNotFound
	}
	var old, next string
	err := s.db.QueryRow(ctx, `WITH o AS (SELECT r.name FROM crm_`+plural[entity]+` c JOIN users r ON r.id = c.responsible_id WHERE c.id::text = $1)
		UPDATE crm_`+plural[entity]+` SET responsible_id = u.id, updated_at = now() FROM users u, o
		WHERE crm_`+plural[entity]+`.id::text = $1 AND u.id::text = $2 RETURNING o.name, u.name`, id, responsibleID).Scan(&old, &next)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var d differ
	d.add("responsible", old, next)
	return s.LogChanges(ctx, userID, entity, id, d)
}
