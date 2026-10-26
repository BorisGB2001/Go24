package web

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/matrixsync"
)

// historyMax is how many of a chat's latest messages «Скачать историю» gives at most.
const historyMax = 20000

// imExport gives the chat's history as a text file «Скачать историю»: each message with its date,
// time and author, a thread's replies under its first message, files and polls by their names.
func (s *Server) imExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	msgs, name, err := s.Matrix.History(r.Context(), sess.UserID, r.URL.Query().Get("room"), historyMax)
	if err != nil {
		s.taskErr(w, r, err, "/im")
		return
	}
	if name == "" {
		name = s.tr(r, "im.history_chat")
	}
	file := strings.Map(func(c rune) rune {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' {
			return c
		}
		return '_'
	}, name) + "-" + time.Now().Format("2006-01-02") + ".txt"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(file))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(s.historyText(r, name, msgs, time.Now())))
}

// historyText is the chat's history as the downloaded file has it.
func (s *Server) historyText(r *http.Request, name string, msgs []matrixsync.Message, now time.Time) string {
	stamp := s.tr(r, "format.datetime")
	var b strings.Builder
	b.WriteString(s.tr(r, "im.history_title", name, now.Format(stamp)) + "\n\n")
	replies := map[string][]matrixsync.Message{}
	for _, m := range msgs {
		if m.ThreadRoot != "" {
			replies[m.ThreadRoot] = append(replies[m.ThreadRoot], m)
		}
	}
	line := func(m matrixsync.Message, indent string) {
		text := m.Body
		switch {
		case m.Encrypted:
			text = "🔒 " + s.tr(r, "im.encrypted")
		case m.Event != "":
			if t, err := s.imFragment(r, "imevent", m); err == nil {
				text = html.UnescapeString(t)
			}
		case m.Call != nil:
			text = s.callText(*m.Call)
		case m.File != nil:
			text = s.tr(r, "im.history_file", m.File.Name)
		case m.Poll != nil:
			var answers []string
			for _, a := range m.Poll.Answers {
				answers = append(answers, a.Text)
			}
			text += " (" + strings.Join(answers, " / ") + ")"
		}
		who := m.SenderName + ": "
		if m.Event != "" || m.Call != nil {
			who = ""
		}
		if m.Forwarded != "" {
			who += s.tr(r, "im.forwarded", m.Forwarded) + ": "
		}
		text = strings.ReplaceAll(text, "\n", "\n"+indent+"    ")
		b.WriteString(indent + m.At.Format(stamp) + " " + who + text + "\n")
	}
	for _, m := range msgs {
		if m.ThreadRoot != "" {
			continue
		}
		line(m, "")
		for _, rep := range replies[m.ID] {
			line(rep, "    ↳ ")
		}
	}
	return b.String()
}

// apiHistory: the chat's history as text, as «Скачать историю» gives it.
func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	msgs, name, err := s.Matrix.History(r.Context(), p.UserID, r.PathValue("room"), historyMax)
	if err != nil {
		return err
	}
	if name == "" {
		name = s.tr(r, "im.history_chat")
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(s.historyText(r, name, msgs, time.Now())))
	return nil
}
