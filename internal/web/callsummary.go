package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"go24/internal/ai"
	"go24/internal/calls"
	"go24/internal/i18n"
)

// ---------- «Итоги встречи», as CoPilot's: the AI sums up a recorded meeting from its transcript ----------

// summaryOn: the meetings are summed up — the AI is here and the administrators let it.
func (s *Server) summaryOn(ctx context.Context) bool {
	if s.AI == nil || s.Recordings == nil {
		return false
	}
	on, err := s.Recordings.Summarizing(ctx)
	if err != nil {
		s.Log.Warn("call summary setting", "err", err)
	}
	return on
}

// meetingSummary is what the AI answers: the meeting in short, the agreements, the tasks and the next meeting.
type meetingSummary struct {
	Summary    string        `json:"summary"`
	Agreements []string      `json:"agreements"`
	Tasks      []meetingTask `json:"tasks"`
	Next       string        `json:"next"`
}

type meetingTask struct {
	Title string `json:"title"`
	Who   string `json:"who"`
	Due   string `json:"due"`
}

// parseSummary reads the AI's JSON (in a ```json fence or with words around it as well); false — it is not JSON.
func parseSummary(answer string) (meetingSummary, bool) {
	var sm meetingSummary
	i, j := strings.Index(answer, "{"), strings.LastIndex(answer, "}")
	if i < 0 || j < i || json.Unmarshal([]byte(answer[i:j+1]), &sm) != nil {
		return sm, false
	}
	return sm, strings.TrimSpace(sm.Summary) != "" || len(sm.Agreements) > 0 || len(sm.Tasks) > 0
}

// summaryText is the summary as the call's chat or task reads it (rich: in bold, a link «Создать задачу» at each
// task — newTask is its address) or as the file on the Drive (plain).
func (s *Server) summaryText(lang, head string, sm meetingSummary, rich bool, newTask func(title string) string) string {
	bold := func(t string) string {
		if rich {
			return "[b]" + t + "[/b]"
		}
		return t
	}
	var b strings.Builder
	b.WriteString(bold(head) + "\n")
	if t := strings.TrimSpace(sm.Summary); t != "" {
		b.WriteString("\n" + t + "\n")
	}
	var agreed []string
	for _, a := range sm.Agreements {
		if a = strings.TrimSpace(a); a != "" {
			agreed = append(agreed, a)
		}
	}
	if len(agreed) > 0 {
		b.WriteString("\n" + bold(s.I18n.T(lang, "call.summary_agreements")) + "\n")
		for _, a := range agreed {
			b.WriteString("• " + a + "\n")
		}
	}
	first := true
	for _, t := range sm.Tasks {
		title := strings.TrimSpace(t.Title)
		if title == "" {
			continue
		}
		if first {
			b.WriteString("\n" + bold(s.I18n.T(lang, "call.summary_tasks")) + "\n")
			first = false
		}
		line := "• " + title
		var about []string
		if w := strings.TrimSpace(t.Who); w != "" {
			about = append(about, w)
		}
		if d := strings.TrimSpace(t.Due); d != "" {
			about = append(about, s.I18n.T(lang, "call.summary_due", d))
		}
		if len(about) > 0 {
			line += " — " + strings.Join(about, ", ")
		}
		if rich && newTask != nil {
			line += " · [url=" + newTask(title) + "]" + s.I18n.T(lang, "call.summary_new_task") + "[/url]"
		}
		b.WriteString(line + "\n")
	}
	if n := strings.TrimSpace(sm.Next); n != "" {
		b.WriteString("\n" + bold(s.I18n.T(lang, "call.summary_next")) + "\n" + n + "\n")
	}
	return b.String()
}

// summarizeMeeting has the AI of the one who started the recording (otherwise the company's) sum the meeting up from
// its transcript: the summary goes to the call's chat or task, with «Создать задачу» at each task (a subtask of the
// call's task), and into a file beside the recording. A failure is told to the call as well.
func (s *Server) summarizeMeeting(ctx context.Context, rec calls.Recording, fileID, transcript string) {
	lang := s.portalLang()
	err := s.meetingSummary(ctx, rec, fileID, transcript)
	if err == nil {
		return
	}
	s.Log.Error("call summary", "recording", rec.ID, "err", err)
	text := s.I18n.T(lang, "call.summary_failed", rec.Title)
	var ie *i18n.Error
	if errors.As(err, &ie) { // no AI connection, the AI did not answer: said why
		text += ": " + s.I18n.Err(lang, ie)
	}
	s.tellCall(ctx, rec, text, "", nil)
}

func (s *Server) meetingSummary(ctx context.Context, rec calls.Recording, fileID, transcript string) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	lang := s.portalLang()
	answer, err := s.AI.Write(ctx, rec.StartedBy, "calls", s.I18n.T(lang, "call.summary_prompt"), transcript)
	if err != nil {
		return err
	}
	if answer == "" {
		return i18n.E("err.ai_call", "empty")
	}
	head := s.I18n.T(lang, "call.summary_head", rec.Title, rec.Started.Local().Format(s.I18n.T(lang, "format.datetime")))
	q, _ := url.ParseQuery(rec.Query)
	newTask := func(title string) string {
		v := url.Values{"title": {title}}
		if key := q.Get("task"); key != "" {
			v.Set("parent", key)
		}
		return strings.TrimRight(s.BaseURL, "/") + "/tasks/new?" + v.Encode()
	}
	sm, ok := parseSummary(answer)
	plain, told := head+"\n\n"+answer+"\n", "[b]"+head+"[/b]\n\n"+answer
	if ok {
		plain, told = s.summaryText(lang, head, sm, false, nil), s.summaryText(lang, head, sm, true, newTask)
	}
	var file string
	if m, err := s.Storages.FileMeta(ctx, fileID); err == nil {
		name, id, err := s.textBeside(ctx, rec, m, s.I18n.T(lang, "call.summary_file"), plain)
		if err != nil {
			s.Log.Warn("call summary file", "recording", rec.ID, "err", err)
		} else {
			file = id
			told = strings.TrimRight(told, "\n") + "\n\n" + s.I18n.T(lang, "call.summary_saved", name, s.BaseURL+"/files/"+id)
		}
	}
	if err := s.Recordings.Summarized(ctx, rec.ID, plain, file); err != nil {
		return err
	}
	s.Log.Info("call summary saved", "recording", rec.ID, "file", file)
	s.tellCall(ctx, rec, told, "", nil)
	return nil
}
