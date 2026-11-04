package mail

import (
	"context"
	"fmt"
	"mime"
	netmail "net/mail"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestSplitName(t *testing.T) {
	for _, c := range []struct{ in, first, middle, last string }{
		{"Анна", "Анна", "", ""},
		{"Anna Smith", "Anna", "", "Smith"},
		{"Иван Петрович", "Иван", "Петрович", ""},
		{"Иванов Иван Петрович", "Иван", "Петрович", "Иванов"},
		{"Мария Ильинична", "Мария", "Ильинична", ""},
		{"John Ronald Reuel Tolkien", "John", "Ronald Reuel", "Tolkien"},
		{"  ", "", "", ""},
	} {
		if f, m, l := splitName(c.in); f != c.first || m != c.middle || l != c.last {
			t.Errorf("splitName(%q) = %q %q %q", c.in, f, m, l)
		}
	}
}

func TestMergeText(t *testing.T) {
	codes := map[string]string{"first": "first", "company": "company", "имя": "first", "компания": "company"}
	r := MergeRcpt{Email: "a@x.ru", First: "Анна", Company: `ООО «Рога & <Копыта>»`}
	if got := mergeText("Привет, {Имя}! {first} из {КОМПАНИЯ}, {нет такого} {}", r, codes, false); got !=
		"Привет, Анна! Анна из ООО «Рога & <Копыта>», {нет такого} {}" {
		t.Errorf("text = %q", got)
	}
	if got := mergeText("<p>{ Компания }</p><p>{a<b}</p>", r, codes, true); got != "<p>ООО «Рога &amp; &lt;Копыта&gt;»</p><p>{a<b}</p>" {
		t.Errorf("html = %q", got)
	}
}

func many(n int) string {
	list := make([]string, n)
	for i := range list {
		list[i] = fmt.Sprintf("a%d@x.ru", i)
	}
	return strings.Join(list, ", ")
}

func TestMergeCheck(t *testing.T) {
	if err := MergeCheck("a@x.ru, b@x.ru", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][3]string{{"", "", ""}, {"bad", "", ""}, {"a@x.ru", "c@x.ru", ""}, {"a@x.ru", "", "c@x.ru"},
		{many(MaxMerge + 1), "", ""}} {
		if err := MergeCheck(c[0], c[1], c[2]); err == nil {
			t.Errorf("accepted %q", c)
		}
	}
	if err := (Draft{Merge: true, To: many(MaxRcpts + 1)}).Check(); err != nil {
		t.Errorf("a merge to more than %d: %v", MaxRcpts, err)
	}
}

// TestMerge runs against GreenMail and the test database (as TestSendAs): a merged draft goes to each of «Кому» as
// a letter of its own with the fields of the recipient — an employee, a contact of the user, a CRM contact, a bare
// address with a name; the addresses it went to already are skipped, and each letter is kept in «Отправленные».
func TestMerge(t *testing.T) {
	url, host := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_MAIL_HOST")
	if url == "" || host == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_MAIL_HOST not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	n := time.Now().UnixNano()
	addr := fmt.Sprintf("mm%d@go24.test", n)
	emp, mine, crm, bare, skip := fmt.Sprintf("emp%d@go24.test", n), fmt.Sprintf("mine%d@client.test", n), fmt.Sprintf("crm%d@client.test", n),
		fmt.Sprintf("bare%d@client.test", n), fmt.Sprintf("skip%d@client.test", n)
	var rob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('rob', 'Роб', $1) RETURNING id`, addr).Scan(&rob))
	_, err = pool.Exec(ctx, `INSERT INTO users (oidc_sub, name, email, first_name, second_name, last_name, position)
		VALUES ('anna', 'Анна Смирнова', $1, 'Анна', 'Сергеевна', 'Смирнова', 'Бухгалтер')`, emp)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO mail_contacts (user_id, name, email, company) VALUES ($1, 'Иванов Иван Петрович', $2, 'ООО Ромашка')`, rob, mine)
	must(err)
	var co string
	must(pool.QueryRow(ctx, `INSERT INTO crm_companies (title, responsible_id, created_by) VALUES ('АО Лютик', $1, $1) RETURNING id`, rob).Scan(&co))
	_, err = pool.Exec(ctx, `INSERT INTO crm_contacts (name, email, position, company_id, responsible_id, created_by)
		VALUES ('Ольга Петрова', $1, 'Директор', $2, $3, $3)`, crm, co, rob)
	must(err)

	s := NewStore(pool, "test-secret")
	names := map[string]string{"mail.merge.f.first": "Имя", "mail.merge.f.middle": "Отчество", "mail.merge.f.last": "Фамилия",
		"mail.merge.f.company": "Компания", "mail.merge.f.position": "Должность"}
	s.Tr = func(key string, args ...any) string {
		if v, ok := names[key]; ok {
			return v
		}
		return key
	}
	box, err := s.Add(ctx, rob, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)

	to := strings.Join([]string{emp, mine, crm, `"Пётр Сидоров" <` + bare + `>`, skip}, ", ")
	if _, err := s.SaveDraft(ctx, rob, Draft{MailboxID: box, To: to, CC: emp, Merge: true}); err != nil {
		t.Fatal(err) // a draft is kept as it is
	}
	if err := (Draft{Merge: true, To: to, CC: emp}).Check(); err == nil {
		t.Fatal("a merge with «Копия» goes")
	}
	id, err := s.SaveDraft(ctx, rob, Draft{MailboxID: box, To: to, Subject: "Здравствуйте, {Имя} {Отчество}", Merge: true,
		HTML: "<p>{Фамилия}: {Компания}, {Должность}, {email}, {другое}</p>"})
	must(err)
	if d, err := s.Draft(ctx, rob, id); err != nil || !d.Merge || d.MergeDone != 0 {
		t.Fatalf("draft = %+v %v", d, err)
	}
	l, total, err := s.MergePreview(ctx, rob, to, "Здравствуйте, {Имя}", "<p>{Компания}</p>", -2)
	must(err)
	if total != 5 || l.To != `Пётр Сидоров <`+bare+`>` || l.Subject != "Здравствуйте, Пётр" || strings.Contains(l.HTML, "{") {
		t.Fatalf("preview = %d %+v", total, l)
	}
	_, err = pool.Exec(ctx, `UPDATE mail_drafts SET merge_done = ARRAY[$2] WHERE id::text = $1`, id, strings.ToUpper(skip))
	must(err)
	must(s.SendDraft(ctx, rob, id))

	dec := new(mime.WordDecoder)
	for _, c := range []struct{ who, subject, text string }{
		{emp, "Здравствуйте, Анна Сергеевна", "Смирнова: , Бухгалтер, " + emp + ", {другое}"},
		{mine, "Здравствуйте, Иван Петрович", "Иванов: ООО Ромашка, , " + mine + ", {другое}"},
		{crm, "Здравствуйте, Ольга", "Петрова: АО Лютик, Директор, " + crm + ", {другое}"},
		{bare, "Здравствуйте, Пётр", "Сидоров: , , " + bare + ", {другое}"},
	} {
		got := inbox(t, host, c.who)
		if len(got) != 1 {
			t.Fatalf("%s got %d letters", c.who, len(got))
		}
		m, err := netmail.ReadMessage(strings.NewReader(got[0]))
		must(err)
		subject, _ := dec.DecodeHeader(m.Header.Get("Subject"))
		if subject != c.subject || m.Header.Get("Cc") != "" || strings.Count(m.Header.Get("To"), "@") != 1 {
			t.Errorf("%s: subject %q, to %q", c.who, subject, m.Header.Get("To"))
		}
		if body := parseFull(strings.NewReader(got[0])).body; !strings.Contains(body, c.text) {
			t.Errorf("%s: text %q", c.who, body)
		}
	}
	if got := inbox(t, host, skip); len(got) != 0 {
		t.Fatalf("sent again to the one done: %d", len(got))
	}
	if _, err := s.Draft(ctx, rob, id); err == nil {
		t.Fatal("the draft stays after the merge")
	}
	var sent int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE f.role = 'sent'
		AND x.subject LIKE 'Здравствуйте, %'`).Scan(&sent))
	if sent != 4 {
		t.Fatalf("%d letters in «Отправленные»", sent)
	}
}
