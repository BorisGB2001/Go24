package crm

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// More robots: a document robot («Создать документ») makes a document of the card by a template of the
// document generator, of the card's responsible; a process robot («Запустить бизнес-процесс») starts a template of
// the card's kind as an autostart does; an observers robot («Изменить наблюдателей») adds, removes or puts in people
// as the card's observers; a close robot («Завершить дела») completes the card's open deeds (all, or of one kind); a
// sign robot («Отправить на подпись») makes a document as the document robot does and sends it for signing: the
// employees given (or else the card's responsible) sign for the company, then the card's client by the e-mailed link.

// ObserverModes are what the observers robot does with its people.
var ObserverModes = []string{"add", "remove", "set"}

// checkRobotMore checks (and tidies) the settings of the robots of this file.
func (s *Store) checkRobotMore(ctx context.Context, stageID, kind string, p *RobotParams) error {
	switch kind {
	case "sign":
		var users []string // in the order they sign
		for _, u := range p.Users {
			if u = strings.TrimSpace(u); u != "" && !slices.Contains(users, u) {
				users = append(users, u)
			}
		}
		p.Users = users
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM users WHERE id::text = ANY($1) AND active AND NOT guest`, p.Users).Scan(&n); err != nil ||
			n != len(p.Users) || n > 10 {
			return i18n.E("err.robot_user")
		}
		fallthrough
	case "document":
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_doc_templates t JOIN crm_stages st ON st.entity = t.entity
			LEFT JOIN crm_pipelines pl ON pl.id = st.pipeline_id
			WHERE st.id::text = $1 AND t.id::text = $2 AND t.type_id IS NOT DISTINCT FROM pl.type_id)`, stageID, p.Doc).Scan(&ok); err != nil || !ok {
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

// robotSign makes the document and sends it for signing to the signers (none — the card's responsible) and the card's
// client, by e-mail or else by SMS to the client's phone, or else in the client's chat of the open lines (SendSign
// finds it, none — errNoEmail); the responsible sends it. The history names it.
func (s *Store) robotSign(ctx context.Context, actorID string, c card, p RobotParams) error {
	if s.MakeDocument == nil || s.SendSign == nil {
		return i18n.E("err.robot_doc")
	}
	to, err := s.clientEmail(ctx, c)
	phone := ""
	if errors.Is(err, errNoEmail) {
		if phones, perr := s.ClientPhones(ctx, c.entity, c.id); perr == nil && len(phones) > 0 {
			to, phone, err = "", phones[0].Phone, nil
		} else if s.HasChat != nil && s.HasChat(ctx, c.entity, c.id) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	id, err := s.MakeDocument(ctx, c.responsible, c.entity, c.id, p.Doc)
	if err != nil {
		return err
	}
	signers := p.Users
	if len(signers) == 0 {
		signers = []string{c.responsible}
	}
	title, err := s.SendSign(ctx, c.responsible, c.entity, c.id, id, signers, c.client, to, phone)
	if err != nil {
		return err
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "sign:"+title)
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
	return s.ChangeObservers(ctx, actorID, c.entity, c.id, p.Obs, p.Users)
}

// ChangeObservers adds the people (IDs) to the observers of a deal or a lead, removes them («remove») or makes them
// the only ones («set»), of ObserverModes; nothing changes — no history.
func (s *Store) ChangeObservers(ctx context.Context, actorID, entity, id, mode string, users []string) error {
	old, err := s.Observers(ctx, entity, id)
	if err != nil {
		return err
	}
	ids := ObserverIDs(old)
	next := slices.Clone(ids)
	switch mode {
	case "set":
		next = slices.Clone(users)
	case "remove":
		next = slices.DeleteFunc(next, func(u string) bool { return slices.Contains(users, u) })
	default:
		for _, u := range users {
			if !slices.Contains(next, u) {
				next = append(next, u)
			}
		}
	}
	slices.Sort(ids)
	if sorted := slices.Sorted(slices.Values(next)); slices.Equal(ids, sorted) {
		return nil
	}
	return s.SetObservers(ctx, actorID, entity, id, next)
}

// robotClose completes the card's open deeds (of the kind, if one is set).
func (s *Store) robotClose(ctx context.Context, actorID string, c card, kind string) error {
	n, err := s.CloseActivities(ctx, actorID, c.entity, c.id, kind)
	if err != nil || n == 0 {
		return err
	}
	return s.event(ctx, s.db, c.entity, c.id, actorID, "robot", "close:"+strconv.Itoa(n))
}

// CloseActivities completes the card's open deeds (of the kind of ActivityKinds, "" — all) and says how many.
func (s *Store) CloseActivities(ctx context.Context, actorID, entity, id, kind string) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM crm_activities WHERE entity = $1 AND entity_id::text = $2 AND done_at IS NULL
		AND ($3 = '' OR kind = $3) ORDER BY deadline`, entity, id, kind)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, a := range ids {
		if _, err := s.CompleteActivity(ctx, actorID, a); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
