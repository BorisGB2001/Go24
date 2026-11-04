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
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestPhoneSummary: CoPilot's summary of a conversation — the AI of the employee who talked (else the company's) is
// given the record's text with who called whom and writes the summary, kept with the call and shown by REST; a silent
// record is not summed up; with no AI connection — ErrNone; the key's owner asks for it again by REST.
func TestPhoneSummary(t *testing.T) {
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
	_, err = pool.Exec(ctx, `INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, started_at, status, record, line, seconds, transcript)
		VALUES ('s1', '102', '89000000001', $1, now(), 'answered', '102.wav', '+74950000000', 65, 'Здравствуйте, привезём заказ в пятницу.'),
		       ('s2', '102', '89000000002', $1, now(), 'answered', '103.wav', '+74950000000', 5, 'В записи нет речи')`, bob)
	must(err)
	callOf := func(uid string) telephony.Call {
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM telephony_calls WHERE uniqueid = $1`, uid).Scan(&id))
		c, err := store.CallByID(ctx, id)
		must(err)
		return c
	}

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
		io.WriteString(w, `{"content":[{"type":"text","text":" Договорились: доставка в пятницу. "}],"usage":{"input_tokens":50,"output_tokens":8}}`)
	}))
	defer prov.Close()

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	aiStore := ai.NewStore(pool, "secret")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, AI: aiStore,
		Telephony: &telephony.PBX{Store: store, Records: t.TempDir(), Key: "ast-key", Dir: t.TempDir()},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// no AI connection yet
	if err := srv.phoneSummarize(ctx, callOf("s1"), ""); !errors.Is(err, ai.ErrNone) {
		t.Fatalf("no connection: %v", err)
	}
	_, err = aiStore.Save(ctx, bob, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	must(srv.phoneSummarize(ctx, callOf("s1"), ""))
	c := callOf("s1")
	if c.Summary != "Договорились: доставка в пятницу." {
		t.Fatalf("summary %q", c.Summary)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "CoPilot") || !strings.Contains(asked[0], "89000000001") || !strings.Contains(asked[0], "в пятницу") {
		t.Fatalf("asked %q", asked)
	}
	var used int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage WHERE user_id = $1 AND agent = 'telephony' AND in_tokens = 50`, bob).Scan(&used))
	if used != 1 {
		t.Fatalf("usage %d", used)
	}

	// a silent record is not summed up
	silent := callOf("s2")
	must(store.SetTranscript(ctx, silent.ID, srv.portalTr("im.voice_silent")))
	if err := srv.phoneSummarize(ctx, callOf("s2"), ""); err == nil || len(asked) != 1 {
		t.Fatalf("silent: %v, asked %d", err, len(asked))
	}

	// REST: the summary is with the call; asked again through the key owner's AI
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	k, err := keys.Create(ctx, bob, "k", []string{"telephony:read", "telephony:write"}, time.Hour)
	must(err)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/v1/telephony/calls/"+c.ID+"/summary", nil)
	req.Header.Set("Authorization", "Bearer "+k)
	resp, err := http.DefaultClient.Do(req)
	must(err)
	defer resp.Body.Close()
	var out apiTelCall
	must(json.NewDecoder(resp.Body).Decode(&out))
	if resp.StatusCode != http.StatusOK || out.Summary != "Договорились: доставка в пятницу." || len(asked) != 2 {
		t.Fatalf("REST: %d %+v, asked %d", resp.StatusCode, out, len(asked))
	}
}
