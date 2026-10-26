package tasks

import (
	"context"
	"errors"
	"testing"

	"go24/internal/i18n"
)

// TestAccessRoles: the roles of «Права доступа». The default role keeps the rights as they were (a head sees the
// tasks of subordinates and has the creator's powers in theirs); a role of its own takes them away or gives all
// tasks; «Ставить задачи» limits the responsible; collabs and guests are not opened by a role.
func TestAccessRoles(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	head, worker, other, auditor := user("Начальник"), user("Сотрудник"), user("Коллега"), user("Ревизор")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Продажи', $1) RETURNING id`, head).Scan(&dept))
	_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, worker, dept)
	must(err)

	pk, err := s.CreateProject(ctx, other, "AR", "Права", "")
	must(err)
	p, err := s.ProjectByKey(ctx, other, pk)
	must(err)
	must(s.SetVisibility(ctx, p, "participants"))
	byWorker, err := s.CreateTask(ctx, worker, p, NewTask{Title: "Поставил сотрудник", ResponsibleID: other})
	must(err)
	foreign, err := s.CreateTask(ctx, other, p, NewTask{Title: "Чужая"})
	must(err)
	byWorkerID, _ := s.TaskIDByKey(ctx, byWorker)
	foreignID, _ := s.TaskIDByKey(ctx, foreign)
	sees := func(u, id string) bool {
		t.Helper()
		l, err := s.ListTasks(ctx, Filter{ProjectID: p.ID, Viewer: u})
		must(err)
		listed := false
		for _, x := range l {
			listed = listed || x.ID == id
		}
		_, err = s.GetCard(ctx, u, id)
		if (err == nil) != listed {
			t.Fatalf("the list (%v) and the card (%v) disagree", listed, err)
		}
		return listed
	}

	// the default role: as before
	if !sees(head, byWorkerID) || sees(head, foreignID) || sees(auditor, foreignID) {
		t.Fatal("the default role changed what one sees")
	}
	if c, _ := s.GetCard(ctx, head, byWorkerID); !c.CanEdit() || !c.CanDelete {
		t.Fatal("the head lost the creator's powers in a subordinate's task")
	}

	roles, err := s.AccessRoles(ctx)
	must(err)
	if len(roles) != 1 || !roles[0].Default || roles[0].Perms["view"] != "sub" || roles[0].Perms["assign"] != "all" {
		t.Fatalf("the built-in role: %+v", roles)
	}
	if err := s.DeleteAccessRole(ctx, roles[0].ID); err == nil {
		t.Fatal("the default role was deleted")
	}
	// the default role without subordinates: the head keeps their own tasks only
	_, err = s.SaveAccessRole(ctx, AccessRole{ID: roles[0].ID, Perms: AccessPerms{"view": "own", "edit": "own", "delete": "own", "assign": "sub"}})
	must(err)
	if sees(head, byWorkerID) {
		t.Fatal("the head sees a subordinate's task without the right")
	}
	// «Ставить задачи: себе и подчинённым»
	var e *i18n.Error
	if _, err := s.CreateTask(ctx, worker, p, NewTask{Title: "Начальнику", ResponsibleID: head}); !errors.As(err, &e) || e.Key != "err.assign_forbidden" {
		t.Fatalf("a task assigned upwards: %v", err)
	}
	if _, err := s.CreateTask(ctx, head, p, NewTask{Title: "Сотруднику", ResponsibleID: worker}); err != nil {
		t.Fatalf("a task for a subordinate: %v", err)
	}
	c, err := s.GetCard(ctx, other, foreignID)
	must(err)
	if err := s.UpdateTask(ctx, other, foreignID, TaskUpdate{Version: c.Version, Title: c.Title, ResponsibleID: worker}); !errors.As(err, &e) || e.Key != "err.assign_forbidden" {
		t.Fatalf("the responsible changed against the right: %v", err)
	}

	// a role of its own: all tasks, changed and deleted
	id, err := s.SaveAccessRole(ctx, AccessRole{Name: "Ревизия", Perms: AccessPerms{"view": "all", "edit": "all", "delete": "all", "export": "bogus"}})
	must(err)
	must(s.SetAccessMember(ctx, id, auditor, true))
	perms, err := s.PermsOf(ctx, auditor)
	must(err)
	if perms["view"] != "all" || perms["export"] != "none" || perms["assign"] != "sub" {
		t.Fatalf("the auditor's rights: %v", perms)
	}
	if !sees(auditor, foreignID) {
		t.Fatal("«все» does not show a foreign task")
	}
	c, err = s.GetCard(ctx, auditor, foreignID)
	must(err)
	if !c.CanEdit() || !c.CanDelete {
		t.Fatal("«все» does not let change or delete")
	}
	must(s.UpdateTask(ctx, auditor, foreignID, TaskUpdate{Version: c.Version, Title: "Проверено", ResponsibleID: c.ResponsibleID}))
	must(s.DeleteTask(ctx, auditor, foreignID))
	// out of the role: the default one again
	must(s.SetAccessMember(ctx, id, auditor, false))
	if perms, _ := s.PermsOf(ctx, auditor); perms["view"] != "own" {
		t.Fatalf("out of the role: %v", perms)
	}
	must(s.DeleteAccessRole(ctx, id))

	// a guest keeps their own tasks, whatever role they are given
	guest := user("Гость")
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	id, err = s.SaveAccessRole(ctx, AccessRole{Name: "Всё", Perms: AccessPerms{"view": "all", "export": "all"}})
	must(err)
	must(s.SetAccessMember(ctx, id, guest, true))
	if perms, _ := s.PermsOf(ctx, guest); perms["view"] != "own" || perms["export"] != "none" {
		t.Fatalf("a guest's rights: %v", perms)
	}
	if _, err := s.SaveAccessRole(ctx, AccessRole{Name: "  "}); err == nil {
		t.Fatal("a role without a name")
	}
}
