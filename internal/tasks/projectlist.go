package tasks

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The projects list: besides the name and the role, each row shows the project's «Активность» (when
// anything happened in it last), «Эффективность» (the share of its tasks in work this month without violations, as
// the person's efficiency counts them), «Участники» (the first faces and how many there are) and the person's
// counters — their overdue tasks and the tasks with comments they have not seen.

// Face is a member shown in the row.
type Face struct {
	ID, Name, PhotoKey string
}

// ProjectStats are the columns of one row of the projects list.
type ProjectStats struct {
	Activity   time.Time
	Efficiency int // 100 when no task was in work this month
	Members    int
	Faces      []Face // up to listFaces, owner and moderators first
	Overdue    int
	Comments   int
}

const listFaces = 5

// Sorts of the projects list.
var ProjectSorts = []string{"name", "activity", "efficiency", "members"}

// ProjectStatsOf counts the columns of the given projects for the user; now is the moment the month runs to.
func (s *Store) ProjectStatsOf(ctx context.Context, userID string, ids []string, now time.Time) (map[string]ProjectStats, error) {
	out := make(map[string]ProjectStats, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text,
		       greatest(p.updated_at, (SELECT max(t.updated_at) FROM tasks t WHERE t.project_id = p.id),
		                (SELECT max(c.created_at) FROM task_comments c JOIN tasks t ON t.id = c.task_id WHERE t.project_id = p.id)),
		       (SELECT count(*) FROM project_members m WHERE m.project_id = p.id)::int,
		       `+collabCountersSQL+`
		FROM projects p WHERE p.id = ANY($2::uuid[])`, userID, ids)
	if err != nil {
		return nil, err
	}
	var id string
	var st ProjectStats
	if _, err := pgx.ForEachRow(rows, []any{&id, &st.Activity, &st.Members, &st.Overdue, &st.Comments}, func() error {
		st.Efficiency = 100
		out[id] = st
		return nil
	}); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(ctx, `
		SELECT m.project_id::text, u.id::text, u.name, u.photo_key FROM project_members m JOIN users u ON u.id = m.user_id
		WHERE m.project_id = ANY($1::uuid[])
		ORDER BY m.project_id, CASE m.role WHEN 'owner' THEN 0 WHEN 'manager' THEN 1 ELSE 2 END, m.added_at, u.name`, ids)
	if err != nil {
		return nil, err
	}
	var f Face
	if _, err := pgx.ForEachRow(rows, []any{&id, &f.ID, &f.Name, &f.PhotoKey}, func() error {
		if st := out[id]; len(st.Faces) < listFaces {
			st.Faces = append(st.Faces, f)
			out[id] = st
		}
		return nil
	}); err != nil {
		return nil, err
	}

	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	effs, err := s.projectEfficiency(ctx, ids, from, now)
	if err != nil {
		return nil, err
	}
	for id, pct := range effs {
		st := out[id]
		st.Efficiency = pct
		out[id] = st
	}
	return out, nil
}

// projectEfficiency is the efficiency of each project for [from, end): its tasks in work then, not marked «Не учитывать
// в эффективности», against those overdue or returned for rework.
func (s *Store) projectEfficiency(ctx context.Context, ids []string, from, end time.Time) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `SELECT t.project_id::text, t.id::text, t.created_at, t.deadline, t.closed_at
		FROM tasks t WHERE t.project_id = ANY($1::uuid[]) AND NOT t.no_efficiency
		  AND t.created_at < $3 AND (t.closed_at IS NULL OR t.closed_at >= $2)`, ids, from, end)
	if err != nil {
		return nil, err
	}
	byProject := map[string][]*effTask{}
	byID := map[string]*effTask{}
	var pid string
	var t effTask
	if _, err := pgx.ForEachRow(rows, []any{&pid, &t.ID, &t.Created, &t.Deadline, &t.ClosedAt}, func() error {
		x := t
		byProject[pid] = append(byProject[pid], &x)
		byID[x.ID] = &x
		return nil
	}); err != nil {
		return nil, err
	}
	taskIDs := make([]string, 0, len(byID))
	for id := range byID {
		taskIDs = append(taskIDs, id)
	}
	rows, err = s.db.Query(ctx, `SELECT task_id::text, created_at FROM task_history
		WHERE field = 'status' AND old_value = 'awaiting_control' AND new_value <> 'completed'
		  AND created_at >= $1 AND created_at < $2 AND task_id = ANY($3::uuid[])`, from, end, taskIDs)
	if err != nil {
		return nil, err
	}
	var id string
	var at time.Time
	if _, err := pgx.ForEachRow(rows, []any{&id, &at}, func() error {
		byID[id].reworks = append(byID[id].reworks, at)
		return nil
	}); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(byProject))
	for pid, list := range byProject {
		flat := make([]effTask, len(list))
		for i, t := range list {
			flat[i] = *t
		}
		total, violated, _ := score(flat, from, end)
		out[pid] = percent(total, violated)
	}
	return out, nil
}

// SortProjects orders the list by the column (ProjectSorts; name — by default), desc reversing it; ties go by name.
func SortProjects(list []Project, stats map[string]ProjectStats, by string, desc bool) {
	less := func(a, b Project) int {
		x, y := stats[a.ID], stats[b.ID]
		switch by {
		case "activity":
			return x.Activity.Compare(y.Activity)
		case "efficiency":
			return x.Efficiency - y.Efficiency
		case "members":
			return x.Members - y.Members
		}
		return 0
	}
	sort.SliceStable(list, func(i, j int) bool {
		c := less(list[i], list[j])
		if desc {
			c = -c
		}
		if c == 0 {
			n := strings.Compare(strings.ToLower(list[i].Name), strings.ToLower(list[j].Name))
			if by == "name" && desc {
				n = -n
			}
			return n < 0
		}
		return c < 0
	})
}
