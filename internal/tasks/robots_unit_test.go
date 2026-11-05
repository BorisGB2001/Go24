package tasks

import (
	"context"
	"testing"
	"time"
)

func TestRobotParamsWait(t *testing.T) {
	tests := []struct {
		name   string
		params RobotParams
		want   time.Duration
	}{
		{
			name:   "minutes delay",
			params: RobotParams{Delay: 15, Unit: "min"},
			want:   15 * time.Minute,
		},
		{
			name:   "hours delay",
			params: RobotParams{Delay: 3, Unit: "hour"},
			want:   3 * time.Hour,
		},
		{
			name:   "days delay",
			params: RobotParams{Delay: 2, Unit: "day"},
			want:   48 * time.Hour,
		},
		{
			name:   "zero delay",
			params: RobotParams{Delay: 0, Unit: "min"},
			want:   0,
		},
		{
			name:   "unknown unit returns 0",
			params: RobotParams{Delay: 10, Unit: "week"},
			want:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.params.Wait(); got != tc.want {
				t.Errorf("Wait() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRobotContextHelpers(t *testing.T) {
	ctx := context.Background()

	// depthOf
	if got := depthOf(ctx); got != 0 {
		t.Errorf("depthOf(empty) = %d, want 0", got)
	}
	ctxWithDepth := context.WithValue(ctx, robotDepth{}, 3)
	if got := depthOf(ctxWithDepth); got != 3 {
		t.Errorf("depthOf(3) = %d, want 3", got)
	}

	// isRobot
	if got := isRobot(ctx); got != false {
		t.Errorf("isRobot(empty) = %v, want false", got)
	}
	ctxWithRobot := context.WithValue(ctx, robotKey{}, true)
	if got := isRobot(ctxWithRobot); got != true {
		t.Errorf("isRobot(true) = %v, want true", got)
	}
}
