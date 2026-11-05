package tasks

import "testing"

func TestTaskDodBlocks(t *testing.T) {
	tests := []struct {
		name string
		dod  *TaskDod
		want bool
	}{
		{
			name: "nil dod does not block",
			dod:  nil,
			want: false,
		},
		{
			name: "not in sprint does not block",
			dod: &TaskDod{
				InSprint: false,
				Type:     ScrumType{Required: true},
				Done:     false,
			},
			want: false,
		},
		{
			name: "in sprint but type not required does not block",
			dod: &TaskDod{
				InSprint: true,
				Type:     ScrumType{Required: false},
				Done:     false,
			},
			want: false,
		},
		{
			name: "in sprint, required, but all items done does not block",
			dod: &TaskDod{
				InSprint: true,
				Type:     ScrumType{Required: true},
				Done:     true,
			},
			want: false,
		},
		{
			name: "in sprint, required, and not done blocks",
			dod: &TaskDod{
				InSprint: true,
				Type:     ScrumType{Required: true},
				Done:     false,
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.dod.Blocks(); got != tc.want {
				t.Errorf("Blocks() = %v, want %v", got, tc.want)
			}
		})
	}
}
