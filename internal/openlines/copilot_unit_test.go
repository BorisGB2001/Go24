package openlines

import (
	"testing"
)

func TestConversation(t *testing.T) {
	t.Run("empty messages", func(t *testing.T) {
		sess := Session{ID: "sess-123456", Name: "Иван"}
		if got := Conversation(sess, nil); got != "" {
			t.Errorf("Conversation(nil) = %q; want empty", got)
		}
		if got := Conversation(sess, []Message{}); got != "" {
			t.Errorf("Conversation([]) = %q; want empty", got)
		}
	})

	t.Run("visitor name vs visitor id prefix", func(t *testing.T) {
		withName := Session{ID: "abcdef123456", Name: "Иван"}
		msgs := []Message{{Body: "Здравствуйте"}}
		got := Conversation(withName, msgs)
		want := "Иван: Здравствуйте\n"
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}

		withoutName := Session{ID: "abcdef123456", Name: ""}
		gotNoName := Conversation(withoutName, msgs)
		wantNoName := "#abcdef: Здравствуйте\n"
		if gotNoName != wantNoName {
			t.Errorf("got %q; want %q", gotNoName, wantNoName)
		}
	})

	t.Run("operator messages use operator name", func(t *testing.T) {
		sess := Session{ID: "sess-1", Name: "Клиент"}
		msgs := []Message{
			{Body: "Вопрос", Operator: ""},
			{Body: "Ответ", Operator: "Елена"},
		}
		got := Conversation(sess, msgs)
		want := "Клиент: Вопрос\nЕлена: Ответ\n"
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}
	})

	t.Run("auto messages use widget name", func(t *testing.T) {
		sess := Session{ID: "sess-1", Name: "Клиент", Widget: "Онлайн-консультант"}
		msgs := []Message{
			{Body: "Добро пожаловать!", Kind: "welcome"},
			{Body: "Как дела?", Operator: ""},
			{Body: "Все операторы заняты", Kind: "noanswer"},
			{Body: "Спасибо за обращение!", Kind: "close"},
		}
		got := Conversation(sess, msgs)
		want := "Онлайн-консультант: Добро пожаловать!\nКлиент: Как дела?\nОнлайн-консультант: Все операторы заняты\nОнлайн-консультант: Спасибо за обращение!\n"
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}
	})

	t.Run("internal messages are omitted", func(t *testing.T) {
		sess := Session{ID: "sess-1", Name: "Клиент"}
		msgs := []Message{
			{Body: "Начало диалога"},
			{Body: "Диалог перехвачен супервайзером", Kind: "note"},
			{Body: "Это VIP клиент, дать скидку 10%", Kind: "hidden", Operator: "Босс"},
			{Body: "Принято!", Operator: "Оператор"},
		}
		got := Conversation(sess, msgs)
		want := "Клиент: Начало диалога\nОператор: Принято!\n"
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}
	})

	t.Run("messages with file attachments", func(t *testing.T) {
		sess := Session{ID: "sess-1", Name: "Клиент"}
		msgs := []Message{
			// file only
			{FileName: "price.pdf", Operator: "Менеджер"},
			// text and file
			{Body: "Вот фото дефекта", FileName: "screen.png"},
		}
		got := Conversation(sess, msgs)
		want := "Менеджер: 📎 price.pdf\nКлиент: Вот фото дефекта\n📎 screen.png\n"
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}
	})
}
