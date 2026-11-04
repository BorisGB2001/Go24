package web

import (
	"net/http"
	"net/url"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Скоринг сделок» of the CRM (/crm/scoring) ----------

// As: an administrator turns the scoring on for a pipeline of deals; it learns from the pipeline's closed
// deals and shows on every open deal — its kanban tile and card — how likely it is to be won and why.

func (s *Server) crmScoring(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "scoring", "Min": crm.MinScoreDeals, "MinEach": crm.MinScoreEach, "Features": crm.ScoreFeatures}
	err := s.crmTabs(r, sess, data)
	if err == nil {
		data["Scoring"], err = s.CRM.ScoringList(r.Context())
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmscoring", page{TitleKey: "crm.score.tab", Nav: "crm_deals", Flash: r.URL.Query().Get("msg"), Data: data})
}

// crmScoringSave: the administrator turns the scoring of a pipeline on ("on") or off ("off") or makes it learn
// again at once ("train").
func (s *Server) crmScoringSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/crm/scoring")
		return
	}
	var err error
	pipe := r.FormValue("pipeline")
	switch r.FormValue("op") {
	case "on", "off":
		err = s.CRM.SetScoring(r.Context(), sess.UserID, pipe, r.FormValue("op") == "on")
	case "train":
		err = s.CRM.TrainScoring(r.Context(), pipe)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/scoring")
		return
	}
	http.Redirect(w, r, "/crm/scoring?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

type scoringJSON struct {
	PipelineID string     `json:"pipeline_id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	Ready      bool       `json:"ready"`
	Samples    int        `json:"samples"`
	Won        int        `json:"won"`
	Accuracy   float64    `json:"accuracy"`
	TrainedAt  *time.Time `json:"trained_at"`
	Closed     int        `json:"closed"`
}

// GET /api/v1/crm/scoring — the scoring of the pipelines of deals: on or off, what it learned from, its accuracy.
func (s *Server) apiCRMScoring(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.ScoringList(r.Context())
	if err != nil {
		return err
	}
	out := make([]scoringJSON, 0, len(list))
	for _, c := range list {
		name := c.Name
		if name == "" {
			name = s.tr(r, "crm.pipeline."+c.Code)
		}
		out = append(out, scoringJSON{PipelineID: c.PipelineID, Name: name, Enabled: c.Enabled, Ready: c.Ready() && c.Samples > 0,
			Samples: c.Samples, Won: c.Won, Accuracy: c.Accuracy, TrainedAt: c.TrainedAt, Closed: c.Closed})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/crm/scoring/{pipeline} {"enabled": true} turns the scoring on or off (administrators); {"train": true}
// makes it learn again at once.
func (s *Server) apiCRMScoringSet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	var in struct {
		Enabled *bool `json:"enabled"`
		Train   bool  `json:"train"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	pipe := r.PathValue("pipeline")
	var err error
	switch {
	case in.Enabled != nil:
		err = s.CRM.SetScoring(r.Context(), p.UserID, pipe, *in.Enabled)
	case in.Train:
		err = s.CRM.TrainScoring(r.Context(), pipe)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		return err
	}
	return s.apiCRMScoring(w, r, p)
}

// GET /api/v1/crm/deals/{id}/score — the probability of an open deal to be won and why; null — the deal is closed or
// its pipeline is not scored.
func (s *Server) apiCRMScore(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.PathValue("kind") != "deals" {
		return crm.ErrNotFound
	}
	if _, err := s.apiCRMItem(r, p, "read"); err != nil {
		return err
	}
	sc, err := s.CRM.ScoreOf(r.Context(), r.PathValue("id"))
	if err != nil || sc == nil {
		if err == nil {
			writeJSON(w, http.StatusOK, nil)
		}
		return err
	}
	type factor struct {
		Feature string `json:"feature"`
		Value   string `json:"value"`
		Text    string `json:"text"`
		Up      bool   `json:"up"`
	}
	out := struct {
		Probability int      `json:"probability"`
		Factors     []factor `json:"factors"`
	}{Probability: sc.Percent, Factors: []factor{}}
	tr := func(key string) string { return s.tr(r, key) }
	for _, f := range sc.Factors {
		text := f.Name
		switch {
		case f.Feature == "origin" || f.Feature == "deal_type":
			if text = f.Dict.Text(tr); f.Value == "none" {
				text = tr("crm.score.v.none")
			}
		case f.Feature != "responsible":
			text = tr("crm.score.v." + f.Feature + "." + f.Value)
		}
		out.Factors = append(out.Factors, factor{Feature: f.Feature, Value: f.Value, Text: text, Up: f.Up})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
