package tasks

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Checklists of a task: several named lists with their own progress; an item can be renamed,
// dragged (to another list too), marked important; «@Name» in an item makes that person an accomplice.
// Participants who do the task and project managers edit them (TaskCard.CanWork).

type ChecklistItem struct {
	ID        string
	Title     string
	Done      bool
	Important bool
	ListID    string
}

// Checklist is one list of the task with its items.
type Checklist struct {
	ID    string
	Title string // "" — «Чек-лист N» in the page's language
	N     int    // its number in the task, from 1
	Items []ChecklistItem
}

func (c Checklist) Done() int {
	n := 0
	for _, i := range c.Items {
		if i.Done {
			n++
		}
	}
	return n
}

// ListPrefix starts a new checklist in the line form of checklists (new task form, templates, CSV, flows):
// "# Name" — a list named Name, the lines after it are its items.
const ListPrefix = "#"

// ChecklistLines is the task's checklists in the line form: the first list's name only when it has one or there
// are more lists, so a single plain checklist stays the plain list of items it always was.
func (c TaskCard) ChecklistLines() []string {
	var out []string
	for i, l := range c.Checklists {
		if l.Title != "" || i > 0 {
			out = append(out, ListPrefix+" "+l.Title)
		}
		for _, it := range l.Items {
			out = append(out, it.Title)
		}
	}
	return out
}

func loadChecklists(ctx context.Context, q querier, taskID string) ([]Checklist, error) {
	rows, err := q.Query(ctx, `SELECT id, title FROM task_checklists WHERE task_id = $1 ORDER BY sort, created_at`, taskID)
	if err != nil {
		return nil, err
	}
	n := 0
	lists, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Checklist, error) {
		n++
		l := Checklist{N: n}
		return l, row.Scan(&l.ID, &l.Title)
	})
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT id, title, done, important, checklist_id FROM task_checklist_items
		WHERE task_id = $1 ORDER BY sort, created_at`, taskID)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ChecklistItem])
	if err != nil {
		return nil, err
	}
	at := map[string]int{}
	for i, l := range lists {
		at[l.ID] = i
	}
	for _, it := range items {
		if i, ok := at[it.ListID]; ok {
			lists[i].Items = append(lists[i].Items, it)
		}
	}
	return lists, nil
}

// insertChecklistLines adds items from lines to the task: with lists, "# Name" starts a new list (an empty name
// is allowed); other lines go to the current one — listID, or with "" the task's first list (made when there is none).
func insertChecklistLines(ctx context.Context, tx pgx.Tx, actorID, taskID, listID string, lines []string, lists bool) (added []string, err error) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if name, ok := strings.CutPrefix(line, ListPrefix); ok && lists {
			if listID, err = insertChecklist(ctx, tx, taskID, strings.TrimSpace(name)); err != nil {
				return nil, err
			}
			continue
		}
		if line == "" {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO task_checklist_items (task_id, checklist_id, title, created_by, sort)
			VALUES ($1, nullif($2, '')::uuid, $3, $4, (SELECT coalesce(max(sort), 0) + 1 FROM task_checklist_items WHERE task_id = $1))`,
			taskID, listID, line, actorID); err != nil {
			return nil, err
		}
		added = append(added, line)
	}
	return added, nil
}

func insertChecklist(ctx context.Context, tx pgx.Tx, taskID, title string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO task_checklists (task_id, title, sort)
		VALUES ($1, left($2, 200), (SELECT coalesce(max(sort), 0) + 1 FROM task_checklists WHERE task_id = $1)) RETURNING id`,
		taskID, title).Scan(&id)
	return id, err
}

// checklistTx runs fn for a user who may edit the task's checklists.
func (s *Store) checklistTx(ctx context.Context, userID, taskID string, fn func(pgx.Tx) error) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, false)
		if err != nil {
			return err
		}
		if !(a.Roles.Worker() || a.Roles.Manager) {
			return ErrForbidden
		}
		return fn(tx)
	})
}

// AddChecklistItem adds an item to the task's first checklist (robots, the API).
func (s *Store) AddChecklistItem(ctx context.Context, userID, taskID, title string) error {
	return s.AddChecklistItems(ctx, userID, taskID, "", title)
}

// AddChecklistItems adds every line of text as an item of the list (a pasted list becomes many items, as in
// the portal); listID "" is the first list. People mentioned as "@Name" become accomplices.
func (s *Store) AddChecklistItems(ctx context.Context, userID, taskID, listID, text string) error {
	lines := strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		if listID != "" {
			if err := listOfTask(ctx, tx, taskID, listID); err != nil {
				return err
			}
		}
		added, err := insertChecklistLines(ctx, tx, userID, taskID, listID, lines, false) // lists have their own button
		if err != nil {
			return err
		}
		if len(added) == 0 {
			return invalid("err.checklist_empty")
		}
		for _, t := range added {
			if err := addHistory(ctx, tx, taskID, userID, "checklist", "", t); err != nil {
				return err
			}
		}
		return checklistMentions(ctx, tx, userID, taskID, strings.Join(added, "\n"))
	})
}

// checklistMentions makes people mentioned in an item accomplices of the task with a person
// added to a checklist item; the creator, the responsible person and those already in the task stay as they are.
func checklistMentions(ctx context.Context, tx pgx.Tx, userID, taskID, text string) error {
	found, err := mentioned(ctx, tx, text)
	if err != nil || len(found) == 0 {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT u FROM unnest($2::uuid[]) u
		WHERE NOT EXISTS (SELECT 1 FROM tasks t WHERE t.id = $1 AND u IN (t.creator_id, t.responsible_id))
		  AND NOT EXISTS (SELECT 1 FROM task_participants p WHERE p.task_id = $1 AND p.user_id = u)`, taskID, mentionIDs(found))
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := setParticipant(ctx, tx, userID, taskID, id, "accomplice", true); err != nil {
			return err
		}
	}
	return nil
}

func listOfTask(ctx context.Context, tx pgx.Tx, taskID, listID string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_checklists WHERE id::text = $1 AND task_id = $2)`,
		listID, taskID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// AddChecklist adds an empty list to the task («Добавить чек-лист») and returns its id.
func (s *Store) AddChecklist(ctx context.Context, userID, taskID, title string) (id string, err error) {
	err = s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		id, err = insertChecklist(ctx, tx, taskID, strings.TrimSpace(title))
		return err
	})
	return id, err
}

// RenameChecklist names a list ("" — back to «Чек-лист N»).
func (s *Store) RenameChecklist(ctx context.Context, userID, taskID, listID, title string) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE task_checklists SET title = left($3, 200) WHERE id::text = $1 AND task_id = $2`,
			listID, taskID, strings.TrimSpace(title))
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
		return err
	})
}

// DeleteChecklist removes a list with all its items.
func (s *Store) DeleteChecklist(ctx context.Context, userID, taskID, listID string) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM task_checklist_items WHERE checklist_id::text = $1 AND task_id = $2 RETURNING title`,
			listID, taskID)
		if err != nil {
			return err
		}
		titles, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM task_checklists WHERE id::text = $1 AND task_id = $2`, listID, taskID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		for _, t := range titles {
			if err := addHistory(ctx, tx, taskID, userID, "checklist", t, ""); err != nil {
				return err
			}
		}
		return nil
	})
}

// ChangeChecklistItem flips the item's done flag, or deletes the item when remove is true.
func (s *Store) ChangeChecklistItem(ctx context.Context, userID, taskID, itemID string, remove bool) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		var title string
		var done bool
		var err error
		if remove {
			err = tx.QueryRow(ctx, `DELETE FROM task_checklist_items WHERE id::text = $1 AND task_id = $2 RETURNING title, done`, itemID, taskID).Scan(&title, &done)
		} else {
			err = tx.QueryRow(ctx, `UPDATE task_checklist_items
				SET done = NOT done,
				    done_by = CASE WHEN done THEN NULL ELSE $3::uuid END,
				    done_at = CASE WHEN done THEN NULL ELSE now() END
				WHERE id::text = $1 AND task_id = $2 RETURNING title, done`, itemID, taskID, userID).Scan(&title, &done)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch {
		case remove:
			return addHistory(ctx, tx, taskID, userID, "checklist", title, "")
		case done:
			return addHistory(ctx, tx, taskID, userID, "checklist_done", "", title)
		default:
			return addHistory(ctx, tx, taskID, userID, "checklist_undone", "", title)
		}
	})
}

// EditChecklistItem renames an item; newly mentioned people become accomplices.
func (s *Store) EditChecklistItem(ctx context.Context, userID, taskID, itemID, title string) error {
	title = strings.TrimSpace(strings.ReplaceAll(title, "\n", " "))
	if title == "" {
		return invalid("err.checklist_empty")
	}
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		var old string
		err := tx.QueryRow(ctx, `UPDATE task_checklist_items i SET title = $3 FROM task_checklist_items o
			WHERE i.id = o.id AND i.id::text = $1 AND i.task_id = $2 RETURNING o.title`, itemID, taskID, title).Scan(&old)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || old == title {
			return err
		}
		if err := addHistory(ctx, tx, taskID, userID, "checklist", old, title); err != nil {
			return err
		}
		return checklistMentions(ctx, tx, userID, taskID, title)
	})
}

// MarkChecklistItem sets or clears the item's «Важный» mark.
func (s *Store) MarkChecklistItem(ctx context.Context, userID, taskID, itemID string, important bool) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE task_checklist_items SET important = $3 WHERE id::text = $1 AND task_id = $2`,
			itemID, taskID, important)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
		return err
	})
}

// MoveChecklistItem puts an item into a list (listID "" — its own) before another item (beforeID "" — at the end).
func (s *Store) MoveChecklistItem(ctx context.Context, userID, taskID, itemID, listID, beforeID string) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		var own string
		err := tx.QueryRow(ctx, `SELECT checklist_id FROM task_checklist_items WHERE id::text = $1 AND task_id = $2`,
			itemID, taskID).Scan(&own)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if listID == "" {
			listID = own
		} else if err := listOfTask(ctx, tx, taskID, listID); err != nil {
			return err
		}
		// the list's order without the item, the item put in its place, then all numbered anew
		rows, err := tx.Query(ctx, `SELECT id::text FROM task_checklist_items WHERE checklist_id = $1 AND id::text <> $2
			ORDER BY sort, created_at`, listID, itemID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		at := len(ids)
		for i, id := range ids {
			if id == beforeID {
				at = i
			}
		}
		ids = append(ids[:at], append([]string{itemID}, ids[at:]...)...)
		// sort is task-wide (new items go after every other): the list keeps its place among the task's numbers
		_, err = tx.Exec(ctx, `UPDATE task_checklist_items i SET checklist_id = $2, sort = o.n
			FROM unnest($1::uuid[]) WITH ORDINALITY AS o(id, n) WHERE i.id = o.id`, ids, listID)
		return err
	})
}
