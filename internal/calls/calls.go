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
// what the others see.
func (p *Provider) Token(identity, name, room string, now time.Time) (string, error) {
	if p == nil {
		return "", errors.New("calls: not configured")
	}
	return p.sign(map[string]any{
		"iss": p.Key, "sub": identity, "name": name, "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(TokenTTL).Unix(),
		"video": map[string]any{"room": room, "roomJoin": true, "canPublish": true, "canSubscribe": true, "canPublishData": true},
	})
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

// Participants are the identities (go24 user IDs) in a LiveKit room now, without a recorder; none — the room is
// empty or gone.
func (p *Provider) Participants(ctx context.Context, room string) ([]string, error) {
	var out struct {
		Participants []struct {
			Identity string `json:"identity"`
			Kind     any    `json:"kind"`
		} `json:"participants"`
	}
	found, err := p.roomService(ctx, room, "ListParticipants", map[string]string{"room": room}, &out)
	if err != nil || !found { // the room is gone: nobody
		return nil, err
	}
	var ids []string
	for _, x := range out.Participants {
		if x.Kind == "EGRESS" || x.Kind == float64(4) { // the recorder of the call is nobody of it
			continue
		}
		ids = append(ids, x.Identity)
	}
	return ids, nil
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
