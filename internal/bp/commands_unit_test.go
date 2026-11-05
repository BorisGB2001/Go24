package bp

import (
	"context"
	"testing"

	"go24/internal/company"
	"go24/internal/tasks"
)

// TestCommandsVisibility: button visibility based on process status, step kind, and caller permissions.
func TestCommandsVisibility(t *testing.T) {
	cmds := []Command{
		{Title: "Approve", Goto: 3},
		{Title: "Reject", Goto: 0},
	}
	steps := []Step{
		{Kind: "notify", Text: "Start"},
		{
			Kind:     "commands",
			To:       "starter",
			Users:    []string{"usr-1", "usr-2"},
			Commands: cmds,
		},
		{Kind: "notify", Text: "End"},
	}

	inst := Instance{
		Status:    "running",
		Step:      1,
		Steps:     steps,
		StartedBy: company.Person{ID: "usr-starter", Name: "Starter"},
	}

	s := &Store{}
	ctx := context.Background()

	// 1. Process not running
	stoppedInst := inst
	stoppedInst.Status = "stopped"
	if res := s.Commands(ctx, "usr-starter", stoppedInst); res != nil {
		t.Errorf("Commands() expected nil for stopped instance, got %v", res)
	}

	// 2. Step index beyond steps length
	outOfBoundsInst := inst
	outOfBoundsInst.Step = 10
	if res := s.Commands(ctx, "usr-starter", outOfBoundsInst); res != nil {
		t.Errorf("Commands() expected nil for out-of-bounds step, got %v", res)
	}

	// 3. Parallel branches active
	branchInst := inst
	branchInst.Branches = []int{1, 2}
	if res := s.Commands(ctx, "usr-starter", branchInst); res != nil {
		t.Errorf("Commands() expected nil when parallel branches active, got %v", res)
	}

	// 4. Current step is not a 'commands' step
	notifyInst := inst
	notifyInst.Step = 0
	if res := s.Commands(ctx, "usr-starter", notifyInst); res != nil {
		t.Errorf("Commands() expected nil when step is not commands, got %v", res)
	}

	// 5. Administrator context gets commands regardless of user ID
	adminCtx := tasks.WithAdmin(ctx)
	if res := s.Commands(adminCtx, "outsider", inst); len(res) != 2 {
		t.Errorf("Commands(admin) expected 2 commands, got %v", res)
	}

	// 6. Authorized users get commands: starter, or users listed in Users
	for _, uid := range []string{"usr-starter", "usr-1", "usr-2"} {
		res := s.Commands(ctx, uid, inst)
		if len(res) != 2 || res[0].Title != "Approve" {
			t.Errorf("Commands(%q) expected commands, got %v", uid, res)
		}
	}

	// 7. Non-authorized user gets nil
	if res := s.Commands(ctx, "unauthorized_user", inst); res != nil {
		t.Errorf("Commands(unauthorized) expected nil, got %v", res)
	}
}
