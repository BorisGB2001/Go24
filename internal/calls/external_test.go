package calls

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestExternalMeetings: Zoom — the token of the Server-to-Server app (account_credentials, kept for the next
// meeting), the meeting hosted by the employee, by the account's owner when Zoom has no such user; Телемост — a
// public meeting with the employee as co-host, OAuth token; VK — calls.start by the community, an error of VK told.
func TestExternalMeetings(t *testing.T) {
	var tokens int
	var log []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		log = append(log, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		switch {
		case r.URL.Path == "/oauth/token":
			user, pass, _ := r.BasicAuth()
			if r.URL.Query().Get("grant_type") != "account_credentials" || r.URL.Query().Get("account_id") != "acc" || user != "cid" || pass != "zsecret" {
				w.WriteHeader(401)
				return
			}
			tokens++
			_, _ = w.Write([]byte(`{"access_token":"ztok","expires_in":3600}`))
		case r.URL.Path == "/zoom/users/stranger@t/meetings":
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":1001,"message":"User does not exist"}`))
		case strings.HasPrefix(r.URL.Path, "/zoom/users/"):
			_, _ = w.Write([]byte(`{"id":85012345678,"join_url":"https://zoom.us/j/85012345678?pwd=x"}`))
		case r.URL.Path == "/telemost/conferences":
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"05868601","join_url":"https://telemost.yandex.ru/j/05868601"}`))
		case r.URL.Path == "/vk/calls.start":
			q, _ := url.ParseQuery(string(b))
			if q.Get("group_id") == "-7" {
				_, _ = w.Write([]byte(`{"error":{"error_code":15,"error_msg":"Access denied"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"response":{"join_link":"https://vk.com/call/join/abc","call_id":"42"}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	x := NewExternal(nil, "s")
	x.ZoomAPI, x.ZoomOAuth, x.TelemostAPI, x.VKAPI = srv.URL+"/zoom", srv.URL+"/oauth/token", srv.URL+"/telemost", srv.URL+"/vk"
	ctx := context.Background()

	zoom := ServiceConfig{Service: "zoom", Enabled: true, AccountID: "acc", ClientID: "cid", secret: "zsecret"}
	if !zoom.Ready() || (ServiceConfig{Service: "zoom", Enabled: true, secret: "x"}).Ready() || (ServiceConfig{Service: "telegram", Enabled: true, secret: "x"}).Ready() {
		t.Fatal("Ready")
	}
	m, err := x.zoomMeeting(ctx, zoom, "anna@t", "Созвон")
	if err != nil || m.URL != "https://zoom.us/j/85012345678?pwd=x" || m.ID != "85012345678" {
		t.Fatalf("zoom: %+v %v", m, err)
	}
	if !strings.Contains(log[1], "POST /zoom/users/anna@t/meetings Bearer ztok") || !strings.Contains(log[1], `"topic":"Созвон"`) {
		t.Fatalf("zoom asked %v", log)
	}
	log = nil
	if m, err = x.zoomMeeting(ctx, zoom, "stranger@t", "Созвон"); err != nil || m.URL == "" || tokens != 1 {
		t.Fatalf("zoom by the owner: %+v %v, tokens %d", m, err, tokens)
	}
	if len(log) != 2 || !strings.Contains(log[1], "/zoom/users/me/meetings") {
		t.Fatalf("zoom asked %v", log)
	}
	if _, err := x.zoomMeeting(ctx, ServiceConfig{Service: "zoom", Enabled: true, AccountID: "other", ClientID: "cid", secret: "bad"}, "", "x"); err == nil {
		t.Fatal("zoom with wrong keys made a meeting")
	}

	log = nil
	m, err = x.telemostMeeting(ctx, ServiceConfig{Service: "telemost", Enabled: true, secret: "ytok"}, "anna@t")
	if err != nil || m.URL != "https://telemost.yandex.ru/j/05868601" {
		t.Fatalf("telemost: %+v %v", m, err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(log[0][strings.Index(log[0], "{"):]), &body)
	if !strings.Contains(log[0], "OAuth ytok") || body["waiting_room_level"] != "PUBLIC" || body["cohosts"].([]any)[0].(map[string]any)["email"] != "anna@t" {
		t.Fatalf("telemost asked %v", log)
	}

	log = nil
	m, err = x.vkMeeting(ctx, ServiceConfig{Service: "vk", Enabled: true, secret: "vtok", GroupID: "123"})
	if err != nil || m.URL != "https://vk.com/call/join/abc" || m.ID != "42" || !strings.Contains(log[0], "group_id=-123") || !strings.Contains(log[0], "access_token=vtok") {
		t.Fatalf("vk: %+v %v %v", m, err, log)
	}
	if _, err := x.vkMeeting(ctx, ServiceConfig{Service: "vk", Enabled: true, secret: "vtok", GroupID: "-7"}); err == nil || !strings.Contains(err.Error(), "Access denied") {
		t.Fatalf("vk error: %v", err)
	}

	sealed, err := x.seal("тайна")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := x.open(sealed); err != nil || plain != "тайна" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := NewExternal(nil, "other").open(sealed); err == nil {
		t.Fatal("another secret opened the key")
	}
}
