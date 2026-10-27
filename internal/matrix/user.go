package matrix

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Calls made with an employee's own token (go24's messenger shows exactly what that person may see).

// UserSync is /sync as a user: rooms with their name, members summary, unread counts and the last messages.
type UserSync struct {
	NextBatch string `json:"next_batch"`
	Rooms     struct {
		Join map[string]struct {
			Summary struct {
				Heroes  []string `json:"m.heroes"`
				Joined  int      `json:"m.joined_member_count"`
				Invited int      `json:"m.invited_member_count"`
			} `json:"summary"`
			State struct {
				Events []Event `json:"events"`
			} `json:"state"`
			Timeline struct {
				Events []Event `json:"events"`
			} `json:"timeline"`
			Unread    UnreadCounts            `json:"unread_notifications"`        // the main timeline's
			Threads   map[string]UnreadCounts `json:"unread_thread_notifications"` // each thread's, by its root
			Ephemeral struct {
				Events []Event `json:"events"`
			} `json:"ephemeral"` // receipts and typing, when asked for
			AccountData struct {
				Events []Event `json:"events"`
			} `json:"account_data"` // the person's own marks of the room: m.tag, m.marked_unread
		} `json:"join"`
	} `json:"rooms"`
	AccountData struct {
		Events []Event `json:"events"`
	} `json:"account_data"`
}

// UnreadCounts are the unread messages (Matrix: notifications) and those of them that mention the person
// (highlights).
type UnreadCounts struct {
	Notifications int `json:"notification_count"`
	Highlights    int `json:"highlight_count"`
}

// SyncAs syncs as the token's owner; threads are counted on their own (unread_thread_notifications), so the main
// timeline's counts leave their replies out. limit = timeline events per room; ephemeral — the ephemeral events wanted
// (m.receipt, m.typing), none by default.
func (c *Client) SyncAs(ctx context.Context, token, since string, timeout time.Duration, limit int, ephemeral ...string) (*UserSync, error) {
	eph, _ := json.Marshal(append([]string{}, ephemeral...))
	q := url.Values{}
	q.Set("timeout", strconv.Itoa(int(timeout.Milliseconds())))
	if since == "" {
		q.Set("timeout", freshSync())
	}
	q.Set("filter", `{"room":{"timeline":{"limit":`+strconv.Itoa(limit)+`,"unread_thread_notifications":true,"types":["m.room.message","m.room.encrypted","m.reaction","m.room.redaction","org.matrix.msc3381.poll.start","org.matrix.msc3381.poll.response","org.matrix.msc3381.poll.end","m.poll.start","m.poll.response","m.poll.end",`+
		`"m.room.member","m.room.name","m.room.avatar","m.room.power_levels","go24.autodelete"]},`+
		`"state":{"lazy_load_members":true,"types":["m.room.name","m.room.create","m.room.member","m.room.encryption","m.room.avatar","m.room.power_levels"]},"ephemeral":{"types":`+string(eph)+`},`+
		`"account_data":{"types":["m.tag","m.marked_unread","go24.hidden","go24.draft"]}},`+
		`"presence":{"types":[]},"account_data":{"types":["m.direct"]}}`)
	if since != "" {
		q.Set("since", since)
	}
	var out UserSync
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/sync?"+q.Encode(), nil, &out, token)
	return &out, err
}

// Messages pages back through a room's history (from "" = newest): messages and the changes of the chat itself.
func (c *Client) Messages(ctx context.Context, token, roomID, from string, limit int) ([]Event, string, error) {
	return c.MessagesOf(ctx, token, roomID, from, limit, messageTypes)
}

// messageTypes are the events a chat's timeline shows (a JSON list).
const messageTypes = `["m.room.message","m.room.encrypted","m.room.member","m.room.name","m.room.avatar","m.room.power_levels","go24.autodelete","org.matrix.msc3381.poll.start","m.poll.start"]`

// MessagesOf pages a room's timeline backwards, only events of the given types (a JSON list).
func (c *Client) MessagesOf(ctx context.Context, token, roomID, from string, limit int, types string) ([]Event, string, error) {
	return c.MessagesFiltered(ctx, token, roomID, from, limit, `{"types":`+types+`}`)
}

// MessagesFiltered pages a room's timeline backwards through a room event filter (JSON), e.g. only the events with
// a file ("contains_url").
func (c *Client) MessagesFiltered(ctx context.Context, token, roomID, from string, limit int, filter string) ([]Event, string, error) {
	q := url.Values{}
	q.Set("dir", "b")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("filter", filter)
	if from != "" {
		q.Set("from", from)
	}
	var out struct {
		Chunk []Event `json:"chunk"`
		End   string  `json:"end"`
	}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+esc(roomID)+"/messages?"+q.Encode(), nil, &out, token)
	return out.Chunk, out.End, err
}

// Thread returns the replies of a thread, oldest first.
func (c *Client) Thread(ctx context.Context, token, roomID, rootID string) ([]Event, error) {
	var out struct {
		Chunk []Event `json:"chunk"`
	}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v1/rooms/"+esc(roomID)+"/relations/"+esc(rootID)+"/m.thread?limit=200&dir=f", nil, &out, token)
	return out.Chunk, err
}

// Threads lists a room's threads, the latest active first: their roots, each with its count of replies and the
// latest reply (unsigned m.relations.m.thread).
func (c *Client) Threads(ctx context.Context, token, roomID string, limit int) ([]Event, error) {
	var out struct {
		Chunk []Event `json:"chunk"`
	}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v1/rooms/"+esc(roomID)+"/threads?limit="+strconv.Itoa(limit), nil, &out, token)
	return out.Chunk, err
}

// LastInThread is the newest reply of a thread ("" — none yet).
func (c *Client) LastInThread(ctx context.Context, token, roomID, rootID string) (string, error) {
	var out struct {
		Chunk []Event `json:"chunk"`
	}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v1/rooms/"+esc(roomID)+"/relations/"+esc(rootID)+"/m.thread?limit=1&dir=b", nil, &out, token)
	if err != nil || len(out.Chunk) == 0 {
		return "", err
	}
	return out.Chunk[0].EventID, nil
}

// Reactions are the m.reaction events on an event.
func (c *Client) Reactions(ctx context.Context, token, roomID, eventID string) ([]Event, error) {
	var out struct {
		Chunk []Event `json:"chunk"`
	}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v1/rooms/"+esc(roomID)+"/relations/"+esc(eventID)+"/m.annotation/m.reaction?limit=200", nil, &out, token)
	return out.Chunk, err
}

// Relations are the events of one kind of relation to an event (m.reference: the votes of a poll), oldest first.
func (c *Client) Relations(ctx context.Context, token, roomID, eventID, relType string) ([]Event, error) {
	var out struct {
		Chunk []Event `json:"chunk"`
	}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v1/rooms/"+esc(roomID)+"/relations/"+esc(eventID)+"/"+esc(relType)+"?limit=500&dir=f", nil, &out, token)
	return out.Chunk, err
}

// Event loads one event (e.g. a thread root).
func (c *Client) EventAs(ctx context.Context, token, roomID, eventID string) (Event, error) {
	var ev Event
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+esc(roomID)+"/event/"+esc(eventID), nil, &ev, token)
	return ev, err
}

// ReadReceipt marks the room read up to the event.
func (c *Client) ReadReceipt(ctx context.Context, token, roomID, eventID string) error {
	return c.Do(ctx, http.MethodPost, "/_matrix/client/v3/rooms/"+esc(roomID)+"/read_markers",
		map[string]string{"m.fully_read": eventID, "m.read": eventID}, nil, token)
}

// CreateDM starts a private chat with another user, unencrypted (so go24 can show it), and records it as a
// direct chat for both sides' clients.
func (c *Client) CreateDM(ctx context.Context, token, otherMXID string) (string, error) {
	var out struct {
		RoomID string `json:"room_id"`
	}
	err := c.Do(ctx, http.MethodPost, "/_matrix/client/v3/createRoom", map[string]any{
		"preset": "trusted_private_chat", "is_direct": true, "invite": []string{otherMXID}, "visibility": "private",
	}, &out, token)
	return out.RoomID, err
}

// CreateNotes starts the person's chat with themselves («Заметки»): a private chat with nobody invited.
func (c *Client) CreateNotes(ctx context.Context, token, name string) (string, error) {
	var out struct {
		RoomID string `json:"room_id"`
	}
	err := c.Do(ctx, http.MethodPost, "/_matrix/client/v3/createRoom", map[string]any{
		"preset": "trusted_private_chat", "is_direct": true, "visibility": "private", "name": name,
	}, &out, token)
	return out.RoomID, err
}

// JoinAs accepts an invitation (or joins) as the token's owner.
func (c *Client) JoinAs(ctx context.Context, token, roomID string) error {
	return c.Do(ctx, http.MethodPost, "/_matrix/client/v3/join/"+esc(roomID), map[string]any{}, nil, token)
}

// DirectRooms reads the user's m.direct account data: other user → rooms.
func (c *Client) DirectRooms(ctx context.Context, token, mxid string) (map[string][]string, error) {
	out := map[string][]string{}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/user/"+esc(mxid)+"/account_data/m.direct", nil, &out, token)
	if IsCode(err, "M_NOT_FOUND") {
		return out, nil
	}
	return out, err
}

func (c *Client) SetDirectRooms(ctx context.Context, token, mxid string, m map[string][]string) error {
	return c.Do(ctx, http.MethodPut, "/_matrix/client/v3/user/"+esc(mxid)+"/account_data/m.direct", m, nil, token)
}

// CreateGroup starts a group chat (unencrypted, so go24 can show it) with the people invited; its owner alone has
// the rights of an administrator of the chat (rename, picture, removing people), anyone in it may invite.
func (c *Client) CreateGroup(ctx context.Context, token, owner, name string, invite []string, levels PowerLevels) (string, error) {
	var out struct {
		RoomID string `json:"room_id"`
	}
	pl := PowerLevels{"users": map[string]any{owner: 100}, "invite": 0, "kick": 50, "state_default": 50, "events_default": 0,
		"events": map[string]any{"m.room.pinned_events": 0, "m.room.power_levels": 100}} // anyone in the chat pins
	for k, v := range levels {
		pl[k] = v
	}
	if invite == nil { // an open chat may start with no one else; Synapse fails on a null list
		invite = []string{}
	}
	err := c.Do(ctx, http.MethodPost, "/_matrix/client/v3/createRoom", map[string]any{
		"preset": "private_chat", "name": name, "invite": invite, "visibility": "private", "power_level_content_override": pl,
	}, &out, token)
	return out.RoomID, err
}

// SetJoinRuleAs makes the room open to the members of a space ("restricted": they join without an invitation, as in
// Element's «Space members can find and join») or invite-only again (space "").
func (c *Client) SetJoinRuleAs(ctx context.Context, token, roomID, space string) error {
	content := map[string]any{"join_rule": "invite"}
	if space != "" {
		content = map[string]any{"join_rule": "restricted", "allow": []map[string]string{{"type": "m.room_membership", "room_id": space}}}
	}
	return c.SetStateAs(ctx, token, roomID, "m.room.join_rules", "", content)
}

// AdminState reads a room's whole state through the admin API — for those not in it.
func (c *Client) AdminState(ctx context.Context, roomID string) ([]Event, error) {
	var out struct {
		State []Event `json:"state"`
	}
	err := c.Do(ctx, http.MethodGet, "/_synapse/admin/v1/rooms/"+esc(roomID)+"/state", nil, &out)
	return out.State, err
}

// JoinedRooms are the rooms the token's owner is in.
func (c *Client) JoinedRooms(ctx context.Context, token string) (map[string]bool, error) {
	var out struct {
		Rooms []string `json:"joined_rooms"`
	}
	if err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/joined_rooms", nil, &out, token); err != nil {
		return nil, err
	}
	m := map[string]bool{}
	for _, r := range out.Rooms {
		m[r] = true
	}
	return m, nil
}

// AdminMessages pages a room's timeline backwards through the admin API (the bot need not be in the room), only the
// event types of Messages.
func (c *Client) AdminMessages(ctx context.Context, roomID, from string, limit int) ([]Event, string, error) {
	q := url.Values{}
	q.Set("dir", "b")
	q.Set("limit", strconv.Itoa(limit))
	q.Set("filter", `{"types":`+messageTypes+`}`)
	if from != "" {
		q.Set("from", from)
	}
	var out struct {
		Chunk []Event `json:"chunk"`
		End   string  `json:"end"`
	}
	err := c.Do(ctx, http.MethodGet, "/_synapse/admin/v1/rooms/"+esc(roomID)+"/messages?"+q.Encode(), nil, &out)
	return out.Chunk, out.End, err
}

// InviteAs invites someone into the room as the token's owner.
func (c *Client) InviteAs(ctx context.Context, token, roomID, mxid string) error {
	return c.Do(ctx, http.MethodPost, "/_matrix/client/v3/rooms/"+esc(roomID)+"/invite", map[string]string{"user_id": mxid}, nil, token)
}

// KickAs removes someone from the room as the token's owner (Matrix checks their rights).
func (c *Client) KickAs(ctx context.Context, token, roomID, mxid string) error {
	return c.Do(ctx, http.MethodPost, "/_matrix/client/v3/rooms/"+esc(roomID)+"/kick", map[string]string{"user_id": mxid}, nil, token)
}

// LeaveAs leaves the room as the token's owner.
func (c *Client) LeaveAs(ctx context.Context, token, roomID string) error {
	return c.Do(ctx, http.MethodPost, "/_matrix/client/v3/rooms/"+esc(roomID)+"/leave", map[string]any{}, nil, token)
}

// StateAs reads a state event of the room as the token's owner.
func (c *Client) StateAs(ctx context.Context, token, roomID, eventType, stateKey string, out any) error {
	return c.Do(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+esc(roomID)+"/state/"+esc(eventType)+"/"+esc(stateKey), nil, out, token)
}

// PowerLevels is the part of m.room.power_levels go24 reads and changes: people's levels, the rest kept as it was.
type PowerLevels map[string]any

// Level is the person's level in the room (users_default for those not named).
func (p PowerLevels) Level(mxid string) int {
	if users, ok := p["users"].(map[string]any); ok {
		if v, ok := users[mxid].(float64); ok {
			return int(v)
		}
	}
	v, _ := p["users_default"].(float64)
	return int(v)
}

// SetLevel names the person's level.
func (p PowerLevels) SetLevel(mxid string, level int) {
	users, ok := p["users"].(map[string]any)
	if !ok {
		users = map[string]any{}
		p["users"] = users
	}
	users[mxid] = float64(level)
}

// Search finds messages by their words, newest first, in the rooms given (none — every room of the token's owner);
// next continues a search (the token it gave back, "" — no more).
func (c *Client) Search(ctx context.Context, token, term string, rooms []string, next string) ([]Event, string, error) {
	filter := map[string]any{"types": []string{"m.room.message"}}
	if len(rooms) > 0 {
		filter["rooms"] = rooms
	}
	path := "/_matrix/client/v3/search"
	if next != "" {
		path += "?next_batch=" + url.QueryEscape(next)
	}
	var out struct {
		Categories struct {
			RoomEvents struct {
				Results []struct {
					Result Event `json:"result"`
				} `json:"results"`
				NextBatch string `json:"next_batch"`
			} `json:"room_events"`
		} `json:"search_categories"`
	}
	err := c.Do(ctx, http.MethodPost, path, map[string]any{"search_categories": map[string]any{"room_events": map[string]any{
		"search_term": term, "order_by": "recent", "keys": []string{"content.body"}, "filter": filter}}}, &out, token)
	var evs []Event
	for _, r := range out.Categories.RoomEvents.Results {
		evs = append(evs, r.Result)
	}
	return evs, out.Categories.RoomEvents.NextBatch, err
}

// Around is a message with the messages before it (newest first, as /messages gives them) and after it (oldest
// first); start pages further back.
type Around struct {
	Before []Event `json:"events_before"`
	Event  Event   `json:"event"`
	After  []Event `json:"events_after"`
	Start  string  `json:"start"`
}

// Context gives a message and those around it, only messages and the changes of the chat itself.
func (c *Client) Context(ctx context.Context, token, roomID, eventID string, limit int) (*Around, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("filter", `{"types":["m.room.message","m.room.encrypted","m.room.member","m.room.name","m.room.avatar","m.room.power_levels","go24.autodelete","org.matrix.msc3381.poll.start","m.poll.start"]}`)
	var out Around
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/rooms/"+esc(roomID)+"/context/"+esc(eventID)+"?"+q.Encode(), nil, &out, token)
	return &out, err
}

// Receipt: someone has read the room up to the event, at ts (ms).
type Receipt struct {
	User, Event string
	TS          int64
}

// ThreadReceipt marks one thread of the room read up to the event (thread "main" — the main timeline, without the
// threads); the event must belong to that thread.
func (c *Client) ThreadReceipt(ctx context.Context, token, roomID, eventID, thread string) error {
	return c.Do(ctx, http.MethodPost, "/_matrix/client/v3/rooms/"+esc(roomID)+"/receipt/m.read/"+esc(eventID),
		map[string]string{"thread_id": thread}, nil, token)
}

// ThreadCounts gives the unread counts of a room's threads, by their roots (threads with none are left out).
func (c *Client) ThreadCounts(ctx context.Context, token, roomID string) (map[string]UnreadCounts, error) {
	room, _ := json.Marshal([]string{roomID})
	q := url.Values{}
	q.Set("timeout", freshSync())
	// a timeline of one event: with none Synapse leaves the room out of an initial sync
	q.Set("filter", `{"room":{"rooms":`+string(room)+`,"timeline":{"limit":1,"unread_thread_notifications":true},"state":{"types":[]},`+
		`"ephemeral":{"types":[]},"account_data":{"types":[]}},"presence":{"types":[]},"account_data":{"types":[]}}`)
	var out UserSync
	if err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/sync?"+q.Encode(), nil, &out, token); err != nil {
		return nil, err
	}
	return out.Rooms.Join[roomID].Threads, nil
}

// ReceiptsOf reads an m.receipt event: the read receipts it carries (threads' own receipts left out).
func ReceiptsOf(ev Event) []Receipt {
	if ev.Type != "m.receipt" {
		return nil
	}
	var out []Receipt
	for id, kinds := range ev.Content {
		read, _ := kinds.(map[string]any)["m.read"].(map[string]any)
		for user, v := range read {
			r, _ := v.(map[string]any)
			if t, _ := r["thread_id"].(string); t != "" && t != "main" {
				continue
			}
			ts, _ := r["ts"].(float64)
			out = append(out, Receipt{User: user, Event: id, TS: int64(ts)})
		}
	}
	return out
}

// Receipts gives the latest read receipt of each person in the room (a sync of that room alone).
func (c *Client) Receipts(ctx context.Context, token, roomID string) ([]Receipt, error) {
	room, _ := json.Marshal([]string{roomID})
	q := url.Values{}
	q.Set("timeout", freshSync())
	q.Set("filter", `{"room":{"rooms":`+string(room)+`,"timeline":{"limit":0},"state":{"types":[]},"ephemeral":{"types":["m.receipt"]},`+
		`"account_data":{"types":[]}},"presence":{"types":[]},"account_data":{"types":[]}}`)
	var out UserSync
	if err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/sync?"+q.Encode(), nil, &out, token); err != nil {
		return nil, err
	}
	var rs []Receipt
	for _, ev := range out.Rooms.Join[roomID].Ephemeral.Events {
		rs = append(rs, ReceiptsOf(ev)...)
	}
	return rs, nil
}

// Typing tells the room the token's owner is typing (for a few seconds) or has stopped.
func (c *Client) Typing(ctx context.Context, token, roomID, mxid string, on bool) error {
	body := map[string]any{"typing": on}
	if on {
		body["timeout"] = 6000
	}
	return c.Do(ctx, http.MethodPut, "/_matrix/client/v3/rooms/"+esc(roomID)+"/typing/"+esc(mxid), body, nil, token)
}

// SetTag puts a tag on the room for the token's owner (m.favourite — the chat pinned at the top) or takes it off.
func (c *Client) SetTag(ctx context.Context, token, mxid, roomID, tag string, on bool) error {
	path := "/_matrix/client/v3/user/" + esc(mxid) + "/rooms/" + esc(roomID) + "/tags/" + esc(tag)
	if !on {
		return c.Do(ctx, http.MethodDelete, path, nil, nil, token)
	}
	return c.Do(ctx, http.MethodPut, path, map[string]any{"order": 0.5}, nil, token)
}

// RoomData reads the token's owner's account data of the room into out (M_NOT_FOUND — none).
func (c *Client) RoomData(ctx context.Context, token, mxid, roomID, typ string, out any) error {
	return c.Do(ctx, http.MethodGet, "/_matrix/client/v3/user/"+esc(mxid)+"/rooms/"+esc(roomID)+"/account_data/"+esc(typ), nil, out, token)
}

// SetRoomData writes the token's owner's account data of the room (m.marked_unread, say).
func (c *Client) SetRoomData(ctx context.Context, token, mxid, roomID, typ string, content any) error {
	return c.Do(ctx, http.MethodPut, "/_matrix/client/v3/user/"+esc(mxid)+"/rooms/"+esc(roomID)+"/account_data/"+esc(typ), content, nil, token)
}

// MutedRooms are the rooms the token's owner has muted: an enabled override push rule named by the room that does
// not notify (as Element mutes a chat).
func (c *Client) MutedRooms(ctx context.Context, token string) (map[string]bool, error) {
	var out struct {
		Global struct {
			Override []struct {
				RuleID  string `json:"rule_id"`
				Enabled bool   `json:"enabled"`
				Actions []any  `json:"actions"`
			} `json:"override"`
		} `json:"global"`
	}
	if err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/pushrules/", nil, &out, token); err != nil {
		return nil, err
	}
	muted := map[string]bool{}
	for _, r := range out.Global.Override {
		if !strings.HasPrefix(r.RuleID, "!") || !r.Enabled {
			continue
		}
		notify := false
		for _, a := range r.Actions {
			notify = notify || a == "notify"
		}
		muted[r.RuleID] = !notify
	}
	return muted, nil
}

// Mute stops the room's notifications for the token's owner (on) or brings them back.
func (c *Client) Mute(ctx context.Context, token, roomID string, on bool) error {
	path := "/_matrix/client/v3/pushrules/global/override/" + esc(roomID)
	if !on {
		err := c.Do(ctx, http.MethodDelete, path, nil, nil, token)
		if IsCode(err, "M_NOT_FOUND") {
			return nil
		}
		return err
	}
	return c.Do(ctx, http.MethodPut, path, map[string]any{
		"conditions": []map[string]string{{"kind": "event_match", "key": "room_id", "pattern": roomID}}, "actions": []any{},
	}, nil, token)
}

var syncs atomic.Int64

// freshSync is the timeout of an initial sync: Synapse answers such a sync at once whatever its timeout, but keeps
// the answer to the same request for a while (sync_response_cache_duration, 2 minutes), so a chat pinned or read a
// moment ago would come back as it was; a timeout never asked before makes the request new.
func freshSync() string {
	return strconv.FormatInt(syncs.Add(1)%1000000+time.Now().UnixMilli()%1000*1000000, 10)
}
