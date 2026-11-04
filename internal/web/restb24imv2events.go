package web

import (
	"strconv"

	"go24/internal/matrixsync"
)

// REST compatible with the portal, the events of im.v2 (GO24DEV-966): im.v2.Event.subscribe, unsubscribe, get — the
// caller's chats as a log of events (matrixsync/imv2events.go), taken by polling.

func init() {
	portalMethods["im.v2.event.subscribe"] = portalMethod{Scope: "im:read", Run: func(s *Server, c portalCall) (portalOut, error) {
		return s.portalIMV2Subscribe(c, true)
	}}
	portalMethods["im.v2.event.unsubscribe"] = portalMethod{Scope: "im:read", Run: func(s *Server, c portalCall) (portalOut, error) {
		return s.portalIMV2Subscribe(c, false)
	}}
	portalMethods["im.v2.event.get"] = portalMethod{Scope: "im:read", Run: (*Server).portalIMV2Events}
}

// im.v2.Event.subscribe, unsubscribe → true: go24 starts (stops) keeping the caller's events of im.v2.
func (s *Server) portalIMV2Subscribe(c portalCall, on bool) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	if err := s.Matrix.SubscribeV2(c.r.Context(), c.p.UserID, on); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// im.v2.Event.get: offset (the events below it are taken), limit (1–1000, 100) → {events, nextOffset, hasMore}.
func (s *Server) portalIMV2Events(c portalCall) (portalOut, error) {
	if err := s.portalNeedMatrix(c); err != nil {
		return portalOut{}, err
	}
	offset, _ := strconv.ParseInt(portalV2Str(c.in, "offset"), 10, 64)
	limit, _ := strconv.Atoi(portalV2Str(c.in, "limit"))
	if limit <= 0 {
		limit = 100
	}
	list, next, more, err := s.Matrix.EventsV2(c.r.Context(), c.p.UserID, offset, min(limit, 1000))
	if err != nil {
		return portalOut{}, err
	}
	if list == nil {
		list = []matrixsync.BotEvent{}
	}
	return portalOut{Result: map[string]any{"events": list, "nextOffset": next, "hasMore": more}}, nil
}
