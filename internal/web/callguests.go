package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- Guest links of calls, as the guest entry: /call/g/{token} for anyone, no sign-in ----------

// guestLinkURL is the address a guest opens (go24 over https when calls have their own address).
func (s *Server) guestLinkURL(token string) string {
	base := s.CallURL
	if base == "" {
		base = s.BaseURL
	}
	return base + "/call/g/" + token
}

// callGuestQuery is the call of the request as /call?… has it (task=, event= or room=, and video=).
func callGuestQuery(r *http.Request) string {
	q := url.Values{}
	for _, k := range []string{"task", "event", "room", "video"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	return q.Encode()
}

type guestLinkJSON struct {
	ID       string    `json:"id"`
	URL      string    `json:"url"`
	Expires  time.Time `json:"expires"`
	Password bool      `json:"password"`
	Lobby    bool      `json:"lobby"`
}

// callGuests (the people of the call, ?task=|event=|room=): the call's guest links and the guests in its lobby; POST
// makes a link — ttl (hours: 1, 24 or 168), password, lobby=1.
func (s *Server) callGuests(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.CallGuests == nil || s.Calls == nil || tasks.IsGuestCtx(r.Context()) { // a guest of a collab invites nobody
		s.jsonErr(w, r, i18n.E("err.calls_off"))
		return
	}
	room, title, _, _, err := s.callRoom(r, sess)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		hours, _ := strconv.Atoi(r.FormValue("ttl"))
		ttl := calls.GuestLinkTTLs[0]
		for _, d := range calls.GuestLinkTTLs {
			if d == time.Duration(hours)*time.Hour {
				ttl = d
			}
		}
		l, err := s.CallGuests.CreateLink(ctx, sess.UserID, room, title, callGuestQuery(r), r.FormValue("password"), r.FormValue("lobby") == "1", ttl)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		s.Log.Info("call guest link", "user_id", sess.UserID, "room", room)
		writeJSON(w, http.StatusCreated, guestLinkJSON{ID: l.ID, URL: s.guestLinkURL(l.Token), Expires: l.Expires, Password: l.Password, Lobby: l.Lobby})
		return
	}
	links, err := s.CallGuests.Links(ctx, room)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	waiting, err := s.CallGuests.Waiting(ctx, room)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := struct {
		Links   []guestLinkJSON     `json:"links"`
		Waiting []map[string]string `json:"waiting"`
	}{Links: []guestLinkJSON{}, Waiting: []map[string]string{}}
	for _, l := range links {
		out.Links = append(out.Links, guestLinkJSON{ID: l.ID, URL: s.guestLinkURL(l.Token), Expires: l.Expires, Password: l.Password, Lobby: l.Lobby})
	}
	for _, g := range waiting {
		out.Waiting = append(out.Waiting, map[string]string{"id": g.ID, "name": g.Name})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// callGuestRevoke switches a link of the call off; the guests it let in leave the call.
func (s *Server) callGuestRevoke(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.CallGuests == nil || s.Calls == nil {
		s.jsonErr(w, r, i18n.E("err.calls_off"))
		return
	}
	room, _, _, _, err := s.callRoom(r, sess)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	guests, err := s.CallGuests.Revoke(r.Context(), room, r.PathValue("id"))
	if errors.Is(err, calls.ErrGuestGone) {
		err = i18n.E("err.not_found")
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	for _, g := range guests {
		if err := s.Calls.RemoveParticipant(r.Context(), room, calls.GuestIdentity(g)); err != nil && !errors.Is(err, calls.ErrNoAPI) {
			s.Log.Warn("call guest remove", "room", room, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// callLobby: someone of the call lets the waiting guest in (admit) or not (deny).
func (s *Server) callLobby(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	action := r.PathValue("action")
	if s.CallGuests == nil || action != "admit" && action != "deny" {
		http.NotFound(w, r)
		return
	}
	room, _, _, _, err := s.callRoom(r, sess)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	ok, err := s.CallGuests.Decide(r.Context(), room, r.PathValue("id"), sess.UserID, action == "admit")
	if err == nil && !ok {
		err = i18n.E("call.guest_decided")
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func guestCookie(token string) string { return "go24cg_" + token }

// guestOpen finds the link of the request and the guest whose browser came by it before ("" — none yet).
func (s *Server) guestOpen(w http.ResponseWriter, r *http.Request) (calls.GuestLink, calls.Guest, bool) {
	var g calls.Guest
	if s.CallGuests == nil || s.Calls == nil {
		http.NotFound(w, r)
		return calls.GuestLink{}, g, false
	}
	l, err := s.CallGuests.Open(r.Context(), r.PathValue("token"))
	if err != nil {
		if !errors.Is(err, calls.ErrGuestGone) {
			s.fail(w, "call guest link", err)
			return l, g, false
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "callguest", page{TitleKey: "call.guest_gone", Data: map[string]any{"Gone": true}})
		return l, g, false
	}
	if c, err := r.Cookie(guestCookie(l.Token)); err == nil {
		if id, secret, ok := strings.Cut(c.Value, "."); ok {
			g, _ = s.CallGuests.Get(r.Context(), l, id, secret)
		}
	}
	return l, g, true
}

// guestPage is what a guest link opens: the name (and the password) to give, then the lobby until someone lets the
// guest in, then the call itself; ?left=1 — the guest has left it.
func (s *Server) guestPage(w http.ResponseWriter, r *http.Request) {
	if s.CallURL != "" && r.Header.Get("X-Forwarded-Proto") != "https" { // camera and microphone need a secure page
		http.Redirect(w, r, s.CallURL+r.URL.RequestURI(), http.StatusFound)
		return
	}
	l, g, ok := s.guestOpen(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	data := map[string]any{"Token": l.Token, "Title": l.Title, "Password": l.Password, "Expires": l.Expires}
	switch {
	case g.ID == "":
		data["Ask"] = true
	case g.State == calls.GuestDenied:
		data["Denied"] = true
	case g.State == calls.GuestWaiting:
		data["Waiting"] = true
	case r.URL.Query().Get("left") == "1":
		data["Left"] = true
	default:
		name := g.Name + " (" + s.tr(r, "call.guest") + ")"
		token, err := s.Calls.Token(calls.GuestIdentity(g.ID), name, l.Room, time.Now())
		if err != nil {
			s.fail(w, "call token", err)
			return
		}
		s.Log.Info("call guest joined", "guest", g.ID, "room", l.Room)
		video := !strings.Contains(l.Query, "video=0")
		s.render(w, r, "call", page{Title: s.tr(r, "call.title") + " · " + l.Title, Data: map[string]any{
			"URL": s.Calls.URL, "Token": token, "Name": l.Title, "Back": "/call/g/" + l.Token + "?left=1", "Video": video, "Guest": true}})
		return
	}
	s.render(w, r, "callguest", page{Title: s.tr(r, "call.title") + " · " + l.Title, Data: data})
}

// guestArrive takes the guest's name (and the link's password): they wait in the lobby, or come in at once when the
// link has none.
func (s *Server) guestArrive(w http.ResponseWriter, r *http.Request) {
	l, _, ok := s.guestOpen(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if utf8.RuneCountInString(name) > 60 {
		name = string([]rune(name)[:60])
	}
	if name == "" || !l.CheckPassword(r.FormValue("password")) {
		if name != "" {
			time.Sleep(time.Second) // guessing is slow
		}
		w.Header().Set("Cache-Control", "no-store")
		s.render(w, r, "callguest", page{Title: s.tr(r, "call.title") + " · " + l.Title, Data: map[string]any{
			"Token": l.Token, "Title": l.Title, "Password": l.Password, "Expires": l.Expires, "Ask": true, "Name": name,
			"Wrong": name != ""}})
		return
	}
	g, secret, err := s.CallGuests.Arrive(r.Context(), l, name)
	if err != nil {
		s.fail(w, "call guest", err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: guestCookie(l.Token), Value: g.ID + "." + secret, Path: "/call/g/" + l.Token,
		MaxAge: int(time.Until(l.Expires).Seconds()) + 1, HttpOnly: true, Secure: r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/call/g/"+l.Token, http.StatusSeeOther)
}

// guestState is the lobby asking whether the guest is let in yet.
func (s *Server) guestState(w http.ResponseWriter, r *http.Request) {
	_, g, ok := s.guestOpen(w, r)
	if !ok {
		return
	}
	state := g.State
	if g.ID == "" {
		state = "none"
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"state": state})
}
