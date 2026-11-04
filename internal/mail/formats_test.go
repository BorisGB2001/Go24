package mail

import "testing"

// TestLookOf: the first rule on that takes a letter decides its look; «Не прочитано» and the rules' conditions.
func TestLookOf(t *testing.T) {
	boss := Format{Name: "Начальник", Enabled: true, If: Cond{From: "boss@go24.test"}, Look: Look{Color: "red", Bold: true}}
	unread := Format{Name: "Новые", Enabled: true, Unread: true, Look: Look{Color: "blue"}}
	off := Format{Name: "Выкл", Enabled: false, If: Cond{Subject: "счёт"}, Look: Look{Color: "green"}}
	high := Format{Name: "Важные мне", Enabled: true, If: Cond{Importance: "high", ToMe: true}, Look: Look{Color: "orange", Italic: true}}
	list := []Format{off, boss, unread, high}
	me := "anna@go24.test"
	for _, c := range []struct {
		name string
		m    Message
		want string
	}{
		{"boss, read", Message{FromAddr: "Boss@go24.test", Seen: true, MailboxEmail: me}, "red"},
		{"boss before unread", Message{FromAddr: "boss@go24.test", Unread: 1, MailboxEmail: me}, "red"},
		{"unread", Message{FromAddr: "x@y.test", Unread: 1, MailboxEmail: me}, "blue"},
		{"a conversation with an unread letter", Message{FromAddr: "x@y.test", Seen: true, Unread: 2, MailboxEmail: me}, "blue"},
		{"off rule skipped", Message{Subject: "Счёт №5", Seen: true, MailboxEmail: me}, ""},
		{"high to me", Message{Importance: 1, To: "Анна <anna@go24.test>", Seen: true, MailboxEmail: me}, "orange"},
		{"high to another", Message{Importance: 1, To: "eve@go24.test", Seen: true, MailboxEmail: me}, ""},
	} {
		got := ""
		if l := LookOf(list, c.m); l != nil {
			got = l.Color
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if l := LookOf(list, Message{FromAddr: "boss@go24.test", Seen: true}); !l.Bold || l.Italic {
		t.Errorf("font: %+v", l)
	}
	for _, f := range []Format{{Name: "", Look: Look{Color: "red"}, Unread: true}, {Name: "x", Look: Look{Color: "red"}},
		{Name: "x", Unread: true, Look: Look{Color: "pink"}}, {Name: "x", If: Cond{Importance: "urgent"}, Look: Look{Color: "red"}}} {
		if checkFormat(&f) == nil {
			t.Errorf("%+v passed", f)
		}
	}
	if f := (Format{Name: " x ", Unread: true, Look: Look{Color: "teal"}}); checkFormat(&f) != nil || f.Name != "x" {
		t.Errorf("good one: %+v", f)
	}
}
