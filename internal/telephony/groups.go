package telephony

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Группы очередей»: a call waits in a queue for the employees of a group (Asterisk's app_queue) — they
// ring all at once, in turn or evenly (round robin, remembering who was rung last); the client hears the group's music
// (or the ringing) for Wait seconds at most, then the call goes by the line's «nobody answered». A line rings its group
// instead of its employees; a voice menu's item connects with a group.

// Group is a queue group.
type Group struct {
	ID, Name   string
	Users      []string
	Strategy   string // all | linear | evenly
	Ring, Wait int    // seconds one employee rings; the client waits at most
	Music      []byte // on saving: new music (WAV); nil — the old one stays
	DropMusic  bool   // on saving: no music any more
	MusicSum   string // the music's checksum ("" — none: the client hears the ringing)
}

// Strategies of a group, and app_queue's names of them.
var groupStrategies = map[string]string{"all": "ringall", "linear": "linear", "evenly": "rrmemory"}

// GroupKey names the group's queue, its context and its music class in Asterisk.
func GroupKey(id string) string { return "go24-q-" + strings.ReplaceAll(id, "-", "")[:12] }

// Groups are the queue groups by name.
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, users::text[], strategy, ring, wait, coalesce(md5(music), '')
		FROM telephony_groups ORDER BY lower(name), created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Group, error) {
		var g Group
		err := r.Scan(&g.ID, &g.Name, &g.Users, &g.Strategy, &g.Ring, &g.Wait, &g.MusicSum)
		return g, err
	})
}

// SaveGroup adds (ID "") or changes a group.
func (s *Store) SaveGroup(ctx context.Context, userID string, g Group) (string, error) {
	g.Name = strings.TrimSpace(g.Name)
	if g.Name == "" || len([]rune(g.Name)) > 100 || strings.ContainsAny(g.Name, "\r\n") {
		return "", i18n.E("err.group_name")
	}
	var users []string
	for _, u := range g.Users {
		if u != "" && !slices.Contains(users, u) {
			users = append(users, u)
		}
	}
	if len(users) == 0 {
		return "", i18n.E("err.group_users")
	}
	if _, ok := groupStrategies[g.Strategy]; !ok {
		g.Strategy = "all"
	}
	if g.Ring < 5 || g.Ring > 120 || g.Wait < 10 || g.Wait > 1800 {
		return "", i18n.E("err.group_timing")
	}
	if g.Music != nil {
		var err error
		if g.Music, err = Greeting(g.Music); err != nil {
			return "", err
		}
	}
	if g.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO telephony_groups (name, users, strategy, ring, wait, music, created_by)
			VALUES ($1, $2::uuid[], $3, $4, $5, $6, $7) RETURNING id::text`, g.Name, users, g.Strategy, g.Ring, g.Wait, g.Music, userID).Scan(&g.ID)
		return g.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE telephony_groups SET name = $2, users = $3::uuid[], strategy = $4, ring = $5, wait = $6,
		music = CASE WHEN $8 THEN NULL ELSE coalesce($7::bytea, music) END WHERE id::text = $1`,
		g.ID, g.Name, users, g.Strategy, g.Ring, g.Wait, g.Music, g.DropMusic)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return g.ID, err
}

// DeleteGroup removes a group: its lines ring their own employees again, the menus' items connecting with it are
// dropped (the «nothing pressed» of a menu hangs up).
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM telephony_groups WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE telephony_menus SET
		items = coalesce((SELECT jsonb_agg(i.v) FROM jsonb_array_elements(items) AS i(v) WHERE i.v->>'group' IS DISTINCT FROM $1), '[]'),
		timeout = CASE WHEN timeout->>'group' = $1 THEN '{"act": "hangup"}' ELSE timeout END
		WHERE items @> jsonb_build_array(jsonb_build_object('group', $1::text)) OR timeout->>'group' = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GroupMusic is the music of a group (a WAV for Asterisk); nil — none.
func (s *Store) GroupMusic(ctx context.Context, id string) ([]byte, error) {
	var g []byte
	err := s.db.QueryRow(ctx, `SELECT music FROM telephony_groups WHERE id::text = $1`, id).Scan(&g)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

// RenderGroups is the groups' part of Asterisk's configuration: the queues (go24/queues.conf), their music
// (go24/musiconhold.conf) and a context each. A call goes to go24-q-K,s,1 with GO24_BACK (where its «nobody answered»
// starts) and GO24_RATEP (the line's invitation to rate, "" — none) set. The employee who answers (go24-q-who; Queue
// gives its gosub no arguments — the call's channel comes inherited, GO24_QCALLER) is told to the call's channel: ${SHARED(GO24_QBY)} and the time ${SHARED(GO24_QANS)} (go24-cdr reads them). numbers are
// the employees' inner numbers by their ids.
func RenderGroups(groups []Group, numbers map[string]string) (queues, moh, dialplan string) {
	var q, m, d strings.Builder
	q.WriteString("; go24 writes this file (Settings → Telephony → Queue groups): do not edit it by hand\n")
	m.WriteString("; go24 writes this file (Settings → Telephony → Queue groups): do not edit it by hand\n")
	for _, g := range groups {
		k := GroupKey(g.ID)
		fmt.Fprintf(&q, "\n; %s\n[%s]\nstrategy = %s\ntimeout = %d\nretry = 3\nwrapuptime = 0\njoinempty = yes\nleavewhenempty = no\n"+
			"announce-frequency = 0\nannounce-holdtime = no\nannounce-position = no\nperiodic-announce-frequency = 0\n",
			callerName(g.Name), k, groupStrategies[g.Strategy], g.Ring)
		opt := "cr" // c: on after the employee hangs up (the rating); r: no music — the client hears the ringing
		if g.MusicSum != "" {
			opt = "c"
			fmt.Fprintf(&q, "musicclass = %s\n", k)
			fmt.Fprintf(&m, "\n[%s]\nmode = playlist\nentry = %s\n", k, GreetingFile(g.MusicSum))
		}
		first := ""
		for _, u := range g.Users {
			if n := numbers[u]; n != "" {
				fmt.Fprintf(&q, "member => Local/%[1]s@go24-ring/n,0,%[1]s\n", n)
				if first == "" {
					first = n
				}
			}
		}
		fmt.Fprintf(&d, "\n; %s\n[%s]\nexten => s,1,Set(GO24_BY=%s)\n same => n,Set(__GO24_QCALLER=${CHANNEL})\n same => n,Queue(%s,%s,,,%d,,go24-q-who)\n"+
			" same => n,GotoIf($[\"${SHARED(GO24_QBY)}\" != \"\"]?ok)\n same => n,Set(DIALSTATUS=NOANSWER)\n same => n,Goto(${GO24_BACK})\n"+
			" same => n(ok),Set(DIALSTATUS=ANSWER)\n same => n,Gosub(go24-rate,s,1(${GO24_RATEP}))\n same => n,Hangup()\n",
			callerName(g.Name), k, first, k, opt, g.Wait)
	}
	return q.String(), m.String(), d.String()
}
