package portalrest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/legacyimport"
)

// Tasks of a portal through REST: groups become projects (with their members), tasks keep their people, dates,
// statuses, subtasks, comments and checklists. Texts lose every real name (the anonymizer knows the portal's people,
// so ImportUsers runs first in the same run). portal_map remembers what each object became: a second run updates.

type TaskStats struct{ Projects, Tasks, Comments, Checks int }

type portalGroup struct {
	ID          flexStr `json:"ID"`
	Name        string  `json:"NAME"`
	Description string  `json:"DESCRIPTION"`
	OwnerID     flexStr `json:"OWNER_ID"`
	Closed      string  `json:"CLOSED"`
	Scrum       flexStr `json:"SCRUM_MASTER_ID"`
}

type portalTask struct {
	ID            flexStr  `json:"id"`
	ParentID      flexStr  `json:"parentId"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Status        flexStr  `json:"status"`
	Priority      flexStr  `json:"priority"`
	GroupID       flexStr  `json:"groupId"`
	CreatedBy     flexStr  `json:"createdBy"`
	ResponsibleID flexStr  `json:"responsibleId"`
	Accomplices   []string `json:"accomplices"`
	Auditors      []string `json:"auditors"`
	Deadline      string   `json:"deadline"`
	StartPlan     string   `json:"startDatePlan"`
	EndPlan       string   `json:"endDatePlan"`
	Created       string   `json:"createdDate"`
	Closed        string   `json:"closedDate"`
	Changed       string   `json:"changedDate"`
	TaskControl   string   `json:"taskControl"`
	AllowDeadline string   `json:"allowChangeDeadline"`
	MatchWorkTime string   `json:"matchWorkTime"`
	TimeEstimate  flexStr  `json:"timeEstimate"`
	Comments      flexStr  `json:"commentsCount"`
}

type portalComment struct {
	ID      flexStr `json:"ID"`
	Author  flexStr `json:"AUTHOR_ID"`
	Message string  `json:"POST_MESSAGE"`
	Date    string  `json:"POST_DATE"`
}

type portalCheck struct {
	ID       flexStr `json:"ID"`
	ParentID flexStr `json:"PARENT_ID"`
	Title    string  `json:"TITLE"`
	Done     string  `json:"IS_COMPLETE"`
	Sort     flexStr `json:"SORT_INDEX"`
}

func when(s string) *time.Time {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.Year() < 1990 {
		return nil
	}
	return &t
}

// ImportTasks reads the portal's groups and tasks and writes them into go24.
func ImportTasks(ctx context.Context, c *Client, db *pgxpool.Pool, o Options) (TaskStats, error) {
	var st TaskStats
	var groups []portalGroup
	// IS_ADMIN: every group of the portal, not only those the webhook's owner is in
	if err := List(ctx, c, "sonet_group.get", map[string]any{"ORDER": map[string]string{"ID": "ASC"}, "IS_ADMIN": "Y"},
		func(g portalGroup) error { groups = append(groups, g); return nil }); err != nil {
		return st, err
	}
	members := map[string][][2]string{} // group → (user, role)
	for i := 0; i < len(groups); i += 50 {
		cmds := map[string]string{}
		for _, g := range groups[i:min(i+50, len(groups))] {
			cmds["g"+string(g.ID)] = "sonet_group.user.get?ID=" + string(g.ID)
		}
		res, err := c.Batch(ctx, cmds)
		if err != nil {
			return st, err
		}
		for name, raw := range res {
			var list []struct {
				User flexStr `json:"USER_ID"`
				Role string  `json:"ROLE"`
			}
			if json.Unmarshal(raw, &list) == nil {
				for _, m := range list {
					members[strings.TrimPrefix(name, "g")] = append(members[strings.TrimPrefix(name, "g")], [2]string{string(m.User), m.Role})
				}
			}
		}
	}
	var list []portalTask
	for start := 0; ; { // tasks.task.list pages come as {"tasks": [...]}
		res, err := c.Call(ctx, "tasks.task.list", map[string]any{"start": start,
			"order": map[string]string{"ID": "asc"},
			"select": []string{"ID", "PARENT_ID", "TITLE", "DESCRIPTION", "STATUS", "PRIORITY", "GROUP_ID", "CREATED_BY", "RESPONSIBLE_ID",
				"ACCOMPLICES", "AUDITORS", "DEADLINE", "START_DATE_PLAN", "END_DATE_PLAN", "CREATED_DATE", "CLOSED_DATE", "CHANGED_DATE",
				"TASK_CONTROL", "ALLOW_CHANGE_DEADLINE", "MATCH_WORK_TIME", "TIME_ESTIMATE", "COMMENTS_COUNT"}})
		if err != nil {
			return st, err
		}
		var page portalTaskList
		if err := json.Unmarshal(res.Result, &page); err != nil {
			return st, fmt.Errorf("tasks.task.list: unexpected result: %w", err)
		}
		list = append(list, page.Tasks...)
		if res.Next == nil {
			break
		}
		start = *res.Next
	}
	// comments and checklists, 50 tasks per call
	comments := map[string][]portalComment{}
	checks := map[string][]portalCheck{}
	for i := 0; i < len(list); i += 25 {
		cmds := map[string]string{}
		for _, t := range list[i:min(i+25, len(list))] {
			if atoi(string(t.Comments)) > 0 {
				cmds["c"+string(t.ID)] = "task.commentitem.getlist?TASKID=" + string(t.ID) + "&ORDER[POST_DATE]=asc"
			}
			cmds["k"+string(t.ID)] = "task.checklistitem.getlist?TASKID=" + string(t.ID)
		}
		res, err := c.Batch(ctx, cmds)
		if err != nil {
			return st, err
		}
		for name, raw := range res {
			switch name[0] {
			case 'c':
				var cs []portalComment
				if json.Unmarshal(raw, &cs) == nil {
					comments[name[1:]] = cs
				}
			case 'k':
				var ks []portalCheck
				if json.Unmarshal(raw, &ks) == nil {
					checks[name[1:]] = ks
				}
			}
		}
	}

	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('go24.quiet', 'on', true)`); err != nil { // an import notifies nobody
			return err
		}
		user := map[string]string{}
		rows, err := tx.Query(ctx, `SELECT oidc_sub, id::text FROM users WHERE oidc_sub LIKE $1`, "portal:"+o.Portal+":%")
		if err != nil {
			return err
		}
		for rows.Next() {
			var sub, id string
			if err := rows.Scan(&sub, &id); err != nil {
				rows.Close()
				return err
			}
			user[strings.TrimPrefix(sub, "portal:"+o.Portal+":")] = id
		}
		rows.Close()
		if len(user) == 0 {
			return fmt.Errorf("no people of portal %q in go24: import users first (-what users,tasks)", o.Portal)
		}
		ids := make([]string, 0, len(user))
		for k := range user {
			ids = append(ids, k)
		}
		sort.Slice(ids, func(i, j int) bool { return atoi(ids[i]) < atoi(ids[j]) })
		fallback := user[ids[0]] // someone who left the portal (or a bot) is shown as its first person
		who := func(portal string) string {
			if id, ok := user[portal]; ok {
				return id
			}
			return fallback
		}
		mapped := func(kind, portal string) (string, bool) {
			var id string
			err := tx.QueryRow(ctx, `SELECT id::text FROM portal_map WHERE portal = $1 AND kind = $2 AND portal_id = $3`, o.Portal, kind, atoi(portal)).Scan(&id)
			return id, err == nil
		}
		remember := func(kind, portal, id string) error {
			_, err := tx.Exec(ctx, `INSERT INTO portal_map (portal, kind, portal_id, id) VALUES ($1, $2, $3, $4)
				ON CONFLICT (portal, kind, portal_id) DO UPDATE SET id = EXCLUDED.id`, o.Portal, kind, atoi(portal), id)
			return err
		}
		text := func(s string) string { return legacyimport.CleanBBCode(o.Anon.Text(s), o.Anon) }

		// projects
		project := map[string]string{}
		project0 := func(key, name, desc, owner, visibility, kind string, archived bool, portal string) (string, error) {
			if id, ok := mapped("group", portal); ok {
				_, err := tx.Exec(ctx, `UPDATE projects SET name = $2, description = $3, archived = $4 WHERE id = $1`, id, name, desc, archived)
				return id, err
			}
			var id string
			if err := tx.QueryRow(ctx, `INSERT INTO projects (key, name, description, owner_id, visibility, kind, archived)
				VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, key, name, desc, owner, visibility, kind, archived).Scan(&id); err != nil {
				return "", fmt.Errorf("project %s: %w", key, err)
			}
			st.Projects++
			return id, remember("group", portal, id)
		}
		for _, g := range groups {
			kind := "group"
			if atoi(string(g.Scrum)) > 0 {
				kind = "scrum"
			}
			id, err := project0("CR"+string(g.ID), strings.TrimSpace(g.Name), text(g.Description), who(string(g.OwnerID)), "members", kind, g.Closed == "Y", string(g.ID))
			if err != nil {
				return err
			}
			project[string(g.ID)] = id
			for _, m := range members[string(g.ID)] {
				uid, ok := user[m[0]]
				if !ok {
					continue
				}
				role := map[string]string{"A": "owner", "E": "manager", "K": "member"}[m[1]]
				if role == "" {
					continue // invited, not yet a member
				}
				if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, $3)
					ON CONFLICT (project_id, user_id) DO UPDATE SET role = EXCLUDED.role`, id, uid, role); err != nil {
					return err
				}
			}
		}
		// tasks outside groups: only their participants see them, as on the portal
		loose, err := project0("ACME", "Задачи без проекта (портал)", "Задачи портала, которые не были в группах: их видят только участники.",
			fallback, "participants", "group", false, "0")
		if err != nil {
			return err
		}
		project["0"] = loose

		// tasks
		taskID := map[string]string{}
		for _, t := range list {
			pid := project[string(t.GroupID)]
			if pid == "" {
				pid = loose
			}
			status := legacyimport.MapStatus(atoi(string(t.Status)))
			prio := min(max(atoi(string(t.Priority)), 0), 2)
			var est *int
			if s := atoi(string(t.TimeEstimate)); s > 0 {
				m := s / 60
				est = &m
			}
			created, closed := when(t.Created), when(t.Closed)
			if created == nil {
				now := time.Now()
				created = &now
			}
			if status != "completed" {
				closed = nil
			}
			args := []any{pid, clip(text(t.Title), 500), text(t.Description), status, prio, who(string(t.CreatedBy)), who(string(t.ResponsibleID)),
				t.TaskControl == "Y", when(t.Deadline), when(t.StartPlan), when(t.EndPlan), t.AllowDeadline == "Y", t.MatchWorkTime == "Y",
				est, *created, closed}
			id, ok := mapped("task", string(t.ID))
			if ok {
				// a task that changed its project (or was in the wrong one) gets the next number there
				if _, err := tx.Exec(ctx, `WITH n AS (UPDATE projects SET task_seq = task_seq + 1 WHERE id = $1
						AND NOT EXISTS (SELECT 1 FROM tasks WHERE id = $2 AND project_id = $1) RETURNING task_seq)
					UPDATE tasks SET number = n.task_seq, project_id = $1 FROM n WHERE tasks.id = $2`, pid, id); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
				args = append(args, id)
				if _, err := tx.Exec(ctx, `UPDATE tasks SET project_id = $1, title = $2, description = $3, status = $4, priority = $5,
					creator_id = $6, responsible_id = $7, requires_control = $8, deadline = $9, start_plan = $10, end_plan = $11,
					allow_change_deadline = $12, match_work_time = $13, estimate_minutes = $14, created_at = $15, closed_at = $16
					WHERE id = $17`, args...); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
			} else {
				if err := tx.QueryRow(ctx, `WITH n AS (UPDATE projects SET task_seq = task_seq + 1 WHERE id = $1 RETURNING task_seq)
					INSERT INTO tasks (project_id, number, title, description, status, priority, creator_id, responsible_id,
					  requires_control, deadline, start_plan, end_plan, allow_change_deadline, match_work_time, estimate_minutes,
					  created_at, updated_at, closed_at)
					SELECT $1, n.task_seq, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15, $16 FROM n
					RETURNING id`, args...).Scan(&id); err != nil {
					return fmt.Errorf("task %s: %w", t.ID, err)
				}
				if err := remember("task", string(t.ID), id); err != nil {
					return err
				}
				st.Tasks++
			}
			taskID[string(t.ID)] = id
			if _, err := tx.Exec(ctx, `DELETE FROM task_participants WHERE task_id = $1`, id); err != nil {
				return err
			}
			for role, people := range map[string][]string{"accomplice": t.Accomplices, "auditor": t.Auditors} {
				for _, p := range people {
					if uid, ok := user[p]; ok {
						if _, err := tx.Exec(ctx, `INSERT INTO task_participants (task_id, user_id, role) VALUES ($1, $2, $3)
							ON CONFLICT DO NOTHING`, id, uid, role); err != nil {
							return err
						}
					}
				}
			}
			for _, cm := range comments[string(t.ID)] {
				if _, ok := mapped("comment", string(cm.ID)); ok {
					continue
				}
				body := text(cm.Message)
				if strings.TrimSpace(body) == "" {
					continue
				}
				at := when(cm.Date)
				if at == nil {
					at = created
				}
				var cid string
				if err := tx.QueryRow(ctx, `INSERT INTO task_comments (task_id, author_id, body, created_at, updated_at)
					VALUES ($1, $2, $3, $4, $4) RETURNING id`, id, who(string(cm.Author)), clip(body, 20000), *at).Scan(&cid); err != nil {
					return err
				}
				if err := remember("comment", string(cm.ID), cid); err != nil {
					return err
				}
				st.Comments++
			}
			// checklist: the portal's top items are the lists' names; their children are the items
			ks := checks[string(t.ID)]
			parents := map[string]bool{}
			for _, k := range ks {
				parents[string(k.ParentID)] = true
			}
			sort.Slice(ks, func(i, j int) bool { return atoi(string(ks[i].Sort)) < atoi(string(ks[j].Sort)) })
			for n, k := range ks {
				if atoi(string(k.ParentID)) == 0 && parents[string(k.ID)] {
					continue
				}
				if _, ok := mapped("check", string(k.ID)); ok {
					continue
				}
				var kid string
				if err := tx.QueryRow(ctx, `INSERT INTO task_checklist_items (task_id, title, done, sort, created_by)
					VALUES ($1, $2, $3, $4, $5) RETURNING id`, id, clip(text(k.Title), 500), k.Done == "Y", n, who(string(t.CreatedBy))).Scan(&kid); err != nil {
					return err
				}
				if err := remember("check", string(k.ID), kid); err != nil {
					return err
				}
				st.Checks++
			}
		}
		// subtasks, once every task exists
		for _, t := range list {
			if p := taskID[string(t.ParentID)]; p != "" && atoi(string(t.ParentID)) > 0 {
				if _, err := tx.Exec(ctx, `UPDATE tasks SET parent_id = $2 WHERE id = $1`, taskID[string(t.ID)], p); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return st, err
}

// portalTaskList: tasks.task.list wraps its page as {"tasks": [...]}.
type portalTaskList struct {
	Tasks []portalTask `json:"tasks"`
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
