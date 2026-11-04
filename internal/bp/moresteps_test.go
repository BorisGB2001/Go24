package bp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sms"
	"go24/internal/tasks"
)

func TestRandomString(t *testing.T) {
	v, err := randomString("ab", 50)
	if err != nil || len(v) != 50 || strings.Trim(v, "ab") != "" {
		t.Fatalf("%q %v", v, err)
	}
	for range 50 {
		n, err := calc("rand(3; 5)", "")
		if k, _ := strconv.Atoi(n); err != nil || k < 3 || k > 5 {
			t.Fatalf("rand(3; 5) = %q %v", n, err)
		}
	}
	if n, err := calc("rand(7; 7) + 1", ""); err != nil || n != "8" {
		t.Fatalf("rand(7; 7) + 1 = %q %v", n, err)
	}
	if _, err := calc("rand(5)", ""); err == nil {
		t.Fatal("rand of one argument")
	}
}

// TestMoreSteps: «Генерация случайной строки» puts a string of the chosen characters into a variable (digits into a
// number); «Управление рабочим днём» starts and pauses the employee's day (twice — no failure), «Статус рабочего
// дня» puts it into the process; «Отправка SMS» goes to the phone written through the provider chosen, and to the
// client of the process's lead by default, into the lead's timeline.
func TestMoreSteps(t *testing.T) {
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
	var ann, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'ann', 'ann@t') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'ivan', 'ivan@t') RETURNING id`).Scan(&ivan))
	cs := crm.NewStore(pool)
	s := NewStore(pool, tasks.NewStore(pool), cs, company.NewStore(pool))
	s.SMS = sms.NewStore(pool, "secret")
	var sent []map[string]any
	s.SMS.Post = func(_ context.Context, r sms.Request) (sms.Response, error) {
		b, _ := json.Marshal(r.JSON)
		m := map[string]any{"url": r.URL}
		_ = json.Unmarshal(b, &m)
		m["url"] = r.URL
		sent = append(sent, m)
		return sms.Response{Status: 200, Body: []byte(`{"id":"m1"}`)}, nil
	}
	cs.SendSMS = func(ctx context.Context, userID, entity, entityID, phone, text string) error {
		_, err := s.SMS.Send(ctx, sms.Out{Phone: phone, Text: text, AuthorID: userID, Entity: entity, EntityID: entityID})
		return err
	}
	_, err = s.SMS.SaveSender(ctx, sms.Sender{Kind: "gateway", Name: "Main", Login: "http://main/send", Active: true}, "T1")
	must(err)
	other, err := s.SMS.SaveSender(ctx, sms.Sender{Kind: "gateway", Name: "Other", Login: "http://other/send", Active: true}, "T2")
	must(err)

	var ve *i18n.Error
	for name, st := range map[string]Step{
		"random without a length":     {Kind: "random", Field: "code"},
		"random into a constant":      {Kind: "random", Field: "fixed", Size: 5},
		"random into a user field":    {Kind: "random", Field: "who", Size: 5},
		"random of unknown chars":     {Kind: "random", Field: "code", Size: 5, Chars: []string{"emoji"}},
		"workday without an act":      {Kind: "workday", To: "starter"},
		"workday of an unknown act":   {Kind: "workday", Act: "sleep"},
		"sms without a text":          {Kind: "sms", Value: "+79000000000"},
		"a provider of another step":  {Kind: "notify", To: "starter", Text: "x", Sender: other},
		"a length of another step":    {Kind: "notify", To: "starter", Text: "x", Size: 3},
		"an act of another step":      {Kind: "notify", To: "starter", Text: "x", Act: "start"},
		"a random string too long":    {Kind: "random", Field: "code", Size: MaxRandom + 1},
		"workday fields before: read": {Kind: "if", Field: "workday_status", Op: "filled"},
	} {
		_, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Fields: []Field{{Key: "code", Label: "Код", Kind: "string", Scope: "var"},
			{Key: "fixed", Label: "К", Kind: "string", Scope: "const", Default: "x"}, {Key: "who", Label: "Кто", Kind: "user", Scope: "var"}},
			Steps: []Step{st, {Kind: "workdayinfo"}}})
		if !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, ann, Template{Name: "Смена", Active: true,
		Fields: []Field{{Key: "who", Label: "Кто", Kind: "user", Required: true}, {Key: "code", Label: "Код", Kind: "string", Scope: "var"},
			{Key: "pin", Label: "PIN", Kind: "number", Scope: "var"}, {Key: "dice", Label: "Кубик", Kind: "number", Scope: "var"},
			{Key: "extra", Label: "Ещё номер", Kind: "string", Scope: "var"}},
		Steps: []Step{{Kind: "random", Field: "code", Size: 12, Chars: []string{"upper", "digits"}},
			{Kind: "random", Field: "pin", Size: 4},
			{Kind: "set", Field: "dice", Value: "= rand(1; 6)"},
			{Kind: "workday", Act: "start", To: FieldPrefix + "who"},
			{Kind: "workday", Act: "start", To: FieldPrefix + "who"},
			{Kind: "workday", Act: "pause", To: FieldPrefix + "who"},
			{Kind: "workdayinfo", To: FieldPrefix + "who"},
			{Kind: "if", Field: "workday_status", Op: "eq", Value: "paused", Goto: 10},
			{Kind: "sms", Value: "8 900 111-22-33, {extra}", Text: "Код {code}, статус {workday_status}", Sender: other},
			{Kind: "log", Text: "{workday_status} {workday_started}"}}})
	must(err)
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"who": ivan})
	must(err)
	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Status != "done" {
		lines, _ := s.Log(ctx, id)
		t.Fatalf("the process: %s %+v", x.Status, lines)
	}
	if !regexp.MustCompile(`^[A-Z0-9]{12}$`).MatchString(x.Values["code"]) || !regexp.MustCompile(`^[1-9][0-9]{3}$`).MatchString(x.Values["pin"]) {
		t.Fatalf("random strings: %q %q", x.Values["code"], x.Values["pin"])
	}
	if d, _ := strconv.Atoi(x.Values["dice"]); d < 1 || d > 6 {
		t.Fatalf("rand(1; 6): %q", x.Values["dice"])
	}
	if x.Values["workday_status"] != "paused" || x.Values["workday_started"] == "" || x.Values["workday_ended"] != "" {
		t.Fatalf("the working day: %+v", x.Values)
	}
	if day, err := company.NewStore(pool).Today(ctx, ivan, time.Now()); err != nil || day == nil || day.State() != "paused" {
		t.Fatalf("Ivan's day: %+v %v", day, err)
	}
	if len(sent) != 1 || sent[0]["url"] != "http://other/send" || sent[0]["phone"] != "+79001112233" ||
		sent[0]["text"] != "Код "+x.Values["code"]+", статус paused" {
		t.Fatalf("the SMS: %+v", sent)
	}

	// a lead's process: the client's phone, the default provider, a line of the lead's timeline
	lead, err := cs.SaveLead(ctx, ann, crm.Lead{Title: "Заявка", Name: "Олег", Phone: "8 900 555-44-33", Responsible: crm.Ref{ID: ann}})
	must(err)
	ltpl, err := s.SaveTemplate(ctx, ann, Template{Name: "SMS лиду", Entity: "lead", Active: true,
		Steps: []Step{{Kind: "sms", Text: "Здравствуйте, {title}"}}})
	must(err)
	lid, err := s.Start(ctx, ann, ltpl, lead, nil)
	must(err)
	if x, _ := s.Instance(ctx, ann, lid); x.Status != "done" || len(sent) != 2 || sent[1]["url"] != "http://main/send" ||
		sent[1]["phone"] != "+79005554433" {
		t.Fatalf("the lead's SMS: %s %+v", x.Status, sent)
	}
	events, err := cs.Timeline(ctx, "lead", lead)
	must(err)
	if len(events) == 0 || events[0].Kind != "sms" {
		t.Fatalf("the lead's timeline: %+v", events)
	}
	// no phone anywhere — the process fails with the reason
	alone, err := s.SaveTemplate(ctx, ann, Template{Name: "Без номера", Active: true,
		Fields: []Field{{Key: "ph", Label: "Телефон", Kind: "string", Scope: "var"}}, Steps: []Step{{Kind: "sms", Text: "x", Value: "{ph}"}}})
	must(err)
	aid, err := s.Start(ctx, ann, alone, "", nil)
	must(err)
	if x, _ := s.Instance(ctx, ann, aid); x.Status != "failed" {
		t.Fatalf("an SMS without a phone: %s", x.Status)
	}
}
