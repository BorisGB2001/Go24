package web

import (
	"context"
	"errors"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// ---------- «Звонки» (LiveKit) ----------

// callRoom finds what the call is about and checks the person may take part: a task (whoever sees it) or a chat
// of the messenger (its members). It returns the LiveKit room, the title and where the call is announced.
func (s *Server) callRoom(r *http.Request, sess auth.Session) (room, title, back string, announce func(string) error, err error) {
	q := r.URL.Query()
	if key := q.Get("task"); key != "" {
		id, err := s.Tasks.TaskIDByKey(r.Context(), key)
		if err != nil {
			return "", "", "", nil, err
		}
		card, err := s.Tasks.GetCard(r.Context(), sess.UserID, id)
		if err != nil {
			return "", "", "", nil, err
		}
		return "task-" + card.Key(), card.Key() + " " + card.Title, "/t/" + card.Key(), func(body string) error {
			_, err := s.postComment(r, sess, id, body)
			return err
		}, nil
	}
	if id := q.Get("event"); id != "" { // a video meeting of the calendar: whoever sees the meeting
		e, err := s.Calendar.Get(r.Context(), sess.UserID, id)
		if err != nil {
			return "", "", "", nil, err
		}
		if !e.Video || e.Hidden {
			return "", "", "", nil, tasks.ErrForbidden
		}
		return "event-" + e.ID, e.Title, "/calendar/" + e.ID, func(string) error { return nil }, nil
	}
	if chat := q.Get("room"); chat != "" && s.Matrix != nil {
		rooms, err := s.Matrix.Rooms(r.Context(), sess.UserID)
		if err != nil {
			return "", "", "", nil, err
		}
		for _, rm := range rooms {
			if rm.ID == chat {
				return imCallRoom(chat), rm.Name, "/im?room=" + url.QueryEscape(chat), func(body string) error {
					_, err := s.Matrix.Send(r.Context(), sess.UserID, chat, body, "")
					return err
				}, nil
			}
		}
		return "", "", "", nil, tasks.ErrForbidden
	}
	return "", "", "", nil, i18n.E("err.bad_form")
}

// callPage joins a call; announce=1 also tells the task chat or the messenger chat, with the link to join.
// Camera and microphone need a secure page, so a plain-http visit goes to the https address of go24 first.
func (s *Server) callPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Calls == nil {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	if s.CallURL != "" && r.Header.Get("X-Forwarded-Proto") != "https" {
		http.Redirect(w, r, s.CallURL+r.URL.RequestURI(), http.StatusFound)
		return
	}
	room, title, back, announce, err := s.callRoom(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	join := r.URL.Query()
	join.Del("announce")
	join.Del("ring")
	join.Del("r")
	if r.URL.Query().Get("ring") == "1" && s.Rings != nil && r.URL.Query().Get("room") != "" { // a call from a chat rings its people
		ring, err := s.startRing(r, sess, r.URL.Query().Get("room"), r.URL.Query().Get("video") != "0", joinLink(s, join))
		if err != nil {
			s.Log.Warn("call ring", "err", err)
		}
		next := join
		if ring != "" {
			next = r.URL.Query()
			next.Del("ring")
			next.Set("r", ring)
		}
		http.Redirect(w, r, "/call?"+next.Encode(), http.StatusSeeOther) // a reload does not ring again
		return
	}
	link := joinLink(s, join)
	chat := r.URL.Query().Get("room")
	if chat != "" && s.CallLines != nil && s.Matrix != nil { // the call's line in the chat stands for the announcement
		s.callJoined(r, sess, chat, room, r.URL.Query().Get("video") != "0")
		if r.URL.Query().Get("announce") == "1" {
			http.Redirect(w, r, "/call?"+join.Encode(), http.StatusSeeOther)
			return
		}
	}
	if r.URL.Query().Get("announce") == "1" {
		if err := announce(s.tr(r, "call.announce", sess.Name, link)); err != nil {
			s.Log.Warn("call announce", "err", err)
		}
		http.Redirect(w, r, "/call?"+join.Encode(), http.StatusSeeOther) // a reload does not announce again
		return
	}
	host := s.callHost(r, sess, room)
	token, err := s.Calls.Token(sess.UserID, sess.Name, room, host, time.Now())
	if err != nil {
		s.fail(w, "call token", err)
		return
	}
	s.Log.Info("call joined", "user_id", sess.UserID, "room", room)
	s.render(w, r, "call", page{Title: s.tr(r, "call.title") + " · " + title, Nav: "", Data: map[string]any{
		"URL": s.Calls.URL, "Token": token, "Name": title, "Back": back, "Link": link, "Base": s.BaseURL,
		"Video": r.URL.Query().Get("video") != "0", "Ring": r.URL.Query().Get("r"), "Chat": r.URL.Query().Get("room"),
		"RingMore": s.Rings != nil && s.Matrix != nil && r.URL.Query().Get("room") != "",
		"Guests":   s.CallGuests != nil && !tasks.IsGuestCtx(r.Context()), "Query": join.Encode(),
		"Host":     host && s.Calls.APIURL != ""}})
}

// callHost: the person leads the call, as «ведущий» — the creator, the responsible and the project's
// managers of a task, the organizer of a calendar meeting, who started the call of a chat now going on; an
// administrator in admin mode leads any call. A guest of a collab leads none.
func (s *Server) callHost(r *http.Request, sess auth.Session, room string) bool {
	ctx := r.Context()
	if tasks.IsGuestCtx(ctx) {
		return false
	}
	if sess.AdminActive() {
		return true
	}
	q := r.URL.Query()
	switch {
	case q.Get("task") != "":
		id, err := s.Tasks.TaskIDByKey(ctx, q.Get("task"))
		if err != nil {
			return false
		}
		card, err := s.Tasks.GetCard(ctx, sess.UserID, id)
		return err == nil && (card.Roles.Creator || card.Roles.Responsible || card.Roles.Manager)
	case q.Get("event") != "":
		e, err := s.Calendar.Get(ctx, sess.UserID, q.Get("event"))
		return err == nil && e.Mine(sess.UserID)
	case q.Get("room") != "" && s.CallLines != nil:
		by, err := s.CallLines.Starter(ctx, room)
		if err != nil {
			s.Log.Warn("call host", "room", room, "err", err)
		}
		return by == sess.UserID
	}
	return false
}

// callModerate (the host of the call, ?task=|event=|room=): action=mute turns off the microphone,
// the camera or the shared screen (what=) of the participant who= — or of everyone but the host when who is empty;
// action=remove puts the participant out of the call (a guest — for good). The person is told by the host's page.
func (s *Server) callModerate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if s.Calls == nil || s.Calls.APIURL == "" {
		s.jsonErr(w, r, i18n.E("err.calls_off"))
		return
	}
	room, _, _, _, err := s.callRoom(r, sess)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if !s.callHost(r, sess, room) {
		s.jsonErr(w, r, tasks.ErrForbidden)
		return
	}
	who := r.FormValue("who")
	switch r.FormValue("action") {
	case "mute":
		what := r.FormValue("what")
		if what != calls.Microphone && what != calls.Camera && what != calls.Screen {
			s.jsonErr(w, r, i18n.E("err.bad_form"))
			return
		}
		n, err := s.Calls.Mute(ctx, room, who, what, sess.UserID)
		if err != nil {
			s.Log.Error("call mute", "room", room, "err", err)
			s.jsonErr(w, r, i18n.E("call.host_failed"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"muted": n})
	case "remove":
		if who == "" || who == sess.UserID {
			s.jsonErr(w, r, i18n.E("err.bad_form"))
			return
		}
		if id, ok := strings.CutPrefix(who, calls.GuestIdentity("")); ok && s.CallGuests != nil {
			if err := s.CallGuests.Expel(ctx, room, id, sess.UserID); err != nil {
				s.jsonErr(w, r, err)
				return
			}
		}
		if err := s.Calls.RemoveParticipant(ctx, room, who); err != nil {
			s.Log.Error("call remove", "room", room, "err", err)
			s.jsonErr(w, r, i18n.E("call.host_failed"))
			return
		}
		s.Log.Info("call participant removed", "room", room, "who", who, "by", sess.UserID)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		s.jsonErr(w, r, i18n.E("err.bad_form"))
	}
}

// joinLink is the address to join the call (go24 over https when calls have their own address).
func joinLink(s *Server, join url.Values) string {
	base := s.CallURL
	if base == "" {
		base = s.BaseURL
	}
	return base + "/call?" + join.Encode()
}

// imCallRoom is the LiveKit room of a messenger chat's call.
func imCallRoom(chat string) string {
	h := sha256.Sum256([]byte(chat))
	return "im-" + hex.EncodeToString(h[:8])
}

// startRing rings the people of the chat (but the caller and those in except): «Звонит …» on any page of theirs.
// It gives the ring.
func (s *Server) startRing(r *http.Request, sess auth.Session, room string, video bool, url string, except ...string) (string, error) {
	ctx := r.Context()
	members, err := s.Matrix.ChatMembers(ctx, sess.UserID, room)
	if err != nil {
		return "", err
	}
	var people []string
	for _, m := range members {
		if m.ID != "" && m.ID != sess.UserID && !slices.Contains(except, m.ID) {
			people = append(people, m.ID)
		}
	}
	if len(people) == 0 {
		return "", nil
	}
	ring, err := s.Rings.Start(ctx, sess.UserID, room, video, people)
	if err != nil {
		return "", err
	}
	ev := s.ringEvent(ctx, ring)
	ev.URL = url
	if len(members) > 2 { // a group chat: which one
		if rooms, err := s.Matrix.Rooms(ctx, sess.UserID); err == nil {
			for _, rm := range rooms {
				if rm.ID == room {
					ev.Chat = rm.Name
				}
			}
		}
	}
	for _, p := range people {
		if err := s.Rings.Notify(ctx, p, ev); err != nil {
			s.Log.Warn("call ring", "user", p, "err", err)
		}
	}
	return ring.ID, nil
}

// ringEvent is the ring as the people rung see it: who calls, audio or video, how long it still rings.
func (s *Server) ringEvent(ctx context.Context, ring calls.Ring) calls.Event {
	ev := calls.Event{Ring: ring.ID, Kind: "ring", Video: ring.Video, Left: int((calls.RingTime - time.Since(ring.At)).Seconds())}
	var photo string
	if err := s.DB.QueryRow(ctx, `SELECT name, photo_key FROM users WHERE id = $1`, ring.CallerID).Scan(&ev.From, &photo); err == nil {
		ev.Photo = company.PhotoURL(ring.CallerID, photo)
	}
	q := url.Values{"room": {ring.Room}, "video": {"0"}}
	if ring.Video {
		q.Set("video", "1")
	}
	ev.URL = joinLink(s, q)
	return ev
}

// callUser calls a colleague from their card: their private chat, ringing (video=0 — sound only).
func (s *Server) callUser(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Matrix == nil || s.Calls == nil {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	room, err := s.Matrix.DirectChat(r.Context(), sess.UserID, r.URL.Query().Get("user"))
	if err != nil {
		s.taskErr(w, r, err, "/u/"+r.URL.Query().Get("user"))
		return
	}
	video := "1"
	if r.URL.Query().Get("video") == "0" {
		video = "0"
	}
	http.Redirect(w, r, "/call?"+url.Values{"room": {room}, "ring": {"1"}, "video": {video}}.Encode(), http.StatusSeeOther)
}

// callRing: the person rung answers (action accept | decline), or the caller stops the ringing (end): the caller
// learns each answer; whoever has not answered by the end missed the call.
func (s *Server) callRing(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Rings == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	ring, err := s.Rings.Get(ctx, sess.UserID, r.PathValue("id"))
	if err != nil {
		s.jsonErr(w, r, i18n.E("err.not_found"))
		return
	}
	switch action := r.PathValue("action"); {
	case (action == "accept" || action == "decline") && ring.CallerID != sess.UserID:
		answer := map[string]string{"accept": "accepted", "decline": "declined"}[action]
		ok, err := s.Rings.Answer(ctx, sess.UserID, ring.ID, answer)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		if !ok {
			s.jsonErr(w, r, i18n.E("call.ring_over"))
			return
		}
		_ = s.Rings.Notify(ctx, sess.UserID, calls.Event{Ring: ring.ID, Kind: "stop"}) // their other tabs and devices
		_ = s.Rings.Notify(ctx, ring.CallerID, calls.Event{Ring: ring.ID, Kind: answer, Person: sess.Name})
		if len(ring.People) == 1 { // a private chat: nobody else to ring
			_, _ = s.Rings.End(ctx, ring.ID)
		}
	case action == "end" && ring.CallerID == sess.UserID:
		missed, err := s.Rings.End(ctx, ring.ID)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		for _, p := range missed {
			_ = s.Rings.Notify(ctx, p, calls.Event{Ring: ring.ID, Kind: "stop"})
		}
	default:
		s.jsonErr(w, r, i18n.E("err.forbidden"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// callJoined counts the person into the chat's call; the first one starts it — its line goes into the chat.
func (s *Server) callJoined(r *http.Request, sess auth.Session, chat, room string, video bool) {
	ctx := r.Context()
	cs, isNew, err := s.CallLines.Join(ctx, room, chat, sess.UserID, video)
	if err != nil || !isNew {
		if err != nil {
			s.Log.Warn("call line", "err", err)
		}
		return
	}
	line := matrixsync.CallLine{State: "started", Video: video, People: []string{sess.Name}}
	id, err := s.Matrix.SendCall(ctx, sess.UserID, chat, s.callText(line), line)
	if err == nil {
		err = s.CallLines.SetLine(ctx, cs.ID, id)
	}
	if err != nil {
		s.Log.Warn("call line", "err", err)
	}
}

// callText is the call's line for clients other than go24 (Element), in the company's language.
func (s *Server) callText(l matrixsync.CallLine) string {
	lang := s.I18n.Match("")
	switch l.State {
	case "started":
		return s.I18n.T(lang, "call.line_started", l.By())
	case "missed":
		return s.I18n.T(lang, "call.line_missed", l.By())
	}
	dur := s.I18n.T(lang, "call.dur_mins", l.Mins())
	if l.Secs < 60 { // as the template's callDur
		dur = s.I18n.T(lang, "call.dur_secs", l.Secs)
	}
	return s.I18n.T(lang, "call.line_ended", dur, strings.Join(l.People, ", "))
}

// CallLinesLoop (go24-worker) ends the calls of chats nobody is in any more: their lines tell how long they went on
// and who took part, or that nobody came.
func (s *Server) CallLinesLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.endCalls(ctx)
		}
	}
}

func (s *Server) endCalls(ctx context.Context) {
	going, err := s.CallLines.Going(ctx)
	if err != nil {
		s.Log.Warn("call lines", "err", err)
		return
	}
	for _, cs := range going {
		if time.Since(cs.Started) < 40*time.Second { // the first one is still connecting
			continue
		}
		in, err := s.Calls.Participants(ctx, cs.Room)
		if err != nil || len(in) > 0 {
			if err != nil {
				s.Log.Warn("call participants", "room", cs.Room, "err", err)
			}
			continue
		}
		done, ok, err := s.CallLines.Finish(ctx, cs.ID)
		if err != nil || !ok || done.Event == "" {
			continue
		}
		line := matrixsync.CallLine{State: "ended", Video: done.Video, Secs: int(done.Ended.Sub(done.Started).Seconds())}
		rows, err := s.DB.Query(ctx, `SELECT u.name FROM unnest($1::uuid[]) WITH ORDINALITY p(id, n) JOIN users u ON u.id = p.id ORDER BY p.n`, done.People)
		if err == nil {
			line.People, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if err != nil {
			s.Log.Warn("call lines", "err", err)
		}
		if len(done.People) < 2 {
			line.State = "missed"
		}
		if err := s.Matrix.EditCall(ctx, done.StartedBy, done.Chat, done.Event, s.callText(line), line); err != nil {
			s.Log.Warn("call line end", "room", done.Chat, "err", err)
		}
	}
}

// callRingMore rings the chat's people who are not in its call yet («Позвонить остальным» on the call page).
func (s *Server) callRingMore(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	chat := r.FormValue("room")
	if s.Rings == nil || s.Matrix == nil || chat == "" {
		s.jsonErr(w, r, i18n.E("err.calls_off"))
		return
	}
	in, err := s.Calls.Participants(r.Context(), imCallRoom(chat))
	if err != nil && !errors.Is(err, calls.ErrNoAPI) {
		s.Log.Warn("call participants", "err", err)
	}
	q := url.Values{"room": {chat}, "video": {"1"}}
	if r.FormValue("video") == "0" {
		q.Set("video", "0")
	}
	if _, err := s.startRing(r, sess, chat, q.Get("video") == "1", joinLink(s, q), in...); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
