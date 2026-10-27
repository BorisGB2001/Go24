package crm

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// More robots: a document robot («Создать документ») makes a document of the card by a template of the
// document generator, of the card's responsible; a process robot («Запустить бизнес-процесс») starts a template of
// the card's kind as an autostart does; an observers robot («Изменить наблюдателей») adds, removes or puts in people
// as the card's observers; a close robot («Завершить дела») completes the card's open deeds (all, or of one kind).

// ObserverModes are what the observers robot does with its people.
var ObserverModes = []string{"add", "remove", "set"}

// checkRobotMore checks (and tidies) the settings of the robots of this file.
func (s *Store) checkRobotMore(ctx context.Context, stageID, kind string, p *RobotParams) error {
	switch kind {
	case "document":
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_doc_templates t JOIN crm_stages st ON st.entity = t.entity
			WHERE st.id::text = $1 AND t.id::text = $2)`, stageID, p.Doc).Scan(&ok); err != nil || !ok {
			return i18n.E("err.robot_doc")
		}
	case "bp":
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bp_templates t JOIN crm_stages st ON st.entity = t.entity
			LEFT JOIN crm_pipelines pl ON pl.id = st.pipeline_id
			WHERE st.id::text = $1 AND t.id::text = $2 AND t.type_id IS NOT DISTINCT FROM pl.type_id)`, stageID, p.BP).Scan(&ok); err != nil || !ok {
			return i18n.E("err.robot_bp")
		}
	case "observers":
		var users []string
		for _, u := range p.Users {
			if u = strings.TrimSpace(u); u != "" && !slices.Contains(users, u) {
				users = append(users, u)
			}
		}
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE id::text = ANY($1)`, users).Scan(&n); err != nil ||
			n != len(users) || len(users) > 100 || (n == 0 && p.Obs != "set") {
			return i18n.E("err.robot_user")
		}
		if p.Users = users; !slices.Contains(ObserverModes, p.Obs) {
			p.Obs = "add"
		}
	case "close":
		if !slices.Contains(ActivityKinds, p.Act) {
			p.Act = ""
		}
	}
	return nil
}

// robotDocument makes the document; the history names it.
func (s *Store) robotDocument(ctx context.Context, actorID string, c card, tplID string) error {
	if s.MakeDocument == nil {
		return i18n.E("err.robot_doc")
	}
	id, err := s.MakeDocument(ctx, c.responsible, c.entity, c.id, tplID)
	if err != nil {
		return err
	}
	d, err := s.DocumentByID(ctx, id)
	if err != nil {
		return err
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "document:"+d.TemplateName+" № "+d.Number)
}

// robotProcess starts the business process; the history names it.
func (s *Store) robotProcess(ctx context.Context, actorID string, c card, tplID string) error {
	if s.StartBP == nil {
		return i18n.E("err.robot_bp")
	}
	var name string
	if err := s.db.QueryRow(ctx, `SELECT name FROM bp_templates WHERE id::text = $1`, tplID).Scan(&name); err != nil {
		return i18n.E("err.robot_bp")
	}
	if _, err := s.StartBP(ctx, actorID, tplID, c.id); err != nil {
		return err
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "bp:"+name)
}

// robotObservers changes the card's observers (the change is in its history as any other).
func (s *Store) robotObservers(ctx context.Context, actorID string, c card, p RobotParams) error {
	old, err := s.Observers(ctx, c.entity, c.id)
	if err != nil {
		return err
	}
	ids := ObserverIDs(old)
	next := slices.Clone(ids)
	switch p.Obs {
	case "set":
		next = slices.Clone(p.Users)
	case "remove":
		next = slices.DeleteFunc(next, func(u string) bool { return slices.Contains(p.Users, u) })
	default:
		for _, u := range p.Users {
			if !slices.Contains(next, u) {
				next = append(next, u)
			}
		}
	}
	slices.Sort(ids)
	if sorted := slices.Sorted(slices.Values(next)); slices.Equal(ids, sorted) {
		return nil
	}
	return s.SetObservers(ctx, actorID, c.entity, c.id, next)
}

// robotClose completes the card's open deeds (of the kind, if one is set).
func (s *Store) robotClose(ctx context.Context, actorID string, c card, kind string) error {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM crm_activities WHERE entity = $1 AND entity_id::text = $2 AND done_at IS NULL
		AND ($3 = '' OR kind = $3) ORDER BY deadline`, c.entity, c.id, kind)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(ids) == 0 {
		return err
	}
	for _, id := range ids {
		if _, err := s.CompleteActivity(ctx, actorID, id); err != nil {
			return err
		}
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "close:"+strconv.Itoa(len(ids)))
}
