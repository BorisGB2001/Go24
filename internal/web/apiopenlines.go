package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/openlines"
)

// ---------- «Открытые линии» over REST, as imopenlines.* ----------
//
// The lines (read), the dialogues (the list with the page's tabs and search, one with its messages, the messages after
// an id), an operator's answer, close and reopen, transfer, spam, a CRM lead, the head's rating; «Быстрые ответы»;
// «Статистика». The rights are those of the pages: an operator's statistics are their own, the company's quick
// answers and others' statistics are an administrator's. The events — webhooks openline.started, openline.message,
// openline.closed.

func (s *Server) apiOpenLinesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/openlines/lines", s.api("openlines:read", s.apiOLLines))
	mux.HandleFunc("GET /api/v1/openlines/dialogs", s.api("openlines:read", s.apiOLDialogs))
	mux.HandleFunc("GET /api/v1/openlines/dialogs/{id}", s.api("openlines:read", s.apiOLDialog))
	mux.HandleFunc("GET /api/v1/openlines/dialogs/{id}/messages", s.api("openlines:read", s.apiOLMessages))
	mux.HandleFunc("POST /api/v1/openlines/dialogs/{id}/messages", s.api("openlines:write", s.apiOLReply))
	for _, op := range olOps {
		mux.HandleFunc("POST /api/v1/openlines/dialogs/{id}/"+op, s.api("openlines:write", s.apiOLAction))
	}
	mux.HandleFunc("GET /api/v1/openlines/quick", s.api("openlines:read", s.apiOLQuick))
	mux.HandleFunc("POST /api/v1/openlines/quick", s.api("openlines:write", s.apiOLQuickSave))
	mux.HandleFunc("PATCH /api/v1/openlines/quick/{id}", s.api("openlines:write", s.apiOLQuickSave))
	mux.HandleFunc("DELETE /api/v1/openlines/quick/{id}", s.api("openlines:write", s.apiOLQuickDelete))
	mux.HandleFunc("GET /api/v1/openlines/stats", s.api("openlines:read", s.apiOLStats))
	mux.HandleFunc("GET /api/v1/openlines/pause", s.api("openlines:read", s.apiOLPause))
	mux.HandleFunc("POST /api/v1/openlines/pause", s.api("openlines:write", s.apiOLPause))
}

// apiOLPause: the key owner's pause — {"paused"}; POST {"paused": true|false} sets it.
func (s *Server) apiOLPause(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPost {
		var in struct {
			Paused bool `json:"paused"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.OpenLines.Pause(r.Context(), p.UserID, in.Paused); err != nil {
			return err
		}
	}
	on, err := s.OpenLines.Paused(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]bool{"paused": on})
	return nil
}

// olOps are a dialogue's actions: POST /api/v1/openlines/dialogs/{id}/<op>.
var olOps = []string{"close", "reopen", "transfer", "spam", "lead", "rate", "pin", "unpin", "intercept", "form"}

type apiOLLine struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Active     bool      `json:"active"`
	Operators  []apiUser `json:"operators"`  // the queue
	QueueMode  string    `json:"queue_mode"` // all | evenly | strict
	AnswerWait int       `json:"answer_wait_minutes"`
	HoursOn    bool      `json:"hours_on"`
	HoursDays  []int32   `json:"hours_days,omitempty"` // 0 Sunday … 6 Saturday
	HoursFrom  string    `json:"hours_from,omitempty"`
	HoursTo    string    `json:"hours_to,omitempty"`
	Autoclose  int       `json:"autoclose_hours"`
	VoteOn     bool      `json:"vote_on"`
	CRMAuto    bool      `json:"crm_auto"`
	// KPI of the answer time, minutes (0 — off), and who is told of a late dialogue
	KPIFirst    int  `json:"kpi_first_minutes"`
	KPIFurther  int  `json:"kpi_further_minutes"`
	KPIOperator bool `json:"kpi_operator"`
	KPIHeads    bool `json:"kpi_heads"`
	// the chat bot «Интерактивное меню»: its greeting, menu (title, action text|menu|queue|close, text, items) and what
	// a text that picks no item does (menu | queue)
	BotOn     bool                `json:"bot_on"`
	BotText   string              `json:"bot_text,omitempty"`
	BotMenu   []openlines.BotItem `json:"bot_menu,omitempty"`
	BotOther  string              `json:"bot_other,omitempty"`
	Origins   []string            `json:"origins"`
	CreatedAt time.Time           `json:"created_at"`
}

type apiOLDialog struct {
	ID        string     `json:"id"`
	Line      apiUser    `json:"line"` // id and name of the line
	Status    string     `json:"status"`
	Spam      bool       `json:"spam"`
	Waiting   bool       `json:"waiting"` // the visitor waits for an answer
	Unread    int        `json:"unread"`
	Operator  *apiUser   `json:"operator"`
	Name      string     `json:"name"`
	Phone     string     `json:"phone"`
	Email     string     `json:"email"`
	Page      string     `json:"page"`
	LeadID    string     `json:"lead_id,omitempty"`
	ContactID string     `json:"contact_id,omitempty"`
	Last      string     `json:"last"`
	Vote      int        `json:"vote"` // 1 👍, -1 👎, 0 — none
	VoteText  string     `json:"vote_comment,omitempty"`
	HeadVote  int        `json:"head_vote"`
	HeadText  string     `json:"head_comment,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	LastAt    time.Time  `json:"last_at"`
	URL       string     `json:"url"`                  // the dialogue on the page
	ConsentAt *time.Time `json:"consent_at,omitempty"` // when the visitor accepted the line's agreement
	Pinned    bool       `json:"pinned"`               // «Закрепить за собой»: not passed on to another operator
	KPILate   int        `json:"kpi_late"`             // how many times the visitor waited longer than the line's KPI
	Bot       bool       `json:"bot"`                  // the client is with the line's chat bot
}

type apiOLMessage struct {
	ID       int64      `json:"id"`
	From     string     `json:"from"` // visitor | operator | hidden (an operator's hidden one) | line (its automatic messages) | note (a service line)
	Operator *apiUser   `json:"operator,omitempty"`
	Text     string     `json:"text"`
	Kind     string     `json:"kind,omitempty"` // of the line's: welcome, offline, noanswer, close, form_done, bot; of an operator's: form
	At       time.Time  `json:"at"`
	File     *apiOLFile `json:"file,omitempty"`
	Form     *apiOLForm `json:"form,omitempty"`
	Buttons  []string   `json:"buttons,omitempty"` // the chat bot's menu (kind bot)
}

// apiOLForm is the CRM form a message sent (kind form) or the client filled (form_done).
type apiOLForm struct {
	ID     string     `json:"id"`
	DoneAt *time.Time `json:"done_at,omitempty"` // when the client filled the sent form
}

// apiOLFile is a message's file; url — for go24's users (the session's cookie).
type apiOLFile struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type apiOLQuickAnswer struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Text      string    `json:"text"`
	Common    bool      `json:"common"` // the company's (administrators change them)
	CreatedAt time.Time `json:"created_at"`
}

type apiOLStatsRow struct {
	Key         string `json:"key"` // the line's or the operator's id, the day 2006-01-02
	Name        string `json:"name,omitempty"`
	New         int    `json:"new"`
	Closed      int    `json:"closed"`
	NoAnswer    int    `json:"no_answer"`
	KPI         int    `json:"kpi_late"` // the dialogues that waited longer than the line's KPI
	Spam        int    `json:"spam"`
	Up          int    `json:"votes_up"`
	Down        int    `json:"votes_down"`
	FirstAnswer int    `json:"first_answer_seconds"`
	ToClose     int    `json:"to_close_seconds"`
}

// apiOLWho is whose dialogues the key's owner sees; with a dialogue's id — ErrNotFound when not this one.
func (s *Server) apiOLWho(ctx context.Context, p apikeys.Principal, id string) (openlines.Who, error) {
	who, err := s.olWho(ctx, p.UserID, s.apiAdmin(ctx, p.UserID))
	if err == nil && id != "" {
		err = s.OpenLines.See(ctx, who, id)
	}
	return who, err
}

func apiOLDialogOf(x openlines.Session) apiOLDialog {
	d := apiOLDialog{ID: x.ID, Line: apiUser{x.WidgetID, x.Widget}, Status: x.Status, Spam: x.Spam, Waiting: x.Waiting, Unread: x.Unread,
		Name: x.Name, Phone: x.Phone, Email: x.Email, Page: x.Page, LeadID: x.LeadID, ContactID: x.ContactID, Last: x.Last,
		Vote: x.Vote, VoteText: x.VoteComment, HeadVote: x.HeadVote, HeadText: x.HeadComment, CreatedAt: x.CreatedAt, LastAt: x.LastAt,
		URL: "/openlines?s=" + x.ID, ConsentAt: x.ConsentAt, Pinned: x.Pinned, KPILate: x.KPILate, Bot: x.Bot}
	if x.OperatorID != "" {
		d.Operator = &apiUser{x.OperatorID, x.Operator}
	}
	return d
}

func apiOLMessagesOf(list []openlines.Message) []apiOLMessage {
	out := []apiOLMessage{}
	for _, m := range list {
		x := apiOLMessage{ID: m.ID, From: "visitor", Text: m.Body, At: m.At, Buttons: m.Buttons}
		if m.FileName != "" {
			x.File = &apiOLFile{m.FileName, m.FileType, m.FileSize, "/openlines/file/" + strconv.FormatInt(m.ID, 10)}
		}
		switch {
		case m.Kind == "note":
			x.From = "note"
		case m.Kind == "hidden":
			x.From, x.Operator = "hidden", &apiUser{m.AuthorID, m.Operator}
		case m.Auto():
			x.From, x.Kind = "line", m.Kind
		case m.AuthorID != "":
			x.From, x.Operator = "operator", &apiUser{m.AuthorID, m.Operator}
			if m.Kind == "form" {
				x.Kind = m.Kind
			}
		}
		if m.FormID != "" {
			x.Form = &apiOLForm{m.FormID, m.FormDone}
		}
		out = append(out, x)
	}
	return out
}

func apiOLStatsOf(r openlines.StatsRow) apiOLStatsRow {
	return apiOLStatsRow{Key: r.Key, Name: r.Name, New: r.New, Closed: r.Closed, NoAnswer: r.NoAnswer, KPI: r.KPI, Spam: r.Spam, Up: r.Up, Down: r.Down,
		FirstAnswer: int(r.FirstAnswer().Seconds()), ToClose: int(r.ToClose().Seconds())}
}

func (s *Server) apiOLLines(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.OpenLines.Widgets(r.Context())
	if err != nil {
		return err
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, u := range users {
		names[u.ID] = u.Name
	}
	out := []apiOLLine{}
	for _, x := range list {
		l := apiOLLine{ID: x.ID, Name: x.Name, Active: x.Active, Operators: []apiUser{}, QueueMode: x.QueueMode, AnswerWait: x.AnswerWait,
			HoursOn: x.HoursOn, Autoclose: x.Autoclose, VoteOn: x.VoteOn, CRMAuto: x.CRMAuto, Origins: x.Origins, CreatedAt: x.CreatedAt,
			KPIFirst: x.KPIFirst, KPIFurther: x.KPIFurther, KPIOperator: x.KPIOperator, KPIHeads: x.KPIHeads, BotOn: x.BotOn}
		if x.BotOn {
			l.BotText, l.BotMenu, l.BotOther = x.BotText, x.BotMenu, x.BotOther
		}
		if x.HoursOn {
			l.HoursDays, l.HoursFrom, l.HoursTo = x.HoursDays, x.HoursFrom, x.HoursTo
		}
		for _, id := range x.Operators {
			l.Operators = append(l.Operators, apiUser{id, names[id]})
		}
		out = append(out, l)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiOLDialogs: ?tab=mine|noanswer|all|closed (all — the open ones, as on the page), q — a name, phone, e-mail or a
// word of the conversation; ?counts=1 adds the tabs' counters.
func (s *Server) apiOLDialogs(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	tab := q.Get("tab")
	if tab == "" {
		tab = "all"
	}
	if !slices.Contains(olTabs, tab) {
		return i18n.E("err.bad_form")
	}
	who, err := s.apiOLWho(r.Context(), p, "")
	if err != nil {
		return err
	}
	list, err := s.OpenLines.Sessions(r.Context(), openlines.Filter{Tab: tab, Q: q.Get("q"), UserID: p.UserID, Who: who})
	if err != nil {
		return err
	}
	out := []apiOLDialog{}
	for _, x := range list {
		out = append(out, apiOLDialogOf(x))
	}
	if q.Get("counts") != "1" {
		writeJSON(w, http.StatusOK, out)
		return nil
	}
	counts, err := s.OpenLines.Counts(r.Context(), who)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"dialogs": out, "counts": counts})
	return nil
}

// apiOLDialog is the dialogue with its messages; the operator has read it, as on the page.
func (s *Server) apiOLDialog(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	who, err := s.apiOLWho(r.Context(), p, r.PathValue("id"))
	if err != nil {
		return err
	}
	x, msgs, err := s.OpenLines.Open(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	past, open, err := s.OpenLines.ClientDialogues(r.Context(), who, x.ID)
	if err != nil {
		return err
	}
	// the client's other dialogues: those not the key owner's to open — without their messages
	type client struct {
		apiOLDialog
		CanOpen bool `json:"can_open"`
	}
	others := []client{}
	for _, o := range past {
		c := client{apiOLDialogOf(o), open[o.ID]}
		if !c.CanOpen {
			c.Last = ""
		}
		others = append(others, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"dialog": apiOLDialogOf(x), "messages": apiOLMessagesOf(msgs), "client_dialogs": others,
		"can_rate": s.olHead(r, auth.Session{UserID: p.UserID, Admin: s.apiAdmin(r.Context(), p.UserID)}, x.OperatorID)})
	return nil
}

// apiOLMessages: ?after=<id> — the messages after it (500 at most), the dialogue stays unread.
func (s *Server) apiOLMessages(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		var err error
		if after, err = strconv.ParseInt(v, 10, 64); err != nil || after < 0 {
			return i18n.E("err.bad_form")
		}
	}
	if _, err := s.apiOLWho(r.Context(), p, r.PathValue("id")); err != nil {
		return err
	}
	list, err := s.OpenLines.Messages(r.Context(), r.PathValue("id"), after)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiOLMessagesOf(list))
	return nil
}

// apiOLReply: {"text"} — the operator's answer; the first to answer takes the dialogue. {"text", "hidden": true} — a
// hidden message (the visitor does not get it, it does not take the dialogue).
func (s *Server) apiOLReply(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Text   string `json:"text"`
		Hidden bool   `json:"hidden"` // «Скрытое сообщение»: only go24's people see it
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := s.apiOLWho(r.Context(), p, id); err != nil {
		return err
	}
	reply := s.OpenLines.Reply
	if in.Hidden {
		reply = func(ctx context.Context, userID, id, body string) error {
			return s.OpenLines.Hide(ctx, userID, id, body, nil)
		}
	}
	if err := reply(r.Context(), p.UserID, id, in.Text); err != nil {
		return err
	}
	return s.apiOLDialogNow(w, r, id, http.StatusCreated)
}

// apiOLDialogNow answers with the dialogue as it is after an action.
func (s *Server) apiOLDialogNow(w http.ResponseWriter, r *http.Request, id string, status int) error {
	list, err := s.OpenLines.Sessions(r.Context(), openlines.Filter{ID: id, Who: openlines.Who{All: true}}) // the one just acted on
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return openlines.ErrNotFound
	}
	writeJSON(w, status, apiOLDialogOf(list[0]))
	return nil
}

// apiOLAction: close, reopen, spam, pin, unpin, intercept (no body); transfer {"to": user id} or {"line": line id}; rate {"vote": 1|-1, "comment"} — the head's
// rating (administrators and the operator's managers); lead — a CRM lead of the dialogue or its binding to the client
// with the visitor's phone or e-mail: {"kind": "lead"|"contact", "id", "url"}.
func (s *Server) apiOLAction(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	ctx := r.Context()
	if _, err := s.apiOLWho(ctx, p, id); err != nil {
		return err
	}
	var in struct {
		To      string `json:"to"`
		Line    string `json:"line"`
		Vote    int    `json:"vote"`
		Comment string `json:"comment"`
		Form    string `json:"form_id"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	var err error
	switch op := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]; op {
	case "close", "reopen":
		err = s.OpenLines.SetStatus(ctx, p.UserID, id, op == "reopen")
	case "spam":
		err = s.OpenLines.Spam(ctx, p.UserID, id)
	case "pin", "unpin":
		err = s.OpenLines.Pin(ctx, p.UserID, id, op == "pin")
	case "intercept":
		err = s.OpenLines.Intercept(ctx, p.UserID, id)
	case "form":
		err = s.OpenLines.SendForm(ctx, p.UserID, id, in.Form, s.olFormLink)
	case "transfer":
		if in.Line != "" {
			err = s.OpenLines.TransferLine(ctx, p.UserID, id, in.Line)
		} else {
			err = s.OpenLines.Transfer(ctx, p.UserID, id, in.To)
		}
	case "rate":
		var x openlines.Session
		if x, _, err = s.OpenLines.Open(ctx, p.UserID, id); err != nil {
			return err
		}
		if !s.olHead(r, auth.Session{UserID: p.UserID, Admin: s.apiAdmin(ctx, p.UserID)}, x.OperatorID) {
			return i18n.E("err.forbidden")
		}
		err = s.OpenLines.HeadVote(ctx, p.UserID, id, in.Vote, in.Comment)
	case "lead":
		if !p.Can("crm:write") {
			return i18n.E("err.api_no_scope", "crm:write")
		}
		if err := s.CRM.Check(ctx, p.UserID, "lead", "write", p.UserID); err != nil {
			return err
		}
		card, err := s.OpenLines.MakeLead(ctx, p.UserID, id, s.apiTr(r)("widget.source"))
		if err != nil {
			return err
		}
		kind, cid, _ := strings.Cut(strings.TrimPrefix(card, "/crm/"), "/")
		writeJSON(w, http.StatusOK, map[string]string{"kind": strings.TrimSuffix(kind, "s"), "id": cid, "url": card})
		return nil
	}
	if err != nil {
		return err
	}
	return s.apiOLDialogNow(w, r, id, http.StatusOK)
}

func (s *Server) apiOLQuick(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.OpenLines.Quick(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []apiOLQuickAnswer{}
	for _, q := range list {
		out = append(out, apiOLQuickAnswer{q.ID, q.Title, q.Body, q.Common, q.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiOLQuickSave: POST — a new answer, PATCH — a change; {"title", "text", "common"} (the company's — administrators).
func (s *Server) apiOLQuickSave(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title  string `json:"title"`
		Text   string `json:"text"`
		Common bool   `json:"common"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.OpenLines.SaveQuick(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID),
		openlines.Quick{ID: r.PathValue("id"), Title: in.Title, Body: in.Text, Common: in.Common})
	if err != nil {
		return err
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]string{"id": id})
	return nil
}

func (s *Server) apiOLQuickDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.OpenLines.DeleteQuick(r.Context(), p.UserID, s.apiAdmin(r.Context(), p.UserID), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiOLStats: ?from=&to= (2006-01-02; by default — this month), line, operator (an operator — only their own).
func (s *Server) apiOLStats(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	f := openlines.StatsFilter{From: today.AddDate(0, 0, 1-today.Day()), To: today, Widget: q.Get("line"), OperatorID: q.Get("operator")}
	for k, t := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(k); v != "" {
			d := formDay(v)
			if d == nil {
				return i18n.E("err.bad_form")
			}
			*t = *d
		}
	}
	if f.To.Before(f.From) || f.To.Sub(f.From) > 366*24*time.Hour {
		return i18n.E("err.bad_form")
	}
	if who, err := s.apiOLWho(r.Context(), p, ""); err != nil {
		return err
	} else if !who.All {
		f.OperatorID = p.UserID
	}
	st, err := s.OpenLines.Stats(r.Context(), f)
	if err != nil {
		return err
	}
	rows := func(list []openlines.StatsRow) []apiOLStatsRow {
		out := []apiOLStatsRow{}
		for _, x := range list {
			out = append(out, apiOLStatsOf(x))
		}
		return out
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.From.Format(time.DateOnly), "to": f.To.Format(time.DateOnly),
		"total": apiOLStatsOf(st.Total), "lines": rows(st.Lines), "operators": rows(st.Operators), "days": rows(st.Days)})
	return nil
}
