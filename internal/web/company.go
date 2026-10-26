package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/authentik"
	"go24/internal/company"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Сотрудники»: list, company structure, employee card ----------

func (s *Server) tree(r *http.Request) (*company.Department, []*company.Department, error) {
	if err := s.Company.EnsureRoot(r.Context(), s.tr(r, "company.root_default")); err != nil {
		return nil, nil, err
	}
	return s.Company.Tree(r.Context())
}

// employeesViewCookie remembers the list's view in this browser: list or tiles.
const employeesViewCookie = "go24_emp_view"

// employees: GET /company — the list of «Сотрудники» (?q, ?dept, ?fired, ?preset=here|absent|new) as a table or
// tiles (?view=tiles|list, remembered); ?format=csv — the same people for Excel.
func (s *Server) employees(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, depts, err := s.tree(r)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	q := r.URL.Query()
	f := company.Filter{Query: q.Get("q"), DepartmentID: q.Get("dept"), TeamID: q.Get("team"), Fired: sess.Admin && q.Get("fired") == "1",
		Guests: sess.Admin && q.Get("guests") == "1"}
	if f.Guests { // guests have no departments, nor working days and absences
		f.DepartmentID = ""
	} else if slices.Contains(company.EmployeePresets, q.Get("preset")) {
		f.Preset = q.Get("preset")
	}
	list, err := s.Company.Employees(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	switch q.Get("format") {
	case "csv":
		s.employeesCSV(w, r, list, f.Guests)
		return
	case "vcf":
		s.employeesVCF(w, r, list)
		return
	}
	view := q.Get("view")
	if view == "tiles" || view == "list" {
		http.SetCookie(w, &http.Cookie{Name: employeesViewCookie, Value: view, Path: "/company", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode})
	} else if c, err := r.Cookie(employeesViewCookie); err == nil && c.Value == "tiles" {
		view = "tiles"
	}
	// who is absent today: a mark beside the name
	now := time.Now()
	absent := map[string]company.Absence{}
	if today, err := s.Company.Absences(r.Context(), now, now); err == nil {
		for _, a := range today {
			if _, ok := absent[a.UserID]; !ok {
				absent[a.UserID] = a
			}
		}
	}
	var team *company.Team // the team the list is filtered by
	if f.TeamID != "" {
		if team, err = s.Company.Team(r.Context(), f.TeamID); err != nil && !errors.Is(err, company.ErrNotFound) {
			s.taskErr(w, r, err, "/my")
			return
		}
	}
	keep := url.Values{}
	for _, k := range []string{"q", "dept", "team", "fired", "preset", "guests"} {
		if v := q.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	s.render(w, r, "company", page{TitleKey: "title.employees", Nav: "company", Data: map[string]any{
		"Employees": list, "Departments": depts, "Filter": f, "Admin": sess.Admin, "Presets": company.EmployeePresets,
		"Tiles": view == "tiles", "Absent": absent, "Keep": listQuery{keep}, "Team": team, "CardDAV": s.carddavURL(r),
	}})
}

// listQuery is the query of a list page; With makes its links.
type listQuery struct{ url.Values }

// With: the query with one parameter changed ("" — removed).
func (v listQuery) With(k, val string) string {
	q := url.Values{}
	for key, vals := range v.Values {
		q[key] = vals
	}
	if val == "" {
		q.Del(k)
	} else {
		q.Set(k, val)
	}
	return q.Encode()
}

// employeesCSV: the list for Excel — the columns of the table and the rest of the card.
func (s *Server) employeesCSV(w http.ResponseWriter, r *http.Request, list []company.Profile, guests bool) {
	tr := func(k string) string { return s.tr(r, k) }
	date := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("02.01.2006")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=employees-"+time.Now().Format(time.DateOnly)+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	head := []string{"company.col.name", "company.col.position", "company.col.department", "company.col.email", "profile.work_phone",
		"profile.mobile_phone", "profile.inner_phone", "profile.city", "profile.birthday", "profile.employment_date"}
	if guests { // a guest's collabs instead of departments
		head[2] = "company.col.collabs"
	}
	for i, k := range head {
		head[i] = tr(k)
	}
	_ = cw.Write(head)
	for _, p := range list {
		var depts []string
		for _, d := range p.Departments {
			depts = append(depts, d.Name)
		}
		for _, c := range p.Collabs {
			depts = append(depts, c.Name)
		}
		birthday := ""
		if p.Birthday != nil { // as in the card: the year only when the employee shows it
			birthday = p.Birthday.Format("02.01")
			if p.BirthdayYear {
				birthday = p.Birthday.Format("02.01.2006")
			}
		}
		_ = cw.Write([]string{p.FullName(), p.Position, strings.Join(depts, ", "), p.Email, p.WorkPhone, p.MobilePhone, p.InnerPhone,
			p.City, birthday, date(p.Employment)})
	}
	cw.Flush()
}

func (s *Server) structure(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	root, depts, err := s.tree(r)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "structure", page{TitleKey: "title.structure", Nav: "structure", Data: map[string]any{
		"Root": root, "Departments": depts, "Users": users, "Admin": sess.Admin, "Me": sess.UserID,
	}})
}

// teams manages the teams of the structure: op=add|delete (administrators), op=update (administrators, and the
// team's head — without its department and head).
func (s *Server) teams(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/company/structure"
	_ = r.ParseForm()
	in := company.TeamInput{DepartmentID: r.FormValue("department"), Name: r.FormValue("name"), Description: r.FormValue("description"),
		HeadID: r.FormValue("head"), Deputies: r.Form["deputies"], Members: r.Form["members"]}
	id := r.FormValue("id")
	var err error
	switch op := r.FormValue("op"); {
	case op == "update":
		var ok bool
		if ok, err = s.Company.CanEditTeam(r.Context(), sess.UserID, sess.Admin, id); err == nil && !ok {
			err = company.ErrForbidden
		} else if err == nil {
			err = s.Company.UpdateTeam(r.Context(), id, in, !sess.Admin)
		}
	case !sess.Admin:
		err = company.ErrForbidden
	case op == "add":
		id, err = s.Company.CreateTeam(r.Context(), in)
	case op == "delete":
		err = s.Company.DeleteTeam(r.Context(), id)
	default:
		err = i18n.E("err.bad_form")
	}
	if errors.Is(err, company.ErrForbidden) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("team "+r.FormValue("op"), "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// departments manages the structure (administrators): op=add|update|delete, and what is dragged in it —
// op=move (a department id under parent), op=move_user (a person user from one department to another, to).
// Asked for JSON (the drags), it answers 204 or {"error"}.
func (s *Server) departments(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/company/structure"
	if !sess.Admin {
		if wantsJSON(r) {
			s.jsonErr(w, r, company.ErrForbidden)
			return
		}
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	var err error
	switch r.FormValue("op") {
	case "add":
		err = s.Company.AddDepartment(r.Context(), r.FormValue("parent"), r.FormValue("name"), r.FormValue("head"))
	case "update":
		_ = r.ParseForm()
		err = s.Company.UpdateDepartment(r.Context(), r.FormValue("id"), r.FormValue("parent"), r.FormValue("name"), r.FormValue("head"), r.Form["deputies"]...)
	case "delete":
		err = s.Company.DeleteDepartment(r.Context(), r.FormValue("id"))
	case "move":
		err = s.Company.MoveDepartment(r.Context(), r.FormValue("id"), r.FormValue("parent"))
	case "move_user":
		err = s.Company.MoveEmployee(r.Context(), sess.UserID, r.FormValue("user"), r.FormValue("from"), r.FormValue("to"))
	default:
		err = i18n.E("err.bad_form")
	}
	if wantsJSON(r) {
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		s.Log.Info("department "+r.FormValue("op"), "user_id", sess.UserID, "id", r.FormValue("id"), "target", r.FormValue("user"))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("department "+r.FormValue("op"), "user_id", sess.UserID, "id", r.FormValue("id"))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) profile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if id == "me" {
		http.Redirect(w, r, "/u/"+sess.UserID, http.StatusFound)
		return
	}
	if !s.knowsPerson(r, sess.UserID, id) { // a guest's card is for the people of their collabs, and theirs for them
		s.taskErr(w, r, tasks.ErrNotFound, "/company")
		return
	}
	p, err := s.Company.Profile(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	if tasks.IsGuestCtx(r.Context()) { // a guest sees the person, not the company: no structure, thanks, own fields
		s.guestProfile(w, r, sess, p)
		return
	}
	managers, err := s.Company.Managers(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	subs, err := s.Company.Subordinates(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	_, depts, err := s.tree(r)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	// Open tasks of this person that the viewer may see (the viewer's own visibility rules apply).
	open, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{UserID: id, Doing: true, OnlyOpen: true, Viewer: sess.UserID})
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	inDept := map[string]bool{}
	for _, d := range p.Departments {
		inDept[d.ID] = true
	}
	mxid, element := s.matrixOf(p)
	var thanks []feed.Thanks
	if s.Feed != nil {
		if thanks, err = s.Feed.Gratitudes(r.Context(), sess.UserID, id); err != nil {
			s.Log.Error("profile gratitudes", "err", err)
		}
	}
	fields, err := s.Tasks.FieldValuesOf(r.Context(), tasks.UserFieldSet, id)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	hr, err := s.Company.HROf(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	tab := r.URL.Query().Get("tab") // the profile's tabs: "" (general), tasks, thanks, security (the person and administrators)
	security := sess.UserID == id || sess.Admin
	if tab != "tasks" && tab != "thanks" && (tab != "security" || !security) {
		tab = ""
	}
	var devices []deviceRow
	if tab == "security" {
		if devices, err = s.profileDevices(r, sess, id); err != nil {
			s.taskErr(w, r, err, "/company")
			return
		}
	}
	s.render(w, r, "profile", page{Title: p.FullName(), Nav: "company", Data: map[string]any{
		"Thanks": thanks, "Fields": fields, "Tab": tab, "Security": security, "Devices": devices, "AccountURL": s.accountURL(),
		"MatrixID": mxid, "ElementURL": element,
		"P": p, "Managers": managers, "Subordinates": subs, "Departments": depts, "InDept": inDept,
		"Tasks": open, "CanEdit": sess.UserID == id || sess.Admin, "Admin": sess.Admin, "Me": sess.UserID == id,
		"Phone": s.telephonyOf(r.Context(), sess, id), "NumberLocked": s.Telephony != nil && !sess.Admin,
		"HR": hr, "HRRights": company.HRRights,
	}})
}

func (s *Server) saveProfile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/u/" + id
	if sess.UserID != id && !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	fail := func(err error) {
		if wantsJSON(r) { // the form stays open with everything typed; the message shows in it
			s.jsonErr(w, r, err)
			return
		}
		s.taskErr(w, r, err, back)
	}
	birthday, err := parseDate(r.FormValue("birthday"))
	if err != nil {
		fail(err)
		return
	}
	employment, err := parseDate(r.FormValue("employment_date"))
	if err != nil {
		fail(err)
		return
	}
	inner := r.FormValue("inner_phone")
	if s.Telephony != nil { // the inner number is a SIP account: administrators give it, no two employees have the same
		if !sess.Admin {
			p, err := s.Company.Profile(r.Context(), id)
			if err != nil {
				fail(err)
				return
			}
			inner = p.InnerPhone
		} else if err := s.Telephony.SetNumber(r.Context(), id, inner); err != nil {
			fail(err)
			return
		}
		defer s.Telephony.Kick()
	}
	err = s.Company.SaveProfile(r.Context(), id, company.ProfileUpdate{
		LastName: r.FormValue("last_name"), FirstName: r.FormValue("first_name"), SecondName: r.FormValue("second_name"),
		Position: r.FormValue("position"), WorkPhone: r.FormValue("work_phone"), MobilePhone: r.FormValue("mobile_phone"),
		About: r.FormValue("about"), Birthday: birthday, Employment: employment,
		Gender: r.FormValue("gender"), City: r.FormValue("city"), InnerPhone: inner,
		Telegram: r.FormValue("telegram"), WhatsApp: r.FormValue("whatsapp"), Website: r.FormValue("website"),
		BirthdayYear: r.FormValue("birthday_year") == "1",
	})
	if err != nil {
		fail(err)
		return
	}
	s.syncAuthentik(id)
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]string{"redirect": back})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// syncAuthentik writes the card into Authentik where Authentik has no data (in the background: the card is
// already saved, and Authentik being slow or down must not stop people from editing their card).
func (s *Server) syncAuthentik(userID string) {
	if s.Authentik == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		p, err := s.Company.Profile(ctx, userID)
		if err != nil {
			s.Log.Error("authentik sync: profile", "user_id", userID, "err", err)
			return
		}
		var depts []string
		for _, d := range p.Departments {
			depts = append(depts, d.Name)
		}
		birthday := ""
		if p.Birthday != nil {
			birthday = p.Birthday.Format("2006-01-02")
		}
		changed, err := s.Authentik.FillMissing(ctx, authentik.Profile{Email: p.Email, Name: p.FullName(), Attributes: map[string]string{
			"last_name": p.LastName, "first_name": p.FirstName, "middle_name": p.SecondName, "position": p.Position,
			"department": strings.Join(depts, ", "), "phone": p.MobilePhone, "work_phone": p.WorkPhone,
			"inner_phone": p.InnerPhone, "city": p.City, "birthday": birthday,
		}})
		if err != nil {
			s.Log.Error("authentik sync", "user_id", userID, "err", err)
			return
		}
		if changed {
			s.Log.Info("authentik sync: filled missing data", "user_id", userID)
		}
	}()
}

const maxPhotoSize = 5 << 20

// photoTypes are the image formats accepted for employee photos (checked by content, not by the file name).
var photoTypes = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}

// userPhoto uploads (or with op=delete removes) the employee's own photo: the employee or an administrator.
func (s *Server) userPhoto(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/u/" + id
	if sess.UserID != id && !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if r.FormValue("op") == "delete" {
		if _, err := s.Company.SetPhoto(r.Context(), id, "", ""); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoSize+1<<16)
	f, hdr, err := r.FormFile("photo")
	if err != nil || hdr.Size > maxPhotoSize {
		s.taskErr(w, r, i18n.E("err.photo"), back)
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ct := http.DetectContentType(head[:n])
	ext, ok := photoTypes[ct]
	if !ok {
		s.taskErr(w, r, i18n.E("err.photo"), back)
		return
	}
	key := "go24/photos/" + id + "/" + randomHex(8) + ext
	body := io.MultiReader(bytes.NewReader(head[:n]), f)
	if err := s.Storages.Put(r.Context(), sess.UserID, false, "", key, body, hdr.Size, ct); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if _, err := s.Company.SetPhoto(r.Context(), id, key, ct); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("photo uploaded", "user_id", sess.UserID, "target", id)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// photoFile serves an employee photo to signed-in users.
func (s *Server) photoFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Company.Profile(r.Context(), r.PathValue("id"))
	if err != nil || p.PhotoKey == "" || !s.knowsPerson(r, sess.UserID, p.ID) {
		http.NotFound(w, r)
		return
	}
	obj, err := s.Storages.Open(r.Context(), nil, p.PhotoKey)
	if err != nil {
		s.fail(w, "open photo", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", p.PhotoType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400") // the URL changes with every new photo
	_, _ = io.Copy(w, obj)
}

func (s *Server) userDepartments(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/u/"+id)
		return
	}
	if err := s.Company.SetDepartments(r.Context(), sess.UserID, id, r.Form["dept"]); err != nil {
		s.taskErr(w, r, err, "/u/"+id)
		return
	}
	http.Redirect(w, r, "/u/"+id, http.StatusSeeOther)
}

// userActive dismisses or returns an employee (administrators; logged).
func (s *Server) userActive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	active := r.FormValue("active") == "1"
	if err := s.Company.SetActive(r.Context(), sess.UserID, id, active); err != nil {
		s.taskErr(w, r, err, "/u/"+id)
		return
	}
	s.Log.Info("employee active", "user_id", sess.UserID, "target", id, "active", active)
	kind := "user_fired"
	if active {
		kind = "user_returned"
	}
	var name string
	_ = s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id::text = $1`, id).Scan(&name)
	s.audit(r, audit.Event{Kind: kind, UserID: sess.UserID, Target: name, TargetID: id})
	s.Matrix.Wake()    // a dismissed employee's Matrix account is closed right away
	s.Telephony.Kick() // and their SIP accounts
	http.Redirect(w, r, "/u/"+id, http.StatusSeeOther)
}

// userEmployee makes a guest of collabs an employee (an administrator): out of the guests' groups of Authentik
// first — otherwise the next sign-in would make them a guest again — then in go24.
func (s *Server) userEmployee(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.makeEmployee(r.Context(), sess.UserID, id); err != nil {
		s.taskErr(w, r, err, "/u/"+id)
		return
	}
	s.Log.Info("guest made employee", "user_id", sess.UserID, "target", id)
	http.Redirect(w, r, "/u/"+id, http.StatusSeeOther)
}

func (s *Server) makeEmployee(ctx context.Context, actorID, id string) error {
	p, err := s.Company.Profile(ctx, id)
	if err != nil {
		return err
	}
	if !p.Guest {
		return i18n.E("err.not_guest")
	}
	for _, g := range s.GuestGroups {
		if err := s.Authentik.LeaveGroup(ctx, p.Username, g); err != nil {
			s.Log.Error("authentik leave group", "target", id, "group", g, "err", err)
			return i18n.E("err.guest_authentik")
		}
	}
	if err := s.Company.MakeEmployee(ctx, actorID, id); err != nil {
		return err
	}
	s.Matrix.Wake() // the company's space and the name without «(гость)» in the collabs' rooms
	return nil
}

// matrixOf is the person's Matrix ID (from their Authentik login) and the Element address, when the messenger is on.
func (s *Server) matrixOf(p company.Profile) (string, string) {
	if s.Matrix == nil || p.Username == "" {
		return "", ""
	}
	return "@" + strings.ToLower(p.Username) + ":" + s.Matrix.M.ServerName, s.Matrix.ElementURL
}

// profileCard is the pop-up card of a person (the name in the top bar, people in lists).
func (s *Server) profileCard(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if id == "me" {
		id = sess.UserID
	}
	if !s.knowsPerson(r, sess.UserID, id) {
		s.jsonErr(w, r, tasks.ErrNotFound)
		return
	}
	p, err := s.Company.Profile(r.Context(), id)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	mxid, element := s.matrixOf(p)
	if tasks.IsGuestCtx(r.Context()) {
		p.Departments = nil // the structure of the company is not a guest's business
	}
	absent, _ := s.Company.AbsentNow(r.Context(), id)
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["profile"].ExecuteTemplate(&buf, "pcard", map[string]any{
		"P": p, "MatrixID": mxid, "ElementURL": element, "Me": id == sess.UserID, "Absent": absent, "Calls": s.Calls != nil, "Presence": s.myPresence(r.Context(), sess.UserID),
	}); err != nil {
		s.fail(w, "profile card", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}
