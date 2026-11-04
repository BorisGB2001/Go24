package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestAsk: each provider is asked in its own dialect — the system text, the turns, the tools, the calls and their
// results — and its answer gives the text, the calls and the tokens.
func TestAsk(t *testing.T) {
	msgs := []Message{
		{Role: "user", Text: "Сколько задач?"},
		{Role: "assistant", Calls: []Call{{ID: "c1", Name: "count", Args: json.RawMessage(`{"open":true}`)}}},
		{Role: "user", Results: []Result{{ID: "c1", Name: "count", Text: "7"}}},
	}
	tools := []ToolSpec{{Name: "count", Description: "Counts tasks", Schema: json.RawMessage(`{"type":"object","properties":{"open":{"type":"boolean"}}}`)}}
	cases := []struct {
		provider, path, auth, answer string
		want                         []string // in the request
	}{
		{"anthropic", "/messages", "x-api-key: k1",
			`{"content":[{"type":"text","text":"Посчитаю"},{"type":"tool_use","id":"c2","name":"count","input":{"open":false}}],"usage":{"input_tokens":10,"cache_read_input_tokens":5,"output_tokens":3}}`,
			[]string{`"system":"Be brief"`, `"type":"tool_use","id":"c1","name":"count","input":{"open":true}`, `"type":"tool_result","tool_use_id":"c1","content":"7"`, `"input_schema"`}},
		{"openai", "/chat/completions", "Authorization: Bearer k1",
			`{"choices":[{"message":{"role":"assistant","content":"Посчитаю","tool_calls":[{"id":"c2","type":"function","function":{"name":"count","arguments":"{\"open\":false}"}}]}}],"usage":{"prompt_tokens":15,"completion_tokens":3}}`,
			[]string{`{"role":"system","content":"Be brief"}`, `"tool_calls":[{"id":"c1","type":"function","function":{"name":"count","arguments":"{\"open\":true}"}}]`, `{"role":"tool","content":"7","tool_call_id":"c1"}`, `"parameters"`}},
		{"google", "/models/gemini-x:generateContent", "x-goog-api-key: k1",
			`{"candidates":[{"content":{"role":"model","parts":[{"text":"Посчитаю"},{"functionCall":{"name":"count","args":{"open":false}}}]}}],"usageMetadata":{"promptTokenCount":15,"candidatesTokenCount":3}}`,
			[]string{`"systemInstruction":{"parts":[{"text":"Be brief"}]}`, `"functionCall":{"id":"c1","name":"count","args":{"open":true}}`, `"functionResponse":{"id":"c1","name":"count","response":{"result":"7"}}`, `"functionDeclarations"`}},
	}
	for _, tc := range cases {
		var got, auth, path string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got, path = string(b), r.URL.Path
			auth = map[string]string{"anthropic": "x-api-key: " + r.Header.Get("x-api-key"), "openai": "Authorization: " + r.Header.Get("Authorization"),
				"google": "x-goog-api-key: " + r.Header.Get("x-goog-api-key")}[tc.provider]
			w.Write([]byte(tc.answer))
		}))
		s := NewStore(nil, "x")
		model := map[string]string{"anthropic": "claude-x", "openai": "gpt-x", "google": "gemini-x"}[tc.provider]
		r, err := s.Ask(context.Background(), Connection{Provider: tc.provider, BaseURL: srv.URL}, "k1", model, "Be brief", msgs, tools)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if path != tc.path || auth != tc.auth {
			t.Errorf("%s: path %q, auth %q", tc.provider, path, auth)
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %s in %s", tc.provider, w, got)
			}
		}
		if r.Text != "Посчитаю" || len(r.Calls) != 1 || r.Calls[0].Name != "count" || string(r.Calls[0].Args) != `{"open":false}` ||
			r.Calls[0].ID == "" || r.InTokens != 15 || r.OutTokens != 3 {
			t.Errorf("%s: reply %+v", tc.provider, r)
		}
	}
	// the provider's error comes back with its message; no model — an error before any call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
	}))
	defer srv.Close()
	s := NewStore(nil, "x")
	if _, err := s.Ask(context.Background(), Connection{Provider: "anthropic", BaseURL: srv.URL, Model: "m"}, "k", "", "", msgs, nil); err == nil ||
		err.Error() != "HTTP 429: slow down" {
		t.Errorf("error = %v", err)
	}
	if _, err := s.Ask(context.Background(), Connection{Provider: "openai", BaseURL: srv.URL}, "k", "", "", msgs, nil); err == nil ||
		!strings.Contains(err.Error(), "err.ai_model_required") {
		t.Errorf("no model: %v", err)
	}
}

// TestAgent runs against the test database: a tool that reads runs at once, one that writes waits for the
// employee; confirmed — runs with their rights, rejected — the model learns it; a new message rejects what waits;
// every call of the model is noted with its tokens; a failure of the provider is the run's error.
func TestAgent(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ag1', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ag2', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))

	// the model: asks to list, then to create, then answers; «fail» in the text — the provider fails
	var asked []string
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []struct {
				Role    string      `json:"role"`
				Content []anthBlock `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		last := in.Messages[len(in.Messages)-1].Content
		b, _ := json.Marshal(last)
		asked = append(asked, string(b))
		answer := func(blocks string) {
			w.Write([]byte(`{"content":[` + blocks + `],"usage":{"input_tokens":100,"output_tokens":10}}`))
		}
		switch {
		case strings.Contains(string(b), "fail"):
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"overloaded"}}`))
		case strings.Contains(string(b), "Создай"):
			answer(`{"type":"tool_use","id":"l1","name":"list","input":{}},{"type":"tool_use","id":"l2","name":"nope","input":{}}`)
		case strings.Contains(string(b), `"tool_use_id":"l1"`):
			answer(`{"type":"text","text":"Создаю"},{"type":"tool_use","id":"c1","name":"create","input":{"title":"Отчёт"}},{"type":"tool_use","id":"c2","name":"create","input":{"title":"План"}}`)
		default:
			answer(`{"type":"text","text":"Готово"}`)
		}
	}))
	defer prov.Close()
	s := NewStore(pool, "secret")
	_, err = s.Save(ctx, ivan, Connection{Owner: ivan, Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	var created []string
	agent := &Agent{Name: "tasks", System: func(ctx context.Context, userID string) string { return "You help " + userID },
		Tools: []Tool{
			{ToolSpec: ToolSpec{Name: "list", Schema: json.RawMessage(`{"type":"object"}`)},
				Run: func(ctx context.Context, userID string, args json.RawMessage) (string, error) { return "2 tasks of " + userID, nil }},
			{ToolSpec: ToolSpec{Name: "create", Schema: json.RawMessage(`{"type":"object"}`)}, Write: true,
				Run: func(ctx context.Context, userID string, args json.RawMessage) (string, error) {
					created = append(created, userID+" "+string(args))
					return "created", nil
				}},
		}}

	// Anna has no connection: the run fails with err.ai_none
	r, err := s.Say(ctx, anna, agent, "", "Создай задачи")
	must(err)
	if r.Status != RunError || r.Err().Key != "err.ai_none" || r.Title != "Создай задачи" {
		t.Fatalf("no connection: %+v", r)
	}
	r, err = s.Say(ctx, ivan, agent, "", "Создай задачи")
	must(err)
	if r.Status != RunConfirm || len(r.Pending()) != 2 || len(created) != 0 {
		t.Fatalf("confirm: %+v", r)
	}
	if a := r.Action("l1"); a.Status != ActDone || a.Result != "2 tasks of "+ivan || a.Write {
		t.Errorf("read tool: %+v", a)
	}
	if a := r.Action("l2"); a.Status != ActError {
		t.Errorf("unknown tool: %+v", a)
	}
	if _, err := s.GetRun(ctx, anna, r.ID); err != ErrNotFound {
		t.Errorf("another's run: %v", err)
	}
	if _, err := s.Decide(ctx, anna, agent, r.ID, r.Action("c1").ID, true); err != ErrNotFound {
		t.Errorf("another decides: %v", err)
	}
	// the first is confirmed: runs, the agent still waits for the second
	r, err = s.Decide(ctx, ivan, agent, r.ID, r.Action("c1").ID, true)
	must(err)
	if r.Status != RunConfirm || len(created) != 1 || created[0] != ivan+` {"title": "Отчёт"}` && created[0] != ivan+` {"title":"Отчёт"}` {
		t.Fatalf("confirmed: %+v %v", r.Status, created)
	}
	if _, err := s.Decide(ctx, ivan, agent, r.ID, r.Action("c1").ID, true); err == nil {
		t.Error("decided twice")
	}
	// the second is rejected: the model gets both results and answers
	r, err = s.Decide(ctx, ivan, agent, r.ID, r.Action("c2").ID, false)
	must(err)
	if r.Status != RunDone || len(created) != 1 || r.Messages[len(r.Messages)-1].Text != "Готово" {
		t.Fatalf("rejected: %+v", r)
	}
	if last := asked[len(asked)-1]; !strings.Contains(last, `"tool_use_id":"c1","content":"created"`) ||
		!strings.Contains(last, `"tool_use_id":"c2","content":"`+rejected+`","is_error":true`) {
		t.Errorf("results: %s", last)
	}
	// a new message in the run while actions wait: they are rejected
	r, err = s.Say(ctx, ivan, agent, r.ID, "Создай ещё")
	must(err)
	if r.Status != RunConfirm {
		t.Fatalf("again: %+v", r)
	}
	r, err = s.Say(ctx, ivan, agent, r.ID, "Не надо")
	must(err)
	if r.Status != RunDone || len(r.Pending()) != 0 || len(created) != 1 {
		t.Fatalf("went on: %+v", r)
	}
	// the provider fails: the run's error; the next message goes with the one not answered
	r, err = s.Say(ctx, ivan, agent, r.ID, "fail")
	must(err)
	if r.Status != RunError || r.Err().Key != "err.ai_call" || !strings.Contains(r.Error, "overloaded") {
		t.Fatalf("fail: %+v", r)
	}
	n := len(r.Messages)
	r, err = s.Say(ctx, ivan, agent, r.ID, "ещё раз")
	must(err)
	if r.Status != RunError || len(r.Messages) != n || !strings.HasSuffix(r.Messages[n-1].Text, "fail\n\nещё раз") {
		t.Fatalf("retry: %+v", r.Messages)
	}

	runs, err := s.Runs(ctx, ivan, "tasks", 10)
	must(err)
	if len(runs) != 1 || runs[0].ID != r.ID {
		t.Errorf("runs: %+v", runs)
	}
	var calls, in, out int
	must(pool.QueryRow(ctx, `SELECT count(*), sum(in_tokens), sum(out_tokens) FROM ai_usage WHERE user_id = $1 AND agent = 'tasks' AND company = false`,
		ivan).Scan(&calls, &in, &out))
	if calls != 6 || in != 600 || out != 60 {
		t.Errorf("usage: %d calls, %d/%d tokens", calls, in, out)
	}
	must(s.DeleteRun(ctx, ivan, r.ID))
	if _, err := s.GetRun(ctx, ivan, r.ID); err != ErrNotFound {
		t.Errorf("deleted: %v", err)
	}
}
