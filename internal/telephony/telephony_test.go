package telephony

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRender: each employee has an account of their SIP phone and of the softphone (WebRTC), with the password kept
// as md5(login:go24:password) and their name as the caller ID; one with a number taken by someone earlier has none.
func TestRender(t *testing.T) {
	conf := Render([]Line{
		{UserID: "1", Name: `Анна "Аня" <Петрова>`, Number: "101", Password: "pw1", WebPassword: "web1"},
		{UserID: "2", Name: "Борис", Number: "101", Password: "pw2", WebPassword: "web2", Dup: true},
	}, false)
	for _, want := range []string{
		"[101]\ntype = endpoint\ncontext = go24-phones\n", "[w101]\ntype = endpoint\ncontext = go24-phones\n", "webrtc = yes\n",
		"callerid = \"Анна Аня Петрова\" <101>\n", "md5_cred = " + md5cred("101", "pw1") + "\n", "md5_cred = " + md5cred("w101", "web1") + "\n",
		"[w101]\ntype = aor\nmax_contacts = 5\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("no %q in\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "pw1") || strings.Contains(conf, "Борис") || strings.Count(conf, "type = endpoint") != 2 {
		t.Errorf("a password in the open or the second 101:\n%s", conf)
	}
	if rec := Render([]Line{{UserID: "1", Name: "Анна", Number: "101"}}, true); strings.Count(rec, "set_var = GO24_RECORD=1\n") != 2 ||
		strings.Contains(conf, "GO24_RECORD") {
		t.Errorf("recording: the dialplan does not know or knows when off:\n%s", rec)
	}
	if strings.Count(conf, "set_var = TRANSFER_CONTEXT=go24-transfer\n") != 2 {
		t.Errorf("a transferred call does not go to go24-transfer:\n%s", conf)
	}
	if p := Password(12); len(p) != 12 || strings.ContainsAny(p, "0O1lI") {
		t.Errorf("password %q", p)
	}
}

// TestLines: inner numbers given by administrators make lines with passwords (sealed, the same each time), a number
// may not be taken twice, a new password replaces the old, dismissed employees and guests have no line; the PBX writes
// the accounts into its folder and has Asterisk reload them only when they changed, and none while telephony is off.
func TestLines(t *testing.T) {
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
	ann, bob, eve, gus := user("ann"), user("bob"), user("eve"), user("gus")
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, gus)
	must(err)
	s := NewStore(pool, "secret")

	must(s.SetNumber(ctx, ann, "101"))
	must(s.SetNumber(ctx, bob, " 102 "))
	must(s.SetNumber(ctx, gus, "103"))
	var ie *i18n.Error
	if err := s.SetNumber(ctx, eve, "101"); !errors.As(err, &ie) || ie.Key != "err.inner_phone_taken" {
		t.Fatalf("101 twice: %v", err)
	}
	if err := s.SetNumber(ctx, eve, "12a"); !errors.As(err, &ie) || ie.Key != "err.inner_phone" {
		t.Fatalf("bad number: %v", err)
	}
	lines, err := s.Lines(ctx)
	must(err)
	if len(lines) != 2 || lines[0].Number != "101" || lines[0].Name != "ann" || lines[1].Number != "102" || lines[1].Login() != "102" ||
		lines[1].WebLogin() != "w102" || len(lines[0].Password) != 12 || len(lines[0].WebPassword) != 24 || lines[0].Dup {
		t.Fatalf("lines %+v", lines)
	}
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT secret FROM telephony_lines WHERE user_id = $1`, ann).Scan(&sealed))
	if strings.Contains(string(sealed), lines[0].Password) {
		t.Fatal("the password is kept in the open")
	}
	again, ok, err := s.LineOf(ctx, ann)
	must(err)
	if !ok || again.Password != lines[0].Password || again.WebPassword != lines[0].WebPassword {
		t.Fatalf("the passwords changed: %+v", again)
	}
	pw, err := s.ResetPassword(ctx, ann)
	must(err)
	if again, _, _ = s.LineOf(ctx, ann); pw == lines[0].Password || again.Password != pw || again.WebPassword != lines[0].WebPassword {
		t.Fatalf("new password %q, line %+v", pw, again)
	}
	if _, err := s.ResetPassword(ctx, eve); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no number, new password: %v", err)
	}
	// numbers imported twice: the first one who came keeps the account
	_, err = pool.Exec(ctx, `UPDATE users SET inner_phone = '102' WHERE id = $1`, eve)
	must(err)
	lines, err = s.Lines(ctx)
	must(err)
	if len(lines) != 3 || lines[1].UserID != bob || lines[1].Dup || lines[2].UserID != eve || !lines[2].Dup {
		t.Fatalf("lines with 102 twice %+v", lines)
	}
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, eve)
	must(err)

	// the PBX: the file and the reloads
	var reloads, dialplans atomic.Int32
	ari := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "go24" || p != "ari" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/ari/asterisk/modules/res_pjsip.so" {
			reloads.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/ari/asterisk/modules/pbx_config.so" {
			dialplans.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/ari/asterisk/modules/") { // the queues and their music
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	defer ari.Close()
	dir := t.TempDir()
	pbx := &PBX{Store: s, Dir: dir, ARI: &ARI{URL: ari.URL, User: "go24", Password: "ari"}}
	file := filepath.Join(dir, "pjsip.conf")
	read := func() string {
		b, err := os.ReadFile(file)
		must(err)
		return string(b)
	}
	must(pbx.Sync(ctx))
	if c := read(); strings.Contains(c, "[101]") || reloads.Load() != 1 {
		t.Fatalf("telephony off: %d reloads,\n%s", reloads.Load(), c)
	}
	must(s.SetEnabled(ctx, ann, true))
	must(pbx.Sync(ctx))
	must(pbx.Sync(ctx))
	if c := read(); !strings.Contains(c, "[101]") || !strings.Contains(c, "[w102]") || strings.Contains(c, "eve") || reloads.Load() != 2 {
		t.Fatalf("telephony on: %d reloads,\n%s", reloads.Load(), c)
	}
	must(s.SetNumber(ctx, bob, ""))
	must(pbx.Sync(ctx))
	if c := read(); strings.Contains(c, "[102]") || reloads.Load() != 3 {
		t.Fatalf("102 taken away: %d reloads,\n%s", reloads.Load(), c)
	}
	// go24 restarted: the file is the same, Asterisk is not bothered
	pbx2 := &PBX{Store: s, Dir: dir, ARI: pbx.ARI}
	must(pbx2.Sync(ctx))
	if reloads.Load() != 3 {
		t.Fatalf("after a restart: %d reloads", reloads.Load())
	}
	// Asterisk does not answer: the file is written, the reload is tried again next time
	pbx2.ARI = &ARI{URL: ari.URL, User: "go24", Password: "wrong"}
	must(s.SetNumber(ctx, bob, "105"))
	if err := pbx2.Sync(ctx); err == nil || !strings.Contains(read(), "[105]") {
		t.Fatalf("asterisk refused: %v", err)
	}
	pbx2.ARI = pbx.ARI
	must(pbx2.Sync(ctx))
	if reloads.Load() != 4 {
		t.Fatalf("reload again: %d", reloads.Load())
	}

	// the softphone: one's own number while telephony is on; who is on the other end
	if n, err := s.NumberOf(ctx, bob); err != nil || n != "105" {
		t.Fatalf("bob's number: %q %v", n, err)
	}
	if n, _ := s.NumberOf(ctx, gus); n != "" {
		t.Fatalf("a guest has a softphone: %q", n)
	}
	if w, ok, err := s.WhoIs(ctx, "105"); err != nil || !ok || w.UserID != bob || w.Name != "bob" {
		t.Fatalf("who is 105: %+v %v %v", w, ok, err)
	}
	if _, ok, _ := s.WhoIs(ctx, "+79001234567"); ok {
		t.Fatal("an outside number is an employee")
	}

	// the log: bob calls ann (the record kept), an outside number misses ann (its record dropped), told twice — once
	t0 := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	id, err := s.SaveCall(ctx, CDR{UniqueID: "1.1", From: "105", To: "101", Dial: "ANSWER", Record: "x.wav", Start: t0, Answer: t0.Add(5 * time.Second), End: t0.Add(80 * time.Second)})
	must(err)
	if again, err := s.SaveCall(ctx, CDR{UniqueID: "1.1", From: "105", To: "101", Start: t0}); err != nil || again != "" || id == "" {
		t.Fatalf("told twice: %q %q %v", id, again, err)
	}
	_, err = s.SaveCall(ctx, CDR{UniqueID: "1.2", From: "+79001234567", To: "101", Dial: "NOANSWER", Record: "y.wav", Start: t0.Add(time.Hour)})
	must(err)
	_, err = s.SaveCall(ctx, CDR{UniqueID: "1.3", From: "101", To: "109", Dial: "", Record: "../../etc/passwd", Start: t0.Add(2 * time.Hour)})
	must(err)
	all, err := s.Calls(ctx, CallFilter{})
	must(err)
	if len(all) != 3 || all[2].FromUser.UserID != bob || all[2].ToUser.Name != "ann" || all[2].Seconds != 75 || all[2].Duration() != "1:15" ||
		all[2].Record != "x.wav" || all[1].Record != "" || all[1].Status != StatusNoAnswer || all[1].FromUser.UserID != "" || all[0].Status != StatusFailed || all[0].Record != "" {
		t.Fatalf("the log: %+v", all)
	}
	for _, c := range []struct {
		f    CallFilter
		want int
	}{
		{CallFilter{UserID: ann}, 3}, {CallFilter{UserID: ann, Kind: "in"}, 2}, {CallFilter{UserID: ann, Kind: "out"}, 1},
		{CallFilter{UserID: ann, Kind: "missed"}, 1}, {CallFilter{UserID: bob}, 1}, {CallFilter{UserID: eve}, 0},
		{CallFilter{Number: "900"}, 1}, {CallFilter{From: t0.Add(30 * time.Minute)}, 2}, {CallFilter{To: t0.Add(30 * time.Minute)}, 1},
		{CallFilter{Before: all[0].Started}, 2}, {CallFilter{Limit: 1}, 1},
	} {
		if got, err := s.Calls(ctx, c.f); err != nil || len(got) != c.want {
			t.Errorf("%+v: %d calls, want %d (%v)", c.f, len(got), c.want, err)
		}
	}
	if c, err := s.CallByID(ctx, id); err != nil || c.Record != "x.wav" || c.ToUser.UserID != ann {
		t.Fatalf("the call: %+v %v", c, err)
	}
	if _, err := s.CallByID(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no call: %v", err)
	}
	// recording is the administrators' setting; the accounts tell the dialplan
	must(s.SetRecording(ctx, ann, true))
	must(pbx.Sync(ctx))
	if c := read(); !strings.Contains(c, "set_var = GO24_RECORD=1") {
		t.Fatalf("recording on, the accounts do not say so:\n%s", c)
	}

	// external lines: the password sealed, the accounts and go24's dialplan written, the dialplan reloaded once
	if _, err := s.SaveTrunk(ctx, ann, Trunk{Name: "Основной", Host: "sip.example.ru", Username: "go24", Number: "+7 495 123-45-67"}); err == nil {
		t.Fatal("a new line without its password")
	}
	for _, bad := range []Trunk{{Name: "", Host: "h", Username: "u", Number: "1", Password: "p"}, {Name: "x", Host: "bad host", Username: "u", Number: "1", Password: "p"},
		{Name: "x", Host: "h", Username: "u;x", Number: "1", Password: "p"}, {Name: "x", Host: "h", Username: "u", Number: "12a", Password: "p"}} {
		if _, err := s.SaveTrunk(ctx, ann, bad); !errors.As(err, &ie) {
			t.Errorf("a bad line saved: %+v %v", bad, err)
		}
	}
	tid, err := s.SaveTrunk(ctx, ann, Trunk{Name: "Основной", Host: "sip.example.ru:5060", Username: "go24", Password: "p;w", Number: "+7 495 123-45-67",
		Register: true, RingMode: "queue", Ring: []string{bob, ann}, OutAll: false, Out: []string{bob}})
	must(err)
	dir2 := dialplans.Load()
	must(pbx.Sync(ctx))
	trunks, err := s.Trunks(ctx)
	must(err)
	if len(trunks) != 1 || trunks[0].Password != "p;w" || trunks[0].Number != "+74951234567" || trunks[0].RingMode != "queue" || len(trunks[0].Ring) != 2 {
		t.Fatalf("the line: %+v", trunks)
	}
	k := trunks[0].Key()
	ext, err := os.ReadFile(filepath.Join(dir, "extensions.conf"))
	must(err)
	if c := read(); !strings.Contains(c, "["+k+"]\ntype = registration\n") || !strings.Contains(c, `password = p\;w`) || strings.Contains(c, "p;w") ||
		!strings.Contains(c, "contact_user = 74951234567\n") || dialplans.Load() != dir2+1 {
		t.Fatalf("the line's accounts (%d dialplan reloads):\n%s", dialplans.Load(), c)
	}
	if e := string(ext); !strings.Contains(e, "[go24-in-"+k+"]") || !strings.Contains(e, "Dial(Local/105@go24-ring,20)") ||
		!strings.Contains(e, "exten => _X./105,1,Gosub(go24-dial-out,s,1("+k+",+74951234567,${EXTEN}))") || strings.Contains(e, "/101,") {
		t.Fatalf("go24's dialplan:\n%s", e)
	}
	must(pbx.Sync(ctx))
	if dialplans.Load() != dir2+1 {
		t.Fatal("the dialplan reloaded with nothing changed")
	}
	// the black list: a number by its last 10 digits (8…, +7… — one number), the lines' calls from it hear «busy»
	if e := string(ext); !strings.Contains(e, "[go24-black]\n") ||
		!strings.Contains(e, "exten => s,1,Set(GO24_BNUM=${FILTER(0123456789,${CALLERID(num)})})\n same => n,GotoIf(${DIALPLAN_EXISTS(go24-black,${GO24_BNUM:-10},1)}?black,1)\n") ||
		!strings.Contains(e, "exten => black,1,Busy(5)\n") {
		t.Fatalf("no black list in the dialplan:\n%s", e)
	}
	for _, bad := range []string{"", "12", "abc"} {
		if err := s.Block(ctx, ann, bad, ""); !errors.As(err, &ie) {
			t.Errorf("blocked %q: %v", bad, err)
		}
	}
	must(s.Block(ctx, ann, "8 (900) 111-22-33", "спам"))
	must(s.Block(ctx, ann, "+79001112233", "")) // the same number: the comment stays
	must(s.Block(ctx, ann, "555", "короткий"))
	bl, err := s.Blacklist(ctx)
	must(err)
	if len(bl) != 2 || bl[1].Number != "89001112233" || bl[1].Comment != "спам" || bl[1].ByName != "ann" {
		t.Fatalf("black list: %+v", bl)
	}
	must(pbx.Sync(ctx))
	ext, err = os.ReadFile(filepath.Join(dir, "extensions.conf"))
	must(err)
	if e := string(ext); !strings.Contains(e, "[go24-black]\nexten => 555,1,Return()\nexten => 9001112233,1,Return()\n") || dialplans.Load() != dir2+2 {
		t.Fatalf("the black list's dialplan:\n%s", e)
	}
	must(s.Unblock(ctx, bl[0].ID))
	must(s.Unblock(ctx, bl[1].ID))
	if err := s.Unblock(ctx, bl[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unblocked twice: %v", err)
	}
	must(pbx.Sync(ctx))
	// working hours and what happens to a call nobody answered: the voicemail with the line's greeting, a forward
	// out through the line; bad hours or numbers are not saved
	line := trunks[0]
	for _, bad := range []Trunk{{WorkOn: true, WorkFrom: "18:00", WorkTo: "09:00", WorkDays: []int{1}}, {WorkOn: true, WorkDays: []int{9}},
		{NoAnswer: ActForward, NoAnswerTo: "abc"}, {Greeting: []byte("not a wav")}} {
		bad.ID, bad.Name, bad.Host, bad.Username, bad.Number = tid, line.Name, line.Host, line.Username, line.Number
		if _, err := s.SaveTrunk(ctx, ann, bad); !errors.As(err, &ie) {
			t.Errorf("bad rules saved: %+v %v", bad, err)
		}
	}
	line.WorkOn, line.WorkDays, line.WorkFrom, line.WorkTo = true, []int{5, 1, 2}, "08:30", "17:00"
	line.NoAnswer, line.OffHours, line.OffHoursTo = ActVoicemail, ActForward, "+7 (900) 555-00-11"
	line.Greeting = testWAV(16000, 2, 16000)
	_, err = s.SaveTrunk(ctx, ann, line)
	must(err)
	must(pbx.Sync(ctx))
	trunks, err = s.Trunks(ctx)
	must(err)
	if l := trunks[0]; !l.WorkOn || fmt.Sprint(l.WorkDays) != "[1 2 5]" || l.OffHoursTo != "+79005550011" || l.GreetingSum == "" {
		t.Fatalf("the line's rules: %+v", l)
	}
	sum := trunks[0].GreetingSum
	g, err := os.ReadFile(filepath.Join(dir, "greet-"+sum+".wav"))
	must(err)
	if len(g) != 44+8000*2 { // a second, as 8 kHz mono
		t.Fatalf("the greeting's file: %d bytes", len(g))
	}
	if _, err := os.Stat(filepath.Join(dir, "beep.wav")); err != nil {
		t.Fatalf("no beep: %v", err)
	}
	ext, err = os.ReadFile(filepath.Join(dir, "extensions.conf"))
	must(err)
	if e := string(ext); !strings.Contains(e, "GotoIfTime(08:30-17:00,mon&tue&fri,*,*") || !strings.Contains(e, "Gosub(go24-voicemail,s,1("+AstDir+"/greet-"+sum+"))") ||
		!strings.Contains(e, "same => n(off),Gosub(go24-forward,s,1("+k+",+74951234567,+79005550011))") || !strings.Contains(e, "n(done),Hangup()") {
		t.Fatalf("the line's rules in the dialplan:\n%s", e)
	}
	// the greeting kept when no new one; removed — its file too
	line.Greeting, line.OffHours = nil, ActHangup
	_, err = s.SaveTrunk(ctx, ann, line)
	must(err)
	if trunks, _ = s.Trunks(ctx); trunks[0].GreetingSum != sum {
		t.Fatal("the greeting lost")
	}
	line.DropGreeting = true
	_, err = s.SaveTrunk(ctx, ann, line)
	must(err)
	must(pbx.Sync(ctx))
	if _, err := os.Stat(filepath.Join(dir, "greet-"+sum+".wav")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removed greeting's file stays: %v", err)
	}
	// the password kept when not given; the line gone
	_, err = s.SaveTrunk(ctx, ann, Trunk{ID: tid, Name: "Основной", Host: "sip.example.ru", Username: "go24", Number: "+74951234567", OutAll: true})
	must(err)
	if trunks, _ = s.Trunks(ctx); trunks[0].Password != "p;w" || !trunks[0].OutAll || trunks[0].RingMode != "all" {
		t.Fatalf("the line changed: %+v", trunks)
	}
	must(s.DeleteTrunk(ctx, tid))
	if err := s.DeleteTrunk(ctx, tid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	// a call from an external line: the employee who answered is on go24's end
	_, err = s.SaveCall(ctx, CDR{UniqueID: "2.1", From: "+79001234567", To: "+74951234567", By: "105", Line: "+74951234567", Dial: "ANSWER",
		Start: t0, Answer: t0.Add(time.Second), End: t0.Add(time.Minute)})
	must(err)
	if got, _ := s.Calls(ctx, CallFilter{UserID: bob, Kind: "in"}); len(got) != 1 || got[0].Line != "+74951234567" || got[0].ToUser.UserID != bob {
		t.Fatalf("a call from the line: %+v", got)
	} else if n, dir, emp := got[0].Outside(); n != "+79001234567" || dir != "in" || emp != bob {
		t.Fatalf("its outside: %s %s %s", n, dir, emp)
	}
	// transferred: by whom and to whom, with their names; nonsense dropped
	_, err = s.SaveCall(ctx, CDR{UniqueID: "2.2", From: "+79001234568", To: "+74951234567", By: "105", Line: "+74951234567", Dial: "ANSWER",
		XFrom: "w1", XTo: "105", Start: t0, Answer: t0.Add(time.Second), End: t0.Add(time.Minute)})
	must(err)
	_, err = s.SaveCall(ctx, CDR{UniqueID: "2.3", From: "+79001234569", To: "+74951234567", By: "105", Line: "+74951234567", Dial: "ANSWER",
		XFrom: "999", XTo: "105", Start: t0, Answer: t0.Add(time.Second), End: t0.Add(time.Minute)})
	must(err)
	if got, _ := s.Calls(ctx, CallFilter{UserID: bob, Number: "+7900123456"}); len(got) != 3 {
		t.Fatalf("transferred calls: %+v", got)
	} else {
		x := map[string]Call{}
		for _, c := range got {
			x[c.From] = c
		}
		if c := x["+79001234569"]; c.XFrom != "999" || c.XTo != "105" || c.XToName != "bob" || c.XFromName != "" {
			t.Fatalf("a transferred call: %+v", c)
		}
		if c := x["+79001234568"]; c.XFrom != "" || c.XTo != "" {
			t.Fatalf("a bad transfer kept: %+v", c)
		}
	}

	// a voicemail: not answered by the employees, the message kept; none left — just a call nobody answered
	_, err = s.SaveCall(ctx, CDR{UniqueID: "3.1", From: "+79001234567", To: "+74951234567", By: "105", Line: "+74951234567", Dial: "NOANSWER",
		Voicemail: true, Record: "vm.wav", Start: t0, Answer: t0.Add(30 * time.Second), End: t0.Add(50 * time.Second)})
	must(err)
	_, err = s.SaveCall(ctx, CDR{UniqueID: "3.2", From: "+79001234567", To: "+74951234567", By: "105", Line: "+74951234567", Dial: "NOANSWER",
		Voicemail: true, Start: t0, Answer: t0.Add(30 * time.Second), End: t0.Add(31 * time.Second)})
	must(err)
	if got, _ := s.Calls(ctx, CallFilter{UserID: bob, Kind: "missed"}); len(got) != 2 {
		t.Fatalf("voicemails missed: %+v", got)
	} else {
		byStatus := map[string]Call{}
		for _, c := range got {
			byStatus[c.Status] = c
		}
		if vm := byStatus[StatusVoicemail]; vm.Record != "vm.wav" || vm.Answered != nil || vm.Seconds != 20 || byStatus[StatusNoAnswer].ID == "" {
			t.Fatalf("voicemail: %+v", got)
		}
	}
	// «Статистика звонков»: the calls of the lines (not the inner ones) by employee and by day; the voicemail's message
	// is not a conversation
	_, err = s.SaveCall(ctx, CDR{UniqueID: "4.1", From: "105", To: "+79005550000", Line: "+74951234567", Dial: "ANSWER",
		Start: t0, Answer: t0.Add(10 * time.Second), End: t0.Add(130 * time.Second)})
	must(err)
	_, err = s.SaveCall(ctx, CDR{UniqueID: "4.2", From: "105", To: "+79005550001", Line: "+74951234567", Dial: "NOANSWER", Start: t0})
	must(err)
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	st, err := s.Stats(ctx, StatFilter{From: day.AddDate(0, 0, -1), To: day.AddDate(0, 0, 1)}, time.UTC)
	must(err)
	if len(st.Users) != 1 || st.Users[0].UserID != bob || st.Users[0].Name != "bob" {
		t.Fatalf("stats by employee: %+v", st.Users)
	}
	if r := st.Total; r.In != 5 || r.Missed != 2 || r.Answered() != 3 || r.MissedPct() != 40 || r.Out != 2 || r.OutOK != 1 ||
		r.Seconds != 3*59+120 || r.AvgTalk() != 74 || r.AvgWait() != 1 {
		t.Fatalf("stats: %+v", r)
	}
	if len(st.Days) != 2 || st.Days[0].In != 0 || st.Days[1].In != 5 || st.Days[1].Answered() != 3 || st.Days[1].Out != 2 || !st.Days[1].Day.Equal(day) {
		t.Fatalf("stats by day: %+v", st.Days)
	}
	if st, _ := s.Stats(ctx, StatFilter{From: day, To: day.AddDate(0, 0, 1), Line: "+70000000000"}, time.UTC); st.Total.In+st.Total.Out != 0 {
		t.Fatalf("another line's stats: %+v", st.Total)
	}
	if st, _ := s.Stats(ctx, StatFilter{From: day, To: day.AddDate(0, 0, 1), UserID: ann}, time.UTC); len(st.Users) != 0 {
		t.Fatalf("ann's stats: %+v", st.Users)
	}
	must(s.SetEnabled(ctx, ann, false))
	if n, _ := s.NumberOf(ctx, bob); n != "" {
		t.Fatalf("telephony off, still a softphone: %q", n)
	}
}

// TestDialable: what one may dial from a card — a phone number as people write it, nothing else.
func TestDialable(t *testing.T) {
	for in, want := range map[string]string{
		"101": "101", "tel:+7 (900) 123-45-67": "+79001234567", " 8.800.555 ": "8800555", "*72#": "*72#",
		"": "", "12+3": "", "abc": "", "101;drop": "", "123456789012345678901": "",
	} {
		if got := Dialable(in); got != want {
			t.Errorf("Dialable(%q) = %q, want %q", in, got, want)
		}
	}
}

// testWAV is a PCM WAV of a 440 Hz tone: n frames.
func testWAV(rate, channels, n int) []byte {
	var b bytes.Buffer
	size := n * channels * 2
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+size))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, []uint32{16})
	binary.Write(&b, binary.LittleEndian, []uint16{1, uint16(channels)})
	binary.Write(&b, binary.LittleEndian, []uint32{uint32(rate), uint32(rate * channels * 2)})
	binary.Write(&b, binary.LittleEndian, []uint16{uint16(channels * 2), 16})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(size))
	for i := 0; i < n; i++ {
		v := int16(10000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
		for c := 0; c < channels; c++ {
			binary.Write(&b, binary.LittleEndian, v)
		}
	}
	return b.Bytes()
}

// TestGreeting: an uploaded WAV becomes 8 kHz mono of the same length; what is not a PCM WAV, or too long, is refused.
func TestGreeting(t *testing.T) {
	for _, c := range []struct{ rate, ch, n, want int }{{8000, 1, 8000, 8000}, {44100, 2, 22050, 4000}, {16000, 1, 1600, 800}} {
		g, err := Greeting(testWAV(c.rate, c.ch, c.n))
		if err != nil {
			t.Fatal(err)
		}
		if len(g) != 44+c.want*2 || string(g[:4]) != "RIFF" || binary.LittleEndian.Uint32(g[24:]) != 8000 || binary.LittleEndian.Uint16(g[22:]) != 1 {
			t.Errorf("%+v: %d bytes", c, len(g))
		}
	}
	for _, bad := range [][]byte{nil, []byte("RIFF....WAVE"), []byte("ID3 mp3 file"), testWAV(8000, 1, 61*8000)} {
		if _, err := Greeting(bad); err == nil {
			t.Errorf("refused nothing of %d bytes", len(bad))
		}
	}
	if b := Beep(); len(b) != 44+8000 {
		t.Errorf("beep: %d bytes", len(b))
	}
}
