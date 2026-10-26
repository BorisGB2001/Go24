package mail

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Списки контактов», as Outlook's: the user's named lists of addresses. Typed in «Кому», «Копия» or «СК», a list is
// offered by its name, and so is a department of the company (as Exchange's distribution groups: its people and those
// of its subdepartments with an e-mail); picked, it turns into its people.

// Member is an address of a contact list.
type Member struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ContactList is a «Список контактов» of the user.
type ContactList struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []Member `json:"members"`
}

// Group is a list or a department offered among the recipients: Kind is "list" or "dept".
type Group struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Members []Member `json:"members"`
}

// MaxMembers bounds a contact list; MaxContactLists — the user's lists.
const (
	MaxMembers      = 500
	MaxContactLists = 200
)

// Members reads the addresses of a list as they are typed: separated by commas, semicolons or lines, each
// «Имя <адрес>» or the address; the same address once.
func Members(text string) ([]Member, error) {
	list, err := addresses(strings.NewReplacer("\r\n", ",", "\n", ",").Replace(text))
	if err != nil {
		return nil, i18n.E("err.mail_cl_address")
	}
	var out []Member
	seen := map[string]bool{}
	for _, a := range list {
		k := strings.ToLower(a.Address)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, Member{Name: a.Name, Email: a.Address})
	}
	return out, nil
}

// String is the list's addresses as they are typed, one a line.
func (c ContactList) String() string {
	lines := make([]string, len(c.Members))
	for i, m := range c.Members {
		lines[i] = m.Email
		if m.Name != "" {
			name := m.Name
			if strings.ContainsAny(name, `,;"<>()@:\.[]`) {
				name = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
			}
			lines[i] = name + " <" + m.Email + ">"
		}
	}
	return strings.Join(lines, "\n")
}

func scanContactLists(rows pgx.Rows, err error) ([]ContactList, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ContactList
	for rows.Next() {
		var c ContactList
		var raw []byte
		if err := rows.Scan(&c.ID, &c.Name, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &c.Members); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const contactListSQL = `SELECT id::text, name, members FROM mail_contact_lists`

// ContactLists are the user's contact lists by name.
func (s *Store) ContactLists(ctx context.Context, userID string) ([]ContactList, error) {
	return scanContactLists(s.db.Query(ctx, contactListSQL+` WHERE user_id::text = $1 ORDER BY lower(name)`, userID))
}

// ContactListOf is a contact list of the user.
func (s *Store) ContactListOf(ctx context.Context, userID, id string) (ContactList, error) {
	list, err := scanContactLists(s.db.Query(ctx, contactListSQL+` WHERE id::text = $1 AND user_id::text = $2`, id, userID))
	if err != nil {
		return ContactList{}, err
	}
	if len(list) == 0 {
		return ContactList{}, ErrNotFound
	}
	return list[0], nil
}

// SaveContactList makes a contact list of the user (ID "") or changes one.
func (s *Store) SaveContactList(ctx context.Context, userID string, c ContactList) (string, error) {
	c.Name = strings.TrimSpace(c.Name)
	switch {
	case c.Name == "" || utf8.RuneCountInString(c.Name) > 100:
		return "", i18n.E("err.mail_cl_name")
	case len(c.Members) == 0:
		return "", i18n.E("err.mail_cl_empty")
	case len(c.Members) > MaxMembers:
		return "", i18n.E("err.mail_cl_many")
	}
	members, err := json.Marshal(c.Members)
	if err != nil {
		return "", err
	}
	if c.ID == "" {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_contact_lists WHERE user_id::text = $1`, userID).Scan(&n); err != nil {
			return "", err
		}
		if n >= MaxContactLists {
			return "", i18n.E("err.mail_cl_limit")
		}
		err = s.db.QueryRow(ctx, `INSERT INTO mail_contact_lists (user_id, name, members) VALUES ($1, $2, $3) RETURNING id::text`,
			userID, c.Name, members).Scan(&c.ID)
		return c.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_contact_lists SET name = $3, members = $4 WHERE id::text = $1 AND user_id::text = $2`,
		c.ID, userID, c.Name, members)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return c.ID, err
}

// DeleteContactList deletes a contact list of the user.
func (s *Store) DeleteContactList(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_contact_lists WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Groups are the user's contact lists and the company's departments whose names match q, with their people.
func (s *Store) Groups(ctx context.Context, userID, q string, limit int) ([]Group, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	lists, err := scanContactLists(s.db.Query(ctx, contactListSQL+` WHERE user_id::text = $1 AND name ILIKE $2
		ORDER BY lower(name) LIMIT $3`, userID, like, limit))
	if err != nil {
		return nil, err
	}
	var out []Group
	for _, c := range lists {
		out = append(out, Group{ID: c.ID, Name: c.Name, Kind: "list", Members: c.Members})
	}
	rows, err := s.db.Query(ctx, `WITH RECURSIVE d AS (
			SELECT id AS top, id FROM departments WHERE name ILIKE $1
			UNION SELECT d.top, x.id FROM departments x JOIN d ON x.parent_id = d.id)
		SELECT t.id::text, t.name, coalesce(jsonb_agg(DISTINCT jsonb_build_object('name', u.name, 'email', u.email))
			FILTER (WHERE u.id IS NOT NULL), '[]')
		FROM departments t JOIN d ON d.top = t.id
		LEFT JOIN user_departments ud ON ud.department_id = d.id
		LEFT JOIN users u ON u.id = ud.user_id AND u.active AND u.email LIKE '%@%'
		GROUP BY t.id, t.name ORDER BY lower(t.name) LIMIT $2`, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		g := Group{Kind: "dept"}
		var raw []byte
		if err := rows.Scan(&g.ID, &g.Name, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &g.Members); err != nil {
			return nil, err
		}
		if len(g.Members) > 0 {
			out = append(out, g)
		}
	}
	return out, rows.Err()
}
