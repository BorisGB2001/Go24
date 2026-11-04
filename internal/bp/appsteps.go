package bp

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/apps"
	"go24/internal/i18n"
)

// Actions of applications (app), as the steps applications add to the designer by bizproc.activity.add: a
// step posts the action's properties ({…} filled) to the application's handler; when the action is with an answer
// (USE_SUBSCRIPTION), the process waits for bizproc.event.send with the step's token (AppAnswer) — not longer than
// the step's Timeout — and the values returned become its fields AppPrefix<code>_<key>.

// AppPrefix begins the keys of the fields the values an application's action returns go into.
const AppPrefix = "app_"

var appKeyRe = regexp.MustCompile(`[^a-z0-9_]+`)

// AppFieldKey is the key of the field of the value key the action of the code returns.
func AppFieldKey(code, key string) string {
	return AppPrefix + appKeyRe.ReplaceAllString(strings.ToLower(code), "_") + "_" + strings.ToLower(key)
}

// appKinds: a type of a property → the kind of a field.
var appKinds = map[string]string{"string": "string", "text": "text", "int": "number", "double": "number", "bool": "bool",
	"date": "date", "datetime": "datetime", "select": "string", "user": "user"}

// AppFields are the fields of the values the action returns.
func AppFields(act apps.Activity) []Field {
	out := []Field{}
	for _, p := range act.Returns {
		out = append(out, Field{Key: AppFieldKey(act.Code, p.Key), Label: act.Name + ": " + p.Name, Kind: appKinds[p.Type]})
	}
	return out
}

// checkAppStep: the step's action is of an installed application, its values are of the action's properties and
// none required is left empty (without a default); its name and the fields of its answer are those of the action now.
func (s *Store) checkAppStep(ctx context.Context, n int, st *Step) error {
	if s.Apps == nil {
		return i18n.E("err.bp_step")
	}
	act, err := s.Apps.Activity(ctx, st.App)
	if err != nil {
		return i18n.E("err.bp_app_missing", n)
	}
	set := map[string]string{}
	for _, a := range st.Sets {
		if _, dup := set[a.Field]; dup || len([]rune(a.Value)) > 1000 {
			return i18n.E("err.bp_step")
		}
		set[a.Field] = a.Value
	}
	known := map[string]bool{}
	for _, p := range act.Props {
		known[p.Key] = true
		if p.Required && strings.TrimSpace(set[p.Key]) == "" && p.Default == "" {
			return i18n.E("err.bp_app_required", n, p.Name)
		}
	}
	for k := range set {
		if !known[k] {
			return i18n.E("err.bp_app_prop", n, k)
		}
	}
	st.Text, st.Fields = act.AppName+": "+act.Name, nil // the list of steps shows the name
	if act.Subscribe {
		st.Fields = AppFields(act)
	} else {
		st.Timeout = 0
	}
	return nil
}

// appStep posts the step to the application; with an answer the process (its branch) waits on the step by the token.
func (s *Store) appStep(ctx context.Context, x Instance, st Step) (wait bool, err error) {
	if s.Apps == nil || s.AppCall == nil {
		return false, i18n.E("err.bp_app_missing", x.Step+1)
	}
	act, err := s.Apps.Activity(ctx, st.App)
	if err != nil {
		return false, i18n.E("err.bp_app_missing", x.Step+1)
	}
	props := map[string]any{}
	for _, p := range act.Props {
		v := p.Default
		for _, a := range st.Sets {
			if a.Field == p.Key && strings.TrimSpace(a.Value) != "" {
				v = s.fillRaw(ctx, x, a.Value)
			}
		}
		if p.Type == "user" && v != "" && !strings.HasPrefix(v, "user_") { // an employee names one: user_<number>
			id, err := s.person(ctx, x, v)
			if err != nil {
				return false, err
			}
			var num int64
			if err := s.db.QueryRow(ctx, `SELECT num FROM users WHERE id::text = $1`, id).Scan(&num); err != nil {
				return false, err
			}
			v = "user_" + strconv.FormatInt(num, 10)
		}
		props[p.Key] = v
	}
	token := ""
	if act.Subscribe { // waits before the post: an application may answer at once
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		token = x.ID + "|A" + strconv.Itoa(x.Step+1) + "|" + hex.EncodeToString(b)
		var wake *time.Time
		if st.Timeout > 0 {
			w := time.Now().Add(time.Duration(st.Timeout) * time.Minute)
			wake = &w
		}
		if x.branch > 0 {
			_, err = s.db.Exec(ctx, `UPDATE bp_branches SET wait_app = $2, wake_at = $3, step = $4, waiting = true WHERE instance_id = $1 AND n = $5`,
				x.ID, token, wake, x.Step, x.branch-1)
		} else {
			_, err = s.db.Exec(ctx, `UPDATE bp_instances SET wait_app = $2, wake_at = $3 WHERE id = $1`, x.ID, token, wake)
		}
		if err != nil {
			return false, err
		}
	}
	if err := s.AppCall(ctx, act, x.StartedBy.ID, x, token, props); err != nil {
		if token != "" {
			_, _ = s.db.Exec(ctx, `UPDATE bp_instances SET wait_app = NULL, wake_at = NULL WHERE wait_app = $1`, token)
			_, _ = s.db.Exec(ctx, `UPDATE bp_branches SET wait_app = NULL, wake_at = NULL, waiting = false WHERE wait_app = $1`, token)
		}
		return false, err
	}
	s.log(ctx, x.ID, x.Step, "", "step", "app:"+act.Code)
	return act.Subscribe, nil
}

// ErrAppToken: no process waits for an answer of the token (bizproc.event.send) — it is over, answered or timed out.
var ErrAppToken = i18n.E("err.bp_app_token")

// AppAnswer is the answer of an application to the step of its action waiting by the token (bizproc.event.send): the
// values returned (by the keys of the action's RETURN_PROPERTIES) go into the process's fields, the message into its
// history, and the process goes on.
func (s *Store) AppAnswer(ctx context.Context, appID, token string, values map[string]string, message string) error {
	var id string
	var step int
	err := s.db.QueryRow(ctx, `SELECT i.id::text, i.step FROM bp_instances i WHERE i.wait_app = $1 AND i.status = 'running'
		UNION ALL SELECT i.id::text, b.step FROM bp_branches b JOIN bp_instances i ON i.id = b.instance_id WHERE b.wait_app = $1 AND i.status = 'running'`,
		token).Scan(&id, &step)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAppToken
	}
	if err != nil {
		return err
	}
	x, err := s.instance(ctx, id)
	if err != nil {
		return err
	}
	if step < 0 || step >= len(x.Steps) || x.Steps[step].Kind != "app" {
		return ErrAppToken
	}
	st := x.Steps[step]
	if s.Apps == nil {
		return ErrAppToken
	}
	act, err := s.Apps.Activity(ctx, st.App)
	if err != nil || act.AppID != appID {
		return ErrAppToken
	}
	got := map[string]string{}
	for k, v := range values {
		got[strings.ToLower(k)] = v
	}
	set := map[string]string{}
	for _, p := range act.Returns {
		v, ok := got[strings.ToLower(p.Key)]
		if !ok {
			continue
		}
		f := Field{Key: AppFieldKey(act.Code, p.Key), Kind: appKinds[p.Type]}
		if f.Kind == "user" { // user_<number>, or a go24 id
			if n, ok := strings.CutPrefix(v, "user_"); ok {
				if s.db.QueryRow(ctx, `SELECT id::text FROM users WHERE num::text = $1`, n).Scan(&v) != nil {
					v = ""
				}
			}
		} else if c, err := cleanValue(f, v); err == nil {
			v = c
		}
		set[f.Key] = v
	}
	vals, _ := json.Marshal(set)
	if _, err := s.db.Exec(ctx, `UPDATE bp_instances SET values = coalesce(values, '{}'::jsonb) || $2::jsonb WHERE id::text = $1`, id, vals); err != nil {
		return err
	}
	if msg := strings.TrimSpace(message); msg != "" {
		s.log(ctx, id, step, "", "report", cmp.Or(act.Name, act.Code)+": "+msg)
	}
	// the step is over as a pause that ends now: the worker's wake goes on with it — at once here, unless the process
	// runs (the post of the step itself is not over yet)
	now := time.Now()
	tag, err := s.db.Exec(ctx, `UPDATE bp_instances SET wait_app = NULL, wake_at = $2 WHERE wait_app = $1`, token, now)
	if err == nil && tag.RowsAffected() == 0 {
		_, err = s.db.Exec(ctx, `UPDATE bp_branches SET wait_app = NULL, wake_at = $2 WHERE wait_app = $1`, token, now)
	}
	if err != nil {
		return err
	}
	s.log(ctx, id, step, "", "step", "app:"+act.Code+":answer")
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	var free bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 57))`, id).Scan(&free)
	if err == nil && free {
		_, err = conn.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended($1, 57))`, id)
	}
	conn.Release()
	if err != nil || !free {
		return err
	}
	if errs := s.WakeDue(ctx, now); len(errs) > 0 {
		return fmt.Errorf("bp: %v", errs[0])
	}
	return nil
}

// AppLog writes the application's message into the history of the process waiting for its answer
// (bizproc.activity.log).
func (s *Store) AppLog(ctx context.Context, token, message string) error {
	var id string
	var step int
	err := s.db.QueryRow(ctx, `SELECT i.id::text, i.step FROM bp_instances i WHERE i.wait_app = $1
		UNION ALL SELECT b.instance_id::text, b.step FROM bp_branches b WHERE b.wait_app = $1`, token).Scan(&id, &step)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAppToken
	}
	if err == nil && strings.TrimSpace(message) != "" {
		s.log(ctx, id, step, "", "report", strings.TrimSpace(message))
	}
	return err
}
