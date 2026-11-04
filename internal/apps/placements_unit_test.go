package apps

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"
)

// TestPlaces verifies the predefined set of placement locations.
func TestPlaces(t *testing.T) {
	expected := []string{
		"CRM_LEAD_DETAIL_TAB",
		"CRM_DEAL_DETAIL_TAB",
		"CRM_CONTACT_DETAIL_TAB",
		"CRM_COMPANY_DETAIL_TAB",
		"TASK_VIEW_TAB",
		"IM_TEXTAREA",
		"IM_SIDEBAR",
		"IM_CONTEXT_MENU",
		"IM_NAVIGATION",
	}

	if len(Places) != len(expected) {
		t.Fatalf("len(Places) = %d, want %d", len(Places), len(expected))
	}

	for _, p := range expected {
		if !slices.Contains(Places, p) {
			t.Errorf("Places missing %q", p)
		}
	}
}

// TestDynamicPlace verifies dynamic CRM smart process tab name formation.
func TestDynamicPlace(t *testing.T) {
	cases := []struct {
		typeID string
		want   string
	}{
		{"128", "CRM_DYNAMIC_128_DETAIL_TAB"},
		{"custom_process", "CRM_DYNAMIC_custom_process_DETAIL_TAB"},
		{"", "CRM_DYNAMIC__DETAIL_TAB"},
	}

	for _, c := range cases {
		if got := DynamicPlace(c.typeID); got != c.want {
			t.Errorf("DynamicPlace(%q) = %q, want %q", c.typeID, got, c.want)
		}
	}
}

// TestCardPlace verifies CRM card placement resolution for standard entities and smart processes.
func TestCardPlace(t *testing.T) {
	cases := []struct {
		entity string
		typeID string
		want   string
	}{
		{"deal", "14", "CRM_DYNAMIC_14_DETAIL_TAB"},
		{"deal", "", "CRM_DEAL_DETAIL_TAB"},
		{"lead", "", "CRM_LEAD_DETAIL_TAB"},
		{"lead", "14", "CRM_LEAD_DETAIL_TAB"},
		{"contact", "", "CRM_CONTACT_DETAIL_TAB"},
		{"contact", "99", "CRM_CONTACT_DETAIL_TAB"},
		{"company", "", "CRM_COMPANY_DETAIL_TAB"},
		{"company", "100", "CRM_COMPANY_DETAIL_TAB"},
		{"custom", "", "CRM_CUSTOM_DETAIL_TAB"},
	}

	for _, c := range cases {
		if got := CardPlace(c.entity, c.typeID); got != c.want {
			t.Errorf("CardPlace(%q, %q) = %q, want %q", c.entity, c.typeID, got, c.want)
		}
	}
}

// TestPlacementTab verifies tab title resolution (custom placement title falling back to app title).
func TestPlacementTab(t *testing.T) {
	tests := []struct {
		name     string
		p        Placement
		expected string
	}{
		{
			name:     "both set, title wins",
			p:        Placement{Title: "Billing History", AppTitle: "QuickBooks Integration"},
			expected: "Billing History",
		},
		{
			name:     "title empty, falls back to AppTitle",
			p:        Placement{Title: "", AppTitle: "QuickBooks Integration"},
			expected: "QuickBooks Integration",
		},
		{
			name:     "both empty",
			p:        Placement{Title: "", AppTitle: ""},
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Tab(); got != tc.expected {
				t.Errorf("Tab() = %q, want %q", got, tc.expected)
			}
		})
	}
}

// TestPlacementKind verifies normalizing smart process placements to CRM_DYNAMIC_DETAIL_TAB.
func TestPlacementKind(t *testing.T) {
	tests := []struct {
		placement string
		wantKind  string
	}{
		{"CRM_DYNAMIC_128_DETAIL_TAB", "CRM_DYNAMIC_DETAIL_TAB"},
		{"CRM_DYNAMIC_INVOICE_DETAIL_TAB", "CRM_DYNAMIC_DETAIL_TAB"},
		{"CRM_LEAD_DETAIL_TAB", "CRM_LEAD_DETAIL_TAB"},
		{"CRM_DEAL_DETAIL_TAB", "CRM_DEAL_DETAIL_TAB"},
		{"TASK_VIEW_TAB", "TASK_VIEW_TAB"},
		{"CUSTOM_TAB", "CUSTOM_TAB"},
	}

	for _, tc := range tests {
		p := Placement{Placement: tc.placement}
		if got := p.Kind(); got != tc.wantKind {
			t.Errorf("Kind() for placement %q = %q, want %q", tc.placement, got, tc.wantKind)
		}
	}
}

// TestPlacementJSON verifies JSON marshaling and field tags for Placement.
func TestPlacementJSON(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	p := Placement{
		ID:          "p-123",
		AppID:       "app-456", // json:"-"
		Placement:   "CRM_DEAL_DETAIL_TAB",
		Handler:     "https://app.example.com/deal-tab",
		Title:       "Deals Tab",
		Description: "Tab description",
		CreatedAt:   now,
		AppTitle:    "Secret App Title", // json:"-"
	}

	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal(Placement): %v", err)
	}

	rawStr := string(raw)
	// Internal fields should be omitted from JSON
	for _, excluded := range []string{"app-456", "Secret App Title"} {
		if slices.Contains([]rune(rawStr), []rune(excluded)[0]) && jsonContainsString(rawStr, excluded) {
			t.Errorf("Placement JSON should not contain %q, got: %s", excluded, rawStr)
		}
	}

	// Verify unmarshaling back
	var decoded Placement
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal(Placement): %v", err)
	}

	if decoded.ID != p.ID || decoded.Placement != p.Placement || decoded.Handler != p.Handler ||
		decoded.Title != p.Title || decoded.Description != p.Description || !decoded.CreatedAt.Equal(p.CreatedAt) {
		t.Errorf("decoded Placement does not match original: %+v vs %+v", decoded, p)
	}
	if decoded.AppID != "" || decoded.AppTitle != "" {
		t.Errorf("decoded AppID/AppTitle should be empty, got AppID=%q AppTitle=%q", decoded.AppID, decoded.AppTitle)
	}
}

func jsonContainsString(jsonStr, target string) bool {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return false
	}
	for _, v := range m {
		if s, ok := v.(string); ok && s == target {
			return true
		}
	}
	return false
}

// TestAppOwns verifies whether a target URL belongs to an app's registered handler or install hosts.
func TestAppOwns(t *testing.T) {
	app := App{
		HandlerURL: "https://app.example.com/handler",
		InstallURL: "http://install.example.com:8080/setup",
	}

	parseURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", raw, err)
		}
		return u
	}

	tests := []struct {
		name string
		app  App
		u    *url.URL
		want bool
	}{
		{"nil URL", app, nil, false},
		{"ftp scheme", app, parseURL("ftp://app.example.com/file"), false},
		{"file scheme", app, parseURL("file:///etc/passwd"), false},
		{"javascript scheme", app, &url.URL{Scheme: "javascript", Opaque: "alert(1)"}, false},
		{"empty host", app, &url.URL{Scheme: "https", Path: "/handler"}, false},
		{"matching handler URL", app, parseURL("https://app.example.com/another/path?q=1"), true},
		{"matching handler URL case-insensitive host", app, parseURL("https://APP.EXAMPLE.COM/another"), true},
		{"handler URL scheme mismatch", app, parseURL("http://app.example.com/handler"), false},
		{"handler URL port mismatch", app, parseURL("https://app.example.com:8443/handler"), false},
		{"handler URL subdomain mismatch", app, parseURL("https://sub.app.example.com/handler"), false},
		{"matching install URL with port", app, parseURL("http://install.example.com:8080/complete"), true},
		{"matching install URL case-insensitive host", app, parseURL("http://INSTALL.EXAMPLE.COM:8080/complete"), true},
		{"install URL scheme mismatch", app, parseURL("https://install.example.com:8080/setup"), false},
		{"install URL port mismatch", app, parseURL("http://install.example.com/setup"), false},
		{"foreign host", app, parseURL("https://evil.example.com/phish"), false},
		{"empty app URLs", App{}, parseURL("https://example.com/handler"), false},
		{"malformed handler URL in app", App{HandlerURL: ":::badurl"}, parseURL("https://example.com"), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.app.Owns(tc.u)
			if got != tc.want {
				t.Errorf("Owns(%v) = %v, want %v", tc.u, got, tc.want)
			}
		})
	}
}

// TestPlaceOptions: OPTIONS of a widget of the messenger are kept by their names (any case of the name,
// unknown ones dropped), context, role and extranet checked; other places keep none.
func TestPlaceOptions(t *testing.T) {
	got, err := placeOptions("IM_SIDEBAR", PlaceOptions{"ICONNAME": "fa-bug", "context": "user; lines", "role": "admin", "extranet": "y", "x": "1", "color": "aqua"})
	if err != nil || got["iconName"] != "fa-bug" || got["context"] != "USER; LINES" || got["role"] != "ADMIN" || got["extranet"] != "Y" || got["x"] != "" || got["color"] != "AQUA" {
		t.Errorf("placeOptions: %v %v", got, err)
	}
	for _, bad := range []PlaceOptions{{"context": "GROUPS"}, {"role": "boss"}, {"extranet": "maybe"}, {"width": "-5"}, {"height": "tall"}} {
		if _, err := placeOptions("IM_TEXTAREA", bad); err == nil {
			t.Errorf("placeOptions(%v) took it", bad)
		}
	}
	if got, err := placeOptions("TASK_VIEW_TAB", PlaceOptions{"role": "boss"}); err != nil || len(got) != 0 {
		t.Errorf("a card's tab keeps options: %v %v", got, err)
	}
	var o PlaceOptions
	if err := json.Unmarshal([]byte(`{"width": 200, "extranet": true, "context": "CHAT", "n": null}`), &o); err != nil || o["width"] != "200" || o["extranet"] != "Y" || o["context"] != "CHAT" || len(o) != 3 {
		t.Errorf("PlaceOptions from JSON: %v %v", o, err)
	}
}

// TestPlacementFor: the widget's context (ALL by default), role (ADMIN — administrators) and extranet (Y — guests too).
func TestPlacementFor(t *testing.T) {
	p := Placement{}
	if !p.ForChat("USER") || !p.ForChat("BOT") || !p.For(false, false) || p.For(false, true) {
		t.Error("the defaults: all chats, all employees, no guests")
	}
	p.Options = PlaceOptions{"context": "USER;CRM", "role": "ADMIN", "extranet": "Y"}
	if !p.ForChat("CRM") || p.ForChat("CHAT") || p.ForChat("BOT") || p.For(false, false) || !p.For(true, true) {
		t.Error("USER;CRM for administrators and guests")
	}
	p.Options = PlaceOptions{"width": "5000", "height": "x"}
	if p.Width() != 800 || p.Height() != 100 {
		t.Errorf("the frame's size: %d×%d", p.Width(), p.Height())
	}
}
