package ai

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestMonthStart(t *testing.T) {
	ms := MonthStart(time.Date(2026, 10, 29, 15, 4, 0, 0, time.Local))
	if !ms.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)) {
		t.Errorf("MonthStart = %v", ms)
	}
	if c := (Connection{PriceIn: 3, PriceOut: 15}).Cost(1_000_000, 100_000); math.Abs(c-4.5) > 1e-9 {
		t.Errorf("Cost = %v", c)
	}
}

// TestUsage runs against the test database: a call notes its cost by the connection's prices; the company's
// connection stops an employee at their monthly limit, their own limit is over the connection's, 0 — none; one's own
// connection has no limit; the report sums the employees and the months.
func TestUsage(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('us1', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('us2', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	calls := 0
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"content":[{"type":"text","text":"Да"}],"usage":{"input_tokens":100,"output_tokens":10}}`))
	}))
	defer prov.Close()
	s := NewStore(pool, "secret")

	if _, err := s.Save(ctx, anna, Connection{Name: "x", Provider: "anthropic", Model: "m", PriceIn: -1}, "k"); err == nil {
		t.Error("a negative price is saved")
	}
	cid, err := s.Save(ctx, anna, Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x",
		PriceIn: 3, PriceOut: 15, MonthlyLimit: 200}, "k")
	must(err)
	c, err := s.Get(ctx, ivan, cid)
	must(err)
	if c.PriceIn != 3 || c.PriceOut != 15 || c.MonthlyLimit != 200 {
		t.Fatalf("connection %+v", c)
	}

	// 110 tokens a call: 110, 220 — then the limit of 200 stops the third before the provider is asked
	for i := range 2 {
		if _, err := s.Write(ctx, ivan, "mail", "", "Привет"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	var cost float64
	must(pool.QueryRow(ctx, `SELECT sum(cost)::float8 FROM ai_usage WHERE user_id = $1`, ivan).Scan(&cost))
	if math.Abs(cost-2*0.00045) > 1e-9 {
		t.Errorf("cost = %v", cost)
	}
	if _, err := s.Write(ctx, ivan, "mail", "", "Привет"); err == nil || !strings.Contains(err.Error(), "err.ai_limit") || calls != 2 {
		t.Errorf("over the limit: %v, %d calls", err, calls)
	}
	q, err := s.Quota(ctx, ivan, c)
	must(err)
	if q.Limit != 200 || q.Used != 220 || q.Own {
		t.Errorf("quota %+v", q)
	}
	// the agents are stopped too
	ag := &Agent{Name: "tasks"}
	r, err := s.Say(ctx, ivan, ag, "", "Привет")
	must(err)
	if r.Status != RunError || !strings.Contains(r.Error, "200") {
		t.Errorf("agent over the limit: %+v", r)
	}

	// Ivan's own limit: 0 — none, 300 — over the connection's; Anna is not touched
	zero, three := int64(0), int64(300)
	must(s.SetLimit(ctx, ivan, &zero))
	if _, err := s.Write(ctx, ivan, "mail", "", "Привет"); err != nil {
		t.Errorf("no limit: %v", err)
	}
	must(s.SetLimit(ctx, ivan, &three))
	if _, err := s.Write(ctx, ivan, "mail", "", "Привет"); err == nil {
		t.Error("the own limit of 300 passed 330 tokens")
	}
	if q, _ := s.Quota(ctx, ivan, c); !q.Own || q.Limit != 300 {
		t.Errorf("own quota %+v", q)
	}
	if _, err := s.Write(ctx, anna, "mail", "", "Привет"); err != nil {
		t.Errorf("anna: %v", err)
	}
	must(s.SetLimit(ctx, ivan, nil))
	if l, _ := s.Limit(ctx, ivan); l != nil {
		t.Errorf("limit left %v", *l)
	}

	// «Настройки CoPilot» (GO24DEV-1005): a tool switched off, AI for the chosen only — by the employee or their
	// department (with the ones below, a head too); the provider is not asked
	before := calls
	must(s.SetPolicy(ctx, anna, Policy{Off: []string{"mail", "bogus"}}))
	if p, _ := s.Policy(ctx); len(p.Off) != 1 || !p.Everyone() {
		t.Errorf("policy %+v", p)
	}
	if _, err := s.Write(ctx, anna, "mail", "", "Привет"); err == nil || !strings.Contains(err.Error(), "err.ai_tool_off") {
		t.Errorf("mail off: %v", err)
	}
	if err := s.Permit(ctx, anna, "tasks"); err != nil {
		t.Errorf("tasks on: %v", err)
	}
	var top, sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&top))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Продажи', $1, $2) RETURNING id`, top, anna).Scan(&sub))
	must(s.SetPolicy(ctx, "", Policy{Users: []string{ivan}}))
	if err := s.Permit(ctx, anna, "tasks"); err == nil || !strings.Contains(err.Error(), "err.ai_denied") {
		t.Errorf("anna not chosen: %v", err)
	}
	if _, err := s.Write(ctx, anna, "tasks", "", "Привет"); err == nil {
		t.Error("anna called AI past the policy")
	}
	must(s.SetPolicy(ctx, "", Policy{Depts: []string{top}}))
	if err := s.Permit(ctx, anna, ""); err != nil { // the head of a department below the chosen one
		t.Errorf("anna by department: %v", err)
	}
	if err := s.Permit(ctx, ivan, ""); err == nil {
		t.Error("ivan is out of the departments")
	}
	r, err = s.Say(ctx, ivan, ag, "", "Привет")
	must(err)
	if r.Status != RunError || calls != before {
		t.Errorf("agent past the policy: %+v, %d calls", r, calls-before)
	}
	must(s.SetPolicy(ctx, "", Policy{}))

	// one's own connection has no limit even if asked
	own, err := s.Save(ctx, ivan, Connection{Owner: ivan, Name: "Свой", Provider: "anthropic", BaseURL: prov.URL, Model: "m", MonthlyLimit: 1}, "k")
	must(err)
	must(s.Choose(ctx, ivan, own))
	if _, err := s.Write(ctx, ivan, "mail", "", "Привет"); err != nil {
		t.Errorf("own connection: %v", err)
	}

	// the report: Ivan 4 calls (3 of the company), Anna 1
	from := MonthStart(time.Now())
	users, err := s.ByUser(ctx, from, from.AddDate(0, 1, 0))
	must(err)
	if len(users) != 2 || users[0].Name != "Иван" || users[0].Calls != 4 || users[0].Tokens() != 440 || users[0].Company != 330 ||
		users[1].Name != "Анна" || users[1].Calls != 1 {
		t.Errorf("by user %+v", users)
	}
	months, err := s.ByMonth(ctx, from.AddDate(0, -11, 0), "")
	must(err)
	if len(months) != 1 || months[0].Month != from.Format("2006-01") || months[0].Calls != 5 {
		t.Errorf("by month %+v", months)
	}
	if mine, _ := s.ByMonth(ctx, from, anna); len(mine) != 1 || mine[0].Calls != 1 {
		t.Errorf("anna's months %+v", mine)
	}
}
