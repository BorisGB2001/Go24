package marketing

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Рекламные источники» and «Сквозная аналитика»: the UTM tags a lead or a deal came with, the
// company's advertising channels (the utm_source values and the CRM «Источник» that are a channel's) and what was
// spent on them a day; the report sets the expenses of a period against the leads, deals and revenue of each channel.

// UTM are the tags of the address a client came by.
type UTM struct {
	Source, Medium, Campaign, Content, Term string
}

// UTMKeys are the parameters of an address, in UTM's order.
var UTMKeys = []string{"utm_source", "utm_medium", "utm_campaign", "utm_content", "utm_term"}

const maxUTM = 200

func (u UTM) IsZero() bool { return u == UTM{} }

func (u *UTM) fields() []*string { return []*string{&u.Source, &u.Medium, &u.Campaign, &u.Content, &u.Term} }

// Clean trims the tags and cuts them to 200 characters.
func (u UTM) Clean() UTM {
	for _, f := range u.fields() {
		*f = strings.TrimSpace(*f)
		if r := []rune(*f); len(r) > maxUTM {
			*f = string(r[:maxUTM])
		}
	}
	return u
}

// Values are the tags as utm_* parameters (the empty left out).
func (u UTM) Values() url.Values {
	v := url.Values{}
	for i, f := range u.fields() {
		if *f != "" {
			v.Set(UTMKeys[i], *f)
		}
	}
	return v
}

// ParseUTM takes the utm_* parameters of an address or a form.
func ParseUTM(get func(string) string) UTM {
	var u UTM
	for i, f := range u.fields() {
		*f = get(UTMKeys[i])
	}
	return u.Clean()
}

var utmCol = map[string]string{"lead": "lead_id", "deal": "deal_id"}

// UTMOf are the tags of a lead or a deal; a deal without its own has those of the lead it was made of (inherited).
func (s *Store) UTMOf(ctx context.Context, entity, id string) (u UTM, inherited bool, err error) {
	col, ok := utmCol[entity]
	if !ok {
		return u, false, nil
	}
	q := `SELECT source, medium, campaign, content, term, false FROM mkt_utm WHERE ` + col + `::text = $1`
	if entity == "deal" {
		q += ` UNION ALL (SELECT u.source, u.medium, u.campaign, u.content, u.term, true FROM crm_leads l JOIN mkt_utm u ON u.lead_id = l.id
			WHERE l.deal_id::text = $1 ORDER BY l.created_at LIMIT 1)`
	}
	err = s.db.QueryRow(ctx, q+` ORDER BY 6 LIMIT 1`, id).Scan(&u.Source, &u.Medium, &u.Campaign, &u.Content, &u.Term, &inherited)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return u, inherited, err
}

// SetUTM keeps the tags of a lead or a deal; no tags remove them.
func (s *Store) SetUTM(ctx context.Context, entity, id string, u UTM) error {
	col, ok := utmCol[entity]
	if !ok {
		return ErrNotFound
	}
	if u = u.Clean(); u.IsZero() {
		_, err := s.db.Exec(ctx, `DELETE FROM mkt_utm WHERE `+col+`::text = $1`, id)
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mkt_utm (`+col+`, source, medium, campaign, content, term) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (`+col+`) DO UPDATE SET source = $2, medium = $3, campaign = $4, content = $5, term = $6`,
		id, u.Source, u.Medium, u.Campaign, u.Content, u.Term)
	return err
}

// AdSource is an advertising channel of the company.
type AdSource struct {
	ID, Name  string
	UTM       []string // the utm_source values of the channel
	CRMSource string   // the id of the «Источник» item of the cards without UTM tags
	Color     string
	Sort      int
}

// AdSources are the channels in their order.
func (s *Store) AdSources(ctx context.Context) ([]AdSource, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, utm, coalesce(crm_source::text, ''), color, sort FROM mkt_ad_sources ORDER BY sort, lower(name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[AdSource])
}

// SplitUTM reads the utm_source values of a channel: separated by commas, spaces or lines; lower case, once each.
func SplitUTM(text string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, v := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\r' || r == '\t' }) {
		if r := []rune(v); len(r) <= maxUTM && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// SaveAdSource adds a channel (a.ID "") or changes one.
func (s *Store) SaveAdSource(ctx context.Context, a AdSource) (string, error) {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len([]rune(a.Name)) > maxName || len(a.UTM) > 50 || (a.Color != "" && !validColor(a.Color)) {
		return "", i18n.E("err.mkt_adsource")
	}
	if a.UTM == nil {
		a.UTM = []string{}
	}
	var src any
	if a.CRMSource != "" {
		src = a.CRMSource
	}
	if a.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO mkt_ad_sources (name, utm, crm_source, color, sort)
			VALUES ($1, $2, $3, $4, coalesce((SELECT max(sort) + 10 FROM mkt_ad_sources), 10)) RETURNING id::text`, a.Name, a.UTM, src, a.Color).Scan(&a.ID)
		return a.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mkt_ad_sources SET name = $2, utm = $3, crm_source = $4, color = $5 WHERE id::text = $1`, a.ID, a.Name, a.UTM, src, a.Color)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return a.ID, err
}

func validColor(c string) bool {
	if len(c) != 7 || c[0] != '#' {
		return false
	}
	_, err := strconv.ParseUint(c[1:], 16, 32)
	return err == nil
}

// DeleteAdSource removes a channel with its expenses.
func (s *Store) DeleteAdSource(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mkt_ad_sources WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Expense is what was spent on a channel a day.
type Expense struct {
	ID, SourceID, SourceName string
	Day                      time.Time
	Amount                   float64
	Currency                 string
	Campaign, Comment        string // Campaign — the utm_campaign it was spent on
	CreatedBy, AuthorName    string
}

// Expenses are those of the days [from, to] (zero — no bound) of a channel ("" — of all), the latest first.
func (s *Store) Expenses(ctx context.Context, sourceID string, from, to time.Time, limit int) ([]Expense, error) {
	rows, err := s.db.Query(ctx, `SELECT e.id::text, e.source_id::text, a.name, e.day, e.amount::float8, e.currency, e.campaign, e.comment,
			coalesce(e.created_by::text, ''), coalesce(u.name, '')
		FROM mkt_ad_expenses e JOIN mkt_ad_sources a ON a.id = e.source_id LEFT JOIN users u ON u.id = e.created_by
		WHERE ($1 = '' OR e.source_id::text = $1) AND ($2::date IS NULL OR e.day >= $2) AND ($3::date IS NULL OR e.day <= $3)
		ORDER BY e.day DESC, e.created_at DESC LIMIT $4`, sourceID, dateArg(from), dateArg(to), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Expense])
}

func dateArg(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.Format(time.DateOnly)
}

// currency checks a currency of the CRM list; empty is the base one.
func (s *Store) currency(ctx context.Context, code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return s.CRM.BaseCurrency(ctx)
	}
	rates, err := s.CRM.Rates(ctx)
	if err != nil {
		return "", err
	}
	if _, ok := rates[code]; !ok {
		return "", i18n.E("err.crm_currency")
	}
	return code, nil
}

func (e *Expense) clean() error {
	e.Campaign, e.Comment = strings.TrimSpace(e.Campaign), strings.TrimSpace(e.Comment)
	if e.SourceID == "" || e.Day.IsZero() || !(e.Amount >= 0 && e.Amount < 1e15) || math.IsNaN(e.Amount) ||
		len([]rune(e.Campaign)) > maxUTM || len([]rune(e.Comment)) > 1000 {
		return i18n.E("err.mkt_expense")
	}
	e.Amount = math.Round(e.Amount*100) / 100
	return nil
}

// AddExpense writes down an expense.
func (s *Store) AddExpense(ctx context.Context, userID string, e Expense) (string, error) {
	if err := e.clean(); err != nil {
		return "", err
	}
	cur, err := s.currency(ctx, e.Currency)
	if err != nil {
		return "", err
	}
	err = s.db.QueryRow(ctx, `INSERT INTO mkt_ad_expenses (source_id, day, amount, currency, campaign, comment, created_by)
		SELECT id, $2, $3, $4, $5, $6, $7 FROM mkt_ad_sources WHERE id::text = $1 RETURNING id::text`,
		e.SourceID, e.Day.Format(time.DateOnly), e.Amount, cur, e.Campaign, e.Comment, userID).Scan(&e.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return e.ID, err
}

// DeleteExpense removes an expense: its author's or, by an administrator, any.
func (s *Store) DeleteExpense(ctx context.Context, userID string, admin bool, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mkt_ad_expenses WHERE id::text = $1 AND ($3 OR created_by::text = $2)`, id, userID, admin)
	if err == nil && tag.RowsAffected() == 0 {
		err = s.missing(ctx, "mkt_ad_expenses", id)
	}
	return err
}

// ImportExpenses reads expenses from a CSV (as a spreadsheet saves it, «;» or «,»): date, channel (its name or
// one of its utm_source), amount, and optionally currency, campaign, comment; a first line that is no expense — the
// header — is skipped. Nothing is written if a line is wrong: the error names it.
func (s *Store) ImportExpenses(ctx context.Context, userID string, r io.Reader) (int, error) {
	data, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return 0, err
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma, cr.FieldsPerRecord, cr.LazyQuotes = ';', -1, true
	if first, _, _ := strings.Cut(text, "\n"); !strings.Contains(first, ";") {
		cr.Comma = ','
	}
	lines, err := cr.ReadAll()
	if err != nil {
		return 0, i18n.E("err.mkt_import", "1")
	}
	srcs, err := s.AdSources(ctx)
	if err != nil {
		return 0, err
	}
	byName := map[string]string{}
	for _, a := range srcs {
		byName[strings.ToLower(a.Name)] = a.ID
		for _, u := range a.UTM {
			if byName[u] == "" {
				byName[u] = a.ID
			}
		}
	}
	var list []Expense
	for i, l := range lines {
		for len(l) < 6 {
			l = append(l, "")
		}
		day, derr := parseDay(l[0])
		amount, aerr := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(l[2]), " ", ""), ",", "."), 64)
		if i == 0 && (derr != nil || aerr != nil) {
			continue // the header
		}
		e := Expense{SourceID: byName[strings.ToLower(strings.TrimSpace(l[1]))], Day: day, Amount: amount, Currency: l[3], Campaign: l[4], Comment: l[5]}
		if derr != nil || aerr != nil || e.clean() != nil {
			return 0, i18n.E("err.mkt_import", strconv.Itoa(i+1))
		}
		if e.Currency, err = s.currency(ctx, e.Currency); err != nil {
			return 0, i18n.E("err.mkt_import", strconv.Itoa(i+1))
		}
		list = append(list, e)
	}
	if len(list) == 0 {
		return 0, i18n.E("err.mkt_import_empty")
	}
	if len(list) > 10000 {
		return 0, i18n.E("err.mkt_import", "10001")
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, e := range list {
			if _, err := tx.Exec(ctx, `INSERT INTO mkt_ad_expenses (source_id, day, amount, currency, campaign, comment, created_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, e.SourceID, e.Day.Format(time.DateOnly), e.Amount, e.Currency, e.Campaign, e.Comment, userID); err != nil {
				return err
			}
		}
		return nil
	})
	return len(list), err
}

// parseDay reads a date as 2006-01-02 or 02.01.2006.
func parseDay(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}
	return time.Parse("02.01.2006", v)
}

// Channel is a line of the report: a channel (ID "" — the cards of no channel) or a campaign of it (Name — its
// utm_campaign).
type Channel struct {
	ID, Name, Color   string
	Expenses          float64 // in the base currency
	Leads, Deals, Won int     // created in the period; Won — those of the deals now won
	Revenue           float64 // of the won deals, in the base currency
	Campaigns         []*Channel
}

// CostPerLead is what a lead cost; 0 — no expenses or no leads.
func (c Channel) CostPerLead() float64 { return per(c.Expenses, c.Leads) }

// CostPerSale is what a won deal cost.
func (c Channel) CostPerSale() float64 { return per(c.Expenses, c.Won) }

func per(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return math.Round(sum/float64(n)*100) / 100
}

// ROI is the percent the revenue returned over the expenses: (revenue − expenses) / expenses; ok false — no expenses.
func (c Channel) ROI() (pct int, ok bool) {
	if c.Expenses <= 0 {
		return 0, false
	}
	return int(math.Round((c.Revenue - c.Expenses) / c.Expenses * 100)), true
}

func (c *Channel) add(o Channel) {
	c.Expenses += o.Expenses
	c.Leads += o.Leads
	c.Deals += o.Deals
	c.Won += o.Won
	c.Revenue += o.Revenue
}

func (c *Channel) campaign(name string) *Channel {
	for _, x := range c.Campaigns {
		if x.Name == name {
			return x
		}
	}
	x := &Channel{Name: name}
	c.Campaigns = append(c.Campaigns, x)
	return x
}

// Report is «Сквозная аналитика» of [From, To).
type Report struct {
	From, To time.Time
	Currency string     // the base one
	Channels []*Channel // in the channels' order; the cards of no channel last, if there are any
	Total    Channel
}

// matcher finds the channel of a card: by its utm_source or, failing that, its «Источник».
type matcher struct {
	byUTM, byOrigin map[string]int
}

func newMatcher(srcs []AdSource) matcher {
	m := matcher{byUTM: map[string]int{}, byOrigin: map[string]int{}}
	for i, a := range srcs {
		for _, u := range a.UTM {
			if _, ok := m.byUTM[u]; !ok {
				m.byUTM[u] = i
			}
		}
		if _, ok := m.byOrigin[a.CRMSource]; a.CRMSource != "" && !ok {
			m.byOrigin[a.CRMSource] = i
		}
	}
	return m
}

func (m matcher) of(utmSource, origin string) int {
	if i, ok := m.byUTM[strings.ToLower(strings.TrimSpace(utmSource))]; ok {
		return i
	}
	if i, ok := m.byOrigin[origin]; ok {
		return i
	}
	return -1
}

// inBase is the SQL of an amount in the base currency, as the CRM counts it.
func inBase(amount, currency string) string {
	return `(` + amount + ` * coalesce((SELECT cr.rate FROM crm_currencies cr WHERE cr.code = ` + currency + `), 1))`
}

// Through makes the report of [from, to) for the viewer: the expenses of the days, the leads and the deals created
// in the period that the viewer may read, each set to its channel; a deal without UTM tags and «Источник» of its
// own has those of the lead it was made of.
func (s *Store) Through(ctx context.Context, viewer string, from, to time.Time) (Report, error) {
	rep := Report{From: from, To: to}
	var err error
	if rep.Currency, err = s.CRM.BaseCurrency(ctx); err != nil {
		return rep, err
	}
	srcs, err := s.AdSources(ctx)
	if err != nil {
		return rep, err
	}
	m := newMatcher(srcs)
	lines := make([]*Channel, len(srcs)+1)
	for i, a := range srcs {
		lines[i] = &Channel{ID: a.ID, Name: a.Name, Color: a.Color}
	}
	none := &Channel{}
	lines[len(srcs)] = none
	line := func(i int) *Channel {
		if i < 0 {
			return none
		}
		return lines[i]
	}
	byID := map[string]int{}
	for i, a := range srcs {
		byID[a.ID] = i
	}
	// the expenses of the days
	rows, err := s.db.Query(ctx, `SELECT source_id::text, campaign, sum(`+inBase("amount", "currency")+`)::float8 FROM mkt_ad_expenses
		WHERE day >= $1::date AND day < $2::date GROUP BY 1, 2`, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return rep, err
	}
	var src, camp string
	var sum float64
	if _, err := pgx.ForEachRow(rows, []any{&src, &camp, &sum}, func() error {
		c := line(byID[src])
		c.Expenses += sum
		c.campaign(camp).Expenses += sum
		return nil
	}); err != nil {
		return rep, err
	}
	// the leads
	ls, err := s.CRM.ReadScope(ctx, viewer, "lead", "l", 1)
	if err != nil {
		return rep, err
	}
	rows, err = s.db.Query(ctx, `SELECT coalesce(u.source, ''), coalesce(u.campaign, ''), coalesce(l.source_id::text, '')
		FROM crm_leads l LEFT JOIN mkt_utm u ON u.lead_id = l.id WHERE l.created_at >= $2 AND l.created_at < $3 AND (`+ls+`)`, viewer, from, to)
	if err != nil {
		return rep, err
	}
	var origin string
	if _, err := pgx.ForEachRow(rows, []any{&src, &camp, &origin}, func() error {
		c := line(m.of(src, origin))
		c.Leads++
		c.campaign(camp).Leads++
		return nil
	}); err != nil {
		return rep, err
	}
	// the deals (not the cards of smart processes)
	ds, err := s.CRM.ReadScope(ctx, viewer, "deal", "d", 1)
	if err != nil {
		return rep, err
	}
	rows, err = s.db.Query(ctx, `SELECT coalesce(du.source, lu.source, ''), coalesce(du.campaign, lu.campaign, ''),
			coalesce(d.source_id::text, lu.source_id::text, ''), st.semantic = 'success', `+inBase("d.amount", "d.currency")+`::float8
		FROM crm_deals d JOIN crm_pipelines p ON p.id = d.pipeline_id AND p.type_id IS NULL JOIN crm_stages st ON st.id = d.stage_id
		LEFT JOIN mkt_utm du ON du.deal_id = d.id
		LEFT JOIN LATERAL (SELECT u.source, u.campaign, l.source_id FROM crm_leads l LEFT JOIN mkt_utm u ON u.lead_id = l.id
			WHERE l.deal_id = d.id ORDER BY l.created_at LIMIT 1) lu ON true
		WHERE d.created_at >= $2 AND d.created_at < $3 AND (`+ds+`)`, viewer, from, to)
	if err != nil {
		return rep, err
	}
	var won bool
	if _, err := pgx.ForEachRow(rows, []any{&src, &camp, &origin, &won, &sum}, func() error {
		c := line(m.of(src, origin))
		for _, c := range []*Channel{c, c.campaign(camp)} {
			c.Deals++
			if won {
				c.Won++
				c.Revenue += sum
			}
		}
		return nil
	}); err != nil {
		return rep, err
	}
	for _, c := range lines {
		c.Expenses, c.Revenue = round2(c.Expenses), round2(c.Revenue)
		for _, x := range c.Campaigns {
			x.Expenses, x.Revenue = round2(x.Expenses), round2(x.Revenue)
		}
		sort.SliceStable(c.Campaigns, func(i, j int) bool { // the named campaigns by expenses, then by revenue; «no campaign» last
			a, b := c.Campaigns[i], c.Campaigns[j]
			if (a.Name == "") != (b.Name == "") {
				return b.Name == ""
			}
			if a.Expenses != b.Expenses {
				return a.Expenses > b.Expenses
			}
			return a.Revenue > b.Revenue
		})
		if len(c.Campaigns) == 1 && c.Campaigns[0].Name == "" {
			c.Campaigns = nil // nothing to unfold
		}
		rep.Total.add(*c)
	}
	if none.Leads == 0 && none.Deals == 0 {
		lines = lines[:len(srcs)]
	}
	rep.Channels = lines
	rep.Total.Expenses, rep.Total.Revenue = round2(rep.Total.Expenses), round2(rep.Total.Revenue)
	return rep, nil
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
