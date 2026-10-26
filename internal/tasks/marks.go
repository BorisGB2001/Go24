package tasks

import (
	"context"
	"strings"
)

// Task marks: tags are the task's own and seen by everyone; favourites, pins and «выключить звук»
// are each person's own.

var markKinds = map[string]bool{"favorite": true, "pin": true, "mute": true}

// SetMark puts or takes off one of the person's marks on a task they see.
func (s *Store) SetMark(ctx context.Context, userID, taskID, mark string, on bool) error {
	if !markKinds[mark] {
		return invalid("err.bad_form")
	}
	if err := s.CheckVisible(ctx, userID, taskID); err != nil {
		return err
	}
	var err error
	if on {
		_, err = s.db.Exec(ctx, `INSERT INTO task_marks (user_id, task_id, mark) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, userID, taskID, mark)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM task_marks WHERE user_id = $1 AND task_id = $2 AND mark = $3`, userID, taskID, mark)
	}
	return err
}

// Marks are the person's marks on a task.
func (s *Store) Marks(ctx context.Context, userID, taskID string) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT mark FROM task_marks WHERE user_id = $1 AND task_id = $2`, userID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out[m] = true
	}
	return out, rows.Err()
}

// SetTag adds or removes a tag (people who may work with the task); the history keeps it.
func (s *Store) SetTag(ctx context.Context, userID, taskID, tag string, add bool) error {
	tag = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(tag), "#"))
	if tag == "" || len([]rune(tag)) > 50 {
		return invalid("err.tag")
	}
	a, err := loadAccess(ctx, s.db, userID, taskID, false)
	if err != nil {
		return err
	}
	if !(a.Roles.Worker() || a.Roles.Creator || a.Roles.Manager) {
		return ErrForbidden
	}
	if add {
		tag2, err := s.db.Exec(ctx, `INSERT INTO task_tags (task_id, tag) VALUES ($1, $2) ON CONFLICT DO NOTHING`, taskID, tag)
		if err != nil || tag2.RowsAffected() == 0 {
			return err
		}
		return addHistory(ctx, s.db, taskID, userID, "tag", "", tag)
	}
	t, err := s.db.Exec(ctx, `DELETE FROM task_tags WHERE task_id = $1 AND tag = $2`, taskID, tag)
	if err != nil || t.RowsAffected() == 0 {
		return err
	}
	return addHistory(ctx, s.db, taskID, userID, "tag", tag, "")
}

// FillMarks sets Tags of the tasks and the viewer's Favorite/Pinned/Muted.
func (s *Store) FillMarks(ctx context.Context, viewer string, list []Task) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	at := map[string]int{}
	for i, t := range list {
		ids[i], at[t.ID] = t.ID, i
	}
	rows, err := s.db.Query(ctx, `SELECT task_id::text, tag FROM task_tags WHERE task_id = ANY($1::uuid[]) ORDER BY lower(tag)`, ids)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, tag string
		if err := rows.Scan(&id, &tag); err != nil {
			rows.Close()
			return err
		}
		list[at[id]].Tags = append(list[at[id]].Tags, tag)
	}
	rows.Close()
	if viewer == "" {
		return nil
	}
	rows, err = s.db.Query(ctx, `SELECT task_id::text, mark FROM task_marks WHERE user_id = $1 AND task_id = ANY($2::uuid[])`, viewer, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, m string
		if err := rows.Scan(&id, &m); err != nil {
			return err
		}
		t := &list[at[id]]
		switch m {
		case "favorite":
			t.Favorite = true
		case "pin":
			t.Pinned = true
		case "mute":
			t.Muted = true
		}
	}
	return rows.Err()
}

// Tags are the tags in use, most used first (for suggestions).
func (s *Store) Tags(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT tag FROM task_tags GROUP BY tag ORDER BY count(*) DESC, tag LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
