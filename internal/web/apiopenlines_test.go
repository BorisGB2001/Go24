package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/openlines"
	"go24/internal/tasks"
)

// TestOpenLinesAPI: «Открытые линии» through REST API v1, as imopenlines.* — the lines, the dialogues and their
// messages, an answer, transfer, close, reopen, spam, a lead, the head's rating, quick answers and statistics; the
// key's scopes and the pages' rights are kept; the webhooks openline.started, openline.message, openline.closed.
func TestOpenLinesAPI(t *testing.T) {
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
	user := func(name string, groups ...string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ($1, $1, $1 || '@t', $2) RETURNING id`,
			name, append([]string{}, groups...)).Scan(&id))
		return id
	}
	boss, anna, eve := user("Босс", "admins"), user("Анна"), user("Ева")
	var hook string
	must(pool.QueryRow(ctx, `INSERT INTO webhooks (name, url, events, secret, created_by)
		VALUES ('n8n', 'https://n8n.example/hook', '{openline.started,openline.message,openline.closed}', 's', $1) RETURNING id`, boss).Scan(&hook))
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	bossKey, annaKey, eveKey := key(boss, "openlines:read", "openlines:write", "crm:write"), key(anna, "openlines:read", "openlines:write"),
		key(eve, "openlines:read", "openlines:write", "crm:write")
	readOnly := key(anna, "openlines:read")
	ol := openlines.NewStore(pool, crm.NewStore(pool))
	ol.Tr = func(key string, _ ...any) string { return key }
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: ol.CRM, OpenLines: ol, Keys: keys, AdminGroups: []string{"admins"},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(method, path, token string, body any, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.StatusCode, raw, want)
		}
		return raw
	}
	decode := func(raw []byte, v any) {
		t.Helper()
		must(json.Unmarshal(raw, v))
	}
	events := func(event string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM webhook_deliveries WHERE webhook_id = $1 AND event = $2`, hook, event).Scan(&n))
		return n
	}

	wid, err := ol.SaveWidget(ctx, boss, openlines.Widget{Name: "Сайт магазина", Active: true, Operators: []string{anna, eve}})
	must(err)
	var lines []apiOLLine
	decode(call("GET", "/api/v1/openlines/lines", annaKey, nil, 200), &lines)
	if len(lines) != 1 || lines[0].ID != wid || len(lines[0].Operators) != 2 || lines[0].Operators[0].Name != "Анна" {
		t.Fatalf("lines %+v", lines)
	}

	sid, tok, err := ol.Visit(ctx, wid, "", "https://shop.example/")
	must(err)
	must(ol.VisitorSay(ctx, wid, tok, "Есть доставка?"))
	if events("openline.started") != 1 || events("openline.message") != 1 {
		t.Fatal("no webhooks of the new dialogue")
	}
	var list struct {
		Dialogs []apiOLDialog  `json:"dialogs"`
		Counts  map[string]int `json:"counts"`
	}
	decode(call("GET", "/api/v1/openlines/dialogs?tab=noanswer&counts=1", annaKey, nil, 200), &list)
	if len(list.Dialogs) != 1 || list.Dialogs[0].ID != sid || !list.Dialogs[0].Waiting || list.Dialogs[0].Line.ID != wid || list.Counts["noanswer"] != 1 {
		t.Fatalf("dialogs %+v", list)
	}
	call("GET", "/api/v1/openlines/dialogs?tab=nope", annaKey, nil, 422)
	var found []apiOLDialog
	decode(call("GET", "/api/v1/openlines/dialogs?q=доставка", annaKey, nil, 200), &found)
	if len(found) != 1 {
		t.Fatalf("search %+v", found)
	}

	// the answer: a read-only key may not; Анна takes the dialogue
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/messages", readOnly, map[string]string{"text": "Да"}, 403)
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/messages", annaKey, map[string]string{"text": " "}, 422)
	var d apiOLDialog
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/messages", annaKey, map[string]string{"text": "Да, курьером"}, 201), &d)
	if d.Operator == nil || d.Operator.ID != anna || d.Waiting {
		t.Fatalf("after the answer %+v", d)
	}
	var one struct {
		Dialog   apiOLDialog    `json:"dialog"`
		Messages []apiOLMessage `json:"messages"`
		CanRate  bool           `json:"can_rate"`
	}
	decode(call("GET", "/api/v1/openlines/dialogs/"+sid, annaKey, nil, 200), &one)
	var visitor, operator *apiOLMessage
	for i, m := range one.Messages {
		switch m.From {
		case "visitor":
			visitor = &one.Messages[i]
		case "operator":
			operator = &one.Messages[i]
		}
	}
	if visitor == nil || visitor.Text != "Есть доставка?" || operator == nil || operator.Operator.ID != anna || one.CanRate {
		t.Fatalf("dialog %+v", one)
	}
	// Анна's dialogue: Ева, an operator of the line too, does not see it
	call("GET", "/api/v1/openlines/dialogs/"+sid, eveKey, nil, 404)
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/messages", eveKey, map[string]string{"text": "Я тоже"}, 404)
	decode(call("GET", "/api/v1/openlines/dialogs?tab=all", eveKey, nil, 200), &found)
	if len(found) != 0 {
		t.Fatalf("Ева sees %+v", found)
	}
	var after []apiOLMessage
	decode(call("GET", "/api/v1/openlines/dialogs/"+sid+"/messages?after="+strconv.FormatInt(visitor.ID, 10), annaKey, nil, 200), &after)
	if len(after) == 0 || after[0].ID <= visitor.ID {
		t.Fatalf("after %+v", after)
	}
	call("GET", "/api/v1/openlines/dialogs/00000000-0000-0000-0000-000000000000/messages", annaKey, nil, 404)
	if events("openline.message") != 2 {
		t.Fatal("the operator's message has no webhook")
	}

	// transfer, the head's rating, a lead, close and reopen, spam
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/transfer", annaKey, map[string]string{"to": eve}, 200), &d)
	if d.Operator == nil || d.Operator.ID != eve {
		t.Fatalf("transfer %+v", d)
	}
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/rate", eveKey, map[string]any{"vote": 1}, 403)
	call("GET", "/api/v1/openlines/dialogs/"+sid, annaKey, nil, 404) // transferred: no longer Анна's
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/pin", eveKey, nil, 200), &d)
	if !d.Pinned {
		t.Fatalf("pin %+v", d)
	}
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/pin", eveKey, nil, 422)
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/unpin", eveKey, nil, 200), &d)
	if d.Pinned {
		t.Fatalf("unpin %+v", d)
	}
	// a hidden message (no webhook: the visitor does not get it) and «Перехватить»: Босс takes Ева's dialogue and gives it back
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/messages", bossKey, map[string]any{"text": "Предложи скидку", "hidden": true}, 201), &d)
	if d.Operator == nil || d.Operator.ID != eve {
		t.Fatalf("hidden message took the dialogue %+v", d)
	}
	var all []apiOLMessage
	decode(call("GET", "/api/v1/openlines/dialogs/"+sid+"/messages", eveKey, nil, 200), &all)
	if m := all[len(all)-1]; m.From != "hidden" || m.Text != "Предложи скидку" || m.Operator == nil || m.Operator.ID != boss {
		t.Fatalf("hidden message %+v", m)
	}
	if events("openline.message") != 2 {
		t.Fatal("a hidden message went to the webhook")
	}
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/intercept", eveKey, nil, 422) // her own
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/intercept", bossKey, nil, 200), &d)
	if d.Operator == nil || d.Operator.ID != boss {
		t.Fatalf("intercept %+v", d)
	}
	call("GET", "/api/v1/openlines/dialogs/"+sid, eveKey, nil, 404)
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/transfer", bossKey, map[string]string{"to": eve}, 200)
	var pause map[string]bool
	decode(call("POST", "/api/v1/openlines/pause", annaKey, map[string]bool{"paused": true}, 200), &pause)
	decode(call("GET", "/api/v1/openlines/pause", annaKey, nil, 200), &pause)
	if !pause["paused"] {
		t.Fatal("Анна is not on a pause")
	}
	call("POST", "/api/v1/openlines/pause", readOnly, map[string]bool{"paused": false}, 403)
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/rate", bossKey, map[string]any{"vote": 1, "comment": "Хорошо"}, 200), &d)
	if d.HeadVote != 1 {
		t.Fatalf("rate %+v", d)
	}
	call("POST", "/api/v1/openlines/dialogs/"+sid+"/lead", annaKey, nil, 404)
	var lead map[string]string
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/lead", eveKey, nil, 200), &lead)
	if lead["kind"] != "lead" || lead["id"] == "" || lead["url"] != "/crm/leads/"+lead["id"] {
		t.Fatalf("lead %+v", lead)
	}
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/close", eveKey, nil, 200), &d)
	if d.Status != "closed" || events("openline.closed") != 1 {
		t.Fatalf("close %+v", d)
	}
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/reopen", eveKey, nil, 200), &d)
	if d.Status != "open" {
		t.Fatalf("reopen %+v", d)
	}
	decode(call("POST", "/api/v1/openlines/dialogs/"+sid+"/spam", eveKey, nil, 200), &d)
	if d.Status != "closed" || !d.Spam || events("openline.closed") != 2 {
		t.Fatalf("spam %+v", d)
	}

	// quick answers: one's own; the company's — an administrator
	call("POST", "/api/v1/openlines/quick", annaKey, map[string]any{"title": "Цены", "text": "В каталоге", "common": true}, 403)
	var q map[string]string
	decode(call("POST", "/api/v1/openlines/quick", annaKey, map[string]any{"title": "Доставка", "text": "Курьером за день"}, 201), &q)
	call("POST", "/api/v1/openlines/quick", bossKey, map[string]any{"title": "Цены", "text": "В каталоге", "common": true}, 201)
	call("PATCH", "/api/v1/openlines/quick/"+q["id"], eveKey, map[string]any{"title": "Чужой", "text": "x"}, 404)
	call("PATCH", "/api/v1/openlines/quick/"+q["id"], annaKey, map[string]any{"title": "Доставка", "text": "Курьером за два часа"}, 200)
	var quick []apiOLQuickAnswer
	decode(call("GET", "/api/v1/openlines/quick", annaKey, nil, 200), &quick)
	if len(quick) != 2 || quick[0].Title != "Доставка" || quick[0].Text != "Курьером за два часа" || !quick[1].Common {
		t.Fatalf("quick %+v", quick)
	}
	call("DELETE", "/api/v1/openlines/quick/"+q["id"], annaKey, nil, 204)

	// statistics (spam apart from the new dialogues): an operator — only their own, also when asking for another one
	day := time.Now().Format(time.DateOnly)
	var st struct {
		Total     apiOLStatsRow   `json:"total"`
		Operators []apiOLStatsRow `json:"operators"`
		Days      []apiOLStatsRow `json:"days"`
	}
	decode(call("GET", "/api/v1/openlines/stats?from="+day+"&to="+day, bossKey, nil, 200), &st)
	if st.Total.New != 0 || st.Total.Spam != 1 || len(st.Days) != 1 || st.Days[0].Key != day {
		t.Fatalf("stats %+v", st)
	}
	decode(call("GET", "/api/v1/openlines/stats?operator="+eve, annaKey, nil, 200), &st)
	if st.Total.Spam != 0 {
		t.Fatalf("Анна sees Ева's %+v", st.Total)
	}
	call("GET", "/api/v1/openlines/stats?from=2026-13-01", annaKey, nil, 422)
}
