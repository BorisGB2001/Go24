package telephony

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRenderCallbacks: a form rings its employees with the client's number shown; the one who answers becomes the
// caller of the half calling the client through the line; nobody answered — the client's missed call to the line.
func TestRenderCallbacks(t *testing.T) {
	tr := Trunk{ID: "abcdefab-1234-5678-9abc-def012345678", Number: "+74951234567"}
	cb := Callback{ID: "11111111-2222-3333-4444-555555555555", Name: "Сайт; <главный>", Trunk: tr.ID, Ring: []string{"u1", "gone", "u2"}, RingMode: "queue"}
	all := Callback{ID: "22222222-2222-3333-4444-555555555555", Name: "Лендинг", Trunk: tr.ID, Ring: []string{"u1", "u2"}}
	lost := Callback{ID: "33333333-2222-3333-4444-555555555555", Name: "Без линии", Trunk: "none", Ring: []string{"u1"}}
	got := RenderCallbacks([]Callback{cb, all, lost}, map[string]Trunk{tr.ID: tr}, map[string]string{"u1": "101", "u2": "102"}, true)
	k := tr.Key()
	for _, want := range []string{
		"; Сайт главный\n[go24-cb-111111112222]\nexten => _X.,1,Set(GO24_CBNUM=${EXTEN})\n same => n,Goto(s,1)\nexten => _+X.,1,Set(GO24_CBNUM=${EXTEN})\n",
		"exten => s,1,Set(GO24_CB=${CUT(CHANNEL,\\;,1)}\\;1)\n same => n,Set(CALLERID(num)=${GO24_CBNUM})\n same => n,Set(CALLERID(name)=Сайт главный)\n",
		" same => n,Dial(Local/101@go24-ring,20,U(go24-cb-who^${GO24_CB}))\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n" +
			" same => n,Dial(Local/102@go24-ring,20,U(go24-cb-who^${GO24_CB}))\n",
		" same => n,Set(GO24_FROM=${GO24_CBNUM})\n same => n,Set(GO24_TO=+74951234567)\n same => n,Set(GO24_LINE=+74951234567)\n same => n,Set(GO24_BY=101)\n" +
			" same => n,Set(CHANNEL(hangup_handler_push)=go24-cdr,s,1)\n same => n(done),Hangup()\n",
		"[go24-cb-111111112222-out]\nexten => _X.,1,Set(GO24_TO=${EXTEN})\n",
		"exten => s,1,Set(GO24_LINE=+74951234567)\n same => n,Set(GO24_FROM=${SHARED(GO24_FROM)})\n same => n,Set(GO24_RECORD=1)\n same => n,Set(CHANNEL(hangup_handler_push)=go24-cdr,s,1)\n" +
			" same => n,Set(CHANNEL(hangup_handler_push)=go24-cb-end,s,1)\n same => n,Set(CALLERID(all)=+74951234567 <+74951234567>)\n" +
			" same => n,Gosub(go24-record,s,1)\n same => n,Dial(PJSIP/${GO24_TO}@" + k + ",60)\n",
		"[go24-cb-222222222222]\n", " same => n,Dial(Local/101@go24-ring&Local/102@go24-ring,45,U(go24-cb-who^${GO24_CB}))\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "go24-cb-333333332222") || strings.Count(got, "@go24-ring") != 4 {
		t.Fatalf("a form without its line, or an employee without a number:\n%s", got)
	}
}

// TestRenderRecordNotice: with recording on, a line telling about it plays its notice to an incoming call (in the
// working hours, before anyone rings) and to the client answering an outgoing call or a call back; recording off or
// no notice — nothing.
func TestRenderRecordNotice(t *testing.T) {
	tr := Trunk{ID: "abcdefab-1234-5678-9abc-def012345678", Number: "+74951234567", Ring: []string{"u1"}, OutAll: true,
		WorkOn: true, WorkDays: []int{1, 2, 3, 4, 5}, WorkFrom: "09:00", WorkTo: "18:00", NoAnswer: ActHangup, OffHours: ActHangup,
		RecNoteOn: true, RecNoteSum: "n1"}
	note := GreetingFile("n1")
	numbers := map[string]string{"u1": "101"}
	_, d := RenderTrunks([]Trunk{tr}, numbers, nil, nil, nil, true, "")
	for _, want := range []string{
		" same => n(work),NoOp()\n same => n,Answer()\n same => n,Playback(" + note + ")\n same => n,Dial(Local/101@go24-ring,45)\n",
		"exten => _X./101,1,Set(GO24_RECNOTE=" + note + ")\n same => n,Gosub(go24-dial-out,s,1(" + tr.Key() + ",+74951234567,${EXTEN}))\n",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("no %q in\n%s", want, d)
		}
	}
	cb := Callback{ID: "11111111-2222-3333-4444-555555555555", Name: "Сайт", Trunk: tr.ID, Ring: []string{"u1"}}
	if got := RenderCallbacks([]Callback{cb}, map[string]Trunk{tr.ID: tr}, numbers, true); !strings.Contains(got, ",60,A("+note+"))\n") {
		t.Fatalf("the call back without the notice:\n%s", got)
	}
	for _, x := range []struct {
		tr     Trunk
		record bool
	}{{tr, false}, {func() Trunk { y := tr; y.RecNoteOn = false; return y }(), true}, {func() Trunk { y := tr; y.RecNoteSum = ""; return y }(), true}} {
		_, d := RenderTrunks([]Trunk{x.tr}, numbers, nil, nil, nil, x.record, "")
		cbs := RenderCallbacks([]Callback{cb}, map[string]Trunk{tr.ID: x.tr}, numbers, x.record)
		if strings.Contains(d, "Playback(") || strings.Contains(d, "GO24_RECNOTE") || strings.Contains(cbs, ",A(") {
			t.Fatalf("a notice while it should not be played (%+v, %v):\n%s\n%s", x.tr, x.record, d, cbs)
		}
	}
}

// TestWorking: a line takes calls in its working hours (the portal's time zone) or always.
func TestWorking(t *testing.T) {
	tr := Trunk{WorkOn: true, WorkDays: []int{1, 2, 3, 4, 5}, WorkFrom: "09:00", WorkTo: "18:00"}
	for at, want := range map[time.Time]bool{
		time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local):   true,  // Monday
		time.Date(2026, 10, 26, 17, 59, 0, 0, time.Local): true,  //
		time.Date(2026, 10, 26, 18, 0, 0, 0, time.Local):  false, //
		time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local):  false, // Sunday
	} {
		if tr.Working(at) != want {
			t.Fatalf("working at %s: %v", at, !want)
		}
	}
	if !(Trunk{}).Working(time.Date(2026, 10, 25, 3, 0, 0, 0, time.Local)) {
		t.Fatal("a line without working hours is off")
	}
}

// TestCallbacks: the forms kept and checked; a number's request stands a while; a form gone with its line.
func TestCallbacks(t *testing.T) {
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
	tid, err := s.SaveTrunk(ctx, ann, Trunk{Name: "Основной", Host: "sip.example.ru", Username: "go24", Password: "p", Number: "+74951234567", OutAll: true})
	must(err)
	var ie *i18n.Error
	for _, bad := range []Callback{
		{Name: " ", Trunk: tid, Ring: []string{ann}},
		{Name: "Сайт", Trunk: tid},
		{Name: "Сайт", Trunk: "00000000-0000-0000-0000-000000000000", Ring: []string{ann}},
		{Name: "Сайт", Trunk: tid, Ring: []string{ann}, Button: strings.Repeat("я", 61)},
	} {
		if _, err := s.SaveCallback(ctx, ann, bad); !errors.As(err, &ie) {
			t.Fatalf("a bad form saved: %+v: %v", bad, err)
		}
	}
	id, err := s.SaveCallback(ctx, ann, Callback{Name: " Сайт ", Trunk: tid, Ring: []string{ann}, RingMode: "dance", Title: "Перезвоним"})
	must(err)
	cbs, err := s.Callbacks(ctx)
	must(err)
	if len(cbs) != 1 || cbs[0].Name != "Сайт" || cbs[0].RingMode != "all" || len(cbs[0].Token) != 32 || cbs[0].Ring[0] != ann {
		t.Fatalf("the forms: %+v", cbs)
	}
	if c, err := s.CallbackByToken(ctx, cbs[0].Token); err != nil || c.ID != id || c.Title != "Перезвоним" {
		t.Fatalf("by token: %+v %v", c, err)
	}
	if _, err := s.CallbackByToken(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a form of a bad token: %v", err)
	}
	if _, err := s.SaveCallback(ctx, ann, Callback{ID: id, Name: "Сайт", Trunk: tid, Ring: []string{ann}, RingMode: "queue"}); err != nil {
		t.Fatal(err)
	}
	// a number asks once in a while
	rid, err := s.AddCallbackRequest(ctx, id, CallbackRequest{Number: "+79001234567", Name: "Иван", Status: CallbackCalling})
	must(err)
	if _, err := s.AddCallbackRequest(ctx, id, CallbackRequest{Number: "+79001234567", Status: CallbackCalling}); !errors.Is(err, ErrCallbackRecent) {
		t.Fatalf("a request twice: %v", err)
	}
	_, err = s.AddCallbackRequest(ctx, id, CallbackRequest{Number: "+79001234568", Status: CallbackLater})
	must(err)
	must(s.SetCallbackStatus(ctx, rid, CallbackLater))
	reqs, err := s.CallbackRequests(ctx, id, 10)
	must(err)
	if len(reqs) != 2 || reqs[1].Name != "Иван" || reqs[1].Status != CallbackLater {
		t.Fatalf("the requests: %+v", reqs)
	}
	// «Оценка разговора»: asked only with an invitation; the rating of an answered call kept, counted in the statistics
	if err := s.SetTrunkRating(ctx, tid, true, nil, false); !errors.As(err, &ie) {
		t.Fatalf("rating without an invitation: %v", err)
	}
	if trunks, _ := s.Trunks(ctx); trunks[0].RateOn || trunks[0].RateSum != "" {
		t.Fatalf("rating on without an invitation: %+v", trunks[0])
	}
	must(s.SetTrunkRating(ctx, tid, true, testWAV(8000, 1, 8000), false))
	if trunks, _ := s.Trunks(ctx); !trunks[0].RateOn || trunks[0].RateSum == "" {
		t.Fatalf("rating on: %+v", trunks[0])
	}
	if g, err := s.TrunkRatePrompt(ctx, tid); err != nil || len(g) < 44 {
		t.Fatalf("the invitation: %d %v", len(g), err)
	}
	// «Предупреждать о записи разговора»: the same — only with a notice, dropped with it
	if err := s.SetTrunkRecordNotice(ctx, tid, true, nil, false); !errors.As(err, &ie) {
		t.Fatalf("a notice of recording without its file: %v", err)
	}
	must(s.SetTrunkRecordNotice(ctx, tid, true, testWAV(8000, 1, 8000), false))
	if trunks, _ := s.Trunks(ctx); !trunks[0].RecNoteOn || trunks[0].RecNoteSum == "" {
		t.Fatalf("the notice of recording on: %+v", trunks[0])
	}
	if g, err := s.TrunkRecordNotice(ctx, tid); err != nil || len(g) < 44 {
		t.Fatalf("the notice of recording: %d %v", len(g), err)
	}
	must(s.SetTrunkRecordNotice(ctx, tid, false, nil, true))
	if trunks, _ := s.Trunks(ctx); trunks[0].RecNoteOn || trunks[0].RecNoteSum != "" || !trunks[0].RateOn {
		t.Fatalf("the notice of recording dropped: %+v", trunks[0])
	}
	must(s.SetNumber(ctx, ann, "101"))
	t0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	for i, c := range []CDR{
		{UniqueID: "r.1", From: "+79001112233", To: "+74951234567", By: "101", Line: "+74951234567", Dial: "ANSWER", Rating: 5},
		{UniqueID: "r.2", From: "+79001112234", To: "+74951234567", By: "101", Line: "+74951234567", Dial: "ANSWER", Rating: 4},
		{UniqueID: "r.3", From: "+79001112235", To: "+74951234567", By: "101", Line: "+74951234567", Dial: "ANSWER", Rating: 9},
		{UniqueID: "r.4", From: "+79001112236", To: "+74951234567", By: "101", Line: "+74951234567", Dial: "NOANSWER", Rating: 3},
	} {
		c.Start, c.Answer, c.End = t0.Add(time.Duration(i)*time.Minute), t0.Add(time.Duration(i)*time.Minute+time.Second), t0.Add(time.Duration(i+1)*time.Minute)
		if c.Dial != "ANSWER" {
			c.Answer = time.Time{}
		}
		_, err := s.SaveCall(ctx, c)
		must(err)
	}
	if got, _ := s.Calls(ctx, CallFilter{Number: "+7900111223"}); len(got) != 4 || got[3].Rating != 5 || got[1].Rating != 0 || got[0].Rating != 0 {
		t.Fatalf("the ratings: %+v", got)
	}
	st, err := s.Stats(ctx, StatFilter{From: t0.Add(-time.Hour), To: t0.Add(time.Hour)}, time.UTC)
	must(err)
	if st.Total.Rated != 2 || st.Total.AvgRating() != "4.5" || (StatRow{}).AvgRating() != "" {
		t.Fatalf("the ratings in the statistics: %+v", st.Total)
	}
	must(s.SetTrunkRating(ctx, tid, false, nil, true))
	if trunks, _ := s.Trunks(ctx); trunks[0].RateOn || trunks[0].RateSum != "" {
		t.Fatalf("the invitation dropped: %+v", trunks[0])
	}
	// the line gone — the form with it
	must(s.DeleteTrunk(ctx, tid))
	if cbs, _ := s.Callbacks(ctx); len(cbs) != 0 {
		t.Fatalf("a form without its line: %+v", cbs)
	}
	if err := s.DeleteCallback(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}

// TestWorkingHolidays: a holiday of the line («31.12» every year, «2027-01-07» once) is out of its working hours; the hours
// are the line's own time zone's.
func TestWorkingHolidays(t *testing.T) {
	tr := Trunk{WorkOn: true, WorkDays: []int{1, 2, 3, 4, 5}, WorkFrom: "09:00", WorkTo: "18:00", WorkTZ: "Asia/Tokyo",
		Holidays: []string{"12-31", "2027-01-07"}}
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	for at, want := range map[time.Time]bool{
		time.Date(2026, 12, 30, 10, 0, 0, 0, tokyo):    true,  // Wednesday
		time.Date(2026, 12, 31, 10, 0, 0, 0, tokyo):    false, // Thursday, every year
		time.Date(2027, 1, 7, 10, 0, 0, 0, tokyo):      false, // Thursday, once
		time.Date(2028, 1, 7, 10, 0, 0, 0, tokyo):      true,  // Friday
		time.Date(2026, 12, 30, 1, 0, 0, 0, time.UTC):  true,  // 10:00 in Tokyo
		time.Date(2026, 12, 30, 10, 0, 0, 0, time.UTC): false, // 19:00 in Tokyo
	} {
		if tr.Working(at) != want {
			t.Errorf("working at %s: %v", at, !want)
		}
	}
	if s := tr.HolidaysText(); s != "31.12\n07.01.2027" {
		t.Errorf("holidays as written: %q", s)
	}
}

// TestRenderHolidays: the line's holidays (go24-hol-K) go as out of its working hours, checked in its time zone.
func TestRenderHolidays(t *testing.T) {
	tr := Trunk{ID: "11112222-3333-4444-5555-666677778888", Name: "Офис", Host: "sip.example.ru", Username: "u", Number: "+74951234567", WorkOn: true,
		WorkDays: []int{1, 2, 3, 4, 5}, WorkFrom: "09:00", WorkTo: "18:00", Holidays: []string{"01-01", "2026-12-31"}}
	k := tr.Key()
	_, d := RenderTrunks([]Trunk{tr}, nil, nil, nil, nil, false, "Europe/Amsterdam")
	for _, want := range []string{
		" same => n,GotoIf($[${DIALPLAN_EXISTS(go24-hol-" + k + ",${STRFTIME(${EPOCH},Europe/Amsterdam,%m%d)},1)} | " +
			"${DIALPLAN_EXISTS(go24-hol-" + k + ",${STRFTIME(${EPOCH},Europe/Amsterdam,%Y%m%d)},1)}]?off)\n same => n,GotoIfTime(09:00-18:00,mon&tue&wed&thu&fri,*,*,Europe/Amsterdam?work:off)",
		"\n[go24-hol-" + k + "]\nexten => 0101,1,NoOp()\nexten => 20261231,1,NoOp()\n",
	} {
		if !strings.Contains(d, want) {
			t.Fatalf("no %q in\n%s", want, d)
		}
	}
	tr.WorkTZ = "Asia/Tokyo"
	if _, d = RenderTrunks([]Trunk{tr}, nil, nil, nil, nil, false, ""); !strings.Contains(d, "${STRFTIME(${EPOCH},Asia/Tokyo,%m%d)}") ||
		!strings.Contains(d, "*,*,Asia/Tokyo?work:off") {
		t.Fatalf("the line's time zone:\n%s", d)
	}
	tr.Holidays = nil
	if _, d = RenderTrunks([]Trunk{tr}, nil, nil, nil, nil, false, ""); strings.Contains(d, "go24-hol-") {
		t.Fatalf("holidays without any:\n%s", d)
	}
}
