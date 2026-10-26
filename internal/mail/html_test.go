package mail

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestSanitize(t *testing.T) {
	got := Sanitize(`<html><head><style>body{color:red}</style><title>x</title></head><body>
		<p class="button" id="mailact" onclick="steal()" style="color: red; position: fixed; background: url(http://t/x.png); font-family: 'Times New Roman'">Привет</p>
		<script>alert(1)</script><iframe src="http://evil"></iframe><form action="/x"><input name="a">Текст</form>
		<a href="javascript:alert(1)">плохо</a> <a href="https://go24.test/x?a=1&amp;b=2">хорошо</a>
		<img src="cid:pic1@x"><img src="http://t/pixel.gif" onerror="x()"><img src="/files/0d8f9a6e-5b6a-4d0e-9a39-2f7c1e1f0a11?inline=1"><img src="file:///etc/passwd">
		<!--[if mso]><p>mso</p><![endif]--><table border="1"><tr><td colspan="2" background="http://t/b.png">ячейка</td></tr></table>
		<div data-signature="s1">Анна</div><svg><script>x()</script></svg></body></html>`)
	for _, bad := range []string{"<style", "<script", "<iframe", "<form", "<input", "onclick", "onerror", "class=", "id=", "position", "url(",
		"javascript", "file:", "background=", "mso", "<svg", "<title"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q kept in %s", bad, got)
		}
	}
	for _, good := range []string{`<p style="color: red; font-family: &#39;Times New Roman&#39;">Привет</p>`, "Текст", `href="https://go24.test/x?a=1&amp;b=2"`,
		`target="_blank" rel="noopener noreferrer"`, `src="cid:pic1@x"`, `src="http://t/pixel.gif"`, `src="/files/0d8f9a6e-5b6a-4d0e-9a39-2f7c1e1f0a11?inline=1"`,
		`<td colspan="2">ячейка</td>`, `<div data-signature="s1">Анна</div>`, `>плохо</a>`} {
		if !strings.Contains(got, good) {
			t.Errorf("%q lost in %s", good, got)
		}
	}
	if !OutsidePictures(got) || OutsidePictures(Sanitize(`<img src="cid:a">`)) {
		t.Error("outside pictures are not told apart")
	}
	if w := WithPictures(got, map[string]string{"pic1@x": "f1"}); !strings.Contains(w, `src="/files/f1?inline=1"`) {
		t.Errorf("the letter's own picture is not pointed at its file: %s", w)
	}
}

func TestHTMLText(t *testing.T) {
	got := HTMLText(`<p>Добрый <b>день</b>,</p><p>список:</p><ul><li>один</li><li>два</li></ul><p>сайт <a href="https://go24.test">здесь</a><br>Анна</p>`)
	want := "Добрый день,\n\nсписок:\n\n- один\n- два\n\nсайт здесь <https://go24.test>\nАнна"
	if got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
	if TextHTML("a < b\nc") != "a &lt; b<br>c" {
		t.Fatal("plain text as HTML")
	}
}

func TestImportance(t *testing.T) {
	for _, c := range []struct {
		imp, xp, p string
		want       int
	}{{"High", "", "", 1}, {"low", "", "", -1}, {"", "1 (Highest)", "", 1}, {"", "5", "", -1}, {"", "3", "", 0}, {"", "", "urgent", 1}, {"", "", "", 0}} {
		if got := importanceOf(c.imp, c.xp, c.p); got != c.want {
			t.Errorf("importance %+v = %d", c, got)
		}
	}
}

// TestCompose: the letter as the recipients get it — the text with its HTML, the pictures of the HTML, the files;
// the blind copy only in the sender's own copy; importance and receipts in the headers; it parses back.
func TestCompose(t *testing.T) {
	e := envelope{From: mail.Address{Name: "Анна", Address: "anna@go24.test"}, To: []*mail.Address{{Name: "Клиент", Address: "boss@client.test"}},
		CC: []*mail.Address{{Address: "cc@client.test"}}, BCC: []*mail.Address{{Address: "boss2@go24.test"}}, Host: "go24.test", Now: time.Now()}
	l := Letter{Subject: "Счёт", HTML: `<p>Готово, <b>счёт</b> во вложении</p><img src="cid:p1@go24">`, Text: "Готово, счёт во вложении",
		Files:      []Attachment{{Name: "счёт.txt", ContentType: "text/plain", Data: []byte("100 руб")}, {Name: "logo.png", ContentType: "image/png", Data: []byte("PNG"), ContentID: "p1@go24"}},
		Importance: 1, Receipt: true, Refs: []string{"root@x", "last@x"}}
	msg, own := compose(e, l)
	s := string(msg)
	for _, want := range []string{"multipart/mixed", "multipart/alternative", "multipart/related", "Importance: High", "X-Priority: 1",
		"Disposition-Notification-To: ", "Cc: <cc@client.test>", "In-Reply-To: <last@x>", "References: <root@x> <last@x>", "Content-Id: <p1@go24>"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "Bcc:") || strings.Contains(s, "boss2@") {
		t.Error("the blind copy is in what the recipients get")
	}
	if !strings.HasSuffix(own, "go24@go24.test") {
		t.Errorf("message id %q", own)
	}
	p := parseFull(bytes.NewReader(msg))
	if p.body != "Готово, счёт во вложении" || !strings.Contains(p.html, "<b>счёт</b>") || p.importance != 1 || p.receiptTo != "anna@go24.test" {
		t.Fatalf("parsed back: %+v", p)
	}
	if len(p.files) != 2 || p.files[0].Name != "счёт.txt" && p.files[1].Name != "счёт.txt" {
		t.Fatalf("files parsed back: %+v", p.files)
	}
	for _, f := range p.files {
		if f.Name == "logo.png" && f.ContentID != "p1@go24" {
			t.Errorf("the picture lost its Content-ID: %+v", f)
		}
	}
	e.WithBCC = true
	if c, _ := compose(e, l); !strings.Contains(string(c), "Bcc: <boss2@go24.test>") {
		t.Error("the sender's copy has no blind copy")
	}
	// a plain letter with a file only
	plain, _ := compose(envelope{From: mail.Address{Address: "anna@go24.test"}, To: e.To, Host: "go24.test", Now: time.Now()},
		Letter{Subject: "Re", Text: "Готово", Files: l.Files[:1]})
	if b, files := parse(bytes.NewReader(plain)); b != "Готово" || len(files) != 1 || string(files[0].Data) != "100 руб" || strings.Contains(string(plain), "alternative") {
		t.Fatalf("plain letter parses to %q %+v", b, files)
	}
}

func TestDraftCheck(t *testing.T) {
	if (Draft{}).Check() == nil || (Draft{To: "not an address"}).Check() == nil {
		t.Fatal("a draft without a proper recipient can go")
	}
	if err := (Draft{BCC: `"Анна, бух." <a@x.ru>; b@x.ru`}).Check(); err != nil {
		t.Fatalf("blind copies only: %v", err)
	}
}
