package web

import (
	"strings"
	"testing"

	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// TestIMAppWidgets: the widgets of applications in the messenger — IM_NAVIGATION in the messenger's
// header (the application instead of a chat), IM_SIDEBAR in the chat's header, IM_TEXTAREA by the input field (its
// frame's size), IM_CONTEXT_MENU kept for a message's «…»; the icon of Font Awesome is go24's when it has one, the
// color is the portal's; no widgets — no host of the frames.
func TestIMAppWidgets(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	render := func(data map[string]any) string {
		t.Helper()
		var b strings.Builder
		if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "content", page{User: &auth.Session{}, Data: data}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	r := matrixsync.Room{ID: "!g:x", Name: "Отдел", Kind: "group"}
	w := func(id, place, title string, o apps.PlaceOptions) apps.Placement {
		return apps.Placement{ID: id, Placement: place, Title: title, Options: o}
	}
	nav := w("n1", "IM_NAVIGATION", "Склад", apps.PlaceOptions{"iconName": "fa-cloud", "color": "aqua"})
	widgets := map[string][]apps.Placement{"IM_NAVIGATION": {nav},
		"IM_SIDEBAR":      {w("s1", "IM_SIDEBAR", "База знаний", apps.PlaceOptions{"iconName": "fa-no-such"})},
		"IM_TEXTAREA":     {w("t1", "IM_TEXTAREA", "Шаблоны", apps.PlaceOptions{"width": "250", "height": "120"})},
		"IM_CONTEXT_MENU": {w("c1", "IM_CONTEXT_MENU", "В CRM", nil)}}
	out := render(map[string]any{"Rooms": []matrixsync.Room{r}, "Room": r.ID, "Current": r, "Tab": "all", "Apps": widgets})
	for _, want := range []string{`href="/im?app=n1"`, `i-cloud-upload`, `style="--app-color:#12a5b8"`,
		`data-im-app="s1" data-place="IM_SIDEBAR" data-title="База знаний"`, `i-layout-grid`,
		`data-im-app="t1" data-place="IM_TEXTAREA" data-title="Шаблоны" data-w="250" data-h="120"`, `data-app-pop`,
		`<template data-ctx-apps><button type="button" class="im-ctxapp" data-im-app="c1" data-place="IM_CONTEXT_MENU"`,
		`data-app-pane`, `data-app-select`, "js/apphost.js"} {
		if !strings.Contains(out, want) {
			t.Errorf("the chat lacks %q", want)
		}
	}
	if strings.Contains(out, "data-im-nav") {
		t.Error("a chat open and the application of the navigation shown")
	}

	out = render(map[string]any{"Tab": "all", "Apps": map[string][]apps.Placement{"IM_NAVIGATION": {nav}}, "NavApp": nav})
	for _, want := range []string{`class="small on"`, `data-tab="n1" data-src="/apps/tab/n1" data-im-nav`, "Склад"} {
		if !strings.Contains(out, want) {
			t.Errorf("the application instead of a chat lacks %q", want)
		}
	}
	if strings.Contains(out, "data-app-pane") || strings.Contains(out, "im.pick") || strings.Contains(out, "Выберите чат") {
		t.Error("no chat: the side panel of widgets or the «pick a chat» line")
	}

	out = render(map[string]any{"Rooms": []matrixsync.Room{r}, "Room": r.ID, "Current": r, "Tab": "all", "Apps": map[string][]apps.Placement{}})
	if strings.Contains(out, "js/apphost.js") || strings.Contains(out, "data-im-app") {
		t.Error("no widgets, yet their buttons or host")
	}
}

// TestIMAppIcon: Font Awesome's names become go24's icons (like ones for those it lacks), others the application's one.
func TestIMAppIcon(t *testing.T) {
	for in, want := range map[string]string{"fa-star": "star", "FA-COG": "settings", "fa fa-check": "check", "fa-cloud": "cloud-upload", "": "layout-grid", "fa-no-such": "layout-grid"} {
		if got := imAppIcon(in); got != want {
			t.Errorf("imAppIcon(%q) = %q, want %q", in, got, want)
		}
	}
}
