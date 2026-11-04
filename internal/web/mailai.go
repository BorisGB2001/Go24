package web

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// CoPilot of the mail, as Copilot in Outlook: a pane beside the mail where the employee talks to the mail agent
// (ai.Agent «mail») — «Кратко о письме», «Черновик ответа», «Разобрать входящие» or anything else. The agent reads
// the employee's letters with their rights; a draft of a reply, flags, «прочитано» and moves wait for «Подтвердить».

// mailAIReq carries the request in the context of the agent: the letter open beside the pane and the language.
type mailAIReq struct{}

type mailAIOpen struct {
	r      *http.Request
	letter string
}

func mailAIOf(ctx context.Context) mailAIOpen {
	o, _ := ctx.Value(mailAIReq{}).(mailAIOpen)
	return o
}

// The agent's limits: the letters a list gives, the text of a letter it reads.
const (
	mailAIList    = 20
	mailAIListMax = 50
	mailAIText    = 6000
	mailAIEarlier = 1500 // each earlier letter of the conversation
	mailAIPreview = 200
)

// mailAIMarks are the actions the agent may do on letters (mail.Store.Act): no deleting.
var mailAIMarks = []string{"read", "unread", "flag", "unflag", "done", "pin", "unpin", "archive", "junk", "move"}

func aiClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func toolJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	return string(b), err
}

// mailAgent is the agent of the mail.
func (s *Server) mailAgent() *ai.Agent {
	obj := func(props string, required ...string) json.RawMessage {
		req, _ := json.Marshal(required)
		if required == nil {
			req = []byte("[]")
		}
		return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `}`)
	}
	return &ai.Agent{Name: "mail", System: s.mailAISystem, Tools: []ai.Tool{
		{ToolSpec: ai.ToolSpec{Name: "list_folders", Description: "The employee's mail folders with their ids, roles and unread counts.",
			Schema: obj(``)}, Run: s.mailAIFolders},
		{ToolSpec: ai.ToolSpec{Name: "list_letters", Description: "Letters of a folder, the newest first: id, sender, subject, date, unread, flagged and the start of the text. " +
			"folder: \"inbox\" (the inboxes of all mailboxes, by default), \"flagged\" or a folder id; unread — only unread ones; query — a search (words, from:, to:, subject:).",
			Schema: obj(`"folder":{"type":"string"},"unread":{"type":"boolean"},"query":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":50}`)},
			Run: s.mailAILetters},
		{ToolSpec: ai.ToolSpec{Name: "read_letter", Description: "A letter in full: addresses, date, files and its text; conversation — with the earlier letters of its conversation. Does not mark it read.",
			Schema: obj(`"id":{"type":"string"},"conversation":{"type":"boolean"}`, "id")}, Run: s.mailAIRead},
		{ToolSpec: ai.ToolSpec{Name: "draft_reply", Description: "Saves a draft of a reply to a letter in «Drafts» (the employee reviews and sends it); text — the reply itself, plain text in the letter's language; all — reply to all.",
			Schema: obj(`"id":{"type":"string"},"text":{"type":"string"},"all":{"type":"boolean"}`, "id", "text")}, Write: true, Run: s.mailAIDraft},
		{ToolSpec: ai.ToolSpec{Name: "mark_letters", Description: "Does an action on letters: read, unread, flag, unflag, done (flag complete), pin, unpin, archive, junk, move (to folder — its id).",
			Schema: obj(`"ids":{"type":"array","items":{"type":"string"}},"action":{"type":"string","enum":["`+strings.Join(mailAIMarks, `","`)+`"]},"folder":{"type":"string"}`, "ids", "action")},
			Write: true, Run: s.mailAIMark},
	}}
}

// mailAISystem tells the model whom it works for, when, and which letter is open.
func (s *Server) mailAISystem(ctx context.Context, userID string) string {
	o := mailAIOf(ctx)
	lang := s.portalLang()
	if o.r != nil {
		lang = s.lang(o.r)
	}
	var name string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, userID).Scan(&name)
	var boxes []string
	if list, err := s.Mail.Mailboxes(ctx, userID); err == nil {
		for _, b := range list {
			boxes = append(boxes, b.Email)
		}
	}
	open := s.I18n.T(lang, "mail.ai.no_letter")
	if o.letter != "" {
		if list, err := s.Mail.Thread(ctx, userID, o.letter, false); err == nil && len(list) > 0 {
			m := list[0]
			for _, x := range list {
				if x.ID == o.letter {
					m = x
				}
			}
			open = s.I18n.T(lang, "mail.ai.open_letter", m.ID, m.Subject, m.From())
		}
	}
	return s.I18n.T(lang, "mail.ai.prompt", name, strings.Join(boxes, ", "), time.Now().Format("2006-01-02 15:04 (Mon)"), open)
}

func (s *Server) mailAIFolders(ctx context.Context, userID string, _ json.RawMessage) (string, error) {
	list, err := s.Mail.Folders(ctx, userID, "")
	if err != nil {
		return "", err
	}
	boxes, err := s.Mail.Mailboxes(ctx, userID)
	if err != nil {
		return "", err
	}
	email := map[string]string{}
	for _, b := range boxes {
		email[b.ID] = b.Email
	}
	type folder struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Role    string `json:"role,omitempty"`
		Mailbox string `json:"mailbox"`
		Unread  int    `json:"unread"`
		Total   int    `json:"total"`
	}
	out := []folder{}
	for _, f := range list {
		if f.Selectable || f.Local {
			out = append(out, folder{f.ID, f.Name, f.Role, email[f.MailboxID], f.Unseen, f.Total})
		}
	}
	return toolJSON(out)
}

func (s *Server) mailAILetters(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct {
		Folder, Query string
		Unread        bool
		Limit         int
	}
	_ = json.Unmarshal(raw, &in)
	o := mail.ListOpts{Folder: in.Folder, Q: in.Query}
	switch in.Folder {
	case "", "inbox":
		o.Folder = mail.AllInboxes
	}
	if in.Unread {
		o.Filter = "unread"
	}
	if in.Limit <= 0 {
		in.Limit = mailAIList
	}
	in.Limit = min(in.Limit, mailAIListMax)
	list, err := s.Mail.ListBy(ctx, userID, o)
	if err != nil {
		return "", err
	}
	type letter struct {
		ID      string `json:"id"`
		From    string `json:"from"`
		To      string `json:"to,omitempty"`
		Subject string `json:"subject"`
		Date    string `json:"date"`
		Folder  string `json:"folder"`
		Unread  bool   `json:"unread,omitempty"`
		Flagged bool   `json:"flagged,omitempty"`
		High    bool   `json:"important,omitempty"`
		Files   int    `json:"files,omitempty"`
		Text    string `json:"text"`
	}
	out := []letter{}
	for i, m := range list {
		if i == in.Limit {
			break
		}
		l := letter{ID: m.ID, From: m.From(), Subject: m.Subject, Date: m.SentAt.Local().Format("2006-01-02 15:04"), Folder: m.FolderName,
			Unread: !m.Seen, Flagged: m.Flagged, High: m.Importance > 0, Files: m.Attached, Text: aiClip(m.Body, mailAIPreview)}
		if m.Outgoing() {
			l.To = m.To
		}
		out = append(out, l)
	}
	return toolJSON(out)
}

func (s *Server) mailAIRead(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct {
		ID           string
		Conversation bool
	}
	_ = json.Unmarshal(raw, &in)
	list, err := s.Mail.Thread(ctx, userID, in.ID, false)
	if err != nil {
		return "", err
	}
	type letter struct {
		ID      string   `json:"id"`
		From    string   `json:"from"`
		To      string   `json:"to"`
		CC      string   `json:"cc,omitempty"`
		Date    string   `json:"date"`
		Subject string   `json:"subject"`
		Folder  string   `json:"folder"`
		High    bool     `json:"important,omitempty"`
		Files   []string `json:"files,omitempty"`
		Text    string   `json:"text"`
	}
	of := func(m mail.Message, n int) (letter, error) {
		l := letter{ID: m.ID, From: m.From(), To: m.To, CC: m.CC, Date: m.SentAt.Local().Format("2006-01-02 15:04"), Subject: m.Subject,
			Folder: m.FolderName, High: m.Importance > 0, Text: aiClip(m.Body, n)}
		if m.FromName != "" {
			l.From += " <" + m.FromAddr + ">"
		}
		files, err := s.Mail.Files(ctx, userID, m.ID)
		for _, f := range files {
			l.Files = append(l.Files, f.Name)
		}
		return l, err
	}
	var out struct {
		Letter  letter   `json:"letter"`
		Earlier []letter `json:"earlier,omitempty"` // the oldest first
	}
	for _, m := range list {
		if m.ID == in.ID {
			if out.Letter, err = of(m, mailAIText); err != nil {
				return "", err
			}
			break
		}
		if in.Conversation {
			l, err := of(m, mailAIEarlier)
			if err != nil {
				return "", err
			}
			out.Earlier = append(out.Earlier, l)
		}
	}
	if out.Letter.ID == "" {
		return "", mail.ErrNotFound
	}
	return toolJSON(out)
}

// mailAIDraft saves a reply with the agent's text in «Черновики», as «Ответить» would make it: the addresses, the
// subject, the signature and the quote of the letter.
func (s *Server) mailAIDraft(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct {
		ID, Text string
		All      bool
	}
	_ = json.Unmarshal(raw, &in)
	if strings.TrimSpace(in.Text) == "" {
		return "", i18n.E("err.ai_text_required")
	}
	r := mailAIOf(ctx).r
	if r == nil {
		r, _ = http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	}
	r = r.WithContext(ctx)
	q := map[string][]string{"letter": {in.ID}}
	if in.All {
		q["mode"] = []string{"all"}
	}
	c, err := s.mailCompose(r, auth.Session{UserID: userID}, q)
	if err != nil {
		return "", err
	}
	text := mail.TextHTML(strings.TrimSpace(in.Text))
	c.Draft.HTML = strings.Replace(c.HTML, "<p><br></p>", "<div>"+text+"</div><p><br></p>", 1)
	id, err := s.Mail.SaveDraft(ctx, userID, c.Draft)
	if err != nil {
		return "", err
	}
	folder := ""
	if list, err := s.Mail.Thread(ctx, userID, id, false); err == nil && len(list) > 0 {
		folder = list[0].FolderID
	}
	return toolJSON(map[string]string{"draft": id, "url": mailURL(folder, id)})
}

func (s *Server) mailAIMark(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct {
		IDs            []string
		Action, Folder string
	}
	_ = json.Unmarshal(raw, &in)
	ok := false
	for _, a := range mailAIMarks {
		ok = ok || a == in.Action
	}
	if !ok || len(in.IDs) == 0 {
		return "", i18n.E("err.bad_form")
	}
	if err := s.Mail.Act(ctx, userID, in.IDs, in.Action, in.Folder); err != nil {
		return "", err
	}
	return "ok", nil
}

// mailAIPane is the pane of CoPilot.
type mailAIPane struct {
	On      bool     // an AI connection to work through
	Run     ai.Run   // the conversation shown (no ID — a new one)
	Runs    []ai.Run // the last ones, for «Разговоры»
	Items   []mailAIItem
	Error   string
	Pending bool // an action waits for «Подтвердить»
}

// mailAIItem is a turn of the conversation as the pane shows it: the employee's message or the agent's answer with
// the actions it asked for.
type mailAIItem struct {
	Me   bool
	Text string
	Acts []mailAIAct
}

type mailAIAct struct {
	ai.Action
	Text, Do, Folder, URL string // draft_reply: the reply; mark_letters: the action and the folder; a draft made: its link
	Count                 int
}

// mailAIRuns is how many past conversations «Разговоры» lists.
const mailAIRuns = 15

func (s *Server) mailAIPaneOf(r *http.Request, userID string, run ai.Run, err error) (mailAIPane, error) {
	ctx := r.Context()
	p := mailAIPane{Run: run}
	if _, _, e := s.AI.For(ctx, userID); e == nil {
		p.On = true
	} else if !errors.Is(e, ai.ErrNone) {
		return p, e
	}
	var e error
	if p.Runs, e = s.AI.Runs(ctx, userID, "mail", mailAIRuns); e != nil {
		return p, e
	}
	if err != nil {
		p.Error = s.errText(r, err)
	} else if run.Status == ai.RunError {
		p.Error = s.I18n.Err(s.lang(r), run.Err())
	}
	for _, m := range run.Messages {
		if m.Role == "user" {
			if m.Text != "" {
				p.Items = append(p.Items, mailAIItem{Me: true, Text: m.Text})
			}
			continue
		}
		it := mailAIItem{Text: m.Text}
		for _, c := range m.Calls {
			a := mailAIAct{Action: run.Action(c.ID)}
			if a.ID == "" {
				continue
			}
			var args struct {
				Text, Action, Folder string
				IDs                  []string
			}
			_ = json.Unmarshal(a.Args, &args)
			a.Text, a.Do, a.Count = args.Text, args.Action, len(args.IDs)
			if args.Folder != "" {
				if f, err := s.Mail.Folder(ctx, userID, args.Folder); err == nil {
					a.Folder = f.Name
				}
			}
			var res struct{ URL string }
			if a.Status == ai.ActDone && json.Unmarshal([]byte(a.Result), &res) == nil {
				a.URL = res.URL
			}
			p.Pending = p.Pending || a.Status == ai.ActPending
			it.Acts = append(it.Acts, a)
		}
		if it.Text != "" || len(it.Acts) > 0 {
			p.Items = append(p.Items, it)
		}
	}
	return p, nil
}

func (s *Server) mailAIRender(w http.ResponseWriter, r *http.Request, sess auth.Session, run ai.Run, err error) {
	p, e := s.mailAIPaneOf(r, sess.UserID, run, err)
	if e != nil {
		s.jsonErr(w, r, e)
		return
	}
	var buf bytes.Buffer
	if e := s.pages[s.lang(r)]["mail"].ExecuteTemplate(&buf, "mcopilot", p); e != nil {
		s.jsonErr(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// mailAIContext is the request's context for the agent: the letter open beside the pane.
func mailAIContext(r *http.Request) context.Context {
	return context.WithValue(r.Context(), mailAIReq{}, mailAIOpen{r: r, letter: r.FormValue("letter")})
}

// mailCopilot is the pane alone, for the script (static/js/mailai.js): ?run= — a past conversation.
func (s *Server) mailCopilot(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil {
		http.NotFound(w, r)
		return
	}
	var run ai.Run
	if id := r.URL.Query().Get("run"); id != "" {
		var err error
		if run, err = s.AI.GetRun(r.Context(), sess.UserID, id); err != nil || run.Agent != "mail" {
			run = ai.Run{}
		}
	}
	s.mailAIRender(w, r, sess, run, nil)
}

// mailCopilotSay sends the employee's message (text) to the agent: run — the conversation going on ("" — a new one),
// letter — the letter open.
func (s *Server) mailCopilotSay(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil {
		http.NotFound(w, r)
		return
	}
	run, err := s.AI.Say(mailAIContext(r), sess.UserID, s.mailAgent(), r.FormValue("run"), r.FormValue("text"))
	if err != nil { // the conversation as it is, with the error under it
		if id := cmp.Or(run.ID, r.FormValue("run")); id != "" {
			run, _ = s.AI.GetRun(r.Context(), sess.UserID, id)
		}
	}
	s.mailAIRender(w, r, sess, run, err)
}

// mailCopilotDecide confirms (yes=1) or rejects an action the agent waits with.
func (s *Server) mailCopilotDecide(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("run")
	run, err := s.AI.Decide(mailAIContext(r), sess.UserID, s.mailAgent(), id, r.PathValue("action"), r.FormValue("yes") == "1")
	if run.ID == "" {
		run, _ = s.AI.GetRun(r.Context(), sess.UserID, id)
	}
	s.mailAIRender(w, r, sess, run, err)
}

// mailCopilotDelete removes a conversation; the pane starts a new one.
func (s *Server) mailCopilotDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.AI == nil {
		http.NotFound(w, r)
		return
	}
	err := s.AI.DeleteRun(r.Context(), sess.UserID, r.PathValue("run"))
	s.mailAIRender(w, r, sess, ai.Run{}, err)
}

// apiAIRun is a conversation with an agent in the REST API.
type apiAIRun struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Status   string        `json:"status"` // running, confirm (an action waits), done, error
	Error    string        `json:"error,omitempty"`
	Model    string        `json:"model,omitempty"`
	Messages []apiAIMsg    `json:"messages,omitempty"`
	Actions  []apiAIAction `json:"actions,omitempty"`
	Created  time.Time     `json:"created_at"`
	Updated  time.Time     `json:"updated_at"`
}

type apiAIMsg struct {
	Role string `json:"role"` // user, assistant
	Text string `json:"text"`
}

type apiAIAction struct {
	ID     string          `json:"id"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
	Write  bool            `json:"write"`
	Status string          `json:"status"` // done, error, pending, rejected
	Result string          `json:"result,omitempty"`
}

func (s *Server) apiAIRunOf(r *http.Request, run ai.Run) apiAIRun {
	out := apiAIRun{ID: run.ID, Title: run.Title, Status: run.Status, Model: run.Model, Created: run.CreatedAt, Updated: run.UpdatedAt}
	if run.Status == ai.RunError {
		out.Error = s.I18n.Err(s.I18n.Match(r.Header.Get("Accept-Language")), run.Err())
	}
	for _, m := range run.Messages {
		if m.Text != "" {
			out.Messages = append(out.Messages, apiAIMsg{m.Role, m.Text})
		}
	}
	for _, a := range run.Actions {
		out.Actions = append(out.Actions, apiAIAction{a.ID, a.Tool, a.Args, a.Write, a.Status, a.Result})
	}
	return out
}

func (s *Server) apiMailCopilotRuns(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	list, err := s.AI.Runs(r.Context(), p.UserID, "mail", 100)
	if err != nil {
		return err
	}
	out := []apiAIRun{}
	for _, run := range list {
		out = append(out, s.apiAIRunOf(r, run))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailCopilotRun(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	run, err := s.AI.GetRun(r.Context(), p.UserID, r.PathValue("run"))
	if err == nil && run.Agent != "mail" {
		err = ai.ErrNotFound
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiAIRunOf(r, run))
	return nil
}

func (s *Server) apiMailCopilotSay(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	var in struct {
		Run    string `json:"run"`
		Text   string `json:"text"`
		Letter string `json:"letter"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx := context.WithValue(r.Context(), mailAIReq{}, mailAIOpen{r: r, letter: in.Letter})
	run, err := s.AI.Say(ctx, p.UserID, s.mailAgent(), in.Run, in.Text)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiAIRunOf(r, run))
	return nil
}

func (s *Server) apiMailCopilotDecide(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	var in struct {
		Yes bool `json:"yes"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	run, err := s.AI.Decide(context.WithValue(r.Context(), mailAIReq{}, mailAIOpen{r: r}), p.UserID, s.mailAgent(), r.PathValue("run"), r.PathValue("action"), in.Yes)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiAIRunOf(r, run))
	return nil
}
