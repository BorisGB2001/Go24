package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: «Перевести», as Outlook's translator ----------

// A letter not in the user's language (mail.Detect) gets a banner over it: «Перевести на …» and «Никогда не
// переводить с …»; with «Всегда переводить» the page asks for the translation by itself. «Ещё → Перевести» translates
// any letter into any language. The translation is made by the user's AI connection (the tokens go to the usage as
// the mail agent's) and kept with the letter (mail.Translated): the reading pane shows it in place of the letter's
// text and subject, «Показать оригинал» — the letter as it is. ?tr=LANG shows the translation into LANG, ?tr=0 — the
// original.

// mailTrText is how much of a letter's text goes to the model.
const mailTrText = 12000

func (s *Server) mailTranslateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mail/letters/{letter}/translate", s.requireUser(s.mailTranslatePost))
	mux.HandleFunc("POST /mail/settings/translate", s.requireUser(s.mailTranslateSave))
	mux.HandleFunc("POST /api/v1/mail/messages/{id}/translate", s.api("mail:write", s.apiMailTranslate))
}

// mailTrState is what the reading pane shows of «Перевести» for its letter.
type mailTrState struct {
	From, To         string // the letter's language ("" — not told), the user's
	FromName, ToName string
	Offer            bool              // the banner «Перевести на …»
	Auto             bool              // «Всегда переводить»: the page asks for it by itself
	Shown            *mail.Translation // shown in place of the letter
	ShownFrom        string            // the name of its language
	Langs            []string          // «Ещё → Перевести»: into what
	Names            map[string]string
}

// mailTrTarget is the language the user's letters are translated into: theirs from «Перевод», else the interface's.
func (s *Server) mailTrTarget(r *http.Request, p mail.TranslatePrefs) string {
	if p.To != "" {
		return p.To
	}
	if l, _, _ := strings.Cut(s.lang(r), "-"); slices.Contains(mail.Languages, l) {
		return l
	}
	return "en"
}

func (s *Server) mailTrNames(r *http.Request) map[string]string {
	out := make(map[string]string, len(mail.Languages))
	for _, l := range mail.Languages {
		out[l] = s.tr(r, "mail.tr.lang."+l)
	}
	return out
}

// mailTranslateData fills «Tr» of the reading pane: the translation shown or the banner offering it.
func (s *Server) mailTranslateData(r *http.Request, userID string, m mail.Message, data map[string]any) error {
	if m.Outgoing() || m.Draft {
		return nil
	}
	ctx := r.Context()
	p, err := s.Mail.TranslatePrefs(ctx, userID)
	if err != nil {
		return err
	}
	st := &mailTrState{To: s.mailTrTarget(r, p), From: mail.Detect(m.Subject + "\n" + m.Body), Langs: mail.Languages, Names: s.mailTrNames(r)}
	st.FromName, st.ToName = st.Names[st.From], st.Names[st.To]
	data["Tr"] = st
	want := r.URL.Query().Get("tr")
	if want == "" && p.Mode == "always" && st.From != "" && st.From != st.To && !slices.Contains(p.Skip, st.From) {
		want = st.To
	}
	if want != "" && want != "0" {
		t, ok, err := s.Mail.Translated(ctx, userID, m.ID, want)
		if err != nil {
			return err
		}
		if ok {
			st.Shown, st.ShownFrom = &t, st.Names[t.From]
			return nil
		}
	}
	if st.From == "" || st.From == st.To || p.Mode == "never" || slices.Contains(p.Skip, st.From) || s.AI == nil {
		return nil
	}
	if _, _, err := s.AI.For(ctx, userID); errors.Is(err, ai.ErrNone) {
		return nil // no AI connection — nothing to offer; «Ещё → Перевести» tells how to connect one
	} else if err != nil {
		return err
	}
	st.Offer, st.Auto = true, p.Mode == "always" && want != "0"
	return nil
}

// mailTranslate is the translation of the user's letter into lang: the kept one, else the model's (kept then).
func (s *Server) mailTranslate(r *http.Request, userID, id, lang string) (mail.Translation, error) {
	ctx := r.Context()
	if !slices.Contains(mail.Languages, lang) {
		return mail.Translation{}, i18n.E("err.bad_form")
	}
	if t, ok, err := s.Mail.Translated(ctx, userID, id, lang); err != nil || ok {
		return t, err
	}
	m, err := s.Mail.Letter(ctx, userID, id)
	if err != nil {
		return mail.Translation{}, err
	}
	if s.AI == nil {
		return mail.Translation{}, ai.ErrNone
	}
	in, _ := json.Marshal(map[string]string{"subject": m.Subject, "text": aiClip(mail.TranslateText(m.Body), mailTrText)})
	reply, err := s.AI.Write(ctx, userID, "mail", s.I18n.T("en-US", "mail.tr.prompt", s.I18n.T("en-US", "mail.tr.lang."+lang)), string(in))
	if err != nil {
		return mail.Translation{}, err
	}
	t := mail.Translation{Lang: lang, From: mail.Detect(m.Subject + "\n" + m.Body)}
	t.Subject, t.Text, t.From = mailTrReply(reply, m.Subject, t.From)
	if err := s.Mail.SaveTranslation(ctx, userID, id, t); err != nil {
		return t, err
	}
	t, _, err = s.Mail.Translated(ctx, userID, id, lang)
	return t, err
}

// mailTrReply reads the model's answer, {"from", "subject", "text"}: a model that answered with the text alone
// leaves the subject as it was.
func mailTrReply(reply, subject, from string) (string, string, string) {
	var out struct{ From, Subject, Text string }
	if i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}"); i >= 0 && j > i && json.Unmarshal([]byte(reply[i:j+1]), &out) == nil && out.Text != "" {
		if out.Subject == "" {
			out.Subject = subject
		}
		if out.From = strings.ToLower(strings.TrimSpace(out.From)); !slices.Contains(mail.Languages, out.From) {
			out.From = from
		}
		return out.Subject, strings.TrimSpace(out.Text), out.From
	}
	return subject, strings.TrimSpace(reply), from
}

// mailTrBack is the letter again, with its translation (tr=LANG) or as it is (tr=0).
func mailTrBack(r *http.Request, tr string) string {
	return "/mail?" + url.Values{"folder": {r.FormValue("folder")}, "letter": {r.PathValue("letter")}, "tr": {tr}}.Encode()
}

// mailTranslatePost is the banner's «Перевести на …» and «Никогда не переводить с …», «Ещё → Перевести».
func (s *Server) mailTranslatePost(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.FormValue("do") == "never" {
		if err := s.Mail.NeverTranslate(r.Context(), sess.UserID, r.FormValue("from")); err != nil {
			s.mailErr(w, r, err, mailTrBack(r, "0"))
			return
		}
		if fromForm(r) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		http.Redirect(w, r, mailTrBack(r, "0"), http.StatusSeeOther)
		return
	}
	lang := r.FormValue("lang")
	t, err := s.mailTranslate(r, sess.UserID, r.PathValue("letter"), lang)
	if err != nil {
		s.mailErr(w, r, err, mailTrBack(r, "0"))
		return
	}
	if fromForm(r) {
		writeJSON(w, http.StatusOK, t)
		return
	}
	http.Redirect(w, r, mailTrBack(r, lang), http.StatusSeeOther)
}

// mailTranslateSave keeps «Параметры → Перевод».
func (s *Server) mailTranslateSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), settingsURL("translate"))
		return
	}
	err := s.Mail.SaveTranslatePrefs(r.Context(), sess.UserID, mail.TranslatePrefs{Mode: r.FormValue("mode"), To: r.FormValue("to"), Skip: r.Form["skip"]})
	if err != nil {
		s.mailErr(w, r, err, settingsURL("translate"))
		return
	}
	http.Redirect(w, r, settingsURL("translate", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// apiMailTranslate: POST /api/v1/mail/messages/{id}/translate {lang} — the letter's translation ({lang, from, subject,
// text, created_at}); lang "" — into the language of the key owner's «Перевод».
func (s *Server) apiMailTranslate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Lang string `json:"lang"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Lang == "" {
		prefs, err := s.Mail.TranslatePrefs(r.Context(), p.UserID)
		if err != nil {
			return err
		}
		in.Lang = s.mailTrTarget(r, prefs)
	}
	t, err := s.mailTranslate(r, p.UserID, r.PathValue("id"), in.Lang)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, t)
	return nil
}
