package web

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/notify"
	"go24/internal/tasks"
)

func TestChatFeed(t *testing.T) {
	day1 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.Local)
	day2 := day1.Add(24 * time.Hour)
	c := tasks.TaskCard{
		Task: tasks.Task{CreatorID: "a", ResponsibleID: "b"},
		Comments: []tasks.Comment{
			{ID: "m2", AuthorID: "me", Body: "later", CreatedAt: day2},
			{ID: "m1", AuthorID: "x", Body: "first", CreatedAt: day1},
		},
		History: []tasks.HistoryEntry{ // newest first, as GetCard returns it
			{Field: "status", New: "completed", CreatedAt: day2.Add(time.Minute)},
			{Field: "description", CreatedAt: day1.Add(time.Minute)},
			{Field: "created", New: "Задача", CreatedAt: day1.Add(-time.Minute)},
		},
		Accomplices: []tasks.User{{ID: "b"}, {ID: "c"}},
		Auditors:    []tasks.User{{ID: "d"}},
	}
	feed := chatFeed(c, "me", nil)
	if len(feed) != 4 {
		t.Fatalf("feed has %d items, want 4 (description changes are skipped)", len(feed))
	}
	if feed[0].Event == nil || feed[0].Event.Field != "created" || feed[1].Comment.ID != "m1" ||
		feed[2].Comment.ID != "m2" || feed[3].Event.Field != "status" {
		t.Fatalf("wrong order: %+v", feed)
	}
	if !feed[0].NewDay || feed[1].NewDay || !feed[2].NewDay || feed[3].NewDay {
		t.Fatalf("day separators: %+v", feed)
	}
	if feed[1].Mine || !feed[2].Mine {
		t.Fatal("own messages not marked")
	}
	if n := peopleCount(c); n != 4 {
		t.Fatalf("people = %d, want 4 (a, b, c, d)", n)
	}
}

func TestProfileCard(t *testing.T) {
	bundle, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(1990, 3, 8, 0, 0, 0, 0, time.UTC)
	p := company.Profile{ID: "u1", Name: "Пётр Иванов", LastName: "Иванов", FirstName: "Пётр", Position: "Инженер",
		Email: "p@x", MobilePhone: "+7 900", Telegram: "@petr_iv", WhatsApp: "+7 (900) 111-22-33", Website: "https://example.org",
		Birthday: &day, Departments: []company.DeptRef{{ID: "d1", Name: "ИТ"}}}
	for _, l := range bundle.Langs() {
		var b strings.Builder
		err := pages[l.Code]["profile"].ExecuteTemplate(&b, "pcard", map[string]any{
			"P": p, "MatrixID": "@petr:go24.lan", "ElementURL": "https://el", "Me": false})
		if err != nil {
			t.Fatal(l.Code, err)
		}
		out := b.String()
		for _, want := range []string{"https://t.me/petr_iv", "https://wa.me/79001112233", "https://el/#/user/%40petr%3ago24.lan", "Иванов Пётр", "ИТ"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: card lacks %q", l.Code, want)
			}
		}
		if strings.Contains(out, "/logout") {
			t.Errorf("%s: logout on someone else's card", l.Code)
		}
	}
}

func TestIMMessage(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	msgs := []matrixsync.Message{{ID: "$1", SenderName: "Анна", SenderID: "u2", Body: "<b>x</b>", At: time.Now(), Replies: 3},
		{ID: "$2", Mine: true, Body: "моё", At: time.Now(), Edited: true}, {ID: "$3", Encrypted: true, At: time.Now()}}
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", msgs); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"&lt;b&gt;x&lt;/b&gt;", "Ответов: 3", `class="msg mine"`, "🔒", "изменено"} {
		if !strings.Contains(out, want) {
			t.Errorf("im messages lack %q", want)
		}
	}
}

// TestIMFiles: a picture shows in the chat, a voice message is a player with its length, any other file is a card to
// download; the file is given inline only when it is a picture (not SVG), sound or video.
func TestIMFiles(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	msgs := []matrixsync.Message{
		{ID: "$p", RoomID: "!r:x", Body: "a.png", At: time.Now(), File: &matrixsync.File{Kind: "image", Name: "a.png", Mime: "image/png", Width: 40, Height: 30}},
		{ID: "$v", RoomID: "!r:x", Body: "voice.ogg", At: time.Now(), File: &matrixsync.File{Kind: "audio", Name: "voice.ogg", Duration: 65000}},
		{ID: "$f", RoomID: "!r:x", Body: "отчёт.pdf", At: time.Now(), File: &matrixsync.File{Kind: "file", Name: "отчёт.pdf", Size: 2048}},
		{ID: "$t", RoomID: "!r:x", Body: "v2.ogg", At: time.Now(), File: &matrixsync.File{Kind: "audio", Name: "v2.ogg", Duration: 3000, Transcript: "уже расшифровано"}},
		{ID: "$z", RoomID: "!r:x", Body: "all.zip", At: time.Now(), File: &matrixsync.File{Kind: "file", Name: "all.zip", Mime: "application/zip"}},
	}
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", msgs); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`<img src="/im/file?room=%21r%3Ax&amp;event=%24p"`, `width="40"`, `<audio controls preload="none" src="/im/file?room=%21r%3Ax&amp;event=%24v"`,
		"1:05", `href="/im/file?room=%21r%3Ax&amp;event=%24f&amp;dl=1"`, "отчёт.pdf", "2,0 КБ", "уже расшифровано"} {
		if !strings.Contains(out, want) {
			t.Errorf("file messages lack %q:\n%s", want, out)
		}
	}
	// the viewer: a picture and a PDF open over the chat, the PDF keeps its «Скачать»; a ZIP is only to save
	for _, want := range []string{`data-im-view="image" data-name="a.png"`, `data-im-view="pdf" data-name="отчёт.pdf"`, `class="im-filedl"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the viewer's links lack %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "data-im-view") != 2 {
		t.Errorf("only the picture and the PDF go to the viewer:\n%s", out)
	}
	if strings.Count(out, "data-transcribe") != 1 {
		t.Error("«Текст» should be under the voice message without a transcript only")
	}
	for mime, inline := range map[string]bool{"image/png": true, "image/svg+xml": false, "audio/ogg": true, "video/mp4": true,
		"text/html": false, "application/pdf": false} {
		if inlineMedia(mime) != inline {
			t.Errorf("inline %s = %v", mime, !inline)
		}
	}
	// a PDF is shown in the browser for the viewer (also when Matrix knows it only by its name), saved with dl=1
	for _, c := range []struct {
		name, mime, ct, disp string
		dl                   bool
	}{
		{"a.pdf", "", "application/pdf", "inline", false},
		{"a.pdf", "application/pdf", "application/pdf", "attachment", true},
		{"a.png", "image/png", "image/png", "inline", false},
		{"a.html", "text/html", "application/octet-stream", "attachment", false},
		{"a.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/octet-stream", "attachment", false},
	} {
		if ct, disp := imFileType(c.name, c.mime, c.dl); ct != c.ct || disp != c.disp {
			t.Errorf("%s (%s, dl %v) = %s %s", c.name, c.mime, c.dl, ct, disp)
		}
	}
	// the viewer's document: CSV as a table; a ZIP — nothing to show
	if p := imPreview("t.csv", "text/csv", strings.NewReader("Имя;Сумма\nАнна;5")); p["kind"] != "html" || !strings.Contains(fmt.Sprint(p["html"]), "Анна") {
		t.Errorf("csv preview = %v", p)
	}
	if p := imPreview("a.zip", "application/zip", strings.NewReader("PK")); p["kind"] != "" || p["html"] != nil {
		t.Errorf("zip preview = %v", p)
	}
}

// TestIMGroup: a group chat's header — its people, the owner's forms (name, picture, removing people), adding and
// leaving for everybody; the lines about the chat itself.
func TestIMGroup(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	room := matrixsync.Room{ID: "!g:x", Name: "Запуск", Kind: "group"}
	render := func(owner bool) string {
		var b strings.Builder
		g := &matrixsync.Group{Owner: owner, CanEdit: owner, CanAdd: true, Topic: "Про запуск", Rights: matrixsync.Rights{Add: "all", Edit: "admins", Write: "all"},
			Members: []matrixsync.Member{{ID: "u1", Name: "Анна", Owner: true, Me: owner}, {ID: "u2", Name: "Борис", Me: !owner, Removable: owner}, {ID: "u3", Name: "Гость", Guest: true, Removable: owner}}}
		if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imgroup", map[string]any{"G": g, "Room": room}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	own, other := render(true), render(false)
	for _, want := range []string{"/im/group/rename", "/im/group/photo", "/im/group/topic", ">Про запуск</textarea>", `value="Запуск"`, "владелец", "Исключить из чата: Борис?", "/im/group/add", "/im/group/leave", "> 3"} {
		if !strings.Contains(own, want) {
			t.Errorf("the owner's header lacks %q:\n%s", want, own)
		}
	}
	if strings.Count(own, "/im/group/remove") != 2 {
		t.Error("the owner removes everybody but themselves")
	}
	// the rights: the owner's, each with its choice selected
	for _, want := range []string{"/im/group/rights", "Права доступа", `<select name="add"><option value="all" selected>Все участники`,
		`<select name="edit"><option value="all">Все участники</option><option value="admins" selected>`, "Только владелец"} {
		if !strings.Contains(own, want) {
			t.Errorf("the owner's rights lack %q", want)
		}
	}
	if strings.Contains(other, "/im/group/rights") {
		t.Error("a member sets the rights")
	}
	for _, not := range []string{"/im/group/rename", "/im/group/photo", "/im/group/remove"} {
		if strings.Contains(other, not) {
			t.Errorf("a member who does not own the chat sees %q", not)
		}
	}
	if strings.Contains(other, "/im/group/topic") || !strings.Contains(other, `<p class="small im-topic-full">Про запуск</p>`) {
		t.Error("a member edits the description or does not see it")
	}
	if !strings.Contains(other, "/im/group/add") || !strings.Contains(other, "/im/group/leave") {
		t.Error("a member cannot add people or leave")
	}
	var b strings.Builder
	msgs := []matrixsync.Message{{ID: "$1", SenderName: "Анна", Event: "added", Target: "Борис", At: time.Now()},
		{ID: "$2", SenderName: "Анна", Event: "removed", Target: "Гость", At: time.Now()}, {ID: "$3", Target: "Борис", Event: "left", At: time.Now()},
		{ID: "$4", SenderName: "Анна", Event: "renamed", Body: "Запуск-2", At: time.Now()}, {ID: "$5", SenderName: "Анна", Event: "photo", At: time.Now()},
		{ID: "$6", SenderName: "Анна", Event: "owner", Target: "Борис", At: time.Now()}, {ID: "$7", SenderName: "Борис", Event: "admin", Target: "Анна", At: time.Now()},
		{ID: "$8", SenderName: "Борис", Event: "unadmin", Target: "Анна", At: time.Now()}}
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", msgs); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Анна приглашает в чат: Борис", "Анна исключает из чата: Гость", "Борис покидает чат", "Анна: новое название чата «Запуск-2»", "Анна: новая картинка чата",
		"Анна делает владельцем чата: Борис", "Борис назначает администратором: Анна", "Борис снимает администратора: Анна"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("the lines lack %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), "im-menu") || strings.Count(b.String(), `class="msg system"`) != 8 {
		t.Errorf("a line about the chat is not a message:\n%s", b.String())
	}
}

// TestIMPins: the bar of pins (the latest first, how many), the menu's «Закрепить»/«Открепить», the search results
// with their chat and a link to the message (a thread's reply — to its thread).
func TestIMPins(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(name string, data any) string {
		var b strings.Builder
		if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, name, data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	pins := []matrixsync.Message{{ID: "$2", SenderName: "Анна", Body: "Чек-лист", Pinned: true}, {ID: "$1", SenderName: "Борис", Body: "План", Pinned: true, ThreadRoot: "$0"}}
	bar := exec("impins", pins)
	for _, want := range []string{"Закреплено (2)", "Анна: Чек-лист", `data-goto="$2"`, `data-goto-thread="$0"`, `data-unpin="$1"`} {
		if !strings.Contains(bar, want) {
			t.Errorf("the bar lacks %q:\n%s", want, bar)
		}
	}
	if exec("impins", []matrixsync.Message(nil)) != "" {
		t.Error("a bar without pins")
	}
	list := exec("imlist", []matrixsync.Message{{ID: "$1", RoomID: "!r", Body: "a", Pinned: true, At: time.Now()}, {ID: "$2", RoomID: "!r", Body: "b", At: time.Now()}})
	if !strings.Contains(list, `data-act="unpin"`) || !strings.Contains(list, `data-act="pin"`) {
		t.Errorf("the menu's pin items:\n%s", list)
	}
	hits := exec("imsearch", map[string]any{"Q": "план", "Names": map[string]string{"!r:x": "Запуск"}, "Hits": []matrixsync.Message{
		{ID: "$1", RoomID: "!r:x", SenderName: "Анна", Body: "План запуска", At: time.Now()}, {ID: "$3", RoomID: "!o:x", ThreadRoot: "$0", Body: "в треде", At: time.Now()}}})
	for _, want := range []string{"<b>Запуск</b> · Анна", "План запуска", `href="/im?room=%21r%3Ax&amp;at=%241"`, `href="/im?room=%21o%3Ax&amp;thread=%240"`, `data-hit-thread="$0"`} {
		if !strings.Contains(hits, want) {
			t.Errorf("the results lack %q:\n%s", want, hits)
		}
	}
	if none := exec("imsearch", map[string]any{"Q": "xyz", "Hits": []matrixsync.Message(nil)}); !strings.Contains(none, "Ничего не нашлось: «xyz»") {
		t.Errorf("no results: %s", none)
	}
	// one's own messages: ✓ sent, ✓✓ read with who has read it; others' have no mark
	marks := exec("imlist", []matrixsync.Message{{ID: "$m", Mine: true, Body: "a", At: time.UnixMilli(1700000000000), ReadBy: []string{"Анна", "Борис"}},
		{ID: "$s", Mine: true, Body: "b", At: time.Now()}, {ID: "$o", Body: "c", At: time.Now()}})
	for _, want := range []string{`data-ts="1700000000000"`, `title="Прочитано: Анна, Борис">✓✓`, `data-readers="Анна, Борис"`, `title="Отправлено">✓<`} {
		if !strings.Contains(marks, want) {
			t.Errorf("the read marks lack %q:\n%s", want, marks)
		}
	}
	if strings.Count(marks, "data-tick") != 2 {
		t.Error("a mark under someone else's message")
	}
}

// TestIMPage: the messenger's page with a group chat open — the list with each chat's marks and menu, the group's
// header, the pins, the search pane.
func TestIMPage(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	grp := matrixsync.Room{ID: "!g:x", Name: "Запуск", Kind: "group", Photo: true, Favorite: true, Muted: true}
	rooms := []matrixsync.Room{grp, {ID: "!d:x", Name: "Анна", Kind: "dm", Marked: true, Unread: 2,
		Last: &matrixsync.Message{SenderName: "Анна", Event: "left", Target: "Анна"}}}
	data := map[string]any{"Rooms": rooms, "Room": grp.ID, "Current": grp, "Tab": "all", "Calls": true,
		"Group": &matrixsync.Group{Owner: true, CanEdit: true, Members: []matrixsync.Member{{ID: "u1", Name: "Я", Owner: true, Me: true}}},
		"Pins":  []matrixsync.Message{{ID: "$p", SenderName: "Я", Body: "План", Pinned: true}}}
	var b strings.Builder
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "content", page{Data: data}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`data-favorite="1" data-muted="1" data-marked=""`, `data-favorite="" data-muted="" data-marked="1"`,
		`<img class="im-ico" src="/im/photo?room=%21g%3Ax"`, "Закрепить чат", "Без звука", "Отметить непрочитанным", "Анна покидает чат",
		"/im/group/rename", "Закреплено", "data-search-pane", "data-typing", "Групповой чат"} {
		if !strings.Contains(out, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// the lines of calls: going on (with «Присоединиться»), over, missed
	var lines strings.Builder
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&lines, "imlist", []matrixsync.Message{
		{ID: "$1", RoomID: "!g:x", At: time.Now(), Notice: true, Call: &matrixsync.CallLine{State: "started", Video: true, People: []string{"Анна"}}},
		{ID: "$2", RoomID: "!g:x", At: time.Now(), Notice: true, Call: &matrixsync.CallLine{State: "ended", Secs: 754, People: []string{"Анна", "Борис"}}},
		{ID: "$3", RoomID: "!g:x", At: time.Now(), Notice: true, Call: &matrixsync.CallLine{State: "ended", Secs: 40, People: []string{"Анна", "Борис"}}},
		{ID: "$4", RoomID: "!g:x", At: time.Now(), Notice: true, Call: &matrixsync.CallLine{State: "missed", People: []string{"Анна"}}}}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Анна начинает звонок", `href="/call?room=%21g%3Ax&amp;video=1"`, "Присоединиться", "Звонок завершён · 13 мин · Анна, Борис",
		"Звонок завершён · 40 с · Анна, Борис", "Пропущенный звонок: Анна"} {
		if !strings.Contains(lines.String(), want) {
			t.Errorf("the call lines lack %q:\n%s", want, lines.String())
		}
	}
	if strings.Count(lines.String(), "Присоединиться") != 1 || strings.Contains(lines.String(), "im-menu") {
		t.Error("«Присоединиться» only while the call goes on; no menu on a call's line")
	}
	s := &Server{I18n: bundle}
	if got := s.callText(matrixsync.CallLine{State: "ended", Secs: 754, People: []string{"Анна", "Борис"}}); got != "Звонок завершён · 13 мин · Анна, Борис" {
		t.Errorf("the line for Element: %q", got)
	}
}

// TestIMTask: a task made of a chat message — the title of its first line (a file's name, or who wrote it), the
// description with who, when, which chat, the message and the link to it; the message's menu offers it.
func TestIMTask(t *testing.T) {
	bundle, _ := i18n.Load("")
	s := &Server{I18n: bundle, BaseURL: "http://go24.test/"}
	r := httptest.NewRequest("GET", "/tasks/new", nil)
	r.Header.Set("Accept-Language", "ru-RU")
	at := time.Date(2026, 10, 21, 9, 30, 0, 0, time.Local)
	link := s.imMessageURL("!r:x", "$e")
	if link != "http://go24.test/im?room=%21r%3Ax&at=%24e" {
		t.Fatalf("the link: %s", link)
	}
	title, desc := s.messageTask(r, matrixsync.Message{SenderName: "Анна", At: at, Body: "\n  Сверить счета  \nдо пятницы"}, "Бухгалтерия", link)
	if title != "Сверить счета" || desc != "Сообщение от Анна, 21.10.2026 09:30, чат «Бухгалтерия»\n\n\n  Сверить счета  \nдо пятницы\n\n"+link {
		t.Fatalf("a task of a message: %q %q", title, desc)
	}
	if title, desc := s.messageTask(r, matrixsync.Message{SenderName: "Анна", At: at, Body: "a.pdf", File: &matrixsync.File{Name: "акт.pdf"}}, "", link); title != "акт.pdf" ||
		!strings.HasPrefix(desc, "Сообщение от Анна, 21.10.2026 09:30\n\nакт.pdf") {
		t.Fatalf("a task of a file: %q %q", title, desc)
	}
	if title, _ := s.messageTask(r, matrixsync.Message{SenderName: "Анна", At: at, Body: "  "}, "", link); title != "Сообщение от Анна" {
		t.Fatalf("a task of an empty message: %q", title)
	}
	bundle2, _ := i18n.Load("")
	pages, err := buildPages(bundle2)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", []matrixsync.Message{{ID: "$e", RoomID: "!r:x", Body: "a", At: at}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `href="/tasks/new?im_room=%21r%3Ax&amp;im_event=%24e"`) || !strings.Contains(b.String(), "Создать задачу") {
		t.Errorf("the menu lacks «Создать задачу»:\n%s", b.String())
	}
	if !strings.Contains(b.String(), `href="/calendar/new?im_room=%21r%3Ax&amp;im_event=%24e"`) || !strings.Contains(b.String(), "Создать встречу") {
		t.Errorf("the menu lacks «Создать встречу»:\n%s", b.String())
	}
	b.Reset() // the meeting's form made of the message: its source, the message's address passed on
	e := calendar.Event{Title: title, Start: at, End: at.Add(time.Hour)}
	if err := pages["ru-RU"]["event"].ExecuteTemplate(&b, "content", page{Data: map[string]any{"E": e, "CanEdit": true,
		"IM": &imSource{Room: "!r:x", Event: "$e", Sender: "Анна"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Встреча из сообщения: Анна") || !strings.Contains(b.String(), `name="im_room" value="!r:x"`) ||
		!strings.Contains(b.String(), `name="im_event" value="$e"`) {
		t.Errorf("the meeting's form of a message:\n%s", b.String())
	}
	if got := s.tr(r, "im.event_made", "Планёрка", "21.10.2026 09:30", "http://x/calendar/1"); got != "Создана встреча «Планёрка» на 21.10.2026 09:30: http://x/calendar/1" {
		t.Errorf("the answer in the chat: %q", got)
	}

	// «Создать пост в ленте»: the menu's item; the feed's form with the message, the chat's project chosen, its
	// file named, the message's address passed on; without a message the form is a plain one
	b.Reset()
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", []matrixsync.Message{{ID: "$e", RoomID: "!r:x", Body: "a", At: at}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `href="/feed?im_room=%21r%3Ax&amp;im_event=%24e"`) || !strings.Contains(b.String(), "Создать пост в ленте") {
		t.Errorf("the menu lacks «Создать пост в ленте»:\n%s", b.String())
	}
	feedForm := func(data map[string]any) string {
		b.Reset()
		data["Projects"], data["IMProject"] = []any{tasks.Project{ID: "p1", Name: "Бухгалтерия"}, tasks.Project{ID: "p2", Name: "Склад"}}, cmpOr(data["IMProject"])
		if err := pages["ru-RU"]["feed"].ExecuteTemplate(&b, "content", page{Data: data}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	form := feedForm(map[string]any{"IM": &imSource{Room: "!r:x", Event: "$e", Sender: "Анна"}, "IMBody": desc, "IMFile": "акт.pdf", "IMProject": "p1"})
	for _, want := range []string{"Пост из сообщения: Анна", "акт.pdf", `name="im_room" value="!r:x"`, `name="im_event" value="$e"`,
		`<option value="p1" selected>Бухгалтерия`, `<option value="p2">Склад`, "до пятницы\n\n" + strings.ReplaceAll(link, "&", "&amp;") + "</textarea>"} {
		if !strings.Contains(form, want) {
			t.Errorf("the feed's form of a message lacks %q", want)
		}
	}
	if form := feedForm(map[string]any{}); strings.Contains(form, "im_room") || strings.Contains(form, " selected") {
		t.Errorf("the feed's plain form:\n%s", form)
	}
	if got := s.tr(r, "im.post_made", "http://x/feed#post-1"); got != "Создан пост в ленте: http://x/feed#post-1" {
		t.Errorf("the answer in the chat: %q", got)
	}
}

// cmpOr is the template's value of an optional string: "" when not given.
func cmpOr(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// TestIMSaved: «Избранное» and «Напомнить» — the menu's items, the marks beside the time, when «Через час» and
// «Завтра» mean, the empty list, the reminder's notification.
func TestIMSaved(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(page, name string, data any) string {
		var b strings.Builder
		if err := pages["ru-RU"][page].ExecuteTemplate(&b, name, data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	list := exec("im", "imlist", []matrixsync.Message{{ID: "$1", RoomID: "!r", Body: "a", Saved: true, RemindAt: time.Now().Add(time.Hour), At: time.Now()},
		{ID: "$2", RoomID: "!r", Body: "b", At: time.Now()}})
	for _, want := range []string{`data-act="unsave"`, `data-act="save"`, `data-act="remind"`, `class="im-saved" title="Избранное">★`, `class="im-reminder" title="Напомню `} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q:\n%s", want, list)
		}
	}
	if strings.Count(list, "im-saved") != 1 || strings.Count(list, "im-reminder") != 1 {
		t.Error("a mark on a message neither kept nor to be reminded of")
	}
	if empty := exec("im", "imsearch", map[string]any{"Empty": "пусто"}); !strings.Contains(empty, "пусто") {
		t.Errorf("the empty saved list: %s", empty)
	}
	n := exec("notifications", "nitem", notify.Item{ID: 1, Kind: "im_remind", Body: "Анна: План", CreatedAt: time.Now()})
	if !strings.Contains(n, "⏰ Напоминание о сообщении: Анна: План") {
		t.Errorf("the reminder's notification: %s", n)
	}

	loc := time.FixedZone("CET", 3600)
	now := time.Date(2026, 10, 21, 22, 30, 0, 0, loc)
	for v, want := range map[string]time.Time{"": {}, "1h": now.Add(time.Hour), "tomorrow": time.Date(2026, 10, 22, 9, 0, 0, 0, loc),
		"2026-10-23T14:15": time.Date(2026, 10, 23, 14, 15, 0, 0, loc)} {
		if at, err := remindAt(v, now); err != nil || !at.Equal(want) {
			t.Errorf("remindAt(%q) = %v %v, want %v", v, at, err, want)
		}
	}
	for _, v := range []string{"2026-10-21T22:00", "завтра", "2026-10-23"} {
		if _, err := remindAt(v, now); err == nil {
			t.Errorf("remindAt(%q) took it", v)
		}
	}
}

// TestIMPoll: a poll's message — its answers, the results once one has voted (with who chose what), hidden until
// it is over when so asked; «Отменить голос» and «Завершить опрос» for whom they are.
func TestIMPoll(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(m matrixsync.Message) string {
		var b strings.Builder
		if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "immsg", m); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	answers := func() []matrixsync.PollAnswer {
		return []matrixsync.PollAnswer{{ID: "a1", Text: "Пятница", Count: 2, Pct: 66, Names: "Анна, Борис", Mine: true}, {ID: "a2", Text: "Понедельник", Count: 1, Pct: 33, Names: "Вера"}}
	}
	fresh := exec(matrixsync.Message{ID: "$p", RoomID: "!r", At: time.Now(), Poll: &matrixsync.Poll{Question: "Когда созвон?", Answers: answers()[:2]}})
	for _, want := range []string{"📊 Когда созвон?", `data-poll-answer="a1"`, "Выберите один вариант", "Проголосовали: 0"} {
		if !strings.Contains(fresh, want) {
			t.Errorf("a poll not voted in lacks %q:\n%s", want, fresh)
		}
	}
	for _, not := range []string{"im-poll-bar", "Анна, Борис", "data-poll-end", "data-poll-retract"} {
		if strings.Contains(fresh, not) {
			t.Errorf("a poll not voted in shows %q", not)
		}
	}
	voted := exec(matrixsync.Message{ID: "$p", RoomID: "!r", Mine: true, At: time.Now(), Poll: &matrixsync.Poll{Question: "Когда?", Answers: answers(), Votes: 3, Voted: true, Mine: true, Multi: true}})
	for _, want := range []string{`style="width: 66%"`, "66% · 2", `title="Анна, Борис"`, "✓ Пятница", `data-mine="true"`, "Отменить голос", "Завершить опрос", "Выберите один или несколько", `data-multi="true"`} {
		if !strings.Contains(voted, want) {
			t.Errorf("a poll voted in lacks %q:\n%s", want, voted)
		}
	}
	hidden := exec(matrixsync.Message{ID: "$p", RoomID: "!r", At: time.Now(), Poll: &matrixsync.Poll{Question: "Когда?", Answers: answers(), Votes: 3, Voted: true, Hidden: true}})
	if strings.Contains(hidden, "66%") || strings.Contains(hidden, "Анна, Борис") || !strings.Contains(hidden, "Итоги — после завершения") {
		t.Errorf("a poll with hidden results:\n%s", hidden)
	}
	over := exec(matrixsync.Message{ID: "$p", RoomID: "!r", Mine: true, At: time.Now(), Poll: &matrixsync.Poll{Question: "Когда?", Answers: answers(), Votes: 3, Hidden: true, Ended: true, Mine: true}})
	if !strings.Contains(over, "66% · 2") || !strings.Contains(over, "Опрос завершён") || strings.Contains(over, "data-poll-end") || !strings.Contains(over, " disabled") {
		t.Errorf("an ended poll:\n%s", over)
	}
}

// TestIMChannel: a channel — its mark in the list and the header; its owner switches it and names admins; one who
// only reads it has a note instead of the field.
func TestIMChannel(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	render := func(g *matrixsync.Group) string {
		ch := matrixsync.Room{ID: "!c:x", Name: "Новости", Kind: "channel"}
		data := map[string]any{"Rooms": []matrixsync.Room{ch}, "Room": ch.ID, "Current": ch, "Tab": "all", "Group": g, "ReadOnly": !g.Writer}
		var b strings.Builder
		if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "content", page{Data: data}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	owner := render(&matrixsync.Group{Owner: true, Channel: true, Writer: true, Members: []matrixsync.Member{{ID: "u1", Name: "Я", Owner: true, Me: true},
		{ID: "u2", Name: "Анна", Admin: true}, {ID: "u3", Name: "Борис"}}})
	for _, want := range []string{"i-speakerphone", "> Канал</span>", "Сделать обычным чатом", `action="/im/group/admin"`, "Снять администратора",
		"Сделать администратором", ">Администратор</span>", "data-send", "Канал — пишут только администраторы"} {
		if !strings.Contains(owner, want) {
			t.Errorf("the owner's channel lacks %q", want)
		}
	}
	if strings.Count(owner, `action="/im/group/admin"`) != 2 || strings.Contains(owner, "data-readonly") {
		t.Error("admin buttons for the others only; the owner writes")
	}
	if strings.Count(owner, `action="/im/group/owner"`) != 2 || !strings.Contains(owner, "Сделать владельцем чата: Анна?") || !strings.Contains(owner, "i-crown") {
		t.Error("«Сделать владельцем» for the others")
	}
	reader := render(&matrixsync.Group{Channel: true, Members: []matrixsync.Member{{ID: "u1", Name: "Борис", Owner: true}, {ID: "u3", Name: "Я", Me: true}}})
	if !strings.Contains(reader, "Писать в канале могут только администраторы") || strings.Contains(reader, "data-send>") || strings.Contains(reader, "data-send-thread") ||
		!strings.Contains(reader, `class="im read-only"`) || strings.Contains(reader, "/im/group/admin") || strings.Contains(reader, "/im/group/owner") || strings.Contains(reader, "/im/group/channel") {
		t.Errorf("the channel for one who reads it:\n%s", reader)
	}
	grp := render(&matrixsync.Group{Owner: true, Writer: true, Members: []matrixsync.Member{{ID: "u1", Name: "Я", Owner: true, Me: true}, {ID: "u2", Name: "Анна"}}})
	if !strings.Contains(grp, "Сделать каналом") || !strings.Contains(grp, "Сделать администратором") {
		t.Error("a group chat: «Сделать каналом», admins too")
	}
	noadd := render(&matrixsync.Group{Writer: true, Members: []matrixsync.Member{{ID: "u1", Name: "Борис", Owner: true}, {ID: "u3", Name: "Я", Me: true}}})
	if strings.Contains(noadd, "/im/group/add") || strings.Contains(noadd, "/im/group/rename") {
		t.Error("the rights let them neither add people nor change the chat")
	}
}

// TestIMHistory: the downloaded history — the date, time and author of each message, a thread's replies under its
// first message, files, polls, calls and the lines about the chat.
func TestIMHistory(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{I18n: bundle, pages: pages}
	at := func(min int) time.Time { return time.Date(2026, 10, 21, 9, min, 0, 0, time.Local) }
	msgs := []matrixsync.Message{
		{ID: "$1", SenderName: "Анна", Body: "План:\nпятница", At: at(0)},
		{ID: "$2", SenderName: "Борис", Body: "ок", At: at(1)},
		{ID: "$3", SenderName: "Вера", Body: "в треде", ThreadRoot: "$1", At: at(2)},
		{ID: "$4", SenderName: "Борис", File: &matrixsync.File{Name: "план.pdf"}, Body: "план.pdf", At: at(3)},
		{ID: "$5", SenderName: "Анна", Body: "📊 Когда?", Poll: &matrixsync.Poll{Question: "Когда?", Answers: []matrixsync.PollAnswer{{Text: "Пт"}, {Text: "Пн"}}}, At: at(4)},
		{ID: "$6", SenderName: "Анна", Event: "added", Target: "Глеб", At: at(5)},
		{ID: "$7", SenderName: "Анна", Notice: true, Call: &matrixsync.CallLine{State: "ended", Secs: 754, People: []string{"Анна", "Борис"}}, At: at(6)},
		{ID: "$8", SenderName: "Борис", Forwarded: "Глеб", Body: "смотри", At: at(7)},
	}
	got := s.historyText(httptest.NewRequest("GET", "/im/export", nil), "Запуск", msgs, at(30))
	want := "Чат «Запуск» · история на 21.10.2026 09:30\n\n" +
		"21.10.2026 09:00 Анна: План:\n    пятница\n" +
		"    ↳ 21.10.2026 09:02 Вера: в треде\n" +
		"21.10.2026 09:01 Борис: ок\n" +
		"21.10.2026 09:03 Борис: [Файл: план.pdf]\n" +
		"21.10.2026 09:04 Анна: 📊 Когда? (Пт / Пн)\n" +
		"21.10.2026 09:05 Анна приглашает в чат: Глеб\n" +
		"21.10.2026 09:06 Звонок завершён · 13 мин · Анна, Борис\n" +
		"21.10.2026 09:07 Борис: Переслано от Глеб: смотри\n"
	if got != want {
		t.Errorf("the history:\n%s\nwant:\n%s", got, want)
	}
}

// TestIMMedia: the chat's panel «Медиа, файлы и ссылки» — tabs with counts, pictures as thumbnails, files and voice
// messages to download, links, each a way to its message (in a thread too).
func TestIMMedia(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	at := time.Date(2026, 10, 21, 9, 5, 0, 0, time.Local)
	err = pages["ru-RU"]["im"].ExecuteTemplate(&b, "immedia", chatMedia{Room: "!r",
		Media: []matrixsync.Message{{ID: "$p", SenderName: "Анна", At: at, File: &matrixsync.File{Kind: "image", Name: "снимок.png"}},
			{ID: "$v", SenderName: "Анна", At: at, ThreadRoot: "$t", File: &matrixsync.File{Kind: "video", Name: "ролик.mp4"}}},
		Files: []matrixsync.Message{{ID: "$f", SenderName: "Борис", At: at, File: &matrixsync.File{Kind: "file", Name: "план.pdf", Size: 2048}}},
		Audio: []matrixsync.Message{{ID: "$a", SenderName: "Вера", At: at, File: &matrixsync.File{Kind: "audio", Name: "voice.ogg", Duration: 4200}}},
		Links: []matrixsync.Link{{URL: "https://example.com/a?b=1", MessageID: "$l", SenderName: "Глеб", At: at}}})
	if err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{`Медиа <span class="muted">2</span>`, `Файлы <span class="muted">1</span>`, `Аудио <span class="muted">1</span>`, `Ссылки <span class="muted">1</span>`,
		`<img loading="lazy" src="/im/file?room=%21r&amp;event=%24p"`, `data-goto="$v" data-goto-thread="$t"`, `i-video`,
		`href="/im/file?room=%21r&amp;event=%24f&amp;dl=1" download>`, "план.pdf", "2,0 КБ · Борис", "0:04 · Вера",
		`href="https://example.com/a?b=1" target="_blank"`, `data-goto="$l"`, "21.10.2026 09:05"} {
		if !strings.Contains(got, want) {
			t.Errorf("the panel lacks %q:\n%s", want, got)
		}
	}
	b.Reset()
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "immedia", chatMedia{Room: "!r"}); err != nil || strings.Count(b.String(), "В этом чате пока ничего нет.") != 4 {
		t.Errorf("the empty panel: %v %s", err, b.String())
	}
}

// TestIMNotes: «Заметки» — the button in the list's header, its mark in the list, the header says only you see it,
// and there is no call to make.
func TestIMNotes(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	n := matrixsync.Room{ID: "!n:x", Name: "Заметки", Kind: "notes"}
	data := map[string]any{"Rooms": []matrixsync.Room{n}, "Room": n.ID, "Current": n, "Tab": "all", "Calls": true, "CallLog": true, "CallsUnseen": 2}
	var b strings.Builder
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "content", page{Data: data}); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{`href="/im/notes"`, "i-notebook", "Только вы видите этот чат", `data-kind="notes"`,
		`data-im-tab="channel"`, `data-im-tab="calls"`, `data-calls-unseen >2<`, "data-calls hidden"} {
		if !strings.Contains(out, want) {
			t.Errorf("notes lack %q", want)
		}
	}
	if strings.Contains(out, "/call?room=") {
		t.Error("a call from the chat with oneself")
	}
}
