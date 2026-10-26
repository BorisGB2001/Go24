package tasks

import (
	"context"
	"errors"
	"testing"
)

// TestFlows: a flow is made by the project's manager; a task put into it gets the flow's deadline and the
// responsible by its distribution (moderator; team in turn skipping the absent; the creator until someone takes it);
// the flow's people see its tasks, creators outside the project may put tasks in; a flow that is off takes none.
func TestFlows(t *testing.T) {
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
	boss, anna, boris, vera, client := newUser("boss"), newUser("anna"), newUser("boris"), newUser("vera"), newUser("client")
	p, err := s.CreateProject(ctx, boss, "SUP", "Поддержка", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, boss, p)
	must(err)
	must(s.SetMember(ctx, proj, anna, "member"))
	must(s.SetVisibility(ctx, proj, "participants")) // project members do not see each other's tasks: the flow does it

	in := FlowInput{Name: "Заявки", ProjectKey: "SUP", Distribution: FlowQueue, PlannedMinutes: 120, CreatorsAll: true, Active: true,
		Members: []string{anna, boris, vera}}
	if _, err := s.SaveFlow(ctx, anna, in); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member makes a flow: %v", err)
	}
	if _, err := s.SaveFlow(ctx, boss, FlowInput{Name: "Пусто", ProjectKey: "SUP", Distribution: FlowQueue, PlannedMinutes: 60}); err == nil {
		t.Fatal("a queue without a team")
	}
	id, err := s.SaveFlow(ctx, boss, in)
	must(err)
	if _, err := s.SaveFlow(ctx, boss, in); err == nil {
		t.Fatal("the same name twice")
	}

	// in turn: anna, boris, (vera is away) anna
	must2 := func(key string, err error) string {
		t.Helper()
		must(err)
		tid, err := s.TaskIDByKey(ctx, key)
		must(err)
		return tid
	}
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', current_date, current_date, $1)`, vera)
	must(err)
	var got []string
	for i := 0; i < 3; i++ {
		tid := must2(s.CreateFlowTask(ctx, client, id, FlowTask{Title: "Не работает почта"}))
		tk, err := s.TaskByID(ctx, tid)
		must(err)
		if tk.FlowID != id || tk.Deadline == nil || tk.CreatorID != client {
			t.Fatalf("flow task: %+v", tk)
		}
		got = append(got, tk.ResponsibleID)
	}
	if got[0] != anna || got[1] != boris || got[2] != anna {
		t.Fatalf("queue: %v (anna %s, boris %s)", got, anna, boris)
	}
	// boris (not in the project) sees the flow's tasks, the creator outside the project sees own
	list, err := s.ListTasks(ctx, Filter{FlowID: id, Viewer: boris})
	must(err)
	if len(list) != 3 {
		t.Fatalf("team sees %d", len(list))
	}
	f, err := s.Flow(ctx, boss, id)
	must(err)
	if f.Stats.Queue != 3 || f.Stats.Efficiency != 100 || len(f.Members) != 3 {
		t.Fatalf("flow: %+v", f)
	}

	// manually: the moderator
	in.ID, in.Distribution, in.ModeratorID = id, FlowManually, vera
	_, err = s.SaveFlow(ctx, boss, in)
	must(err)
	tid := must2(s.CreateFlowTask(ctx, client, id, FlowTask{Title: "Вручную"}))
	if tk, _ := s.TaskByID(ctx, tid); tk.ResponsibleID != vera {
		t.Fatalf("moderator: %s", tk.ResponsibleID)
	}

	// himself: waits with the creator, the team is notified, one takes it
	in.Distribution = FlowHimself
	_, err = s.SaveFlow(ctx, boss, in)
	must(err)
	tid = must2(s.CreateFlowTask(ctx, client, id, FlowTask{Title: "Кто возьмёт"}))
	tk, err := s.TaskByID(ctx, tid)
	must(err)
	if tk.ResponsibleID != client || !s.CanTake(ctx, boris, tk) || s.CanTake(ctx, client, tk) {
		t.Fatalf("himself: %+v", tk)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE task_id = $1 AND kind = 'flow_task'`, tid).Scan(&n))
	if n != 3 {
		t.Fatalf("team notified: %d", n)
	}
	must(s.TakeFlowTask(ctx, boris, tid))
	if err := s.TakeFlowTask(ctx, anna, tid); err == nil {
		t.Fatal("taken twice")
	}
	if tk, _ := s.TaskByID(ctx, tid); tk.ResponsibleID != boris || tk.Status != StatusInProgress {
		t.Fatalf("taken: %+v", tk)
	}

	// limited creators and a flow that is off
	in.CreatorsAll, in.Creators = false, nil
	_, err = s.SaveFlow(ctx, boss, in)
	must(err)
	if _, err := s.CreateFlowTask(ctx, client, id, FlowTask{Title: "Нельзя"}); err == nil {
		t.Fatal("a stranger puts a task into a closed flow")
	}
	must(s.SetFlowActive(ctx, boss, id, false))
	if _, err := s.CreateFlowTask(ctx, anna, id, FlowTask{Title: "Выключен"}); err == nil {
		t.Fatal("a flow that is off took a task")
	}
	if err := s.DeleteFlow(ctx, anna, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member deletes the flow: %v", err)
	}
	must(s.DeleteFlow(ctx, boss, id))
	if tk, err := s.TaskByID(ctx, tid); err != nil || tk.FlowID != "" {
		t.Fatalf("task after the flow: %+v %v", tk, err)
	}
}
