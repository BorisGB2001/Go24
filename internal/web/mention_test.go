package web

import "testing"

func TestMarkMentions(t *testing.T) {
	mentionNames.Store(&mentionList{names: []string{"Анна Петрова", "Иван Иванов", "Анна"}, ids: map[string]string{"Анна Петрова": "u1", "Иван Иванов": "", "Анна": "u3"}})
	defer mentionNames.Store(nil)
	for in, want := range map[string]string{
		"@Анна Петрова, глянь":  `<span class="mention" data-user="u1">@Анна Петрова</span>, глянь`,
		"@Анна, и @Иван Иванов": `<span class="mention" data-user="u3">@Анна</span>, и <span class="mention">@Иван Иванов</span>`, // two Ivans: no card
		"@Аннаbel":              "@Аннаbel",
	} {
		if got := markMentions(in); got != want {
			t.Errorf("markMentions(%q) = %q, want %q", in, got, want)
		}
	}
}
