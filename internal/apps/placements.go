package apps

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Places are the places of go24 an application can put its handler into, as placement.bind: a tab of
// a CRM card and of a task, and the widgets of the messenger (IMPlaces). A smart process's card is
// CRM_DYNAMIC_<type>_DETAIL_TAB (DynamicPlace).
var Places = append([]string{"CRM_LEAD_DETAIL_TAB", "CRM_DEAL_DETAIL_TAB", "CRM_CONTACT_DETAIL_TAB", "CRM_COMPANY_DETAIL_TAB", "TASK_VIEW_TAB"},
	IMPlaces...)

// IMPlaces are the widgets of the messenger: a button of the panel by the input field (IM_TEXTAREA), of the chat's
// header opening the side panel (IM_SIDEBAR), an item of a message's menu (IM_CONTEXT_MENU) and of the messenger's
// own header — the application instead of a chat (IM_NAVIGATION).
var IMPlaces = []string{"IM_TEXTAREA", "IM_SIDEBAR", "IM_CONTEXT_MENU", "IM_NAVIGATION"}

// imOptions are the OPTIONS of placement.bind a widget of the messenger keeps.
var imOptions = []string{"iconName", "context", "role", "color", "width", "height", "extranet"}

// imContexts are the kinds of chats of OPTIONS.context.
var imContexts = []string{"USER", "CHAT", "LINES", "CRM", "ALL"}

// DynamicPlace is the tab of the cards of a smart process.
func DynamicPlace(typeID string) string { return "CRM_DYNAMIC_" + typeID + "_DETAIL_TAB" }

// CardPlace is the place of a CRM card: entity is lead, deal, contact or company; a deal of a smart process is the
// place of its type.
func CardPlace(entity, typeID string) string {
	if entity == "deal" && typeID != "" {
		return DynamicPlace(typeID)
	}
	return "CRM_" + strings.ToUpper(entity) + "_DETAIL_TAB"
}

// Options are PLACEMENT_OPTIONS an application's tab gets for the card it is opened in: {"ID": …} of a CRM card,
// {"taskId": …} of a task.
func Options(place, id string) map[string]string {
	if slices.Contains(IMPlaces, place) { // the chat and the message are the messenger's (web: imAppOptions)
		return map[string]string{}
	}
	if place == "TASK_VIEW_TAB" {
		return map[string]string{"taskId": id}
	}
	return map[string]string{"ID": id}
}

// Placement is a handler of an application bound to a place.
type Placement struct {
	ID          string    `json:"id"`
	AppID       string    `json:"-"`
	Placement   string    `json:"placement"`
	Handler     string    `json:"handler"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
	// Options are OPTIONS of placement.bind of a widget of the messenger (imOptions), as given
	Options  PlaceOptions `json:"options"`
	AppTitle string       `json:"-"` // the tab's title without its own one
}

// PlaceOptions are OPTIONS of placement.bind: strings, numbers and booleans are taken as text (width 200 as "200").
type PlaceOptions map[string]string

func (o *PlaceOptions) UnmarshalJSON(b []byte) error {
	var in map[string]any
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*o = PlaceOptions{}
	for k, v := range in {
		switch x := v.(type) {
		case string:
			(*o)[k] = x
		case float64:
			(*o)[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			(*o)[k] = map[bool]string{true: "Y", false: "N"}[x]
		}
	}
	return nil
}

// Opt is an option of the widget, or what the portal takes without it.
func (p Placement) Opt(name string) string {
	if v := p.Options[name]; v != "" {
		return v
	}
	return map[string]string{"context": "ALL", "role": "USER", "extranet": "N", "width": "100", "height": "100"}[name]
}

// ForChat: the widget is for a chat of the kind (USER, CHAT, LINES or CRM), by its OPTIONS.context.
func (p Placement) ForChat(kind string) bool {
	for _, c := range strings.Split(p.Opt("context"), ";") {
		if c = strings.ToUpper(strings.TrimSpace(c)); c == "ALL" || c == kind {
			return true
		}
	}
	return false
}

// For: the widget is for the employee — OPTIONS.role ADMIN is for administrators, extranet Y lets guests see it.
func (p Placement) For(admin, guest bool) bool {
	return (admin || !strings.EqualFold(p.Opt("role"), "ADMIN")) && (!guest || strings.EqualFold(p.Opt("extranet"), "Y"))
}

// Width and Height are the frame's OPTIONS.width and height of IM_TEXTAREA in pixels, within what the panel shows.
func (p Placement) Width() int { return p.px("width", 100, 800) }

func (p Placement) Height() int { return p.px("height", 50, 600) }

func (p Placement) px(name string, lo, hi int) int {
	v, err := strconv.Atoi(p.Opt(name))
	if err != nil {
		v = 100
	}
	return min(max(v, lo), hi)
}

// Tab is what the card's tab is called.
func (p Placement) Tab() string {
	if p.Title != "" {
		return p.Title
	}
	return p.AppTitle
}

// Kind is the place without the smart process's type (CRM_DYNAMIC_DETAIL_TAB) — what it is called.
func (p Placement) Kind() string {
	if strings.HasPrefix(p.Placement, "CRM_DYNAMIC_") {
		return "CRM_DYNAMIC_DETAIL_TAB"
	}
	return p.Placement
}

// Owns: the address is on the host of the application's handler or install handler (http(s) only) — where its
// redirect_uri and the handlers of its places may be.
func (a App) Owns(u *url.URL) bool {
	if u == nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return false
	}
	for _, raw := range []string{a.HandlerURL, a.InstallURL} {
		if h, err := url.Parse(raw); err == nil && raw != "" && h.Scheme == u.Scheme && strings.EqualFold(h.Host, u.Host) {
			return true
		}
	}
	return false
}

// AllPlaces are the places to bind to: Places and the tabs of every smart process.
func (s *Store) AllPlaces(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM crm_types ORDER BY name`)
	if err != nil {
		return nil, err
	}
	types, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	list := slices.Clone(Places)
	for _, t := range types {
		list = append(list, DynamicPlace(t))
	}
	return list, nil
}

// Bind puts the application's handler into the place (placement.bind); the same handler bound again gets the new
// title and description. The handler is on the application's host.
func (s *Store) Bind(ctx context.Context, a App, p Placement) (Placement, error) {
	p.Placement, p.Handler = strings.ToUpper(strings.TrimSpace(p.Placement)), strings.TrimSpace(p.Handler)
	p.Title, p.Description = strings.TrimSpace(p.Title), strings.TrimSpace(p.Description)
	places, err := s.AllPlaces(ctx)
	if err != nil {
		return p, err
	}
	if !slices.Contains(places, p.Placement) {
		return p, i18n.E("err.app_placement", p.Placement)
	}
	if u, err := url.Parse(p.Handler); err != nil || checkURL(p.Handler, true) != nil || !a.Owns(u) {
		return p, i18n.E("err.app_handler_host")
	}
	if len([]rune(p.Title)) > 100 || len([]rune(p.Description)) > 500 {
		return p, i18n.E("err.app_name")
	}
	if p.Options, err = placeOptions(p.Placement, p.Options); err != nil {
		return p, err
	}
	err = s.db.QueryRow(ctx, `INSERT INTO app_placements (app_id, placement, handler_url, title, description, options) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (app_id, placement, handler_url) DO UPDATE SET title = EXCLUDED.title, description = EXCLUDED.description, options = EXCLUDED.options
		RETURNING id::text, created_at`, a.ID, p.Placement, p.Handler, p.Title, p.Description, p.Options).Scan(&p.ID, &p.CreatedAt)
	p.AppID = a.ID
	return p, err
}

// placeOptions are the OPTIONS a place keeps: of a widget of the messenger, its imOptions (context — of imContexts,
// role USER or ADMIN, extranet Y or N, sizes in pixels); other places keep none.
func placeOptions(place string, in PlaceOptions) (PlaceOptions, error) {
	out := PlaceOptions{}
	if !slices.Contains(IMPlaces, place) {
		return out, nil
	}
	for k, v := range in {
		i := slices.IndexFunc(imOptions, func(o string) bool { return strings.EqualFold(o, k) })
		if v = strings.TrimSpace(v); i < 0 || v == "" {
			continue
		}
		name := imOptions[i]
		bad := len(v) > 100
		switch name {
		case "context":
			v = strings.ToUpper(v)
			for _, c := range strings.Split(v, ";") {
				bad = bad || !slices.Contains(imContexts, strings.TrimSpace(c))
			}
		case "role":
			v = strings.ToUpper(v)
			bad = bad || v != "USER" && v != "ADMIN"
		case "extranet":
			v = strings.ToUpper(v)
			bad = bad || v != "Y" && v != "N"
		case "width", "height":
			n, err := strconv.Atoi(v)
			bad = bad || err != nil || n <= 0
		case "color":
			v = strings.ToUpper(v)
		}
		if bad {
			return nil, i18n.E("err.app_place_option", name, v)
		}
		out[name] = v
	}
	return out, nil
}

// Unbind takes the application's handler off the place (placement.unbind); without a handler — every handler of
// the place. It tells how many were taken off.
func (s *Store) Unbind(ctx context.Context, appID, place, handler string) (int64, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM app_placements WHERE app_id::text = $1 AND placement = upper($2) AND ($3 = '' OR handler_url = $3)`,
		appID, strings.TrimSpace(place), strings.TrimSpace(handler))
	return tag.RowsAffected(), err
}

const placeCols = `p.id::text, p.app_id::text, p.placement, p.handler_url, p.title, p.description, p.created_at, p.options,
	CASE WHEN a.menu_title <> '' THEN a.menu_title ELSE a.name END`

func (s *Store) placements(ctx context.Context, where string, args ...any) ([]Placement, error) {
	rows, err := s.db.Query(ctx, `SELECT `+placeCols+` FROM app_placements p JOIN apps a ON a.id = p.app_id `+where+
		` ORDER BY a.created_at, p.created_at`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Placement])
}

// Placements are the places of an application (placement.get), or of all, by application, with appID "".
func (s *Store) Placements(ctx context.Context, appID string) ([]Placement, error) {
	return s.placements(ctx, "WHERE $1 = '' OR p.app_id::text = $1", appID)
}

// InPlace are the tabs of the place the employee sees: of installed applications that are for them.
func (s *Store) InPlace(ctx context.Context, place, userID string) ([]Placement, error) {
	return s.placements(ctx, "WHERE p.placement = $2 AND a.installed_at IS NOT NULL AND "+appFor, userID, place)
}

// Placement is a tab to open for the employee, with its application; ErrNotFound when it is not for them.
func (s *Store) Placement(ctx context.Context, id, userID string) (Placement, App, error) {
	list, err := s.placements(ctx, "WHERE p.id::text = $2 AND a.installed_at IS NOT NULL AND "+appFor, userID, id)
	if err != nil {
		return Placement{}, App{}, err
	}
	if len(list) == 0 {
		return Placement{}, App{}, i18n.E("err.not_found")
	}
	a, err := s.Get(ctx, list[0].AppID)
	return list[0], a, err
}
