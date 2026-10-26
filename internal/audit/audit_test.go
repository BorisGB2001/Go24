package audit

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowser(t *testing.T) {
	for agent, want := range map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36":               "Chrome 130 · Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0": "Edge 130 · Windows",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15":            "Safari 17 · macOS",
		"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                                                        "Firefox 131 · Linux",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148":    "Safari 17 · iOS",
		"curl/8.5.0": "curl/8.5.0",
		"":           "",
	} {
		if got := Browser(agent); got != want {
			t.Errorf("Browser(%q) = %q, want %q", agent, got, want)
		}
	}
}

func TestClientIP(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.18.0.5:41000" // caddy in the compose network
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 192.168.1.20")
	if ip := ClientIP(r); ip != "192.168.1.20" {
		t.Errorf("behind the proxy: %s", ip)
	}
	r.RemoteAddr = "8.8.8.8:5000" // a public peer cannot speak for another address
	if ip := ClientIP(r); ip != "8.8.8.8" {
		t.Errorf("a public peer: %s", ip)
	}
	r.RemoteAddr = "10.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "garbage")
	if ip := ClientIP(r); ip != "10.0.0.1" {
		t.Errorf("a bad header: %s", ip)
	}
}

func TestBadDue(t *testing.T) {
	s := NewStore(nil)
	now := time.Now()
	if !s.badDue("1.1.1.1", now) || s.badDue("1.1.1.1", now.Add(30*time.Second)) || !s.badDue("2.2.2.2", now) {
		t.Error("a wrong key is written once a minute per address")
	}
	if !s.badDue("1.1.1.1", now.Add(61*time.Second)) {
		t.Error("a minute later it is written again")
	}
}
