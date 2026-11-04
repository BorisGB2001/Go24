package web

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go24/internal/auth"
	"go24/internal/bp"
)

// TestStepValues: «Изменить» — the designer's form filled with a step's values (bpStepValues) reads back as the same
// step (bpStepFromForm), of every kind the form keeps without asking the services.
func TestStepValues(t *testing.T) {
	steps := []bp.Step{
		{Kind: "notify", Text: "Hello {name}", To: "starter", Days: 0},
		{Kind: "approve", Text: "Approve?", To: "manager", Users: []string{"u1", "u2"}, Mode: "vote", Percent: 60, WaitAll: true, Goto: 7,
			Delegate: "all", Timeout: 2 * 24 * 60, Remark: "reject", Yes: "OK", No: "Nope"},
		{Kind: "review", Text: "Read", To: "starter", Timeout: 90, Remark: "yes", Yes: "Read it"},
		{Kind: "request", Text: "More", To: "responsible", Reject: true, Goto: 3, Timeout: 120},
		{Kind: "commands", Text: "Choose", To: "starter", Users: []string{"u1"}, Commands: []bp.Command{{Title: "Back", Goto: 2}, {Title: "Next"}},
			Timeout: 60, Goto: 5},
		{Kind: "wait", Timeout: 3 * 24 * 60},
		{Kind: "wait", Timeout: 90, WorkTime: true},
		{Kind: "wait", Field: "due", WorkTime: true},
		{Kind: "if", Field: "sum", Op: "gt", Value: "100", Goto: 4},
		{Kind: "if", Field: "sum", Op: "gt", Value: "100", Goto: 4, More: []bp.Cond{{Field: "city", Op: "eq", Value: "Moscow"}, {Or: true, Field: "vip", Op: "filled"}}},
		{Kind: "loop", Field: "n", Op: "lt", Value: "= {n} + 1", Goto: 2, More: []bp.Cond{{Field: "done", Op: "empty"}}},
		{Kind: "set", Field: "n", Value: "= {n} + 1"},
		{Kind: "goto", Goto: 9},
		{Kind: "parallel", Goto: 8, Branches: []int{4, 6}},
		{Kind: "pick", Users: []string{"u1", "u2"}, Mode: "random", Field: "who", Skip: true, To: "u3"},
		{Kind: "chat", Text: "Hi", To: "starter", From: "u1", Users: []string{"u2"}},
		{Kind: "post", Text: "News", From: "manager", Users: []string{"u1"}, Subject: "Title"},
		{Kind: "mail", Text: "Body", From: "starter", To: "client", Email: "a@example.com, {email}", Subject: "Subj"},
		{Kind: "comment", Text: "Note", From: "u1"},
		{Kind: "log", Text: "Logged {n}"},
		{Kind: "activity", Text: "Call", To: "responsible", Days: 2, Deed: "call", Note: "about it"},
		{Kind: "update", Sets: []bp.Assign{{Field: "amount", Value: "{sum}"}, {Field: "responsible", Value: "manager"}}},
		{Kind: "create", Text: "New", To: "starter", Entity: "lead", Sets: []bp.Assign{{Field: "title", Value: "{title}"}}},
		{Kind: "crmread", Entity: "deal", Value: "{created_card}"},
		{Kind: "crmconvert", Value: "12", Into: []string{"contact", "deal"}},
		{Kind: "crmobservers", Entity: "lead", Value: "{created_card}", Mode: "remove", Users: []string{"starter", "u1"}},
		{Kind: "crmclose", Entity: "deal", Deed: "meeting"},
		{Kind: "crmresponsible", Entity: "lead", Value: "{created_card}", Users: []string{"u1", "starter"}, Mode: "queue", Skip: true, To: "u2"},
		{Kind: "crmresponsible", Users: []string{"u1"}, Mode: "random"},
		{Kind: "create", Entity: "deal", Type: "t1", Stage: "s2", Text: "Contract", To: "starter", Sets: []bp.Assign{{Field: "amount", Value: "5"}}},
		{Kind: "create", Entity: "deal", Stage: "s1", Text: "Deal", To: "starter"},
		{Kind: "crmupdate", Entity: "deal", Value: "{created_card}", Sets: []bp.Assign{{Field: "title", Value: "Signed"}}},
		{Kind: "crmupdate", Sets: []bp.Assign{{Field: "comment", Value: "ok"}}},
		{Kind: "diskfolder", From: "starter", Drive: "company", Folder: "Docs/{name}"},
		{Kind: "diskcopy", From: "u1", Drive: "my", Folder: "Out", Value: "{created_folder}", Move: true},
		{Kind: "absence", Text: "Trip", Absence: "trip", Start: "from", End: "till"},
		{Kind: "task", Text: "Do it", To: "manager", Project: "P1", Days: 5, Users: []string{"u1"}, Watchers: []string{"u2"}, Note: "desc",
			High: true, Control: true, Wait: true, Items: []string{"one", "two"}},
		{Kind: "event", Text: "Meet", Users: []string{"u1"}, Start: "at", End: "", Note: "room 1"},
		{Kind: "sms", Text: "Code {code}", Value: "+7 900 000-00-00", Sender: "s1"},
		{Kind: "workday", To: "starter", Act: "finish", Text: "Report"},
		{Kind: "workdayinfo", To: "u1"},
		{Kind: "random", Field: "code", Size: 6, Chars: []string{"digits"}},
		{Kind: "foreach", Field: "list", Item: "one"},
		{Kind: "stage", Stage: "WON"},
		{Kind: "webhook", URL: "https://n8n.example.com/webhook/x"},
		{Kind: "end", Text: "Done"},
	}
	s := &Server{}
	for _, st := range steps {
		r := httptest.NewRequest(http.MethodPost, "/bp/templates/x", strings.NewReader(bpStepValues(st).Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := s.bpStepFromForm(r, auth.Session{}, nil); !reflect.DeepEqual(got, st) {
			t.Errorf("%s:\n got %+v\nwant %+v", st.Kind, got, st)
		}
	}
	// a pause of an old template in days — in the units of the form
	if v := bpStepValues(bp.Step{Kind: "wait", Days: 2}); v.Get("timeout") != "2" || v.Get("timeout_unit") != "day" {
		t.Errorf("an old pause: %v", v)
	}
	// a new step's form: the starter, a card's process mails the client, the random string of letters and digits
	v := bpNewStepValues(bp.Template{Entity: "deal"})
	if v.Get("to") != "starter" || v.Get("mail_to") != "client" || v.Get("size") != "8" || len(v["chars"]) != len(bp.RandomChars)-1 {
		t.Errorf("new step values %v", v)
	}
	if v := bpNewStepValues(bp.Template{Entity: "none"}); v.Has("mail_to") {
		t.Errorf("a standalone process mails the client: %v", v)
	}
}
