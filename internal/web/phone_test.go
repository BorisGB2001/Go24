package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sites"
	"go24/internal/speech"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestPhone: the softphone window is for an employee with an inner number (on plain http it goes to the https
// address), it names the browser's softphone by a cookie; «Позвонить» of a page goes to that window, the window's
// line goes to the user's pages with who is on the other end; its SIP goes over the WebSocket to Asterisk without
// go24's cookies; without a number — nothing of it.
func TestPhone(t *testing.T) {
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
	var anna, bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, anna, "101"))
	must(store.SetNumber(ctx, bob, "102"))
	must(store.SetEnabled(ctx, anna, true))

	// Asterisk's http server: the WebSocket of SIP (it only says what it got)
	got := make(chan *http.Request, 1)
	ast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r
		c, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer c.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Protocol: sip\r\n\r\n")
		buf.Flush()
	}))
	defer ast.Close()

	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, pages: pages, CallURL: "https://go24.test:8444",
		Telephony: &telephony.PBX{Store: store, SIPHost: "192.168.1.15", ARI: &telephony.ARI{URL: ast.URL, User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	session := func(who string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who}))
		return rec.Result().Cookies()[0]
	}
	do := func(who, method, path string, form url.Values, hdr map[string]string, cookies ...*http.Cookie) (*http.Response, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		req.AddCookie(session(who))
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res, string(raw)
	}
	https := map[string]string{"X-Forwarded-Proto": "https"}

	// the window: plain http goes to https; there — the account of the softphone and the cookie naming it
	if res, _ := do(anna, "GET", "/phone?dial=102", nil, nil); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "https://go24.test:8444/phone?dial=102" {
		t.Fatalf("http: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	res, page := do(anna, "GET", "/phone?dial=102", nil, https)
	if res.StatusCode != http.StatusOK || !strings.Contains(page, `data-login="w101"`) || !strings.Contains(page, `data-dial="102"`) || strings.Contains(page, `class="side"`) {
		t.Fatalf("the window: %d\n%s", res.StatusCode, page)
	}
	var phone *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == phoneCookie {
			phone = c
		}
	}
	if phone == nil || !validPhoneID(phone.Value) || !strings.Contains(page, `data-id="`+phone.Value+`"`) {
		t.Fatalf("no cookie of the softphone: %v", phone)
	}
	if res, _ := do(anna, "GET", "/phone", nil, https, phone); !strings.Contains(res.Header.Get("Set-Cookie"), phone.Value) {
		t.Fatal("the softphone got another name")
	}
	// eve has no number: no window, no commands, no socket
	if res, _ := do(eve, "GET", "/phone", nil, https); res.StatusCode == http.StatusOK {
		t.Fatal("eve has a softphone")
	}
	if res, _ := do(eve, "POST", "/telephony/cmd", url.Values{"cmd": {"dial"}, "number": {"101"}}, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve's command: %d", res.StatusCode)
	}
	if res, _ := do(eve, "GET", "/telephony/ws", nil, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve's socket: %d", res.StatusCode)
	}
	conn, err := pool.Acquire(ctx)
	must(err)
	defer conn.Release()
	_, err = conn.Exec(ctx, "LISTEN go24_notify")
	must(err)
	next := func() (string, map[string]any) {
		t.Helper()
		wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		n, err := conn.Conn().WaitForNotification(wctx)
		must(err)
		key, value, _ := strings.Cut(n.Payload, ":")
		var v map[string]any
		must(json.Unmarshal([]byte(value), &v))
		return key, v
	}

	// «Позвонить» of a page goes to the browser's softphone; a number as people write it
	res, out := do(anna, "POST", "/telephony/cmd", url.Values{"cmd": {"dial"}, "number": {"+7 (900) 123-45-67"}}, nil, phone)
	if res.StatusCode != http.StatusOK || !strings.Contains(out, `"sent": "`+phone.Value+`"`) {
		t.Fatalf("dial: %d %s", res.StatusCode, out)
	}
	if key, v := next(); key != "p"+anna+"."+phone.Value || v["cmd"] != "dial" || v["number"] != "+79001234567" {
		t.Fatalf("dial went: %s %v", key, v)
	}
	if res, _ := do(anna, "POST", "/telephony/cmd", url.Values{"cmd": {"dial"}, "number": {"101; rm"}}, nil, phone); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad number: %d", res.StatusCode)
	}
	// no softphone in this browser yet: the page is told to open it
	if _, out := do(anna, "POST", "/telephony/cmd", url.Values{"cmd": {"dial"}, "number": {"102"}}, nil); strings.Contains(out, "sent") || !strings.Contains(out, `"open": "https://go24.test:8444/phone"`) {
		t.Fatalf("no window: %s", out)
	}

	// the window's line: bob rings anna — her pages learn who it is
	res, out = do(anna, "POST", "/telephony/state", url.Values{"phone": {phone.Value}, "state": {"ringing"}, "number": {"102"}, "dir": {"in"}}, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(out, `"from": "Борис"`) {
		t.Fatalf("state: %d %s", res.StatusCode, out)
	}
	if key, v := next(); key != "c"+anna || v["kind"] != "phone" || v["state"] != "ringing" || v["user"] != bob || v["url"] != "/u/"+bob || v["phone"] != phone.Value {
		t.Fatalf("the line went: %s %v", key, v)
	}
	if res, _ := do(anna, "POST", "/telephony/state", url.Values{"state": {"exploded"}}, nil); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad state: %d", res.StatusCode)
	}

	// SIP over the WebSocket: to Asterisk's /ws, the upgrade passes, go24's session does not
	raw, err := net.Dial("tcp", strings.TrimPrefix(hs.URL, "http://"))
	must(err)
	defer raw.Close()
	req, _ := http.NewRequest("GET", hs.URL+"/telephony/ws?x=1", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Protocol", "sip")
	req.AddCookie(session(anna))
	must(req.Write(raw))
	wres, err := http.ReadResponse(bufio.NewReader(raw), req)
	must(err)
	if wres.StatusCode != http.StatusSwitchingProtocols || wres.Header.Get("Sec-WebSocket-Protocol") != "sip" {
		t.Fatalf("the socket: %d %v", wres.StatusCode, wres.Header)
	}
	ar := <-got
	if ar.URL.Path != "/ws" || ar.URL.RawQuery != "" || ar.Header.Get("Cookie") != "" || ar.Header.Get("Sec-WebSocket-Protocol") != "sip" {
		t.Fatalf("asterisk got: %s %v", ar.URL, ar.Header)
	}

	// Asterisk tells about a call that ended: only with its key; the conversation is in the folder of recordings
	srv.Telephony.Key, srv.Telephony.Records = "astkey", t.TempDir()
	must(os.WriteFile(srv.Telephony.Records+"/r1.wav", []byte("RIFFwav"), 0o644))
	must(os.WriteFile(srv.Telephony.Records+"/r2.wav", []byte("RIFFsilence"), 0o644))
	cdr := func(form url.Values) int {
		res, err := http.PostForm(hs.URL+"/telephony/cdr", form)
		must(err)
		res.Body.Close()
		return res.StatusCode
	}
	call := url.Values{"key": {"astkey"}, "id": {"1729.1"}, "from": {"102"}, "to": {"101"}, "start": {"1761400000.5"}, "answer": {"1761400004.000000"},
		"end": {"1761400064"}, "dial": {"ANSWER"}, "rec": {"r1.wav"}}
	if st := cdr(url.Values{"key": {"wrong"}, "id": {"x"}, "start": {"1"}}); st != http.StatusForbidden {
		t.Fatalf("a call without the key: %d", st)
	}
	if st := cdr(call); st != http.StatusNoContent {
		t.Fatalf("the call: %d", st)
	}
	if st := cdr(url.Values{"key": {"astkey"}, "id": {"1729.2"}, "from": {"101"}, "to": {"102"}, "start": {"1761400100"}, "answer": {"0.000000"},
		"end": {"1761400110"}, "dial": {"NOANSWER"}, "rec": {"r2.wav"}}); st != http.StatusNoContent {
		t.Fatalf("a missed call: %d", st)
	}
	if _, err := os.Stat(srv.Telephony.Records + "/r2.wav"); !os.IsNotExist(err) {
		t.Fatal("the silence of a missed call is kept")
	}
	// the log: anna sees her calls with the recording, eve sees none of them and cannot listen
	res, page = do(anna, "GET", "/telephony/calls", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "Борис") || !strings.Contains(page, "1:00") || strings.Count(page, "/record\"") != 1 {
		t.Fatalf("anna's log: %d\n%s", res.StatusCode, page)
	}
	calls, err := store.Calls(ctx, telephony.CallFilter{UserID: anna})
	must(err)
	if len(calls) != 2 || calls[1].Record != "r1.wav" || calls[0].Status != telephony.StatusNoAnswer {
		t.Fatalf("the log: %+v", calls)
	}
	if res, body := do(anna, "GET", "/telephony/calls/"+calls[1].ID+"/record", nil, nil); res.StatusCode != http.StatusOK || body != "RIFFwav" || res.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("listen: %d %q", res.StatusCode, body)
	}
	if res, _ := do(eve, "GET", "/telephony/calls/"+calls[1].ID+"/record", nil, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve listens: %d", res.StatusCode)
	}
	if _, p := do(eve, "GET", "/telephony/calls?user="+anna, nil, nil); strings.Contains(p, "Борис") {
		t.Fatal("eve sees anna's calls")
	}
	// «Расшифровка»: the record's text by Whisper, for those who may listen; by itself after the call when switched on
	heard := make(chan string, 4)
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, h, err := r.FormFile("audio_file")
		if err != nil || r.URL.Path != "/asr" || r.URL.Query().Get("language") != "ru" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		f.Close()
		heard <- h.Filename
		io.WriteString(w, "Добрый день, это Анна.\n")
	}))
	defer ws.Close()
	srv.Speech = speech.New(ws.URL)
	if res, _ := do(eve, "POST", "/telephony/calls/"+calls[1].ID+"/transcript", url.Values{}, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve transcribes: %d", res.StatusCode)
	}
	if res, _ := do(anna, "POST", "/telephony/calls/"+calls[1].ID+"/transcript", url.Values{}, map[string]string{"Referer": hs.URL + "/telephony/calls?kind=in"}); res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/telephony/calls?kind=in" || <-heard != "r1.wav" {
		t.Fatalf("anna transcribes: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if _, p := do(anna, "GET", "/telephony/calls", nil, nil); !strings.Contains(p, "Добрый день, это Анна.") || strings.Count(p, "/transcript\"") != 0 {
		t.Fatal("the transcript not in the log")
	}
	must(store.SetTranscribing(ctx, anna, true))
	must(os.WriteFile(srv.Telephony.Records+"/r9.wav", []byte("RIFFauto"), 0o644))
	if st := cdr(url.Values{"key": {"astkey"}, "id": {"1729.9"}, "from": {"101"}, "to": {"102"}, "start": {"1761400300"}, "answer": {"1761400302"},
		"end": {"1761400330"}, "dial": {"ANSWER"}, "rec": {"r9.wav"}}); st != http.StatusNoContent {
		t.Fatalf("a recorded call: %d", st)
	}
	select {
	case name := <-heard:
		if name != "r9.wav" {
			t.Fatalf("transcribed by itself: %s", name)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the new record not transcribed by itself")
	}
	for i := 0; ; i++ {
		got, _ := store.Calls(ctx, telephony.CallFilter{UserID: anna, Number: "102"})
		if len(got) > 0 && got[0].Record == "r9.wav" && got[0].Transcript != "" {
			break
		}
		if i == 50 {
			t.Fatalf("the automatic transcript not kept: %+v", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
	must(store.SetTranscribing(ctx, anna, false))
	srv.Speech = nil
	// «Статистика звонков»: the calls of the lines; eve sees only her own
	if st := cdr(url.Values{"key": {"astkey"}, "id": {"1729.3"}, "from": {"+79001112200"}, "to": {"+74951234567"}, "by": {"101"}, "line": {"+74951234567"},
		"start": {"1761400200"}, "answer": {"1761400204"}, "end": {"1761400264"}, "dial": {"ANSWER"}}); st != http.StatusNoContent {
		t.Fatalf("a call from the line: %d", st)
	}
	res, page = do(anna, "GET", "/telephony/stats?from=2025-10-31&to=2025-10-01", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(page, "Статистика звонков") || !strings.Contains(page, `value="2025-10-01"`) ||
		!strings.Contains(page, "/telephony/calls?from=2025-10-01&amp;to=2025-10-31&amp;user="+anna) || !strings.Contains(page, "<b>1:00</b>") {
		t.Fatalf("anna's stats: %d\n%s", res.StatusCode, page)
	}
	if _, p := do(eve, "GET", "/telephony/stats?from=2025-10-01&to=2025-10-31&user="+anna, nil, nil); strings.Contains(p, "user="+anna) || !strings.Contains(p, "Ваши звонки") {
		t.Fatal("eve sees anna's stats")
	}

	// CRM: a client calling goes to their card; an unknown number calling makes a lead; one's own call to an unknown
	// number makes nothing until «Создать сделку»
	srv.CRM = crm.NewStore(pool)
	contact, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "ООО Ромашка", Phone: "+7 (900) 111-22-33", Responsible: crm.Ref{ID: anna}})
	must(err)
	must(os.WriteFile(srv.Telephony.Records+"/r3.wav", []byte("RIFFclient"), 0o644))
	// «сначала ответственному»: Asterisk asks whose client calls — the responsible's inner number, only with its key
	owner := func(key, from string) (int, string) {
		t.Helper()
		res, err := http.PostForm(hs.URL+"/telephony/owner", url.Values{"key": {key}, "from": {from}})
		must(err)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(b)
	}
	if st, n := owner("astkey", "89001112233"); st != http.StatusOK || n != "101" {
		t.Fatalf("the client's responsible: %d %q", st, n)
	}
	if st, n := owner("astkey", "+74950000009"); st != http.StatusOK || n != "" {
		t.Fatalf("an unknown number's responsible: %d %q", st, n)
	}
	if st, n := owner("wrong", "89001112233"); st != http.StatusForbidden || n == "101\n" || strings.Contains(n, "101") {
		t.Fatalf("without the key: %d %q", st, n)
	}
	in := func(id, from, to, dial, rec string) {
		t.Helper()
		v := url.Values{"key": {"astkey"}, "id": {id}, "from": {from}, "to": {to}, "start": {"1761401000"}, "end": {"1761401090"}, "dial": {dial}, "rec": {rec}}
		if dial == "ANSWER" {
			v.Set("answer", "1761401010")
		}
		if st := cdr(v); st != http.StatusNoContent {
			t.Fatalf("cdr %s: %d", id, st)
		}
	}
	timeline := func(entity, id string) string {
		t.Helper()
		evs, err := srv.CRM.Timeline(ctx, entity, id)
		must(err)
		var b strings.Builder
		for _, e := range evs {
			if e.Kind == "call" {
				b.WriteString(e.Body + "\n")
			}
		}
		return b.String()
	}
	in("c.1", "89001112233", "101", "ANSWER", "r3.wav")
	if tl := timeline("contact", contact); !strings.Contains(tl, `"dir":"in"`) || !strings.Contains(tl, `"sec":80`) || !strings.Contains(tl, `"rec":true`) {
		t.Fatalf("the client's timeline: %q", tl)
	}
	in("c.2", "+7 495 000-00-01", "101", "NOANSWER", "")
	leads, err := srv.CRM.Duplicates(ctx, "lead", "", "", "+74950000001", "")
	must(err)
	if len(leads) != 1 || leads[0].Entity != "lead" || leads[0].Title != "Звонок: +7 495 000-00-01" {
		t.Fatalf("the lead of a new number: %+v", leads)
	}
	if tl := timeline("lead", leads[0].ID); !strings.Contains(tl, `"dir":"missed"`) {
		t.Fatalf("the lead's timeline: %q", tl)
	}
	must(store.SetLeadAuto(ctx, anna, false))
	in("c.3", "+7 495 000-00-02", "101", "NOANSWER", "")
	if l, _ := srv.CRM.Duplicates(ctx, "lead", "", "", "+74950000002", ""); len(l) != 0 {
		t.Fatal("a lead though the setting is off")
	}
	in("c.4", "101", "+7 812 000-00-03", "ANSWER", "")
	all, err := store.Calls(ctx, telephony.CallFilter{UserID: anna})
	must(err)
	var outCall telephony.Call
	for _, c := range all {
		if c.To == "+7 812 000-00-03" {
			outCall = c
		}
		if c.From == "89001112233" && (c.CRMEntity != "contact" || c.CRMName != "ООО Ромашка") {
			t.Fatalf("the log has no client: %+v", c)
		}
	}
	if outCall.ID == "" || outCall.CRMEntity != "" {
		t.Fatalf("an outgoing call to a new number: %+v", outCall)
	}
	if res, _ := do(eve, "POST", "/telephony/calls/"+outCall.ID+"/crm", url.Values{"what": {"deal"}}, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve makes a deal of anna's call: %d", res.StatusCode)
	}
	res, _ = do(anna, "POST", "/telephony/calls/"+outCall.ID+"/crm", url.Values{"what": {"deal"}}, nil)
	deal, _ := strings.CutPrefix(res.Header.Get("Location"), "/crm/deals/")
	if res.StatusCode != http.StatusSeeOther || deal == "" || !strings.Contains(timeline("deal", deal), `"dir":"out"`) {
		t.Fatalf("the deal of a call: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if res, _ := do(anna, "POST", "/telephony/calls/"+outCall.ID+"/crm", url.Values{"what": {"lead"}}, nil); res.Header.Get("Location") != "/crm/deals/"+deal {
		t.Fatalf("a second card of one call: %s", res.Header.Get("Location"))
	}
	// the softphone rings with a client: their name and card
	_, out2 := do(anna, "POST", "/telephony/state", url.Values{"phone": {phone.Value}, "state": {"ringing"}, "number": {"+79001112233"}, "dir": {"in"}}, nil)
	if !strings.Contains(out2, `"from": "ООО Ромашка"`) || !strings.Contains(out2, `"url": "/crm/contacts/`+contact+`"`) {
		t.Fatalf("a client rings: %s", out2)
	}
	// «Карточка звонка»: the client with open deals and the calls before, «Создать лид» for an unknown number; the
	// comment written while talking waits for the call, then goes to it and its «Звонок» in the CRM card
	cardOf := func(who, number string) map[string]any {
		t.Helper()
		res, body := do(who, "GET", "/telephony/card?number="+url.QueryEscape(number), nil, nil)
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("card %s: %d %s", number, res.StatusCode, body)
		}
		return m
	}
	if res, _ := do(eve, "GET", "/telephony/card?number=89001112233", nil, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve without a number opens a card: %d", res.StatusCode)
	}
	card := cardOf(anna, "89001112233")
	cl, _ := card["client"].(map[string]any)
	if cl["name"] != "ООО Ромашка" || cl["url"] != "/crm/contacts/"+contact || cl["kind"] != "Контакт" || len(card["calls"].([]any)) != 1 {
		t.Fatalf("the client's card: %v", card)
	}
	if cl, _ := cardOf(anna, "+7 812 000-00-03")["client"].(map[string]any); cl == nil || len(cl["deals"].([]any)) != 1 {
		t.Fatalf("the deal of the client: %v", cl)
	}
	if c := cardOf(anna, "+74950000077"); c["client"] != nil || len(c["create"].([]any)) != 2 {
		t.Fatalf("an unknown number's card: %v", c)
	}
	if c := cardOf(anna, "102"); c["colleague"] != true || c["calls"] != nil {
		t.Fatalf("a colleague's card: %v", c)
	}
	at := strconv.FormatInt(int64(card["at"].(float64)), 10)
	note := func(text string) string {
		t.Helper()
		_, body := do(anna, "POST", "/telephony/note", url.Values{"number": {"+7 900 111-22-33"}, "at": {at}, "comment": {text}}, nil)
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil || m["saved"] != true {
			t.Fatalf("the note: %s", body)
		}
		return m["call"].(string)
	}
	if id := note("Перезвонить в пятницу"); id != "" {
		t.Fatalf("a note of a call still going went to %s", id)
	}
	if c := cardOf(anna, "89001112233"); c["comment"] != "Перезвонить в пятницу" {
		t.Fatalf("the card forgot the note: %v", c["comment"])
	}
	now := time.Now().Unix()
	if st := cdr(url.Values{"key": {"astkey"}, "id": {"c.5"}, "from": {"89001112233"}, "to": {"101"}, "dial": {"ANSWER"}, "start": {strconv.FormatInt(now, 10)},
		"answer": {strconv.FormatInt(now+3, 10)}, "end": {strconv.FormatInt(now+60, 10)}}); st != http.StatusNoContent {
		t.Fatalf("cdr c.5: %d", st)
	}
	if tl := timeline("contact", contact); !strings.Contains(tl, `"note":"Перезвонить в пятницу"`) {
		t.Fatalf("the note in the timeline: %q", tl)
	}
	if id := note("Перезвонить в понедельник"); id == "" {
		t.Fatal("a note after the call is not the call's")
	}
	if tl := timeline("contact", contact); !strings.Contains(tl, "Перезвонить в понедельник") || strings.Contains(tl, "пятницу") {
		t.Fatalf("the changed note in the timeline: %q", tl)
	}
	if _, p := do(anna, "GET", "/telephony/calls", nil, nil); !strings.Contains(p, "Перезвонить в понедельник") {
		t.Fatal("the call log has no comment")
	}
	if c := cardOf(anna, "89001112233"); c["comment"] != "" || c["calls"].([]any)[0].(map[string]any)["comment"] != "Перезвонить в понедельник" {
		t.Fatalf("the card after the call: %v", c)
	}
	note("")
	if tl := timeline("contact", contact); strings.Contains(tl, `"note"`) {
		t.Fatalf("a cleared note in the timeline: %q", tl)
	}
	res, out = do(anna, "POST", "/telephony/card", url.Values{"number": {"+74950000077"}, "what": {"contact"}}, nil)
	if !strings.Contains(out, `"url": "/crm/contacts/`) {
		t.Fatalf("«Создать контакт» of the card: %d %s", res.StatusCode, out)
	}
	if c := cardOf(anna, "+74950000077"); c["client"] == nil || c["create"] != nil {
		t.Fatalf("the card of the contact just made: %v", c)
	}

	// external lines are the administrators'
	line := url.Values{"name": {"Основной"}, "host": {"sip.example.ru"}, "username": {"go24"}, "password": {"pw"}, "number": {"+74951234567"},
		"register": {"1"}, "ring": {anna, bob}, "ring_mode": {"queue"}, "out_all": {"1"}}
	if res, _ := do(eve, "POST", "/telephony/trunks", line, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve connects a line: %d", res.StatusCode)
	}
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Name: "anna", Admin: true, AdminMode: true}))
	req2, _ := http.NewRequest("POST", hs.URL+"/telephony/trunks", strings.NewReader(line.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.AddCookie(rec.Result().Cookies()[0])
	res2, err := client.Do(req2)
	must(err)
	res2.Body.Close()
	trunks, err := store.Trunks(ctx)
	must(err)
	if res2.StatusCode != http.StatusSeeOther || len(trunks) != 1 || trunks[0].RingMode != "queue" || len(trunks[0].Ring) != 2 || !trunks[0].Register {
		t.Fatalf("the administrator's line: %d %+v", res2.StatusCode, trunks)
	}
	// its handling of calls (the form with the greeting's file): working hours, voicemail, a forward
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	for k, vs := range line {
		for _, v := range vs {
			mw.WriteField(k, v)
		}
	}
	for k, v := range map[string]string{"work_on": "1", "work_from": "09:00", "work_to": "18:00", "noanswer": "voicemail", "offhours": "forward",
		"offhours_to": "102"} {
		mw.WriteField(k, v)
	}
	mw.WriteField("work_days", "1")
	mw.WriteField("work_days", "3")
	fw, _ := mw.CreateFormFile("greeting", "hello.wav")
	fw.Write(telephony.Beep())
	mw.Close()
	req3, _ := http.NewRequest("POST", hs.URL+"/telephony/trunks/"+trunks[0].ID, &form)
	req3.Header.Set("Content-Type", mw.FormDataContentType())
	req3.AddCookie(rec.Result().Cookies()[0])
	res3, err := client.Do(req3)
	must(err)
	res3.Body.Close()
	trunks, err = store.Trunks(ctx)
	must(err)
	if l := trunks[0]; res3.StatusCode != http.StatusSeeOther || !l.WorkOn || len(l.WorkDays) != 2 || l.NoAnswer != telephony.ActVoicemail ||
		l.OffHoursTo != "102" || l.GreetingSum == "" || l.Password != "pw" {
		t.Fatalf("the line's handling: %d %+v", res3.StatusCode, l)
	}
	req4, _ := http.NewRequest("GET", hs.URL+"/telephony/trunks/"+trunks[0].ID+"/greeting", nil)
	req4.AddCookie(rec.Result().Cookies()[0])
	res4, err := client.Do(req4)
	must(err)
	res4.Body.Close()
	if res4.StatusCode != http.StatusOK || res4.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("the greeting: %d", res4.StatusCode)
	}
	if res, _ := do(eve, "GET", "/telephony/trunks/"+trunks[0].ID+"/greeting", nil, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve listens to the greeting: %d", res.StatusCode)
	}
	// a voice menu (the administrators'): its items by the form's rows, empty rows skipped; the settings show it
	admin := func(path string, v url.Values) *http.Response {
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := client.Do(req)
		must(err)
		res.Body.Close()
		return res
	}
	menu := url.Values{"name": {"Главное меню"}, "wait": {"5"}, "repeats": {"2"}, "direct": {"1"}, "digit": {"1", "2", "3"},
		"item0_act": {"ring"}, "item0_users": {anna, bob}, "item0_mode": {"queue"}, "item1_act": {"forward"}, "item1_to": {"+7 900 555-00-11"},
		"item2_act": {""}, "timeout_act": {"voicemail"}}
	if res, _ := do(eve, "POST", "/telephony/menus", menu, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve makes a menu: %d", res.StatusCode)
	}
	if res := admin("/telephony/menus", menu); res.StatusCode != http.StatusSeeOther || !strings.HasSuffix(res.Header.Get("Location"), "#menus") {
		t.Fatalf("the administrator's menu: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	menus, err := store.Menus(ctx)
	must(err)
	if len(menus) != 1 || len(menus[0].Items) != 2 || menus[0].Items[0].Mode != "queue" || len(menus[0].Items[0].Users) != 2 ||
		menus[0].Items[1].To != "+79005550011" || menus[0].Timeout.Act != telephony.ActVoicemail || !menus[0].Direct {
		t.Fatalf("the menu: %+v", menus)
	}
	reqP, _ := http.NewRequest("GET", hs.URL+"/settings/telephony", nil)
	reqP.AddCookie(rec.Result().Cookies()[0])
	resP, err := client.Do(reqP)
	must(err)
	body, _ := io.ReadAll(resP.Body)
	resP.Body.Close()
	if !strings.Contains(string(body), `action="/telephony/menus/`+menus[0].ID+`"`) || !strings.Contains(string(body), `<option value="`+menus[0].ID+`"`) {
		t.Fatalf("the menu in the settings: %d", resP.StatusCode)
	}
	if res := admin("/telephony/menus/"+menus[0].ID+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the menu deleted: %d", res.StatusCode)
	}
	if menus, _ = store.Menus(ctx); len(menus) != 0 {
		t.Fatalf("the menu stays: %+v", menus)
	}
	// «Группы очереди»: the administrators'; the line rings its group
	grp := url.Values{"name": {"Продажи"}, "users": {anna, bob}, "strategy": {"evenly"}, "ring": {"15"}, "wait": {"90"}}
	if res, _ := do(eve, "POST", "/telephony/groups", grp, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve makes a group: %d", res.StatusCode)
	}
	if res := admin("/telephony/groups", grp); res.StatusCode != http.StatusSeeOther || !strings.HasSuffix(res.Header.Get("Location"), "#groups") {
		t.Fatalf("the administrator's group: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	groups, err := store.Groups(ctx)
	must(err)
	if len(groups) != 1 || groups[0].Strategy != "evenly" || len(groups[0].Users) != 2 || groups[0].Wait != 90 {
		t.Fatalf("the groups: %+v", groups)
	}
	line.Set("ring_group", groups[0].ID)
	if res := admin("/telephony/trunks/"+trunks[0].ID, line); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the line with a group: %d", res.StatusCode)
	}
	if got, _ := store.Trunks(ctx); got[0].Group != groups[0].ID {
		t.Fatalf("the line's group: %+v", got[0])
	}
	line.Del("ring_group")
	if res := admin("/telephony/groups/"+groups[0].ID+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the group deleted: %d", res.StatusCode)
	}
	// «Оценка разговора» needs an invitation
	line.Set("rate_on", "1")
	if res := admin("/telephony/trunks/"+trunks[0].ID, line); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the line asking for a rating: %d", res.StatusCode)
	}
	if got, _ := store.Trunks(ctx); got[0].RateOn {
		t.Fatalf("rating on without an invitation: %+v", got[0])
	}
	line.Del("rate_on")
	// «Обратный звонок»: the administrators' form; the client's request — out of reach of Asterisk it waits, the
	// employees told to call back, a new number makes a lead; a robot and a number asking again make nothing
	cbForm := url.Values{"name": {"Сайт"}, "trunk": {trunks[0].ID}, "ring": {anna}, "ring_mode": {"all"}, "button": {"Позвоните мне"}}
	if res, _ := do(eve, "POST", "/telephony/callbacks", cbForm, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve makes a callback form: %d", res.StatusCode)
	}
	if res := admin("/telephony/callbacks", cbForm); res.StatusCode != http.StatusSeeOther || !strings.HasSuffix(res.Header.Get("Location"), "#callbacks") {
		t.Fatalf("the administrator's callback form: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	cbs, err := store.Callbacks(ctx)
	must(err)
	if len(cbs) != 1 || cbs[0].Button != "Позвоните мне" {
		t.Fatalf("the callback forms: %+v", cbs)
	}
	if res, p := do("", "GET", "/cb/"+cbs[0].Token+"?embed=1", nil, nil); res.StatusCode != http.StatusOK || !strings.Contains(p, "Позвоните мне") || !strings.Contains(p, "go24form") {
		t.Fatalf("the public callback form: %d\n%s", res.StatusCode, p)
	}
	if res, _ := do("", "GET", "/cb/nope", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Fatalf("a callback form of a bad token: %d", res.StatusCode)
	}
	must(store.SetLeadAuto(ctx, anna, true))
	old := sites.FormStamp(time.Now().Add(-time.Minute))
	ask := func(v url.Values) (int, string) {
		t.Helper()
		res, p := do("", "POST", "/cb/"+cbs[0].Token, v, nil)
		return res.StatusCode, p
	}
	if st, p := ask(url.Values{"t": {old}, "phone": {"12"}}); st != http.StatusUnprocessableEntity || !strings.Contains(p, "Укажите номер телефона") {
		t.Fatalf("a callback to a bad number: %d", st)
	}
	if st, _ := ask(url.Values{"t": {sites.FormStamp(time.Now())}, "phone": {"+7 900 444-55-66"}}); st != http.StatusOK {
		t.Fatalf("a robot's callback: %d", st)
	}
	if st, p := ask(url.Values{"t": {old}, "phone": {"+7 900 444-55-66"}, "name": {"Пётр"}, "page": {"https://example.com/price"}}); st != http.StatusOK || !strings.Contains(p, "скоро перезвоним") {
		t.Fatalf("a callback: %d\n%s", st, p)
	}
	ask(url.Values{"t": {old}, "phone": {"+79004445566"}})
	reqs, err := store.CallbackRequests(ctx, cbs[0].ID, 10)
	must(err)
	if len(reqs) != 1 || reqs[0].Number != "+79004445566" || reqs[0].Name != "Пётр" || reqs[0].Status != telephony.CallbackLater || reqs[0].Page != "https://example.com/price" {
		t.Fatalf("the callback requests: %+v", reqs)
	}
	var calledBack int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'phone' AND body LIKE 'Перезвоните: Пётр, +79004445566%'`, anna).Scan(&calledBack))
	if calledBack != 1 {
		t.Fatalf("told to call back: %d", calledBack)
	}
	if l, _ := srv.CRM.Duplicates(ctx, "lead", "", "", "+79004445566", ""); len(l) != 1 || l[0].Title != "Обратный звонок: +79004445566" {
		t.Fatalf("the callback's lead: %+v", l)
	}
	must(store.SetLeadAuto(ctx, anna, false))
	_, err = pool.Exec(ctx, `DELETE FROM notifications WHERE body LIKE 'Перезвоните:%'`)
	must(err)
	if res, p := do(anna, "GET", "/settings/telephony", nil, nil); res.StatusCode == http.StatusOK && !strings.Contains(p, "/cb/"+cbs[0].Token) {
		t.Fatal("the callback form not in the settings")
	}
	if res := admin("/telephony/callbacks/"+cbs[0].ID+"/delete", nil); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("the callback form deleted: %d", res.StatusCode)
	}
	// a voicemail on the line: the message kept, the employee told; a client who hung up before the beep — a missed call
	must(os.WriteFile(srv.Telephony.Records+"/vm1.wav", make([]byte, 20000), 0o644))
	must(os.WriteFile(srv.Telephony.Records+"/vm2.wav", make([]byte, 100), 0o644))
	for _, v := range []url.Values{
		{"id": {"vm.1"}, "rec": {"vm1.wav"}, "from": {"+79007770011"}},
		{"id": {"vm.2"}, "rec": {"vm2.wav"}, "from": {"+79007770022"}},
	} {
		v.Set("key", "astkey")
		v.Set("to", "+74951234567")
		v.Set("line", "+74951234567")
		v.Set("by", "101")
		v.Set("vm", "1")
		v.Set("dial", "NOANSWER")
		v.Set("start", "1761402000")
		v.Set("answer", "1761402030")
		v.Set("end", "1761402050")
		if st := cdr(v); st != http.StatusNoContent {
			t.Fatalf("voicemail %s: %d", v.Get("id"), st)
		}
	}
	vms, err := store.Calls(ctx, telephony.CallFilter{UserID: anna, Kind: "missed", Number: "+7900777"})
	must(err)
	if len(vms) != 2 || vms[0].Status == vms[1].Status {
		t.Fatalf("voicemails: %+v", vms)
	}
	for _, c := range vms {
		if (c.Status == telephony.StatusVoicemail) != (c.Record == "vm1.wav") {
			t.Fatalf("a voicemail's record: %+v", c)
		}
	}
	if _, err := os.Stat(srv.Telephony.Records + "/vm1.wav"); err != nil {
		t.Fatal("the message removed")
	}
	var told []string
	rows, err := pool.Query(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'phone' ORDER BY id`, anna)
	must(err)
	for rows.Next() {
		var b string
		must(rows.Scan(&b))
		told = append(told, b)
	}
	if len(told) != 2 || !strings.Contains(strings.Join(told, "\n"), "Голосовое сообщение от +79007770011 на линию +74951234567") ||
		!strings.Contains(strings.Join(told, "\n"), "Пропущенный звонок от +79007770022") {
		t.Fatalf("anna told: %q", told)
	}

	// «Мои настройки телефонии» in the profile: the employee keeps their own (not someone else's), the dialplan follows
	if res, _ := do(eve, "POST", "/u/"+anna+"/telephony/prefs", url.Values{"forward": {"always"}, "to": {"+79001112233"}}, nil); res.StatusCode != http.StatusForbidden {
		t.Fatalf("eve set anna's forwarding: %d", res.StatusCode)
	}
	res, _ = do(anna, "POST", "/u/"+anna+"/telephony/prefs", url.Values{"forward": {"noanswer"}, "after": {"25"}, "to": {"+7 900 111-22-33"}}, nil)
	if res.StatusCode != http.StatusSeeOther || !strings.HasSuffix(res.Header.Get("Location"), "#ptel") {
		t.Fatalf("anna's settings: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if p, err := store.PrefsOf(ctx, anna); err != nil || p.Forward != telephony.ForwardNoAnswer || p.After != 25 || p.To != "+79001112233" {
		t.Fatalf("anna's settings kept: %+v %v", p, err)
	}
	if res, _ := do(anna, "POST", "/u/"+anna+"/telephony/prefs", url.Values{"forward": {"always"}}, nil); res.StatusCode != http.StatusSeeOther ||
		!strings.Contains(res.Header.Get("Location"), "msg=") {
		t.Fatalf("forwarding without a mobile: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if p, _ := store.PrefsOf(ctx, anna); p.Forward != telephony.ForwardNoAnswer {
		t.Fatalf("forwarding without a mobile kept: %+v", p)
	}
}
