package matrixsync

import (
	"context"
	"time"

	"go24/internal/matrix"
)

// allowCalls lets the members of every room of the server take part in its calls from Element (Element Call), as
// the rooms go24 creates now do: once at start, rooms already so are left alone. The levels are changed by the bot
// when it may, otherwise by an employee in the room who may (the owner of a group chat).
func (s *Service) allowCalls(ctx context.Context) {
	bot, err := s.M.WhoAmI(ctx)
	if err != nil {
		s.Log.Warn("matrix: element call levels", "err", err)
		return
	}
	changed := 0
	for from := ""; ; {
		rooms, next, err := s.M.AdminRooms(ctx, from)
		if err != nil {
			s.Log.Warn("matrix: element call levels", "err", err)
			return
		}
		for _, room := range rooms {
			ok, err := s.allowCallsIn(ctx, bot, room)
			if err != nil {
				s.Log.Warn("matrix: element call levels", "room", room, "err", err)
			}
			if ok {
				changed++
			}
		}
		if next == "" {
			break
		}
		from = next
	}
	if changed > 0 {
		s.Log.Info("matrix: calls from Element allowed", "rooms", changed)
	}
}

func (s *Service) allowCallsIn(ctx context.Context, bot, room string) (bool, error) {
	state, err := s.M.AdminState(ctx, room)
	if err != nil {
		return false, err
	}
	var pl matrix.PowerLevels
	var joined []string
	for _, ev := range state {
		switch {
		case ev.Type == "m.room.power_levels" && ev.StateKey != nil && *ev.StateKey == "":
			pl = ev.Content
		case ev.Type == "m.room.member" && ev.StateKey != nil && ev.Content["membership"] == "join":
			joined = append(joined, *ev.StateKey)
		}
	}
	if pl == nil || !matrix.AllowCalls(pl) {
		return false, nil
	}
	need := plLevel(pl, "state_default", 50)
	if v, ok := plEvents(pl)["m.room.power_levels"].(float64); ok {
		need = int(v)
	}
	for _, mx := range joined {
		if pl.Level(mx) < need {
			continue
		}
		if mx == bot {
			return true, s.M.SetState(ctx, room, "m.room.power_levels", "", pl)
		}
		var userID string
		if s.DB.QueryRow(ctx, `SELECT m.user_id::text FROM matrix_users m JOIN users u ON u.id = m.user_id
			WHERE m.mxid = $1 AND u.active`, mx).Scan(&userID) != nil {
			continue
		}
		if _, tok, err := s.token(ctx, userID); err == nil {
			return true, s.M.SetStateAs(ctx, tok, room, "m.room.power_levels", "", pl)
		}
	}
	return false, nil // nobody go24 may act as: Element's own «Включить звонки» is left to the room's admins
}

// CallDevice is go24's call page as a device of the person in Element Call: in LiveKit they are «@user:server:GO24WEB»,
// so Element finds their tracks by their m.call.member.
const CallDevice = "GO24WEB"

// callMemberKey is the state key of the person's membership in the call of the room (Element Call's form).
func callMemberKey(mxid string) string { return "_" + mxid + "_" + CallDevice + "_m.call" }

// CallIdentity is the person's identity in the LiveKit room of a chat's call.
func (s *Service) CallIdentity(ctx context.Context, userID string) (string, error) {
	mx, _, err := s.token(ctx, userID)
	if err != nil {
		return "", err
	}
	return mx + ":" + CallDevice, nil
}

// JoinCall tells Element the person is in the chat's call from go24: their m.call.member, with go24 as the focus
// (focusURL — its MatrixRTC service) and the LiveKit room alias.
func (s *Service) JoinCall(ctx context.Context, userID, room, focusURL, alias string, video bool, now time.Time) error {
	mx, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	intent := "audio"
	if video {
		intent = "video"
	}
	return forbidden(s.M.SetStateAs(ctx, tok, room, matrix.CallMemberType, callMemberKey(mx), map[string]any{
		"application": "m.call", "call_id": "", "scope": "m.room", "device_id": CallDevice,
		"created_ts": now.UnixMilli(), "expires": int64(4 * time.Hour / time.Millisecond), "m.call.intent": intent,
		"focus_active":   map[string]any{"type": "livekit", "focus_selection": "oldest_membership"},
		"foci_preferred": []map[string]any{{"type": "livekit", "livekit_service_url": focusURL, "livekit_alias": alias}},
	}))
}

// CallMember is the m.call.member of someone in go24's call page.
type CallMember struct {
	UserID, Identity string
	Since            time.Time
}

// CallMembers are the people the room's state shows in its call from go24.
func (s *Service) CallMembers(ctx context.Context, room string) ([]CallMember, error) {
	state, err := s.M.AdminState(ctx, room)
	if err != nil {
		return nil, err
	}
	var out []CallMember
	for _, ev := range state {
		if ev.Type != matrix.CallMemberType || ev.StateKey == nil || ev.Content["device_id"] != CallDevice {
			continue
		}
		mx := ev.Sender
		if *ev.StateKey != callMemberKey(mx) {
			continue
		}
		m := CallMember{Identity: mx + ":" + CallDevice}
		if ts, ok := ev.Content["created_ts"].(float64); ok {
			m.Since = time.UnixMilli(int64(ts))
		}
		if s.DB.QueryRow(ctx, `SELECT user_id::text FROM matrix_users WHERE mxid = $1`, mx).Scan(&m.UserID) != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// LeaveCall: the person is no longer in the chat's call from go24 (their m.call.member is emptied, as Element's).
func (s *Service) LeaveCall(ctx context.Context, userID, room string) error {
	mx, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	return forbidden(s.M.SetStateAs(ctx, tok, room, matrix.CallMemberType, callMemberKey(mx), map[string]any{}))
}
