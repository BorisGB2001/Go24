package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/portal"
)

// settingsLink is an entry of «Настройки»: a page of settings under its section.
type settingsLink struct {
	Href, TitleKey, Icon string
	Admin                bool // only for administrators
}

// settingsSections are the sections of «Настройки», as the settings of a portal are grouped.
var settingsSections = []struct {
	TitleKey string
	Links    []settingsLink
}{
	{"settings.sec.portal", []settingsLink{
		{"/settings/portal", "portal.title", "home", true},
		{"/settings/admins", "admins.title", "shield-lock", true},
		{"/settings/tools", "sections.title", "adjustments", true},
		{"/settings/ip", "ipaccess.title", "lock", true},
		{"/settings/backup", "backup.title", "database", true},
		{"/settings/notifications", "notify.settings", "bell", false},
		{"/settings/notify-bots", "notifyout.bots", "send", true},
	}},
	{"settings.sec.company", []settingsLink{
		{"/company/invite", "invite.title", "users", true},
		{"/settings/user-fields", "nav.user_fields", "list-check", true},
		{"/settings/worktime", "title.worktime", "clock", true},
		{"/settings/rooms", "rooms.title", "calendar-event", true},
		{"/settings/calls", "call.settings", "phone", true},
		{"/settings/call-links", "call.ext_my", "video", false},
		{"/settings/telephony", "tel.title", "phone", true},
		{"/settings/collabs", "collab.creators.title", "users", true},
		{"/booking/rights", "booking.rights_settings", "shield-lock", true},
	}},
	{"settings.sec.tasks", []settingsLink{
		{"/settings/task-fields", "nav.task_fields", "list-check", true},
		{"/templates", "templates.title", "copy", false},
		{"/tasks/robots", "taskrobot.title", "robot", false},
		{"/tasks/trash", "trash.title", "trash", false},
	}},
	{"settings.sec.crm", []settingsLink{
		{"/crm/roles", "crmroles.title", "shield-lock", true},
		{"/crm/types", "crm.types", "layout-kanban", true},
		{"/crm/dicts", "crm.dicts", "list", true},
		{"/crm/currencies", "crm.currencies", "chart-bar", true},
		{"/crm/products", "crm.products", "box", false},
		{"/crm/mail-templates", "crm.mailtpl.title", "mail", false},
		{"/bp/templates", "bp.templates", "hierarchy-2", false},
	}},
	{"settings.sec.disk", []settingsLink{
		{"/settings/disk", "disk.limits", "database", true},
		{"/settings/storages", "title.storages", "server-2", false},
	}},
	{"settings.sec.dev", []settingsLink{
		{"/settings/keys", "nav.api_keys", "key", false},
		{"/settings/webhooks", "hooks.title", "bolt", true},
		{"/settings/apps", "apps.title", "layout-grid", true},
		{"/settings/events", "audit.title", "shield-lock", true},
		{"/settings/ai", "ai.title", "sparkles", false},
	}},
}

// settingsPage is «Настройки»: all the pages of settings by sections; an employee sees those open to them.
func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	type section struct {
		TitleKey string
		Links    []settingsLink
	}
	var list []section
	invite := sess.Admin
	if s.Company != nil {
		invite, _ = s.Company.CanInvite(r.Context(), sess.UserID, sess.Admin) // an HR officer with «invite» too
	}
	for _, sec := range settingsSections {
		var links []settingsLink
		for _, l := range sec.Links {
			if l.Href == "/settings/telephony" && s.Telephony == nil { // no Asterisk: nothing to set
				continue
			}
			if _, off := s.offSection(r.Context(), l.Href); off { // its section is switched off in «Инструменты»
				continue
			}
			if !l.Admin || sess.Admin || l.Href == "/company/invite" && invite {
				links = append(links, l)
			}
		}
		if len(links) > 0 {
			list = append(list, section{sec.TitleKey, links})
		}
	}
	s.render(w, r, "settings", page{TitleKey: "settings.title", Nav: "settings", Data: map[string]any{"Sections": list}})
}

// portalSettings reads the settings for a page or the API (the defaults when the portal has no store).
func (s *Server) portalSettings(r *http.Request) (portal.Settings, error) {
	if s.Portal == nil {
		return portal.Settings{}, nil
	}
	return s.Portal.Get(r.Context())
}

// portalPage is «Общие настройки» of the portal (administrators).
func (s *Server) portalPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	set, err := s.portalSettings(r)
	if err != nil {
		s.fail(w, "portal settings", err)
		return
	}
	flash := ""
	if r.URL.Query().Get("saved") == "1" {
		flash = s.tr(r, "portal.saved")
	}
	s.render(w, r, "portal", page{TitleKey: "portal.title", Nav: "portal", Flash: flash, Data: map[string]any{
		"S": set, "DateFormats": portal.DateFormatNames, "Samples": portalSamples(), "Zones": portalZones,
		"MaxLogo": portal.MaxLogo >> 10}})
}

// portalSamples show each date format on today's date.
func portalSamples() map[string]string {
	m := map[string]string{}
	for k, f := range portal.DateFormats {
		m[k] = time.Now().Format(f)
	}
	return m
}

// portalZones are offered in the form; any other zone of the IANA base may be typed.
var portalZones = []string{"Europe/Kaliningrad", "Europe/Moscow", "Europe/Samara", "Asia/Yekaterinburg", "Asia/Omsk",
	"Asia/Novosibirsk", "Asia/Krasnoyarsk", "Asia/Irkutsk", "Asia/Yakutsk", "Asia/Vladivostok", "Asia/Magadan",
	"Asia/Kamchatka", "Europe/Minsk", "Europe/Kyiv", "Asia/Almaty", "Asia/Tashkent", "Asia/Tbilisi", "Asia/Yerevan",
	"Asia/Baku", "Europe/Istanbul", "Europe/Amsterdam", "Europe/Berlin", "Europe/Paris", "Europe/London", "UTC",
	"America/New_York", "America/Chicago", "America/Los_Angeles", "Asia/Dubai", "Asia/Shanghai", "Asia/Tokyo"}

// portalSave stores the form: the settings, then a new logo or its removal.
func (s *Server) portalSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin || s.Portal == nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, portal.MaxLogo+64<<10)
	if err := r.ParseMultipartForm(portal.MaxLogo + 64<<10); err != nil && err != http.ErrNotMultipart {
		s.taskErr(w, r, i18n.E("err.portal_logo_size"), "/settings/portal")
		return
	}
	set := portal.Settings{Name: r.FormValue("name"), Lang: r.FormValue("lang"), TZ: r.FormValue("tz"),
		DateFormat: r.FormValue("date_format"), TimeFormat: r.FormValue("time_format")}
	err := portal.Check(set, s.I18n.Supported)
	if err == nil {
		err = s.Portal.Save(r.Context(), set, sess.UserID)
	}
	if err == nil && r.FormValue("logo_delete") != "" {
		err = s.Portal.DeleteLogo(r.Context())
	} else if f, _, ferr := r.FormFile("logo"); err == nil && ferr == nil {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(f, portal.MaxLogo+1))
		f.Close()
		if err == nil {
			err = s.Portal.SaveLogo(r.Context(), data, sess.UserID)
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/settings/portal")
		return
	}
	s.audit(r, audit.Event{Kind: "portal_saved", UserID: sess.UserID})
	http.Redirect(w, r, "/settings/portal?saved=1", http.StatusSeeOther)
}

// portalLogo serves the logo to everyone (the sign-in page shows it too).
func (s *Server) portalLogo(w http.ResponseWriter, r *http.Request) {
	if s.Portal == nil {
		http.NotFound(w, r)
		return
	}
	data, ctype, err := s.Portal.Logo(r.Context())
	if err != nil {
		s.fail(w, "portal logo", err)
		return
	}
	if data == nil {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(data)
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox") // an SVG runs no script
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// apiPortal: GET gives the settings; PUT (administrators) changes the fields given ({"name": "…"}) and gives them all.
func (s *Server) apiPortal(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	set, err := s.portalSettings(r)
	if err != nil {
		return err
	}
	if r.Method == http.MethodPut {
		if s.Portal == nil || !s.apiAdmin(r.Context(), p.UserID) {
			return i18n.E("err.forbidden")
		}
		if err := readJSON(r, &set); err != nil {
			return err
		}
		if err := portal.Check(set, s.I18n.Supported); err != nil {
			return err
		}
		if err := s.Portal.Save(r.Context(), set, p.UserID); err != nil {
			return err
		}
		s.audit(r, audit.Event{Kind: "portal_saved", UserID: p.UserID})
		if set, err = s.Portal.Get(r.Context()); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, set)
	return nil
}
