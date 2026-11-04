package booking

import (
	"context"
	"errors"
	"testing"
)

// TestSaveWaitValidation tests validation of client contact information on waiting list entries.
func TestSaveWaitValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{}

	tests := []struct {
		name     string
		nameVal  string
		phoneVal string
	}{
		{"both empty", "", ""},
		{"spaces only", "   ", "   "},
		{"tabs and newlines", "\t\n", "\r\n"},
		{"mixed whitespace", "   ", "\t"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := Wait{ClientName: tc.nameVal, ClientPhone: tc.phoneVal}
			if _, err := s.SaveWait(ctx, "user", w); !errors.Is(err, ErrClient) {
				t.Fatalf("SaveWait() error = %v; want ErrClient", err)
			}
		})
	}
}
