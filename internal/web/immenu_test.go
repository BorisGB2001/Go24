package web

import (
	"strings"
	"testing"
	"time"

	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// TestIMMessageMenu: the «…» of a message gathers, a row of quick reactions with the whole choice,
// «Ответить», «Цитировать», «Переслать» to a chat and by mail, «Поделиться картинкой», «Создать задачу»; the message
// carries its author's photo for the picture.
func TestIMMessageMenu(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	msgs := []matrixsync.Message{{ID: "$t", RoomID: "!r", SenderID: "u1", SenderName: "Анна", SenderPhoto: "p.jpg", Body: "привет", At: time.Now()}}
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", msgs); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	menu := out[strings.Index(out, `class="im-menu"`):]
	menu = menu[:strings.Index(menu, "</details>")]
	order := []string{`class="im-menu-reacts"`, `data-react="👍"`, `data-react-more`, `data-act="reply"`, `data-act="quote"`, `data-act="forward"`,
		`data-act="mail"`, "Переслать по почте", `data-act="share"`, "Поделиться картинкой", "/tasks/new?im_room="}
	at := 0
	for _, want := range order {
		i := strings.Index(menu[at:], want)
		if i < 0 {
			t.Fatalf("the menu lacks %q (or it is out of order):\n%s", want, menu)
		}
		at += i
	}
	if want := `data-photo="` + company.PhotoURL("u1", "p.jpg") + `"`; company.PhotoURL("u1", "p.jpg") == "" || !strings.Contains(out, want) {
		t.Errorf("the message lacks its author's photo:\n%s", out)
	}
}
