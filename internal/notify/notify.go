// Package notify is «Уведомления»: the bell with what happened to a person's tasks and posts.
// Notifications are written by database triggers (see migration 022); this package lists and marks them read.
package notify

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("notification not found")

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

type Item struct {
	ID          int64
	Kind        string // task_new | comment | status | deadline | responsible | participant | post_comment | like | important
	ActorID     string
	ActorName   string
	ActorPhoto  string // photo key
	TaskKey     string // "" for feed notifications
	TaskTitle   string
	PostID      string
	PostTitle   string // title or the start of the post
	EventID     string // a meeting (invitation, answer)
	EventTitle  string
	ProjectKey  string // an invitation to a project
	ProjectName string
	URL         string // where it leads, when the notification says so itself (a CRM robot)
	Body        string
	CreatedAt   time.Time
	Read        bool
}

// Link is where the notification leads.
func (i Item) Link() string {
	switch {
	case i.URL != "":
		return i.URL
	case i.TaskKey != "":
		return "/t/" + i.TaskKey
	case i.PostID != "":
		return "/feed#post-" + i.PostID
	case i.EventID != "":
		return "/calendar/" + i.EventID
	case i.ProjectKey != "":
		return "/projects#invites"
	}
	return "/"
}

// Deadline parses the new deadline of a «deadline» notification (nil: removed or another kind).
func (i Item) Deadline() *time.Time {
	if i.Kind != "deadline" || i.Body == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, i.Body)
	if err != nil {
		return nil
	}
	return &t
}

const selectSQL = `SELECT n.id, n.kind, coalesce(n.actor_id::text, ''), coalesce(a.name, ''), coalesce(a.photo_key, ''),
		coalesce(p.key || '-' || t.number, ''), coalesce(t.title, ''), coalesce(n.post_id::text, ''),
		coalesce(nullif(fp.title, ''), left(fp.body, 80), ''), coalesce(n.event_id::text, ''), coalesce(ev.title, ''),
		coalesce(np.key, ''), coalesce(np.name, ''), coalesce(n.url, ''), n.body, n.created_at, n.read_at IS NOT NULL
	FROM notifications n
	LEFT JOIN users a ON a.id = n.actor_id
	LEFT JOIN tasks t ON t.id = n.task_id LEFT JOIN projects p ON p.id = t.project_id
	LEFT JOIN feed_posts fp ON fp.id = n.post_id
	LEFT JOIN events ev ON ev.id = n.event_id
	LEFT JOIN projects np ON np.id = n.project_id`

func scan(rows pgx.Rows) ([]Item, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var i Item
		err := r.Scan(&i.ID, &i.Kind, &i.ActorID, &i.ActorName, &i.ActorPhoto, &i.TaskKey, &i.TaskTitle, &i.PostID, &i.PostTitle,
			&i.EventID, &i.EventTitle, &i.ProjectKey, &i.ProjectName, &i.URL, &i.Body, &i.CreatedAt, &i.Read)
		return i, err
	})
}

// List returns the user's newest notifications, older than before (0: from the newest).
func (s *Store) List(ctx context.Context, userID string, before int64, limit int) ([]Item, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.db.Query(ctx, selectSQL+` WHERE n.user_id = $1 AND n.id < $2 ORDER BY n.id DESC LIMIT $3`, userID, before, limit)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// Get loads one notification of the user.
func (s *Store) Get(ctx context.Context, userID, id string) (Item, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return Item{}, ErrNotFound
	}
	rows, err := s.db.Query(ctx, selectSQL+` WHERE n.user_id = $1 AND n.id = $2`, userID, n)
	if err != nil {
		return Item{}, err
	}
	list, err := scan(rows)
	if err != nil {
		return Item{}, err
	}
	if len(list) == 0 {
		return Item{}, ErrNotFound
	}
	return list[0], nil
}

func (s *Store) Unread(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkRead marks one notification read (id 0: all of them).
func (s *Store) MarkRead(ctx context.Context, userID string, id int64) error {
	_, err := s.db.Exec(ctx, `UPDATE notifications SET read_at = now()
		WHERE user_id = $1 AND read_at IS NULL AND ($2 = 0 OR id = $2)`, userID, id)
	return err
}

// MarkTaskRead: opening a task card reads its notifications.
func (s *Store) MarkTaskRead(ctx context.Context, userID, taskID string) error {
	_, err := s.db.Exec(ctx, `UPDATE notifications SET read_at = now()
		WHERE user_id = $1 AND task_id = $2 AND read_at IS NULL`, userID, taskID)
	return err
}

// MarkCommentsRead reads the user's notifications of new task messages and mentions — the green counter, as
// «Прочитать все» tasks; with a project, only in its tasks. Returns how many were read.
func (s *Store) MarkCommentsRead(ctx context.Context, userID, projectID string) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE notifications SET read_at = now()
		WHERE user_id = $1 AND read_at IS NULL AND kind IN ('comment', 'mention') AND task_id IS NOT NULL
		AND ($2 = '' OR task_id IN (SELECT id FROM tasks_all WHERE project_id::text = $2))`, userID, projectID)
	return tag.RowsAffected(), err
}

// Prune forgets read notifications older than the given age (the portal keeps them for a limited time too).
func (s *Store) Prune(ctx context.Context, age time.Duration) error {
	_, err := s.db.Exec(ctx, `DELETE FROM notifications WHERE read_at IS NOT NULL AND created_at < now() - $1::interval`,
		strconv.Itoa(int(age.Seconds()))+" seconds")
	return err
}

// Text is a notification as one line of plain text (the messenger copy); tr translates locale keys, when formats
// a time. It says the same as the bell's item.
func Text(i Item, tr func(key string, args ...any) string, when func(time.Time) string) string {
	t := i.ActorName + " " + tr("notify.kind."+i.Kind)
	switch i.Kind {
	case "status":
		t += " «" + tr("status."+i.Body) + "»"
	case "deadline":
		if d := i.Deadline(); d != nil {
			t += " " + when(*d)
		} else {
			t += " " + tr("notify.no_deadline")
		}
	case "participant":
		t += " «" + tr("role."+i.Body) + "»"
	case "like":
		t += " " + i.Body
	case "comment_like": // the reaction's emoji, then the comment
		t += " " + i.Reaction()
		i.Body = i.Excerpt()
	case "flow_task", "mail_assign", "mail_recall", "disk_share", "disk_file_share", "disk_comment", "kb_page_changed", "kb_page_new", "kb_page_trashed", "kb_comment", "kb_reply", "kb_mention", "kb_like":
		t += " «" + i.Body + "»"
	case "subtasks_done":
		t = "✅ " + tr("notify.kind.subtasks_done")
	case "event_reply":
		t = i.ActorName + " " + tr("notify.kind.event_reply."+i.Body)
	case "task_remind":
		t = "⏰ " + tr("notify.kind.task_remind")
	case "crm_robot", "task_robot":
		return "🤖 " + i.Body
	case "crm_activity", "crm_recurring", "crm_observer", "crm_comment", "crm_stage":
		t += " «" + i.Body + "»"
	case "crm_activity_remind":
		return "⏰ " + tr("notify.kind.crm_activity_remind") + ": " + i.Body
	case "sign_request", "sign_review":
		t += " «" + i.Body + "»"
	case "sign_done", "sign_refused", "sign_cancelled":
		return "✍ " + tr("notify.kind."+i.Kind) + ": " + i.Body
	case "booking_new", "booking_late", "booking_unconfirmed", "booking_confirmed", "booking_cancelled", "booking_rated", "event_slot", "event_slot_cancel":
		return "📅 " + tr("notify.kind."+i.Kind) + ": " + i.Body
	case "bp":
		return "⚙ " + i.Body
	case "backup_failed", "backup_restored", "backup_restore_failed":
		return "💾 " + tr("notify.kind."+i.Kind) + ": " + tr(i.Body)
	case "workday_change":
		t += " " + i.Body
	case "workday_decided": // "approved|25.10.2026"
		status, day, _ := strings.Cut(i.Body, "|")
		return i.ActorName + " " + tr("notify.kind.workday_decided."+status) + " " + day
	case "honor": // the reason
		t = "🏆 " + i.ActorName + " " + tr("notify.kind.honor")
		if i.Body != "" {
			t += ": " + i.Body
		}
		return t
	case "work_report", "work_report_comment": // the period (and whose)
		t += " · " + i.Body
	case "work_report_mark": // "good|20.10.2026 — 23.10.2026"
		mark, period, _ := strings.Cut(i.Body, "|")
		return i.ActorName + " " + tr("notify.kind.work_report_mark") + " " + period + ": " + tr("wreport.mark."+mark)
	case "absence_hr": // "added|vacation|Пётр Иванов|01.11.2026|14.11.2026" — an HR officer's action with an absence
		if f := i.HRParts(); f != nil {
			return i.ActorName + " " + tr("notify.kind.absence_hr."+f[0]) + " «" + tr("absence.kind."+f[1]) + "» · " + f[2] + ", " + f[3] + " — " + f[4]
		}
	case "openline":
		return "💬 " + i.Body
	case "site_form":
		return "🌐 " + tr("notify.kind.site_form") + ": " + i.Body
	case "crm_form":
		return "📋 " + tr("notify.kind.crm_form") + ": " + i.Body
	case "phone":
		return "📞 " + i.Body
	case "im_remind":
		return "⏰ " + tr("notify.kind.im_remind") + ": " + i.Body
	case "mail":
		return "✉ " + i.Body
	case "mail_remind":
		return "⏰ " + tr("notify.kind.mail_remind") + ": " + i.Body
	case "event_remind":
		t = "⏰ " + tr("notify.kind.event_remind")
		if at, err := time.Parse(time.RFC3339, i.Body); err == nil {
			t += " " + when(at)
		}
	}
	if i.TaskKey != "" {
		t += " · " + i.TaskKey + " " + i.TaskTitle
	} else if i.PostTitle != "" {
		t += " · " + i.PostTitle
	} else if i.EventTitle != "" {
		t += " · " + i.EventTitle
	} else if i.ProjectName != "" {
		t += " · " + i.ProjectName
	}
	if i.Body != "" && (i.Kind == "comment" || i.Kind == "post_comment" || i.Kind == "post_followed" || i.Kind == "mention" || i.Kind == "comment_like" || i.Kind == "event_comment") {
		t += "\n" + i.Body
	}
	return t
}

// Split splits a body "a|b" in two (b "" when there is no «|»): the status and the day of «workday_decided», the
// mark and the period of «work_report_mark».
func (i Item) Split() []string {
	a, b, _ := strings.Cut(i.Body, "|")
	return []string{a, b}
}

// HRParts splits an «absence_hr» body: the action, the reason, whose, from, to (nil — another kind).
func (i Item) HRParts() []string {
	if f := strings.Split(i.Body, "|"); i.Kind == "absence_hr" && len(f) == 5 {
		return f
	}
	return nil
}

// Reaction is the emoji of a reaction to a feed post or comment ("like", "comment_like").
func (i Item) Reaction() string {
	emoji, _, _ := strings.Cut(i.Body, " ")
	return emoji
}

// Excerpt is the text of the comment a reaction is to ("comment_like").
func (i Item) Excerpt() string {
	_, text, _ := strings.Cut(i.Body, " ")
	return text
}

// Kinds are the notification kinds a person can turn off, grouped as on the settings page.
var Kinds = []struct {
	Group string
	Kinds []string
}{
	{"tasks", []string{"task_new", "flow_task", "subtasks_done", "comment", "mention", "status", "deadline", "responsible", "participant", "task_remind", "task_robot"}},
	{"feed", []string{"post_to_you", "post_shared", "post_comment", "post_followed", "like", "comment_like", "important", "gratitude"}},
	{"calendar", []string{"event", "event_open", "event_reply", "event_remind", "event_comment", "event_slot", "event_slot_cancel"}},
	{"projects", []string{"project_invite", "collab_added", "collab_joined", "collab_manager", "collab_member", "collab_removed", "collab_owner"}},
	{"crm", []string{"crm_activity", "crm_activity_remind", "crm_recurring", "crm_observer", "crm_comment", "crm_stage", "crm_robot", "bp", "openline", "site_form", "crm_form"}},
	{"telephony", []string{"phone"}},
	{"mail", []string{"mail", "mail_assign", "mail_remind", "mail_recall"}},
	{"messenger", []string{"im_remind"}},
	{"disk", []string{"disk_share", "disk_file_share", "disk_comment"}},
	{"kb", []string{"kb_page_changed", "kb_page_new", "kb_page_trashed", "kb_comment", "kb_reply", "kb_mention", "kb_like"}},
	{"sign", []string{"sign_request", "sign_review", "sign_done", "sign_refused", "sign_cancelled"}},
	{"booking", []string{"booking_new", "booking_late", "booking_unconfirmed", "booking_confirmed", "booking_cancelled", "booking_rated"}},
	{"company", []string{"workday_change", "workday_decided", "work_report", "work_report_mark", "work_report_comment", "absence_hr", "honor", "backup_failed", "backup_restored", "backup_restore_failed"}},
}

// Settings of a person: kinds turned off, copies in the messenger.
type Settings struct {
	Off    map[string]bool
	Matrix bool
}

func (s *Store) Settings(ctx context.Context, userID string) (Settings, error) {
	var off []string
	st := Settings{Off: map[string]bool{}}
	err := s.db.QueryRow(ctx, `SELECT notify_off, notify_matrix FROM users WHERE id = $1`, userID).Scan(&off, &st.Matrix)
	for _, k := range off {
		st.Off[k] = true
	}
	return st, err
}

// SaveSettings keeps the kinds the person wants (on) and the messenger choice.
func (s *Store) SaveSettings(ctx context.Context, userID string, on map[string]bool, matrix bool) error {
	off := []string{}
	for _, g := range Kinds {
		for _, k := range g.Kinds {
			if !on[k] {
				off = append(off, k)
			}
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE users SET notify_off = $2, notify_matrix = $3 WHERE id = $1`, userID, off, matrix)
	return err
}
