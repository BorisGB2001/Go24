package tasks

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// The people picker ui.entity-selector): a search from the first letter of the name, surname,
// position, department or email — any case, «ё» as «е», typed in the wrong keyboard layout too; «Последние» —
// the people the viewer recently worked with on tasks; a department's people. Fired people are never offered,
// only shown when they are already chosen (IDs).

// PersonHit is one person in the picker.
type PersonHit struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Position   string `json:"position"`
	Department string `json:"dept"`
	PhotoKey   string `json:"-"`
	Email      string `json:"-"`
	Active     bool   `json:"-"`
	Guest      bool   `json:"guest,omitempty"` // from outside the company (a collab's guest)
	first      string
	last       string
	recent     int // the place among the recent people (0 — not recent)
}

// PeopleQuery chooses whom the picker lists.
type PeopleQuery struct {
	Q          string
	Department string // only the department's people (Deep — with its sub-departments)
	Deep       bool
	Recent     bool     // the recent people first, then nobody else unless Q is set
	IDs        []string // exactly these people, fired too (to show the chosen ones)
	Messenger  bool     // only people with a messenger account
	Self       bool     // the viewer may be listed
	Collab     string   // only the members of the collab with this key (the viewer's collab; an administrator's — any)
	Limit      int      // 0 — everybody
	Offset     int
}

// RecentPeople is how many people «Последние» shows.
const RecentPeople = 30

// People lists the people of the picker, best matches first.
func (s *Store) People(ctx context.Context, viewerID string, q PeopleQuery) ([]PersonHit, error) {
	where := []string{"u.name <> ''"}
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if len(q.IDs) > 0 {
		where = append(where, "u.id::text = ANY("+arg(q.IDs)+")")
	} else {
		where = append(where, "u.active")
		if !q.Self {
			where = append(where, "u.id::text <> "+arg(viewerID))
		}
	}
	if q.Department != "" {
		if q.Deep {
			where = append(where, `EXISTS (WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = `+arg(q.Department)+`
				UNION ALL SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id)
				SELECT 1 FROM user_departments ud JOIN sub ON sub.id = ud.department_id WHERE ud.user_id = u.id)`)
		} else {
			where = append(where, "EXISTS (SELECT 1 FROM user_departments ud WHERE ud.user_id = u.id AND ud.department_id::text = "+arg(q.Department)+")")
		}
	}
	if q.Collab != "" {
		in := `EXISTS (SELECT 1 FROM projects cp JOIN project_members cm ON cm.project_id = cp.id
			WHERE cp.kind = 'collab' AND cp.key = ` + arg(q.Collab) + ` AND cm.user_id = u.id`
		if !IsAdmin(ctx) {
			in += ` AND EXISTS (SELECT 1 FROM project_members vm WHERE vm.project_id = cp.id AND vm.user_id::text = ` + arg(viewerID) + `)`
		}
		where = append(where, in+")")
	}
	if !IsAdmin(ctx) { // a guest — the people of their collabs; an employee — no guest of someone else's collab
		where = append(where, PeerSQL(arg(viewerID), "u"))
	}
	if q.Messenger {
		where = append(where, "EXISTS (SELECT 1 FROM matrix_users m WHERE m.user_id = u.id)")
	}
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name, u.position, u.email, u.photo_key, u.active, u.guest, u.first_name, u.last_name,
		coalesce((SELECT string_agg(d.name, ', ' ORDER BY d.sort, d.name) FROM user_departments ud
			JOIN departments d ON d.id = ud.department_id WHERE ud.user_id = u.id), '')
		FROM users u WHERE `+strings.Join(where, " AND ")+` ORDER BY u.name, u.email`, args...)
	if err != nil {
		return nil, err
	}
	var all []PersonHit
	for rows.Next() {
		var p PersonHit
		if err := rows.Scan(&p.ID, &p.Name, &p.Position, &p.Email, &p.PhotoKey, &p.Active, &p.Guest, &p.first, &p.last, &p.Department); err != nil {
			rows.Close()
			return nil, err
		}
		if IsGuestCtx(ctx) {
			p.Department = "" // the structure of the company is not a guest's business
		}
		all = append(all, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if q.Recent {
		place, err := s.recentPeople(ctx, viewerID)
		if err != nil {
			return nil, err
		}
		for i := range all {
			all[i].recent = place[all[i].ID]
			if all[i].ID == viewerID {
				all[i].recent = -1 // the viewer comes first
			}
		}
	}
	out := MatchPeople(all, q.Q, q.Recent)
	if q.Department != "" { // the head first, as in the structure
		var head string
		if err := s.db.QueryRow(ctx, `SELECT coalesce(head_id::text, '') FROM departments WHERE id::text = $1`, q.Department).Scan(&head); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		for i, p := range out {
			if p.ID == head && i > 0 {
				copy(out[1:i+1], out[:i])
				out[0] = p
				break
			}
		}
	}
	if q.Offset > 0 {
		if q.Offset >= len(out) {
			return []PersonHit{}, nil
		}
		out = out[q.Offset:]
	}
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// recentPeople: the people of the viewer's tasks of the last half year, the latest first (1, 2, …).
func (s *Store) recentPeople(ctx context.Context, viewerID string) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `WITH mine AS (
			SELECT t.id, t.creator_id, t.responsible_id, t.updated_at FROM tasks t
			WHERE t.updated_at > now() - interval '180 days' AND (t.creator_id = $1 OR t.responsible_id = $1
				OR EXISTS (SELECT 1 FROM task_participants p WHERE p.task_id = t.id AND p.user_id = $1))),
		people AS (
			SELECT creator_id AS uid, updated_at FROM mine
			UNION ALL SELECT responsible_id, updated_at FROM mine
			UNION ALL SELECT p.user_id, m.updated_at FROM mine m JOIN task_participants p ON p.task_id = m.id)
		SELECT uid::text FROM people WHERE uid <> $1 GROUP BY uid ORDER BY max(updated_at) DESC LIMIT $2`, viewerID, RecentPeople)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	place := map[string]int{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		place[id] = len(place) + 1
	}
	return place, rows.Err()
}

// MatchPeople keeps the people every word of q starts a word of (name, surname, position, department, email),
// in the typed layout or the other one. Those whose name matches come first; with recent, the recent people
// lead and, when nothing is typed, are the only ones.
func MatchPeople(all []PersonHit, q string, recent bool) []PersonHit {
	words := strings.Fields(fold(q))
	var variants [][]string
	if len(words) > 0 {
		variants = [][]string{words, strings.Fields(fold(switchLayout(q)))}
	}
	type hit struct {
		p      PersonHit
		byName bool
	}
	var hits []hit
	for _, p := range all {
		if len(variants) == 0 {
			if !recent || p.recent != 0 {
				hits = append(hits, hit{p: p})
			}
			continue
		}
		name := tokens(p.Name + " " + p.first + " " + p.last)
		rest := tokens(p.Position + " " + p.Department + " " + p.Email)
		for _, v := range variants {
			if all := prefixes(v, name); all || prefixes(v, append(name, rest...)) {
				hits = append(hits, hit{p: p, byName: all})
				break
			}
		}
	}
	rank := func(h hit) int {
		if h.p.recent != 0 {
			return h.p.recent
		}
		return RecentPeople + 1
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].byName != hits[j].byName {
			return hits[i].byName
		}
		return recent && rank(hits[i]) < rank(hits[j])
	})
	out := make([]PersonHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.p)
	}
	return out
}

// prefixes: every word starts one of the tokens.
func prefixes(words, toks []string) bool {
	for _, w := range words {
		found := false
		for _, t := range toks {
			if strings.HasPrefix(t, w) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func tokens(s string) []string {
	return strings.FieldsFunc(fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func fold(s string) string { return strings.ReplaceAll(strings.ToLower(s), "ё", "е") }

// switchLayout retypes the text in the other keyboard layout (ЙЦУКЕН ↔ QWERTY): «ghbdtn» → «привет».
func switchLayout(s string) string {
	const en, ru = "`qwertyuiop[]asdfghjkl;'zxcvbnm,.", "ёйцукенгшщзхъфывапролджэячсмитьбю"
	enR, ruR := []rune(en), []rune(ru)
	m := make(map[rune]rune, 2*len(enR))
	for i := range enR {
		m[enR[i]], m[ruR[i]] = ruR[i], enR[i]
	}
	out := []rune(strings.ToLower(s))
	for i, r := range out {
		if x, ok := m[r]; ok {
			out[i] = x
		}
	}
	return string(out)
}
