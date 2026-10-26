package telephony

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRenderMenus: the dialplan of a voice menu — the greeting, waiting for a key, an extension per item, inner numbers
// dialed right away (not those a key has), nothing pressed — once more, then the timeout's action.
func TestRenderMenus(t *testing.T) {
	main := Menu{ID: "11111111-2222-3333-4444-555555555555", Name: "Главное; меню", Wait: 4, Repeats: 2, Direct: true, GreetingSum: "abc",
		Items: []MenuItem{
			{Digit: "1", Act: MenuRing, Users: []string{"u1", "u2"}, Mode: "queue"},
			{Digit: "2", Act: MenuRing, Users: []string{"u1", "u3"}},
			{Digit: "3", Act: ActForward, To: "+79005550011"},
			{Digit: "4", Act: ActForward, To: "102"},
			{Digit: "5", Act: ActVoicemail},
			{Digit: "6", Act: MenuGoto, Menu: "99999999-2222-3333-4444-555555555555"},
			{Digit: "*", Act: MenuRepeat},
			{Digit: "#", Act: ActHangup},
		},
		Timeout: MenuItem{Act: ActVoicemail}}
	sub := Menu{ID: "99999999-2222-3333-4444-555555555555", Name: "Бухгалтерия", Wait: 5, Repeats: 1, Timeout: MenuItem{Act: MenuRing, Users: []string{"gone"}}}
	got := RenderMenus([]Menu{main, sub}, map[string]string{"u1": "101", "u2": "102", "u3": "5"}, map[string]string{"g1": "go24-q-g1"})
	for _, want := range []string{
		"; Главное меню\n[go24-ivr-111111112222]\nexten => s,1,Set(GO24_TRY=0)\n same => n,Answer()",
		" same => n(again),Set(TIMEOUT(digit)=3)\n same => n,Background(" + AstDir + "/greet-abc)\n same => n,WaitExten(4)\n",
		"exten => 1,1,Set(GO24_BY=101)\n same => n,Dial(Local/101@go24-ring,20,g)\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n" +
			" same => n,Dial(Local/102@go24-ring,20,g)\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n same => n,Goto(${GO24_BACK})\n same => n(done),Gosub(go24-rate,s,1(${GO24_RATEP}))\n same => n,Hangup()\n",
		"exten => 2,1,Set(GO24_BY=101)\n same => n,Dial(Local/101@go24-ring&Local/5@go24-ring,45,g)\n",
		"exten => 3,1,Gosub(go24-forward,s,1(${GO24_TRUNK},${GO24_TNUM},+79005550011))\n",
		"exten => 4,1,Set(GO24_BY=102)\n same => n,Dial(Local/102@go24-ring,45,g)\n",
		"exten => 5,1,Gosub(go24-voicemail,s,1(${GO24_GREET}))\n same => n,Hangup()\n",
		"exten => 6,1,Goto(go24-ivr-999999992222,s,1)\n",
		"exten => *,1,Goto(s,1)\n", "exten => #,1,Hangup()\n",
		"exten => 101,1,Set(GO24_BY=101)\n same => n,Dial(Local/101@go24-ring,45,g)\n", "exten => 102,1,",
		"exten => t,1,Set(GO24_TRY=$[${GO24_TRY} + 1])\n same => n,GotoIf($[${GO24_TRY} < 2]?s,again)\n same => n,Gosub(go24-voicemail,s,1(${GO24_GREET}))\n",
		"exten => i,1,Set(GO24_TRY=$[${GO24_TRY} + 1])\n",
		// no greeting: just waiting; nobody to ring — the line's «nobody answered»
		"[go24-ivr-999999992222]\nexten => s,1,Set(GO24_TRY=0)\n same => n,Answer()\n same => n,Wait(1)\n same => n(again),Set(TIMEOUT(digit)=3)\n same => n,WaitExten(5)\n",
		"same => n,GotoIf($[${GO24_TRY} < 1]?s,again)\n same => n,Goto(${GO24_BACK})\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "exten => 5,1,Set(GO24_BY=5)") {
		t.Fatalf("a key's extension twice:\n%s", got)
	}
	// a line with the menu: its calls go there with what the menu needs of the line
	tr := Trunk{ID: "abcdefab-1234-5678-9abc-def012345678", Name: "Линия", Host: "sip.example.ru", Username: "go24", Number: "+74951234567",
		Ring: []string{"u1"}, Menu: main.ID, NoAnswer: ActVoicemail, WorkOn: true, WorkDays: []int{1}, WorkFrom: "09:00", WorkTo: "18:00", OffHours: ActHangup}
	_, d := RenderTrunks([]Trunk{tr}, map[string]string{"u1": "101"}, map[string]string{main.ID: MenuKey(main.ID)}, nil, false, "")
	k := tr.Key()
	for _, want := range []string{
		" same => n,Gosub(go24-in,s,1(+74951234567,," + k + "))\n same => n,GotoIfTime(",
		" same => n(work),NoOp()\n same => n,Set(GO24_TRUNK=" + k + ")\n same => n,Set(GO24_TNUM=+74951234567)\n same => n,Set(GO24_GREET=)\n" +
			" same => n,Set(GO24_BACK=go24-in-" + k + ",s,noans)\n same => n,Set(GO24_RATEP=)\n same => n,Goto(go24-ivr-111111112222,s,1)\n same => n(noans),Gosub(go24-voicemail,s,1())\n",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("no %q in\n%s", want, d)
		}
	}
	if strings.Contains(d, "Local/101@go24-ring") {
		t.Fatalf("a line with a menu rings its employees itself:\n%s", d)
	}
	// the client rates the conversation: the menu's rings and the line's own
	tr.RateOn, tr.RateSum = true, "r1"
	if _, d = RenderTrunks([]Trunk{tr}, map[string]string{"u1": "101"}, map[string]string{main.ID: MenuKey(main.ID)}, nil, false, ""); !strings.Contains(d, " same => n,Set(GO24_RATEP="+AstDir+"/greet-r1)\n") {
		t.Fatalf("the menu does not ask for the rating:\n%s", d)
	}
	tr.Menu, tr.RingMode = "", "queue"
	_, d = RenderTrunks([]Trunk{tr}, map[string]string{"u1": "101"}, nil, nil, false, "")
	for _, want := range []string{
		" same => n,Dial(Local/101@go24-ring,20,g)\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?rate)\n",
		" same => n(rate),Gosub(go24-rate,s,1(" + AstDir + "/greet-r1))\n same => n(done),Hangup()\n",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("no %q in\n%s", want, d)
		}
	}
}

// TestMenus: voice menus kept and checked; a line sent to a menu; a menu deleted — the lines ring their employees, the
// items of other menus leading to it are dropped; the menus in Asterisk's dialplan with their greetings.
func TestMenus(t *testing.T) {
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
	ann, bob := user("ann"), user("bob")
	s := NewStore(pool, "secret")
	must(s.SetEnabled(ctx, ann, true))
	must(s.SetNumber(ctx, ann, "101"))
	must(s.SetNumber(ctx, bob, "102"))

	subID, err := s.SaveMenu(ctx, ann, Menu{Name: "Бухгалтерия", Wait: 5, Repeats: 1, Timeout: MenuItem{Act: MenuRepeat}})
	must(err)
	var ie *i18n.Error
	for _, bad := range []Menu{
		{Name: " ", Wait: 5, Repeats: 1},
		{Name: "М", Wait: 0, Repeats: 1},
		{Name: "М", Wait: 5, Repeats: 6},
		{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "A", Act: ActHangup}}},
		{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: ActHangup}, {Digit: "1", Act: ActVoicemail}}},
		{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: MenuRing}}},
		{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: ActForward, To: "abc"}}},
		{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: MenuGoto, Menu: "00000000-0000-0000-0000-000000000000"}}},
		{Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: "dance"}}},
		{Name: "М", Wait: 5, Repeats: 1, Greeting: []byte("not a wav")},
		{ID: subID, Name: "М", Wait: 5, Repeats: 1, Items: []MenuItem{{Digit: "1", Act: MenuGoto, Menu: subID}}},
	} {
		if _, err := s.SaveMenu(ctx, ann, bad); !errors.As(err, &ie) {
			t.Fatalf("a bad menu saved: %+v: %v", bad, err)
		}
	}
	mainID, err := s.SaveMenu(ctx, ann, Menu{Name: "Главное", Wait: 4, Repeats: 2, Direct: true, Greeting: testWAV(8000, 1, 8000),
		Items: []MenuItem{
			{Digit: "#", Act: ActHangup},
			{Digit: "2", Act: MenuGoto, Menu: subID},
			{Digit: "1", Act: MenuRing, Users: []string{ann, bob, ann}, Mode: "queue"},
			{Digit: "3"}, // a row left empty
			{Digit: "0", Act: ActForward, To: "+7 (900) 555-00-11"},
		},
		Timeout: MenuItem{Act: MenuGoto, Menu: subID}})
	must(err)
	menus, err := s.Menus(ctx)
	must(err)
	if len(menus) != 2 || menus[0].Name != "Бухгалтерия" || menus[0].Timeout.Act != ActHangup || menus[1].GreetingSum == "" {
		t.Fatalf("menus: %+v", menus)
	}
	m := menus[1]
	if len(m.Items) != 4 || m.Items[0].Digit != "1" || len(m.Items[0].Users) != 2 || m.Items[0].Mode != "queue" || m.Items[1].Digit != "2" ||
		m.Items[2].Digit != "0" || m.Items[2].To != "+79005550011" || m.Items[3].Digit != "#" || m.Timeout.Menu != subID {
		t.Fatalf("the main menu: %+v", m)
	}
	if len(m.Rows()) != 6 {
		t.Fatal("no empty rows to add an item")
	}

	// a line answered by the menu
	line := Trunk{Name: "Основной", Host: "sip.example.ru", Username: "go24", Password: "pw", Number: "+74951234567", Ring: []string{bob}, Menu: "00000000-0000-0000-0000-000000000000"}
	if _, err := s.SaveTrunk(ctx, ann, line); !errors.As(err, &ie) {
		t.Fatalf("a line with no such menu: %v", err)
	}
	line.Menu = mainID
	tid, err := s.SaveTrunk(ctx, ann, line)
	must(err)
	ari := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ari.Close()
	dir := t.TempDir()
	pbx := &PBX{Store: s, Dir: dir, ARI: &ARI{URL: ari.URL}}
	must(pbx.Sync(ctx))
	ext, err := os.ReadFile(filepath.Join(dir, "extensions.conf"))
	must(err)
	mk, sk := MenuKey(mainID), MenuKey(subID)
	for _, want := range []string{"Goto(" + mk + ",s,1)", "[" + mk + "]", "[" + sk + "]", "Background(" + AstDir + "/greet-" + m.GreetingSum + ")",
		"exten => 2,1,Goto(" + sk + ",s,1)", "exten => 102,1,"} {
		if !strings.Contains(string(ext), want) {
			t.Fatalf("no %q in the dialplan:\n%s", want, ext)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "greet-"+m.GreetingSum+".wav")); err != nil {
		t.Fatalf("no greeting of the menu: %v", err)
	}
	if g, err := s.MenuGreeting(ctx, mainID); err != nil || len(g) != 44+8000*2 {
		t.Fatalf("the menu's greeting: %d %v", len(g), err)
	}

	// changed: the greeting stays; deleted: lines ring their employees, other menus' items to it gone
	m.Greeting, m.Name = nil, "Главное меню"
	_, err = s.SaveMenu(ctx, ann, m)
	must(err)
	if menus, _ = s.Menus(ctx); menus[1].GreetingSum != m.GreetingSum || menus[1].Name != "Главное меню" {
		t.Fatalf("the menu changed: %+v", menus[1])
	}
	must(s.DeleteMenu(ctx, subID))
	if err := s.DeleteMenu(ctx, subID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	if menus, _ = s.Menus(ctx); len(menus) != 1 || len(menus[0].Items) != 3 || menus[0].Timeout.Act != ActHangup {
		t.Fatalf("items to the deleted menu stay: %+v", menus)
	}
	must(s.DeleteMenu(ctx, mainID))
	if trunks, _ := s.Trunks(ctx); trunks[0].ID != tid || trunks[0].Menu != "" {
		t.Fatalf("the line keeps the deleted menu: %+v", trunks[0])
	}
	must(pbx.Sync(ctx))
	if _, err := os.Stat(filepath.Join(dir, "greet-"+m.GreetingSum+".wav")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the deleted menu's greeting stays: %v", err)
	}
	if ext, _ = os.ReadFile(filepath.Join(dir, "extensions.conf")); strings.Contains(string(ext), "go24-ivr-") || !strings.Contains(string(ext), "Local/102@go24-ring") {
		t.Fatalf("the dialplan after the menus are gone:\n%s", ext)
	}
}
