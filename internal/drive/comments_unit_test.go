package drive

import (
	"context"
	"errors"
	"testing"

	"go24/internal/i18n"
)

func TestAddCommentValidation(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	// Empty comment or comment consisting solely of whitespace characters must be rejected.
	for _, body := range []string{
		"",
		"   ",
		"\t\n\r",
		" \u00a0 ", // non-breaking space
	} {
		_, err := s.AddComment(ctx, "u1", "file-1", body)
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.comment_empty" {
			t.Errorf("AddComment(body=%q) error = %v, want err.comment_empty", body, err)
		}
	}
}

func TestCommentCountsEmpty(t *testing.T) {
	s := NewStore(nil, nil)
	ctx := context.Background()

	got, err := s.CommentCounts(ctx, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("CommentCounts(nil) = (%v, %v), want empty map", got, err)
	}

	got, err = s.CommentCounts(ctx, []string{})
	if err != nil || len(got) != 0 {
		t.Errorf("CommentCounts([]) = (%v, %v), want empty map", got, err)
	}
}
