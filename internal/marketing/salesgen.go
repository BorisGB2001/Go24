package marketing

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/crm"
	"go24/internal/i18n"
)

// «Генератор продаж»: repeat sales to the clients of a CRM segment of contacts or companies. Started, it
// fixes its clients (without those with a deal or a lead in work, unless asked) and makes for each a «Повторный лид» or
// a deal of the chosen pipeline and stage — by the client's responsible or the chosen employees in turn — a hundred at
// once and the rest by the worker; each of them is told once. Its progress is the cards made, won and lost.

// MaxSalesGen are the clients of a generator at most; salesGenRound — the cards made at once and each round.
const (
	MaxSalesGen   = 5000
	salesGenRound = 100
)

// SalesGen is a generator of repeat sales with its progress.
type SalesGen struct {
	ID, Title, SegmentID, SegmentName string
	Target                            string // lead | deal
	PipelineID, PipelineName, StageID string // of a deal; "" stage — the pipeline's first
	CardTitle, Comment                string // of the cards: {client} in the title is the client's name
	SkipOpen                          bool   // not to clients with a deal or a lead in work
	Assign                            string // owner — the client's responsible; staff — Staff in turn
	Staff                             []string
	Status                            string // draft, started (cards being made), done
	CreatedBy, AuthorName             string
	StartedAt                         *time.Time
	UpdatedAt                         time.Time
	Total, Made, Failed, Won, Lost    int
}

// Editable: a draft, not started yet.
func (g SalesGen) Editable() bool { return g.Status == "draft" }

// InWork: the cards made and not closed yet.
func (g SalesGen) InWork() int { return g.Made - g.Won - g.Lost }

const salesGenCols = `g.id::text, g.title, coalesce(g.segment_id::text, ''), coalesce(sg.name, ''), g.target, coalesce(g.pipeline_id::text, ''),
	coalesce(p.name, ''), coalesce(g.stage_id::text, ''), g.card_title, g.comment, g.skip_open, g.assign,
	coalesce((SELECT array_agg(x::text) FROM unnest(g.staff) x), '{}'), g.status, coalesce(g.created_by::text, ''), coalesce(u.name, ''),
	g.started_at, g.updated_at, count(i.id)::int, count(i.card_id)::int, count(i.id) FILTER (WHERE i.error <> '')::int,
	count(i.id) FILTER (WHERE coalesce(ds.semantic, ls.semantic) = 'success')::int,
	count(i.id) FILTER (WHERE coalesce(ds.semantic, ls.semantic) = 'failure')::int
	FROM mkt_salesgen g LEFT JOIN mkt_segments sg ON sg.id = g.segment_id LEFT JOIN crm_pipelines p ON p.id = g.pipeline_id
	LEFT JOIN users u ON u.id = g.created_by LEFT JOIN mkt_salesgen_items i ON i.gen_id = g.id
	LEFT JOIN crm_deals d ON g.target = 'deal' AND d.id = i.card_id LEFT JOIN crm_stages ds ON ds.id = d.stage_id
	LEFT JOIN crm_leads l ON g.target = 'lead' AND l.id = i.card_id LEFT JOIN crm_stages ls ON ls.id = l.stage_id`

const salesGenGroup = ` GROUP BY g.id, sg.name, p.name, u.name`

// SalesGens are the company's generators, the latest first.
func (s *Store) SalesGens(ctx context.Context) ([]SalesGen, error) {
	rows, err := s.db.Query(ctx, `SELECT `+salesGenCols+salesGenGroup+` ORDER BY g.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SalesGen])
}

// SalesGen is one of them.
func (s *Store) SalesGen(ctx context.Context, id string) (SalesGen, error) {
	rows, err := s.db.Query(ctx, `SELECT `+salesGenCols+` WHERE g.id::text = $1`+salesGenGroup, id)
	if err != nil {
		return SalesGen{}, err
	}
	g, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[SalesGen])
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return g, err
}

func (s *Store) mayChangeSalesGen(ctx context.Context, userID string, admin bool, id string) (SalesGen, error) {
	g, err := s.SalesGen(ctx, id)
	if err == nil && !admin && g.CreatedBy != userID {
		err = ErrForbidden
	}
	return g, err
}

// SaveSalesGen adds a generator (g.ID "") or changes a draft one: the author's or, by an administrator, any.
func (s *Store) SaveSalesGen(ctx context.Context, userID string, admin bool, g SalesGen) (string, error) {
	g.Title, g.CardTitle, g.Comment = strings.TrimSpace(g.Title), strings.TrimSpace(g.CardTitle), strings.TrimSpace(g.Comment)
	if g.Assign != "staff" {
		g.Assign = "owner"
	}
	if g.Target != "lead" {
		g.Target = "deal"
	}
	if g.Title == "" || len([]rune(g.Title)) > maxName || len([]rune(g.CardTitle)) > 250 || len([]rune(g.Comment)) > 10000 ||
		g.Assign == "staff" && len(g.Staff) == 0 {
		return "", i18n.E("err.mkt_sg")
	}
	if g.Staff == nil {
		g.Staff = []string{}
	}
	if g.Target == "lead" {
		g.PipelineID, g.StageID = "", ""
	} else if g.StageID != "" { // the stage is of the pipeline
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_stages WHERE id::text = $1 AND pipeline_id::text = $2)`,
			g.StageID, g.PipelineID).Scan(&ok); err != nil {
			return "", err
		}
		if !ok {
			g.StageID = ""
		}
	}
	args := []any{g.Title, nullable(g.SegmentID), g.Target, nullable(g.PipelineID), nullable(g.StageID), g.CardTitle, g.Comment, g.SkipOpen,
		g.Assign, g.Staff}
	if g.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO mkt_salesgen (title, segment_id, target, pipeline_id, stage_id, card_title, comment, skip_open,
			assign, staff, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid[], $11) RETURNING id::text`,
			append(args, userID)...).Scan(&g.ID)
		return g.ID, err
	}
	old, err := s.mayChangeSalesGen(ctx, userID, admin, g.ID)
	if err != nil {
		return "", err
	}
	if !old.Editable() {
		return "", i18n.E("err.mkt_sg_started")
	}
	_, err = s.db.Exec(ctx, `UPDATE mkt_salesgen SET title = $1, segment_id = $2, target = $3, pipeline_id = $4, stage_id = $5, card_title = $6,
		comment = $7, skip_open = $8, assign = $9, staff = $10::uuid[], updated_at = now() WHERE id::text = $11`, append(args, g.ID)...)
	return g.ID, err
}

func nullable(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// DeleteSalesGen removes a generator; the leads and deals it made stay.
func (s *Store) DeleteSalesGen(ctx context.Context, userID string, admin bool, id string) error {
	if _, err := s.mayChangeSalesGen(ctx, userID, admin, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM mkt_salesgen WHERE id::text = $1`, id)
	return err
}

// Client is a client of a generator.
type Client struct {
	Entity, EntityID, Name, Responsible string
}

// clientSQL is the query of a segment's clients: entity, id, name, responsible.
var clientSQL = map[string]string{
	"contact": `SELECT 'contact', x.id::text, x.name, coalesce(x.responsible_id::text, '') FROM crm_contacts x WHERE true`,
	"company": `SELECT 'company', x.id::text, x.title, coalesce(x.responsible_id::text, '') FROM crm_companies x WHERE true`,
}

// openSQL tells a client (x) with a deal (not of a smart process) or a lead in work.
var openSQL = map[string]string{
	"contact": ` AND NOT EXISTS (SELECT 1 FROM crm_deals d JOIN crm_stages st ON st.id = d.stage_id JOIN crm_pipelines p ON p.id = d.pipeline_id
			WHERE st.semantic = 'process' AND p.type_id IS NULL
			AND (d.contact_id = x.id OR EXISTS (SELECT 1 FROM crm_deal_contacts dc WHERE dc.deal_id = d.id AND dc.contact_id = x.id)))
		AND NOT EXISTS (SELECT 1 FROM crm_leads l JOIN crm_stages st ON st.id = l.stage_id WHERE st.semantic = 'process' AND l.contact_id = x.id)`,
	"company": ` AND NOT EXISTS (SELECT 1 FROM crm_deals d JOIN crm_stages st ON st.id = d.stage_id JOIN crm_pipelines p ON p.id = d.pipeline_id
			WHERE st.semantic = 'process' AND p.type_id IS NULL AND d.company_id = x.id)
		AND NOT EXISTS (SELECT 1 FROM crm_leads l JOIN crm_stages st ON st.id = l.stage_id WHERE st.semantic = 'process' AND l.company_id = x.id)`,
}

// SalesGenClients are the clients of the generator's segment the viewer may see (CRM rights).
func (s *Store) SalesGenClients(ctx context.Context, viewer string, g SalesGen) ([]Client, error) {
	if g.SegmentID == "" {
		return nil, nil
	}
	seg, err := s.Segment(ctx, g.SegmentID)
	if err != nil {
		return nil, err
	}
	q, ok := clientSQL[seg.Entity]
	if !ok {
		return nil, i18n.E("err.mkt_sg_segment")
	}
	if g.SkipOpen {
		q += openSQL[seg.Entity]
	}
	q, args, err := s.segmentQuery(ctx, viewer, seg, q)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Client])
	if err != nil {
		return nil, err
	}
	if len(list) > MaxSalesGen {
		return nil, i18n.E("err.mkt_sg_too_many", MaxSalesGen)
	}
	return list, nil
}

// StartSalesGen fixes the clients of a draft (as its starter sees them), each with the responsible of its card: the
// client's (the starter when it has none) or the chosen employees in turn; tells each of them once and makes the first
// cards at once. It returns how many clients there are.
func (s *Store) StartSalesGen(ctx context.Context, userID string, admin bool, id string) (int, error) {
	g, err := s.mayChangeSalesGen(ctx, userID, admin, id)
	if err != nil {
		return 0, err
	}
	if !g.Editable() {
		return 0, i18n.E("err.mkt_sg_started")
	}
	if err := s.CRM.Check(ctx, userID, g.Target, "add", userID); err != nil { // as making a card of one's own
		return 0, err
	}
	if g.Target == "deal" && g.PipelineID == "" {
		return 0, i18n.E("err.mkt_sg_pipeline")
	}
	list, err := s.SalesGenClients(ctx, userID, g)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, i18n.E("err.mkt_sg_nobody")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mkt_salesgen SET status = 'started', started_at = now(), updated_at = now() WHERE id::text = $1 AND status = 'draft'`, id)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, i18n.E("err.mkt_sg_started")
	}
	cards := map[string]int{} // by the responsible
	for i, x := range list {
		who := x.Responsible
		if g.Assign == "staff" {
			who = g.Staff[i%len(g.Staff)]
		}
		if who == "" {
			who = userID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mkt_salesgen_items (gen_id, entity, entity_id, name, responsible_id) VALUES ($1, $2, $3, $4, $5)`,
			g.ID, x.Entity, x.EntityID, x.Name, who); err != nil {
			return 0, err
		}
		cards[who]++
	}
	url := "/crm/deals"
	if g.Target == "lead" {
		url = "/crm/leads"
	}
	for who, n := range cards {
		if who == userID {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'crm_salesgen', $3, $4)`,
			who, userID, g.Title+" ("+strconv.Itoa(n)+")", url); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	_, err = s.MakeSalesGenCards(ctx, g.ID)
	return len(list), err
}

// MakeSalesGenCards makes the cards of the started generators (only of genID, when given) not made yet, a round of
// them, and closes the generators whose clients are all done. It returns the cards made; a card that could not be made
// keeps its error and is not tried again.
func (s *Store) MakeSalesGenCards(ctx context.Context, genID string) (int, error) {
	rows, err := s.db.Query(ctx, `UPDATE mkt_salesgen_items i SET claimed_at = now() FROM mkt_salesgen g
		WHERE g.id = i.gen_id AND i.id IN (SELECT x.id FROM mkt_salesgen_items x JOIN mkt_salesgen y ON y.id = x.gen_id
			WHERE x.claimed_at IS NULL AND y.status = 'started' AND ($1 = '' OR y.id::text = $1) ORDER BY x.id LIMIT $2 FOR UPDATE OF x SKIP LOCKED)
		RETURNING i.id, g.id::text, g.target, coalesce(g.pipeline_id::text, ''), coalesce(g.stage_id::text, ''), g.title, g.card_title, g.comment,
			coalesce(g.created_by::text, ''), i.entity, i.entity_id::text, i.name, coalesce(i.responsible_id::text, '')`, genID, salesGenRound)
	if err != nil {
		return 0, err
	}
	type item struct {
		ID                                                                    int64
		GenID, Target, PipelineID, StageID, Title, CardTitle, Comment, Author string
		Entity, EntityID, Name, Responsible                                   string
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[item])
	if err != nil {
		return 0, err
	}
	made := 0
	for _, x := range items {
		title := strings.TrimSpace(x.CardTitle)
		if title == "" {
			title = x.Title + ": {client}"
		}
		title = strings.ReplaceAll(title, "{client}", x.Name)
		cardID, err := s.salesGenCard(ctx, x.Target, x.PipelineID, x.StageID, title, x.Comment, x.Author, x.Responsible, x.Entity, x.EntityID, x.Name)
		if err != nil {
			if ctx.Err() != nil {
				return made, ctx.Err()
			}
			_, err = s.db.Exec(ctx, `UPDATE mkt_salesgen_items SET error = $2 WHERE id = $1`, x.ID, err.Error())
		} else {
			made++
			_, err = s.db.Exec(ctx, `UPDATE mkt_salesgen_items SET card_id = $2 WHERE id = $1`, x.ID, cardID)
		}
		if err != nil {
			return made, err
		}
	}
	_, err = s.db.Exec(ctx, `UPDATE mkt_salesgen g SET status = 'done', updated_at = now() WHERE g.status = 'started' AND ($1 = '' OR g.id::text = $1)
		AND NOT EXISTS (SELECT 1 FROM mkt_salesgen_items i WHERE i.gen_id = g.id AND i.claimed_at IS NULL)`, genID)
	return made, err
}

// salesGenCard makes the card of a client: a deal with the client (and a contact's company) or a repeat lead of it with
// its name, phone and e-mail. The robots of the card's stage run as on any new card, by the generator's author.
func (s *Store) salesGenCard(ctx context.Context, target, pipeline, stage, title, comment, author, responsible, entity, id, name string) (string, error) {
	var phone, email, company, companyTitle string
	q := `SELECT phone, email, coalesce(company_id::text, ''), coalesce((SELECT title FROM crm_companies c WHERE c.id = x.company_id), '')
		FROM crm_contacts x WHERE id::text = $1`
	if entity == "company" {
		q = `SELECT phone, email, '', title FROM crm_companies x WHERE id::text = $1`
	}
	if err := s.db.QueryRow(ctx, q, id).Scan(&phone, &email, &company, &companyTitle); err == pgx.ErrNoRows {
		return "", ErrNotFound // the client was removed after the start
	} else if err != nil {
		return "", err
	}
	if target == "lead" {
		l := crm.Lead{Title: title, Comment: comment, Phone: strings.TrimSpace(phone), Email: strings.TrimSpace(email),
			CompanyTitle: companyTitle, Responsible: crm.Ref{ID: responsible}, ContactID: id, CompanyID: company}
		if entity == "company" {
			l.ContactID, l.CompanyID = "", id
		} else {
			l.Name = name
		}
		return s.CRM.SaveLead(ctx, author, l)
	}
	d := crm.Deal{Title: title, Comment: comment, PipelineID: pipeline, Stage: crm.Stage{ID: stage}, Responsible: crm.Ref{ID: responsible},
		Company: crm.Ref{ID: company}}
	if entity == "company" {
		d.Company = crm.Ref{ID: id}
	} else {
		d.Contact = crm.Ref{ID: id}
	}
	return s.CRM.SaveDeal(ctx, author, d)
}

// SalesGenItem is a client of a started generator and its card.
type SalesGenItem struct {
	ID                             int64
	Entity, EntityID, Name         string
	ResponsibleID, ResponsibleName string
	CardID, CardTitle              string // "" title — the card was removed
	Semantic, Stage                string // the stage: "code:<code>" of a built-in one (crmStage of the pages) or its name
	Error                          string
}

// SalesGenItems are the clients of the generator (state: work, won, lost, failed or "" — all), a page of them, and how
// many there are.
func (s *Store) SalesGenItems(ctx context.Context, g SalesGen, state string, limit, offset int) ([]SalesGenItem, int, error) {
	card := ` LEFT JOIN crm_deals c ON c.id = i.card_id`
	if g.Target == "lead" {
		card = ` LEFT JOIN crm_leads c ON c.id = i.card_id`
	}
	cond := map[string]string{"work": "st.semantic = 'process'", "won": "st.semantic = 'success'", "lost": "st.semantic = 'failure'",
		"failed": "i.error <> ''"}[state]
	if cond == "" {
		cond = "true"
	}
	from := ` FROM mkt_salesgen_items i` + card + ` LEFT JOIN crm_stages st ON st.id = c.stage_id LEFT JOIN users u ON u.id = i.responsible_id
		WHERE i.gen_id::text = $1 AND ` + cond
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*)`+from, g.ID).Scan(&n); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT i.id, i.entity, i.entity_id::text, i.name, coalesce(i.responsible_id::text, ''), coalesce(u.name, ''),
		coalesce(i.card_id::text, ''), coalesce(c.title, ''), coalesce(st.semantic, ''), coalesce('code:' || st.code, st.name, ''), i.error`+from+`
		ORDER BY i.id LIMIT $2 OFFSET $3`, g.ID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SalesGenItem])
	return list, n, err
}
