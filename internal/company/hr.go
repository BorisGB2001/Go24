package company

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Сотрудник кадров» (an HR officer) and vacations: the plan of the year, the reminder, the report by departments.

// HRRights are what an administrator lets an HR officer do: enter and change anyone's absences, invite employees,
// keep «Доска почёта», keep the courses of «Обучение».
var HRRights = []string{"absences", "invite", "honor", "learning"}

// HR is a person's HR role.
type HR struct {
	On     bool
	Rights []string
}

// Can: the person is an HR officer with the right.
func (h HR) Can(right string) bool { return h.On && slices.Contains(h.Rights, right) }

// HROf returns the person's HR role.
func (s *Store) HROf(ctx context.Context, userID string) (HR, error) {
	var h HR
	err := s.db.QueryRow(ctx, `SELECT hr, hr_rights FROM users WHERE id::text = $1 AND active AND NOT guest`, userID).Scan(&h.On, &h.Rights)
	if err == pgx.ErrNoRows {
		return HR{}, nil
	}
	return h, err
}

// SetHR makes the person an HR officer with the rights (known ones, each once) or takes the role away; an
// administrator decides (checked by the caller).
func (s *Store) SetHR(ctx context.Context, userID string, on bool, rights []string) error {
	clean := []string{}
	for _, r := range rights {
		if on && slices.Contains(HRRights, r) && !slices.Contains(clean, r) {
			clean = append(clean, r)
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE users SET hr = $2, hr_rights = $3 WHERE id::text = $1 AND NOT guest`, userID, on, clean)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// CanInvite: administrators, HR officers an administrator let invite employees, and every employee when «Сотрудники»
// lets them.
func (s *Store) CanInvite(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	h, err := s.HROf(ctx, userID)
	if err != nil || h.Can("invite") {
		return h.Can("invite"), err
	}
	set, err := s.StaffSettings(ctx)
	if err != nil || !set.InviteAll { // «Сотрудники»: every employee invites
		return false, err
	}
	var employee bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active AND NOT guest)`, userID).Scan(&employee)
	return employee, err
}

// notifyHR tells of an HR officer's action with an absence (added, changed, deleted) the officer, the employee and
// the employee's managers, each once.
func (s *Store) notifyHR(ctx context.Context, actor, action string, a Absence) error {
	to := []string{actor, a.UserID}
	managers, err := s.Managers(ctx, a.UserID)
	if err != nil {
		return err
	}
	for _, m := range managers {
		to = append(to, m.ID)
	}
	slices.Sort(to)
	body := strings.Join([]string{action, a.Kind, a.UserName, a.From.Format("02.01.2006"), a.To.Format("02.01.2006")}, "|")
	link := "/company/absences?from=" + a.From.Format(time.DateOnly)
	for _, id := range slices.Compact(to) {
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'absence_hr', $3, $4)`,
			id, actor, body, link); err != nil {
			return err
		}
	}
	return nil
}

// VacationRemind: an employee with no vacation in this year's plan who has not said «в этом году не напоминать».
func (s *Store) VacationRemind(ctx context.Context, userID string, now time.Time) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT u.vacation_remind_off <> $2 AND NOT EXISTS (SELECT 1 FROM absences a WHERE a.user_id = u.id
			AND a.kind = 'vacation' AND a.starts_on <= make_date($2, 12, 31) AND a.ends_on >= make_date($2, 1, 1))
		FROM users u WHERE u.id::text = $1 AND u.active AND NOT u.guest`, userID, now.Year()).Scan(&on)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return on, err
}

// VacationRemindOff: «в этом году не напоминать».
func (s *Store) VacationRemindOff(ctx context.Context, userID string, year int) error {
	_, err := s.db.Exec(ctx, `UPDATE users SET vacation_remind_off = $2 WHERE id::text = $1`, userID, year)
	return err
}

// VacationLine is an employee's vacations of the year.
type VacationLine struct {
	Person
	Vacations []Absence
	Days      int // calendar days within the year
}

// VacationDept is a department of the report: its own people (not the sub-departments').
type VacationDept struct {
	ID, Name string
	Depth    int
	Lines    []VacationLine
	Days     int
	NoPlan   int // people with no vacation in the plan
}

// VacationReport: the vacations of the year by departments, as in the structure (a person in two departments is
// in both; the head is in their department); people in no department — last, with an empty ID. only, when not nil,
// keeps those people (a manager sees their own).
func (s *Store) VacationReport(ctx context.Context, year int, only map[string]bool) ([]VacationDept, error) {
	_, depts, err := s.Tree(ctx)
	if err != nil {
		return nil, err
	}
	first, last := time.Date(year, 1, 1, 0, 0, 0, 0, time.Local), time.Date(year, 12, 31, 0, 0, 0, 0, time.Local)
	list, err := s.Absences(ctx, first, last)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, name, position, photo_key FROM users WHERE active AND NOT guest ORDER BY name`)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
	if err != nil {
		return nil, err
	}
	active := map[string]Person{}
	for _, p := range all {
		active[p.ID] = p
	}
	byUser := map[string][]Absence{}
	for _, a := range list {
		if a.Kind == "vacation" {
			byUser[a.UserID] = append(byUser[a.UserID], a)
		}
	}
	line := func(p Person) VacationLine {
		l := VacationLine{Person: p, Vacations: byUser[p.ID]}
		for _, a := range l.Vacations {
			from, to := a.From, a.To
			if from.Before(first) {
				from = first
			}
			if to.After(last) {
				to = last
			}
			l.Days += int(to.Sub(from).Hours()/24+0.5) + 1
		}
		return l
	}
	placed := map[string]bool{}
	var out []VacationDept
	add := func(d VacationDept, people []Person) {
		for _, p := range people {
			if only != nil && !only[p.ID] || slices.ContainsFunc(d.Lines, func(l VacationLine) bool { return l.ID == p.ID }) {
				continue
			}
			placed[p.ID] = true
			l := line(p)
			d.Lines, d.Days = append(d.Lines, l), d.Days+l.Days
			if len(l.Vacations) == 0 {
				d.NoPlan++
			}
		}
		if len(d.Lines) > 0 {
			out = append(out, d)
		}
	}
	for _, d := range depts {
		people := d.Employees
		if h, ok := active[d.HeadID]; ok && !slices.ContainsFunc(people, func(p Person) bool { return p.ID == h.ID }) {
			people = append([]Person{h}, people...)
		}
		add(VacationDept{ID: d.ID, Name: d.Name, Depth: d.Depth}, people)
	}
	all = slices.DeleteFunc(all, func(p Person) bool { return placed[p.ID] })
	add(VacationDept{}, all)
	return out, nil
}
