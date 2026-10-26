package company

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// ---------- teams ----------
//
// «Команды» of the portal company structure: a team stands under a department and shows there, has a head, deputies
// and members from any departments (a person may be in several teams). Unlike departments, teams make nobody's
// manager. Administrators create, move and delete teams and set their heads; a team's head changes its name,
// description, deputies and members.

// Team is a team with its people.
type Team struct {
	ID           string
	DepartmentID string
	Department   string // its name
	Name         string
	Description  string
	Head         *Person
	Deputies     []Person
	Members      []Person // neither the head nor deputies
}

// Total counts the team's people: head, deputies and members.
func (t *Team) Total() int {
	n := len(t.Deputies) + len(t.Members)
	if t.Head != nil {
		n++
	}
	return n
}

// TeamInput is what a team form or the REST changes.
type TeamInput struct {
	DepartmentID string
	Name         string
	Description  string
	HeadID       string
	Deputies     []string
	Members      []string
}

func cleanTeam(in *TeamInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 200 {
		return i18n.E("err.team_name")
	}
	in.Description = strings.TrimSpace(in.Description)
	if len([]rune(in.Description)) > 2000 {
		return i18n.E("err.team_description")
	}
	if in.DepartmentID == "" {
		return i18n.E("err.bad_form")
	}
	return nil
}

// Teams lists all teams with their people, by name; deptID — only the teams of that department.
func (s *Store) Teams(ctx context.Context, deptID string) ([]*Team, error) {
	rows, err := s.db.Query(ctx, `SELECT t.id, t.department_id, d.name, t.name, t.description FROM teams t
		JOIN departments d ON d.id = t.department_id WHERE $1 = '' OR t.department_id::text = $1 ORDER BY t.name, t.id`, deptID)
	if err != nil {
		return nil, err
	}
	var out []*Team
	byID := map[string]*Team{}
	for rows.Next() {
		t := &Team{}
		if err := rows.Scan(&t.ID, &t.DepartmentID, &t.Department, &t.Name, &t.Description); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
		byID[t.ID] = t
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	ids := make([]string, len(out))
	for i, t := range out {
		ids[i] = t.ID
	}
	rows, err = s.db.Query(ctx, `SELECT m.team_id::text, m.role, u.id, u.name, u.position, u.photo_key FROM team_members m
		JOIN users u ON u.id = m.user_id WHERE m.team_id = ANY($1::uuid[]) AND u.active ORDER BY u.name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var tid, role string
		var p Person
		if err := rows.Scan(&tid, &role, &p.ID, &p.Name, &p.Position, &p.PhotoKey); err != nil {
			return nil, err
		}
		t := byID[tid]
		switch role {
		case "head":
			t.Head = &p
		case "deputy":
			t.Deputies = append(t.Deputies, p)
		default:
			t.Members = append(t.Members, p)
		}
	}
	return out, rows.Err()
}

// Team is one team with its people.
func (s *Store) Team(ctx context.Context, id string) (*Team, error) {
	var deptID string
	err := s.db.QueryRow(ctx, `SELECT department_id::text FROM teams WHERE id::text = $1`, id).Scan(&deptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	list, err := s.Teams(ctx, deptID)
	if err != nil {
		return nil, err
	}
	for _, t := range list {
		if t.ID == id {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

// CanEditTeam: administrators and the team's head.
func (s *Store) CanEditTeam(ctx context.Context, userID string, admin bool, teamID string) (bool, error) {
	if admin {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id::text = $1 AND user_id = $2 AND role = 'head')`,
		teamID, userID).Scan(&ok)
	return ok, err
}

// CreateTeam adds a team under a department (administrators — checked by the caller) and returns its id.
func (s *Store) CreateTeam(ctx context.Context, in TeamInput) (string, error) {
	if err := cleanTeam(&in); err != nil {
		return "", err
	}
	var id string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO teams (department_id, name, description)
			SELECT id, $2, $3 FROM departments WHERE id::text = $1 RETURNING id::text`, in.DepartmentID, in.Name, in.Description).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return setTeamPeople(ctx, tx, id, in)
	})
	return id, err
}

// UpdateTeam saves a team. byHead — the team's head saves it: the department and the head stay as they are.
func (s *Store) UpdateTeam(ctx context.Context, id string, in TeamInput, byHead bool) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var dept string
		var head *string
		err := tx.QueryRow(ctx, `SELECT t.department_id::text, (SELECT m.user_id::text FROM team_members m WHERE m.team_id = t.id AND m.role = 'head')
			FROM teams t WHERE t.id::text = $1 FOR UPDATE`, id).Scan(&dept, &head)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if byHead {
			in.DepartmentID, in.HeadID = dept, ""
			if head != nil {
				in.HeadID = *head
			}
		}
		if err := cleanTeam(&in); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE teams SET department_id = d.id, name = $3, description = $4 FROM departments d
			WHERE teams.id::text = $1 AND d.id::text = $2`, id, in.DepartmentID, in.Name, in.Description)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM team_members WHERE team_id::text = $1`, id); err != nil {
			return err
		}
		return setTeamPeople(ctx, tx, id, in)
	})
}

// setTeamPeople writes the head, deputies and members of a team; a person takes the highest of the roles given.
func setTeamPeople(ctx context.Context, tx pgx.Tx, id string, in TeamInput) error {
	if in.HeadID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO team_members (team_id, user_id, role) SELECT $1, u.id, 'head' FROM users u
			WHERE u.id::text = $2`, id, in.HeadID); err != nil {
			return err
		}
	}
	for _, g := range []struct {
		role string
		ids  []string
	}{{"deputy", in.Deputies}, {"member", in.Members}} {
		ids := slices.DeleteFunc(slices.Clone(g.ids), func(x string) bool { return x == "" })
		if len(ids) == 0 {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO team_members (team_id, user_id, role)
			SELECT $1, u.id, $2 FROM users u WHERE u.id::text = ANY($3) ON CONFLICT DO NOTHING`, id, g.role, ids); err != nil {
			return err
		}
	}
	return nil
}

// DeleteTeam removes a team (administrators — checked by the caller); its people stay where they are.
func (s *Store) DeleteTeam(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM teams WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// fillTeams: the teams each person is in.
func (s *Store) fillTeams(ctx context.Context, list []Profile, idx map[string]int, ids []string) error {
	rows, err := s.db.Query(ctx, `SELECT m.user_id::text, t.id, t.name FROM team_members m
		JOIN teams t ON t.id = m.team_id WHERE m.user_id = ANY($1::uuid[]) ORDER BY t.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid string
		var t DeptRef
		if err := rows.Scan(&uid, &t.ID, &t.Name); err != nil {
			return err
		}
		list[idx[uid]].Teams = append(list[idx[uid]].Teams, t)
	}
	return rows.Err()
}
