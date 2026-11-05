package tasks

import (
	"slices"
	"testing"
)

func TestProjectCollab(t *testing.T) {
	if !(Project{Kind: "collab"}).Collab() {
		t.Error("kind=collab must report Collab() true")
	}
	if (Project{Kind: "project"}).Collab() {
		t.Error("kind=project must report Collab() false")
	}
	if (Project{Kind: "group"}).Collab() {
		t.Error("kind=group must report Collab() false")
	}
	if (Project{Kind: "scrum"}).Collab() {
		t.Error("kind=scrum must report Collab() false")
	}
}

func TestProjectCanRemove(t *testing.T) {
	tests := []struct {
		name    string
		project Project
		member  Member
		want    bool
	}{
		{
			name:    "cannot remove owner",
			project: Project{MyRole: "admin"},
			member:  Member{Role: "owner"},
			want:    false,
		},
		{
			name:    "viewer who cannot manage cannot remove anyone",
			project: Project{MyRole: "member"},
			member:  Member{Role: "member"},
			want:    false,
		},
		{
			name:    "member brought via department cannot be removed directly",
			project: Project{MyRole: "owner"},
			member:  Member{Role: "member", ViaDept: true},
			want:    false,
		},
		{
			name:    "collab manager cannot remove another manager",
			project: Project{Kind: "collab", MyRole: "manager"},
			member:  Member{Role: "manager"},
			want:    false,
		},
		{
			name:    "collab manager can remove a member",
			project: Project{Kind: "collab", MyRole: "manager"},
			member:  Member{Role: "member"},
			want:    true,
		},
		{
			name:    "collab owner can remove a manager",
			project: Project{Kind: "collab", MyRole: "owner"},
			member:  Member{Role: "manager"},
			want:    true,
		},
		{
			name:    "regular project manager can remove another member",
			project: Project{Kind: "project", MyRole: "manager"},
			member:  Member{Role: "member"},
			want:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.project.CanRemove(tc.member); got != tc.want {
				t.Errorf("CanRemove() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCollabLogEntryChanges(t *testing.T) {
	tests := []struct {
		name  string
		entry CollabLogEntry
		want  []string
	}{
		{
			name:  "action is not settings returns nil",
			entry: CollabLogEntry{Action: "info", Detail: "invite_perm=members"},
			want:  nil,
		},
		{
			name:  "detail is empty returns nil",
			entry: CollabLogEntry{Action: "settings", Detail: ""},
			want:  nil,
		},
		{
			name: "single setting change",
			entry: CollabLogEntry{
				Action: "settings",
				Detail: "invite_perm=members",
			},
			want: []string{"invite_perm.members"},
		},
		{
			name: "multiple settings changes",
			entry: CollabLogEntry{
				Action: "settings",
				Detail: "invite_perm=members,chat_history=off",
			},
			want: []string{"invite_perm.members", "chat_history.off"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.entry.Changes()
			if !slices.Equal(got, tc.want) {
				t.Errorf("Changes() = %v, want %v", got, tc.want)
			}
		})
	}
}
