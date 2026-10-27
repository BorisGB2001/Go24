package portalrest

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/anon"
	"go24/internal/portal"
)

// User is what user.get returns (the fields go24 uses).
type User struct {
	ID          string    `json:"ID"`
	Active      bool      `json:"ACTIVE"`
	Type        string    `json:"USER_TYPE"`
	Name        string    `json:"NAME"`
	LastName    string    `json:"LAST_NAME"`
	SecondName  string    `json:"SECOND_NAME"`
	Email       string    `json:"EMAIL"`
	Gender      string    `json:"PERSONAL_GENDER"`
	Birthday    string    `json:"PERSONAL_BIRTHDAY"`
	City        string    `json:"PERSONAL_CITY"`
	Mobile      string    `json:"PERSONAL_MOBILE"`
	Phone       string    `json:"PERSONAL_PHONE"`
	WorkPhone   string    `json:"WORK_PHONE"`
	Inner       flexStr   `json:"UF_PHONE_INNER"`
	Position    string    `json:"WORK_POSITION"`
	Employment  string    `json:"UF_EMPLOYMENT_DATE"`
	TimeZone    string    `json:"TIME_ZONE"`
	Departments []flexInt `json:"UF_DEPARTMENT"`
}

// flexStr / flexInt accept numbers or strings: the portal is not consistent about JSON types.
type flexStr string

func (f *flexStr) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*f = flexStr(s)
		return nil
	}
	*f = flexStr(strings.Trim(string(b), `"`))
	if *f == "null" {
		*f = ""
	}
	return nil
}

type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	n, err := strconv.Atoi(strings.Trim(string(b), `"`))
	*f = flexInt(n)
	return err
}

// Options of the import of one portal.
type Options struct {
	Portal string           // short tag of the portal, part of the users' identity: "acme" → oidc_sub "portal:acme:<id>"
	Anon   *anon.Anonymizer // seeded per portal, so re-imports give the same pseudonyms
	Seed   string
}

type UserStats struct{ Users, Active, Departments int }

// ImportUsers copies the portal's employees into go24 with every personal detail replaced:
// names (by gender), e-mail, phones, inner phone, birthday, city and employment date are made up;
// positions are kept as job titles but shuffled between people, so a title does not point to a person.
// Departments are linked by their portal ID; their names come with ImportDepartments (scope "department").
func ImportUsers(ctx context.Context, c *Client, db *pgxpool.Pool, o Options) (UserStats, error) {
	var st UserStats
	var users []User
	for _, active := range []bool{true, false} {
		err := List(ctx, c, "user.get", map[string]any{"FILTER": map[string]any{"ACTIVE": active, "USER_TYPE": "employee"}},
			func(u User) error { users = append(users, u); return nil })
		if err != nil {
			return st, err
		}
	}
	slices.SortFunc(users, func(a, b User) int { return atoi(a.ID) - atoi(b.ID) })

	positions := make([]string, 0, len(users))
	for _, u := range users {
		if p := strings.TrimSpace(u.Position); p != "" {
			positions = append(positions, p)
		}
	}
	r := rng(o.Seed, "positions")
	r.Shuffle(len(positions), func(i, j int) { positions[i], positions[j] = positions[j], positions[i] })

	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var rootID string
		if err := tx.QueryRow(ctx, `SELECT id FROM departments WHERE parent_id IS NULL`).Scan(&rootID); err != nil {
			return fmt.Errorf("company structure: open «Сотрудники» in go24 once, or import departments first: %w", err)
		}
		pos := 0
		for _, u := range users {
			id := atoi(u.ID)
			g := map[string]string{"M": "M", "F": "F"}[u.Gender]
			p := o.Anon.AddPerson(anon.Person{ID: id, First: u.Name, Last: u.LastName, Middle: u.SecondName, Gender: g, Email: u.Email})
			if g == "" {
				g = p.Gender
			}
			position := ""
			if strings.TrimSpace(u.Position) != "" && pos < len(positions) {
				position = positions[pos]
				pos++
			}
			fr := rng(o.Seed, "user", u.ID)
			phone := func(real string) string {
				if strings.TrimSpace(real) == "" {
					return ""
				}
				return o.Anon.Phone(real)
			}
			inner := ""
			if string(u.Inner) != "" {
				inner = strconv.Itoa(1000 + fr.IntN(9000))
			}
			var birthday, employed *time.Time
			if u.Birthday != "" {
				d := time.Date(1965+fr.IntN(38), time.Month(1+fr.IntN(12)), 1+fr.IntN(28), 0, 0, 0, 0, time.UTC)
				birthday = &d
			}
			if u.Employment != "" {
				d := time.Date(2015+fr.IntN(11), time.Month(1+fr.IntN(12)), 1+fr.IntN(28), 0, 0, 0, 0, time.UTC)
				employed = &d
			}
			city := ""
			if strings.TrimSpace(u.City) != "" {
				city = cities[fr.IntN(len(cities))]
			}
			name := strings.TrimSpace(p.FakeFirst + " " + p.FakeLast)
			var uid string
			if err := tx.QueryRow(ctx, `
				INSERT INTO users (oidc_sub, email, name, last_name, first_name, second_name, gender, position,
				                   work_phone, mobile_phone, inner_phone, birthday, city, employment_date, time_zone, active)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
				ON CONFLICT (oidc_sub) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name,
				  last_name = EXCLUDED.last_name, first_name = EXCLUDED.first_name, second_name = EXCLUDED.second_name,
				  gender = EXCLUDED.gender, position = EXCLUDED.position, work_phone = EXCLUDED.work_phone,
				  mobile_phone = EXCLUDED.mobile_phone, inner_phone = EXCLUDED.inner_phone, birthday = EXCLUDED.birthday,
				  city = EXCLUDED.city, employment_date = EXCLUDED.employment_date, time_zone = EXCLUDED.time_zone,
				  active = EXCLUDED.active
				RETURNING id`,
				fmt.Sprintf("portal:%s:%d", o.Portal, id), p.FakeEmail, name, p.FakeLast, p.FakeFirst, p.FakeMiddle, g, position,
				phone(u.WorkPhone), phone(firstNonEmpty(u.Mobile, u.Phone)), inner, birthday, city, employed, u.TimeZone, u.Active,
			).Scan(&uid); err != nil {
				return fmt.Errorf("user %d: %w", id, err)
			}
			st.Users++
			if u.Active {
				st.Active++
			}
			if _, err := tx.Exec(ctx, `DELETE FROM user_departments WHERE user_id = $1`, uid); err != nil {
				return err
			}
			for _, d := range u.Departments {
				var did string
				err := tx.QueryRow(ctx, `INSERT INTO departments (parent_id, name, legacy_id) VALUES ($1, $2, $3)
					ON CONFLICT (legacy_id) DO UPDATE SET legacy_id = EXCLUDED.legacy_id RETURNING id`,
					rootID, fmt.Sprintf("#%d", d), int(d)).Scan(&did)
				if err != nil {
					return fmt.Errorf("department %d: %w", d, err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, uid, did); err != nil {
					return err
				}
			}
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM departments WHERE legacy_id IS NOT NULL`).Scan(&st.Departments)
	})
	if err == nil { // the names by the portal's «Формат имени»
		_, err = portal.RenameAll(ctx, db)
	}
	return st, err
}

// Department is what department.get returns.
type Department struct {
	ID     flexInt `json:"ID"`
	Name   string  `json:"NAME"`
	Sort   flexInt `json:"SORT"`
	Parent flexStr `json:"PARENT"`
	Head   flexStr `json:"UF_HEAD"`
}

// ImportDepartments copies the structure (names kept: they are the company's, like company names).
// The portal's top department becomes go24's company node; heads are linked to imported people.
func ImportDepartments(ctx context.Context, c *Client, db *pgxpool.Pool, o Options) (int, error) {
	var depts []Department
	if err := List(ctx, c, "department.get", map[string]any{}, func(d Department) error { depts = append(depts, d); return nil }); err != nil {
		return 0, err
	}
	n := 0
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		ids := map[int]string{}
		// parents before children
		var placed func(d Department) error
		byID := map[int]Department{}
		for _, d := range depts {
			byID[int(d.ID)] = d
		}
		placed = func(d Department) error {
			if _, ok := ids[int(d.ID)]; ok {
				return nil
			}
			parent := atoi(string(d.Parent))
			var parentID any
			if parent != 0 {
				if pd, ok := byID[parent]; ok {
					if err := placed(pd); err != nil {
						return err
					}
					parentID = ids[parent]
				}
			}
			var id string
			if parentID == nil { // the company itself: take over go24's root node
				// ImportUsers may have made a placeholder for this department: move its people to the root.
				if _, err := tx.Exec(ctx, `INSERT INTO user_departments (user_id, department_id)
					SELECT ud.user_id, r.id FROM user_departments ud JOIN departments p ON p.id = ud.department_id
					CROSS JOIN departments r WHERE p.legacy_id = $1 AND p.parent_id IS NOT NULL AND r.parent_id IS NULL
					ON CONFLICT DO NOTHING`, int(d.ID)); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM departments WHERE legacy_id = $1 AND parent_id IS NOT NULL`, int(d.ID)); err != nil {
					return err
				}
				err := tx.QueryRow(ctx, `UPDATE departments SET name = $1, legacy_id = $2, sort = 0 WHERE parent_id IS NULL RETURNING id`,
					d.Name, int(d.ID)).Scan(&id)
				if err != nil {
					return fmt.Errorf("root department: %w", err)
				}
			} else if err := tx.QueryRow(ctx, `INSERT INTO departments (parent_id, name, sort, legacy_id) VALUES ($1, $2, $3, $4)
					ON CONFLICT (legacy_id) DO UPDATE SET parent_id = EXCLUDED.parent_id, name = EXCLUDED.name, sort = EXCLUDED.sort
					RETURNING id`, parentID, d.Name, int(d.Sort), int(d.ID)).Scan(&id); err != nil {
				return fmt.Errorf("department %d: %w", d.ID, err)
			}
			ids[int(d.ID)] = id
			n++
			if h := atoi(string(d.Head)); h != 0 {
				if _, err := tx.Exec(ctx, `UPDATE departments SET head_id = (SELECT id FROM users WHERE oidc_sub = $2) WHERE id = $1`,
					id, fmt.Sprintf("portal:%s:%d", o.Portal, h)); err != nil {
					return err
				}
			}
			return nil
		}
		for _, d := range depts {
			if err := placed(d); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

var cities = []string{"Москва", "Санкт-Петербург", "Новосибирск", "Екатеринбург", "Казань", "Нижний Новгород", "Самара",
	"Ростов-на-Дону", "Краснодар", "Владивосток", "Хабаровск", "Мурманск", "Пермь", "Воронеж", "Тюмень", "Иркутск"}

func rng(seed string, parts ...string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(seed))
	for _, p := range parts {
		h.Write([]byte{0})
		h.Write([]byte(p))
	}
	s := h.Sum64()
	return rand.New(rand.NewPCG(s, s^0x9e3779b97f4a7c15))
}

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
