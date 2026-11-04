package drive

import (
	"context"
	"testing"
)

func TestTrimVersionsZero(t *testing.T) {
	// A keep setting of 0 (or negative) means all versions are retained, requiring no deletion query.
	ctx := context.Background()
	if err := trimVersions(ctx, nil, "file-1", 0); err != nil {
		t.Errorf("trimVersions(keep=0) = %v, want nil", err)
	}
	if err := trimVersions(ctx, nil, "file-1", -5); err != nil {
		t.Errorf("trimVersions(keep=-5) = %v, want nil", err)
	}
}
