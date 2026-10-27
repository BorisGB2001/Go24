package matrixsync

import (
	"fmt"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/matrix"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// fakeSynapse implements the parts of the client-server and admin APIs go24 uses, in memory.
type fakeSynapse struct {
	mu       sync.Mutex
	users    map[string]*matrix.AdminUser // mxid → account
	extID    map[string]string            // mxid → external id
	rooms    map[string]map[string]bool   // room → joined mxids
	spaces   map[string]bool
	levels   map[string]map[string]any // room → its power levels besides the people's (events_default, events, invite, kick, state_default)
	names    map[string]string
	sent     []sentEvent
	tokens   map[string]string // token → mxid
	pending  []matrix.Event    // delivered by the next /sync
	redacted []string
	acct     map[string]map[string]any  // mxid → m.direct
	invited  map[string]map[string]bool // room → mxids invited when it was made
	media    map[string][]byte          // mxc → the uploaded file
	nroom    int
	read     map[string]string // "mxid room" → the event of the read marker
	readAll  map[string]string // "mxid room" → the event of the last unthreaded receipt (it reads the threads too)
	readTh   map[string]string // "mxid room root" → the event of the person's receipt in that thread
	state    []stateEvent      // state events sent (path "room/state/type/key")
	power    map[string]map[string]float64 // room → people's power levels (rooms made with them: group chats)
	avatar   map[string]string             // room → its picture (mxc)
	pins     map[string][]string           // room → m.room.pinned_events
	botRooms map[string]bool               // rooms the bot made: only the bot changes their state (state_default 100)
	typing   map[string]bool               // "room mxid" → typing now
	roomData map[string]map[string]any     // "mxid room" → the person's account data of the room (type → content; tags under m.tag)
	rules    map[string]map[string]bool    // mxid → rooms muted by an override push rule
}

// timelineEvent is a sent event as the timeline gives it: a message, or (when it has "_event") a change of the
// chat itself — a membership (state key "_key"), a name, a picture.
func timelineEvent(e sentEvent, ts int) map[string]any {
	ev := map[string]any{"event_id": e.ID, "type": "m.room.message", "sender": e.Sender, "content": e.Content, "origin_server_ts": ts}
	if t, ok := e.Content["_event"].(string); ok {
		ev["type"] = t
		ev["state_key"] = e.Content["_key"]
		if prev, ok := e.Content["_prev"]; ok {
			ev["unsigned"] = map[string]any{"prev_content": prev}
		}
	}
	return ev
}

// threadOf is the thread root of a sent event ("" — the main timeline).
func threadOf(e sentEvent) string {
	if rel, _ := e.Content["m.relates_to"].(map[string]any); rel["rel_type"] == "m.thread" {
		root, _ := rel["event_id"].(string)
		return root
	}
	return ""
}

// withThread adds to a thread root, as Synapse bundles it, the count of its replies and the newest one.
func (f *fakeSynapse) withThread(ev map[string]any) map[string]any {
	n, latest := 0, map[string]any(nil)
	for i, e := range f.sent {
		if threadOf(e) == ev["event_id"] {
			n, latest = n+1, timelineEvent(e, i+10)
		}
	}
	if n > 0 {
		ev["unsigned"] = map[string]any{"m.relations": map[string]any{"m.thread": map[string]any{"count": n, "latest_event": latest}}}
	}
	return ev
}

// threadUnread counts, by thread root, the replies of others in the room the person has not read: after their receipt
// in the thread and after their last unthreaded one (as Synapse's unread_thread_notifications).
func (f *fakeSynapse) threadUnread(who, room string) map[string]map[string]int {
	out := map[string]map[string]int{}
	for i, e := range f.sent {
		root := threadOf(e)
		if e.Room != room || root == "" || e.Sender == who || i <= indexOf(f.sent, f.readAll[who+" "+room]) || i <= indexOf(f.sent, f.readTh[who+" "+room+" "+root]) {
			continue
		}
		if out[root] == nil {
			out[root] = map[string]int{"notification_count": 0, "highlight_count": 0}
		}
		out[root]["notification_count"]++
	}
	return out
}

// chatEvent records a change of the chat (by a person, not by the bot) into its timeline.
func (f *fakeSynapse) chatEvent(room, sender, typ, key string, content map[string]any) {
	c := maps.Clone(content)
	c["_event"], c["_key"] = typ, key
	f.sent = append(f.sent, sentEvent{Room: room, Sender: sender, Content: c, ID: "$s" + strconv.Itoa(len(f.sent))})
}

// lastState is the content of the room's state event of the type last put (nil — none).
func (f *fakeSynapse) lastState(room, typ string) map[string]any {
	for i := len(f.state) - 1; i >= 0; i-- {
		if strings.HasPrefix(f.state[i].Path, room+"/state/"+typ+"/") {
			return f.state[i].Content
		}
	}
	return nil
}

// restricted: the room's join rule lets the person in — "restricted" to a room (a space) they are in.
func (f *fakeSynapse) restricted(room, who string) bool {
	jr := f.lastState(room, "m.room.join_rules")
	if jr == nil || jr["join_rule"] != "restricted" {
		return false
	}
	allow, _ := jr["allow"].([]any)
	for _, a := range allow {
		if in, _ := a.(map[string]any)["room_id"].(string); f.rooms[in][who] {
			return true
		}
	}
	return false
}

// allowed: someone has the power level needed in the room (rooms made without levels let anyone in them do anything).
// level is a level of the room's power levels (def — none set).
func (f *fakeSynapse) level(room, key string, def float64) float64 {
	if v, ok := f.levels[room][key].(float64); ok {
		return v
	}
	return def
}

// roomLevels are the power levels the fake keeps, besides the people's.
func roomLevels(pl map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"events_default", "events", "invite", "kick", "state_default"} {
		if v, ok := pl[k]; ok {
			out[k] = v
		}
	}
	return out
}

func (f *fakeSynapse) allowed(room, who string, need float64) bool {
	p, ok := f.power[room]
	return !ok || p[who] >= need
}

// stateEvent: a state event put into a room.
type stateEvent struct {
	Path, Sender string
	Content      map[string]any
}

type sentEvent struct {
	Room, Sender string
	Content      map[string]any
	ID           string
}

func newFake() *fakeSynapse {
	return &fakeSynapse{power: map[string]map[string]float64{}, avatar: map[string]string{}, pins: map[string][]string{}, botRooms: map[string]bool{}, typing: map[string]bool{}, roomData: map[string]map[string]any{}, rules: map[string]map[string]bool{}, media: map[string][]byte{}, invited: map[string]map[string]bool{}, read: map[string]string{}, readAll: map[string]string{}, readTh: map[string]string{}, acct: map[string]map[string]any{}, users: map[string]*matrix.AdminUser{}, extID: map[string]string{}, rooms: map[string]map[string]bool{},
		spaces: map[string]bool{}, levels: map[string]map[string]any{}, names: map[string]string{}, tokens: map[string]string{"bot": "@go24:t.lan"}}
}

func (f *fakeSynapse) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	who := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if who == "" {
		w.WriteHeader(401)
		_ = json.NewEncoder(w).Encode(map[string]string{"errcode": "M_UNKNOWN_TOKEN"})
		return
	}
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	path, _ := url.PathUnescape(r.URL.EscapedPath())
	out := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch {
	case strings.HasPrefix(path, "/_synapse/admin/v2/users/"):
		mx := strings.TrimPrefix(path, "/_synapse/admin/v2/users/")
		if r.Method == http.MethodGet {
			u, ok := f.users[mx]
			if !ok {
				w.WriteHeader(404)
				out(map[string]string{"errcode": "M_NOT_FOUND"})
				return
			}
			out(u)
			return
		}
		u := f.users[mx]
		if u == nil {
			u = &matrix.AdminUser{Name: mx}
			f.users[mx] = u
		}
		u.DisplayName, _ = body["displayname"].(string)
		u.Deactivated, _ = body["deactivated"].(bool)
		if ids, ok := body["external_ids"].([]any); ok && len(ids) > 0 {
			f.extID[mx], _ = ids[0].(map[string]any)["external_id"].(string)
		}
		out(map[string]any{})
	case strings.HasPrefix(path, "/_synapse/admin/v1/deactivate/"):
		mx := strings.TrimPrefix(path, "/_synapse/admin/v1/deactivate/")
		f.users[mx].Deactivated = true
		for _, m := range f.rooms {
			delete(m, mx)
		}
		out(map[string]any{})
	case strings.HasPrefix(path, "/_synapse/admin/v1/users/") && strings.HasSuffix(path, "/login"):
		mx := strings.TrimSuffix(strings.TrimPrefix(path, "/_synapse/admin/v1/users/"), "/login")
		tok := "tok-" + mx
		f.tokens[tok] = mx
		out(map[string]string{"access_token": tok})
	case strings.HasPrefix(path, "/_synapse/admin/v1/join/"):
		room := strings.TrimPrefix(path, "/_synapse/admin/v1/join/")
		mx, _ := body["user_id"].(string)
		if u := f.users[mx]; u == nil || u.Deactivated {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		if !f.rooms[room]["@go24:t.lan"] { // as Synapse: the admin invites the user first, so must be in the room
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN", "error": "@go24:t.lan not in room " + room})
			return
		}
		f.rooms[room][mx] = true
		out(map[string]any{})
	case strings.HasPrefix(path, "/_matrix/client/v3/join/"):
		room := strings.TrimPrefix(path, "/_matrix/client/v3/join/")
		open := !f.invited[room][who] && !f.rooms[room][who] && f.restricted(room, who)
		if !f.invited[room][who] && !f.rooms[room][who] && !open {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		if open { // as Synapse: a join by the room's join rule names who authorised it
			f.chatEvent(room, who, "m.room.member", who, map[string]any{"membership": "join", "join_authorised_via_users_server": "@x:t.lan"})
		}
		f.rooms[room][who] = true
		out(map[string]any{})
	case path == "/_matrix/client/v3/joined_rooms":
		rooms := []string{}
		for id, m := range f.rooms {
			if m[who] {
				rooms = append(rooms, id)
			}
		}
		out(map[string]any{"joined_rooms": rooms})
	case strings.HasPrefix(path, "/_synapse/admin/v1/rooms/") && strings.HasSuffix(path, "/state"):
		room := strings.TrimSuffix(strings.TrimPrefix(path, "/_synapse/admin/v1/rooms/"), "/state")
		key := ""
		ev := func(typ, k string, c map[string]any) map[string]any {
			return map[string]any{"type": typ, "state_key": k, "sender": "@x:t.lan", "content": c}
		}
		state := []map[string]any{ev("m.room.name", key, map[string]any{"name": f.names[room]})}
		for _, typ := range []string{"m.room.join_rules", "m.room.topic"} {
			if c := f.lastState(room, typ); c != nil {
				state = append(state, ev(typ, key, c))
			}
		}
		if f.levels[room] != nil {
			state = append(state, ev("m.room.power_levels", key, f.levels[room]))
		}
		for mx, in := range f.rooms[room] {
			if in {
				state = append(state, ev("m.room.member", mx, map[string]any{"membership": "join"}))
			}
		}
		out(map[string]any{"state": state})
	case path == "/_matrix/client/v3/createRoom":
		if v, ok := body["invite"]; ok && v == nil { // as Synapse: a null list of invitations fails
			w.WriteHeader(500)
			out(map[string]string{"errcode": "M_UNKNOWN"})
			return
		}
		f.nroom++
		id := "!r" + string(rune('a'+f.nroom)) + ":t.lan"
		f.rooms[id] = map[string]bool{who: true}
		f.botRooms[id] = who == "@go24:t.lan"
		f.names[id], _ = body["name"].(string)
		f.invited[id] = map[string]bool{}
		if inv, ok := body["invite"].([]any); ok {
			for _, mx := range inv {
				f.invited[id][mx.(string)] = true
			}
		}
		if pl, ok := body["power_level_content_override"].(map[string]any); ok && pl["users"] != nil {
			f.levels[id] = roomLevels(pl)
			f.power[id] = map[string]float64{}
			for mx, v := range pl["users"].(map[string]any) {
				f.power[id][mx] = v.(float64)
			}
		}
		if cc, ok := body["creation_content"].(map[string]any); ok && cc["type"] == "m.space" {
			f.spaces[id] = true
		}
		out(map[string]string{"room_id": id})
	case strings.HasSuffix(path, "/members"):
		room := strings.TrimSuffix(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/members")
		if !f.rooms[room][who] {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		var chunk []map[string]any
		for mx := range f.rooms[room] {
			chunk = append(chunk, map[string]any{"state_key": mx, "content": map[string]string{"membership": "join"}})
		}
		out(map[string]any{"chunk": chunk})
	case strings.HasSuffix(path, "/kick"):
		room := strings.TrimSuffix(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/kick")
		mx := body["user_id"].(string)
		if who != "@go24:t.lan" { // a person: in the room, with the right to remove people
			if !f.rooms[room][who] || !f.allowed(room, who, f.level(room, "kick", 50)) || f.power[room][mx] >= f.power[room][who] {
				w.WriteHeader(403)
				out(map[string]string{"errcode": "M_FORBIDDEN"})
				return
			}
			f.chatEvent(room, who, "m.room.member", mx, map[string]any{"membership": "leave"})
		}
		delete(f.rooms[room], mx)
		out(map[string]any{})
	case strings.HasSuffix(path, "/invite"):
		room := strings.TrimSuffix(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/invite")
		if !f.rooms[room][who] || !f.allowed(room, who, f.level(room, "invite", 0)) {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		mx := body["user_id"].(string)
		f.invited[room][mx] = true
		f.chatEvent(room, who, "m.room.member", mx, map[string]any{"membership": "invite"})
		out(map[string]any{})
	case strings.HasSuffix(path, "/leave"):
		room := strings.TrimSuffix(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/leave")
		delete(f.rooms[room], who)
		delete(f.invited[room], who)
		f.chatEvent(room, who, "m.room.member", who, map[string]any{"membership": "leave"})
		out(map[string]any{})
	case strings.Contains(path, "/redact/"):
		f.redacted = append(f.redacted, strings.Split(path, "/")[7])
		out(map[string]string{"event_id": "$redaction"})
	case strings.Contains(path, "/send/"):
		room := strings.TrimPrefix(path, "/_matrix/client/v3/rooms/")
		room = room[:strings.Index(room, "/")]
		if strings.Contains(path, "/send/m.reaction/") {
			body["_type"] = "m.reaction"
		}
		if typ := strings.Split(path, "/")[7]; typ != "m.room.message" && typ != "m.reaction" { // a poll, a vote
			body["_event"] = typ
		}
		typ := strings.Split(path, "/")[7]
		need, _ := f.levels[room]["events_default"].(float64)
		evs, _ := f.levels[room]["events"].(map[string]any)
		if ev, ok := evs[typ].(float64); ok {
			need = ev
		}
		if !f.rooms[room][who] || !f.allowed(room, who, need) {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		id := "$e" + string(rune('a'+len(f.sent)))
		f.sent = append(f.sent, sentEvent{Room: room, Sender: who, Content: body, ID: id})
		out(map[string]string{"event_id": id})
	case path == "/_matrix/client/v3/search":
		re := body["search_categories"].(map[string]any)["room_events"].(map[string]any)
		term := strings.ToLower(re["search_term"].(string))
		only := map[string]bool{}
		if rs, ok := re["filter"].(map[string]any)["rooms"].([]any); ok {
			for _, r := range rs {
				only[r.(string)] = true
			}
		}
		results := []map[string]any{}
		for i := len(f.sent) - 1; i >= 0; i-- {
			e := f.sent[i]
			body, _ := e.Content["body"].(string)
			if !f.rooms[e.Room][who] || (len(only) > 0 && !only[e.Room]) || e.Content["_event"] != nil || !strings.Contains(strings.ToLower(body), term) {
				continue
			}
			ev := timelineEvent(e, 3)
			ev["room_id"] = e.Room
			results = append(results, map[string]any{"result": ev})
		}
		out(map[string]any{"search_categories": map[string]any{"room_events": map[string]any{"results": results}}})
	case strings.Contains(path, "/context/"):
		parts := strings.Split(path, "/")
		room, id := parts[5], parts[7]
		if !f.rooms[room][who] {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		var in []sentEvent
		at := -1
		for _, e := range f.sent {
			if e.Room == room {
				if e.ID == id {
					at = len(in)
				}
				in = append(in, e)
			}
		}
		if at < 0 {
			w.WriteHeader(404)
			out(map[string]string{"errcode": "M_NOT_FOUND"})
			return
		}
		before, after := []map[string]any{}, []map[string]any{}
		for i := at - 1; i >= 0; i-- {
			before = append(before, timelineEvent(in[i], 1))
		}
		for _, e := range in[at+1:] {
			after = append(after, timelineEvent(e, 1))
		}
		out(map[string]any{"event": timelineEvent(in[at], 1), "events_before": before, "events_after": after, "start": "c0"})
	case strings.Contains(path, "/state/"):
		parts := strings.SplitN(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/", 4) // room, "state", type, key
		room, typ := parts[0], parts[2]
		if who != "@go24:t.lan" && !f.rooms[room][who] {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		if r.Method == http.MethodGet {
			switch {
			case typ == "m.room.power_levels" && f.power[room] != nil:
				out(map[string]any{"users": f.power[room], "kick": f.level(room, "kick", 50), "invite": f.level(room, "invite", 0),
					"state_default": f.level(room, "state_default", 50), "events_default": f.levels[room]["events_default"], "events": f.levels[room]["events"]})
			case typ == "m.room.power_levels": // made without levels: anyone in it does anything (allowed)
				out(map[string]any{"state_default": 0})
			case typ == "m.room.avatar" && f.avatar[room] == "":
				w.WriteHeader(404)
				out(map[string]string{"errcode": "M_NOT_FOUND"})
			case typ == "m.room.avatar":
				out(map[string]any{"url": f.avatar[room]})
			case typ == "m.room.name" && f.names[room] != "":
				out(map[string]any{"name": f.names[room]})
			case typ == "m.room.pinned_events" && f.pins[room] == nil:
				w.WriteHeader(404)
				out(map[string]string{"errcode": "M_NOT_FOUND"})
			case typ == "m.room.pinned_events":
				out(map[string]any{"pinned": f.pins[room]})
			case typ == "go24.autodelete" || typ == "m.room.topic" || typ == "m.room.join_rules": // the last one put, as Matrix keeps a room's state
				for i := len(f.state) - 1; i >= 0; i-- {
					if strings.HasPrefix(f.state[i].Path, room+"/state/"+typ+"/") {
						out(f.state[i].Content)
						return
					}
				}
				w.WriteHeader(404)
				out(map[string]string{"errcode": "M_NOT_FOUND"})
			default: // as before: the other rooms' states read empty
				out(map[string]any{})
			}
			return
		}
		need := f.level(room, "state_default", 50)
		if typ == "m.room.pinned_events" {
			need = 0 // group chats let anyone pin
		}
		if evs, _ := f.levels[room]["events"].(map[string]any); evs != nil {
			if v, ok := evs[typ].(float64); ok {
				need = v
			}
		}
		if who != "@go24:t.lan" && typ != "m.room.member" && (f.botRooms[room] || !f.allowed(room, who, need)) {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		switch typ {
		case "m.room.power_levels":
			if users, ok := body["users"].(map[string]any); ok && who != "@go24:t.lan" {
				if f.power[room] != nil { // a change of the levels the room was made with: a line of the chat
					prev := map[string]any{}
					for mx, v := range f.power[room] {
						prev[mx] = v
					}
					f.chatEvent(room, who, typ, "", map[string]any{"users": users, "_prev": map[string]any{"users": prev}})
				}
				f.levels[room] = roomLevels(body)
				f.power[room] = map[string]float64{}
				for mx, v := range users {
					f.power[room][mx] = v.(float64)
				}
			}
		case "m.room.name":
			f.names[room], _ = body["name"].(string)
		case "m.room.avatar":
			f.avatar[room], _ = body["url"].(string)
		case "m.room.pinned_events":
			f.pins[room] = []string{}
			for _, id := range body["pinned"].([]any) {
				f.pins[room] = append(f.pins[room], id.(string))
			}
		}
		if (who != "@go24:t.lan" || body["by"] != nil) && (typ == "m.room.name" || typ == "m.room.avatar" || typ == "go24.autodelete") {
			f.chatEvent(room, who, typ, "", body)
		}
		f.state = append(f.state, stateEvent{Path: strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), Sender: who, Content: body})
		out(map[string]any{})
	case strings.Contains(path, "/account_data/m.direct"):
		if r.Method == http.MethodGet {
			if f.acct[who] == nil {
				w.WriteHeader(404)
				out(map[string]string{"errcode": "M_NOT_FOUND"})
				return
			}
			out(f.acct[who])
			return
		}
		f.acct[who] = body
		out(map[string]any{})
	case strings.HasPrefix(path, "/_matrix/client/v3/user/") && strings.Contains(path, "/rooms/"):
		parts := strings.Split(path, "/") // /_matrix/client/v3/user/{u}/rooms/{r}/tags|account_data/{name}
		u, room, kind, name := parts[5], parts[7], parts[8], parts[9]
		if u != who {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		k := who + " " + room
		if f.roomData[k] == nil {
			f.roomData[k] = map[string]any{}
		}
		data := f.roomData[k]
		tags, _ := data["m.tag"].(map[string]any)
		if tags == nil {
			tags = map[string]any{}
		}
		switch {
		case kind == "tags" && r.Method == http.MethodPut:
			tags[name] = body
			data["m.tag"] = tags
		case kind == "tags" && r.Method == http.MethodDelete:
			delete(tags, name)
			data["m.tag"] = tags
		case kind == "account_data" && r.Method == http.MethodPut:
			data[name] = body
		case kind == "account_data":
			if data[name] == nil {
				w.WriteHeader(404)
				out(map[string]string{"errcode": "M_NOT_FOUND"})
				return
			}
			out(data[name])
			return
		}
		out(map[string]any{})
	case strings.HasPrefix(path, "/_matrix/client/v3/pushrules/"):
		if f.rules[who] == nil {
			f.rules[who] = map[string]bool{}
		}
		if r.Method == http.MethodGet {
			rules := []map[string]any{{"rule_id": ".m.rule.master", "enabled": false, "actions": []any{}}}
			for room := range f.rules[who] {
				rules = append(rules, map[string]any{"rule_id": room, "enabled": true, "actions": []any{}})
			}
			out(map[string]any{"global": map[string]any{"override": rules}})
			return
		}
		room := strings.TrimPrefix(path, "/_matrix/client/v3/pushrules/global/override/")
		if r.Method == http.MethodDelete {
			if !f.rules[who][room] {
				w.WriteHeader(404)
				out(map[string]string{"errcode": "M_NOT_FOUND"})
				return
			}
			delete(f.rules[who], room)
		} else {
			f.rules[who][room] = true
		}
		out(map[string]any{})
	case strings.Contains(path, "/typing/"):
		parts := strings.Split(path, "/")
		if !f.rooms[parts[5]][who] || parts[7] != who {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		f.typing[parts[5]+" "+who] = body["typing"] == true
		out(map[string]any{})
	case strings.HasSuffix(path, "/read_markers"):
		ev, _ := body["m.read"].(string)
		room := strings.Split(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/")[0]
		if ev != "" {
			f.read[who+" "+room], f.readAll[who+" "+room] = ev, ev
		}
		out(map[string]any{})
	case strings.Contains(path, "/receipt/m.read/"): // a threaded receipt: as Synapse, the event must be of that thread
		parts := strings.Split(path, "/")
		room, ev, thread := parts[5], parts[8], body["thread_id"].(string)
		at := indexOf(f.sent, ev)
		if at < 0 || (thread == "main") != (threadOf(f.sent[at]) == "") || (thread != "main" && threadOf(f.sent[at]) != thread) {
			w.WriteHeader(400)
			out(map[string]string{"errcode": "M_INVALID_PARAM", "error": "event_id " + ev + " is not related to thread " + thread})
			return
		}
		if thread == "main" {
			f.read[who+" "+room] = ev
		} else {
			f.readTh[who+" "+room+" "+thread] = ev
		}
		out(map[string]any{})
	case strings.HasSuffix(path, "/threads"):
		room := strings.Split(path, "/")[5]
		var chunk []map[string]any
		for i := len(f.sent) - 1; i >= 0; i-- { // the latest answered first
			root := threadOf(f.sent[i])
			if f.sent[i].Room != room || root == "" || slices.ContainsFunc(chunk, func(c map[string]any) bool { return c["event_id"] == root }) {
				continue
			}
			if at := indexOf(f.sent, root); at >= 0 {
				chunk = append(chunk, f.withThread(timelineEvent(f.sent[at], at+10)))
			}
		}
		out(map[string]any{"chunk": chunk})
	case strings.Contains(path, "/messages"):
		room := strings.TrimPrefix(strings.TrimPrefix(path, "/_matrix/client/v3/rooms/"), "/_synapse/admin/v1/rooms/")
		room = room[:strings.Index(room, "/")]
		if !f.rooms[room][who] && !strings.HasPrefix(path, "/_synapse/admin/") {
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		var chunk []map[string]any
		var filter struct {
			ContainsURL *bool    `json:"contains_url"`
			Types       []string `json:"types"`
		}
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filter")), &filter)
		for i := len(f.sent) - 1; i >= 0; i-- {
			if e := f.sent[i]; e.Room == room && (filter.ContainsURL == nil || *filter.ContainsURL == (e.Content["url"] != nil)) {
				ev := f.withThread(timelineEvent(e, i+10)) // in the order sent, as /sync gives them
				if filter.Types == nil || slices.Contains(filter.Types, ev["type"].(string)) {
					chunk = append(chunk, ev)
				}
			}
		}
		if r.URL.Query().Get("from") == "t0" { // one page: the history ends there
			chunk = nil
		}
		out(map[string]any{"chunk": chunk, "end": "t0"})
	case strings.Contains(path, "/relations/"):
		parts := strings.Split(path, "/")
		room, root := parts[5], parts[7]
		var chunk []map[string]any
		for _, e := range f.sent {
			if rel, _ := e.Content["m.relates_to"].(map[string]any); e.Room == room && rel["event_id"] == root && rel["rel_type"] == parts[8] &&
				!slices.Contains(f.redacted, e.ID) { // a redacted event relates to nothing any more
				ev := timelineEvent(e, 2)
				if parts[8] == "m.reference" {
					ev["origin_server_ts"] = indexOf(f.sent, e.ID) + 10 // in the order sent
				}
				chunk = append(chunk, ev)
			}
		}
		if r.URL.Query().Get("dir") == "b" { // the newest first, as many as asked
			slices.Reverse(chunk)
			if n, _ := strconv.Atoi(r.URL.Query().Get("limit")); n > 0 && len(chunk) > n {
				chunk = chunk[:n]
			}
		}
		out(map[string]any{"chunk": chunk})
	case path == "/_matrix/media/v3/upload":
		mxc := fmt.Sprintf("mxc://t.lan/m%d", len(f.media))
		f.media[mxc] = b
		out(map[string]string{"content_uri": mxc})
	case strings.HasPrefix(path, "/_matrix/client/v1/media/download/"):
		data, ok := f.media["mxc://"+strings.TrimPrefix(path, "/_matrix/client/v1/media/download/")]
		if !ok {
			w.WriteHeader(404)
			out(map[string]string{"errcode": "M_NOT_FOUND"})
			return
		}
		_, _ = w.Write(data)
	case strings.Contains(path, "/event/"):
		parts := strings.Split(path, "/")
		if !f.rooms[parts[5]][who] { // as Synapse: the events of a room one is not in are not given
			w.WriteHeader(403)
			out(map[string]string{"errcode": "M_FORBIDDEN"})
			return
		}
		for _, e := range f.sent {
			if e.ID == parts[7] {
				out(timelineEvent(e, 1))
				return
			}
		}
		w.WriteHeader(404)
		out(map[string]string{"errcode": "M_NOT_FOUND"})
	case path == "/_matrix/client/v3/sync" && who != "@go24:t.lan":
		join := map[string]any{}
		for id, members := range f.rooms {
			if !members[who] {
				continue
			}
			var tl []map[string]any
			highlights, n := 0, 0 // as Synapse counts them: messages of others that mention the person
			for i, e := range f.sent {
				if e.Room == id {
					n++
					tl = []map[string]any{timelineEvent(e, i+10)} // in the order sent
					if e.Sender != who && mentions(e.Content, who) {
						highlights++
					}
				}
			}
			state := []map[string]any{}
			name := map[string]any{"type": "m.room.name", "state_key": "", "event_id": "$name" + id, "sender": who, "content": map[string]string{"name": f.names[id]}}
			if young := n == 1; young { // as Synapse: a new room's name comes in its timeline, not in the state before it
				tl = append([]map[string]any{name}, tl...)
			} else {
				state = append(state, name)
			}
			if f.spaces[id] {
				state = append(state, map[string]any{"type": "m.room.create", "state_key": "", "content": map[string]string{"type": "m.space"}})
			}
			if f.levels[id]["events_default"] != nil {
				state = append(state, map[string]any{"type": "m.room.power_levels", "state_key": "", "content": f.levels[id]})
			}
			if f.avatar[id] != "" {
				state = append(state, map[string]any{"type": "m.room.avatar", "state_key": "", "content": map[string]string{"url": f.avatar[id]}})
			}
			receipts := map[string]any{} // the read markers of the room's people, as one m.receipt
			for k, ev := range f.read {
				if mxid, rm, _ := strings.Cut(k, " "); rm == id {
					if receipts[ev] == nil {
						receipts[ev] = map[string]any{"m.read": map[string]any{}}
					}
					receipts[ev].(map[string]any)["m.read"].(map[string]any)[mxid] = map[string]any{"ts": 0}
				}
			}
			join[id] = map[string]any{"state": map[string]any{"events": state}, "timeline": map[string]any{"events": tl},
				"unread_notifications": map[string]int{"notification_count": 1, "highlight_count": highlights},
				"ephemeral": map[string]any{"events": []map[string]any{{"type": "m.receipt", "content": receipts}}}}
			if th := f.threadUnread(who, id); len(th) > 0 && strings.Contains(r.URL.Query().Get("filter"), `"unread_thread_notifications":true`) {
				join[id].(map[string]any)["unread_thread_notifications"] = th
			}
			var ad []map[string]any // the person's own marks of the room
			for typ, c := range f.roomData[who+" "+id] {
				if typ == "m.tag" {
					c = map[string]any{"tags": c}
				}
				ad = append(ad, map[string]any{"type": typ, "content": c})
			}
			join[id].(map[string]any)["account_data"] = map[string]any{"events": ad}
		}
		ad := []map[string]any{}
		if f.acct[who] != nil {
			ad = append(ad, map[string]any{"type": "m.direct", "content": f.acct[who]})
		}
		out(map[string]any{"next_batch": "n", "rooms": map[string]any{"join": join}, "account_data": map[string]any{"events": ad}})
	case path == "/_matrix/client/v3/sync":
		res := map[string]any{"next_batch": "s" + time.Now().Format("150405.000000")}
		if r.URL.Query().Get("since") != "" && len(f.pending) > 0 {
			join := map[string]any{}
			for _, ev := range f.pending {
				room := ev.Content["_room"].(string)
				delete(ev.Content, "_room")
				join[room] = map[string]any{"timeline": map[string]any{"events": []matrix.Event{ev}}}
			}
			f.pending = nil
			res["rooms"] = map[string]any{"join": join}
		}
		out(res)
	default:
		w.WriteHeader(404)
		out(map[string]string{"errcode": "M_UNRECOGNIZED", "error": path})
	}
}

func TestMatrixSync(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
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
	newUser := func(username string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, username) VALUES ('sub-' || $1, $1, $1 || '@x', $1) RETURNING id`, username).Scan(&id))
		return id
	}
	owner, dev := newUser("Owner"), newUser("dev")
	newUser("outsider") // an employee outside the project
	var imported string //, never signed in: no username, no Matrix account
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('portal:x:1', 'Импортированный', '') RETURNING id`).Scan(&imported))

	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ts := tasks.NewStore(pool)
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: ts, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IDP: "oidc-authentik", ElementURL: "https://el", AppURL: "https://go24", Company: "ООО Ромашка"})

	_, err = ts.CreateProject(ctx, owner, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, owner, "GO")
	must(ts.SetMember(ctx, p, dev, "member"))
	_, err = ts.CreateProject(ctx, owner, "SEC", "Секретно", "")
	must(err)
	sec, _ := ts.ProjectByKey(ctx, owner, "SEC")
	must(ts.SetVisibility(ctx, sec, "participants"))

	must(s.Reconcile(ctx))
	// accounts: lower-cased login, linked to the Authentik subject; no account for the imported person
	if u := fake.users["@owner:t.lan"]; u == nil || fake.extID["@owner:t.lan"] != "sub-Owner" || u.DisplayName != "Owner" {
		t.Fatalf("owner account: %+v %v", u, fake.extID)
	}
	if len(fake.users) != 3 {
		t.Fatalf("accounts = %d, want 3", len(fake.users))
	}
	space, _ := s.roomOf(ctx, "space", nil)
	gid := p.ID
	room, _ := s.roomOf(ctx, "project", &gid)
	sid := sec.ID
	secRoom, _ := s.roomOf(ctx, "project", &sid)
	// a "participants only" project also has a room (its chat and posts, members only); its tasks get own rooms
	if !fake.spaces[space] || room == "" || secRoom == "" || len(fake.rooms[secRoom]) != 2 {
		t.Fatalf("space=%q room=%q secret project room=%q members %v", space, room, secRoom, fake.rooms[secRoom])
	}
	if len(fake.rooms[space]) != 4 || !fake.rooms[room]["@dev:t.lan"] || fake.rooms[room]["@outsider:t.lan"] {
		t.Fatalf("members: space %v, project %v", fake.rooms[space], fake.rooms[room])
	}

	// a go24 message goes to the task's thread under the author's name
	key, err := ts.CreateTask(ctx, owner, p, tasks.NewTask{Title: "Сделать", ResponsibleID: dev})
	must(err)
	tid, _ := ts.TaskIDByKey(ctx, key)
	c, err := ts.PostComment(ctx, dev, tid, "привет из go24")
	must(err)
	must(s.onComment(ctx, tid, c))
	last := fake.sent[len(fake.sent)-1]
	root := fake.sent[0].ID
	rel, _ := last.Content["m.relates_to"].(map[string]any)
	if last.Sender != "@dev:t.lan" || last.Room != room || rel["event_id"] != root || last.Content["go24.comment"] != c.ID {
		t.Fatalf("sent = %+v (root %s)", last, root)
	}
	// the imported author has no account: the bot posts with their name
	must(ts.SetParticipant(ctx, owner, tid, imported, "auditor", true))
	ci, err := ts.PostComment(ctx, imported, tid, "старый комментарий")
	must(err)
	must(s.onComment(ctx, tid, ci))
	if l := fake.sent[len(fake.sent)-1]; l.Sender != "@go24:t.lan" || l.Content["body"] != "Импортированный: старый комментарий" {
		t.Fatalf("bot post = %+v", l)
	}

	// a Matrix reply in the thread comes back into the task chat once; our own echo is ignored
	fake.mu.Lock()
	fake.pending = []matrix.Event{
		{EventID: "$m1", Type: "m.room.message", Sender: "@owner:t.lan", Content: map[string]any{"_room": room, "msgtype": "m.text",
			"body": "ответ из Element", "m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": root}}},
	}
	fake.mu.Unlock()
	go s.syncLoop(ctx)
	deadline := time.Now().Add(10 * time.Second)
	var n int
	for time.Now().Before(deadline) {
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM task_comments WHERE task_id = $1 AND body = 'ответ из Element'`, tid).Scan(&n)
		if n > 0 {
			break
		}
		fake.mu.Lock()
		if len(fake.pending) == 0 && n == 0 { // the first sync only takes the position; deliver again
			fake.pending = []matrix.Event{{EventID: "$m1", Type: "m.room.message", Sender: "@owner:t.lan", Content: map[string]any{"_room": room,
				"msgtype": "m.text", "body": "ответ из Element", "m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": root}}}}
		}
		fake.mu.Unlock()
		time.Sleep(200 * time.Millisecond)
	}
	if n != 1 {
		t.Fatalf("inbound comments = %d, want 1", n)
	}

	// a plain reply (not in the thread) to the task's message also lands in the task; a message without
	// a relation stays in the project's general chat
	before := 0
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM task_comments WHERE task_id = $1`, tid).Scan(&before)
	must(s.inbound(ctx, room, matrix.Event{EventID: "$m2", Type: "m.room.message", Sender: "@owner:t.lan", Content: map[string]any{
		"msgtype": "m.text", "body": "> <@dev:t.lan> привет из go24\n\nответ цитатой",
		"m.relates_to": map[string]any{"m.in_reply_to": map[string]any{"event_id": fake.sent[1].ID}}}}))
	must(s.inbound(ctx, room, matrix.Event{EventID: "$m3", Type: "m.room.message", Sender: "@owner:t.lan", Content: map[string]any{
		"msgtype": "m.text", "body": "общий чат проекта"}}))
	var after int
	var lastBody string
	_ = pool.QueryRow(ctx, `SELECT count(*), (array_agg(body ORDER BY created_at DESC))[1] FROM task_comments WHERE task_id = $1`, tid).Scan(&after, &lastBody)
	if after != before+1 || lastBody != "ответ цитатой" {
		t.Fatalf("reply: %d → %d, last %q", before, after, lastBody)
	}

	// a task of a participants-only project gets its own room with exactly its participants
	sk, err := ts.CreateTask(ctx, owner, sec, tasks.NewTask{Title: "Тайна", ResponsibleID: dev})
	must(err)
	stid, _ := ts.TaskIDByKey(ctx, sk)
	link, err := s.Link(ctx, stid)
	must(err)
	troom, _ := s.roomOf(ctx, "task", &stid)
	if troom == "" || !strings.HasPrefix(link, "https://el/#/room/"+troom) {
		t.Fatalf("task room %q link %q", troom, link)
	}
	must(s.Reconcile(ctx))
	if len(fake.rooms[troom]) != 3 || !fake.rooms[troom]["@dev:t.lan"] || fake.rooms[troom]["@outsider:t.lan"] {
		t.Fatalf("task room members = %v", fake.rooms[troom])
	}

	// leaving the project removes from its room; dismissal deactivates the account everywhere
	must(ts.RemoveMember(ctx, p, dev))
	must(s.Reconcile(ctx))
	if fake.rooms[room]["@dev:t.lan"] {
		t.Fatal("removed member still in the project room")
	}
	must(pool.QueryRow(ctx, `UPDATE users SET active = false WHERE id = $1 RETURNING id`, dev).Scan(new(string)))
	must(s.Reconcile(ctx))
	if !fake.users["@dev:t.lan"].Deactivated || fake.rooms[troom]["@dev:t.lan"] || fake.rooms[space]["@dev:t.lan"] {
		t.Fatalf("dismissed: %+v, task room %v", fake.users["@dev:t.lan"], fake.rooms[troom])
	}
}

func TestFeedBridge(t *testing.T) {
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
	newUser := func(username string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-' || $1, $1, $1) RETURNING id`, username).Scan(&id))
		return id
	}
	ann, bob := newUser("ann"), newUser("bob")
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ts := tasks.NewStore(pool)
	fs := feed.NewStore(pool, ts)
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: ts, Feed: fs,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co", FeedName: "Лента"})
	must(s.Reconcile(ctx))
	room, _ := s.roomOf(ctx, "feed", nil)
	if room == "" || fake.names[room] != "Лента" || !fake.rooms[room]["@ann:t.lan"] || !fake.rooms[room]["@bob:t.lan"] {
		t.Fatalf("feed room %q: %v", room, fake.rooms[room])
	}
	last := func() sentEvent { return fake.sent[len(fake.sent)-1] }

	// go24 → Matrix: post, edit, comment, like, unlike, delete
	pid, err := fs.CreatePost(ctx, ann, "", "Новость", "Текст", true)
	must(err)
	s.OnPost(ctx, pid)
	if l := last(); l.Room != room || l.Sender != "@ann:t.lan" || l.Content["body"] != "❗ Новость\nТекст" || l.Content["go24.post"] != pid {
		t.Fatalf("post sent = %+v", l)
	}
	postEv := last().ID
	must(fs.EditPost(ctx, ann, pid, "Новость", "Исправлено"))
	s.OnPostEdit(ctx, pid)
	if rel, _ := last().Content["m.relates_to"].(map[string]any); rel["rel_type"] != "m.replace" || rel["event_id"] != postEv {
		t.Fatalf("edit sent = %+v", last())
	}
	c, err := fs.AddComment(ctx, bob, pid, "Отлично")
	must(err)
	s.OnFeedComment(ctx, pid, c)
	if rel, _ := last().Content["m.relates_to"].(map[string]any); last().Sender != "@bob:t.lan" || rel["rel_type"] != "m.thread" || rel["event_id"] != postEv {
		t.Fatalf("comment sent = %+v", last())
	}
	_, err = fs.React(ctx, bob, pid, "laugh")
	must(err)
	s.OnReact(ctx, pid, "", bob)
	reaction := last()
	rel, _ := reaction.Content["m.relates_to"].(map[string]any)
	if _, ev := fs.MyReaction(ctx, pid, bob, false); reaction.Content["_type"] != "m.reaction" || ev != reaction.ID || rel["key"] != "😂" {
		t.Fatalf("reaction = %+v", reaction)
	}
	s.OnUnreact(ctx, pid, reaction.ID)
	if len(fake.redacted) != 1 || fake.redacted[0] != reaction.ID {
		t.Fatalf("redacted = %v", fake.redacted)
	}
	// a reaction to a comment goes to the comment's event
	_, _, err = fs.ReactComment(ctx, ann, c.ID, "wonder")
	must(err)
	s.OnReact(ctx, pid, c.ID, ann)
	if rel, _ := last().Content["m.relates_to"].(map[string]any); rel["key"] != "😮" || rel["event_id"] != fs.CommentEvent(ctx, c.ID) || rel["event_id"] == "" {
		t.Fatalf("comment reaction = %+v", last())
	}

	// Matrix → go24: a new message in the feed room is a post; a thread reply is a comment; a reaction is a like;
	// an edit and a deletion by the author follow; our own events are never copied back
	in := func(ev matrix.Event) { t.Helper(); must(s.inbound(ctx, room, ev)) }
	in(matrix.Event{EventID: "$n1", Type: "m.room.message", Sender: "@bob:t.lan", Content: map[string]any{"msgtype": "m.text", "body": "**Заголовок**\nиз Element"}})
	var mpost, title, body string
	must(pool.QueryRow(ctx, `SELECT id, title, body FROM feed_posts WHERE matrix_event = '$n1'`).Scan(&mpost, &title, &body))
	if title != "Заголовок" || body != "из Element" {
		t.Fatalf("post from Matrix: %q %q", title, body)
	}
	in(matrix.Event{EventID: "$n2", Type: "m.room.message", Sender: "@ann:t.lan", Content: map[string]any{"msgtype": "m.text", "body": "коммент",
		"m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": "$n1"}}})
	in(matrix.Event{EventID: "$n3", Type: "m.reaction", Sender: "@ann:t.lan", Content: map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$n1", "key": "❤️"}}})
	in(matrix.Event{EventID: "$n6", Type: "m.reaction", Sender: "@bob:t.lan", Content: map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": "$n2", "key": "😂"}}})
	in(matrix.Event{EventID: "$n4", Type: "m.room.message", Sender: "@bob:t.lan", Content: map[string]any{"msgtype": "m.text", "body": "* правка",
		"m.new_content": map[string]any{"msgtype": "m.text", "body": "правка"}, "m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": "$n1"}}})
	in(matrix.Event{EventID: "$n5", Type: "m.room.message", Sender: "@ann:t.lan", Content: map[string]any{"msgtype": "m.text", "body": "* чужая правка",
		"m.new_content": map[string]any{"msgtype": "m.text", "body": "взлом"}, "m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": "$n1"}}})
	in(matrix.Event{EventID: "$e9", Type: "m.room.message", Sender: "@ann:t.lan", Content: map[string]any{"msgtype": "m.text", "body": "эхо", "go24.post": "x"}})
	items, err := fs.List(ctx, ann, feed.Filter{})
	must(err)
	var got *feed.Post
	for _, it := range items {
		if it.Post != nil && it.Post.ID == mpost {
			got = it.Post
		}
	}
	if got == nil || got.Body != "правка" || len(got.Comments) != 1 || got.Comments[0].Body != "коммент" || got.Reactions.Mine != "kiss" || got.Reactions.Total != 1 ||
		got.Comments[0].Reactions.Total != 1 || got.Comments[0].Reactions.Top[0].Reaction != "laugh" {
		t.Fatalf("post after Matrix events = %+v", got)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM feed_posts`).Scan(&n)
	if n != 2 {
		t.Fatalf("posts = %d (echo copied?)", n)
	}
	in(matrix.Event{EventID: "$n6", Type: "m.room.redaction", Sender: "@ann:t.lan", Redacts: "$n1"}) // not the author: ignored
	in(matrix.Event{EventID: "$n7", Type: "m.room.redaction", Sender: "@bob:t.lan", Content: map[string]any{"redacts": "$n1"}})
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM feed_posts`).Scan(&n)
	if n != 1 {
		t.Fatalf("posts after the author's deletion = %d", n)
	}
}

func TestMessenger(t *testing.T) {
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
	newUser := func(username string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-' || $1, $1, $1) RETURNING id`, username).Scan(&id))
		return id
	}
	ann, bob, eve := newUser("ann"), newUser("bob"), newUser("eve")
	var ghost string // imported, no Matrix account
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('portal:1', 'ghost') RETURNING id`).Scan(&ghost))
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))

	// a private chat: started from go24, the other person is in it at once, both see it as a private chat
	room, err := s.DirectChat(ctx, ann, bob)
	must(err)
	again, err := s.DirectChat(ctx, ann, bob)
	must(err)
	if again != room || !fake.rooms[room]["@bob:t.lan"] {
		t.Fatalf("direct chat %q / %q, members %v", room, again, fake.rooms[room])
	}
	if _, err := s.DirectChat(ctx, ann, ghost); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("chat with someone without an account: %v", err)
	}
	// «Заметки»: the chat with oneself, made once, only its owner in it, listed as notes under its name
	notes, err := s.NotesChat(ctx, ann, "Заметки")
	must(err)
	if again, err := s.NotesChat(ctx, ann, "Заметки"); err != nil || again != notes || notes == room {
		t.Fatalf("notes %q / %q (%v)", notes, again, err)
	}
	if len(fake.rooms[notes]) != 1 || !fake.rooms[notes]["@ann:t.lan"] || len(fake.invited[notes]) != 0 {
		t.Fatalf("notes members %v, invited %v", fake.rooms[notes], fake.invited[notes])
	}
	_, err = s.Send(ctx, ann, notes, "купить молоко", "")
	must(err)
	all, err := s.Rooms(ctx, ann)
	must(err)
	var seen bool
	for _, r := range all {
		if r.ID == notes {
			seen = r.Kind == "notes" && r.Name == "Заметки" && r.Last != nil && r.Last.Body == "купить молоко"
		}
		if r.ID == room && r.Kind != "dm" {
			t.Fatalf("the chat with bob became %q", r.Kind)
		}
	}
	if !seen {
		t.Fatalf("ann's chats = %+v", all)
	}
	if bobs, err := s.NotesChat(ctx, bob, "Заметки"); err != nil || bobs == notes {
		t.Fatalf("bob's notes %q (%v)", bobs, err)
	}
	m, err := s.Send(ctx, ann, room, "привет, Боб", "")
	must(err)
	if fake.sent[len(fake.sent)-1].Sender != "@ann:t.lan" {
		t.Fatal("not sent as ann")
	}
	_, err = s.Send(ctx, bob, room, "ответ в треде", m.ID)
	must(err)

	// bob sees the chat (named after ann), the message and its thread
	rooms, err := s.Rooms(ctx, bob)
	must(err)
	var dm *Room
	for i := range rooms {
		if rooms[i].ID == room {
			dm = &rooms[i]
		}
		if rooms[i].Kind == "space" {
			t.Fatal("the space is listed as a chat")
		}
	}
	if dm == nil || dm.Kind != "dm" || dm.PeerID != ann || dm.Last == nil {
		t.Fatalf("bob's chats = %+v", rooms)
	}
	msgs, _, err := s.Messages(ctx, bob, room, "")
	must(err)
	if len(msgs) != 1 || msgs[0].Body != "привет, Боб" || msgs[0].SenderID != ann || msgs[0].Mine {
		t.Fatalf("main timeline = %+v (thread replies must not be in it)", msgs)
	}
	root, replies, err := s.Thread(ctx, ann, room, m.ID)
	must(err)
	if root.ID != m.ID || len(replies) != 1 || replies[0].SenderID != bob {
		t.Fatalf("thread = %+v %+v", root, replies)
	}
	// read markers: one's own message is read (in its thread); opening the chat reads its main timeline, the thread
	// keeps its reply unread (TestThreadUnread)
	reply := fake.sent[len(fake.sent)-1].ID
	if got := fake.readTh["@bob:t.lan "+room+" "+m.ID]; got != reply {
		t.Fatalf("bob's marker in the thread after his reply = %q, want %q", got, reply)
	}
	must(s.MarkRoomRead(ctx, ann, room))
	if got := fake.read["@ann:t.lan "+room]; got != m.ID {
		t.Fatalf("ann's marker = %q, want the newest message outside the threads %q", got, m.ID)
	}
	// files (in a chat of their own): a picture goes into the media repository as bob's and comes back to eve; ann, not
	// in the chat, gets nothing
	fchat, err := s.DirectChat(ctx, bob, eve)
	must(err)
	pic, err := s.SendFile(ctx, bob, fchat, "", "снимок.png", "image/png", strings.NewReader("PNGDATA"), 7, nil)
	must(err)
	if pic.File == nil || pic.File.Kind != "image" || pic.File.Name != "снимок.png" || fake.sent[len(fake.sent)-1].Sender != "@bob:t.lan" {
		t.Fatalf("the picture sent: %+v", pic.File)
	}
	f, body, err := s.OpenFile(ctx, eve, fchat, pic.ID)
	must(err)
	data, _ := io.ReadAll(body)
	body.Close()
	if string(data) != "PNGDATA" || f.Mime != "image/png" || f.Size != 7 {
		t.Fatalf("eve's copy of the picture: %q %+v", data, f)
	}
	if _, _, err := s.OpenFile(ctx, ann, fchat, pic.ID); err == nil {
		t.Fatal("someone outside the chat opened its file")
	}
	voice, err := s.SendFile(ctx, eve, fchat, "", "voice.ogg", "audio/ogg", strings.NewReader("OGG"), 3, map[string]any{"duration": 4200})
	must(err)
	if c := fake.sent[len(fake.sent)-1].Content; voice.File.Kind != "audio" || voice.File.Duration != 4200 || c["org.matrix.msc3245.voice"] == nil {
		t.Fatalf("the voice message: %+v %v", voice.File, c)
	}
	if list, _, err := s.Messages(ctx, bob, fchat, ""); err != nil || len(list) < 2 || list[len(list)-1].File == nil || list[len(list)-2].File.Kind != "image" {
		t.Fatalf("files in the chat: %v %+v", err, list)
	}
	// the chat's «Медиа, файлы и ссылки»: the files newest first, each link once (where it was written last), not
	// for one outside the chat
	_, err = s.Send(ctx, bob, fchat, "смотри https://go24.lan/t/GO24DEV-1, и https://example.com/a?b=1.", "")
	must(err)
	_, err = s.Send(ctx, eve, fchat, "ещё раз https://go24.lan/t/GO24DEV-1", "")
	must(err)
	files, links, err := s.Media(ctx, eve, fchat)
	must(err)
	if len(files) != 2 || files[0].ID != voice.ID || files[1].ID != pic.ID || files[1].File.Kind != "image" {
		t.Fatalf("the chat's files: %+v", files)
	}
	if len(links) != 2 || links[0].URL != "https://go24.lan/t/GO24DEV-1" || links[0].SenderName != "eve" || links[1].URL != "https://example.com/a?b=1" || links[1].SenderName != "bob" {
		t.Fatalf("the chat's links: %+v", links)
	}
	if _, _, err := s.Media(ctx, ann, fchat); err == nil {
		t.Fatal("someone outside the chat saw its files")
	}
	// reply with a quote, edit and delete one's own only, forward to another chat (the same file in Matrix)
	q, err := s.Reply(ctx, eve, fchat, "отвечаю на картинку", "", pic.ID)
	must(err)
	if rel, _ := fake.sent[len(fake.sent)-1].Content["m.relates_to"].(map[string]any); rel["m.in_reply_to"] == nil || rel["rel_type"] != nil {
		t.Fatalf("the reply's relation: %v", rel)
	}
	if q.Reply == nil || q.Reply.ID != pic.ID || q.Reply.SenderName != "bob" {
		t.Fatalf("the reply's quote: %+v", q.Reply)
	}
	if list, _, _ := s.Messages(ctx, bob, fchat, ""); list[len(list)-1].Reply == nil || list[len(list)-1].Reply.Body != "снимок.png" {
		t.Fatalf("the quote in the chat: %+v", list[len(list)-1].Reply)
	}
	if _, err := s.Edit(ctx, bob, fchat, q.ID, "чужое"); err == nil {
		t.Fatal("bob edited eve's message")
	}
	if _, err := s.Edit(ctx, bob, fchat, pic.ID, "картинка"); err == nil {
		t.Fatal("a file was edited as a text")
	}
	ed, err := s.Edit(ctx, eve, fchat, q.ID, "исправила")
	must(err)
	if c := fake.sent[len(fake.sent)-1].Content; ed.Body != "исправила" || !ed.Edited || c["m.new_content"].(map[string]any)["body"] != "исправила" {
		t.Fatalf("the edit: %+v %v", ed, c)
	}
	if list, _, _ := s.Messages(ctx, bob, fchat, ""); list[len(list)-1].Body != "исправила" || !list[len(list)-1].Edited {
		t.Fatalf("the edited message in the chat: %+v", list[len(list)-1])
	}
	if err := s.Delete(ctx, bob, fchat, q.ID); err == nil {
		t.Fatal("bob deleted eve's message")
	}
	// formatting: a plain text goes to Matrix as it is, a formatted one also as HTML for Element
	if c := fake.sent[len(fake.sent)-1].Content; c["formatted_body"] != nil {
		t.Fatalf("a plain edit carries HTML: %v", c)
	}
	fm, err := s.Reply(ctx, eve, fchat, "[b]важно[/b] см. https://x.ru", "", "")
	must(err)
	if c := fake.sent[len(fake.sent)-1].Content; c["format"] != "org.matrix.custom.html" ||
		c["formatted_body"] != `<b>важно</b> см. <a href="https://x.ru" target="_blank" rel="noopener">https://x.ru</a>` {
		t.Fatalf("a formatted message: %v", c)
	}
	_, err = s.Edit(ctx, eve, fchat, fm.ID, "*да*")
	must(err)
	if c := fake.sent[len(fake.sent)-1].Content; c["formatted_body"] != "* <b>да</b>" || c["m.new_content"].(map[string]any)["formatted_body"] != "<b>да</b>" {
		t.Fatalf("a formatted edit: %v", c)
	}
	n := len(fake.redacted)
	must(s.Delete(ctx, eve, fchat, q.ID))
	if len(fake.redacted) != n+1 {
		t.Fatal("eve's message not redacted")
	}
	fw, err := s.Forward(ctx, bob, fchat, pic.ID, room)
	must(err)
	c := fake.sent[len(fake.sent)-1]
	if c.Room != room || c.Content["url"] != fake.sent[0+indexOf(fake.sent, pic.ID)].Content["url"] || fw.Forwarded != "bob" || fw.File == nil {
		t.Fatalf("the forwarded picture: %+v %v", fw, c.Content)
	}
	if _, err := s.Forward(ctx, ann, fchat, pic.ID, room); err == nil {
		t.Fatal("ann forwarded from a chat she is not in")
	}
	// live: an edit and a deletion reach the open chat as changes
	if c := liveChange(matrix.Event{Type: "m.room.redaction", Redacts: "$x"}, "!r"); c == nil || c.Change != "delete" || c.ID != "$x" {
		t.Fatalf("a live deletion: %+v", c)
	}
	if c := liveChange(matrix.Event{Type: "m.room.message", Content: map[string]any{"m.new_content": map[string]any{"body": "новое"},
		"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": "$y"}}}, "!r"); c == nil || c.Change != "edit" || c.Body != "новое" {
		t.Fatalf("a live edit: %+v", c)
	}
	if who, text := replyFallback("> <@ann:t.lan> первая\n> вторая\n\nответ"); who != "@ann:t.lan" || text != "первая\nвторая" {
		t.Fatalf("the reply fallback: %q %q", who, text)
	}
	// live unread counts: given at once
	lctx, cancel := context.WithCancel(ctx)
	var counts map[string]int
	_ = s.Live(lctx, bob, nil, func(c, _, _ map[string]int) { counts = c; cancel() })
	if counts[room] != 1 {
		t.Fatalf("bob's unread counts = %v", counts)
	}
	// someone outside the chat cannot read it
	if _, _, err := s.Messages(ctx, eve, room, ""); err == nil {
		t.Fatal("eve read a private chat")
	}
	if _, err := s.Send(ctx, eve, room, "влезть", ""); err == nil {
		t.Fatal("eve wrote into a private chat")
	}

	// a group chat: ann makes it with bob and eve (ghost has no messenger, ann herself is not invited), they are in
	// it at once; ann owns it
	mx := func(id string) string {
		var m string
		must(pool.QueryRow(ctx, `SELECT mxid FROM matrix_users WHERE user_id = $1`, id).Scan(&m))
		return m
	}
	if _, err := s.CreateGroup(ctx, ann, "Пусто", []string{ghost, ann}, false); err == nil {
		t.Fatal("a group chat without anybody else")
	}
	grp, err := s.CreateGroup(ctx, ann, " Запуск ", []string{bob, eve, ghost, ann}, false)
	must(err)
	if !fake.rooms[grp][mx(bob)] || !fake.rooms[grp][mx(eve)] || fake.names[grp] != "Запуск" || len(fake.invited[grp]) != 2 {
		t.Fatalf("the group chat: %v %q %v", fake.rooms[grp], fake.names[grp], fake.invited[grp])
	}
	g, err := s.GroupInfo(ctx, bob, grp)
	must(err)
	if g == nil || g.Owner || len(g.Members) != 3 || !g.Members[0].Owner || g.Members[0].ID != ann {
		t.Fatalf("bob's view of the group: %+v", g)
	}
	if g, err := s.GroupInfo(ctx, ann, room); err != nil || g != nil {
		t.Fatalf("a private chat taken for a group: %+v %v", g, err)
	}
	if rs, _ := s.Rooms(ctx, eve); !hasRoom(rs, grp, "group", "Запуск") {
		t.Fatalf("eve's chats: %+v", rs)
	}
	// only the owner renames, changes the picture and removes people
	if err := s.RenameGroup(ctx, bob, grp, "Моё"); err == nil {
		t.Fatal("bob renamed ann's chat")
	}
	if err := s.RemoveFromGroup(ctx, bob, grp, eve); err == nil || !fake.rooms[grp][mx(eve)] {
		t.Fatal("bob removed eve")
	}
	if err := s.SetGroupPhoto(ctx, bob, grp, "a.png", "image/png", strings.NewReader("PNG"), 3); err == nil {
		t.Fatal("bob changed the picture")
	}
	if err := s.SetGroupPhoto(ctx, ann, grp, "a.svg", "image/svg+xml", strings.NewReader("<svg/>"), 6); err == nil {
		t.Fatal("an SVG for a picture")
	}
	must(s.RenameGroup(ctx, ann, grp, "Запуск-2"))
	// the description: the owner's, of 1000 letters at most; everybody sees it
	if err := s.SetGroupTopic(ctx, bob, grp, "Моё"); err == nil {
		t.Fatal("bob described ann's chat")
	}
	if err := s.SetGroupTopic(ctx, ann, grp, strings.Repeat("я", 1001)); err == nil {
		t.Fatal("a description of 1001 letters")
	}
	must(s.SetGroupTopic(ctx, ann, grp, "  Запуск продукта: сроки и задачи "))
	if g, err := s.GroupInfo(ctx, bob, grp); err != nil || g.Topic != "Запуск продукта: сроки и задачи" {
		t.Fatalf("the description bob sees: %+v %v", g, err)
	}
	// «Прочитать всё»: eve's marker goes to the newest event of each chat she has
	delete(fake.read, mx(eve)+" "+grp)
	must(s.MarkAllRead(ctx, eve))
	if fake.read[mx(eve)+" "+grp] == "" {
		t.Fatal("eve's group chat is not read")
	}
	must(s.SetGroupPhoto(ctx, ann, grp, "a.png", "image/png", strings.NewReader("PNG"), 3))
	if res, err := s.RoomPhoto(ctx, eve, grp); err != nil {
		t.Fatal(err)
	} else if b, _ := io.ReadAll(res.Body); string(b) != "PNG" {
		t.Fatalf("the picture eve gets: %q", b)
	}
	if rs, _ := s.Rooms(ctx, eve); !hasRoom(rs, grp, "group", "Запуск-2") || !rs[indexRoom(rs, grp)].Photo {
		t.Fatalf("eve's chats after the change: %+v", rs)
	}
	must(s.RemoveFromGroup(ctx, ann, grp, eve))
	if fake.rooms[grp][mx(eve)] {
		t.Fatal("eve is still in the chat")
	}
	if _, err := s.RoomPhoto(ctx, eve, grp); err == nil {
		t.Fatal("eve got the picture of a chat she has left")
	}
	if err := s.AddToGroup(ctx, eve, grp, []string{eve}); err == nil {
		t.Fatal("eve came back by herself")
	}
	must(s.AddToGroup(ctx, bob, grp, []string{eve, ann})) // anyone in it adds people; ann is there already
	if !fake.rooms[grp][mx(eve)] {
		t.Fatal("bob could not bring eve back")
	}
	// the chat tells what happened to it
	gm, _, err := s.Messages(ctx, bob, grp, "")
	must(err)
	var lines []string
	for _, m := range gm {
		if m.Event != "" {
			lines = append(lines, m.SenderName+" "+m.Event+" "+m.Target+m.Body)
		}
	}
	if strings.Join(lines, "; ") != "ann renamed Запуск-2; ann photo ; ann removed eve; bob added eve" {
		t.Fatalf("the lines of the chat: %q", lines)
	}
	// the owner leaves: the chat passes to the first of the others
	must(s.LeaveGroup(ctx, ann, grp))
	if fake.rooms[grp][mx(ann)] {
		t.Fatal("ann is still in the chat")
	}
	if g, err := s.GroupInfo(ctx, bob, grp); err != nil || g == nil || !g.Owner || len(g.Members) != 2 {
		t.Fatalf("bob after ann has left: %+v %v", g, err)
	}
	if err := s.LeaveGroup(ctx, ann, grp); err == nil {
		t.Fatal("ann left a chat she is not in")
	}

	// pins: anyone in the chat pins, the latest pin first; the messages know they are pinned
	plan, err := s.Send(ctx, bob, grp, "План запуска: пятница", "")
	must(err)
	later, err := s.Send(ctx, eve, grp, "Чек-лист готов", "")
	must(err)
	must(s.Pin(ctx, bob, grp, plan.ID, true))
	must(s.Pin(ctx, eve, grp, later.ID, true))
	must(s.Pin(ctx, eve, grp, plan.ID, true)) // again: it moves to the end, once
	pins, err := s.Pins(ctx, bob, grp)
	must(err)
	if len(pins) != 2 || pins[0].ID != plan.ID || pins[1].ID != later.ID || !pins[0].Pinned || pins[0].Body != "План запуска: пятница" {
		t.Fatalf("the pins: %+v", pins)
	}
	gm, _, err = s.Messages(ctx, eve, grp, "")
	must(err)
	for _, m := range gm {
		if m.Pinned != (m.ID == plan.ID || m.ID == later.ID) {
			t.Fatalf("pinned flag of %s: %v", m.Body, m.Pinned)
		}
	}
	must(s.Pin(ctx, bob, grp, later.ID, false))
	if pins, _ := s.Pins(ctx, eve, grp); len(pins) != 1 || pins[0].ID != plan.ID {
		t.Fatalf("the pins after unpinning: %+v", pins)
	}
	if err := s.Pin(ctx, ann, grp, plan.ID, false); err == nil {
		t.Fatal("ann took a pin back in a chat she has left")
	}
	// the search: in the chat and in all the person's chats, only where they are
	if hits, err := s.Search(ctx, eve, "запуска", grp); err != nil || len(hits) != 1 || hits[0].ID != plan.ID || hits[0].RoomID != grp {
		t.Fatalf("the search in the chat: %+v %v", hits, err)
	}
	if hits, _ := s.Search(ctx, bob, "ГОТОВ", ""); len(hits) != 1 || hits[0].ID != later.ID {
		t.Fatalf("the search in all chats: %+v", hits)
	}
	if hits, _ := s.Search(ctx, ann, "запуска", ""); len(hits) != 0 {
		t.Fatalf("ann found messages of a chat she has left: %+v", hits)
	}
	if hits, _ := s.Search(ctx, bob, " з ", ""); hits != nil {
		t.Fatal("a search of one letter")
	}
	// the page around a message: it and its neighbours, oldest first
	around, _, err := s.Around(ctx, eve, grp, plan.ID)
	must(err)
	if i := indexMsg(around, plan.ID); i < 0 || i+1 >= len(around) || around[i+1].ID != later.ID || !around[i].Pinned {
		t.Fatalf("the page around the plan: %+v", around)
	}
	if _, _, err := s.Around(ctx, ann, grp, plan.ID); err == nil {
		t.Fatal("ann read around a message of a chat she has left")
	}

	// read receipts: eve has read the chat — bob's messages so far are read by her, not the one he sends after
	must(s.MarkRoomRead(ctx, eve, grp))
	fresh, err := s.Send(ctx, bob, grp, "Ещё одно", "")
	must(err)
	gm, _, err = s.Messages(ctx, bob, grp, "")
	must(err)
	for _, m := range gm {
		switch m.ID {
		case plan.ID:
			if strings.Join(m.ReadBy, ",") != "eve" {
				t.Fatalf("who has read the plan: %v", m.ReadBy)
			}
		case fresh.ID:
			if len(m.ReadBy) != 0 {
				t.Fatalf("the fresh message read by %v", m.ReadBy)
			}
		case later.ID: // eve's own: her receipt is not shown to her as someone else's
			if len(m.ReadBy) != 0 {
				t.Fatalf("eve's message read by %v", m.ReadBy)
			}
		}
	}
	// typing: told as the employee, only in a chat they are in
	must(s.Typing(ctx, bob, grp, true))
	if !fake.typing[grp+" "+mx(bob)] {
		t.Fatal("bob's typing not told")
	}
	must(s.Typing(ctx, bob, grp, false))
	if fake.typing[grp+" "+mx(bob)] {
		t.Fatal("bob still typing")
	}
	if err := s.Typing(ctx, ann, grp, true); err == nil {
		t.Fatal("ann typing in a chat she has left")
	}
	// live: someone else's receipt and typing come as changes, one's own do not
	live := s.liveEphemeral(ctx, mx(bob), grp, []matrix.Event{
		{Type: "m.receipt", Content: map[string]any{plan.ID: map[string]any{"m.read": map[string]any{mx(eve): map[string]any{"ts": 7.0},
			mx(bob): map[string]any{"ts": 8.0}}}, "$t": map[string]any{"m.read": map[string]any{mx(eve): map[string]any{"ts": 9.0, "thread_id": "$root"}}}}},
		{Type: "m.typing", Content: map[string]any{"user_ids": []any{mx(eve), mx(bob)}}},
		{Type: "m.typing", Content: map[string]any{"user_ids": []any{}}}})
	if len(live) != 3 || live[0].Change != "read" || live[0].ID != plan.ID || live[0].Body != "eve" || live[0].At.UnixMilli() != 7 ||
		live[1].Change != "typing" || live[1].Body != "eve" || live[2].Change != "typing" || live[2].Body != "" {
		t.Fatalf("the live receipts and typing: %+v", live)
	}

	// the chat list's own marks: pinned at the top, muted, marked unread until opened — each person's own
	must(s.RoomMark(ctx, bob, grp, "favorite", true))
	must(s.RoomMark(ctx, bob, grp, "mute", true))
	must(s.RoomMark(ctx, bob, grp, "unread", true))
	rs, err := s.Rooms(ctx, bob)
	must(err)
	if rs[0].ID != grp || !rs[0].Favorite || !rs[0].Muted || !rs[0].Marked {
		t.Fatalf("bob's list: %+v", rs[0])
	}
	if rs, _ := s.Rooms(ctx, eve); rs[indexRoom(rs, grp)].Favorite || rs[indexRoom(rs, grp)].Muted || rs[indexRoom(rs, grp)].Marked {
		t.Fatal("bob's marks reached eve")
	}
	must(s.MarkRoomRead(ctx, bob, grp))
	must(s.RoomMark(ctx, bob, grp, "mute", false))
	must(s.RoomMark(ctx, bob, grp, "mute", false)) // once more: nothing to take off, no error
	if rs, _ := s.Rooms(ctx, bob); rs[0].ID != grp || rs[0].Marked || rs[0].Muted || !rs[0].Favorite {
		t.Fatalf("bob's list after reading and unmuting: %+v", rs[0])
	}
	must(s.RoomMark(ctx, bob, grp, "favorite", false))
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, grp)].Favorite {
		t.Fatal("still pinned")
	}
	if err := s.RoomMark(ctx, ann, grp, "favorite", true); err == nil {
		t.Fatal("ann marked a chat she is not in")
	}
	if err := s.RoomMark(ctx, bob, grp, "nonsense", true); err == nil {
		t.Fatal("an unknown mark")
	}
	// «Скрыть чат»: out of bob's list (not eve's) until a newer message
	must(s.RoomMark(ctx, bob, grp, "hidden", true))
	if rs, _ := s.Rooms(ctx, bob); !rs[indexRoom(rs, grp)].Hidden {
		t.Fatal("the chat not hidden")
	}
	if rs, _ := s.Rooms(ctx, eve); rs[indexRoom(rs, grp)].Hidden {
		t.Fatal("hidden for eve too")
	}
	_, err = s.Send(ctx, eve, grp, "снова здесь", "")
	must(err)
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, grp)].Hidden {
		t.Fatal("a new message did not bring the chat back")
	}

	// a draft: the person's own, in the chat list; a blank one is none
	must(s.SetDraft(ctx, bob, grp, "", "допишу позже"))
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, grp)].Draft != "допишу позже" {
		t.Fatal("no draft")
	}
	if rs, _ := s.Rooms(ctx, eve); rs[indexRoom(rs, grp)].Draft != "" {
		t.Fatal("bob's draft shown to eve")
	}
	// a thread's draft is its own: the chat's one stays, and the thread's goes when it is blank
	must(s.SetDraft(ctx, bob, grp, "$root", "в тред"))
	if td, _ := s.ThreadDraft(ctx, bob, grp, "$root"); td != "в тред" {
		t.Fatalf("thread draft %q", td)
	}
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, grp)].Draft != "допишу позже" {
		t.Fatal("the thread's draft took the chat's one")
	}
	if td, _ := s.ThreadDraft(ctx, eve, grp, "$root"); td != "" {
		t.Fatal("bob's thread draft shown to eve")
	}
	must(s.SetDraft(ctx, bob, grp, "", "  \n"))
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, grp)].Draft != "" {
		t.Fatal("a blank draft kept")
	}
	if td, _ := s.ThreadDraft(ctx, bob, grp, "$root"); td != "в тред" {
		t.Fatal("the chat's blank draft took the thread's one")
	}
	must(s.SetDraft(ctx, bob, grp, "$root", ""))
	if td, _ := s.ThreadDraft(ctx, bob, grp, "$root"); td != "" {
		t.Fatal("a blank thread draft kept")
	}

	// автоудаление: in a private chat either of the two, in a group chat its owner; the messages sent after it was
	// turned on go when their time is up, the chat says who turned it on or off
	if ttl, can, err := s.AutoDelete(ctx, bob, room); err != nil || ttl != 0 || !can {
		t.Fatal("auto-delete of a private chat", ttl, can, err)
	}
	if err := s.SetAutoDelete(ctx, bob, room, 5); err == nil {
		t.Fatal("a lifetime not offered")
	}
	must(s.SetAutoDelete(ctx, bob, room, 86400))
	if ttl, _, _ := s.AutoDelete(ctx, ann, room); ttl != 86400 {
		t.Fatal("auto-delete not kept", ttl)
	}
	if msgs, _, _ := s.Messages(ctx, ann, room, ""); len(msgs) == 0 || msgs[len(msgs)-1].Event != "autodelete" || msgs[len(msgs)-1].Body != "86400" {
		t.Fatal("no line about auto-delete")
	}
	owner, other := bob, eve
	if g, _ := s.GroupInfo(ctx, eve, grp); g.Owner {
		owner, other = eve, bob
	}
	if _, can, _ := s.AutoDelete(ctx, other, grp); can {
		t.Fatal("not the owner may change auto-delete")
	}
	if err := s.SetAutoDelete(ctx, other, grp, 3600); err == nil {
		t.Fatal("not the owner turned auto-delete on")
	}
	if _, can, _ := s.AutoDelete(ctx, owner, grp); !can {
		t.Fatal("the owner may not change auto-delete")
	}
	var own string
	must(pool.QueryRow(ctx, `SELECT room_id FROM matrix_rooms LIMIT 1`).Scan(&own))
	if err := s.SetAutoDelete(ctx, ann, own, 3600); err == nil {
		t.Fatal("auto-delete in go24's own chat")
	}
	gone := len(fake.redacted)
	must(s.expire(ctx, time.Now()))
	if len(fake.redacted) != gone {
		t.Fatal("deleted before their time", fake.redacted[gone:])
	}
	// the fake's messages are of 1970: turned on then, a day later they go — the messages, not the lines about the chat
	if _, err := pool.Exec(ctx, `UPDATE im_autodelete SET since = to_timestamp(0)`); err != nil {
		t.Fatal(err)
	}
	must(s.expire(ctx, time.UnixMilli(86400_000+5000)))
	var want []string
	for _, e := range fake.sent {
		if e.Room == room && e.Content["_event"] == nil {
			want = append(want, e.ID)
		}
	}
	if got := fake.redacted[gone:]; len(want) == 0 || len(got) != len(want) {
		t.Fatalf("auto-delete: deleted %v, want %v", got, want)
	}
	gone = len(fake.redacted)
	must(s.expire(ctx, time.UnixMilli(86400_000+6000)))
	if len(fake.redacted) != gone {
		t.Fatal("deleted twice", fake.redacted[gone:])
	}
	must(s.SetAutoDelete(ctx, ann, room, 0))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM im_autodelete`).Scan(&n))
	if msgs, _, _ := s.Messages(ctx, ann, room, ""); n != 0 || msgs[len(msgs)-1].Event != "autodelete" || msgs[len(msgs)-1].Body != "0" {
		t.Fatal("auto-delete not off", n)
	}

	// «Избранное»: each person's own, the latest kept first; the messages know they are kept or to be reminded of
	must(s.Save(ctx, bob, grp, plan.ID, true))
	must(s.Save(ctx, bob, grp, later.ID, true))
	must(s.Save(ctx, eve, grp, fresh.ID, true))
	if _, err := pool.Exec(ctx, `UPDATE im_saved SET created_at = now() - interval '1 hour' WHERE event_id = $1`, plan.ID); err != nil {
		t.Fatal(err)
	}
	kept, err := s.Saved(ctx, bob)
	must(err)
	if len(kept) != 2 || kept[0].ID != later.ID || kept[1].ID != plan.ID || !kept[0].Saved || kept[1].RoomID != grp {
		t.Fatalf("bob's saved: %+v", kept)
	}
	if err := s.Save(ctx, ann, grp, plan.ID, true); err == nil {
		t.Fatal("ann kept a message of a chat she has left")
	}
	must(s.Save(ctx, bob, grp, later.ID, false))
	must(s.Remind(ctx, bob, grp, plan.ID, time.Now().Add(-time.Minute))) // due
	must(s.Remind(ctx, bob, grp, fresh.ID, time.Now().Add(time.Hour)))
	gm, _, err = s.Messages(ctx, bob, grp, "")
	must(err)
	for _, m := range gm {
		if m.Saved != (m.ID == plan.ID) || m.RemindAt.IsZero() != (m.ID != plan.ID && m.ID != fresh.ID) {
			t.Fatalf("bob's marks of %s: %v %v", m.Body, m.Saved, m.RemindAt)
		}
	}
	// the due reminder becomes a notification with a link to the message, once; the later one waits
	must(s.SendReminders(ctx, func(m Message) string { return Excerpt(m, 5) }))
	must(s.SendReminders(ctx, func(m Message) string { return Excerpt(m, 5) }))
	var text, link string
	var due int
	must(pool.QueryRow(ctx, `SELECT count(*), max(body), max(url) FROM notifications WHERE user_id = $1 AND kind = 'im_remind'`, bob).Scan(&due, &text, &link))
	if due != 1 || text != "bob: План…" || link != "/im?room="+url.QueryEscape(grp)+"&at="+url.QueryEscape(plan.ID) {
		t.Fatalf("the reminders: %d %q %q", due, text, link)
	}
	var rows int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM im_saved WHERE user_id = $1`, bob).Scan(&rows))
	if rows != 2 { // the plan is still kept, the fresh one waits; the later one (neither) is forgotten
		t.Fatalf("bob's rows: %d", rows)
	}
	must(s.Remind(ctx, bob, grp, fresh.ID, time.Time{}))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM im_saved WHERE user_id = $1`, bob).Scan(&rows))
	if rows != 1 {
		t.Fatalf("bob's rows after «Не напоминать»: %d", rows)
	}

	// polls: as Element makes them; 2–10 answers; one answer each, the latest vote counts, a vote taken back
	if _, err := s.CreatePoll(ctx, bob, grp, "", "Когда?", []string{"Пятница", " "}, false, false); err == nil {
		t.Fatal("a poll of one answer")
	}
	poll, err := s.CreatePoll(ctx, bob, grp, "", " Когда созвон? ", []string{"Пятница", "", "Понедельник", "Вторник"}, false, false)
	must(err)
	if p := poll.Poll; p == nil || p.Question != "Когда созвон?" || len(p.Answers) != 3 || p.Answers[1].Text != "Понедельник" || !p.Mine || poll.Body != "📊 Когда созвон?" {
		t.Fatalf("the poll: %+v %+v", poll, poll.Poll)
	}
	if sent := fake.sent[indexOf(fake.sent, poll.ID)].Content; sent["_event"] != pollStart || !strings.HasPrefix(sent["body"].(string), "Когда созвон?\n1. Пятница") {
		t.Fatalf("the poll as sent: %+v", sent)
	}
	_, err = s.Vote(ctx, eve, grp, poll.ID, []string{"a1"})
	must(err)
	if _, err := s.Vote(ctx, eve, grp, poll.ID, []string{"a1", "a2"}); err == nil {
		t.Fatal("two answers to a poll of one")
	}
	if _, err := s.Vote(ctx, eve, grp, poll.ID, []string{"a9"}); err == nil {
		t.Fatal("an answer the poll has not")
	}
	v, err := s.Vote(ctx, bob, grp, poll.ID, []string{"a1"})
	must(err)
	if p := v.Poll; p.Votes != 2 || p.Answers[0].Count != 2 || p.Answers[0].Pct != 100 || p.Answers[0].Names != "eve, bob" || !p.Answers[0].Mine || !p.Voted || !p.Shown() {
		t.Fatalf("the votes: %+v", p)
	}
	v, err = s.Vote(ctx, eve, grp, poll.ID, []string{"a2"}) // she changes her mind
	must(err)
	if p := v.Poll; p.Votes != 2 || p.Answers[0].Count != 1 || p.Answers[1].Count != 1 || p.Answers[1].Names != "eve" || p.Answers[1].Pct != 50 || !p.Answers[1].Mine || p.Answers[0].Mine {
		t.Fatalf("the votes after eve's change: %+v", p)
	}
	if _, err := s.Vote(ctx, ann, grp, poll.ID, []string{"a1"}); err == nil {
		t.Fatal("ann voted in a chat she has left")
	}
	// the page knows the votes; the live stream redraws the poll on a vote
	gm, _, err = s.Messages(ctx, eve, grp, "")
	must(err)
	if i := indexMsg(gm, poll.ID); i < 0 || gm[i].Poll == nil || gm[i].Poll.Votes != 2 || gm[i].Poll.Mine || !gm[i].Poll.Answers[1].Mine {
		t.Fatalf("the poll on eve's page: %+v", gm)
	}
	vote := fake.sent[len(fake.sent)-1]
	_, btok, err := s.token(ctx, bob)
	must(err)
	live = s.livePolls(ctx, mx(bob), btok, grp, []matrix.Event{{Type: pollResponse, Sender: vote.Sender, Content: vote.Content}})
	if len(live) != 1 || live[0].Change != "poll" || live[0].ID != poll.ID || live[0].Poll.Votes != 2 {
		t.Fatalf("the live poll: %+v", live)
	}
	// the author ends it: the results are final, no more votes; nobody else ends it
	if _, err := s.EndPoll(ctx, eve, grp, poll.ID, "Опрос завершён"); err == nil {
		t.Fatal("eve ended bob's poll")
	}
	v, err = s.EndPoll(ctx, bob, grp, poll.ID, "Опрос завершён")
	must(err)
	if !v.Poll.Ended || v.Poll.Votes != 2 {
		t.Fatalf("the ended poll: %+v", v.Poll)
	}
	if _, err := s.Vote(ctx, eve, grp, poll.ID, []string{}); err == nil {
		t.Fatal("a vote after the end")
	}
	// several answers, results when it is over; a vote taken back
	multi, err := s.CreatePoll(ctx, eve, grp, "", "Что взять?", []string{"Чай", "Кофе", "Воду"}, true, true)
	must(err)
	v, err = s.Vote(ctx, bob, grp, multi.ID, []string{"a1", "a3", "a1"})
	must(err)
	if p := v.Poll; !p.Multi || !p.Hidden || p.Shown() || p.Votes != 1 || p.Answers[0].Count != 1 || p.Answers[2].Count != 1 {
		t.Fatalf("the poll of several answers: %+v", p)
	}
	v, err = s.Vote(ctx, bob, grp, multi.ID, nil)
	must(err)
	if p := v.Poll; p.Votes != 0 || p.Voted || p.Answers[0].Count != 0 {
		t.Fatalf("bob's vote taken back: %+v", p)
	}

	// a channel: its owner and admins write and pin, the others read, react and vote
	ch, err := s.CreateGroup(ctx, bob, "Новости", []string{eve}, true)
	must(err)
	if g, err := s.GroupInfo(ctx, bob, ch); err != nil || !g.Channel || !g.Owner || !g.Writer {
		t.Fatalf("bob's channel: %+v %v", g, err)
	}
	if g, _ := s.GroupInfo(ctx, eve, ch); !g.Channel || g.Writer || g.Owner {
		t.Fatalf("the channel for eve: %+v", g)
	}
	if rs, _ := s.Rooms(ctx, eve); !hasRoom(rs, ch, "channel", "Новости") {
		t.Fatalf("eve's list: %+v", rs)
	}
	news, err := s.Send(ctx, bob, ch, "Запуск в пятницу", "")
	must(err)
	if _, err := s.Send(ctx, eve, ch, "А можно?", ""); err == nil {
		t.Fatal("eve wrote in the channel")
	}
	if _, err := s.Send(ctx, eve, ch, "в треде", news.ID); err == nil {
		t.Fatal("eve wrote in a thread of the channel")
	}
	must(s.React(ctx, eve, ch, news.ID, "👍"))
	// the REST way: a reaction put stays put, one taken back stays away
	sent, red := len(fake.sent), len(fake.redacted)
	must(s.SetReaction(ctx, eve, ch, news.ID, "👍", true))
	if len(fake.sent) != sent {
		t.Fatal("a reaction put twice")
	}
	must(s.SetReaction(ctx, eve, ch, news.ID, "👍", false))
	must(s.SetReaction(ctx, eve, ch, news.ID, "👍", false))
	if len(fake.redacted) != red+1 {
		t.Fatalf("the reaction taken back: %d", len(fake.redacted)-red)
	}
	must(s.SetReaction(ctx, eve, ch, news.ID, "👍", true))
	if err := s.Pin(ctx, eve, ch, news.ID, true); err == nil {
		t.Fatal("eve pinned in the channel")
	}
	cpoll, err := s.CreatePoll(ctx, bob, ch, "", "Идём?", []string{"Да", "Нет"}, false, false)
	must(err)
	_, err = s.Vote(ctx, eve, ch, cpoll.ID, []string{"a1"})
	must(err)
	// admins: named by the owner only; an admin writes
	if err := s.SetAdmin(ctx, eve, ch, eve, true); err == nil {
		t.Fatal("eve made herself an admin")
	}
	must(s.SetAdmin(ctx, bob, ch, eve, true))
	if g, _ := s.GroupInfo(ctx, eve, ch); !g.Writer || g.Owner || len(g.Members) != 2 || g.Members[1].Name != "eve" || !g.Members[1].Admin {
		t.Fatalf("eve an admin: %+v", g)
	}
	_, err = s.Send(ctx, eve, ch, "Теперь могу", "")
	must(err)
	if err := s.SetAdmin(ctx, bob, ch, bob, false); err == nil {
		t.Fatal("the owner made himself no admin")
	}
	must(s.SetAdmin(ctx, bob, ch, eve, false))
	if _, err := s.Send(ctx, eve, ch, "Ещё", ""); err == nil {
		t.Fatal("eve wrote after she is no admin")
	}
	// a group chat again: everyone writes; only the owner changes it, admins are only of channels
	if err := s.SetChannel(ctx, eve, ch, false); err == nil {
		t.Fatal("eve changed bob's channel")
	}
	must(s.SetChannel(ctx, bob, ch, false))
	_, err = s.Send(ctx, eve, ch, "Ура", "")
	must(err)
	if rs, _ := s.Rooms(ctx, eve); !hasRoom(rs, ch, "group", "Новости") {
		t.Fatalf("eve's list after: %+v", rs)
	}
	// admins and rights of a group chat: by default anyone adds people, the owner and admins
	// change the chat and remove people, anyone writes
	if g, _ := s.GroupInfo(ctx, eve, ch); g.Rights != (Rights{RightAll, RightAdmins, RightAll}) || !g.CanAdd || g.CanEdit || !g.Writer {
		t.Fatalf("the rights for eve: %+v", g)
	}
	if err := s.RenameGroup(ctx, eve, ch, "Моё"); err == nil {
		t.Fatal("eve renamed the chat")
	}
	must(s.SetAdmin(ctx, bob, ch, eve, true))
	if g, _ := s.GroupInfo(ctx, eve, ch); !g.CanEdit || !g.Members[1].Admin || g.Members[0].Removable {
		t.Fatalf("eve an admin of the group chat: %+v", g)
	}
	must(s.SetGroupTopic(ctx, eve, ch, "Новости отдела"))
	if err := s.RemoveFromGroup(ctx, eve, ch, bob); err == nil {
		t.Fatal("an admin removed the owner")
	}
	if err := s.SetRights(ctx, eve, ch, Rights{RightOwner, RightOwner, RightOwner}); err == nil {
		t.Fatal("an admin changed the rights")
	}
	if err := s.SetRights(ctx, bob, ch, Rights{"x", RightOwner, RightAll}); err == nil {
		t.Fatal("a right of no kind")
	}
	// only the owner adds people and changes the chat; admins write, the others read
	must(s.SetRights(ctx, bob, ch, Rights{RightOwner, RightOwner, RightAdmins}))
	if g, _ := s.GroupInfo(ctx, eve, ch); g.Rights != (Rights{RightOwner, RightOwner, RightAdmins}) || g.CanAdd || g.CanEdit || !g.Writer || !g.Channel {
		t.Fatalf("the rights after: %+v", g)
	}
	if err := s.AddToGroup(ctx, eve, ch, []string{ann}); err == nil {
		t.Fatal("eve added people against the rights")
	}
	if err := s.SetGroupTopic(ctx, eve, ch, ""); err == nil {
		t.Fatal("eve changed the chat against the rights")
	}
	must(s.SetAdmin(ctx, bob, ch, eve, false))
	if _, err := s.Send(ctx, eve, ch, "Можно?", ""); err == nil {
		t.Fatal("eve wrote though only admins write")
	}
	must(s.SetRights(ctx, bob, ch, Rights{RightAll, RightAdmins, RightAll}))
	// the whole history to download: oldest first, threads' replies too, the chat's name; not for one gone from it
	hist, hname, err := s.History(ctx, eve, ch, 1000)
	must(err)
	last := ""
	for _, m := range hist {
		if !m.Notice {
			last = m.Body
		}
	}
	if hname != "Новости" || len(hist) < 4 || hist[0].Body != "Запуск в пятницу" || last != "Ура" || hist[len(hist)-1].Event != "unadmin" {
		t.Fatalf("the history: %q %+v", hname, hist)
	}
	if _, _, err := s.History(ctx, ann, grp, 1000); err == nil {
		t.Fatal("ann downloaded a chat she has left")
	}
	must(s.SetChannel(ctx, bob, grp, true)) // a group chat made a channel
	if g, _ := s.GroupInfo(ctx, eve, grp); !g.Channel || g.Writer {
		t.Fatalf("the group chat made a channel: %+v", g)
	}
	// the owner hands the chat over «Сделать владельцем»: bob stays as one of its people
	if err := s.SetOwner(ctx, eve, ch, eve); err == nil {
		t.Fatal("eve took the chat herself")
	}
	if err := s.SetOwner(ctx, bob, ch, ann); err == nil {
		t.Fatal("the chat handed to one not in it")
	}
	must(s.SetOwner(ctx, bob, ch, eve))
	if g, _ := s.GroupInfo(ctx, bob, ch); g.Owner || g.Members[0].Name != "eve" || !g.Members[0].Owner || g.Members[1].Admin {
		t.Fatalf("bob after he handed the chat: %+v", g)
	}
	if g, _ := s.GroupInfo(ctx, eve, ch); !g.Owner || !g.Members[1].Removable {
		t.Fatalf("eve owns the chat: %+v", g)
	}
	if err := s.SetAdmin(ctx, bob, ch, eve, false); err == nil {
		t.Fatal("bob still names admins")
	}
	must(s.SetAdmin(ctx, eve, ch, bob, true))
	// the chat tells who owns it now and who its admins are
	cm, _, err := s.Messages(ctx, eve, ch, "")
	must(err)
	var levels []string
	for _, m := range cm {
		if m.Event == "owner" || m.Event == "admin" || m.Event == "unadmin" {
			levels = append(levels, m.SenderName+" "+m.Event+" "+m.Target)
		}
	}
	if strings.Join(levels, "; ") != "bob admin eve; bob unadmin eve; bob admin eve; bob unadmin eve; bob owner eve; eve admin bob" {
		t.Fatalf("the lines of the levels: %q", levels)
	}
}

func indexMsg(ms []Message, id string) int {
	for i, m := range ms {
		if m.ID == id {
			return i
		}
	}
	return -1
}

func hasRoom(rs []Room, id, kind, name string) bool {
	i := indexRoom(rs, id)
	return i >= 0 && rs[i].Kind == kind && rs[i].Name == name
}

func indexRoom(rs []Room, id string) int {
	for i, r := range rs {
		if r.ID == id {
			return i
		}
	}
	return -1
}

func TestMatrixFirst(t *testing.T) {
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
	var ann, old string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-ann', 'ann', 'ann') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('portal:7', 'Старый') RETURNING id`).Scan(&old))
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ts := tasks.NewStore(pool)
	fs := feed.NewStore(pool, ts)
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: ts, Feed: fs,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co", FeedName: "Лента"})
	_, err = ts.CreateProject(ctx, ann, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, ann, "GO")
	must(s.Reconcile(ctx))
	key, err := ts.CreateTask(ctx, ann, p, tasks.NewTask{Title: "История"})
	must(err)
	tid, _ := ts.TaskIDByKey(ctx, key)
	must(ts.SetParticipant(ctx, ann, tid, old, "auditor", true))
	// a comment from before the messenger (e.g. imported)
	_, err = pool.Exec(ctx, `INSERT INTO task_comments (task_id, author_id, body, created_at) VALUES ($1, $2, 'было раньше', '2026-02-06 11:53+03')`, tid, old)
	must(err)

	c, err := s.PostTaskComment(ctx, ann, tid, "новое")
	must(err)
	n := len(fake.sent)
	// thread header, the old comment moved over with its date, then the new one — in this order
	if n < 3 || !strings.HasPrefix(fake.sent[n-2].Content["body"].(string), "Старый: 🕓 06.02.2026") || fake.sent[n-1].Content["body"] != "новое" ||
		fake.sent[n-1].Sender != "@ann:t.lan" {
		t.Fatalf("sent: %+v", fake.sent[max(0, n-3):])
	}
	var mapped int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM matrix_events e JOIN task_comments c ON c.id = e.comment_id WHERE c.task_id = $1`, tid).Scan(&mapped))
	if mapped != 2 || c.Body != "новое" {
		t.Fatalf("indexed comments with events = %d", mapped)
	}
	before := len(fake.sent)
	_, err = s.PostTaskComment(ctx, ann, tid, "ещё")
	must(err)
	if len(fake.sent) != before+1 {
		t.Fatal("history moved twice")
	}

	// feed: published to Matrix, edited there first, deleted there first
	pid, err := fs.CreatePost(ctx, ann, "", "Тема", "Текст", false)
	must(err)
	must(s.PublishPost(ctx, pid))
	b, _ := fs.BridgeInfo(ctx, pid)
	if b.Event == "" || b.Room == "" {
		t.Fatal("post not stored in Matrix")
	}
	must(s.PublishEdit(ctx, pid, "Тема", "Новый текст"))
	if nc, _ := fake.sent[len(fake.sent)-1].Content["m.new_content"].(map[string]any); nc["body"] != "Тема\nНовый текст" {
		t.Fatalf("edit = %+v", fake.sent[len(fake.sent)-1])
	}
	must(s.PublishDelete(ctx, pid))
	if fake.redacted[len(fake.redacted)-1] != b.Event {
		t.Fatal("post not removed from Matrix")
	}
	// a project post goes to the project's room
	pp, err := fs.CreatePost(ctx, ann, p.ID, "", "проекту", false)
	must(err)
	must(s.PublishPost(ctx, pp))
	pb, _ := fs.BridgeInfo(ctx, pp)
	gid := p.ID
	proom, _ := s.roomOf(ctx, "project", &gid)
	if pb.Room != proom {
		t.Fatalf("project post room %q, want %q", pb.Room, proom)
	}

	// mentions: "@Name" goes out as a Matrix mention; a mention from Element comes back as "@Name" and notifies
	var bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-bob', 'Боб', 'bob') RETURNING id`).Scan(&bob))
	must(ts.SetMember(ctx, p, bob, "member"))
	must(s.Reconcile(ctx))
	_, err = s.PostTaskComment(ctx, ann, tid, "@Боб глянь")
	must(err)
	last := fake.sent[len(fake.sent)-1].Content
	if m, _ := last["m.mentions"].(map[string]any); fmt.Sprint(m["user_ids"]) != "[@bob:t.lan]" ||
		!strings.Contains(fmt.Sprint(last["formatted_body"]), `href="https://matrix.to/#/@bob:t.lan">Боб</a>`) {
		t.Fatalf("mention sent as %+v", last)
	}
	// Боб sees «@» at the project's chat (the person was mentioned) and the message marked as one to him
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, proom)].Mentions != 1 {
		t.Fatalf("mentions of the project chat: %+v", rs[indexRoom(rs, proom)])
	}
	if rs, _ := s.Rooms(ctx, ann); rs[indexRoom(rs, proom)].Mentions != 0 {
		t.Fatal("the author's own mention counted")
	}
	// the task's chat is a thread of the project's chat: new for Боб there, read when he opens the task's card
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, proom)].Threads == 0 {
		t.Fatalf("the task's thread for bob: %+v", rs[indexRoom(rs, proom)])
	}
	must(s.ReadTaskThread(ctx, bob, tid))
	if rs, _ := s.Rooms(ctx, bob); rs[indexRoom(rs, proom)].Threads != 0 {
		t.Fatalf("the task's card read its chat: %+v", rs[indexRoom(rs, proom)])
	}
	if !mentions(map[string]any{"m.mentions": map[string]any{"room": true}}, "@x:t.lan") || mentions(map[string]any{}, "@x:t.lan") {
		t.Fatal("mentions: @room or nothing")
	}
	var thread string
	must(pool.QueryRow(ctx, `SELECT matrix_thread FROM tasks WHERE id = $1`, tid).Scan(&thread))
	must(s.inbound(ctx, proom, matrix.Event{EventID: "$mention", Type: "m.room.message", Sender: "@bob:t.lan", Content: map[string]any{
		"msgtype": "m.text", "body": "ann, готово", "m.mentions": map[string]any{"user_ids": []any{"@ann:t.lan"}},
		"m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": thread}}}))
	var body, kind string
	must(pool.QueryRow(ctx, `SELECT body FROM task_comments WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, tid).Scan(&body))
	must(pool.QueryRow(ctx, `SELECT kind FROM notifications WHERE user_id = $1 ORDER BY id DESC LIMIT 1`, ann).Scan(&kind))
	if body != "@ann, готово" || kind != "mention" {
		t.Fatalf("inbound mention: body %q, notification %q", body, kind)
	}

	// notifications repeated in the messenger: the bot's private chat, once
	s.Notify = notify.NewStore(pool)
	s.NotifyRoom = "Уведомления go24"
	s.NotifyText = func(it notify.Item) string { return it.ActorName + " " + it.Kind + " " + it.TaskKey }
	s.AppURL = "https://go24"
	_, err = pool.Exec(ctx, `UPDATE users SET notify_matrix = true WHERE id = $1`, ann)
	must(err)
	sentBefore := len(fake.sent)
	must(s.PushPending(ctx))
	var pushed []sentEvent
	for _, e := range fake.sent[sentBefore:] {
		if e.Sender == "@go24:t.lan" && strings.Contains(fmt.Sprint(e.Content["body"]), "Боб mention "+key) {
			pushed = append(pushed, e)
		}
	}
	if len(pushed) != 1 || !strings.Contains(fmt.Sprint(pushed[0].Content["body"]), "https://go24/notifications/") {
		t.Fatalf("pushed = %+v", fake.sent[sentBefore:])
	}
	afterFirst := len(fake.sent)
	must(s.PushPending(ctx))
	for _, e := range fake.sent[afterFirst:] {
		if strings.Contains(fmt.Sprint(e.Content["body"]), "Боб mention") {
			t.Fatal("a notification was pushed twice")
		}
	}
	var nroom string
	ref := ann
	nroom, _ = s.roomOf(ctx, "notify", &ref)
	if nroom == "" || pushed[0].Room != nroom {
		t.Fatalf("notify room %q, pushed to %q", nroom, pushed[0].Room)
	}

	// status log: a line in the task's thread, ✅ on its header when done, removed when reopened
	s.HistoryText = func(actor, acting, field, old, next string, at time.Time) string { return actor + ": " + old + " → " + next }
	must(ts.ApplyAction(ctx, ann, tid, tasks.ActionComplete))
	before = len(fake.sent)
	must(s.LogStatuses(ctx))
	var line, react *sentEvent
	for i := range fake.sent[before:] {
		e := &fake.sent[before+i]
		if e.Content["msgtype"] == "m.notice" && strings.HasSuffix(fmt.Sprint(e.Content["body"]), "→ completed") {
			line = e
		}
		if rel, _ := e.Content["m.relates_to"].(map[string]any); rel["key"] == "✅" && rel["event_id"] == thread {
			react = e
		}
	}
	if line == nil || react == nil {
		t.Fatalf("status log: %+v", fake.sent[before:])
	}
	must(s.LogStatuses(ctx)) // nothing new
	must(ts.ApplyAction(ctx, ann, tid, tasks.ActionRenew))
	must(s.LogStatuses(ctx))
	if fake.redacted[len(fake.redacted)-1] != react.ID {
		t.Fatal("✅ not removed when the task was reopened")
	}

	// reactions on task chat messages: from go24 into Matrix (as the person), from Element into go24, and back
	var cid string
	must(pool.QueryRow(ctx, `SELECT c.id::text FROM task_comments c JOIN matrix_events e ON e.comment_id = c.id
		WHERE c.task_id = $1 AND c.body = '@Боб глянь'`, tid).Scan(&cid))
	must(s.ReactComment(ctx, ann, tid, cid, "🔥"))
	sentR := fake.sent[len(fake.sent)-1]
	if rel, _ := sentR.Content["m.relates_to"].(map[string]any); rel["key"] != "🔥" || sentR.Sender != "@ann:t.lan" {
		t.Fatalf("reaction sent as %+v", sentR)
	}
	must(s.inbound(ctx, proom, matrix.Event{EventID: "$r1", Type: "m.reaction", Sender: "@bob:t.lan", Content: map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": sentR.Content["m.relates_to"].(map[string]any)["event_id"], "key": "🔥"}}}))
	one := []tasks.Comment{{ID: cid}}
	must(ts.FillReactions(ctx, ann, one))
	if len(one[0].Reactions) != 1 || one[0].Reactions[0].Count != 2 || !one[0].Reactions[0].Mine {
		t.Fatalf("reactions = %+v", one[0].Reactions)
	}
	must(s.inbound(ctx, proom, matrix.Event{EventID: "$r2", Type: "m.room.redaction", Sender: "@bob:t.lan", Redacts: "$r1"}))
	must(s.ReactComment(ctx, ann, tid, cid, "🔥")) // ann takes hers back: redacted in Matrix
	if fake.redacted[len(fake.redacted)-1] != sentR.ID {
		t.Fatal("a reaction taken back in go24 stays in Matrix")
	}
	one = []tasks.Comment{{ID: cid}}
	must(ts.FillReactions(ctx, ann, one))
	if len(one[0].Reactions) != 0 {
		t.Fatalf("reactions left: %+v", one[0].Reactions)
	}
	// readdressed to everyone: the messages in the project's room go, the post and its comment (with its date) are
	// written anew in the feed's room, in the post's thread; their removal there deletes nothing in go24
	pc, err := fs.AddComment(ctx, ann, pp, "к проекту")
	must(err)
	must(s.PublishComment(ctx, pp, pc))
	cev := fake.sent[len(fake.sent)-1].ID
	_, _, err = fs.SetAudience(ctx, ann, pp, "", feed.Audience{})
	must(err)
	must(s.MovePost(ctx, pp))
	moved, _ := fs.BridgeInfo(ctx, pp)
	froom, _ := s.roomOf(ctx, "feed", nil)
	if moved.Room != froom || moved.Event == pb.Event || !slices.Contains(fake.redacted, pb.Event) || !slices.Contains(fake.redacted, cev) {
		t.Fatalf("moved post: room %q (want %q), event %q, redacted %v", moved.Room, froom, moved.Event, fake.redacted)
	}
	mc := fake.sent[len(fake.sent)-1]
	if rel, _ := mc.Content["m.relates_to"].(map[string]any); mc.Room != froom || rel["event_id"] != moved.Event ||
		!strings.HasPrefix(mc.Content["body"].(string), "🕓 ") || !strings.HasSuffix(mc.Content["body"].(string), "к проекту") {
		t.Fatalf("moved comment: %+v", mc)
	}
	if kind, _, _, _ := fs.ByEvent(ctx, pb.Event); kind != "" {
		t.Fatal("the old event still means the post")
	}
}

func indexOf(sent []sentEvent, id string) int {
	for i, e := range sent {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// TestCallLine: a call's line is a notice with go24.call; its end comes as an edit (bundled by the server, or live).
func TestCallLine(t *testing.T) {
	s := &Service{M: matrix.New("http://x", "t.lan", "bot")}
	ev := matrix.Event{EventID: "$c", Type: "m.room.message", Sender: "@ann:t.lan", TS: 1000,
		Content: map[string]any{"msgtype": "m.notice", "body": "ann начинает звонок", "go24.call": map[string]any{"state": "started", "video": true, "people": []any{"ann"}}}}
	m, ok := s.message(ev, "!r", "@bob:t.lan", nil)
	if !ok || m.Call == nil || m.Call.State != "started" || !m.Call.Video || m.Call.By() != "ann" || !m.Notice || m.Editable() {
		t.Fatalf("a call's line: %+v %+v", m, m.Call)
	}
	ev.Unsigned.Relations.Replace = &matrix.Event{Content: map[string]any{"m.new_content": map[string]any{"msgtype": "m.notice", "body": "Звонок завершён",
		"go24.call": map[string]any{"state": "ended", "secs": 754.0, "people": []any{"ann", "bob"}}}}}
	if m, _ := s.message(ev, "!r", "@bob:t.lan", nil); m.Call.State != "ended" || m.Call.Secs != 754 || m.Call.Mins() != 13 || len(m.Call.People) != 2 || m.Edited {
		t.Fatalf("the call over: %+v", m.Call)
	}
	live := liveChange(matrix.Event{Type: "m.room.message", Content: map[string]any{"m.new_content": map[string]any{"body": "x",
		"go24.call": map[string]any{"state": "missed", "people": []any{"ann"}}}, "m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": "$c"}}}, "!r")
	if live == nil || live.Call == nil || live.Call.State != "missed" || live.ID != "$c" {
		t.Fatalf("the live end of a call: %+v", live)
	}
	if callOf(map[string]any{"go24.call": map[string]any{}}) != nil || callOf(map[string]any{}) != nil {
		t.Fatal("a line without a state")
	}
}

// TestThreadUnread: replies in threads are counted apart from the chat — under their root («новых: N»), beside the
// chat and in «Треды» (unread first); opening the chat reads only its main timeline, opening a thread reads that
// thread, «Прочитать всё» reads everything.
func TestThreadUnread(t *testing.T) {
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
	newUser := func(username string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-' || $1, $1, $1) RETURNING id`, username).Scan(&id))
		return id
	}
	ann, bob := newUser("ann"), newUser("bob")
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))
	room, err := s.DirectChat(ctx, ann, bob)
	must(err)
	send := func(who, body, root string) string {
		t.Helper()
		m, err := s.Send(ctx, who, room, body, root)
		must(err)
		return m.ID
	}
	q := send(bob, "вопрос", "")
	send(bob, "ответ 1", q)
	send(bob, "ответ 2", q)
	mine := send(ann, "своё", "")
	send(ann, "дополнение", mine) // her own reply: nothing new for her
	threadsOf := func() int {
		t.Helper()
		rooms, err := s.Rooms(ctx, ann)
		must(err)
		for _, r := range rooms {
			if r.ID == room {
				return r.Threads
			}
		}
		t.Fatal("no chat")
		return 0
	}
	newOf := func(id string) (int, int) {
		t.Helper()
		msgs, _, err := s.Messages(ctx, ann, room, "")
		must(err)
		for _, m := range msgs {
			if m.ID == id {
				return m.Replies, m.NewReplies
			}
		}
		t.Fatalf("no message %s", id)
		return 0, 0
	}
	if n := threadsOf(); n != 2 {
		t.Fatalf("unread replies in threads = %d, want 2", n)
	}
	if n, k := newOf(q); n != 2 || k != 2 {
		t.Fatalf("the root: %d replies, %d new; want 2, 2", n, k)
	}
	if n, k := newOf(mine); n != 1 || k != 0 {
		t.Fatalf("her own thread: %d replies, %d new; want 1, 0", n, k)
	}
	// the chat opened: its main timeline is read (a receipt of the main thread, on a message outside the threads)
	must(s.MarkRoomRead(ctx, ann, room))
	if fake.read["@ann:t.lan "+room] != mine || threadsOf() != 2 {
		t.Fatalf("reading the chat: marker %q, threads %d", fake.read["@ann:t.lan "+room], threadsOf())
	}
	// «Треды»: the one with new replies first, though the other was answered later
	list, err := s.Threads(ctx, ann, room)
	must(err)
	if len(list) != 2 || list[0].Root.ID != q || list[0].Root.NewReplies != 2 || list[0].LastBy != "bob" || list[1].Root.ID != mine || list[1].Root.NewReplies != 0 {
		t.Fatalf("the threads: %+v", list)
	}
	// the counts live: the threads beside the chat's own
	lctx, cancel := context.WithCancel(ctx)
	var th map[string]int
	_ = s.Live(lctx, ann, nil, func(_, _, threads map[string]int) { th = threads; cancel() })
	if th[room] != 2 {
		t.Fatalf("live thread counts = %v", th)
	}
	// the thread opened: read up to its newest reply
	must(s.ThreadRead(ctx, ann, room, q))
	if n := threadsOf(); n != 0 {
		t.Fatalf("after reading the thread: %d", n)
	}
	if _, k := newOf(q); k != 0 {
		t.Fatalf("after reading the thread the root has %d new", k)
	}
	send(bob, "ответ 3", q)
	if n, k := newOf(q); n != 3 || k != 1 {
		t.Fatalf("a new reply: %d replies, %d new; want 3, 1", n, k)
	}
	// «Прочитать всё»: the threads too
	must(s.MarkAllRead(ctx, ann))
	if n := threadsOf(); n != 0 {
		t.Fatalf("after «Прочитать всё»: %d", n)
	}
}
