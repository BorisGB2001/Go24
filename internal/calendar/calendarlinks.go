package calendar

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// A calendar connected by its iCal link connects outside calendars (Google, Yandex, Outlook.com,
// iCloud): one of the person's own calendars whose meetings go24 loads from the link again and again — new, changed
// and gone ones; in go24 they are read-only.

// LinkEvery is how often go24-worker loads a connected calendar again.
const LinkEvery = 30 * time.Minute

type syncKey struct{}

// syncing: the meeting is saved by SyncLink (only it changes a connected calendar).
func syncing(ctx context.Context) bool { v, _ := ctx.Value(syncKey{}).(bool); return v }

// LinkURL checks an iCal link (webcal:// is https://); it is given back as it is kept.
func LinkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if rest, ok := strings.CutPrefix(strings.ToLower(raw), "webcal://"); ok {
		raw = "https://" + raw[len(raw)-len(rest):]
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || len(raw) > 2000 {
		return "", i18n.E("err.calendar_link")
	}
	u.Fragment = ""
	return u.String(), nil
}

// AddLink connects a calendar by its iCal link: a new calendar of the viewer's (named by the link's site when there
// is no name); returns its ID. SyncLink loads it.
func (s *Store) AddLink(ctx context.Context, viewer, name, raw string) (string, error) {
	link, err := LinkURL(raw)
	if err != nil {
		return "", err
	}
	if name = strings.TrimSpace(name); name == "" {
		u, _ := url.Parse(link)
		name = u.Hostname()
	}
	var dup bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_calendars WHERE owner_id::text = $1 AND source_url = $2)`, viewer, link).Scan(&dup); err != nil {
		return "", err
	}
	if dup {
		return "", i18n.E("err.calendar_link_dup")
	}
	id, err := s.SaveSection(ctx, viewer, Section{Name: name})
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `UPDATE user_calendars SET source_url = $2 WHERE id::text = $1`, id, link)
	return id, err
}

// Link is the viewer's connected calendar id.
func (s *Store) Link(ctx context.Context, viewer, id string) (Section, error) {
	list, err := s.Sections(ctx, viewer)
	if err != nil {
		return Section{}, err
	}
	for _, x := range list {
		if x.ID == id && x.URL != "" {
			return x, nil
		}
	}
	return Section{}, ErrNotFound
}

// LinksDue are the connected calendars not loaded for LinkEvery, the longest waiting first.
func (s *Store) LinksDue(ctx context.Context, now time.Time) ([]Section, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.name, c.color, c.source_url, c.synced_at, c.sync_error, c.owner_id::text, c.dav_url, c.dav_login
		FROM user_calendars c JOIN users u ON u.id = c.owner_id
		WHERE c.source_url <> '' AND u.active AND (c.synced_at IS NULL OR c.synced_at < $1)
		ORDER BY c.synced_at NULLS FIRST LIMIT 50`, now.Add(-LinkEvery))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Section])
}

// LinkFailed keeps why the connected calendar was not loaded (its meetings stay as they were).
func (s *Store) LinkFailed(ctx context.Context, id, why string) error {
	_, err := s.db.Exec(ctx, `UPDATE user_calendars SET synced_at = now(), sync_error = $2 WHERE id::text = $1`, id, why)
	return err
}

// SyncLink makes the meetings of the connected calendar x those of its link (list): new ones added, the others changed,
// those gone from the link deleted. A meeting go24 cannot keep is left out. It tells how many there are now.
func (s *Store) SyncLink(ctx context.Context, x Section, list []Imported) (int, error) {
	ctx = context.WithValue(ctx, syncKey{}, true)
	var uids []string
	for _, im := range list {
		if im.UID == "" {
			continue
		}
		uid := x.ID + "/" + im.UID // the same file imported into another calendar is another meeting
		e := im.Event
		e.ProjectID, e.Company, e.RoomID, e.Video, e.CategoryID = "", false, "", false, ""
		e.CalendarID, e.OwnerID = x.ID, x.OwnerID
		err := s.db.QueryRow(ctx, `SELECT id::text FROM events WHERE calendar_id::text = $1 AND import_uid = $2`, x.ID, uid).Scan(&e.ID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
		if e.ID, err = s.Save(ctx, x.OwnerID, e, nil); err != nil {
			if ctx.Err() != nil {
				return 0, err
			}
			continue // a meeting of the file go24 does not take (no title, a bad time…)
		}
		if err := s.keepSkips(ctx, e.ID, e.Repeat, e.Skip, `UPDATE events SET import_uid = $2 WHERE id::text = $1`, uid); err != nil {
			return 0, err
		}
		uids = append(uids, uid)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM events WHERE calendar_id::text = $1 AND NOT (coalesce(import_uid, '') = ANY($2::text[]))`, x.ID, uids); err != nil {
		return 0, err
	}
	_, err := s.db.Exec(ctx, `UPDATE user_calendars SET synced_at = now(), sync_error = '' WHERE id::text = $1`, x.ID)
	return len(uids), err
}
