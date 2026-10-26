package calendar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Two-way sync over CalDAV: a calendar program (Outlook with a CalDAV add-in, Thunderbird, iPhone and
// Mac, Android with DAVx5) holds the person's calendars as collections of meetings — the main one with everything they
// see (their meetings, those they are invited to, those of their projects and of the company) and each of their own
// calendars — and changes, adds and deletes meetings there as on the calendar's page.

// DavItem is a meeting as a collection holds it: its name there, its calendar file and the tag of that file.
type DavItem struct {
	Name, ETag, ICS string
	Event           Event
}

// Override is a changed occurrence of a series in a calendar file (RECURRENCE-ID): its original start and the
// meeting it becomes, or Cancelled — deleted.
type Override struct {
	At        time.Time
	Cancelled bool
	Event     Event
}

// DavEvents are the meetings of the viewer's collection: calendarID "" — the main one (everything they see but the
// meetings of their own other calendars and those they declined), else one of their own calendars.
func (s *Store) DavEvents(ctx context.Context, viewer, calendarID string) ([]DavItem, error) {
	where, args := ` AND NOT (e.owner_id = $1 AND e.calendar_id IS NOT NULL)`, []any{viewer, false}
	if calendarID != "" {
		where, args = ` AND e.owner_id = $1 AND e.calendar_id::text = $3`, append(args, calendarID)
	}
	rows, err := s.db.Query(ctx, selectSQL+` WHERE `+visible+where+` ORDER BY e.starts_at, e.id`, args...)
	if err != nil {
		return nil, err
	}
	list, err := s.scan(ctx, viewer, rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	ids := make([]string, len(list))
	for i, e := range list {
		ids[i] = e.ID
	}
	type meta struct {
		uid, name string
		at        time.Time
	}
	metas := map[string]meta{}
	mrows, err := s.db.Query(ctx, `SELECT id::text, coalesce(import_uid, ''), coalesce(dav_name, ''), updated_at FROM events WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer mrows.Close()
	for mrows.Next() {
		var id string
		var m meta
		if err := mrows.Scan(&id, &m.uid, &m.name, &m.at); err != nil {
			return nil, err
		}
		metas[id] = m
	}
	if err := mrows.Err(); err != nil {
		return nil, err
	}
	out := make([]DavItem, 0, len(list))
	for _, e := range list {
		if e.MyStatus == "declined" {
			continue
		}
		m := metas[e.ID]
		e.UID = m.uid
		it := DavItem{Name: m.name, Event: e, ICS: EventICS(e, m.at)}
		if it.Name == "" {
			it.Name = e.ID + ".ics"
		}
		sum := sha256.Sum256([]byte(it.ICS))
		it.ETag = `"` + hex.EncodeToString(sum[:10]) + `"`
		out = append(out, it)
	}
	return out, nil
}

// DavCTag is the tag of a collection: it changes with any of its meetings.
func DavCTag(list []DavItem) string {
	h := sha256.New()
	for _, it := range list {
		h.Write([]byte(it.Name + " " + it.ETag + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)[:10])
}

// DavItemOf is the meeting named name in the viewer's collection (nil — none).
func (s *Store) DavItemOf(ctx context.Context, viewer, calendarID, name string) (*DavItem, error) {
	list, err := s.DavResources(ctx, viewer, calendarID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].Name == name {
			return &list[i], nil
		}
	}
	return nil, nil
}

// DavPut saves the meeting a calendar program put into the viewer's collection under name: the one held there under
// that name is changed (only the viewer's own), else the viewer's meeting of the same UID (put here from another of
// their calendars), else a new one is made. What a calendar file does not tell (the people, room, project, colour)
// stays as it was. overrides are the changed occurrences of the series. It returns the meeting's ID and whether it
// is new.
func (s *Store) DavPut(ctx context.Context, viewer, calendarID, name string, im Imported, overrides []Override) (string, bool, error) {
	var old *Event
	it, err := s.DavItemOf(ctx, viewer, calendarID, name)
	if err != nil {
		return "", false, err
	}
	if it != nil {
		old = &it.Event
	} else {
		var id string
		err := s.db.QueryRow(ctx, `SELECT id::text FROM events WHERE owner_id::text = $1 AND (import_uid = $2 OR id::text || '@go24' = $2)`,
			viewer, im.UID).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
		if id != "" {
			e, err := s.Get(ctx, viewer, id)
			if err != nil {
				return "", false, err
			}
			old = &e
		}
	}
	if old != nil && old.OwnerID != viewer { // someone else's meeting: it is answered on the page, not changed
		return "", false, ErrForbidden
	}
	e, invite := synced(old, im.Event)
	if e.ProjectID == "" && !e.Company {
		e.CalendarID = calendarID
	}
	id, err := s.Save(ctx, viewer, e, invite)
	if err != nil {
		return "", false, err
	}
	uid, dav := any(nil), any(nil)
	if im.UID != "" && im.UID != id+"@go24" {
		uid = im.UID
	}
	if name != id+".ics" {
		dav = name
	}
	skip := im.Event.Skip
	for _, o := range overrides {
		if o.Cancelled {
			skip = append(skip, o.At)
		}
	}
	err = s.keepSkips(ctx, id, e.Repeat, skip, `UPDATE events SET import_uid = coalesce($2, import_uid), dav_name = $3 WHERE id::text = $1`, uid, dav)
	if err != nil || e.Repeat == "" {
		return id, old == nil, err
	}
	for _, o := range overrides {
		if o.Cancelled {
			continue
		}
		var child string
		err := s.db.QueryRow(ctx, `SELECT id::text FROM events WHERE series_id::text = $1 AND series_at = $2`, id, o.At).Scan(&child)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return id, old == nil, err
		}
		if child != "" {
			c, err := s.Get(ctx, viewer, child)
			if err != nil {
				return id, old == nil, err
			}
			x, inv := synced(&c, o.Event)
			x.Repeat, x.Until = "", nil
			if _, err := s.Save(ctx, viewer, x, inv); err != nil {
				return id, old == nil, err
			}
			continue
		}
		series := e
		series.ID = id
		x, _ := synced(&series, o.Event)
		// an occurrence the series does not have is left: the program shows it, go24 does not
		if _, err := s.SaveOccurrence(ctx, viewer, id, o.At, ScopeOne, x, invite); err != nil && !errors.Is(err, ErrNotFound) {
			return id, old == nil, err
		}
	}
	return id, old == nil, nil
}

// synced is the meeting old (nil — a new one) as a calendar file tells it, and the people it keeps.
func synced(old *Event, in Event) (Event, []string) {
	if old == nil {
		in.ID, in.ProjectID, in.Company, in.RoomID, in.Video = "", "", false, "", false
		return in, nil
	}
	e := *old
	e.Title, e.Description, e.Start, e.End, e.AllDay = in.Title, in.Description, in.Start, in.End, in.AllDay
	e.Repeat, e.Every, e.Until, e.Reminds, e.Private = in.Repeat, in.Every, in.Until, in.Reminds, in.Private
	e.Days, e.Count = in.Days, in.Count
	// the file tells the room in the place (see writeEvent)
	e.Where = in.Where
	if in.TZ != "" || in.AllDay { // a time in UTC keeps the zone the meeting had
		e.TZ = in.TZ
	}
	if e.RoomName != "" {
		e.Where = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(in.Where), e.RoomName))
	}
	if in.Accessibility == "free" || e.Accessibility == "free" { // a file tells busy or free only
		e.Accessibility = in.Accessibility
	}
	var invite []string
	for _, a := range old.Attendees {
		invite = append(invite, a.ID)
	}
	return e, invite
}

// DavDelete deletes the meeting named name from the viewer's collection: their own one goes away, one they are
// invited to is declined.
func (s *Store) DavDelete(ctx context.Context, viewer, calendarID, name string) error {
	it, err := s.DavItemOf(ctx, viewer, calendarID, name)
	if err != nil {
		return err
	}
	switch {
	case it == nil:
		return ErrNotFound
	case it.Event.OwnerID == viewer:
		return s.Delete(ctx, viewer, it.Event.ID)
	case it.Event.MyStatus != "":
		return s.Respond(ctx, viewer, it.Event.ID, "declined")
	}
	return ErrForbidden
}
