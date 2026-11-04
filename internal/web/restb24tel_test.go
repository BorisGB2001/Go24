package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestRestPortalTel: telephony.externalcall.* — an outside PBX registers an incoming call of an unknown number (a lead is
// made, the employee is shown the call), attaches its record by a link and finishes it (the call goes into the log
// with its cost, into the lead's timeline, the notification is hidden); an outgoing call to a contact finds the
// contact, CRM_CREATE=0 and a line without «create in CRM» make no lead; show and hide; a record attached before the
// finish; another's call is the administrators'. telephony.externalLine.* — the administrators' lines;
// searchCrmEntities; voximplant.statistic.get by «Права доступа» with the period and the fields.
func TestRestPortalTel(t *testing.T) {
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
	user := func(name string, admin bool) (string, int64) {
		var id string
		var n int64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, admin) VALUES ($1, $1, $2) RETURNING id, num`, name, admin).Scan(&id, &n))
		return id, n
	}
	anna, annaNum := user("Анна", true)
	boris, borisNum := user("Борис", false)
	eve, eveNum := user("Ева", false)
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, anna, "101"))
	must(store.SetNumber(ctx, boris, "102"))
	must(store.SetEnabled(ctx, anna, true))
	bundle, err := i18n.Load("")
	must(err)
	records := t.TempDir()
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, CRM: crm.NewStore(pool),
		Telephony: &telephony.PBX{Store: store, Records: records, Key: "k", Dir: t.TempDir()}, BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	// the PBX's records: a test server is not of the internet
	pbx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ID3-record") }))
	defer pbx.Close()
	saved := recordClient
	recordClient = pbx.Client()
	defer func() { recordClient = saved }()

	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hook := func(uid string, n int64) string {
		token, err := srv.Keys.Create(ctx, uid, "pbx", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	annaHook, borisHook, eveHook := hook(anna, annaNum), hook(boris, borisNum), hook(eve, eveNum)
	call := func(h, method, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+h+method+".json", "application/json", strings.NewReader(body))
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	res := func(h, method, body string) map[string]any {
		t.Helper()
		return call(h, method, body, 200)["result"].(map[string]any)
	}
	str := func(v any) string { return portalStr(v) }
	bell := func(uid string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'phone'`, uid).Scan(&n))
		return n
	}

	// the lines: the administrators'
	call(borisHook, "telephony.externalLine.add", `{"NUMBER": "+74951112233", "NAME": "Офис"}`, 403)
	if got := res(annaHook, "telephony.externalLine.add", `{"NUMBER": "+74951112233", "NAME": "Офис"}`); got["ID"] != "+74951112233" {
		t.Errorf("externalLine.add: %v", got)
	}
	call(annaHook, "telephony.externalLine.add", `{"NUMBER": "+74950000099", "NAME": "Склад", "CRM_AUTO_CREATE": "N"}`, 200)
	call(annaHook, "telephony.externalLine.update", `{"NUMBER": "+74951112233", "NAME": "Главный офис"}`, 200)
	call(annaHook, "telephony.externalLine.update", `{"NUMBER": "+70000000000", "NAME": "Нет"}`, 400)
	lines := call(borisHook, "telephony.externalLine.get", `{}`, 200)["result"].([]any)
	if len(lines) != 2 || lines[1].(map[string]any)["NAME"] != "Главный офис" || lines[1].(map[string]any)["CRM_AUTO_CREATE"] != "Y" ||
		lines[0].(map[string]any)["CRM_AUTO_CREATE"] != "N" {
		t.Errorf("externalLine.get: %v", lines)
	}

	// an incoming call of an unknown number: a lead, Boris is shown it
	reg := res(borisHook, "telephony.externalcall.register", `{"USER_PHONE_INNER": "102", "PHONE_NUMBER": "+7 900 111-22-33",
		"TYPE": 2, "LINE_NUMBER": "+74951112233", "CALL_START_DATE": "`+time.Now().Add(-time.Minute).Format(time.RFC3339)+`"}`)
	callID := str(reg["CALL_ID"])
	if !strings.HasPrefix(callID, "externalCall.") || reg["CRM_ENTITY_TYPE"] != "LEAD" || reg["CRM_CREATED_LEAD"] == nil ||
		reg["CRM_ENTITY_ID"] != reg["CRM_CREATED_LEAD"] || len(reg["CRM_CREATED_ENTITIES"].([]any)) != 1 {
		t.Fatalf("register: %v", reg)
	}
	if bell(boris) != 1 {
		t.Errorf("Boris is not shown the call")
	}
	call(eveHook, "telephony.externalcall.finish", `{"CALL_ID": "`+callID+`"}`, 403)
	call(borisHook, "telephony.externalcall.register", `{"USER_ID": `+num(annaNum)+`, "PHONE_NUMBER": "89000000000"}`, 403)

	fin := res(borisHook, "telephony.externalcall.finish", `{"CALL_ID": "`+callID+`", "DURATION": 42, "STATUS_CODE": "200",
		"COST": "1.50", "COST_CURRENCY": "RUB", "VOTE": 5, "RECORD_URL": "`+pbx.URL+`/rec/42.mp3"}`)
	if fin["CALL_ID"] != callID || fin["CALL_DURATION"].(float64) != 42 || fin["CALL_TYPE"].(float64) != 2 || fin["COST"].(float64) != 1.5 ||
		fin["COST_CURRENCY"] != "RUB" || fin["CRM_ENTITY_TYPE"] != "LEAD" || fin["CRM_ENTITY_ID"] != reg["CRM_ENTITY_ID"] ||
		str(fin["PORTAL_USER_ID"]) != num(borisNum) || fin["CALL_VOTE"].(float64) != 5 || fin["PORTAL_NUMBER"] != "+74951112233" ||
		fin["CALL_FAILED_CODE"] != "200" || !strings.HasSuffix(str(fin["CALL_RECORD_URL"]), "/record") {
		t.Errorf("finish: %v", fin)
	}
	var rec, lead, status string
	must(pool.QueryRow(ctx, `SELECT record, crm_id::text, status FROM telephony_calls WHERE uniqueid = $1`, callID).Scan(&rec, &lead, &status))
	if data, err := os.ReadFile(filepath.Join(records, rec)); err != nil || string(data) != "ID3-record" || !strings.HasSuffix(rec, ".mp3") ||
		status != "answered" {
		t.Errorf("the record: %q %v %s", rec, err, status)
	}
	var events int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity = 'lead' AND entity_id = $1 AND kind = 'call'`, lead).Scan(&events))
	if events != 1 {
		t.Errorf("the lead's timeline: %d calls", events)
	}
	if bell(boris) != 0 {
		t.Errorf("the call is still shown")
	}
	call(borisHook, "telephony.externalcall.finish", `{"CALL_ID": "`+callID+`"}`, 400) // finished already

	// an outgoing call to a contact; no lead when not asked or the line does not make them
	contact := str(call(annaHook, "crm.contact.add", `{"fields": {"NAME": "Пётр", "ASSIGNED_BY_ID": `+num(annaNum)+`,
		"PHONE": [{"VALUE": "+79005556677", "VALUE_TYPE": "WORK"}]}}`, 200)["result"])
	out := res(annaHook, "telephony.externalcall.register", `{"USER_ID": `+num(borisNum)+`, "PHONE_NUMBER": "89005556677", "TYPE": 1, "SHOW": 0}`)
	if out["CRM_ENTITY_TYPE"] != "CONTACT" || str(out["CRM_ENTITY_ID"]) != contact || out["CRM_CREATED_LEAD"] != nil {
		t.Errorf("an outgoing call to a contact: %v", out)
	}
	if bell(boris) != 0 {
		t.Errorf("SHOW=0 showed the call")
	}
	if got := res(borisHook, "telephony.externalcall.register", `{"USER_ID": `+num(borisNum)+`, "PHONE_NUMBER": "89007770000", "TYPE": 2, "CRM_CREATE": 0, "SHOW": 0}`); got["CRM_ENTITY_TYPE"] != "" {
		t.Errorf("CRM_CREATE=0: %v", got)
	}
	if got := res(borisHook, "telephony.externalcall.register", `{"USER_ID": `+num(borisNum)+`, "PHONE_NUMBER": "89007770001", "TYPE": 2, "LINE_NUMBER": "+74950000099", "SHOW": 0}`); got["CRM_CREATED_LEAD"] != nil {
		t.Errorf("a line without «create in CRM»: %v", got)
	}

	// show and hide; a record before the finish; a missed call
	outID := str(out["CALL_ID"])
	call(borisHook, "telephony.externalcall.show", `{"CALL_ID": "`+outID+`", "USER_ID": [`+num(borisNum)+`]}`, 200)
	call(borisHook, "telephony.externalcall.show", `{"CALL_ID": "`+outID+`"}`, 200)
	if bell(boris) != 1 {
		t.Errorf("show: %d", bell(boris))
	}
	call(borisHook, "telephony.externalcall.show", `{"CALL_ID": "`+outID+`", "USER_ID": `+num(annaNum)+`}`, 403)
	call(borisHook, "telephony.externalcall.hide", `{"CALL_ID": "`+outID+`"}`, 200)
	if bell(boris) != 0 {
		t.Errorf("hide: %d", bell(boris))
	}
	call(borisHook, "telephony.externalcall.attachRecord", `{"CALL_ID": "`+outID+`", "FILENAME": "a.wav", "FILE_CONTENT": "`+
		base64.StdEncoding.EncodeToString([]byte("RIFF-out"))+`"}`, 200)
	miss := res(borisHook, "telephony.externalcall.finish", `{"CALL_ID": "`+outID+`", "DURATION": 10, "STATUS_CODE": "486", "FAILED_REASON": "Busy"}`)
	if miss["CALL_FAILED_CODE"] != "486" || miss["CALL_FAILED_REASON"] != "Busy" || miss["CALL_DURATION"].(float64) != 0 || miss["CALL_TYPE"].(float64) != 1 ||
		miss["CRM_ENTITY_TYPE"] != "CONTACT" || miss["CALL_RECORD_URL"] == "" {
		t.Errorf("a busy call with a record: %v", miss)
	}
	// a record attached after the finish replaces the one there
	att := res(borisHook, "telephony.externalcall.attachRecord", `{"CALL_ID": "`+callID+`", "FILENAME": "b.ogg", "FILE_CONTENT": "`+
		base64.StdEncoding.EncodeToString([]byte("OggS"))+`"}`)
	if !strings.HasSuffix(str(att["FILE_NAME"]), ".ogg") || att["CALL_RECORD_URL"] == nil {
		t.Errorf("attachRecord after the finish: %v", att)
	}
	if _, err := os.Stat(filepath.Join(records, rec)); !os.IsNotExist(err) {
		t.Errorf("the record replaced is kept: %v", err)
	}
	call(eveHook, "telephony.externalcall.attachRecord", `{"CALL_ID": "`+callID+`", "FILE_CONTENT": "eA=="}`, 403)

	// searchCrmEntities
	found := call(eveHook, "telephony.externalCall.searchCrmEntities", `{"PHONE_NUMBER": "+7 (900) 555-66-77"}`, 200)["result"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["CRM_ENTITY_TYPE"] != "CONTACT" || str(found[0].(map[string]any)["CRM_ENTITY_ID"]) != contact ||
		str(found[0].(map[string]any)["ASSIGNED_BY_ID"]) != num(annaNum) || found[0].(map[string]any)["ASSIGNED_BY"].(map[string]any)["USER_PHONE_INNER"] != "101" {
		t.Errorf("searchCrmEntities: %v", found)
	}

	// voximplant.statistic.get: Boris's calls, Eve has none
	stat := call(borisHook, "voximplant.statistic.get", `{"order": {"CALL_START_DATE": "ASC"}}`, 200)
	if stat["total"].(float64) != 2 || stat["result"].([]any)[0].(map[string]any)["CALL_ID"] != callID {
		t.Errorf("statistic.get: %v", stat)
	}
	if got := call(borisHook, "voximplant.statistic.get", `{"filter": {"CALL_TYPE": 1}, "select": ["CALL_ID", "COST"]}`, 200)["result"].([]any); len(got) != 1 ||
		got[0].(map[string]any)["CALL_ID"] != outID || len(got[0].(map[string]any)) != 3 {
		t.Errorf("statistic.get by CALL_TYPE: %v", got)
	}
	tomorrow := time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	if got := call(borisHook, "voximplant.statistic.get", `{"filter": {">=CALL_START_DATE": "`+tomorrow+`T00:00:00"}}`, 200)["result"].([]any); len(got) != 0 {
		t.Errorf("statistic.get from tomorrow: %v", got)
	}
	if got := call(borisHook, "voximplant.statistic.get", `{"filter": {"<=CALL_START_DATE": "`+tomorrow+`"}}`, 200)["result"].([]any); len(got) != 2 {
		t.Errorf("statistic.get up to tomorrow: %v", got)
	}
	if got := call(eveHook, "voximplant.statistic.get", `{}`, 200)["result"].([]any); len(got) != 0 {
		t.Errorf("Eve's statistic: %v", got)
	}

	call(borisHook, "telephony.externalLine.delete", `{"NUMBER": "+74950000099"}`, 403)
	call(annaHook, "telephony.externalLine.delete", `{"NUMBER": "+74950000099"}`, 200)
	call(annaHook, "telephony.externalLine.delete", `{"NUMBER": "+74950000099"}`, 400)
}
