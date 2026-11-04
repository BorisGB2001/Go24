package web

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/kb"
	"go24/internal/openlines"
	"go24/internal/payqr"
	"go24/internal/sites"
	"go24/internal/telephony"
)

// ---------- «Виджет на сайт»: the public side (no go24 account) ----------

//go:embed widget/widget.js
var widgetJS string

// widgetCORS answers a site's page only when the widget may be shown there.
func (s *Server) widgetCORS(w http.ResponseWriter, r *http.Request) (openlines.Widget, bool) {
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil || !wd.Active {
		http.NotFound(w, r)
		return wd, false
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		self := "http://" + r.Host // go24's own pages (the preview) may always show it
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			self = "https://" + r.Host
		}
		if !wd.Allowed(origin) && !strings.EqualFold(origin, self) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return wd, false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Go24-Visitor")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		w.Header().Set("Access-Control-Max-Age", "600")
	}
	return wd, true
}

// widgetScript is the script a site puts on its pages: <script src=".../w/{id}/widget.js" async></script>.
func (s *Server) widgetScript(w http.ResponseWriter, r *http.Request) {
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil || !wd.Active {
		http.NotFound(w, r)
		return
	}
	lang := s.lang(r)
	t := map[string]string{}
	for _, k := range []string{"open", "chat", "contact", "placeholder", "contact_note", "name", "phone", "email", "question", "send", "thanks",
		"vote", "vote_comment", "vote_thanks", "history", "history_note", "history_sent", "attach", "typing", "read", "file_big", "consent", "consent_accept", "telegram", "max", "vk", "viber", "ok", "avito", "whatsapp",
		"form_fill", "form_filled", "booking_open", "booking_booked", "close", "callback", "callback_note", "callback_send", "callback_sent"} {
		t[k] = s.I18n.T(lang, "widget."+k)
	}
	bot, _ := s.OpenLines.Telegram(r.Context(), wd.ID) // the line's other channels: «Написать в Telegram», «… в MAX», «… во ВКонтакте»
	maxBot, _ := s.OpenLines.Max(r.Context(), wd.ID)
	vkGroup, _ := s.OpenLines.VK(r.Context(), wd.ID)
	viberBot, _ := s.OpenLines.Viber(r.Context(), wd.ID)
	okGroup, _ := s.OpenLines.OK(r.Context(), wd.ID)
	avito, _ := s.OpenLines.Avito(r.Context(), wd.ID)
	wa, _ := s.OpenLines.WhatsApp(r.Context(), wd.ID)
	var callback map[string]string // «Обратный звонок»: the form's title and text
	if c, ok := s.widgetCallback(r.Context(), wd); ok {
		callback = map[string]string{"title": c.Title, "text": c.Text, "button": c.Button}
	}
	cfg, _ := json.Marshal(map[string]any{"id": wd.ID, "base": s.widgetBase(r), "color": wd.Color, "name": wd.Name,
		"greeting": wd.Greeting, "t": t, "max_file": openlines.MaxFile, "consent": map[bool]string{true: wd.ConsentText}[wd.ConsentOn],
		"telegram": bot.Name, "max": maxBot.Name, "vk": vkGroup.Screen, "viber": viberBot.URI, "ok": okGroup.Link, "avito": avito.Link, "whatsapp": waDigits(wa.Number), "callback": callback, "position": wd.Position, "pages_mode": wd.PagesMode, "pages": wd.Pages, "show_delay": wd.ShowDelay,
		"hello": map[bool]string{true: wd.HelloText}[wd.HelloOn], "hello_delay": wd.HelloDelay, "hello_photo": s.widgetHelloPhoto(r, wd),
		"offline": wd.HideOffline && !wd.InHours(time.Now()), "page": r.URL.Query().Get("page") == "1"})
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60") // working hours: the button hides within a minute
	_, _ = w.Write([]byte(strings.Replace(widgetJS, "/*CONFIG*/{}", string(cfg), 1)))
}

// olChatPublic is the line's «Живой чат» by a link (/online/{id}): a page of its own with the chat on all of it, for
// a letter, a social network or a QR code on a card.
func (s *Server) olChatPublic(w http.ResponseWriter, r *http.Request) {
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil || !wd.Active {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.render(w, r, "olchatpub", page{Data: wd})
}

// widgetQR: the QR code of the line's chat link (PNG), to print or to show to clients (administrators).
func (s *Server) widgetQR(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	png, err := payqr.PNG(s.olPublicBase(r) + "/online/" + wd.ID)
	if err != nil {
		s.fail(w, "widget qr", err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="chat-qr.png"`)
	}
	_, _ = w.Write(png)
}

// olPublicBase is go24's address for a line's code and chat link: the https one, if any (a site on https loads only
// https scripts).
func (s *Server) olPublicBase(r *http.Request) string {
	if s.CallURL != "" {
		return strings.TrimSuffix(s.CallURL, "/")
	}
	return s.absURL(r, "")
}

// widgetAPI: visit | messages (with the dialogue's state; seen=1 — the widget is open) | say | file | typing | contact |
// vote | history, for the visitor whose token is in X-Go24-Visitor.
func (s *Server) widgetAPI(w http.ResponseWriter, r *http.Request) {
	wd, ok := s.widgetCORS(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.PathValue("op") == "file" && r.Method == http.MethodPost {
		s.widgetFile(w, r, wd)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var in struct {
		Page, Body, Name, Phone, Email, Text string
		Vote                                 int
		Website                              string // «Обратный звонок»: the trap field and the seconds the form was open
		Elapsed                              int64
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&in)
	}
	token := r.Header.Get("X-Go24-Visitor")
	var err error
	if op := r.PathValue("op"); wd.ConsentOn && (op == "say" || op == "contact" || op == "callback") {
		err = s.widgetConsented(r, wd, token)
	}
	out := map[string]any{}
	switch op := r.PathValue("op"); {
	case err != nil:
	case op == "visit" && r.Method == http.MethodPost:
		_, out["token"], err = s.OpenLines.Visit(r.Context(), wd.ID, token, in.Page)
	case op == "messages" && r.Method == http.MethodGet:
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		var list []openlines.Message
		if r.URL.Query().Get("seen") == "1" {
			if err = s.OpenLines.VisitorSeen(r.Context(), wd.ID, token); err != nil {
				break
			}
		}
		if list, err = s.OpenLines.VisitorMessages(r.Context(), wd.ID, token, after); err == nil {
			base := s.widgetBase(r)
			msgs := make([]map[string]any, 0, len(list))
			for _, m := range list {
				x := map[string]any{"id": m.ID, "operator": m.Operator, "body": m.Body}
				if m.FileName != "" {
					x["file"] = map[string]any{"name": m.FileName, "size": m.FileSize, "image": m.Image(), "audio": m.Audio(), "url": openlines.FileLink(base, wd.ID, m)}
				}
				switch { // a CRM form an operator sent: its card and the form inside the widget (its link line is for Telegram)
				case m.Kind == "form" && m.FormToken != "":
					x["body"], _, _ = strings.Cut(m.Body, "\n")
					x["form"] = map[string]any{"id": m.FormID, "url": base + "/cf/" + url.PathEscape(m.FormToken) + "?embed=1&ol=" + m.FormSecret,
						"done": m.FormDone != nil}
				case m.Kind == "form_done":
					x["form_done"] = m.FormID
				case m.Kind == "booking" && m.BookingToken != "": // a booking link: the page inside the widget
					x["body"], _, _ = strings.Cut(m.Body, "\n")
					x["booking"] = map[string]any{"id": m.BookingLinkID, "url": base + "/bk/" + url.PathEscape(m.BookingToken) + "?embed=1&ol=" + m.FormSecret,
						"done": m.FormDone != nil}
				case m.Kind == "booking_done":
					x["booking_done"] = m.BookingLinkID
				case len(m.Buttons) > 0: // the chat bot's menu: its items as buttons (the lines of the text are for Telegram)
					x["body"] = strings.TrimSpace(strings.TrimSuffix(m.Body, strings.Join(m.Buttons, "\n")))
					x["buttons"] = m.Buttons
				}
				msgs = append(msgs, x)
			}
			out["messages"] = msgs
			var seen openlines.Seen
			if seen, err = s.OpenLines.VisitorSeenState(r.Context(), wd.ID, token); err != nil {
				break
			}
			op := map[string]any{"name": seen.Operator, "typing": seen.Typing, "read": seen.Read}
			if seen.Photo {
				op["photo"] = base + "/w/" + wd.ID + "/photo/" + seen.OperatorID
			}
			out["operator"] = op
			if wd.ConsentOn {
				if out["consent"], err = s.OpenLines.Consented(r.Context(), wd.ID, token); err != nil {
					break
				}
			}
			var st openlines.State
			if st, err = s.OpenLines.VisitorState(r.Context(), wd.ID, token); err == nil {
				if st.VoteText == "" {
					st.VoteText = s.tr(r, "widget.vote")
				}
				out["state"] = map[string]any{"closed": st.Closed, "vote": st.Vote, "can_vote": st.CanVote, "vote_text": st.VoteText,
					"history": st.History, "email": st.Email}
			}
		}
	case op == "typing" && r.Method == http.MethodPost:
		err = s.OpenLines.VisitorTyping(r.Context(), wd.ID, token)
	case op == "consent" && r.Method == http.MethodPost:
		err = s.OpenLines.VisitorConsent(r.Context(), wd.ID, token, clientIP(r))
	case op == "say" && r.Method == http.MethodPost:
		err = s.OpenLines.VisitorSay(r.Context(), wd.ID, token, in.Body)
	case op == "contact" && r.Method == http.MethodPost:
		err = s.OpenLines.Contact(r.Context(), wd.ID, token, in.Name, in.Phone, in.Email, in.Text, s.tr(r, "widget.source"))
	case op == "callback" && r.Method == http.MethodPost:
		c, ok := s.widgetCallback(r.Context(), wd)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var key string
		var status int
		stamp := sites.FormStamp(time.Now().Add(-time.Duration(in.Elapsed) * time.Second))
		if key, status, err = s.callbackRequest(r, c, in.Phone, in.Name, in.Page, in.Website, stamp); err == nil && key != "" {
			writeJSON(w, status, map[string]string{"error": s.tr(r, key)})
			return
		}
	case op == "vote" && r.Method == http.MethodPost:
		err = s.OpenLines.VisitorVote(r.Context(), wd.ID, token, in.Vote, in.Text)
	case op == "history" && r.Method == http.MethodPost:
		err = s.OpenLines.SendHistory(r.Context(), wd.ID, token, in.Email)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// widgetCallback is the form of «Обратный звонок» the line's widget offers (none — false).
func (s *Server) widgetCallback(ctx context.Context, wd openlines.Widget) (telephony.Callback, bool) {
	if wd.CallbackID == "" || s.Telephony == nil {
		return telephony.Callback{}, false
	}
	list, err := s.Telephony.Callbacks(ctx)
	if err != nil {
		s.Log.Error("widget callback", "err", err, "line", wd.ID)
		return telephony.Callback{}, false
	}
	for _, c := range list {
		if c.ID == wd.CallbackID {
			return c, true
		}
	}
	return telephony.Callback{}, false
}

// widgetConsented: has the visitor accepted the line's agreement (err.ol_consent — not yet)?
func (s *Server) widgetConsented(r *http.Request, wd openlines.Widget, token string) error {
	ok, err := s.OpenLines.Consented(r.Context(), wd.ID, token)
	if err == nil && !ok {
		err = i18n.E("err.ol_consent")
	}
	return err
}

// widgetBase is go24's address as the site's page reaches it.
func (s *Server) widgetBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// olFormLink is the address of a CRM form sent in a dialogue: go24's own, with the key of the dialogue's filling.
func (s *Server) olFormLink(token, secret string) string {
	return s.BaseURL + "/cf/" + url.PathEscape(token) + "?ol=" + secret
}

// olUpload stores the form's «file» (if any) in the built-in storage for a message of a dialogue.
func (s *Server) olUpload(r *http.Request) (*openlines.File, error) {
	f, hdr, err := r.FormFile("file")
	if errors.Is(err, http.ErrMissingFile) {
		return nil, nil
	}
	if err != nil {
		return nil, i18n.E("err.ol_file")
	}
	defer f.Close()
	if hdr.Size > openlines.MaxFile {
		return nil, i18n.E("err.ol_file")
	}
	name := openlines.CleanName(hdr.Filename)
	ct := hdr.Header.Get("Content-Type")
	if ct == "" || ct == "application/octet-stream" {
		ct = mime.TypeByExtension(path.Ext(name))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	key := path.Join("openlines", time.Now().Format("2006/01"), randomHex(16), name)
	if err := s.Storages.Put(r.Context(), "", false, "", key, f, hdr.Size, ct); err != nil {
		return nil, err
	}
	return &openlines.File{Key: key, Name: name, Type: ct, Size: hdr.Size}, nil
}

// widgetFile is the visitor's file (with the text «body», if any).
func (s *Server) widgetFile(w http.ResponseWriter, r *http.Request, wd openlines.Widget) {
	r.Body = http.MaxBytesReader(w, r.Body, openlines.MaxFile+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.jsonErr(w, r, i18n.E("err.ol_file"))
		return
	}
	var err error
	if wd.ConsentOn {
		err = s.widgetConsented(r, wd, r.Header.Get("X-Go24-Visitor"))
	}
	var f *openlines.File
	if err == nil {
		f, err = s.olUpload(r)
	}
	if err == nil && f == nil {
		err = i18n.E("err.ol_file")
	}
	if err == nil {
		err = s.OpenLines.VisitorSend(r.Context(), wd.ID, r.Header.Get("X-Go24-Visitor"), r.FormValue("body"), f)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// olServe gives a dialogue's file: a picture is shown, anything else is downloaded.
func (s *Server) olServe(w http.ResponseWriter, r *http.Request, f openlines.StoredFile) {
	obj, err := s.Storages.Open(r.Context(), nil, f.Key)
	if err != nil {
		s.fail(w, "open openlines file", err)
		return
	}
	defer obj.Close()
	disposition := "attachment"
	if (openlines.Message{FileType: f.Type}).Image() || openlines.Audio(f.Name, f.Type) { // a voice message plays in the page
		disposition = "inline"
	}
	w.Header().Set("Content-Type", f.Type)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; sandbox")
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename*=UTF-8''%s", disposition, url.PathEscape(f.Name)))
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = io.Copy(w, obj)
}

// widgetFileGet: a file of the visitor's dialogue by its link (/w/{id}/file/{msg}/{secret}).
func (s *Server) widgetFileGet(w http.ResponseWriter, r *http.Request) {
	msg, _ := strconv.ParseInt(r.PathValue("msg"), 10, 64)
	f, err := s.OpenLines.FileOf(r.Context(), msg)
	if err != nil || f.WidgetID != r.PathValue("id") || !f.Secret(r.PathValue("secret")) {
		http.NotFound(w, r)
		return
	}
	s.olServe(w, r, f)
}

// widgetPhoto is the photo of an operator who has answered in the line.
// widgetHelloPhoto is the link to the photo of the operator of «Автоприглашение» — the queue's first; "" — none.
func (s *Server) widgetHelloPhoto(r *http.Request, wd openlines.Widget) string {
	if !wd.HelloOn || len(wd.Operators) == 0 {
		return ""
	}
	if key, _, err := s.OpenLines.OperatorPhoto(r.Context(), wd.ID, wd.Operators[0]); err != nil || key == "" {
		return ""
	}
	return s.widgetBase(r) + "/w/" + wd.ID + "/photo/" + wd.Operators[0]
}

func (s *Server) widgetPhoto(w http.ResponseWriter, r *http.Request) {
	key, typ, err := s.OpenLines.OperatorPhoto(r.Context(), r.PathValue("id"), r.PathValue("user"))
	if err != nil || key == "" {
		http.NotFound(w, r)
		return
	}
	obj, err := s.Storages.Open(r.Context(), nil, key)
	if err != nil {
		s.fail(w, "open photo", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", typ)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = io.Copy(w, obj)
}

// ---------- «Открытые линии»: the operators' side ----------

// olTabs are the tabs of the dialogue list.
var olTabs = []string{"mine", "noanswer", "all", "closed"}

func (s *Server) openlinesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	tab := q.Get("tab")
	if q.Get("closed") == "1" { // the old link of «Закрытые»
		tab = "closed"
	}
	if !slices.Contains(olTabs, tab) {
		tab = "all"
	}
	who, err := s.olWho(r.Context(), sess.UserID, sess.Admin)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	list, err := s.OpenLines.Sessions(r.Context(), openlines.Filter{Tab: tab, Q: q.Get("q"), UserID: sess.UserID, Who: who})
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	counts, err := s.OpenLines.Counts(r.Context(), who)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	quick, err := s.OpenLines.Quick(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	// the list's own address: a dialogue opened from it keeps the tab and the search
	keep := url.Values{}
	if tab != "all" {
		keep.Set("tab", tab)
	}
	if v := q.Get("q"); v != "" {
		keep.Set("q", v)
	}
	type tabLink struct {
		Tab, URL string
		Count    int
	}
	tabs := []tabLink{}
	for _, t := range olTabs {
		v := url.Values{}
		if t != "all" {
			v.Set("tab", t)
		}
		if x := q.Get("q"); x != "" {
			v.Set("q", x)
		}
		link := "/openlines"
		if len(v) > 0 {
			link += "?" + v.Encode()
		}
		tabs = append(tabs, tabLink{t, link, counts[t]})
	}
	data := map[string]any{"Sessions": list, "Tab": tab, "Tabs": tabs, "Q": q.Get("q"), "Keep": keep.Encode(), "Quick": quick}
	if data["Paused"], err = s.OpenLines.Paused(r.Context(), sess.UserID); err != nil {
		s.taskErr(w, r, err, "/")
		return
	}
	if id := q.Get("s"); id != "" {
		if err := s.OpenLines.See(r.Context(), who, id); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
		x, msgs, err := s.OpenLines.Open(r.Context(), sess.UserID, id)
		if err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
		users, err := s.Tasks.ListUsers(r.Context())
		if err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
		data["Current"], data["Messages"], data["Users"] = x, msgs, users
		if data["Lines"], err = s.OpenLines.Widgets(r.Context()); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
		var read int64 // «Прочитано» under the last operator's message the visitor has seen
		for _, m := range msgs {
			if m.AuthorID != "" && m.ID <= x.VisRead {
				read = m.ID
			}
		}
		data["Read"] = read
		data["CanRate"] = s.olHead(r, sess, x.OperatorID)
		data["AI"], data["Speech"] = s.aiOn(r.Context(), sess.UserID, "openlines"), s.Speech != nil
		// who may take the invited colleagues out of the dialogue: its operator, the lines' heads, administrators
		data["CanUninvite"] = who.All || x.OperatorID == sess.UserID
		var forms []crm.Form // «Форма»: the active CRM forms to send to the client
		if s.CRM != nil && !slices.Contains(s.sectionsOff(r.Context()), "crm") {
			list, err := s.CRM.Forms(r.Context())
			if err != nil {
				s.taskErr(w, r, err, "/openlines")
				return
			}
			for _, f := range list {
				if f.Active {
					forms = append(forms, f)
				}
			}
		}
		data["Forms"] = forms
		if data["BookingLinks"], err = s.olBookingLinks(r.Context(), sess.UserID, sess.Admin); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
		if data["Past"], data["PastOpen"], err = s.OpenLines.ClientDialogues(r.Context(), who, id); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	s.render(w, r, "openlines", page{TitleKey: "ol.title", Nav: "openlines", Data: data})
}

// olPause: the operator's pause — «Не принимать новые диалоги» (on=1) or back to the queue.
func (s *Server) olPause(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.OpenLines.Pause(r.Context(), sess.UserID, r.FormValue("on") == "1"); err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	http.Redirect(w, r, "/openlines", http.StatusSeeOther)
}

// olWho is whose dialogues the user sees, as the roles of the open lines: administrators and the lines' heads —
// every one; the others — their own, the unassigned ones of their lines' queue and those of their people.
func (s *Server) olWho(ctx context.Context, userID string, admin bool) (openlines.Who, error) {
	x := openlines.Who{UserID: userID, All: admin}
	if !x.All {
		var err error
		if x.All, err = s.OpenLines.IsHead(ctx, userID); err != nil {
			return x, err
		}
	}
	if !x.All && s.Company != nil {
		team, err := s.Company.Subordinates(ctx, userID)
		if err != nil {
			return x, err
		}
		for _, p := range team {
			x.Team = append(x.Team, p.ID)
		}
	}
	return x, nil
}

// olHead: may the user rate the operator's work in a dialogue — administrators, the lines' heads and the operator's
// managers, as the heads of the open lines.
func (s *Server) olHead(r *http.Request, sess auth.Session, operator string) bool {
	if sess.Admin {
		return true
	}
	if head, err := s.OpenLines.IsHead(r.Context(), sess.UserID); err == nil && head {
		return true
	}
	if operator == "" || operator == sess.UserID || s.Company == nil {
		return false
	}
	heads, err := s.Company.Managers(r.Context(), operator)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(heads, func(p company.Person) bool { return p.ID == sess.UserID })
}

// openlinesAction: reply (body, file), close, reopen, summary (CoPilot), lead, transfer (to), invite (to), uninvite (who, "" — oneself), spam, rate (vote, comment). back — the list's tab and search.
func (s *Server) openlinesAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, openlines.MaxFile+1<<20)
	list := "/openlines"
	if v, err := url.ParseQuery(r.FormValue("back")); err == nil && len(v) > 0 {
		list += "?" + url.Values{"tab": v["tab"], "q": v["q"]}.Encode()
	}
	sep := "?"
	if strings.Contains(list, "?") {
		sep = "&"
	}
	back := list + sep + "s=" + url.QueryEscape(id)
	who, err := s.olWho(r.Context(), sess.UserID, sess.Admin)
	if err == nil {
		err = s.OpenLines.See(r.Context(), who, id)
	}
	if err != nil {
		http.Redirect(w, r, list+sep+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	switch r.FormValue("op") {
	case "reply":
		var f *openlines.File
		if f, err = s.olUpload(r); err == nil && r.FormValue("hidden") != "" {
			err = s.OpenLines.Hide(r.Context(), sess.UserID, id, r.FormValue("body"), f)
		} else if err == nil {
			err = s.OpenLines.ReplyWith(r.Context(), sess.UserID, id, r.FormValue("body"), f)
		}
	case "close":
		err = s.OpenLines.SetStatus(r.Context(), sess.UserID, id, false)
		back = list
	case "reopen":
		err = s.OpenLines.SetStatus(r.Context(), sess.UserID, id, true)
	case "transfer":
		if line := r.FormValue("line"); line != "" {
			err = s.OpenLines.TransferLine(r.Context(), sess.UserID, id, line)
		} else {
			err = s.OpenLines.Transfer(r.Context(), sess.UserID, id, r.FormValue("to"))
		}
		if err == nil && s.OpenLines.See(r.Context(), who, id) != nil { // passed on to someone else's: back to the list
			back = list
		}
	case "intercept":
		err = s.OpenLines.Intercept(r.Context(), sess.UserID, id)
	case "invite":
		err = s.OpenLines.Invite(r.Context(), sess.UserID, id, r.FormValue("to"))
	case "uninvite":
		err = s.OpenLines.Uninvite(r.Context(), who, id, cmp.Or(r.FormValue("who"), sess.UserID))
		if err == nil && s.OpenLines.See(r.Context(), who, id) != nil { // left it: back to the list
			back = list
		}
	case "form":
		err = s.OpenLines.SendForm(r.Context(), sess.UserID, id, r.FormValue("form"), s.olFormLink)
	case "booking":
		err = s.olSendBooking(r.Context(), sess.UserID, sess.Admin, id, r.FormValue("link"))
	case "summary":
		err = s.olSummarize(r.Context(), id, sess.UserID)
	case "spam":
		err = s.OpenLines.Spam(r.Context(), sess.UserID, id)
		back = list
	case "pin", "unpin":
		err = s.OpenLines.Pin(r.Context(), sess.UserID, id, r.FormValue("op") == "pin")
	case "rate":
		var x openlines.Session
		if x, _, err = s.OpenLines.Open(r.Context(), sess.UserID, id); err == nil {
			if !s.olHead(r, sess, x.OperatorID) {
				err = i18n.E("err.forbidden")
			} else {
				vote, _ := strconv.Atoi(r.FormValue("vote"))
				err = s.OpenLines.HeadVote(r.Context(), sess.UserID, id, vote, r.FormValue("comment"))
			}
		}
	case "lead":
		if err = s.CRM.Check(r.Context(), sess.UserID, "lead", "add", sess.UserID); err == nil {
			var card string
			if card, err = s.OpenLines.MakeLead(r.Context(), sess.UserID, id, s.tr(r, "widget.source")); err == nil {
				back = card
			}
		}
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		back = list + sep + "s=" + url.QueryEscape(id) + "&msg=" + url.QueryEscape(s.errText(r, err))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// openlinesLive tells an open «Открытые линии» page which dialogue changed (a message, its operator, status): the
// page fetches itself anew.
func (s *Server) openlinesLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Live == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	events, stop := s.Live.Subscribe("ol")
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case id := <-events:
			if t, ok := strings.CutPrefix(id, "t:"); ok { // the visitor is typing
				fmt.Fprintf(w, "event: typing\ndata: %s\n\n", t)
			} else {
				fmt.Fprintf(w, "event: session\ndata: %s\n\n", id)
			}
			flusher.Flush()
		}
	}
}

// openlinesTyping: the operator is typing in the dialogue (the visitor's widget shows it).
func (s *Server) openlinesTyping(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	who, err := s.olWho(r.Context(), sess.UserID, sess.Admin)
	if err == nil {
		err = s.OpenLines.See(r.Context(), who, r.PathValue("id"))
	}
	if err == nil {
		err = s.OpenLines.Typing(r.Context(), sess.UserID, r.PathValue("id"))
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// openlinesFile: a file of a dialogue, for the operators.
func (s *Server) openlinesFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	msg, _ := strconv.ParseInt(r.PathValue("msg"), 10, 64)
	f, err := s.OpenLines.FileOf(r.Context(), msg)
	if err == nil {
		var who openlines.Who
		if who, err = s.olWho(r.Context(), sess.UserID, sess.Admin); err == nil {
			err = s.OpenLines.See(r.Context(), who, f.SessionID)
		}
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.olServe(w, r, f)
}

// olQuickPage: «Быстрые ответы» — one's own and the company's (administrators change those).
func (s *Server) olQuickPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.OpenLines.Quick(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	s.render(w, r, "olquick", page{TitleKey: "ol.quick", Nav: "openlines", Data: map[string]any{"Quick": list}})
}

func (s *Server) olQuickSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	_, err := s.OpenLines.SaveQuick(r.Context(), sess.UserID, sess.Admin, openlines.Quick{ID: id, Title: r.FormValue("title"), Body: r.FormValue("body"),
		Common: r.FormValue("common") == "on"})
	if err != nil {
		http.Redirect(w, r, "/openlines/quick?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/quick", http.StatusSeeOther)
}

func (s *Server) olQuickDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.OpenLines.DeleteQuick(r.Context(), sess.UserID, sess.Admin, r.PathValue("id")); err != nil {
		http.Redirect(w, r, "/openlines/quick?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/quick", http.StatusSeeOther)
}

// widgetsPage: the widgets and their code to put on a site (administrators).
func (s *Server) widgetsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.OpenLines.Widgets(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	base := s.olPublicBase(r)
	bots, maxBots, vkGroups, viberBots := map[string]openlines.TelegramBot{}, map[string]openlines.MaxBot{}, map[string]openlines.VKGroup{}, map[string]openlines.ViberBot{} // the channels Telegram, MAX, VK and Viber of each line
	// and OK, Avito and WhatsApp
	okGroups, avitos, was := map[string]openlines.OKGroup{}, map[string]openlines.AvitoAccount{}, map[string]openlines.WANumber{}
	for _, wd := range list {
		if bots[wd.ID], err = s.OpenLines.Telegram(r.Context(), wd.ID); err == nil {
			maxBots[wd.ID], err = s.OpenLines.Max(r.Context(), wd.ID)
		}
		if err == nil {
			vkGroups[wd.ID], err = s.OpenLines.VK(r.Context(), wd.ID)
		}
		if err == nil {
			viberBots[wd.ID], err = s.OpenLines.Viber(r.Context(), wd.ID)
		}
		if err == nil {
			okGroups[wd.ID], err = s.OpenLines.OK(r.Context(), wd.ID)
		}
		if err == nil {
			avitos[wd.ID], err = s.OpenLines.Avito(r.Context(), wd.ID)
		}
		if err == nil {
			was[wd.ID], err = s.OpenLines.WhatsApp(r.Context(), wd.ID)
		}
		if err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	heads, err := s.OpenLines.Heads(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	connectors := map[string][]openlines.ConnectorLine{} // the applications' channels of each line
	conns, err := s.OpenLines.ConnectorLines(r.Context(), "")
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	for _, c := range conns {
		connectors[c.WidgetID] = append(connectors[c.WidgetID], c)
	}
	var callbacks []telephony.Callback // the forms of «Обратный звонок» a line's widget may offer
	if s.Telephony != nil {
		if callbacks, err = s.Telephony.Callbacks(r.Context()); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	var spaces []kb.Space // the knowledge base spaces a line's AI bot may answer from
	if s.KB != nil {
		if spaces, err = s.KB.Spaces(r.Context(), sess.UserID); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	var holidays string // «Из производственного календаря»: the company's holidays for a line
	if sched, err := s.Tasks.Schedule(r.Context()); err == nil {
		holidays = strings.Join(openlines.HolidaysDM(sched.Holidays), ",")
	}
	fresh := openlines.Widget{QueueMode: "all", AnswerWait: 3, HoursDays: []int32{1, 2, 3, 4, 5}, HoursFrom: "09:00", HoursTo: "18:00", NoanswerAfter: 5, KPIOperator: true,
		VoteOn: true, HistoryOn: true, CRMAuto: true}
	s.render(w, r, "widgets", page{TitleKey: "ol.widgets", Nav: "openlines", Data: map[string]any{"Widgets": list, "Users": users, "Base": base,
		"New": fresh, "Days": []int{1, 2, 3, 4, 5, 6, 0}, "Bots": bots, "MaxBots": maxBots, "VKGroups": vkGroups, "ViberBots": viberBots, "OKGroups": okGroups, "Avitos": avitos, "WhatsApps": was, "Heads": heads,
		"Connectors": connectors, "Callbacks": callbacks, "Spaces": spaces,
		"Zones": calendar.Zones(time.Now(), ""), "PortalTZ": portalTZ(), "CompanyHolidays": holidays}})
}

// olHeadsSave sets the heads of the open lines: they see every dialogue and everyone's statistics (administrators).
func (s *Server) olHeadsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	if err := s.OpenLines.SetHeads(r.Context(), sess.UserID, r.Form["heads"]); err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

func (s *Server) widgetSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/openlines/widgets")
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	num := func(k string) int { n, _ := strconv.Atoi(r.FormValue(k)); return n }
	days := []int32{}
	for _, d := range r.Form["hours_days"] {
		if n, err := strconv.Atoi(d); err == nil {
			days = append(days, int32(n))
		}
	}
	var menu []openlines.BotItem // the chat bot's menu: the tree olbot.js sends as JSON
	if v := r.FormValue("bot_menu"); v != "" && json.Unmarshal([]byte(v), &menu) != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/openlines/widgets")
		return
	}
	var autoMsgs []openlines.AutoMsg // «Автоматические сообщения»: a delay in hours or days and a text each
	for i, text := range r.Form["automsg_text"] {
		n, unit := 0, 60
		if i < len(r.Form["automsg_count"]) {
			n, _ = strconv.Atoi(r.Form["automsg_count"][i])
		}
		if i < len(r.Form["automsg_unit"]) && r.Form["automsg_unit"][i] == "days" {
			unit = 24 * 60
		}
		autoMsgs = append(autoMsgs, openlines.AutoMsg{After: n * unit, Text: text})
	}
	_, err := s.OpenLines.SaveWidget(r.Context(), sess.UserID, openlines.Widget{ID: id, Name: r.FormValue("name"), Greeting: r.FormValue("greeting"),
		Color: r.FormValue("color"), Origins: strings.Fields(r.FormValue("origins")), Operators: r.Form["operators"], Active: r.FormValue("active") == "on",
		QueueMode: r.FormValue("queue_mode"), AnswerWait: num("answer_wait"), MaxChats: num("max_chats"), MaxChatsType: r.FormValue("max_chats_type"),
		CRMForward: r.FormValue("crm_forward") == "on", HoursOn: r.FormValue("hours_on") == "on", HoursDays: days,
		HoursFrom: r.FormValue("hours_from"), HoursTo: r.FormValue("hours_to"), OfflineText: r.FormValue("offline_text"),
		WelcomeText: r.FormValue("welcome_text"), NoanswerText: r.FormValue("noanswer_text"), NoanswerAfter: num("noanswer_after"),
		CloseText: r.FormValue("close_text"), Autoclose: num("autoclose"),
		VoteOn: r.FormValue("vote_on") == "on", VoteText: r.FormValue("vote_text"), HistoryOn: r.FormValue("history_on") == "on",
		CRMAuto: r.FormValue("crm_auto") == "on", ConsentOn: r.FormValue("consent_on") == "on", ConsentText: r.FormValue("consent_text"),
		KPIFirst: num("kpi_first"), KPIFurther: num("kpi_further"), KPIOperator: r.FormValue("kpi_operator") == "on", KPIHeads: r.FormValue("kpi_heads") == "on",
		BotOn: r.FormValue("bot_on") == "on", BotText: r.FormValue("bot_text"), BotMenu: menu, BotOther: r.FormValue("bot_other"),
		Position: r.FormValue("position"), PagesMode: r.FormValue("pages_mode"), Pages: strings.Split(r.FormValue("pages"), "\n"), ShowDelay: num("show_delay"),
		HelloOn: r.FormValue("hello_on") == "on", HelloText: r.FormValue("hello_text"), HelloDelay: num("hello_delay"), HideOffline: r.FormValue("hide_offline") == "on",
		AISummary: r.FormValue("ai_summary") == "on", CallbackID: r.FormValue("callback_id"),
		AIBot: r.FormValue("ai_bot") == "on", AISpace: r.FormValue("ai_space"),
		HoursTZ: r.FormValue("hours_tz"), Holidays: strings.FieldsFunc(r.FormValue("holidays"), func(c rune) bool { return c == ',' || c == ';' || unicode.IsSpace(c) }),
		AutoMsgs: autoMsgs})
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetTelegram connects the line's Telegram bot by its token (op=connect, token) or disconnects it (administrators).
func (s *Server) widgetTelegram(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	if r.FormValue("op") == "disconnect" {
		err = s.OpenLines.DisconnectTelegram(r.Context(), r.PathValue("id"))
	} else {
		_, err = s.OpenLines.ConnectTelegram(r.Context(), r.PathValue("id"), r.FormValue("token"))
	}
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetMax connects the line's MAX bot by its token (op=connect, token) or disconnects it (administrators).
func (s *Server) widgetMax(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	if r.FormValue("op") == "disconnect" {
		err = s.OpenLines.DisconnectMax(r.Context(), r.PathValue("id"))
	} else {
		_, err = s.OpenLines.ConnectMax(r.Context(), r.PathValue("id"), r.FormValue("token"))
	}
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetViber connects the line's Viber bot by its token (op=connect, token) — Viber is told to call go24 at its
// public address — or disconnects it (administrators).
func (s *Server) widgetViber(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	if r.FormValue("op") == "disconnect" {
		err = s.OpenLines.DisconnectViber(r.Context(), r.PathValue("id"))
	} else {
		_, err = s.OpenLines.ConnectViber(r.Context(), r.PathValue("id"), r.FormValue("token"), s.BaseURL)
	}
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetOK connects the line's OK group by its bot token (op=connect, token, link — the group's page) — OK is told to
// call go24 at its public address — or disconnects it (administrators).
func (s *Server) widgetOK(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	if r.FormValue("op") == "disconnect" {
		err = s.OpenLines.DisconnectOK(r.Context(), r.PathValue("id"))
	} else {
		_, err = s.OpenLines.ConnectOK(r.Context(), r.PathValue("id"), r.FormValue("token"), r.FormValue("link"), s.BaseURL)
	}
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetAvito connects the line's Avito account by its application's client id and secret (op=connect, client_id,
// client_secret) or disconnects it (administrators).
func (s *Server) widgetAvito(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	if r.FormValue("op") == "disconnect" {
		err = s.OpenLines.DisconnectAvito(r.Context(), r.PathValue("id"))
	} else {
		_, err = s.OpenLines.ConnectAvito(r.Context(), r.PathValue("id"), r.FormValue("client_id"), r.FormValue("client_secret"), s.BaseURL)
	}
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetWhatsApp connects the line's WhatsApp Business number (op=connect, phone — its id in the Cloud API, waba — the
// business account's id, token, app_secret) — with waba Meta is told to call go24 at its public address — or
// disconnects it (administrators).
func (s *Server) widgetWhatsApp(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	if r.FormValue("op") == "disconnect" {
		err = s.OpenLines.DisconnectWhatsApp(r.Context(), r.PathValue("id"))
	} else {
		_, err = s.OpenLines.ConnectWhatsApp(r.Context(), r.PathValue("id"), r.FormValue("phone"), r.FormValue("waba"), r.FormValue("token"),
			r.FormValue("app_secret"), s.BaseURL)
	}
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetConnectorOff turns an application's channel off on the line (administrators); the application hears
// ONIMCONNECTORSTATUSDELETE and turns it on again itself.
func (s *Server) widgetConnectorOff(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.OpenLines.TurnOffConnector(r.Context(), r.PathValue("conn"), r.PathValue("id")); err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// waDigits is a WhatsApp number as wa.me takes it: its digits.
func waDigits(number string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, number)
}

// waVerify answers Meta's check of a line's webhook address (GET with hub.mode, hub.verify_token, hub.challenge).
func (s *Server) waVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	challenge, err := s.OpenLines.WAVerify(r.Context(), r.PathValue("id"), q.Get("hub.mode"), q.Get("hub.verify_token"), q.Get("hub.challenge"))
	switch {
	case errors.Is(err, openlines.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, openlines.ErrWASign):
		http.Error(w, "forbidden", http.StatusForbidden)
	case err != nil:
		s.fail(w, "whatsapp verify", err)
	default:
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, challenge)
	}
}

// waHook takes a call of Meta to a line's WhatsApp number: the clients' messages.
func (s *Server) waHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch err := s.OpenLines.WAHook(r.Context(), r.PathValue("id"), r.Header.Get("X-Hub-Signature-256"), body); {
	case errors.Is(err, openlines.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, openlines.ErrWASign):
		http.Error(w, "forbidden", http.StatusForbidden)
	case err != nil:
		s.fail(w, "whatsapp hook", err) // Meta calls again
	}
}

// okHook takes a call of OK to a line's group: a client's message or button, the chat's start.
func (s *Server) okHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch err := s.OpenLines.OKHook(r.Context(), r.PathValue("id"), r.PathValue("secret"), body); {
	case errors.Is(err, openlines.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		s.fail(w, "ok hook", err) // OK calls again
	}
}

// viberHook takes a call of Viber to a line's bot: a client's message, the chat opened (the greeting is the answer).
func (s *Server) viberHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply, err := s.OpenLines.ViberHook(r.Context(), r.PathValue("id"), r.Header.Get("X-Viber-Content-Signature"), body)
	switch {
	case errors.Is(err, openlines.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, openlines.ErrViberSign):
		http.Error(w, "forbidden", http.StatusForbidden)
	case err != nil:
		s.fail(w, "viber hook", err) // Viber calls again
	case reply != nil:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(reply)
	}
}

// widgetVK connects the line's VK community by its access key (op=connect, token) or disconnects it (administrators).
func (s *Server) widgetVK(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	if r.FormValue("op") == "disconnect" {
		err = s.OpenLines.DisconnectVK(r.Context(), r.PathValue("id"))
	} else {
		_, err = s.OpenLines.ConnectVK(r.Context(), r.PathValue("id"), r.FormValue("token"))
	}
	if err != nil {
		http.Redirect(w, r, "/openlines/widgets?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

func (s *Server) widgetDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.OpenLines.DeleteWidget(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/openlines/widgets")
		return
	}
	http.Redirect(w, r, "/openlines/widgets", http.StatusSeeOther)
}

// widgetPreview is a page of go24 with the widget on it, to try it before putting it on a site.
func (s *Server) widgetPreview(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	wd, err := s.OpenLines.Widget(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/openlines/widgets")
		return
	}
	s.render(w, r, "widgetpreview", page{Title: wd.Name, Nav: "openlines", Data: wd})
}

// ---------- «Статистика» ----------

var olStatsBy = []string{"days", "lines", "operators", "dialogs"}

// olChannels are the channels a dialogue comes from, as the filter of «Детальная статистика» lists them.
var olChannels = []string{"widget", "telegram", "whatsapp", "max", "vk", "viber", "ok", "avito", "app"}

// olChannel names a channel: the brand, or a word of the locale.
func (s *Server) olChannel(r *http.Request, c string) string {
	switch c {
	case "widget", "app":
		return s.tr(r, "ol.channel_"+c)
	case "vk", "ok", "avito":
		return s.tr(r, "ol."+c)
	}
	return map[string]string{"telegram": "Telegram", "whatsapp": "WhatsApp", "max": "MAX", "viber": "Viber"}[c]
}

// openlinesStats is the report of the lines for the days ?from= (the month's first day) to ?to= (today), by days, lines
// or operators (?by=), of a line (?line=) and an operator (?operator=); ?csv=1 gives the table as a CSV file. An
// administrator or a head of the lines sees every operator; the others — their own dialogues. ?by=dialogs is
// «Детальная статистика»: the dialogues themselves, also of a status (?status=), a channel (?channel=) and
// a rating (?vote=), sorted by ?sort=.
func (s *Server) openlinesStats(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	f := openlines.StatsFilter{From: today.AddDate(0, 0, 1-today.Day()), To: today, Widget: q.Get("line"), OperatorID: q.Get("operator")}
	for k, t := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if d := formDay(q.Get(k)); d != nil {
			*t = *d
		}
	}
	if f.To.Before(f.From) {
		f.From, f.To = f.To, f.From
	}
	if f.To.Sub(f.From) > 366*24*time.Hour { // a year at most: the days' table stays a page
		f.From = f.To.AddDate(-1, 0, 0)
	}
	all := sess.Admin // administrators and the lines' heads see everyone's
	if !all {
		var err error
		if all, err = s.OpenLines.IsHead(r.Context(), sess.UserID); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	if !all {
		f.OperatorID = sess.UserID
	}
	by := q.Get("by")
	if !slices.Contains(olStatsBy, by) {
		by = "days"
	}
	st, err := s.OpenLines.Stats(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	rows := map[string][]openlines.StatsRow{"days": st.Days, "lines": st.Lines, "operators": st.Operators}[by]
	var detail []openlines.DetailRow
	df := openlines.DetailFilter{StatsFilter: f, Status: q.Get("status"), Channel: q.Get("channel"), Vote: q.Get("vote"), Sort: q.Get("sort")}
	more := false
	if by == "dialogs" {
		if detail, more, err = s.OpenLines.Detail(r.Context(), df); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	if q.Get("csv") == "1" {
		if by == "dialogs" {
			s.openlinesDetailCSV(w, r, detail)
			return
		}
		s.openlinesStatsCSV(w, r, by, rows, st.Total)
		return
	}
	widgets, err := s.OpenLines.Widgets(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/openlines")
		return
	}
	data := map[string]any{"F": f, "By": by, "BySet": olStatsBy, "Stats": st, "Rows": rows, "Widgets": widgets,
		"DF": df, "Detail": detail, "More": more, "Limit": openlines.DetailLimit}
	if all {
		if data["Users"], err = s.Tasks.ListUsers(r.Context()); err != nil {
			s.taskErr(w, r, err, "/openlines")
			return
		}
	}
	link := func(k, v string) string {
		x := r.URL.Query()
		x.Del("csv")
		x.Set(k, v)
		return r.URL.Path + "?" + x.Encode()
	}
	byLinks := map[string]string{}
	for _, b := range olStatsBy {
		byLinks[b] = link("by", b)
	}
	data["ByLinks"], data["CSV"] = byLinks, link("csv", "1")
	if by == "dialogs" {
		channels := [][2]string{}
		for _, c := range olChannels {
			channels = append(channels, [2]string{c, s.olChannel(r, c)})
		}
		// a column's header sorts by it, ascending; again — descending
		sorts := map[string]string{}
		for _, k := range openlines.DetailSorts {
			v := k
			if df.Sort == k {
				v = "-" + k
			}
			sorts[k] = link("sort", v)
		}
		names := map[string]string{}
		for _, x := range detail {
			names[x.Channel] = s.olChannel(r, x.Channel)
		}
		data["Channels"], data["Sorts"], data["ChannelNames"] = channels, sorts, names
	}
	s.render(w, r, "olstats", page{TitleKey: "ol.stats", Nav: "openlines", Data: data})
}

// openlinesStatsCSV gives the report's table as the page shows it, with the totals' row, as a CSV file Excel opens.
func (s *Server) openlinesStatsCSV(w http.ResponseWriter, r *http.Request, by string, rows []openlines.StatsRow, total openlines.StatsRow) {
	tr := func(k string) string { return s.tr(r, k) }
	secs := func(d time.Duration) string { return strconv.Itoa(int(d / time.Second)) }
	first := map[string]string{"days": "ol.stats_day", "lines": "ol.stats_line", "operators": "ol.operator"}[by]
	head := []string{tr(first)}
	for _, k := range []string{"ol.stats_new", "ol.stats_closed", "ol.stats_noanswer", "ol.stats_kpi", "ol.stats_answer_s", "ol.stats_close_s", "ol.stats_up", "ol.stats_down", "ol.stats_spam"} {
		head = append(head, tr(k))
	}
	line := func(name string, x openlines.StatsRow) []string {
		return []string{name, strconv.Itoa(x.New), strconv.Itoa(x.Closed), strconv.Itoa(x.NoAnswer), strconv.Itoa(x.KPI), secs(x.FirstAnswer()), secs(x.ToClose()),
			strconv.Itoa(x.Up), strconv.Itoa(x.Down), strconv.Itoa(x.Spam)}
	}
	out := [][]string{}
	for _, x := range rows {
		name := x.Name
		switch {
		case by == "days":
			name = x.Day.Format(tr("format.date"))
		case name == "":
			name = tr("ol.stats_nobody")
		}
		out = append(out, line(name, x))
	}
	out = append(out, line(tr("stock.total"), total))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+"openlines-"+by+"-"+time.Now().Format(time.DateOnly)+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(out)
}

// openlinesDetailCSV gives «Детальная статистика» as a CSV file Excel opens: a dialogue a row, the times in seconds.
func (s *Server) openlinesDetailCSV(w http.ResponseWriter, r *http.Request, list []openlines.DetailRow) {
	tr := func(k string) string { return s.tr(r, k) }
	secs := func(d time.Duration) string {
		if d == 0 {
			return ""
		}
		return strconv.Itoa(int(d / time.Second))
	}
	at := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.In(time.Local).Format("2006-01-02 15:04:05")
	}
	head := []string{}
	for _, k := range []string{"ol.stats_num", "ol.stats_line", "ol.stats_channel", "ol.stats_client", "crm.f.phone", "crm.f.email", "ol.operator",
		"ol.stats_status", "ol.stats_started", "ol.stats_closed_at", "ol.stats_answer_s", "ol.stats_close_s", "ol.stats_msgs",
		"ol.stats_vote", "ol.stats_head_vote", "ol.stats_kpi", "ol.stats_spam", "ol.stats_crm"} {
		head = append(head, tr(k))
	}
	base := "http://" + r.Host
	if r.TLS != nil {
		base = "https://" + r.Host
	}
	out := [][]string{}
	for _, x := range list {
		status := tr("ol.past_" + x.Status)
		crmLink := ""
		switch {
		case x.LeadID != "":
			crmLink = base + "/crm/leads/" + x.LeadID
		case x.ContactID != "":
			crmLink = base + "/crm/contacts/" + x.ContactID
		}
		spam := ""
		if x.Spam {
			spam = "1"
		}
		out = append(out, []string{strconv.FormatInt(x.Num, 10), x.Line, s.olChannel(r, x.Channel), x.Client(), x.Phone, x.Email, x.Operator,
			status, at(&x.Started), at(x.Closed), secs(x.FirstAnswer()), secs(x.ToClose()), strconv.Itoa(x.Messages),
			strconv.Itoa(x.Vote), strconv.Itoa(x.HeadVote), strconv.Itoa(x.KPILate), spam, crmLink})
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+"openlines-dialogs-"+time.Now().Format(time.DateOnly)+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(out)
}

// taskFromDialogue fills the new task's form of an open line's dialogue the user sees, as «Создать задачу» of
// the portal: the client in the title, the link to the dialogue and its last messages in the description, the CRM card.
func (s *Server) taskFromDialogue(r *http.Request, sess auth.Session, id string, f *taskForm) {
	who, err := s.olWho(r.Context(), sess.UserID, sess.Admin)
	if err == nil {
		err = s.OpenLines.See(r.Context(), who, id)
	}
	if err != nil {
		return
	}
	x, err := s.OpenLines.Session(r.Context(), id)
	if err != nil {
		return
	}
	msgs, err := s.OpenLines.Messages(r.Context(), id, 0)
	if err != nil {
		return
	}
	f.OL, f.Title = &x, s.tr(r, "ol.task_title", x.Title())
	var b strings.Builder
	b.WriteString(s.tr(r, "ol.task_head", x.Widget, s.BaseURL+"/openlines?s="+x.ID))
	if c := strings.Join(slices.DeleteFunc([]string{x.Phone, x.Email}, func(v string) bool { return v == "" }), ", "); c != "" {
		b.WriteString("\n" + c)
	}
	b.WriteString("\n")
	if n := len(msgs); n > 20 {
		msgs = msgs[n-20:]
	}
	for _, m := range msgs {
		if m.Internal() {
			continue
		}
		who := m.Operator
		switch {
		case m.Auto():
			who = s.tr(r, "ol.auto_msg")
		case who == "":
			who = x.Title()
		}
		b.WriteString("\n" + who + ", " + m.At.Local().Format(s.tr(r, "format.datetime")) + ":\n" + m.Text() + "\n")
	}
	f.Description = clipText(b.String(), 20000)
	if f.CRM == "" {
		if x.LeadID != "" {
			f.CRM = "/crm/leads/" + x.LeadID
		} else if x.ContactID != "" {
			f.CRM = "/crm/contacts/" + x.ContactID
		}
	}
}

// taskDialogueDone notes the new task in the dialogue it was made of.
func (s *Server) taskDialogueDone(r *http.Request, sess auth.Session, id, key string) {
	who, err := s.olWho(r.Context(), sess.UserID, sess.Admin)
	if err == nil {
		err = s.OpenLines.See(r.Context(), who, id)
	}
	if err == nil {
		err = s.OpenLines.TaskMade(r.Context(), sess.UserID, id, key, r.FormValue("title"))
	}
	if err != nil {
		s.Log.Warn("task of a dialogue", "task", key, "err", err)
	}
}
