package kb

import (
	"testing"
)

func TestFillVars(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		title string
		vars  PageVars
		want  string
	}{
		{
			name:  "replaces all placeholders",
			text:  "Meeting: {{title}}\nDate: {{date}}\nAuthor: {{user}}",
			title: "Q3 Review",
			vars:  PageVars{Date: "2026-10-28", User: "Alice Smith"},
			want:  "Meeting: Q3 Review\nDate: 2026-10-28\nAuthor: Alice Smith",
		},
		{
			name:  "multiple occurrences of same placeholder",
			text:  "{{title}} by {{user}} (cc: {{user}})",
			title: "Spec",
			vars:  PageVars{User: "Bob"},
			want:  "Spec by Bob (cc: Bob)",
		},
		{
			name:  "empty values for variables",
			text:  "Title: {{title}}, Date: {{date}}, User: {{user}}",
			title: "",
			vars:  PageVars{},
			want:  "Title: , Date: , User: ",
		},
		{
			name:  "no placeholders in text",
			text:  "Static content without variables",
			title: "Ignored",
			vars:  PageVars{Date: "2026-01-01", User: "Eve"},
			want:  "Static content without variables",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fillVars(tc.text, tc.title, tc.vars)
			if got != tc.want {
				t.Fatalf("fillVars() =\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}
