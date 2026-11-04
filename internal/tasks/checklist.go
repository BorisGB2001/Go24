package tasks

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Checklists of a task: several named lists with their own progress; an item can be renamed,
// dragged (to another list too), marked important, shifted right under the item above it and back (nested items go
// with their parent); «@Name» in an item makes that person an accomplice and a member of the item.
// Participants who do the task and project managers edit them (TaskCard.CanWork).

type ChecklistItem struct {
	ID        string
	Title     string
	Done      bool
	Important bool
	ListID    string
	Level     int    // the depth in the list: 0 — top, n+1 — under the nearest item above of level n
	Members   []User // people mentioned in the item
	Parent    string // the item it is nested in ("" — top)
	Sub       int    // its own nested items (one level down)
	SubDone   int
}

// MaxChecklistLevel is the deepest nesting of an item.
const MaxChecklistLevel = 4

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
			out = append(out, strings.Repeat("  ", it.Level)+it.Title)
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
	rows, err = q.Query(ctx, `SELECT id, title, done, important, checklist_id, level FROM task_checklist_items
		WHERE task_id = $1 ORDER BY sort, created_at`, taskID)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ChecklistItem, error) {
		var it ChecklistItem
		return it, row.Scan(&it.ID, &it.Title, &it.Done, &it.Important, &it.ListID, &it.Level)
	})
	if err != nil {
		return nil, err
	}
	rows, err = q.Query(ctx, `SELECT m.item_id::text, u.id::text, u.name, u.guest FROM task_checklist_item_members m
		JOIN task_checklist_items i ON i.id = m.item_id JOIN users u ON u.id = m.user_id
		WHERE i.task_id = $1 ORDER BY u.name`, taskID)
	if err != nil {
		return nil, err
	}
	members := map[string][]User{}
	var item string
	var u User
	if _, err = pgx.ForEachRow(rows, []any{&item, &u.ID, &u.Name, &u.Guest}, func() error {
		members[item] = append(members[item], u)
		return nil
	}); err != nil {
		return nil, err
	}
	at := map[string]int{}
	for i, l := range lists {
		at[l.ID] = i
	}
	for _, it := range items {
		if i, ok := at[it.ListID]; ok {
			it.Members = members[it.ID]
			lists[i].Items = append(lists[i].Items, it)
		}
	}
	for _, l := range lists {
		nestItems(l.Items)
	}
	return lists, nil
}

// nestItems fills each item's parent and counts of its own nested items.
func nestItems(items []ChecklistItem) {
	var path []string // the parents of the current level
	for i := range items {
		lvl := min(items[i].Level, len(path))
		path = path[:lvl]
		if lvl > 0 {
			items[i].Parent = path[lvl-1]
		}
		path = append(path, items[i].ID)
	}
	for i := range items {
		for _, n := range items[i+1:] {
			if n.Level <= items[i].Level {
				break
			}
			if n.Level == items[i].Level+1 {
				items[i].Sub++
				if n.Done {
					items[i].SubDone++
				}
			}
		}
	}
}

// ChecklistLine reads a line of the line form or of a pasted list: an indent of a tab or two spaces per level, as a
// nested list is pasted, and a list marker («-», «*», «•», «–») before the title are not part of the item.
func ChecklistLine(line string) (title string, level int) {
	line = strings.TrimRight(line, " \t\r")
	spaces := 0
	for len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		if line[0] == '\t' {
			spaces += 2
		} else {
			spaces++
		}
		line = line[1:]
	}
	for _, m := range []string{"- ", "* ", "• ", "– "} {
		if rest, ok := strings.CutPrefix(line, m); ok {
			line = rest
			break
		}
	}
	return strings.TrimSpace(line), min(spaces/2, MaxChecklistLevel)
}

// addedItem is an item insertChecklistLines added.
type addedItem struct{ ID, Title string }

// insertChecklistLines adds items from lines to the task: with lists, "# Name" starts a new list (an empty name
// is allowed); other lines go to the current one — listID, or with "" the task's first list (made when there is none).
// An indented line is nested, at most one level deeper than the item above it.
func insertChecklistLines(ctx context.Context, tx pgx.Tx, actorID, taskID, listID string, lines []string, lists bool) (added []addedItem, err error) {
	prev := -1 // the level of the list's last item; -1 — the list is empty
	if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT level FROM task_checklist_items WHERE task_id = $1
			AND checklist_id = coalesce(nullif($2, '')::uuid,
				(SELECT id FROM task_checklists WHERE task_id = $1 ORDER BY sort, created_at LIMIT 1))
			ORDER BY sort DESC LIMIT 1), -1)`, taskID, listID).Scan(&prev); err != nil {
		return nil, err
	}
	for _, line := range lines {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), ListPrefix); ok && lists {
			if listID, err = insertChecklist(ctx, tx, taskID, strings.TrimSpace(name)); err != nil {
				return nil, err
			}
			prev = -1
			continue
		}
		title, level := ChecklistLine(line)
		if title == "" {
			continue
		}
		level = min(level, prev+1)
		var id string
		if err = tx.QueryRow(ctx, `INSERT INTO task_checklist_items (task_id, checklist_id, title, created_by, level, sort)
			VALUES ($1, nullif($2, '')::uuid, $3, $4, $5, (SELECT coalesce(max(sort), 0) + 1 FROM task_checklist_items WHERE task_id = $1))
			RETURNING id::text`, taskID, listID, title, actorID, level).Scan(&id); err != nil {
			return nil, err
		}
		prev = level
		added = append(added, addedItem{id, title})
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
		for _, it := range added {
			if err := addHistory(ctx, tx, taskID, userID, "checklist", "", it.Title); err != nil {
				return err
			}
			if err := checklistMentions(ctx, tx, userID, taskID, it.ID, it.Title); err != nil {
				return err
			}
		}
		return nil
	})
}

// checklistMentions makes people mentioned in an item its members and accomplices of the task
// with a person added to a checklist item; the creator, the responsible person and those already in the task stay
// as they are. Members no longer mentioned leave the item (not the task).
func checklistMentions(ctx context.Context, tx pgx.Tx, userID, taskID, itemID, text string) error {
	found, err := mentioned(ctx, tx, text)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM task_checklist_item_members WHERE item_id = $1 AND user_id <> ALL($2::uuid[])`,
		itemID, mentionIDs(found)); err != nil {
		return err
	}
	if len(found) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO task_checklist_item_members (item_id, user_id)
		SELECT $1, u FROM unnest($2::uuid[]) u ON CONFLICT DO NOTHING`, itemID, mentionIDs(found)); err != nil {
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

// AddChecklistWith adds a list with the items at once (CoPilot's checklist) and returns its id.
func (s *Store) AddChecklistWith(ctx context.Context, userID, taskID, title string, items []string) (id string, err error) {
	err = s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		if id, err = insertChecklist(ctx, tx, taskID, strings.TrimSpace(title)); err != nil {
			return err
		}
		added, err := insertChecklistLines(ctx, tx, userID, taskID, id, items, false)
		if err != nil {
			return err
		}
		if len(added) == 0 {
			return invalid("err.checklist_empty")
		}
		for _, it := range added {
			if err := addHistory(ctx, tx, taskID, userID, "checklist", "", it.Title); err != nil {
				return err
			}
		}
		return nil
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

// ckRow is an item of a list as the nesting sees it.
type ckRow struct {
	ID    string
	Level int
	Done  bool
	Title string
}

// itemList is the list the item is in: its id, the items in order and the item's place among them.
func itemList(ctx context.Context, tx pgx.Tx, taskID, itemID string) (list string, items []ckRow, at int, err error) {
	err = tx.QueryRow(ctx, `SELECT checklist_id::text FROM task_checklist_items WHERE id::text = $1 AND task_id = $2`,
		itemID, taskID).Scan(&list)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, 0, ErrNotFound
	}
	if err != nil {
		return "", nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id::text, level, done, title FROM task_checklist_items WHERE checklist_id = $1
		ORDER BY sort, created_at`, list)
	if err != nil {
		return "", nil, 0, err
	}
	items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ckRow])
	at = slices.IndexFunc(items, func(r ckRow) bool { return r.ID == itemID })
	if err == nil && at < 0 {
		err = ErrNotFound
	}
	return list, items, at, err
}

// blockEnd is where the item at «at» ends with its nested items.
func blockEnd(items []ckRow, at int) int {
	end := at + 1
	for end < len(items) && items[end].Level > items[at].Level {
		end++
	}
	return end
}

func ckIDs(items []ckRow) []string {
	ids := make([]string, len(items))
	for i, r := range items {
		ids[i] = r.ID
	}
	return ids
}

// ChangeChecklistItem flips the item's done flag, or deletes the item when remove is true — with its nested items,
// : they are marked (or opened) with it; an opened item opens the items it is nested in.
func (s *Store) ChangeChecklistItem(ctx context.Context, userID, taskID, itemID string, remove bool) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		_, items, at, err := itemList(ctx, tx, taskID, itemID)
		if err != nil {
			return err
		}
		blk := items[at:blockEnd(items, at)]
		if remove {
			if _, err := tx.Exec(ctx, `DELETE FROM task_checklist_items WHERE id = ANY($1::uuid[])`, ckIDs(blk)); err != nil {
				return err
			}
			for _, r := range blk {
				if err := addHistory(ctx, tx, taskID, userID, "checklist", r.Title, ""); err != nil {
					return err
				}
			}
			return nil
		}
		done := !items[at].Done
		change := blk
		if !done { // the items above it that hold it
			lvl := items[at].Level
			for i := at - 1; i >= 0 && lvl > 0; i-- {
				if items[i].Level < lvl {
					lvl = items[i].Level
					change = append(change, items[i])
				}
			}
		}
		var flipped []string
		for _, r := range change {
			if r.Done != done {
				flipped = append(flipped, r.ID)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE task_checklist_items
			SET done = $2, done_by = CASE WHEN $2 THEN $3::uuid END, done_at = CASE WHEN $2 THEN now() END
			WHERE id = ANY($1::uuid[])`, flipped, done, userID); err != nil {
			return err
		}
		field := "checklist_undone"
		if done {
			field = "checklist_done"
		}
		for _, r := range change {
			if r.Done != done {
				if err := addHistory(ctx, tx, taskID, userID, field, "", r.Title); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// IndentChecklistItem shifts the item with its nested items one level right (under the item above it) or left
// («Сдвинуть вправо / влево», Tab and Shift+Tab).
func (s *Store) IndentChecklistItem(ctx context.Context, userID, taskID, itemID string, right bool) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		_, items, at, err := itemList(ctx, tx, taskID, itemID)
		if err != nil {
			return err
		}
		blk := items[at:blockEnd(items, at)]
		delta := -1
		if right {
			delta = 1
			deepest := 0
			for _, r := range blk {
				deepest = max(deepest, r.Level)
			}
			if at == 0 || items[at-1].Level < items[at].Level || deepest >= MaxChecklistLevel {
				return nil // nothing to go under, or as deep as it goes
			}
		} else if items[at].Level == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE task_checklist_items SET level = level + $2 WHERE id = ANY($1::uuid[])`, ckIDs(blk), delta)
		return err
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
		return checklistMentions(ctx, tx, userID, taskID, itemID, title)
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

// ChecklistItemFiles lets the user attach files to the item of the task or take them out of it — those who edit its
// checklists — and writes the history: «Файл в пункте чек-листа», the item's title and
// the file's name (added) or the other way round (removed). fn does the attaching or the taking out.
func (s *Store) ChecklistItemFiles(ctx context.Context, userID, taskID, itemID string, added bool, fn func() (name string, err error)) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		var title string
		err := tx.QueryRow(ctx, `SELECT title FROM task_checklist_items WHERE id::text = $1 AND task_id = $2`, itemID, taskID).Scan(&title)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		name, err := fn()
		if err != nil {
			return err
		}
		v := title + ": " + name
		if added {
			return addHistory(ctx, tx, taskID, userID, "checklist_file", "", v)
		}
		return addHistory(ctx, tx, taskID, userID, "checklist_file", v, "")
	})
}

// MoveChecklistItem puts an item with its nested items into a list (listID "" — its own) before another item
// (beforeID "" — at the end); the item goes at most one level deeper than the item it lands after.
func (s *Store) MoveChecklistItem(ctx context.Context, userID, taskID, itemID, listID, beforeID string) error {
	return s.checklistTx(ctx, userID, taskID, func(tx pgx.Tx) error {
		own, items, at, err := itemList(ctx, tx, taskID, itemID)
		if err != nil {
			return err
		}
		blk := slices.Clone(items[at:blockEnd(items, at)])
		if listID == "" {
			listID = own
		} else if err := listOfTask(ctx, tx, taskID, listID); err != nil {
			return err
		}
		// the list's order without the block, the block put in its place, then all numbered anew
		rows, err := tx.Query(ctx, `SELECT id::text, level, done, title FROM task_checklist_items
			WHERE checklist_id::text = $1 AND id <> ALL($2::uuid[]) ORDER BY sort, created_at`, listID, ckIDs(blk))
		if err != nil {
			return err
		}
		rest, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ckRow])
		if err != nil {
			return err
		}
		to := len(rest)
		for i, r := range rest {
			if r.ID == beforeID {
				to = i
			}
		}
		top := 0
		if to > 0 {
			top = min(blk[0].Level, rest[to-1].Level+1)
		}
		delta := top - blk[0].Level
		for i := range blk {
			blk[i].Level += delta
		}
		all := slices.Concat(rest[:to], blk, rest[to:])
		levels := make([]int, len(all))
		for i, r := range all {
			levels[i] = r.Level
		}
		// sort is task-wide (new items go after every other): the list keeps its place among the task's numbers
		_, err = tx.Exec(ctx, `UPDATE task_checklist_items i SET checklist_id = $2, sort = o.n, level = o.level
			FROM unnest($1::uuid[], $3::int[]) WITH ORDINALITY AS o(id, level, n) WHERE i.id = o.id`, ckIDs(all), listID, levels)
		return err
	})
}
