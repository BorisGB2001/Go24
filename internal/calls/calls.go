// Package calls is «Звонки»: audio and video calls of go24 on LiveKit (an own server or LiveKit Cloud — the same
// API, only the address and the keys differ). go24 decides who may join a room and signs the room token; the
// browser then talks to LiveKit itself.
package calls

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider is a LiveKit deployment.
type Provider struct {
	URL, Key, Secret string // the URL browsers connect to (wss://…)
	APIURL           string // LiveKit's API as go24 reaches it (http://livekit:7880); "" — go24 does not ask it
	HTTP             *http.Client
}

// New is nil when calls are not configured.
func New(url, key, secret string) *Provider {
	if url == "" || key == "" || secret == "" {
		return nil
	}
	return &Provider{URL: url, Key: key, Secret: secret}
}

// TokenTTL is how long a token lets one join (a call may go on longer: the connection keeps it).
const TokenTTL = 6 * time.Hour

// Token is a LiveKit access token (a JWT signed with HS256) to join one room: identity is the go24 user id, name
// what the others see; host — the person leads the call (as «ведущий»), which everyone in it sees in
// the participant's metadata.
func (p *Provider) Token(identity, name, room string, host bool, now time.Time) (string, error) {
	if p == nil {
		return "", errors.New("calls: not configured")
	}
	c := map[string]any{
		"iss": p.Key, "sub": identity, "name": name, "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(TokenTTL).Unix(),
		"video": map[string]any{"room": room, "roomJoin": true, "canPublish": true, "canSubscribe": true, "canPublishData": true},
	}
	if host {
		c["metadata"] = `{"host":true}`
	}
	return p.sign(c)
}

// sign makes a JWT (HS256) of the claims with the API secret.
func (p *Provider) sign(c map[string]any) (string, error) {
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claims, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	mac := hmac.New(sha256.New, []byte(p.Secret))
	mac.Write([]byte(unsigned))
	return unsigned + "." + enc.EncodeToString(mac.Sum(nil)), nil
}

// ErrNoAPI: go24 has no address of LiveKit's API.
var ErrNoAPI = errors.New("calls: no LiveKit API address")

// participant is a participant of a LiveKit room as its RoomService tells (enums come as names or as numbers).
type participant struct {
	Identity string `json:"identity"`
	Kind     any    `json:"kind"`
	Tracks   []struct {
		Sid    string `json:"sid"`
		Source any    `json:"source"`
		Muted  bool   `json:"muted"`
	} `json:"tracks"`
}

// recorder: the participant is the recorder of the call (LiveKit Egress), nobody of it.
func (x participant) recorder() bool { return x.Kind == "EGRESS" || x.Kind == float64(4) }

// Sources of tracks as go24 names them (LiveKit's TrackSource).
const (
	Microphone = "microphone"
	Camera     = "camera"
	Screen     = "screen_share" // the screen and its sound
)

// trackSource is the source of a track: LiveKit's enum by its name or its number.
func trackSource(v any) string {
	switch v {
	case "CAMERA", float64(1):
		return Camera
	case "MICROPHONE", float64(2):
		return Microphone
	case "SCREEN_SHARE", float64(3), "SCREEN_SHARE_AUDIO", float64(4):
		return Screen
	}
	return ""
}

// participants are the people in a LiveKit room now with their tracks, without a recorder; none — the room is
// empty or gone.
func (p *Provider) participants(ctx context.Context, room string) ([]participant, error) {
	var out struct {
		Participants []participant `json:"participants"`
	}
	found, err := p.roomService(ctx, room, "ListParticipants", map[string]string{"room": room}, &out)
	if err != nil || !found { // the room is gone: nobody
		return nil, err
	}
	var ps []participant
	for _, x := range out.Participants {
		if !x.recorder() {
			ps = append(ps, x)
		}
	}
	return ps, nil
}

// Participants are the identities (go24 user IDs) in a LiveKit room now, without a recorder; none — the room is
// empty or gone.
func (p *Provider) Participants(ctx context.Context, room string) ([]string, error) {
	ps, err := p.participants(ctx, room)
	var ids []string
	for _, x := range ps {
		ids = append(ids, x.Identity)
	}
	return ids, err
}

// Mute turns off the tracks of the source (Microphone, Camera, Screen) of the participant — or, identity "", of
// everyone in the room but except — as the host of a call does; the person may turn them on again.
// It gives how many people it muted.
func (p *Provider) Mute(ctx context.Context, room, identity, source, except string) (int, error) {
	ps, err := p.participants(ctx, room)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, x := range ps {
		if (identity != "" && x.Identity != identity) || x.Identity == except {
			continue
		}
		muted := false
		for _, t := range x.Tracks {
			if t.Muted || trackSource(t.Source) != source {
				continue
			}
			if _, err := p.roomService(ctx, room, "MutePublishedTrack", map[string]any{"room": room, "identity": x.Identity,
				"track_sid": t.Sid, "muted": true}, nil); err != nil {
				return n, err
			}
			muted = true
		}
		if muted {
			n++
		}
	}
	return n, nil
}

// RemoveParticipant puts the participant out of the room (a guest whose link is revoked); one not there is no error.
func (p *Provider) RemoveParticipant(ctx context.Context, room, identity string) error {
	_, err := p.roomService(ctx, room, "RemoveParticipant", map[string]string{"room": room, "identity": identity}, nil)
	return err
}

// roomService calls LiveKit's RoomService as the room's admin; found false — no such room or participant.
func (p *Provider) roomService(ctx context.Context, room, method string, in, out any) (bool, error) {
	return p.twirp(ctx, "RoomService", method, map[string]any{"room": room, "roomAdmin": true}, in, out)
}

// twirp calls a service of LiveKit's API with a short token of the grant; found false — LiveKit has no such thing.
func (p *Provider) twirp(ctx context.Context, service, method string, grant map[string]any, in, out any) (bool, error) {
	if p == nil || p.APIURL == "" {
		return false, ErrNoAPI
	}
	now := time.Now()
	tok, err := p.sign(map[string]any{"iss": p.Key, "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix(),
		"video": grant})
	if err != nil {
		return false, err
	}
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.APIURL, "/")+"/twirp/livekit."+service+"/"+method, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	client := p.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var e struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(raw, &e)
	if res.StatusCode == http.StatusNotFound || e.Code == "not_found" {
		return false, nil
	}
	if res.StatusCode != http.StatusOK {
		return false, fmt.Errorf("livekit: %s: %s %s", method, res.Status, e.Code)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return false, err
		}
	}
	return true, nil
}
