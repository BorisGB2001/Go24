package tasks

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// «Привязка к CRM»: links of a task to CRM items. Until go24 has its own CRM, an item is a card of the portal
// portal given by its URL.

// CRMLink is one linked CRM item.
type CRMLink struct {
	URL      string
	Kind     string // deal | contact | company | lead | quote | invoice | item (smart process) | other
	EntityID int64  // 0 — not recognised
	Own      bool   // a card of go24's own CRM (URL is its path)
}

// crmPath recognises the portal CRM cards: /crm/deal/details/12/, /crm/type/177/details/5/ and the like.
var crmPath = regexp.MustCompile(`/crm/(deal|contact|company|lead|quote|invoice|type/\d+)/details/(\d+)`)

// ownCRM is a card of go24's own CRM, kept as its path: /crm/deals/<uuid>.
var ownCRM = regexp.MustCompile(`^/crm/(deals|leads|contacts|companies)/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var ownKinds = map[string]string{"deals": "deal", "leads": "lead", "contacts": "contact", "companies": "company"}

// ParseCRM checks a link and finds the item's type and number in it.
func ParseCRM(raw string) (CRMLink, error) {
	raw = strings.TrimSpace(raw)
	if m := ownCRM.FindStringSubmatch(raw); m != nil {
		return CRMLink{URL: raw, Kind: ownKinds[m[1]], Own: true}, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 1000 {
		return CRMLink{}, invalid("err.crm_link")
	}
	l := CRMLink{URL: raw, Kind: "other"}
	if m := crmPath.FindStringSubmatch(u.Path); m != nil {
		l.Kind = m[1]
		if strings.HasPrefix(l.Kind, "type/") {
			l.Kind = "item"
		}
		l.EntityID, _ = strconv.ParseInt(m[2], 10, 64)
	}
	return l, nil
}

// AddCRMLink links a CRM item to the task (people who may work with it); the history keeps it.
func (s *Store) AddCRMLink(ctx context.Context, userID, taskID, raw string) error {
	l, err := ParseCRM(raw)
	if err != nil {
		return err
	}
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return err
	}
	if !(a.Roles.Worker() || a.Roles.Creator || a.Roles.Manager) {
		return ErrForbidden
	}
	var id *int64
	if l.EntityID != 0 {
		id = &l.EntityID
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO task_crm_links (task_id, url, kind, entity_id, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`, taskID, l.URL, l.Kind, id, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return addHistory(ctx, s.db, taskID, userID, "crm", "", l.URL)
}

// RemoveCRMLink unlinks a CRM item.
func (s *Store) RemoveCRMLink(ctx context.Context, userID, taskID, raw string) error {
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return err
	}
	if !(a.Roles.Worker() || a.Roles.Creator || a.Roles.Manager) {
		return ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM task_crm_links WHERE task_id = $1 AND url = $2`, taskID, raw)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return addHistory(ctx, s.db, taskID, userID, "crm", raw, "")
}

// CRMLinks are the task's CRM items in the order they were linked.
func (s *Store) CRMLinks(ctx context.Context, taskID string) ([]CRMLink, error) {
	rows, err := s.db.Query(ctx, `SELECT url, kind, coalesce(entity_id, 0), url LIKE '/crm/%' FROM task_crm_links WHERE task_id = $1 ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (CRMLink, error) {
		var l CRMLink
		err := r.Scan(&l.URL, &l.Kind, &l.EntityID, &l.Own)
		return l, err
	})
}
