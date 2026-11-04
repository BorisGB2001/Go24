package mail

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Перевести», as Outlook's translator: a letter not in the user's language gets a banner offering its translation
// (the web package has the user's AI connection make it); the translation is kept with the letter, so it opens again
// without a new call. «Параметры → Перевод»: always translate, ask or never, into what language, which languages not
// to offer.

// Languages a letter is detected in and translated into.
var Languages = []string{"ru", "en", "de", "fr", "es", "it", "pt", "nl", "pl", "uk", "tr", "cs", "sv", "zh", "ja", "ko", "ar", "el", "he"}

// TranslateModes of «Параметры → Перевод»: translate by itself, offer it (the default), never offer.
var TranslateModes = []string{"ask", "always", "never"}

// TranslatePrefs are the user's «Перевод».
type TranslatePrefs struct {
	Mode string   // one of TranslateModes
	To   string   // the language letters are translated into ("" — the language of the interface)
	Skip []string // the languages not offered for translation
}

// TranslatePrefs of the user (the defaults when not set).
func (s *Store) TranslatePrefs(ctx context.Context, userID string) (TranslatePrefs, error) {
	p := TranslatePrefs{Mode: "ask"}
	err := s.db.QueryRow(ctx, `SELECT translate_mode, translate_to, translate_skip FROM mail_settings WHERE user_id::text = $1`, userID).
		Scan(&p.Mode, &p.To, &p.Skip)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return p, err
}

// SaveTranslatePrefs keeps the user's «Перевод».
func (s *Store) SaveTranslatePrefs(ctx context.Context, userID string, p TranslatePrefs) error {
	if !oneOf(p.Mode, TranslateModes) || (p.To != "" && !oneOf(p.To, Languages)) {
		return i18n.E("err.bad_form")
	}
	skip := []string{}
	for _, l := range p.Skip {
		if !oneOf(l, Languages) {
			return i18n.E("err.bad_form")
		}
		if !slices.Contains(skip, l) {
			skip = append(skip, l)
		}
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, translate_mode, translate_to, translate_skip) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET translate_mode = EXCLUDED.translate_mode, translate_to = EXCLUDED.translate_to,
			translate_skip = EXCLUDED.translate_skip`, userID, p.Mode, p.To, skip)
	return err
}

// NeverTranslate is the banner's «Никогда не переводить с …»: the language joins those not offered.
func (s *Store) NeverTranslate(ctx context.Context, userID, lang string) error {
	if !oneOf(lang, Languages) {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, translate_skip) VALUES ($1, ARRAY[$2])
		ON CONFLICT (user_id) DO UPDATE SET translate_skip = CASE WHEN $2 = ANY(mail_settings.translate_skip) THEN mail_settings.translate_skip
			ELSE mail_settings.translate_skip || $2 END`, userID, lang)
	return err
}

// Translation is a letter's subject and text in another language.
type Translation struct {
	Lang    string    `json:"lang"`
	From    string    `json:"from"` // the letter's language ("" — not known)
	Subject string    `json:"subject"`
	Text    string    `json:"text"`
	At      time.Time `json:"created_at"`
}

// Letter is a letter of the user as it is (not marked read): what a translation is made of.
func (s *Store) Letter(ctx context.Context, userID, id string) (Message, error) {
	return s.message(ctx, userID, id)
}

// Translated is the kept translation of the user's letter into lang (false — none yet).
func (s *Store) Translated(ctx context.Context, userID, id, lang string) (Translation, bool, error) {
	var t Translation
	err := s.db.QueryRow(ctx, `SELECT t.lang, t.from_lang, t.subject, t.body, t.created_at FROM mail_translations t
		JOIN mail_messages x ON x.id = t.message_id JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2 AND t.lang = $3`, id, userID, lang).Scan(&t.Lang, &t.From, &t.Subject, &t.Text, &t.At)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	return t, err == nil, err
}

// SaveTranslation keeps the translation of the user's letter (a new one replaces the one into the same language).
func (s *Store) SaveTranslation(ctx context.Context, userID, id string, t Translation) error {
	if !oneOf(t.Lang, Languages) {
		return i18n.E("err.bad_form")
	}
	if !oneOf(t.From, Languages) {
		t.From = ""
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO mail_translations (message_id, lang, from_lang, subject, body)
		SELECT x.id, $3, $4, $5, $6 FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE x.id::text = $1 AND m.user_id::text = $2
		ON CONFLICT (message_id, lang) DO UPDATE SET from_lang = EXCLUDED.from_lang, subject = EXCLUDED.subject, body = EXCLUDED.body,
			created_at = now()`, id, userID, t.Lang, t.From, t.Subject, t.Text)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// TranslateText is what of a letter is translated: its text without the quoted earlier letters (lines from «>»)
// — the same as what its language is told by.
func TranslateText(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t\r"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// The commonest words of the languages in Latin letters: what tells them apart.
var stopWords = map[string][]string{
	"en": {"the", "and", "you", "for", "that", "with", "this", "are", "have", "will", "your", "is", "of", "to", "we", "be", "please", "thank", "from", "not", "our", "it", "on", "in"},
	"de": {"der", "die", "das", "und", "ich", "sie", "nicht", "mit", "ist", "ein", "eine", "für", "auf", "wir", "zu", "den", "von", "bitte", "danke", "ihre", "uns", "auch", "es", "im"},
	"fr": {"le", "la", "les", "et", "est", "vous", "nous", "pour", "une", "des", "que", "dans", "pas", "avec", "sur", "merci", "votre", "je", "du", "ce", "au", "il", "qui", "bonjour"},
	"es": {"el", "los", "las", "y", "es", "que", "por", "para", "una", "con", "del", "su", "usted", "gracias", "como", "pero", "muy", "hola", "lo", "se", "al", "le", "más", "este"},
	"it": {"il", "che", "di", "della", "per", "una", "sono", "non", "con", "grazie", "gli", "anche", "ciao", "questo", "come", "nel", "alla", "lei", "ho", "ma", "si", "mi", "vi", "buongiorno"},
	"pt": {"o", "os", "as", "que", "não", "uma", "com", "para", "por", "obrigado", "você", "seu", "sua", "como", "mais", "olá", "em", "do", "da", "dos", "no", "na", "ao", "está"},
	"nl": {"de", "het", "een", "en", "ik", "je", "niet", "van", "dat", "met", "voor", "zijn", "op", "wij", "u", "uw", "bedankt", "graag", "ook", "maar", "is", "er", "dit", "naar"},
	"pl": {"i", "w", "nie", "na", "się", "jest", "że", "do", "z", "to", "dla", "jak", "pan", "pani", "dziękuję", "proszę", "czy", "są", "od", "po", "ale", "mamy", "oraz", "tak"},
	"tr": {"ve", "bir", "bu", "için", "ile", "da", "de", "çok", "teşekkür", "ederim", "sizin", "olarak", "ama", "gibi", "daha", "merhaba", "var", "yok", "ne", "mi", "değil", "her", "ben", "siz"},
	"cs": {"a", "je", "se", "na", "že", "to", "v", "jsem", "pro", "s", "ale", "jak", "děkuji", "prosím", "jsme", "není", "tak", "by", "od", "do", "také", "které", "ve", "být"},
	"sv": {"och", "att", "det", "som", "en", "på", "är", "för", "med", "jag", "inte", "har", "till", "av", "vi", "ni", "tack", "om", "den", "hej", "kan", "ett", "vad", "så"},
}

// Detect tells the language of a text by its letters and its commonest words ("" — too little text or not told).
func Detect(text string) string {
	text = TranslateText(text)
	if r := []rune(text); len(r) > 3000 {
		text = string(r[:3000])
	}
	var latin, cyr, han, kana, hangul, arabic, greek, hebrew, ukr int
	for _, c := range text {
		switch {
		case unicode.In(c, unicode.Hiragana, unicode.Katakana):
			kana++
		case unicode.Is(unicode.Han, c):
			han++
		case unicode.Is(unicode.Hangul, c):
			hangul++
		case unicode.Is(unicode.Cyrillic, c):
			cyr++
			if strings.ContainsRune("іїєґІЇЄҐ", c) {
				ukr++
			}
		case unicode.Is(unicode.Arabic, c):
			arabic++
		case unicode.Is(unicode.Greek, c):
			greek++
		case unicode.Is(unicode.Hebrew, c):
			hebrew++
		case unicode.Is(unicode.Latin, c):
			latin++
		}
	}
	cjk := han + kana + hangul
	if latin+cyr+arabic+greek+hebrew+cjk*3 < 20 {
		return ""
	}
	switch top := max(latin, cyr, arabic, greek, hebrew, cjk*3); top {
	case cjk * 3:
		switch {
		case kana > 0:
			return "ja"
		case hangul > han:
			return "ko"
		}
		return "zh"
	case cyr:
		if ukr*50 >= cyr { // і, ї, є, ґ: more than one letter in fifty
			return "uk"
		}
		return "ru"
	case arabic:
		return "ar"
	case greek:
		return "el"
	case hebrew:
		return "he"
	}
	words := strings.FieldsFunc(strings.ToLower(text), func(c rune) bool { return !unicode.IsLetter(c) })
	score := map[string]int{}
	for _, w := range words {
		for lang, list := range stopWords {
			if slices.Contains(list, w) {
				score[lang]++
			}
		}
	}
	best, n := "", 0
	for _, lang := range Languages { // in a fixed order: the same text, the same answer
		if score[lang] > n {
			best, n = lang, score[lang]
		}
	}
	if n < 2 || n*20 < len(words) { // fewer than one word in twenty of the language's commonest: not told
		return ""
	}
	return best
}
