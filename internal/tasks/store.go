package tasks

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

// Sentinel errors are pointers, so errors.Is works; messages are locale keys.
var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
	ErrConflict  = i18n.E("err.conflict")
)

// ---------- admin mode ----------

type adminKey struct{}

// WithAdmin marks the request as made by an administrator in admin mode: every project and task becomes
// visible, and the administrator gets project-manager rights. It never grants the creator's or the
// responsible's own actions (approving a result stays with the creator).
// Private conversations (personal chats, private groups) must stay invisible even in admin mode:
// any future chat feature checks membership only and must not consult IsAdmin.
func WithAdmin(ctx context.Context) context.Context { return context.WithValue(ctx, adminKey{}, true) }

// WithoutAdmin is the context without admin mode: to ask what someone else may see.
func WithoutAdmin(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminKey{}, false)
}

// IsAdmin reports whether the request runs in admin mode.
func IsAdmin(ctx context.Context) bool { v, _ := ctx.Value(adminKey{}).(bool); return v }

// invalid is a user-facing input error (locale key + args).
func invalid(key string, args ...any) error { return i18n.E(key, args...) }

var projectKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)

type User struct {
	ID    string
	Name  string
	Email string
	Guest bool // from outside the company (a collab's guest)
}

type Project struct {
	ID          string
	Key         string
	Name        string
	Description string
	MyRole      string
	OpenTasks   int
	Visibility  string // members | participants
	Kind        string // group | project | scrum
	PhotoKey    string // the avatar in the storage ("" — none)
	DateStart   *time.Time
	DateFinish  *time.Time
	Archived    bool
	NoGuestTask bool // the viewer is a guest, and the collab does not let guests make tasks
	OwnerEdits  bool // a collab whose settings, name and avatar only its owner changes
	ScrumLead   bool // the viewer is the scrum project's product owner or scrum master
}

func (p Project) Scrum() bool { return p.Kind == "scrum" }

func (p Project) CanManage() bool {
	return p.MyRole == "owner" || p.MyRole == "manager" || p.MyRole == "admin"
}

// CanEdit: the viewer changes the project's name, description, avatar and a collab's settings — its owners and
// managers, of a collab by its «кто может изменять настройки».
func (p Project) CanEdit() bool {
	return p.CanManage() && (!p.OwnerEdits || p.MyRole != "manager")
}

// CanLead: the viewer runs the scrum — starts and completes sprints, sets the team and the Definition of Done:
// the project's moderators, its product owner and scrum master.
func (p Project) CanLead() bool { return !p.Archived && (p.CanManage() || p.ScrumLead) }

// CanWork: members create and plan tasks; an archived project takes no new work.
func (p Project) CanWork() bool { return !p.Archived && (p.CanManage() || p.MyRole == "member") }

// Frozen: a collab in the archive — read-only for everyone.
func (p Project) Frozen() bool { return p.Collab() && p.Archived }

// CanCreate: the viewer may put new tasks into the project (a guest — where the collab lets guests).
func (p Project) CanCreate() bool { return p.CanWork() && !p.NoGuestTask }

type Member struct {
	User
	Role string
}

type Task struct {
	ID              string
	ProjectID       string
	ProjectKey      string
	Number          int
	Title           string
	Description     string
	Status          Status
	Priority        Priority
	CreatorID       string
	CreatorName     string
	ResponsibleID   string
	ResponsibleName string
	ParentKey       string
	ParentTitle     string
	RequiresControl bool
	Deadline        *time.Time
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClosedAt        *time.Time
	EstimateMinutes *int
	StartPlan       *time.Time // planned start date (Gantt)
	EndPlan         *time.Time // planned end date (Gantt)
	StageID         string     // kanban stage of the project ("" = the first stage)
	// Deadline rules (the portal ALLOW_CHANGE_DEADLINE, MATCH_WORK_TIME)
	AllowChangeDeadline bool   // the responsible may move the deadline
	MatchWorkTime       bool   // dates skip weekends, holidays and off-hours
	NoEfficiency        bool   // «Не учитывать в эффективности»
	FlowID, FlowName    string // the flow the task was put into («Потоки»)
	Params
	// Scrum
	SprintID    string
	EpicID      string
	StoryPoints *float64
	// subtasks (direct children): how many, how many completed
	SubTotal, SubDone int
	Rating            string // «Оценка»: positive | negative | ""
	// marks: tags (everyone's), and the viewer's own favourite / pin / muted
	Tags                    []string
	Favorite, Pinned, Muted bool
	// Unread: new messages in the task chat for the viewer (unread «написал / упомянул» notifications), as the
	// green counter task lists; opening the task reads them
	Unread int
}

// Params are the task parameters (SE_PARAMETER) about subtasks and the result.
type Params struct {
	DatesFromSubtasks bool // «Сроки определяются сроками подзадач»: the deadline and plan dates follow the subtasks
	AutoComplete      bool // «Автоматически завершать задачу при завершении подзадач»
	ResultRequired    bool // «Не завершать задачу без результата»
}

// DatesFollow reports that the task's dates come from its subtasks now: nobody sets them by hand.
func (t Task) DatesFollow() bool { return t.DatesFromSubtasks && t.SubTotal > 0 }

func (t Task) Key() string { return fmt.Sprintf("%s-%d", t.ProjectKey, t.Number) }

func (t Task) Overdue() bool {
	return t.Deadline != nil && !t.Status.Closed() && t.Deadline.Before(time.Now())
}

type Comment struct {
	ID         string
	AuthorID   string
	AuthorName string
	Body       string
	CreatedAt  time.Time
	Reactions  []Reaction `db:"-"` // filled by FillReactions
}

type HistoryEntry struct {
	ActorName  string
	Field      string
	Old        string
	New        string
	CreatedAt  time.Time
	ActingName string // the change was made «ИО за» this person ("" = in one's own right)
}

// TaskCard is everything the task page needs.
type TaskCard struct {
	Task
	Roles       Roles
	MemberRole  string
	Accomplices []User
	Auditors    []User
	Checklist   []ChecklistItem // all items of all lists, in order
	Checklists  []Checklist
	Comments    []Comment
	History     []HistoryEntry
	Subtasks    []Task
	Actions     []Action
	TimeEntries []TimeEntry
	TimeTotal   int  // seconds
	CanDelete   bool // to the trash: the creator, the role's right («Права доступа»), or admin mode
	Collab      bool // the task is in a collab
	Frozen      bool // … in the archive: read-only
}

// CanComplete reports that the viewer may complete the task (or send it to control) now.
func (c TaskCard) CanComplete() bool { return slices.Contains(c.Actions, ActionComplete) }

// TimePercent: logged time against the estimate, capped at 100 for the progress bar.
func (c TaskCard) TimePercent() int {
	if c.EstimateMinutes == nil || *c.EstimateMinutes == 0 {
		return 0
	}
	return min(100, c.TimeTotal*100/(*c.EstimateMinutes*60))
}

// OverEstimate reports logged time beyond the estimate.
func (c TaskCard) OverEstimate() bool {
	return c.EstimateMinutes != nil && *c.EstimateMinutes > 0 && c.TimeTotal > *c.EstimateMinutes*60
}

// CanEdit: the creator and project managers edit task fields and participants.
func (c TaskCard) CanEdit() bool { return c.Roles.Creator || c.Roles.Manager }

// CanWork: participants who do the task and project managers edit the checklist.
func (c TaskCard) CanWork() bool { return c.Roles.Worker() || c.Roles.Manager }

func (c TaskCard) ChecklistDone() int {
	n := 0
	for _, i := range c.Checklist {
		if i.Done {
			n++
		}
	}
	return n
}

type Store struct {
	db *pgxpool.Pool
	// Comment posts a robot's comment to the task chat (the messenger first, as people's comments go);
	// unset, robots write only the go24 index (PostComment).
	Comment func(ctx context.Context, userID, taskID, body string) error
	// Kick, if set, is called after every change; it must not block (main runs the robots queue on it).
	Kick func()
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	run := fn
	if d := depthOf(ctx); d > 0 {
		run = func(tx pgx.Tx) error { // a robot's change queues the next robots one level deeper
			if _, err := tx.Exec(ctx, `SELECT set_config('go24.robot_depth', $1, true)`, strconv.Itoa(d)); err != nil {
				return err
			}
			return fn(tx)
		}
	}
	err := pgx.BeginFunc(ctx, s.db, run)
	if err == nil && s.Kick != nil {
		s.Kick() // the change may have queued robots: run them now, not on the next round of the worker
	}
	return err
}

// ---------- users ----------

// ListUsers are the active people to choose from; in a request of a person (WithViewer) only those they may know:
// a guest gets the people of their collabs, an employee no guest of a collab they are not in.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	if v := Viewer(ctx); v != "" && !IsAdmin(ctx) {
		rows, err := s.db.Query(ctx, `SELECT u.id, u.name, u.email, u.guest FROM users u WHERE u.active AND `+PeerSQL("$1", "u")+`
			ORDER BY u.name, u.email`, v)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByPos[User])
	}
	rows, err := s.db.Query(ctx, `SELECT id, name, email, guest FROM users WHERE active ORDER BY name, email`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[User])
}

func userName(ctx context.Context, q querier, id string) string {
	var name string
	if err := q.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, id).Scan(&name); err != nil {
		return id
	}
	return name
}

// ---------- projects ----------

func (s *Store) CreateProject(ctx context.Context, actorID, key, name, description string) (string, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	name = strings.TrimSpace(name)
	if !projectKeyRe.MatchString(key) {
		return "", invalid("err.project_key")
	}
	if name == "" {
		return "", invalid("err.project_name_required")
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx,
			`INSERT INTO projects (key, name, description, owner_id) VALUES ($1, $2, $3, $4) RETURNING id`,
			key, name, strings.TrimSpace(description), actorID,
		).Scan(&id)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return invalid("err.project_key_taken", key)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, id, actorID)
		return err
	})
	return key, err
}

// visibleSQL is the single rule for "user u may see task t" (t joined with its project p), used by every list,
// search and count; loadAccess applies the same rule to one task. u and admin are SQL placeholders.
// In a collab one must be its member as well: someone who has left it keeps no task of it.
func visibleSQL(u, admin string) string {
	return `(` + admin + ` OR ((p.kind <> 'collab' OR EXISTS (SELECT 1 FROM project_members cpm
		    WHERE cpm.project_id = t.project_id AND cpm.user_id = ` + u + `)) AND (
		   t.creator_id = ` + u + ` OR t.responsible_id = ` + u + `
		OR EXISTS (SELECT 1 FROM task_participants vtp WHERE vtp.task_id = t.id AND vtp.user_id = ` + u + `)
		OR (p.visibility = 'members' AND EXISTS (SELECT 1 FROM project_members vpm
		    WHERE vpm.project_id = t.project_id AND vpm.user_id = ` + u + `))
		OR (t.flow_id IS NOT NULL AND ` + flowPeopleSQL("t.flow_id", u) + `)
		OR ((SELECT task_perm(` + u + `::uuid, 'view')) IN ('sub', 'all') AND (t.creator_id IN ` + underSQL(u) + ` OR t.responsible_id IN ` + underSQL(u) + `
		    OR EXISTS (SELECT 1 FROM task_participants stp WHERE stp.task_id = t.id AND stp.user_id IN ` + underSQL(u) + `)))
		OR ((SELECT task_perm(` + u + `::uuid, 'edit')) IN ('sub', 'all') AND t.creator_id IN ` + underSQL(u) + `)
		OR (p.kind <> 'collab' AND (SELECT task_perm(` + u + `::uuid, 'view')) = 'all'))))`
}

// underSQL is the subquery of the people user u stands above «задачи подчинённых»: the employees of
// the departments u heads or is a deputy of, with all departments below, and the heads of the departments below —
// not the head a deputy stands in for. It does not depend on the task, so the planner runs it once per query.
func underSQL(u string) string {
	return `(WITH RECURSIVE down AS (SELECT id FROM departments WHERE head_id = ` + u + `
			UNION SELECT department_id FROM department_deputies WHERE user_id = ` + u + `
			UNION SELECT d.id FROM departments d JOIN down ON d.parent_id = down.id),
		over AS (SELECT d.head_id FROM departments d JOIN department_deputies dd ON dd.department_id = d.id
			WHERE dd.user_id = ` + u + ` AND d.head_id IS NOT NULL)
		SELECT ud.user_id FROM user_departments ud JOIN down ON down.id = ud.department_id
		WHERE ud.user_id <> ` + u + ` AND ud.user_id NOT IN (SELECT head_id FROM over)
		UNION SELECT d.head_id FROM departments d JOIN down ON down.id = d.parent_id WHERE d.head_id <> ` + u + `)`
}

// Subordinates are the active employees user u stands above (underSQL): «Задачи сотрудника» for each of them.
func (s *Store) Subordinates(ctx context.Context, userID string) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, email, guest FROM users WHERE active AND NOT guest AND id IN `+underSQL("$1")+`
		ORDER BY name, email`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[User])
}

// VisibleSQL is visibleSQL for the reports of other sections (BI): the tasks t of the projects p.
func VisibleSQL(u, admin string) string { return visibleSQL(u, admin) }

func (s *Store) ListProjects(ctx context.Context, userID string) ([]Project, error) {
	return s.listProjects(ctx, userID, false)
}

// ArchivedProjects are the user's projects in the archive («Архив» of the projects list).
func (s *Store) ArchivedProjects(ctx context.Context, userID string) ([]Project, error) {
	return s.listProjects(ctx, userID, true)
}

func (s *Store) listProjects(ctx context.Context, userID string, archived bool) ([]Project, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id, p.key, p.name, p.description, coalesce(pm.role, 'admin'),
		       (SELECT count(*) FROM tasks t WHERE t.project_id = p.id AND t.status <> 'completed' AND `+visibleSQL("$1", "$2")+`)::int,
		       p.visibility, p.kind, p.photo_key, p.date_start, p.date_finish, p.archived,
		       p.kind = 'collab' AND NOT p.guests_tasks AND `+GuestSQL("$1")+`, p.kind = 'collab' AND p.edit_perm = 'owner',
		       p.kind = 'scrum' AND pm.role IS NOT NULL AND coalesce($1 IN (p.scrum_master_id, coalesce(p.scrum_owner_id,
		           (SELECT o.user_id FROM project_members o WHERE o.project_id = p.id AND o.role = 'owner' LIMIT 1))), false)
		FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE p.archived = $3 AND (pm.user_id IS NOT NULL OR $2)
		ORDER BY p.name`, userID, IsAdmin(ctx), archived)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Project])
}

// ProjectByKey returns the project if the user is a member.
func (s *Store) ProjectByKey(ctx context.Context, userID, key string) (Project, error) {
	var p Project
	err := s.db.QueryRow(ctx, `
		SELECT p.id, p.key, p.name, p.description, coalesce(pm.role, ''),
		       (SELECT count(*) FROM tasks t WHERE t.project_id = p.id AND t.status <> 'completed' AND `+visibleSQL("$2", "$3")+`)::int,
		       p.visibility, p.kind, p.photo_key, p.date_start, p.date_finish, p.archived,
		       p.kind = 'collab' AND NOT p.guests_tasks AND `+GuestSQL("$2")+`, p.kind = 'collab' AND p.edit_perm = 'owner',
		       p.kind = 'scrum' AND pm.role IS NOT NULL AND coalesce($2 IN (p.scrum_master_id, coalesce(p.scrum_owner_id,
		           (SELECT o.user_id FROM project_members o WHERE o.project_id = p.id AND o.role = 'owner' LIMIT 1))), false)
		FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $2
		WHERE p.key = $1`, strings.ToUpper(key), userID, IsAdmin(ctx),
	).Scan(&p.ID, &p.Key, &p.Name, &p.Description, &p.MyRole, &p.OpenTasks, &p.Visibility, &p.Kind,
		&p.PhotoKey, &p.DateStart, &p.DateFinish, &p.Archived, &p.NoGuestTask, &p.OwnerEdits, &p.ScrumLead)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if p.MyRole == "" {
		if !IsAdmin(ctx) {
			if p.Kind == "collab" || IsGuestCtx(ctx) { // an outsider does not learn that a collab exists, a guest — any project
				return Project{}, ErrNotFound
			}
			return p, ErrForbidden
		}
		p.MyRole = "admin"
	}
	return p, nil
}

func (s *Store) Members(ctx context.Context, projectID string) ([]Member, error) {
	rows, err := s.db.Query(ctx, `
		SELECT u.id, u.name, u.email, pm.role, u.guest
		FROM project_members pm JOIN users u ON u.id = pm.user_id
		WHERE pm.project_id = $1
		ORDER BY CASE pm.role WHEN 'owner' THEN 0 WHEN 'manager' THEN 1 WHEN 'member' THEN 2 ELSE 3 END, u.name`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Member, error) {
		var m Member
		err := r.Scan(&m.ID, &m.Name, &m.Email, &m.Role, &m.Guest)
		return m, err
	})
}

// SetMember adds a user to the project or changes their role. The owner's role is fixed.
func (s *Store) SetMember(ctx context.Context, p Project, userID, role string) error {
	if p.Collab() { // a collab's «кто может приглашать»
		return s.setCollabMember(ctx, p, Viewer(ctx), userID, role)
	}
	if !p.CanManage() {
		return ErrForbidden
	}
	switch role {
	case "manager", "member", "viewer":
	default:
		return invalid("err.unknown_role")
	}
	if err := s.checkMemberRole(ctx, p, userID, role); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `
		INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role
		WHERE project_members.role <> 'owner'`, p.ID, userID, role)
	return err
}

func (s *Store) RemoveMember(ctx context.Context, p Project, userID string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if p.Collab() && p.MyRole == "manager" { // a moderator takes out members, not other moderators
			var role string
			if err := tx.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id::text = $2`, p.ID, userID).Scan(&role); err == nil && role != "member" {
				return ErrForbidden
			}
		}
		return removeMember(ctx, tx, p, userID, "member_removed")
	})
}

// ---------- tasks ----------

type NewTask struct {
	Title               string
	Description         string
	ResponsibleID       string
	Priority            Priority
	Deadline            *time.Time
	RequiresControl     bool
	ParentKey           string // optional, e.g. GO-12
	Accomplices         []string
	Auditors            []string
	StageID             string // kanban stage ("" = the first one)
	AllowChangeDeadline bool
	MatchWorkTime       bool
	SprintID            string   // scrum: a planned/active sprint ("" = the backlog)
	StoryPoints         *float64 // scrum estimate
	Checklist           []string // checklist items (empty lines are skipped), "# Name" starts another list
	TemplateID          string   // the template the task was made from
	NoEfficiency        bool     // «Не учитывать в эффективности»
	FlowID              string   // put into this flow (the flow has chosen the responsible)
	Params
}

// CreateTask creates a task in the project and returns its key (GO-13).
func (s *Store) CreateTask(ctx context.Context, actorID string, p Project, in NewTask) (string, error) {
	if !p.CanWork() {
		return "", ErrForbidden
	}
	if err := guestMay(ctx, s.db, p.ID, actorID, "tasks"); err != nil {
		return "", err
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return "", invalid("err.task_title_required")
	}
	if in.ResponsibleID == "" {
		in.ResponsibleID = actorID
	}
	if in.Priority < 0 || in.Priority > 2 {
		in.Priority = 1
	}

	if in.MatchWorkTime && in.Deadline != nil {
		sched, err := s.Schedule(ctx)
		if err != nil {
			return "", err
		}
		in.Deadline = sched.Adjust(in.Deadline)
	}
	var key string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if in.FlowID == "" { // a flow picks the responsible by its own rules
			if err := mayAssign(ctx, tx, actorID, in.ResponsibleID); err != nil {
				return err
			}
		}
		if err := collabStrangers(ctx, tx, p.ID, append(append([]string{in.ResponsibleID}, in.Accomplices...), in.Auditors...)...); err != nil {
			return err
		}
		var parentID *string
		if in.ParentKey != "" {
			pk, num, err := ParseKey(in.ParentKey)
			if err != nil {
				return invalid("err.parent_bad_key", in.ParentKey)
			}
			var id string
			err = tx.QueryRow(ctx, `SELECT t.id FROM tasks t JOIN projects p ON p.id = t.project_id
				WHERE p.key = $1 AND t.number = $2`, pk, num).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				return invalid("err.parent_not_found", in.ParentKey)
			}
			if err != nil {
				return err
			}
			parentID = &id
		}

		var num int
		if err := tx.QueryRow(ctx,
			`UPDATE projects SET task_seq = task_seq + 1, updated_at = now() WHERE id = $1 RETURNING task_seq`, p.ID,
		).Scan(&num); err != nil {
			return err
		}
		var taskID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO tasks (project_id, number, title, description, priority, creator_id, responsible_id,
			                   parent_id, requires_control, deadline, allow_change_deadline, match_work_time, no_efficiency, flow_id,
			                   dates_from_subtasks, auto_complete, result_required)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, nullif($14, '')::uuid, $15, $16, $17) RETURNING id`,
			p.ID, num, in.Title, strings.TrimSpace(in.Description), in.Priority, actorID, in.ResponsibleID,
			parentID, in.RequiresControl, in.Deadline, in.AllowChangeDeadline, in.MatchWorkTime, in.NoEfficiency, in.FlowID,
			in.DatesFromSubtasks, in.AutoComplete, in.ResultRequired,
		).Scan(&taskID); err != nil {
			return err
		}
		for role, ids := range map[string][]string{"accomplice": in.Accomplices, "auditor": in.Auditors} {
			for _, uid := range ids {
				if uid == "" {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO task_participants (task_id, user_id, role) VALUES ($1, $2, $3)
					ON CONFLICT DO NOTHING`, taskID, uid, role); err != nil {
					return err
				}
			}
		}
		if in.StageID != "" {
			st, err := s.stageByID(ctx, tx, p.ID, in.StageID)
			if err != nil {
				return err
			}
			if st.Finish() { // a new task is open; the finish stage is reached by completing it
				return invalid("err.stage_finish_new")
			}
			if err := putInStage(ctx, tx, taskID, st.ID); err != nil {
				return err
			}
			if p.Scrum() { // «+» on the sprint kanban: the task joins the active sprint
				if _, err := tx.Exec(ctx, `UPDATE tasks SET sprint_id = (SELECT id FROM sprints WHERE project_id = $2 AND status = 'active')
					WHERE id = $1`, taskID, p.ID); err != nil {
					return err
				}
			}
		}
		if in.SprintID != "" && p.Scrum() {
			sp, err := s.sprintOf(ctx, tx, p.ID, in.SprintID)
			if err != nil {
				return err
			}
			if sp.Status == "completed" {
				return invalid("err.sprint_completed")
			}
			if _, err := tx.Exec(ctx, `UPDATE tasks SET sprint_id = $2 WHERE id = $1`, taskID, sp.ID); err != nil {
				return err
			}
		}
		if in.StoryPoints != nil {
			if _, err := tx.Exec(ctx, `UPDATE tasks SET story_points = $2 WHERE id = $1`, taskID, in.StoryPoints); err != nil {
				return err
			}
		}
		if _, err := insertChecklistLines(ctx, tx, actorID, taskID, "", in.Checklist, true); err != nil {
			return err
		}
		if in.TemplateID != "" {
			if _, err := tx.Exec(ctx, `UPDATE tasks SET template_id = $2 WHERE id = $1`, taskID, in.TemplateID); err != nil {
				return err
			}
		}
		key = fmt.Sprintf("%s-%d", p.Key, num)
		return addHistory(ctx, tx, taskID, actorID, "created", "", in.Title)
	})
	return key, err
}

// ParseKey splits "GO-12" into ("GO", 12).
func ParseKey(key string) (string, int, error) {
	i := strings.LastIndex(key, "-")
	if i <= 0 {
		return "", 0, fmt.Errorf("bad task key %q", key)
	}
	n, err := strconv.Atoi(key[i+1:])
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("bad task key %q", key)
	}
	return strings.ToUpper(key[:i]), n, nil
}

const taskSelect = `
	SELECT t.id, t.project_id, p.key, t.number, t.title, t.description, t.status, t.priority,
	       t.creator_id, cu.name, t.responsible_id, ru.name,
	       coalesce(pp.key || '-' || pt.number, ''), coalesce(pt.title, ''),
	       t.requires_control, t.deadline, t.version, t.created_at, t.updated_at, t.closed_at, t.estimate_minutes, t.start_plan, t.end_plan,
	       coalesce(t.stage_id::text, ''), t.allow_change_deadline, t.match_work_time,
	       coalesce(t.sprint_id::text, ''), coalesce(t.epic_id::text, ''), t.story_points::float8,
	       (SELECT count(*) FROM tasks c WHERE c.parent_id = t.id),
	       (SELECT count(*) FROM tasks c WHERE c.parent_id = t.id AND c.status = 'completed'), t.rating, t.no_efficiency,
	       coalesce(t.flow_id::text, ''), coalesce((SELECT f.name FROM task_flows f WHERE f.id = t.flow_id), ''),
	       t.dates_from_subtasks, t.auto_complete, t.result_required
	FROM tasks t
	JOIN projects p ON p.id = t.project_id
	JOIN users cu ON cu.id = t.creator_id
	JOIN users ru ON ru.id = t.responsible_id
	LEFT JOIN tasks pt ON pt.id = t.parent_id
	LEFT JOIN projects pp ON pp.id = pt.project_id`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ProjectID, &t.ProjectKey, &t.Number, &t.Title, &t.Description, &t.Status, &t.Priority,
		&t.CreatorID, &t.CreatorName, &t.ResponsibleID, &t.ResponsibleName,
		&t.ParentKey, &t.ParentTitle,
		&t.RequiresControl, &t.Deadline, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.ClosedAt, &t.EstimateMinutes, &t.StartPlan, &t.EndPlan, &t.StageID,
		&t.AllowChangeDeadline, &t.MatchWorkTime, &t.SprintID, &t.EpicID, &t.StoryPoints, &t.SubTotal, &t.SubDone, &t.Rating, &t.NoEfficiency, &t.FlowID, &t.FlowName,
		&t.DatesFromSubtasks, &t.AutoComplete, &t.ResultRequired)
	return t, err
}

// ListLimit caps every task list; the lists show it by pages (Paginate).
const ListLimit = 5000

// Filter selects tasks for lists. Role-based filters ("my tasks") need no project membership.
type Filter struct {
	UserID    string
	ProjectID string
	Role      string // responsible | accomplice | creator | auditor | subordinates | "" (any)
	ParentID  string
	FlowID    string // the tasks of a flow
	OnlyOpen  bool
	// The sidebar counters (see Counters) link to exactly these selections:
	Doing     bool   // UserID is responsible or accomplice  (counter "open")
	Overdue   bool   // open and past the deadline            (counter "overdue", with Doing)
	CreatorID string // created by this user                  (counter "to_control", with Status)
	Status    Status
	// NewComments: only tasks with unread messages for UserID (the green counter «новые комментарии»)
	NewComments bool
	Favorites   bool   // only the UserID's favourites
	Tag         string // only tasks with this tag
	// The filter of the task list (the portal «Фильтр»):
	Search        string   // words in the title, or a task key
	Statuses      []Status // any of these statuses
	ResponsibleID string
	MemberID      string // a participant in any role
	Period        string // a column of «Сроки» (DeadlinePeriods)
	HighPriority  bool
	CRMLink       string // linked to this CRM card (its URL)
	// Viewer limits the list to tasks this user may see (visibleSQL). Admin mode lifts the limit.
	// Role filters ("my tasks") are visible by definition and need no Viewer.
	Viewer string
}

const listOrder = `(t.status = 'completed'), (t.deadline IS NULL), t.deadline, t.priority DESC, t.created_at DESC`

func (s *Store) ListTasks(ctx context.Context, f Filter) ([]Task, error) {
	return s.listOrdered(ctx, f, listOrder)
}

func (s *Store) listOrdered(ctx context.Context, f Filter, order string) ([]Task, error) {
	var where []string
	args := []any{}
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }

	if f.ProjectID != "" {
		where = append(where, "t.project_id = "+arg(f.ProjectID))
	}
	if f.ParentID != "" {
		where = append(where, "t.parent_id = "+arg(f.ParentID))
	}
	if f.FlowID != "" {
		where = append(where, "t.flow_id = "+arg(f.FlowID))
	}
	if f.OnlyOpen {
		where = append(where, "t.status <> 'completed'")
	}
	if f.Doing {
		u := arg(f.UserID)
		where = append(where, "(t.responsible_id = "+u+" OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = "+u+" AND tp.role = 'accomplice'))")
	}
	if f.Overdue {
		where = append(where, "t.status <> 'completed' AND t.deadline < now()")
	}
	if f.CreatorID != "" {
		where = append(where, "t.creator_id = "+arg(f.CreatorID))
	}
	if f.Status != "" {
		where = append(where, "t.status = "+arg(string(f.Status)))
	}
	if f.Viewer != "" && !IsAdmin(ctx) {
		where = append(where, visibleSQL(arg(f.Viewer), "false"))
	}
	if f.Favorites {
		where = append(where, "EXISTS (SELECT 1 FROM task_marks k WHERE k.task_id = t.id AND k.mark = 'favorite' AND k.user_id = "+arg(f.UserID)+")")
	}
	if f.Tag != "" {
		where = append(where, "EXISTS (SELECT 1 FROM task_tags g WHERE g.task_id = t.id AND lower(g.tag) = lower("+arg(f.Tag)+"))")
	}
	if f.Search != "" {
		q := arg("%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(f.Search)) + "%")
		where = append(where, "(t.title ILIKE "+q+" OR (p.key || '-' || t.number) ILIKE "+q+")")
	}
	if len(f.Statuses) > 0 {
		st := make([]string, len(f.Statuses))
		for i, x := range f.Statuses {
			st[i] = string(x)
		}
		where = append(where, "t.status = ANY("+arg(st)+")")
	}
	if f.ResponsibleID != "" {
		where = append(where, "t.responsible_id = "+arg(f.ResponsibleID))
	}
	if f.MemberID != "" {
		u := arg(f.MemberID)
		where = append(where, "(t.creator_id = "+u+" OR t.responsible_id = "+u+
			" OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = "+u+"))")
	}
	if f.Period != "" {
		from, to, err := periodRange(f.Period, time.Now())
		if err != nil {
			return nil, err
		}
		switch {
		case f.Period == "none":
			where = append(where, "t.deadline IS NULL")
		case from == nil: // overdue: open tasks only
			where = append(where, "t.status <> 'completed' AND t.deadline < "+arg(*to))
		case to == nil:
			where = append(where, "t.deadline >= "+arg(*from))
		default:
			where = append(where, "t.deadline >= "+arg(*from)+" AND t.deadline < "+arg(*to))
		}
	}
	if f.HighPriority {
		where = append(where, "t.priority = 2")
	}
	if f.CRMLink != "" {
		where = append(where, "EXISTS (SELECT 1 FROM task_crm_links cl WHERE cl.task_id = t.id AND cl.url = "+arg(f.CRMLink)+")")
	}
	if f.NewComments {
		where = append(where, "EXISTS (SELECT 1 FROM notifications n WHERE n.task_id = t.id AND n.user_id = "+arg(f.UserID)+
			" AND n.read_at IS NULL AND n.kind IN ('comment', 'mention'))")
	}
	if f.Role != "" {
		u := arg(f.UserID)
		switch f.Role {
		case "responsible":
			where = append(where, "t.responsible_id = "+u)
		case "creator": // «Поручил»: поставил другим
			where = append(where, "t.creator_id = "+u+" AND t.responsible_id <> "+u)
		case "accomplice", "auditor":
			where = append(where, "EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = "+u+
				" AND tp.role = "+arg(f.Role)+")")
		case "subordinates": // «Подчинённые»: a participant is under the user
			where = append(where, "(t.creator_id IN "+underSQL(u)+" OR t.responsible_id IN "+underSQL(u)+
				" OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id IN "+underSQL(u)+"))")
		default:
			return nil, invalid("err.unknown_filter_role", f.Role)
		}
	}
	sql := taskSelect
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += ` ORDER BY ` + order + ` LIMIT ` + strconv.Itoa(ListLimit)

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	viewer := f.Viewer
	if viewer == "" {
		viewer = f.UserID
	}
	if viewer == "" {
		viewer = f.CreatorID
	}
	if err := s.FillMarks(ctx, viewer, out); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Pinned && !out[j].Pinned }) // pinned first
	return out, s.FillUnread(ctx, viewer, out)
}

// FillUnread sets Task.Unread for the viewer: messages in the task chat they have not seen yet.
func (s *Store) FillUnread(ctx context.Context, viewer string, list []Task) error {
	if viewer == "" || len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	at := map[string]int{}
	for i, t := range list {
		ids[i] = t.ID
		at[t.ID] = i
	}
	rows, err := s.db.Query(ctx, `SELECT task_id::text, count(*) FROM notifications
		WHERE user_id = $1 AND read_at IS NULL AND kind IN ('comment', 'mention') AND task_id = ANY($2::uuid[])
		GROUP BY task_id`, viewer, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		list[at[id]].Unread = n
	}
	return rows.Err()
}

// access describes what a user may do with a task.
type access struct {
	TaskID          string
	ProjectID       string
	Status          Status
	RequiresControl bool
	CreatorID       string
	ResponsibleID   string
	Roles           Roles
	MemberRole      string
	Visibility      string // of the task's project
	Collab          bool   // the task is in a collab: to an outsider it does not exist
	Frozen          bool   // the collab is in the archive: its tasks are read-only
	ActingFor       string // the person this user acts for here («исполняющий обязанности»), "" = own access
	FlowPerson      bool   // the task is in a flow of this user's (administrator, moderator, team)
	Supervisor      bool   // a participant of the task is under this user (see underSQL)
	ViewAll         bool   // the user's role sees all tasks («Права доступа»)
	CanDelete       bool   // to the trash: the creator, the role's right, admin mode
}

// visible mirrors visibleSQL for one task.
func (a access) visible() bool {
	if a.Collab && a.MemberRole == "" {
		return false
	}
	r := a.Roles
	return (a.MemberRole != "" && a.Visibility == "members") || r.Creator || r.Responsible || r.Accomplice || r.Auditor || a.FlowPerson || a.Supervisor || a.ViewAll
}

func loadAccess(ctx context.Context, q querier, userID, taskID string, forUpdate bool) (access, error) {
	a := access{TaskID: taskID}
	sql := `SELECT t.project_id, t.status, t.requires_control, t.creator_id, t.responsible_id, p.visibility, p.kind = 'collab',
		p.kind = 'collab' AND p.archived, t.flow_id IS NOT NULL AND ` + flowPeopleSQL("t.flow_id", "$2") + `
		FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`
	if forUpdate {
		sql += " FOR UPDATE OF t"
	}
	err := q.QueryRow(ctx, sql, taskID, userID).Scan(&a.ProjectID, &a.Status, &a.RequiresControl, &a.CreatorID, &a.ResponsibleID, &a.Visibility, &a.Collab, &a.Frozen, &a.FlowPerson)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if err != nil {
		return a, err
	}
	if err := a.rolesOf(ctx, q, userID); err != nil {
		return a, err
	}
	// the head of the creator has the creator's powers, the head of another participant sees the task
	var headOfCreator bool
	err = q.QueryRow(ctx, `SELECT t.creator_id IN `+underSQL("$2")+`,
		t.responsible_id IN `+underSQL("$2")+` OR EXISTS (SELECT 1 FROM task_participants stp WHERE stp.task_id = t.id AND stp.user_id IN `+underSQL("$2")+`)
		FROM tasks t WHERE t.id = $1`, taskID, userID).Scan(&headOfCreator, &a.Supervisor)
	if err != nil {
		return a, err
	}
	// «исполняющий обязанности»: while someone is away, the person acting for them gets the powers they were given
	rows, err := q.Query(ctx, `SELECT user_id::text, acting_rights FROM absences
		WHERE acting_id = $1 AND current_date BETWEEN starts_on AND ends_on`, userID)
	if err != nil {
		return a, err
	}
	type acting struct {
		For    string
		Rights []string
	}
	actings, err := pgx.CollectRows(rows, pgx.RowToStructByPos[acting])
	if err != nil {
		return a, err
	}
	for _, ac := range actings {
		x := access{TaskID: taskID, ProjectID: a.ProjectID, CreatorID: a.CreatorID, ResponsibleID: a.ResponsibleID, Visibility: a.Visibility, Collab: a.Collab}
		if err := x.rolesOf(ctx, q, ac.For); err != nil {
			return a, err
		}
		if !x.visible() {
			continue
		}
		for _, r := range ac.Rights {
			switch r {
			case "view":
				a.ActingFor = ac.For
			case "work":
				a.Roles.Responsible = a.Roles.Responsible || x.Roles.Responsible
				a.Roles.Accomplice = a.Roles.Accomplice || x.Roles.Accomplice
			case "control":
				a.Roles.Creator = a.Roles.Creator || x.Roles.Creator
			case "manage":
				a.Roles.Manager = a.Roles.Manager || x.Roles.Manager
			}
		}
		if a.Roles != (Roles{}) && a.ActingFor == "" {
			a.ActingFor = ac.For
		}
	}
	// the rights of the person's roles: the tasks of subordinates, all tasks (collabs keep their own roles)
	perms, err := permsOf(ctx, q, userID)
	if err != nil {
		return a, err
	}
	a.CanDelete = a.Roles.Creator || (perms.Has("delete", "sub") && headOfCreator) || (perms.Has("delete", "all") && !a.Collab) || IsAdmin(ctx)
	a.Roles.Creator = a.Roles.Creator || (perms.Has("edit", "sub") && headOfCreator)
	a.Roles.Manager = a.Roles.Manager || (perms.Has("edit", "all") && !a.Collab)
	a.Supervisor = (a.Supervisor || headOfCreator) && perms.Has("view", "sub")
	a.ViewAll = perms.Has("view", "all") && !a.Collab
	if !a.visible() && a.ActingFor == "" && !IsAdmin(ctx) {
		if a.Collab || IsGuestCtx(ctx) { // a guest does not learn that a task of the company exists
			return a, ErrNotFound
		}
		return a, ErrForbidden
	}
	if a.Frozen { // the archive of a collab: one reads its tasks, nobody changes them
		a.Roles, a.ActingFor, a.CanDelete = Roles{Auditor: true}, "", false
	}
	return a, nil
}

// rolesOf fills the person's own roles in the task (the task's fields are loaded).
func (a *access) rolesOf(ctx context.Context, q querier, userID string) error {
	a.Roles = Roles{Creator: a.CreatorID == userID, Responsible: a.ResponsibleID == userID}
	rows, err := q.Query(ctx, `SELECT role FROM task_participants WHERE task_id = $1 AND user_id = $2`, a.TaskID, userID)
	if err != nil {
		return err
	}
	roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, r := range roles {
		a.Roles.Accomplice = a.Roles.Accomplice || r == "accomplice"
		a.Roles.Auditor = a.Roles.Auditor || r == "auditor"
	}
	a.MemberRole = ""
	err = q.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`,
		a.ProjectID, userID).Scan(&a.MemberRole)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// In a "participants" project the project role manages the project, not other people's tasks.
	a.Roles.Manager = ((a.MemberRole == "owner" || a.MemberRole == "manager") && a.Visibility == "members") || IsAdmin(ctx)
	a.Roles.Accomplice = a.Roles.Accomplice || isRobot(ctx) // robots and triggers work the task as a participant would
	return nil
}

// TaskIDByKey resolves "GO-12" to the task id.
func (s *Store) TaskIDByKey(ctx context.Context, key string) (string, error) {
	pk, num, err := ParseKey(key)
	if err != nil {
		return "", ErrNotFound
	}
	var id string
	err = s.db.QueryRow(ctx, `SELECT t.id FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE p.key = $1 AND t.number = $2`, pk, num).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

// RowCard is the head of a card: the task, the viewer's roles and what they may do with it — the menu of a row in
// task lists.
func (s *Store) RowCard(ctx context.Context, userID, taskID string) (TaskCard, error) {
	var c TaskCard
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return c, err
	}
	if c.Task, err = scanTask(s.db.QueryRow(ctx, taskSelect+" WHERE t.id = $1", taskID)); err != nil {
		return c, err
	}
	c.Roles, c.MemberRole = a.Roles, a.MemberRole
	c.CanDelete = a.CanDelete
	c.Collab, c.Frozen = a.Collab, a.Frozen
	c.Actions = AllowedActions(c.Status, c.Roles, c.RequiresControl)
	return c, nil
}

func (s *Store) GetCard(ctx context.Context, userID, taskID string) (TaskCard, error) {
	c, err := s.RowCard(ctx, userID, taskID)
	if err != nil {
		return c, err
	}

	rows, err := s.db.Query(ctx, `SELECT u.id, u.name, u.email, tp.role, u.guest FROM task_participants tp
		JOIN users u ON u.id = tp.user_id WHERE tp.task_id = $1 ORDER BY u.name`, taskID)
	if err != nil {
		return c, err
	}
	members, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Member, error) {
		var m Member
		err := r.Scan(&m.ID, &m.Name, &m.Email, &m.Role, &m.Guest)
		return m, err
	})
	if err != nil {
		return c, err
	}
	for _, m := range members {
		if m.Role == "accomplice" {
			c.Accomplices = append(c.Accomplices, m.User)
		} else {
			c.Auditors = append(c.Auditors, m.User)
		}
	}

	if c.Checklists, err = loadChecklists(ctx, s.db, taskID); err != nil {
		return c, err
	}
	for _, l := range c.Checklists {
		c.Checklist = append(c.Checklist, l.Items...)
	}

	rows, err = s.db.Query(ctx, `SELECT c.id, c.author_id, u.name, c.body, c.created_at FROM task_comments c
		JOIN users u ON u.id = c.author_id WHERE c.task_id = $1 ORDER BY c.created_at`, taskID)
	if err != nil {
		return c, err
	}
	if c.Comments, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Comment]); err != nil {
		return c, err
	}
	if err := s.FillReactions(ctx, userID, c.Comments); err != nil {
		return c, err
	}

	rows, err = s.db.Query(ctx, `SELECT u.name, h.field, h.old_value, h.new_value, h.created_at, coalesce(af.name, '') FROM task_history h
		JOIN users u ON u.id = h.actor_id LEFT JOIN users af ON af.id = h.acting_for WHERE h.task_id = $1 ORDER BY h.id DESC LIMIT 100`, taskID)
	if err != nil {
		return c, err
	}
	if c.History, err = pgx.CollectRows(rows, pgx.RowToStructByPos[HistoryEntry]); err != nil {
		return c, err
	}

	if c.Subtasks, err = s.ListTasks(ctx, Filter{ParentID: taskID, Viewer: userID}); err != nil {
		return c, err
	}
	c.TimeEntries, c.TimeTotal, err = s.TaskTime(ctx, taskID)
	return c, err
}

// ApplyAction changes the task status according to the portal-style workflow.
func (s *Store) ApplyAction(ctx context.Context, userID, taskID string, act Action) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		next, err := Transition(a.Status, act, a.Roles, a.RequiresControl)
		if err != nil {
			return err
		}
		if err := setStatus(ctx, tx, taskID, userID, a.Status, next); err != nil {
			return err
		}
		return syncStage(ctx, tx, a.ProjectID, taskID, next)
	})
}

// setStatus writes a status change that Transition has allowed.
func setStatus(ctx context.Context, tx pgx.Tx, taskID, userID string, old, next Status) error {
	if (next == StatusCompleted || next == StatusAwaitingControl) && old != StatusAwaitingControl {
		ok, err := hasResultIfRequired(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("err.result_required")
		}
		if ok, err = dodDone(ctx, tx, taskID); err != nil {
			return err
		}
		if !ok {
			return invalid("err.dod_required")
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE tasks SET status = $2, version = version + 1, updated_at = now(),
		       closed_at = CASE WHEN $2 = 'completed' THEN now() END,
		       closed_by = CASE WHEN $2 = 'completed' THEN $3::uuid END
		WHERE id = $1`, taskID, string(next), userID); err != nil {
		return err
	}
	if err := addHistory(ctx, tx, taskID, userID, "status", string(old), string(next)); err != nil {
		return err
	}
	return parentFollows(ctx, tx, taskID, userID, next)
}

// hasResultIfRequired: «Не завершать задачу без результата» — such a task needs a result before it is completed.
func hasResultIfRequired(ctx context.Context, q querier, taskID string) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT NOT t.result_required OR EXISTS (SELECT 1 FROM task_results r WHERE r.task_id = t.id)
		FROM tasks t WHERE t.id = $1`, taskID).Scan(&ok)
	return ok, err
}

// parentFollows: better than the portal, where a parent task's status never follows its subtasks — a subtask taken
// into work starts its waiting parent (and so on up); when the last subtask is done, the parent's creator is
// asked whether to complete it (the parent may hold work of its own, so it is not closed by itself) — unless the
// parent is «Автоматически завершать задачу при завершении подзадач»: then it completes (or goes to
// control, when its creator is someone else) as done by the one who finished the last subtask, and so on up.
func parentFollows(ctx context.Context, tx pgx.Tx, taskID, userID string, next Status) error {
	var parent, projectID, creator string
	var status Status
	var auto, control bool
	err := tx.QueryRow(ctx, `SELECT p.id::text, p.project_id::text, p.creator_id::text, p.status, p.auto_complete, p.requires_control
		FROM tasks t JOIN tasks p ON p.id = t.parent_id
		WHERE t.id = $1`, taskID).Scan(&parent, &projectID, &creator, &status, &auto, &control)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	switch {
	case next == StatusInProgress && status == StatusPending:
		if err := setStatus(ctx, tx, parent, userID, status, StatusInProgress); err != nil {
			return err
		}
		return syncStage(ctx, tx, projectID, parent, StatusInProgress)
	case next == StatusCompleted && status != StatusCompleted && (creator != userID || auto):
		var open bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tasks WHERE parent_id = $1 AND status <> 'completed')`, parent).Scan(&open); err != nil || open {
			return err
		}
		if auto && status != StatusAwaitingControl {
			ok, err := hasResultIfRequired(ctx, tx, parent)
			if err == nil && ok {
				ok, err = dodDone(ctx, tx, parent)
			}
			if err != nil {
				return err
			}
			if ok { // without the result (or the ticked DoD) it waits: the creator is asked as usual
				done := StatusCompleted
				if control && creator != userID {
					done = StatusAwaitingControl
				}
				if err := setStatus(ctx, tx, parent, userID, status, done); err != nil {
					return err
				}
				return syncStage(ctx, tx, projectID, parent, done)
			}
		}
		if creator == userID {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, task_id) VALUES ($1, $2, 'subtasks_done', $3)`, creator, userID, parent)
		return err
	}
	return nil
}

type TaskUpdate struct {
	Version             int
	Title               string
	Description         string
	ResponsibleID       string
	Priority            Priority
	Deadline            *time.Time
	RequiresControl     bool
	EstimateMinutes     *int
	StartPlan           *time.Time
	EndPlan             *time.Time
	AllowChangeDeadline bool
	MatchWorkTime       bool
	Params
}

// UpdateTask edits task fields (creator or project manager), with optimistic locking by version.
func (s *Store) UpdateTask(ctx context.Context, userID, taskID string, u TaskUpdate) error {
	u.Title = strings.TrimSpace(u.Title)
	if u.Title == "" {
		return invalid("err.task_title_required")
	}
	if u.MatchWorkTime {
		sched, err := s.Schedule(ctx)
		if err != nil {
			return err
		}
		u.Deadline = sched.Adjust(u.Deadline)
		u.StartPlan, u.EndPlan = sched.AdjustDay(u.StartPlan), sched.AdjustDay(u.EndPlan)
	}
	if u.StartPlan != nil && u.EndPlan != nil && u.EndPlan.Before(*u.StartPlan) {
		return invalid("err.plan_dates")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Creator || a.Roles.Manager) {
			return ErrForbidden
		}
		var cur TaskUpdate
		var subtasks bool
		if err := tx.QueryRow(ctx, `SELECT version, title, description, responsible_id, priority, deadline, requires_control, estimate_minutes,
			start_plan, end_plan, allow_change_deadline, match_work_time, dates_from_subtasks, auto_complete, result_required,
			EXISTS (SELECT 1 FROM tasks c WHERE c.parent_id = t.id) FROM tasks t WHERE id = $1`, taskID).Scan(&cur.Version, &cur.Title, &cur.Description, &cur.ResponsibleID,
			&cur.Priority, &cur.Deadline, &cur.RequiresControl, &cur.EstimateMinutes, &cur.StartPlan, &cur.EndPlan,
			&cur.AllowChangeDeadline, &cur.MatchWorkTime, &cur.DatesFromSubtasks, &cur.AutoComplete, &cur.ResultRequired, &subtasks); err != nil {
			return err
		}
		if cur.Version != u.Version {
			return ErrConflict
		}
		if u.DatesFromSubtasks && subtasks { // the dates follow the subtasks (the trigger sets them when the box is ticked)
			u.Deadline, u.StartPlan, u.EndPlan = cur.Deadline, cur.StartPlan, cur.EndPlan
		}
		if u.ResponsibleID == "" {
			u.ResponsibleID = cur.ResponsibleID
		}
		if u.ResponsibleID != cur.ResponsibleID {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active)`, u.ResponsibleID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return invalid("err.pick_person")
			}
			if err := mayAssign(ctx, tx, userID, u.ResponsibleID); err != nil {
				return err
			}
			if err := collabStrangers(ctx, tx, a.ProjectID, u.ResponsibleID); err != nil {
				return err
			}
		}

		type change struct{ field, old, new string }
		var changes []change
		if cur.Title != u.Title {
			changes = append(changes, change{"title", cur.Title, u.Title})
		}
		if cur.Description != strings.TrimSpace(u.Description) {
			changes = append(changes, change{"description", "", ""})
		}
		if cur.ResponsibleID != u.ResponsibleID {
			changes = append(changes, change{"responsible", userName(ctx, tx, cur.ResponsibleID), userName(ctx, tx, u.ResponsibleID)})
		}
		if cur.Priority != u.Priority {
			changes = append(changes, change{"priority", strconv.Itoa(int(cur.Priority)), strconv.Itoa(int(u.Priority))})
		}
		if fmtTime(cur.Deadline) != fmtTime(u.Deadline) {
			changes = append(changes, change{"deadline", fmtTime(cur.Deadline), fmtTime(u.Deadline)})
		}
		if cur.RequiresControl != u.RequiresControl {
			changes = append(changes, change{"requires_control", strconv.FormatBool(cur.RequiresControl), strconv.FormatBool(u.RequiresControl)})
		}
		if minutesText(cur.EstimateMinutes) != minutesText(u.EstimateMinutes) {
			changes = append(changes, change{"estimate", minutesText(cur.EstimateMinutes), minutesText(u.EstimateMinutes)})
		}
		if dateText(cur.StartPlan) != dateText(u.StartPlan) {
			changes = append(changes, change{"start_plan", dateText(cur.StartPlan), dateText(u.StartPlan)})
		}
		if dateText(cur.EndPlan) != dateText(u.EndPlan) {
			changes = append(changes, change{"end_plan", dateText(cur.EndPlan), dateText(u.EndPlan)})
		}
		if cur.AllowChangeDeadline != u.AllowChangeDeadline {
			changes = append(changes, change{"allow_change_deadline", strconv.FormatBool(cur.AllowChangeDeadline), strconv.FormatBool(u.AllowChangeDeadline)})
		}
		if cur.MatchWorkTime != u.MatchWorkTime {
			changes = append(changes, change{"match_work_time", strconv.FormatBool(cur.MatchWorkTime), strconv.FormatBool(u.MatchWorkTime)})
		}
		for _, p := range []struct {
			field    string
			old, new bool
		}{{"dates_from_subtasks", cur.DatesFromSubtasks, u.DatesFromSubtasks}, {"auto_complete", cur.AutoComplete, u.AutoComplete},
			{"result_required", cur.ResultRequired, u.ResultRequired}} {
			if p.old != p.new {
				changes = append(changes, change{p.field, strconv.FormatBool(p.old), strconv.FormatBool(p.new)})
			}
		}
		if len(changes) == 0 {
			return nil
		}

		if _, err := tx.Exec(ctx, `UPDATE tasks SET title = $2, description = $3, responsible_id = $4, priority = $5,
			deadline = $6, requires_control = $7, estimate_minutes = $8, start_plan = $9, end_plan = $10, -- date columns: pgx takes the calendar date of the time value
			allow_change_deadline = $11, match_work_time = $12, dates_from_subtasks = $13, auto_complete = $14, result_required = $15,
			version = version + 1, updated_at = now() WHERE id = $1`,
			taskID, u.Title, strings.TrimSpace(u.Description), u.ResponsibleID, u.Priority, u.Deadline, u.RequiresControl, u.EstimateMinutes,
			u.StartPlan, u.EndPlan, u.AllowChangeDeadline, u.MatchWorkTime, u.DatesFromSubtasks, u.AutoComplete, u.ResultRequired,
		); err != nil {
			return err
		}
		planMoved := false
		for _, c := range changes {
			if err := addHistory(ctx, tx, taskID, userID, c.field, c.old, c.new); err != nil {
				return err
			}
			planMoved = planMoved || c.field == "start_plan" || c.field == "end_plan"
		}
		if planMoved {
			_, err := s.shiftSuccessors(ctx, tx, userID, taskID)
			return err
		}
		return nil
	})
}

// SetParticipant adds or removes an accomplice/auditor (creator, responsible or project manager).
func (s *Store) SetParticipant(ctx context.Context, userID, taskID, targetID, role string, add bool) error {
	if role != "accomplice" && role != "auditor" {
		return invalid("err.unknown_role")
	}
	if targetID == "" {
		return invalid("err.pick_person")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := canSetPeople(ctx, tx, userID, taskID); err != nil {
			return err
		}
		if add { // no one picked (the «+» with an empty field) or no such person
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active)`, targetID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return invalid("err.pick_person")
			}
		}
		return setParticipant(ctx, tx, userID, taskID, targetID, role, add)
	})
}

// SetParticipants makes the task's accomplices and auditors exactly these people (the task form saves the
// whole lists); everyone added or removed is written to the history, newcomers are notified.
func (s *Store) SetParticipants(ctx context.Context, userID, taskID string, accomplices, auditors []string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if err := canSetPeople(ctx, tx, userID, taskID); err != nil {
			return err
		}
		for role, want := range map[string][]string{"accomplice": accomplices, "auditor": auditors} {
			rows, err := tx.Query(ctx, `SELECT user_id::text FROM task_participants WHERE task_id = $1 AND role = $2`, taskID, role)
			if err != nil {
				return err
			}
			cur, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			keep := map[string]bool{}
			for _, id := range want {
				if id != "" {
					keep[id] = true
				}
			}
			have := map[string]bool{}
			for _, id := range cur {
				have[id] = true
				if !keep[id] {
					if err := setParticipant(ctx, tx, userID, taskID, id, role, false); err != nil {
						return err
					}
				}
			}
			for _, id := range want {
				if id != "" && !have[id] {
					have[id] = true
					if err := setParticipant(ctx, tx, userID, taskID, id, role, true); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func canSetPeople(ctx context.Context, tx pgx.Tx, userID, taskID string) error {
	a, err := loadAccess(ctx, tx, userID, taskID, true)
	if err != nil {
		return err
	}
	if !(a.Roles.Creator || a.Roles.Responsible || a.Roles.Manager) {
		return ErrForbidden
	}
	return nil
}

func setParticipant(ctx context.Context, tx pgx.Tx, userID, taskID, targetID, role string, add bool) error {
	var tag pgconn.CommandTag
	var err error
	if add {
		var projectID string
		if err := tx.QueryRow(ctx, `SELECT project_id FROM tasks WHERE id = $1`, taskID).Scan(&projectID); err != nil {
			return err
		}
		if err := collabStrangers(ctx, tx, projectID, targetID); err != nil {
			return err
		}
		tag, err = tx.Exec(ctx, `INSERT INTO task_participants (task_id, user_id, role) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, taskID, targetID, role)
	} else {
		tag, err = tx.Exec(ctx, `DELETE FROM task_participants WHERE task_id = $1 AND user_id = $2 AND role = $3`,
			taskID, targetID, role)
	}
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	name := userName(ctx, tx, targetID)
	if add {
		if targetID != userID { // «вас добавили соисполнителем/наблюдателем»
			if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, task_id, body)
				SELECT $1, $2, 'participant', $3, $4 WHERE NOT go24_quiet()`, targetID, userID, taskID, role); err != nil {
				return err
			}
		}
		return addHistory(ctx, tx, taskID, userID, role, "", name)
	}
	return addHistory(ctx, tx, taskID, userID, role, name, "")
}

// ---------- comments ----------

func (s *Store) AddComment(ctx context.Context, userID, taskID, body string) error {
	_, err := s.PostComment(ctx, userID, taskID, body)
	return err
}

// PostComment adds a message to the task chat and returns it. People mentioned as "@Name" (or passed in
// mentions, e.g. from the messenger) who are not in the task become its auditors, and get
// a «mention» notification instead of the usual one.
func (s *Store) PostComment(ctx context.Context, userID, taskID, body string, mentions ...string) (Comment, error) {
	var c Comment
	body = strings.TrimSpace(body)
	if body == "" {
		return c, invalid("err.comment_empty")
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := loadAccess(ctx, tx, userID, taskID, false); err != nil {
			return err
		}
		found, err := mentioned(ctx, tx, body)
		if err != nil {
			return err
		}
		ids := uniq(userID, mentionIDs(found), mentions)
		if len(ids) > 0 {
			// newcomers watch the task (only active employees count)
			rows, err := tx.Query(ctx, `INSERT INTO task_participants (task_id, user_id, role)
				SELECT $1, u.id, 'auditor' FROM users u
				WHERE u.id = ANY($2::uuid[]) AND u.active
				  AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = $1 AND u.id IN (t.creator_id, t.responsible_id))
				  AND NOT EXISTS (SELECT 1 FROM task_participants p WHERE p.task_id = $1 AND p.user_id = u.id)
				RETURNING (SELECT name FROM users WHERE id = user_id)`, taskID, ids)
			if err != nil {
				return err
			}
			names, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				return err
			}
			for _, n := range names {
				if err := addHistory(ctx, tx, taskID, userID, "auditor", "", n); err != nil {
					return err
				}
			}
		}
		if err := tx.QueryRow(ctx, `WITH n AS (INSERT INTO task_comments (task_id, author_id, body) VALUES ($1, $2, $3)
				RETURNING id, author_id, body, created_at)
			SELECT n.id, n.author_id, u.name, n.body, n.created_at FROM n JOIN users u ON u.id = n.author_id`, taskID, userID, body).
			Scan(&c.ID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		// the trigger has just notified the task members of the message (now() is this transaction's time);
		// whoever turned message notices off still gets the mention
		if _, err := tx.Exec(ctx, `UPDATE notifications SET kind = 'mention'
			WHERE task_id = $1 AND kind = 'comment' AND created_at = now() AND user_id = ANY($2::uuid[])`, taskID, ids); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, task_id, body)
			SELECT u.id, $3, 'mention', $1, left($4, 300) FROM users u
			WHERE u.id = ANY($2::uuid[]) AND u.active AND NOT go24_quiet()
			  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.user_id = u.id AND n.task_id = $1 AND n.created_at = now())`,
			taskID, ids, userID, body)
		return err
	})
	return c, err
}

// Event is a task event for the feed: a task was created or completed.
type Event struct {
	Kind      string // created | completed
	TaskKey   string
	TaskTitle string
	ActorID   string
	ActorName string
	At        time.Time
}

// Events returns the newest task events the user may see (the same visibility rule as task lists).
func (s *Store) Events(ctx context.Context, userID string, before time.Time, limit int) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT CASE WHEN h.field = 'created' THEN 'created' ELSE 'completed' END,
			p.key || '-' || t.number, t.title, h.actor_id, u.name, h.created_at
		FROM task_history h JOIN tasks t ON t.id = h.task_id JOIN projects p ON p.id = t.project_id JOIN users u ON u.id = h.actor_id
		WHERE (h.field = 'created' OR (h.field = 'status' AND h.new_value = 'completed')) AND h.created_at < $2
		  AND `+visibleSQL("$1", "$3")+`
		ORDER BY h.created_at DESC LIMIT $4`, userID, before, IsAdmin(ctx), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Event])
}

// CommentByID loads one message of a task chat (the caller checks that the viewer may see the task).
func (s *Store) CommentByID(ctx context.Context, taskID, id string) (Comment, error) {
	var c Comment
	err := s.db.QueryRow(ctx, `SELECT c.id, c.author_id, u.name, c.body, c.created_at FROM task_comments c
		JOIN users u ON u.id = c.author_id WHERE c.id = $1 AND c.task_id = $2`, id, taskID).
		Scan(&c.ID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// ---------- helpers ----------

func addHistory(ctx context.Context, q querier, taskID, actorID, field, oldV, newV string) error {
	_, err := q.Exec(ctx, `INSERT INTO task_history (task_id, actor_id, field, old_value, new_value)
		VALUES ($1, $2, $3, $4, $5)`, taskID, actorID, field, oldV, newV)
	return err
}

// dateText stores a plan date in history as YYYY-MM-DD ("" = none).
func dateText(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// minutesText stores an estimate in history as plain minutes ("" = none); the UI formats it.
func minutesText(m *int) string {
	if m == nil {
		return ""
	}
	return strconv.Itoa(*m)
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339) // locale-neutral; the UI formats it per language
}

// ---------- kanban ----------

// Moves returns, for each task id, the action that moves the task into a target status for this user.
func (s *Store) Moves(ctx context.Context, userID string, list []Task) (map[string]map[Status]Action, error) {
	roles, err := s.RolesFor(ctx, userID, list)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[Status]Action, len(list))
	for _, t := range list {
		r := roles[t.ID]
		m := map[Status]Action{}
		for _, a := range AllowedActions(t.Status, r, t.RequiresControl) {
			next, _ := Transition(t.Status, a, r, t.RequiresControl)
			if _, taken := m[next]; !taken {
				m[next] = a
			}
		}
		out[t.ID] = m
	}
	return out, nil
}

// RolesFor computes the user's roles in many tasks at once (boards); loadAccess does it for one task.
func (s *Store) RolesFor(ctx context.Context, userID string, list []Task) (map[string]Roles, error) {
	out := make(map[string]Roles, len(list))
	if len(list) == 0 {
		return out, nil
	}
	taskIDs := make([]string, 0, len(list))
	projectIDs := make([]string, 0, 1)
	for _, t := range list {
		taskIDs = append(taskIDs, t.ID)
		projectIDs = append(projectIDs, t.ProjectID)
	}

	partRoles := map[string][]string{}
	rows, err := s.db.Query(ctx, `SELECT task_id::text, role FROM task_participants
		WHERE user_id = $1 AND task_id = ANY($2::uuid[])`, userID, taskIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var tid, role string
		if err := rows.Scan(&tid, &role); err != nil {
			rows.Close()
			return nil, err
		}
		partRoles[tid] = append(partRoles[tid], role)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	memberRoles, frozen, collab := map[string]string{}, map[string]bool{}, map[string]bool{}
	rows, err = s.db.Query(ctx, `SELECT p.id::text, coalesce(pm.role, ''), p.visibility, p.kind = 'collab' AND p.archived, p.kind = 'collab' FROM projects p
		LEFT JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE p.id = ANY($2::uuid[])`, userID, projectIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pid, role, vis string
		var fr, co bool
		if err := rows.Scan(&pid, &role, &vis, &fr, &co); err != nil {
			rows.Close()
			return nil, err
		}
		if vis != "members" { // see loadAccess: no task-manager rights from the project role
			role = ""
		}
		memberRoles[pid], frozen[pid], collab[pid] = role, fr, co
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	perms, err := permsOf(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	for _, t := range list {
		r := Roles{Creator: t.CreatorID == userID, Responsible: t.ResponsibleID == userID}
		for _, role := range partRoles[t.ID] {
			r.Accomplice = r.Accomplice || role == "accomplice"
			r.Auditor = r.Auditor || role == "auditor"
		}
		mr := memberRoles[t.ProjectID]
		r.Manager = mr == "owner" || mr == "manager" || IsAdmin(ctx) || (perms.Has("edit", "all") && !collab[t.ProjectID])
		if frozen[t.ProjectID] { // as loadAccess: the archive of a collab is read-only
			r = Roles{Auditor: true}
		}
		out[t.ID] = r
	}
	return out, nil
}

// AllStatuses lists every task status (filters, locale checks).
var AllStatuses = []Status{StatusPending, StatusInProgress, StatusAwaitingControl, StatusDeferred, StatusCompleted}

// ---------- counters & search ----------

// Counters feed the sidebar badges, like the portal task counters.
type Counters struct {
	Open      int // open tasks where I am responsible or accomplice
	Overdue   int // of them past the deadline
	ToControl int // tasks I created that wait for my approval
	Comments  int // tasks with messages I have not seen (green «новые комментарии»)
}

func (s *Store) Counters(ctx context.Context, userID string) (Counters, error) {
	var c Counters
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE t.status <> 'completed'),
		       count(*) FILTER (WHERE t.status <> 'completed' AND t.deadline < now()),
		       (SELECT count(*) FROM tasks c WHERE c.creator_id = $1 AND c.status = 'awaiting_control'),
		       (SELECT count(DISTINCT n.task_id) FROM notifications n
		         WHERE n.user_id = $1 AND n.read_at IS NULL AND n.kind IN ('comment', 'mention') AND n.task_id IS NOT NULL)
		FROM tasks t
		WHERE t.responsible_id = $1
		   OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = $1 AND tp.role = 'accomplice')`,
		userID).Scan(&c.Open, &c.Overdue, &c.ToControl, &c.Comments)
	return c, err
}

// Search finds tasks visible to the user by title or description.
func (s *Store) Search(ctx context.Context, userID, q string) ([]Task, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
	rows, err := s.db.Query(ctx, taskSelect+`
		WHERE (t.title ILIKE $2 OR t.description ILIKE $2)
		  AND `+visibleSQL("$1", "$3")+`
		ORDER BY (t.status = 'completed'), t.updated_at DESC LIMIT 50`, userID, pattern, IsAdmin(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.FillUnread(ctx, userID, out)
}

// CheckVisible returns nil if the user may see the task (ErrForbidden / ErrNotFound otherwise).
func (s *Store) CheckVisible(ctx context.Context, userID, taskID string) error {
	_, err := loadAccess(ctx, s.db, userID, taskID, false)
	return err
}

// TaskByID loads one task row (the caller has checked visibility).
func (s *Store) TaskByID(ctx context.Context, taskID string) (Task, error) {
	t, err := scanTask(s.db.QueryRow(ctx, taskSelect+" WHERE t.id = $1", taskID))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// SetVisibility changes who sees the project's tasks (project owner/manager or admin mode).
func (s *Store) SetVisibility(ctx context.Context, p Project, visibility string) error {
	if !p.CanManage() {
		return ErrForbidden
	}
	if visibility != "members" && visibility != "participants" {
		return invalid("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET visibility = $2, updated_at = now() WHERE id = $1`, p.ID, visibility)
	return err
}

// TaskRef is a task by key and title (a path of parents).
type TaskRef struct{ Key, Title string }

// Ancestors are the task's parents from the top one down (a subtask of a subtask of …).
func (s *Store) Ancestors(ctx context.Context, taskID string) ([]TaskRef, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT parent_id, 1 AS depth FROM tasks WHERE id = $1
			UNION ALL
			SELECT t.parent_id, up.depth + 1 FROM tasks t JOIN up ON t.id = up.parent_id WHERE up.depth < 50)
		SELECT p.key || '-' || t.number, t.title FROM up JOIN tasks t ON t.id = up.parent_id JOIN projects p ON p.id = t.project_id
		ORDER BY up.depth DESC`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[TaskRef])
}

// SubtreeProgress counts all the task's descendants (at any depth) and the completed ones.
func (s *Store) SubtreeProgress(ctx context.Context, taskID string) (done, total int, err error) {
	err = s.db.QueryRow(ctx, `WITH RECURSIVE down AS (
			SELECT id, status, 1 AS depth FROM tasks WHERE parent_id = $1
			UNION ALL
			SELECT t.id, t.status, down.depth + 1 FROM tasks t JOIN down ON t.parent_id = down.id WHERE down.depth < 50)
		SELECT count(*) FILTER (WHERE status = 'completed'), count(*) FROM down`, taskID).Scan(&done, &total)
	return
}

// TimeValue is how a time entry is kept in the history: "seconds|YYYY-MM-DD|comment".
func TimeValue(seconds int, day time.Time, comment string) string {
	return strconv.Itoa(seconds) + "|" + day.Format("2006-01-02") + "|" + strings.TrimSpace(comment)
}

// FileAdded writes «файл добавлен» to the task's history (the file store calls it after an upload).
func (s *Store) FileAdded(ctx context.Context, userID, taskID, name string) error {
	return addHistory(ctx, s.db, taskID, userID, "file", "", name)
}

// Delegate is «Делегировать»: the responsible hands an open task to another person, who becomes the
// responsible, and stays in it as an auditor (unless they made the task). The newcomer leaves the accomplices and
// auditors; the status and the other fields are kept.
func (s *Store) Delegate(ctx context.Context, userID, taskID, toID string) error {
	if toID == "" || toID == userID {
		return invalid("err.pick_person")
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !a.Roles.Responsible {
			return ErrForbidden
		}
		if a.Status.Closed() {
			return invalid("err.delegate_closed")
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active)`, toID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return invalid("err.pick_person")
		}
		if err := mayAssign(ctx, tx, userID, toID); err != nil {
			return err
		}
		if err := collabStrangers(ctx, tx, a.ProjectID, toID); err != nil {
			return err
		}
		from := a.ResponsibleID
		if _, err := tx.Exec(ctx, `UPDATE tasks SET responsible_id = $2, version = version + 1, updated_at = now() WHERE id = $1`,
			taskID, toID); err != nil {
			return err
		}
		if err := addHistory(ctx, tx, taskID, userID, "responsible", userName(ctx, tx, from), userName(ctx, tx, toID)); err != nil {
			return err
		}
		for _, role := range []string{"accomplice", "auditor"} {
			if err := setParticipant(ctx, tx, userID, taskID, toID, role, false); err != nil {
				return err
			}
		}
		if from == a.CreatorID {
			return nil
		}
		return setParticipant(ctx, tx, userID, taskID, from, "auditor", true)
	})
}

// SetResponsible hands the task to another responsible (the group action of the list), keeping its other fields.
func (s *Store) SetResponsible(ctx context.Context, userID, taskID, responsibleID string) error {
	c, err := s.GetCard(ctx, userID, taskID)
	if err != nil {
		return err
	}
	if c.ResponsibleID == responsibleID {
		return nil
	}
	return s.UpdateTask(ctx, userID, taskID, TaskUpdate{Version: c.Version, Title: c.Title, Description: c.Description,
		ResponsibleID: responsibleID, Priority: c.Priority, Deadline: c.Deadline, RequiresControl: c.RequiresControl,
		EstimateMinutes: c.EstimateMinutes, StartPlan: c.StartPlan, EndPlan: c.EndPlan,
		AllowChangeDeadline: c.AllowChangeDeadline, MatchWorkTime: c.MatchWorkTime, Params: c.Params})
}
