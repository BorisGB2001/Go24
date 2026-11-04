package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestRCError(t *testing.T) {
	err := &RCError{Msg: "remote host unreachable"}
	if got := err.Error(); got != "remote host unreachable" {
		t.Errorf("RCError.Error() = %q, want %q", got, "remote host unreachable")
	}
}

func TestNewRC(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:5572":    "http://127.0.0.1:5572",
		"http://127.0.0.1:5572/":   "http://127.0.0.1:5572",
		"http://127.0.0.1:5572///": "http://127.0.0.1:5572",
	} {
		rc := NewRC(in, "admin", "secret")
		if rc.base != want {
			t.Errorf("NewRC(%q).base = %q, want %q", in, rc.base, want)
		}
		if rc.user != "admin" || rc.pass != "secret" {
			t.Errorf("NewRC credentials mismatch: user=%q, pass=%q", rc.user, rc.pass)
		}
		if rc.hc == nil || rc.hc.Timeout != 2*time.Minute {
			t.Errorf("NewRC client timeout = %v, want 2m", rc.hc.Timeout)
		}
	}
}

func TestEscapeSegments(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"a/b/c", "a/b/c"},
		{"space dir/file name.txt", "space%20dir/file%20name.txt"},
		{"dir/файл (1).txt", "dir/%D1%84%D0%B0%D0%B9%D0%BB%20%281%29.txt"},
		{"query?and#hash", "query%3Fand%23hash"},
		{"remote:bucket/path", "remote:bucket/path"},
		{"a/./b/../c", "a/./b/../c"},
		{"/leading/and/trailing/", "/leading/and/trailing/"},
		{"a//b", "a//b"},
		{"colon:in:segment/more:colons", "colon:in:segment/more:colons"},
	}
	for _, tc := range cases {
		if got := escapeSegments(tc.in); got != tc.want {
			t.Errorf("escapeSegments(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRCCallAndDo(t *testing.T) {
	t.Run("basic auth and json output", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			if !ok || user != "user1" || pass != "pass1" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, `{"error":"invalid content type"}`, http.StatusBadRequest)
				return
			}
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if in["echo"] != "ping" {
				http.Error(w, `{"error":"unexpected payload"}`, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"reply": "pong"})
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "user1", "pass1")
		var out map[string]string
		err := rc.call(context.Background(), "test/method", map[string]string{"echo": "ping"}, &out)
		if err != nil {
			t.Fatalf("call failed: %v", err)
		}
		if out["reply"] != "pong" {
			t.Fatalf("call output = %v, want pong", out)
		}

		// Wrong credentials
		badRC := NewRC(srv.URL, "wrong", "creds")
		err = badRC.call(context.Background(), "test/method", map[string]string{"echo": "ping"}, nil)
		var rce *RCError
		if !errors.As(err, &rce) || rce.Msg != "unauthorized" {
			t.Fatalf("expected RCError 'unauthorized', got %v", err)
		}
	})

	t.Run("server error json vs status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/json-err":
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"object not found"}`))
			case "/plain-err":
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("502 Bad Gateway"))
			case "/empty-err":
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":""}`))
			}
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")

		// JSON error
		err := rc.call(context.Background(), "json-err", map[string]any{}, nil)
		var rce *RCError
		if !errors.As(err, &rce) || rce.Msg != "object not found" {
			t.Errorf("json-err want 'object not found', got %v", err)
		}

		// Plain non-JSON error
		err = rc.call(context.Background(), "plain-err", map[string]any{}, nil)
		if !errors.As(err, &rce) || !strings.Contains(rce.Msg, "502 Bad Gateway") {
			t.Errorf("plain-err want status containing '502 Bad Gateway', got %v", err)
		}

		// JSON with empty error string falls back to status
		err = rc.call(context.Background(), "empty-err", map[string]any{}, nil)
		if !errors.As(err, &rce) || !strings.Contains(rce.Msg, "500 Internal Server Error") {
			t.Errorf("empty-err want status containing '500 Internal Server Error', got %v", err)
		}
	})

	t.Run("invalid json response with out non-nil", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not valid json"))
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		var out struct{ V string }
		err := rc.call(context.Background(), "bad-json", map[string]any{}, &out)
		if err == nil {
			t.Fatal("expected json decode error, got nil")
		}
	})

	t.Run("network error", func(t *testing.T) {
		rc := NewRC("http://127.0.0.1:1", "u", "p")
		err := rc.call(context.Background(), "noop", map[string]any{}, nil)
		if err == nil {
			t.Fatal("expected connection error, got nil")
		}
	})

	t.Run("marshal error", func(t *testing.T) {
		rc := NewRC("http://127.0.0.1:5572", "u", "p")
		err := rc.call(context.Background(), "noop", make(chan int), nil)
		if err == nil {
			t.Fatal("expected json marshal error for channel type, got nil")
		}
	})
}

func TestRCPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rc/noop" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rc := NewRC(srv.URL, "u", "p")
	if err := rc.Ping(context.Background()); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	badRC := NewRC(srv.URL+"/not-found", "u", "p")
	if err := badRC.Ping(context.Background()); err == nil {
		t.Fatal("Ping on non-existent endpoint should fail")
	}
}

func TestRCCreateRemote(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/create" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rc := NewRC(srv.URL, "u", "p")
	params := map[string]string{"type": "s3", "endpoint": "https://s3.example.com"}
	err := rc.CreateRemote(context.Background(), "myrem", "s3", params)
	if err != nil {
		t.Fatalf("CreateRemote failed: %v", err)
	}
	if received["name"] != "myrem" || received["type"] != "s3" {
		t.Fatalf("unexpected remote payload: %+v", received)
	}
	opts, _ := received["opt"].(map[string]any)
	if opts["obscure"] != true || opts["nonInteractive"] != true {
		t.Fatalf("unexpected remote options: %+v", opts)
	}
}

func TestRCDeleteRemote(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config/delete" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rc := NewRC(srv.URL, "u", "p")
	if err := rc.DeleteRemote(context.Background(), "myrem"); err != nil {
		t.Fatalf("DeleteRemote failed: %v", err)
	}
	if received["name"] != "myrem" {
		t.Fatalf("unexpected name in payload: %+v", received)
	}
}

func TestRCCheck(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/list" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rc := NewRC(srv.URL, "u", "p")
	if err := rc.Check(context.Background(), "myrem:"); err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if received["fs"] != "myrem:" || received["remote"] != "" {
		t.Fatalf("unexpected check payload: %+v", received)
	}
	opts, _ := received["opt"].(map[string]any)
	if opts["dirsOnly"] != true {
		t.Fatalf("Check opt dirsOnly must be true, got %+v", opts)
	}
}

func TestRCMkdir(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/operations/mkdir" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	rc := NewRC(srv.URL, "u", "p")
	if err := rc.Mkdir(context.Background(), "myrem", "backups/2026"); err != nil {
		t.Fatalf("Mkdir failed: %v", err)
	}
	if received["fs"] != "myrem:" || received["remote"] != "backups/2026" {
		t.Fatalf("unexpected mkdir payload: %+v", received)
	}
}

func TestRCUpload(t *testing.T) {
	t.Run("upload success with multipart", func(t *testing.T) {
		var receivedQuery string
		var receivedFileName string
		var receivedBody []byte

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/operations/uploadfile") {
				http.NotFound(w, r)
				return
			}
			receivedQuery = r.URL.RawQuery
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer file.Close()
			receivedFileName = header.Filename
			receivedBody, _ = io.ReadAll(file)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		content := []byte("upload content for storage test")
		err := rc.Upload(context.Background(), "myrem:", "docs/sub", "test.txt", bytes.NewReader(content))
		if err != nil {
			t.Fatalf("Upload failed: %v", err)
		}
		if receivedFileName != "test.txt" {
			t.Errorf("uploaded filename = %q, want 'test.txt'", receivedFileName)
		}
		if !bytes.Equal(receivedBody, content) {
			t.Errorf("uploaded body = %q, want %q", receivedBody, content)
		}
		if !strings.Contains(receivedQuery, "fs=myrem%3A") || !strings.Contains(receivedQuery, "remote=docs%2Fsub") {
			t.Errorf("uploaded query = %q, want fs and remote encoded", receivedQuery)
		}
	})

	t.Run("reader error during upload", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		err := rc.Upload(context.Background(), "myrem:", "docs", "bad.txt", iotest.ErrReader(errors.New("disk read error")))
		if err == nil {
			t.Fatal("expected upload error from broken reader, got nil")
		}
	})
}

func TestRCOpen(t *testing.T) {
	t.Run("open success and path escaping", func(t *testing.T) {
		files := map[string][]byte{
			"/[myrem:]/simple.txt":   []byte("simple content"),
			"/[myrem:]/sub/path.txt": []byte("nested content"),
			"/[myrem:]/sub/%D1%84%D0%B0%D0%B9%D0%BB%20%281%29%231%3F.txt": []byte("unicode content"),
			"/[local:/tmp/dir]/data.csv":                                  []byte("csv content"),
			"/[myrem:]/etc/passwd":                                        []byte("safe content"),
		}

		var lastRequestedURL string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			if !ok || user != "user" || pass != "pass" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			lastRequestedURL = r.URL.RequestURI()
			data, found := files[r.URL.EscapedPath()]
			if !found {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "user", "pass")

		// Normal file
		r, err := rc.Open(context.Background(), "myrem:", "simple.txt")
		if err != nil {
			t.Fatalf("Open simple.txt: %v", err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if string(data) != "simple content" {
			t.Errorf("got %q, want 'simple content'", data)
		}

		// Unicode, spaces, query marks and hashes
		r, err = rc.Open(context.Background(), "myrem:", "sub/файл (1)#1?.txt")
		if err != nil {
			t.Fatalf("Open unicode file: %v", err)
		}
		data, _ = io.ReadAll(r)
		r.Close()
		if string(data) != "unicode content" {
			t.Errorf("got %q, want 'unicode content'", data)
		}

		// Directory traversal ../ attacks
		r, err = rc.Open(context.Background(), "myrem:", "../../../etc/passwd")
		if err != nil {
			t.Fatalf("Open with traversal: %v", err)
		}
		data, _ = io.ReadAll(r)
		r.Close()
		if string(data) != "safe content" {
			t.Errorf("got %q, want 'safe content'", data)
		}
		if !strings.HasSuffix(lastRequestedURL, "/etc/passwd") || strings.Contains(lastRequestedURL, "..") {
			t.Errorf("traversal not prevented in URL: %q", lastRequestedURL)
		}

		// Local path fs
		r, err = rc.Open(context.Background(), "local:/tmp/dir", "data.csv")
		if err != nil {
			t.Fatalf("Open with local path fs: %v", err)
		}
		data, _ = io.ReadAll(r)
		r.Close()
		if string(data) != "csv content" {
			t.Errorf("got %q, want 'csv content'", data)
		}

		// Not found error
		_, err = rc.Open(context.Background(), "myrem:", "missing.txt")
		var rce *RCError
		if !errors.As(err, &rce) || !strings.Contains(rce.Msg, "404 Not Found") {
			t.Fatalf("expected 404 RCError for missing file, got %v", err)
		}

		// Network error on Open
		badRC := NewRC("http://127.0.0.1:1", "user", "pass")
		_, err = badRC.Open(context.Background(), "myrem:", "simple.txt")
		if err == nil {
			t.Fatal("expected network error on unreachable Open, got nil")
		}
	})
}

func TestRCDeleteAndPurge(t *testing.T) {
	var deletePayload, purgePayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/operations/deletefile":
			_ = json.NewDecoder(r.Body).Decode(&deletePayload)
			w.WriteHeader(http.StatusOK)
		case "/operations/purge":
			_ = json.NewDecoder(r.Body).Decode(&purgePayload)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rc := NewRC(srv.URL, "u", "p")

	// Delete
	if err := rc.Delete(context.Background(), "myrem:", "path/to/file.txt"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if deletePayload["fs"] != "myrem:" || deletePayload["remote"] != "path/to/file.txt" {
		t.Fatalf("unexpected delete payload: %+v", deletePayload)
	}

	// Purge
	if err := rc.Purge(context.Background(), "myrem:", "path/to/dir"); err != nil {
		t.Fatalf("Purge failed: %v", err)
	}
	if purgePayload["fs"] != "myrem:" || purgePayload["remote"] != "path/to/dir" {
		t.Fatalf("unexpected purge payload: %+v", purgePayload)
	}
}

func TestRCCopyDir(t *testing.T) {
	t.Run("sync error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"remote not configured"}`, http.StatusBadRequest)
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		err := rc.CopyDir(context.Background(), "src:", "dst:")
		var rce *RCError
		if !errors.As(err, &rce) || rce.Msg != "remote not configured" {
			t.Fatalf("expected remote not configured error, got %v", err)
		}
	})

	t.Run("context cancel triggers job stop", func(t *testing.T) {
		stopped := make(chan int64, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/sync/copy":
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 77})
			case "/job/stop":
				var in map[string]any
				_ = json.NewDecoder(r.Body).Decode(&in)
				if jid, ok := in["jobid"].(float64); ok {
					stopped <- int64(jid)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{})
			}
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		err := rc.CopyDir(ctx, "src:", "dst:")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		select {
		case jid := <-stopped:
			if jid != 77 {
				t.Fatalf("stopped jobid = %d, want 77", jid)
			}
		case <-time.After(time.Second):
			t.Fatal("job/stop was not called upon context cancel")
		}
	})

	t.Run("polling success and failure", func(t *testing.T) {
		t.Parallel()
		var pollCount int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/sync/copy":
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 101})
			case "/job/status":
				pollCount++
				_ = json.NewEncoder(w).Encode(map[string]any{
					"finished": true,
					"success":  true,
				})
			}
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		if err := rc.CopyDir(context.Background(), "src:", "dst:"); err != nil {
			t.Fatalf("CopyDir success run failed: %v", err)
		}
	})

	t.Run("polling failed job", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/sync/copy":
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 202})
			case "/job/status":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"finished": true,
					"success":  false,
					"error":    "target out of space",
				})
			}
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		err := rc.CopyDir(context.Background(), "src:", "dst:")
		var rce *RCError
		if !errors.As(err, &rce) || rce.Msg != "target out of space" {
			t.Fatalf("expected 'target out of space', got %v", err)
		}
	})

	t.Run("polling status error", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/sync/copy":
				_ = json.NewEncoder(w).Encode(map[string]any{"jobid": 303})
			case "/job/status":
				http.Error(w, `{"error":"database failure"}`, http.StatusInternalServerError)
			}
		}))
		defer srv.Close()

		rc := NewRC(srv.URL, "u", "p")
		err := rc.CopyDir(context.Background(), "src:", "dst:")
		var rce *RCError
		if !errors.As(err, &rce) || rce.Msg != "database failure" {
			t.Fatalf("expected 'database failure', got %v", err)
		}
	})
}

// TestRCLocalFileBackend_EndToEnd simulates an rclone rcd server backed by a local filesystem directory.
// It verifies real file creation, reading, path traversal protection, deletion, and folder purging with t.TempDir().
func TestRCLocalFileBackend_EndToEnd(t *testing.T) {
	root := t.TempDir()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/operations/mkdir":
			var in struct {
				FS     string `json:"fs"`
				Remote string `json:"remote"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			target := filepath.Join(root, filepath.Clean(in.Remote))
			_ = os.MkdirAll(target, 0755)
			w.WriteHeader(http.StatusOK)

		case strings.HasPrefix(r.URL.Path, "/operations/uploadfile"):
			q := r.URL.Query()
			remote := filepath.Clean(q.Get("remote"))
			file, header, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			defer file.Close()

			targetDir := filepath.Join(root, remote)
			_ = os.MkdirAll(targetDir, 0755)
			dstPath := filepath.Join(targetDir, filepath.Clean(header.Filename))
			out, err := os.Create(dstPath)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			defer out.Close()
			_, _ = io.Copy(out, file)
			w.WriteHeader(http.StatusOK)

		case strings.HasPrefix(r.URL.Path, "/[local:]/"):
			sub := strings.TrimPrefix(r.URL.Path, "/[local:]/")
			target := filepath.Join(root, filepath.Clean(sub))
			// Verify no escaping root
			rel, err := filepath.Rel(root, target)
			if err != nil || strings.HasPrefix(rel, "..") {
				http.Error(w, "forbidden path traversal", http.StatusForbidden)
				return
			}
			data, err := os.ReadFile(target)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)

		case r.URL.Path == "/operations/deletefile":
			var in struct {
				FS     string `json:"fs"`
				Remote string `json:"remote"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			target := filepath.Join(root, filepath.Clean(in.Remote))
			_ = os.Remove(target)
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/operations/purge":
			var in struct {
				FS     string `json:"fs"`
				Remote string `json:"remote"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			target := filepath.Join(root, filepath.Clean(in.Remote))
			_ = os.RemoveAll(target)
			w.WriteHeader(http.StatusOK)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	rc := NewRC(srv.URL, "admin", "secret")
	ctx := context.Background()

	// 1. Mkdir on local backend
	if err := rc.Mkdir(ctx, "local", "reports/2026"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "reports/2026")); err != nil || !info.IsDir() {
		t.Fatalf("expected folder on disk: %v", err)
	}

	// 2. Upload file to local backend
	payload := []byte("quarterly financial report content")
	if err := rc.Upload(ctx, "local:", "reports/2026", "q3.txt", bytes.NewReader(payload)); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	filePath := filepath.Join(root, "reports/2026/q3.txt")
	diskBytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read written file from disk: %v", err)
	}
	if !bytes.Equal(diskBytes, payload) {
		t.Fatalf("written file bytes = %q, want %q", diskBytes, payload)
	}

	// 3. Open and read back via RC.Open
	r, err := rc.Open(ctx, "local:", "reports/2026/q3.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	readBack, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(readBack, payload) {
		t.Fatalf("readBack = %q, want %q", readBack, payload)
	}

	// 4. Path traversal test: attempt to escape root via ../
	// RC.Open cleans the path, turning "../../etc/passwd" into "etc/passwd" relative to local root
	r, err = rc.Open(ctx, "local:", "../../../etc/passwd")
	if err == nil {
		r.Close()
		t.Fatal("Open of non-existent escaped path should have returned 404")
	}

	// 5. Delete file
	if err := rc.Delete(ctx, "local:", "reports/2026/q3.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after Delete, got %v", err)
	}

	// 6. Purge directory
	if err := rc.Purge(ctx, "local:", "reports"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "reports")); !os.IsNotExist(err) {
		t.Fatalf("directory should not exist after Purge, got %v", err)
	}
}
