package sign

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTemplateStruct(t *testing.T) {
	now := time.Now()
	tpl := Template{
		ID:         "tpl-100",
		Title:      "Standard Employment Contract",
		FileID:     "file-200",
		FileName:   "contract.docx",
		Signers:    []string{"u-1", "u-2"},
		Reviewers:  []string{"u-3"},
		Observers:  []string{"u-4"},
		RemindDays: 3,
		AuthorID:   "u-admin",
		Author:     "HR Manager",
		CreatedAt:  now,
	}
	if tpl.ID != "tpl-100" || tpl.Title != "Standard Employment Contract" ||
		tpl.FileID != "file-200" || tpl.FileName != "contract.docx" ||
		len(tpl.Signers) != 2 || len(tpl.Reviewers) != 1 || len(tpl.Observers) != 1 ||
		tpl.RemindDays != 3 || tpl.AuthorID != "u-admin" || tpl.Author != "HR Manager" ||
		!tpl.CreatedAt.Equal(now) {
		t.Errorf("Template struct fields mismatch: %+v", tpl)
	}
}

func TestNewTemplate_Validation(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)

	tests := []struct {
		name  string
		title string
	}{
		{"empty title", ""},
		{"whitespace only title", "   \t \n "},
		{"too long title: 301 chars", strings.Repeat("A", 301)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.NewTemplate(ctx, Template{Title: tc.title})
			if !errors.Is(err, ErrTitle) {
				t.Errorf("NewTemplate(%q) error = %v, want ErrTitle", tc.title, err)
			}
		})
	}
}
