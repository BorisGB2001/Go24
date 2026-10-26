package web

import (
	"context"
	"net/http"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/menu"
)

// menuEntry is an item of the left menu as the layout draws it: go24's (ID) or an own link (Custom), with the page
// for the counters and the current section.
type menuEntry struct {
	menu.Item
	P *page
}

// menuLabels are the texts of «Настроить меню» for menu.js (menu.<key> in the locales).
var menuLabels = []string{"hide", "show", "first", "item_edit", "item_delete", "title", "url", "url_hint", "newtab", "save",
	"cancel", "add", "reset_confirm", "apply_all_confirm", "reset_all_confirm", "delete_confirm", "star_add", "star_in", "more"}

// menuAvail are go24's items of the menu this employee has: the messenger when it is on, «Приложения» when some
// application is for them (administrators — always, they set them up).
func menuAvail(p *page, apps bool) []string {
	var out []string
	for _, b := range menu.Builtin {
		if (b.ID != "im" || p.Messenger != "") && (b.ID != "apps" || apps || p.User.Admin) {
			out = append(out, b.ID)
		}
	}
	return out
}

// menuApps are the items of the applications for the employee that have a menu item («Название пункта меню»), and
// whether any application is for them.
func (s *Server) menuApps(ctx context.Context, userID string) (items []menu.Custom, any bool) {
	if s.Apps == nil {
		return nil, false
	}
	list, err := s.Apps.Installed(ctx, userID)
	if err != nil {
		return nil, false
	}
	for _, a := range list {
		if a.MenuTitle != "" {
			items = append(items, menu.Custom{ID: menu.AppPrefix + a.ID, Title: a.MenuTitle, URL: "/apps/" + a.ID})
		}
	}
	return items, len(list) > 0
}

// navMenu sets the page's left menu in the employee's order (guests keep their short menu as it is).
func (s *Server) navMenu(ctx context.Context, p *page) {
	if p.User == nil || p.Guest {
		return
	}
	var c menu.Config
	if s.Menus != nil {
		c, p.MenuOwn, _, _ = s.Menus.Get(ctx, p.User.UserID)
	}
	apps, any := s.menuApps(ctx, p.User.UserID)
	shown, hidden := menu.Arrange(c, s.availOn(ctx, menuAvail(p, any)), apps...)
	for _, it := range shown {
		p.Menu = append(p.Menu, menuEntry{it, p})
	}
	for _, it := range hidden {
		p.MenuHidden = append(p.MenuHidden, menuEntry{it, p})
	}
}

// menuStart is the page opened after signing in: the first item of the employee's menu when a menu is set up
// (their own or the company's), else «Мои задачи».
func (s *Server) menuStart(ctx context.Context, userID string) string {
	if s.Menus == nil {
		return "/my"
	}
	c, _, set, err := s.Menus.Get(ctx, userID)
	if err != nil || !set {
		return "/my"
	}
	var avail []string
	for _, b := range menu.Builtin {
		if b.ID != "im" || s.Matrix != nil {
			avail = append(avail, b.ID)
		}
	}
	apps, _ := s.menuApps(ctx, userID)
	shown, _ := menu.Arrange(c, s.availOn(ctx, avail), apps...)
	if len(shown) == 0 || shown[0].Custom != nil && !shown[0].Custom.IsLocal() {
		return "/my" // an outside link is not a start page of go24
	}
	return shown[0].URL()
}

// menuSave keeps the employee's menu as the page sends it (JSON: order, hidden, custom); ?all=1 — an
// administrator's «Применить для всех»: the menu becomes the company's default.
func (s *Server) menuSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var c menu.Config
	if err := readJSON(r, &c); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	c, err := menu.Check(c)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	if r.URL.Query().Get("all") == "1" {
		if !sess.Admin {
			s.jsonErr(w, r, i18n.E("err.forbidden"))
			return
		}
		err = s.Menus.SaveDefault(r.Context(), c, sess.UserID)
	} else {
		err = s.Menus.Save(r.Context(), sess.UserID, c)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// menuReset: «Сбросить настройки меню» — the company's (or go24's) menu is back; ?all=1 — an administrator drops
// the company's default, go24's order is back for those with no menu of their own.
func (s *Server) menuReset(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error
	if r.URL.Query().Get("all") == "1" {
		if !sess.Admin {
			s.jsonErr(w, r, i18n.E("err.forbidden"))
			return
		}
		err = s.Menus.ResetDefault(r.Context())
	} else {
		err = s.Menus.Reset(r.Context(), sess.UserID)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
