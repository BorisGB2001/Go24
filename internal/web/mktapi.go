package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/i18n"
	mailpkg "go24/internal/mail"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// REST of «Маркетинг»: campaigns (a draft made, started, paused, resumed, stopped), segments, letter templates and
// the stop list — for n8n and other systems, as the pages do it, with the key owner's rights (docs/n8n.md).

type apiCampaign struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Status       string     `json:"status"`
	MailboxID    string     `json:"mailbox_id,omitempty"`
	Subject      string     `json:"subject"`
	HTML         string     `json:"html,omitempty"`
	SendAt       *time.Time `json:"send_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Author       string     `json:"author_id,omitempty"`
	Segments     []string   `json:"segments"`
	Excluded     []string   `json:"excluded"`
	Total        int        `json:"recipients"`
	Sent         int        `json:"sent"`
	Errors       int        `json:"errors"`
	Skipped      int        `json:"skipped"`
	Opened       int        `json:"opened"`
	Clicked      int        `json:"clicked"`
	Unsubscribed int        `json:"unsubscribed"`
	AB           *apiAB     `json:"ab,omitempty"`
}

// apiAB is the A/B test of a campaign: variant B, the test's share of the addressees (%), hours, what wins (opened,
// clicked) and the winner (a, b) when chosen.
type apiAB struct {
	Subject string `json:"subject"`
	HTML    string `json:"html,omitempty"`
	Share   int    `json:"share"`
	Hours   int    `json:"hours"`
	Metric  string `json:"metric"`
	Winner  string `json:"winner,omitempty"`
}

func campaignJSON(c marketing.Campaign, body bool) apiCampaign {
	out := apiCampaign{ID: c.ID, Title: c.Title, Status: c.Status, MailboxID: c.MailboxID, Subject: c.Subject, SendAt: c.SendAt,
		StartedAt: c.StartedAt, FinishedAt: c.FinishedAt, Author: c.CreatedBy, Segments: c.Segments, Excluded: c.Excluded,
		Total: c.Total, Sent: c.Sent, Errors: c.Errors, Skipped: c.Skipped, Opened: c.Opened, Clicked: c.Clicked, Unsubscribed: c.Unsubscribed}
	if c.Planned() {
		out.Status = "planned"
	}
	if body {
		out.HTML = c.Body
	}
	if c.AB() {
		out.AB = &apiAB{Subject: c.ABSubject, Share: c.ABShare, Hours: c.ABHours, Metric: c.ABMetric, Winner: c.ABWinner}
		if body {
			out.AB.HTML = c.ABBody
		}
	}
	if out.Segments == nil {
		out.Segments = []string{}
	}
	if out.Excluded == nil {
		out.Excluded = []string{}
	}
	return out
}

// GET /api/v1/marketing/campaigns — the campaigns, the latest first.
func (s *Server) apiMktCampaigns(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.mktAllowedUser(r.Context(), p.UserID); err != nil {
		return err
	}
	list, err := s.Marketing.Campaigns(r.Context())
	if err != nil {
		return err
	}
	out := []apiCampaign{}
	for _, c := range list {
		out = append(out, campaignJSON(c, false))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/marketing/campaigns/{id} — one with its letter.
func (s *Server) apiMktCampaign(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.mktAllowedUser(r.Context(), p.UserID); err != nil {
		return err
	}
	c, err := s.Marketing.Campaign(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, campaignJSON(c, true))
	return nil
}

// POST /api/v1/marketing/campaigns — a draft: {title, mailbox_id, subject, html, segments, excluded, send_at} or
// {template_id, …} — the template's subject and letter unless given; "start": true starts it at once.
func (s *Server) apiMktAddCampaign(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if err := s.mktAllowedUser(ctx, p.UserID); err != nil {
		return err
	}
	var in struct {
		Title      string     `json:"title"`
		MailboxID  string     `json:"mailbox_id"`
		Subject    string     `json:"subject"`
		HTML       string     `json:"html"`
		TemplateID string     `json:"template_id"`
		Segments   []string   `json:"segments"`
		Excluded   []string   `json:"excluded"`
		SendAt     *time.Time `json:"send_at"`
		Start      bool       `json:"start"`
		AB         *apiAB     `json:"ab"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.TemplateID != "" {
		t, err := s.mktTemplate(r, in.TemplateID)
		if err != nil {
			return err
		}
		if in.Subject == "" {
			in.Subject = t.Subject
		}
		if in.HTML == "" {
			in.HTML = t.Body
		}
		if strings.TrimSpace(in.Title) == "" {
			in.Title = t.Name
		}
	}
	if in.MailboxID != "" {
		if s.Mail == nil {
			return i18n.E("err.not_found")
		}
		if _, err := s.Mail.Mailbox(ctx, p.UserID, in.MailboxID); err != nil {
			return err
		}
	}
	c := marketing.Campaign{Title: in.Title, MailboxID: in.MailboxID, Subject: in.Subject, Body: mailpkg.Sanitize(in.HTML),
		Segments: in.Segments, Excluded: in.Excluded, SendAt: in.SendAt}
	if in.AB != nil {
		c.ABSubject, c.ABBody, c.ABShare, c.ABHours, c.ABMetric = in.AB.Subject, mailpkg.Sanitize(in.AB.HTML), in.AB.Share, in.AB.Hours, in.AB.Metric
	}
	id, err := s.Marketing.SaveCampaign(ctx, p.UserID, false, c)
	if err != nil {
		return err
	}
	if in.Start {
		if _, err := s.Marketing.Start(ctx, p.UserID, tasks.IsAdmin(ctx), id); err != nil {
			_ = s.Marketing.DeleteCampaign(ctx, p.UserID, false, id) // nothing half-made stays
			return err
		}
	}
	if c, err = s.Marketing.Campaign(ctx, id); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, campaignJSON(c, true))
	return nil
}

// POST /api/v1/marketing/campaigns/{id}/{action} — start, pause, resume, stop, winner-a, winner-b (the A/B test ended
// with it): the author's or, an administrator's key, any.
func (s *Server) apiMktCampaignAction(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, id := r.Context(), r.PathValue("id")
	if err := s.mktAllowedUser(ctx, p.UserID); err != nil {
		return err
	}
	admin := tasks.IsAdmin(ctx)
	var err error
	switch r.PathValue("action") {
	case "start":
		_, err = s.Marketing.Start(ctx, p.UserID, admin, id)
	case "pause":
		err = s.Marketing.SetState(ctx, p.UserID, admin, id, "paused")
	case "resume":
		err = s.Marketing.SetState(ctx, p.UserID, admin, id, "sending")
	case "stop":
		err = s.Marketing.SetState(ctx, p.UserID, admin, id, "stopped")
	case "winner-a", "winner-b":
		err = s.Marketing.ChooseWinner(ctx, p.UserID, admin, id, strings.TrimPrefix(r.PathValue("action"), "winner-"))
	default:
		err = i18n.E("err.not_found")
	}
	if err != nil {
		return err
	}
	c, err := s.Marketing.Campaign(ctx, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, campaignJSON(c, false))
	return nil
}

// GET /api/v1/marketing/segments — the segments; ?count=1 — with the number of addressees the key owner gets.
func (s *Server) apiMktSegments(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if err := s.mktAllowedUser(ctx, p.UserID); err != nil {
		return err
	}
	list, err := s.Marketing.Segments(ctx)
	if err != nil {
		return err
	}
	type seg struct {
		ID     string           `json:"id"`
		Name   string           `json:"name"`
		Entity string           `json:"entity"`
		Filter marketing.Filter `json:"filter"`
		Count  *int             `json:"count,omitempty"`
	}
	out := []seg{}
	for _, g := range list {
		x := seg{ID: g.ID, Name: g.Name, Entity: g.Entity, Filter: g.Filter}
		if r.URL.Query().Get("count") == "1" {
			if a, err := s.Marketing.Audience(ctx, p.UserID, g); err == nil {
				n := len(a)
				x.Count = &n
			}
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/marketing/templates — the letter templates: ready-made ("base-…", in the key's Accept-Language) and
// the company's; ?html=1 — with the letters.
func (s *Server) apiMktTemplates(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.mktAllowedUser(r.Context(), p.UserID); err != nil {
		return err
	}
	list, err := s.Marketing.Templates(r.Context())
	if err != nil {
		return err
	}
	type tpl struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Subject string `json:"subject"`
		Base    bool   `json:"base,omitempty"`
		Author  string `json:"author_id,omitempty"`
		HTML    string `json:"html,omitempty"`
	}
	out := []tpl{}
	for _, t := range append(s.mktBaseTemplates(r), list...) {
		x := tpl{ID: t.ID, Name: t.Name, Subject: t.Subject, Base: t.CreatedBy == "", Author: t.CreatedBy}
		if r.URL.Query().Get("html") == "1" {
			x.HTML = t.Body
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/marketing/stoplist?q= — the stop list, the latest first (up to 1000).
func (s *Server) apiMktStopList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.mktAllowedUser(r.Context(), p.UserID); err != nil {
		return err
	}
	list, err := s.Marketing.StopList(r.Context(), r.URL.Query().Get("q"), 1000)
	if err != nil {
		return err
	}
	type stop struct {
		Email    string    `json:"email"`
		Campaign string    `json:"campaign_id,omitempty"`
		At       time.Time `json:"created_at"`
	}
	out := []stop{}
	for _, x := range list {
		out = append(out, stop{x.Email, x.Campaign, x.At})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/marketing/stoplist {"emails": ["a@b.c", …]} — into the stop list; {"added": n} — how many were new.
func (s *Server) apiMktStopAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.mktAllowedUser(r.Context(), p.UserID); err != nil {
		return err
	}
	var in struct {
		Emails []string `json:"emails"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	n, err := s.Marketing.AddStop(r.Context(), p.UserID, strings.Join(in.Emails, "\n"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": n})
	return nil
}

// DELETE /api/v1/marketing/stoplist/{email} — off the stop list: campaigns go to it again.
func (s *Server) apiMktStopRemove(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.mktAllowedUser(r.Context(), p.UserID); err != nil {
		return err
	}
	if err := s.Marketing.RemoveStop(r.Context(), r.PathValue("email")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiCalls struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Status    string      `json:"status"`
	SegmentID string      `json:"segment_id,omitempty"`
	Script    string      `json:"script"`
	Deadline  *time.Time  `json:"deadline,omitempty"`
	Assign    string      `json:"assign"`
	Staff     []string    `json:"staff"`
	StartedAt *time.Time  `json:"started_at,omitempty"`
	Author    string      `json:"author_id,omitempty"`
	Total     int         `json:"cards"`
	Done      int         `json:"done"`
	Items     []apiCallee `json:"items,omitempty"`
}

type apiCallee struct {
	Entity      string     `json:"entity"`
	EntityID    string     `json:"entity_id"`
	Name        string     `json:"name"`
	Phone       string     `json:"phone"`
	Responsible string     `json:"responsible_id,omitempty"`
	ActivityID  string     `json:"activity_id,omitempty"`
	DoneAt      *time.Time `json:"done_at,omitempty"`
}

func callsJSON(c marketing.CallList) apiCalls {
	out := apiCalls{ID: c.ID, Title: c.Title, Status: c.Status, SegmentID: c.SegmentID, Script: c.Script, Deadline: c.Deadline, Assign: c.Assign,
		Staff: c.Staff, StartedAt: c.StartedAt, Author: c.CreatedBy, Total: c.Total, Done: c.Done}
	if out.Staff == nil {
		out.Staff = []string{}
	}
	return out
}

// GET /api/v1/marketing/calls — the call lists («Обзвон»), the latest first.
func (s *Server) apiMktCalls(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.mktAllowedUser(r.Context(), p.UserID); err != nil {
		return err
	}
	list, err := s.Marketing.CallLists(r.Context())
	if err != nil {
		return err
	}
	out := []apiCalls{}
	for _, c := range list {
		out = append(out, callsJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/marketing/calls/{id} — one with its cards (?done=1 — called, 0 — not yet; up to 1000 from ?offset=).
func (s *Server) apiMktCall(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if err := s.mktAllowedUser(ctx, p.UserID); err != nil {
		return err
	}
	c, err := s.Marketing.CallList(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, _, err := s.Marketing.CallItems(ctx, c.ID, r.URL.Query().Get("done"), 1000, max(off, 0))
	if err != nil {
		return err
	}
	out := callsJSON(c)
	out.Items = []apiCallee{}
	for _, x := range items {
		out.Items = append(out.Items, apiCallee{x.Entity, x.EntityID, x.Name, x.Phone, x.ResponsibleID, x.ActivityID, x.DoneAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// POST /api/v1/marketing/calls — {title, segment_id, script, deadline, assign: owner|staff, staff, start}: a call list,
// started at once with start.
func (s *Server) apiMktAddCalls(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if err := s.mktAllowedUser(ctx, p.UserID); err != nil {
		return err
	}
	var in struct {
		Title     string     `json:"title"`
		SegmentID string     `json:"segment_id"`
		Script    string     `json:"script"`
		Deadline  *time.Time `json:"deadline"`
		Assign    string     `json:"assign"`
		Staff     []string   `json:"staff"`
		Start     bool       `json:"start"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.Marketing.SaveCallList(ctx, p.UserID, false, marketing.CallList{Title: in.Title, SegmentID: in.SegmentID, Script: in.Script,
		Deadline: in.Deadline, Assign: in.Assign, Staff: in.Staff})
	if err != nil {
		return err
	}
	if in.Start {
		if _, err := s.Marketing.StartCallList(ctx, p.UserID, tasks.IsAdmin(ctx), id); err != nil {
			_ = s.Marketing.DeleteCallList(ctx, p.UserID, false, id) // nothing half-made stays
			return err
		}
	}
	c, err := s.Marketing.CallList(ctx, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, callsJSON(c))
	return nil
}
