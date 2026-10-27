package sysinfo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRun: the services are checked at once, in the probes' order; an answer below 500 works, 500 and no answer do
// not; the address shown has no credentials or path.
func TestRun(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusUnauthorized) // asked without a key: still alive
		}
	}))
	defer ok.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer bad.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()
	probes := append(append(append(HTTPProbe("a", ok.URL+"/", "/health"), HTTPProbe("b", ok.URL, "/ari")...),
		HTTPProbe("c", bad.URL, "/")...), HTTPProbe("d", slow.URL, "/")...)
	probes = append(probes, Probe{ID: "e", Check: func(context.Context) error { return errors.New("no bucket") }})
	if len(HTTPProbe("none", "", "/")) != 0 {
		t.Error("a service without an address is probed")
	}
	t0 := time.Now()
	got := Run(context.Background(), probes, 300*time.Millisecond)
	if time.Since(t0) > time.Second {
		t.Errorf("checks took %v: not at once", time.Since(t0))
	}
	want := map[string]bool{"a": true, "b": true, "c": false, "d": false, "e": false}
	for i, c := range got {
		if c.ID != probes[i].ID || c.OK != want[c.ID] || c.OK != (c.Error == "") {
			t.Errorf("%d: %+v", i, c)
		}
	}
	if got[2].Error != "HTTP 502 Bad Gateway" || got[4].Error != "no bucket" {
		t.Errorf("errors: %q, %q", got[2].Error, got[4].Error)
	}
	if p := HTTPProbe("m", "https://bot:secret@matrix.example:8448/base/", "/x")[0]; p.Target != "https://matrix.example:8448" ||
		p.URL != "https://bot:secret@matrix.example:8448/base/x" {
		t.Errorf("probe = %+v", p)
	}
}

// TestDisk: the space of a folder; little free space is a trouble, a missing folder an error.
func TestDisk(t *testing.T) {
	d := DiskOf("backup", t.TempDir())
	if d.Error != "" || d.Total <= 0 || d.Free <= 0 || d.Free > d.Total {
		t.Errorf("disk = %+v", d)
	}
	if d := DiskOf("x", "/no/such/folder"); d.Error == "" {
		t.Error("a missing folder has no error")
	}
	for _, c := range []struct {
		total, free int64
		low         bool
	}{{100 << 30, 1 << 30, true}, {100 << 30, 3 << 30, false}, {10 << 30, 900 << 20, true}, {10 << 30, 1100 << 20, false}} {
		if d := (Disk{Total: c.total, Free: c.free}); d.Low() != c.low {
			t.Errorf("%d of %d: low = %v", c.free, c.total, d.Low())
		}
	}
	if (Disk{Total: 200, Free: 50}).UsedPercent() != 75 {
		t.Error("used percent")
	}
}
