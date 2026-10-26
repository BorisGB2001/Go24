package tasks

import (
	"slices"
	"testing"
)

func TestTransition(t *testing.T) {
	creator := Roles{Creator: true}
	resp := Roles{Responsible: true}
	auditor := Roles{Auditor: true}
	manager := Roles{Manager: true}

	cases := []struct {
		name    string
		cur     Status
		action  Action
		roles   Roles
		control bool
		want    Status
		wantErr bool
	}{
		{"start pending", StatusPending, ActionStart, resp, false, StatusInProgress, false},
		{"start deferred", StatusDeferred, ActionStart, resp, false, StatusInProgress, false},
		{"auditor cannot start", StatusPending, ActionStart, auditor, false, StatusPending, true},
		{"pause", StatusInProgress, ActionPause, resp, false, StatusPending, false},
		{"pause needs in_progress", StatusPending, ActionPause, resp, false, StatusPending, true},
		{"complete without control", StatusInProgress, ActionComplete, resp, false, StatusCompleted, false},
		{"complete with control goes to review", StatusInProgress, ActionComplete, resp, true, StatusAwaitingControl, false},
		{"creator completes own task directly", StatusInProgress, ActionComplete, creator, true, StatusCompleted, false},
		{"approve by creator", StatusAwaitingControl, ActionApprove, creator, true, StatusCompleted, false},
		{"responsible cannot approve", StatusAwaitingControl, ActionApprove, resp, true, StatusAwaitingControl, true},
		{"disapprove returns to pending", StatusAwaitingControl, ActionDisapprove, creator, true, StatusPending, false},
		{"defer", StatusPending, ActionDefer, resp, false, StatusDeferred, false},
		{"renew completed by creator", StatusCompleted, ActionRenew, creator, false, StatusPending, false},
		{"renew by project manager", StatusCompleted, ActionRenew, manager, false, StatusPending, false},
		{"auditor cannot renew", StatusCompleted, ActionRenew, auditor, false, StatusCompleted, true},
		{"cannot complete twice", StatusCompleted, ActionComplete, resp, false, StatusCompleted, true},
		{"unknown action", StatusPending, Action("fly"), resp, false, StatusPending, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Transition(c.cur, c.action, c.roles, c.control)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if got != c.want {
				t.Fatalf("status = %s, want %s", got, c.want)
			}
		})
	}
}

func TestAllowedActions(t *testing.T) {
	got := AllowedActions(StatusPending, Roles{Responsible: true}, false)
	want := []Action{ActionStart, ActionComplete, ActionDefer}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := AllowedActions(StatusAwaitingControl, Roles{Creator: true}, true); !slices.Equal(got, []Action{ActionApprove, ActionDisapprove}) {
		t.Fatalf("creator on control: got %v", got)
	}
	if got := AllowedActions(StatusInProgress, Roles{Auditor: true}, false); len(got) != 0 {
		t.Fatalf("auditor must have no actions, got %v", got)
	}
}
