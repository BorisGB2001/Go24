package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
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

// notifications is the full list (/notifications) with the search and filter: words, section, author,
// period, unread only.
func (s *Server) notifications(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	f := notifyFilter(q)
	items, err := s.Notify.Search(r.Context(), sess.UserID, f, before, notifyPage)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	more := ""
	if len(items) == notifyPage {
		q.Set("before", strconv.FormatInt(items[len(items)-1].ID, 10))
		more = q.Encode()
	}
	actors, err := s.Notify.Actors(r.Context(), sess.UserID)
	if err != nil {
		s.Log.Error("notify actors", "err", err)
	}
	groups := make([]string, 0, len(notify.Kinds))
	for _, g := range notify.Kinds {
		groups = append(groups, g.Group)
	}
	s.render(w, r, "notifications", page{TitleKey: "notify.title", Nav: "notifications",
		Data: map[string]any{"Items": items, "More": more, "Filter": f, "Groups": groups, "Actors": actors,
			"From": q.Get("from"), "To": q.Get("to")}})
}

// notifyFilter reads the filter of the list (or of the REST list) from the query: q, group, author, from, to
// (YYYY-MM-DD, local days), unread=1.
func notifyFilter(q url.Values) notify.Filter {
	f := notify.Filter{Q: strings.TrimSpace(q.Get("q")), Group: q.Get("group"), ActorID: q.Get("author"), Unread: q.Get("unread") == "1"}
	if t, err := time.ParseInLocation("2006-01-02", q.Get("from"), time.Local); err == nil {
		f.From = t
	}
	if t, err := time.ParseInLocation("2006-01-02", q.Get("to"), time.Local); err == nil {
		f.To = t
	}
	return f
}

// notifyDelete removes one notification (✕ in the list and the bell); answers the unread count.
func (s *Server) notifyDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Notify.Delete(r.Context(), sess.UserID, id); err != nil && !errors.Is(err, notify.ErrNotFound) {
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

// answerNotify answers a confirming notification (see notify.Item.Answers) as its card would — the meeting's «Приду»,
// the project's invitation, the group's request — and reads it. It returns the notification as it is now.
func (s *Server) answerNotify(ctx context.Context, userID, id, answer string, can func(scope string) bool) (notify.Item, error) {
	it, err := s.Notify.Get(ctx, userID, id)
	if errors.Is(err, notify.ErrNotFound) {
		return it, tasks.ErrNotFound
	}
	if err != nil {
		return it, err
	}
	if !slices.Contains(it.Answers(), answer) {
		return it, i18n.E("err.bad_form")
	}
	switch it.Kind {
	case "event":
		if !can("calendar:write") {
			return it, i18n.E("err.api_no_scope", "calendar:write")
		}
		err = s.Calendar.Respond(ctx, userID, it.EventID, answer)
	case "project_invite":
		if !can("projects:write") {
			return it, i18n.E("err.api_no_scope", "projects:write")
		}
		err = s.Tasks.AnswerInvite(ctx, userID, it.ProjectKey, answer == "accept")
	case "group_join_request":
		if !can("projects:write") {
			return it, i18n.E("err.api_no_scope", "projects:write")
		}
		var p tasks.Project
		if p, err = s.Tasks.ProjectByKey(ctx, userID, it.ProjectKey); err == nil {
			err = s.Tasks.AnswerJoinRequest(ctx, p, userID, it.ActorID, answer == "accept")
		}
	}
	if err != nil {
		return it, err
	}
	if answer == "accept" {
		s.Matrix.Wake() // the new member joins the project's chat
	}
	if err := s.Notify.MarkRead(ctx, userID, it.ID); err != nil {
		return it, err
	}
	return s.Notify.Get(ctx, userID, id)
}

// notifyAnswer: a button of a confirming notification in the bell or the list; answers the item re-rendered and the
// unread count.
func (s *Server) notifyAnswer(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	it, err := s.answerNotify(r.Context(), sess.UserID, r.PathValue("id"), r.FormValue("answer"), func(string) bool { return true })
	if err != nil {
		if !wantsJSON(r) {
			s.taskErr(w, r, err, "/notifications")
			return
		}
		s.jsonErr(w, r, err)
		return
	}
	s.Log.Info("notification answer", "user_id", sess.UserID, "id", it.ID, "kind", it.Kind, "answer", r.FormValue("answer"))
	if !wantsJSON(r) {
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
		return
	}
	html, err := s.notifyHTML(r, "nitem", it)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	n, _ := s.Notify.Unread(r.Context(), sess.UserID)
	writeJSON(w, http.StatusOK, map[string]any{"html": html, "unread": n})
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
	var feedNews <-chan string // new posts and comments: the «Лента» counter of the menu, not for a guest
	if s.Feed != nil && !tasks.IsGuestCtx(r.Context()) {
		posts, stopPosts := s.Live.Subscribe("feed")
		defer stopPosts()
		comments, stopComments := s.Live.Subscribe("feedc")
		defer stopComments()
		news := make(chan string, 1)
		feedNews = news
		go func() { // both into one channel; a burst is counted once
			for {
				var v string
				select {
				case <-r.Context().Done():
					return
				case v = <-posts:
				case v = <-comments:
				}
				select {
				case news <- v:
				default:
				}
			}
		}()
	}
	lastNews := -1
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
		case <-feedNews:
			n, err := s.Feed.News(r.Context(), sess.UserID)
			if err != nil || n == lastNews {
				continue
			}
			lastNews = n
			fmt.Fprintf(w, "event: feed\ndata: {\"news\":%d}\n\n", n)
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
		chats := false // a linked chat takes chat messages: their row is shown
		for _, l := range links {
			if l.Linked {
				linked = append(linked, l)
				chats = chats || l.Chats
			}
		}
		data["Links"], data["Linked"], data["Chats"] = links, linked, chats
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

// notifyPushDevice: POST /settings/notifications/push/devices — this browser's push subscription (endpoint, p256dh,
// auth, label) becomes the person's device; JSON {ok} or {error}.
func (s *Server) notifyPushDevice(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	var sub notifyout.PushSub
	sub.Endpoint, sub.Keys.P256dh, sub.Keys.Auth = r.FormValue("endpoint"), r.FormValue("p256dh"), r.FormValue("auth")
	if err := s.NotifyOut.AddDevice(r.Context(), sess.UserID, sub, r.FormValue("label")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// notifyPushRemove: POST /settings/notifications/push/devices/{id}/remove — push no more on that device.
func (s *Server) notifyPushRemove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.NotifyOut.RemoveDevice(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/settings/notifications")
		return
	}
	http.Redirect(w, r, "/settings/notifications?msg="+url.QueryEscape(s.tr(r, "notifyout.push_removed"))+"#ch-push", http.StatusSeeOther)
}

// notifyPushTest: POST /settings/notifications/push/test — a trial notification to all the person's devices.
func (s *Server) notifyPushTest(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	n, err := s.NotifyOut.TestPush(r.Context(), sess.UserID, notifyout.Message{Text: s.tr(r, "notifyout.push_test_text"),
		Link: s.NotifyOut.AppURL + "/settings/notifications"})
	msg := s.tr(r, "notifyout.push_test_sent", n)
	if errors.Is(err, notifyout.ErrGone) {
		msg = s.tr(r, "notifyout.push_test_gone") // the push services forgot every device: the list is empty now
	} else if err != nil {
		msg = s.tr(r, "notifyout.push_test_failed")
	}
	http.Redirect(w, r, "/settings/notifications?msg="+url.QueryEscape(msg)+"#ch-push", http.StatusSeeOther)
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
		NeedsServer, Keyed bool
		Hook, Verify       string // a messenger the administrators give go24's address themselves (WhatsApp)
	}
	var list []bot
	for _, b := range bots {
		c := s.NotifyOut.Channel(b.Channel)
		_, keyed := c.(notifyout.Keyed)
		hook, verify := s.NotifyOut.HookSetup(b)
		list = append(list, bot{b, c.NeedsServer(), keyed, hook, verify})
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

// notifyChallenge answers a messenger's check of go24's address (notifyout.Challenged).
func (s *Server) notifyChallenge(w http.ResponseWriter, r *http.Request) {
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.NotifyOut.Channel(r.PathValue("channel")).(notifyout.Challenged); !ok {
		http.NotFound(w, r)
		return
	}
	answer, err := s.NotifyOut.Challenge(r.Context(), r.PathValue("channel"), r.URL.Query())
	if errors.Is(err, notifyout.ErrHookSign) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err != nil {
		s.fail(w, "notify challenge", err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, answer)
}

// notifyHook takes a call of a messenger that calls go24 itself (notifyout.Hooked): the messages to the company's bot.
func (s *Server) notifyHook(w http.ResponseWriter, r *http.Request) {
	if s.NotifyOut == nil {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.NotifyOut.Channel(r.PathValue("channel")).(notifyout.Hooked); !ok {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply, err := s.NotifyOut.Hook(r.Context(), r.PathValue("channel"), r.Header, body)
	if errors.Is(err, notifyout.ErrHookSign) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err != nil {
		s.fail(w, "notify hook", err) // the messenger calls again
		return
	}
	if reply != nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}
}
