package web

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go24/internal/ai"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// CoPilot fills the client's CRM card from a conversation, as «заполнение полей по звонку»: the AI reads
// the record's text and finds the values of the fields of the card being worked on — the lead, or the client's open
// deal. An empty field is filled at once, a filled one is offered in the card's timeline («Применить» / «Отклонить»).

// crmKinds: a CRM entity → its address part.
var crmKinds = map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}

// phoneFill: «Заполнить карточку» — CoPilot fills the client's card from the conversation through the AI of the one
// who asks (they must be able to change the card); back to where the button was.
func (s *Server) phoneFill(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/telephony/calls"
	if ref, err := url.Parse(r.Referer()); err == nil && strings.HasPrefix(ref.Path, "/") && !strings.HasPrefix(ref.Path, "//") {
		back = ref.RequestURI()
	}
	if s.Telephony == nil || s.Telephony.Records == "" || s.CRM == nil {
		http.NotFound(w, r)
		return
	}
	c, err := s.Telephony.CallByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && c.Record == "") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony fill", err)
		return
	}
	if !s.phoneHears(r.Context(), sess, c) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	entity, id, err := s.phoneFillCheck(r, sess, c)
	if err == nil {
		_, err = s.phoneFillCard(r.Context(), c, sess.UserID, entity, id)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// phoneFillCheck finds the card the call fills and checks that the one who asks may change it.
func (s *Server) phoneFillCheck(r *http.Request, sess auth.Session, c telephony.Call) (string, string, error) {
	if c.CRMEntity == "" || c.CRMID == "" {
		return "", "", i18n.E("err.tel_fill_none")
	}
	entity, id, err := s.CRM.FillTarget(r.Context(), c.CRMEntity, c.CRMID)
	if err == nil && id == "" {
		err = i18n.E("err.tel_fill_none")
	}
	if err == nil {
		err = s.crmCheck(r, sess, crmKinds[entity], id, "write")
	}
	return entity, id, err
}

// phoneFillAuto fills the client's card by itself after the record is turned into text, when it is turned on.
func (s *Server) phoneFillAuto(ctx context.Context, id string) {
	if on, err := s.Telephony.Filling(ctx); err != nil || !on || s.CRM == nil {
		return
	}
	c, err := s.Telephony.CallByID(ctx, id)
	if err != nil || c.Filled != nil || c.CRMEntity == "" {
		return
	}
	entity, card, err := s.CRM.FillTarget(ctx, c.CRMEntity, c.CRMID)
	if err == nil && card != "" {
		_, err = s.phoneFillCard(ctx, c, "", entity, card)
	}
	if err != nil && !errors.Is(err, ai.ErrNone) {
		s.Log.Error("telephony fill", "err", err, "call", id)
	}
}

// phoneFillCard has the AI find the values of the card's fields in the conversation (the record's text is made first
// if there is none): empty fields are filled, the rest offered; CoPilot's line goes to the card's timeline when it found
// anything. The AI is that of userID, "" — of the employee who talked; the changes are theirs.
func (s *Server) phoneFillCard(ctx context.Context, c telephony.Call, userID, entity, id string) (crm.Fill, error) {
	fill := crm.Fill{Call: c.ID}
	if s.AI == nil {
		return fill, ai.ErrNone
	}
	if c.Transcript == "" {
		if err := s.phoneTranscribe(ctx, c); err != nil {
			return fill, err
		}
		var err error
		if c, err = s.Telephony.CallByID(ctx, c.ID); err != nil {
			return fill, err
		}
	}
	if strings.TrimSpace(c.Transcript) == "" || c.Transcript == s.portalTr("im.voice_silent") {
		return fill, i18n.E("err.tel_summary_silent")
	}
	number, _, emp := c.Outside()
	if emp == "" {
		emp = cmp.Or(c.FromUser.UserID, c.ToUser.UserID)
	}
	if userID == "" {
		userID = emp
	}
	if userID == "" {
		return fill, ai.ErrNone
	}
	fields, err := s.CRM.FillFields(ctx, entity, id)
	if err != nil {
		return fill, err
	}
	lang := s.portalLang()
	name := func(f crm.FillField) string { return cmp.Or(f.Name, s.I18n.T(lang, "crm.f."+f.Key)) }
	var b strings.Builder
	b.WriteString(s.I18n.T(lang, "tel.summary_call", c.From, c.To, c.Duration()))
	if c.CRMName != "" {
		b.WriteString("\n" + s.I18n.T(lang, "tel.summary_client", c.CRMName, number))
	}
	b.WriteString("\n\n" + s.I18n.T(lang, "tel.fill_fields") + "\n")
	for _, f := range fields {
		line := map[string]any{"key": f.Key, "name": name(f), "type": f.Kind, "value": f.Value}
		if len(f.Options) > 0 {
			line["options"] = f.Options
		}
		j, _ := json.Marshal(line)
		b.Write(j)
		b.WriteString("\n")
	}
	b.WriteString("\n" + s.I18n.T(lang, "tel.fill_transcript") + "\n" + c.Transcript)
	text, err := s.AI.Write(ctx, userID, "telephony", s.I18n.T(lang, "tel.fill_prompt"), b.String())
	if err != nil {
		return fill, err
	}
	found := parseFill(text)
	author := cmp.Or(emp, userID)
	for _, f := range fields {
		v, ok := crm.FillValue(f, found[f.Key])
		if !ok || strings.EqualFold(v, strings.TrimSpace(f.Value)) {
			continue
		}
		it := crm.FillItem{Field: f.Key, Name: f.Name, Old: f.Value, New: v, State: "offered"}
		if f.Kind == "date" || f.Kind == "bool" {
			it.Kind = f.Kind
		}
		if strings.TrimSpace(f.Value) == "" {
			if err := s.CRM.SetCardFields(ctx, author, entity, id, map[string]string{f.Key: v}); err != nil {
				s.Log.Warn("telephony fill field", "err", err, "call", c.ID, "field", f.Key)
				continue
			}
			it.State = "filled"
		}
		fill.Items = append(fill.Items, it)
	}
	if len(fill.Items) > 0 {
		if err := s.CRM.AddFill(ctx, entity, id, author, fill); err != nil {
			return fill, err
		}
	}
	return fill, s.Telephony.SetFilled(ctx, c.ID)
}

// parseFill reads the AI's answer: a JSON object of field keys and values, maybe wrapped in words or a code block.
func parseFill(text string) map[string]string {
	out := map[string]string{}
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j < i {
		return out
	}
	var raw map[string]any
	if json.Unmarshal([]byte(text[i:j+1]), &raw) != nil {
		return out
	}
	for k, v := range raw {
		switch v := v.(type) {
		case string:
			out[k] = v
		case float64:
			out[k] = strconv.FormatFloat(v, 'f', -1, 64)
		case bool:
			if v {
				out[k] = "1"
			}
		}
	}
	return out
}

// crmFillRow is a field of CoPilot's line of a CRM timeline as the card shows it.
type crmFillRow struct{ Label, Old, New, State string }

// crmFillDecide: «Применить» / «Отклонить» an offered value of CoPilot's line in a card's timeline.
func (s *Server) crmFillDecide(r *http.Request, sess auth.Session, entity, id string, ev int64) error {
	n, err := strconv.Atoi(r.FormValue("n"))
	if err != nil {
		return crm.ErrNotFound
	}
	return s.CRM.DecideFill(r.Context(), sess.UserID, entity, id, ev, n, r.FormValue("apply") == "1")
}
