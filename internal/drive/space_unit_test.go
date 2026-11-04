package drive

import (
	"context"
	"errors"
	"testing"
)

func TestSpaceUsed(t *testing.T) {
	spZero := Space{}
	if got := spZero.Used(); got != 0 {
		t.Errorf("spZero.Used() = %d, want 0", got)
	}

	sp := Space{
		Files:    1024,
		Versions: 512,
		Trash:    256,
	}
	if got := sp.Used(); got != 1792 {
		t.Errorf("sp.Used() = %d, want 1792", got)
	}
}

func TestBigFileTotal(t *testing.T) {
	bZero := BigFile{}
	if got := bZero.Total(); got != 0 {
		t.Errorf("bZero.Total() = %d, want 0", got)
	}

	b := BigFile{
		Size:         5000,
		VersionsSize: 3000,
	}
	if got := b.Total(); got != 8000 {
		t.Errorf("b.Total() = %d, want 8000", got)
	}
}

func TestDriveMayClean(t *testing.T) {
	for _, c := range []struct {
		name string
		d    Drive
		want bool
	}{
		{"manager of entire drive may clean", Drive{CanManage: true}, true},
		{"viewer without manage rights cannot clean", Drive{CanManage: false}, false},
		{"shared folder cannot be cleaned from sub-view", Drive{CanManage: true, Root: "sh-1"}, false},
		{"shared file cannot be cleaned from sub-view", Drive{CanManage: true, File: "sf-1"}, false},
		{"shared folder with both root and file set cannot clean", Drive{CanManage: true, Root: "sh-1", File: "sf-1"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.mayClean(); got != c.want {
				t.Errorf("Drive.mayClean() = %v, want %v", got, c.want)
			}
			if got := c.d.MayClean(); got != c.want {
				t.Errorf("Drive.MayClean() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNilIfEmpty(t *testing.T) {
	if got := nilIfEmpty(nil); got != nil {
		t.Errorf("nilIfEmpty(nil) = %v, want nil", got)
	}
	if got := nilIfEmpty([]string{}); got != nil {
		t.Errorf("nilIfEmpty([]) = %v, want nil", got)
	}
	items := []string{"file-1", "file-2"}
	got := nilIfEmpty(items)
	if len(got) != 2 || got[0] != "file-1" || got[1] != "file-2" {
		t.Errorf("nilIfEmpty(items) = %v, want %v", got, items)
	}
}

func TestCleanGuards(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	dReadOnly := Drive{CanManage: false}
	if _, err := s.Space(ctx, dReadOnly); !errors.Is(err, ErrForbidden) {
		t.Errorf("Space(CanManage=false) = %v, want ErrForbidden", err)
	}
	if _, err := s.PurgeVersions(ctx, dReadOnly, nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("PurgeVersions(CanManage=false) = %v, want ErrForbidden", err)
	}
	if err := s.EmptyTrash(ctx, "u1", dReadOnly); !errors.Is(err, ErrForbidden) {
		t.Errorf("EmptyTrash(CanManage=false) = %v, want ErrForbidden", err)
	}
}
