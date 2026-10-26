package web

import (
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// ---------- «Приложения», as «Разработчикам → Локальное приложение» ----------

//go:embed widget/go24.js
var appSDKJS string

// appsSettings is «Настройки → Приложения» (administrators): the local applications, their codes and keys.
func (s *Server) appsSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.Apps.List(r.Context())
	if err != nil {
		s.fail(w, "apps", err)
		return
	}
	refs, err := s.Apps.Access(r.Context())
	if err != nil {
		s.fail(w, "apps access", err)
		return
	}
	all, err := s.Apps.Placements(r.Context(), "")
	if err != nil {
		s.fail(w, "app placements", err)
		return
	}
	places := map[string][]apps.Placement{} // «Места встраивания» the applications bound
	for _, p := range all {
		places[p.AppID] = append(places[p.AppID], p)
	}
	evs, err := s.Apps.AppEvents(r.Context(), "")
	if err != nil {
		s.fail(w, "app events", err)
		return
	}
	events := map[string][]apps.Event{} // the events the applications subscribed to
	for _, e := range evs {
		events[e.AppID] = append(events[e.AppID], e)
	}
	bots := map[string][]matrixsync.Bot{} // the applications' chat bots
	if s.Matrix != nil {
		all, err := s.Matrix.Bots(r.Context(), "")
		if err != nil {
			s.fail(w, "app bots", err)
			return
		}
		for _, b := range all {
			bots[b.AppID] = append(bots[b.AppID], b)
		}
	}
	access := map[string][]tasks.User{} // the chips of «Кому доступно»
	for app, list := range refs {
		for _, ref := range list {
			access[app] = append(access[app], tasks.User{ID: ref.ID, Name: ref.Name})
		}
	}
	s.render(w, r, "apps_settings", page{TitleKey: "apps.title", Nav: "apps_settings", Data: map[string]any{
		"Apps": list, "Empty": apps.App{}, "Scopes": apikeys.AllScopes, "Base": strings.TrimRight(s.BaseURL, "/"), "Access": access,
		"Places": places, "Events": events, "Bots": bots}})
}

// appSave: op=save (a new one with id «new»), access («Кому доступно»: to — people and «dept:ID»), install,
// uninstall, secret (a new key), delete.
func (s *Server) appSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/settings/apps", http.StatusSeeOther)
		return
	}
	ctx, id := r.Context(), r.PathValue("id")
	var a apps.App
	if id != "new" {
		var err error
		if a, err = s.Apps.Get(ctx, id); err != nil {
			s.taskErr(w, r, err, "/settings/apps")
			return
		}
	}
	var err error
	msg, kind := "", ""
	switch r.FormValue("op") {
	case "install":
		if err = s.Apps.Install(ctx, a, sess.UserID, s.BaseURL, s.lang(r)); err == nil {
			kind, msg = "app_installed", s.tr(r, "apps.installed_ok")
		}
	case "uninstall":
		if err = s.Apps.Uninstall(ctx, id, s.BaseURL, s.lang(r)); err == nil {
			kind = "app_uninstalled"
		}
	case "access":
		if err = s.Apps.SetAccess(ctx, id, r.Form["to"]); err == nil {
			kind, msg = "app_saved", s.tr(r, "common.saved")
		}
	case "secret":
		if err = s.Apps.NewSecret(ctx, id); err == nil {
			kind, msg = "app_saved", s.tr(r, "apps.secret_new")
		}
	case "delete":
		if err = s.Apps.Delete(ctx, id); err == nil {
			kind = "app_deleted"
		}
	default:
		if id == "new" {
			id = ""
		}
		a = apps.App{ID: id, Name: r.FormValue("name"), HandlerURL: r.FormValue("handler_url"), InstallURL: r.FormValue("install_url"),
			MenuTitle: r.FormValue("menu_title"), APIOnly: r.FormValue("api_only") == "on", Scopes: r.Form["scopes"]}
		if id, err = s.Apps.Save(ctx, sess.UserID, a); err == nil {
			kind = "app_saved"
		}
	}
	if kind != "" {
		s.audit(r, audit.Event{Kind: kind, UserID: sess.UserID, Target: a.Name})
	}
	if err != nil {
		msg = s.errText(r, err)
	}
	back := "/settings/apps"
	if msg != "" {
		back += "?msg=" + url.QueryEscape(msg)
	}
	if id != "" && r.FormValue("op") != "delete" {
		back += "#app-" + id
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// appsPage lists the installed applications an employee opens (those for them).
func (s *Server) appsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Apps.Installed(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "apps", err)
		return
	}
	s.render(w, r, "apps", page{TitleKey: "apps.list", Nav: "apps", Data: map[string]any{"Apps": list, "Admin": sess.Admin}})
}

// appPage opens an application in a frame: the frame gets a form POSTed to the handler with the
// employee's tokens (AUTH_ID, REFRESH_ID, AUTH_EXPIRES, member_id, PLACEMENT), the address carries DOMAIN, PROTOCOL
// and LANG.
func (s *Server) appPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	a, err := s.Apps.Get(ctx, r.PathValue("id"))
	if err != nil || !a.Installed() || a.APIOnly {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	if ok, err := s.Apps.MayUse(ctx, a.ID, sess.UserID); err != nil || !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	placement := r.URL.Query().Get("placement")
	if placement == "" {
		placement = "DEFAULT"
	}
	options := r.URL.Query().Get("options")
	if options == "" {
		options = "{}"
	}
	s.appFrame(w, r, sess, a, a.HandlerURL, placement, options, false)
}

// appTab is the tab of an application in a card (a place it bound its handler to): the frame of the card opens it
// with the card's id, the handler gets PLACEMENT and PLACEMENT_OPTIONS of the card ({"ID": …}, {"taskId": …}).
func (s *Server) appTab(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, a, err := s.Apps.Placement(r.Context(), r.PathValue("id"), sess.UserID)
	if err != nil {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	options, _ := json.Marshal(apps.Options(p.Placement, r.URL.Query().Get("id")))
	s.appFrame(w, r, sess, a, p.Handler, p.Placement, string(options), true)
}

// appFrame renders the page posting the employee's new tokens to the handler: the application's own page
// (in the frame of go24's page), or bare — the frame of a card's tab posts itself.
func (s *Server) appFrame(w http.ResponseWriter, r *http.Request, sess auth.Session, a apps.App, handler, placement, options string, bare bool) {
	t, err := s.Apps.Issue(r.Context(), a.ID, sess.UserID)
	if err != nil {
		s.fail(w, "app tokens", err)
		return
	}
	base, _ := url.Parse(s.BaseURL)
	target, err := url.Parse(handler)
	if err != nil || base == nil {
		s.fail(w, "app url", err)
		return
	}
	q := target.Query()
	q.Set("DOMAIN", base.Host)
	q.Set("PROTOCOL", map[bool]string{true: "1", false: "0"}[base.Scheme == "https"])
	q.Set("LANG", s.lang(r))
	q.Set("APP_SID", t.Access[len(t.Access)-16:])
	target.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store") // the page carries tokens
	s.render(w, r, "app", page{Title: a.Title(), Nav: "app:" + a.ID, Bare: bare, Data: map[string]any{"App": a, "Action": target.String(),
		"Placement": placement, "Options": options,
		"Fields": [][2]string{{"AUTH_ID", t.Access}, {"AUTH_EXPIRES", strconv.Itoa(int(apps.AccessTTL.Seconds()))}, {"REFRESH_ID", t.Refresh},
			{"member_id", apps.MemberID(s.BaseURL)}, {"status", "L"}, {"PLACEMENT", placement}, {"PLACEMENT_OPTIONS", options}}}})
}

// appFrameAuth gives go24.js of an application's frame (static/js/apphost.js asks for it) what BX24.getAuth and
// placement.info give: new tokens of the employee, the place and its options, the settings of the application and
// the employee's, and the addresses the frame may be on (the application's hosts). tab= (a card's tab, with id=
// of the card) or app= (its page, with placement= and options=).
func (s *Server) appFrameAuth(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	var (
		a                  apps.App
		placement, options string
		err                error
	)
	if tab := r.FormValue("tab"); tab != "" {
		var p apps.Placement
		if p, a, err = s.Apps.Placement(ctx, tab, sess.UserID); err == nil {
			placement = p.Placement
			o, _ := json.Marshal(apps.Options(p.Placement, r.FormValue("id")))
			options = string(o)
		}
	} else if a, err = s.Apps.Get(ctx, r.FormValue("app")); err == nil {
		if !a.Installed() || a.APIOnly {
			err = tasks.ErrNotFound
		} else if ok, e := s.Apps.MayUse(ctx, a.ID, sess.UserID); e != nil || !ok {
			err = tasks.ErrForbidden
		}
		placement, options = cmp.Or(r.FormValue("placement"), "DEFAULT"), r.FormValue("options")
	}
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": s.tr(r, "err.not_found")})
		return
	}
	var opts any = map[string]any{}
	if options != "" && json.Unmarshal([]byte(options), &opts) != nil {
		opts = map[string]any{}
	}
	t, err := s.Apps.Issue(ctx, a.ID, sess.UserID)
	if err != nil {
		s.fail(w, "app tokens", err)
		return
	}
	appOpts, err := s.Apps.Options(ctx, a.ID, "")
	if err != nil {
		s.fail(w, "app options", err)
		return
	}
	userOpts, err := s.Apps.Options(ctx, a.ID, sess.UserID)
	if err != nil {
		s.fail(w, "app options", err)
		return
	}
	var origins []string
	for _, raw := range []string{a.HandlerURL, a.InstallURL} {
		if u, err := url.Parse(raw); err == nil && raw != "" && u.Host != "" {
			origins = append(origins, u.Scheme+"://"+u.Host)
		}
	}
	base, _ := url.Parse(s.BaseURL)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"origins": origins, "title": a.Title(), "app": a.ID,
		"auth": map[string]any{"access_token": t.Access, "refresh_token": t.Refresh, "expires_in": int(apps.AccessTTL.Seconds()),
			"domain": base.Host, "member_id": apps.MemberID(s.BaseURL), "client_endpoint": strings.TrimRight(s.BaseURL, "/") + "/api/v1/"},
		"placement": placement, "options": opts, "lang": s.lang(r), "user_id": sess.UserID, "admin": s.apiAdmin(ctx, sess.UserID),
		"app_options": appOpts, "user_options": userOpts})
}

// appSDK is go24.js, the library of an application's page in go24's frame, as BX24.js
// (<script src="https://go24…/go24.js">): it talks to go24's page around the frame.
func (s *Server) appSDK(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = io.WriteString(w, appSDKJS)
}

// appTabs are the tabs of the applications in the place for the employee (a card's «Data.AppTabs»).
func (s *Server) appTabs(r *http.Request, userID, place string) []apps.Placement {
	if s.Apps == nil {
		return nil
	}
	list, err := s.Apps.InPlace(r.Context(), place, userID)
	if err != nil {
		s.Log.Error("app tabs", "place", place, "err", err)
	}
	return list
}

// oauthAuthorize is /oauth/authorize of an installed application: an employee signed in to go24 is sent back to the
// application's address (redirect_uri on the handler's or install handler's host) with a code for /oauth/token.
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	a, err := s.Apps.ByClientID(r.Context(), q.Get("client_id"))
	if err != nil || !a.Installed() {
		http.Error(w, s.tr(r, "err.app_client"), http.StatusBadRequest)
		return
	}
	if ok, err := s.Apps.MayUse(r.Context(), a.ID, sess.UserID); err != nil || !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := q.Get("redirect_uri")
	if back == "" {
		back = a.HandlerURL
	}
	u, err := url.Parse(back)
	if err != nil || !a.Owns(u) {
		http.Error(w, s.tr(r, "err.app_redirect"), http.StatusBadRequest)
		return
	}
	code, err := s.Apps.NewCode(r.Context(), a.ID, sess.UserID)
	if err != nil {
		s.fail(w, "app code", err)
		return
	}
	base, _ := url.Parse(s.BaseURL)
	v := u.Query()
	v.Set("code", code)
	if st := q.Get("state"); st != "" {
		v.Set("state", st)
	}
	v.Set("domain", base.Host)
	v.Set("member_id", apps.MemberID(s.BaseURL))
	v.Set("scope", strings.Join(a.Scopes, ","))
	v.Set("server_domain", base.Host)
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// oauthToken is /oauth/token: grant_type=authorization_code (code) or refresh_token (refresh_token),
// with client_id and client_secret; parameters in the query or the form. The answer is the «auth» block.
func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthErr(w, "invalid_request", err.Error())
		return
	}
	ctx := r.Context()
	a, err := s.Apps.Client(ctx, r.Form.Get("client_id"), r.Form.Get("client_secret"))
	if err != nil {
		oauthErr(w, "invalid_client", s.I18n.Err(s.lang(r), err))
		return
	}
	if !a.Installed() {
		oauthErr(w, "invalid_client", s.tr(r, "err.app_client"))
		return
	}
	var t apps.Tokens
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		t, err = s.Apps.Exchange(ctx, a, r.Form.Get("code"))
	case "refresh_token":
		t, err = s.Apps.Refresh(ctx, a, r.Form.Get("refresh_token"))
	default:
		oauthErr(w, "unsupported_grant_type", r.Form.Get("grant_type"))
		return
	}
	if err != nil {
		var ie *i18n.Error
		if !errors.As(err, &ie) {
			s.Log.Error("oauth token", "err", err)
		}
		oauthErr(w, "invalid_grant", s.I18n.Err(s.lang(r), err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, apps.Auth(a, t, s.BaseURL))
}

func oauthErr(w http.ResponseWriter, code, text string) {
	status := http.StatusBadRequest
	if code == "invalid_client" {
		status = http.StatusUnauthorized
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": text})
}
