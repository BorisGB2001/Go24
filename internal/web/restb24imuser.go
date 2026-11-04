package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/company"
	"go24/internal/i18n"
)

// REST compatible with the portal, the messenger, part 3 (GO24DEV-915): im.message.like, im.dialog.writing,
// im.chat.mute, im.recent.pin, im.recent.unread, im.recent.hide, im.counters.get; im.user.get, im.user.list.get,
// im.user.status.get, im.user.status.set; im.search.user.list, im.search.chat.list.

func init() {
	for name, m := range map[string]portalMethod{
		"im.message.like":     {Scope: "im:write", Run: (*Server).portalMessageLike},
		"im.dialog.writing":   {Scope: "im:write", Run: (*Server).portalDialogWriting},
		"im.chat.mute":        {Scope: "im:write", Run: (*Server).portalChatMute},
		"im.recent.pin":       {Scope: "im:write", Run: (*Server).portalRecentPin},
		"im.recent.unread":    {Scope: "im:write", Run: (*Server).portalRecentUnread},
		"im.recent.hide":      {Scope: "im:write", Run: (*Server).portalRecentHide},
		"im.counters.get":     {Scope: "im:read", Run: (*Server).portalCounters},
		"im.user.get":         {Scope: "im:read", Run: (*Server).portalIMUserGet},
		"im.user.list.get":    {Scope: "im:read", Run: (*Server).portalIMUserList},
		"im.user.status.get":  {Scope: "im:read", Run: (*Server).portalIMStatusGet},
		"im.user.status.set":  {Scope: "im:write", Run: (*Server).portalIMStatusSet},
		"im.search.user.list": {Scope: "im:read", Run: (*Server).portalSearchUsers},
		"im.search.chat.list": {Scope: "im:read", Run: (*Server).portalSearchChats},
	} {
		portalMethods[strings.ToLower(name)] = m // looked up in lower case
	}
}

// portalAction is ACTION: Y (the default) or N; plus and minus for a like.
func portalAction(c portalCall) bool {
	switch strings.ToUpper(strings.TrimSpace(portalStr(portalParam(c.in, "ACTION")))) {
	case "N", "MINUS", "0", "FALSE":
		return false
	}
	return true
}

// portalLike is the reaction im.message.like puts: the «Нравится».
const portalLike = "👍"

// im.message.like: the caller likes the message MESSAGE_ID — ACTION plus, minus, or auto (the default: the other
// way than it is).
func (s *Server) portalMessageLike(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, event, err := s.portalMessage(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	switch strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "ACTION")))) {
	case "plus":
		err = s.Matrix.SetReaction(ctx, c.p.UserID, room, event, portalLike, true)
	case "minus":
		err = s.Matrix.SetReaction(ctx, c.p.UserID, room, event, portalLike, false)
	default:
		err = s.Matrix.React(ctx, c.p.UserID, room, event, portalLike)
	}
	return portalOut{Result: err == nil}, err
}

// im.dialog.writing: the others in DIALOG_ID see «печатает…» of the caller.
func (s *Server) portalDialogWriting(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, err := s.portalDialog(c, portalParam(c.in, "DIALOG_ID"))
	if err != nil {
		return portalOut{}, err
	}
	err = s.Matrix.Typing(c.r.Context(), c.p.UserID, room, true)
	return portalOut{Result: err == nil}, err
}

// portalMark sets the caller's own mark of a chat (see RoomMark) as ACTION says.
func (s *Server) portalMark(c portalCall, room, mark string, on bool) (portalOut, error) {
	err := s.Matrix.RoomMark(c.r.Context(), c.p.UserID, room, mark, on)
	return portalOut{Result: err == nil}, err
}

// portalMarkDialog: the mark of the chat DIALOG_ID.
func (s *Server) portalMarkDialog(c portalCall, mark string, on bool) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	room, err := s.portalDialog(c, portalParam(c.in, "DIALOG_ID"))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalMark(c, room, mark, on)
}

// im.chat.mute: the chat CHAT_ID without notifications for the caller (ACTION=N — with them again).
func (s *Server) portalChatMute(c portalCall) (portalOut, error) {
	room, err := s.portalChat(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalMark(c, room, "mute", portalAction(c))
}

// im.recent.pin: DIALOG_ID pinned at the top of the caller's chats (ACTION=N — unpinned).
func (s *Server) portalRecentPin(c portalCall) (portalOut, error) {
	return s.portalMarkDialog(c, "favorite", portalAction(c))
}

// im.recent.unread: DIALOG_ID marked unread (ACTION=N — the mark taken off).
func (s *Server) portalRecentUnread(c portalCall) (portalOut, error) {
	return s.portalMarkDialog(c, "unread", portalAction(c))
}

// im.recent.hide: DIALOG_ID hidden from the caller's chats until a newer message.
func (s *Server) portalRecentHide(c portalCall) (portalOut, error) {
	return s.portalMarkDialog(c, "hidden", true)
}

// im.counters.get: the caller's unread — CHAT (by the chat's number), DIALOG (by the colleague's number), LINES
// (open lines have their own counters), TYPE: ALL, NOTIFY, CHAT, DIALOG, LINES. A muted chat does not count, as in
// the portal.
func (s *Server) portalCounters(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	notifs, err := s.Notify.Unread(ctx, c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	chats, dialogs := map[string]int{}, map[string]int{}
	var inChats, inDialogs int
	if s.Matrix != nil {
		rooms, err := s.Matrix.Rooms(ctx, c.p.UserID)
		if err != nil {
			return portalOut{}, err
		}
		for _, rm := range rooms {
			n := rm.Unread + rm.Threads
			if rm.Marked && n == 0 {
				n = 1
			}
			if n == 0 || rm.Muted || rm.Hidden {
				continue
			}
			if rm.Kind == "dm" && rm.PeerID != "" {
				num, err := s.portalUserNum(c, rm.PeerID)
				if err != nil {
					return portalOut{}, err
				}
				dialogs[strconv.FormatInt(num, 10)] = n
				inDialogs += n
				continue
			}
			num, err := s.portalChatNum(ctx, rm.ID)
			if err != nil {
				return portalOut{}, err
			}
			chats[strconv.FormatInt(num, 10)] = n
			inChats += n
		}
	}
	return portalOut{Result: map[string]any{"CHAT": chats, "DIALOG": dialogs, "LINES": map[string]int{},
		"TYPE": map[string]int{"ALL": notifs + inChats + inDialogs, "NOTIFY": notifs, "CHAT": inChats,
			"DIALOG": inDialogs, "LINES": 0}}}, nil
}

// portalIMUsers are the employees ids (a guest of a collab sees only those they know) in the fields of the
// messenger: id, name, first_name, last_name, work_position, avatar, gender, birthday, extranet,
// status (online, away, dnd — the chosen one), idle, last_activity_date, departments, absent (the last day of an
// absence going on), phones. By number.
func (s *Server) portalIMUsers(c portalCall, ids []string) (map[int64]map[string]any, error) {
	ctx := c.r.Context()
	out := map[int64]map[string]any{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT u.id::text, u.num, u.active, u.name, u.first_name, u.last_name, u.position,
			u.photo_key, u.gender, u.birthday, u.birthday_show_year, u.guest, u.presence, u.seen_at, u.work_phone,
			u.mobile_phone, u.inner_phone,
			COALESCE((SELECT array_agg(d.num ORDER BY d.num) FROM user_departments ud JOIN departments d ON d.id = ud.department_id
				WHERE ud.user_id = u.id), '{}'),
			(SELECT max(a.ends_on) FROM absences a WHERE a.user_id = u.id AND current_date BETWEEN a.starts_on AND a.ends_on)
		FROM users u WHERE u.id::text = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var id, name, first, last, position, photo, gender, status, work, mobile, inner string
		var num int64
		var active, showYear, guest bool
		var birthday, seen, absent *time.Time
		var depts []int64
		if err := rows.Scan(&id, &num, &active, &name, &first, &last, &position, &photo, &gender, &birthday, &showYear,
			&guest, &status, &seen, &work, &mobile, &inner, &depts, &absent); err != nil {
			return nil, err
		}
		if guest && id != c.p.UserID && !s.knowsPerson(c.r, c.p.UserID, id) && !s.apiAdmin(ctx, c.p.UserID) {
			continue
		}
		u := map[string]any{"id": num, "active": active, "name": name, "first_name": first, "last_name": last,
			"work_position": position, "color": "", "avatar": "", "gender": gender, "birthday": "", "extranet": guest,
			"network": false, "bot": false, "connector": false, "external_auth_id": "default", "status": "online",
			"idle": false, "last_activity_date": false, "mobile_last_date": false, "departments": depts, "absent": false,
			"phones": map[string]string{"work_phone": work, "personal_mobile": mobile, "inner_phone": inner}}
		if photo != "" {
			u["avatar"] = strings.TrimRight(s.BaseURL, "/") + company.PhotoURL(id, photo)
		}
		if birthday != nil {
			u["birthday"] = birthday.Format("02-01")
		}
		if status != "" {
			u["status"] = status
		}
		if seen != nil {
			u["last_activity_date"] = seen.Format(time.RFC3339)
			if status == "away" || now.Sub(*seen) > onlineFor {
				u["idle"] = seen.Format(time.RFC3339)
			}
		}
		if absent != nil {
			u["absent"] = absent.Format(time.RFC3339)
		}
		out[num] = u
	}
	return out, rows.Err()
}

// portalUserIDs: the numbers of employees → their ids; unknown numbers are left out.
func (s *Server) portalUserIDs(c portalCall, v any) ([]string, error) {
	var ids []string
	for _, x := range portalList(v) {
		id, err := s.portalNum(c.r.Context(), "users", x)
		if ie := (*i18n.Error)(nil); errors.As(err, &ie) && ie.Key == "err.not_found" {
			continue // an unknown number: null in im.user.list.get
		} else if err != nil {
			return nil, err
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// im.user.get: the employee ID (the caller when none) in the messenger's fields (see portalIMUsers).
func (s *Server) portalIMUserGet(c portalCall) (portalOut, error) {
	id := c.p.UserID
	if v := strings.TrimSpace(portalStr(portalParam(c.in, "ID"))); v != "" && v != "0" {
		ids, err := s.portalUserIDs(c, v)
		if err != nil {
			return portalOut{}, err
		}
		if len(ids) == 0 {
			return portalOut{}, i18n.E("err.not_found")
		}
		id = ids[0]
	}
	users, err := s.portalIMUsers(c, []string{id})
	if err != nil {
		return portalOut{}, err
	}
	for _, u := range users {
		return portalOut{Result: u}, nil
	}
	return portalOut{}, i18n.E("err.not_found")
}

// im.user.list.get: the employees ID (numbers) → {number: the employee in the messenger's fields}; unknown — null.
func (s *Server) portalIMUserList(c portalCall) (portalOut, error) {
	vals := portalList(portalParam(c.in, "ID"))
	if len(vals) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "INVALID_FORMAT", "err.portal_user")
	}
	var ids []string
	for _, v := range vals {
		got, err := s.portalUserIDs(c, v)
		if err != nil {
			return portalOut{}, err
		}
		ids = append(ids, got...)
	}
	users, err := s.portalIMUsers(c, ids)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	for _, v := range vals {
		n, _ := strconv.ParseInt(v, 10, 64)
		if u, ok := users[n]; ok {
			out[v] = u
		} else {
			out[v] = nil
		}
	}
	return portalOut{Result: out}, nil
}

// im.user.status.get: the caller's chosen status — online, away or dnd («Не беспокоить»).
func (s *Server) portalIMStatusGet(c portalCall) (portalOut, error) {
	status := s.myPresence(c.r.Context(), c.p.UserID)
	if status == "" {
		status = "online"
	}
	return portalOut{Result: status}, nil
}

// im.user.status.set: the caller's status STATUS — online, away or dnd, as in their card.
func (s *Server) portalIMStatusSet(c portalCall) (portalOut, error) {
	status := strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "STATUS"))))
	switch status {
	case "online":
		status = ""
	case "away", "dnd":
	default:
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "STATUS_ERROR", "err.portal_status")
	}
	_, err := s.DB.Exec(c.r.Context(), `UPDATE users SET presence = $2, seen_at = now() WHERE id = $1`, c.p.UserID, status)
	s.Telephony.Kick() // «Не беспокоить»: the clients' calls may pass them by now (or not)
	return portalOut{Result: err == nil}, err
}

// portalFind is FIND of the search methods: at least three characters wants.
func (s *Server) portalFind(c portalCall) (string, error) {
	find := strings.TrimSpace(portalStr(portalParam(c.in, "FIND")))
	if len([]rune(find)) < 3 {
		return "", s.portalErr(c.r, http.StatusBadRequest, "FIND_SHORT", "err.portal_find")
	}
	return find, nil
}

// im.search.user.list: active employees whose name, e-mail or position has FIND → {number: the employee in the
// messenger's fields}, by name, a page of 50 from OFFSET.
func (s *Server) portalSearchUsers(c portalCall) (portalOut, error) {
	find, err := s.portalFind(c)
	if err != nil {
		return portalOut{}, err
	}
	offset, _ := strconv.Atoi(portalStr(portalParam(c.in, "OFFSET")))
	rows, err := s.DB.Query(c.r.Context(), `SELECT id::text FROM users WHERE active AND (NOT guest OR $3)
		AND (name ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%' OR position ILIKE '%' || $1 || '%')
		ORDER BY name, num OFFSET $2 LIMIT 50`, find, max(offset, 0), s.apiAdmin(c.r.Context(), c.p.UserID))
	if err != nil {
		return portalOut{}, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return portalOut{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return portalOut{}, err
	}
	users, err := s.portalIMUsers(c, ids)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	for n, u := range users {
		out[strconv.FormatInt(n, 10)] = u
	}
	return portalOut{Result: out}, nil
}

// im.search.chat.list: the caller's group chats whose name has FIND → {number: {id, name, owner, type, avatar}}.
func (s *Server) portalSearchChats(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	find, err := s.portalFind(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	rooms, err := s.Matrix.Rooms(ctx, c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	needle := strings.ToLower(find)
	for _, rm := range rooms {
		if rm.Kind == "dm" || rm.Kind == "notes" || !strings.Contains(strings.ToLower(rm.Name), needle) {
			continue
		}
		num, err := s.portalChatNum(ctx, rm.ID)
		if err != nil {
			return portalOut{}, err
		}
		typ := "chat"
		if rm.Kind == "channel" {
			typ = "channel"
		}
		out[strconv.FormatInt(num, 10)] = map[string]any{"id": num, "name": rm.Name, "type": typ, "avatar": "",
			"color": "", "entity_type": rm.Kind}
	}
	return portalOut{Result: out}, nil
}
