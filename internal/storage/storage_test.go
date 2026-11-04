package storage

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// readAWSChunked decodes the AWS Signature Version 4 chunked transfer encoding stream.
func readAWSChunked(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(r)
	var out []byte
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if idx := strings.Index(line, ";"); idx != -1 {
			line = line[:idx]
		}
		size, err := strconv.ParseInt(line, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			break
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		// Trailing \r\n
		if _, err := br.ReadString('\n'); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func TestStorageNew(t *testing.T) {
	t.Run("bucket exists on startup", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cleanPath := strings.Trim(r.URL.Path, "/")
			if cleanPath == "mybucket" {
				if r.Method == http.MethodGet && r.URL.Query().Has("location") {
					w.Header().Set("Content-Type", "application/xml")
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))
					return
				}
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		ctx := context.Background()
		st, err := New(ctx, srv.Listener.Addr().String(), "key", "secret", "mybucket", false)
		if err != nil {
			t.Fatalf("New with existing bucket: %v", err)
		}
		if st.bucket != "mybucket" {
			t.Fatalf("st.bucket = %q, want 'mybucket'", st.bucket)
		}
	})

	t.Run("bucket created if missing", func(t *testing.T) {
		var created bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cleanPath := strings.Trim(r.URL.Path, "/")
			if cleanPath == "newbucket" {
				if r.Method == http.MethodGet && r.URL.Query().Has("location") {
					if !created {
						http.NotFound(w, r)
						return
					}
					w.Header().Set("Content-Type", "application/xml")
					_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))
					return
				}
				if r.Method == http.MethodHead {
					if !created {
						http.NotFound(w, r)
						return
					}
					w.WriteHeader(http.StatusOK)
					return
				}
				if r.Method == http.MethodPut {
					created = true
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		ctx := context.Background()
		st, err := New(ctx, srv.Listener.Addr().String(), "key", "secret", "newbucket", false)
		if err != nil {
			t.Fatalf("New with missing bucket: %v", err)
		}
		if !created {
			t.Fatal("bucket was not created")
		}
		if st == nil {
			t.Fatal("expected Store instance, got nil")
		}
	})

	t.Run("context cancel terminates retry loop", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled
		_, err := New(ctx, srv.Listener.Addr().String(), "key", "secret", "anybucket", false)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("invalid endpoint returns error", func(t *testing.T) {
		ctx := context.Background()
		_, err := New(ctx, "invalid://host:bad_port", "key", "secret", "bucket", false)
		if err == nil {
			t.Fatal("expected minio client init error on invalid endpoint")
		}
	})
}

func TestStoragePing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := strings.Trim(r.URL.Path, "/")
		switch cleanPath {
		case "exists":
			if r.Method == http.MethodGet && r.URL.Query().Has("location") {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))
				return
			}
			w.WriteHeader(http.StatusOK)
		case "missing":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchBucket</Code><Message>The specified bucket does not exist.</Message></Error>`))
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	addr := srv.Listener.Addr().String()

	// Existing bucket
	st, err := New(ctx, addr, "key", "secret", "exists", false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := st.Ping(ctx); err != nil {
		t.Fatalf("Ping on existing bucket: %v", err)
	}

	// Missing bucket
	st.bucket = "missing"
	err = st.Ping(ctx)
	if err == nil || !strings.Contains(err.Error(), "bucket missing not found") {
		t.Fatalf("Ping on missing bucket want 'bucket missing not found', got %v", err)
	}

	// Broken server returns error
	st.bucket = "broken"
	err = st.Ping(ctx)
	if err == nil {
		t.Fatal("Ping on broken bucket should return error")
	}
}

func TestStorageEnsureBucket(t *testing.T) {
	var makeBucketCalled bool
	var failMakeBucket bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := strings.Trim(r.URL.Path, "/")
		switch cleanPath {
		case "exists":
			if r.Method == http.MethodGet && r.URL.Query().Has("location") {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))
				return
			}
			w.WriteHeader(http.StatusOK)
		case "not-exists":
			if r.Method == http.MethodPut {
				makeBucketCalled = true
				if failMakeBucket {
					w.WriteHeader(http.StatusConflict)
					return
				}
				w.WriteHeader(http.StatusOK)
				return
			}
			http.NotFound(w, r)
		case "server-error":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	addr := srv.Listener.Addr().String()
	st, err := New(ctx, addr, "key", "secret", "exists", false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// ensureBucket on existing bucket does nothing
	if err := st.ensureBucket(ctx); err != nil {
		t.Fatalf("ensureBucket: %v", err)
	}

	// ensureBucket on non-existing bucket calls MakeBucket
	st.bucket = "not-exists"
	if err := st.ensureBucket(ctx); err != nil {
		t.Fatalf("ensureBucket missing: %v", err)
	}
	if !makeBucketCalled {
		t.Fatal("MakeBucket was not called")
	}

	// MakeBucket fails
	failMakeBucket = true
	if err := st.ensureBucket(ctx); err == nil {
		t.Fatal("ensureBucket should fail when MakeBucket fails")
	}

	// BucketExists fails with server error
	st.bucket = "server-error"
	if err := st.ensureBucket(ctx); err == nil {
		t.Fatal("ensureBucket should fail when BucketExists returns error")
	}
}

func TestStoragePutGetWalk(t *testing.T) {
	var mu sync.Mutex
	objects := make(map[string][]byte)
	var failPut, failList bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := strings.Trim(r.URL.Path, "/")
		parts := strings.SplitN(cleanPath, "/", 2)
		bucket := parts[0]
		key := ""
		if len(parts) > 1 {
			key = parts[1]
		}

		if bucket != "mybucket" {
			http.NotFound(w, r)
			return
		}

		// Bucket endpoints
		if key == "" {
			if r.Method == http.MethodGet && r.URL.Query().Has("location") {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`))
				return
			}
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodGet {
				if failList {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				var entries []string
				for k, v := range objects {
					entries = append(entries, fmt.Sprintf(`<Contents><Key>%s</Key><Size>%d</Size><LastModified>2026-10-28T00:00:00.000Z</LastModified></Contents>`, k, len(v)))
				}
				xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>mybucket</Name>
  <IsTruncated>false</IsTruncated>
  %s
</ListBucketResult>`, strings.Join(entries, "\n"))
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(xml))
				return
			}
			http.NotFound(w, r)
			return
		}

		// Object endpoints
		switch r.Method {
		case http.MethodPut:
			if failPut {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			var data []byte
			var err error
			if strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
				data, err = readAWSChunked(r.Body)
			} else {
				data, err = io.ReadAll(r.Body)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			mu.Lock()
			objects[key] = data
			mu.Unlock()
			w.Header().Set("ETag", `"0123456789abcdef"`)
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			mu.Lock()
			data, ok := objects[key]
			mu.Unlock()
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
				return
			}
			w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	addr := srv.Listener.Addr().String()
	st, err := New(ctx, addr, "key", "secret", "mybucket", false)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1. Put object
	data1 := []byte("first object bytes")
	if err := st.Put(ctx, "folder/first.txt", bytes.NewReader(data1), int64(len(data1)), "text/plain"); err != nil {
		t.Fatalf("Put first.txt: %v", err)
	}

	data2 := []byte("second object bytes")
	if err := st.Put(ctx, "second.bin", bytes.NewReader(data2), int64(len(data2)), "application/octet-stream"); err != nil {
		t.Fatalf("Put second.bin: %v", err)
	}

	// Put failure
	failPut = true
	if err := st.Put(ctx, "err.txt", bytes.NewReader([]byte("x")), 1, "text/plain"); err == nil {
		t.Fatal("expected Put to fail when server returns 500")
	}
	failPut = false

	// 2. Get existing object
	rc, err := st.Get(ctx, "folder/first.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	read1, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(read1, data1) {
		t.Fatalf("read1 = %q, want %q", read1, data1)
	}

	// Get non-existent object: error returned when reading
	rcMissing, err := st.Get(ctx, "folder/missing.txt")
	if err != nil {
		t.Fatalf("Get returns client error: %v", err)
	}
	_, err = io.ReadAll(rcMissing)
	rcMissing.Close()
	if err == nil {
		t.Fatal("expected NoSuchKey error when reading missing object")
	}

	// 3. Walk all objects
	seen := make(map[string]int64)
	err = st.Walk(ctx, func(key string, size int64, mod time.Time) error {
		seen[key] = size
		if mod.IsZero() {
			t.Errorf("expected non-zero mod time for %s", key)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if seen["folder/first.txt"] != int64(len(data1)) || seen["second.bin"] != int64(len(data2)) {
		t.Fatalf("Walk seen objects: %v", seen)
	}

	// Walk aborts when fn returns error
	abortErr := errors.New("walk stop")
	err = st.Walk(ctx, func(key string, size int64, mod time.Time) error {
		return abortErr
	})
	if !errors.Is(err, abortErr) {
		t.Fatalf("expected abortErr, got %v", err)
	}

	// Walk error when ListObjects fails
	failList = true
	err = st.Walk(ctx, func(key string, size int64, mod time.Time) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected Walk error when server returns 500")
	}
	failList = false

	// Walk on empty bucket
	mu.Lock()
	clear(objects)
	mu.Unlock()
	walkedCount := 0
	err = st.Walk(ctx, func(key string, size int64, mod time.Time) error {
		walkedCount++
		return nil
	})
	if err != nil {
		t.Fatalf("Walk empty bucket: %v", err)
	}
	if walkedCount != 0 {
		t.Fatalf("walked %d objects on empty bucket, want 0", walkedCount)
	}
}
