package apps

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go24/internal/hooks"
	"go24/internal/i18n"
)

// TestStaticConstants verifies limits and prefix for static application archives.
func TestStaticConstants(t *testing.T) {
	if MaxArchive != 20<<20 {
		t.Fatalf("MaxArchive = %d, want %d", MaxArchive, 20<<20)
	}
	if MaxFiles != 500 {
		t.Fatalf("MaxFiles = %d, want 500", MaxFiles)
	}
	if MaxFileSize != 10<<20 {
		t.Fatalf("MaxFileSize = %d, want %d", MaxFileSize, 10<<20)
	}
	if MaxUnpacked != 50<<20 {
		t.Fatalf("MaxUnpacked = %d, want %d", MaxUnpacked, 50<<20)
	}
	if StaticPrefix != "/apps/static/" {
		t.Fatalf("StaticPrefix = %q, want /apps/static/", StaticPrefix)
	}

	wantErr := i18n.E("err.app_archive")
	if !errors.Is(ErrArchive, wantErr) && ErrArchive.Error() != wantErr.Error() {
		t.Fatalf("ErrArchive = %v, want %v", ErrArchive, wantErr)
	}
}

// TestAppStaticURL verifies calculation of the public static application entrypoint URL.
func TestAppStaticURL(t *testing.T) {
	app := App{ID: "550e8400-e29b-41d4-a716-446655440000"}

	cases := []struct {
		base string
		want string
	}{
		{"https://go24.example.com", "https://go24.example.com/apps/static/550e8400-e29b-41d4-a716-446655440000/index.html"},
		{"https://go24.example.com/", "https://go24.example.com/apps/static/550e8400-e29b-41d4-a716-446655440000/index.html"},
		{"http://localhost:8080/", "http://localhost:8080/apps/static/550e8400-e29b-41d4-a716-446655440000/index.html"},
	}

	for _, c := range cases {
		if got := app.StaticURL(c.base); got != c.want {
			t.Errorf("StaticURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

// helper to build in-memory zip bytes.
func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip.Create(%q): %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip.Write(%q): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip.Close(): %v", err)
	}
	return buf.Bytes()
}

// TestUnzipValidRootIndex verifies unpacking an archive where index.html is at root.
func TestUnzipValidRootIndex(t *testing.T) {
	raw := buildZip(t, map[string][]byte{
		"index.html":       []byte("<!DOCTYPE html><html><body>App</body></html>"),
		"css/style.css":    []byte("body { color: black; }"),
		"js/main.js":       []byte("console.log('hi');"),
		"assets/icon.png":  []byte("\x89PNG\r\n\x1a\n"),
		"assets/data.xyz":  []byte("unknown binary"),
		"folder/":          nil, // directory entry
		"__MACOSX/._app":   []byte("macosx metadata"),
		".DS_Store":        []byte("macos ds store"),
		"sub/.DS_Store":    []byte("macos nested ds store"),
		`win\scripts\a.js`: []byte("var a = 1;"),
	})

	files, err := unzip(raw)
	if err != nil {
		t.Fatalf("unzip failed: %v", err)
	}

	// index.html must be present
	idx, ok := files["index.html"]
	if !ok {
		t.Fatal("missing index.html")
	}
	if !strings.Contains(idx.Type, "text/html") {
		t.Errorf("index.html type = %q, want text/html", idx.Type)
	}
	if string(idx.Data) != "<!DOCTYPE html><html><body>App</body></html>" {
		t.Errorf("index.html content = %q", string(idx.Data))
	}

	// css/style.css
	if css, ok := files["css/style.css"]; !ok || !strings.Contains(css.Type, "text/css") {
		t.Errorf("css/style.css missing or bad type: %v, %q", ok, css.Type)
	}

	// js/main.js
	if js, ok := files["js/main.js"]; !ok || !strings.Contains(js.Type, "javascript") {
		t.Errorf("js/main.js missing or bad type: %v, %q", ok, js.Type)
	}

	// assets/icon.png
	if png, ok := files["assets/icon.png"]; !ok || png.Type != "image/png" {
		t.Errorf("assets/icon.png missing or bad type: %v, %q", ok, png.Type)
	}

	// assets/data.xyz unknown mime
	if xyz, ok := files["assets/data.xyz"]; !ok || xyz.Type != "application/octet-stream" {
		t.Errorf("assets/data.xyz missing or bad type: %v, %q", ok, xyz.Type)
	}

	// win\scripts\a.js normalized to forward slash
	if win, ok := files["win/scripts/a.js"]; !ok || string(win.Data) != "var a = 1;" {
		t.Errorf("windows path not normalized properly: %v", ok)
	}

	// Ignored entries must not be present
	for _, bad := range []string{"folder/", "__MACOSX/._app", ".DS_Store", "sub/.DS_Store"} {
		if _, exists := files[bad]; exists {
			t.Errorf("file %q should have been ignored", bad)
		}
	}
}

// TestUnzipSingleFolderArchive verifies unpacking when all files are in a single root folder.
func TestUnzipSingleFolderArchive(t *testing.T) {
	raw := buildZip(t, map[string][]byte{
		"build/index.html":      []byte("<html>Index</html>"),
		"build/css/app.css":     []byte("h1 { font-size: 16px; }"),
		"build/images/logo.png": []byte("fake-png"),
	})

	files, err := unzip(raw)
	if err != nil {
		t.Fatalf("unzip single folder archive failed: %v", err)
	}

	if _, ok := files["index.html"]; !ok {
		t.Errorf("expected build/index.html to be unpacked as index.html")
	}
	if _, ok := files["css/app.css"]; !ok {
		t.Errorf("expected build/css/app.css to be unpacked as css/app.css")
	}
	if _, ok := files["images/logo.png"]; !ok {
		t.Errorf("expected build/images/logo.png to be unpacked as images/logo.png")
	}
}

// TestUnzipErrors verifies archive rejections for corrupt data, missing index, path traversal and limits.
func TestUnzipErrors(t *testing.T) {
	t.Run("CorruptZipBytes", func(t *testing.T) {
		_, err := unzip([]byte("not a zip archive"))
		if !errors.Is(err, ErrArchive) {
			t.Errorf("unzip(garbage) = %v, want ErrArchive", err)
		}
	})

	t.Run("EmptyZip", func(t *testing.T) {
		raw := buildZip(t, map[string][]byte{})
		_, err := unzip(raw)
		if !errors.Is(err, ErrArchive) {
			t.Errorf("unzip(empty) = %v, want ErrArchive", err)
		}
	})

	t.Run("MissingIndexHtml", func(t *testing.T) {
		raw := buildZip(t, map[string][]byte{
			"app.js":    []byte("var x = 1;"),
			"style.css": []byte("body {}"),
		})
		_, err := unzip(raw)
		if !errors.Is(err, ErrArchive) {
			t.Errorf("unzip without index.html = %v, want ErrArchive", err)
		}
	})

	t.Run("MultipleRootFoldersWithoutRootIndex", func(t *testing.T) {
		raw := buildZip(t, map[string][]byte{
			"folderA/index.html": []byte("Index A"),
			"folderB/index.html": []byte("Index B"),
		})
		_, err := unzip(raw)
		if !errors.Is(err, ErrArchive) {
			t.Errorf("unzip multiple roots = %v, want ErrArchive", err)
		}
	})

	t.Run("RootFilesAndFolderWithoutRootIndex", func(t *testing.T) {
		raw := buildZip(t, map[string][]byte{
			"readme.txt":     []byte("Readme"),
			"sub/index.html": []byte("Index"),
		})
		_, err := unzip(raw)
		if !errors.Is(err, ErrArchive) {
			t.Errorf("unzip root file + subfolder = %v, want ErrArchive", err)
		}
	})

	t.Run("PathTraversalDotDot", func(t *testing.T) {
		for _, bad := range []string{
			"../index.html",
			"sub/../../index.html",
			"folder/../index.html",
		} {
			raw := buildZip(t, map[string][]byte{
				bad:          []byte("malicious"),
				"index.html": []byte("normal"),
			})
			_, err := unzip(raw)
			if !errors.Is(err, ErrArchive) {
				t.Errorf("unzip with path %q = %v, want ErrArchive", bad, err)
			}
		}
	})

	t.Run("LeadingSlashPath", func(t *testing.T) {
		raw := buildZip(t, map[string][]byte{
			"/index.html": []byte("leading slash"),
		})
		_, err := unzip(raw)
		if !errors.Is(err, ErrArchive) {
			t.Errorf("unzip(/index.html) = %v, want ErrArchive", err)
		}
	})

	t.Run("MaxFilesExceeded", func(t *testing.T) {
		files := map[string][]byte{
			"index.html": []byte("main"),
		}
		for i := 1; i <= MaxFiles; i++ {
			files[fmt.Sprintf("file_%d.txt", i)] = []byte("content")
		}
		raw := buildZip(t, files)
		_, err := unzip(raw)
		if !errors.Is(err, ErrArchive) {
			t.Errorf("unzip(>MaxFiles) = %v, want ErrArchive", err)
		}
	})
}

// TestSetArchivePreconditions verifies SetArchive checks before calling into postgres.
func TestSetArchivePreconditions(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	t.Run("ExceedsMaxArchive", func(t *testing.T) {
		tooBig := make([]byte, MaxArchive+1)
		err := s.SetArchive(ctx, "app-1", tooBig)
		if !errors.Is(err, ErrArchive) {
			t.Errorf("SetArchive(>MaxArchive) = %v, want ErrArchive", err)
		}
	})

	t.Run("CorruptedArchive", func(t *testing.T) {
		err := s.SetArchive(ctx, "app-1", []byte("bad archive"))
		if !errors.Is(err, ErrArchive) {
			t.Errorf("SetArchive(bad) = %v, want ErrArchive", err)
		}
	})
}

// TestStaticFileHTTPHandling verifies HTTP serving headers and path normalization for static files.
func TestStaticFileHTTPHandling(t *testing.T) {
	staticFiles := map[string]StaticFile{
		"index.html": {Type: "text/html; charset=utf-8", Data: []byte("<h1>Welcome</h1>")},
		"app.js":     {Type: "application/javascript", Data: []byte("console.log('test');")},
	}

	serveStatic := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/apps/static/app1/")
		if p == "" || strings.HasSuffix(p, "/") {
			p += "index.html"
		}
		f, ok := staticFiles[p]
		if !ok {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", f.Type)
		w.Header().Set("Content-Security-Policy", "sandbox allow-scripts")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(f.Data)
	})

	server := httptest.NewServer(serveStatic)
	defer server.Close()

	client := server.Client()

	// 1. Root path defaults to index.html
	res, err := client.Get(server.URL + "/apps/static/app1/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q, want text/html", res.Header.Get("Content-Type"))
	}
	if string(body) != "<h1>Welcome</h1>" {
		t.Errorf("body = %q, want <h1>Welcome</h1>", string(body))
	}

	// 2. Direct file request
	res2, err := client.Get(server.URL + "/apps/static/app1/app.js")
	if err != nil {
		t.Fatalf("GET app.js: %v", err)
	}
	defer res2.Body.Close()
	body2, _ := io.ReadAll(res2.Body)
	if res2.StatusCode != http.StatusOK || string(body2) != "console.log('test');" {
		t.Errorf("GET app.js = %d %q", res2.StatusCode, string(body2))
	}

	// 3. 404 for missing file
	res3, err := client.Get(server.URL + "/apps/static/app1/missing.css")
	if err != nil {
		t.Fatalf("GET missing.css: %v", err)
	}
	defer res3.Body.Close()
	if res3.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res3.StatusCode)
	}
}

// TestStorePostHTTP verifies the HTTP client dispatch method Store.post with an httptest server.
func TestStorePostHTTP(t *testing.T) {
	secret := "test_client_secret_key"
	event := "ONAPPINSTALL"
	payload := []byte(`{"event":"ONAPPINSTALL","data":{"VERSION":1}}`)

	var receivedMethod, receivedContentType, receivedUA, receivedEvent, receivedSig string
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedContentType = r.Header.Get("Content-Type")
		receivedUA = r.Header.Get("User-Agent")
		receivedEvent = r.Header.Get("X-Go24-Event")
		receivedSig = r.Header.Get("X-Go24-Signature")
		receivedBody, _ = io.ReadAll(r.Body)

		if r.URL.Path == "/error" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/huge" {
			// Write > 64KB response to test truncation limit
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("A"), 128<<10))
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":true}`))
	}))
	defer srv.Close()

	s := NewStore(nil)
	ctx := context.Background()

	// 1. Successful POST
	status, err := s.post(ctx, srv.URL, secret, event, payload)
	if err != nil {
		t.Fatalf("post failed: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", receivedMethod)
	}
	if receivedContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", receivedContentType)
	}
	if receivedUA != "go24-app" {
		t.Errorf("User-Agent = %q, want go24-app", receivedUA)
	}
	if receivedEvent != event {
		t.Errorf("X-Go24-Event = %q, want %q", receivedEvent, event)
	}
	expectedSig := hooks.Sign(secret, payload)
	if receivedSig != expectedSig {
		t.Errorf("X-Go24-Signature = %q, want %q", receivedSig, expectedSig)
	}
	if !bytes.Equal(receivedBody, payload) {
		t.Errorf("received body mismatch")
	}

	// 2. Server 500 error response returns status code without error
	status500, err := s.post(ctx, srv.URL+"/error", secret, event, payload)
	if err != nil {
		t.Fatalf("post /error returned err: %v", err)
	}
	if status500 != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status500)
	}

	// 3. Huge response reading up to 64KB
	statusHuge, err := s.post(ctx, srv.URL+"/huge", secret, event, payload)
	if err != nil {
		t.Fatalf("post /huge failed: %v", err)
	}
	if statusHuge != http.StatusOK {
		t.Errorf("status = %d, want 200", statusHuge)
	}

	// 4. Invalid target URL returns error
	_, err = s.post(ctx, "http://127.0.0.1:0/unreachable", secret, event, payload)
	if err == nil {
		t.Errorf("expected post to unreachable address to fail")
	}

	// 5. Canceled context returns error
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.post(canceledCtx, srv.URL, secret, event, payload)
	if err == nil {
		t.Errorf("expected post with canceled context to fail")
	}
}
