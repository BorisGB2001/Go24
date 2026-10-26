package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/bi"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/portal"
	"go24/internal/tasks"
)

// TestBIFilesHTTP: «Свои наборы данных» — a CSV uploaded, its types checked and changed, a widget of the set in the
// editor and on a dashboard, the file loaded again, the set in the REST and the export; others read it but neither
// change nor delete it.
func TestBIFilesHTTP(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, I18n: bundle, Portal: portal.NewStore(pool, bundle), CRM: crm.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BI = bi.NewStore(pool, srv.CRM)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, id := range []string{anna, ivan} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		as[id] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var where string
	do := func(who, method, path, ctype string, body io.Reader, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("Accept-Language", "ru")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		where = res.Header.Get("Location")
		return string(raw)
	}
	send := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		if form == nil {
			return do(who, method, path, "", nil, want)
		}
		return do(who, method, path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), want)
	}
	upload := func(who, path string, fields map[string]string, file string, want int) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range fields {
			must(mw.WriteField(k, v))
		}
		fw, err := mw.CreateFormFile("file", "Продажи по городам.csv")
		must(err)
		_, _ = fw.Write([]byte(file))
		must(mw.Close())
		do(who, "POST", path, mw.FormDataContentType(), &buf, want)
	}
	has := func(page string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if !strings.Contains(page, p) {
				t.Fatalf("no %q on the page", p)
			}
		}
	}

	has(send(anna, "GET", "/bi", nil, 200), `href="/bi/files"`)
	has(send(anna, "GET", "/bi/files", nil, 200), "Своих наборов пока нет", "Загрузить CSV")
	upload(anna, "/bi/files", nil, "", 303) // no rows
	if !strings.Contains(where, "msg=") {
		t.Fatalf("an empty file: %s", where)
	}
	upload(anna, "/bi/files", nil, "Город;Дата;Сумма;Оплачен\nТверь;05.03.2026;1 000,5;да\nМосква;20.03.2026;200;нет\nТверь;вчера;300;да\n", 303)
	m := regexp.MustCompile(`^/bi/files\?id=(\d+)$`).FindStringSubmatch(where)
	if m == nil {
		t.Fatalf("created: %s", where)
	}
	id, code := m[1], "file_"+m[1]
	page := send(anna, "GET", where, nil, 200)
	has(page, "<h1>Продажи по городам</h1>", "Строк: 3", `name="type_c3"`, `<option value="number" selected>`, `<option value="bool" selected>`,
		`<option value="string" selected>`, "1 000,5", "Да", code)
	has(send(anna, "GET", "/bi/files", nil, 200), `href="/bi/files?id=`+id+`"`, "Продажи по городам")

	// the date column is text because of «вчера»: a date it becomes, «вчера» is not read
	send(anna, "POST", "/bi/files/"+id+"/save", url.Values{"title": {"Города"}, "name_c3": {"Выручка"}, "type_c2": {"date"},
		"type_c1": {"string"}, "type_c3": {"number"}, "type_c4": {"bool"}}, 303)
	has(send(anna, "GET", "/bi/files?id="+id, nil, 200), "<h1>Города</h1>", "не распознано: 1", "2026-03-05", `value="Выручка"`)
	send(ivan, "POST", "/bi/files/"+id+"/save", url.Values{"title": {"Чужое"}}, 403)
	send(ivan, "POST", "/bi/files/"+id+"/delete", nil, 403)
	if strings.Contains(send(ivan, "GET", "/bi/files?id="+id, nil, 200), "Обновить файл") {
		t.Fatal("another may load the file")
	}

	// a widget of the set: the editor offers it with its fields, the dashboard counts over the period on its date
	send(anna, "POST", "/bi", url.Values{"title": {"Своё"}, "period": {"all"}}, 303)
	dash := strings.TrimSuffix(where, "?edit=1")
	ed := send(anna, "GET", dash+"/widget?dataset="+code+"&kind=bar&dim=c1&agg0=sum&mfield0=c3", nil, 200)
	has(ed, `<optgroup label="Свои наборы данных"><option value="`+code+`" selected>Города</option>`, `<option value="c3" selected>Выручка</option>`, "Города: Сумма: Выручка", "<rect")
	send(anna, "POST", dash+"/widget", url.Values{"dataset": {code}, "kind": {"table"}, "width": {"12"}, "dim": {"c1"}, "agg0": {"sum"}, "mfield0": {"c3"}}, 303)
	has(send(anna, "GET", dash, nil, 200), "Тверь", "1 300,5", "Москва")
	has(send(anna, "GET", dash+"?period=range&from=2026-03-01&to=2026-03-31", nil, 200), "1 000,5")
	if strings.Contains(send(anna, "GET", dash+"?period=range&from=2026-03-01&to=2026-03-31", nil, 200), "1 300,5") {
		t.Fatal("the period does not apply")
	}

	// the file loaded again: rows added, a new column
	upload(anna, "/bi/files/"+id+"/load", map[string]string{"mode": "add"}, "Город,Сумма,Менеджер\nКазань,50,Пётр\n", 303)
	has(send(anna, "GET", "/bi/files?id="+id, nil, 200), "Строк: 4", `value="Менеджер"`, "Казань")
	upload(anna, "/bi/files/"+id+"/load", map[string]string{"mode": "replace"}, "Город;Сумма\nСочи;7\n", 303)
	has(send(anna, "GET", dash, nil, 200), "Сочи")

	// the REST knows the set and its fields; the external export reads it
	f, err := srv.BI.File(ctx, mustInt(t, id))
	must(err)
	if f.Rows != 1 || len(f.Columns) != 5 {
		t.Fatalf("%+v", f)
	}
	sets, err := srv.biDatasets(ctx, func(k string) string { return srv.I18n.T("ru", k) })
	must(err)
	if last := sets[len(sets)-1]; last.Code != code || last.Name != "Города" || last.Fields[2].Name != "Выручка" || !last.Fields[2].Measure {
		t.Fatalf("%+v", last)
	}

	send(anna, "POST", "/bi/files/"+id+"/delete", nil, 303)
	if where != "/bi/files" {
		t.Fatal(where)
	}
	send(anna, "GET", "/bi/files?id="+id, nil, 404)
	has(send(anna, "GET", dash, nil, 200), "bi-err") // the widget of the deleted set
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}
