package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/matrixsync"
)

// TestElementCallToken: Element Call asks go24 (the room's MatrixRTC focus) for its LiveKit token — the person in
// the chat gets the LiveKit room of the chat's call in go24, identity «@user:server:DEVICE» and their go24 name;
// a bad OpenID token, someone not in the chat, a token of another server get nothing; /get_token sends Element
// Call to /sfu/get, delegated leave events are declined (it keeps them itself), the preflight is allowed.
func TestElementCallToken(t *testing.T) {
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
	user := func(name, mxid string) {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		_, err := pool.Exec(ctx, `INSERT INTO matrix_users (user_id, mxid, token) VALUES ($1, $2, $2)`, id, mxid)
		must(err)
	}
	user("Анна Иванова", "@anna:go24.test")
	user("Пётр", "@petr:go24.test")
	var anna string
	must(pool.QueryRow(ctx, `SELECT user_id::text FROM matrix_users WHERE mxid = '@anna:go24.test'`).Scan(&anna))

	const chat = "!chat:go24.test"
	var mu sync.Mutex
	members := map[string]map[string]any{} // state key → the m.call.member put (by whose token)
	var memberBy []string
	syn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/_matrix/client/v3/rooms/"+chat+"/state/"+matrix.CallMemberType+"/"):
			key := strings.TrimPrefix(r.URL.Path, "/_matrix/client/v3/rooms/"+chat+"/state/"+matrix.CallMemberType+"/")
			var c map[string]any
			_ = json.NewDecoder(r.Body).Decode(&c)
			members[key] = c
			memberBy = append(memberBy, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			writeJSON(w, 200, map[string]string{"event_id": "$m"})
		case r.URL.Path == "/_matrix/federation/v1/openid/userinfo":
			switch r.URL.Query().Get("access_token") {
			case "anna-openid":
				writeJSON(w, 200, map[string]string{"sub": "@anna:go24.test"})
			case "petr-openid":
				writeJSON(w, 200, map[string]string{"sub": "@petr:go24.test"})
			default:
				writeJSON(w, 401, map[string]string{"errcode": "M_UNKNOWN_TOKEN"})
			}
		case r.URL.Path == "/_synapse/admin/v1/rooms/"+chat+"/state":
			anna, petr := "@anna:go24.test", "@petr:go24.test"
			state := []map[string]any{
				{"type": "m.room.member", "state_key": anna, "content": map[string]string{"membership": "join"}},
				{"type": "m.room.member", "state_key": petr, "content": map[string]string{"membership": "leave"}},
			}
			for key, c := range members {
				state = append(state, map[string]any{"type": matrix.CallMemberType, "state_key": key, "sender": anna, "content": c})
			}
			writeJSON(w, 200, map[string]any{"state": state})
		default:
			w.WriteHeader(404)
		}
	}))
	defer syn.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &Server{DB: pool, Log: log, CallURL: "https://go24.test:8444", Calls: calls.New("wss://lk.test", "key", "secret"),
		Matrix: matrixsync.New(&matrixsync.Service{DB: pool, Log: log, M: matrix.New(syn.URL, "go24.test", "bot")})}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	post := func(path, body string) (int, map[string]any, http.Header) {
		t.Helper()
		res, err := http.Post(hs.URL+path, "application/json", strings.NewReader(body))
		must(err)
		defer res.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out, res.Header
	}
	ask := func(token, server string) string {
		return `{"room": "` + chat + `", "device_id": "DEV1", "openid_token": {"access_token": "` + token +
			`", "matrix_server_name": "` + server + `", "expires_in": 3600, "token_type": "Bearer"}}`
	}

	st, out, h := post("/matrixrtc/sfu/get", ask("anna-openid", "go24.test"))
	if st != 200 || out["url"] != "wss://lk.test" || h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("Анна's token: %d %v", st, out)
	}
	jwt, _ := out["jwt"].(string)
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Sub   string `json:"sub"`
		Name  string `json:"name"`
		Video struct {
			Room     string `json:"room"`
			RoomJoin bool   `json:"roomJoin"`
		} `json:"video"`
	}
	must(json.Unmarshal(raw, &claims))
	if claims.Sub != "@anna:go24.test:DEV1" || claims.Name != "Анна Иванова" || claims.Video.Room != imCallRoom(chat) || !claims.Video.RoomJoin {
		t.Fatalf("the token's claims: %+v", claims)
	}

	for name, body := range map[string]string{
		"a bad OpenID token":        ask("forged", "go24.test"),
		"someone who left the chat": ask("petr-openid", "go24.test"),
		"another server's token":    ask("anna-openid", "evil.test"),
		"no device":                 `{"room": "` + chat + `", "openid_token": {"access_token": "anna-openid"}}`,
	} {
		if st, out, _ := post("/matrixrtc/sfu/get", body); st == 200 || out["jwt"] != nil {
			t.Errorf("%s got a token: %d", name, st)
		}
	}
	if st, out, _ := post("/matrixrtc/sfu/get", strings.Replace(ask("anna-openid", "go24.test"), `"device_id"`,
		`"delay_id": "d1", "delay_timeout": 18000, "delay_cs_api_url": "https://m", "device_id"`, 1)); st != 400 || out["errcode"] != "M_BAD_JSON" {
		t.Errorf("a delegated leave event: %d %v", st, out)
	}
	if st, _, _ := post("/matrixrtc/get_token", `{}`); st != 404 {
		t.Errorf("/get_token: %d", st)
	}
	req, _ := http.NewRequest(http.MethodOptions, hs.URL+"/matrixrtc/sfu/get", nil)
	res, err := http.DefaultClient.Do(req)
	must(err)
	res.Body.Close()
	if res.StatusCode != 204 || !strings.Contains(res.Header.Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Errorf("the preflight: %d", res.StatusCode)
	}

	// go24's call page of the chat: Анна is in the call for Element as well, with go24 as the focus
	id := srv.elementCallJoin(ctx, anna, chat, imCallRoom(chat), false)
	key := "_@anna:go24.test_GO24WEB_m.call"
	m := members[key]
	foci, _ := m["foci_preferred"].([]any)
	focus, _ := foci[0].(map[string]any)
	if id != "@anna:go24.test:GO24WEB" || m["device_id"] != "GO24WEB" || m["application"] != "m.call" || m["m.call.intent"] != "audio" ||
		focus["livekit_service_url"] != "https://go24.test:8444/matrixrtc" || focus["livekit_alias"] != imCallRoom(chat) ||
		len(memberBy) != 1 || memberBy[0] != "@anna:go24.test" {
		t.Fatalf("Анна's membership: %s %v by %v", id, m, memberBy)
	}
	if got := srv.callUsers(ctx, []string{id, "@petr:go24.test:DEV1", "guest-1"}); len(got) != 3 || !slices.Contains(got, anna) {
		t.Errorf("the people of the identities: %v", got)
	}
	srv.leftElementCall(ctx, chat, []string{id})                     // still in LiveKit
	srv.leftElementCall(ctx, chat, []string{"@petr:go24.test:DEV1"}) // has just opened the page
	if len(members[key]) == 0 {
		t.Fatal("the membership of one in the call is gone")
	}
	members[key]["created_ts"] = float64(time.Now().Add(-2 * time.Minute).UnixMilli())
	srv.leftElementCall(ctx, chat, []string{"@petr:go24.test:DEV1"})
	if len(members[key]) != 0 {
		t.Fatalf("Анна left the call page, her membership stays: %v", members[key])
	}
}
