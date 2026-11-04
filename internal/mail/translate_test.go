package mail

import "testing"

// TestDetect: a letter's language is told by its letters (Cyrillic — Russian or Ukrainian, Japanese kana, Chinese,
// Korean, Arabic, Greek) or, in Latin letters, by its commonest words; the quoted earlier letters do not count; too
// little text or words of no language known — not told.
func TestDetect(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"Hello Anna,\n\nWe would like to order ten boxes. Please send them to our office by Friday.\n\nThank you,\nJohn", "en"},
		{"Sehr geehrte Frau Petrova,\n\nich möchte Sie bitten, die Rechnung für den Auftrag noch einmal zu prüfen. Vielen Dank und mit freundlichen Grüßen", "de"},
		{"Bonjour,\n\nNous avons bien reçu votre commande et nous vous remercions pour votre confiance. Le colis est envoyé avec le transporteur.", "fr"},
		{"Hola, gracias por su mensaje. El pedido está listo y lo enviamos para usted con el transportista que nos indicó por teléfono.", "es"},
		{"Ciao, grazie per la tua email. Il pacco è pronto e lo spediamo domani, ma non con il corriere della settimana scorsa.", "it"},
		{"Goedemorgen, bedankt voor uw bericht. Wij sturen het pakket morgen naar het adres van uw kantoor, maar het is niet zeker.", "nl"},
		{"Dzień dobry, dziękuję za wiadomość. Zamówienie jest gotowe i wyślemy je jutro do pani biura, ale nie wiem, czy to pasuje.", "pl"},
		{"Здравствуйте! Спасибо за письмо, заказ готов, отправим его завтра курьером на адрес вашего офиса.", "ru"},
		{"Доброго дня! Дякуємо за лист, замовлення готове, ми відправимо його завтра кур'єром на адресу вашого офісу.", "uk"},
		{"お世話になっております。ご注文の商品は明日発送いたします。よろしくお願いいたします。", "ja"},
		{"您好，感谢您的来信。您的订单已经准备好了，我们明天发货。", "zh"},
		{"안녕하세요. 주문하신 상품은 내일 발송될 예정입니다. 감사합니다.", "ko"},
		{"مرحبا، شكرا على رسالتك. الطلب جاهز وسنرسله غدا إلى عنوان مكتبكم.", "ar"},
		{"Γεια σας, ευχαριστούμε για το μήνυμά σας. Η παραγγελία είναι έτοιμη και θα σταλεί αύριο.", "el"},
		{"Хорошо, спасибо, привезём в пятницу.\n> Hello Anna, we would like to order ten boxes. Please send them to our office by Friday. Thank you for the order.", "ru"},
		{"Thanks!", ""},
		{"Xqz vrbt plmk wzzt qrpl mnbv trew lkjh gfds poiu ytre wqas", ""},
	} {
		if got := Detect(c.text); got != c.want {
			t.Errorf("Detect(%.40q) = %q, want %q", c.text, got, c.want)
		}
	}
}

// TestTranslateText: what is translated of a letter leaves out its quoted earlier letters.
func TestTranslateText(t *testing.T) {
	got := TranslateText("Thank you!  \r\n\nSee below.\n> old line\n  >> older\nJohn\n")
	if got != "Thank you!\n\nSee below.\nJohn" {
		t.Fatalf("%q", got)
	}
}
