package mail

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Shared mailboxes of a department, as Outlook's shared mailbox with a helpdesk's touch: the owner of a mailbox
// shares it with a department (its sub-departments too) and with people — to read it, to send as it, to manage it.
// Its conversations are requests: one of the people answers each (assigned, with a notification), and each is new,
// in progress or done; a new letter in a done one opens it again. Guests from outside never see a shared mailbox.

// ShareLevels are the levels of access, from the least.
var ShareLevels = []string{"read", "send", "manage"}

// TicketStatuses are the states of a request.
var TicketStatuses = []string{"new", "progress", "done"}

func levelRank(l string) int {
	for i, x := range ShareLevels {
		if x == l {
			return i + 1
		}
	}
	return 0
}

// LevelAtLeast: does the level give what is needed?
func LevelAtLeast(level, need string) bool {
	return levelRank(level) >= levelRank(need) && levelRank(level) > 0
}

// Share is a shared mailbox.
type Share struct {
	MailboxID, Email, Name       string
	OwnerID, OwnerName           string
	DepartmentID, DepartmentName string
	DeptLevel                    string // none, read, send, manage
	People                       []SharePerson
	Level                        string // the viewer's
	Open                         int    // requests not done
	SyncedAt                     *time.Time
	Error                        string
}

// Title is how the mailbox is called: its name, else its address.
func (sh Share) Title() string {
	if sh.Name != "" {
		return sh.Name
	}
	return sh.Email
}

// SharePerson is someone the mailbox is shared with by name.
type SharePerson struct{ UserID, Name, Level string }

// ErrNotShared: the mailbox is not shared (or not with the user).
var ErrNotShared = i18n.E("err.not_found")

// SharedLevel is the user's access to a shared mailbox ("" — none): the owner manages it; a guest has none.
func (s *Store) SharedLevel(ctx context.Context, userID, mailboxID string) (string, error) {
	var owner, guest, shared bool
	var dept *string
	var deptLevel, personal string
	err := s.db.QueryRow(ctx, `SELECT m.user_id::text = $1, coalesce((SELECT guest FROM users WHERE id::text = $1), true),
			sh.mailbox_id IS NOT NULL, sh.department_id::text, coalesce(sh.dept_level, 'none'),
			coalesce((SELECT level FROM mail_share_people p WHERE p.mailbox_id = m.id AND p.user_id::text = $1), '')
		FROM mailboxes m LEFT JOIN mail_shares sh ON sh.mailbox_id = m.id WHERE m.id::text = $2`, userID, mailboxID).
		Scan(&owner, &guest, &shared, &dept, &deptLevel, &personal)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!shared || guest)) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if owner {
		return "manage", nil
	}
	level := personal
	if dept != nil && deptLevel != "none" && levelRank(deptLevel) > levelRank(level) {
		var member bool
		if err := s.db.QueryRow(ctx, `WITH RECURSIVE d AS (SELECT id FROM departments WHERE id::text = $2
				UNION ALL SELECT x.id FROM departments x JOIN d ON x.parent_id = d.id)
			SELECT EXISTS (SELECT 1 FROM user_departments u JOIN users p ON p.id = u.user_id
				WHERE u.user_id::text = $1 AND p.active AND u.department_id IN (SELECT id FROM d))`, userID, *dept).Scan(&member); err != nil {
			return "", err
		}
		if member {
			level = deptLevel
		}
	}
	return level, nil
}

// sharedCheck: the user has at least the level on the shared mailbox; it gives the share.
func (s *Store) sharedCheck(ctx context.Context, userID, mailboxID, need string) (Share, error) {
	level, err := s.SharedLevel(ctx, userID, mailboxID)
	if err != nil {
		return Share{}, err
	}
	if level == "" {
		return Share{}, ErrNotShared
	}
	if !LevelAtLeast(level, need) {
		return Share{}, ErrForbidden
	}
	sh, err := s.share(ctx, mailboxID)
	sh.Level = level
	return sh, err
}

const shareSQL = `SELECT sh.mailbox_id::text, m.email, sh.name, m.user_id::text, u.name, coalesce(sh.department_id::text, ''),
		coalesce(d.name, ''), sh.dept_level, m.synced_at, m.error,
		(SELECT count(*) FROM mail_tickets t WHERE t.mailbox_id = sh.mailbox_id AND t.status <> 'done')::int
	FROM mail_shares sh JOIN mailboxes m ON m.id = sh.mailbox_id JOIN users u ON u.id = m.user_id
	LEFT JOIN departments d ON d.id = sh.department_id`

func scanShare(r pgx.Row) (Share, error) {
	var sh Share
	err := r.Scan(&sh.MailboxID, &sh.Email, &sh.Name, &sh.OwnerID, &sh.OwnerName, &sh.DepartmentID, &sh.DepartmentName, &sh.DeptLevel,
		&sh.SyncedAt, &sh.Error, &sh.Open)
	return sh, err
}

func (s *Store) share(ctx context.Context, mailboxID string) (Share, error) {
	sh, err := scanShare(s.db.QueryRow(ctx, shareSQL+` WHERE sh.mailbox_id::text = $1`, mailboxID))
	if errors.Is(err, pgx.ErrNoRows) {
		return sh, ErrNotShared
	}
	if err != nil {
		return sh, err
	}
	rows, err := s.db.Query(ctx, `SELECT p.user_id::text, u.name, p.level FROM mail_share_people p JOIN users u ON u.id = p.user_id
		WHERE p.mailbox_id::text = $1 ORDER BY u.name`, mailboxID)
	if err != nil {
		return sh, err
	}
	sh.People, err = pgx.CollectRows(rows, pgx.RowToStructByPos[SharePerson])
	return sh, err
}

// Shares are the shared mailboxes the user may open, with the user's level.
func (s *Store) Shares(ctx context.Context, userID string) ([]Share, error) {
	rows, err := s.db.Query(ctx, `SELECT mailbox_id::text FROM mail_shares ORDER BY name, mailbox_id`)
	if err != nil {
		return nil, err
	}
	ids, err := pgxCollectStrings(rows)
	if err != nil {
		return nil, err
	}
	var out []Share
	for _, id := range ids {
		level, err := s.SharedLevel(ctx, userID, id)
		if err != nil {
			return nil, err
		}
		if level == "" {
			continue
		}
		sh, err := s.share(ctx, id)
		if err != nil {
			return nil, err
		}
		sh.Level = level
		out = append(out, sh)
	}
	return out, nil
}

// SharedBox is a shared mailbox for the user with at least the level.
func (s *Store) SharedBox(ctx context.Context, userID, mailboxID, need string) (Share, error) {
	return s.sharedCheck(ctx, userID, mailboxID, need)
}

// ShareBox makes the owner's mailbox shared (or changes it, for those who manage it): its name for the letters
// sent as it, the department and its level.
func (s *Store) ShareBox(ctx context.Context, userID, mailboxID, name, deptID, deptLevel string) error {
	name = clip(strings.TrimSpace(name), 200)
	if deptLevel != "none" && levelRank(deptLevel) == 0 {
		return i18n.E("err.bad_form")
	}
	var dept any
	if deptID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM departments WHERE id::text = $1)`, deptID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return i18n.E("err.bad_form")
		}
		dept = deptID
	}
	if _, err := s.Mailbox(ctx, userID, mailboxID); err != nil { // not the owner: one who manages it changes it
		if _, err := s.sharedCheck(ctx, userID, mailboxID, "manage"); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_shares (mailbox_id, name, department_id, dept_level, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (mailbox_id) DO UPDATE SET name = EXCLUDED.name, department_id = EXCLUDED.department_id, dept_level = EXCLUDED.dept_level`,
		mailboxID, name, dept, deptLevel, userID)
	return err
}

// UnshareBox: the owner's mailbox is the owner's own again (its requests are forgotten).
func (s *Store) UnshareBox(ctx context.Context, userID, mailboxID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_shares sh USING mailboxes m WHERE m.id = sh.mailbox_id AND sh.mailbox_id::text = $1
		AND m.user_id::text = $2`, mailboxID, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetSharePerson gives a person a level on the shared mailbox ("" — takes it away); a guest from outside never.
func (s *Store) SetSharePerson(ctx context.Context, userID, mailboxID, personID, level string) error {
	if _, err := s.sharedCheck(ctx, userID, mailboxID, "manage"); err != nil {
		return err
	}
	if level == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM mail_share_people WHERE mailbox_id::text = $1 AND user_id::text = $2`, mailboxID, personID)
		return err
	}
	if levelRank(level) == 0 {
		return i18n.E("err.bad_form")
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO mail_share_people (mailbox_id, user_id, level) SELECT $1, u.id, $3 FROM users u
		WHERE u.id::text = $2 AND u.active AND NOT u.guest ON CONFLICT (mailbox_id, user_id) DO UPDATE SET level = EXCLUDED.level`,
		mailboxID, personID, level)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.mail_share_person")
	}
	return err
}

// SharedCan: may the user see the letter (its files) through a shared mailbox?
func (s *Store) SharedCan(ctx context.Context, userID, letterID string) bool {
	var box string
	if err := s.db.QueryRow(ctx, `SELECT x.mailbox_id::text FROM mail_messages x JOIN mail_shares sh ON sh.mailbox_id = x.mailbox_id
		WHERE x.id::text = $1`, letterID).Scan(&box); err != nil {
		return false
	}
	level, err := s.SharedLevel(ctx, userID, box)
	return err == nil && level != ""
}

// ---------- requests ----------

// Ticket is a conversation of a shared mailbox as a request: its newest letter stands for it.
type Ticket struct {
	Thread, LetterID, Subject, FromAddr, FromName, Preview string
	LastAt                                                 time.Time
	Count, Unread                                          int
	Status                                                 string // new, progress, done
	Reopened                                               bool   // done, then a new letter came
	AssigneeID, AssigneeName                               string
}

// From is how the sender is shown.
func (t Ticket) From() string {
	if t.FromName != "" {
		return t.FromName
	}
	return t.FromAddr
}

// TicketFilter narrows the requests: status open (not done) | new | progress | done | all; mine — assigned to the
// user; nobody — assigned to no one; q — words of the subject or the sender.
type TicketFilter struct {
	Status       string
	Mine, Nobody bool
	Q            string
}

// threadKey is a letter's conversation in a shared mailbox.
const threadKey = `coalesce(nullif(x.thread, ''), x.id::text)`

// ticketSQL: the conversations of the mailbox that have letters from outside (not in the trash, the junk, the
// drafts), each with its newest letter, its state and who answers it.
const ticketSQL = `WITH c AS (
	SELECT ` + threadKey + ` AS thread, x.id, x.subject, x.from_addr, x.from_name, left(x.body, 300) AS preview, x.sent_at, x.seen,
		f.role IN ('sent', 'drafts') AS outgoing
	FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id LEFT JOIN mail_drafts d ON d.id = x.id
	WHERE x.mailbox_id::text = $1 AND f.role NOT IN ('trash', 'junk') AND d.id IS NULL),
g AS (SELECT thread, count(*)::int AS n, count(*) FILTER (WHERE NOT seen AND NOT outgoing)::int AS unread,
	max(sent_at) FILTER (WHERE NOT outgoing) AS last_in, max(sent_at) AS last_at FROM c GROUP BY thread HAVING bool_or(NOT outgoing)),
l AS (SELECT DISTINCT ON (thread) * FROM c ORDER BY thread, sent_at DESC),
fi AS (SELECT DISTINCT ON (thread) thread, from_addr, from_name FROM c WHERE NOT outgoing ORDER BY thread, sent_at)
SELECT g.thread, l.id::text AS id, l.subject, fi.from_addr, fi.from_name, l.preview, g.last_at, g.n, g.unread,
	CASE WHEN t.status IS NULL THEN 'new' WHEN t.status = 'done' AND g.last_in > t.updated_at THEN 'progress' ELSE t.status END AS st,
	coalesce(t.status = 'done' AND g.last_in > t.updated_at, false) AS reopened, coalesce(t.assignee_id::text, '') AS assignee,
	coalesce(u.name, '') AS assignee_name
FROM g JOIN l ON l.thread = g.thread JOIN fi ON fi.thread = g.thread
LEFT JOIN mail_tickets t ON t.mailbox_id::text = $1 AND t.thread = g.thread LEFT JOIN users u ON u.id = t.assignee_id`

func scanTickets(rows pgx.Rows, err error) ([]Ticket, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ticket, error) {
		var t Ticket
		err := r.Scan(&t.Thread, &t.LetterID, &t.Subject, &t.FromAddr, &t.FromName, &t.Preview, &t.LastAt, &t.Count, &t.Unread, &t.Status,
			&t.Reopened, &t.AssigneeID, &t.AssigneeName)
		return t, err
	})
}

// Tickets are the requests of a shared mailbox the user may read, the newest first.
func (s *Store) Tickets(ctx context.Context, userID, mailboxID string, f TicketFilter) ([]Ticket, error) {
	if _, err := s.sharedCheck(ctx, userID, mailboxID, "read"); err != nil {
		return nil, err
	}
	where := []string{"true"}
	args := sqlArgs{mailboxID}
	switch f.Status {
	case "new", "progress", "done":
		where = append(where, "st = "+args.add(f.Status))
	case "all":
	default:
		where = append(where, "st <> 'done'")
	}
	if f.Mine {
		where = append(where, "assignee = "+args.add(userID))
	}
	if f.Nobody {
		where = append(where, "assignee = ''")
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		p := args.add("%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%")
		where = append(where, "(subject ILIKE "+p+" OR from_addr ILIKE "+p+" OR from_name ILIKE "+p+")")
	}
	sql := `SELECT * FROM (` + ticketSQL + `) z WHERE ` + strings.Join(where, " AND ") + ` ORDER BY last_at DESC LIMIT ` + args.add(MaxList)
	return scanTickets(s.db.Query(ctx, sql, args...))
}

// LetterTicket is the request of a letter of a shared mailbox (and the mailbox's owner, whose mailbox it is).
func (s *Store) LetterTicket(ctx context.Context, userID, mailboxID, letterID string) (Ticket, error) {
	if _, err := s.sharedCheck(ctx, userID, mailboxID, "read"); err != nil {
		return Ticket{}, err
	}
	var thread string
	err := s.db.QueryRow(ctx, `SELECT `+threadKey+` FROM mail_messages x WHERE x.id::text = $1 AND x.mailbox_id::text = $2`, letterID, mailboxID).Scan(&thread)
	if errors.Is(err, pgx.ErrNoRows) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	list, err := scanTickets(s.db.Query(ctx, ticketSQL+` WHERE g.thread = $2`, mailboxID, thread))
	if err != nil {
		return Ticket{}, err
	}
	if len(list) == 0 { // a conversation of the user's own letters only
		return Ticket{Thread: thread, LetterID: letterID, Status: "new"}, nil
	}
	return list[0], nil
}

// Assign makes someone of the shared mailbox (who may read it) answer the request; "" — no one. The new one is
// told (a notification that leads to the letter).
func (s *Store) Assign(ctx context.Context, userID, mailboxID, letterID, assigneeID string) error {
	if _, err := s.sharedCheck(ctx, userID, mailboxID, "send"); err != nil {
		return err
	}
	t, err := s.LetterTicket(ctx, userID, mailboxID, letterID)
	if err != nil {
		return err
	}
	var who any
	if assigneeID != "" {
		level, err := s.SharedLevel(ctx, assigneeID, mailboxID)
		if err != nil {
			return err
		}
		if level == "" {
			return i18n.E("err.mail_share_person")
		}
		who = assigneeID
	}
	status := t.Status
	if status == "new" && assigneeID != "" {
		status = "progress"
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO mail_tickets (mailbox_id, thread, assignee_id, status, updated_by) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (mailbox_id, thread) DO UPDATE SET assignee_id = EXCLUDED.assignee_id, status = EXCLUDED.status,
			updated_by = EXCLUDED.updated_by, updated_at = now()`, mailboxID, t.Thread, who, status, userID); err != nil {
			return err
		}
		if assigneeID == "" || assigneeID == userID || assigneeID == t.AssigneeID {
			return nil
		}
		subject := t.Subject
		if subject == "" {
			subject = t.From()
		}
		_, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) SELECT $1, $2, 'mail_assign', $3, $4
			WHERE NOT go24_quiet()`, assigneeID, userID, clip(subject, 300), "/mail/shared/"+mailboxID+"?letter="+t.LetterID)
		return err
	})
}

// SetTicketStatus moves the request on: new, progress, done.
func (s *Store) SetTicketStatus(ctx context.Context, userID, mailboxID, letterID, status string) error {
	if status != "new" && status != "progress" && status != "done" {
		return i18n.E("err.bad_form")
	}
	if _, err := s.sharedCheck(ctx, userID, mailboxID, "send"); err != nil {
		return err
	}
	t, err := s.LetterTicket(ctx, userID, mailboxID, letterID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO mail_tickets (mailbox_id, thread, status, updated_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (mailbox_id, thread) DO UPDATE SET status = EXCLUDED.status, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		mailboxID, t.Thread, status, userID)
	return err
}

// SendShared answers a letter of the shared mailbox as the mailbox (its name as the sender's): the answer joins the
// conversation and is kept in the mailbox's «Отправленные»; a new request is now in progress, and the one who
// answered it answers it from now on when nobody did.
func (s *Store) SendShared(ctx context.Context, userID, mailboxID, letterID string, l Letter) error {
	sh, err := s.sharedCheck(ctx, userID, mailboxID, "send")
	if err != nil {
		return err
	}
	t, err := s.LetterTicket(ctx, userID, mailboxID, letterID)
	if err != nil {
		return err
	}
	if l.Refs, err = s.refsOf(ctx, sh.OwnerID, letterID); err != nil {
		return err
	}
	l.fromName = sh.Name
	if l.fromName == "" {
		l.fromName = sh.DepartmentName
	}
	if l.fromName == "" { // the one who answers
		_ = s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, userID).Scan(&l.fromName)
	}
	if err := s.SendLetter(ctx, sh.OwnerID, mailboxID, l); err != nil {
		return err
	}
	status := t.Status
	if status != "done" || t.Reopened {
		status = "progress"
	}
	var who any = t.AssigneeID
	if t.AssigneeID == "" {
		who = userID
	}
	_, err = s.db.Exec(ctx, `INSERT INTO mail_tickets (mailbox_id, thread, assignee_id, status, updated_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (mailbox_id, thread) DO UPDATE SET assignee_id = coalesce(mail_tickets.assignee_id, EXCLUDED.assignee_id),
		status = EXCLUDED.status, updated_by = EXCLUDED.updated_by, updated_at = now()`, mailboxID, t.Thread, who, status, userID)
	return err
}

// SharedPeople are the people who may open the shared mailbox (to assign its requests to).
func (s *Store) SharedPeople(ctx context.Context, userID, mailboxID string) ([]SharePerson, error) {
	if _, err := s.sharedCheck(ctx, userID, mailboxID, "read"); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, name FROM users WHERE active AND NOT guest ORDER BY name`)
	if err != nil {
		return nil, err
	}
	type person struct{ id, name string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (person, error) {
		var p person
		err := r.Scan(&p.id, &p.name)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	var out []SharePerson
	for _, p := range list {
		level, err := s.SharedLevel(ctx, p.id, mailboxID)
		if err != nil {
			return nil, err
		}
		if level != "" {
			out = append(out, SharePerson{UserID: p.id, Name: p.name, Level: level})
		}
	}
	return out, nil
}
