package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestProjectCard: «О проекте», the type «проект» with dates, the avatar and the archive.
func TestProjectCard(t *testing.T) {
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
	owner, member := newUser("owner"), newUser("member")
	key, err := s.CreateProject(ctx, owner, "pc", "Сайт", "")
	must(err)
	p, err := s.ProjectByKey(ctx, owner, key)
	must(err)
	must(s.SetMember(ctx, p, member, "member"))
	pm, err := s.ProjectByKey(ctx, member, key)
	must(err)

	start, finish := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	if err := s.SaveProjectInfo(ctx, pm, "x", "", nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member edited the project: %v", err)
	}
	var ve *i18n.Error
	if err := s.SaveProjectInfo(ctx, p, "Сайт", "", &finish, &start); !errors.As(err, &ve) {
		t.Fatalf("finish before start: %v", err)
	}
	must(s.SaveProjectInfo(ctx, p, " Новый сайт ", "Редизайн", &start, &finish))
	must(s.SetProjectKind(ctx, p, "project"))
	_, err = s.SetProjectPhoto(ctx, p, "go24/projects/a.png", "image/png")
	must(err)
	p, _ = s.ProjectByKey(ctx, owner, key)
	if p.Name != "Новый сайт" || p.Kind != "project" || p.DateFinish == nil || p.PhotoURL() == "" {
		t.Fatalf("project = %+v", p)
	}
	if k, ct, err := s.ProjectPhoto(ctx, owner, "pc"); err != nil || k != "go24/projects/a.png" || ct != "image/png" {
		t.Fatalf("photo = %s %s %v", k, ct, err)
	}

	must(s.SetArchived(ctx, p, true))
	if list, _ := s.ListProjects(ctx, member); len(list) != 0 {
		t.Fatalf("an archived project is listed: %+v", list)
	}
	if list, _ := s.ArchivedProjects(ctx, member); len(list) != 1 {
		t.Fatalf("archive = %+v", list)
	}
	pm, _ = s.ProjectByKey(ctx, member, key)
	if _, err := s.CreateTask(ctx, member, pm, NewTask{Title: "Ещё", ResponsibleID: member}); err == nil {
		t.Fatal("a task was created in an archived project")
	}
	p, _ = s.ProjectByKey(ctx, owner, key)
	must(s.SetArchived(ctx, p, false))
	if list, _ := s.ListProjects(ctx, member); len(list) != 1 {
		t.Fatal("the project did not come back")
	}
}

// TestInvites: an invitation, the bell, accepting and declining, who may invite.
func TestInvites(t *testing.T) {
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
	owner, member, anna, petr := newUser("owner"), newUser("member"), newUser("anna"), newUser("petr")
	key, err := s.CreateProject(ctx, owner, "iv", "Приглашения", "")
	must(err)
	p, _ := s.ProjectByKey(ctx, owner, key)
	must(s.SetMember(ctx, p, member, "member"))
	pm, _ := s.ProjectByKey(ctx, member, key)

	if err := s.InviteMember(ctx, pm, member, anna, "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member invited while only managers may: %v", err)
	}
	must(s.InviteMember(ctx, p, owner, anna, "viewer"))
	var ve *i18n.Error
	if err := s.InviteMember(ctx, p, owner, member, "member"); !errors.As(err, &ve) || ve.Key != "err.already_member" {
		t.Fatalf("a member invited: %v", err)
	}
	var bell int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'project_invite' AND project_id = $2`, anna, p.ID).Scan(&bell))
	if inv, _ := s.MyInvites(ctx, anna); len(inv) != 1 || inv[0].Role != "viewer" || bell != 1 {
		t.Fatalf("anna's invitations = %+v, bell %d", inv, bell)
	}
	if _, err := s.ProjectByKey(ctx, anna, key); !errors.Is(err, ErrForbidden) {
		t.Fatalf("invited is not a member yet: %v", err)
	}
	must(s.AnswerInvite(ctx, anna, "IV", true))
	if pa, err := s.ProjectByKey(ctx, anna, key); err != nil || pa.MyRole != "viewer" {
		t.Fatalf("after accepting: %+v %v", pa, err)
	}
	if err := s.AnswerInvite(ctx, anna, "IV", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("accepted twice: %v", err)
	}

	must(s.SetInvitePerm(ctx, p, "members"))
	if err := s.InviteMember(ctx, pm, member, petr, "manager"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member invited a manager: %v", err)
	}
	must(s.InviteMember(ctx, pm, member, petr, "member"))
	must(s.AnswerInvite(ctx, petr, "iv", false))
	if _, err := s.ProjectByKey(ctx, petr, key); !errors.Is(err, ErrForbidden) {
		t.Fatalf("declined but joined: %v", err)
	}
	if inv, _ := s.ProjectInvites(ctx, p.ID); len(inv) != 0 {
		t.Fatalf("answered invitations stay: %+v", inv)
	}
}
