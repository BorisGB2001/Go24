package web

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// CoPilot in tasks (GO24DEV-868): «CoPilot: чек-лист» — the AI makes a checklist of the task's title
// and description and adds it to the task as a list of its own; «CoPilot: итоги» — the AI sums the task's chat up
// (what was decided, who does what, what is still open) and posts the summary to the chat. The AI is the
// employee's connection, its tokens go to the usage as «tasks». «CoPilot» of the task's description (GO24DEV-933), as
// the CoPilot button of the editor: the AI writes the description of the title, improves, shortens it or fixes
// its mistakes, and the form shows the answer to put in place of the text or after it.

const (
	copilotItems    = 30    // items of a checklist at most
	copilotItemLen  = 200   // runes of an item
	copilotChatRune = 30000 // the last part of a long chat the AI reads
)

// copilotModes are what «CoPilot» of the description does: write it, improve, shorten, fix the mistakes.
var copilotModes = []string{"write", "improve", "shorten", "fix"}

// copilotBullet is the mark a model puts before an item: «- », «* », «• », «1. », «2) », «[ ] ».
var copilotBullet = regexp.MustCompile(`^\s*(?:[-*•–]\s+|\d{1,2}[.)]\s*|\[[ xX]?\]\s*)+`)

// copilotMarkup is the Markdown a model may put into an item despite the prompt.
var copilotMarkup = strings.NewReplacer("**", "", "__", "", "`", "")

// copilotLines are the items of the model's answer: one a line, without marks and headings.
func copilotLines(text string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		line = strings.TrimSpace(copilotBullet.ReplaceAllString(line, ""))
		line = strings.TrimSpace(copilotMarkup.Replace(line))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasSuffix(line, ":") {
			continue
		}
		if r := []rune(line); len(r) > copilotItemLen {
			line = string(r[:copilotItemLen])
		}
		if out = append(out, line); len(out) == copilotItems {
			break
		}
	}
	return out
}

// taskCopilotOn: the employee has an AI to ask (the card shows CoPilot's buttons).
func (s *Server) taskCopilotOn(ctx context.Context, userID string) bool {
	if !s.aiOn(ctx, userID, "tasks") {
		return false
	}
	_, _, err := s.AI.For(ctx, userID)
	return err == nil
}

// taskCopilotChecklist has the AI make a checklist of the task and adds it; it returns the items.
func (s *Server) taskCopilotChecklist(ctx context.Context, userID, taskID string) ([]string, error) {
	if s.AI == nil || tasks.IsGuestCtx(ctx) {
		return nil, ai.ErrNone
	}
	c, err := s.Tasks.GetCard(ctx, userID, taskID)
	if err != nil {
		return nil, err
	}
	if !c.CanWork() || c.Frozen {
		return nil, tasks.ErrForbidden
	}
	lang := s.portalLang()
	text := s.I18n.T(lang, "task.copilot_about", c.Title) + "\n\n" + c.Description
	if len(c.Checklist) > 0 {
		var b strings.Builder
		for _, it := range c.Checklist {
			b.WriteString("- " + it.Title + "\n")
		}
		text += "\n\n" + s.I18n.T(lang, "task.copilot_has_items") + "\n" + b.String()
	}
	answer, err := s.AI.Write(ctx, userID, "tasks", s.I18n.T(lang, "task.copilot_checklist_prompt"), text)
	if err != nil {
		return nil, err
	}
	items := copilotLines(answer)
	if len(items) == 0 {
		return nil, i18n.E("err.ai_call", "empty")
	}
	if _, err := s.Tasks.AddChecklistWith(ctx, userID, taskID, "CoPilot", items); err != nil {
		return nil, err
	}
	return items, nil
}

// taskCopilotSummary has the AI sum the task's chat up and posts the summary to the chat as the employee's message
// headed by CoPilot; it returns the summary.
func (s *Server) taskCopilotSummary(ctx context.Context, userID, taskID string) (string, error) {
	if s.AI == nil || tasks.IsGuestCtx(ctx) {
		return "", ai.ErrNone
	}
	c, err := s.Tasks.GetCard(ctx, userID, taskID)
	if err != nil {
		return "", err
	}
	if c.Frozen {
		return "", tasks.ErrForbidden
	}
	var b strings.Builder
	for _, m := range c.Comments {
		if strings.TrimSpace(m.Body) != "" {
			b.WriteString(m.AuthorName + ": " + m.Body + "\n")
		}
	}
	chat := b.String()
	if strings.TrimSpace(chat) == "" {
		return "", i18n.E("err.task_copilot_empty")
	}
	if r := []rune(chat); len(r) > copilotChatRune {
		chat = string(r[len(r)-copilotChatRune:])
	}
	lang := s.portalLang()
	text := s.I18n.T(lang, "task.copilot_about", c.Title) + "\n\n" + c.Description + "\n\n" + s.I18n.T(lang, "task.copilot_chat") + "\n" + chat
	summary, err := s.AI.Write(ctx, userID, "tasks", s.I18n.T(lang, "task.copilot_summary_prompt"), text)
	if err != nil {
		return "", err
	}
	if summary == "" {
		return "", i18n.E("err.ai_call", "empty")
	}
	body := s.I18n.T(lang, "task.copilot_summary_head") + "\n" + summary
	if s.Matrix == nil {
		_, err = s.Tasks.PostComment(ctx, userID, taskID, body)
	} else {
		_, err = s.Matrix.PostTaskReply(ctx, userID, taskID, "", body)
		err = s.messengerErr(err)
	}
	return summary, err
}

// taskCopilotText has the AI write (of the title and what is typed), improve, shorten or fix a task's description;
// it returns the new text, the form decides where it goes.
func (s *Server) taskCopilotText(ctx context.Context, userID, mode, title, text string) (string, error) {
	if s.AI == nil || tasks.IsGuestCtx(ctx) {
		return "", ai.ErrNone
	}
	if !slices.Contains(copilotModes, mode) {
		return "", tasks.ErrNotFound
	}
	title, text = strings.TrimSpace(title), strings.TrimSpace(text)
	if mode == "write" && title == "" && text == "" {
		return "", i18n.E("err.task_copilot_no_title")
	}
	if mode != "write" && text == "" {
		return "", i18n.E("err.task_copilot_no_text")
	}
	if r := []rune(text); len(r) > copilotChatRune {
		text = string(r[:copilotChatRune])
	}
	lang := s.portalLang()
	answer, err := s.AI.Write(ctx, userID, "tasks", s.I18n.T(lang, "task.copilot_text_prompt."+mode),
		s.I18n.T(lang, "task.copilot_about", title)+"\n\n"+text)
	if err != nil {
		return "", err
	}
	if answer = strings.TrimSpace(answer); answer == "" {
		return "", i18n.E("err.ai_call", "empty")
	}
	return answer, nil
}

// taskCopilotTextPost is POST /tasks/copilot (mode, title, text) of the task form: {text} or {error}.
func (s *Server) taskCopilotTextPost(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	text, err := s.taskCopilotText(r.Context(), sess.UserID, r.FormValue("mode"), r.FormValue("title"), r.FormValue("text"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": s.errText(r, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
}

// apiTaskCopilotText is POST /api/v1/tasks/copilot/text ({mode, title, text}) → {text}.
func (s *Server) apiTaskCopilotText(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Mode, Title, Text string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	text, err := s.taskCopilotText(r.Context(), p.UserID, in.Mode, in.Title, in.Text)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"text": text})
	return nil
}

// taskCopilot is POST /t/{key}/copilot/{what}: «CoPilot: чек-лист» (checklist) and «CoPilot: итоги» (summary).
func (s *Server) taskCopilot(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	what := r.PathValue("what")
	anchor := map[string]string{"checklist": "#checklist", "summary": "#comments"}[what]
	s.taskPost(w, r, anchor, func(id string) error {
		var err error
		switch what {
		case "checklist":
			_, err = s.taskCopilotChecklist(r.Context(), sess.UserID, id)
		case "summary":
			_, err = s.taskCopilotSummary(r.Context(), sess.UserID, id)
		default:
			err = tasks.ErrNotFound
		}
		return err
	})
}

// apiTaskCopilot is POST /api/v1/tasks/{key}/copilot/checklist ({items}) and …/copilot/summary ({text}).
func (s *Server) apiTaskCopilot(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		return err
	}
	switch r.PathValue("what") {
	case "checklist":
		items, err := s.taskCopilotChecklist(r.Context(), p.UserID, id)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	case "summary":
		text, err := s.taskCopilotSummary(r.Context(), p.UserID, id)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	default:
		return tasks.ErrNotFound
	}
	return nil
}
