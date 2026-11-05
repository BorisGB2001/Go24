package tasks

import (
	"slices"
	"testing"
)

func TestMentionAt_WordBoundaries(t *testing.T) {
	tests := []struct {
		name string
		body string
		who  string
		want int
	}{
		{
			name: "mention at start with comma",
			body: "@Иван, привет",
			who:  "Иван",
			want: 0,
		},
		{
			name: "mention in middle with space",
			body: "Привет @Иван как дела?",
			who:  "Иван",
			want: len("Привет "),
		},
		{
			name: "mention at end of body",
			body: "Спасибо @Иван",
			who:  "Иван",
			want: len("Спасибо "),
		},
		{
			name: "mention followed by punctuation colon",
			body: "Внимание @Иван: срочно",
			who:  "Иван",
			want: len("Внимание "),
		},
		{
			name: "mention followed by exclamation mark",
			body: "Отлично, @Иван!",
			who:  "Иван",
			want: len("Отлично, "),
		},
		{
			name: "mention followed by Cyrillic letter does not match",
			body: "Привет @Иванов",
			who:  "Иван",
			want: -1,
		},
		{
			name: "mention followed by digit does not match",
			body: "Проверь @User1",
			who:  "User",
			want: -1,
		},
		{
			name: "mention followed by Latin letter does not match",
			body: "Contact @JohnDoe for details",
			who:  "John",
			want: -1,
		},
		{
			name: "first occurrence is prefix of another word, second is exact match",
			body: "@Иванов и @Иван, здравствуйте",
			who:  "Иван",
			want: len("@Иванов и "),
		},
		{
			name: "no at sign in body",
			body: "Привет Иван",
			who:  "Иван",
			want: -1,
		},
		{
			name: "name not present",
			body: "Привет @Петр",
			who:  "Иван",
			want: -1,
		},
		{
			name: "two-word name with punctuation",
			body: "Смотри, @Анна Ли: задача готова",
			who:  "Анна Ли",
			want: len("Смотри, "),
		},
		{
			name: "two-word name prefix of longer name",
			body: "@Анна Лиина и @Анна Ли",
			who:  "Анна Ли",
			want: len("@Анна Лиина и "),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MentionAt(tc.body, tc.who)
			if got != tc.want {
				t.Errorf("MentionAt(%q, %q) = %d, want %d", tc.body, tc.who, got, tc.want)
			}
		})
	}
}

func TestMarkMentions_Complex(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		names []string
		want  string
	}{
		{
			name:  "plain names marked with at",
			body:  "Привет, Анна Ли и Иван!",
			names: []string{"Иван", "Анна Ли"},
			want:  "Привет, @Анна Ли и @Иван!",
		},
		{
			name:  "already marked mention is not doubled",
			body:  "Привет, @Анна Ли и Иван!",
			names: []string{"Анна Ли", "Иван"},
			want:  "Привет, @Анна Ли и @Иван!",
		},
		{
			name:  "multiple distinct names",
			body:  "Анна Ли и Петр Семёнов",
			names: []string{"Анна Ли", "Петр Семёнов"},
			want:  "@Анна Ли и @Петр Семёнов",
		},
		{
			name:  "empty names in slice ignored",
			body:  "Привет, Петр",
			names: []string{"", "Петр", ""},
			want:  "Привет, @Петр",
		},
		{
			name:  "no names in text",
			body:  "Обычный текст без имен",
			names: []string{"Иван", "Борис"},
			want:  "Обычный текст без имен",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MarkMentions(tc.body, tc.names)
			if got != tc.want {
				t.Errorf("MarkMentions() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUniqMentions(t *testing.T) {
	author := "user-author"
	list1 := []string{"u1", "u2", "user-author", "u1", ""}
	list2 := []string{"u3", "u2", "user-author", ""}
	list3 := []string{"u4"}

	got := uniq(author, list1, list2, list3)
	want := []string{"u1", "u2", "u3", "u4"}

	if !slices.Equal(got, want) {
		t.Errorf("uniq() = %v, want %v", got, want)
	}

	// Empty calls
	if gotEmpty := uniq(author); len(gotEmpty) != 0 {
		t.Errorf("uniq(author) = %v, want empty", gotEmpty)
	}
}

func TestMentionIDs(t *testing.T) {
	input := []Mention{
		{ID: "1", Name: "One"},
		{ID: "2", Name: "Two"},
	}
	got := mentionIDs(input)
	want := []string{"1", "2"}
	if !slices.Equal(got, want) {
		t.Errorf("mentionIDs() = %v, want %v", got, want)
	}
}
