package matrixsync

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/i18n"
	"go24/internal/matrix"
)

// Polls of a chat, made the way Element makes them (MSC3381): the poll is an event of its own, each
// vote — a response referring to it (the latest of each person counts), and its author ends it.
const (
	pollStart    = "org.matrix.msc3381.poll.start"
	pollResponse = "org.matrix.msc3381.poll.response"
	pollEnd      = "org.matrix.msc3381.poll.end"
	pollText     = "org.matrix.msc1767.text"
	pollHidden   = "org.matrix.msc3381.poll.undisclosed"
	pollOpen     = "org.matrix.msc3381.poll.disclosed"
	PollMax      = 10 // answers of a poll at most
)

// Poll is a poll as its message shows it.
type Poll struct {
	Question string
	Answers  []PollAnswer
	Multi    bool // several answers may be chosen
	Hidden   bool // the results are shown when it is over
	Ended    bool
	Votes    int  // how many have voted
	Voted    bool // the employee has
	Mine     bool // the employee's own: they end it
}

// PollAnswer is one answer of a poll with its votes.
type PollAnswer struct {
	ID, Text string
	Count    int
	Pct      int    // of those who have voted
	Names    string // who chose it
	Mine     bool   // the employee chose it
}

// Shown: the results are to be seen — once one has voted (or the poll is over), and of a poll with hidden results
// only when it is over.
func (p *Poll) Shown() bool { return p.Ended || (p.Voted && !p.Hidden) }

func isPoll(t string) bool { return t == pollStart || t == "m.poll.start" }

// pollOf reads a poll's question and answers (nil — not a poll).
func pollOf(content map[string]any) *Poll {
	c, ok := content[pollStart].(map[string]any)
	if !ok {
		if c, ok = content["m.poll"].(map[string]any); !ok {
			return nil
		}
	}
	text := func(v any) string {
		m, _ := v.(map[string]any)
		if s, _ := m[pollText].(string); s != "" {
			return s
		}
		s, _ := m["body"].(string)
		return s
	}
	p := &Poll{Question: text(c["question"]), Hidden: c["kind"] == pollHidden || c["kind"] == "m.poll.undisclosed"}
	if n, _ := c["max_selections"].(float64); n > 1 {
		p.Multi = true
	}
	answers, _ := c["answers"].([]any)
	for _, a := range answers {
		m, _ := a.(map[string]any)
		id, _ := m["id"].(string)
		if id != "" {
			p.Answers = append(p.Answers, PollAnswer{ID: id, Text: text(m)})
		}
	}
	if p.Question == "" || len(p.Answers) == 0 {
		return nil
	}
	return p
}

func (p *Poll) has(id string) bool {
	return slices.ContainsFunc(p.Answers, func(a PollAnswer) bool { return a.ID == id })
}

// tally counts the votes of a poll from its responses and end: each person's latest response before the end (by its
// author) counts, answers it does not have are left out.
func (s *Service) tally(ctx context.Context, tok, roomID, me string, m *Message) {
	p := m.Poll
	evs, err := s.M.Relations(ctx, tok, roomID, m.ID, "m.reference")
	if err != nil {
		s.Log.Warn("matrix: poll votes", "err", err)
		return
	}
	slices.SortStableFunc(evs, func(a, b matrix.Event) int { return int(a.TS - b.TS) })
	end := int64(-1)
	for _, ev := range evs {
		if (ev.Type == pollEnd || ev.Type == "m.poll.end") && ev.Sender == m.SenderMXID {
			end = ev.TS
			break
		}
	}
	p.Ended = end >= 0
	votes := map[string][]string{}
	var order []string
	for _, ev := range evs {
		if ev.Type != pollResponse && ev.Type != "m.poll.response" || (p.Ended && ev.TS > end) {
			continue
		}
		c, _ := ev.Content[pollResponse].(map[string]any)
		if c == nil {
			c, _ = ev.Content["m.selections"].(map[string]any)
		}
		raw, _ := c["answers"].([]any)
		var ids []string
		for _, a := range raw {
			if id, _ := a.(string); p.has(id) && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if !p.Multi && len(ids) > 1 {
			ids = ids[:1]
		}
		if _, ok := votes[ev.Sender]; !ok {
			order = append(order, ev.Sender)
		}
		votes[ev.Sender] = ids
	}
	who, _ := s.people(ctx, order)
	names := make([][]string, len(p.Answers))
	for _, mx := range order {
		ids := votes[mx]
		if len(ids) == 0 { // taken back
			continue
		}
		p.Votes++
		name := strings.TrimPrefix(strings.SplitN(mx, ":", 2)[0], "@")
		if w, ok := who[mx]; ok {
			name = w.Name
		}
		for i := range p.Answers {
			if slices.Contains(ids, p.Answers[i].ID) {
				p.Answers[i].Count++
				names[i] = append(names[i], name)
				if mx == me {
					p.Answers[i].Mine, p.Voted = true, true
				}
			}
		}
	}
	for i := range p.Answers {
		if p.Votes > 0 {
			p.Answers[i].Pct = p.Answers[i].Count * 100 / p.Votes
		}
		p.Answers[i].Names = strings.Join(names[i], ", ")
	}
}

// tallyPolls counts the votes of the polls among the page's messages.
func (s *Service) tallyPolls(ctx context.Context, tok, roomID, me string, msgs []Message) {
	for i := range msgs {
		if msgs[i].Poll != nil {
			s.tally(ctx, tok, roomID, me, &msgs[i])
		}
	}
}

// pollMessage is a poll's message with its votes as they are now.
func (s *Service) pollMessage(ctx context.Context, me, tok, roomID, eventID string) (Message, error) {
	ev, err := s.M.EventAs(ctx, tok, roomID, eventID)
	if err != nil {
		return Message{}, forbidden(err)
	}
	msgs := s.convert(ctx, []matrix.Event{ev}, roomID, me, false)
	if len(msgs) == 0 || msgs[0].Poll == nil {
		return Message{}, i18n.E("err.not_found")
	}
	s.tallyPolls(ctx, tok, roomID, me, msgs)
	return msgs[0], nil
}

// CreatePoll starts a poll in the chat (in a thread when root is set): the question, 2–10 answers, whether several
// may be chosen and whether the results wait until it is over.
func (s *Service) CreatePoll(ctx context.Context, userID, roomID, root, question string, answers []string, multi, hidden bool) (Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, err
	}
	question = strings.TrimSpace(question)
	var list []any
	fallback := []string{question}
	for _, a := range answers {
		if a = strings.TrimSpace(a); a != "" {
			list = append(list, map[string]any{"id": "a" + strconv.Itoa(len(list)+1), pollText: a})
			fallback = append(fallback, strconv.Itoa(len(list))+". "+a)
		}
	}
	if question == "" || len(list) < 2 || len(list) > PollMax {
		return Message{}, i18n.E("im.poll_bad")
	}
	kind, most := pollOpen, 1
	if hidden {
		kind = pollHidden
	}
	if multi {
		most = len(list)
	}
	content := map[string]any{pollText: strings.Join(fallback, "\n"), "body": strings.Join(fallback, "\n"),
		pollStart: map[string]any{"question": map[string]any{pollText: question}, "kind": kind, "max_selections": most, "answers": list}}
	if root != "" {
		content["m.relates_to"] = map[string]any{"rel_type": "m.thread", "event_id": root, "is_falling_back": true,
			"m.in_reply_to": map[string]string{"event_id": root}}
	}
	id, err := s.M.SendEvent(ctx, roomID, pollStart, content, tok)
	if err != nil {
		return Message{}, forbidden(err)
	}
	if err := s.M.ThreadReceipt(ctx, tok, roomID, id, cmp.Or(root, "main")); err != nil {
		s.Log.Warn("read receipt after poll", "room", roomID, "err", err)
	}
	ev := matrix.Event{EventID: id, Type: pollStart, Sender: me, TS: time.Now().UnixMilli(), Content: content}
	msgs := s.convert(ctx, []matrix.Event{ev}, roomID, me, false)
	if len(msgs) == 0 {
		return Message{}, i18n.E("im.poll_bad")
	}
	return msgs[0], nil
}

// Vote gives the employee's answers to a poll (none — the vote is taken back); the poll comes back with the votes.
func (s *Service) Vote(ctx context.Context, userID, roomID, pollID string, answers []string) (Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, err
	}
	m, err := s.pollMessage(ctx, me, tok, roomID, pollID)
	if err != nil {
		return Message{}, err
	}
	if m.Poll.Ended {
		return Message{}, i18n.E("im.poll_over")
	}
	ids := []string{}
	for _, a := range answers {
		if !m.Poll.has(a) {
			return Message{}, i18n.E("err.bad_form")
		}
		if !slices.Contains(ids, a) {
			ids = append(ids, a)
		}
	}
	if !m.Poll.Multi && len(ids) > 1 {
		return Message{}, i18n.E("err.bad_form")
	}
	if _, err := s.M.SendEvent(ctx, roomID, pollResponse, map[string]any{pollResponse: map[string]any{"answers": ids},
		"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": pollID}}, tok); err != nil {
		return Message{}, forbidden(err)
	}
	return s.pollMessage(ctx, me, tok, roomID, pollID)
}

// EndPoll ends the employee's own poll (text: what other Matrix apps show of it); the results are final.
func (s *Service) EndPoll(ctx context.Context, userID, roomID, pollID, text string) (Message, error) {
	me, tok, err := s.token(ctx, userID)
	if err != nil {
		return Message{}, err
	}
	m, err := s.pollMessage(ctx, me, tok, roomID, pollID)
	if err != nil {
		return Message{}, err
	}
	if !m.Mine {
		return Message{}, i18n.E("err.forbidden")
	}
	if !m.Poll.Ended {
		if _, err := s.M.SendEvent(ctx, roomID, pollEnd, map[string]any{pollEnd: map[string]any{}, pollText: text, "body": text,
			"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": pollID}}, tok); err != nil {
			return Message{}, forbidden(err)
		}
	}
	return s.pollMessage(ctx, me, tok, roomID, pollID)
}

// livePolls: the polls whose votes the events change, as they are now (Change "poll").
func (s *Service) livePolls(ctx context.Context, me, tok, roomID string, evs []matrix.Event) []Message {
	var ids []string
	for _, ev := range evs {
		if ev.Type != pollResponse && ev.Type != pollEnd && ev.Type != "m.poll.response" && ev.Type != "m.poll.end" {
			continue
		}
		rel, _ := ev.Content["m.relates_to"].(map[string]any)
		if id, _ := rel["event_id"].(string); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	var out []Message
	for _, id := range ids {
		if m, err := s.pollMessage(ctx, me, tok, roomID, id); err == nil {
			m.Change = "poll"
			out = append(out, m)
		}
	}
	return out
}
