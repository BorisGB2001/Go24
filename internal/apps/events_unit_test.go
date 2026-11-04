package apps

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestEventsList verifies the catalog of supported events for application subscriptions.
func TestEventsList(t *testing.T) {
	required := []string{
		"ONCRMLEADADD", "ONCRMLEADUPDATE", "ONCRMLEADDELETE",
		"ONCRMDEALADD", "ONCRMDEALUPDATE", "ONCRMDEALDELETE",
		"ONCRMCONTACTADD", "ONCRMCONTACTUPDATE", "ONCRMCONTACTDELETE",
		"ONCRMCOMPANYADD", "ONCRMCOMPANYUPDATE", "ONCRMCOMPANYDELETE",
		"ONCRMDYNAMICITEMADD", "ONCRMDYNAMICITEMUPDATE", "ONCRMDYNAMICITEMDELETE",
		"ONTASKADD", "ONTASKUPDATE", "ONTASKDELETE",
		"ONUSERADD", "ONAPPUNINSTALL", "ONVOXIMPLANTCALLEND",
		"ONIMBOTMESSAGEADD", "ONIMBOTJOINCHAT", "ONIMBOTDELETE", "ONIMCOMMANDADD",
		"ONIMCONNECTORMESSAGEADD", "ONIMCONNECTORDIALOGFINISH", "ONIMCONNECTORSTATUSDELETE",
	}

	seen := make(map[string]bool)
	for _, e := range Events {
		if seen[e] {
			t.Errorf("duplicate event in Events: %q", e)
		}
		seen[e] = true
	}

	for _, r := range required {
		if !slices.Contains(Events, r) {
			t.Errorf("Events missing required event: %q", r)
		}
	}
}

// TestEventBackoff verifies the retry schedule durations for failed deliveries.
func TestEventBackoff(t *testing.T) {
	expected := []time.Duration{
		time.Minute,
		5 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		6 * time.Hour,
	}

	if len(eventBackoff) != len(expected) {
		t.Fatalf("len(eventBackoff) = %d, want %d", len(eventBackoff), len(expected))
	}

	for i, d := range expected {
		if eventBackoff[i] != d {
			t.Errorf("eventBackoff[%d] = %v, want %v", i, eventBackoff[i], d)
		}
	}
}

// TestBareAuth verifies the stripped authentication map sent when no user tokens are available.
func TestBareAuth(t *testing.T) {
	app := App{
		ClientID:         "local.12345678",
		ApplicationToken: "apptok_secret_value",
		Scopes:           []string{"crm", "tasks", "telephony"},
	}

	for _, base := range []string{"https://go24.example.com", "https://go24.example.com/"} {
		auth := bareAuth(app, base)

		// Keys that must be absent
		for _, forbidden := range []string{"access_token", "refresh_token", "expires", "expires_in", "user_id"} {
			if _, exists := auth[forbidden]; exists {
				t.Errorf("bareAuth with base %q must not contain %q", base, forbidden)
			}
		}

		// Keys that must be present and match expected values
		if got := auth["scope"]; got != "crm,tasks,telephony" {
			t.Errorf("bareAuth scope = %v, want %q", got, "crm,tasks,telephony")
		}
		if got := auth["domain"]; got != "go24.example.com" {
			t.Errorf("bareAuth domain = %v, want %q", got, "go24.example.com")
		}
		if got := auth["client_endpoint"]; got != "https://go24.example.com/api/v1/" {
			t.Errorf("bareAuth client_endpoint = %v, want %q", got, "https://go24.example.com/api/v1/")
		}
		if got := auth["server_endpoint"]; got != "https://go24.example.com/oauth/" {
			t.Errorf("bareAuth server_endpoint = %v, want %q", got, "https://go24.example.com/oauth/")
		}
		if got := auth["application_token"]; got != app.ApplicationToken {
			t.Errorf("bareAuth application_token = %v, want %q", got, app.ApplicationToken)
		}
		if got := auth["status"]; got != "L" {
			t.Errorf("bareAuth status = %v, want %q", got, "L")
		}
		if got := auth["member_id"]; got != MemberID(base) {
			t.Errorf("bareAuth member_id = %v, want %q", got, MemberID(base))
		}
	}
}

// TestEventJSON verifies JSON marshaling and field name mapping for Event.
func TestEventJSON(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	lastStatus := 200
	e := Event{
		ID:         "ev-42",
		AppID:      "app-ignore", // json:"-"
		Event:      "ONTASKADD",
		Handler:    "https://hook.example.com/tasks",
		UserID:     "user-uuid-99", // json:"auth_type"
		LastAt:     &now,
		LastStatus: &lastStatus,
		LastError:  "",
		CreatedAt:  now,
		Pending:    1,
	}

	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal(Event): %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal map: %v", err)
	}

	// Verify auth_type mapping
	if m["auth_type"] != "user-uuid-99" {
		t.Errorf("m[auth_type] = %v, want user-uuid-99", m["auth_type"])
	}
	if _, exists := m["user_id"]; exists {
		t.Error("JSON contains user_id, expected only auth_type")
	}
	if _, exists := m["app_id"]; exists {
		t.Error("JSON contains app_id, expected json:\"-\"")
	}
	if m["event"] != "ONTASKADD" || m["handler"] != "https://hook.example.com/tasks" || m["pending"] != float64(1) {
		t.Errorf("m does not match expected fields: %+v", m)
	}

	// Verify roundtrip
	var decoded Event
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal(Event): %v", err)
	}
	if decoded.ID != e.ID || decoded.Event != e.Event || decoded.Handler != e.Handler || decoded.UserID != e.UserID {
		t.Errorf("decoded Event mismatch: %+v vs %+v", decoded, e)
	}
}

// TestEventPayloads verifies structure of payloads dispatched to application handlers.
func TestEventPayloads(t *testing.T) {
	app := App{
		ClientID:         "local.123",
		ClientSecret:     "secret",
		ApplicationToken: "tok",
	}
	auth := bareAuth(app, "https://go24.example")

	t.Run("StandardDeliveryPayload", func(t *testing.T) {
		ts := time.Now().Unix()
		taskData := map[string]any{"ID": "105", "TITLE": "Deploy release"}
		dataBytes, _ := json.Marshal(taskData)

		body, err := json.Marshal(map[string]any{
			"event":            "ONTASKADD",
			"event_handler_id": "h-123",
			"data":             json.RawMessage(dataBytes),
			"ts":               ts,
			"auth":             auth,
		})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		var parsed struct {
			Event          string         `json:"event"`
			EventHandlerID string         `json:"event_handler_id"`
			Data           map[string]any `json:"data"`
			TS             int64          `json:"ts"`
			Auth           map[string]any `json:"auth"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}

		if parsed.Event != "ONTASKADD" || parsed.EventHandlerID != "h-123" || parsed.TS != ts {
			t.Errorf("parsed event payload mismatch: %+v", parsed)
		}
		if parsed.Data["ID"] != "105" || parsed.Data["TITLE"] != "Deploy release" {
			t.Errorf("parsed data mismatch: %+v", parsed.Data)
		}
		if parsed.Auth["application_token"] != "tok" {
			t.Errorf("parsed auth mismatch: %+v", parsed.Auth)
		}
	})

	t.Run("UninstallPayload", func(t *testing.T) {
		body, err := json.Marshal(map[string]any{
			"event":            "ONAPPUNINSTALL",
			"event_handler_id": "h-456",
			"ts":               time.Now().Unix(),
			"data":             map[string]any{"LANGUAGE_ID": "ru", "CLEAN": 0},
			"auth":             auth,
		})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		var parsed struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if parsed.Event != "ONAPPUNINSTALL" || parsed.Data["LANGUAGE_ID"] != "ru" || parsed.Data["CLEAN"] != float64(0) {
			t.Errorf("parsed uninstall payload mismatch: %+v", parsed)
		}
	})

	t.Run("InstallPayload", func(t *testing.T) {
		tokens := Tokens{Access: "go24at_acc", Refresh: "go24rt_ref", Expires: time.Now().Add(time.Hour), UserID: "u1"}
		fullAuth := Auth(app, tokens, "https://go24.example")

		body, err := json.Marshal(map[string]any{
			"event":            "ONAPPINSTALL",
			"event_handler_id": 0,
			"ts":               time.Now().Unix(),
			"data":             map[string]any{"VERSION": 1, "LANGUAGE_ID": "en"},
			"auth":             fullAuth,
		})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}

		var parsed struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
			Auth  map[string]any `json:"auth"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if parsed.Event != "ONAPPINSTALL" || parsed.Data["VERSION"] != float64(1) || parsed.Data["LANGUAGE_ID"] != "en" {
			t.Errorf("parsed install payload mismatch: %+v", parsed)
		}
		if parsed.Auth["access_token"] != "go24at_acc" {
			t.Errorf("parsed auth missing access_token: %+v", parsed.Auth)
		}
	})
}

// TestBindEventPreconditions verifies BindEvent input validations before hitting the database.
func TestBindEventPreconditions(t *testing.T) {
	s := &Store{}
	ctx := context.Background()
	app := App{
		ID:         "app-1",
		HandlerURL: "https://my-app.example.com/events",
	}

	tests := []struct {
		name       string
		event      Event
		wantSubstr string
	}{
		{
			name:       "unknown event name",
			event:      Event{Event: "ONNOTANEVENT", Handler: "https://my-app.example.com/events"},
			wantSubstr: "err.app_event",
		},
		{
			name:       "invalid handler URL format",
			event:      Event{Event: "ONTASKADD", Handler: ":::bad-url"},
			wantSubstr: "err.app_handler_host",
		},
		{
			name:       "handler URL non-http",
			event:      Event{Event: "ONTASKADD", Handler: "ftp://my-app.example.com/events"},
			wantSubstr: "err.app_handler_host",
		},
		{
			name:       "handler host not owned by app",
			event:      Event{Event: "ONTASKADD", Handler: "https://foreign-host.example.com/events"},
			wantSubstr: "err.app_handler_host",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.BindEvent(ctx, app, tc.event)
			if err == nil {
				t.Fatalf("expected BindEvent to return error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain expected substring %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestPortalData: a bot event's copy for the portal goes to a handler only; neither handler sees Portal_DATA.
func TestPortalData(t *testing.T) {
	data := func() map[string]any {
		return map[string]any{"PARAMS": map[string]any{"DIALOG_ID": "!r:x"}, "Portal_DATA": map[string]any{"PARAMS": map[string]any{"DIALOG_ID": "chat7"}}}
	}
	if got := portalData(data(), true); got["PARAMS"].(map[string]any)["DIALOG_ID"] != "chat7" || got["Portal_DATA"] != nil {
		t.Errorf("for the portal: %v", got)
	}
	if got := portalData(data(), false); got["PARAMS"].(map[string]any)["DIALOG_ID"] != "!r:x" || got["Portal_DATA"] != nil {
		t.Errorf("for go24: %v", got)
	}
	if got := portalData(map[string]any{"FIELDS": map[string]any{"ID": "x"}}, true); got["FIELDS"] == nil {
		t.Errorf("an event without a copy: %v", got)
	}
}
