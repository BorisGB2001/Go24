package matrix

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

// ---------- admin API (Synapse) ----------

type AdminUser struct {
	Name        string `json:"name"`
	Deactivated bool   `json:"deactivated"`
	DisplayName string `json:"displayname"`
}

// GetUser returns nil when the account does not exist.
func (c *Client) GetUser(ctx context.Context, mxid string) (*AdminUser, error) {
	var u AdminUser
	err := c.Do(ctx, http.MethodGet, "/_synapse/admin/v2/users/"+esc(mxid), nil, &u)
	if IsCode(err, "M_NOT_FOUND") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// PutUser creates or updates an account linked to the employee's Authentik identity (SSO finds it at login).
func (c *Client) PutUser(ctx context.Context, mxid, displayName, idp, externalID string, deactivated bool) error {
	body := map[string]any{"displayname": displayName, "deactivated": deactivated}
	if externalID != "" {
		body["external_ids"] = []map[string]string{{"auth_provider": idp, "external_id": externalID}}
	}
	return c.Do(ctx, http.MethodPut, "/_synapse/admin/v2/users/"+esc(mxid), body, nil)
}

// Deactivate closes the account: the person leaves every room and can no longer sign in. Messages stay.
func (c *Client) Deactivate(ctx context.Context, mxid string) error {
	return c.Do(ctx, http.MethodPost, "/_synapse/admin/v1/deactivate/"+esc(mxid), map[string]any{"erase": false}, nil)
}

// LoginAs gives the bot a token of that employee (to post their messages from go24 under their name).
func (c *Client) LoginAs(ctx context.Context, mxid string) (string, error) {
	var out struct {
		Token string `json:"access_token"`
	}
	err := c.Do(ctx, http.MethodPost, "/_synapse/admin/v1/users/"+esc(mxid)+"/login", map[string]any{}, &out)
	return out.Token, err
}

// ForceJoin puts a local user into a room the bot administers (no invitation to accept).
func (c *Client) ForceJoin(ctx context.Context, roomID, mxid string) error {
	return c.Do(ctx, http.MethodPost, "/_synapse/admin/v1/join/"+esc(roomID), map[string]string{"user_id": mxid}, nil)
}

// ---------- rooms ----------

// CreateRoom makes a private, invite-only, unencrypted room (or a space) owned by the bot.
func (c *Client) CreateRoom(ctx context.Context, name, topic string, space bool) (string, error) {
	body := map[string]any{
		"name": name, "topic": topic, "preset": "private_chat", "visibility": "private",
		"initial_state": []map[string]any{
			{"type": "m.room.history_visibility", "state_key": "", "content": map[string]string{"history_visibility": "shared"}},
			{"type": "m.room.guest_access", "state_key": "", "content": map[string]string{"guest_access": "forbidden"}},
		},
		"power_level_content_override": map[string]any{"invite": 100, "kick": 100, "ban": 100, "state_default": 100,
			"events": map[string]int{"m.room.name": 100, "m.room.topic": 100, "m.room.power_levels": 100, CallType: 0, CallMemberType: 0}},
	}
	if space {
		body["creation_content"] = map[string]string{"type": "m.space"}
	}
	var out struct {
		RoomID string `json:"room_id"`
	}
	err := c.Do(ctx, http.MethodPost, "/_matrix/client/v3/createRoom", body, &out)
	return out.RoomID, err
}

// Element Call's state events: the call of the room and the people in it (MatrixRTC). Element's «Включить звонки»
// lets the room's members send them.
const (
	CallType       = "org.matrix.msc3401.call"
	CallMemberType = "org.matrix.msc3401.call.member"
)

// AllowCalls lets whoever may write in the room take part in its calls, from Element as well: the levels of the call
// events are those of messages. It tells whether it changed them.
func AllowCalls(pl PowerLevels) bool {
	events, _ := pl["events"].(map[string]any)
	if events == nil {
		events = map[string]any{}
		pl["events"] = events
	}
	level, _ := num(pl["events_default"])
	changed := false
	for _, t := range []string{CallType, CallMemberType} {
		if v, ok := num(events[t]); !ok || v != level {
			events[t], changed = level, true
		}
	}
	return changed
}

// num is a level as JSON gives it (float64) or as go24 puts it (int).
func num(v any) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

func (c *Client) SetState(ctx context.Context, roomID, eventType, stateKey string, content any) error {
	return c.Do(ctx, http.MethodPut, "/_matrix/client/v3/rooms/"+esc(roomID)+"/state/"+esc(eventType)+"/"+esc(stateKey), content, nil)
}

// State reads a state event of the room into out.
func (c *Client) State(ctx context.Context, roomID, eventType, stateKey string, out any) error {
	return c.Do(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+esc(roomID)+"/state/"+esc(eventType)+"/"+esc(stateKey), nil, out)
}

// SetStateAs sends a state event as the user of the token (their own m.room.member, say).
func (c *Client) SetStateAs(ctx context.Context, token, roomID, eventType, stateKey string, content any) error {
	return c.Do(ctx, http.MethodPut, "/_matrix/client/v3/rooms/"+esc(roomID)+"/state/"+esc(eventType)+"/"+esc(stateKey), content, nil, token)
}

// AddToSpace links a room into the company space (both directions, as the spec asks).
func (c *Client) AddToSpace(ctx context.Context, spaceID, roomID string) error {
	via := map[string]any{"via": []string{c.ServerName}}
	if err := c.SetState(ctx, spaceID, "m.space.child", roomID, via); err != nil {
		return err
	}
	return c.SetState(ctx, roomID, "m.space.parent", spaceID, map[string]any{"via": []string{c.ServerName}, "canonical": true})
}

// Members returns joined and invited users of a room.
func (c *Client) Members(ctx context.Context, roomID string, token ...string) (map[string]string, error) {
	var out struct {
		Chunk []struct {
			StateKey string `json:"state_key"`
			Content  struct {
				Membership string `json:"membership"`
			} `json:"content"`
		} `json:"chunk"`
	}
	if err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+esc(roomID)+"/members", nil, &out, token...); err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, e := range out.Chunk {
		if e.Content.Membership == "join" || e.Content.Membership == "invite" {
			m[e.StateKey] = e.Content.Membership
		}
	}
	return m, nil
}

func (c *Client) Kick(ctx context.Context, roomID, mxid, reason string) error {
	return c.Do(ctx, http.MethodPost, "/_matrix/client/v3/rooms/"+esc(roomID)+"/kick", map[string]string{"user_id": mxid, "reason": reason}, nil)
}

// DeleteRoom deletes a room with its history (Synapse's admin API): everyone in it is put out, the room is purged.
func (c *Client) DeleteRoom(ctx context.Context, roomID string) error {
	return c.Do(ctx, http.MethodDelete, "/_synapse/admin/v1/rooms/"+esc(roomID), map[string]any{"purge": true}, nil)
}

var txn atomic.Int64

// Send posts a message (as the bot, or as an employee with their token) and returns its event ID.
func (c *Client) Send(ctx context.Context, roomID string, content map[string]any, token string) (string, error) {
	return c.SendEvent(ctx, roomID, "m.room.message", content, token)
}

func txnID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + strconv.FormatInt(txn.Add(1), 36)
}

// SendEvent posts an event of any type (m.reaction…).
func (c *Client) SendEvent(ctx context.Context, roomID, eventType string, content map[string]any, token string) (string, error) {
	var out struct {
		EventID string `json:"event_id"`
	}
	err := c.Do(ctx, http.MethodPut, "/_matrix/client/v3/rooms/"+esc(roomID)+"/send/"+esc(eventType)+"/"+txnID(), content, &out, token)
	return out.EventID, err
}

// Redact removes an event (the bot administers the rooms it made, so it may redact anything there).
func (c *Client) Redact(ctx context.Context, roomID, eventID, reason string) error {
	return c.Do(ctx, http.MethodPut, "/_matrix/client/v3/rooms/"+esc(roomID)+"/redact/"+esc(eventID)+"/"+txnID(),
		map[string]string{"reason": reason}, nil)
}

// RedactAs deletes an event as the token's owner (their own, or another's when their power level lets them).
func (c *Client) RedactAs(ctx context.Context, token, roomID, eventID string) error {
	return c.Do(ctx, http.MethodPut, "/_matrix/client/v3/rooms/"+esc(roomID)+"/redact/"+esc(eventID)+"/"+txnID(),
		map[string]string{}, nil, token)
}

// ---------- sync (Matrix → go24) ----------

type Event struct {
	EventID  string         `json:"event_id"`
	Type     string         `json:"type"`
	Sender   string         `json:"sender"`
	Content  map[string]any `json:"content"`
	TS       int64          `json:"origin_server_ts"`
	Redacts  string         `json:"redacts"` // m.room.redaction in room versions before 11 (newer: content.redacts)
	StateKey *string        `json:"state_key"`
	RoomID   string         `json:"room_id,omitempty"` // given by /search
	Unsigned struct {
		PrevContent map[string]any `json:"prev_content"` // a state event's content before it
		Relations   struct {
			Thread *struct {
				Count  int    `json:"count"`
				Latest *Event `json:"latest_event"` // the newest reply
			} `json:"m.thread"`
			Replace *Event `json:"m.replace"` // the latest edit, bundled by the server
		} `json:"m.relations"`
	} `json:"unsigned"`
}

type SyncResult struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[string]struct {
			Timeline struct {
				Events []Event `json:"events"`
			} `json:"timeline"`
		} `json:"join"`
	} `json:"rooms"`
}

// Sync long-polls new events of the bot's rooms. An empty since starts from now (no history).
func (c *Client) Sync(ctx context.Context, since string, timeout time.Duration) (*SyncResult, error) {
	q := url.Values{}
	q.Set("timeout", strconv.Itoa(int(timeout.Milliseconds())))
	q.Set("filter", `{"room":{"timeline":{"types":["m.room.message","m.reaction","m.room.redaction"],"limit":100},"state":{"lazy_load_members":true},"ephemeral":{"types":[]}},"presence":{"types":[]},"account_data":{"types":[]}}`)
	if since != "" {
		q.Set("since", since)
	}
	var out SyncResult
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/sync?"+q.Encode(), nil, &out)
	return &out, err
}

// Permalink opens a room (and an event in it) in Element.
func Permalink(elementURL, roomID, eventID string) string {
	u := fmt.Sprintf("%s/#/room/%s", elementURL, roomID)
	if eventID != "" {
		u += "/" + eventID
	}
	return u
}
