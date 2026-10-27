// Package company is the «Сотрудники» section: employee profiles and the company structure
// (departments with sub-departments and heads). Who is whose manager follows the portal: the head of the
// employee's department, and for a head — the head of the parent department.
package company

import (
	"context"
	"errors"
	"hash/fnv"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
	"go24/internal/portal"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

type Store struct {
	db   *pgxpool.Pool
	seen pulseSeen // the marks of «Пульс компании» written today
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Person is a short reference to an employee.
type Person struct {
	ID       string
	Name     string
	Position string
	PhotoKey string
}

// PhotoURL is the uploaded photo, or "" for the go24 picture.
func (p Person) PhotoURL() string { return PhotoURL(p.ID, p.PhotoKey) }

// PhotoURL is the address of an uploaded photo ("" when there is none).
func PhotoURL(id, key string) string {
	if key == "" {
		return ""
	}
	h := fnv.New32a()
	h.Write([]byte(key))
	return "/u/" + id + "/photo?v=" + strconv.FormatUint(uint64(h.Sum32()), 36) // a new photo gets a new URL
}

type DeptRef struct {
	ID   string
	Name string
}

// Profile is the employee card.
type Profile struct {
	ID           string
	Name         string
	Email        string
	LastName     string
	FirstName    string
	SecondName   string
	Position     string
	WorkPhone    string
	MobilePhone  string
	Birthday     *time.Time
	BirthdayYear bool // the year of birth is shown to others
	About        string
	Active       bool
	LastLogin    time.Time
	Gender       string // M, F or ""
	City         string
	InnerPhone   string
	Employment   *time.Time
	TimeZone     string
	PhotoKey     string
	PhotoType    string
	Telegram     string
	WhatsApp     string
	Website      string
	Username     string // Authentik login: also the Matrix ID (@username:server)
	Guest        bool   // from outside the company: a collab's guest
	Departments  []DeptRef
	Teams        []DeptRef // the teams the person is in
	Collabs      []DeptRef // a guest's collabs (ID — the key); filled for the list of guests
}

// EditFirst / EditLast fill the card form: the saved parts, or — before the card was ever filled in —
// the display name split as «Имя Фамилия», so nobody has to type their name again.
func (p Profile) EditFirst() string {
	if p.FirstName != "" || p.LastName != "" {
		return p.FirstName
	}
	f, _, _ := strings.Cut(strings.TrimSpace(p.Name), " ")
	return f
}

func (p Profile) EditLast() string {
	if p.FirstName != "" || p.LastName != "" {
		return p.LastName
	}
	_, l, _ := strings.Cut(strings.TrimSpace(p.Name), " ")
	return strings.TrimSpace(l)
}

// TelegramLink opens the chat with the person in Telegram.
func (p Profile) TelegramLink() string {
	if p.Telegram == "" {
		return ""
	}
	return "https://t.me/" + strings.TrimPrefix(p.Telegram, "@")
}

// WhatsAppLink opens WhatsApp with the person's number (digits only).
func (p Profile) WhatsAppLink() string {
	d := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, p.WhatsApp)
	if d == "" {
		return ""
	}
	return "https://wa.me/" + d
}

func (p Profile) PhotoURL() string { return PhotoURL(p.ID, p.PhotoKey) }

// FullName is «Фамилия Имя Отчество» when the parts are filled, else the display name.
func (p Profile) FullName() string {
	if n := strings.Join(strings.Fields(p.LastName+" "+p.FirstName+" "+p.SecondName), " "); n != "" {
		return n
	}
	return p.Name
}

// Department is a node of the company structure.
type Department struct {
	ID        string
	ParentID  string
	Name      string
	HeadID    string
	HeadName  string
	HeadPhoto string   // photo key of the head
	Deputies  []Person // заместители руководителя
	Employees []Person
	Teams     []*Team // the teams under the department
	Children  []*Department
	Depth     int
}

func (d *Department) HeadPhotoURL() string { return PhotoURL(d.HeadID, d.HeadPhoto) }

// Total counts the employees of the department and all its sub-departments (each person once).
func (d *Department) Total() int {
	seen := map[string]bool{}
	var walk func(*Department)
	walk = func(n *Department) {
		for _, p := range n.Employees {
			seen[p.ID] = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(d)
	return len(seen)
}

// EnsureRoot creates the company node (the root of the structure) on first use.
func (s *Store) EnsureRoot(ctx context.Context, name string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO departments (name, sort) SELECT $1, 0
		WHERE NOT EXISTS (SELECT 1 FROM departments WHERE parent_id IS NULL)`, name)
	return err
}

// Tree returns the company structure with active employees in each department.
func (s *Store) Tree(ctx context.Context) (*Department, []*Department, error) {
	rows, err := s.db.Query(ctx, `SELECT d.id, coalesce(d.parent_id::text, ''), d.name, coalesce(d.head_id::text, ''), coalesce(u.name, ''), coalesce(u.photo_key, '')
		FROM departments d LEFT JOIN users u ON u.id = d.head_id ORDER BY d.sort, d.name`)
	if err != nil {
		return nil, nil, err
	}
	var flat []*Department
	byID := map[string]*Department{}
	for rows.Next() {
		d := &Department{}
		if err := rows.Scan(&d.ID, &d.ParentID, &d.Name, &d.HeadID, &d.HeadName, &d.HeadPhoto); err != nil {
			rows.Close()
			return nil, nil, err
		}
		flat = append(flat, d)
		byID[d.ID] = d
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT dd.department_id::text, u.id, u.name, u.position, u.photo_key FROM department_deputies dd
		JOIN users u ON u.id = dd.user_id ORDER BY u.name`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var did string
		var p Person
		if err := rows.Scan(&did, &p.ID, &p.Name, &p.Position, &p.PhotoKey); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if d := byID[did]; d != nil {
			d.Deputies = append(d.Deputies, p)
		}
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT ud.department_id::text, u.id, u.name, u.position, u.photo_key FROM user_departments ud
		JOIN users u ON u.id = ud.user_id WHERE u.active ORDER BY u.name`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var did string
		var p Person
		if err := rows.Scan(&did, &p.ID, &p.Name, &p.Position, &p.PhotoKey); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if d := byID[did]; d != nil {
			d.Employees = append(d.Employees, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	teams, err := s.Teams(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	for _, t := range teams {
		if d := byID[t.DepartmentID]; d != nil {
			d.Teams = append(d.Teams, t)
		}
	}
	var root *Department
	for _, d := range flat {
		if d.ParentID == "" {
			root = d
		} else if p := byID[d.ParentID]; p != nil {
			p.Children = append(p.Children, d)
		}
	}
	if root == nil {
		return nil, nil, ErrNotFound
	}
	// flat list in tree order with depth, for pickers
	var ordered []*Department
	var walk func(*Department, int)
	walk = func(d *Department, depth int) {
		d.Depth = depth
		ordered = append(ordered, d)
		for _, c := range d.Children {
			walk(c, depth+1)
		}
	}
	walk(root, 0)
	return root, ordered, nil
}

func cleanName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len([]rune(n)) > 200 {
		return "", i18n.E("err.department_name")
	}
	return n, nil
}

func nullable(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// AddDepartment creates a sub-department (administrators only — checked by the caller).
func (s *Store) AddDepartment(ctx context.Context, parentID, name, headID string) error {
	_, err := s.CreateDepartment(ctx, parentID, name, headID)
	return err
}

// CreateDepartment is AddDepartment returning the new department's id.
func (s *Store) CreateDepartment(ctx context.Context, parentID, name, headID string) (string, error) {
	name, err := cleanName(name)
	if err != nil {
		return "", err
	}
	if parentID == "" {
		return "", i18n.E("err.bad_form")
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO departments (parent_id, name, head_id) VALUES ($1, $2, $3) RETURNING id::text`,
		parentID, name, nullable(headID)).Scan(&id)
	return id, err
}

// UpdateDepartment renames a department, changes its head and moves it under another parent
// (never under itself or its own sub-department). The company node stays the root.
func (s *Store) UpdateDepartment(ctx context.Context, id, parentID, name, headID string, deputies ...string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var curParent *string
		err := tx.QueryRow(ctx, `SELECT parent_id::text FROM departments WHERE id = $1 FOR UPDATE`, id).Scan(&curParent)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if curParent == nil { // the company itself
			parentID = ""
		} else {
			if err := checkParent(ctx, tx, id, parentID); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE departments SET name = $2, parent_id = $3, head_id = $4 WHERE id = $1`,
			id, name, nullable(parentID), nullable(headID)); err != nil {
			return err
		}
		var keep []string
		for _, d := range deputies {
			if d != "" && d != headID {
				keep = append(keep, d)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM department_deputies WHERE department_id = $1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO department_deputies (department_id, user_id)
			SELECT $1, u.id FROM users u WHERE u.id = ANY($2::uuid[]) ON CONFLICT DO NOTHING`, id, keep)
		return err
	})
}

// checkParent: a department goes under another one, not under itself nor under its own sub-departments.
func checkParent(ctx context.Context, tx pgx.Tx, id, parentID string) error {
	if parentID == "" {
		return i18n.E("err.bad_form")
	}
	var cycle bool
	if err := tx.QueryRow(ctx, `WITH RECURSIVE sub AS (
			SELECT id FROM departments WHERE id = $1
			UNION ALL SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id)
		SELECT EXISTS (SELECT 1 FROM sub WHERE id = $2)`, id, parentID).Scan(&cycle); err != nil {
		return err
	}
	if cycle {
		return i18n.E("err.department_cycle")
	}
	return nil
}

// MoveDepartment puts a department with its sub-departments and people under another one (dragged in the
// structure); its name, head and deputies stay. The company itself stays at the top.
func (s *Store) MoveDepartment(ctx context.Context, id, parentID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var curParent *string
		err := tx.QueryRow(ctx, `SELECT parent_id::text FROM departments WHERE id::text = $1 FOR UPDATE`, id).Scan(&curParent)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if curParent == nil {
			return i18n.E("err.bad_form")
		}
		if err := checkParent(ctx, tx, id, parentID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE departments SET parent_id = p.id FROM departments p WHERE departments.id = $1 AND p.id::text = $2`, id, parentID)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}

// Superiors are everyone who stands above the person: heads and their deputies of the person's departments and of
// every department above them.
func (s *Store) Superiors(ctx context.Context, userID string) ([]Person, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT d.id, d.parent_id FROM user_departments ud JOIN departments d ON d.id = ud.department_id WHERE ud.user_id = $1
			UNION
			SELECT p.id, p.parent_id FROM departments p JOIN up ON p.id = up.parent_id)
		SELECT DISTINCT ON (u.id) u.id, u.name, u.position, u.photo_key FROM (
			SELECT d.head_id AS uid FROM departments d JOIN up ON up.id = d.id
			UNION SELECT dd.user_id FROM department_deputies dd JOIN up ON up.id = dd.department_id) x
		JOIN users u ON u.id = x.uid WHERE u.id <> $1 AND u.active ORDER BY u.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
}

// DeleteDepartment removes an empty department: no sub-departments and no employees. Its teams go to the department
// above.
func (s *Store) DeleteDepartment(ctx context.Context, id string) error {
	var root bool
	var children, people int
	err := s.db.QueryRow(ctx, `SELECT parent_id IS NULL,
			(SELECT count(*) FROM departments c WHERE c.parent_id = d.id),
			(SELECT count(*) FROM user_departments ud WHERE ud.department_id = d.id)
		FROM departments d WHERE d.id = $1`, id).Scan(&root, &children, &people)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if root || children > 0 || people > 0 {
		return i18n.E("err.department_not_empty")
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE teams SET department_id = (SELECT parent_id FROM departments WHERE id = $1)
			WHERE department_id = $1`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM departments WHERE id = $1`, id)
		return err
	})
}

// ---------- employees ----------

const profileSelect = `SELECT u.id, u.name, u.email, u.last_name, u.first_name, u.second_name, u.position,
	u.work_phone, u.mobile_phone, u.birthday, u.about, u.active, u.last_login_at,
	u.gender, u.city, u.inner_phone, u.employment_date, u.time_zone, u.photo_key, u.photo_type,
	u.telegram, u.whatsapp, u.website, u.username, u.guest, u.birthday_show_year FROM users u`

func scanProfile(row pgx.Row) (Profile, error) {
	var p Profile
	err := row.Scan(&p.ID, &p.Name, &p.Email, &p.LastName, &p.FirstName, &p.SecondName, &p.Position,
		&p.WorkPhone, &p.MobilePhone, &p.Birthday, &p.About, &p.Active, &p.LastLogin,
		&p.Gender, &p.City, &p.InnerPhone, &p.Employment, &p.TimeZone, &p.PhotoKey, &p.PhotoType,
		&p.Telegram, &p.WhatsApp, &p.Website, &p.Username, &p.Guest, &p.BirthdayYear)
	return p, err
}

// Filter of the employee list.
type Filter struct {
	Query        string // name, position, e-mail or phone
	DepartmentID string // with its sub-departments
	TeamID       string // the people of a team
	Fired        bool   // show dismissed employees instead of active ones
	Guests       bool   // the guests of collabs instead of employees (the «Гости коллабов»), for administrators
	// Preset is a quick filter of the list: here — the working day is going on (started, not finished),
	// absent — absent today, new — taken on (or added to go24) within the last 30 days
	Preset string
	Today  time.Time // the day of the quick filters (zero — now)
}

// EmployeePresets are the quick filters of the list.
var EmployeePresets = []string{"here", "absent", "new"}

// Employees lists people, like the portal «Сотрудники» grid. Service accounts (oidc_sub 'legacy:0'
// placeholders) and guests of collabs (they are not employees) are left out — Filter.Guests lists only those.
func (s *Store) Employees(ctx context.Context, f Filter) ([]Profile, error) {
	where := []string{"u.active = $1", "u.oidc_sub <> 'legacy:0'", "NOT u.guest"}
	if f.Guests {
		where[2] = "u.guest"
	}
	args := []any{!f.Fired}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q)+"%")
		n := "$" + strconv.Itoa(len(args))
		where = append(where, "(u.name ILIKE "+n+" OR u.last_name ILIKE "+n+" OR u.position ILIKE "+n+" OR u.email ILIKE "+n+
			" OR u.work_phone ILIKE "+n+" OR u.mobile_phone ILIKE "+n+" OR u.inner_phone ILIKE "+n+")")
	}
	if f.DepartmentID != "" {
		args = append(args, f.DepartmentID)
		where = append(where, `EXISTS (WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id = $`+strconv.Itoa(len(args))+`
			UNION ALL SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id)
			SELECT 1 FROM user_departments ud WHERE ud.user_id = u.id AND ud.department_id IN (SELECT id FROM sub))`)
	}
	if f.TeamID != "" {
		args = append(args, f.TeamID)
		where = append(where, "EXISTS (SELECT 1 FROM team_members m WHERE m.user_id = u.id AND m.team_id::text = $"+strconv.Itoa(len(args))+")")
	}
	if slices.Contains(EmployeePresets, f.Preset) {
		today := f.Today
		if today.IsZero() {
			today = time.Now()
		}
		args = append(args, today.Format(time.DateOnly))
		n := "$" + strconv.Itoa(len(args)) + "::date"
		switch f.Preset {
		case "here":
			where = append(where, "EXISTS (SELECT 1 FROM workdays w WHERE w.user_id = u.id AND w.day = "+n+" AND w.ended_at IS NULL)")
		case "absent":
			where = append(where, "EXISTS (SELECT 1 FROM absences a WHERE a.user_id = u.id AND "+n+" BETWEEN a.starts_on AND a.ends_on)")
		case "new":
			where = append(where, "coalesce(u.employment_date, u.created_at::date) > "+n+" - 30")
		}
	}
	rows, err := s.db.Query(ctx, profileSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY u.name LIMIT 2000", args...)
	if err != nil {
		return nil, err
	}
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if f.Guests {
		if err := s.fillCollabs(ctx, out); err != nil {
			return nil, err
		}
	}
	return out, s.fillDepartments(ctx, out)
}

// fillCollabs: the collabs each person is a member of, archived ones too.
func (s *Store) fillCollabs(ctx context.Context, list []Profile) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	idx := map[string]int{}
	for i, p := range list {
		ids[i] = p.ID
		idx[p.ID] = i
	}
	rows, err := s.db.Query(ctx, `SELECT m.user_id::text, p.key, p.name FROM project_members m
		JOIN projects p ON p.id = m.project_id WHERE p.kind = 'collab' AND m.user_id = ANY($1::uuid[]) ORDER BY p.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		var c DeptRef
		if err := rows.Scan(&uid, &c.ID, &c.Name); err != nil {
			return err
		}
		list[idx[uid]].Collabs = append(list[idx[uid]].Collabs, c)
	}
	return rows.Err()
}

func (s *Store) fillDepartments(ctx context.Context, list []Profile) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	idx := map[string]int{}
	for i, p := range list {
		ids[i] = p.ID
		idx[p.ID] = i
	}
	rows, err := s.db.Query(ctx, `SELECT ud.user_id::text, d.id, d.name FROM user_departments ud
		JOIN departments d ON d.id = ud.department_id WHERE ud.user_id = ANY($1::uuid[]) ORDER BY d.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		var d DeptRef
		if err := rows.Scan(&uid, &d.ID, &d.Name); err != nil {
			return err
		}
		list[idx[uid]].Departments = append(list[idx[uid]].Departments, d)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	return s.fillTeams(ctx, list, idx, ids)
}

func (s *Store) Profile(ctx context.Context, id string) (Profile, error) {
	p, err := scanProfile(s.db.QueryRow(ctx, profileSelect+" WHERE u.id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	list := []Profile{p}
	err = s.fillDepartments(ctx, list)
	return list[0], err
}

// Managers are the employee's managers computes them: the head of each of the employee's
// departments; where the employee is the head, the nearest head above.
func (s *Store) Managers(ctx context.Context, userID string) ([]Person, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT d.id, d.parent_id, d.head_id, 0 AS lvl, d.id AS start FROM user_departments ud
			JOIN departments d ON d.id = ud.department_id WHERE ud.user_id = $1
			UNION ALL
			SELECT p.id, p.parent_id, p.head_id, up.lvl + 1, up.start FROM departments p JOIN up ON p.id = up.parent_id
			WHERE up.head_id IS NULL OR up.head_id = $1)
		SELECT DISTINCT ON (u.id) u.id, u.name, u.position, u.photo_key FROM up JOIN users u ON u.id = up.head_id
		WHERE up.head_id <> $1 AND u.active ORDER BY u.id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
}

// Subordinates are the people of departments the user heads (with heads of their sub-departments).
func (s *Store) Subordinates(ctx context.Context, userID string) ([]Person, error) {
	rows, err := s.db.Query(ctx, `
		SELECT DISTINCT u.id, u.name, u.position, u.photo_key FROM departments d
		JOIN user_departments ud ON ud.department_id = d.id JOIN users u ON u.id = ud.user_id
		WHERE d.head_id = $1 AND u.id <> $1 AND u.active
		UNION
		SELECT u.id, u.name, u.position, u.photo_key FROM departments d JOIN departments c ON c.parent_id = d.id
		JOIN users u ON u.id = c.head_id WHERE d.head_id = $1 AND u.id <> $1 AND u.active
		ORDER BY 2`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
}

// Phones: digits with the usual separators and an optional leading +, 5 to 20 digits; an extension is 1–8 digits.
var (
	phoneRe = regexp.MustCompile(`^\+?[0-9][0-9 ()\-.]{3,24}[0-9]$`)
	innerRe = regexp.MustCompile(`^[0-9]{1,8}$`)
	// Telegram usernames: 5–32 letters, digits and underscores
	telegramRe = regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_]{4,31}$`)
)

// SetPhoto stores the key of an uploaded photo ("" removes it); the caller has saved the object.
func (s *Store) SetPhoto(ctx context.Context, userID, key, contentType string) (old string, err error) {
	err = s.db.QueryRow(ctx, `UPDATE users u SET photo_key = $2, photo_type = $3 FROM users o
		WHERE u.id = $1 AND o.id = u.id RETURNING o.photo_key`, userID, key, contentType).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return old, err
}

// ProfileUpdate is what the profile form saves.
type ProfileUpdate struct {
	LastName, FirstName, SecondName, Position, WorkPhone, MobilePhone, About string
	Gender, City, InnerPhone, Telegram, WhatsApp, Website                    string
	Birthday, Employment                                                     *time.Time
	BirthdayYear                                                             bool
}

// SaveProfile updates the card (the employee themself or an administrator). The display name follows
// the portal: «Имя Фамилия».
func (s *Store) SaveProfile(ctx context.Context, id string, u ProfileUpdate) error {
	t := strings.TrimSpace
	u.LastName, u.FirstName, u.SecondName = t(u.LastName), t(u.FirstName), t(u.SecondName)
	if u.FirstName == "" && u.LastName == "" {
		return i18n.E("err.profile_name")
	}
	if u.Gender != "" && u.Gender != "M" && u.Gender != "F" {
		return i18n.E("err.bad_form")
	}
	for _, ph := range []string{u.WorkPhone, u.MobilePhone} {
		if ph = t(ph); ph != "" && !phoneRe.MatchString(ph) {
			return i18n.E("err.phone", ph)
		}
	}
	if in := t(u.InnerPhone); in != "" && !innerRe.MatchString(in) {
		return i18n.E("err.inner_phone")
	}
	u.Telegram = strings.TrimPrefix(t(u.Telegram), "https://t.me/")
	if u.Telegram != "" {
		u.Telegram = "@" + strings.TrimPrefix(u.Telegram, "@")
		if !telegramRe.MatchString(u.Telegram) {
			return i18n.E("err.telegram")
		}
	}
	if wa := t(u.WhatsApp); wa != "" && !phoneRe.MatchString(wa) {
		return i18n.E("err.phone", wa)
	}
	if w := t(u.Website); w != "" {
		if !strings.HasPrefix(w, "http://") && !strings.HasPrefix(w, "https://") {
			w = "https://" + w
		}
		if pu, err := url.Parse(w); err != nil || pu.Host == "" || strings.ContainsAny(w, " <>\"'") {
			return i18n.E("err.website")
		}
		u.Website = w
	}
	name := portal.UserName(u.FirstName, u.LastName, u.SecondName)
	tag, err := s.db.Exec(ctx, `UPDATE users SET last_name = $2, first_name = $3, second_name = $4, position = $5,
		work_phone = $6, mobile_phone = $7, about = $8, birthday = $9, name = $10,
		gender = $11, city = $12, inner_phone = $13, employment_date = $14,
		telegram = $15, whatsapp = $16, website = $17, birthday_show_year = $18 WHERE id = $1`,
		id, u.LastName, u.FirstName, u.SecondName, t(u.Position), t(u.WorkPhone), t(u.MobilePhone), t(u.About), u.Birthday, name,
		u.Gender, t(u.City), t(u.InnerPhone), u.Employment, u.Telegram, t(u.WhatsApp), t(u.Website), u.BirthdayYear)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetDepartments puts the employee into departments (administrators). A move from other departments goes to
// «Кадровые изменения».
func (s *Store) SetDepartments(ctx context.Context, actorID, userID string, deptIDs []string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		was, err := deptNamesOf(ctx, tx, userID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_departments WHERE user_id = $1`, userID); err != nil {
			return err
		}
		for _, d := range deptIDs {
			if d == "" {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, d); err != nil {
				return err
			}
		}
		now, err := deptNamesOf(ctx, tx, userID)
		if err != nil || len(was) == 0 || slices.Equal(was, now) {
			return err // the first departments are not a move
		}
		return logStaff(ctx, tx, userID, "moved", actorID, was)
	})
}

// MoveEmployee moves a person from one department to another (dragged in the structure): the other departments of
// the person stay; already in the new one — just out of the old one. Logged as a move, as SetDepartments does.
func (s *Store) MoveEmployee(ctx context.Context, actorID, userID, fromID, toID string) error {
	rows, err := s.db.Query(ctx, `SELECT department_id::text FROM user_departments WHERE user_id::text = $1`, userID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if !slices.Contains(ids, fromID) {
		return ErrNotFound
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM departments WHERE id::text = $1)`, toID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	next := slices.DeleteFunc(ids, func(d string) bool { return d == fromID || d == toID })
	return s.SetDepartments(ctx, actorID, userID, append(next, toID))
}

// MakeEmployee makes a guest of collabs an employee: they keep their collabs and see the company from now on.
func (s *Store) MakeEmployee(ctx context.Context, actorID, userID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET guest = false WHERE id = $1 AND guest`, userID)
		if err == nil && tag.RowsAffected() == 0 {
			return i18n.E("err.not_guest")
		}
		if err != nil {
			return err
		}
		return logStaff(ctx, tx, userID, "hired", actorID, nil)
	})
}

// SetActive dismisses (false) or returns (true) an employee (administrators). A dismissed employee
// cannot sign in to go24 and disappears from pickers; their tasks and messages stay.
func (s *Store) SetActive(ctx context.Context, actorID, userID string, active bool) error {
	if actorID == userID && !active {
		return i18n.E("err.fire_self")
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var was bool
		err := tx.QueryRow(ctx, `SELECT active FROM users WHERE id::text = $1 FOR UPDATE`, userID).Scan(&was)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || was == active {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET active = $2 WHERE id = $1`, userID, active); err != nil {
			return err
		}
		kind := "fired"
		if active {
			kind = "returned"
		}
		return logStaff(ctx, tx, userID, kind, actorID, nil)
	})
}
