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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/openlines"
	"go24/internal/tasks"
)

// TestOpenLinesCopilot: CoPilot in open lines — «Подсказать ответ» drafts the operator's answer from the conversation
// and their quick answers; «Подвести итоги» writes the summary, kept with the dialogue, put into the timeline of its
// CRM card and given by REST; a line with «CoPilot: итоги диалога» has a closed dialogue summed up by itself once,
// other lines and empty dialogues are not; with no AI connection — ErrNone.
func TestOpenLinesCopilot(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))

	var asked []string // the system prompt and the text of each call of the model
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
		answer := "Возврат оформим завтра."
		if strings.Contains(in.System, "итоги") {
			answer = "Клиент хочет вернуть товар; договорились на завтра."
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": answer}},
			"usage": map[string]int{"input_tokens": 40, "output_tokens": 9}})
	}))
	defer prov.Close()

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	aiStore := ai.NewStore(pool, "secret")
	ol := openlines.NewStore(pool, crm.NewStore(pool))
	ol.Tr = func(key string, _ ...any) string { return key }
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: ol.CRM, OpenLines: ol, Keys: keys, I18n: bundle, AI: aiStore,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	auto, err := ol.SaveWidget(ctx, anna, openlines.Widget{Name: "Сайт", Active: true, Operators: []string{anna}, AISummary: true})
	must(err)
	plain, err := ol.SaveWidget(ctx, anna, openlines.Widget{Name: "Опт", Active: true, Operators: []string{anna}})
	must(err)
	if w, err := ol.Widget(ctx, auto); err != nil || !w.AISummary {
		t.Fatalf("line %+v %v", w, err)
	}
	dialogue := func(line string) string {
		sid, tok, err := ol.Visit(ctx, line, "", "https://shop.example/")
		must(err)
		must(ol.VisitorSay(ctx, line, tok, "Хочу вернуть товар"))
		must(ol.Reply(ctx, anna, sid, "Подскажите номер заказа"))
		must(ol.VisitorSay(ctx, line, tok, "Заказ 512"))
		return sid
	}
	sid, other := dialogue(auto), dialogue(plain)
	_, err = ol.SaveQuick(ctx, anna, false, openlines.Quick{Title: "Возврат", Body: "Возврат — в течение 14 дней"})
	must(err)

	// no AI connection yet
	if _, err := srv.olSuggest(ctx, sid, anna); !errors.Is(err, ai.ErrNone) {
		t.Fatalf("no connection: %v", err)
	}
	_, err = aiStore.Save(ctx, anna, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	// «Подсказать ответ» by REST: the conversation and the quick answers go to the model
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	k, err := keys.Create(ctx, anna, "k", []string{"openlines:read", "openlines:write"}, time.Hour)
	must(err)
	post := func(path string, v any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, hs.URL+path, bytes.NewReader(nil))
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", path, res.StatusCode, raw)
		}
		must(json.Unmarshal(raw, v))
	}
	var hint struct {
		Text string `json:"text"`
	}
	post("/api/v1/openlines/dialogs/"+sid+"/suggest", &hint)
	if hint.Text != "Возврат оформим завтра." || len(asked) != 1 || !strings.Contains(asked[0], "Заказ 512") ||
		!strings.Contains(asked[0], "Анна: Подскажите номер заказа") || !strings.Contains(asked[0], "в течение 14 дней") || !strings.Contains(asked[0], "Сайт") {
		t.Fatalf("suggest %q, asked %q", hint.Text, asked)
	}
	if x, _ := ol.Session(ctx, sid); x.Summary != "" {
		t.Fatal("a suggestion is not a summary")
	}

	// a lead of the dialogue; closed — summed up by itself once, its card gets the summary; the other line's is not
	card, err := ol.MakeLead(ctx, anna, sid, "site")
	must(err)
	must(ol.SetStatus(ctx, anna, sid, false))
	must(ol.SetStatus(ctx, anna, other, false))
	srv.OLSummarizeDue(ctx, time.Now())
	srv.OLSummarizeDue(ctx, time.Now())
	x, err := ol.Session(ctx, sid)
	must(err)
	if x.Summary != "Клиент хочет вернуть товар; договорились на завтра." || len(asked) != 2 || !strings.Contains(asked[1], "Хочу вернуть товар") {
		t.Fatalf("summary %q, asked %d", x.Summary, len(asked))
	}
	if y, _ := ol.Session(ctx, other); y.Summary != "" {
		t.Fatal("a line without CoPilot's summaries got one")
	}
	events, err := ol.CRM.Timeline(ctx, "lead", strings.TrimPrefix(card, "/crm/leads/"))
	must(err)
	found := false
	for _, e := range events {
		found = found || (strings.Contains(e.Body, "CoPilot") && strings.Contains(e.Body, "договорились на завтра"))
	}
	if !found {
		t.Fatalf("no summary in the lead's timeline: %+v", events)
	}
	var used int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage WHERE user_id = $1 AND agent = 'openlines'`, anna).Scan(&used))
	if used != 2 {
		t.Fatalf("usage %d", used)
	}

	// «Подвести итоги» by REST for the other line's dialogue: the dialogue with its summary
	var d apiOLDialog
	post("/api/v1/openlines/dialogs/"+other+"/summary", &d)
	if d.ID != other || d.Summary == "" || len(asked) != 3 {
		t.Fatalf("REST summary %+v, asked %d", d, len(asked))
	}

	// a dialogue without the conversation is not summed up
	empty, _, err := ol.Visit(ctx, auto, "", "https://shop.example/")
	must(err)
	if err := srv.olSummarize(ctx, empty, anna); err == nil || len(asked) != 3 {
		t.Fatalf("empty: %v, asked %d", err, len(asked))
	}
}
