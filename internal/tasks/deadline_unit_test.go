package tasks

import "testing"

func TestCanChangeDeadline(t *testing.T) {
	tests := []struct {
		name string
		card TaskCard
		want bool
	}{
		{
			name: "dates follow subtasks blocks change",
			card: TaskCard{
				Task: Task{
					Params:   Params{DatesFromSubtasks: true},
					SubTotal: 2,
				},
				Roles: Roles{Creator: true},
			},
			want: false,
		},
		{
			name: "creator can change deadline",
			card: TaskCard{
				Roles: Roles{Creator: true},
			},
			want: true,
		},
		{
			name: "manager can change deadline",
			card: TaskCard{
				Roles: Roles{Manager: true},
			},
			want: true,
		},
		{
			name: "responsible can change when allowed",
			card: TaskCard{
				Task: Task{
					AllowChangeDeadline: true,
				},
				Roles: Roles{Responsible: true},
			},
			want: true,
		},
		{
			name: "responsible cannot change when not allowed",
			card: TaskCard{
				Task: Task{
					AllowChangeDeadline: false,
				},
				Roles: Roles{Responsible: true},
			},
			want: false,
		},
		{
			name: "accomplice cannot change deadline",
			card: TaskCard{
				Task: Task{
					AllowChangeDeadline: true,
				},
				Roles: Roles{Accomplice: true},
			},
			want: false,
		},
		{
			name: "auditor cannot change deadline",
			card: TaskCard{
				Task: Task{
					AllowChangeDeadline: true,
				},
				Roles: Roles{Auditor: true},
			},
			want: false,
		},
		{
			name: "no roles cannot change deadline",
			card: TaskCard{},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.card.CanChangeDeadline(); got != tc.want {
				t.Errorf("CanChangeDeadline() = %v, want %v", got, tc.want)
			}
		})
	}
}
