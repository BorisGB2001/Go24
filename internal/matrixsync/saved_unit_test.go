package matrixsync

import "testing"

// TestExcerpt tests snippet formatting, rune-based truncation, whitespace collapsing, and file fallback.
func TestExcerpt(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		n    int
		want string
	}{
		{
			name: "short plain text",
			msg:  Message{SenderName: "Alice", Body: "Hello world"},
			n:    20,
			want: "Alice: Hello world",
		},
		{
			name: "collapses whitespace",
			msg:  Message{SenderName: "Bob", Body: "  Hello \n\t  multiple   spaces  "},
			n:    50,
			want: "Bob: Hello multiple spaces",
		},
		{
			name: "truncation with ellipsis",
			msg:  Message{SenderName: "Carol", Body: "123456789012345"},
			n:    10,
			want: "Carol: 1234567890…",
		},
		{
			name: "unicode and Cyrillic runes",
			msg:  Message{SenderName: "Дмитрий", Body: "Привет мир! Это проверка юникода."},
			n:    11,
			want: "Дмитрий: Привет мир!…",
		},
		{
			name: "trailing whitespace before ellipsis is trimmed",
			msg:  Message{SenderName: "Eve", Body: "Short word then lots of text"},
			n:    11, // "Short word " -> trimmed to "Short word" + "…"
			want: "Eve: Short word…",
		},
		{
			name: "file attachment takes file name over body",
			msg: Message{
				SenderName: "Frank",
				Body:       "Ignored body text",
				File:       &File{Name: "Report 2026.pdf"},
			},
			n:    30,
			want: "Frank: Report 2026.pdf",
		},
		{
			name: "empty body",
			msg:  Message{SenderName: "Grace", Body: ""},
			n:    10,
			want: "Grace: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Excerpt(tt.msg, tt.n); got != tt.want {
				t.Errorf("Excerpt() = %q, want %q", got, tt.want)
			}
		})
	}
}
