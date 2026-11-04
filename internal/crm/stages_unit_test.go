package crm

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

func TestStageName_Unit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "valid short name",
			input:   "В работе",
			want:    "В работе",
			wantErr: false,
		},
		{
			name:    "trims spaces",
			input:   "  Согласование счета  \n\t",
			want:    "Согласование счета",
			wantErr: false,
		},
		{
			name:    "empty string fails",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "100 runes succeeds",
			input:   strings.Repeat("ж", 100),
			want:    strings.Repeat("ж", 100),
			wantErr: false,
		},
		{
			name:    "101 runes fails",
			input:   strings.Repeat("ж", 101),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stageName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("stageName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != "err.crm_stage_name" {
					t.Errorf("expected err.crm_stage_name, got %v", err)
				}
			} else if got != tt.want {
				t.Errorf("stageName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStageColor_Unit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "empty color defaults to tasks.StageColors[0]",
			input:   "",
			want:    tasks.StageColors[0],
			wantErr: false,
		},
		{
			name:    "whitespace color defaults to tasks.StageColors[0]",
			input:   "   ",
			want:    tasks.StageColors[0],
			wantErr: false,
		},
		{
			name:    "hash prefix stripped and uppercased",
			input:   "#39a8ef",
			want:    "39A8EF",
			wantErr: false,
		},
		{
			name:    "already uppercase without hash",
			input:   "FFA900",
			want:    "FFA900",
			wantErr: false,
		},
		{
			name:    "invalid length 3 chars fails",
			input:   "#FFF",
			wantErr: true,
		},
		{
			name:    "invalid length 8 chars fails",
			input:   "#FFAA0011",
			wantErr: true,
		},
		{
			name:    "non hex characters fail",
			input:   "ZZZZZZ",
			wantErr: true,
		},
		{
			name:    "special characters fail",
			input:   "39-8EF",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := stageColor(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("stageColor(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != "err.stage_color" {
					t.Errorf("expected err.stage_color, got %v", err)
				}
			} else if got != tt.want {
				t.Errorf("stageColor(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestDefaultStagesContent_Unit(t *testing.T) {
	// Verify standard pipeline stages have the required semantic ordering:
	// process stages first, then success, then failure.
	if len(newPipelineStages) == 0 {
		t.Fatal("newPipelineStages is empty")
	}
	foundSuccess := false
	for i, st := range newPipelineStages {
		if st.code == "" || st.color == "" || st.semantic == "" {
			t.Errorf("stage %d invalid: %+v", i, st)
		}
		if st.semantic == "success" {
			foundSuccess = true
		}
		if foundSuccess && st.semantic == "process" {
			t.Errorf("process stage %s found after success stage", st.code)
		}
	}
	if !foundSuccess {
		t.Error("newPipelineStages must have at least one success stage")
	}

	// Verify smart process stages
	if len(newTypeStages) == 0 {
		t.Fatal("newTypeStages is empty")
	}
	for i, st := range newTypeStages {
		if st.code == "" || st.color == "" || st.semantic == "" {
			t.Errorf("type stage %d invalid: %+v", i, st)
		}
	}
}
