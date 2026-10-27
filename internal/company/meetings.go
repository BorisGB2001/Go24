package company

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Собрания и планёрки» («Время и отчёты»): a meeting has its organizer, the one who keeps the minutes
// (the secretary) and the members; its agenda — items and sub-items with the reporter, the time for it, the result
// (the decision) and the tasks set on it. The meeting is prepared, started («Начать собрание») and finished — the
// minutes (protocol) stay; «Следующее собрание» makes the next one of the series with the same people, the items not
// discussed and the open tasks of this one in view. Members, the organizer, the secretary and administrators see the
// meeting; the organizer, the secretary and administrators change it.

// Meeting statuses.
const (
	MeetingPrepare = "prepare"
	MeetingAction  = "action"
	MeetingDone    = "done"
)

// MeetingStatuses in their order.
var MeetingStatuses = []string{MeetingPrepare, MeetingAction, MeetingDone}

// Limits of the texts, in characters.
const (
	MeetingTitleMax = 255
	MeetingTextMax  = 20000
)

// Meeting is a meeting with (Meeting only) its agenda.
type Meeting struct {
	ID                        int64
	Title, Description, Place string
	Start                     time.Time
	Minutes                   int
	Status                    string
	Owner                     Person
	Keeper                    Person // ID "" — none
	Members                   []Person
	PrevID                    int64 // the previous meeting of the series (0 — none)
	PrevTitle                 string
	PrevStart                 time.Time
	NextID                    int64 // the meeting made next to this one (0 — none)
	EventID                   string
	Protocol                  string // the common result of the meeting
	Started, Finished         *time.Time
	Items                     []MeetingItem // Meeting only: in the agenda's order, sub-items after their item
	Carried                   []MeetingTask // Meeting only: the open tasks of the previous meeting
}

// End is when the meeting is planned to end.
func (m Meeting) End() time.Time { return m.Start.Add(time.Duration(m.Minutes) * time.Minute) }

// CanEdit: the organizer, the secretary and administrators change the meeting.
func (m Meeting) CanEdit(userID string, admin bool) bool {
	return admin || userID == m.Owner.ID || (m.Keeper.ID != "" && userID == m.Keeper.ID)
}

// MemberIDs are the IDs of the members.
func (m Meeting) MemberIDs() []string {
	ids := make([]string, len(m.Members))
	for i, p := range m.Members {
		ids[i] = p.ID
	}
	return ids
}

// People are everyone of the meeting: the organizer, the secretary and the members, once each.
func (m Meeting) People() []string {
	ids := []string{m.Owner.ID}
	if m.Keeper.ID != "" && m.Keeper.ID != m.Owner.ID {
		ids = append(ids, m.Keeper.ID)
	}
	for _, id := range m.MemberIDs() {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// MeetingItem is an item of the agenda.
type MeetingItem struct {
	ID       int64
	ParentID int64  // 0 — a top item
	Num      string // its number in the agenda: 1, 2, 2.1…
	Title    string
	Reporter Person // ID "" — none
	Minutes  int    // the time for the item (0 — not set)
	Result   string
	Tasks    []MeetingTask
}

// MeetingTask is a task set on an item.
type MeetingTask struct {
	ItemID      int64
	ItemTitle   string
	ID, Key     string
	Title       string
	Status      string
	Responsible Person
	Deadline    *time.Time
}

// Open: the task is not completed.
func (t MeetingTask) Open() bool { return t.Status != "completed" }

// MeetingFilter narrows the list.
type MeetingFilter struct {
	Role   string // "" — all the viewer sees, owner — organized by the viewer, member — the viewer is in
	Status string // "" — any
	Query  string // in the title
}

// meetingSees is the condition of a meeting (m) the viewer ($1) sees ($2 — is an administrator).
const meetingSees = `($2 OR m.owner_id::text = $1 OR m.keeper_id::text = $1
	OR EXISTS (SELECT 1 FROM meeting_members mm WHERE mm.meeting_id = m.id AND mm.user_id::text = $1))`

const meetingSQL = `SELECT m.id, m.title, m.description, m.place, m.starts_at, m.minutes, m.status,
		o.id::text, o.name, o.position, o.photo_key,
		coalesce(k.id::text, ''), coalesce(k.name, ''), coalesce(k.position, ''), coalesce(k.photo_key, ''),
		coalesce(m.prev_id, 0), coalesce(p.title, ''), coalesce(p.starts_at, 'epoch'),
		coalesce((SELECT max(n.id) FROM meetings n WHERE n.prev_id = m.id), 0),
		coalesce(m.event_id::text, ''), m.protocol, m.started_at, m.finished_at,
		ARRAY(SELECT u.id::text || '|' || u.name || '|' || u.position || '|' || u.photo_key FROM meeting_members mm
			JOIN users u ON u.id = mm.user_id WHERE mm.meeting_id = m.id ORDER BY u.name)
	FROM meetings m JOIN users o ON o.id = m.owner_id LEFT JOIN users k ON k.id = m.keeper_id
	LEFT JOIN meetings p ON p.id = m.prev_id`

func scanMeetings(rows pgx.Rows, err error) ([]Meeting, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Meeting, error) {
		var m Meeting
		var members []string
		err := row.Scan(&m.ID, &m.Title, &m.Description, &m.Place, &m.Start, &m.Minutes, &m.Status,
			&m.Owner.ID, &m.Owner.Name, &m.Owner.Position, &m.Owner.PhotoKey,
			&m.Keeper.ID, &m.Keeper.Name, &m.Keeper.Position, &m.Keeper.PhotoKey,
			&m.PrevID, &m.PrevTitle, &m.PrevStart, &m.NextID, &m.EventID, &m.Protocol, &m.Started, &m.Finished, &members)
		for _, v := range members {
			f := strings.SplitN(v, "|", 4)
			if len(f) == 4 {
				m.Members = append(m.Members, Person{ID: f[0], Name: f[1], Position: f[2], PhotoKey: f[3]})
			}
		}
		return m, err
	})
}

// Meetings lists the meetings the viewer sees, the latest first.
func (s *Store) Meetings(ctx context.Context, viewer string, admin bool, f MeetingFilter) ([]Meeting, error) {
	args := []any{viewer, admin}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where := []string{meetingSees}
	switch f.Role {
	case "owner":
		where = append(where, "m.owner_id::text = $1")
	case "member":
		where = append(where, "(m.keeper_id::text = $1 OR EXISTS (SELECT 1 FROM meeting_members mm WHERE mm.meeting_id = m.id AND mm.user_id::text = $1))")
	}
	if f.Status != "" {
		where = append(where, "m.status = "+arg(f.Status))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		where = append(where, "m.title ILIKE '%' || "+arg(q)+" || '%'")
	}
	return scanMeetings(s.db.Query(ctx, meetingSQL+" WHERE "+strings.Join(where, " AND ")+" ORDER BY m.starts_at DESC, m.id DESC LIMIT 500", args...))
}

// meetingTaskSQL selects the tasks of items.
const meetingTaskSQL = `SELECT i.id, i.title, t.id::text, pr.key || '-' || t.number, t.title, t.status,
		r.id::text, r.name, r.position, r.photo_key, t.deadline
	FROM meeting_item_tasks it JOIN meeting_items i ON i.id = it.item_id JOIN tasks t ON t.id = it.task_id
	JOIN projects pr ON pr.id = t.project_id JOIN users r ON r.id = t.responsible_id`

func scanMeetingTasks(rows pgx.Rows, err error) ([]MeetingTask, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (MeetingTask, error) {
		var t MeetingTask
		err := row.Scan(&t.ItemID, &t.ItemTitle, &t.ID, &t.Key, &t.Title, &t.Status,
			&t.Responsible.ID, &t.Responsible.Name, &t.Responsible.Position, &t.Responsible.PhotoKey, &t.Deadline)
		return t, err
	})
}

// Meeting returns a meeting the viewer sees, with its agenda and the open tasks of the previous meeting.
func (s *Store) Meeting(ctx context.Context, viewer string, admin bool, id int64) (Meeting, error) {
	list, err := scanMeetings(s.db.Query(ctx, meetingSQL+" WHERE m.id = $3 AND "+meetingSees, viewer, admin, id))
	if err != nil {
		return Meeting{}, err
	}
	if len(list) == 0 {
		return Meeting{}, ErrNotFound
	}
	m := list[0]
	rows, err := s.db.Query(ctx, `SELECT i.id, coalesce(i.parent_id, 0), i.title, coalesce(u.id::text, ''), coalesce(u.name, ''),
			coalesce(u.position, ''), coalesce(u.photo_key, ''), i.minutes, i.result
		FROM meeting_items i LEFT JOIN meeting_items p ON p.id = i.parent_id LEFT JOIN users u ON u.id = i.reporter_id
		WHERE i.meeting_id = $1 ORDER BY coalesce(p.sort, i.sort), coalesce(p.id, i.id), i.parent_id IS NOT NULL, i.sort, i.id`, id)
	if err != nil {
		return Meeting{}, err
	}
	if m.Items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (MeetingItem, error) {
		var i MeetingItem
		err := row.Scan(&i.ID, &i.ParentID, &i.Title, &i.Reporter.ID, &i.Reporter.Name, &i.Reporter.Position, &i.Reporter.PhotoKey,
			&i.Minutes, &i.Result)
		return i, err
	}); err != nil {
		return Meeting{}, err
	}
	top, sub := 0, 0
	for i := range m.Items {
		if m.Items[i].ParentID == 0 {
			top, sub = top+1, 0
			m.Items[i].Num = strconv.Itoa(top)
		} else {
			sub++
			m.Items[i].Num = strconv.Itoa(top) + "." + strconv.Itoa(sub)
		}
	}
	tasks, err := scanMeetingTasks(s.db.Query(ctx, meetingTaskSQL+" WHERE i.meeting_id = $1 ORDER BY pr.key, t.number", id))
	if err != nil {
		return Meeting{}, err
	}
	for _, t := range tasks {
		for i := range m.Items {
			if m.Items[i].ID == t.ItemID {
				m.Items[i].Tasks = append(m.Items[i].Tasks, t)
			}
		}
	}
	if m.PrevID != 0 {
		if m.Carried, err = scanMeetingTasks(s.db.Query(ctx, meetingTaskSQL+` WHERE i.meeting_id = $1 AND t.status <> 'completed'
			ORDER BY t.deadline NULLS LAST, pr.key, t.number`, m.PrevID)); err != nil {
			return Meeting{}, err
		}
	}
	return m, nil
}

// editable returns the meeting the actor may change (ErrForbidden — only sees, ErrNotFound — does not see).
func (s *Store) editable(ctx context.Context, actor string, admin bool, id int64) (Meeting, error) {
	list, err := scanMeetings(s.db.Query(ctx, meetingSQL+" WHERE m.id = $3 AND "+meetingSees, actor, admin, id))
	if err != nil {
		return Meeting{}, err
	}
	if len(list) == 0 {
		return Meeting{}, ErrNotFound
	}
	if !list[0].CanEdit(actor, admin) {
		return Meeting{}, ErrForbidden
	}
	return list[0], nil
}

// staffOnly keeps the IDs of active employees (not guests), once each.
func (s *Store) staffOnly(ctx context.Context, q pgx.Tx, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id::text FROM users WHERE id::text = ANY($1) AND active AND NOT guest ORDER BY name`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SaveMeeting creates (ID 0) or changes the meeting with exactly these members; the organizer of a new one is the
// actor unless m.Owner.ID says another; newly added people are notified. Returns the meeting's ID.
func (s *Store) SaveMeeting(ctx context.Context, actor string, admin bool, m Meeting, members []string) (int64, error) {
	m.Title, m.Description, m.Place = strings.TrimSpace(m.Title), strings.TrimSpace(m.Description), strings.TrimSpace(m.Place)
	if m.Title == "" {
		return 0, i18n.E("err.meeting_title")
	}
	if utf8.RuneCountInString(m.Title) > MeetingTitleMax || utf8.RuneCountInString(m.Description) > MeetingTextMax ||
		utf8.RuneCountInString(m.Place) > MeetingTitleMax || m.Start.IsZero() || m.Minutes < 5 || m.Minutes > 24*60 {
		return 0, i18n.E("err.bad_form")
	}
	var was Meeting
	if m.ID != 0 {
		var err error
		if was, err = s.editable(ctx, actor, admin, m.ID); err != nil {
			return 0, err
		}
		if m.Owner.ID == "" {
			m.Owner = was.Owner
		}
	} else if m.Owner.ID == "" {
		m.Owner.ID = actor
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	ok, err := s.staffOnly(ctx, tx, []string{m.Owner.ID})
	if err != nil {
		return 0, err
	}
	if len(ok) == 0 {
		return 0, i18n.E("err.bad_form")
	}
	var keeper any
	if m.Keeper.ID != "" {
		if ok, err = s.staffOnly(ctx, tx, []string{m.Keeper.ID}); err != nil {
			return 0, err
		}
		if len(ok) == 0 {
			return 0, i18n.E("err.bad_form")
		}
		keeper = m.Keeper.ID
	}
	if members, err = s.staffOnly(ctx, tx, members); err != nil {
		return 0, err
	}
	if members == nil {
		members = []string{} // not NULL: no one stays
	}
	var prev any
	if m.PrevID != 0 && m.ID == 0 { // the series goes on from a meeting the actor sees
		if _, err := s.editable(ctx, actor, admin, m.PrevID); err != nil && err != ErrForbidden {
			return 0, err
		}
		prev = m.PrevID
	}
	if m.ID == 0 {
		if err := tx.QueryRow(ctx, `INSERT INTO meetings (title, description, place, starts_at, minutes, owner_id, keeper_id, prev_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`, m.Title, m.Description, m.Place, m.Start, m.Minutes, m.Owner.ID, keeper, prev).Scan(&m.ID); err != nil {
			return 0, err
		}
	} else if _, err := tx.Exec(ctx, `UPDATE meetings SET title = $2, description = $3, place = $4, starts_at = $5, minutes = $6,
		owner_id = $7, keeper_id = $8 WHERE id = $1`, m.ID, m.Title, m.Description, m.Place, m.Start, m.Minutes, m.Owner.ID, keeper); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM meeting_members WHERE meeting_id = $1 AND NOT (user_id::text = ANY($2))`, m.ID, members); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO meeting_members (meeting_id, user_id) SELECT $1, u::uuid FROM unnest($2::text[]) u
		ON CONFLICT DO NOTHING`, m.ID, members); err != nil {
		return 0, err
	}
	// the people newly in the meeting learn of it
	m.Members = nil
	for _, id := range members {
		m.Members = append(m.Members, Person{ID: id})
	}
	var fresh []string
	for _, id := range m.People() {
		if id != actor && !slices.Contains(was.People(), id) {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
			SELECT u::uuid, $2, 'meeting', $3, $4 FROM unnest($1::text[]) u`, fresh, actor,
			strings.ReplaceAll(m.Title, "|", "/")+"|"+m.Start.In(time.Local).Format("02.01.2006 15:04"), "/worktime/meetings/"+strconv.FormatInt(m.ID, 10)); err != nil {
			return 0, err
		}
	}
	return m.ID, tx.Commit(ctx)
}

// SetMeetingEvent remembers the meeting of the calendar ("" — none).
func (s *Store) SetMeetingEvent(ctx context.Context, id int64, eventID string) error {
	var ev any
	if eventID != "" {
		ev = eventID
	}
	_, err := s.db.Exec(ctx, `UPDATE meetings SET event_id = $2 WHERE id = $1`, id, ev)
	return err
}

// DeleteMeeting deletes the meeting with its agenda (the tasks stay) and returns it as it was.
func (s *Store) DeleteMeeting(ctx context.Context, actor string, admin bool, id int64) (Meeting, error) {
	m, err := s.editable(ctx, actor, admin, id)
	if err != nil {
		return Meeting{}, err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM meetings WHERE id = $1`, id)
	return m, err
}

// SetMeetingStatus starts (action), finishes (done) or takes back to work (action from done) the meeting; a started
// meeting goes back to preparing too.
func (s *Store) SetMeetingStatus(ctx context.Context, actor string, admin bool, id int64, status string) error {
	m, err := s.editable(ctx, actor, admin, id)
	if err != nil {
		return err
	}
	if !slices.Contains(MeetingStatuses, status) {
		return i18n.E("err.bad_form")
	}
	if status == m.Status {
		return nil
	}
	_, err = s.db.Exec(ctx, `UPDATE meetings SET status = $2,
		started_at = CASE WHEN $2 = 'prepare' THEN NULL WHEN $2 = 'action' AND started_at IS NULL THEN now() ELSE started_at END,
		finished_at = CASE WHEN $2 = 'done' THEN now() ELSE NULL END WHERE id = $1`, id, status)
	return err
}

// SetMeetingProtocol writes the common result of the meeting.
func (s *Store) SetMeetingProtocol(ctx context.Context, actor string, admin bool, id int64, text string) error {
	if _, err := s.agenda(ctx, actor, admin, id); err != nil {
		return err
	}
	if text = strings.TrimSpace(text); utf8.RuneCountInString(text) > MeetingTextMax {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `UPDATE meetings SET protocol = $2 WHERE id = $1`, id, text)
	return err
}

// agenda returns the meeting whose agenda and minutes the actor may change: a finished meeting is read-only until
// taken back to work.
func (s *Store) agenda(ctx context.Context, actor string, admin bool, id int64) (Meeting, error) {
	m, err := s.editable(ctx, actor, admin, id)
	if err == nil && m.Status == MeetingDone {
		err = i18n.E("err.meeting_done")
	}
	return m, err
}

// itemMeeting is the meeting of an item the actor may change.
func (s *Store) itemMeeting(ctx context.Context, actor string, admin bool, itemID int64) (Meeting, error) {
	var id int64
	if err := s.db.QueryRow(ctx, `SELECT meeting_id FROM meeting_items WHERE id = $1`, itemID).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return Meeting{}, ErrNotFound
		}
		return Meeting{}, err
	}
	return s.agenda(ctx, actor, admin, id)
}

// checkItem validates an item's fields.
func (s *Store) checkItem(ctx context.Context, i *MeetingItem) error {
	i.Title, i.Result = strings.TrimSpace(i.Title), strings.TrimSpace(i.Result)
	if i.Title == "" {
		return i18n.E("err.meeting_item_title")
	}
	if utf8.RuneCountInString(i.Title) > MeetingTitleMax || utf8.RuneCountInString(i.Result) > MeetingTextMax || i.Minutes < 0 || i.Minutes > 24*60 {
		return i18n.E("err.bad_form")
	}
	if i.Reporter.ID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active AND NOT guest)`, i.Reporter.ID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return i18n.E("err.bad_form")
		}
	}
	return nil
}

// AddMeetingItem adds an item at the end of the agenda (or a sub-item at the end of its item) and returns its ID.
func (s *Store) AddMeetingItem(ctx context.Context, actor string, admin bool, meetingID int64, i MeetingItem) (int64, error) {
	if _, err := s.agenda(ctx, actor, admin, meetingID); err != nil {
		return 0, err
	}
	if err := s.checkItem(ctx, &i); err != nil {
		return 0, err
	}
	var parent, reporter any
	if i.ParentID != 0 {
		var ok bool // a sub-item of a top item of the same meeting: the agenda has two levels
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM meeting_items WHERE id = $1 AND meeting_id = $2 AND parent_id IS NULL)`,
			i.ParentID, meetingID).Scan(&ok); err != nil {
			return 0, err
		}
		if !ok {
			return 0, i18n.E("err.bad_form")
		}
		parent = i.ParentID
	}
	if i.Reporter.ID != "" {
		reporter = i.Reporter.ID
	}
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO meeting_items (meeting_id, parent_id, sort, title, reporter_id, minutes, result)
		VALUES ($1, $2, coalesce((SELECT max(sort) + 1 FROM meeting_items WHERE meeting_id = $1 AND parent_id IS NOT DISTINCT FROM $2), 0),
			$3, $4, $5, $6) RETURNING id`, meetingID, parent, i.Title, reporter, i.Minutes, i.Result).Scan(&id)
	return id, err
}

// UpdateMeetingItem changes the item's title, reporter, time and result.
func (s *Store) UpdateMeetingItem(ctx context.Context, actor string, admin bool, i MeetingItem) error {
	if _, err := s.itemMeeting(ctx, actor, admin, i.ID); err != nil {
		return err
	}
	if err := s.checkItem(ctx, &i); err != nil {
		return err
	}
	var reporter any
	if i.Reporter.ID != "" {
		reporter = i.Reporter.ID
	}
	_, err := s.db.Exec(ctx, `UPDATE meeting_items SET title = $2, reporter_id = $3, minutes = $4, result = $5 WHERE id = $1`,
		i.ID, i.Title, reporter, i.Minutes, i.Result)
	return err
}

// SetMeetingItemResult writes the result (the decision) of the item.
func (s *Store) SetMeetingItemResult(ctx context.Context, actor string, admin bool, itemID int64, result string) error {
	if _, err := s.itemMeeting(ctx, actor, admin, itemID); err != nil {
		return err
	}
	if result = strings.TrimSpace(result); utf8.RuneCountInString(result) > MeetingTextMax {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `UPDATE meeting_items SET result = $2 WHERE id = $1`, itemID, result)
	return err
}

// DeleteMeetingItem deletes the item with its sub-items (their tasks stay).
func (s *Store) DeleteMeetingItem(ctx context.Context, actor string, admin bool, itemID int64) error {
	if _, err := s.itemMeeting(ctx, actor, admin, itemID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM meeting_items WHERE id = $1`, itemID)
	return err
}

// MoveMeetingItem moves the item one place up (up) or down among its neighbours.
func (s *Store) MoveMeetingItem(ctx context.Context, actor string, admin bool, itemID int64, up bool) error {
	if _, err := s.itemMeeting(ctx, actor, admin, itemID); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT n.id FROM meeting_items i JOIN meeting_items n ON n.meeting_id = i.meeting_id
		AND n.parent_id IS NOT DISTINCT FROM i.parent_id WHERE i.id = $1 ORDER BY n.sort, n.id`, itemID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return err
	}
	at := slices.Index(ids, itemID)
	to := at + 1
	if up {
		to = at - 1
	}
	if at < 0 || to < 0 || to >= len(ids) {
		return nil
	}
	ids[at], ids[to] = ids[to], ids[at]
	for n, id := range ids {
		if _, err := tx.Exec(ctx, `UPDATE meeting_items SET sort = $2 WHERE id = $1`, id, n); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// LinkMeetingTask puts a task (set on the item) among the item's tasks; who may — the caller checks.
func (s *Store) LinkMeetingTask(ctx context.Context, itemID int64, taskID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO meeting_item_tasks (item_id, task_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, itemID, taskID)
	return err
}

// MeetingOfItem returns the meeting of an item the actor may change.
func (s *Store) MeetingOfItem(ctx context.Context, actor string, admin bool, itemID int64) (Meeting, error) {
	return s.itemMeeting(ctx, actor, admin, itemID)
}

// NextMeeting makes the next meeting of the series: the same title, place, length and people, a week later (or
// at the time given), the items not discussed (without a result) carried over with their sub-items; the open
// tasks of this meeting are seen from the next one. Returns the new meeting's ID.
func (s *Store) NextMeeting(ctx context.Context, actor string, admin bool, id int64, at time.Time) (int64, error) {
	m, err := s.editable(ctx, actor, admin, id)
	if err != nil {
		return 0, err
	}
	if at.IsZero() {
		at = m.Start.AddDate(0, 0, 7)
	}
	full, err := s.Meeting(ctx, actor, admin, id)
	if err != nil {
		return 0, err
	}
	next := Meeting{Title: m.Title, Description: m.Description, Place: m.Place, Start: at, Minutes: m.Minutes,
		Owner: m.Owner, Keeper: m.Keeper, PrevID: m.ID}
	nid, err := s.SaveMeeting(ctx, actor, admin, next, m.MemberIDs())
	if err != nil {
		return 0, err
	}
	parents := map[int64]int64{} // the old item → the new one
	for _, i := range full.Items {
		if i.Result != "" {
			continue
		}
		ni := MeetingItem{Title: i.Title, Reporter: i.Reporter, Minutes: i.Minutes}
		if i.ParentID != 0 {
			p, ok := parents[i.ParentID]
			if !ok {
				continue // its item was discussed
			}
			ni.ParentID = p
		}
		if parents[i.ID], err = s.AddMeetingItem(ctx, actor, admin, nid, ni); err != nil {
			return 0, err
		}
	}
	return nid, nil
}
