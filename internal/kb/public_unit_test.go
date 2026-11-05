package kb

import (
	"testing"
)

func TestPublicBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		spaceID string
		token   string
		want    string
	}{
		{
			name:    "rewrites file links and page links",
			body:    "Read [doc](/kb/space1/page2) and see ![pic](/files/123-abc?inline=1).",
			spaceID: "space1",
			token:   "pubtok",
			want:    "Read [doc](/kbp/pubtok/page2) and see ![pic](/kbp/pubtok/f/123-abc?inline=1).",
		},
		{
			name:    "does not rewrite links to other spaces",
			body:    "Check [other](/kb/space2/page9) and [this](/kb/space1/page1).",
			spaceID: "space1",
			token:   "pubtok",
			want:    "Check [other](/kb/space2/page9) and [this](/kbp/pubtok/page1).",
		},
		{
			name:    "external links untouched",
			body:    "Visit [site](https://example.com/files/1).",
			spaceID: "space1",
			token:   "pubtok",
			want:    "Visit [site](https://example.com/files/1).",
		},
		{
			name:    "empty body untouched",
			body:    "",
			spaceID: "space1",
			token:   "pubtok",
			want:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PublicBody(tc.body, tc.spaceID, tc.token)
			if got != tc.want {
				t.Fatalf("PublicBody() =\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestNullable(t *testing.T) {
	if got := nullable(""); got != nil {
		t.Fatalf("nullable(\"\") = %v, want nil", got)
	}

	val := "hello"
	got := nullable(val)
	if got == nil || *got != val {
		t.Fatalf("nullable(%q) = %v, want pointer to %q", val, got, val)
	}
}
