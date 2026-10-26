package tasks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"go24/internal/i18n"
)

// TestCollabSettings: the settings of a collab hold on the server — who invites members and guests, what guests may
// do; taking a person out takes their access; the archive is read-only; deleting is the owner's; all of it is logged.
func TestCollabSettings(t *testing.T) {
	pool, s := freshDB(t)
	bg := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(bg, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@test', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, mod, dev, client := newUser("owner", false), newUser("mod", false), newUser("dev", false), newUser("client", true)
	newbie, guest2, outsider := newUser("newbie", false), newUser("guest2", true), newUser("outsider", false)
	as := func(u string) context.Context {
		g, _ := s.IsGuest(bg, u)
		return WithViewer(bg, u, g)
	}
	collab := func(u string) Project {
		t.Helper()
		p, err := s.CollabByKey(as(u), u, "CL1")
		must(err)
		return p
	}
	var ve *i18n.Error
	isErr := func(err error, key string) bool { return errors.As(err, &ve) && ve.Key == key }

	key, err := s.CreateCollab(as(owner), owner, "Альфа", "", []string{dev, client})
	must(err)
	if key != "CL1" {
		t.Fatal(key)
	}
	must(s.SetMember(as(owner), collab(owner), mod, "manager"))

	// the people fields of a collab's task offer its members only (guests too); a stranger to the collab gets nobody
	names := func(viewer string) string {
		t.Helper()
		list, err := s.People(as(viewer), viewer, PeopleQuery{Collab: "CL1", Self: true})
		must(err)
		var out []string
		for _, p := range list {
			out = append(out, p.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names(owner); got != "client,dev,mod,owner" {
		t.Fatalf("people of the collab for the owner: %s", got)
	}
	if got := names(client); got != "client,dev,mod,owner" {
		t.Fatalf("people of the collab for the guest: %s", got)
	}
	if got := names(outsider); got != "" {
		t.Fatalf("people of someone else's collab: %s", got)
	}

	// the defaults: moderators invite, chat history shown, guests make tasks and upload files
	set, err := s.CollabSettings(bg, collab(owner))
	must(err)
	if set != (CollabSettings{InvitePerm: "managers", GuestInvitePerm: "managers", ChatHistory: true, GuestTasks: true, GuestFiles: true,
		EditPerm: "managers", ChatPerm: "members"}) {
		t.Fatalf("defaults: %+v", set)
	}
	// a member and a guest change nothing; a bad value is refused
	for _, u := range []string{dev, client} {
		if err := s.SetCollabSettings(as(u), collab(u), u, set); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s changed the settings: %v", u, err)
		}
	}
	if err := s.SetCollabSettings(as(owner), collab(owner), owner, CollabSettings{InvitePerm: "all", GuestInvitePerm: "owner"}); !errors.As(err, &ve) {
		t.Fatalf("a bad setting: %v", err)
	}

	// «кто может приглашать»: by default a member adds nobody; with «все участники» — an employee, not a moderator
	if err := s.SetMember(as(dev), collab(dev), newbie, "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member added someone: %v", err)
	}
	set.InvitePerm, set.GuestInvitePerm = "members", "owner"
	must(s.SetCollabSettings(as(mod), collab(mod), mod, set))
	if err := s.SetMember(as(dev), collab(dev), newbie, "manager"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member made a moderator: %v", err)
	}
	must(s.SetMember(as(dev), collab(dev), newbie, "member"))
	if err := s.SetMember(as(dev), collab(dev), newbie, "manager"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member changed a role: %v", err)
	}
	// a guest invites nobody, whatever the setting
	if err := s.SetMember(as(client), collab(client), outsider, "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a guest added someone: %v", err)
	}
	// «кто может приглашать гостей» = the owner: neither a moderator nor a member brings a guest — not by the picker,
	// not by a link
	for _, u := range []string{mod, dev} {
		if err := s.SetMember(as(u), collab(u), guest2, "member"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s added a guest: %v", u, err)
		}
		if _, err := s.CreateGuestInvite(as(u), collab(u), u, "link", "", "", 0); !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s made a guest link: %v", u, err)
		}
		if s.CanInviteGuests(as(u), collab(u), u) {
			t.Fatalf("%s may invite guests", u)
		}
	}
	if !s.CanAddMembers(as(dev), collab(dev)) || s.CanAddMembers(as(client), collab(client)) {
		t.Fatal("CanAddMembers")
	}
	must(s.SetMember(as(owner), collab(owner), guest2, "member"))

	// what guests may do
	tkey, err := s.CreateTask(as(client), client, collab(client), NewTask{Title: "От гостя"})
	must(err)
	tid, err := s.TaskIDByKey(bg, tkey)
	must(err)
	set.GuestTasks, set.GuestFiles = false, false
	must(s.SetCollabSettings(as(owner), collab(owner), owner, set))
	if _, err := s.CreateTask(as(client), client, collab(client), NewTask{Title: "Ещё"}); !isErr(err, "err.collab_guest_no_tasks") {
		t.Fatalf("a guest made a task where guests make none: %v", err)
	}
	if _, err := s.CreateTask(as(dev), dev, collab(dev), NewTask{Title: "От сотрудника"}); err != nil {
		t.Fatalf("an employee: %v", err)
	}
	if err := s.CanAttach(bg, client, tid); !isErr(err, "err.collab_guest_no_files") {
		t.Fatalf("a guest attaches where guests upload nothing: %v", err)
	}
	if err := s.CanAttach(bg, client, ""); err == nil {
		t.Fatal("a guest's file without a task")
	}
	must(s.CanAttach(bg, dev, tid))
	must(s.CanAttach(bg, dev, ""))
	set.GuestFiles = true
	must(s.SetCollabSettings(as(owner), collab(owner), owner, set))
	must(s.CanAttach(bg, client, tid))

	// taking a moderator out: only the owner; their links stop working, their tasks and meetings pass to the owner
	set.GuestInvitePerm = "managers"
	must(s.SetCollabSettings(as(owner), collab(owner), owner, set))
	inv, err := s.CreateGuestInvite(as(mod), collab(mod), mod, "link", "", "", 0)
	must(err)
	mkey, err := s.CreateTask(as(mod), mod, collab(mod), NewTask{Title: "Модератора", Auditors: []string{dev}})
	must(err)
	mid, _ := s.TaskIDByKey(bg, mkey)
	must(s.SetMember(as(owner), collab(owner), dev, "manager"))
	if err := s.RemoveMember(as(dev), collab(dev), mod); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator took out a moderator: %v", err)
	}
	var evID string
	must(pool.QueryRow(bg, `INSERT INTO events (owner_id, project_id, title, starts_at, ends_at) VALUES ($1, $2, 'Созвон', now() + interval '1 day',
		now() + interval '1 day 1 hour') RETURNING id`, mod, collab(owner).ID).Scan(&evID))
	_, err = pool.Exec(bg, `INSERT INTO event_attendees (event_id, user_id) VALUES ($1, $2)`, evID, mod)
	must(err)
	must(s.RemoveMember(as(owner), collab(owner), mod))
	if _, err := s.CollabByKey(as(mod), mod, "CL1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the removed moderator still sees the collab: %v", err)
	}
	if _, err := s.GuestInviteByToken(bg, inv.Token); !isErr(err, "err.guest_invite_gone") {
		t.Fatalf("the removed moderator's link still works: %v", err)
	}
	if _, err := s.GetCard(as(mod), mod, mid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the removed moderator opens the collab's task: %v", err)
	}
	var resp, evOwner string
	var attends bool
	must(pool.QueryRow(bg, `SELECT responsible_id FROM tasks WHERE id = $1`, mid).Scan(&resp))
	must(pool.QueryRow(bg, `SELECT owner_id, EXISTS (SELECT 1 FROM event_attendees WHERE event_id = $1 AND user_id = $2) FROM events WHERE id = $1`,
		evID, mod).Scan(&evOwner, &attends))
	if resp != owner || evOwner != owner || attends {
		t.Fatalf("after removal: responsible %s, meeting of %s, attends %v", resp, evOwner, attends)
	}
	// a moderator takes out a guest
	must(s.RemoveMember(as(dev), collab(dev), guest2))

	// the archive: hidden from the list, read-only, restored
	if err := s.SetArchived(as(client), collab(client), true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a guest archived the collab: %v", err)
	}
	must(s.SetArchived(as(owner), collab(owner), true))
	if l, _ := s.ListCollabs(bg, dev, false, ""); len(l) != 0 {
		t.Fatalf("an archived collab in the list: %+v", l)
	}
	if l, _ := s.ListCollabs(bg, dev, true, ""); len(l) != 1 || !l[0].Archived {
		t.Fatalf("the archive: %+v", l)
	}
	if !s.CollabArchived(bg, dev, tkey) || !s.CollabArchived(bg, client, "CL1") || s.CollabArchived(bg, outsider, "CL1") {
		t.Fatal("CollabArchived")
	}
	card, err := s.GetCard(as(client), client, tid)
	must(err)
	if !card.Frozen || card.CanEdit() || card.CanWork() || card.CanDelete || len(card.Actions) != 0 {
		t.Fatalf("an archived collab's task may be changed: %+v %+v", card.Roles, card.Actions)
	}
	if err := s.ApplyAction(as(client), client, tid, ActionStart); err == nil {
		t.Fatal("a task of the archive started")
	}
	if _, err := s.CreateTask(as(owner), owner, collab(owner), NewTask{Title: "В архив"}); err == nil {
		t.Fatal("a task made in the archive")
	}
	if err := s.SetMember(as(owner), collab(owner), newbie, "manager"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a role changed in the archive: %v", err)
	}
	if err := s.SetCollabSettings(as(owner), collab(owner), owner, set); !errors.Is(err, ErrForbidden) {
		t.Fatalf("settings changed in the archive: %v", err)
	}
	must(s.SetArchived(as(owner), collab(owner), false))
	if card, _ := s.GetCard(as(client), client, tid); card.Frozen || !card.CanEdit() {
		t.Fatal("the restored collab's task stays read-only")
	}

	// the log
	log, err := s.CollabLog(bg, collab(owner), 100)
	must(err)
	var actions []string
	for _, e := range log {
		actions = append(actions, e.Action)
	}
	for _, a := range []string{"created", "member_added", "role_changed", "settings", "invite_created", "member_removed", "archived", "restored"} {
		if !slices.Contains(actions, a) {
			t.Errorf("the log has no %q: %v", a, actions)
		}
	}
	if log[0].Action != "restored" || log[0].Actor != "owner" {
		t.Errorf("the newest line: %+v", log[0])
	}
	for _, e := range log {
		if e.Action == "member_removed" && e.Target == "guest2" && (!e.TargetGuest || e.Actor != "dev") {
			t.Errorf("the guest's removal: %+v", e)
		}
		if e.Action == "settings" && len(e.Changes()) == 0 {
			t.Errorf("a settings line without changes: %+v", e)
		}
	}
	if _, err := s.CollabLog(bg, collab(newbie), 10); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member reads the log: %v", err)
	}

	// deleting: the owner only (not a moderator), with the name typed again; the tasks and files go
	_, err = pool.Exec(bg, `INSERT INTO files (object_key, name, content_type, size, task_id, uploaded_by) VALUES ('k1', 'a.txt', 'text/plain', 1, $1, $2)`, tid, client)
	must(err)
	if err := s.DeleteCollab(as(dev), collab(dev), dev, "Альфа"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator deleted the collab: %v", err)
	}
	if err := s.DeleteCollab(as(owner), collab(owner), owner, "Альф"); !isErr(err, "err.collab_delete_confirm") {
		t.Fatalf("deleted without the name: %v", err)
	}
	must(s.DeleteCollab(as(owner), collab(owner), owner, " Альфа "))
	if _, err := s.CollabByKey(bg, owner, "CL1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted collab: %v", err)
	}
	var left int
	must(pool.QueryRow(bg, `SELECT (SELECT count(*) FROM tasks_all WHERE id = $1) + (SELECT count(*) FROM files WHERE object_key = 'k1')
		+ (SELECT count(*) FROM collab_log)`, tid).Scan(&left))
	if left != 0 {
		t.Fatalf("the deleted collab left %d rows", left)
	}
	// an administrator deletes any collab
	k2, err := s.CreateCollab(as(owner), owner, "Бета", "", nil)
	must(err)
	adm := WithAdmin(as(outsider))
	p2, err := s.CollabByKey(adm, outsider, k2)
	must(err)
	must(s.DeleteCollab(adm, p2, outsider, "Бета"))
}

// TestTransferCollab: the owner hands the collab over to an employee of it and stays a moderator; a guest, a stranger
// to the collab, a moderator's attempt are refused; the new owner is told, the log says it, the old owner may leave.
func TestTransferCollab(t *testing.T) {
	pool, s := freshDB(t)
	bg := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(bg, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@test', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, dev, client, outsider := newUser("owner", false), newUser("dev", false), newUser("client", true), newUser("outsider", false)
	as := func(u string) context.Context {
		g, _ := s.IsGuest(bg, u)
		return WithViewer(bg, u, g)
	}
	collab := func(u string) Project {
		t.Helper()
		p, err := s.CollabByKey(as(u), u, "CL1")
		must(err)
		return p
	}
	var ve *i18n.Error
	isErr := func(err error, key string) bool { return errors.As(err, &ve) && ve.Key == key }

	_, err := s.CreateCollab(as(owner), owner, "Альфа", "", []string{dev, client})
	must(err)
	if err := s.TransferCollab(as(dev), collab(dev), dev, dev); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member takes the collab: %v", err)
	}
	if err := s.TransferCollab(as(owner), collab(owner), owner, client); !isErr(err, "err.collab_owner_guest") {
		t.Fatalf("to a guest: %v", err)
	}
	if err := s.TransferCollab(as(owner), collab(owner), owner, outsider); !isErr(err, "err.collab_owner_member") {
		t.Fatalf("to a stranger: %v", err)
	}
	must(s.TransferCollab(as(owner), collab(owner), owner, dev))
	if r := collab(owner).MyRole; r != "manager" {
		t.Fatalf("the former owner: %s", r)
	}
	if r := collab(dev).MyRole; r != "owner" {
		t.Fatalf("the new owner: %s", r)
	}
	var ownerID string
	must(pool.QueryRow(bg, `SELECT owner_id::text FROM projects WHERE key = 'CL1'`).Scan(&ownerID))
	if ownerID != dev {
		t.Fatal("projects.owner_id stays the former owner")
	}
	var n int
	must(pool.QueryRow(bg, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'collab_owner' AND url = '/p/CL1'`, dev).Scan(&n))
	if n != 1 {
		t.Fatalf("the new owner told %d times", n)
	}
	log, err := s.CollabLog(as(dev), collab(dev), 10)
	must(err)
	if log[0].Action != "owner_changed" || log[0].Target != "dev" {
		t.Fatalf("log: %+v", log[0])
	}
	// the former owner, now a moderator, takes nothing back and may leave
	if err := s.TransferCollab(as(owner), collab(owner), owner, owner); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator takes the collab back: %v", err)
	}
	must(s.LeaveProject(as(owner), collab(owner), owner))
	if err := s.LeaveProject(as(dev), collab(dev), dev); !isErr(err, "err.owner_cannot_leave") {
		t.Fatalf("the new owner leaves: %v", err)
	}
}

// TestCollabEditPerm: «кто может изменять настройки» — with «только владелец» a moderator changes neither the
// settings nor the name or the avatar; only the owner (or an administrator) chooses it; the log says it.
func TestCollabEditPerm(t *testing.T) {
	pool, s := freshDB(t)
	bg := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(bg, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	owner, mod := newUser("owner"), newUser("mod")
	collab := func(u string) Project {
		t.Helper()
		p, err := s.CollabByKey(WithViewer(bg, u, false), u, "CL1")
		must(err)
		return p
	}
	_, err := s.CreateCollab(WithViewer(bg, owner, false), owner, "Альфа", "", nil)
	must(err)
	must(s.SetMember(WithViewer(bg, owner, false), collab(owner), mod, "manager"))

	// by default moderators change the settings, but not who changes them
	set, _ := s.CollabSettings(bg, collab(mod))
	if !collab(mod).CanEdit() {
		t.Fatal("a moderator cannot edit by default")
	}
	set.EditPerm, set.ChatPerm = "owner", "managers"
	must(s.SetCollabSettings(WithViewer(bg, mod, false), collab(mod), mod, set))
	if got, _ := s.CollabSettings(bg, collab(owner)); got.EditPerm != "managers" || got.ChatPerm != "managers" {
		t.Fatalf("a moderator chose who edits: %+v", got)
	}
	// the owner makes it theirs: the moderator is out of the settings, the name, the avatar
	must(s.SetCollabSettings(WithViewer(bg, owner, false), collab(owner), owner, set))
	p := collab(mod)
	if p.CanEdit() || !p.CanManage() {
		t.Fatalf("a moderator under «только владелец»: edit %v manage %v", p.CanEdit(), p.CanManage())
	}
	if err := s.SetCollabSettings(WithViewer(bg, mod, false), p, mod, set); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator changed the settings: %v", err)
	}
	if err := s.SaveProjectInfo(WithViewer(bg, mod, false), p, "Бета", "", nil, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator renamed the collab: %v", err)
	}
	if _, err := s.SetProjectPhoto(bg, p, "k", "image/png"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator changed the avatar: %v", err)
	}
	must(s.SaveProjectInfo(WithViewer(bg, owner, false), collab(owner), "Бета", "", nil, nil))
	set.ChatPerm = "bogus"
	if err := s.SetCollabSettings(WithViewer(bg, owner, false), collab(owner), owner, set); err == nil {
		t.Fatal("a bad «кто может писать в чат» taken")
	}
	log, err := s.CollabLog(bg, collab(owner), 10)
	must(err)
	var changes []string
	for _, e := range log {
		changes = append(changes, e.Changes()...)
	}
	if !slices.Contains(changes, "edit_perm.owner") || !slices.Contains(changes, "chat_perm.managers") {
		t.Fatalf("the log of the rights: %v", changes)
	}
}
