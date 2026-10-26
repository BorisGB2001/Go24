package web

import (
	"bytes"
	"net/http"
	"strconv"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- project kanban (stages groups) ----------

type boardColumn struct {
	tasks.StageColumn
	Cards []boardCard
}

type boardCard struct {
	tasks.Task
	tasks.CardInfo
	CanMove bool
}

func (s *Server) board(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	stageCols, err := s.Tasks.Board(r.Context(), sess.UserID, p)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	var all []tasks.Task
	for _, c := range stageCols {
		all = append(all, c.Tasks...)
	}
	cards, err := s.cards(r, sess.UserID, all)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	cols := make([]boardColumn, len(stageCols))
	for i, c := range stageCols {
		cols[i].StageColumn = c
		for _, t := range c.Tasks {
			cols[i].Cards = append(cols[i].Cards, cards[t.ID])
		}
	}
	data := map[string]any{"Project": p, "Columns": cols, "Colors": tasks.StageColors, "FinishLimit": tasks.FinishLimit}
	if p.Scrum() { // the kanban of a scrum team is its active sprint
		sp, err := s.Tasks.ActiveSprint(r.Context(), p.ID)
		if err != nil {
			s.taskErr(w, r, err, "/projects")
			return
		}
		plan, err := s.Tasks.ScrumPlanFor(r.Context(), sess.UserID, p)
		if err != nil {
			s.taskErr(w, r, err, "/projects")
			return
		}
		var planned []tasks.Sprint
		for _, x := range plan.Sprints {
			if x.Status == "planned" {
				planned = append(planned, x)
			}
		}
		data["Sprint"], data["Planned"] = sp, planned
	}
	s.render(w, r, "board", page{Title: p.Name, Nav: projectNav(p), Data: data})
}

// cards prepares kanban cards: counters and whether the user may drag each one.
func (s *Server) cards(r *http.Request, userID string, list []tasks.Task) (map[string]boardCard, error) {
	ids := make([]string, len(list))
	for i, t := range list {
		ids[i] = t.ID
	}
	info, err := s.Tasks.CardInfo(r.Context(), ids)
	if err != nil {
		return nil, err
	}
	roles, err := s.Tasks.RolesFor(r.Context(), userID, list)
	if err != nil {
		return nil, err
	}
	out := make(map[string]boardCard, len(list))
	for _, t := range list {
		rl := roles[t.ID]
		out[t.ID] = boardCard{Task: t, CardInfo: info[t.ID], CanMove: rl.Worker() || rl.Manager}
	}
	return out, nil
}

// cardHTML renders one kanban card with the board template, so a script can put it on the page.
func (s *Server) cardHTML(r *http.Request, userID string, t tasks.Task) (string, error) {
	cards, err := s.cards(r, userID, []tasks.Task{t})
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["board"].ExecuteTemplate(&buf, "kcard", cards[t.ID]); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// stageMove serves drag & drop on the project kanban.
// Answer: {stage, status, card (HTML), counters}; on error {error} with 4xx and the page puts the card back.
func (s *Server) stageMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err == nil {
		err = s.Tasks.MoveToStage(r.Context(), sess.UserID, id, r.FormValue("stage"))
	}
	var t tasks.Task
	if err == nil {
		t, err = s.Tasks.TaskByID(r.Context(), id)
	}
	var card string
	if err == nil {
		card, err = s.cardHTML(r, sess.UserID, t)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	stage := t.StageID
	c, _ := s.Tasks.Counters(r.Context(), sess.UserID)
	writeJSON(w, http.StatusOK, map[string]any{
		"stage": stage, "status": t.Status, "card": card,
		"counters": map[string]int{"open": c.Open, "overdue": c.Overdue, "to_control": c.ToControl, "comments": c.Comments},
	})
}

// quickTask answers the «+» form of a kanban column: the new card's HTML.
func (s *Server) quickTask(w http.ResponseWriter, r *http.Request, sess auth.Session, key string) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), key)
	var t tasks.Task
	if err == nil {
		t, err = s.Tasks.TaskByID(r.Context(), id)
	}
	var card string
	if err == nil {
		card, err = s.cardHTML(r, sess.UserID, t)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": key, "stage": t.StageID, "card": card})
}

// projectStages manages the kanban columns (project owner/manager).
func (s *Server) projectStages(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key") + "/board"
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		id := r.FormValue("id")
		switch r.FormValue("op") {
		case "add":
			err = s.Tasks.AddProjectStage(r.Context(), p, r.FormValue("title"), r.FormValue("color"))
		case "update":
			err = s.Tasks.UpdateProjectStage(r.Context(), p, id, r.FormValue("title"), r.FormValue("color"))
		case "delete":
			err = s.Tasks.DeleteProjectStage(r.Context(), p, id)
		case "left", "right":
			dir, _ := strconv.Atoi(map[string]string{"left": "-1", "right": "1"}[r.FormValue("op")])
			err = s.Tasks.ShiftProjectStage(r.Context(), p, id, dir)
		default:
			err = i18n.E("err.bad_form")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
