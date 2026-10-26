package web

import (
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// TestIMThreadsMarkup: under a thread root — «Ответов: N» and, when some are unread, «новых: M» picked out; «Треды»
// lists the threads as they come (the unread first is the service's order) with the last reply's author.
func TestIMThreadsMarkup(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 25, 14, 5, 0, 0, time.Local)
	var b strings.Builder
	msgs := []matrixsync.Message{{ID: "$a", RoomID: "!r", SenderName: "Анна", Body: "вопрос", At: at, Replies: 12, NewReplies: 3},
		{ID: "$b", RoomID: "!r", SenderName: "Борис", Body: "прочитано", At: at, Replies: 2},
		{ID: "$c", RoomID: "!r", SenderName: "Борис", Body: "без тредов", At: at}}
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", msgs); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`im-thlink new" data-open-thread="$a" data-replies="12" data-new="3"`, "Ответов: 12", `data-new-text >новых: 3</span>`,
		`im-thlink" data-open-thread="$b" data-replies="2" data-new="0"`, "Ответов: 2", `data-new-text hidden>`, "Ответить в треде"} {
		if !strings.Contains(out, want) {
			t.Errorf("the roots lack %q:\n%s", want, out)
		}
	}
	b.Reset()
	list := []matrixsync.ThreadItem{{Root: msgs[0], LastAt: at, LastBy: "Борис"}, {Root: msgs[1], LastAt: at, LastBy: "Анна"}}
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imthreads", list); err != nil {
		t.Fatal(err)
	}
	out = b.String()
	if i, j := strings.Index(out, `data-open-thread="$a"`), strings.Index(out, `data-open-thread="$b"`); i < 0 || j < i {
		t.Errorf("the threads' order:\n%s", out)
	}
	for _, want := range []string{`im-thitem new" data-open-thread="$a"`, "новых: 3", "последний ответ: Борис, 25.10.2026 14:05", "вопрос"} {
		if !strings.Contains(out, want) {
			t.Errorf("«Треды» lack %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "im-thnew") != 1 {
		t.Errorf("«новых» only where there are some:\n%s", out)
	}
	b.Reset()
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imthreads", []matrixsync.ThreadItem{}); err != nil || !strings.Contains(b.String(), "ещё нет тредов") {
		t.Errorf("no threads: %v %s", err, b.String())
	}
}
