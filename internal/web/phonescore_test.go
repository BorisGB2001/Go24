package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

func TestParseScore(t *testing.T) {
	for _, c := range []struct {
		in   string
		ok   bool
		n    int
		text string
	}{
		{"72\n✅ Поздоровался\n❌ Не договорился", true, 72, "✅ Поздоровался\n❌ Не договорился"},
		{"Оценка: 40%\n\nСоветы: …", true, 40, "Советы: …"},
		{"  100 ", true, 100, ""},
		{"140\n…", false, 0, ""},
		{"Не могу оценить", false, 0, ""},
	} {
		n, text, ok := telephony.ParseScore(c.in)
		if ok != c.ok || n != c.n || text != c.text {
			t.Errorf("%q: %d %q %v", c.in, n, text, ok)
		}
	}
}

// TestPhoneScore: CoPilot's call assessment — scripts are kept and checked; the script for the call's direction goes
// before the one for all calls; the AI is given the script and the record's text, its score and account are kept with
// the call, counted in the statistics and shown by REST; a score below the script's bar tells the employee's head.
func TestPhoneScore(t *testing.T) {
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
	var bob, head, dept string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('head', 'Анна', 'head@t') RETURNING id`).Scan(&head))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Продажи', $1) RETURNING id`, head).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, bob, dept)
	must(err)
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, bob, "102"))
	_, err = pool.Exec(ctx, `INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, started_at, status, record, line, seconds, transcript)
		VALUES ('s1', '102', '89000000001', $1, now(), 'answered', '102.wav', '+74950000000', 65, 'Здравствуйте, это Борис из «Ромашки». Привезём заказ в пятницу.')`, bob)
	must(err)
	callOf := func(uid string) telephony.Call {
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM telephony_calls WHERE uniqueid = $1`, uid).Scan(&id))
		c, err := store.CallByID(ctx, id)
		must(err)
		return c
	}

	// scripts
	if _, err := store.SaveScript(ctx, bob, telephony.Script{Name: " ", Body: "x"}); err == nil {
		t.Fatal("a script with no name")
	}
	if _, err := store.SaveScript(ctx, bob, telephony.Script{Name: "Пусто"}); err == nil {
		t.Fatal("a script with no body")
	}
	all, err := store.SaveScript(ctx, bob, telephony.Script{Name: "Общий", Body: "1. Поздоровался", Calls: "x", Auto: true, Low: 150})
	must(err)
	out, err := store.SaveScript(ctx, bob, telephony.Script{Name: "Продажа", Body: "1. Представился\n2. Договорился о доставке", Calls: "out", Auto: true, Low: 80})
	must(err)
	sc, err := store.ScriptByID(ctx, all)
	must(err)
	if sc.Calls != "all" || sc.Low != 100 {
		t.Fatalf("script %+v", sc)
	}
	if sc, ok, err := store.AutoScript(ctx, "out"); err != nil || !ok || sc.ID != out {
		t.Fatalf("auto out: %+v %v %v", sc, ok, err)
	}
	if sc, ok, err := store.AutoScript(ctx, "in"); err != nil || !ok || sc.ID != all {
		t.Fatalf("auto in: %+v %v %v", sc, ok, err)
	}
	if c := callOf("s1"); c.Score != -1 {
		t.Fatalf("not scored yet: %d", c.Score)
	}

	answer := "60\n✅ Представился\n❌ Не уточнил время\nСоветы: назовите время доставки."
	var asked []string
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System   string `json:"system"`
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		asked = append(asked, in.System+"\n"+in.Messages[0].Content[0].Text)
		b, _ := json.Marshal(answer)
		io.WriteString(w, `{"content":[{"type":"text","text":`+string(b)+`}],"usage":{"input_tokens":50,"output_tokens":8}}`)
	}))
	defer prov.Close()

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	aiStore := ai.NewStore(pool, "secret")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, AI: aiStore, Company: company.NewStore(pool),
		Telephony: &telephony.PBX{Store: store, Records: t.TempDir(), Key: "ast-key", Dir: t.TempDir()},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sc, err = store.ScriptByID(ctx, out)
	must(err)
	if err := srv.phoneScore(ctx, callOf("s1"), sc, ""); !errors.Is(err, ai.ErrNone) {
		t.Fatalf("no connection: %v", err)
	}
	_, err = aiStore.Save(ctx, bob, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	must(srv.phoneScore(ctx, callOf("s1"), sc, ""))
	c := callOf("s1")
	if c.Score != 60 || c.ScoreName != "Продажа" || !strings.HasPrefix(c.ScoreText, "✅ Представился") {
		t.Fatalf("score %d %q %q", c.Score, c.ScoreName, c.ScoreText)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "Договорился о доставке") || !strings.Contains(asked[0], "в пятницу") || !strings.Contains(asked[0], "Исходящий") {
		t.Fatalf("asked %q", asked)
	}
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'phone_score' AND body LIKE '60% по скрипту «Продажа»: Борис, 89000000001,%'`, head).Scan(&told))
	if told != 1 {
		t.Fatalf("the head told %d times", told)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'phone_score' AND user_id <> $1`, head).Scan(&told))
	if told != 0 {
		t.Fatalf("others told %d times", told)
	}

	// statistics
	st, err := store.Stats(ctx, telephony.StatFilter{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}, time.Local)
	must(err)
	if st.Total.Scored != 1 || st.Total.AvgScore() != 60 || len(st.Users) != 1 || st.Users[0].AvgScore() != 60 {
		t.Fatalf("stats %+v", st)
	}

	// REST: the scripts; assessed again by the general script, no notice above its bar
	answer = "95\n✅ Поздоровался"
	must(store.DeleteScript(ctx, out))
	if c := callOf("s1"); c.ScoreName != "Продажа" {
		t.Fatalf("the score of a deleted script: %q", c.ScoreName)
	}
	_, err = store.SaveScript(ctx, bob, telephony.Script{ID: all, Name: "Общий", Body: "1. Поздоровался", Calls: "all", Auto: true, Low: 50})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	k, err := keys.Create(ctx, bob, "k", []string{"telephony:read", "telephony:write"}, time.Hour)
	must(err)
	call := func(method, path, body string, v any) int {
		req, _ := http.NewRequest(method, hs.URL+path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		must(err)
		defer resp.Body.Close()
		must(json.NewDecoder(resp.Body).Decode(v))
		return resp.StatusCode
	}
	var list struct {
		Scripts []struct{ ID, Name string } `json:"scripts"`
	}
	if code := call(http.MethodGet, "/api/v1/telephony/scripts", "", &list); code != http.StatusOK || len(list.Scripts) != 1 || list.Scripts[0].ID != all {
		t.Fatalf("scripts: %d %+v", code, list)
	}
	var got apiTelCall
	if code := call(http.MethodPost, "/api/v1/telephony/calls/"+c.ID+"/assess", `{"script":"`+all+`"}`, &got); code != http.StatusOK ||
		got.Score == nil || got.Score.Percent != 95 || got.Score.Script != "Общий" || got.Score.Text != "✅ Поздоровался" {
		t.Fatalf("assess: %d %+v", code, got.Score)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'phone_score'`).Scan(&told))
	if told != 1 {
		t.Fatalf("told %d times", told)
	}
	var bad map[string]any
	if code := call(http.MethodPost, "/api/v1/telephony/calls/"+c.ID+"/assess", `{"script":"`+out+`"}`, &bad); code == http.StatusOK {
		t.Fatal("assessed by a deleted script")
	}
}
