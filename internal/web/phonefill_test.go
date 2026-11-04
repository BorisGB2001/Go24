package web

import (
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
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestPhoneFill: CoPilot fills the client's CRM card from a conversation — the call of a contact fills its open deal:
// empty fields (the amount, own list and date fields) are set at once, a filled one (the comment) is offered; values that
// do not fit a field or name no field are dropped. CoPilot's line goes to the deal's timeline, an offered value is applied
// or rejected once; the lead of a call is filled itself, a converted lead — its deal, a client without deals — nothing.
// REST fills again: what is the same already is not offered.
func TestPhoneFill(t *testing.T) {
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
	var bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, bob, "102"))
	must(store.SetEnabled(ctx, bob, true))
	ts := tasks.NewStore(pool)
	cs := crm.NewStore(pool)
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Доставка", Kind: "list", Options: []string{"Самовывоз", "Курьер"}}))
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Дата поставки", Kind: "date"}))
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Склад", Kind: "string"}))
	own, err := ts.FieldsOf(ctx, tasks.CRMFieldSets["deal"])
	must(err)
	field := map[string]string{}
	for _, f := range own {
		field[f.Name] = "uf:" + f.ID
	}
	pipes, err := cs.Pipelines(ctx)
	must(err)
	contact, err := cs.SaveContact(ctx, bob, crm.Contact{Name: "Иван Петров", Phone: "89000000001", Responsible: crm.Ref{ID: bob}})
	must(err)
	deal, err := cs.SaveDeal(ctx, bob, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Contact: crm.Ref{ID: contact},
		Comment: "Старый комментарий", Responsible: crm.Ref{ID: bob}})
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, started_at, status, record, line, seconds, transcript, crm_entity, crm_id)
		VALUES ('f1', '102', '89000000001', $1, now(), 'answered', '102.wav', '+74950000000', 65,
			'Нам нужно на сто пятьдесят тысяч, курьером, шестого ноября.', 'contact', $2)`, bob, contact)
	must(err)
	callOf := func(uid string) telephony.Call {
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM telephony_calls WHERE uniqueid = $1`, uid).Scan(&id))
		c, err := store.CallByID(ctx, id)
		must(err)
		return c
	}

	answer := "```json\n" + `{"amount": "150 000", "comment": "Нужна доставка курьером к 6 ноября", "` + field["Доставка"] + `": "курьер",
		"` + field["Дата поставки"] + `": "2026-11-06", "` + field["Склад"] + `": "", "title": "Другое", "bogus": "x"}` + "\n```"
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
		out, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": answer}},
			"usage": map[string]int{"input_tokens": 70, "output_tokens": 20}})
		w.Write(out)
	}))
	defer prov.Close()

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	aiStore := ai.NewStore(pool, "secret")
	srv := &Server{DB: pool, Tasks: ts, CRM: cs, Keys: keys, I18n: bundle, AI: aiStore,
		Telephony: &telephony.PBX{Store: store, Records: t.TempDir(), Key: "ast-key", Dir: t.TempDir()},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	entity, id, err := cs.FillTarget(ctx, "contact", contact)
	must(err)
	if entity != "deal" || id != deal {
		t.Fatalf("target %s %s", entity, id)
	}
	if _, err := srv.phoneFillCard(ctx, callOf("f1"), "", entity, id); !errors.Is(err, ai.ErrNone) {
		t.Fatalf("no connection: %v", err)
	}
	_, err = aiStore.Save(ctx, bob, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	// the auto filling after the transcript: off — nothing
	srv.phoneFillAuto(ctx, callOf("f1").ID)
	if len(asked) != 0 {
		t.Fatal("filled while off")
	}
	must(store.SetFilling(ctx, bob, true))
	srv.phoneFillAuto(ctx, callOf("f1").ID)
	if len(asked) != 1 || !strings.Contains(asked[0], "CoPilot") || !strings.Contains(asked[0], `"key":"amount"`) ||
		!strings.Contains(asked[0], "Старый комментарий") || !strings.Contains(asked[0], `"options":["Самовывоз","Курьер"]`) ||
		!strings.Contains(asked[0], "сто пятьдесят") {
		t.Fatalf("asked %q", asked)
	}
	if callOf("f1").Filled == nil {
		t.Fatal("not marked filled")
	}
	srv.phoneFillAuto(ctx, callOf("f1").ID) // filled once by itself
	if len(asked) != 1 {
		t.Fatal("filled twice by itself")
	}
	d, err := cs.Deal(ctx, deal)
	must(err)
	if d.Amount != 150000 || d.Comment != "Старый комментарий" {
		t.Fatalf("deal %v %q", d.Amount, d.Comment)
	}
	vals, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal)
	must(err)
	got := map[string]string{}
	for _, v := range vals {
		got[v.Name] = v.Value
	}
	if got["Доставка"] != "Курьер" || got["Дата поставки"] != "2026-11-06" || got["Склад"] != "" {
		t.Fatalf("own fields %v", got)
	}

	line := func() crm.Event {
		t.Helper()
		evs, err := cs.Timeline(ctx, "deal", deal)
		must(err)
		for _, e := range evs {
			if e.Kind == "copilot_fill" {
				return e
			}
		}
		t.Fatal("no CoPilot line")
		return crm.Event{}
	}
	ev := line()
	f := crm.ParseFill(ev.Body)
	if f.Call != callOf("f1").ID || len(f.Items) != 4 || f.Offered() != 1 || ev.AuthorID != bob {
		t.Fatalf("line %+v by %s", f, ev.AuthorID)
	}
	n := -1
	for i, it := range f.Items {
		if it.Field == "comment" {
			n = i
			if it.State != "offered" || it.Old != "Старый комментарий" || it.New != "Нужна доставка курьером к 6 ноября" {
				t.Fatalf("comment %+v", it)
			}
		} else if it.State != "filled" {
			t.Fatalf("item %+v", it)
		}
	}
	rows := langFuncs(bundle, "ru-RU")["crmFill"].(func(string) []crmFillRow)(ev.Body)
	shown := map[string]crmFillRow{}
	for _, r := range rows {
		shown[r.Label] = r
	}
	if shown["Сумма"].New != "150000" || shown["Дата поставки"].New != "06.11.2026" || shown["Комментарий"].State != "offered" {
		t.Fatalf("shown %+v", rows)
	}

	// «Отклонить» keeps the comment, then the value is decided
	must(cs.DecideFill(ctx, bob, "deal", deal, ev.ID, n, false))
	if err := cs.DecideFill(ctx, bob, "deal", deal, ev.ID, n, true); err == nil {
		t.Fatal("decided twice")
	}
	if d, _ := cs.Deal(ctx, deal); d.Comment != "Старый комментарий" || crm.ParseFill(line().Body).Items[n].State != "rejected" {
		t.Fatalf("rejected: %q", d.Comment)
	}

	// REST fills again through the key owner's AI: the comment is offered anew and «Применить» writes it
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	k, err := keys.Create(ctx, bob, "k", []string{"telephony:read", "telephony:write"}, time.Hour)
	must(err)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/v1/telephony/calls/"+callOf("f1").ID+"/fill", nil)
	req.Header.Set("Authorization", "Bearer "+k)
	resp, err := http.DefaultClient.Do(req)
	must(err)
	defer resp.Body.Close()
	var out struct {
		Entity, ID string
		Fields     []struct{ Field, Old, New, State string }
	}
	must(json.NewDecoder(resp.Body).Decode(&out))
	if resp.StatusCode != http.StatusOK || out.Entity != "deal" || out.ID != deal || len(out.Fields) != 1 ||
		out.Fields[0].Field != "comment" || out.Fields[0].State != "offered" || len(asked) != 2 {
		t.Fatalf("REST: %d %+v, asked %d", resp.StatusCode, out, len(asked))
	}
	ev = line()
	must(cs.DecideFill(ctx, bob, "deal", deal, ev.ID, 0, true))
	if d, _ := cs.Deal(ctx, deal); d.Comment != "Нужна доставка курьером к 6 ноября" {
		t.Fatalf("applied: %q", d.Comment)
	}

	// the targets: a lead itself, a converted lead — its deal, a client without open deals — none
	lead, err := cs.SaveLead(ctx, bob, crm.Lead{Title: "Лид", Responsible: crm.Ref{ID: bob}})
	must(err)
	if e, x, err := cs.FillTarget(ctx, "lead", lead); err != nil || e != "lead" || x != lead {
		t.Fatalf("lead target %s %s %v", e, x, err)
	}
	_, err = pool.Exec(ctx, `UPDATE crm_leads SET deal_id = $2 WHERE id = $1`, lead, deal)
	must(err)
	if e, x, err := cs.FillTarget(ctx, "lead", lead); err != nil || e != "deal" || x != deal {
		t.Fatalf("converted lead target %s %s %v", e, x, err)
	}
	co, err := cs.SaveCompany(ctx, bob, crm.Company{Title: "Ромашка", Responsible: crm.Ref{ID: bob}})
	must(err)
	if _, x, err := cs.FillTarget(ctx, "company", co); err != nil || x != "" {
		t.Fatalf("company target %s %v", x, err)
	}
}

// TestFillValue: the values CoPilot finds are tidied to the field or dropped.
func TestFillValue(t *testing.T) {
	for _, c := range []struct {
		f    crm.FillField
		in   string
		want string
		ok   bool
	}{
		{crm.FillField{Kind: "number"}, "150 000,50", "150000.5", true},
		{crm.FillField{Kind: "number"}, "много", "", false},
		{crm.FillField{Kind: "date"}, "2026-11-06T10:00", "2026-11-06", true},
		{crm.FillField{Kind: "date"}, "6 ноября", "", false},
		{crm.FillField{Kind: "bool"}, "true", "1", true},
		{crm.FillField{Kind: "bool"}, "false", "", false},
		{crm.FillField{Kind: "list", Options: []string{"Курьер"}}, "КУРЬЕР", "Курьер", true},
		{crm.FillField{Kind: "list", Options: []string{"Курьер"}}, "Почта", "", false},
		{crm.FillField{Kind: "string"}, "  текст ", "текст", true},
		{crm.FillField{Kind: "string"}, " ", "", false},
	} {
		if got, ok := crm.FillValue(c.f, c.in); got != c.want || ok != c.ok {
			t.Errorf("%s %q: %q %v", c.f.Kind, c.in, got, ok)
		}
	}
	if got := parseFill("Вот: {\"amount\": 5, \"x\": true, \"y\": false, \"z\": null}"); got["amount"] != "5" || got["x"] != "1" || len(got) != 2 {
		t.Errorf("parseFill %v", got)
	}
}
