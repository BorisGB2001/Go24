package sms

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"+7 (900) 555-44-33": "+79005554433", "8 900 555 44 33": "+79005554433",
		"9005554433": "+79005554433", "+1 415 555 0100": "+14155550100", "12-34": "", "": "", "+44 20 7946 0958": "+442079460958"} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParts(t *testing.T) {
	for _, c := range []struct {
		text string
		want int
	}{{"", 0}, {strings.Repeat("a", 160), 1}, {strings.Repeat("a", 161), 2}, {strings.Repeat("a", 306), 2}, {strings.Repeat("a", 307), 3},
		{strings.Repeat("я", 70), 1}, {strings.Repeat("я", 71), 2}, {strings.Repeat("a", 100) + "я", 2}} {
		if got := Parts(c.text); got != c.want {
			t.Errorf("Parts(%d runes) = %d, want %d", len([]rune(c.text)), got, c.want)
		}
	}
}

// TestDeliver: each provider's call and how its answers (sent, refused) are read.
func TestDeliver(t *testing.T) {
	ctx := context.Background()
	var got Request
	answer := Response{}
	s := &Store{Post: func(_ context.Context, r Request) (Response, error) { got = r; return answer, nil }}
	cases := []struct {
		x            Sender
		ok, refused  Response
		wantID, why  string
		check        func(Request) bool
	}{
		{Sender{Kind: "smsru", From: "Shop"},
			Response{200, []byte(`{"status":"OK","sms":{"79005554433":{"status":"OK","sms_id":"000-1"}}}`)},
			Response{200, []byte(`{"status":"OK","sms":{"79005554433":{"status":"ERROR","status_text":"Неверный номер"}}}`)},
			"000-1", "Неверный номер",
			func(r Request) bool {
				return r.URL == SMSRuURL && r.Form.Get("api_id") == "KEY" && r.Form.Get("to") == "79005554433" && r.Form.Get("from") == "Shop"
			}},
		{Sender{Kind: "smsc", Login: "shop"},
			Response{200, []byte(`{"id":17,"cnt":1}`)}, Response{200, []byte(`{"error":"authorise error","error_code":2}`)},
			"17", "authorise error",
			func(r Request) bool { return r.Form.Get("login") == "shop" && r.Form.Get("psw") == "KEY" && r.Form.Get("mes") == "Привет" }},
		{Sender{Kind: "twilio", Login: "AC1", From: "+14155550100"},
			Response{201, []byte(`{"sid":"SM1"}`)}, Response{400, []byte(`{"code":21211,"message":"Invalid 'To' Phone Number"}`)},
			"SM1", "Invalid 'To' Phone Number",
			func(r Request) bool {
				return strings.Contains(r.URL, "/Accounts/AC1/") && r.User == "AC1" && r.Pass == "KEY" && r.Form.Get("To") == "+79005554433"
			}},
		{Sender{Kind: "gateway", Login: "http://gw/send", From: "Shop"},
			Response{200, []byte(`{"id":"g-5"}`)}, Response{502, []byte(`{"error":"no balance"}`)},
			"g-5", "no balance",
			func(r Request) bool {
				m, _ := r.JSON.(map[string]string)
				return r.URL == "http://gw/send" && r.Bearer == "KEY" && m["phone"] == "+79005554433" && m["text"] == "Привет" && m["sender"] == "Shop"
			}},
	}
	for _, c := range cases {
		answer = c.ok
		id, err := s.deliver(ctx, c.x, "KEY", "+79005554433", "Привет")
		if err != nil || id != c.wantID || !c.check(got) {
			t.Fatalf("%s sent: %q %v %+v", c.x.Kind, id, err, got)
		}
		answer = c.refused
		if _, err := s.deliver(ctx, c.x, "KEY", "+79005554433", "Привет"); err == nil || err.Error() != c.why {
			t.Fatalf("%s refused: %v", c.x.Kind, err)
		}
	}
	answer = Response{500, []byte("oops")}
	if _, err := s.deliver(ctx, Sender{Kind: "smsru"}, "KEY", "+79005554433", "x"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("not JSON: %v", err)
	}
}

// TestStore: providers (the first is the default one, the key is kept sealed and not changed by an empty one), sending
// through the default or a given one, the log of sent and refused messages.
func TestStore(t *testing.T) {
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
	s := NewStore(pool, "secret")
	var calls []Request
	refuse := false
	s.Post = func(_ context.Context, r Request) (Response, error) {
		calls = append(calls, r)
		if refuse {
			return Response{502, []byte(`{"error":"no balance"}`)}, nil
		}
		if r.URL == SMSRuURL {
			return Response{200, []byte(`{"status":"OK","sms":{"79005554433":{"status":"OK","sms_id":"r1"}}}`)}, nil
		}
		return Response{200, []byte(`{"id":"m1"}`)}, nil
	}
	var ie *i18n.Error
	if _, err := s.Send(ctx, Out{Phone: "+79005554433", Text: "x"}); !errors.As(err, &ie) || ie.Key != "err.sms_no_sender" {
		t.Fatalf("no provider: %v", err)
	}
	if s.Ready(ctx) {
		t.Fatal("not ready without providers")
	}
	if _, err := s.SaveSender(ctx, Sender{Kind: "smsru", Name: "SMS.ru"}, ""); err == nil {
		t.Fatal("SMS.ru needs its key")
	}
	if _, err := s.SaveSender(ctx, Sender{Kind: "gateway", Name: "GW", Login: "ftp://x"}, ""); err == nil {
		t.Fatal("the gateway needs an http address")
	}
	gw, err := s.SaveSender(ctx, Sender{Kind: "gateway", Name: "GW", Login: "http://gw/send", Active: true}, "TOKEN")
	must(err)
	ru, err := s.SaveSender(ctx, Sender{Kind: "smsru", Name: "SMS.ru", Active: true}, "RUKEY")
	must(err)
	list, err := s.Senders(ctx)
	must(err)
	if len(list) != 2 || list[0].ID != gw || !list[0].Default || list[1].Default || !list[0].HasKey {
		t.Fatalf("the first is the default one: %+v", list)
	}
	var raw []byte
	must(pool.QueryRow(ctx, `SELECT secret FROM sms_senders WHERE id = $1`, gw).Scan(&raw))
	if strings.Contains(string(raw), "TOKEN") {
		t.Fatal("the key is sealed")
	}
	_, err = s.SaveSender(ctx, Sender{ID: gw, Kind: "gateway", Name: "Шлюз", Login: "http://gw/send2", Active: true}, "")
	must(err)

	m, err := s.Send(ctx, Out{Phone: "8 900 555-44-33", Text: "Привет", AuthorID: ""})
	must(err)
	if m.Status != "sent" || m.ExtID != "m1" || m.Phone != "+79005554433" || len(calls) != 1 || calls[0].Bearer != "TOKEN" || calls[0].URL != "http://gw/send2" {
		t.Fatalf("sent through the default one, its key kept: %+v %+v", m, calls)
	}
	refuse = true
	if m, err = s.Send(ctx, Out{Phone: "+79005554433", Text: "x"}); err == nil || m.Status != "error" || m.Error != "no balance" {
		t.Fatalf("refused: %+v %v", m, err)
	}
	refuse = false
	_, err = s.Send(ctx, Out{SenderID: ru, Phone: "+79005554433", Text: "x"})
	must(err)
	if calls[2].URL != SMSRuURL || calls[2].Form.Get("api_id") != "RUKEY" {
		t.Fatalf("through the given one: %+v", calls[2])
	}
	if _, err := s.Send(ctx, Out{Phone: "123", Text: "x"}); !errors.As(err, &ie) || ie.Key != "err.sms_phone" {
		t.Fatalf("a wrong phone: %v", err)
	}
	var sent, failed int
	must(pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'sent'), count(*) FILTER (WHERE status = 'error') FROM sms_messages`).Scan(&sent, &failed))
	if sent != 2 || failed != 1 {
		t.Fatalf("the log: %d sent, %d refused", sent, failed)
	}
	must(s.SetDefault(ctx, ru))
	must(s.DeleteSender(ctx, ru))
	if list, _ = s.Senders(ctx); len(list) != 1 || !list[0].Default {
		t.Fatalf("the default one deleted, another becomes it: %+v", list)
	}
}
