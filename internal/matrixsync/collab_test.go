package matrixsync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestCollabRoom: a collab's chat is a room of its members only, outside the company space; a guest is marked
// «(гость)» in it and is in no room of the company (space, feed); the messenger lists it as a collab.
func TestCollabRoom(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	newUser := func(username string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, username, guest) VALUES ('sub-' || $1, $1, $1 || '@x', $1, $2) RETURNING id`,
			username, guest).Scan(&id))
		return id
	}
	owner, client := newUser("owner", false), newUser("client", true)
	outsider := newUser("outsider", false)
	other, otherGuest := newUser("other", false), newUser("otherguest", true)

	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ts := tasks.NewStore(pool)
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: ts, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IDP: "oidc-authentik", Company: "ООО Ромашка", GuestMark: "гость"})

	key, err := ts.CreateCollab(ctx, owner, "Клиент", "Сайт", []string{client})
	must(err)
	c, _ := ts.CollabByKey(ctx, owner, key)
	okey, err := ts.CreateCollab(ctx, other, "Подрядчик", "", []string{otherGuest})
	must(err)
	oc, _ := ts.CollabByKey(ctx, other, okey)
	must(s.Reconcile(ctx))

	space, _ := s.roomOf(ctx, "space", nil)
	room, err := s.ProjectRoom(ctx, c.ID)
	must(err)
	oroom, _ := s.ProjectRoom(ctx, oc.ID)
	if room == "" || oroom == "" || fake.names[room] != "Клиент" {
		t.Fatalf("collab rooms: %q %q", room, oroom)
	}
	if m := fake.rooms[room]; len(m) != 3 || !m["@owner:t.lan"] || !m["@client:t.lan"] || m["@outsider:t.lan"] || m["@otherguest:t.lan"] {
		t.Fatalf("the collab room's people: %v", m)
	}
	// the company space and the company's rooms are the employees' only; the collab room is not in the space
	if m := fake.rooms[space]; m["@client:t.lan"] || m["@otherguest:t.lan"] || !m["@outsider:t.lan"] {
		t.Fatalf("the space's people: %v", m)
	}
	for _, st := range fake.state {
		if strings.HasPrefix(st.Path, space+"/state/m.space.child/") && (strings.HasSuffix(st.Path, room) || strings.HasSuffix(st.Path, oroom)) {
			t.Fatalf("a collab room linked into the company space: %s", st.Path)
		}
		if strings.HasPrefix(st.Path, room+"/state/m.space.parent/") {
			t.Fatalf("the collab room points to the company space: %s", st.Path)
		}
	}
	// the guest's name in the collab room carries the mark, set by the guest's own account
	marked := 0
	for _, st := range fake.state {
		if st.Path == room+"/state/m.room.member/@client:t.lan" {
			marked++
			if st.Sender != "@client:t.lan" || st.Content["displayname"] != "client (гость)" || st.Content["membership"] != "join" {
				t.Fatalf("the guest mark: %+v", st)
			}
		}
		if strings.Contains(st.Path, "/state/m.room.member/@owner:t.lan") {
			t.Fatalf("an employee marked as a guest: %+v", st)
		}
	}
	if marked != 1 {
		t.Fatalf("the guest marked %d times", marked)
	}
	must(s.Reconcile(ctx)) // once only: the mark is put when the guest joins
	n := 0
	for _, st := range fake.state {
		if st.Path == room+"/state/m.room.member/@client:t.lan" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the guest marked again: %d", n)
	}

	// the messenger: the collab's chat is on the collab tab; the guest sees no chat of another collab
	rooms, err := s.Rooms(ctx, client)
	must(err)
	kinds := map[string]string{}
	for _, r := range rooms {
		kinds[r.ID] = r.Kind
	}
	if kinds[room] != "collab" || len(rooms) != 1 {
		t.Fatalf("the guest's chats: %+v", rooms)
	}
	if _, _, err := s.Messages(ctx, client, oroom, ""); err == nil {
		t.Fatal("the guest read another collab's chat")
	}
	if _, err := s.Send(ctx, client, oroom, "привет", ""); err == nil {
		t.Fatal("the guest wrote into another collab's chat")
	}
	// a pin in go24's own chat: the people have no rights over its state in Matrix, the bot puts it for them
	hi, err := s.Send(ctx, client, room, "Вопрос по сайту", "")
	must(err)
	must(s.Pin(ctx, client, room, hi.ID, true))
	if pins, err := s.Pins(ctx, owner, room); err != nil || len(pins) != 1 || pins[0].ID != hi.ID {
		t.Fatalf("the pin in the collab's chat: %+v %v", pins, err)
	}
	if err := s.Pin(ctx, client, oroom, hi.ID, true); err == nil {
		t.Fatal("the guest pinned in another collab's chat")
	}
	// a task of a message: the message, the chat's name and its collab
	if m, chat, pid, err := s.One(ctx, owner, room, hi.ID); err != nil || m.Body != "Вопрос по сайту" || m.SenderID != client || chat == "" || pid != c.ID {
		t.Fatalf("a message for a task: %+v %q %q %v", m, chat, pid, err)
	}
	if _, _, _, err := s.One(ctx, otherGuest, room, hi.ID); err == nil {
		t.Fatal("another collab's guest took a message of this one")
	}
	// private chats of a guest: with the people of their collabs only, both ways; the same chat again next time
	dm, err := s.DirectChat(ctx, client, owner)
	must(err)
	if !fake.rooms[dm]["@owner:t.lan"] || !fake.rooms[dm]["@client:t.lan"] {
		t.Fatalf("the guest's private chat: %v", fake.rooms[dm])
	}
	if again, err := s.DirectChat(ctx, owner, client); err != nil || again != dm {
		t.Fatalf("the owner's chat with the guest: %q %v, want %q", again, err, dm)
	}
	for _, pair := range [][2]string{{client, outsider}, {client, otherGuest}, {outsider, client}, {otherGuest, client}} {
		if _, err := s.DirectChat(ctx, pair[0], pair[1]); !errors.Is(err, ErrNoAccount) {
			t.Fatalf("a private chat across collabs (%s → %s): %v", pair[0], pair[1], err)
		}
	}
	// files in the collab's chat: a guest sends them only where the collab lets guests upload
	if _, err := s.SendFile(ctx, client, room, "", "a.txt", "text/plain", strings.NewReader("x"), 1, nil); err != nil {
		t.Fatalf("the guest's file: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE projects SET guests_files = false WHERE id = $1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendFile(ctx, client, room, "", "b.txt", "text/plain", strings.NewReader("x"), 1, nil); err == nil {
		t.Fatal("the guest sent a file where guests upload nothing")
	}
	if _, err := s.SendFile(ctx, owner, room, "", "c.txt", "text/plain", strings.NewReader("x"), 1, nil); err != nil {
		t.Fatalf("the owner's file: %v", err)
	}
	// a member who leaves the collab leaves its chat
	cg, _ := ts.CollabByKey(ctx, client, key)
	must(ts.LeaveProject(ctx, cg, client))
	must(s.Reconcile(ctx))
	if fake.rooms[room]["@client:t.lan"] {
		t.Fatal("the guest stayed in the chat after leaving the collab")
	}

	// the settings on the room: history for new members, read-only in the archive — put once per change
	last := func(room, typ string) map[string]any {
		var c map[string]any
		n := 0
		for _, st := range fake.state {
			if st.Path == room+"/state/"+typ+"/" && st.Content != nil {
				c = st.Content
				n++
			}
		}
		if c != nil {
			c["_n"] = n
		}
		return c
	}
	if h := last(room, "m.room.history_visibility"); h["history_visibility"] != "shared" {
		t.Fatalf("history of a new collab: %v", h)
	}
	co, _ := ts.CollabByKey(tasks.WithViewer(ctx, owner, false), owner, key)
	set, _ := ts.CollabSettings(ctx, co)
	set.ChatHistory = false
	must(ts.SetCollabSettings(ctx, co, owner, set))
	must(ts.SetArchived(ctx, co, true))
	must(s.Reconcile(ctx))
	must(s.Reconcile(ctx))
	h, pl := last(room, "m.room.history_visibility"), last(room, "m.room.power_levels")
	if h["history_visibility"] != "joined" || h["_n"] != 2 {
		t.Fatalf("history hidden from new members: %v", h)
	}
	if pl["events_default"] != float64(100) || pl["_n"] != 2 {
		t.Fatalf("the archived collab's room is not read-only: %v", pl)
	}
	if !fake.rooms[room]["@owner:t.lan"] {
		t.Fatal("the archive emptied the collab's chat (its members read it)")
	}
	co.Archived = true
	must(ts.SetArchived(ctx, co, false))
	must(s.Reconcile(ctx))
	if pl := last(room, "m.room.power_levels"); pl["events_default"] != float64(0) {
		t.Fatalf("the restored collab's room stays read-only: %v", pl)
	}

	// the chat knows its collab (its header shows the collab's tasks, drive, calendar); a project's chat — none
	if s.RoomCollab(ctx, room) != co.ID || s.RoomCollab(ctx, "!nope:t.lan") != "" {
		t.Fatal("the collab of its chat")
	}

	// a member taken out leaves the chat; a deleted collab's chat is emptied
	co.Archived = false
	must(ts.SetMember(ctx, co, other, "member"))
	must(s.Reconcile(ctx))
	if !fake.rooms[room]["@other:t.lan"] {
		t.Fatal("the new member is not in the chat")
	}

	// «кто может писать в чат»: all but guests, then the owner and moderators — messages need the writers' level,
	// which only they have; go24 refuses the others at once; back to everyone, the level is gone
	must(ts.SetMember(ctx, co, client, "member"))
	writes := func(perm string) {
		t.Helper()
		set, _ := ts.CollabSettings(ctx, co)
		set.ChatPerm = perm
		must(ts.SetCollabSettings(ctx, co, owner, set))
		must(s.Reconcile(ctx))
	}
	levels := func() (float64, map[string]any) {
		pl := last(room, "m.room.power_levels")
		users, _ := pl["users"].(map[string]any)
		v, _ := pl["events_default"].(float64)
		return v, users
	}
	writes("employees")
	if v, users := levels(); v != writerLevel || users["@owner:t.lan"] != float64(writerLevel) || users["@other:t.lan"] != float64(writerLevel) ||
		users["@client:t.lan"] != nil {
		t.Fatalf("all but guests write: %v %v", v, users)
	}
	if why := s.CollabChatClosed(ctx, client, room); why != "employees" {
		t.Fatalf("the guest's chat closed: %q", why)
	}
	if _, err := s.Send(ctx, client, room, "можно?", ""); err == nil {
		t.Fatal("the guest wrote where guests do not")
	}
	if _, err := s.Send(ctx, other, room, "пишу", ""); err != nil {
		t.Fatalf("an employee where all but guests write: %v", err)
	}
	writes("managers")
	if v, users := levels(); v != writerLevel || users["@owner:t.lan"] != float64(writerLevel) || users["@other:t.lan"] != nil {
		t.Fatalf("the owner and moderators write: %v %v", v, users)
	}
	if why := s.CollabChatClosed(ctx, other, room); why != "managers" {
		t.Fatalf("a member's chat closed: %q", why)
	}
	if why := s.CollabChatClosed(ctx, owner, room); why != "" {
		t.Fatalf("the owner's chat closed: %q", why)
	}
	writes("members")
	if v, users := levels(); v != 0 || users["@owner:t.lan"] != nil {
		t.Fatalf("everyone writes again: %v %v", v, users)
	}
	if why := s.CollabChatClosed(ctx, client, room); why != "" {
		t.Fatalf("the guest's chat stays closed: %q", why)
	}

	// «Автоудаление сообщений» of the collab's settings: go24 puts it into the chat once, on behalf of the one who chose
	// it — the chat's line names them, the header shows it to all, nobody changes it in the messenger; the messages
	// go when their time is up (as their authors or go24); turned off, it is gone from the chat and the worker's index
	set, _ = ts.CollabSettings(ctx, co)
	if set.MsgTTL = 1234; ts.SetCollabSettings(ctx, co, owner, set) == nil {
		t.Fatal("a lifetime not offered")
	}
	set.MsgTTL = 86400
	must(ts.SetCollabSettings(ctx, co, owner, set))
	must(s.Reconcile(ctx))
	must(s.Reconcile(ctx))
	if a := last(room, "go24.autodelete"); a["ttl"] != float64(86400) || a["by"] != "@owner:t.lan" || a["_n"] != 1 {
		t.Fatalf("the collab chat's auto-delete: %v", a)
	}
	if ttl, can, err := s.AutoDelete(ctx, client, room); ttl != 86400 || can || err != nil {
		t.Fatalf("the chat's auto-delete for a member: %d %t %v", ttl, can, err)
	}
	msgs, _, err := s.Messages(ctx, client, room, "")
	must(err)
	if l := msgs[len(msgs)-1]; l.Event != "autodelete" || l.Body != "86400" || l.SenderName != "owner" {
		t.Fatalf("the chat's line about auto-delete: %+v", l)
	}
	bye, err := s.Send(ctx, client, room, "удалится через день", "")
	must(err)
	if _, err := pool.Exec(ctx, `UPDATE im_autodelete SET since = to_timestamp(0)`); err != nil {
		t.Fatal(err)
	}
	gone := len(fake.redacted)
	must(s.expire(ctx, time.UnixMilli(86400_000+5000)))
	if !slices.Contains(fake.redacted[gone:], bye.ID) {
		t.Fatalf("the collab chat's message not deleted: %v", fake.redacted[gone:])
	}
	set.MsgTTL = 0
	must(ts.SetCollabSettings(ctx, co, owner, set))
	must(s.Reconcile(ctx))
	n = 0
	must(pool.QueryRow(ctx, `SELECT count(*) FROM im_autodelete`).Scan(&n))
	if a := last(room, "go24.autodelete"); a["ttl"] != float64(0) || a["_n"] != 2 || n != 0 {
		t.Fatalf("the collab chat's auto-delete not off: %v %d", a, n)
	}
	log, err := ts.CollabLog(ctx, co, 10)
	must(err)
	if c := log[0].Changes(); len(c) != 1 || c[0] != "msg_ttl.0" {
		t.Fatalf("the log of auto-delete: %v", c)
	}

	must(ts.RemoveMember(ctx, co, other))
	must(s.Reconcile(ctx))
	if fake.rooms[room]["@other:t.lan"] {
		t.Fatal("the removed member stayed in the chat")
	}
	must(ts.DeleteCollab(ctx, co, owner, co.Name))
	must(s.Reconcile(ctx))
	if len(fake.rooms[room]) != 1 || !fake.rooms[room]["@go24:t.lan"] {
		t.Fatalf("the deleted collab's chat: %v", fake.rooms[room])
	}
	if r, _ := s.ProjectRoom(ctx, co.ID); r != "" {
		t.Fatal("the deleted collab keeps its room")
	}
}
