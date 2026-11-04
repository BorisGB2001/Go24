package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailContactsCardDAV: «Личные контакты» on a phone (GO24DEV-991). A key that may «mail:read» sees the book
// «Личные контакты go24» beside the employees (only those a key may read are listed) with the contacts made in go24;
// a key that may «mail:write» creates, changes and deletes cards (If-Match, If-None-Match), a card is given back as
// the phone sent it, a change in go24 keeps its other fields; a read-only key changes nothing, nobody reaches
// another's contacts. A contact without an address is kept but no recipient. «Импорт» of .vcf and .csv, «Экспорт».
func TestMailContactsCardDAV(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна Иванова', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle,
		Mail: mail.NewStore(pool, "x"), Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	key := func(who string, scopes ...string) string {
		k, err := srv.Keys.Create(ctx, who, "carddav", scopes, 0)
		must(err)
		return k
	}
	writeKey, readKey, staffKey, eveKey := key(anna, "mail:read", "mail:write"), key(anna, "mail:read"), key(anna, "company:read"), key(eve, "mail:read", "mail:write")
	made, err := srv.Mail.SaveContact(ctx, anna, mail.Contact{Name: "Пётр Сидоров", Email: "sid@ip.test", Company: "ИП Сидоров"})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	do := func(token, method, p string, hdr map[string]string, body string, want int) (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+p, strings.NewReader(body))
		if token == "cookie" {
			req.AddCookie(cookie)
			req.Header.Set("X-Go24-Form", "1")
			req.Header.Set("Accept", "application/json")
		} else {
			req.SetBasicAuth("phone", token)
		}
		req.Header.Set("Accept-Language", "ru")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, p, res.StatusCode, want, raw)
		}
		return string(raw), res.Header
	}
	has := func(what, text string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("%s has no %q: %.2500s", what, w, text)
			}
		}
	}
	hasNot := func(what, text string, not ...string) {
		t.Helper()
		for _, w := range not {
			if strings.Contains(text, w) {
				t.Errorf("%s has %q: %.2500s", what, w, text)
			}
		}
	}
	depth1 := map[string]string{"Depth": "1"}
	propfind := `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/">
		<d:prop><d:resourcetype/><d:displayname/><d:getetag/><cs:getctag/><d:current-user-privilege-set/></d:prop></d:propfind>`

	// the books a key may read
	top, _ := do(writeKey, "PROPFIND", "/carddav/", depth1, propfind, 207)
	has("top", top, "<d:href>/carddav/contacts/</d:href>", "Личные контакты go24", "<d:write/>")
	hasNot("top", top, "/carddav/employees/")
	top, _ = do(staffKey, "PROPFIND", "/carddav/", depth1, propfind, 207)
	has("staff top", top, "/carddav/employees/")
	hasNot("staff top", top, "/carddav/contacts/")
	do(staffKey, "PROPFIND", "/carddav/contacts/", depth1, propfind, 404)
	do(readKey, "PROPFIND", "/carddav/employees/", depth1, propfind, 404)

	// the contact made in go24 is there, as a card of its own
	book, _ := do(readKey, "PROPFIND", "/carddav/contacts/", depth1, propfind, 207)
	has("book", book, "/carddav/contacts/"+made.ID+".vcf", "<card:addressbook/>")
	hasNot("read-only book", book, "<d:write/>")
	ctag := book[strings.Index(book, "<cs:getctag>"):]
	ctag = ctag[:strings.Index(ctag, "</cs:getctag>")]
	card, h := do(readKey, "GET", "/carddav/contacts/"+made.ID+".vcf", nil, "", 200)
	has("made card", card, "FN:Пётр Сидоров", "EMAIL;TYPE=INTERNET:sid@ip.test", "ORG:ИП Сидоров", "UID:go24-contact-"+made.ID)
	madeTag := h.Get("ETag")

	// the phone adds a contact: no address, a photo and two phones
	phone := "BEGIN:VCARD\r\nVERSION:3.0\r\nN:Кузнецов;Олег;;;\r\nFN:Олег Кузнецов\r\nTEL;type=CELL:+7 900 555\r\n" +
		"TEL;type=HOME:+7 495 1\r\nPHOTO;ENCODING=b;TYPE=JPEG:QUFB\r\nUID:PHONE-1\r\nEND:VCARD\r\n"
	do(readKey, "PUT", "/carddav/contacts/PHONE-1.vcf", nil, phone, 403)
	do(eveKey, "GET", "/carddav/contacts/"+made.ID+".vcf", nil, "", 404)
	_, h = do(writeKey, "PUT", "/carddav/contacts/PHONE-1.vcf", map[string]string{"If-None-Match": "*"}, phone, 201)
	tag := h.Get("ETag")
	do(writeKey, "PUT", "/carddav/contacts/PHONE-1.vcf", map[string]string{"If-None-Match": "*"}, phone, 412)
	got, h := do(writeKey, "GET", "/carddav/contacts/PHONE-1.vcf", nil, "", 200)
	if got != phone || h.Get("ETag") != tag {
		t.Errorf("the card came back other: %q %s/%s", got, h.Get("ETag"), tag)
	}
	book, _ = do(readKey, "PROPFIND", "/carddav/contacts/", depth1, propfind, 207)
	if strings.Contains(book, ctag+"<") {
		t.Errorf("the book's version did not change: %s", book)
	}

	// changed on the phone: If-Match of another version fails
	phone2 := strings.Replace(phone, "+7 900 555", "+7 900 556", 1)
	do(writeKey, "PUT", "/carddav/contacts/PHONE-1.vcf", map[string]string{"If-Match": madeTag}, phone2, 412)
	do(writeKey, "PUT", "/carddav/contacts/PHONE-1.vcf", map[string]string{"If-Match": tag}, phone2, 204)
	do(writeKey, "PUT", "/carddav/contacts/bad.vcf", nil, "not a card", 400)

	// in go24: offered on its tab, never as a recipient; changed — its photo and other phone stay
	tab, _ := do("cookie", "GET", "/mail/book?src=contact", nil, "", 200)
	has("tab", tab, "Олег Кузнецов", "+7 900 556")
	all, _ := do("cookie", "GET", "/mail/book?q=Кузнецов", nil, "", 200)
	hasNot("recipients", all, "Олег")
	var list []map[string]any
	must(json.Unmarshal([]byte(tab), &list))
	oleg := ""
	for _, c := range list {
		if c["name"] == "Олег Кузнецов" {
			oleg, _ = c["id"].(string)
		}
	}
	req := url.Values{"name": {"Олег Кузнецов"}, "email": {"oleg@x.test"}, "phone": {"+7 900 777"}}.Encode()
	do("cookie", "POST", "/mail/contacts/"+oleg+"/save", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, req, 200)
	got, _ = do(writeKey, "GET", "/carddav/contacts/PHONE-1.vcf", nil, "", 200)
	has("patched", got, "EMAIL;TYPE=INTERNET:oleg@x.test", "TEL;type=CELL:+7 900 777", "TEL;type=HOME:+7 495 1", "PHOTO;ENCODING=b;TYPE=JPEG:QUFB", "UID:PHONE-1")

	// the phone changes the go24 contact (its href is now the phone's), then deletes it
	edited := strings.Replace(card, "ИП Сидоров", "ООО Сидоров", 1)
	do(writeKey, "PUT", "/carddav/contacts/"+made.ID+".vcf", map[string]string{"If-Match": madeTag}, edited, 204)
	if c, err := srv.Mail.Contacts(ctx, anna, "sid@ip.test", 5, true); err != nil || len(c) != 1 || c[0].Company != "ООО Сидоров" {
		t.Errorf("the phone's change = %+v, %v", c, err)
	}
	do(eveKey, "DELETE", "/carddav/contacts/"+made.ID+".vcf", nil, "", 404)
	do(writeKey, "DELETE", "/carddav/contacts/"+made.ID+".vcf", map[string]string{"If-Match": `"nope"`}, "", 412)
	do(writeKey, "DELETE", "/carddav/contacts/"+made.ID+".vcf", nil, "", 204)
	do(writeKey, "GET", "/carddav/contacts/"+made.ID+".vcf", nil, "", 404)

	// «Импорт»: a .vcf (one repeats an address there — it is changed) and an Outlook CSV; «Экспорт»
	upload := func(name, data string, want int) map[string]any {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write([]byte(data))
		_ = mw.Close()
		raw, _ := do("cookie", "POST", "/mail/contacts/import", map[string]string{"Content-Type": mw.FormDataContentType()}, b.String(), want)
		out := map[string]any{}
		_ = json.Unmarshal([]byte(raw), &out)
		return out
	}
	vcf := "BEGIN:VCARD\nVERSION:3.0\nFN:Олег К.\nEMAIL:oleg@x.test\nEND:VCARD\nBEGIN:VCARD\nVERSION:3.0\nFN:Мария\nEMAIL:maria@x.test\nORG:Ромашка\nEND:VCARD\n" +
		"BEGIN:VCARD\nVERSION:3.0\nEND:VCARD\n"
	if res := upload("contacts.vcf", vcf, 200); res["added"] != 1.0 || res["updated"] != 1.0 || res["skipped"] != 1.0 {
		t.Errorf("vcf import = %v", res)
	}
	csv := "First Name,Last Name,E-mail Address,Mobile Phone\nИрина,Белова,irina@x.test,+7 1\n"
	if res := upload("outlook.csv", csv, 200); res["added"] != 1.0 {
		t.Errorf("csv import = %v", res)
	}
	upload("photo.jpg", "\xff\xd8\xff", 422)
	out, h := do("cookie", "GET", "/mail/contacts/export", nil, "", 200)
	if !strings.HasPrefix(h.Get("Content-Type"), "text/vcard") || strings.Count(out, "BEGIN:VCARD") != 3 {
		t.Errorf("export: %s %s", h.Get("Content-Type"), out)
	}
	has("export", out, "FN:Олег К.", "FN:Мария", "FN:Ирина Белова")
	sync, _ := do("cookie", "GET", "/mail/contacts/phone", nil, "", 200)
	has("phone", sync, "/carddav/")
}
