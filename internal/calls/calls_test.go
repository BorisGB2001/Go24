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
	tok, err := p.Token("u1", "Анна", "task-GO-1", false, now)
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

// TestHostAndMute: the host's token carries {"host":true} in the metadata; Mute turns off the tracks of one source
// of a participant or of everyone but the host (sources by name or number, muted ones and the recorder left alone).
func TestHostAndMute(t *testing.T) {
	p := New("wss://lk", "key", "secret")
	tok, _ := p.Token("u1", "Анна", "im-1", true, time.Now())
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	if !strings.Contains(string(raw), `"metadata":"{\"host\":true}"`) {
		t.Fatalf("host token: %s", raw)
	}
	tok, _ = p.Token("u2", "Борис", "im-1", false, time.Now())
	if raw, _ = base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1]); strings.Contains(string(raw), "metadata") {
		t.Fatalf("not a host: %s", raw)
	}
	var muted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/twirp/livekit.RoomService/ListParticipants":
			_, _ = w.Write([]byte(`{"participants":[
				{"identity":"u1","tracks":[{"sid":"TR_a1","source":"MICROPHONE"}]},
				{"identity":"u2","tracks":[{"sid":"TR_b1","source":2},{"sid":"TR_b2","source":"CAMERA"},{"sid":"TR_b3","source":"SCREEN_SHARE"},{"sid":"TR_b4","source":"SCREEN_SHARE_AUDIO"}]},
				{"identity":"u3","tracks":[{"sid":"TR_c1","source":"MICROPHONE","muted":true}]},
				{"identity":"EG_x","kind":"EGRESS","tracks":[{"sid":"TR_e","source":"MICROPHONE"}]}]}`))
		case "/twirp/livekit.RoomService/MutePublishedTrack":
			var in struct {
				Identity string `json:"identity"`
				TrackSid string `json:"track_sid"`
				Muted    bool   `json:"muted"`
			}
			_ = json.Unmarshal(b, &in)
			if in.Muted {
				muted = append(muted, in.Identity+"/"+in.TrackSid)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p.APIURL = srv.URL
	ctx := context.Background()
	if n, err := p.Mute(ctx, "im-1", "", Microphone, "u1"); err != nil || n != 1 || strings.Join(muted, ",") != "u2/TR_b1" {
		t.Fatalf("mute all: %d %v %v", n, err, muted)
	}
	muted = nil
	if n, err := p.Mute(ctx, "im-1", "u2", Screen, "u1"); err != nil || n != 1 || strings.Join(muted, ",") != "u2/TR_b3,u2/TR_b4" {
		t.Fatalf("stop the screen: %d %v %v", n, err, muted)
	}
	muted = nil
	if n, err := p.Mute(ctx, "im-1", "u3", Camera, "u1"); err != nil || n != 0 || muted != nil {
		t.Fatalf("no camera: %d %v %v", n, err, muted)
	}
}
