package tasks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseKey(t *testing.T) {
	tests := []struct {
		key     string
		wantKey string
		wantNum int
		wantErr bool
	}{
		{"GO-12", "GO", 12, false},
		{"proj-1", "PROJ", 1, false},
		{"CRM-100", "CRM", 100, false},
		{"A-B-3", "A-B", 3, false},
		{"X-Y-Z-456", "X-Y-Z", 456, false},
		{"", "", 0, true},
		{"GO", "", 0, true},
		{"GO-", "", 0, true},
		{"GO-0", "", 0, true},
		{"GO-abc", "", 0, true},
		{"-5", "", 0, true},
		{"GO--1", "GO-", 1, false}, // LastIndex splits before the final hyphen
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			k, n, err := ParseKey(tc.key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseKey(%q) error = %v, wantErr %v", tc.key, err, tc.wantErr)
			}
			if !tc.wantErr && (k != tc.wantKey || n != tc.wantNum) {
				t.Errorf("ParseKey(%q) = (%q, %d), want (%q, %d)", tc.key, k, n, tc.wantKey, tc.wantNum)
			}
		})
	}
}

func TestTimeValue(t *testing.T) {
	day := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
	got := TimeValue(3600, day, "  Worked on features  ")
	want := "3600|2026-10-28|Worked on features"
	if got != want {
		t.Errorf("TimeValue() = %q, want %q", got, want)
	}
}

func TestDateAndTextHelpers(t *testing.T) {
	// dateText
	if got := dateText(nil); got != "" {
		t.Errorf("dateText(nil) = %q, want empty", got)
	}
	d := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	if got := dateText(&d); got != "2026-10-28" {
		t.Errorf("dateText(&d) = %q, want '2026-10-28'", got)
	}

	// minutesText
	if got := minutesText(nil); got != "" {
		t.Errorf("minutesText(nil) = %q, want empty", got)
	}
	m := 90
	if got := minutesText(&m); got != "90" {
		t.Errorf("minutesText(&90) = %q, want '90'", got)
	}

	// fmtTime
	if got := fmtTime(nil); got != "" {
		t.Errorf("fmtTime(nil) = %q, want empty", got)
	}
	loc := time.FixedZone("UTC+3", 3*3600)
	tm := time.Date(2026, 10, 28, 15, 30, 0, 0, loc)
	wantRFC := "2026-10-28T12:30:00Z"
	if got := fmtTime(&tm); got != wantRFC {
		t.Errorf("fmtTime() = %q, want %q", got, wantRFC)
	}
}

func TestAdminContext(t *testing.T) {
	ctx := context.Background()
	if IsAdmin(ctx) {
		t.Error("IsAdmin(background) must be false")
	}

	adminCtx := WithAdmin(ctx)
	if !IsAdmin(adminCtx) {
		t.Error("IsAdmin(WithAdmin) must be true")
	}

	noAdminCtx := WithoutAdmin(adminCtx)
	if IsAdmin(noAdminCtx) {
		t.Error("IsAdmin(WithoutAdmin) must be false")
	}
}

func TestProjectPureMethods(t *testing.T) {
	// Scrum
	if !(Project{Kind: "scrum"}).Scrum() {
		t.Error("kind=scrum must be Scrum")
	}
	if (Project{Kind: "project"}).Scrum() {
		t.Error("kind=project must not be Scrum")
	}

	// CanManage
	for _, role := range []string{"owner", "manager", "admin"} {
		if !(Project{MyRole: role}).CanManage() {
			t.Errorf("role %q must be able to manage", role)
		}
	}
	for _, role := range []string{"member", "viewer", ""} {
		if (Project{MyRole: role}).CanManage() {
			t.Errorf("role %q must not be able to manage", role)
		}
	}

	// CanEdit: CanManage() && (!OwnerEdits || MyRole != "manager")
	pOwnerEditsManager := Project{MyRole: "manager", OwnerEdits: true}
	if pOwnerEditsManager.CanEdit() {
		t.Error("manager in OwnerEdits project cannot edit")
	}
	pOwnerEditsOwner := Project{MyRole: "owner", OwnerEdits: true}
	if !pOwnerEditsOwner.CanEdit() {
		t.Error("owner in OwnerEdits project can edit")
	}

	// CanLead: !Archived && (CanManage || ScrumLead)
	if (Project{Archived: true, MyRole: "owner"}).CanLead() {
		t.Error("archived project cannot lead")
	}
	if !(Project{MyRole: "member", ScrumLead: true}).CanLead() {
		t.Error("scrum lead member can lead")
	}

	// CanWork: !Archived && (CanManage || MyRole == "member")
	if (Project{Archived: true, MyRole: "member"}).CanWork() {
		t.Error("archived project cannot work")
	}
	if !(Project{MyRole: "member"}).CanWork() {
		t.Error("member can work")
	}

	// Frozen: Collab && Archived
	if !(Project{Kind: "collab", Archived: true}).Frozen() {
		t.Error("archived collab must be frozen")
	}
	if (Project{Kind: "project", Archived: true}).Frozen() {
		t.Error("archived regular project is not frozen (only collabs freeze)")
	}

	// CanCreate: CanWork && !NoGuestTask && ToolOn("tasks")
	pNoGuestTask := Project{MyRole: "member", NoGuestTask: true}
	if pNoGuestTask.CanCreate() {
		t.Error("NoGuestTask must prevent CanCreate")
	}
	pTasksOff := Project{MyRole: "member", ToolsOff: []string{"tasks"}}
	if pTasksOff.CanCreate() {
		t.Error("tasks tool off must prevent CanCreate")
	}
	pCanCreate := Project{MyRole: "member"}
	if !pCanCreate.CanCreate() {
		t.Error("regular member can create tasks")
	}

	// PhotoURL
	pNoPhoto := Project{Key: "PRJ"}
	if got := pNoPhoto.PhotoURL(); got != "" {
		t.Errorf("PhotoURL without key = %q, want empty", got)
	}
	pWithPhoto := Project{Key: "PRJ", PhotoKey: "storage-avatar-key"}
	if got := pWithPhoto.PhotoURL(); !strings.HasPrefix(got, "/p/PRJ/photo?v=") {
		t.Errorf("PhotoURL = %q, want prefix /p/PRJ/photo?v=", got)
	}
}

func TestTaskPureMethods(t *testing.T) {
	// DatesFollow
	if !(Task{Params: Params{DatesFromSubtasks: true}, SubTotal: 2}).DatesFollow() {
		t.Error("DatesFromSubtasks with SubTotal > 0 must follow dates")
	}
	if (Task{Params: Params{DatesFromSubtasks: true}, SubTotal: 0}).DatesFollow() {
		t.Error("DatesFromSubtasks with SubTotal == 0 must not follow dates")
	}
	if (Task{Params: Params{DatesFromSubtasks: false}, SubTotal: 5}).DatesFollow() {
		t.Error("DatesFromSubtasks false must not follow dates")
	}

	// Key
	if got := (Task{ProjectKey: "GO", Number: 42}).Key(); got != "GO-42" {
		t.Errorf("Key() = %q, want 'GO-42'", got)
	}

	// Overdue
	now := time.Now()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	if (Task{Deadline: nil}).Overdue() {
		t.Error("nil deadline cannot be overdue")
	}
	if (Task{Deadline: &past, Status: StatusCompleted}).Overdue() {
		t.Error("completed task cannot be overdue")
	}
	if !(Task{Deadline: &past, Status: StatusInProgress}).Overdue() {
		t.Error("in-progress task with past deadline must be overdue")
	}
	if (Task{Deadline: &future, Status: StatusInProgress}).Overdue() {
		t.Error("in-progress task with future deadline must not be overdue")
	}
}

func TestTaskCardPureMethods(t *testing.T) {
	// CanComplete
	cCanComplete := TaskCard{Actions: []Action{ActionStart, ActionComplete}}
	if !cCanComplete.CanComplete() {
		t.Error("card with ActionComplete must CanComplete")
	}
	cNoComplete := TaskCard{Actions: []Action{ActionStart}}
	if cNoComplete.CanComplete() {
		t.Error("card without ActionComplete cannot CanComplete")
	}

	// TimePercent & OverEstimate
	// EstimateMinutes nil or 0
	cNoEst := TaskCard{TimeTotal: 3600, Task: Task{EstimateMinutes: nil}}
	if cNoEst.TimePercent() != 0 || cNoEst.OverEstimate() {
		t.Errorf("no estimate: percent=%d, overEst=%v", cNoEst.TimePercent(), cNoEst.OverEstimate())
	}
	zero := 0
	cZeroEst := TaskCard{TimeTotal: 3600, Task: Task{EstimateMinutes: &zero}}
	if cZeroEst.TimePercent() != 0 || cZeroEst.OverEstimate() {
		t.Errorf("zero estimate: percent=%d, overEst=%v", cZeroEst.TimePercent(), cZeroEst.OverEstimate())
	}

	// 60 minutes estimate (3600 sec)
	est60 := 60
	// 50% spent (1800 sec)
	cHalf := TaskCard{TimeTotal: 1800, Task: Task{EstimateMinutes: &est60}}
	if cHalf.TimePercent() != 50 || cHalf.OverEstimate() {
		t.Errorf("half: percent=%d, overEst=%v", cHalf.TimePercent(), cHalf.OverEstimate())
	}
	// 100% spent (3600 sec)
	cFull := TaskCard{TimeTotal: 3600, Task: Task{EstimateMinutes: &est60}}
	if cFull.TimePercent() != 100 || cFull.OverEstimate() {
		t.Errorf("full: percent=%d, overEst=%v", cFull.TimePercent(), cFull.OverEstimate())
	}
	// 150% spent (5400 sec): percent capped at 100, OverEstimate is true
	cOver := TaskCard{TimeTotal: 5400, Task: Task{EstimateMinutes: &est60}}
	if cOver.TimePercent() != 100 || !cOver.OverEstimate() {
		t.Errorf("over: percent=%d, overEst=%v", cOver.TimePercent(), cOver.OverEstimate())
	}

	// CanEdit: Roles.Creator || Roles.Manager
	if !(TaskCard{Roles: Roles{Creator: true}}).CanEdit() {
		t.Error("creator must CanEdit")
	}
	if !(TaskCard{Roles: Roles{Manager: true}}).CanEdit() {
		t.Error("manager must CanEdit")
	}
	if (TaskCard{Roles: Roles{Responsible: true}}).CanEdit() {
		t.Error("responsible cannot CanEdit card settings")
	}

	// CanWork: Roles.Worker() || Roles.Manager
	if !(TaskCard{Roles: Roles{Responsible: true}}).CanWork() {
		t.Error("responsible must CanWork")
	}
	if !(TaskCard{Roles: Roles{Accomplice: true}}).CanWork() {
		t.Error("accomplice must CanWork")
	}
	if (TaskCard{Roles: Roles{Auditor: true}}).CanWork() {
		t.Error("auditor cannot CanWork")
	}
}

func TestAccessVisibleAndCanLogTime(t *testing.T) {
	// access.visible
	// Collab without MemberRole
	aCollabStranger := access{Collab: true, MemberRole: "", Roles: Roles{Creator: true}}
	if aCollabStranger.visible() {
		t.Error("collab stranger must never see tasks")
	}

	// Members visibility
	aMembersVis := access{Visibility: "members", MemberRole: "member"}
	if !aMembersVis.visible() {
		t.Error("member in visibility=members must see task")
	}

	// Participants roles
	for _, r := range []Roles{
		{Creator: true},
		{Responsible: true},
		{Accomplice: true},
		{Auditor: true},
	} {
		if !(access{Roles: r}).visible() {
			t.Errorf("role %+v must see task", r)
		}
	}

	// Special flags: FlowPerson, Supervisor, ViewAll
	if !(access{FlowPerson: true}).visible() {
		t.Error("FlowPerson must see task")
	}
	if !(access{Supervisor: true}).visible() {
		t.Error("Supervisor must see task")
	}
	if !(access{ViewAll: true}).visible() {
		t.Error("ViewAll must see task")
	}

	// Outsider on participants visibility
	aOutsider := access{Visibility: "participants", MemberRole: "member"}
	if aOutsider.visible() {
		t.Error("outsider on participants visibility cannot see task unless assigned a role")
	}

	// access.canLogTime: Worker() || Manager
	if !(access{Roles: Roles{Responsible: true}}).canLogTime() {
		t.Error("responsible can log time")
	}
	if !(access{Roles: Roles{Accomplice: true}}).canLogTime() {
		t.Error("accomplice can log time")
	}
	if !(access{Roles: Roles{Manager: true}}).canLogTime() {
		t.Error("manager can log time")
	}
	if (access{Roles: Roles{Auditor: true}}).canLogTime() {
		t.Error("auditor cannot log time")
	}
}
