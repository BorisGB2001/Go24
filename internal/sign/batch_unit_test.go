package sign

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBatchStruct(t *testing.T) {
	now := time.Now()
	b := Batch{
		ID:         "b-1",
		Title:      "HR Vacation Batch",
		TemplateID: "tpl-1",
		SignerID:   "u-ceo",
		Signer:     "General Director",
		AuthorID:   "u-hr",
		Author:     "HR Specialist",
		CreatedAt:  now,
		Total:      10,
		Signed:     5,
		Refused:    1,
		Cancelled:  1,
		Signing:    3,
		Ready:      2,
	}
	if b.ID != "b-1" || b.Title != "HR Vacation Batch" || b.TemplateID != "tpl-1" ||
		b.SignerID != "u-ceo" || b.Signer != "General Director" ||
		b.AuthorID != "u-hr" || b.Author != "HR Specialist" ||
		!b.CreatedAt.Equal(now) || b.Total != 10 || b.Signed != 5 ||
		b.Refused != 1 || b.Cancelled != 1 || b.Signing != 3 || b.Ready != 2 {
		t.Errorf("Batch struct fields mismatch: %+v", b)
	}
}

func TestNewBatch_Validation(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)

	tests := []struct {
		name  string
		title string
	}{
		{"empty title", ""},
		{"whitespace title", "   \t\n  "},
		{"too long title: 301 chars", strings.Repeat("B", 301)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.NewBatch(ctx, Batch{Title: tc.title})
			if !errors.Is(err, ErrTitle) {
				t.Errorf("NewBatch(%q) error = %v, want ErrTitle", tc.title, err)
			}
		})
	}
}
