package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// forEvent is appFor for the employee u who bound the handler.
var forEvent = strings.ReplaceAll(appFor, "$1", "u.id")

// Events are what an application can subscribe its handler to, as event.bind (see migration 323): the
// cards of CRM (a smart process's — ONCRMDYNAMICITEM…), tasks, a new employee, the application's own uninstall, a call of
// telephony that ended and what its chat bots hear (imbot.register binds their handlers; migration 329).
var Events = []string{
	"ONCRMLEADADD", "ONCRMLEADUPDATE", "ONCRMLEADDELETE",
	"ONCRMDEALADD", "ONCRMDEALUPDATE", "ONCRMDEALDELETE",
	"ONCRMCONTACTADD", "ONCRMCONTACTUPDATE", "ONCRMCONTACTDELETE",
	"ONCRMCOMPANYADD", "ONCRMCOMPANYUPDATE", "ONCRMCOMPANYDELETE",
	"ONCRMDYNAMICITEMADD", "ONCRMDYNAMICITEMUPDATE", "ONCRMDYNAMICITEMDELETE",
	"ONTASKADD", "ONTASKUPDATE", "ONTASKDELETE",
	"ONUSERADD", "ONAPPUNINSTALL", "ONVOXIMPLANTCALLEND", // a call of the log ended (web.phoneEvents)
	"ONIMBOTMESSAGEADD", "ONIMBOTJOINCHAT", "ONIMBOTDELETE", "ONIMCOMMANDADD", // of the application's chat bots (matrixsync.Bot)
}

// Event is a handler of an application subscribed to an event.
type Event struct {
	ID         string     `json:"id"`
	AppID      string     `json:"-"`
	Event      string     `json:"event"`
	Handler    string     `json:"handler"`
	UserID     string     `json:"auth_type"` // whose tokens the handler gets
	LastAt     *time.Time `json:"last_at"`
	LastStatus *int       `json:"last_status"`
	LastError  string     `json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
	Pending    int        `json:"pending"`
}

// BindEvent subscribes the application's handler (on its host) to the event (event.bind) for the employee whose
// tokens it gets; the same handler bound again changes only the employee.
func (s *Store) BindEvent(ctx context.Context, a App, e Event) (Event, error) {
	e.Event, e.Handler = strings.ToUpper(strings.TrimSpace(e.Event)), strings.TrimSpace(e.Handler)
	if !slices.Contains(Events, e.Event) {
		return e, i18n.E("err.app_event", e.Event)
	}
	if u, err := url.Parse(e.Handler); err != nil || checkURL(e.Handler, true) != nil || !a.Owns(u) {
		return e, i18n.E("err.app_handler_host")
	}
	err := s.db.QueryRow(ctx, `INSERT INTO app_events (app_id, event, handler_url, user_id) SELECT $1, $2, $3, u.id FROM users u
		WHERE u.id::text = $4 AND u.active ON CONFLICT (app_id, event, handler_url) DO UPDATE SET user_id = EXCLUDED.user_id
		RETURNING id::text, created_at`, a.ID, e.Event, e.Handler, e.UserID).Scan(&e.ID, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = i18n.E("err.app_event_user")
	}
	e.AppID = a.ID
	return e, err
}

// UnbindEvent unsubscribes the application's handler from the event (event.unbind); without a handler — every
// handler of the event. It tells how many were unsubscribed.
func (s *Store) UnbindEvent(ctx context.Context, appID, event, handler string) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM app_events WHERE app_id::text = $1 AND event = upper($2) AND ($3 = '' OR handler_url = $3)`,
		appID, strings.TrimSpace(event), strings.TrimSpace(handler))
	return tag.RowsAffected(), err
}

// AppEvents are the subscriptions of an application (event.get), or of all, by application, with appID "".
func (s *Store) AppEvents(ctx context.Context, appID string) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT e.id::text, e.app_id::text, e.event, e.handler_url, coalesce(e.user_id::text, ''), e.last_at,
		e.last_status, e.last_error, e.created_at, (SELECT count(*) FROM app_event_deliveries d WHERE d.event_id = e.id AND d.done_at IS NULL)::int
		FROM app_events e WHERE $1 = '' OR e.app_id::text = $1 ORDER BY e.created_at, e.event`, appID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Event])
}

// eventBackoff is when a failed delivery is tried again; after the last one it is given up.
var eventBackoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// DeliverEvents posts the events that are due to the handlers (the worker calls it): {"event", "event_handler_id",
// "data", "ts", "auth"} signed with the application's key sends them; «auth» has the tokens of the
// employee who bound the handler while the application is for them, otherwise only application_token and the
// portal's address.
func (s *Store) DeliverEvents(ctx context.Context, now time.Time, base string) (sent int, errs []error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM app_event_deliveries WHERE done_at < $1`, now.Add(-7*24*time.Hour)); err != nil {
		return 0, []error{err}
	}
	rows, err := s.db.Query(ctx, `SELECT d.id, d.event, d.data::text, d.attempts, e.id::text, e.handler_url, e.app_id::text,
		CASE WHEN u.active AND `+forEvent+` THEN u.id::text ELSE '' END
		FROM app_event_deliveries d JOIN app_events e ON e.id = d.event_id JOIN apps a ON a.id = e.app_id LEFT JOIN users u ON u.id = e.user_id
		WHERE d.done_at IS NULL AND d.next_at <= $1 AND a.installed_at IS NOT NULL ORDER BY d.id LIMIT 50`, now)
	if err != nil {
		return 0, []error{err}
	}
	type job struct {
		id                       int64
		event, data              string
		attempts                 int
		handlerID, target, appID string
		userID                   string
	}
	jobs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (job, error) {
		var j job
		err := r.Scan(&j.id, &j.event, &j.data, &j.attempts, &j.handlerID, &j.target, &j.appID, &j.userID)
		return j, err
	})
	if err != nil {
		return 0, []error{err}
	}
	known := map[string]App{}
	tokens := map[string]Tokens{} // one pair of tokens an application and employee a round
	for _, j := range jobs {
		a, ok := known[j.appID]
		if !ok {
			if a, err = s.Get(ctx, j.appID); err != nil {
				errs = append(errs, err)
				continue
			}
			known[j.appID] = a
		}
		auth := bareAuth(a, base)
		if j.userID != "" {
			t, ok := tokens[j.appID+j.userID]
			if !ok {
				if t, err = s.issue(ctx, a.ID, j.userID, AccessTTL); err != nil {
					errs = append(errs, err)
					continue
				}
				tokens[j.appID+j.userID] = t
			}
			auth = Auth(a, t, base)
		}
		body, _ := json.Marshal(map[string]any{"event": j.event, "event_handler_id": j.handlerID, "data": json.RawMessage(j.data),
			"ts": now.Unix(), "auth": auth})
		status, err := s.post(ctx, j.target, a.ClientSecret, j.event, body)
		msg := ""
		if err != nil {
			msg = err.Error()
		} else if status >= 300 {
			msg = "HTTP " + strconv.Itoa(status)
		}
		var st *int
		if status > 0 {
			st = &status
		}
		if _, err := s.db.Exec(ctx, `UPDATE app_events SET last_at = now(), last_status = $2, last_error = left($3, 300) WHERE id::text = $1`,
			j.handlerID, st, msg); err != nil {
			errs = append(errs, err)
		}
		if msg == "" {
			sent++
			_, err = s.db.Exec(ctx, `UPDATE app_event_deliveries SET done_at = now(), attempts = attempts + 1 WHERE id = $1`, j.id)
		} else if j.attempts >= len(eventBackoff) {
			errs = append(errs, fmt.Errorf("app %s: %s given up: %s", a.ClientID, j.event, msg))
			_, err = s.db.Exec(ctx, `UPDATE app_event_deliveries SET done_at = now(), attempts = attempts + 1 WHERE id = $1`, j.id)
		} else {
			_, err = s.db.Exec(ctx, `UPDATE app_event_deliveries SET attempts = attempts + 1, next_at = $2 WHERE id = $1`, j.id, now.Add(eventBackoff[j.attempts]))
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return sent, errs
}

// uninstalled tells the handlers of ONAPPUNINSTALL the application is taken off, at once and once: its tokens are
// already gone, «auth» has application_token and the portal's address.
func (s *Store) uninstalled(ctx context.Context, a App, base, lang string) {
	list, err := s.AppEvents(ctx, a.ID)
	if err != nil {
		return
	}
	auth := bareAuth(a, base)
	for _, e := range list {
		if e.Event != "ONAPPUNINSTALL" {
			continue
		}
		body, _ := json.Marshal(map[string]any{"event": e.Event, "event_handler_id": e.ID, "ts": time.Now().Unix(),
			"data": map[string]any{"LANGUAGE_ID": lang, "CLEAN": 0}, "auth": auth})
		_, _ = s.post(ctx, e.Handler, a.ClientSecret, e.Event, body)
	}
}

// bareAuth is «auth» without an employee's tokens: application_token and the portal's address.
func bareAuth(a App, base string) map[string]any {
	auth := Auth(a, Tokens{}, base)
	for _, k := range []string{"access_token", "refresh_token", "expires", "expires_in", "user_id"} {
		delete(auth, k)
	}
	return auth
}
