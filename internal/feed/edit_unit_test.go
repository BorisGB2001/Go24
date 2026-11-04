package feed

import (
	"context"
	"errors"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestSetAudienceValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Setting both a project and specific recipients is invalid
	for _, to := range []Audience{
		{Users: []string{"u1"}},
		{Depts: []string{"d1"}},
		{Users: []string{"u1"}, Depts: []string{"d1"}},
	} {
		moved, told, err := s.SetAudience(ctx, "actor", "post", "proj1", to)
		if moved || told != nil {
			t.Errorf("SetAudience with project and recipients moved=%v, told=%v, want false, nil", moved, told)
		}
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.bad_form" {
			t.Errorf("SetAudience with project and recipients err = %v, want err.bad_form", err)
		}
	}
}

func TestSetImportantValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	past := time.Now().Add(-1 * time.Hour)
	changed, err := s.SetImportant(ctx, "actor", "post", true, past)
	if changed {
		t.Error("SetImportant with past date returned changed=true, want false")
	}
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.until_past" {
		t.Errorf("SetImportant with past date err = %v, want err.until_past", err)
	}
}

func TestDetachFilesEmpty(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	if err := s.DetachFiles(ctx, "post", nil); err != nil {
		t.Errorf("DetachFiles(nil) err = %v, want nil", err)
	}
	if err := s.DetachFiles(ctx, "post", []string{}); err != nil {
		t.Errorf("DetachFiles([]) err = %v, want nil", err)
	}
}

func TestEditGratitudeValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Invalid badge with people
	err := s.EditGratitude(ctx, "actor", "post", "not_a_badge", []string{"u1"})
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.gratitude" {
		t.Errorf("EditGratitude(invalid badge) err = %v, want err.gratitude", err)
	}

	// Valid badge with empty people
	err = s.EditGratitude(ctx, "actor", "post", "cup", nil)
	if !errors.As(err, &ie) || ie.Key != "err.gratitude" {
		t.Errorf("EditGratitude(nil people) err = %v, want err.gratitude", err)
	}

	err = s.EditGratitude(ctx, "actor", "post", "cup", []string{})
	if !errors.As(err, &ie) || ie.Key != "err.gratitude" {
		t.Errorf("EditGratitude(empty people) err = %v, want err.gratitude", err)
	}
}
