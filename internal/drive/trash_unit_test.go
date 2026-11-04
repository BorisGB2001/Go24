package drive

import (
	"context"
	"errors"
	"testing"
)

func TestDriveMayRestore(t *testing.T) {
	// Restoring follows the same permissions as changing: managers or the author when write is allowed.
	dManager := Drive{CanManage: true}
	if !dManager.MayRestore("u1", "u2") {
		t.Error("manager should be able to restore any item")
	}

	dAuthor := Drive{CanWrite: true}
	if !dAuthor.MayRestore("u1", "u1") {
		t.Error("author with write permissions should be able to restore their item")
	}
	if dAuthor.MayRestore("u1", "u2") {
		t.Error("non-author without manage permissions should not restore someone else's item")
	}

	dReadOnly := Drive{CanWrite: false}
	if dReadOnly.MayRestore("u1", "u1") {
		t.Error("author without write permissions should not restore item")
	}
}

func TestTrashGuards(t *testing.T) {
	// A shared folder drive cannot view or manipulate the trash; the trash belongs to the owner's drive.
	s := NewStore(nil, nil)
	ctx := context.Background()

	dShared := Drive{Root: "shared-folder-root"}
	if _, err := s.Trash(ctx, dShared); !errors.Is(err, ErrForbidden) {
		t.Errorf("Trash(shared drive) = %v, want ErrForbidden", err)
	}
	if err := s.Restore(ctx, "u1", dShared, Selection{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("Restore(shared drive) = %v, want ErrForbidden", err)
	}
	if err := s.Purge(ctx, "u1", dShared, Selection{}); !errors.Is(err, ErrForbidden) {
		t.Errorf("Purge(shared drive) = %v, want ErrForbidden", err)
	}
}
