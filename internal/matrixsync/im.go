package matrixsync

import (
	"cmp"
	"context"
	"errors"
	"html"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrix"
	"go24/internal/msgfmt"
	"go24/internal/tasks"
)

// The messenger inside go24: everything goes through the employee's own Matrix access (their token), so each
// person sees exactly their chats — administrators do not see other people's private chats.

var ErrNoAccount = i18n.E("err.no_matrix_account")

// Room is a chat in the list.
type Room struct {
	ID        string
	Name      string
	Kind      string // dm | notes | feed | project | collab | task | group | channel
	PeerID    string // go24 user of a private chat
	PeerPhoto string
	PeerBot   bool   // the private chat is with a chat bot
	Photo     bool   // a group chat has its own picture (web: /im/photo)
	Favorite  bool   // pinned at the top of the list (the m.favourite tag, Element's «Избранное»)
	Muted     bool   // no notifications (a push rule, as Element mutes)
	Marked    bool   // marked unread by the person
	Hidden    bool   // hidden from the list by the person, until a newer message (go24.hidden: the time it was hidden)
	Draft     string // the person's unsent text in the chat (go24.draft)
	Last      *Message
	Unread    int
	Mentions  int // unread messages that mention the person (Matrix highlights), the «@» of the list
	Threads   int // unread replies in the chat's threads (Unread counts the main timeline only)
	Encrypted bool
	// a group chat of a collab (collab_chats): the messenger keeps it on the collabs' tab, with the collab's name
	CollabID, CollabKey, CollabName string
}

// Message is one message as go24 shows it.
type Message struct {
	ID          string
	RoomID      string
	SenderMXID  string
	SenderID    string // go24 user ("" for someone not in go24)
	SenderName  string
	SenderPhoto string
	SenderGuest bool // a guest from outside (in a collab's chat)
	SenderBot   bool // a chat bot of an application
	Body        string
	At          time.Time
	Mine        bool
	Encrypted   bool
	Notice      bool   // posted by the go24 bot (task headers)
	ThreadRoot  string // for a reply in a thread
	Replies     int    // for a thread root
	NewReplies  int    // of them, those the person has not read yet
	Edited      bool
	Reactions   []tasks.Reaction // emoji under the message (in the loaded page of the chat)
	File        *File            // a file, a picture, a voice message: Body is its name
	Reply       *Quote           // the message this one answers (a reply with a quote)
	Forwarded   string           // forwarded: whose message it was (their name)
	Change      string           // live only: "edit" (Body is the new text) or "delete" of the message ID
	Event       string           // a line about the chat itself: added | removed | left | renamed (Body is the name) | photo | owner | admin | unadmin
	Target      string           // whom the line is about (their name)
	Pinned      bool             // pinned in the chat (the bar under the header)
	ReadBy      []string         // one's own message: who else has read it (names)
	Call        *CallLine        // the line of a call of the chat
	Saved       bool             // in the employee's «Избранное»
	RemindAt    time.Time        // when go24 reminds the employee of it (zero — no reminder)
	Poll        *Poll            // a poll: its question, answers and votes
	ToMe        bool             // someone else's message that mentions the employee (m.mentions)
	Keyboard    [][]Button       // a bot's message: the lines of its buttons
	plain       bool             // a text of its author (not a file, not a notice): may be edited
}

// Quote is the answered message as a reply shows it.
type Quote struct {
	ID, SenderName, Body string
	senderMXID           string
}

// File is what a file message carries (m.file, m.image, m.audio, m.video); the file itself stays in Matrix.
type File struct {
	Kind          string // file | image | audio | video
	Name, Mime    string
	Size          int64
	Width, Height int
	Duration      int    // ms, a voice message's length
	Transcript    string // the text of a voice message, once asked for (go24 keeps it, see web.imTranscribe)
	url           string
}

// fileKinds are the message types with a file, by what go24 shows for them.
var fileKinds = map[string]string{"m.file": "file", "m.image": "image", "m.audio": "audio", "m.video": "video"}

func fileOf(content map[string]any) *File {
	mt, _ := content["msgtype"].(string)
	kind, ok := fileKinds[mt]
	u, _ := content["url"].(string)
	if !ok || !strings.HasPrefix(u, "mxc://") {
		return nil
	}
	f := &File{Kind: kind, url: u}
	f.Name, _ = content["filename"].(string)
	if f.Name == "" {
		f.Name, _ = content["body"].(string)
	}
	info, _ := content["info"].(map[string]any)
	f.Mime, _ = info["mimetype"].(string)
	num := func(k string) int64 { // float64 from JSON; int, int64 in what go24 just sent
		switch v := info[k].(type) {
		case float64:
			return int64(v)
		case int:
			return int64(v)
		case int64:
			return v
		}
		return 0
	}
	f.Size, f.Width, f.Height, f.Duration = num("size"), int(num("w")), int(num("h")), int(num("duration"))
	return f
}

type imPerson struct {
	ID, Name, Photo string
	Guest           bool
	Bot             bool // a chat bot of an application (ID is the bot's)
}

// people maps Matrix IDs to go24 employees and the chat bots of applications.
func (s *Service) people(ctx context.Context, mxids []string) (map[string]imPerson, error) {
	out := map[string]imPerson{}
	if len(mxids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT m.mxid, u.id::text, u.name, u.photo_key, u.guest, false FROM matrix_users m JOIN users u ON u.id = m.user_id
		WHERE m.mxid = ANY($1)
		UNION ALL SELECT b.mxid, b.id::text, trim(b.name || ' ' || b.last_name), '', false, true FROM im_bots b WHERE b.mxid = ANY($1)`, mxids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var mx string
		var p imPerson
		if err := rows.Scan(&mx, &p.ID, &p.Name, &p.Photo, &p.Guest, &p.Bot); err != nil {
			return nil, err
		}
		out[mx] = p
	}
	return out, rows.Err()
}

// token returns the employee's Matrix ID and token, or ErrNoAccount.
func (s *Service) token(ctx context.Context, userID string) (string, string, error) {
	mx, tok, err := s.puppet(ctx, userID)
	if err != nil {
		return "", "", err
	}
	if mx == "" || tok == "" {
		return "", "", ErrNoAccount
	}
	return mx, tok, nil
}

func (s *Service) message(ev matrix.Event, roomID, me string, who map[string]imPerson) (Message, bool) {
	if by := autoDeleteBy(ev); by != "" && ev.Sender == s.M.UserID("go24") { // a collab's, as its settings say
		ev.Sender = by
	}
	m := Message{ID: ev.EventID, RoomID: roomID, SenderMXID: ev.Sender, At: time.UnixMilli(ev.TS), Mine: ev.Sender == me}
	switch ev.Type {
	case "m.room.encrypted":
		m.Encrypted = true
	case "m.room.member", "m.room.name", "m.room.avatar", "m.room.power_levels", autoDeleteType:
		if !s.chatEvent(ev, &m, who) {
			return m, false
		}
	case pollStart, "m.poll.start":
		if m.Poll = pollOf(ev.Content); m.Poll == nil {
			return m, false
		}
		m.Body, m.Poll.Mine = "📊 "+m.Poll.Question, m.Mine
		if rel, ok := ev.Content["m.relates_to"].(map[string]any); ok && rel["rel_type"] == "m.thread" {
			m.ThreadRoot, _ = rel["event_id"].(string)
		}
	case "m.room.message":
		body, _ := ev.Content["body"].(string)
		if body == "" {
			return m, false // redacted
		}
		m.Body = stripReplyFallback(body)
		m.File = fileOf(ev.Content)
		m.Call = callOf(ev.Content)
		mt, _ := ev.Content["msgtype"].(string)
		m.Notice = mt == "m.notice"
		m.plain = mt == "m.text" || mt == "m.emote"
		m.ToMe = !m.Mine && mentions(ev.Content, me)
		if rel, ok := ev.Content["m.relates_to"].(map[string]any); ok {
			switch rel["rel_type"] {
			case "m.thread":
				m.ThreadRoot, _ = rel["event_id"].(string)
			case "m.replace":
				return m, false // edits are applied to their originals, not shown on their own
			}
			// a reply with a quote; in a thread — only when it is not the thread's own fallback to its root
			if in, ok := rel["m.in_reply_to"].(map[string]any); ok && (rel["rel_type"] != "m.thread" || rel["is_falling_back"] != true) {
				if id, _ := in["event_id"].(string); id != "" {
					m.Reply = &Quote{ID: id}
					m.Reply.senderMXID, m.Reply.Body = replyFallback(body)
				}
			}
		}
		if f, ok := ev.Content["go24.forwarded"].(map[string]any); ok {
			m.Forwarded, _ = f["sender"].(string)
		}
		if ed := ev.Unsigned.Relations.Replace; ed != nil { // edited: the server gives its latest edit along
			if nc, ok := ed.Content["m.new_content"].(map[string]any); ok {
				if b, _ := nc["body"].(string); b != "" {
					m.Body, m.Edited = b, true
				}
				if c := callOf(nc); c != nil { // a call's line as the call has ended
					m.Call, m.Edited = c, false
				}
			}
		}
	default:
		return m, false
	}
	if t := ev.Unsigned.Relations.Thread; t != nil {
		m.Replies = t.Count
	}
	if p, ok := who[ev.Sender]; ok {
		m.SenderID, m.SenderName, m.SenderPhoto, m.SenderGuest, m.SenderBot = p.ID, p.Name, p.Photo, p.Guest, p.Bot
		if p.Bot && ev.Type == "m.room.message" {
			m.Keyboard = keyboardRows(keyboardOf(lastContent(ev)))
		}
	} else {
		m.SenderName = strings.TrimPrefix(strings.SplitN(ev.Sender, ":", 2)[0], "@")
		if ev.Sender == s.M.UserID("go24") {
			m.SenderName = "go24"
		}
	}
	return m, true
}

// eventPeople: who an event is from, and whom a membership change is about.
func eventPeople(ev matrix.Event) []string {
	if ev.Type == "m.room.member" && ev.StateKey != nil {
		return []string{ev.Sender, *ev.StateKey}
	}
	if ev.Type == "m.room.power_levels" {
		if target, _ := levelChange(ev); target != "" {
			return []string{ev.Sender, target}
		}
	}
	if by := autoDeleteBy(ev); by != "" {
		return []string{ev.Sender, by}
	}
	return []string{ev.Sender}
}

// autoDeleteBy is whom go24's change of a collab chat's auto-delete is from ("" — not such a change).
func autoDeleteBy(ev matrix.Event) string {
	if ev.Type != autoDeleteType {
		return ""
	}
	by, _ := ev.Content["by"].(string)
	return by
}

// chatEvent makes a line of the chat from a change of the chat itself — people added, removed or gone, a new name
// or picture; false — nothing to show (someone joining after their invitation, the invitation of a private chat,
// what the bot does).
func (s *Service) chatEvent(ev matrix.Event, m *Message, who map[string]imPerson) bool {
	if ev.Sender == s.M.UserID("go24") {
		return false // go24's own chats follow the people of their project or task: that is shown there
	}
	m.Notice = true
	switch ev.Type {
	case "m.room.member":
		if ev.StateKey == nil {
			return false
		}
		target := *ev.StateKey
		switch ev.Content["membership"] {
		case "invite":
			if ev.Content["is_direct"] == true {
				return false
			}
			m.Event = "added"
		case "leave", "ban":
			m.Event = "removed"
			if target == ev.Sender {
				m.Event = "left"
			}
		default:
			return false
		}
		m.Target = strings.TrimPrefix(strings.SplitN(target, ":", 2)[0], "@")
		if p, ok := who[target]; ok {
			m.Target = p.Name
		}
	case "m.room.name":
		m.Event = "renamed"
		m.Body, _ = ev.Content["name"].(string)
		if m.Body == "" {
			return false
		}
	case "m.room.avatar":
		m.Event = "photo"
	case "m.room.power_levels":
		target, event := levelChange(ev)
		if target == "" {
			return false
		}
		m.Event, m.Target = event, strings.TrimPrefix(strings.SplitN(target, ":", 2)[0], "@")
		if p, ok := who[target]; ok {
			m.Target = p.Name
		}
	case autoDeleteType:
		ttl, _ := ev.Content["ttl"].(float64)
		m.Event, m.Body = "autodelete", strconv.Itoa(int(ttl))
	}
	return true
}

// levelChange is whom a change of the chat's power levels is about and what it is: "owner" — they own the chat now,
// "admin" — an admin now, "unadmin" — no admin any more ("" — none of these, e.g. the chat's rights changed, or the
// levels the chat was made with).
func levelChange(ev matrix.Event) (string, string) {
	if ev.Unsigned.PrevContent == nil {
		return "", ""
	}
	was, now := matrix.PowerLevels(ev.Unsigned.PrevContent), matrix.PowerLevels(ev.Content)
	users, _ := ev.Content["users"].(map[string]any)
	old, _ := ev.Unsigned.PrevContent["users"].(map[string]any)
	var ids []string
	for mx := range users {
		ids = append(ids, mx)
	}
	for mx := range old {
		if _, ok := users[mx]; !ok {
			ids = append(ids, mx)
		}
	}
	sort.Strings(ids)
	target, event := "", ""
	for _, mx := range ids {
		a, b := was.Level(mx), now.Level(mx)
		switch {
		case b >= ownerLevel && a < ownerLevel:
			return mx, "owner"
		case b >= adminLevel && b < ownerLevel && a < adminLevel && event != "admin":
			target, event = mx, "admin"
		case a >= adminLevel && a < ownerLevel && b < adminLevel && event == "":
			target, event = mx, "unadmin"
		}
	}
	return target, event
}

// applyEdits replaces bodies of messages edited within the same batch.
func applyEdits(evs []matrix.Event, msgs []Message) {
	idx := map[string]int{}
	for i, m := range msgs {
		idx[m.ID] = i
	}
	for _, ev := range evs {
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		if rel["rel_type"] != "m.replace" {
			continue
		}
		if i, ok := idx[rel["event_id"].(string)]; ok {
			if nc, ok := ev.Content["m.new_content"].(map[string]any); ok {
				if b, _ := nc["body"].(string); b != "" {
					msgs[i].Body, msgs[i].Edited = b, true
				}
			}
		}
	}
}

// Rooms lists the employee's chats, newest activity first (spaces are not chats).
func (s *Service) Rooms(ctx context.Context, userID string) ([]Room, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	sy, err := s.M.SyncAs(ctx, tok, "", 0, 5) // a few: the newest may be someone joining, which is not shown
	if err != nil {
		return nil, err
	}
	direct := map[string]string{} // room → other user
	for _, ev := range sy.AccountData.Events {
		if ev.Type == "m.direct" {
			for mx, rooms := range ev.Content {
				list, _ := rooms.([]any)
				for _, r := range list {
					if id, ok := r.(string); ok {
						direct[id] = mx
					}
				}
			}
		}
	}
	kinds := map[string]string{}
	// a collab's room is a project room of a collab; the messenger keeps collabs on their own tab
	rows, err := s.DB.Query(ctx, `SELECT r.room_id, CASE WHEN p.kind = 'collab' THEN 'collab' ELSE r.kind END
		FROM matrix_rooms r LEFT JOIN projects p ON r.kind = 'project' AND p.id = r.ref_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, k string
		if err := rows.Scan(&id, &k); err != nil {
			rows.Close()
			return nil, err
		}
		kinds[id] = k
	}
	rows.Close()
	type inCollab struct{ ID, Key, Name string }
	collabOf := map[string]inCollab{} // a collab's group chats
	rows, err = s.DB.Query(ctx, `SELECT c.room_id, p.id::text, p.key, p.name FROM collab_chats c JOIN projects p ON p.id = c.project_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var c inCollab
		if err := rows.Scan(&id, &c.ID, &c.Key, &c.Name); err != nil {
			rows.Close()
			return nil, err
		}
		collabOf[id] = c
	}
	rows.Close()

	var mxids []string
	for id, r := range sy.Rooms.Join {
		mxids = append(mxids, r.Summary.Heroes...)
		if d := direct[id]; d != "" {
			mxids = append(mxids, d)
		}
		for _, ev := range r.Timeline.Events {
			mxids = append(mxids, eventPeople(ev)...)
		}
	}
	who, err := s.people(ctx, mxids)
	if err != nil {
		return nil, err
	}
	muted, err := s.M.MutedRooms(ctx, tok)
	if err != nil {
		s.Log.Warn("matrix: push rules", "err", err)
	}
	var out []Room
	for id, r := range sy.Rooms.Join {
		room := Room{ID: id, Kind: kinds[id], Unread: r.Unread.Notifications, Mentions: r.Unread.Highlights, Muted: muted[id]}
		if c, ok := collabOf[id]; ok {
			room.CollabID, room.CollabKey, room.CollabName = c.ID, c.Key, c.Name
		}
		for _, t := range r.Threads {
			room.Threads += t.Notifications
			room.Mentions += t.Highlights
		}
		var hidden float64
		for _, ev := range r.AccountData.Events {
			switch ev.Type {
			case "m.tag":
				tags, _ := ev.Content["tags"].(map[string]any)
				_, room.Favorite = tags["m.favourite"]
			case "m.marked_unread":
				room.Marked = ev.Content["unread"] == true
			case hiddenType:
				hidden, _ = ev.Content["ts"].(float64)
			case draftType:
				room.Draft, _ = ev.Content["text"].(string)
			}
		}
		space, channel := false, false
		// the state before the timeline, then the state events in it (a new room's name is in its first events)
		state := append([]matrix.Event{}, r.State.Events...)
		for _, ev := range r.Timeline.Events {
			if ev.StateKey != nil && *ev.StateKey == "" {
				state = append(state, ev)
			}
		}
		for _, ev := range state {
			switch ev.Type {
			case "m.room.name":
				room.Name, _ = ev.Content["name"].(string)
			case "m.room.create":
				space = ev.Content["type"] == "m.space"
			case "m.room.encryption":
				room.Encrypted = true
			case "m.room.avatar":
				room.Photo = ev.Content["url"] != nil && ev.Content["url"] != ""
			case "m.room.power_levels":
				channel = isChannel(ev.Content)
			}
		}
		if space || room.Kind == "space" {
			continue
		}
		if direct[id] == me && room.Kind == "" { // the chat with oneself
			room.Kind = "notes"
		}
		if peer := direct[id]; peer != "" && room.Kind == "" {
			room.Kind = "dm"
			if p, ok := who[peer]; ok {
				room.PeerID, room.PeerPhoto, room.PeerBot = p.ID, p.Photo, p.Bot
				if room.Name == "" {
					room.Name = p.Name
				}
			}
		}
		if room.Kind == "" {
			room.Kind = "group"
			if channel {
				room.Kind = "channel"
			}
		}
		if room.Name == "" {
			var names []string
			for _, h := range r.Summary.Heroes {
				if p, ok := who[h]; ok {
					names = append(names, p.Name)
				}
			}
			room.Name = strings.Join(names, ", ")
		}
		for i := len(r.Timeline.Events) - 1; i >= 0; i-- {
			if m, ok := s.message(r.Timeline.Events[i], id, me, who); ok {
				room.Last = &m
				break
			}
		}
		room.Hidden = hidden > 0 && (room.Last == nil || room.Last.At.UnixMilli() <= int64(hidden))
		out = append(out, room)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Favorite != out[j].Favorite { // the pinned chats first
			return out[i].Favorite
		}
		a, b := out[i].Last, out[j].Last
		if a == nil || b == nil {
			return a != nil
		}
		return a.At.After(b.At)
	})
	return out, nil
}

// Messages returns a page of a room's main timeline, oldest first, and the token for the previous page.
func (s *Service) Messages(ctx context.Context, userID, roomID, from string) ([]Message, string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, "", err
	}
	evs, end, err := s.M.Messages(ctx, tok, roomID, from, 50)
	if err != nil {
		return nil, "", forbidden(err)
	}
	msgs := s.convert(ctx, append(evs, s.recentReactions(ctx, tok, roomID)...), roomID, me, true)
	s.markPinned(ctx, tok, roomID, msgs)
	s.markReadBy(ctx, tok, me, roomID, msgs)
	s.markSaved(ctx, userID, msgs)
	s.tallyPolls(ctx, tok, roomID, me, msgs)
	s.markNewReplies(ctx, tok, roomID, msgs)
	return msgs, end, nil
}

// recentReactions are the room's latest reactions (the timeline page of messages leaves them out).
func (s *Service) recentReactions(ctx context.Context, tok, roomID string) []matrix.Event {
	evs, _, err := s.M.MessagesOf(ctx, tok, roomID, "", 300, `["m.reaction"]`)
	if err != nil {
		s.Log.Warn("matrix: reactions", "err", err)
		return nil
	}
	out := evs[:0]
	for _, ev := range evs { // only reactions, whatever the server sends
		if ev.Type == "m.reaction" {
			out = append(out, ev)
		}
	}
	return out
}

func (s *Service) convert(ctx context.Context, evs []matrix.Event, roomID, me string, mainOnly bool) []Message {
	var mxids []string
	for _, ev := range evs {
		mxids = append(mxids, eventPeople(ev)...)
	}
	who, _ := s.people(ctx, mxids)
	var out []Message
	for i := len(evs) - 1; i >= 0; i-- { // /messages goes backwards
		m, ok := s.message(evs[i], roomID, me, who)
		if !ok || (mainOnly && m.ThreadRoot != "") {
			continue
		}
		out = append(out, m)
	}
	applyEdits(evs, out)
	applyReactions(evs, out, me, who)
	s.fillQuotes(ctx, evs, out)
	return out
}

// fillQuotes names the senders of quoted and forwarded messages; a quote's text comes from the page, or from the
// reply's own fallback when the answered message is older than the page.
func (s *Service) fillQuotes(ctx context.Context, evs []matrix.Event, msgs []Message) {
	byID := map[string]matrix.Event{}
	for _, ev := range evs {
		byID[ev.EventID] = ev
	}
	var mx []string
	for i := range msgs {
		if q := msgs[i].Reply; q != nil {
			if ev, ok := byID[q.ID]; ok {
				q.senderMXID = ev.Sender
				if b, _ := ev.Content["body"].(string); b != "" {
					q.Body = stripReplyFallback(b)
				}
			}
			mx = append(mx, q.senderMXID)
		}
		if msgs[i].Forwarded != "" {
			mx = append(mx, msgs[i].Forwarded)
		}
	}
	if len(mx) == 0 {
		return
	}
	who, _ := s.people(ctx, mx)
	name := func(mxid string) string {
		if p, ok := who[mxid]; ok {
			return p.Name
		}
		local, _, _ := strings.Cut(strings.TrimPrefix(mxid, "@"), ":")
		return local
	}
	for i := range msgs {
		if q := msgs[i].Reply; q != nil {
			q.SenderName = name(q.senderMXID)
			if r := []rune(q.Body); len(r) > 200 {
				q.Body = string(r[:200]) + "…"
			}
		}
		if msgs[i].Forwarded != "" {
			msgs[i].Forwarded = name(msgs[i].Forwarded)
		}
	}
}

// replyFallback reads the quoted sender and text from a reply's fallback ("> <@ann:x> text\n\nreply").
func replyFallback(body string) (string, string) {
	var who string
	var lines []string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "> ") {
			break
		}
		l = strings.TrimPrefix(l, "> ")
		if who == "" && strings.HasPrefix(l, "<@") {
			if end := strings.Index(l, "> "); end > 0 {
				who, l = l[1:end], l[end+2:]
			} else if strings.HasSuffix(l, ">") {
				who, l = l[1:len(l)-1], ""
			}
		}
		lines = append(lines, l)
	}
	return who, strings.TrimSpace(strings.Join(lines, "\n"))
}

// applyReactions gathers the m.reaction events of the page onto their messages.
func applyReactions(evs []matrix.Event, msgs []Message, me string, who map[string]imPerson) {
	idx := map[string]int{}
	for i, m := range msgs {
		idx[m.ID] = i
	}
	type agg struct {
		names []string
		mine  bool
	}
	per := map[string]map[string]*agg{}
	var order []string
	for i := len(evs) - 1; i >= 0; i-- { // oldest first
		ev := evs[i]
		if ev.Type != "m.reaction" {
			continue
		}
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		target, _ := rel["event_id"].(string)
		key, _ := rel["key"].(string)
		if _, ok := idx[target]; !ok || key == "" {
			continue
		}
		if per[target] == nil {
			per[target] = map[string]*agg{}
		}
		a := per[target][key]
		if a == nil {
			a = &agg{}
			per[target][key] = a
			order = append(order, target+"\x00"+key)
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(ev.Sender, "@"), ":") // someone not in go24 (the bot): their login
		if p, ok := who[ev.Sender]; ok {
			name = p.Name
		}
		a.names = append(a.names, name)
		a.mine = a.mine || ev.Sender == me
	}
	for _, ok := range order {
		target, key, _ := strings.Cut(ok, "\x00")
		a := per[target][key]
		m := &msgs[idx[target]]
		m.Reactions = append(m.Reactions, tasks.Reaction{Key: key, Count: len(a.names), Mine: a.mine, Names: strings.Join(a.names, ", ")})
	}
}

// React puts the employee's reaction on a message, or takes it back when it is there already.
func (s *Service) React(ctx context.Context, userID, roomID, eventID, key string) error {
	return s.reaction(ctx, userID, roomID, eventID, key, nil)
}

// SetReaction puts the employee's reaction on a message (on) or takes it back, whether it is there or not.
func (s *Service) SetReaction(ctx context.Context, userID, roomID, eventID, key string, on bool) error {
	return s.reaction(ctx, userID, roomID, eventID, key, &on)
}

// reaction puts or takes back the employee's reaction: as on says, or the other way than it is (nil).
func (s *Service) reaction(ctx context.Context, userID, roomID, eventID, key string, on *bool) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	if key = strings.TrimSpace(key); key == "" {
		return i18n.E("err.bad_form")
	}
	evs, err := s.M.Reactions(ctx, tok, roomID, eventID)
	if err != nil {
		return forbidden(err)
	}
	for _, ev := range evs {
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		if ev.Sender == me && rel["key"] == key {
			if on != nil && *on {
				return nil
			}
			return forbidden(s.M.Do(ctx, http.MethodPut, "/_matrix/client/v3/rooms/"+url.PathEscape(roomID)+"/redact/"+
				url.PathEscape(ev.EventID)+"/r"+strconv.FormatInt(time.Now().UnixNano(), 36), map[string]string{}, nil, tok))
		}
	}
	if on != nil && !*on {
		return nil
	}
	_, err = s.M.SendEvent(ctx, roomID, "m.reaction", map[string]any{"m.relates_to": map[string]any{
		"rel_type": "m.annotation", "event_id": eventID, "key": key}}, tok)
	return forbidden(err)
}

// Thread returns a thread: its root and the replies.
func (s *Service) Thread(ctx context.Context, userID, roomID, rootID string) (Message, []Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, nil, err
	}
	root, err := s.M.EventAs(ctx, tok, roomID, rootID)
	if err != nil {
		return Message{}, nil, forbidden(err)
	}
	evs, err := s.M.Thread(ctx, tok, roomID, rootID)
	if err != nil {
		return Message{}, nil, forbidden(err)
	}
	all := append([]matrix.Event{root}, evs...)
	who, _ := s.people(ctx, []string{root.Sender})
	r, _ := s.message(root, roomID, me, who)
	// relations come oldest first; convert expects newest first
	for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 {
		evs[i], evs[j] = evs[j], evs[i]
	}
	reacts := s.recentReactions(ctx, tok, roomID)
	replies := s.convert(ctx, append(evs, reacts...), roomID, me, false)
	applyEdits(all, replies)
	s.tallyPolls(ctx, tok, roomID, me, replies)
	head := []Message{r}
	var mx []string
	for _, ev := range reacts {
		mx = append(mx, ev.Sender)
	}
	rwho, _ := s.people(ctx, mx)
	applyReactions(reacts, head, me, rwho)
	return head[0], replies, nil
}

// Send posts a message as the employee (in a thread when root is set). Task rooms and threads reach the
// go24 task chat through the bridge, like any other message.
func (s *Service) Send(ctx context.Context, userID, roomID, body, root string) (Message, error) {
	return s.Reply(ctx, userID, roomID, body, root, "")
}

// Reply posts a message that answers another one with a quote (replyTo; "" — a plain message).
func (s *Service) Reply(ctx context.Context, userID, roomID, body, root, replyTo string) (Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return Message{}, i18n.E("err.comment_empty")
	}
	content := textContent(body)
	if err := s.mention(ctx, content, body, ""); err != nil {
		return Message{}, err
	}
	var quoted matrix.Event
	if replyTo != "" {
		if quoted, err = s.M.EventAs(ctx, tok, roomID, replyTo); err != nil {
			return Message{}, forbidden(err)
		}
		content["go24.reply_to"] = replyTo
	}
	m, err := s.post(ctx, me, tok, roomID, root, content)
	if err == nil && replyTo != "" {
		s.fillQuotes(ctx, []matrix.Event{quoted}, []Message{m})
	}
	if err == nil {
		if err := s.botCommands(ctx, me, tok, roomID, root, m); err != nil {
			s.Log.Warn("matrix: bot command", "room", roomID, "err", err)
		}
	}
	return m, err
}

// textContent is a text message for Matrix; a formatted one (bold, a quote, a link) carries its HTML as well, so
// Element shows it as go24 does.
func textContent(body string) map[string]any {
	content := map[string]any{"msgtype": "m.text", "body": body}
	if rich := msgfmt.HTML(body); rich != strings.ReplaceAll(html.EscapeString(body), "\n", "<br>") {
		content["format"], content["formatted_body"] = "org.matrix.custom.html", rich
	}
	return content
}

// Edit changes the text of one's own message (m.replace, as Element edits).
func (s *Service) Edit(ctx context.Context, userID, roomID, eventID, body string) (Message, error) {
	return s.edit(ctx, userID, roomID, eventID, body, nil)
}

// BotEdit changes a message of the bot (imbot.message.update): body "" keeps the text, kb nil keeps the buttons
// (an empty one takes them away).
func (s *Service) BotEdit(ctx context.Context, botID, roomID, eventID, body string, kb []Button) (Message, error) {
	return s.edit(ctx, botID, roomID, eventID, body, kb)
}

func (s *Service) edit(ctx context.Context, userID, roomID, eventID, body string, kb []Button) (Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, err
	}
	ev, err := s.M.EventAs(ctx, tok, roomID, eventID)
	if err != nil {
		return Message{}, forbidden(err)
	}
	orig, ok := s.message(ev, roomID, me, nil)
	if !ok || ev.Sender != me || !orig.plain {
		return Message{}, i18n.E("err.forbidden")
	}
	if body = strings.TrimSpace(body); body == "" && kb != nil {
		body = orig.Body
	}
	if body == "" {
		return Message{}, i18n.E("err.comment_empty")
	}
	if kb == nil {
		kb = keyboardOf(lastContent(ev))
	}
	nc := textContent(body)
	if len(kb) > 0 {
		nc["go24.keyboard"] = kb
	}
	edit := map[string]any{"msgtype": "m.text", "body": "* " + body, "m.new_content": nc,
		"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": eventID}}
	if f, ok := nc["formatted_body"].(string); ok {
		edit["format"], edit["formatted_body"] = nc["format"], "* "+f
	}
	if _, err := s.M.Send(ctx, roomID, edit, tok); err != nil {
		return Message{}, forbidden(err)
	}
	orig.Body, orig.Edited = body, true
	who, _ := s.people(ctx, []string{me})
	if p, ok := who[me]; ok {
		orig.SenderID, orig.SenderName, orig.SenderPhoto, orig.SenderGuest, orig.SenderBot = p.ID, p.Name, p.Photo, p.Guest, p.Bot
		if p.Bot {
			orig.Keyboard = keyboardRows(kb)
		}
	}
	return orig, nil
}

// Delete takes back one's own message (a redaction: Matrix keeps no text of it).
func (s *Service) Delete(ctx context.Context, userID, roomID, eventID string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	ev, err := s.M.EventAs(ctx, tok, roomID, eventID)
	if err != nil {
		return forbidden(err)
	}
	if ev.Sender != me || ev.Type != "m.room.message" {
		return i18n.E("err.forbidden")
	}
	return forbidden(s.M.RedactAs(ctx, tok, roomID, eventID))
}

// Forward copies a message (its text or its file — the same file in Matrix) into another chat of the employee,
// marked as forwarded from its author.
func (s *Service) Forward(ctx context.Context, userID, fromRoom, eventID, toRoom string) (Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, err
	}
	ev, err := s.M.EventAs(ctx, tok, fromRoom, eventID)
	if err != nil {
		return Message{}, forbidden(err)
	}
	if ev.Type != "m.room.message" {
		return Message{}, i18n.E("err.not_found")
	}
	if pid := s.roomProject(ctx, toRoom); pid != "" && s.Tasks != nil && fileOf(ev.Content) != nil {
		if err := s.Tasks.GuestMay(ctx, pid, userID, "files"); err != nil {
			return Message{}, err
		}
	}
	content := map[string]any{}
	for _, k := range []string{"msgtype", "body", "url", "info", "filename", "org.matrix.msc3245.voice", "org.matrix.msc1767.audio"} {
		if v, ok := ev.Content[k]; ok {
			content[k] = v
		}
	}
	if ed := ev.Unsigned.Relations.Replace; ed != nil { // the edited text, not the first one
		if nc, ok := ed.Content["m.new_content"].(map[string]any); ok && nc["body"] != nil {
			content["body"] = nc["body"]
		}
	}
	if b, _ := content["body"].(string); b != "" {
		content["body"] = stripReplyFallback(b)
	}
	from := ev.Sender
	if f, ok := ev.Content["go24.forwarded"].(map[string]any); ok && f["sender"] != nil { // forwarded again: the author stays
		from, _ = f["sender"].(string)
	}
	content["go24.forwarded"] = map[string]any{"sender": from, "room": fromRoom, "event": eventID}
	m, err := s.post(ctx, me, tok, toRoom, "", content)
	if err == nil {
		m.Forwarded = from
		one := []Message{m}
		s.fillQuotes(ctx, nil, one)
		m = one[0]
	}
	return m, err
}

// SendFile posts a file (a picture, a voice message) as the employee: it goes into the Matrix media repository
// under their name, the message points to it. In a collab's chat a guest sends files where the collab lets guests
// upload them. extra adds to the file's info (a voice message's duration, a picture's size).
func (s *Service) SendFile(ctx context.Context, userID, roomID, root, name, mime string, r io.Reader, size int64, extra map[string]any) (Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, err
	}
	if pid := s.roomProject(ctx, roomID); pid != "" && s.Tasks != nil {
		if err := s.Tasks.GuestMay(ctx, pid, userID, "files"); err != nil {
			return Message{}, err
		}
	}
	name = strings.TrimSpace(name)
	if name == "" || size <= 0 {
		return Message{}, i18n.E("err.file_required")
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	mxc, err := s.M.Upload(ctx, tok, name, mime, r, size)
	if err != nil {
		return Message{}, forbidden(err)
	}
	msgtype := "m.file"
	switch {
	case strings.HasPrefix(mime, "image/"):
		msgtype = "m.image"
	case strings.HasPrefix(mime, "audio/"):
		msgtype = "m.audio"
	case strings.HasPrefix(mime, "video/"):
		msgtype = "m.video"
	}
	info := map[string]any{"mimetype": mime, "size": size}
	for k, v := range extra {
		info[k] = v
	}
	content := map[string]any{"msgtype": msgtype, "body": name, "filename": name, "url": mxc, "info": info}
	if msgtype == "m.audio" && extra["duration"] != nil { // a voice message, as Element records them
		content["org.matrix.msc3245.voice"] = map[string]any{}
		content["org.matrix.msc1767.audio"] = map[string]any{"duration": extra["duration"]}
	}
	return s.post(ctx, me, tok, roomID, root, content)
}

// OpenFile is the file of a message the employee sees: the event is read with their access (so a chat they are
// not in gives nothing), then its file. The caller closes the body.
func (s *Service) OpenFile(ctx context.Context, userID, roomID, eventID string) (*File, io.ReadCloser, error) {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	ev, err := s.M.EventAs(ctx, tok, roomID, eventID)
	if err != nil {
		return nil, nil, forbidden(err)
	}
	f := fileOf(ev.Content)
	if ev.Type != "m.room.message" || f == nil {
		return nil, nil, i18n.E("err.not_found")
	}
	res, err := s.M.Download(ctx, tok, f.url)
	if err != nil {
		return nil, nil, forbidden(err)
	}
	if f.Mime == "" {
		f.Mime = res.Header.Get("Content-Type")
	}
	return f, res.Body, nil
}

// One gives one message of a chat the employee is in, with the chat's name ("" — a private chat) and the project
// (a collab too) the chat belongs to ("" — none): a task made of the message.
func (s *Service) One(ctx context.Context, userID, roomID, eventID string) (Message, string, string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, "", "", err
	}
	ev, err := s.M.EventAs(ctx, tok, roomID, eventID)
	if err != nil {
		return Message{}, "", "", forbidden(err)
	}
	msgs := s.convert(ctx, []matrix.Event{ev}, roomID, me, false)
	if len(msgs) == 0 || msgs[0].Event != "" {
		return Message{}, "", "", i18n.E("err.not_found")
	}
	s.tallyPolls(ctx, tok, roomID, me, msgs)
	var name struct {
		Name string `json:"name"`
	}
	_ = s.M.StateAs(ctx, tok, roomID, "m.room.name", "", &name)
	return msgs[0], name.Name, s.roomProject(ctx, roomID), nil
}

// ChatClosedKeys are the messages of why one does not write in a collab's chat (CollabChatClosed).
var ChatClosedKeys = map[string]string{"archived": "collab.chat_readonly.archived", "employees": "collab.chat_readonly.employees",
	"managers": "collab.chat_readonly.managers"}

// CollabChatClosed: why the person (a go24 user or their Matrix ID) does not write in this chat of a collab, as the
// key of ChatClosedKeys: archived, or the collab's «кто может писать в чат» (employees | managers); "" —
// they write, or it is no collab's chat.
func (s *Service) CollabChatClosed(ctx context.Context, who, roomID string) string {
	var why string
	_ = s.DB.QueryRow(ctx, `SELECT CASE WHEN p.archived THEN 'archived' WHEN NOT `+collabWriterSQL+` THEN p.chat_perm ELSE '' END
		FROM matrix_rooms r JOIN projects p ON p.id = r.ref_id AND p.kind = 'collab'
		JOIN project_members pm ON pm.project_id = p.id JOIN users u ON u.id = pm.user_id
		WHERE r.room_id = $1 AND r.kind = 'project'
			AND (u.id::text = $2 OR EXISTS (SELECT 1 FROM matrix_users mu WHERE mu.user_id = u.id AND mu.mxid = $2))`, roomID, who).Scan(&why)
	return why
}

// RoomCollab is the collab whose chat this room is; "" for other chats.
func (s *Service) RoomCollab(ctx context.Context, roomID string) string {
	var pid string
	_ = s.DB.QueryRow(ctx, `SELECT p.id::text FROM matrix_rooms r JOIN projects p ON p.id = r.ref_id AND p.kind = 'collab'
		WHERE r.room_id = $1 AND r.kind = 'project'`, roomID).Scan(&pid)
	return pid
}

// roomProject is the project (a collab too) whose room — or whose task's room — this is; "" for other chats.
func (s *Service) roomProject(ctx context.Context, roomID string) string {
	var pid string
	_ = s.DB.QueryRow(ctx, `SELECT coalesce(CASE r.kind WHEN 'project' THEN r.ref_id END, t.project_id)::text
		FROM matrix_rooms r LEFT JOIN tasks t ON r.kind = 'task' AND t.id = r.ref_id WHERE r.room_id = $1`, roomID).Scan(&pid)
	return pid
}

// post sends the content as the employee (in a thread when root is set) and gives it back as a chat message.
func (s *Service) post(ctx context.Context, me, tok, roomID, root string, content map[string]any) (Message, error) {
	replyTo, _ := content["go24.reply_to"].(string) // Reply's quote: a relation, not a key of its own
	delete(content, "go24.reply_to")
	switch {
	case root != "" && replyTo != "":
		content["m.relates_to"] = map[string]any{"rel_type": "m.thread", "event_id": root, "is_falling_back": false,
			"m.in_reply_to": map[string]string{"event_id": replyTo}}
	case root != "":
		content["m.relates_to"] = map[string]any{"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
			"m.in_reply_to": map[string]string{"event_id": root}}
	case replyTo != "":
		content["m.relates_to"] = map[string]any{"m.in_reply_to": map[string]string{"event_id": replyTo}}
	}
	if why := s.CollabChatClosed(ctx, me, roomID); why != "" { // Matrix says the same once the room's levels are put
		return Message{}, i18n.E(ChatClosedKeys[why])
	}
	id, err := s.M.Send(ctx, roomID, content, tok)
	if err != nil {
		return Message{}, forbidden(err)
	}
	// one's own message: everything before it has been read — in its thread (the main timeline or a thread), the
	// other threads keep their unread replies
	if err := s.M.ThreadReceipt(ctx, tok, roomID, id, cmp.Or(root, "main")); err != nil {
		s.Log.Warn("read receipt after send", "room", roomID, "err", err)
	}
	who, _ := s.people(ctx, []string{me})
	body, _ := content["body"].(string)
	m := Message{ID: id, RoomID: roomID, SenderMXID: me, Body: body, At: time.Now(), Mine: true, ThreadRoot: root, File: fileOf(content)}
	mt, _ := content["msgtype"].(string)
	m.plain = (mt == "m.text" || mt == "m.emote") && content["go24.forwarded"] == nil
	if replyTo != "" {
		m.Reply = &Quote{ID: replyTo}
	}
	if p, ok := who[me]; ok {
		m.SenderID, m.SenderName, m.SenderPhoto, m.SenderGuest = p.ID, p.Name, p.Photo, p.Guest
	}
	return m, nil
}

// MarkRead moves the employee's read marker to the event.
func (s *Service) MarkRead(ctx context.Context, userID, roomID, eventID string) error {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	return s.M.ReadReceipt(ctx, tok, roomID, eventID)
}

// MarkRoomRead reads the chat's main timeline: the marker goes to its newest message there; the threads keep their
// unread replies until they are opened (ThreadRead), as in Slack.
func (s *Service) MarkRoomRead(ctx context.Context, userID, roomID string) error {
	return s.markRoomRead(ctx, userID, roomID, false)
}

// markRoomRead reads the main timeline, or (threads) the whole chat: an unthreaded receipt at its newest event, so
// no unread count is left behind in a thread.
func (s *Service) markRoomRead(ctx context.Context, userID, roomID string, threads bool) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	var marked struct {
		Unread bool `json:"unread"`
	}
	if s.M.RoomData(ctx, tok, me, roomID, "m.marked_unread", &marked) == nil && marked.Unread { // read: no longer marked
		if err := s.M.SetRoomData(ctx, tok, me, roomID, "m.marked_unread", map[string]bool{"unread": false}); err != nil {
			return err
		}
	}
	if threads {
		evs, _, err := s.M.Messages(ctx, tok, roomID, "", 1)
		if err != nil {
			return forbidden(err)
		}
		if len(evs) == 0 {
			return nil
		}
		return s.M.ReadReceipt(ctx, tok, roomID, evs[0].EventID)
	}
	evs, _, err := s.M.Messages(ctx, tok, roomID, "", 50)
	if err != nil {
		return forbidden(err)
	}
	// the newest one outside the threads: a receipt of the main timeline takes no other (nor an edit of a thread's
	// reply, which the server counts into its thread — then the one before)
	for _, ev := range evs {
		if rel, _ := ev.Content["m.relates_to"].(map[string]any); rel["rel_type"] == "m.thread" {
			continue
		}
		if err = s.M.ThreadReceipt(ctx, tok, roomID, ev.EventID, "main"); !matrix.IsCode(err, "M_INVALID_PARAM") {
			return err
		}
	}
	return nil
}

// ThreadRead reads a thread: the person's marker of that thread goes to its newest reply.
func (s *Service) ThreadRead(ctx context.Context, userID, roomID, rootID string) error {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	last, err := s.M.LastInThread(ctx, tok, roomID, rootID)
	if err != nil || last == "" {
		return forbidden(err)
	}
	return s.M.ThreadReceipt(ctx, tok, roomID, last, rootID)
}

// ThreadItem is a thread in the list of a chat's threads: its root (with its replies counted), when and by whom it
// was last answered.
type ThreadItem struct {
	Root   Message
	LastAt time.Time
	LastBy string
}

// Threads lists the chat's threads, as «Треды» and Slack: those with unread replies first, then the
// latest answered.
func (s *Service) Threads(ctx context.Context, userID, roomID string) ([]ThreadItem, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	roots, err := s.M.Threads(ctx, tok, roomID, 100)
	if err != nil {
		return nil, forbidden(err)
	}
	last := map[string]matrix.Event{} // root → its newest reply
	var mx []string
	for _, ev := range roots {
		if t := ev.Unsigned.Relations.Thread; t != nil && t.Latest != nil {
			last[ev.EventID] = *t.Latest
			mx = append(mx, t.Latest.Sender)
		}
	}
	who, _ := s.people(ctx, mx)
	msgs := s.convert(ctx, roots, roomID, me, true)
	s.markNewReplies(ctx, tok, roomID, msgs)
	out := make([]ThreadItem, 0, len(msgs))
	for _, m := range msgs {
		it := ThreadItem{Root: m, LastAt: m.At}
		if ev, ok := last[m.ID]; ok {
			it.LastAt = time.UnixMilli(ev.TS)
			it.LastBy = strings.TrimPrefix(strings.SplitN(ev.Sender, ":", 2)[0], "@")
			if p, ok := who[ev.Sender]; ok {
				it.LastBy = p.Name
			}
		}
		out = append(out, it)
	}
	slices.SortStableFunc(out, func(a, b ThreadItem) int {
		if (a.Root.NewReplies > 0) != (b.Root.NewReplies > 0) {
			if a.Root.NewReplies > 0 {
				return -1
			}
			return 1
		}
		return b.LastAt.Compare(a.LastAt)
	})
	return out, nil
}

// ReadTaskThread reads the task's chat when it is a thread of its project's room (the task card is open: its chat is
// seen there); a task without one is left as it is — nothing is made.
func (s *Service) ReadTaskThread(ctx context.Context, userID, taskID string) error {
	var room, root string
	err := s.DB.QueryRow(ctx, `SELECT r.room_id, t.matrix_thread FROM tasks_all t
		JOIN matrix_rooms r ON r.kind = 'project' AND r.ref_id = t.project_id WHERE t.id = $1 AND coalesce(t.matrix_thread, '') <> ''`, taskID).Scan(&room, &root)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.ThreadRead(ctx, userID, room, root); err != nil && !errors.Is(err, ErrNoAccount) {
		return err
	}
	return nil
}

// markNewReplies counts, under each thread root of the page, the replies the person has not read yet.
func (s *Service) markNewReplies(ctx context.Context, tok, roomID string, msgs []Message) {
	if !slices.ContainsFunc(msgs, func(m Message) bool { return m.Replies > 0 }) {
		return
	}
	counts, err := s.M.ThreadCounts(ctx, tok, roomID)
	if err != nil {
		s.Log.Warn("matrix: thread counts", "room", roomID, "err", err)
		return
	}
	for i := range msgs {
		msgs[i].NewReplies = min(counts[msgs[i].ID].Notifications, msgs[i].Replies)
	}
}

// MarkAllRead reads every chat of the employee that has unread messages or is marked unread («Прочитать всё»).
func (s *Service) MarkAllRead(ctx context.Context, userID string) error {
	rooms, err := s.Rooms(ctx, userID)
	if err != nil {
		return err
	}
	for _, r := range rooms {
		if r.Unread > 0 || r.Mentions > 0 || r.Threads > 0 || r.Marked {
			if err := s.markRoomRead(ctx, userID, r.ID, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// DirectChat finds or starts the private chat between two employees.
func (s *Service) DirectChat(ctx context.Context, userID, otherID string) (string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return "", err
	}
	// a guest writes only to the people of their collabs, and only they write to the guest (tasks.PeerSQL); a chat
	// bot is the other side as well
	who, err := s.peers(ctx, userID, []string{otherID})
	if err != nil {
		return "", err
	}
	other := who[otherID]
	if other == "" {
		return "", ErrNoAccount
	}
	if other == me {
		return "", i18n.E("err.forbidden")
	}
	direct, err := s.M.DirectRooms(ctx, tok, me)
	if err != nil {
		return "", err
	}
	// the bot is not in private chats: who is in it is asked as the employee
	if rooms := direct[other]; len(rooms) > 0 {
		if mem, err := s.M.Members(ctx, rooms[len(rooms)-1], tok); err == nil {
			if _, ok := mem[other]; ok {
				if _, ok := mem[me]; ok {
					return rooms[len(rooms)-1], nil
				}
			}
		}
	}
	room, err := s.M.CreateDM(ctx, tok, other)
	if err != nil {
		return "", err
	}
	// the other person joins at once (accepts the invitation as themselves: the admin join API wants the bot in the
	// room, and the bot is not in private chats) and both clients know it is a private chat
	_, otok, err := s.puppet(ctx, otherID)
	if err == nil {
		err = s.M.JoinAs(ctx, otok, room)
	}
	if err != nil {
		return "", err
	}
	direct[other] = append(direct[other], room)
	if err := s.M.SetDirectRooms(ctx, tok, me, direct); err != nil {
		return "", err
	}
	if od, err := s.M.DirectRooms(ctx, otok, other); err == nil {
		od[me] = append(od[me], room)
		_ = s.M.SetDirectRooms(ctx, otok, other, od)
	}
	return room, nil
}

// NotesChat is the person's chat with themselves, as «Заметки»: notes, links and files for oneself; made on
// first use with the name given (in the person's language) and kept in m.direct under their own id, so Element too
// shows it as a private chat.
func (s *Service) NotesChat(ctx context.Context, userID, name string) (string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return "", err
	}
	direct, err := s.M.DirectRooms(ctx, tok, me)
	if err != nil {
		return "", err
	}
	if rooms := direct[me]; len(rooms) > 0 {
		if mem, err := s.M.Members(ctx, rooms[len(rooms)-1], tok); err == nil {
			if _, ok := mem[me]; ok {
				return rooms[len(rooms)-1], nil
			}
		}
	}
	room, err := s.M.CreateNotes(ctx, tok, name)
	if err != nil {
		return "", err
	}
	direct[me] = append(direct[me], room)
	if err := s.M.SetDirectRooms(ctx, tok, me, direct); err != nil {
		return "", err
	}
	return room, nil
}

// TaskChat is where a task is discussed (room and thread root), made on first use.
func (s *Service) TaskChat(ctx context.Context, taskID string) (string, string, error) {
	return s.target(ctx, taskID)
}

// Live follows the employee's chats: emit gets each new message (nil — not wanted); counts gets the unread counts
// of the chats (their main timelines), the unread mentions of the employee and the unread replies in the chats'
// threads (room → count) at once and whenever one changes (nil — not wanted).
func (s *Service) Live(ctx context.Context, userID string, emit func(Message), counts func(unread, mentions, threads map[string]int)) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	first, err := s.M.SyncAs(ctx, tok, "", 0, 0)
	if err != nil {
		return err
	}
	unread, ment, threads := map[string]int{}, map[string]int{}, map[string]int{}
	tally := func(id string, u matrix.UnreadCounts, ts map[string]matrix.UnreadCounts) bool {
		n, at, t := u.Notifications, u.Highlights, 0
		for _, c := range ts {
			t += c.Notifications
			at += c.Highlights
		}
		old, ok := unread[id]
		changed := !ok || old != n || ment[id] != at || threads[id] != t
		unread[id], ment[id], threads[id] = n, at, t
		return changed
	}
	for id, r := range first.Rooms.Join {
		tally(id, r.Unread, r.Threads)
	}
	if counts != nil {
		counts(maps.Clone(unread), maps.Clone(ment), maps.Clone(threads))
	}
	since := first.NextBatch
	for ctx.Err() == nil {
		sy, err := s.M.SyncAs(ctx, tok, since, 25*time.Second, 50, "m.receipt", "m.typing")
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		since = sy.NextBatch
		changed := false
		for id, r := range sy.Rooms.Join {
			if tally(id, r.Unread, r.Threads) {
				changed = true
			}
			if emit == nil {
				continue
			}
			evs := r.Timeline.Events
			for i, j := 0, len(evs)-1; i < j; i, j = i+1, j-1 { // convert wants newest first
				evs[i], evs[j] = evs[j], evs[i]
			}
			for _, m := range s.convert(ctx, evs, id, me, false) {
				emit(m)
			}
			for _, ev := range evs { // an edit or a deletion of a message already on the screen
				if c := liveChange(ev, id); c != nil {
					emit(*c)
				}
			}
			for _, c := range s.livePolls(ctx, me, tok, id, evs) { // a vote, the end of a poll
				emit(c)
			}
			for _, c := range s.liveEphemeral(ctx, me, id, r.Ephemeral.Events) {
				emit(c)
			}
		}
		if changed && counts != nil {
			counts(maps.Clone(unread), maps.Clone(ment), maps.Clone(threads))
		}
	}
	return nil
}

// liveChange: the event changes a message shown already — an edit (the new text) or a redaction.
func liveChange(ev matrix.Event, roomID string) *Message {
	switch ev.Type {
	case "m.room.redaction":
		target := ev.Redacts
		if target == "" {
			target, _ = ev.Content["redacts"].(string)
		}
		if target != "" {
			return &Message{ID: target, RoomID: roomID, Change: "delete"}
		}
	case "m.room.message":
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		nc, _ := ev.Content["m.new_content"].(map[string]any)
		target, _ := rel["event_id"].(string)
		if b, _ := nc["body"].(string); rel["rel_type"] == "m.replace" && target != "" && b != "" {
			if c := callOf(nc); c != nil {
				return &Message{ID: target, RoomID: roomID, Change: "edit", Body: b, Call: c, Notice: true, At: time.UnixMilli(ev.TS)}
			}
			return &Message{ID: target, RoomID: roomID, Change: "edit", Body: b, Edited: true}
		}
	}
	return nil
}

// Editable: the employee may change the text of the message (their own plain text).
func (m Message) Editable() bool { return m.Mine && m.plain && m.File == nil && m.Forwarded == "" }

func forbidden(err error) error {
	if matrix.IsCode(err, "M_FORBIDDEN") || matrix.IsCode(err, "M_NOT_FOUND") {
		return i18n.E("err.forbidden")
	}
	return err
}
