package matrixsync

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/matrix"
	"go24/internal/tasks"
)

// CoPilot in the messenger (migration 620): a chat bot built into go24 — people write to it in a
// private chat or add it to a group chat and mention it (or answer its message) there; go24 worker reads its chats
// as any bot's and answers through the AI connection of the one who wrote (Service.CoPilotAsk), with the chat's last
// messages for context; while it thinks, it is typing. A thread is answered in the thread, with the thread's history.
// In a «Чат с CoPilot» of a collab (CreateCollabCoPilot) it answers every message and is told of the collab as the
// one who wrote sees it: its people and tasks (copilotAbout).

// CoPilot is the builtin name of the CoPilot bot.
const CoPilot = "copilot"

// copilotHistory is how many of the chat's last events CoPilot reads for context.
const copilotHistory = 40

// What CoPilot is told of a collab: at most so many open tasks, and the tasks completed in the last days.
const (
	copilotOpenTasks = 150
	copilotDoneTasks = 30
	copilotDoneDays  = 14
)

// Turn is one message of a chat as CoPilot is given it: its own (Bot) or a person's, named in a group chat.
type Turn struct {
	Bot  bool
	Text string
}

// EnsureCoPilot makes the CoPilot bot with its Matrix account (go24 worker, at start) when there is a way to answer
// (CoPilotAsk), keeping its name in the installation's language.
func (s *Service) EnsureCoPilot(ctx context.Context) error {
	if s.CoPilotAsk == nil {
		return nil
	}
	name := strings.TrimSpace(s.CoPilotName)
	if name == "" {
		name = "CoPilot"
	}
	var id, mxid, old string
	err := s.DB.QueryRow(ctx, `SELECT id::text, mxid, name FROM im_bots WHERE builtin = $1`, CoPilot).Scan(&id, &mxid, &old)
	if errors.Is(err, pgx.ErrNoRows) {
		mxid = s.M.UserID(CoPilot)
		err = s.DB.QueryRow(ctx, `INSERT INTO im_bots (builtin, code, name, mxid) VALUES ($1, $1, $2, $3)
			ON CONFLICT (builtin) WHERE builtin <> '' DO UPDATE SET name = EXCLUDED.name RETURNING id::text`, CoPilot, name, mxid).Scan(&id)
		old = ""
	}
	if err != nil || old == name {
		return err
	}
	if old != "" {
		if _, err := s.DB.Exec(ctx, `UPDATE im_bots SET name = $2 WHERE id::text = $1`, id, name); err != nil {
			return err
		}
	}
	return s.M.PutUser(ctx, mxid, name, "", "", false)
}

// CoPilotID is the ID of the CoPilot bot ("" — there is none: no AI in this installation).
func (s *Service) CoPilotID(ctx context.Context) (string, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id::text FROM im_bots WHERE builtin = $1`, CoPilot).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// CoPilotChat finds or starts the employee's private chat with CoPilot (a guest has none).
func (s *Service) CoPilotChat(ctx context.Context, userID string) (string, error) {
	id, err := s.CoPilotID(ctx)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", ErrNoAccount
	}
	return s.DirectChat(ctx, userID, id)
}

// copilotAnswer has CoPilot answer the message ev of userID in the room (of the collab, "" — of none): the history of
// the chat (or of the thread) goes to CoPilotAsk, its answer — or what went wrong — comes back as CoPilot's message.
func (s *Service) copilotAnswer(ctx context.Context, b Bot, tok, room, collab, userID string, ev matrix.Event) {
	root := ""
	if rel, _ := ev.Content["m.relates_to"].(map[string]any); rel["rel_type"] == "m.thread" {
		root, _ = rel["event_id"].(string)
	}
	_ = s.M.Typing(ctx, tok, room, b.mxid, true)
	text, err := s.copilotReply(ctx, b, tok, room, root, collab, userID, ev)
	_ = s.M.Typing(ctx, tok, room, b.mxid, false)
	if err != nil {
		if s.CoPilotError == nil {
			s.Log.Error("matrix: copilot", "room", room, "err", err)
			return
		}
		// a notice: what went wrong is no part of the conversation the next time
		content := textContent(s.CoPilotError(err))
		content["msgtype"] = "m.notice"
		if _, err := s.post(ctx, b.mxid, tok, room, root, content); err != nil {
			s.Log.Error("matrix: copilot answer", "room", room, "err", err)
		}
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if _, err := s.BotSend(ctx, b.ID, room, text, root, BotParts{}); err != nil {
		s.Log.Error("matrix: copilot answer", "room", room, "err", err)
	}
}

// copilotReply reads the conversation and asks for the answer.
func (s *Service) copilotReply(ctx context.Context, b Bot, tok, room, root, collab, userID string, ev matrix.Event) (string, error) {
	about := ""
	if collab != "" {
		var err error
		if about, err = s.copilotAbout(ctx, userID, collab, time.Now()); err != nil {
			return "", err
		}
	}
	evs, _, err := s.M.Messages(ctx, tok, room, "", copilotHistory)
	if err != nil {
		return "", err
	}
	mem, err := s.M.Members(ctx, room, tok)
	if err != nil {
		return "", err
	}
	private, _ := s.botPeer(ctx, b.mxid, mem)
	private = private && collab == "" // a collab's chat names its people, though one may be alone in it yet
	senders := []string{}
	for _, e := range evs {
		senders = append(senders, e.Sender)
	}
	who, err := s.people(ctx, senders)
	if err != nil {
		return "", err
	}
	// newest first: the message itself is among them (unless the history is behind), the rest before it
	seen := false
	var turns []Turn
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		if e.EventID == ev.EventID {
			seen = true
		}
		if t, ok := copilotTurn(e, root, b.mxid, private, who); ok {
			turns = append(turns, t)
		}
	}
	if !seen {
		if t, ok := copilotTurn(ev, root, b.mxid, private, who); ok {
			turns = append(turns, t)
		}
	}
	return s.CoPilotAsk(ctx, userID, about, mergeTurns(turns))
}

// copilotTurn is an event of the chat as a turn of the conversation: a text message of the main timeline (or of the
// thread root's thread and the root itself), not a notice; a person's text in a group chat goes with their name.
func copilotTurn(e matrix.Event, root, botMXID string, private bool, who map[string]imPerson) (Turn, bool) {
	if e.Type != "m.room.message" {
		return Turn{}, false
	}
	body, _ := e.Content["body"].(string)
	mt, _ := e.Content["msgtype"].(string)
	rel, _ := e.Content["m.relates_to"].(map[string]any)
	if body == "" || mt == "m.notice" || rel["rel_type"] == "m.replace" {
		return Turn{}, false
	}
	thread, _ := rel["event_id"].(string)
	if rel["rel_type"] != "m.thread" {
		thread = ""
	}
	if root != "" && e.EventID != root && thread != root || root == "" && thread != "" {
		return Turn{}, false
	}
	body = strings.TrimSpace(stripReplyFallback(body))
	if e.Sender == botMXID {
		return Turn{Bot: true, Text: body}, true
	}
	if p, ok := who[e.Sender]; ok && !private && p.Name != "" {
		body = p.Name + ": " + body
	}
	return Turn{Text: body}, true
}

// mergeTurns makes the turns alternate, as the models want: the turns of one side in a row become one, the
// conversation starts with a person's (CoPilot's greeting is dropped).
func mergeTurns(turns []Turn) []Turn {
	var out []Turn
	for _, t := range turns {
		if len(out) == 0 && t.Bot {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Bot == t.Bot {
			out[n-1].Text += "\n\n" + t.Text
			continue
		}
		out = append(out, t)
	}
	return out
}

// copilotAbout is what CoPilot knows of the collab in its «Чат с CoPilot», as the one who asks sees it (their tasks'
// visibility): the collab, its people and its tasks — the open ones (overdue marked) and those completed lately —
// for the status of the collab, its risks and a task to find. The model reads it; it is no text for people.
func (s *Service) copilotAbout(ctx context.Context, userID, projectID string, now time.Time) (string, error) {
	p, err := s.collabFor(ctx, userID, projectID)
	if err != nil {
		return "", err
	}
	members, err := s.Tasks.Members(ctx, p.ID)
	if err != nil {
		return "", err
	}
	list, err := s.Tasks.ListTasks(ctx, tasks.Filter{ProjectID: p.ID, Viewer: userID})
	if err != nil {
		return "", err
	}
	day := func(t time.Time) string { return t.In(now.Location()).Format("2006-01-02") }
	var b strings.Builder
	fmt.Fprintf(&b, "Collab «%s» (key %s)", p.Name, p.Key)
	if p.Archived {
		b.WriteString(", archived")
	}
	b.WriteString("\n")
	if d := strings.TrimSpace(p.Description); d != "" {
		fmt.Fprintf(&b, "Description: %s\n", d)
	}
	if p.DateStart != nil || p.DateFinish != nil {
		b.WriteString("Dates:")
		if p.DateStart != nil {
			fmt.Fprintf(&b, " from %s", day(*p.DateStart))
		}
		if p.DateFinish != nil {
			fmt.Fprintf(&b, " to %s", day(*p.DateFinish))
		}
		b.WriteString("\n")
	}
	b.WriteString("People:\n")
	for _, m := range members {
		fmt.Fprintf(&b, "- %s (%s", m.Name, m.Role)
		if m.Guest {
			b.WriteString(", guest")
		}
		b.WriteString(")\n")
	}
	var open, done []tasks.Task
	overdue := 0
	for _, t := range list {
		switch {
		case !t.Status.Closed():
			open = append(open, t)
			if t.Overdue() {
				overdue++
			}
		case t.ClosedAt != nil && now.Sub(*t.ClosedAt) < copilotDoneDays*24*time.Hour:
			done = append(done, t)
		}
	}
	fmt.Fprintf(&b, "Tasks: %d open (%d overdue), %d completed in the last %d days. A task's page is /t/KEY.\n",
		len(open), overdue, len(done), copilotDoneDays)
	line := func(t tasks.Task) {
		fmt.Fprintf(&b, "- %s «%s»: %s", t.Key(), t.Title, t.Status)
		if t.ResponsibleName != "" {
			fmt.Fprintf(&b, ", responsible %s", t.ResponsibleName)
		}
		if t.Deadline != nil {
			fmt.Fprintf(&b, ", deadline %s", t.Deadline.In(now.Location()).Format("2006-01-02 15:04"))
			if t.Overdue() {
				b.WriteString(" (OVERDUE)")
			}
		}
		if t.Priority > 1 {
			b.WriteString(", high priority")
		}
		if t.SubTotal > 0 {
			fmt.Fprintf(&b, ", subtasks %d/%d done", t.SubDone, t.SubTotal)
		}
		if t.ClosedAt != nil {
			fmt.Fprintf(&b, ", completed %s", day(*t.ClosedAt))
		}
		b.WriteString("\n")
	}
	if len(open) > 0 {
		b.WriteString("Open tasks (by deadline):\n")
		for i, t := range open {
			if i == copilotOpenTasks {
				fmt.Fprintf(&b, "- … and %d more\n", len(open)-i)
				break
			}
			line(t)
		}
	}
	if len(done) > 0 {
		b.WriteString("Completed lately:\n")
		for i, t := range done {
			if i == copilotDoneTasks {
				break
			}
			line(t)
		}
	}
	return b.String(), nil
}
