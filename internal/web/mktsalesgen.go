package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// «Генератор продаж» of «Маркетинг»: repeat leads or deals for the clients of a CRM segment
// (marketing.SalesGen).

func (s *Server) mktSalesGensPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	list, err := s.Marketing.SalesGens(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	s.render(w, r, "marketing", page{TitleKey: "mkt.salesgen", Nav: "marketing", Flash: r.URL.Query().Get("msg"),
		Data: map[string]any{"Tab": "salesgen", "Gens": list}})
}

// mktSalesGenPage is a generator: the editor of a draft, the progress and the clients of a started one.
func (s *Server) mktSalesGenPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	ctx, q, id := r.Context(), r.URL.Query(), r.PathValue("id")
	g := marketing.SalesGen{Status: "draft", Target: "deal", Assign: "owner", SkipOpen: true}
	if id != "new" {
		var err error
		if g, err = s.Marketing.SalesGen(ctx, id); err != nil {
			s.taskErr(w, r, err, "/marketing/salesgen")
			return
		}
	}
	data := map[string]any{"Tab": "salesgen", "G": g, "Mine": g.ID == "" || g.CreatedBy == sess.UserID || tasks.IsAdmin(ctx), "Staff": setOf(g.Staff)}
	if g.Editable() {
		segs, err := s.Marketing.Segments(ctx)
		if err != nil {
			s.taskErr(w, r, err, "/marketing/salesgen")
			return
		}
		var clients []marketing.Segment // of contacts or companies
		for _, x := range segs {
			if x.Entity == "contact" || x.Entity == "company" {
				clients = append(clients, x)
			}
		}
		data["Segments"] = clients
		if data["Pipelines"], err = s.CRM.Pipelines(ctx); err != nil {
			s.taskErr(w, r, err, "/marketing/salesgen")
			return
		}
		if data["Users"], err = s.Tasks.ListUsers(ctx); err != nil {
			s.taskErr(w, r, err, "/marketing/salesgen")
			return
		}
		if g.SegmentID != "" {
			if list, err := s.Marketing.SalesGenClients(ctx, sess.UserID, g); err == nil {
				data["Count"] = len(list)
			} else {
				data["CountErr"] = s.errText(r, err)
			}
		}
	} else {
		const per = 100
		pg, _ := strconv.Atoi(q.Get("page"))
		pg = max(pg, 1)
		list, n, err := s.Marketing.SalesGenItems(ctx, g, q.Get("state"), per, (pg-1)*per)
		if err != nil {
			s.taskErr(w, r, err, "/marketing/salesgen")
			return
		}
		var pages []int
		for i := 1; i <= (n+per-1)/per; i++ {
			pages = append(pages, i)
		}
		data["Items"], data["State"], data["Page"], data["Pages"] = list, q.Get("state"), pg, pages
	}
	title := g.Title
	if g.ID == "" {
		title = s.tr(r, "mkt.new_salesgen")
	}
	s.render(w, r, "mktsalesgen", page{Title: title, Nav: "marketing", Flash: q.Get("msg"), Data: data})
}

// mktSalesGenSave keeps the draft; «Запустить» (go=start) then makes its cards.
func (s *Server) mktSalesGenSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	_ = r.ParseForm()
	g := marketing.SalesGen{ID: r.PathValue("id"), Title: r.FormValue("title"), SegmentID: r.FormValue("segment"), Target: r.FormValue("target"),
		CardTitle: r.FormValue("card_title"), Comment: r.FormValue("comment"), SkipOpen: r.FormValue("skip_open") != "",
		Assign: r.FormValue("assign"), Staff: r.Form["staff"]}
	if g.ID == "new" {
		g.ID = ""
	}
	g.PipelineID, g.StageID, _ = strings.Cut(r.FormValue("place"), ":") // «pipeline:stage», the stage "" — the pipeline's first
	ctx, admin := r.Context(), tasks.IsAdmin(r.Context())
	id, err := s.Marketing.SaveSalesGen(ctx, sess.UserID, admin, g)
	if err != nil {
		s.taskErr(w, r, err, "/marketing/salesgen/"+r.PathValue("id"))
		return
	}
	back, msg := "/marketing/salesgen/"+id, s.tr(r, "mkt.sg_saved")
	if r.FormValue("go") == "start" {
		n, err := s.Marketing.StartSalesGen(ctx, sess.UserID, admin, id)
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		s.Log.Info("sales generator started", "user_id", sess.UserID, "salesgen", id, "clients", n)
		msg = s.tr(r, "mkt.sg_started", n)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (s *Server) mktSalesGenDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	if err := s.Marketing.DeleteSalesGen(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/marketing/salesgen/"+r.PathValue("id"))
		return
	}
	http.Redirect(w, r, "/marketing/salesgen", http.StatusSeeOther)
}
