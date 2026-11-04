package tasks

import (
	"context"
	"testing"
	"time"
)

// TestProjectStats: the columns of the projects list — activity, efficiency of the month, members with faces, the
// person's counters — and sorting by them.
func TestProjectStats(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	owner, worker := newUser("owner"), newUser("worker")
	keyA, err := s.CreateProject(ctx, owner, "PA", "Альфа", "")
	must(err)
	keyB, err := s.CreateProject(ctx, owner, "PB", "Бета", "")
	must(err)
	a, _ := s.ProjectByKey(ctx, owner, keyA)
	b, _ := s.ProjectByKey(ctx, owner, keyB)
	for i := range 6 {
		must(s.SetMember(ctx, a, newUser("m"+string(rune('a'+i))), "member"))
	}
	must(s.SetMember(ctx, a, worker, "member"))

	now := time.Now()
	past := now.Add(-time.Hour)
	_, err = s.CreateTask(ctx, owner, a, NewTask{Title: "Просрочена", ResponsibleID: worker, Deadline: &past})
	must(err)
	_, err = s.CreateTask(ctx, owner, a, NewTask{Title: "В срок", ResponsibleID: worker})
	must(err)
	_, err = s.CreateTask(ctx, owner, a, NewTask{Title: "Не считается", ResponsibleID: worker, Deadline: &past, NoEfficiency: true})
	must(err)
	// Beta was touched last
	_, err = pool.Exec(ctx, `UPDATE projects SET updated_at = now() + interval '1 minute' WHERE id = $1`, b.ID)
	must(err)

	stats, err := s.ProjectStatsOf(ctx, worker, []string{a.ID, b.ID}, now.Add(time.Minute))
	must(err)
	sa, sb := stats[a.ID], stats[b.ID]
	if sa.Members != 8 || len(sa.Faces) != listFaces || sa.Faces[0].ID != owner {
		t.Fatalf("members of A = %d, faces %+v", sa.Members, sa.Faces)
	}
	if sa.Efficiency != 50 || sb.Efficiency != 100 {
		t.Fatalf("efficiency A = %d, B = %d", sa.Efficiency, sb.Efficiency)
	}
	if sa.Overdue != 2 {
		t.Fatalf("overdue of the worker = %d", sa.Overdue)
	}
	if !sb.Activity.After(sa.Activity) {
		t.Fatalf("activity A %v, B %v", sa.Activity, sb.Activity)
	}

	list := []Project{a, b}
	SortProjects(list, stats, "activity", true)
	if list[0].ID != b.ID {
		t.Fatal("not the latest activity first")
	}
	SortProjects(list, stats, "members", true)
	if list[0].ID != a.ID {
		t.Fatal("not the most members first")
	}
	SortProjects(list, stats, "name", true)
	if list[0].ID != b.ID {
		t.Fatal("name desc: Бета before Альфа")
	}
	SortProjects(list, stats, "efficiency", false)
	if list[0].ID != a.ID {
		t.Fatal("efficiency asc: the lower first")
	}
}
