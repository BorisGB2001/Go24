package web

import (
	"strings"
	"testing"
	"time"

	"go24/internal/mail"
)

// TestReplyAddrs: whom an answer goes to, as in Outlook — reply: the sender; reply all: the sender and the others
// in «Кому» (not the user), «Копия» stays the copy; an own sent letter: whom it went to; the subject's prefix once.
func TestReplyAddrs(t *testing.T) {
	in := mail.Message{MailboxEmail: "anna@x.ru", FolderRole: "inbox", FromName: "Иванов, Пётр", FromAddr: "p@c.ru",
		To: "anna@x.ru, bob@x.ru", CC: "cc@c.ru, ANNA@x.ru", Subject: "Заказ"}
	for _, c := range []struct{ mode, to, cc, subject string }{
		{"reply", `"Иванов, Пётр" <p@c.ru>`, "", "Re: Заказ"},
		{"all", `"Иванов, Пётр" <p@c.ru>, bob@x.ru`, "cc@c.ru", "Re: Заказ"},
		{"fwd", "", "", "Fwd: Заказ"},
	} {
		to, cc, subject := replyAddrs(in, c.mode)
		if to != c.to || cc != c.cc || subject != c.subject {
			t.Errorf("%s: %q | %q | %q", c.mode, to, cc, subject)
		}
	}
	sent := mail.Message{MailboxEmail: "anna@x.ru", FolderRole: "sent", FromAddr: "anna@x.ru", To: "p@c.ru", CC: "cc@c.ru", Subject: "RE: Заказ"}
	if to, cc, subject := replyAddrs(sent, "all"); to != "p@c.ru" || cc != "cc@c.ru" || subject != "RE: Заказ" {
		t.Errorf("reply all to an own letter: %q %q %q", to, cc, subject)
	}
}

// TestFromOf: «От» of the editor — a new letter: the mailbox's default address; a draft or an answer: its own when
// the mailbox sends from it, otherwise the mailbox's; reply all does not go to the mailbox's aliases.
func TestFromOf(t *testing.T) {
	froms := []mail.FromAddress{{MailboxID: "1", Email: "anna@x.ru"}, {MailboxID: "1", Email: "sales@x.ru", Default: true},
		{MailboxID: "2", Email: "a@y.ru", Default: true}}
	for _, c := range []struct {
		d     mail.Draft
		isNew bool
		want  string
	}{
		{mail.Draft{MailboxID: "1"}, true, "sales@x.ru"},
		{mail.Draft{MailboxID: "2"}, true, "a@y.ru"},
		{mail.Draft{MailboxID: "1", From: "SALES@x.ru"}, false, "sales@x.ru"},
		{mail.Draft{MailboxID: "1"}, false, "anna@x.ru"},
		{mail.Draft{MailboxID: "1", From: "gone@x.ru"}, false, "anna@x.ru"},
		{mail.Draft{MailboxID: "1", From: "a@y.ru"}, false, "anna@x.ru"},
	} {
		if got := fromOf(froms, c.d, c.isNew); got != c.want {
			t.Errorf("%+v %v: %q", c.d, c.isNew, got)
		}
	}
	if !hasAddr("Отдел <Sales@x.ru>, b@x.ru", "sales@x.ru") || hasAddr("b@x.ru", "sales@x.ru") {
		t.Error("hasAddr")
	}
	in := mail.Message{MailboxEmail: "anna@x.ru", FolderRole: "inbox", FromAddr: "p@c.ru", To: "sales@x.ru, bob@x.ru", Subject: "S"}
	if to, _, _ := replyAddrs(in, "all", "sales@x.ru"); to != "p@c.ru, bob@x.ru" {
		t.Errorf("reply all: %q", to)
	}
}

func TestSendTime(t *testing.T) {
	now := time.Date(2026, 10, 16, 15, 30, 0, 0, time.Local) // a Friday
	for v, want := range map[string]time.Time{
		"tomorrow_am":      time.Date(2026, 10, 17, 8, 0, 0, 0, time.Local),
		"tomorrow_pm":      time.Date(2026, 10, 17, 13, 0, 0, 0, time.Local),
		"monday":           time.Date(2026, 10, 19, 8, 0, 0, 0, time.Local),
		"2026-10-16T18:00": time.Date(2026, 10, 16, 18, 0, 0, 0, time.Local),
	} {
		if got, ok := sendTime(v, now); !ok || !got.Equal(want) {
			t.Errorf("%s = %v %v", v, got, ok)
		}
	}
	if _, ok := sendTime("2026-10-16T10:00", now); ok {
		t.Error("a time gone by")
	}
	if len(sendPresets(now)) != 3 {
		t.Error("presets")
	}
}

func TestMailDoc(t *testing.T) {
	d := mailDoc("<p>x</p>")
	if !strings.Contains(d, mailCSP) || !strings.Contains(d, `<base target="_blank">`) || !strings.Contains(d, "<body><p>x</p></body>") {
		t.Fatal(d)
	}
	if !strings.Contains(mailCSPOpen, "img-src * data:") || strings.Contains(mailCSP, "script") {
		t.Fatal("the frame's policies")
	}
}
