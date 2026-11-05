package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Actions of applications for the workflow designer, as bizproc.activity.* (migration 836): an
// application adds its action with properties; a step of the action posts them to the handler and, with Subscribe,
// waits for bizproc.event.send with the values of Returns.

var (
	ErrActivityExists  = i18n.E("err.app_activity_exists")
	ErrActivityMissing = i18n.E("err.app_activity_missing")
)

// PropTypes are the types of the properties of an action.
var PropTypes = []string{"string", "text", "int", "double", "bool", "date", "datetime", "select", "user"}

// MaxProps of an action, of its properties and returned values each.
const MaxProps = 50

// Option is a choice of a select property: its key and what the designer shows.
type Option struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Prop is a property of an action (PROPERTIES) or a value it returns (RETURN_PROPERTIES).
type Prop struct {
	Key         string   `json:"key"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Multiple    bool     `json:"multiple,omitempty"`
	Default     string   `json:"default,omitempty"`
	Options     []Option `json:"options,omitempty"`
}

// Activity is an action of an application.
type Activity struct {
	ID          string `json:"id"`
	AppID       string `json:"-"`
	AppName     string `json:"app"`
	Code        string `json:"code"`
	Handler     string `json:"handler"`
	UserID      string `json:"user_id"` // whose tokens the handler gets; "" — the process's starter's
	Subscribe   bool   `json:"subscribe"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Props       []Prop `json:"props"`
	Returns     []Prop `json:"returns"`
}

var (
	codeRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	keyRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,49}$`)
)

func checkProps(list []Prop) error {
	seen := map[string]bool{}
	if len(list) > MaxProps {
		return i18n.E("err.app_activity_props")
	}
	for _, p := range list {
		k := strings.ToLower(p.Key)
		if !keyRe.MatchString(p.Key) || seen[k] || p.Name == "" || len([]rune(p.Name)) > 200 || len([]rune(p.Description)) > 1000 ||
			!slices.Contains(PropTypes, p.Type) || (p.Type == "select") != (len(p.Options) > 0) || len(p.Options) > 100 || len([]rune(p.Default)) > 1000 {
			return i18n.E("err.app_activity_prop", p.Key)
		}
		for _, o := range p.Options {
			if o.Key == "" || o.Value == "" || len([]rune(o.Value)) > 200 {
				return i18n.E("err.app_activity_prop", p.Key)
			}
		}
		seen[k] = true
	}
	return nil
}

// SaveActivity adds the application's action (bizproc.activity.add) or, with update, changes the one of its code
// (bizproc.activity.update); the handler is on the application's host.
func (s *Store) SaveActivity(ctx context.Context, a App, act Activity, update bool) (Activity, error) {
	act.Code, act.Handler, act.Name = strings.TrimSpace(act.Code), strings.TrimSpace(act.Handler), strings.TrimSpace(act.Name)
	if !codeRe.MatchString(act.Code) || act.Name == "" || len([]rune(act.Name)) > 200 || len([]rune(act.Description)) > 2000 {
		return act, i18n.E("err.app_activity")
	}
	if u, err := url.Parse(act.Handler); err != nil || checkURL(act.Handler, true) != nil || !a.Owns(u) {
		return act, i18n.E("err.app_handler_host")
	}
	if err := checkProps(act.Props); err != nil {
		return act, err
	}
	if err := checkProps(act.Returns); err != nil {
		return act, err
	}
	if act.Props == nil {
		act.Props = []Prop{}
	}
	if act.Returns == nil {
		act.Returns = []Prop{}
	}
	props, _ := json.Marshal(act.Props)
	returns, _ := json.Marshal(act.Returns)
	var user *string
	if act.UserID != "" {
		user = &act.UserID
	}
	var err error
	if update {
		err = s.db.QueryRow(ctx, `UPDATE app_bp_activities SET handler_url = $3, user_id = $4, subscribe = $5, name = $6, description = $7,
			props = $8, returns = $9 WHERE app_id = $1 AND code = $2 RETURNING id::text`,
			a.ID, act.Code, act.Handler, user, act.Subscribe, act.Name, act.Description, props, returns).Scan(&act.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrActivityMissing
		}
	} else {
		err = s.db.QueryRow(ctx, `INSERT INTO app_bp_activities (app_id, code, handler_url, user_id, subscribe, name, description, props, returns)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (app_id, code) DO NOTHING RETURNING id::text`,
			a.ID, act.Code, act.Handler, user, act.Subscribe, act.Name, act.Description, props, returns).Scan(&act.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrActivityExists
		}
	}
	act.AppID, act.AppName = a.ID, a.Title()
	return act, err
}

// DeleteActivity takes the application's action of the code off (bizproc.activity.delete); the steps of templates
// with it fail from then on.
func (s *Store) DeleteActivity(ctx context.Context, a App, code string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM app_bp_activities WHERE app_id = $1 AND code = $2`, a.ID, code)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrActivityMissing
	}
	return err
}

func (s *Store) activities(ctx context.Context, where string, args ...any) ([]Activity, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id::text, b.app_id::text, coalesce(nullif(a.menu_title, ''), a.name), b.code, b.handler_url,
		coalesce(b.user_id::text, ''), b.subscribe, b.name, b.description, b.props, b.returns
		FROM app_bp_activities b JOIN apps a ON a.id = b.app_id WHERE `+where+` ORDER BY a.name, b.name`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Activity, error) {
		var x Activity
		err := r.Scan(&x.ID, &x.AppID, &x.AppName, &x.Code, &x.Handler, &x.UserID, &x.Subscribe, &x.Name, &x.Description, &x.Props, &x.Returns)
		return x, err
	})
}

// AppActivities are the application's own actions (bizproc.activity.list).
func (s *Store) AppActivities(ctx context.Context, appID string) ([]Activity, error) {
	return s.activities(ctx, "b.app_id = $1", appID)
}

// Activities are the actions of the installed applications, for the designer.
func (s *Store) Activities(ctx context.Context) ([]Activity, error) {
	return s.activities(ctx, "a.installed_at IS NOT NULL")
}

// Activity is the action of the id, of an installed application.
func (s *Store) Activity(ctx context.Context, id string) (Activity, error) {
	list, err := s.activities(ctx, "b.id::text = $1 AND a.installed_at IS NOT NULL", id)
	if err == nil && len(list) == 0 {
		err = ErrActivityMissing
	}
	if err != nil {
		return Activity{}, err
	}
	return list[0], nil
}

// CallActivity posts a step of the action to its handler: a form of PHP arrays (event_token,
// workflow_id, code, document_id, properties…) with «auth» — the tokens of the action's employee or else of userID.
func (s *Store) CallActivity(ctx context.Context, act Activity, userID, base string, data map[string]any) error {
	a, err := s.Get(ctx, act.AppID)
	if err != nil {
		return err
	}
	user := userID
	if act.UserID != "" {
		user = act.UserID
	}
	var num int64
	if err := s.db.QueryRow(ctx, `SELECT num FROM users WHERE id::text = $1 AND active`, user).Scan(&num); err != nil {
		return i18n.E("err.app_activity_user")
	}
	t, err := s.issue(ctx, a.ID, user, AccessTTL)
	if err != nil {
		return err
	}
	auth := Auth(a, t, base)
	auth["client_endpoint"], auth["user_id"] = strings.TrimRight(base, "/")+"/rest/", num
	body := map[string]any{"code": act.Code, "ts": time.Now().Unix(), "auth": auth,
		"use_subscription": map[bool]string{true: "Y", false: "N"}[act.Subscribe]}
	for k, v := range data {
		body[k] = v
	}
	status, err := s.postAs(ctx, act.Handler, a.ClientSecret, "ONBIZPROCACTIVITY", "application/x-www-form-urlencoded", []byte(PHPForm(body).Encode()))
	if err == nil && status >= 300 {
		err = fmt.Errorf("HTTP %d", status)
	}
	if err != nil {
		return i18n.E("err.app_activity_call", act.Name, err.Error())
	}
	return nil
}

// CallSMS posts a message of the application's SMS provider (messageservice.sender.add) to its handler
// does: a form of PHP arrays (type, code, message_id, message_to, message_body…) with «auth» — the tokens of the
// employee who sends, of none when a robot does.
func (s *Store) CallSMS(ctx context.Context, appID, handler, userID, base string, data map[string]any) error {
	a, err := s.Get(ctx, appID)
	if err != nil {
		return err
	}
	if !a.Installed() {
		return i18n.E("err.sms_app_off")
	}
	auth := bareAuth(a, base)
	var num int64
	if userID != "" && s.db.QueryRow(ctx, `SELECT num FROM users WHERE id::text = $1 AND active`, userID).Scan(&num) == nil {
		t, err := s.issue(ctx, a.ID, userID, AccessTTL)
		if err != nil {
			return err
		}
		auth = Auth(a, t, base)
		auth["user_id"] = num
	}
	auth["client_endpoint"] = strings.TrimRight(base, "/") + "/rest/"
	body := map[string]any{"ts": time.Now().Unix(), "auth": auth}
	for k, v := range data {
		body[k] = v
	}
	status, err := s.postAs(ctx, handler, a.ClientSecret, "ONMESSAGESERVICE", "application/x-www-form-urlencoded", []byte(PHPForm(body).Encode()))
	if err == nil && status >= 300 {
		err = fmt.Errorf("HTTP %d", status)
	}
	return err
}
