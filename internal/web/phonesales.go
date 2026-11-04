package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// ---------- «Скрипты продаж»: the conversation's scenario in the softphone's call card ----------

type salesAnswerJSON struct {
	Label string `json:"label"`
	Next  int    `json:"next"` // the block's index, -1 — the script's end
}

type salesBlockJSON struct {
	Title   string            `json:"title"`
	Text    string            `json:"text"`
	Answers []salesAnswerJSON `json:"answers"`
}

type salesScriptJSON struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Calls  string           `json:"calls"` // all | in | out
	Active bool             `json:"active"`
	Blocks []salesBlockJSON `json:"blocks"`
}

// salesView is a sales script with its blocks parsed: the settings show its walk, the card and REST walk it.
type salesView struct {
	telephony.SalesScript
	Blocks []salesBlockJSON
}

func (v salesView) JSON() salesScriptJSON {
	return salesScriptJSON{ID: v.ID, Name: v.Name, Calls: v.Calls, Active: v.Active, Blocks: v.Blocks}
}

// salesViews parses the scripts; one that does not parse (it always did when saved) has no blocks.
func salesViews(list []telephony.SalesScript) []salesView {
	out := make([]salesView, len(list))
	for i, sc := range list {
		out[i] = salesView{SalesScript: sc, Blocks: []salesBlockJSON{}}
		blocks, _ := telephony.ParseSalesScript(sc.Body)
		for _, b := range blocks {
			jb := salesBlockJSON{Title: b.Title, Text: b.Text, Answers: []salesAnswerJSON{}}
			for _, a := range b.Answers {
				jb.Answers = append(jb.Answers, salesAnswerJSON(a))
			}
			out[i].Blocks = append(out[i].Blocks, jb)
		}
	}
	return out
}

// cardScripts are the active sales scripts of the call card for a call of the direction (in, out; else — all calls).
func (s *Server) cardScripts(ctx context.Context, dir string) ([]salesScriptJSON, error) {
	if dir != "in" && dir != "out" {
		dir = "all"
	}
	list, err := s.Telephony.SalesScripts(ctx, dir)
	if err != nil {
		return nil, err
	}
	out := []salesScriptJSON{}
	for _, v := range salesViews(list) {
		if len(v.Blocks) > 0 {
			out = append(out, v.JSON())
		}
	}
	return out, nil
}

// salesScriptSave adds or changes a sales script (administrators of telephony).
func (s *Server) salesScriptSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	sc := telephony.SalesScript{ID: r.PathValue("id"), Name: r.FormValue("name"), Body: r.FormValue("body"),
		Calls: r.FormValue("calls"), Active: r.FormValue("active") == "1"}
	if _, err := s.Telephony.SaveSalesScript(r.Context(), sess.UserID, sc); err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.taskErr(w, r, err, "/settings/telephony#sales")
		return
	}
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#sales", http.StatusSeeOther)
}

// salesScriptDelete removes a sales script (administrators of telephony).
func (s *Server) salesScriptDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.DeleteSalesScript(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, telephony.ErrNotFound) {
		s.taskErr(w, r, err, "/settings/telephony#sales")
		return
	}
	http.Redirect(w, r, "/settings/telephony#sales", http.StatusSeeOther)
}

// apiTelSalesScripts are the sales scripts with their blocks; ?direction=in|out keeps the active ones for such calls.
func (s *Server) apiTelSalesScripts(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Telephony == nil {
		return i18n.E("err.telephony_off")
	}
	dir := r.URL.Query().Get("direction")
	if dir != "in" && dir != "out" {
		dir = ""
	}
	list, err := s.Telephony.SalesScripts(r.Context(), dir)
	if err != nil {
		return err
	}
	out := []salesScriptJSON{}
	for _, v := range salesViews(list) {
		out = append(out, v.JSON())
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": out})
	return nil
}
