package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/mail"
)

// ---------- «Почта»: «Предлагаемые ответы», as Outlook's suggested replies ----------

// Under a letter received (mail.Suggestable) the reading pane offers a few short answers the user's AI connection
// writes (the tokens go to the usage as the mail agent's); a click opens the reply under the letter with the answer
// in it (mailCompose: ?say=). The page asks for them after the letter is shown, so it does not wait for the model;
// they are kept with the letter (mail.Suggested). «Параметры → Создание и ответ → Показывать предлагаемые ответы».

// mailSgText is how much of a letter's text goes to the model.
const mailSgText = 6000

func (s *Server) mailRepliesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mail/letters/{letter}/suggest", s.requireUser(s.mailRepliesPost))
	mux.HandleFunc("POST /api/v1/mail/messages/{id}/suggest", s.api("mail:write", s.apiMailReplies))
}

// mailSgState is what the reading pane shows of «Предлагаемые ответы»: the kept answers, or Ask — the script asks.
type mailSgState struct {
	Replies []string
	Ask     bool
}

// mailRepliesData fills «Sg» of the reading pane: nothing for a letter not answered this way, with the setting off
// or no AI connection.
func (s *Server) mailRepliesData(r *http.Request, userID string, m mail.Message, on bool, data map[string]any) error {
	if !on || !mail.Suggestable(m) || s.AI == nil {
		return nil
	}
	ctx := r.Context()
	replies, ok, err := s.Mail.Suggested(ctx, userID, m.ID)
	if err != nil || ok {
		if len(replies) > 0 {
			data["Sg"] = &mailSgState{Replies: replies}
		}
		return err
	}
	if _, _, err := s.AI.For(ctx, userID); errors.Is(err, ai.ErrNone) {
		return nil
	} else if err != nil {
		return err
	}
	data["Sg"] = &mailSgState{Ask: true}
	return nil
}

// mailReplies is the answers offered to the user's letter: the kept ones, else the model's (kept then).
func (s *Server) mailReplies(r *http.Request, userID, id string) ([]string, error) {
	ctx := r.Context()
	if replies, ok, err := s.Mail.Suggested(ctx, userID, id); err != nil || ok {
		return replies, err
	}
	m, err := s.Mail.Letter(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if !mail.Suggestable(m) {
		return []string{}, nil
	}
	if s.AI == nil {
		return nil, ai.ErrNone
	}
	from := m.FromName
	if from == "" {
		from = m.FromAddr
	}
	in, _ := json.Marshal(map[string]string{"from": from, "subject": m.Subject, "text": aiClip(mail.TranslateText(m.Body), mailSgText)})
	reply, err := s.AI.Write(ctx, userID, "mail", s.I18n.T("en-US", "mail.sug.prompt", mail.MaxSuggestions), string(in))
	if err != nil {
		return nil, err
	}
	return s.Mail.SaveSuggestions(ctx, userID, id, mailSgReply(reply))
}

// mailSgReply reads the model's answer, a JSON array of strings; anything else — no answers.
func mailSgReply(reply string) []string {
	var out []string
	if i, j := strings.Index(reply, "["), strings.LastIndex(reply, "]"); i >= 0 && j > i {
		json.Unmarshal([]byte(reply[i:j+1]), &out)
	}
	return out
}

// mailRepliesPost is the page asking for the answers of the letter shown: {"replies": […]}.
func (s *Server) mailRepliesPost(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	replies, err := s.mailReplies(r, sess.UserID, r.PathValue("letter"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replies": replies})
}

// apiMailReplies: POST /api/v1/mail/messages/{id}/suggest — the short answers offered to the letter, {"replies": […]}
// (empty — it needs no answer or is not one answered this way).
func (s *Server) apiMailReplies(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	replies, err := s.mailReplies(r, p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"replies": replies})
	return nil
}
