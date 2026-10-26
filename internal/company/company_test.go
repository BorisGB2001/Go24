package company

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestCompany runs against a throwaway PostgreSQL (GO24_TEST_DATABASE_URL, the schema is recreated).
func TestCompany(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	wantKey := func(err error, key string) {
		t.Helper()
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != key {
			t.Fatalf("err = %v, want %s", err, key)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	ceo, cto, dev, ops := newUser("ceo"), newUser("cto"), newUser("dev"), newUser("ops")
	s := NewStore(pool)

	must(s.EnsureRoot(ctx, "Компания"))
	must(s.EnsureRoot(ctx, "Другое имя")) // only once
	root, _, err := s.Tree(ctx)
	must(err)
	if root.Name != "Компания" {
		t.Fatalf("root = %+v", root)
	}
	must(s.UpdateDepartment(ctx, root.ID, "", "ООО Ромашка", ceo))
	must(s.AddDepartment(ctx, root.ID, "ИТ", cto))
	root, flat, err := s.Tree(ctx)
	must(err)
	it := root.Children[0]
	must(s.AddDepartment(ctx, it.ID, "Разработка", ""))
	must(s.AddDepartment(ctx, it.ID, "Эксплуатация", ops))
	root, flat, _ = s.Tree(ctx)
	if len(flat) != 4 || flat[0].Depth != 0 || flat[1].Name != "ИТ" || flat[2].Depth != 2 {
		t.Fatalf("flat = %+v", flat)
	}
	devDept, opsDept := flat[2].ID, flat[3].ID
	if flat[2].Name != "Разработка" {
		devDept, opsDept = opsDept, devDept
	}
	wantKey(s.AddDepartment(ctx, it.ID, "  ", ""), "err.department_name")
	wantKey(s.UpdateDepartment(ctx, it.ID, devDept, "ИТ", cto), "err.department_cycle") // under its own child

	must(s.SetDepartments(ctx, "", ceo, []string{root.ID}))
	must(s.SetDepartments(ctx, "", cto, []string{it.ID}))
	must(s.SetDepartments(ctx, "", dev, []string{devDept}))
	must(s.SetDepartments(ctx, "", ops, []string{opsDept}))

	// managers: dev's department has no head → the head above (cto); a head → the head of the parent
	names := func(ps []Person) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	m, err := s.Managers(ctx, dev)
	must(err)
	if len(m) != 1 || m[0].Name != "cto" {
		t.Fatalf("managers(dev) = %v", names(m))
	}
	m, _ = s.Managers(ctx, ops)
	if len(m) != 1 || m[0].Name != "cto" {
		t.Fatalf("managers(ops) = %v", names(m))
	}
	m, _ = s.Managers(ctx, cto)
	if len(m) != 1 || m[0].Name != "ceo" {
		t.Fatalf("managers(cto) = %v", names(m))
	}
	if m, _ = s.Managers(ctx, ceo); len(m) != 0 {
		t.Fatalf("managers(ceo) = %v", names(m))
	}
	sub, _ := s.Subordinates(ctx, cto)
	if len(sub) != 1 || sub[0].Name != "ops" { // heads of sub-departments; dev's department has no head
		t.Fatalf("subordinates(cto) = %v", names(sub))
	}

	// absences: own and the people's below; changed by the same people, also to another person
	oct := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.Local) }
	must(s.AddAbsence(ctx, dev, dev, "vacation", oct(5), oct(9), "море", false, ops, []string{"view", "view", "bad"}))
	list0, err := s.Absences(ctx, oct(1), oct(31))
	must(err)
	if len(list0) != 1 || list0[0].ActingID != ops || len(list0[0].Rights) != 1 {
		t.Fatalf("absences = %+v", list0)
	}
	id := list0[0].ID
	if !errors.Is(s.UpdateAbsence(ctx, ops, id, "", "sick", oct(5), oct(6), "", false, "", nil), ErrForbidden) {
		t.Fatal("ops changed dev's absence")
	}
	wantKey(s.UpdateAbsence(ctx, dev, id, "", "sick", oct(6), oct(5), "", false, "", nil), "err.absence_dates")
	if !errors.Is(s.UpdateAbsence(ctx, dev, id, cto, "sick", oct(5), oct(6), "", false, "", nil), ErrForbidden) {
		t.Fatal("dev moved the absence to the head")
	}
	must(s.UpdateAbsence(ctx, cto, id, "", "sick", oct(5), oct(6), " болел ", false, "", []string{"view"}))
	a, err := s.Absence(ctx, id)
	must(err)
	if a.UserID != dev || a.Kind != "sick" || !a.To.Equal(oct(6)) || a.Comment != "болел" || a.ActingID != "" || len(a.Rights) != 0 {
		t.Fatalf("changed absence = %+v", a)
	}
	must(s.UpdateAbsence(ctx, cto, id, ops, "trip", oct(5), oct(6), "", false, "", nil)) // to another of cto's people
	if a, _ = s.Absence(ctx, id); a.UserID != ops || a.Kind != "trip" {
		t.Fatalf("moved absence = %+v", a)
	}
	if _, err := s.Absence(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing absence: %v", err)
	}
	must(s.DeleteAbsence(ctx, ops, id, false))

	// «Сотрудник кадров»: with the right «absences» manages anyone's absences, each action is told to the officer, the
	// employee and the employee's managers; «invite» — invites employees
	hrNotes := func(user string) (n int) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'absence_hr'`, user).Scan(&n))
		return n
	}
	if !errors.Is(s.AddAbsence(ctx, ops, dev, "vacation", oct(12), oct(16), "", false, "", nil), ErrForbidden) {
		t.Fatal("ops added dev's absence")
	}
	must(s.SetHR(ctx, ops, true, []string{"invite", "bad", "invite"}))
	if h, err := s.HROf(ctx, ops); err != nil || !h.On || len(h.Rights) != 1 || h.Can("absences") || !h.Can("invite") {
		t.Fatalf("hr = %+v, %v", h, err)
	}
	if ok, _ := s.CanInvite(ctx, ops, false); !ok {
		t.Fatal("the HR officer may not invite")
	}
	if ok, _ := s.CanInvite(ctx, dev, false); ok {
		t.Fatal("dev may invite")
	}
	must(s.SetHR(ctx, ops, true, []string{"absences"}))
	must(s.AddAbsence(ctx, ops, dev, "vacation", oct(12), oct(16), "", false, "", nil))
	if hrNotes(ops) != 1 || hrNotes(dev) != 1 || hrNotes(cto) != 1 || hrNotes(ceo) != 0 {
		t.Fatalf("notes ops %d dev %d cto %d ceo %d", hrNotes(ops), hrNotes(dev), hrNotes(cto), hrNotes(ceo))
	}
	vac, err := s.Absences(ctx, oct(12), oct(12))
	must(err)
	must(s.UpdateAbsence(ctx, ops, vac[0].ID, "", "vacation", oct(12), oct(18), "", false, "", nil))
	must(s.AddAbsence(ctx, ops, ops, "vacation", oct(1), oct(2), "", false, "", nil)) // own: no message
	if hrNotes(dev) != 2 || hrNotes(ops) != 2 {
		t.Fatalf("notes after the change: dev %d ops %d", hrNotes(dev), hrNotes(ops))
	}
	// the vacation plan: dev and ops have one; ceo is reminded until «в этом году не напоминать»
	for who, want := range map[string]bool{dev: false, ops: false, ceo: true} {
		if on, err := s.VacationRemind(ctx, who, oct(25)); err != nil || on != want {
			t.Fatalf("remind %s = %v, %v", who, on, err)
		}
	}
	must(s.VacationRemindOff(ctx, ceo, 2026))
	if on, _ := s.VacationRemind(ctx, ceo, oct(25)); on {
		t.Fatal("reminded after «не напоминать»")
	}
	if on, _ := s.VacationRemind(ctx, ceo, oct(25).AddDate(1, 0, 0)); !on {
		t.Fatal("not reminded the next year")
	}
	// the report by departments: the head in their department, the days of each, who has no plan
	rep, err := s.VacationReport(ctx, 2026, nil)
	must(err)
	got := map[string]string{}
	for _, d := range rep {
		for _, l := range d.Lines {
			got[d.Name+"/"+l.Name] = fmt.Sprint(len(l.Vacations), " ", l.Days)
		}
	}
	if len(rep) != 4 || got["ООО Ромашка/ceo"] != "0 0" || got["ИТ/cto"] != "0 0" || got["Разработка/dev"] != "1 7" || got["Эксплуатация/ops"] != "1 2" || rep[0].NoPlan != 1 {
		t.Fatalf("report = %v", got)
	}
	if rep, _ = s.VacationReport(ctx, 2026, map[string]bool{dev: true}); len(rep) != 1 || rep[0].Lines[0].ID != dev || rep[0].Days != 7 {
		t.Fatalf("report of dev = %+v", rep)
	}
	must(s.DeleteAbsence(ctx, ops, vac[0].ID, false))
	if hrNotes(dev) != 3 || hrNotes(cto) != 3 {
		t.Fatalf("notes after deleting: dev %d cto %d", hrNotes(dev), hrNotes(cto))
	}
	must(s.SetHR(ctx, ops, false, []string{"absences"}))
	if h, _ := s.HROf(ctx, ops); h.On || len(h.Rights) != 0 {
		t.Fatalf("hr taken = %+v", h)
	}

	// list: by department with sub-departments, by text; profile save; dismiss
	list, err := s.Employees(ctx, Filter{DepartmentID: it.ID})
	must(err)
	if len(list) != 3 {
		t.Fatalf("IT employees = %d", len(list))
	}
	// quick filters: at work now (a day started, not finished), absent today, new (taken on within 30 days)
	day := oct(20)
	must2 := func(q string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, q, args...)
		must(err)
	}
	must2(`UPDATE users SET created_at = now() - interval '1 year', employment_date = NULL`)
	must2(`UPDATE users SET employment_date = $2 WHERE id = $1`, ops, day.AddDate(0, 0, -10))
	must2(`INSERT INTO workdays (user_id, day, started_at) VALUES ($1, $3, now()), ($2, $3, now())`, dev, cto, day)
	must2(`UPDATE workdays SET ended_at = now() WHERE user_id = $1`, cto)
	must(s.AddAbsence(ctx, ceo, ceo, "trip", day, day.AddDate(0, 0, 2), "", false, "", nil))
	for preset, want := range map[string]string{"here": "dev", "absent": "ceo", "new": "ops", "bad": "ceo cto dev ops"} {
		got, err := s.Employees(ctx, Filter{Preset: preset, Today: day})
		must(err)
		if n := strings.Join(namesOf(got), " "); n != want {
			t.Errorf("preset %s = %q, want %q", preset, n, want)
		}
	}
	wantKey(s.SaveProfile(ctx, dev, ProfileUpdate{}), "err.profile_name")
	must(s.SaveProfile(ctx, dev, ProfileUpdate{LastName: "Иванов", FirstName: "Пётр", SecondName: "Сергеевич", Position: "Go-разработчик", BirthdayYear: true}))
	p, err := s.Profile(ctx, dev)
	must(err)
	if !p.BirthdayYear {
		t.Fatal("the year of birth is not shown")
	}
	if p.Name != "Пётр Иванов" || p.FullName() != "Иванов Пётр Сергеевич" || len(p.Departments) != 1 {
		t.Fatalf("profile = %+v", p)
	}
	if list, _ = s.Employees(ctx, Filter{Query: "go-разраб"}); len(list) != 1 {
		t.Fatalf("search by position = %d", len(list))
	}
	wantKey(s.SetActive(ctx, ceo, ceo, false), "err.fire_self")
	must(s.SetActive(ctx, ceo, dev, false))
	if list, _ = s.Employees(ctx, Filter{DepartmentID: it.ID}); len(list) != 2 {
		t.Fatalf("after dismissal = %d", len(list))
	}
	if list, _ = s.Employees(ctx, Filter{Fired: true}); len(list) != 1 {
		t.Fatalf("dismissed list = %d", len(list))
	}

	// «Кадровые изменения»: the first departments are no move; a move, a dismissal, a return are logged once
	must(s.SetActive(ctx, ceo, dev, false)) // already dismissed: nothing new
	must(s.SetDepartments(ctx, ceo, ops, []string{devDept, opsDept}))
	must(s.SetDepartments(ctx, ceo, ops, []string{opsDept, devDept})) // the same departments
	staff := func(f StaffFilter) []StaffChange {
		t.Helper()
		ch, err := s.StaffChanges(ctx, f)
		must(err)
		return ch
	}
	if ch := staff(StaffFilter{Kind: "moved"}); len(ch) != 1 || ch[0].ID != ops || ch[0].ActorID != ceo || ch[0].ActorName != "ceo" ||
		!slices.Equal(ch[0].Was, []string{"Эксплуатация"}) || !slices.Equal(ch[0].Departments, []string{"Разработка", "Эксплуатация"}) {
		t.Fatalf("moved = %+v", ch)
	}
	if ch := staff(StaffFilter{Kind: "fired"}); len(ch) != 1 || ch[0].ID != dev || !slices.Equal(ch[0].Departments, []string{"Разработка"}) {
		t.Fatalf("fired = %+v", ch)
	}
	ids := func(ch []StaffChange) []string {
		var out []string
		for _, c := range ch {
			out = append(out, c.ID)
		}
		slices.Sort(out)
		return out
	}
	want := []string{cto, dev, ops}
	slices.Sort(want)
	if got := ids(staff(StaffFilter{Kind: "hired", DepartmentID: it.ID})); !slices.Equal(got, want) { // taken on before the log
		t.Fatalf("hired in IT = %v, want %v", got, want)
	}
	if ch := staff(StaffFilter{DepartmentID: opsDept}); len(ch) != 2 { // ops: moved (was there) and hired
		t.Fatalf("in ops = %+v", ch)
	}
	today := time.Now()
	if ch := staff(StaffFilter{Kind: "moved", To: today.AddDate(0, 0, -1)}); len(ch) != 0 {
		t.Fatalf("moved before today = %+v", ch)
	}
	if ch := staff(StaffFilter{Kind: "moved", From: today, To: today}); len(ch) != 1 {
		t.Fatalf("moved today = %+v", ch)
	}
	must(s.SetActive(ctx, ceo, dev, true))
	must(s.SetActive(ctx, ceo, dev, false))
	if ch := staff(StaffFilter{DepartmentID: devDept}); len(ch) < 4 || ch[0].Kind != "fired" || ch[1].Kind != "returned" {
		t.Fatalf("dev's changes = %+v", ch)
	}
	must(s.SetDepartments(ctx, ceo, ops, []string{opsDept}))

	// dragged in the structure: a person from one department to another (the others stay), a department under another
	deptsOf := func(id string) []string {
		t.Helper()
		p, err := s.Profile(ctx, id)
		must(err)
		var out []string
		for _, d := range p.Departments {
			out = append(out, d.ID)
		}
		slices.Sort(out)
		return out
	}
	must(s.SetDepartments(ctx, ceo, ops, []string{opsDept, root.ID}))
	must(s.MoveEmployee(ctx, ceo, ops, opsDept, devDept))
	if got, want := deptsOf(ops), []string{devDept, root.ID}; !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Fatalf("after the move = %v", got)
	}
	wantKey(s.MoveEmployee(ctx, ceo, ops, opsDept, devDept), "err.not_found") // not in that department
	must(s.MoveEmployee(ctx, ceo, ops, root.ID, devDept))                     // already there: just out of the old one
	if got := deptsOf(ops); !slices.Equal(got, []string{devDept}) {
		t.Fatalf("into own department = %v", got)
	}
	if ch := staff(StaffFilter{Kind: "moved", DepartmentID: devDept}); len(ch) < 2 || ch[0].ID != ops || ch[0].ActorID != ceo {
		t.Fatalf("drag logged = %+v", ch)
	}
	must(s.MoveEmployee(ctx, ceo, ops, devDept, opsDept))
	must(s.SetDepartments(ctx, ceo, ops, []string{opsDept}))
	must(s.MoveDepartment(ctx, opsDept, root.ID))
	_, flat, _ = s.Tree(ctx)
	for _, d := range flat {
		if d.ID == opsDept && (d.ParentID != root.ID || d.Depth != 1 || d.HeadID != ops) {
			t.Fatalf("moved department = %+v", d)
		}
	}
	wantKey(s.MoveDepartment(ctx, it.ID, devDept), "err.department_cycle")
	wantKey(s.MoveDepartment(ctx, it.ID, it.ID), "err.department_cycle")
	wantKey(s.MoveDepartment(ctx, root.ID, it.ID), "err.bad_form") // the company stays at the top
	wantKey(s.MoveDepartment(ctx, opsDept, ceo), "err.not_found")
	must(s.MoveDepartment(ctx, opsDept, it.ID))

	wantKey(s.DeleteDepartment(ctx, it.ID), "err.department_not_empty")
	wantKey(s.DeleteDepartment(ctx, root.ID), "err.department_not_empty")
	must(s.SetDepartments(ctx, "", dev, nil))
	must(s.DeleteDepartment(ctx, devDept))

	// phones are validated; photos are set and removed
	wantKey(s.SaveProfile(ctx, cto, ProfileUpdate{FirstName: "А", MobilePhone: "восемь девятьсот"}), "err.phone")
	wantKey(s.SaveProfile(ctx, cto, ProfileUpdate{FirstName: "А", InnerPhone: "12a"}), "err.inner_phone")
	must(s.SaveProfile(ctx, cto, ProfileUpdate{FirstName: "А", MobilePhone: "+7 (914) 000-00-00", WorkPhone: "8 4132 11-22-33", InnerPhone: "1777"}))
	old, err := s.SetPhoto(ctx, cto, "go24/photos/x.png", "image/png")
	must(err)
	if old != "" {
		t.Fatalf("old photo = %q", old)
	}
	p, _ = s.Profile(ctx, cto)
	if p.PhotoURL() == "" || p.PhotoType != "image/png" {
		t.Fatalf("photo = %+v", p)
	}
	if old, _ = s.SetPhoto(ctx, cto, "", ""); old != "go24/photos/x.png" {
		t.Fatalf("old photo = %q", old)
	}

	// messengers and website
	wantKey(s.SaveProfile(ctx, cto, ProfileUpdate{FirstName: "А", Telegram: "@ab"}), "err.telegram")
	wantKey(s.SaveProfile(ctx, cto, ProfileUpdate{FirstName: "А", Website: "javascript:alert(1) <x>"}), "err.website")
	must(s.SaveProfile(ctx, cto, ProfileUpdate{FirstName: "А", Telegram: "https://t.me/ivan_petrov", WhatsApp: "+7 914 000-00-00", Website: "example.org"}))
	p, _ = s.Profile(ctx, cto)
	if p.Telegram != "@ivan_petrov" || p.TelegramLink() != "https://t.me/ivan_petrov" || p.Website != "https://example.org" || p.WhatsAppLink() != "https://wa.me/79140000000" {
		t.Fatalf("contacts = %q %q %q", p.Telegram, p.Website, p.WhatsAppLink())
	}
}

func namesOf(list []Profile) []string {
	var out []string
	for _, p := range list {
		out = append(out, p.Name)
	}
	return out
}
