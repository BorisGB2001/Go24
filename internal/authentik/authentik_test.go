package authentik

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFillMissing(t *testing.T) {
	var patched map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("email") == "nobody@x" {
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{
				{"pk": 7, "name": "", "email": "p@x", "attributes": map[string]any{"phone": "+7 999", "theme": "dark"}}}})
		case http.MethodPatch:
			if r.URL.Path != "/api/v3/core/users/7/" {
				t.Errorf("patch path %s", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&patched)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	changed, err := c.FillMissing(context.Background(), Profile{Email: "P@X", Name: "Пётр Иванов",
		Attributes: map[string]string{"phone": "+7 111", "position": "Инженер", "city": ""}})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	attrs := patched["attributes"].(map[string]any)
	if patched["name"] != "Пётр Иванов" || attrs["phone"] != "+7 999" || attrs["position"] != "Инженер" || attrs["theme"] != "dark" {
		t.Fatalf("patch = %v", patched)
	}
	if _, ok := attrs["city"]; ok {
		t.Fatal("empty value written")
	}
	if changed, err := c.FillMissing(context.Background(), Profile{Email: "nobody@x", Name: "x"}); changed || err != nil {
		t.Fatalf("unknown user: %v %v", changed, err)
	}
	if New("", "tok") != nil || New(srv.URL, "") != nil {
		t.Fatal("client without URL or token")
	}
}
