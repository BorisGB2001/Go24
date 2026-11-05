package tasks

import "testing"

func TestStageFinish(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"finish", true},
		{"new", false},
		{"work", false},
		{"custom", false},
		{"", false},
	}

	for _, tc := range tests {
		st := Stage{Code: tc.code}
		if got := st.Finish(); got != tc.want {
			t.Errorf("Stage{Code: %q}.Finish() = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestNormColor(t *testing.T) {
	tests := []struct {
		in        string
		wantColor string
		wantErr   bool
	}{
		{"", StageColors[0], false},
		{"   ", StageColors[0], false},
		{"00C4FB", "00C4FB", false},
		{"#47d1e2", "47D1E2", false},
		{" #ffab00 ", "FFAB00", false},
		{"ZZZZZZ", "", true},
		{"#123", "", true},
		{"1234567", "", true},
		{"blue", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := normColor(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("normColor(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.wantColor {
				t.Errorf("normColor(%q) = %q, want %q", tc.in, got, tc.wantColor)
			}
		})
	}
}

func TestStageOf(t *testing.T) {
	stages := []Stage{
		{ID: "st-new", Code: "new"},
		{ID: "st-work", Code: "work"},
		{ID: "st-qa", Code: ""},
		{ID: "st-finish", Code: "finish"},
	}
	index := map[string]int{
		"st-new":    0,
		"st-work":   1,
		"st-qa":     2,
		"st-finish": 3,
	}

	tests := []struct {
		name string
		task Task
		want int
	}{
		{
			name: "task with explicit stage in index",
			task: Task{StageID: "st-work", Status: StatusInProgress},
			want: 1,
		},
		{
			name: "task without stage, status completed goes to finish stage",
			task: Task{StageID: "", Status: StatusCompleted},
			want: 3, // len(stages) - 1
		},
		{
			name: "task without stage, status awaiting control goes to finish stage",
			task: Task{StageID: "", Status: StatusAwaitingControl},
			want: 3,
		},
		{
			name: "task with unknown stage, status completed goes to finish stage",
			task: Task{StageID: "st-unknown", Status: StatusCompleted},
			want: 3,
		},
		{
			name: "task without stage, status pending goes to first stage",
			task: Task{StageID: "", Status: StatusPending},
			want: 0,
		},
		{
			name: "task without stage, status in progress goes to first stage",
			task: Task{StageID: "", Status: StatusInProgress},
			want: 0,
		},
		{
			name: "task without stage, status deferred goes to first stage",
			task: Task{StageID: "", Status: StatusDeferred},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stageOf(tc.task, stages, index); got != tc.want {
				t.Errorf("stageOf() = %d, want %d", got, tc.want)
			}
		})
	}
}
