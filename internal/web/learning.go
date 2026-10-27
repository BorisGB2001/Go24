package web

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Обучение» of the company learning: /company/learning — «Мои курсы», «Все курсы» and «Обучение
// сотрудников» (the assignments a keeper or a head looks at); a course's page with its lessons and the progress,
// the lessons one by one with «Урок пройден»; keepers (administrators, HR officers with the right) make the courses.

const learnBase = "/company/learning"

// learnRights: the viewer keeps the courses (keep) and may assign them (assign).
func (s *Server) learnRights(r *http.Request, sess auth.Session) (keep, assign bool, err error) {
	if keep, err = s.Company.CanTeach(r.Context(), sess.UserID, sess.Admin); err != nil || keep {
		return keep, keep, err
	}
	assign, err = s.Company.CanAssign(r.Context(), sess.UserID, sess.Admin)
	return false, assign, err
}

// learningPage: ?tab=my (default) | all | people; q — words of a course; course, dept, status — of «Обучение сотрудников».
func (s *Server) learningPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	keep, assign, err := s.learnRights(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	q := r.URL.Query()
	tab := q.Get("tab")
	if !slices.Contains([]string{"my", "all", "people"}, tab) || tab == "people" && !assign {
		tab = "my"
	}
	data := map[string]any{"Tab": tab, "Keep": keep, "Assign": assign, "Query": q.Get("q"), "Today": today()}
	switch tab {
	case "people":
		f := company.LearnerFilter{CourseID: q.Get("course"), DepartmentID: q.Get("dept"), Status: q.Get("status")}
		if data["Learners"], err = s.Company.Learners(r.Context(), sess.UserID, keep, f); err != nil {
			break
		}
		data["Filter"] = f
		if q.Get("format") == "csv" {
			list, _ := data["Learners"].([]company.Learner)
			s.learnersCSV(w, r, list)
			return
		}
		if data["Courses"], err = s.Company.Courses(r.Context(), sess.UserID, true, company.CourseFilter{}); err != nil {
			break
		}
		_, data["Departments"], err = s.tree(r)
	default:
		data["Courses"], err = s.Company.Courses(r.Context(), sess.UserID, keep && tab == "all",
			company.CourseFilter{Mine: tab == "my", Query: q.Get("q")})
	}
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	s.render(w, r, "learning", page{TitleKey: "learn.title", Nav: "learning", Data: data})
}

func today() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
}

// learnCourse loads the course of the path the viewer sees (keepers — any) with its lessons, or shows the error.
func (s *Server) learnCourse(w http.ResponseWriter, r *http.Request, sess auth.Session) (c company.Course, lessons []company.Lesson,
	keep, assign, ok bool) {
	keep, assign, err := s.learnRights(r, sess)
	if err == nil {
		c, err = s.Company.Course(r.Context(), r.PathValue("id"), sess.UserID, keep)
	}
	if err == nil {
		lessons, err = s.Company.Lessons(r.Context(), c.ID, sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, learnBase)
		return c, nil, false, false, false
	}
	return c, lessons, keep, assign, true
}

// learnCoursePage: the course — description, lessons by chapters with the viewer's marks, «Начать / Продолжить»; for
// those who assign — «Назначить» and the people the course is assigned to.
func (s *Server) learnCoursePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, lessons, keep, assign, ok := s.learnCourse(w, r, sess)
	if !ok {
		return
	}
	data := map[string]any{"C": c, "Lessons": lessons, "Keep": keep, "Assign": assign, "Today": today(), "Next": nextLesson(lessons)}
	if assign {
		var err error
		if data["Learners"], err = s.Company.Learners(r.Context(), sess.UserID, keep, company.LearnerFilter{CourseID: c.ID}); err == nil {
			data["Users"], err = s.Tasks.ListUsers(r.Context())
		}
		if err != nil {
			s.taskErr(w, r, err, learnBase)
			return
		}
	}
	s.render(w, r, "learncourse", page{Title: c.Title, Nav: "learning", Data: data})
}

// nextLesson: the first lesson not passed (none — the first one; nil — no lessons).
func nextLesson(lessons []company.Lesson) *company.Lesson {
	for i := range lessons {
		if !lessons[i].Done {
			return &lessons[i]
		}
	}
	if len(lessons) > 0 {
		return &lessons[0]
	}
	return nil
}

// learnLessonPage: a lesson of the course with the list of lessons, «Урок пройден», the previous and next ones.
func (s *Server) learnLessonPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, lessons, keep, _, ok := s.learnCourse(w, r, sess)
	if !ok {
		return
	}
	i := slices.IndexFunc(lessons, func(l company.Lesson) bool { return l.ID == r.PathValue("lesson") })
	if i < 0 {
		s.taskErr(w, r, company.ErrNotFound, learnBase+"/"+c.ID)
		return
	}
	data := map[string]any{"C": c, "Lessons": lessons, "L": lessons[i], "N": i + 1, "Keep": keep}
	if l := lessons[i]; l.Test() {
		qs, err := s.Company.Questions(r.Context(), l.ID)
		var tries []company.Attempt
		if err == nil {
			tries, err = s.Company.Attempts(r.Context(), l.ID, sess.UserID)
		}
		if err != nil {
			s.taskErr(w, r, err, learnBase+"/"+c.ID)
			return
		}
		left := -1 // any number
		if l.Attempts > 0 {
			left = max(0, l.Attempts-len(tries))
		}
		for _, a := range tries {
			if a.Finished == nil { // begun: it goes on
				left = max(left, 1)
			}
		}
		data["Questions"], data["Tries"], data["Left"] = len(qs), tries, left
	}
	if i > 0 {
		data["Prev"] = lessons[i-1]
	}
	if i+1 < len(lessons) {
		data["NextL"] = lessons[i+1]
	}
	s.render(w, r, "learnlesson", page{Title: lessons[i].Title + " · " + c.Title, Nav: "learning", Data: data})
}

// learnLessonDone: POST …/{id}/l/{lesson}/done (undo=1 — not passed): the mark, then the next lesson (the last one —
// the course).
func (s *Server) learnLessonDone(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, lessons, _, _, ok := s.learnCourse(w, r, sess)
	if !ok {
		return
	}
	id := r.PathValue("lesson")
	back := learnBase + "/" + c.ID
	undo := r.FormValue("undo") == "1"
	if err := s.Company.MarkLesson(r.Context(), sess.UserID, c.ID, id, !undo); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	i := slices.IndexFunc(lessons, func(l company.Lesson) bool { return l.ID == id })
	switch {
	case undo || i < 0 || lessons[i].Test():
		back += "/l/" + id
	case i+1 < len(lessons):
		back += "/l/" + lessons[i+1].ID
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// learnKeeper: the viewer keeps the courses (or the error page is shown).
func (s *Server) learnKeeper(w http.ResponseWriter, r *http.Request, sess auth.Session, back string) bool {
	can, err := s.Company.CanTeach(r.Context(), sess.UserID, sess.Admin)
	if err == nil && !can {
		err = company.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, back)
	}
	return err == nil
}

// learnCourseForm: /company/learning/new and /company/learning/{id}/edit — the course, «Кто видит» and its lessons.
func (s *Server) learnCourseForm(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.learnKeeper(w, r, sess, learnBase) {
		return
	}
	c := company.Course{Color: company.CourseColors[0]}
	data := map[string]any{"Colors": company.CourseColors, "TitleMax": company.CourseTitleMax, "DescMax": company.CourseDescMax}
	var err error
	if id := r.PathValue("id"); id != "" {
		if c, err = s.Company.Course(r.Context(), id, sess.UserID, true); err == nil {
			data["Lessons"], err = s.Company.Lessons(r.Context(), id, "")
		}
		if err == nil {
			var refs []company.DeptRef
			if refs, err = s.Company.CourseAccess(r.Context(), id); err == nil {
				var chips []tasks.User
				for _, ref := range refs {
					chips = append(chips, tasks.User{ID: ref.ID, Name: ref.Name})
				}
				data["Access"] = chips
			}
		}
	}
	if err != nil {
		s.taskErr(w, r, err, learnBase)
		return
	}
	data["C"] = c
	title := s.tr(r, "learn.new")
	if c.ID != "" {
		title = c.Title
	}
	s.render(w, r, "learnform", page{Title: title, Nav: "learning", Data: data})
}

// learnCourseSave: POST /company/learning (new) or /company/learning/{id} — title, description, color, published,
// access (people and «dept:ID»).
func (s *Server) learnCourseSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.learnKeeper(w, r, sess, learnBase) {
		return
	}
	_ = r.ParseForm()
	c := company.Course{ID: r.PathValue("id"), Title: r.FormValue("title"), Description: r.FormValue("description"),
		Color: r.FormValue("color"), Published: r.FormValue("published") == "1"}
	back := learnBase + "/new"
	if c.ID != "" {
		back = learnBase + "/" + c.ID + "/edit"
	}
	id, err := s.Company.SaveCourse(r.Context(), sess.UserID, c)
	if err == nil {
		err = s.Company.SetCourseAccess(r.Context(), id, r.Form["access"])
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("course saved", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, learnBase+"/"+id+"/edit", http.StatusSeeOther)
}

// learnCourseDelete: POST /company/learning/{id}/delete.
func (s *Server) learnCourseDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.learnKeeper(w, r, sess, learnBase) {
		return
	}
	if err := s.Company.DeleteCourse(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, learnBase)
		return
	}
	s.Log.Info("course deleted", "user_id", sess.UserID, "id", r.PathValue("id"))
	http.Redirect(w, r, learnBase+"?tab=all", http.StatusSeeOther)
}

// learnLessonForm: /company/learning/{id}/l/new and …/l/{lesson}/edit.
func (s *Server) learnLessonForm(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.learnKeeper(w, r, sess, learnBase) {
		return
	}
	c, err := s.Company.Course(r.Context(), r.PathValue("id"), sess.UserID, true)
	var lessons []company.Lesson
	if err == nil {
		lessons, err = s.Company.Lessons(r.Context(), c.ID, "")
	}
	if err != nil {
		s.taskErr(w, r, err, learnBase)
		return
	}
	l := company.Lesson{CourseID: c.ID}
	var chapters []string
	for _, x := range lessons {
		if x.Chapter != "" && !slices.Contains(chapters, x.Chapter) {
			chapters = append(chapters, x.Chapter)
		}
	}
	if len(lessons) > 0 { // a new lesson goes on in the last chapter
		l.Chapter = lessons[len(lessons)-1].Chapter
	}
	l.Kind, l.PassPct = "lesson", 70
	if r.URL.Query().Get("kind") == "test" {
		l.Kind = "test"
	}
	data := map[string]any{"C": c, "Chapters": chapters, "TitleMax": company.CourseTitleMax, "QKinds": company.QuestionKinds}
	if id := r.PathValue("lesson"); id != "" {
		i := slices.IndexFunc(lessons, func(x company.Lesson) bool { return x.ID == id })
		if i < 0 {
			s.taskErr(w, r, company.ErrNotFound, learnBase+"/"+c.ID+"/edit")
			return
		}
		l = lessons[i]
		if l.Test() {
			qs, err := s.Company.Questions(r.Context(), l.ID)
			if err != nil {
				s.taskErr(w, r, err, learnBase+"/"+c.ID+"/edit")
				return
			}
			q := company.Question{Kind: "one", Points: 1} // the form of a new question, or ?q= — of that one
			if qid := r.URL.Query().Get("q"); qid != "" {
				if k := slices.IndexFunc(qs, func(x company.Question) bool { return x.ID == qid }); k >= 0 {
					q = qs[k]
				}
			}
			data["Questions"], data["Q"] = qs, q
		}
	}
	data["L"] = l
	s.render(w, r, "learnlessonform", page{Title: c.Title, Nav: "learning", Data: data})
}

// learnLessonSave: POST /company/learning/{id}/l (new) or …/l/{lesson} — chapter, title, body; then the course's form.
func (s *Server) learnLessonSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cid := r.PathValue("id")
	if !s.learnKeeper(w, r, sess, learnBase+"/"+cid+"/edit") {
		return
	}
	l := company.Lesson{ID: r.PathValue("lesson"), CourseID: cid, Chapter: r.FormValue("chapter"), Title: r.FormValue("title"),
		Body: r.FormValue("body"), Kind: r.FormValue("kind"), Shuffle: r.FormValue("shuffle") == "1"}
	l.PassPct, _ = strconv.Atoi(r.FormValue("pass_pct"))
	l.Attempts, _ = strconv.Atoi(r.FormValue("attempts"))
	l.Minutes, _ = strconv.Atoi(r.FormValue("minutes"))
	back := learnBase + "/" + cid + "/l/new"
	if l.ID != "" {
		back = learnBase + "/" + cid + "/l/" + l.ID + "/edit"
	}
	id, err := s.Company.SaveLesson(r.Context(), l)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if r.FormValue("more") == "1" { // «Сохранить и добавить ещё»
		http.Redirect(w, r, learnBase+"/"+cid+"/l/new", http.StatusSeeOther)
		return
	}
	if l.Kind == "test" && l.ID == "" { // a new test: on to its questions
		http.Redirect(w, r, learnBase+"/"+cid+"/l/"+id+"/edit#questions", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, learnBase+"/"+cid+"/edit#l-"+id, http.StatusSeeOther)
}

// learnLessonOp: POST /company/learning/{id}/l/{lesson}/delete | move (up=1 — up).
func (s *Server) learnLessonOp(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cid, id := r.PathValue("id"), r.PathValue("lesson")
	back := learnBase + "/" + cid + "/edit"
	if !s.learnKeeper(w, r, sess, back) {
		return
	}
	var err error
	if op := r.PathValue("op"); op != "delete" && op != "move" {
		http.NotFound(w, r)
		return
	}
	if r.PathValue("op") == "delete" {
		err = s.Company.DeleteLesson(r.Context(), cid, id)
	} else {
		err = s.Company.MoveLesson(r.Context(), cid, id, r.FormValue("up") == "1")
		back += "#l-" + id
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// learnAssign: POST /company/learning/{id}/assign — to (people and «dept:ID»), due (a day or empty).
func (s *Server) learnAssign(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cid := r.PathValue("id")
	back := learnBase + "/" + cid
	keep, assign, err := s.learnRights(r, sess)
	if err == nil && !assign {
		err = company.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	_ = r.ParseForm()
	var due *time.Time
	if v := r.FormValue("due"); v != "" {
		t, err := time.ParseInLocation(time.DateOnly, v, time.Local)
		if err != nil {
			s.taskErr(w, r, i18n.E("err.bad_form"), back)
			return
		}
		due = &t
	}
	if len(r.Form["to"]) == 0 {
		s.taskErr(w, r, i18n.E("learn.err_nobody"), back)
		return
	}
	n, err := s.Company.Assign(r.Context(), sess.UserID, cid, r.Form["to"], due, keep)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("course assigned", "user_id", sess.UserID, "id", cid, "people", n)
	http.Redirect(w, r, back+"#learners", http.StatusSeeOther)
}

// learnUnassign: POST /company/learning/{id}/unassign — user.
func (s *Server) learnUnassign(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cid := r.PathValue("id")
	back := learnBase + "/" + cid + "#learners"
	keep, assign, err := s.learnRights(r, sess)
	if err == nil && !assign {
		err = company.ErrForbidden
	}
	if err == nil {
		err = s.Company.Unassign(r.Context(), sess.UserID, cid, r.FormValue("user"), keep)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// learnChapters groups lessons by chapter for a template: a new group where the chapter changes.
func learnChapters(lessons []company.Lesson) []learnChapter {
	var out []learnChapter
	for i, l := range lessons {
		if len(out) == 0 || out[len(out)-1].Name != l.Chapter {
			out = append(out, learnChapter{Name: l.Chapter})
		}
		out[len(out)-1].Lessons = append(out[len(out)-1].Lessons, learnItem{Lesson: l, N: i + 1})
	}
	return out
}

type learnChapter struct {
	Name    string
	Lessons []learnItem
}

type learnItem struct {
	company.Lesson
	N int // the number in the course
}

// learnQuestionSave: POST …/l/{lesson}/q (new) or …/l/{lesson}/q/{q} — kind, text, options (lines, «*» — right),
// answers (lines), points.
func (s *Server) learnQuestionSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cid, lid := r.PathValue("id"), r.PathValue("lesson")
	back := learnBase + "/" + cid + "/l/" + lid + "/edit"
	if !s.learnKeeper(w, r, sess, back) {
		return
	}
	if _, err := s.Company.Course(r.Context(), cid, sess.UserID, true); err != nil {
		s.taskErr(w, r, err, learnBase)
		return
	}
	q := company.Question{ID: r.PathValue("q"), LessonID: lid, Kind: r.FormValue("kind"), Text: r.FormValue("text"),
		Options: company.OptionsOf(r.FormValue("options")), Answers: strings.Split(r.FormValue("answers"), "\n")}
	q.Points, _ = strconv.Atoi(r.FormValue("points"))
	if q.ID != "" {
		back += "?q=" + q.ID
	}
	id, err := s.Company.SaveQuestion(r.Context(), q)
	if err != nil {
		s.taskErr(w, r, err, back+"#qform")
		return
	}
	http.Redirect(w, r, learnBase+"/"+cid+"/l/"+lid+"/edit#q-"+id, http.StatusSeeOther)
}

// learnQuestionOp: POST …/l/{lesson}/q/{q}/delete | move (up=1 — up).
func (s *Server) learnQuestionOp(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cid, lid, qid := r.PathValue("id"), r.PathValue("lesson"), r.PathValue("q")
	back := learnBase + "/" + cid + "/l/" + lid + "/edit#questions"
	if !s.learnKeeper(w, r, sess, back) {
		return
	}
	if _, err := s.Company.Course(r.Context(), cid, sess.UserID, true); err != nil {
		s.taskErr(w, r, err, learnBase)
		return
	}
	var err error
	switch r.PathValue("op") {
	case "delete":
		err = s.Company.DeleteQuestion(r.Context(), lid, qid)
	case "move":
		err = s.Company.MoveQuestion(r.Context(), lid, qid, r.FormValue("up") == "1")
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// learnTest loads the test of the path in the course the viewer sees.
func (s *Server) learnTest(w http.ResponseWriter, r *http.Request, sess auth.Session) (company.Course, company.Lesson, bool) {
	c, lessons, _, _, ok := s.learnCourse(w, r, sess)
	if !ok {
		return c, company.Lesson{}, false
	}
	i := slices.IndexFunc(lessons, func(l company.Lesson) bool { return l.ID == r.PathValue("lesson") && l.Test() })
	if i < 0 {
		s.taskErr(w, r, company.ErrNotFound, learnBase+"/"+c.ID)
		return c, company.Lesson{}, false
	}
	return c, lessons[i], true
}

// learnTestStart: POST …/l/{lesson}/start — begins an attempt (or goes on with the one begun).
func (s *Server) learnTestStart(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, l, ok := s.learnTest(w, r, sess)
	if !ok {
		return
	}
	back := learnBase + "/" + c.ID + "/l/" + l.ID
	a, err := s.Company.StartAttempt(r.Context(), l, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"/a/"+strconv.FormatInt(a.ID, 10), http.StatusSeeOther)
}

// learnAttemptPage: …/l/{lesson}/a/{n} — the questions of an attempt going on, or its result.
func (s *Server) learnAttemptPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, l, ok := s.learnTest(w, r, sess)
	if !ok {
		return
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	back := learnBase + "/" + c.ID + "/l/" + l.ID
	a, err := s.Company.Attempt(r.Context(), l.ID, sess.UserID, n)
	if err == nil && a.Finished == nil && !a.Deadline(l.Minutes).IsZero() && time.Now().After(a.Deadline(l.Minutes).Add(time.Minute)) {
		a, err = s.Company.FinishAttempt(r.Context(), l, sess.UserID, n, nil) // the time is over: graded as it is
	}
	var qs []company.Question
	if err == nil {
		qs, err = s.Company.Questions(r.Context(), l.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	var shown []company.Question // in the attempt's order
	for _, id := range a.Questions {
		if k := slices.IndexFunc(qs, func(q company.Question) bool { return q.ID == id }); k >= 0 {
			shown = append(shown, qs[k])
		}
	}
	data := map[string]any{"C": c, "L": l, "A": a, "Questions": shown}
	if d := a.Deadline(l.Minutes); !d.IsZero() && a.Finished == nil {
		data["Seconds"] = max(0, int(time.Until(d).Seconds()))
	}
	s.render(w, r, "learnattempt", page{Title: l.Title + " · " + c.Title, Nav: "learning", Data: data})
}

// learnAttemptSend: POST …/l/{lesson}/a/{n} — the answers: q_<id> — option numbers or the typed text.
func (s *Server) learnAttemptSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, l, ok := s.learnTest(w, r, sess)
	if !ok {
		return
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	back := learnBase + "/" + c.ID + "/l/" + l.ID + "/a/" + r.PathValue("n")
	_ = r.ParseForm()
	answers := map[string]any{}
	for k, vals := range r.PostForm {
		qid, ok := strings.CutPrefix(k, "q_")
		if !ok || len(vals) == 0 {
			continue
		}
		if r.PostForm.Get("t_"+qid) == "1" { // a typed answer
			answers[qid] = vals[0]
			continue
		}
		var nums []int
		for _, v := range vals {
			if i, err := strconv.Atoi(v); err == nil {
				nums = append(nums, i)
			}
		}
		answers[qid] = nums
	}
	a, err := s.Company.FinishAttempt(r.Context(), l, sess.UserID, n, answers)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("test attempt", "user_id", sess.UserID, "lesson", l.ID, "score", a.Score, "max", a.Max, "passed", a.Passed)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// learnCertificate: /company/learning/{id}/certificate[?user=] — the certificate of the course passed, to print: one's
// own, or an employee's for those who see the learners.
func (s *Server) learnCertificate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, _, keep, assign, ok := s.learnCourse(w, r, sess)
	if !ok {
		return
	}
	who := sess.UserID
	if u := r.URL.Query().Get("user"); u != "" && u != sess.UserID {
		if !assign {
			s.taskErr(w, r, company.ErrForbidden, learnBase+"/"+c.ID)
			return
		}
		list, err := s.Company.Learners(r.Context(), sess.UserID, keep, company.LearnerFilter{CourseID: c.ID})
		if err != nil || !slices.ContainsFunc(list, func(l company.Learner) bool { return l.ID == u }) {
			s.taskErr(w, r, company.ErrNotFound, learnBase+"/"+c.ID)
			return
		}
		who = u
	}
	done, err := s.Company.FinishedCourses(r.Context(), who)
	var p company.Profile
	if err == nil {
		p, err = s.Company.Profile(r.Context(), who)
	}
	if err != nil {
		s.taskErr(w, r, err, learnBase+"/"+c.ID)
		return
	}
	i := slices.IndexFunc(done, func(d company.CourseDone) bool { return d.ID == c.ID })
	if i < 0 {
		s.taskErr(w, r, i18n.E("learn.err_not_finished"), learnBase+"/"+c.ID)
		return
	}
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["learncourse"].ExecuteTemplate(&buf, "lcert", map[string]any{"C": c, "P": p, "At": done[i].At}); err != nil {
		s.fail(w, "certificate", err)
		return
	}
	s.invoiceHTML(w, buf.Bytes())
}

// learnersCSV: «Обучение сотрудников» as a CSV file for Excel — employee, position, course, status,
// lessons passed, the best test result, due day, assigned, who assigned, the last lesson.
func (s *Server) learnersCSV(w http.ResponseWriter, r *http.Request, list []company.Learner) {
	date := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("02.01.2006")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=learning-"+time.Now().Format(time.DateOnly)+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	head := []string{"learn.f.person", "company.col.position", "learn.f.course", "learn.f.status", "learn.f.progress", "learn.f.best",
		"learn.f.due", "learn.f.assigned", "learn.f.by", "learn.f.last"}
	for i, k := range head {
		head[i] = s.tr(r, k)
	}
	_ = cw.Write(head)
	day := today()
	for _, l := range list {
		status := "active"
		if l.Finished() {
			status = "finished"
		} else if l.Overdue(day) {
			status = "overdue"
		}
		best := ""
		if l.Best != nil {
			best = fmt.Sprintf("%d%%", *l.Best)
		}
		_ = cw.Write([]string{l.Name, l.Position, l.CourseTitle, s.tr(r, "learn.status."+status), fmt.Sprintf("%d/%d", l.Done, l.Total), best,
			date(l.Due), date(&l.Assigned), l.ByName, date(l.Last)})
	}
	cw.Flush()
}
