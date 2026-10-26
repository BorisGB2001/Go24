package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestCollabCounters: the counters of a collab's card and of «Коллабы» in the menu — the person's overdue tasks and
// the tasks with comments they have not seen, of their active collabs only.
func TestCollabCounters(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@test', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, client, outsider := newUser("owner", false), newUser("client", true), newUser("outsider", false)
	key, err := s.CreateCollab(ctx, owner, "Сайт", "", nil)
	must(err)
	c, err := s.CollabByKey(ctx, owner, key)
	must(err)
	must(s.SetMember(ctx, c, client, "member"))
	past := time.Now().Add(-48 * time.Hour)
	late, err := s.CreateTask(ctx, owner, c, NewTask{Title: "Просрочена", ResponsibleID: client, Deadline: &past})
	must(err)
	_, err = s.CreateTask(ctx, owner, c, NewTask{Title: "Без срока", ResponsibleID: client})
	must(err)
	_, err = s.CreateTask(ctx, owner, c, NewTask{Title: "Чужая просрочена", ResponsibleID: owner, Deadline: &past})
	must(err)
	lateID, _ := s.TaskIDByKey(ctx, late)
	must(s.AddComment(ctx, owner, lateID, "Когда будет готово?"))
	// a project of the company counts nowhere here
	pkey, err := s.CreateProject(ctx, owner, "PRJ", "Проект", "")
	must(err)
	p, _ := s.ProjectByKey(ctx, owner, pkey)
	must(s.SetMember(ctx, p, outsider, "member"))
	_, err = s.CreateTask(ctx, owner, p, NewTask{Title: "Проектная", ResponsibleID: owner, Deadline: &past})
	must(err)

	list, err := s.ListCollabs(ctx, client, false, "")
	must(err)
	if len(list) != 1 || list[0].Overdue != 1 || list[0].Comments != 1 {
		t.Fatalf("the guest's card = %+v", list)
	}
	// the header of the collab's chat: the same card, for its members only, of a collab only
	card, err := s.CollabCard(ctx, client, c.ID)
	must(err)
	if card.Key != key || card.Members != 2 || card.Guests != 1 || card.Overdue != 1 || card.Comments != 1 {
		t.Fatalf("the guest's chat header = %+v", card)
	}
	if _, err := s.CollabCard(ctx, outsider, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an outsider sees another's collab card: %v", err)
	}
	if _, err := s.CollabCard(ctx, owner, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a project as a collab card: %v", err)
	}
	sum, ids, err := s.CollabNav(ctx, client)
	must(err)
	if sum != (CollabCounters{Overdue: 1, Comments: 1}) || len(ids) != 1 || ids[0] != c.ID {
		t.Fatalf("the guest's menu = %+v %v", sum, ids)
	}
	sum, _, err = s.CollabNav(ctx, owner)
	must(err)
	if sum != (CollabCounters{Overdue: 1}) {
		t.Fatalf("the owner's menu = %+v", sum)
	}
	if sum, ids, _ := s.CollabNav(ctx, outsider); sum != (CollabCounters{}) || len(ids) != 0 {
		t.Fatalf("the outsider's menu = %+v %v", sum, ids)
	}
	// the archive counts no more
	must(s.SetArchived(ctx, c, true))
	if sum, ids, _ := s.CollabNav(ctx, client); sum != (CollabCounters{}) || len(ids) != 0 {
		t.Fatalf("an archived collab in the menu = %+v %v", sum, ids)
	}
}
