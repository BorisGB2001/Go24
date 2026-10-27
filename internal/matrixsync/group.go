package matrixsync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// Group chats: a chat of several people with a name and a picture; its owner (who made it, or whom
// it was handed to) names its admins and sets its rights — who adds people, who changes the chat (its name,
// description, picture, auto-delete) and who writes; the owner and admins remove people, anyone may leave. They are
// plain Matrix rooms and the rights are their power levels, so Element keeps to the same; go24's own chats (projects,
// tasks, the feed) and private chats are not groups.
//
// A channel is a group chat where only its owner and admins write (and pin); the others read, react and vote.

// Member is a person of a group chat.
type Member struct {
	ID, Name, Photo string
	Guest, Owner    bool
	Bot             bool // a chat bot of an application
	Admin           bool // the chat's admin (not its owner)
	Me              bool
	Removable       bool // the employee may remove them from the chat
}

// Group is a group chat as its header shows it.
type Group struct {
	Members []Member
	Owner   bool   // the employee owns the chat
	Channel bool   // only the owner and the admins write
	Open    bool   // any employee finds it and joins (open chats)
	CanOpen bool   // the employee makes it open or closed: its owner, not in a collab
	Writer  bool   // the employee writes in it (anyone in a group chat; in a channel — its owner and admins)
	Topic   string // the chat's description (m.room.topic)
	Rights  Rights
	CanAdd  bool // the employee adds people
	CanEdit bool // the employee changes the chat's name, description and picture
}

// Rights of a group chat «Права доступа»: who adds people, who changes the chat and who writes in it
// — RightAll, RightAdmins or RightOwner.
type Rights struct {
	Add, Edit, Write string
}

const (
	RightAll    = "all"    // anyone in the chat
	RightAdmins = "admins" // its owner and admins
	RightOwner  = "owner"  // its owner only
)

// rightLevels are the power levels of the rights.
var rightLevels = map[string]int{RightAll: 0, RightAdmins: adminLevel, RightOwner: ownerLevel}

// editEvents are the state the right to change the chat covers.
var editEvents = []string{"m.room.name", "m.room.topic", "m.room.avatar", autoDeleteType}

// rightOf is the right a power level gives.
func rightOf(level int) string {
	switch {
	case level <= 0:
		return RightAll
	case level <= adminLevel:
		return RightAdmins
	}
	return RightOwner
}

// plLevel is a level of the power levels (key — "invite", "kick", "events_default"…), def when none is set.
func plLevel(pl matrix.PowerLevels, key string, def int) int {
	switch v := pl[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func rightsOf(pl matrix.PowerLevels) Rights {
	return Rights{Add: rightOf(plLevel(pl, "invite", 0)), Edit: rightOf(stateLevel(pl, "m.room.name")),
		Write: rightOf(plLevel(pl, "events_default", 0))}
}

// applyRights puts the rights into the power levels; removing people needs at least an admin, whoever adds them.
func applyRights(pl matrix.PowerLevels, r Rights) {
	add := rightLevels[r.Add]
	pl["invite"], pl["kick"] = add, max(add, adminLevel)
	events := plEvents(pl)
	for _, t := range editEvents {
		events[t] = rightLevels[r.Edit]
	}
	writeRules(pl, rightLevels[r.Write])
}

func plEvents(pl matrix.PowerLevels) map[string]any {
	events, _ := pl["events"].(map[string]any)
	if events == nil {
		events = map[string]any{}
		pl["events"] = events
	}
	return events
}

var errNotGroup = i18n.E("err.forbidden")

// ownerLevel is the power level of a group's owner (a Matrix administrator of the room), adminLevel — of a channel's
// admins (a Matrix moderator).
const (
	ownerLevel = 100
	adminLevel = 50
)

// channelRules makes the power levels those of a channel (on) or of a group chat: in a channel messages and pins
// need an admin, while anyone in it reacts, votes and takes back their own.
func channelRules(pl matrix.PowerLevels, on bool) {
	level := 0
	if on {
		level = adminLevel
	}
	writeRules(pl, level)
}

// writeRules: messages and pins need the level; reactions, votes and taking back one's own — none.
func writeRules(pl matrix.PowerLevels, level int) {
	events := plEvents(pl)
	pl["events_default"], events["m.room.pinned_events"] = level, level
	for _, t := range []string{"m.reaction", "m.room.redaction", pollResponse, "m.poll.response"} {
		events[t] = 0
	}
	if _, ok := events["m.room.power_levels"]; !ok { // the levels themselves: the owner only (admins name no admins)
		events["m.room.power_levels"] = ownerLevel
	}
}

func isChannel(pl matrix.PowerLevels) bool { return plLevel(pl, "events_default", 0) >= adminLevel }

// peers are the go24 users the employee may write to (tasks.PeerSQL), with their Matrix IDs, and the chat bots of
// installed applications (not for guests).
func (s *Service) peers(ctx context.Context, userID string, ids []string) (map[string]string, error) {
	out := map[string]string{} // user → mxid
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT u.id::text, m.mxid FROM matrix_users m JOIN users u ON u.id = m.user_id
		WHERE u.id::text = ANY($1) AND u.active AND `+tasks.PeerSQL("$2", "u")+`
		UNION ALL SELECT b.id::text, b.mxid FROM im_bots b JOIN apps a ON a.id = b.app_id
		WHERE b.id::text = ANY($1) AND a.installed_at IS NOT NULL AND NOT `+tasks.GuestSQL("$2"), ids, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, mx string
		if err := rows.Scan(&id, &mx); err != nil {
			return nil, err
		}
		out[id] = mx
	}
	return out, rows.Err()
}

// CreateGroup makes a group chat (or a channel) of the employee and the people named; they join at once.
func (s *Service) CreateGroup(ctx context.Context, userID, name string, ids []string, channel bool) (string, error) {
	return s.CreateOpenGroup(ctx, userID, name, ids, channel, false)
}

// CreateOpenGroup is CreateGroup that may make the chat open: then it may start with no one else
// but needs its name; others join it themselves.
func (s *Service) CreateOpenGroup(ctx context.Context, userID, name string, ids []string, channel, open bool) (string, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	who, err := s.peers(ctx, userID, ids)
	if err != nil {
		return "", err
	}
	delete(who, userID)
	if open && (name == "" || s.isGuest(ctx, userID)) {
		return "", i18n.E("err.im_open_name")
	}
	if len(who) == 0 && !open {
		return "", i18n.E("err.im_group_people")
	}
	var invite []string
	for _, mx := range who {
		invite = append(invite, mx)
	}
	sort.Strings(invite)
	var levels matrix.PowerLevels
	if channel {
		levels = matrix.PowerLevels{"events": map[string]any{"m.room.power_levels": 100}}
		channelRules(levels, true)
	}
	room, err := s.M.CreateGroup(ctx, tok, me, name, invite, levels)
	if err != nil {
		return "", err
	}
	s.join(ctx, room, who)
	if open {
		if err := s.setOpen(ctx, userID, tok, room, true); err != nil {
			return room, err
		}
	}
	return room, nil
}

// join accepts the invitations as the people themselves (the admin join API wants the bot in the room, and the bot
// is not in people's own chats).
func (s *Service) join(ctx context.Context, room string, who map[string]string) {
	for id := range who {
		_, otok, err := s.puppet(ctx, id)
		if err == nil && otok != "" {
			err = s.M.JoinAs(ctx, otok, room)
		}
		if err != nil {
			s.Log.Warn("matrix: group join", "room", room, "err", err)
		}
	}
}

// group checks the room is a group chat the employee is in and gives its power levels.
func (s *Service) group(ctx context.Context, me, tok, room string) (matrix.PowerLevels, error) {
	var kind string
	err := s.DB.QueryRow(ctx, `SELECT kind FROM matrix_rooms WHERE room_id = $1`, room).Scan(&kind)
	if err == nil {
		return nil, errNotGroup // go24's own chat: its people are those of the project, the task or the company
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	direct, err := s.M.DirectRooms(ctx, tok, me)
	if err != nil {
		return nil, err
	}
	for _, rooms := range direct {
		for _, r := range rooms {
			if r == room {
				return nil, errNotGroup
			}
		}
	}
	pl := matrix.PowerLevels{}
	if err := s.M.StateAs(ctx, tok, room, "m.room.power_levels", "", &pl); err != nil {
		return nil, forbidden(err)
	}
	return pl, nil
}

// GroupInfo gives the people of a group chat, the owners first (nil — the room is not a group chat).
func (s *Service) GroupInfo(ctx context.Context, userID, room string) (*Group, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	pl, err := s.group(ctx, me, tok, room)
	if errors.Is(err, errNotGroup) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lvl := pl.Level(me)
	g := &Group{Owner: lvl >= ownerLevel, Channel: isChannel(pl), Rights: rightsOf(pl)}
	g.Writer = lvl >= plLevel(pl, "events_default", 0)
	g.CanAdd, g.CanEdit = lvl >= plLevel(pl, "invite", 0), lvl >= stateLevel(pl, "m.room.name")
	var topic struct {
		Topic string `json:"topic"`
	}
	if s.M.StateAs(ctx, tok, room, "m.room.topic", "", &topic) == nil { // none set — no description
		g.Topic = topic.Topic
	}
	var jr struct {
		JoinRule string `json:"join_rule"`
	}
	if s.M.StateAs(ctx, tok, room, "m.room.join_rules", "", &jr) == nil {
		g.Open = jr.JoinRule == "restricted"
	}
	g.CanOpen = g.Owner && s.CollabOf(ctx, room) == "" && !s.isGuest(ctx, userID)
	if g.Members, err = s.chatPeople(ctx, me, tok, room, pl); err != nil {
		return nil, err
	}
	sort.Slice(g.Members, func(i, j int) bool {
		a, b := g.Members[i], g.Members[j]
		if a.Owner != b.Owner {
			return a.Owner
		}
		if a.Admin != b.Admin {
			return a.Admin
		}
		return a.Name < b.Name
	})
	return g, nil
}

// chatPeople are the people of a chat as the employee sees them (pl names the owners; nil — none).
func (s *Service) chatPeople(ctx context.Context, me, tok, room string, pl matrix.PowerLevels) ([]Member, error) {
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return nil, forbidden(err)
	}
	var mxids []string
	for mx := range mem {
		mxids = append(mxids, mx)
	}
	who, err := s.people(ctx, mxids)
	if err != nil {
		return nil, err
	}
	var out []Member
	for _, mx := range mxids {
		m := Member{Owner: pl != nil && pl.Level(mx) >= ownerLevel, Me: mx == me}
		m.Admin = pl != nil && !m.Owner && pl.Level(mx) >= adminLevel
		m.Removable = pl != nil && mx != me && canRemove(pl, me, mx)
		if p, ok := who[mx]; ok {
			m.ID, m.Name, m.Photo, m.Guest, m.Bot = p.ID, p.Name, p.Photo, p.Guest, p.Bot
		} else {
			m.Name = strings.TrimPrefix(strings.SplitN(mx, ":", 2)[0], "@")
		}
		out = append(out, m)
	}
	return out, nil
}

// ChatMembers are the people of any chat the employee is in (a call rings them).
func (s *Service) ChatMembers(ctx context.Context, userID, room string) ([]Member, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.chatPeople(ctx, me, tok, room, nil)
}

// canRemove: the chat's rights let me remove other — removing is allowed and they are below me.
func canRemove(pl matrix.PowerLevels, me, other string) bool {
	return pl.Level(me) >= plLevel(pl, "kick", adminLevel) && pl.Level(other) < pl.Level(me)
}

// AddToGroup invites the people into the group chat, as its rights let the employee; they join at once.
func (s *Service) AddToGroup(ctx context.Context, userID, room string, ids []string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	if pl.Level(me) < plLevel(pl, "invite", 0) {
		return i18n.E("err.forbidden")
	}
	who, err := s.peers(ctx, userID, ids)
	if err != nil {
		return err
	}
	if pid := s.CollabOf(ctx, room); pid != "" { // a collab's chat: its people join the collab first
		p, err := s.collabFor(ctx, userID, pid)
		if err != nil {
			return err
		}
		var people []string
		for id := range who {
			people = append(people, id)
		}
		if err := s.intoCollab(ctx, p, people); err != nil {
			return err
		}
	}
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return forbidden(err)
	}
	for id, mx := range who {
		if _, in := mem[mx]; in {
			delete(who, id)
			continue
		}
		if err := s.M.InviteAs(ctx, tok, room, mx); err != nil {
			return forbidden(err)
		}
	}
	s.join(ctx, room, who)
	return nil
}

// RemoveFromGroup takes someone out of the group chat: its owner or an admin, anyone below them.
func (s *Service) RemoveFromGroup(ctx context.Context, userID, room, otherID string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	other, _, err := s.puppet(ctx, otherID)
	if err != nil {
		return err
	}
	if other == "" || other == me || !canRemove(pl, me, other) {
		return i18n.E("err.forbidden")
	}
	return forbidden(s.M.KickAs(ctx, tok, room, other))
}

// LeaveGroup: the employee leaves the group chat; the last owner leaving hands the chat to the first of the others.
func (s *Service) LeaveGroup(ctx context.Context, userID, room string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	if pl.Level(me) >= ownerLevel {
		mem, err := s.M.Members(ctx, room, tok)
		if err != nil {
			return forbidden(err)
		}
		var others []string
		owned := false
		for mx := range mem {
			if mx != me {
				others = append(others, mx)
				owned = owned || pl.Level(mx) >= ownerLevel
			}
		}
		if !owned && len(others) > 0 {
			sort.Strings(others)
			pl.SetLevel(others[0], ownerLevel)
			if err := s.M.SetStateAs(ctx, tok, room, "m.room.power_levels", "", pl); err != nil {
				return forbidden(err)
			}
		}
	}
	return forbidden(s.M.LeaveAs(ctx, tok, room))
}

// RenameGroup gives the group chat a new name, as its rights let the employee (Matrix checks it too).
func (s *Service) RenameGroup(ctx context.Context, userID, room, name string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	if pl.Level(me) < stateLevel(pl, "m.room.name") || strings.TrimSpace(name) == "" {
		return i18n.E("err.forbidden")
	}
	return forbidden(s.M.SetStateAs(ctx, tok, room, "m.room.name", "", map[string]string{"name": strings.TrimSpace(name)}))
}

// SetGroupTopic gives the group chat its description (empty — none), as its rights let.
func (s *Service) SetGroupTopic(ctx context.Context, userID, room, topic string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	topic = strings.TrimSpace(topic)
	if pl.Level(me) < stateLevel(pl, "m.room.topic") {
		return i18n.E("err.forbidden")
	}
	if utf8.RuneCountInString(topic) > maxTopic {
		return i18n.E("err.topic_long")
	}
	return forbidden(s.M.SetStateAs(ctx, tok, room, "m.room.topic", "", map[string]string{"topic": topic}))
}

// maxTopic is the longest description of a group chat, in letters.
const maxTopic = 1000

// SetGroupPhoto puts the group chat's picture into Matrix (m.room.avatar), as its rights let.
func (s *Service) SetGroupPhoto(ctx context.Context, userID, room, name, mime string, r io.Reader, size int64) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	if pl.Level(me) < stateLevel(pl, "m.room.avatar") || !strings.HasPrefix(mime, "image/") || mime == "image/svg+xml" {
		return i18n.E("err.forbidden")
	}
	mxc, err := s.M.Upload(ctx, tok, name, mime, r, size)
	if err != nil {
		return err
	}
	return forbidden(s.M.SetStateAs(ctx, tok, room, "m.room.avatar", "", map[string]any{"url": mxc, "info": map[string]any{"mimetype": mime, "size": size}}))
}

// RoomPhoto opens a chat's picture, for someone in the chat.
func (s *Service) RoomPhoto(ctx context.Context, userID, room string) (*http.Response, error) {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return nil, err
	}
	var av struct {
		URL string `json:"url"`
	}
	if err := s.M.StateAs(ctx, tok, room, "m.room.avatar", "", &av); err != nil {
		return nil, forbidden(err)
	}
	if av.URL == "" {
		return nil, errNotGroup
	}
	return s.M.Download(ctx, tok, av.URL)
}

// SetAdmin names someone of the group chat (or channel) its admin (on) or takes it back: its owner only.
func (s *Service) SetAdmin(ctx context.Context, userID, room, otherID string, on bool) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	other, _, err := s.puppet(ctx, otherID)
	if err != nil {
		return err
	}
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return forbidden(err)
	}
	if _, in := mem[other]; !in || pl.Level(me) < ownerLevel || other == "" || pl.Level(other) >= ownerLevel {
		return i18n.E("err.forbidden")
	}
	level := 0
	if on {
		level = adminLevel
	}
	pl.SetLevel(other, level)
	return forbidden(s.M.SetStateAs(ctx, tok, room, "m.room.power_levels", "", pl))
}

// SetOwner hands the group chat (or channel) to someone in it «Сделать владельцем»: its owner only;
// the former owner stays in it as one of its people.
func (s *Service) SetOwner(ctx context.Context, userID, room, otherID string) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	other, _, err := s.puppet(ctx, otherID)
	if err != nil {
		return err
	}
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return forbidden(err)
	}
	if _, in := mem[other]; !in || pl.Level(me) < ownerLevel || other == "" || other == me {
		return i18n.E("err.forbidden")
	}
	pl.SetLevel(other, ownerLevel)
	pl.SetLevel(me, 0)
	return forbidden(s.M.SetStateAs(ctx, tok, room, "m.room.power_levels", "", pl))
}

// SetChannel makes the group chat a channel (on) or a group chat again: its owner only.
func (s *Service) SetChannel(ctx context.Context, userID, room string, on bool) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	if pl.Level(me) < ownerLevel {
		return i18n.E("err.forbidden")
	}
	channelRules(pl, on)
	return forbidden(s.M.SetStateAs(ctx, tok, room, "m.room.power_levels", "", pl))
}

// SetRights sets the group chat's rights: its owner only.
func (s *Service) SetRights(ctx context.Context, userID, room string, r Rights) error {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	pl, err := s.group(ctx, me, tok, room)
	if err != nil {
		return err
	}
	for _, v := range []string{r.Add, r.Edit, r.Write} {
		if _, ok := rightLevels[v]; !ok {
			return i18n.E("err.forbidden")
		}
	}
	if pl.Level(me) < ownerLevel {
		return i18n.E("err.forbidden")
	}
	applyRights(pl, r)
	return forbidden(s.M.SetStateAs(ctx, tok, room, "m.room.power_levels", "", pl))
}
