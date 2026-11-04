package drive

import (
	"context"
	"errors"
	"testing"
)

func TestDriveViewerArg(t *testing.T) {
	dNoViewer := Drive{}
	if got := dNoViewer.viewerArg(); got != nil {
		t.Errorf("dNoViewer.viewerArg() = %v, want nil", got)
	}

	dViewer := Drive{viewer: "u-viewer-1"}
	got := dViewer.viewerArg()
	if got == nil || *got != "u-viewer-1" {
		t.Errorf("dViewer.viewerArg() = %v, want u-viewer-1", got)
	}
}

func TestOrForbidden(t *testing.T) {
	if got := orForbidden(nil); !errors.Is(got, ErrForbidden) {
		t.Errorf("orForbidden(nil) = %v, want ErrForbidden", got)
	}

	customErr := errors.New("custom error")
	if got := orForbidden(customErr); !errors.Is(got, customErr) {
		t.Errorf("orForbidden(customErr) = %v, want customErr", got)
	}

	if got := orForbidden(ErrNotFound); !errors.Is(got, ErrNotFound) {
		t.Errorf("orForbidden(ErrNotFound) = %v, want ErrNotFound", got)
	}
}

func TestMayRight(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// Only company or project drives have folder rights; personal user drives do not.
	if s.MayRight(ctx, Drive{Kind: "user"}, "folder-1") {
		t.Error("MayRight on user drive = true, want false")
	}
	// Folder ID must not be empty (top of drive has no folder rights).
	if s.MayRight(ctx, Drive{Kind: "company"}, "") {
		t.Error("MayRight on company drive with empty folder = true, want false")
	}
	if s.MayRight(ctx, Drive{Kind: "project"}, "") {
		t.Error("MayRight on project drive with empty folder = true, want false")
	}
}

func TestWithinEarlyReturn(t *testing.T) {
	// If viewer is empty or folder is top (""), Within returns the drive unmodified without DB queries.
	s := NewStore(nil, nil)
	ctx := context.Background()

	dUnrestricted := Drive{Kind: "company", CanWrite: true, CanManage: true}
	got, err := s.Within(ctx, dUnrestricted, "folder-1")
	if err != nil || got != dUnrestricted {
		t.Errorf("Within(unrestricted) = (%+v, %v), want (%+v, nil)", got, err, dUnrestricted)
	}

	dViewerAtTop := Drive{Kind: "company", viewer: "u1"}
	got, err = s.Within(ctx, dViewerAtTop, "")
	if err != nil || got != dViewerAtTop {
		t.Errorf("Within(folder=\"\") = (%+v, %v), want (%+v, nil)", got, err, dViewerAtTop)
	}
}

func TestHiddenInsideEarlyReturn(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// When viewer is empty (admin or unrestricted), no folder is hidden.
	hidden, err := s.hiddenInside(ctx, nil, Drive{}, "folder-1")
	if err != nil || hidden {
		t.Errorf("hiddenInside(no viewer) = (%v, %v), want (false, nil)", hidden, err)
	}
}

func TestSetRightValidation(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// Invalid access mode.
	if err := s.SetRight(ctx, "u1", Drive{}, "f1", "all", "", "invalid_access"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRight(invalid access) = %v, want ErrNotFound", err)
	}
	// Invalid kind.
	if err := s.SetRight(ctx, "u1", Drive{}, "f1", "invalid_kind", "u2", "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRight(invalid kind) = %v, want ErrNotFound", err)
	}
	// Kind user or department with empty whomID.
	if err := s.SetRight(ctx, "u1", Drive{}, "f1", "user", "", "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRight(user without whomID) = %v, want ErrNotFound", err)
	}
	if err := s.SetRight(ctx, "u1", Drive{}, "f1", "department", "", "read"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetRight(department without whomID) = %v, want ErrNotFound", err)
	}
	// User drive cannot set folder rights (MayRight fails).
	if err := s.SetRight(ctx, "u1", Drive{Kind: "user"}, "f1", "all", "", "read"); !errors.Is(err, ErrForbidden) {
		t.Errorf("SetRight(user drive) = %v, want ErrForbidden", err)
	}
}

func TestFolderRightsEmpty(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	got, err := s.FolderRights(ctx, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("FolderRights(nil) = (%v, %v), want empty map", got, err)
	}
	got, err = s.FolderRights(ctx, []string{})
	if err != nil || len(got) != 0 {
		t.Errorf("FolderRights([]) = (%v, %v), want empty map", got, err)
	}
}
