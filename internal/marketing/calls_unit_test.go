package marketing

import "testing"

func TestCallListEditable(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"draft", true},
		{"started", false},
		{"done", false},
		{"", false},
	}
	for _, tc := range tests {
		c := CallList{Status: tc.status}
		if got := c.Editable(); got != tc.want {
			t.Errorf("Editable(Status=%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestCallListLeft(t *testing.T) {
	tests := []struct {
		total int
		done  int
		want  int
	}{
		{100, 35, 65},
		{50, 50, 0},
		{0, 0, 0},
		{10, 0, 10},
	}
	for _, tc := range tests {
		c := CallList{Total: tc.total, Done: tc.done}
		if got := c.Left(); got != tc.want {
			t.Errorf("Left(Total=%d, Done=%d) = %d, want %d", tc.total, tc.done, got, tc.want)
		}
	}
}
