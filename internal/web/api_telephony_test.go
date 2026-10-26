package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestTelAPI: REST of telephony — the log, a call, its record and comment by «Права доступа» (an employee — their own
// calls, an administrator in admin mode — all), the key needs the scope; «Позвонить» asks Asterisk to ring the key
// owner's phones and put them through (recording when it is on), not for one without a number and not outside for one
// whose outgoing calls are forbidden; the lines, the inner numbers with who is online; the black list is the
// administrators'; a call reported by Asterisk goes to the webhooks of "call.ended".
func TestTelAPI(t *testing.T) {
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
	var anna, bob, dan, admin string
	for _, u := range []struct {
		id   *string
		name string
	}{{&anna, "Анна"}, {&bob, "Борис"}, {&dan, "Дан"}, {&admin, "Админ"}} {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, u.name).Scan(u.id))
	}
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, anna, "101"))
	must(store.SetNumber(ctx, bob, "102"))
	must(store.SetEnabled(ctx, admin, true))
	must(store.SetRecording(ctx, admin, true))
	_, err = store.SaveTrunk(ctx, admin, telephony.Trunk{Name: "Москва", Host: "sip.example.org", Username: "login", Password: "pw",
		Number: "+74950000000", Register: true, RingMode: "all", Ring: []string{anna}, OutAll: true})
	must(err)
	records := t.TempDir()
	must(os.WriteFile(filepath.Join(records, "102.wav"), []byte("RIFF-bob"), 0o644))
	_, err = pool.Exec(ctx, `INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, started_at, status, record, line, seconds)
		VALUES ('u1', '102', '89000000001', $1, now() - interval '1 hour', 'answered', '102.wav', '+74950000000', 65),
		       ('u2', '89000000002', '+74950000000', NULL, now(), 'noanswer', '', '+74950000000', 0)`, bob)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE telephony_calls SET to_user = $1 WHERE uniqueid = 'u2'`, anna)
	must(err)
	callOf := func(uid string) string {
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM telephony_calls WHERE uniqueid = $1`, uid).Scan(&id))
		return id
	}

	// Asterisk's ARI: who is online, and what go24 asks it to call
	type origin struct {
		q    url.Values
		body string
	}
	got := make(chan origin, 4)
	ast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/ari/endpoints/PJSIP":
			io.WriteString(w, `[{"resource":"w102","state":"online"},{"resource":"101","state":"offline"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/ari/channels":
			b, _ := io.ReadAll(r.Body)
			got <- origin{r.URL.Query(), string(b)}
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ast.Close()

	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	rw := []string{"telephony:read", "telephony:write"}
	annaKey, bobKey, danKey, adminKey, noScope := key(anna, rw...), key(bob, rw...), key(dan, rw...), key(admin, rw...), key(bob, "crm:read")
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle,
		Telephony: &telephony.PBX{Store: store, Records: records, Key: "ast-key", Dir: t.TempDir(), ARI: &telephony.ARI{URL: ast.URL, User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	do := func(k, method, path, body string, want int, admin ...bool) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+k)
		if len(admin) > 0 {
			req.Header.Set("X-Go24-Admin", "1")
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		return b
	}
	type calls struct {
		Calls []apiTelCall `json:"calls"`
	}
	list := func(k, query string, admin ...bool) []apiTelCall {
		t.Helper()
		var c calls
		must(json.Unmarshal(do(k, "GET", "/api/v1/telephony/calls"+query, "", http.StatusOK, admin...), &c))
		return c.Calls
	}

	// the log: one's own calls; the administrator in admin mode — all
	do(noScope, "GET", "/api/v1/telephony/calls", "", http.StatusForbidden)
	if c := list(bobKey, ""); len(c) != 1 || c[0].Type != "out" || c[0].Direction != "out" || c[0].PhoneNumber != "89000000001" ||
		c[0].FromUser == nil || c[0].FromUser.Name != "Борис" || c[0].Record == "" || c[0].Seconds != 65 {
		t.Fatalf("bob's log: %+v", c)
	}
	if c := list(annaKey, "?type=missed"); len(c) != 1 || c[0].Type != "missed" || c[0].Direction != "in" || c[0].Status != "noanswer" {
		t.Fatalf("anna's missed: %+v", c)
	}
	if c := list(annaKey, "?type=out"); len(c) != 0 {
		t.Fatalf("anna's outgoing: %+v", c)
	}
	if c := list(adminKey, "", true); len(c) != 2 {
		t.Fatalf("the administrator's log: %+v", c)
	}
	if c := list(adminKey, "?q=0002&limit=1", true); len(c) != 1 || c[0].ID != callOf("u2") {
		t.Fatalf("by number: %+v", c)
	}
	do(bobKey, "GET", "/api/v1/telephony/calls?from=yesterday", "", http.StatusUnprocessableEntity)

	// a call, its record and comment: not someone else's
	bobCall := "/api/v1/telephony/calls/" + callOf("u1")
	do(annaKey, "GET", bobCall, "", http.StatusNotFound)
	do(annaKey, "GET", bobCall+"/record", "", http.StatusNotFound)
	do(annaKey, "PATCH", bobCall, `{"comment":"чужой"}`, http.StatusNotFound)
	if b := do(bobKey, "GET", bobCall+"/record", "", http.StatusOK); string(b) != "RIFF-bob" {
		t.Fatalf("the record: %q", b)
	}
	var one apiTelCall
	must(json.Unmarshal(do(bobKey, "PATCH", bobCall, `{"comment":" Перезвонить в пятницу "}`, http.StatusOK), &one))
	if one.Comment != "Перезвонить в пятницу" {
		t.Fatalf("the comment: %+v", one)
	}
	must(json.Unmarshal(do(bobKey, "GET", bobCall, "", http.StatusOK), &one))
	if one.Comment != "Перезвонить в пятницу" || one.Line != "+74950000000" || one.ToUser != nil {
		t.Fatalf("the call: %+v", one)
	}
	do(bobKey, "PATCH", bobCall, `{}`, http.StatusUnprocessableEntity)

	// «Позвонить»: Asterisk rings the employee's phones and puts them through; recording is on
	do(bobKey, "POST", "/api/v1/telephony/call", `{"number":"8 (900) 111-22-33"}`, http.StatusOK)
	o := <-got
	if o.q.Get("endpoint") != "Local/102*89001112233@go24-click/n" || o.q.Get("extension") != "89001112233" || o.q.Get("context") != "go24-phones" ||
		o.q.Get("callerId") != "102" || !strings.Contains(o.body, `"GO24_RECORD":"1"`) {
		t.Fatalf("the originate: %v %s", o.q, o.body)
	}
	do(bobKey, "POST", "/api/v1/telephony/call", `{"number":"abc"}`, http.StatusUnprocessableEntity)
	do(bobKey, "POST", "/api/v1/telephony/call", `{"number":"102"}`, http.StatusUnprocessableEntity)
	do(danKey, "POST", "/api/v1/telephony/call", `{"number":"101"}`, http.StatusUnprocessableEntity) // no number of his own
	// a role forbidding the outside calls: the inner ones still go
	var role string
	must(pool.QueryRow(ctx, `INSERT INTO telephony_roles (name, perms) VALUES ('Без исходящих', '{"calls":"own","records":"own","outgoing":"none","settings":"none"}') RETURNING id`).Scan(&role))
	_, err = pool.Exec(ctx, `INSERT INTO telephony_role_members (role_id, user_id) VALUES ($1, $2)`, role, anna)
	must(err)
	do(annaKey, "POST", "/api/v1/telephony/call", `{"number":"+79001112233"}`, http.StatusForbidden)
	do(annaKey, "POST", "/api/v1/telephony/call", `{"number":"102"}`, http.StatusOK)
	if o := <-got; o.q.Get("endpoint") != "Local/101*102@go24-click/n" {
		t.Fatalf("the inner call: %v", o.q)
	}

	// the lines and the inner numbers
	var lines struct {
		Lines []struct {
			Name, Number string
			CanCall      bool `json:"can_call"`
		}
	}
	must(json.Unmarshal(do(bobKey, "GET", "/api/v1/telephony/lines", "", http.StatusOK), &lines))
	if len(lines.Lines) != 1 || lines.Lines[0].Number != "+74950000000" || !lines.Lines[0].CanCall {
		t.Fatalf("the lines: %+v", lines)
	}
	if b := do(bobKey, "GET", "/api/v1/telephony/lines", "", http.StatusOK); strings.Contains(string(b), "login") || strings.Contains(string(b), "sip.example.org") {
		t.Fatalf("the line's account is shown: %s", b)
	}
	must(json.Unmarshal(do(annaKey, "GET", "/api/v1/telephony/lines", "", http.StatusOK), &lines))
	if lines.Lines[0].CanCall {
		t.Fatalf("anna may call out: %+v", lines)
	}
	var users struct {
		Users []struct {
			ID, Number string
			Online     *bool
		}
	}
	must(json.Unmarshal(do(annaKey, "GET", "/api/v1/telephony/users", "", http.StatusOK), &users))
	if len(users.Users) != 2 || users.Users[0].Number != "101" || users.Users[0].Online == nil || *users.Users[0].Online ||
		users.Users[1].ID != bob || !*users.Users[1].Online {
		t.Fatalf("the users: %+v", users)
	}

	// the statistics: the employee's own
	var st struct {
		Total apiTelStatRow
		Users []apiTelStatRow
	}
	must(json.Unmarshal(do(bobKey, "GET", "/api/v1/telephony/stats", "", http.StatusOK), &st))
	if st.Total.Out != 1 || st.Total.In != 0 {
		t.Fatalf("bob's stats: %+v", st)
	}

	// the black list is the administrators'
	do(bobKey, "GET", "/api/v1/telephony/blacklist", "", http.StatusForbidden)
	do(bobKey, "POST", "/api/v1/telephony/blacklist", `{"number":"89009998877"}`, http.StatusForbidden)
	var black struct {
		Blacklist []apiBlocked
	}
	must(json.Unmarshal(do(adminKey, "POST", "/api/v1/telephony/blacklist", `{"number":"8 900 999-88-77","comment":"спам"}`, http.StatusOK), &black))
	if len(black.Blacklist) != 1 || black.Blacklist[0].Comment != "спам" || black.Blacklist[0].AddedBy != "Админ" {
		t.Fatalf("the black list: %+v", black)
	}
	do(adminKey, "DELETE", "/api/v1/telephony/blacklist/"+black.Blacklist[0].ID, "", http.StatusNoContent)
	do(adminKey, "DELETE", "/api/v1/telephony/blacklist/"+black.Blacklist[0].ID, "", http.StatusNotFound)

	// a call reported by Asterisk goes to the webhooks
	_, err = pool.Exec(ctx, `INSERT INTO webhooks (name, url, events, secret, created_by) VALUES ('n8n', 'http://n8n.test/h', '{call.ended}', 's', $1)`, admin)
	must(err)
	now := time.Now().Unix()
	form := url.Values{"key": {"ast-key"}, "id": {"u3"}, "from": {"89005554433"}, "to": {"+74950000000"}, "by": {"101"}, "line": {"+74950000000"},
		"start": {strconv.FormatInt(now-30, 10)}, "answer": {strconv.FormatInt(now-20, 10)}, "end": {strconv.FormatInt(now, 10)}, "dial": {"ANSWER"}}
	resp, err := http.PostForm(hs.URL+"/telephony/cdr", form)
	must(err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cdr: %d", resp.StatusCode)
	}
	var payload string
	must(pool.QueryRow(ctx, `SELECT payload::text FROM webhook_deliveries WHERE event = 'call.ended'`).Scan(&payload))
	if !strings.Contains(payload, `"phone_number": "89005554433"`) || !strings.Contains(payload, `"direction": "in"`) || !strings.Contains(payload, `"seconds": 20`) {
		t.Fatalf("the webhook: %s", payload)
	}
}
