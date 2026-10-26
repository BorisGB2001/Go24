package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/notify"
	"go24/internal/notifyout"
	"go24/internal/tasks"
)

// ---------- «Уведомления» (the bell) ----------

const notifyPage = 30

func (s *Server) notifyHTML(r *http.Request, name string, data any) (string, error) {
	var buf bytes.Buffer
	err := s.pages[s.lang(r)]["notifications"].ExecuteTemplate(&buf, name, data)
	return buf.String(), err
}

// notifications is the full list (/notifications).
func (s *Server) notifications(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	items, err := s.Notify.List(r.Context(), sess.UserID, before, notifyPage)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	more := ""
	if len(items) == notifyPage {
		more = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	s.render(w, r, "notifications", page{TitleKey: "notify.title", Nav: "notifications",
		Data: map[string]any{"Items": items, "More": more}})
}

// notifyPop is the bell pop-up: the newest notifications as HTML plus the unread count.
func (s *Server) notifyPop(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	items, err := s.Notify.List(r.Context(), sess.UserID, 0, 15)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	html, err := s.notifyHTML(r, "npop", items)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	n, _ := s.Notify.Unread(r.Context(), sess.UserID)
	writeJSON(w, http.StatusOK, map[string]any{"html": html, "unread": n})
}

// notifyRead marks all notifications read (or one: id=); a form goes back to the list.
func (s *Server) notifyRead(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error
	if key := r.FormValue("task"); key != "" { // the task's chat is open: its notifications are read
		var tid string
		if tid, err = s.Tasks.TaskIDByKey(r.Context(), key); err == nil {
			err = s.Notify.MarkTaskRead(r.Context(), sess.UserID, tid)
		}
	} else {
		id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
		err = s.Notify.MarkRead(r.Context(), sess.UserID, id)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if !wantsJSON(r) {
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
		return
	}
	n, _ := s.Notify.Unread(r.Context(), sess.UserID)
	writeJSON(w, http.StatusOK, map[string]any{"unread": n})
}

// notifyOpen reads a notification and goes where it points.
func (s *Server) notifyOpen(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	it, err := s.Notify.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if errors.Is(err, notify.ErrNotFound) {
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, "/notifications")
		return
	}
	if err := s.Notify.MarkRead(r.Context(), sess.UserID, it.ID); err != nil {
		s.Log.Error("notify read", "err", err)
	}
	http.Redirect(w, r, it.Link(), http.StatusSeeOther)
}

// notifyLive streams new notifications of the user (Server-Sent Events): the rendered item and the unread count.
// Pages keep one stream for all tabs of a browser (see menu.js), so the bell does not eat connections.
func (s *Server) notifyLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Live == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	events, stop := s.Live.Subscribe("n" + sess.UserID)
	defer stop()
	var mailNews <-chan string // new letters and changes of the mail (mail.News), not for a guest
	if s.Mail != nil && !tasks.IsGuestCtx(r.Context()) {
		var stopMail func()
		mailNews, stopMail = s.Live.Subscribe("m" + sess.UserID)
		defer stopMail()
	}
	var rings <-chan string // calls ringing this person, answers to their calls (calls.Rings), their softphone's line
	if s.Rings != nil || s.Telephony != nil {
		var stopRings func()
		rings, stopRings = s.Live.Subscribe("c" + sess.UserID)
		defer stopRings()
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	if s.Rings != nil { // a call that rings while the page was being opened
		if list, err := s.Rings.Ringing(r.Context(), sess.UserID); err == nil {
			for _, ring := range list {
				data, _ := json.Marshal(s.ringEvent(r.Context(), ring))
				fmt.Fprintf(w, "event: call\ndata: %s\n\n", data)
			}
		}
	}
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-rings:
			fmt.Fprintf(w, "event: call\ndata: %s\n\n", ev)
			flusher.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case news := <-mailNews:
			fmt.Fprintf(w, "event: mail\ndata: %s\n\n", s.mailNewsEvent(r, sess.UserID, news))
			flusher.Flush()
		case id := <-events:
			it, err := s.Notify.Get(r.Context(), sess.UserID, id)
			if err != nil {
				continue // already gone (e.g. the post was withdrawn)
			}
			html, err := s.notifyHTML(r, "nitem", it)
			if err != nil {
				s.Log.Error("notify render", "err", err)
				continue
			}
			n, _ := s.Notify.Unread(r.Context(), sess.UserID)
			ev := map[string]any{"id": it.ID, "html": strings.TrimSpace(html), "unread": n}
			if it.EventID != "" && s.Calendar != nil { // an invitation came or went: the «Календарь» counter of the menu
				ev["invites"], _ = s.Calendar.Invitations(r.Context(), sess.UserID)
			}
			data, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: notify\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// notifySettings: which notifications the person gets, and where — in go24, in the messenger, in the outside
// messengers whose chats they linked (one column each sets the kinds by channel).
func (s *Server) notifySettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		on := map[string]bool{}
		for _, k := range r.Form["on"] {
			on[k] = true
		}
		if err := s.Notify.SaveSettings(r.Context(), sess.UserID, on, s.Matrix != nil && r.FormValue("matrix") == "on"); err != nil {
			s.taskErr(w, r, err, "/settings/notifications")
			return
		}
		if s.NotifyOut != nil {
			out := map[string]map[string]bool{} // only the columns the page showed: a chat linked meanwhile keeps its kinds
			for _, ch := range r.Form["chs"] {
				if s.NotifyOut.Channel(ch) == nil {
					continue
				}
				out[ch] = map[string]bool{}
				for _, k := range r.Form["ch_"+ch] {
					out[ch][k] = true
				}
			}
			if err := s.NotifyOut.SaveOff(r.Context(), sess.UserID, out); err != nil {
				s.taskErr(w, r, err, "/settings/notifications")
				return
			}
		}
		http.Redirect(w, r, "/settings/notifications?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	st, err := s.Notify.Settings(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	data := map[string]any{"Kinds": notify.Kinds, "Settings": st, "Messenger": s.Matrix != nil}
	if s.NotifyOut != nil {
		links, err := s.NotifyOut.Links(r.Context(), sess.UserID)
		if err != nil {
			s.fail(w, "notify links", err)
			return
		}
		var linked []notifyout.Link
		for _, l := range links {
			if l.Linked {
				linked = append(linked, l)
			}
		}
		data["Links"], data["Linked"] = links, linked
	}
	s.render(w, r, "notify_settings", page{TitleKey: "notify.settings", Nav: "notify_settings", Data: data})
}

// notifyLink starts linking the person's chat with a messenger's bot: a new code, shown with the bot's link.
func (s *Server) notifyLink(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	ch := r.PathValue("channel")
	if _, err := s.NotifyOut.StartLink(r.Context(), sess.UserID, ch); err != nil {
		s.taskErr(w, r, err, "/settings/notifications")
		return
	}
	http.Redirect(w, r, "/settings/notifications#ch-"+url.PathEscape(ch), http.StatusSeeOther)
}

// notifyUnlink: the messenger's bot writes the person no more.
func (s *Server) notifyUnlink(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.NotifyOut.Unlink(r.Context(), sess.UserID, r.PathValue("channel")); err != nil {
		s.taskErr(w, r, err, "/settings/notifications")
		return
	}
	http.Redirect(w, r, "/settings/notifications?msg="+url.QueryEscape(s.tr(r, "notifyout.unlinked")), http.StatusSeeOther)
}

// notifyBots: «Уведомления в мессенджерах» of the settings (administrators) — the company's bot of each messenger.
func (s *Server) notifyBots(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	bots, err := s.NotifyOut.Bots(r.Context())
	if err != nil {
		s.fail(w, "notify bots", err)
		return
	}
	type bot struct {
		notifyout.Bot
		NeedsServer bool
	}
	var list []bot
	for _, b := range bots {
		list = append(list, bot{b, s.NotifyOut.Channel(b.Channel).NeedsServer()})
	}
	s.render(w, r, "notify_bots", page{TitleKey: "notifyout.bots", Nav: "notify_bots", Data: map[string]any{"Bots": list}})
}

func (s *Server) notifyBotSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	ch := r.PathValue("channel")
	err := s.NotifyOut.SaveBot(r.Context(), ch, r.FormValue("enabled") == "1", r.FormValue("server"), r.FormValue("token"),
		r.FormValue("clear") == "1", sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/settings/notify-bots")
		return
	}
	http.Redirect(w, r, "/settings/notify-bots?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#bot-"+url.PathEscape(ch), http.StatusSeeOther)
}
