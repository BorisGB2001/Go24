package tasks

import (
	"slices"
	"testing"
)

func TestStatusClosed(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusCompleted, true},
		{StatusPending, false},
		{StatusInProgress, false},
		{StatusAwaitingControl, false},
		{StatusDeferred, false},
		{Status("unknown"), false},
		{Status(""), false},
	}

	for _, tc := range tests {
		if got := tc.status.Closed(); got != tc.want {
			t.Errorf("Status(%q).Closed() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestRolesWorker(t *testing.T) {
	tests := []struct {
		name  string
		roles Roles
		want  bool
	}{
		{name: "creator is worker", roles: Roles{Creator: true}, want: true},
		{name: "responsible is worker", roles: Roles{Responsible: true}, want: true},
		{name: "accomplice is worker", roles: Roles{Accomplice: true}, want: true},
		{name: "manager alone is not worker", roles: Roles{Manager: true}, want: false},
		{name: "auditor alone is not worker", roles: Roles{Auditor: true}, want: false},
		{name: "no roles is not worker", roles: Roles{}, want: false},
		{name: "manager and accomplice is worker", roles: Roles{Manager: true, Accomplice: true}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.roles.Worker(); got != tc.want {
				t.Errorf("Worker() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTransition_Detailed(t *testing.T) {
	creator := Roles{Creator: true}
	resp := Roles{Responsible: true}
	acc := Roles{Accomplice: true}
	auditor := Roles{Auditor: true}
	manager := Roles{Manager: true}

	tests := []struct {
		name    string
		cur     Status
		action  Action
		roles   Roles
		control bool
		want    Status
		wantErr bool
	}{
		// ActionStart
		{"start from pending by accomplice", StatusPending, ActionStart, acc, false, StatusInProgress, false},
		{"start from deferred by creator", StatusDeferred, ActionStart, creator, false, StatusInProgress, false},
		{"start from in_progress fails", StatusInProgress, ActionStart, resp, false, StatusInProgress, true},
		{"start from awaiting_control fails", StatusAwaitingControl, ActionStart, resp, false, StatusAwaitingControl, true},
		{"start from completed fails", StatusCompleted, ActionStart, resp, false, StatusCompleted, true},
		{"start by manager fails", StatusPending, ActionStart, manager, false, StatusPending, true},

		// ActionPause
		{"pause by creator", StatusInProgress, ActionPause, creator, false, StatusPending, false},
		{"pause by accomplice", StatusInProgress, ActionPause, acc, false, StatusPending, false},
		{"pause from deferred fails", StatusDeferred, ActionPause, resp, false, StatusDeferred, true},
		{"pause from completed fails", StatusCompleted, ActionPause, resp, false, StatusCompleted, true},
		{"pause by manager fails", StatusInProgress, ActionPause, manager, false, StatusInProgress, true},

		// ActionComplete
		{"complete from pending directly", StatusPending, ActionComplete, resp, false, StatusCompleted, false},
		{"complete from in_progress by accomplice with control", StatusInProgress, ActionComplete, acc, true, StatusAwaitingControl, false},
		{"complete from in_progress by creator with control completes directly", StatusInProgress, ActionComplete, creator, true, StatusCompleted, false},
		{"complete by manager fails", StatusInProgress, ActionComplete, manager, false, StatusInProgress, true},
		{"complete by auditor fails", StatusInProgress, ActionComplete, auditor, false, StatusInProgress, true},
		{"complete from deferred fails", StatusDeferred, ActionComplete, resp, false, StatusDeferred, true},
		{"complete from awaiting_control fails", StatusAwaitingControl, ActionComplete, resp, false, StatusAwaitingControl, true},

		// ActionApprove
		{"approve by creator", StatusAwaitingControl, ActionApprove, creator, true, StatusCompleted, false},
		{"approve by manager fails", StatusAwaitingControl, ActionApprove, manager, true, StatusAwaitingControl, true},
		{"approve by accomplice fails", StatusAwaitingControl, ActionApprove, acc, true, StatusAwaitingControl, true},
		{"approve from in_progress fails", StatusInProgress, ActionApprove, creator, true, StatusInProgress, true},
		{"approve from pending fails", StatusPending, ActionApprove, creator, true, StatusPending, true},

		// ActionDisapprove
		{"disapprove by creator returns to pending", StatusAwaitingControl, ActionDisapprove, creator, true, StatusPending, false},
		{"disapprove by manager fails", StatusAwaitingControl, ActionDisapprove, manager, true, StatusAwaitingControl, true},
		{"disapprove by responsible fails", StatusAwaitingControl, ActionDisapprove, resp, true, StatusAwaitingControl, true},
		{"disapprove from completed fails", StatusCompleted, ActionDisapprove, creator, true, StatusCompleted, true},

		// ActionDefer
		{"defer from pending by accomplice", StatusPending, ActionDefer, acc, false, StatusDeferred, false},
		{"defer from in_progress by creator", StatusInProgress, ActionDefer, creator, false, StatusDeferred, false},
		{"defer from awaiting_control fails", StatusAwaitingControl, ActionDefer, resp, false, StatusAwaitingControl, true},
		{"defer from completed fails", StatusCompleted, ActionDefer, resp, false, StatusCompleted, true},
		{"defer by manager fails", StatusPending, ActionDefer, manager, false, StatusPending, true},

		// ActionRenew
		{"renew completed by responsible", StatusCompleted, ActionRenew, resp, false, StatusPending, false},
		{"renew deferred by manager", StatusDeferred, ActionRenew, manager, false, StatusPending, false},
		{"renew deferred by creator", StatusDeferred, ActionRenew, creator, false, StatusPending, false},
		{"renew completed by accomplice fails", StatusCompleted, ActionRenew, acc, false, StatusCompleted, true},
		{"renew deferred by accomplice fails", StatusDeferred, ActionRenew, acc, false, StatusDeferred, true},
		{"renew from pending fails", StatusPending, ActionRenew, creator, false, StatusPending, true},
		{"renew from in_progress fails", StatusInProgress, ActionRenew, resp, false, StatusInProgress, true},
		{"renew from awaiting_control fails", StatusAwaitingControl, ActionRenew, manager, false, StatusAwaitingControl, true},

		// Unknown / invalid action
		{"empty action", StatusPending, Action(""), resp, false, StatusPending, true},
		{"unknown action code", StatusPending, Action("cancel"), creator, false, StatusPending, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Transition(tc.cur, tc.action, tc.roles, tc.control)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Transition() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestAllowedActions_Permutations(t *testing.T) {
	creator := Roles{Creator: true}
	resp := Roles{Responsible: true}
	acc := Roles{Accomplice: true}
	manager := Roles{Manager: true}
	auditor := Roles{Auditor: true}

	tests := []struct {
		name    string
		cur     Status
		roles   Roles
		control bool
		want    []Action
	}{
		{
			name:    "pending responsible without control",
			cur:     StatusPending,
			roles:   resp,
			control: false,
			want:    []Action{ActionStart, ActionComplete, ActionDefer},
		},
		{
			name:    "in_progress accomplice with control",
			cur:     StatusInProgress,
			roles:   acc,
			control: true,
			want:    []Action{ActionPause, ActionComplete, ActionDefer},
		},
		{
			name:    "awaiting_control creator",
			cur:     StatusAwaitingControl,
			roles:   creator,
			control: true,
			want:    []Action{ActionApprove, ActionDisapprove},
		},
		{
			name:    "awaiting_control manager (no actions)",
			cur:     StatusAwaitingControl,
			roles:   manager,
			control: true,
			want:    nil,
		},
		{
			name:    "completed responsible",
			cur:     StatusCompleted,
			roles:   resp,
			control: false,
			want:    []Action{ActionRenew},
		},
		{
			name:    "completed manager",
			cur:     StatusCompleted,
			roles:   manager,
			control: false,
			want:    []Action{ActionRenew},
		},
		{
			name:    "completed accomplice (cannot renew)",
			cur:     StatusCompleted,
			roles:   acc,
			control: false,
			want:    nil,
		},
		{
			name:    "deferred worker",
			cur:     StatusDeferred,
			roles:   resp,
			control: false,
			want:    []Action{ActionStart, ActionRenew},
		},
		{
			name:    "auditor has no actions on in_progress",
			cur:     StatusInProgress,
			roles:   auditor,
			control: false,
			want:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AllowedActions(tc.cur, tc.roles, tc.control)
			if !slices.Equal(got, tc.want) {
				t.Errorf("AllowedActions() = %v, want %v", got, tc.want)
			}
		})
	}
}
