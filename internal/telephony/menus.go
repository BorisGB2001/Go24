package telephony

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Голосовое меню» (IVR): an external line's call (in its working hours) hears the menu's greeting and
// presses a key; the call goes by the item of the key — rings an employee or a group of them (all at once or by
// turns), is forwarded to a number, goes to the voicemail, to another menu, hears the menu again or is hung up.
// Nothing pressed after the menu was played Repeats times (or a key no item has) — the call goes by Timeout. Direct:
// the client may dial an employee's inner number right away. Nobody answered a ring of the menu — the line's own
// «nobody answered» handling.

// Menu is a voice menu.
type Menu struct {
	ID, Name     string
	Wait         int // seconds to wait for a key after the greeting
	Repeats      int // how many times the menu is played
	Direct       bool
	Items        []MenuItem
	Timeout      MenuItem // nothing pressed: an item without a digit
	Greeting     []byte   // on saving: a new greeting (WAV); nil — the old one stays
	DropGreeting bool     // on saving: no greeting any more
	GreetingSum  string   // the greeting's checksum ("" — none)
}

// MenuItem is what pressing a key does.
type MenuItem struct {
	Digit string   `json:"digit,omitempty"` // 0–9, *, #
	Act   string   `json:"act"`             // one of the Menu* actions
	Users []string `json:"users,omitempty"` // MenuRing: the employees
	Mode  string   `json:"mode,omitempty"`  // MenuRing: all | queue
	To    string   `json:"to,omitempty"`    // MenuForward: the number
	Menu  string   `json:"menu,omitempty"`  // MenuGoto: the menu's id
	Group string   `json:"group,omitempty"` // MenuGroup: the queue group's id
}

// Actions of a menu's item (with ActForward, ActVoicemail, ActHangup).
const (
	MenuRing   = "ring"
	MenuGoto   = "menu"
	MenuRepeat = "repeat"
	MenuGroup  = "group" // the call waits in a queue group
)

// MenuDigits are the keys a menu's item may have, in their order.
const MenuDigits = "1234567890*#"

// MenuKey names a menu's context in Asterisk.
func MenuKey(id string) string { return "go24-ivr-" + strings.ReplaceAll(id, "-", "")[:12] }

// Menus are the voice menus by name.
func (s *Store) Menus(ctx context.Context) ([]Menu, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, wait, repeats, direct, items, timeout, coalesce(md5(greeting), '')
		FROM telephony_menus ORDER BY lower(name), created_at, id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Menu, error) {
		var m Menu
		var items, timeout []byte
		if err := r.Scan(&m.ID, &m.Name, &m.Wait, &m.Repeats, &m.Direct, &items, &timeout, &m.GreetingSum); err != nil {
			return m, err
		}
		if err := json.Unmarshal(items, &m.Items); err != nil {
			return m, err
		}
		return m, json.Unmarshal(timeout, &m.Timeout)
	})
}

// SaveMenu adds (ID "") or changes a voice menu; its id.
func (s *Store) SaveMenu(ctx context.Context, userID string, m Menu) (string, error) {
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" || len([]rune(m.Name)) > 100 || strings.ContainsAny(m.Name, "\r\n") {
		return "", i18n.E("err.menu_name")
	}
	if m.Wait < 1 || m.Wait > 30 || m.Repeats < 1 || m.Repeats > 5 {
		return "", i18n.E("err.menu_timing")
	}
	var ids, groups []string
	if err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT array_agg(id::text) FROM telephony_menus), '{}'),
		coalesce((SELECT array_agg(id::text) FROM telephony_groups), '{}')`).Scan(&ids, &groups); err != nil {
		return "", err
	}
	items := []MenuItem{}
	for _, it := range m.Items {
		if it.Act == "" {
			continue // a row left empty
		}
		if len(it.Digit) != 1 || !strings.Contains(MenuDigits, it.Digit) {
			return "", i18n.E("err.menu_digit")
		}
		if slices.ContainsFunc(items, func(o MenuItem) bool { return o.Digit == it.Digit }) {
			return "", i18n.E("err.menu_digit_twice", it.Digit)
		}
		it, err := menuItem(it, ids, groups, m.ID)
		if err != nil {
			return "", err
		}
		items = append(items, it)
	}
	slices.SortStableFunc(items, func(a, b MenuItem) int {
		return strings.Index(MenuDigits, a.Digit) - strings.Index(MenuDigits, b.Digit)
	})
	m.Timeout.Digit = ""
	if m.Timeout.Act == MenuRepeat || m.Timeout.Act == "" {
		m.Timeout.Act = ActHangup
	}
	timeout, err := menuItem(m.Timeout, ids, groups, m.ID)
	if err != nil {
		return "", err
	}
	if m.Greeting != nil {
		if m.Greeting, err = Greeting(m.Greeting); err != nil {
			return "", err
		}
	}
	itemsJSON, _ := json.Marshal(items)
	timeoutJSON, _ := json.Marshal(timeout)
	args := []any{m.Name, m.Wait, m.Repeats, m.Direct, itemsJSON, timeoutJSON, m.Greeting, m.DropGreeting}
	if m.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO telephony_menus (name, wait, repeats, direct, items, timeout, greeting, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $8 THEN NULL ELSE $7::bytea END, $9) RETURNING id::text`,
			append(args, userID)...).Scan(&m.ID)
		return m.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE telephony_menus SET name = $1, wait = $2, repeats = $3, direct = $4, items = $5, timeout = $6,
		greeting = CASE WHEN $8 THEN NULL ELSE coalesce($7::bytea, greeting) END WHERE id::text = $9`, append(args, m.ID)...)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return m.ID, err
}

// menuItem checks an item of a menu: menus are the ids of the menus there are, self — the menu's own ("" — a new one).
func menuItem(it MenuItem, menus, groups []string, self string) (MenuItem, error) {
	out := MenuItem{Digit: it.Digit, Act: it.Act}
	switch it.Act {
	case MenuRing:
		for _, u := range it.Users {
			if u != "" && !slices.Contains(out.Users, u) {
				out.Users = append(out.Users, u)
			}
		}
		if len(out.Users) == 0 {
			return out, i18n.E("err.menu_users")
		}
		out.Mode = "all"
		if it.Mode == "queue" {
			out.Mode = "queue"
		}
	case ActForward:
		out.To = Dialable(it.To)
		if !trunkNo.MatchString(out.To) {
			return out, i18n.E("err.trunk_forward")
		}
	case MenuGoto:
		if it.Menu == self || !slices.Contains(menus, it.Menu) {
			return out, i18n.E("err.menu_goto")
		}
		out.Menu = it.Menu
	case MenuGroup:
		if !slices.Contains(groups, it.Group) {
			return out, i18n.E("err.menu_group")
		}
		out.Group = it.Group
	case MenuRepeat, ActVoicemail, ActHangup:
	default:
		return out, i18n.E("err.menu_digit")
	}
	return out, nil
}

// MenuGreeting is the greeting of a menu (a WAV for Asterisk); nil — none.
func (s *Store) MenuGreeting(ctx context.Context, id string) ([]byte, error) {
	var g []byte
	err := s.db.QueryRow(ctx, `SELECT greeting FROM telephony_menus WHERE id::text = $1`, id).Scan(&g)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

// DeleteMenu removes a voice menu; the lines it answered ring their employees again, the items of other menus that
// led to it are dropped.
func (s *Store) DeleteMenu(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM telephony_menus WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE telephony_menus SET
		items = coalesce((SELECT jsonb_agg(i.v) FROM jsonb_array_elements(items) AS i(v) WHERE i.v->>'menu' IS DISTINCT FROM $1), '[]'),
		timeout = CASE WHEN timeout->>'menu' = $1 THEN '{"act": "hangup"}' ELSE timeout END
		WHERE items @> jsonb_build_array(jsonb_build_object('menu', $1::text)) OR timeout->>'menu' = $1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RenderMenus is the dialplan of the voice menus: a context each. The line that sent the call there has set
// GO24_TRUNK and GO24_TNUM (the line and its number: a forward goes out through it), GO24_GREET (its voicemail's
// greeting) and GO24_BACK (where its «nobody answered» handling starts). numbers are the employees' inner numbers by
// their ids, groups — the queue groups' contexts by their ids.
func RenderMenus(menus []Menu, numbers, groups map[string]string) string {
	var d strings.Builder
	keys := map[string]string{} // the menus' and the groups' contexts
	for _, m := range menus {
		keys[m.ID] = MenuKey(m.ID)
	}
	for id, k := range groups {
		keys[id] = k
	}
	inner := make([]string, 0, len(numbers))
	for _, n := range numbers {
		inner = append(inner, n)
	}
	slices.Sort(inner)
	for _, m := range menus {
		fmt.Fprintf(&d, "\n; %s\n[%s]\nexten => s,1,Set(GO24_TRY=0)\n same => n,Answer()\n same => n,Wait(1)\n", callerName(m.Name), keys[m.ID])
		d.WriteString(" same => n(again),Set(TIMEOUT(digit)=3)\n")
		if m.GreetingSum != "" {
			fmt.Fprintf(&d, " same => n,Background(%s)\n", GreetingFile(m.GreetingSum))
		}
		fmt.Fprintf(&d, " same => n,WaitExten(%d)\n", m.Wait)
		taken := map[string]bool{}
		for _, it := range m.Items {
			taken[it.Digit] = true
			d.WriteString(menuAct(it.Digit, it, keys, numbers))
		}
		if m.Direct {
			for _, n := range inner {
				if !taken[n] {
					d.WriteString(menuAct(n, MenuItem{Act: MenuRing, Users: []string{n}}, keys, nil))
				}
			}
		}
		// nothing pressed, or a key no item has: the menu once more, then Timeout
		for _, e := range []string{"t", "i"} {
			fmt.Fprintf(&d, "exten => %s,1,Set(GO24_TRY=$[${GO24_TRY} + 1])\n same => n,GotoIf($[${GO24_TRY} < %d]?s,again)\n", e, m.Repeats)
			d.WriteString(strings.Replace(menuAct(e, m.Timeout, keys, numbers), "exten => "+e+",1,", " same => n,", 1))
		}
	}
	return d.String()
}

// menuAct is the extension exten of a menu doing it. numbers turn the item's employees into inner numbers (nil: Users
// are the numbers already).
func menuAct(exten string, it MenuItem, keys, numbers map[string]string) string {
	first := "exten => " + exten + ",1,"
	switch it.Act {
	case MenuRing:
		var ring []string
		for _, u := range it.Users {
			if numbers == nil {
				ring = append(ring, u)
			} else if n := numbers[u]; n != "" {
				ring = append(ring, n)
			}
		}
		if len(ring) == 0 {
			return first + "Goto(${GO24_BACK})\n"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%sSet(GO24_BY=%s)\n", first, ring[0])
		if it.Mode == "queue" {
			for _, n := range ring {
				fmt.Fprintf(&b, " same => n,Dial(Local/%s@go24-ring,%d,g)\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n", n, RingTurn)
			}
		} else {
			legs := make([]string, len(ring))
			for i, n := range ring {
				legs[i] = "Local/" + n + "@go24-ring"
			}
			fmt.Fprintf(&b, " same => n,Dial(%s,45,g)\n same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n", strings.Join(legs, "&"))
		}
		b.WriteString(" same => n,Goto(${GO24_BACK})\n same => n(done),Gosub(go24-rate,s,1(${GO24_RATEP}))\n same => n,Hangup()\n")
		return b.String()
	case ActForward:
		if len(it.To) <= innerMax && !strings.HasPrefix(it.To, "+") { // an employee's inner number
			return fmt.Sprintf("%sSet(GO24_BY=%s)\n same => n,Dial(Local/%[2]s@go24-ring,45,g)\n"+
				" same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n same => n,Goto(${GO24_BACK})\n same => n(done),Gosub(go24-rate,s,1(${GO24_RATEP}))\n same => n,Hangup()\n", first, it.To)
		}
		return fmt.Sprintf("%sGosub(go24-forward,s,1(${GO24_TRUNK},${GO24_TNUM},%s))\n same => n,Hangup()\n", first, it.To)
	case ActVoicemail:
		return first + "Gosub(go24-voicemail,s,1(${GO24_GREET}))\n same => n,Hangup()\n"
	case MenuGroup:
		if k := keys[it.Group]; k != "" {
			return first + "Goto(" + k + ",s,1)\n"
		}
		return first + "Goto(${GO24_BACK})\n"
	case MenuGoto:
		if k := keys[it.Menu]; k != "" {
			return first + "Goto(" + k + ",s,1)\n"
		}
	case MenuRepeat:
		return first + "Goto(s,1)\n"
	}
	return first + "Hangup()\n"
}

// Rows are the rows of the menu's form: its items and two empty ones to add an item.
func (m Menu) Rows() []MenuItem {
	return append(slices.Clone(m.Items), MenuItem{}, MenuItem{})
}
