package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestCollabs: making a collab, its roles and guests, and above all that nobody outside learns anything of it —
// an employee outside it, a member of another collab, a guest of another collab.
func TestCollabs(t *testing.T) {
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
	owner, dev, client := newUser("owner", false), newUser("dev", false), newUser("client", true)
	outsider, other, otherGuest := newUser("outsider", false), newUser("other", false), newUser("other-guest", true)

	var ve *i18n.Error
	if _, err := s.CreateCollab(ctx, owner, "  ", "", nil); !errors.As(err, &ve) {
		t.Fatalf("a collab without a name: %v", err)
	}
	if _, err := s.CreateCollab(ctx, client, "Гостевой", "", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a guest made a collab: %v", err)
	}
	// a project took the key go24 would give next: the collab gets the following one
	_, err := s.CreateProject(ctx, outsider, "CL1", "Чужой проект", "")
	must(err)
	key, err := s.CreateCollab(ctx, owner, " Сайт для клиента ", "Редизайн", []string{dev, owner})
	must(err)
	if key != "CL2" {
		t.Fatalf("key = %s", key)
	}
	c, err := s.CollabByKey(ctx, owner, key)
	must(err)
	if !c.Collab() || c.Name != "Сайт для клиента" || c.MyRole != "owner" || c.Visibility != "members" {
		t.Fatalf("collab = %+v", c)
	}
	if _, err := s.CollabByKey(ctx, outsider, "CL1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a project opened as a collab: %v", err)
	}
	// roles: moderators and members; no «наблюдатель»; a guest is only a member, and never of a company project
	if err := s.SetMember(ctx, c, client, "viewer"); !errors.As(err, &ve) {
		t.Fatalf("a viewer in a collab: %v", err)
	}
	if err := s.SetMember(ctx, c, client, "manager"); !errors.As(err, &ve) || ve.Key != "err.guest_member_only" {
		t.Fatalf("a guest moderator: %v", err)
	}
	must(s.SetMember(ctx, c, client, "member"))
	must(s.SetMember(ctx, c, dev, "manager"))
	cl1, _ := s.ProjectByKey(ctx, outsider, "CL1")
	if err := s.SetMember(ctx, cl1, client, "member"); !errors.As(err, &ve) || ve.Key != "err.guest_collab_only" {
		t.Fatalf("a guest in a company project: %v", err)
	}
	if err := s.InviteMember(ctx, cl1, outsider, client, "member"); !errors.As(err, &ve) || ve.Key != "err.guest_collab_only" {
		t.Fatalf("a guest invited into a company project: %v", err)
	}
	if err := s.SetProjectKind(ctx, c, "group"); !errors.As(err, &ve) {
		t.Fatalf("a collab turned into a group: %v", err)
	}
	ms, err := s.Members(ctx, c.ID)
	must(err)
	if len(ms) != 3 || ms[0].Role != "owner" || ms[1].Role != "manager" || ms[2].ID != client || !ms[2].Guest || ms[1].Guest {
		t.Fatalf("members = %+v", ms)
	}
	list, err := s.ListCollabs(ctx, client, false, "")
	must(err)
	if len(list) != 1 || list[0].Key != key || list[0].Members != 3 || list[0].Guests != 1 || list[0].MyRole != "member" {
		t.Fatalf("the guest's collabs = %+v", list)
	}
	// the search: by the name or the description, any case; % and _ are plain characters
	for q, n := range map[string]int{"сайт": 1, "РЕДИЗАЙН": 1, "  клиента ": 1, "Бухгалтерия": 0, "%": 0, "_": 0} {
		if list, err := s.ListCollabs(ctx, client, false, q); err != nil || len(list) != n {
			t.Errorf("search %q = %d collabs, %v; want %d", q, len(list), err, n)
		}
	}
	// the guest works in the collab: sees its tasks, takes one
	cg, err := s.CollabByKey(ctx, client, key)
	must(err)
	tkey, err := s.CreateTask(ctx, dev, c, NewTask{Title: "Макет главной", ResponsibleID: client})
	must(err)
	tid, _ := s.TaskIDByKey(ctx, tkey)
	if _, err := s.GetCard(ctx, client, tid); err != nil {
		t.Fatalf("the guest's task: %v", err)
	}
	_, err = s.CreateTask(ctx, client, cg, NewTask{Title: "Вопрос по макету"})
	must(err)
	// the people of a collab's task are its members: nobody from outside is drawn in
	if _, err := s.CreateTask(ctx, dev, c, NewTask{Title: "x", ResponsibleID: outsider}); !errors.As(err, &ve) || ve.Key != "err.collab_task_members" {
		t.Fatalf("an outsider made responsible: %v", err)
	}
	if _, err := s.CreateTask(ctx, dev, c, NewTask{Title: "x", Auditors: []string{otherGuest}}); !errors.As(err, &ve) || ve.Key != "err.collab_task_members" {
		t.Fatalf("a guest of another collab made an auditor: %v", err)
	}
	if err := s.SetParticipant(ctx, owner, tid, outsider, "auditor", true); !errors.As(err, &ve) || ve.Key != "err.collab_task_members" {
		t.Fatalf("an outsider added as an auditor: %v", err)
	}
	// «+» with no one picked, or no such person: a plain error, not a broken query
	for _, who := range []string{"", "00000000-0000-0000-0000-000000000000"} {
		for _, role := range []string{"accomplice", "auditor"} {
			if err := s.SetParticipant(ctx, owner, tid, who, role, true); !errors.As(err, &ve) || ve.Key != "err.pick_person" {
				t.Fatalf("%s %q: %v", role, who, err)
			}
		}
	}
	card0, _ := s.GetCard(ctx, owner, tid)
	if err := s.SetResponsible(ctx, owner, tid, "00000000-0000-0000-0000-000000000000"); !errors.As(err, &ve) || ve.Key != "err.pick_person" {
		t.Fatalf("no such responsible: %v", err)
	}
	if err := s.SetResponsible(ctx, owner, tid, ""); err != nil { // an empty field keeps the responsible
		t.Fatalf("empty responsible: %v", err)
	}
	if c, _ := s.GetCard(ctx, owner, tid); c.ResponsibleID != card0.ResponsibleID {
		t.Fatalf("responsible changed to %q", c.ResponsibleID)
	}
	if err := s.SetParticipant(ctx, owner, tid, "", "auditor", false); !errors.As(err, &ve) || ve.Key != "err.pick_person" {
		t.Fatalf("removing no one: %v", err)
	}
	card, _ := s.GetCard(ctx, owner, tid)
	if err := s.UpdateTask(ctx, owner, tid, TaskUpdate{Version: card.Version, Title: card.Title, ResponsibleID: outsider, Priority: card.Priority}); !errors.As(err, &ve) || ve.Key != "err.collab_task_members" {
		t.Fatalf("the task handed to an outsider: %v", err)
	}
	_, err = s.SetProjectPhoto(ctx, c, "go24/projects/c.png", "image/png")
	must(err)

	// another collab with its own member and guest
	okey, err := s.CreateCollab(ctx, other, "Подрядчик", "", nil)
	must(err)
	oc, _ := s.CollabByKey(ctx, other, okey)
	must(s.SetMember(ctx, oc, otherGuest, "member"))

	// nobody outside learns a thing: not in lists, not by key, not its tasks, not its avatar
	for name, u := range map[string]string{"outsider": outsider, "member of another collab": other, "guest of another collab": otherGuest} {
		if _, err := s.ProjectByKey(ctx, u, key); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s opened the collab: %v", name, err)
		}
		if list, _ := s.ListCollabs(ctx, u, false, ""); len(list) > 1 || (len(list) == 1 && list[0].Key == key) {
			t.Errorf("%s's collabs = %+v", name, list)
		}
		if list, _ := s.ListProjects(ctx, u); len(list) > 0 && list[0].Key == key {
			t.Errorf("%s's projects = %+v", name, list)
		}
		if _, err := s.GetCard(ctx, u, tid); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s opened a task of the collab: %v", name, err)
		}
		if err := s.CheckVisible(ctx, u, tid); err == nil {
			t.Errorf("%s may see a task of the collab", name)
		}
		if hits, _ := s.Search(ctx, u, "макет"); len(hits) != 0 {
			t.Errorf("%s found the collab's tasks: %+v", name, hits)
		}
		if list, _ := s.ListTasks(ctx, Filter{Viewer: u}); len(list) != 0 {
			t.Errorf("%s lists the collab's tasks: %+v", name, list)
		}
		if list, _ := s.ListTasks(ctx, Filter{Viewer: u, ProjectID: c.ID}); len(list) != 0 {
			t.Errorf("%s lists the collab's tasks by its id: %+v", name, list)
		}
		if _, _, err := s.ProjectPhoto(ctx, u, key); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s got the collab's avatar: %v", name, err)
		}
		if evs, _ := s.Events(ctx, u, time.Now().Add(time.Hour), 50); len(evs) != 0 {
			t.Errorf("%s sees the collab's events: %+v", name, evs)
		}
	}
	// the guest of the collab does not see the other collab either
	if _, err := s.CollabByKey(ctx, client, okey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the guest opened another collab: %v", err)
	}
	if k, _, err := s.ProjectPhoto(ctx, client, key); err != nil || k == "" {
		t.Fatalf("a member's view of the avatar: %v", err)
	}
	// a member leaves; the owner cannot
	must(s.LeaveProject(ctx, cg, client))
	if _, err := s.CollabByKey(ctx, client, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the guest still in the collab after leaving: %v", err)
	}
	// …and keeps none of its tasks, not even the one they are responsible for
	if _, err := s.GetCard(ctx, client, tid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a task of the collab after leaving it: %v", err)
	}
	if list, _ := s.ListTasks(ctx, Filter{Viewer: client}); len(list) != 0 {
		t.Fatalf("tasks after leaving the collab: %+v", list)
	}
	if n, _ := s.Counters(ctx, client); n.Open != 0 {
		t.Fatalf("counters after leaving the collab: %+v", n)
	}
	if card, _ := s.GetCard(ctx, owner, tid); card.ResponsibleID != owner {
		t.Fatalf("the left guest's task went to %s, not to the owner", card.ResponsibleID)
	}
	if err := s.LeaveProject(ctx, c, owner); !errors.As(err, &ve) {
		t.Fatalf("the owner left: %v", err)
	}
}
