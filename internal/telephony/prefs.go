package telephony

// The employee's own telephony settings (profile → «Телефония»): the line their calls out go by and
// forwarding their calls to a mobile.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Forwarding of the employee's calls to their mobile.
const (
	ForwardNever    = ""
	ForwardOffline  = "offline"  // neither the SIP phone nor the softphone is online
	ForwardNoAnswer = "noanswer" // they did not answer within After seconds
	ForwardAlways   = "always"
)

// Forwards are the forwarding modes in the order the form offers them.
var Forwards = []string{ForwardNever, ForwardOffline, ForwardNoAnswer, ForwardAlways}

// Prefs are the employee's own telephony settings.
type Prefs struct {
	OutTrunk string // the external line of their calls out ("" — the first one they may call by)
	Forward  string // one of Forwards
	After    int    // ForwardNoAnswer: how long their phones ring first, seconds
	To       string // the mobile (Dialable)
}

// Bounds of Prefs.After.
const (
	forwardMin = 5
	forwardMax = 60
)

// PrefsOf are the employee's settings (defaults while they have none).
func (s *Store) PrefsOf(ctx context.Context, userID string) (Prefs, error) {
	all, err := s.prefs(ctx, userID)
	if p, ok := all[userID]; ok || err != nil {
		return p, err
	}
	return Prefs{After: 20}, nil
}

// AllPrefs are the settings of the employees who have any, by their ids.
func (s *Store) AllPrefs(ctx context.Context) (map[string]Prefs, error) {
	return s.prefs(ctx, "")
}

func (s *Store) prefs(ctx context.Context, userID string) (map[string]Prefs, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id::text, coalesce(out_trunk::text, ''), forward, forward_after, forward_to
		FROM telephony_prefs WHERE $1 = '' OR user_id::text = $1`, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]Prefs{}
	var id string
	var p Prefs
	_, err = pgx.ForEachRow(rows, []any{&id, &p.OutTrunk, &p.Forward, &p.After, &p.To}, func() error {
		out[id] = p
		return nil
	})
	return out, err
}

// SavePrefs keeps the employee's settings. The line must be one they may call out by; forwarding needs an outside
// number.
func (s *Store) SavePrefs(ctx context.Context, userID string, p Prefs) error {
	if !slices.Contains(Forwards, p.Forward) {
		p.Forward = ForwardNever
	}
	p.After = min(max(p.After, forwardMin), forwardMax)
	if strings.TrimSpace(p.To) != "" {
		to := Dialable(p.To)
		if to == "" || strings.ContainsAny(to, "*#") || (len(to) <= innerMax && to[0] != '+') {
			return i18n.E("err.tel_mobile")
		}
		p.To = to
	} else {
		p.To = ""
	}
	if p.Forward != ForwardNever && p.To == "" {
		return i18n.E("err.tel_mobile")
	}
	if p.OutTrunk != "" {
		ok, err := s.MayCallBy(ctx, userID, p.OutTrunk)
		if err != nil {
			return err
		}
		if !ok {
			return i18n.E("err.tel_out_line")
		}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO telephony_prefs (user_id, out_trunk, forward, forward_after, forward_to)
		VALUES ($1, nullif($2, '')::uuid, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE SET out_trunk = EXCLUDED.out_trunk, forward = EXCLUDED.forward,
			forward_after = EXCLUDED.forward_after, forward_to = EXCLUDED.forward_to, updated_at = now()`,
		userID, p.OutTrunk, p.Forward, p.After, p.To)
	return err
}

// MayCallBy tells whether the employee may call out by the line (its «who may call out»).
func (s *Store) MayCallBy(ctx context.Context, userID, trunkID string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT out_all OR $1::uuid = ANY(out_users) FROM telephony_trunks WHERE id::text = $2`,
		userID, trunkID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

// OutTrunks are the lines the employee may call out by, in their order.
func OutTrunks(trunks []Trunk, userID string) []Trunk {
	var out []Trunk
	for _, t := range trunks {
		if t.OutAll || slices.Contains(t.Out, userID) {
			out = append(out, t)
		}
	}
	return out
}

// RenderForwards is go24-fw of the dialplan: for each employee forwarding their calls, by the inner number, how
// (GO24_FWM), where (GO24_FWTO), after how long (GO24_FWS) and their own line out with its number (GO24_FWOT,
// GO24_FWON; "" — they have none: only calls from a line are forwarded, by that line). go24-dial-me reads them.
func RenderForwards(prefs map[string]Prefs, numbers map[string]string, outs map[string]Trunk) string {
	var b strings.Builder
	b.WriteString("\n[go24-fw]\n")
	users := make([]string, 0, len(prefs))
	for u, p := range prefs {
		if p.Forward != ForwardNever && p.To != "" && numbers[u] != "" {
			users = append(users, u)
		}
	}
	slices.SortFunc(users, func(a, b string) int { return strings.Compare(numbers[a], numbers[b]) })
	for _, u := range users {
		p, n := prefs[u], numbers[u]
		t := outs[n]
		tk := ""
		if t.ID != "" {
			tk = t.Key()
		}
		fmt.Fprintf(&b, "exten => %s,1,Set(GO24_FWM=%s)\n same => n,Set(GO24_FWTO=%s)\n same => n,Set(GO24_FWS=%d)\n"+
			" same => n,Set(GO24_FWOT=%s)\n same => n,Set(GO24_FWON=%s)\n same => n,Return()\n", n, p.Forward, p.To, p.After, tk, t.Number)
	}
	return b.String()
}
