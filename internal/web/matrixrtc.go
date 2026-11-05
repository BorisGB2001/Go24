package web

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/matrix"
	"go24/internal/tasks"
)

// ---------- Element Call (MatrixRTC) on the calls of go24 ----------

// A call of a chat in Element (web, Element X) asks its LiveKit token of the room's «focus» — the service Synapse
// names in its MatrixRTC transports (MSC4143): go24 itself. go24 checks the OpenID token with Synapse and that the
// person is in the chat, and lets them into the very LiveKit room of the chat's call in go24 (imCallRoom), so
// people in Element and in go24 are in one call.

// rtcRequest is the legacy request of Element Call (lk-jwt-service's /sfu/get).
type rtcRequest struct {
	Room        string `json:"room"`
	DeviceID    string `json:"device_id"`
	OpenIDToken struct {
		AccessToken      string `json:"access_token"`
		MatrixServerName string `json:"matrix_server_name"`
	} `json:"openid_token"`
	DelayID string `json:"delay_id"` // the leave event to send for the person when they drop out — go24 does not
}

// rtcCORS: Element Call runs on Element's own address.
func rtcCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func rtcErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"errcode": code, "error": msg})
}

// rtcOptions answers the browser's preflight.
func (s *Server) rtcOptions(w http.ResponseWriter, r *http.Request) {
	rtcCORS(w)
	w.WriteHeader(http.StatusNoContent)
}

// rtcGetToken is the newer request (MSC4195 /get_token): its LiveKit identity is a hash of a membership go24 does not
// publish, so Element Call is told to use /sfu/get (it falls back on 404).
func (s *Server) rtcGetToken(w http.ResponseWriter, r *http.Request) {
	rtcCORS(w)
	rtcErr(w, http.StatusNotFound, "M_NOT_FOUND", "use /sfu/get")
}

// rtcSFUGet gives Element Call the LiveKit address and token of the chat's call: identity «@user:server:DEVICE», as
// the other Element clients find the person's tracks by their m.call.member; the name is the employee's in go24.
func (s *Server) rtcSFUGet(w http.ResponseWriter, r *http.Request) {
	rtcCORS(w)
	if s.Calls == nil || s.Matrix == nil {
		rtcErr(w, http.StatusServiceUnavailable, "M_UNKNOWN", "calls are off")
		return
	}
	var in rtcRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Room == "" ||
		in.DeviceID == "" || in.OpenIDToken.AccessToken == "" {
		rtcErr(w, http.StatusBadRequest, "M_BAD_JSON", "room, device_id and openid_token are needed")
		return
	}
	if in.DelayID != "" { // Element Call then keeps its delayed leave event going itself
		rtcErr(w, http.StatusBadRequest, "M_BAD_JSON", "delayed events are not delegated")
		return
	}
	mx := s.Matrix.M
	if in.OpenIDToken.MatrixServerName != mx.ServerName {
		rtcErr(w, http.StatusForbidden, "M_FORBIDDEN", "another server")
		return
	}
	mxid, err := mx.OpenIDUser(r.Context(), in.OpenIDToken.AccessToken)
	if err != nil {
		if e, ok := err.(*matrix.Error); ok && e.Status < 500 {
			rtcErr(w, http.StatusUnauthorized, "M_UNAUTHORIZED", "bad OpenID token")
			return
		}
		s.Log.Warn("matrixrtc openid", "err", err)
		rtcErr(w, http.StatusBadGateway, "M_UNKNOWN", "the Matrix server does not answer")
		return
	}
	userID, name, err := s.rtcUser(r.Context(), mxid, in.Room)
	if err != nil {
		rtcErr(w, http.StatusForbidden, "M_FORBIDDEN", "not in the chat")
		return
	}
	room := imCallRoom(in.Room)
	token, err := s.Calls.Token(mxid+":"+in.DeviceID, name, room, false, time.Now())
	if err != nil {
		s.Log.Warn("matrixrtc token", "err", err)
		rtcErr(w, http.StatusInternalServerError, "M_UNKNOWN", "no token")
		return
	}
	if s.CallLines != nil { // the call goes on in go24 as well: its line in the chat, its people
		s.callJoined(r.Context(), userID, name, in.Room, room, true)
	}
	s.Log.Info("call joined", "user_id", userID, "room", room, "client", "element")
	writeJSON(w, http.StatusOK, map[string]string{"url": s.Calls.URL, "jwt": token})
}

// rtcUser is the active employee (or collab guest) of the Matrix ID, who is in the chat.
func (s *Server) rtcUser(ctx context.Context, mxid, chat string) (string, string, error) {
	var userID, name string
	if err := s.DB.QueryRow(ctx, `SELECT u.id::text, u.name FROM matrix_users m JOIN users u ON u.id = m.user_id
		WHERE m.mxid = $1 AND u.active`, mxid).Scan(&userID, &name); err != nil {
		return "", "", err
	}
	state, err := s.Matrix.M.AdminState(ctx, chat)
	if err != nil {
		return "", "", err
	}
	for _, ev := range state {
		if ev.Type == "m.room.member" && ev.StateKey != nil && *ev.StateKey == mxid && ev.Content["membership"] == "join" {
			return userID, name, nil
		}
	}
	return "", "", tasks.ErrForbidden
}

// rtcFocus is go24's MatrixRTC service as Element Call reaches it (https: Element runs in a secure page).
func (s *Server) rtcFocus() string {
	base := s.CallURL
	if base == "" {
		base = s.BaseURL
	}
	return base + "/matrixrtc"
}

// elementCallJoin puts the person into the chat's call for Element (their m.call.member) and gives their LiveKit
// identity there; without a Matrix account — their go24 id, as in the calls of tasks.
func (s *Server) elementCallJoin(ctx context.Context, userID, chat, room string, video bool) string {
	identity, err := s.Matrix.CallIdentity(ctx, userID)
	if err != nil {
		return userID
	}
	if err := s.Matrix.JoinCall(ctx, userID, chat, s.rtcFocus(), room, video, time.Now()); err != nil {
		s.Log.Warn("element call member", "room", chat, "err", err)
	}
	return identity
}

// callUsers are the go24 users of the LiveKit identities of a call: their own id, or the person of «@user:server:DEVICE»
// (Element Call, go24's call page of a chat).
func (s *Server) callUsers(ctx context.Context, identities []string) []string {
	var out, mxids []string
	for _, id := range identities {
		if i := strings.LastIndex(id, ":"); strings.HasPrefix(id, "@") && i > 0 {
			mxids = append(mxids, id[:i])
		} else {
			out = append(out, id)
		}
	}
	if len(mxids) == 0 {
		return out
	}
	rows, err := s.DB.Query(ctx, `SELECT user_id::text FROM matrix_users WHERE mxid = ANY($1)`, mxids)
	if err == nil {
		var ids []string
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		out = append(out, ids...)
	}
	if err != nil {
		s.Log.Warn("call users", "err", err)
	}
	return out
}

// leftElementCall: those who left go24's call page of the chat (not in LiveKit's room any more, in) are out of the
// call for Element too. Someone who has just opened the page is still connecting — unless the call is over.
func (s *Server) leftElementCall(ctx context.Context, chat string, in []string) {
	members, err := s.Matrix.CallMembers(ctx, chat)
	if err != nil {
		s.Log.Warn("element call members", "room", chat, "err", err)
		return
	}
	for _, m := range members {
		if slices.Contains(in, m.Identity) || (len(in) > 0 && time.Since(m.Since) < time.Minute) {
			continue
		}
		if err := s.Matrix.LeaveCall(ctx, m.UserID, chat); err != nil {
			s.Log.Warn("element call leave", "room", chat, "err", err)
		}
	}
}
