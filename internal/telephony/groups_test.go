package telephony

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRenderGroups: a group is a queue of its employees (with a number) with its strategy and music; its context
// waits in the queue, then goes back to the line's «nobody answered» or rates the conversation; lines and menus send
// calls there.
func TestRenderGroups(t *testing.T) {
	sales := Group{ID: "11111111-2222-3333-4444-555555555555", Name: "Продажи; [все]", Users: []string{"u1", "gone", "u2"}, Strategy: "evenly",
		Ring: 15, Wait: 90, MusicSum: "m1"}
	quiet := Group{ID: "22222222-2222-3333-4444-555555555555", Name: "Тихие", Users: []string{"u1"}, Strategy: "all", Ring: 20, Wait: 60}
	q, m, d := RenderGroups([]Group{sales, quiet}, map[string]string{"u1": "101", "u2": "102"})
	for _, want := range []string{
		"; Продажи все\n[go24-q-111111112222]\nstrategy = rrmemory\ntimeout = 15\nretry = 3\n",
		"musicclass = go24-q-111111112222\nmember => Local/101@go24-ring/n,0,101\nmember => Local/102@go24-ring/n,0,102\n",
		"[go24-q-222222222222]\nstrategy = ringall\ntimeout = 20\n",
	} {
		if !strings.Contains(q, want) {
			t.Fatalf("no %q in\n%s", want, q)
		}
	}
	if strings.Count(q, "musicclass") != 1 || strings.Contains(q, "gone") {
		t.Fatalf("queues:\n%s", q)
	}
	if m != "; go24 writes this file (Settings → Telephony → Queue groups): do not edit it by hand\n\n[go24-q-111111112222]\nmode = playlist\nentry = "+AstDir+"/greet-m1\n" {
		t.Fatalf("music:\n%s", m)
	}
	for _, want := range []string{
		"[go24-q-111111112222]\nexten => s,1,Set(GO24_BY=101)\n same => n,Set(__GO24_QCALLER=${CHANNEL})\n same => n,Queue(go24-q-111111112222,c,,,90,,go24-q-who)\n" +
			" same => n,GotoIf($[\"${SHARED(GO24_QBY)}\" != \"\"]?ok)\n same => n,Set(DIALSTATUS=NOANSWER)\n same => n,Goto(${GO24_BACK})\n" +
			" same => n(ok),Set(DIALSTATUS=ANSWER)\n same => n,Gosub(go24-rate,s,1(${GO24_RATEP}))\n same => n,Hangup()\n",
		" same => n,Queue(go24-q-222222222222,cr,,,60,,go24-q-who)\n",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("no %q in\n%s", want, d)
		}
	}
	// a line rings its group; a menu's item connects with one (a group gone — the line's «nobody answered»)
	tr := Trunk{ID: "abcdefab-1234-5678-9abc-def012345678", Name: "Линия", Number: "+74951234567", Host: "sip.example.ru", Username: "go24",
		Ring: []string{"u1"}, Group: sales.ID, NoAnswer: ActVoicemail}
	_, plan := RenderTrunks([]Trunk{tr}, map[string]string{"u1": "101"}, map[string]string{sales.ID: GroupKey(sales.ID)}, nil, false, "")
	if want := " same => n,Set(GO24_BACK=go24-in-" + tr.Key() + ",s,noans)\n same => n,Set(GO24_RATEP=)\n same => n,Goto(go24-q-111111112222,s,1)\n" +
		" same => n(noans),Gosub(go24-voicemail,s,1())\n"; !strings.Contains(plan, want) || strings.Contains(plan, "Local/101@go24-ring") {
		t.Fatalf("no %q in\n%s", want, plan)
	}
	// a CRM client's call rings their responsible first (in the working hours), not answered — the group
	tr.ToOwner, tr.WorkOn, tr.WorkDays, tr.WorkFrom, tr.WorkTo = true, true, []int{1}, "09:00", "18:00"
	_, plan = RenderTrunks([]Trunk{tr}, map[string]string{"u1": "101"}, map[string]string{sales.ID: GroupKey(sales.ID)}, nil, false, "")
	if want := " same => n(work),NoOp()\n same => n,Gosub(go24-owner,s,1(20,))\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n" +
		" same => n,Set(GO24_BACK=go24-in-" + tr.Key(); !strings.Contains(plan, want) {
		t.Fatalf("no %q in\n%s", want, plan)
	}
	tr.RateOn, tr.RateSum = true, "r1"
	if _, plan = RenderTrunks([]Trunk{tr}, nil, nil, nil, false, ""); !strings.Contains(plan, "Gosub(go24-owner,s,1(20,g))\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?rate)\n") {
		t.Fatalf("the owner rated:\n%s", plan)
	}
	menu := Menu{ID: "33333333-2222-3333-4444-555555555555", Name: "М", Wait: 5, Repeats: 1,
		Items: []MenuItem{{Digit: "1", Act: MenuGroup, Group: sales.ID}, {Digit: "2", Act: MenuGroup, Group: "gone"}}, Timeout: MenuItem{Act: ActHangup}}
	plan = RenderMenus([]Menu{menu}, nil, map[string]string{sales.ID: GroupKey(sales.ID)})
	for _, want := range []string{"exten => 1,1,Goto(go24-q-111111112222,s,1)\n", "exten => 2,1,Goto(${GO24_BACK})\n"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("no %q in\n%s", want, plan)
		}
	}
}

// TestGroups: the groups kept and checked; a line and a menu's item with a group; a group deleted — the line rings its
// employees, the item is dropped.
func TestGroups(t *testing.T) {
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
	var ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ann', 'ann') RETURNING id`).Scan(&ann))
	s := NewStore(pool, "secret")
	var ie *i18n.Error
	for _, bad := range []Group{
		{Name: " ", Users: []string{ann}, Ring: 20, Wait: 60},
		{Name: "Г", Ring: 20, Wait: 60},
		{Name: "Г", Users: []string{ann}, Ring: 2, Wait: 60},
		{Name: "Г", Users: []string{ann}, Ring: 20, Wait: 5000},
		{Name: "Г", Users: []string{ann}, Ring: 20, Wait: 60, Music: []byte("not a wav")},
	} {
		if _, err := s.SaveGroup(ctx, ann, bad); !errors.As(err, &ie) {
			t.Fatalf("a bad group saved: %+v: %v", bad, err)
		}
	}
	gid, err := s.SaveGroup(ctx, ann, Group{Name: " Продажи ", Users: []string{ann, ann, ""}, Strategy: "dance", Ring: 15, Wait: 90, Music: testWAV(8000, 1, 8000)})
	must(err)
	groups, err := s.Groups(ctx)
	must(err)
	if len(groups) != 1 || groups[0].Name != "Продажи" || len(groups[0].Users) != 1 || groups[0].Strategy != "all" || groups[0].MusicSum == "" {
		t.Fatalf("the groups: %+v", groups)
	}
	if music, err := s.GroupMusic(ctx, gid); err != nil || len(music) < 44 {
		t.Fatalf("the music: %d %v", len(music), err)
	}
	_, err = s.SaveGroup(ctx, ann, Group{ID: gid, Name: "Продажи", Users: []string{ann}, Strategy: "linear", Ring: 15, Wait: 90, DropMusic: true})
	must(err)
	if groups, _ = s.Groups(ctx); groups[0].Strategy != "linear" || groups[0].MusicSum != "" {
		t.Fatalf("the group changed: %+v", groups)
	}
	tid, err := s.SaveTrunk(ctx, ann, Trunk{Name: "Основной", Host: "sip.example.ru", Username: "go24", Password: "p", Number: "+74951234567", OutAll: true})
	must(err)
	must(s.SetTrunkGroup(ctx, tid, gid))
	if trunks, _ := s.Trunks(ctx); trunks[0].Group != gid {
		t.Fatalf("the line's group: %+v", trunks[0])
	}
	must(s.SetTrunkOwner(ctx, tid, true))
	if trunks, _ := s.Trunks(ctx); !trunks[0].ToOwner {
		t.Fatalf("the line rings no responsible: %+v", trunks[0])
	}
	if err := s.SetTrunkOwner(ctx, "nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such line: %v", err)
	}
	if _, err := s.SaveMenu(ctx, ann, Menu{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: MenuGroup, Group: "nope"}}}); !errors.As(err, &ie) {
		t.Fatalf("a menu with no such group: %v", err)
	}
	_, err = s.SaveMenu(ctx, ann, Menu{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: MenuGroup, Group: gid}, {Digit: "2", Act: ActHangup}},
		Timeout: MenuItem{Act: MenuGroup, Group: gid}})
	must(err)
	must(s.DeleteGroup(ctx, gid))
	if trunks, _ := s.Trunks(ctx); trunks[0].Group != "" {
		t.Fatalf("the line keeps the group: %+v", trunks[0])
	}
	if menus, _ := s.Menus(ctx); len(menus[0].Items) != 1 || menus[0].Items[0].Digit != "2" || menus[0].Timeout.Act != ActHangup {
		t.Fatalf("the menu keeps the group: %+v", menus)
	}
	if err := s.DeleteGroup(ctx, gid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
