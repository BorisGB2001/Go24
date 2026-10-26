package calls

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestToken(t *testing.T) {
	if New("wss://x", "", "s") != nil {
		t.Fatal("no key: calls are off")
	}
	p := New("wss://lk", "key", "secret")
	now := time.Unix(1_800_000_000, 0)
	tok, err := p.Token("u1", "Анна", "task-GO-1", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %s", tok)
	}
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	if base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) != parts[2] {
		t.Fatal("bad signature")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss, Sub, Name string
		Exp, Nbf       int64
		Video          struct {
			Room     string
			RoomJoin bool
		}
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.Iss != "key" || c.Sub != "u1" || c.Name != "Анна" || c.Video.Room != "task-GO-1" || !c.Video.RoomJoin ||
		c.Exp != now.Add(TokenTTL).Unix() || c.Nbf >= now.Unix() {
		t.Fatalf("claims = %+v", c)
	}
}

// TestParticipants: go24 asks LiveKit's RoomService who is in a room (the recorder is nobody of it), with a token
// for that room only; a room that is gone has nobody; no API address — go24 does not ask.
func TestParticipants(t *testing.T) {
	var auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if r.URL.Path != "/twirp/livekit.RoomService/ListParticipants" {
			w.WriteHeader(404)
			return
		}
		if strings.Contains(body, "gone") {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":"not_found","msg":"room not found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"participants":[{"identity":"u1","name":"Анна"},{"identity":"EG_x","kind":"EGRESS"},{"identity":"u2"}]}`))
	}))
	defer srv.Close()
	p := New("wss://lk", "key", "secret")
	if _, err := p.Participants(context.Background(), "im-1"); err != ErrNoAPI {
		t.Fatalf("without the API address: %v", err)
	}
	p.APIURL = srv.URL + "/"
	ids, err := p.Participants(context.Background(), "im-1")
	if err != nil || strings.Join(ids, ",") != "u1,u2" || body != `{"room":"im-1"}` {
		t.Fatalf("participants: %v %v %s", ids, err, body)
	}
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss   string `json:"iss"`
		Video struct {
			Room      string `json:"room"`
			RoomAdmin bool   `json:"roomAdmin"`
			RoomJoin  bool   `json:"roomJoin"`
		} `json:"video"`
	}
	if len(parts) != 3 || json.Unmarshal(raw, &c) != nil || c.Iss != "key" || c.Video.Room != "im-1" || !c.Video.RoomAdmin || c.Video.RoomJoin {
		t.Fatalf("the API token: %s", raw)
	}
	if ids, err := p.Participants(context.Background(), "gone"); err != nil || ids != nil {
		t.Fatalf("a room that is gone: %v %v", ids, err)
	}
}
