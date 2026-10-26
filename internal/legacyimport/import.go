// Package legacy imports tasks from a portal on-premise export into go24, anonymizing people on the way.
//
// The export is a directory of TSV files produced by docs/import-legacy.md (text columns are base64):
//
//	users.tsv     ID, NAME, LAST_NAME, SECOND_NAME, PERSONAL_GENDER, EMAIL, LOGIN
//	tasks.tsv     ID, TITLE, DESCRIPTION, STATUS, PRIORITY, CREATED_BY, RESPONSIBLE_ID, PARENT_ID, TASK_CONTROL,
//	              DEADLINE, START_DATE_PLAN, END_DATE_PLAN, CREATED_DATE, CHANGED_DATE, CLOSED_DATE, CLOSED_BY
//	members.tsv   TASK_ID, USER_ID, TYPE (O creator, R responsible, A accomplice, U auditor)
//	comments.tsv  ID, TASK_ID, AUTHOR_ID, POST_DATE, POST_MESSAGE
//
// Imported tasks keep their Legacy number: Legacy task 123 becomes <PROJECT>-123.
package legacyimport

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/anon"
)

type Options struct {
	Dir         string
	ProjectKey  string
	ProjectName string
	OwnerID     string         // go24 user who owns the import project
	Location    *time.Location // time zone of Legacy DATETIME values (Europe/Moscow by default)
	Anon        *anon.Anonymizer
}

type Stats struct {
	Users, Tasks, Participants, Comments, Parents int
}

func (s Stats) String() string {
	return fmt.Sprintf("пользователей %d, задач %d, участников %d, связей родитель-подзадача %d, комментариев %d",
		s.Users, s.Tasks, s.Participants, s.Parents, s.Comments)
}

// Import runs the whole import in one transaction: either everything is imported or nothing.
func Import(ctx context.Context, db *pgxpool.Pool, o Options) (Stats, error) {
	var st Stats
	if o.Location == nil {
		o.Location = time.UTC
	}
	users, err := readTSV(filepath.Join(o.Dir, "users.tsv"), 7)
	if err != nil {
		return st, err
	}
	tasks, err := readTSV(filepath.Join(o.Dir, "tasks.tsv"), 16)
	if err != nil {
		return st, err
	}
	members, err := readTSV(filepath.Join(o.Dir, "members.tsv"), 3)
	if err != nil {
		return st, err
	}
	comments, err := readTSV(filepath.Join(o.Dir, "comments.tsv"), 5)
	if err != nil {
		return st, err
	}

	// People first, so names inside texts can be replaced.
	for _, u := range users {
		o.Anon.AddPerson(anon.Person{ID: atoi(u[0]), First: b64(u[1]), Last: b64(u[2]), Middle: b64(u[3]),
			Gender: u[4], Email: b64(u[5]), Login: b64(u[6])})
	}

	err = pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		userIDs := map[int]string{}
		for _, u := range users {
			p, _ := o.Anon.Person(atoi(u[0]))
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO users (oidc_sub, email, name) VALUES ($1, $2, $3)
				ON CONFLICT (oidc_sub) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name
				RETURNING id`, fmt.Sprintf("legacy:%d", p.ID), p.FakeEmail, p.FullName()).Scan(&id); err != nil {
				return fmt.Errorf("user %d: %w", p.ID, err)
			}
			userIDs[p.ID] = id
			st.Users++
		}
		user := func(legacyID int) (string, error) {
			if id, ok := userIDs[legacyID]; ok {
				return id, nil
			}
			return "", fmt.Errorf("legacy user %d is not in users.tsv", legacyID)
		}
		// Comments without an author (AUTHOR_ID NULL: e-mail replies, guests) go to one anonymous placeholder;
		// the original sender name is not imported.
		external := func() (string, error) {
			if id, ok := userIDs[0]; ok {
				return id, nil
			}
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO users (oidc_sub, email, name) VALUES ('legacy:0', '', 'Внешний автор')
				ON CONFLICT (oidc_sub) DO UPDATE SET name = EXCLUDED.name RETURNING id`).Scan(&id)
			userIDs[0] = id
			return id, err
		}

		projectID, err := ensureProject(ctx, tx, o)
		if err != nil {
			return err
		}
		for _, id := range userIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member')
				ON CONFLICT DO NOTHING`, projectID, id); err != nil {
				return err
			}
		}

		taskIDs := map[int]string{}
		maxNum := 0
		for _, t := range tasks {
			num := atoi(t[0])
			creator, err := user(atoi(t[5]))
			if err != nil {
				return err
			}
			resp, err := user(atoi(t[6]))
			if err != nil {
				return err
			}
			status := MapStatus(atoi(t[3]))
			var closedBy *string
			if cb := atoi(t[15]); cb > 0 && status == "completed" {
				if id, err := user(cb); err == nil {
					closedBy = &id
				}
			}
			created := o.parseTime(t[12])
			changed := o.parseTime(t[13])
			if changed == nil {
				changed = created
			}
			var closedAt *time.Time
			if status == "completed" {
				closedAt = o.parseTime(t[14])
			}
			var id string
			if err := tx.QueryRow(ctx, `
				INSERT INTO tasks (project_id, number, title, description, status, priority, creator_id, responsible_id,
				                   requires_control, deadline, start_plan, end_plan, closed_at, closed_by, legacy_id,
				                   created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::timestamptz::date, $12::timestamptz::date, $13, $14, $15, $16, $17)
				ON CONFLICT (legacy_id) DO UPDATE SET title = EXCLUDED.title, description = EXCLUDED.description,
				    status = EXCLUDED.status, priority = EXCLUDED.priority, creator_id = EXCLUDED.creator_id,
				    responsible_id = EXCLUDED.responsible_id, requires_control = EXCLUDED.requires_control,
				    deadline = EXCLUDED.deadline, closed_at = EXCLUDED.closed_at, closed_by = EXCLUDED.closed_by,
				    updated_at = EXCLUDED.updated_at
				RETURNING id`,
				projectID, num, clip(cleanText(o.Anon.Text(b64(t[1]))), 500), CleanBBCode(o.Anon.Text(b64(t[2])), o.Anon),
				status, clampPriority(atoi(t[4])), creator, resp, t[8] == "Y",
				o.parseTime(t[9]), o.parseTime(t[10]), o.parseTime(t[11]), closedAt, closedBy, num, created, changed,
			).Scan(&id); err != nil {
				return fmt.Errorf("task %d: %w", num, err)
			}
			taskIDs[num] = id
			maxNum = max(maxNum, num)
			st.Tasks++
		}
		if _, err := tx.Exec(ctx, `UPDATE projects SET task_seq = GREATEST(task_seq, $2) WHERE id = $1`, projectID, maxNum); err != nil {
			return err
		}

		for _, t := range tasks {
			parent := atoi(t[7])
			if parent == 0 {
				continue
			}
			if pid, ok := taskIDs[parent]; ok {
				if _, err := tx.Exec(ctx, `UPDATE tasks SET parent_id = $2 WHERE id = $1`, taskIDs[atoi(t[0])], pid); err != nil {
					return err
				}
				st.Parents++
			}
		}

		for _, m := range members {
			role := map[string]string{"A": "accomplice", "U": "auditor"}[m[2]]
			tid, ok := taskIDs[atoi(m[0])]
			if role == "" || !ok {
				continue // O/R are stored on the task itself
			}
			uid, err := user(atoi(m[1]))
			if err != nil {
				return err
			}
			tag, err := tx.Exec(ctx, `INSERT INTO task_participants (task_id, user_id, role) VALUES ($1, $2, $3)
				ON CONFLICT DO NOTHING`, tid, uid, role)
			if err != nil {
				return err
			}
			st.Participants += int(tag.RowsAffected())
		}

		for _, c := range comments {
			tid, ok := taskIDs[atoi(c[1])]
			if !ok {
				continue
			}
			var author string
			if aid := atoi(c[2]); aid > 0 {
				author, err = user(aid)
			} else {
				author, err = external()
			}
			if err != nil {
				return err
			}
			// Anonymize first, then expand [USER=id] into pseudonyms, so pseudonyms are never re-replaced.
			body := CleanBBCode(o.Anon.Text(b64(c[4])), o.Anon)
			if body == "" {
				continue
			}
			at := o.parseTime(c[3])
			if _, err := tx.Exec(ctx, `
				INSERT INTO task_comments (task_id, author_id, body, created_at, updated_at, legacy_id) VALUES ($1, $2, $3, $4, $4, $5)
				ON CONFLICT (legacy_id) DO UPDATE SET body = EXCLUDED.body, author_id = EXCLUDED.author_id`,
				tid, author, clip(body, 20000), at, atoi(c[0])); err != nil {
				return fmt.Errorf("comment %s: %w", c[0], err)
			}
			st.Comments++
		}
		return nil
	})
	return st, err
}

// WriteAuthentikBlueprint writes a blueprint with one authentik_core.user per imported person,
// taken from the go24 database (pseudonyms only, no export files needed). JSON strings are valid YAML.
func WriteAuthentikBlueprint(ctx context.Context, db *pgxpool.Pool, path string) (int, error) {
	rows, err := db.Query(ctx, `SELECT substring(oidc_sub from 8)::int, name, email FROM users
		WHERE oidc_sub LIKE 'legacy:%' AND oidc_sub <> 'legacy:0' ORDER BY 1`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	var b strings.Builder
	b.WriteString("# Generated by go24-import-legacy: imported the portal users (pseudonyms only). Do not edit by hand.\n")
	b.WriteString("version: 1\nmetadata:\n  name: go24 - imported the portal users\nentries:\n")
	n := 0
	for rows.Next() {
		var id int
		var name, email string
		if err := rows.Scan(&id, &name, &email); err != nil {
			return n, err
		}
		fmt.Fprintf(&b, "  - model: authentik_core.user\n    identifiers:\n      username: %s\n", q(fmt.Sprintf("portal-user%d", id)))
		fmt.Fprintf(&b, "    attrs:\n      name: %s\n      email: %s\n      path: %s\n      is_active: true\n", q(name), q(email), q("go24/portal"))
		fmt.Fprintf(&b, "      attributes:\n        go24_source: portal\n        legacy_id: %d\n", id)
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return n, err
	}
	return n, os.WriteFile(path, []byte(b.String()), 0o644)
}

func ensureProject(ctx context.Context, tx pgx.Tx, o Options) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE key = $1`, o.ProjectKey).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	// the portal tasks without a group were visible only to their participants — keep it that way.
	if err := tx.QueryRow(ctx, `INSERT INTO projects (key, name, description, owner_id, visibility) VALUES ($1, $2, $3, $4, 'participants') RETURNING id`,
		o.ProjectKey, o.ProjectName, "Задачи, перенесённые из корпоративного портала (люди обезличены)", o.OwnerID).Scan(&id); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'owner')`, id, o.OwnerID)
	return id, err
}

// MapStatus: Legacy 1 new, 2 pending, 3 in progress, 4 supposedly completed, 5 completed, 6 deferred, 7 declined.
func MapStatus(s int) string {
	switch s {
	case 3:
		return "in_progress"
	case 4:
		return "awaiting_control"
	case 5:
		return "completed"
	case 6, 7:
		return "deferred"
	}
	return "pending"
}

func clampPriority(p int) int {
	if p < 0 || p > 2 {
		return 1
	}
	return p
}

var (
	userTagRe = regexp.MustCompile(`(?is)\[USER=(\d+)\](.*?)\[/USER\]`)
	urlTagRe  = regexp.MustCompile(`(?is)\[URL=([^\]]+)\](.*?)\[/URL\]`)
	diskRe    = regexp.MustCompile(`(?i)\[DISK FILE ID=[^\]]*\]`)
	listItem  = regexp.MustCompile(`\[\*\]`)
	anyTagRe  = regexp.MustCompile(`(?i)\[/?(B|I|U|S|URL|QUOTE|CODE|COLOR|SIZE|FONT|LEFT|RIGHT|CENTER|JUSTIFY|LIST|IMG|TABLE|TR|TD|TH|P|HR|USER|VIDEO|SPOILER)(=[^\]]*)?\]`)
)

// CleanBBCode turns Legacy BBCode into plain text; [USER=17] mentions become the person's pseudonym.
func CleanBBCode(s string, a *anon.Anonymizer) string {
	s = userTagRe.ReplaceAllStringFunc(s, func(m string) string {
		id := atoi(userTagRe.FindStringSubmatch(m)[1])
		if p, ok := a.Person(id); ok {
			return "@" + p.FullName()
		}
		return "@пользователь"
	})
	s = urlTagRe.ReplaceAllString(s, "$2 ($1)")
	s = diskRe.ReplaceAllString(s, "[вложение]")
	s = listItem.ReplaceAllString(s, "• ")
	s = anyTagRe.ReplaceAllString(s, "")
	return cleanText(s)
}

func cleanText(s string) string {
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSpace(s)
}

func (o Options) parseTime(v string) *time.Time {
	if v == "" || v == "NULL" || strings.HasPrefix(v, "0000") {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", v, o.Location)
	if err != nil {
		return nil
	}
	return &t
}

func readTSV(path string, cols int) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for line := 1; sc.Scan(); line++ {
		if sc.Text() == "" {
			continue
		}
		rec := strings.Split(sc.Text(), "\t")
		if len(rec) != cols {
			return nil, fmt.Errorf("%s:%d: %d columns, want %d", filepath.Base(path), line, len(rec), cols)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

func b64(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
