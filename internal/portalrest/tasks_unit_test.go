package portalrest

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWhen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		wantNil bool
		wantYr  int
	}{
		{"valid RFC3339 UTC", "2026-10-28T12:00:00Z", false, 2026},
		{"valid RFC3339 with offset", "2026-10-20T18:00:00+03:00", false, 2026},
		{"boundary year 1990", "1990-01-01T00:00:00Z", false, 1990},
		{"year before 1990 is nil", "1989-12-31T23:59:59Z", true, 0},
		{"zero year is nil", "0001-01-01T00:00:00Z", true, 0},
		{"empty string is nil", "", true, 0},
		{"spaces only is nil", "   \t\n  ", true, 0},
		{"invalid format is nil", "2026-10-20 18:00:00", true, 0},
		{"garbage string is nil", "not-a-timestamp", true, 0},
		{"mysql zero date is nil", "0000-00-00T00:00:00", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := when(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Errorf("when(%q) = %v, want nil", tc.in, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("when(%q) = nil, want non-nil", tc.in)
			}
			if got.Year() != tc.wantYr {
				t.Errorf("when(%q).Year() = %d, want %d", tc.in, got.Year(), tc.wantYr)
			}
		})
	}
}

func TestClip(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"shorter than limit", "hello", 10, "hello"},
		{"exact limit", "hello", 5, "hello"},
		{"longer than limit", "hello world", 5, "hello"},
		{"empty string", "", 5, ""},
		{"zero limit", "hello", 0, ""},
		{"cyrillic shorter than limit", "Привет", 10, "Привет"},
		{"cyrillic exact limit", "Привет", 6, "Привет"},
		{"cyrillic clipped by runes not bytes", "Привет, мир!", 6, "Привет"},
		{"multibyte emojis clipped by runes", "🚀🎉✨🌟🔥", 3, "🚀🎉✨"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := clip(tc.in, tc.n); got != tc.want {
				t.Errorf("clip(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestPortalTaskListJSON(t *testing.T) {
	// the portal tasks.task.list returns {"tasks": [...]}
	raw := `{
		"tasks": [
			{
				"id": "101",
				"parentId": "0",
				"title": "Сделать отчёт",
				"description": "Описание задачи [B]важно[/B]",
				"status": "5",
				"priority": "2",
				"groupId": "5",
				"createdBy": "1",
				"responsibleId": "2",
				"accomplices": ["3", "4"],
				"auditors": ["1"],
				"deadline": "2026-10-20T18:00:00+03:00",
				"startDatePlan": "2026-10-01T09:00:00+03:00",
				"endDatePlan": "2026-10-10T18:00:00+03:00",
				"createdDate": "2026-10-01T09:00:00+03:00",
				"closedDate": "2026-10-02T12:00:00+03:00",
				"changedDate": "2026-10-02T13:00:00+03:00",
				"taskControl": "Y",
				"allowChangeDeadline": "Y",
				"matchWorkTime": "N",
				"timeEstimate": "3600",
				"commentsCount": "3"
			},
			{
				"id": "102",
				"parentId": "101",
				"title": "Подзадача",
				"status": "2",
				"groupId": "0",
				"createdBy": "2",
				"responsibleId": "99",
				"commentsCount": 0
			}
		]
	}`

	var list portalTaskList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("Unmarshal(portalTaskList) failed: %v", err)
	}

	if len(list.Tasks) != 2 {
		t.Fatalf("Tasks count = %d, want 2", len(list.Tasks))
	}

	t1 := list.Tasks[0]
	if t1.ID != "101" || t1.ParentID != "0" {
		t.Errorf("t1 IDs: id=%q parent=%q", t1.ID, t1.ParentID)
	}
	if t1.Title != "Сделать отчёт" || t1.Description != "Описание задачи [B]важно[/B]" {
		t.Errorf("t1 text: title=%q desc=%q", t1.Title, t1.Description)
	}
	if t1.Status != "5" || t1.Priority != "2" || t1.GroupID != "5" {
		t.Errorf("t1 status=%q priority=%q group=%q", t1.Status, t1.Priority, t1.GroupID)
	}
	if t1.CreatedBy != "1" || t1.ResponsibleID != "2" {
		t.Errorf("t1 people: creator=%q responsible=%q", t1.CreatedBy, t1.ResponsibleID)
	}
	if !reflect.DeepEqual(t1.Accomplices, []string{"3", "4"}) {
		t.Errorf("t1 accomplices = %v", t1.Accomplices)
	}
	if !reflect.DeepEqual(t1.Auditors, []string{"1"}) {
		t.Errorf("t1 auditors = %v", t1.Auditors)
	}
	if t1.Deadline != "2026-10-20T18:00:00+03:00" {
		t.Errorf("t1 deadline = %q", t1.Deadline)
	}
	if t1.TaskControl != "Y" || t1.AllowDeadline != "Y" || t1.MatchWorkTime != "N" {
		t.Errorf("t1 flags: control=%q deadline=%q worktime=%q", t1.TaskControl, t1.AllowDeadline, t1.MatchWorkTime)
	}
	if t1.TimeEstimate != "3600" || t1.Comments != "3" {
		t.Errorf("t1 numbers: estimate=%q comments=%q", t1.TimeEstimate, t1.Comments)
	}

	t2 := list.Tasks[1]
	if t2.ID != "102" || t2.ParentID != "101" {
		t.Errorf("t2 IDs: id=%q parent=%q", t2.ID, t2.ParentID)
	}
	if t2.Comments != "0" {
		t.Errorf("t2 comments = %q, want 0", t2.Comments)
	}
}

func TestPortalGroupJSON(t *testing.T) {
	raw := `{
		"ID": 5,
		"NAME": "Отдел продаж",
		"DESCRIPTION": "Проектная группа",
		"OWNER_ID": "1",
		"CLOSED": "N",
		"SCRUM_MASTER_ID": 0
	}`

	var g portalGroup
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatalf("Unmarshal(portalGroup) failed: %v", err)
	}
	if g.ID != "5" {
		t.Errorf("ID = %q, want 5", g.ID)
	}
	if g.Name != "Отдел продаж" || g.Description != "Проектная группа" {
		t.Errorf("Name=%q Description=%q", g.Name, g.Description)
	}
	if g.OwnerID != "1" || g.Closed != "N" || g.Scrum != "0" {
		t.Errorf("OwnerID=%q Closed=%q Scrum=%q", g.OwnerID, g.Closed, g.Scrum)
	}
}

func TestPortalCommentJSON(t *testing.T) {
	raw := `{
		"ID": "900",
		"AUTHOR_ID": 2,
		"POST_MESSAGE": "Готово, проверяйте",
		"POST_DATE": "2026-10-02T11:00:00+03:00"
	}`

	var c portalComment
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("Unmarshal(portalComment) failed: %v", err)
	}
	if c.ID != "900" || c.Author != "2" {
		t.Errorf("ID=%q Author=%q", c.ID, c.Author)
	}
	if c.Message != "Готово, проверяйте" || c.Date != "2026-10-02T11:00:00+03:00" {
		t.Errorf("Message=%q Date=%q", c.Message, c.Date)
	}
}

func TestPortalCheckJSON(t *testing.T) {
	raw := `{
		"ID": 10,
		"PARENT_ID": "0",
		"TITLE": "Проверить договор",
		"IS_COMPLETE": "Y",
		"SORT_INDEX": 20
	}`

	var k portalCheck
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		t.Fatalf("Unmarshal(portalCheck) failed: %v", err)
	}
	if k.ID != "10" || k.ParentID != "0" {
		t.Errorf("ID=%q ParentID=%q", k.ID, k.ParentID)
	}
	if k.Title != "Проверить договор" || k.Done != "Y" || k.Sort != "20" {
		t.Errorf("Title=%q Done=%q Sort=%q", k.Title, k.Done, k.Sort)
	}
}

func TestBatchTasksResponseParsing(t *testing.T) {
	// Test decoding the batch response payload for groups, comments, and checklist items
	// as performed during task import in ImportTasks.
	membersRaw := []byte(`[{"USER_ID":"2","ROLE":"K"},{"USER_ID":1,"ROLE":"A"}]`)
	var members []struct {
		User flexStr `json:"USER_ID"`
		Role string  `json:"ROLE"`
	}
	if err := json.Unmarshal(membersRaw, &members); err != nil {
		t.Fatalf("Unmarshal group members failed: %v", err)
	}
	if len(members) != 2 || members[0].User != "2" || members[0].Role != "K" || members[1].User != "1" || members[1].Role != "A" {
		t.Errorf("unexpected members: %+v", members)
	}

	commentsRaw := []byte(`[{"ID":"901","AUTHOR_ID":"2","POST_MESSAGE":"Comment 1","POST_DATE":"2026-10-02T11:00:00+03:00"}]`)
	var comments []portalComment
	if err := json.Unmarshal(commentsRaw, &comments); err != nil {
		t.Fatalf("Unmarshal comments failed: %v", err)
	}
	if len(comments) != 1 || comments[0].ID != "901" || comments[0].Message != "Comment 1" {
		t.Errorf("unexpected comments: %+v", comments)
	}

	checksRaw := []byte(`[{"ID":"1","PARENT_ID":"0","TITLE":"Top list","IS_COMPLETE":"N","SORT_INDEX":"0"},{"ID":"2","PARENT_ID":"1","TITLE":"Item 1","IS_COMPLETE":"Y","SORT_INDEX":"1"}]`)
	var checks []portalCheck
	if err := json.Unmarshal(checksRaw, &checks); err != nil {
		t.Fatalf("Unmarshal checks failed: %v", err)
	}
	if len(checks) != 2 || checks[0].ID != "1" || checks[1].Done != "Y" || checks[1].ParentID != "1" {
		t.Errorf("unexpected checks: %+v", checks)
	}
}
