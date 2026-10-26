package office

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestToken(t *testing.T) {
	s := New("http://onlyoffice/", "secret", "http://go24:8080")
	token, err := s.Sign(map[string]any{"key": "k1", "status": 2})
	if err != nil {
		t.Fatal(err)
	}
	var cb Callback
	if err := s.Verify(token, &cb); err != nil || cb.Key != "k1" || cb.Status != 2 {
		t.Fatalf("verify = %v %+v", err, cb)
	}
	other := New("http://onlyoffice", "other", "http://go24:8080")
	if err := other.Verify(token, &cb); err != ErrToken {
		t.Errorf("another secret: %v", err)
	}
	parts := strings.Split(token, ".")
	forged, _ := other.Sign(map[string]any{"key": "k2"})
	if err := s.Verify(parts[0]+"."+strings.Split(forged, ".")[1]+"."+parts[2], &cb); err != ErrToken {
		t.Errorf("changed claims: %v", err)
	}
	old, _ := s.Sign(map[string]any{"key": "k3", "exp": time.Now().Add(-time.Minute).Unix()})
	if err := s.Verify(old, &cb); err != ErrToken {
		t.Errorf("expired: %v", err)
	}
	if err := s.Verify("x.y", &cb); err != ErrToken {
		t.Errorf("not a JWT: %v", err)
	}
}

func TestStampAndLinks(t *testing.T) {
	s := New("http://onlyoffice", "secret", "http://go24:8080/")
	if s.Self != "http://go24:8080" || New("", "x", "y") != nil {
		t.Errorf("New = %+v", s)
	}
	st := s.Stamp("f1", "key/a")
	if !s.Stamped(st, "f1", "key/a") || s.Stamped(st, "f1", "key/b") || s.Stamped(st, "f1key/a") {
		t.Error("the stamp is not tied to its parts")
	}
	if got := s.Inside("http://192.168.1.5:8080/office/cache/files/data/k/output.docx?md5=x"); got != "http://onlyoffice/cache/files/data/k/output.docx?md5=x" {
		t.Errorf("Inside = %s", got)
	}
	if got := s.Inside("http://onlyoffice/cache/x"); got != "http://onlyoffice/cache/x" {
		t.Errorf("Inside = %s", got)
	}
	if Key("f1", "a") == Key("f1", "b") || !strings.HasPrefix(Key("f1", "a"), "f1-") {
		t.Error("Key does not follow the file's state")
	}
}

func TestKind(t *testing.T) {
	for name, want := range map[string]string{"a.DOCX": "word", "b.xlsx": "cell", "c.odp": "slide", "d.pdf": "pdf", "e.zip": "", "f": ""} {
		if got := Kind(name); got != want {
			t.Errorf("Kind(%s) = %q, want %q", name, got, want)
		}
	}
	if !Editable("a.docx") || Editable("a.doc") || Editable("a.pdf") {
		t.Error("Editable")
	}
}

// TestConvert: the request to ConvertService is signed; the result's link is the one go24 fetches, an error is ErrConvert.
func TestConvert(t *testing.T) {
	var s *Server
	ds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Token, Key string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		var claims map[string]any
		switch {
		case r.URL.Path != "/converter" || s.Verify(req.Token, &claims) != nil || claims["outputtype"] != "pdf":
			io.WriteString(w, `{"error": -8}`)
		case req.Key == "bad":
			io.WriteString(w, `{"error": -4}`)
		default:
			io.WriteString(w, `{"endConvert": true, "percent": 100, "fileUrl": "http://192.168.1.15:8080/office/cache/files/out.pdf"}`)
		}
	}))
	defer ds.Close()
	s = New(ds.URL, "secret", "http://go24:8080")
	link, err := s.Convert(context.Background(), "http://go24:8080/f", "docx", "k1", "a.docx", "pdf")
	if err != nil || link != ds.URL+"/cache/files/out.pdf" {
		t.Fatalf("convert = %q %v", link, err)
	}
	if _, err := s.Convert(context.Background(), "http://go24:8080/f", "docx", "bad", "a.docx", "pdf"); !errors.Is(err, ErrConvert) {
		t.Errorf("a failed conversion: %v", err)
	}
}
