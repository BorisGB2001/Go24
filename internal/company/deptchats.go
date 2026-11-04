package company

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// ---------- «Чаты и каналы» of departments ----------
//
// As in the portal company structure: a department has group chats and channels of the messenger tied to it; its
// people (with the head and deputies, and with those of sub-departments when the chat says so) come into them by
// themselves, and those who left the department go out — only those the department brought, not those added by hand.

// DeptChat is a chat or a channel of a department.
type DeptChat struct {
	DepartmentID string
	Department   string // its name
	Room         string
	Name         string // the chat's name when it was last seen
	Channel      bool
	Sub          bool   // the people of sub-departments too
	OwnerID      string // on whose behalf people are added and removed
}

var errDeptChatExists = i18n.E("err.dept_chat_exists")

// AddDeptChat ties the chat to the department.
func (s *Store) AddDeptChat(ctx context.Context, c DeptChat) error {
	_, err := s.db.Exec(ctx, `INSERT INTO department_chats (department_id, room, name, channel, sub, owner_id)
		VALUES ($1, $2, $3, $4, $5, $6)`, c.DepartmentID, c.Room, c.Name, c.Channel, c.Sub, c.OwnerID)
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			return errDeptChatExists
		case "23503", "22P02":
			return ErrNotFound
		}
	}
	return err
}

// RemoveDeptChat unties the chat from the department (the chat and its people stay).
func (s *Store) RemoveDeptChat(ctx context.Context, deptID, room string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM department_chats WHERE department_id::text = $1 AND room = $2`, deptID, room)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetDeptChatSub: whether the people of sub-departments come into the chat.
func (s *Store) SetDeptChatSub(ctx context.Context, deptID, room string, sub bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE department_chats SET sub = $3 WHERE department_id::text = $1 AND room = $2`, deptID, room, sub)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// SetDeptChatName keeps the chat's name as the messenger shows it now.
func (s *Store) SetDeptChatName(ctx context.Context, room, name string) error {
	_, err := s.db.Exec(ctx, `UPDATE department_chats SET name = $2 WHERE room = $1 AND name <> $2`, room, name)
	return err
}

// DeptChats are the chats of the department ("" — of all departments), the oldest first.
func (s *Store) DeptChats(ctx context.Context, deptID string) ([]DeptChat, error) {
	rows, err := s.db.Query(ctx, `SELECT c.department_id, d.name, c.room, c.name, c.channel, c.sub, c.owner_id FROM department_chats c
		JOIN departments d ON d.id = c.department_id WHERE $1 = '' OR c.department_id::text = $1 ORDER BY c.created_at, c.room`, deptID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeptChat, error) {
		var c DeptChat
		err := r.Scan(&c.DepartmentID, &c.Department, &c.Room, &c.Name, &c.Channel, &c.Sub, &c.OwnerID)
		return c, err
	})
}

// DeptPeople are the active employees of the department — its members, head and deputies, and those of its
// sub-departments when sub.
func (s *Store) DeptPeople(ctx context.Context, deptID string, sub bool) ([]string, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE down AS (
			SELECT id FROM departments WHERE id::text = $1
			UNION SELECT d.id FROM departments d JOIN down ON d.parent_id = down.id WHERE $2)
		SELECT u.id::text FROM users u WHERE u.active AND NOT u.guest AND (
			u.id IN (SELECT user_id FROM user_departments WHERE department_id IN (SELECT id FROM down))
			OR u.id IN (SELECT head_id FROM departments WHERE id IN (SELECT id FROM down))
			OR u.id IN (SELECT user_id FROM department_deputies WHERE department_id IN (SELECT id FROM down)))
		ORDER BY u.name, u.id`, deptID, sub)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// DeptChatBrought are the people the department brought into its chat.
func (s *Store) DeptChatBrought(ctx context.Context, deptID, room string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id::text FROM department_chat_people WHERE department_id::text = $1 AND room = $2
		ORDER BY user_id`, deptID, room)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SetDeptChatBrought keeps who the department has in its chat now.
func (s *Store) SetDeptChatBrought(ctx context.Context, deptID, room string, ids []string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM department_chat_people WHERE department_id::text = $1 AND room = $2`, deptID, room); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO department_chat_people (department_id, room, user_id)
		SELECT department_id, room, unnest($3::uuid[]) FROM department_chats WHERE department_id::text = $1 AND room = $2
		ON CONFLICT DO NOTHING`, deptID, room, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CanManageDeptChats: administrators, and those «Права доступа» of the structure let keep the department's chats
// (by default its head and deputies, with the sub-departments).
func (s *Store) CanManageDeptChats(ctx context.Context, userID string, admin bool, deptID string) (bool, error) {
	return s.StructCan(ctx, userID, admin, "chats", deptID)
}

// DepartmentName is the department's name.
func (s *Store) DepartmentName(ctx context.Context, id string) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM departments WHERE id::text = $1`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return name, err
}
