package tasks

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestSubordinateTasks: a department head (and a deputy) sees the tasks of the employees of their departments with
// all departments below, has the creator's powers in the tasks those employees created, and only sees the rest.
func TestSubordinateTasks(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
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
	head, deputy, subHead, worker, other, stranger := user("Начальник"), user("Заместитель"), user("Начальник группы"),
		user("Сотрудник"), user("Коллега"), user("Чужой")
	dept := func(name, parent, headID string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ($1, $2, $3) RETURNING id`,
			name, nullable(parent), nullable(headID)).Scan(&id))
		return id
	}
	sales := dept("Продажи", "", head)
	group := dept("Группа", sales, subHead)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4), ($5, $2)`,
		deputy, sales, worker, group, head)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO department_deputies (department_id, user_id) VALUES ($1, $2)`, sales, deputy)
	must(err)

	s := NewStore(pool)
	subs, err := s.Subordinates(ctx, deputy)
	must(err)
	if len(subs) != 2 { // the worker and the head of the group, not the head the deputy stands in for
		t.Errorf("the deputy stands above %+v", subs)
	}
	_, err = s.CreateProject(ctx, other, "OT", "other", "")
	must(err)
	p, err := s.ProjectByKey(ctx, other, "OT")
	must(err)
	must(s.SetVisibility(ctx, p, "participants"))
	// the worker (in a sub-department) created one task, and does another one
	mine, err := s.CreateTask(ctx, worker, p, NewTask{Title: "Поставил сотрудник", ResponsibleID: other})
	must(err)
	given, err := s.CreateTask(ctx, other, p, NewTask{Title: "Делает сотрудник", ResponsibleID: worker})
	must(err)
	foreign, err := s.CreateTask(ctx, other, p, NewTask{Title: "Без сотрудника", ResponsibleID: stranger})
	must(err)
	mineID, _ := s.TaskIDByKey(ctx, mine)
	givenID, _ := s.TaskIDByKey(ctx, given)
	foreignID, _ := s.TaskIDByKey(ctx, foreign)

	for _, boss := range []string{head, deputy, subHead} {
		c, err := s.GetCard(ctx, boss, mineID)
		must(err)
		if !c.Roles.Creator {
			t.Errorf("the head of the creator has no creator's powers: %+v", c.Roles)
		}
		c, err = s.GetCard(ctx, boss, givenID)
		must(err)
		if c.Roles.Creator || c.Roles.Responsible || c.Roles.Manager {
			t.Errorf("the head of the responsible got powers: %+v", c.Roles)
		}
		if _, err := s.GetCard(ctx, boss, foreignID); !errors.Is(err, ErrForbidden) {
			t.Errorf("a task without subordinates is open to the head: %v", err)
		}
		l, err := s.ListTasks(ctx, Filter{ProjectID: p.ID, Viewer: boss})
		must(err)
		if len(l) != 2 {
			t.Errorf("the head sees %d tasks of the project, want 2", len(l))
		}
		l, err = s.ListTasks(ctx, Filter{UserID: boss, Role: "subordinates", OnlyOpen: true})
		must(err)
		if len(l) != 2 {
			t.Errorf("«Подчинённые» has %d tasks, want 2", len(l))
		}
	}
	// the head of the group stands above the worker, not above the head of the department
	headTask, err := s.CreateTask(ctx, other, p, NewTask{Title: "Делает начальник", ResponsibleID: head})
	must(err)
	headTaskID, _ := s.TaskIDByKey(ctx, headTask)
	if _, err := s.GetCard(ctx, subHead, headTaskID); !errors.Is(err, ErrForbidden) {
		t.Errorf("a task of one's head is open: %v", err)
	}
	if _, err := s.GetCard(ctx, deputy, headTaskID); !errors.Is(err, ErrForbidden) {
		t.Errorf("a task of the head is open to the deputy: %v", err)
	}
	// the head of the creator may change the task, as the creator may
	c, err := s.GetCard(ctx, head, mineID)
	must(err)
	must(s.UpdateTask(ctx, head, mineID, TaskUpdate{Version: c.Version, Title: "Поправил начальник", ResponsibleID: other}))
	if _, err := s.GetCard(ctx, worker, foreignID); !errors.Is(err, ErrForbidden) {
		t.Errorf("an employee sees a task of the colleague: %v", err)
	}
	if l, _ := s.ListTasks(ctx, Filter{UserID: stranger, Role: "subordinates"}); len(l) != 0 {
		t.Errorf("one with no subordinates has %d of their tasks", len(l))
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
