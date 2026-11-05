package telephony

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCallerNameSanitization(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`Анна "Аня" <Петрова>`, "Анна Аня Петрова"},
		{"  [Отдел продаж]; \\Иван/\r\n  ", "Отдел продаж Иван/"},
		{`"Bad" <Name> [With] ;Special\Chars`, "Bad Name With SpecialChars"},
		{"  Clean Name  ", "Clean Name"},
		{"", ""},
		{`"<>\[];` + "\r\n", ""},
	}
	for _, tc := range tests {
		if got := callerName(tc.input); got != tc.want {
			t.Errorf("callerName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestMd5cred(t *testing.T) {
	login, pass := "101", "secret123"
	got := md5cred(login, pass)
	expectedHash := md5.Sum([]byte("101:go24:secret123"))
	want := hex.EncodeToString(expectedHash[:])
	if got != want {
		t.Errorf("md5cred(%q, %q) = %q, want %q", login, pass, got, want)
	}
}

func TestGreetingFile(t *testing.T) {
	if got := GreetingFile("abcdef123"); got != "/etc/asterisk/go24/greet-abcdef123" {
		t.Errorf("GreetingFile() = %q, want %q", got, "/etc/asterisk/go24/greet-abcdef123")
	}
}

func TestPBXReady(t *testing.T) {
	var nilPBX *PBX
	if nilPBX.Ready() {
		t.Errorf("nil PBX.Ready() should be false")
	}

	p := &PBX{}
	if p.Ready() {
		t.Errorf("empty PBX.Ready() should be false")
	}

	p.ARI = &ARI{URL: "http://localhost:8088"}
	if p.Ready() { // Dir still empty
		t.Errorf("PBX with empty Dir should not be ready")
	}

	p.Dir = "/tmp/asterisk"
	if !p.Ready() {
		t.Errorf("PBX with ARI and Dir should be ready")
	}
}

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "test.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}

	data := []byte("content of conf file")
	if err := writeFile(target, data); err != nil {
		t.Fatalf("writeFile failed: %v", err)
	}

	read, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(read) != string(data) {
		t.Errorf("file content = %q, want %q", string(read), string(data))
	}

	// Verify temp file is cleaned up after rename
	if _, err := os.Stat(target + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary file was not cleaned up: %v", err)
	}
}

func TestConfFileSync(t *testing.T) {
	var reloads atomic.Int32
	var shouldFailReload atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shouldFailReload.Load() {
			http.Error(w, "module reload failed", http.StatusInternalServerError)
			return
		}
		reloads.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ari := &ARI{URL: server.URL, User: "u", Password: "p"}
	dir := t.TempDir()
	file := filepath.Join(dir, "pjsip.conf")

	var conf confFile
	ctx := context.Background()

	// 1. Initial sync: file written and reloaded
	if err := conf.sync(ctx, ari, file, "version 1", "res_pjsip.so"); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	if reloads.Load() != 1 {
		t.Errorf("reloads = %d, want 1", reloads.Load())
	}

	// 2. Sync again with same content: no file rewrite, no reload
	if err := conf.sync(ctx, ari, file, "version 1", "res_pjsip.so"); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	if reloads.Load() != 1 {
		t.Errorf("second sync should not reload ARI, reloads = %d", reloads.Load())
	}

	// 3. Sync with new content but ARI fails: file written, stays stale
	shouldFailReload.Store(true)
	if err := conf.sync(ctx, ari, file, "version 2", "res_pjsip.so"); err == nil {
		t.Fatalf("sync should fail when ARI fails")
	}
	if !conf.stale {
		t.Errorf("confFile should stay stale after failed reload")
	}

	// 4. Next sync with same content: retries reload because it was stale
	shouldFailReload.Store(false)
	if err := conf.sync(ctx, ari, file, "version 2", "res_pjsip.so"); err != nil {
		t.Fatalf("retry sync failed: %v", err)
	}
	if reloads.Load() != 2 || conf.stale {
		t.Errorf("retry should reload ARI (reloads = %d) and clear stale flag (stale = %v)", reloads.Load(), conf.stale)
	}

	// 5. Restart simulation: new confFile struct with c.written = "", existing file has "version 2"
	var conf2 confFile
	if err := conf2.sync(ctx, ari, file, "version 2", "res_pjsip.so"); err != nil {
		t.Fatalf("conf2 sync failed: %v", err)
	}
	if reloads.Load() != 2 {
		t.Errorf("conf2 on restart should read existing file and not reload if unchanged, reloads = %d", reloads.Load())
	}
}

func TestARIClientMethods(t *testing.T) {
	var lastMethod, lastPath, lastAuth, lastContentType string
	var lastBody []byte
	var lastQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastMethod = r.Method
		lastPath = r.URL.Path
		lastQuery = r.URL.RawQuery
		lastAuth = r.Header.Get("Authorization")
		lastContentType = r.Header.Get("Content-Type")
		lastBody, _ = io.ReadAll(r.Body)

		switch {
		case r.URL.Path == "/ari/asterisk/info":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"system": {"version": "20.5.0"}}`))
		case r.URL.Path == "/ari/endpoints/PJSIP":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`[
				{"resource": "101", "state": "online"},
				{"resource": "w101", "state": "offline"},
				{"resource": "102", "state": "online"}
			]`))
		case r.URL.Path == "/ari/fail":
			http.Error(w, "custom server error", http.StatusBadRequest)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	ari := &ARI{URL: server.URL, User: "go24user", Password: "go24pass"}
	ctx := context.Background()

	// 1. Version
	ver, err := ari.Version(ctx)
	if err != nil || ver != "20.5.0" {
		t.Fatalf("Version() = (%q, %v), want 20.5.0", ver, err)
	}
	if lastMethod != http.MethodGet || lastPath != "/ari/asterisk/info" {
		t.Errorf("Version request = %s %s", lastMethod, lastPath)
	}
	if !strings.HasPrefix(lastAuth, "Basic ") {
		t.Errorf("Authorization header missing Basic auth: %q", lastAuth)
	}

	// 2. Online
	online, err := ari.Online(ctx)
	if err != nil {
		t.Fatalf("Online() failed: %v", err)
	}
	if !online["101"] || !online["102"] || online["w101"] {
		t.Errorf("Online() = %+v, want 101:true, 102:true, w101:false", online)
	}

	// 3. Reload
	if err := ari.Reload(ctx, "pbx_config.so"); err != nil {
		t.Fatalf("Reload() failed: %v", err)
	}
	if lastMethod != http.MethodPut || lastPath != "/ari/asterisk/modules/pbx_config.so" {
		t.Errorf("Reload request = %s %s", lastMethod, lastPath)
	}

	// 4. Call (with record = true)
	if err := ari.Call(ctx, "101", "+79001234567", true); err != nil {
		t.Fatalf("Call() failed: %v", err)
	}
	if lastMethod != http.MethodPost || lastPath != "/ari/channels" {
		t.Errorf("Call request = %s %s", lastMethod, lastPath)
	}
	if lastContentType != "application/json" {
		t.Errorf("Call Content-Type = %q, want application/json", lastContentType)
	}
	if !strings.Contains(lastQuery, "callerId=101") || !strings.Contains(lastQuery, "context=go24-phones") {
		t.Errorf("Call query = %s", lastQuery)
	}
	var callBody struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal(lastBody, &callBody); err != nil || callBody.Variables["GO24_RECORD"] != "1" {
		t.Errorf("Call body variables = %+v", callBody)
	}

	// 5. Originate
	if err := ari.Originate(ctx, "+79001234567", "go24-cb-test", "go24-cb-test-out"); err != nil {
		t.Fatalf("Originate() failed: %v", err)
	}
	if lastMethod != http.MethodPost || lastPath != "/ari/channels" {
		t.Errorf("Originate request = %s %s", lastMethod, lastPath)
	}
	if !strings.Contains(lastQuery, "endpoint=Local%2F%2B79001234567%40go24-cb-test%2Fn") {
		t.Errorf("Originate query missing endpoint: %s", lastQuery)
	}

	// 6. Spy
	if err := ari.Spy(ctx, "101", "PJSIP/w102-0001", "whisper"); err != nil {
		t.Fatalf("Spy() failed: %v", err)
	}
	var spyBody struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal(lastBody, &spyBody); err != nil ||
		spyBody.Variables["GO24_SPY"] != "PJSIP/w102-0001" || spyBody.Variables["GO24_SPYMODE"] != "whisper" {
		t.Errorf("Spy body = %+v", spyBody)
	}

	// 7. InfoCall
	tr := Trunk{ID: "11111111-2222-3333-4444-555555555555", Number: "+74950001122"}
	if err := ari.InfoCall(ctx, tr, "+79001112233", "/etc/asterisk/sound1"); err != nil {
		t.Fatalf("InfoCall() failed: %v", err)
	}
	var infoBody struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal(lastBody, &infoBody); err != nil ||
		infoBody.Variables["GO24_SOUND"] != "/etc/asterisk/sound1" ||
		infoBody.Variables["GO24_LINE"] != "+74950001122" {
		t.Errorf("InfoCall body = %+v", infoBody)
	}

	// 8. Error handling when response status >= 300
	err = ari.do(ctx, http.MethodGet, "/fail", nil)
	if err == nil || !strings.Contains(err.Error(), "custom server error") {
		t.Errorf("expected custom server error, got: %v", err)
	}
}
