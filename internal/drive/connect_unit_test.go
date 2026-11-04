package drive

import (
	"context"
	"testing"
)

func TestConnection(t *testing.T) {
	// A shared folder points to folder_id; a project drive points to project_id.
	dShared := Drive{Root: "folder-123", OwnerID: "owner-456"}
	folder, project := connection(dShared)
	if folder == nil || *folder != "folder-123" {
		t.Errorf("connection(dShared) folder = %v, want folder-123", folder)
	}
	if project != nil {
		t.Errorf("connection(dShared) project = %v, want nil", project)
	}

	dProject := Drive{Root: "", OwnerID: "project-uuid-789", Kind: "project"}
	folder, project = connection(dProject)
	if folder != nil {
		t.Errorf("connection(dProject) folder = %v, want nil", folder)
	}
	if project == nil || *project != "project-uuid-789" {
		t.Errorf("connection(dProject) project = %v, want project-uuid-789", project)
	}
}

func TestConnectIsConnectedPure(t *testing.T) {
	// A drive that is neither a project nor a connected shared folder cannot be connected.
	s := NewStore(nil, nil)
	ctx := context.Background()

	for _, d := range []Drive{
		{Kind: "user", Root: ""},
		{Kind: "company", Root: ""},
		{Kind: "other", Root: ""},
	} {
		if s.IsConnected(ctx, "u-1", d) {
			t.Errorf("IsConnected(d=%+v) = true, want false", d)
		}
	}
}
