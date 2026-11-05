package tasks

import (
	"slices"
	"strings"
	"testing"
)

func TestFieldRequiredAt(t *testing.T) {
	tests := []struct {
		name  string
		field Field
		stage string
		want  bool
	}{
		{
			name:  "not required",
			field: Field{Required: false},
			stage: "stage1",
			want:  false,
		},
		{
			name:  "bool field is never required",
			field: Field{Required: true, Kind: "bool"},
			stage: "stage1",
			want:  false,
		},
		{
			name:  "required at all stages when Stages is empty",
			field: Field{Required: true, Kind: "string", Stages: nil},
			stage: "any_stage",
			want:  true,
		},
		{
			name:  "required at specific stage (matching)",
			field: Field{Required: true, Kind: "string", Stages: []string{"stage1", "stage2"}},
			stage: "stage1",
			want:  true,
		},
		{
			name:  "required at specific stage (not matching)",
			field: Field{Required: true, Kind: "string", Stages: []string{"stage1", "stage2"}},
			stage: "stage3",
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.field.RequiredAt(tc.stage); got != tc.want {
				t.Errorf("RequiredAt(%q) = %v, want %v", tc.stage, got, tc.want)
			}
		})
	}
}

func TestFieldValueText(t *testing.T) {
	tests := []struct {
		name       string
		val        FieldValue
		dateLayout string
		want       string
	}{
		{
			name: "date field with valid date",
			val: FieldValue{
				Field: Field{Kind: "date"},
				Value: "2026-10-28",
			},
			dateLayout: "02.01.2006",
			want:       "28.10.2026",
		},
		{
			name: "date field with invalid date returns raw value",
			val: FieldValue{
				Field: Field{Kind: "date"},
				Value: "not-a-date",
			},
			dateLayout: "02.01.2006",
			want:       "not-a-date",
		},
		{
			name: "non-date field returns raw value",
			val: FieldValue{
				Field: Field{Kind: "string"},
				Value: "some text",
			},
			dateLayout: "02.01.2006",
			want:       "some text",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.val.Text(tc.dateLayout); got != tc.want {
				t.Errorf("Text() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStageList(t *testing.T) {
	tests := []struct {
		name     string
		required bool
		stages   []string
		want     []string
	}{
		{
			name:     "not required returns empty slice",
			required: false,
			stages:   []string{"stage1", "stage2"},
			want:     []string{},
		},
		{
			name:     "required cleans whitespace and duplicates",
			required: true,
			stages:   []string{" stage1 ", "", "stage2", "stage1", "  "},
			want:     []string{"stage1", "stage2"},
		},
		{
			name:     "required with empty input",
			required: true,
			stages:   nil,
			want:     []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stageList(tc.required, tc.stages)
			if !slices.Equal(got, tc.want) {
				t.Errorf("stageList() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFieldCheckAndNormalize(t *testing.T) {
	tests := []struct {
		name    string
		field   Field
		in      string
		want    string
		wantErr bool
	}{
		// Empty string clears value
		{
			name:    "empty string clears value",
			field:   Field{Kind: "string", Name: "Text"},
			in:      "   ",
			want:    "",
			wantErr: false,
		},

		// String
		{
			name:    "valid string",
			field:   Field{Kind: "string", Name: "Text"},
			in:      " Hello World ",
			want:    "Hello World",
			wantErr: false,
		},
		{
			name:    "string too long",
			field:   Field{Kind: "string", Name: "Text"},
			in:      strings.Repeat("a", 2001),
			want:    "",
			wantErr: true,
		},

		// Number
		{
			name:    "valid integer",
			field:   Field{Kind: "number", Name: "Amount"},
			in:      " 123 ",
			want:    "123",
			wantErr: false,
		},
		{
			name:    "valid float with comma and spaces",
			field:   Field{Kind: "number", Name: "Amount"},
			in:      " 1 234,56 ",
			want:    "1234.56",
			wantErr: false,
		},
		{
			name:    "invalid number",
			field:   Field{Kind: "number", Name: "Amount"},
			in:      "abc",
			want:    "",
			wantErr: true,
		},

		// Date
		{
			name:    "valid date",
			field:   Field{Kind: "date", Name: "Day"},
			in:      "2026-10-28",
			want:    "2026-10-28",
			wantErr: false,
		},
		{
			name:    "invalid date format",
			field:   Field{Kind: "date", Name: "Day"},
			in:      "28.10.2026",
			want:    "",
			wantErr: true,
		},

		// Bool
		{
			name:    "valid bool (1)",
			field:   Field{Kind: "bool", Name: "Flag"},
			in:      "1",
			want:    "1",
			wantErr: false,
		},
		{
			name:    "invalid bool (true)",
			field:   Field{Kind: "bool", Name: "Flag"},
			in:      "true",
			want:    "",
			wantErr: true,
		},
		{
			name:    "invalid bool (0)",
			field:   Field{Kind: "bool", Name: "Flag"},
			in:      "0",
			want:    "",
			wantErr: true,
		},

		// List
		{
			name:    "valid list option",
			field:   Field{Kind: "list", Name: "Choice", Options: []string{"opt1", "opt2"}},
			in:      "opt2",
			want:    "opt2",
			wantErr: false,
		},
		{
			name:    "invalid list option",
			field:   Field{Kind: "list", Name: "Choice", Options: []string{"opt1", "opt2"}},
			in:      "opt3",
			want:    "",
			wantErr: true,
		},

		// URL
		{
			name:    "valid http url",
			field:   Field{Kind: "url", Name: "Link"},
			in:      "http://example.com/page",
			want:    "http://example.com/page",
			wantErr: false,
		},
		{
			name:    "valid https url",
			field:   Field{Kind: "url", Name: "Link"},
			in:      "https://example.com/page",
			want:    "https://example.com/page",
			wantErr: false,
		},
		{
			name:    "invalid url scheme ftp",
			field:   Field{Kind: "url", Name: "Link"},
			in:      "ftp://example.com",
			want:    "",
			wantErr: true,
		},
		{
			name:    "missing url host",
			field:   Field{Kind: "url", Name: "Link"},
			in:      "https:///path",
			want:    "",
			wantErr: true,
		},
		{
			name:    "url too long",
			field:   Field{Kind: "url", Name: "Link"},
			in:      "https://example.com/" + strings.Repeat("x", 2000),
			want:    "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.field.Check(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Check(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("Check(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
