package auth

import (
	"testing"
	"time"
)

// TestNewDevices: NewDevices constructs Devices holder with the provided pool reference.
func TestNewDevices(t *testing.T) {
	d := NewDevices(nil)
	if d == nil {
		t.Fatal("expected non-nil Devices")
	}
	if d.db != nil {
		t.Errorf("db = %v, want nil", d.db)
	}
}

// TestDeviceFields: Device struct stores session metadata (ID, timestamps, IP, User-Agent).
func TestDeviceFields(t *testing.T) {
	now := time.Now()
	dev := Device{
		ID:      "dev-1",
		Created: now,
		Seen:    now.Add(time.Minute),
		IP:      "192.168.1.10",
		Agent:   "Mozilla/5.0",
	}

	if dev.ID != "dev-1" {
		t.Errorf("ID = %q, want dev-1", dev.ID)
	}
	if !dev.Created.Equal(now) {
		t.Errorf("Created = %v, want %v", dev.Created, now)
	}
	if !dev.Seen.Equal(now.Add(time.Minute)) {
		t.Errorf("Seen = %v, want %v", dev.Seen, now.Add(time.Minute))
	}
	if dev.IP != "192.168.1.10" {
		t.Errorf("IP = %q, want 192.168.1.10", dev.IP)
	}
	if dev.Agent != "Mozilla/5.0" {
		t.Errorf("Agent = %q, want Mozilla/5.0", dev.Agent)
	}
}
