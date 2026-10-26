package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go24/internal/i18n"
)

// TestFormAware: a background form gets a redirect as JSON (with its #fragment) and a mistake as JSON {error};
// an ordinary request still gets the redirect itself.
func TestFormAware(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{I18n: b, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := formAware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			s.taskErr(w, r, i18n.E("err.project_key"), "/projects")
			return
		}
		http.Redirect(w, r, "/t/GO-1#time", http.StatusSeeOther)
	}))
	do := func(path string, form bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("title=x"))
		if form {
			r.Header.Set("X-Go24-Form", "1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("/ok", false); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/t/GO-1#time" {
		t.Fatalf("plain post: %d %q", w.Code, w.Header().Get("Location"))
	}
	w := do("/ok", true)
	var got map[string]string
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil || w.Code != 200 || got["redirect"] != "/t/GO-1#time" {
		t.Fatalf("form post: %d %v %v", w.Code, got, err)
	}
	w = do("/bad", true)
	got = nil
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil || w.Code != http.StatusUnprocessableEntity || got["error"] == "" {
		t.Fatalf("form mistake: %d %v %v", w.Code, got, err)
	}
	if w := do("/bad", false); w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/projects?msg=") {
		t.Fatalf("plain mistake: %d %q", w.Code, w.Header().Get("Location"))
	}
}
