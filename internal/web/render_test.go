package web

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/backup"
	"go24/internal/auth"
	"go24/internal/bi"
	"go24/internal/booking"
	"go24/internal/bp"
	"go24/internal/calendar"
	"go24/internal/calls"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/hooks"
	"go24/internal/i18n"
	"go24/internal/ipaccess"
	"go24/internal/kb"
	"go24/internal/mail"
	"go24/internal/marketing"
	"go24/internal/matrixsync"
	"go24/internal/notify"
	"go24/internal/notifyout"
	"go24/internal/openlines"
	"go24/internal/portal"
	"go24/internal/sign"
	"go24/internal/sites"
	"go24/internal/storages"
	"go24/internal/sysinfo"
	"go24/internal/tasks"
	"go24/internal/telephony"
	"go24/internal/worktime"
)

// TestPagesRender executes every page template with sample data, so template typos fail in CI, not in the browser.
func TestPagesRender(t *testing.T) {
	now := time.Now()
	vat20, vat7 := 20.0, 7.5
	later := now.Add(3 * time.Hour)
	mailCats := []mail.Category{{ID: "c1", Name: "Важное", Color: "red"}, {ID: "c2", Name: "Клиенты", Color: "teal"}}
	deadline := now.Add(-time.Hour) // overdue
	user := tasks.User{ID: "u1", Name: "Борис", Email: "b@example.ru"}
	shiftWS := company.WorkSchedule{ID: 2, Name: "Склад", Kind: "shift", Tolerance: 15, Members: []string{"dept:d1"},
		Shifts: []company.Shift{{ID: 7, Name: "Дневная", Start: "08:00", End: "20:00", Break: 60}, {ID: 8, Name: "Ночная", Start: "20:00", End: "08:00"}}}
	task := tasks.Task{
		ID: "t1", ProjectKey: "GO", Number: 7, Title: "Сделать трекер", Status: tasks.StatusInProgress, Priority: 2,
		CreatorID: "u1", CreatorName: "Борис", ResponsibleID: "u1", ResponsibleName: "Борис",
		ParentKey: "GO-1", ParentTitle: "Эпик", RequiresControl: true, Deadline: &deadline, Version: 3,
		CreatedAt: now, UpdatedAt: now, FlowID: "f1", FlowName: "Поддержка",
	}
	project := tasks.Project{ID: "p1", Key: "GO", Name: "go24", MyRole: "owner", OpenTasks: 1, Visibility: "participants"}
	card := tasks.TaskCard{CanDelete: true,
		Task:        task,
		Roles:       tasks.Roles{Creator: true, Responsible: true, Manager: true},
		MemberRole:  "owner",
		Accomplices: []tasks.User{user},
		Checklist:   []tasks.ChecklistItem{{ID: "c1", Title: "Схема БД", Done: true}, {ID: "c2", Title: "UI"}},
		Comments: []tasks.Comment{{ID: "m1", AuthorName: "Борис", Body: "строка 1\nстрока 2", CreatedAt: now},
			{ID: "m2", AuthorID: user.ID, AuthorName: user.Name, Body: "моё", CreatedAt: now}},
		History: []tasks.HistoryEntry{{ActorName: "Борис", Field: "status", Old: "pending", New: "in_progress", CreatedAt: now},
			{ActorName: "Борис", Field: "stage", Old: "stage.new", New: "Ревью", CreatedAt: now}},
		Subtasks:    []tasks.Task{task},
		Actions:     tasks.AllowedActions(tasks.StatusInProgress, tasks.Roles{Creator: true}, true),
		TimeEntries: []tasks.TimeEntry{{ID: "e1", UserID: "u1", UserName: "Борис", Seconds: 5400, SpentOn: now, Source: "timer"}},
		TimeTotal:   5400,
	}
	est := 240
	card.EstimateMinutes = &est
	pause := 900

	scrumP := project
	scrumP.Kind = "scrum"
	dept := &company.Department{ID: "d1", Name: "Компания", HeadID: "u1", HeadName: "Борис",
		Employees: []company.Person{{ID: "u1", Name: "Борис"}},
		Children:  []*company.Department{{ID: "d2", ParentID: "d1", Name: "ИТ", Depth: 1, Employees: []company.Person{{ID: "u2", Name: "Пётр", Position: "Инженер"}}}}}

	mktSeg := marketing.Segment{ID: "g1", Name: "Все контакты", Entity: "contact", Filter: marketing.Filter{Q: "ООО"}, CreatedBy: "u1", AuthorName: "Анна", UpdatedAt: now}
	mktDraft := marketing.Campaign{ID: "c1", Title: "Акция", MailboxID: "b1", Subject: "Скидки", Body: "<p>Привет</p>", Status: "draft", SendAt: &now,
		CreatedBy: "u1", AuthorName: "Анна", UpdatedAt: now, Segments: []string{"g1"}}
	mktSent := marketing.Campaign{ID: "c2", Title: "Осень", MailboxID: "b1", Subject: "Осень", Body: "<p>Привет</p>", Status: "sending", StartedAt: &now,
		CreatedBy: "u1", AuthorName: "Анна", UpdatedAt: now, Total: 10, Sent: 6, Errors: 1, Skipped: 1, Opened: 4, Clicked: 2, Unsubscribed: 1, Segments: []string{"g1"}}
	cases := map[string]page{
		"login": {Title: "Вход"},
		"my": {Data: map[string]any{"Role": "responsible", "Tabs": []myTab{{"responsible", 1}}, "Tasks": []tasks.Task{task}, "ShowAll": false, "View": "comments",
			"Bulk": bulkBar{Back: "/my", Users: []tasks.User{user}, Ops: bulkOps}, "Project": &tasks.Project{Key: "CL1", Name: "Клиент"}}},
		"projects": {Data: map[string]any{"List": []tasks.Project{project, {Key: "AR", Name: "Архивный", Kind: "project", PhotoKey: "k", Archived: true}}, "Archived": false,
			"Invites": []tasks.Invite{{ProjectKey: "IN", ProjectName: "Новый", Role: "member", InvitedBy: "Борис", CreatedAt: now}}}},
		"project": {Data: map[string]any{"CanInvite": true, "InvitePerm": "managers", "Invites": []tasks.Invite{{UserID: "u9", UserName: "Анна", Role: "viewer", InvitedBy: "Борис"}}, "Project": project, "Tasks": []tasks.Task{task},
			"Members": []tasks.Member{{User: user, Role: "owner"}}, "Users": []tasks.User{user}, "ShowAll": true, "Me": "u1", "DefaultDeadline": now}},
		"task": {Data: map[string]any{"Card": card, "Users": []tasks.User{user}, "CanTake": true, "AppTabs": []apps.Placement{{ID: "p1", Placement: "TASK_VIEW_TAB", Handler: "https://app/t", AppTitle: "Доставка", Description: "Расчёт"}}, "DodComplete": true, "Dod": &tasks.TaskDod{InSprint: true, CanEdit: true, Types: []tasks.ScrumType{{ID: "st1", Name: "Задача"}}, Type: tasks.ScrumType{ID: "st1", Name: "Задача", Required: true, Items: []tasks.DodItem{{ID: "i1", Title: "Тесты", Done: true}, {ID: "i2", Title: "Ревью"}}}}, "Viewers": []tasks.TaskViewer{{ID: "u1", Name: "Иван", First: now, Last: now, Times: 3}},
			"Pred": []tasks.Link{{ID: "l1", Type: "start_start", FromKey: "GO-1", FromTitle: "Эпик"}}, "Succ": []tasks.Link{{ID: "l2", Type: "finish_start", ToKey: "GO-9", ToTitle: "После"}}, "LinkTypes": tasks.LinkTypes,
			"Files": []storages.File{{ID: "f1", Name: "a.pdf", CreatedAt: now}}, "Storages": []storages.Storage{{ID: "s1", Name: "NC"}},
			"People": 3, "BaseURL": "http://x", "Chat": chatFeed(card, user.ID, map[string]bool{"c1": true}), "Results": []tasks.Result{{Comment: tasks.Comment{ID: "c1", AuthorName: "Анна", Body: "готово", CreatedAt: now}, MarkedBy: "Я", MarkedAt: now}}, "ResultIDs": map[string]bool{"c1": true}, "Form": editTaskForm(card, []tasks.User{user}),
			"Calls": true, "Ancestors": []tasks.TaskRef{{Key: "GO-1", Title: "Корень"}}, "SubDone": 1, "SubTotal": 3,
			"Marks": map[string]bool{"pin": true, "favorite": true}, "Tags": []string{"срочно"}, "AllTags": []string{"срочно", "клиент"},
			"Reminders": []tasks.Reminder{{ID: "r1", Recipient: "self", At: &now}, {ID: "r2", Recipient: "responsible", BeforeMinutes: 60, Due: &now, Sent: &now},
				{ID: "r3", Recipient: "creator", BeforeMinutes: 1440}}, "ReminderBefore": reminderBefore,
			"Fields": []tasks.FieldValue{{Field: tasks.Field{ID: "uf1", Name: "Сумма", Kind: "number"}, Value: "10"},
				{Field: tasks.Field{ID: "uf2", Name: "Этап", Kind: "list", Options: []string{"А", "Б"}}, Value: "Б"},
				{Field: tasks.Field{ID: "uf3", Name: "Срочно", Kind: "bool"}, Value: "1"}, {Field: tasks.Field{ID: "uf4", Name: "Дата", Kind: "date"}, Value: "2026-10-13"},
				{Field: tasks.Field{ID: "uf5", Name: "Ссылка", Kind: "url"}}},
			"CRM": []tasks.CRMLink{{URL: "https://crm.example/crm/deal/details/1/", Kind: "deal", EntityID: 1}, {URL: "https://x.example/", Kind: "other"}}}},
		"timeman": {WorkdayOn: true, VacationRemind: true, CanInvite: true, Workday: &company.Workday{Started: now, PausedAt: &now}, Path: "/worktime",
			DayPlan: []tasks.DayPlanItem{{Task: tasks.Task{ProjectKey: "GO", Number: 1, Title: "Отчёт", Status: tasks.StatusInProgress}, TodaySecs: 1800, Timer: true},
				{Task: tasks.Task{ProjectKey: "GO", Number: 2, Title: "Звонок", Status: tasks.StatusCompleted}}},
			DayPlanChoices: []tasks.Task{{ProjectKey: "GO", Number: 3, Title: "Письмо"}}, Data: map[string]any{
			"Days": []company.Workday{{Day: now, Started: now, Ended: &now, PauseSecs: 600, Report: "ок"}, {Day: now, Started: now}},
			"Team": []company.Workday{{UserID: "u2", Name: "Анна", Started: now}}, "TeamSize": 3, "Start": "09:00", "Now": now, "Total": 3600, "Late": map[string]bool{"u2|" + now.Format(time.DateOnly): true},
			"Month": now, "Prev": "2026-09", "Next": "2026-11", "Me": "u1", "Forgotten": &company.Workday{UserID: "u1", Day: now, Started: now},
			"Pending": []company.WorkdayChange{{ID: 1, UserID: "u2", UserName: "Анна", Day: now, AuthorName: "Анна", Started: &now, OldStarted: &now,
				PauseSecs: &pause, Reason: "забыла"}}}},
		"worksheet": {Data: map[string]any{"Month": now, "Dept": "", "Depts": []*company.Department{{ID: "d1", Name: "Отдел", Depth: 1}},
			"Dates": []time.Time{now, now.AddDate(0, 0, 1)}, "Off": map[string]bool{}, "Today": now.Format(time.DateOnly), "Prev": "2026-09", "Next": "2026-11",
			"Rows": company.BuildSheet([]company.Person{{ID: "u1", Name: "Анна", Position: "Бухгалтер"}},
				[]company.Workday{{UserID: "u1", Day: now, Started: now.Add(-9 * time.Hour), Ended: &now}},
				[]company.Absence{{UserID: "u1", Kind: "sick", From: now.AddDate(0, 0, 1), To: now.AddDate(0, 0, 1)}},
				map[string]bool{"u1|" + now.Format(time.DateOnly): true}, map[string]company.Norm{"u1": {Company: worktime.Default(), WS: &shiftWS,
					Shifts: map[string]company.Shift{now.AddDate(0, 0, 2).Format(time.DateOnly): shiftWS.Shifts[0]}}}, worktime.Default(), time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local), now)}},
		"workday": {Data: map[string]any{"Person": company.Profile{ID: "u1", Name: "Анна"}, "Day": now, "Now": now,
			"Tasks": []tasks.DayTask{{Key: "GO-1", Title: "Отчёт", Secs: 1800}},
			"D": &company.Workday{UserID: "u1", Day: now, Started: now, Ended: &now, PauseSecs: 600, Report: "ок"}, "Flags": []string{"late"},
			"Changes": []company.WorkdayChange{{ID: 1, AuthorName: "Анна", Ended: &now, Reason: "забыла", Status: "approved", DeciderName: "Борис", CreatedAt: now}},
			"Pending": &company.WorkdayChange{ID: 2, AuthorName: "Анна", Started: &now, Reason: "пробка", Status: "pending"},
			"Manager": true, "Approval": false, "CanEdit": true, "Start": "09:00", "End": "18:00", "Pause": 10, "Path": "/worktime/d/u1/2026-10-25",
			"Norm": company.Norm{WS: &shiftWS}, "Plan": company.DayPlan{Work: true, Start: "20:00", End: "08:00", Break: 60, Shift: "Ночная"}}},
		"wschedules": {Data: map[string]any{"Admin": true, "List": []company.ScheduleRef{{ID: 1, Name: "Офис", Kind: "fixed", Depts: 2, People: 1},
			{ID: 2, Name: "Склад", Kind: "shift"}}}},
		"wschedule": {Data: map[string]any{"WS": &shiftWS, "Times": []company.ScheduleTime{{Days: []int{1, 2}, Start: "09:00", End: "18:00", Break: 60}, {}},
			"Shifts": append(shiftWS.Shifts, company.Shift{}), "Kinds": company.ScheduleKinds, "Days": []int{1, 2, 3, 4, 5, 6, 0},
			"Members": []tasks.User{{ID: "dept:d1", Name: "🏢 Склад"}, {ID: "u1", Name: "Анна"}}, "Excluded": []tasks.User{{ID: "u2", Name: "Олег"}},
			"People": []company.Person{{ID: "u1", Name: "Анна"}}, "Admin": true, "Tolerance": "15"}},
		"shiftplan": {Data: map[string]any{"WS": &shiftWS, "People": []company.Person{{ID: "u1", Name: "Анна"}, {ID: "u2", Name: "Олег"}},
			"Can": map[string]bool{"u1": true}, "Editable": true, "Plan": map[string]int64{"u1|" + now.Format(time.DateOnly): 7, "u2|" + now.Format(time.DateOnly): 8},
			"Absent": map[string]string{"u2|" + now.AddDate(0, 0, 1).Format(time.DateOnly): "vacation"}, "Dates": []time.Time{now, now.AddDate(0, 0, 1)},
			"Month": now, "Today": now.Format(time.DateOnly), "Counts": map[int64]int{7: 1}, "Prev": "2026-09", "Next": "2026-11"}},
		"invite": {Data: map[string]any{"Ready": true, "Now": now, "New": "ann@test", "Invites": []company.Invite{
			{Email: "ann@test", Name: "Анна", Link: "https://sso/x", InvitedBy: "Борис", Expires: now.Add(time.Hour), Created: now},
			{Email: "bob@test", Name: "Боб", Joined: true, Expires: now, Created: now}},
			"Admin": true, "Link": "https://go24/register/tok", "Requests": []company.JoinRequest{
				{ID: "j1", Email: "eve@firm.ru", Name: "Ева", IP: "10.0.0.1", State: "pending", Created: now},
				{ID: "j2", Email: "max@firm.ru", Name: "Макс", State: "rejected", Created: now, DecidedBy: "Борис", DecidedAt: &now}}}},
		"staffsettings": {Data: map[string]any{"S": company.StaffSettings{InviteAll: true, Register: true, Approve: true,
			Domains: []string{"firm.ru", "firm.com"}}, "Link": "https://go24/register/tok", "Ready": true}},
		"register": {Data: map[string]any{"Open": true, "Token": "tok", "Domains": []string{"firm.ru"}, "Approve": true}},
		"kb": {Data: map[string]any{"Q": "отпуск", "Projects": []tasks.Project{project},
			"Spaces": []kb.Space{{ID: "s1", Name: "Регламенты", Pages: 3}, {ID: "s2", Name: "go24", ProjectKey: "GO", ProjectName: "go24"}},
			"Hits":   []kb.Hit{{ID: "p1", SpaceID: "s1", SpaceName: "Регламенты", Title: "Отпуск", Snippet: "как оформить"}}}},
		"booking": {Data: map[string]any{"Day": now, "Today": true, "Prev": "2026-10-24", "Next": "2026-10-26", "Type": "",
			"Types": []booking.Type{{ID: "t1", Code: "room", Resources: 1}, {ID: "t2", Name: "Тренажёры", Resources: 1}},
			"Cols": []bookingCol{{R: booking.Resource{ID: "r1", TypeCode: "room", Name: "Переговорная", UserName: "Анна", Slot: 30, From: 540, To: 1080},
				OffTop: 0, OffBot: 432, Bookings: []bookingItem{{B: booking.Booking{ID: "b1", Start: now, End: now.Add(time.Hour), ClientName: "Иван", ClientPhone: "+7", Status: "confirmed", CreatedByName: "Борис",
					Extras: []string{"r2"}, ExtraNames: []string{"Дорожка"}}, Top: 48, Height: 48, Mins: 60}}},
				{R: booking.Resource{ID: "r2", TypeName: "Тренажёры", Name: "Дорожка"}, Off: true}},
			"Hours": []int{9, 10}, "Hour0": 9, "HourPx": 48, "GridPx": 432, "Statuses": booking.Statuses,
			"Counts": map[string]int{"confirmed": 1}, "Admin": true,
			"All": []booking.Resource{{ID: "r1", Name: "Переговорная", Slot: 30}, {ID: "r2", Name: "Дорожка", Slot: 60}},
			"Wait": []booking.Wait{{ID: "w1", ResourceID: "r1", ResourceName: "Переговорная", ClientName: "Мария", ClientPhone: "+7", Note: "утром"},
				{ID: "w2", ClientPhone: "+7 2"}}}},
		"bookingrights": {Data: map[string]any{"Users": []tasks.User{user}, "Use": []tasks.User{{ID: "dept:d1", Name: "Отдел продаж"}, user}, "Manage": nil}},
		"bookinglinks": {Data: map[string]any{"Admin": true, "Base": "https://go24.test", "Msg": "Сохранено",
			"Links": []booking.Link{{ID: "l1", Token: "tk", Name: "Запись", Resources: []string{"r1"}, DaysAhead: 30, Active: true, Bookings: 2},
				{ID: "l2", Token: "tk2", Name: "Все"}},
			"Resources": []booking.Resource{{ID: "r1", Name: "Врач"}}, "Names": map[string]string{"r1": "Врач"},
			"Codes": map[string]string{"l1": bookingEmbedCode("https://go24.test/bk/tk", "Запись")},
			"Edit":  booking.Link{ID: "l1", Name: "Запись", Resources: []string{"r1"}, DaysAhead: 30, Active: true},
			"Users": []tasks.User{{ID: "u1", Name: "Анна"}}}},
		"bookingpub": {Data: map[string]any{"Link": booking.Link{Name: "Запись к врачу", Description: "Приём"}, "Token": "tk",
			"Resources": []booking.Resource{{ID: "r1", Name: "Врач", TypeCode: "employee", Slot: 30}, {ID: "r2", Name: "Кабинет", TypeName: "Свой", Slot: 60}},
			"Res":       &booking.Resource{ID: "r1", Name: "Врач", Slot: 30}, "Day": now, "DayValue": "2026-10-26", "Days": []bookingDay{{now, "2026-10-26", true}, {now, "2026-10-27", false}},
			"PrevWeek": "2026-10-19", "NextWeek": "2026-11-02", "Free": []time.Time{now}, "Slot": "10:00", "Msg": "Занято",
			"Name": "Иван", "Phone": "+7", "Keep": template.URL("name=%D0%98")}},
		"calslotpub": {Data: map[string]any{"Link": calendar.SlotLink{OwnerName: "Борис", Members: []string{"u2"}, MemberNames: []string{"Анна"}, Length: 30},
			"Token": "tk", "Day": now, "DayValue": "2026-10-26", "Days": []bookingDay{{now, "2026-10-26", true}, {now, "2026-10-27", false}},
			"PrevWeek": "2026-10-19", "NextWeek": "2026-11-02", "Free": []time.Time{now}, "Slot": "10:00", "Msg": "Занято",
			"Name": "Иван", "Contact": "ivan@x.ru", "Keep": template.URL("name=%D0%98")}},
		"sign": {Data: map[string]any{"Filter": "", "Filters": sign.Filters, "Waiting": 1, "Me": "u1", "HR": true, "Docs": []sign.Doc{
			{ID: "s1", Title: "Договор № 7", Status: "signing", Author: "Анна", CreatedAt: now, Parties: []sign.Party{
				{ID: "p1", UserID: "u1", Name: "Анна", Ord: 1, Status: "ready"}, {ID: "p2", Name: "Иван", Email: "i@t", Ord: 2, Status: "wait"}}}}}},
		"signnew": {Data: map[string]any{"Me": "u1", "Users": []tasks.User{{ID: "u1", Name: "Анна"}}, "Kind": "deals", "ID": "d1",
			"Doc": crm.Document{ID: "x1", FileName: "Договор.docx"}, "Title": "Договор № 7", "ClientName": "Иван", "ClientEmail": "i@t"}},
		"signdoc": {Data: map[string]any{"Me": "u1", "Mine": true, "MySig": "data:image/png;base64,AA", "MyName": "Анна", "Author": true, "PDF": true, "Link": "https://go24/sg/", "Card": "/crm/deals/d1#documents",
			"Doc": sign.Doc{ID: "s1", Title: "Договор № 7", Status: "signing", Author: "Анна", AuthorID: "u1", CreatedAt: now, Hash: "ab", Parties: []sign.Party{
				{ID: "p1", UserID: "u1", Name: "Анна", Ord: 1, Status: "signed", SignedAt: &now, HasSignature: true}, {ID: "p2", Name: "Иван", Email: "i@t", Token: "tk", Ord: 2, Status: "ready"}}}}},
		"signhr": {Data: map[string]any{"Users": []tasks.User{{ID: "u1", Name: "Анна"}}, "Fields": employeeFields, "Admin": true,
			"Templates": []crm.DocTemplate{{ID: "t1", Name: "Приказ", Entity: "employee"}}, "Self": map[string]string{"t1": "u1"}}},
		"signself": {Data: map[string]any{"Templates": []sign.SelfTemplate{{TemplateID: "t1", Name: "Заявление", SignerID: "u1", Signer: "Анна"}},
			"Tpl": sign.SelfTemplate{TemplateID: "t1", Name: "Заявление", Signer: "Анна"}, "Fields": []selfField{{Name: "StartDate", Date: true}, {Name: "Reason"}}}},
		"signbatch": {Data: map[string]any{"Me": "u2", "Author": true, "Mine": true,
			"Batch": sign.Batch{ID: "b1", Title: "Приказ", SignerID: "u2", Signer: "Боб", Author: "Анна", CreatedAt: now, Total: 3, Signed: 1, Refused: 1, Signing: 1, Ready: 1},
			"Docs": []sign.Doc{{ID: "s1", Title: "Приказ — Ева", Status: "refused", DoneAt: &now, BatchID: "b1", Parties: []sign.Party{
				{ID: "p1", UserID: "u2", Name: "Боб", Ord: 1, Status: "signed"}, {ID: "p2", UserID: "u3", Name: "Ева", Side: "employee", Ord: 2, Status: "refused", Reason: "Не согласна"}}}}}},
		"signpub": {Data: map[string]any{"Token": "tk", "Turn": true, "PDF": true, "Email": "i***@t", "Msg": "Код отправлен", "Code": true,
			"Party": &sign.Party{ID: "p2", Name: "Иван", Status: "ready"}, "Doc": sign.Doc{ID: "s1", Title: "Договор", Status: "signing", Author: "Анна"}}},
		"bi": {Data: map[string]any{"Dashboards": []bi.Dashboard{{ID: "d1", Title: "Продажи", Description: "Сделки", CreatedBy: "u1",
			AuthorName: "Борис", UpdatedAt: now, Widgets: 3, Status: "draft", GroupID: "g1", GroupTitle: "Финансы"}}, "Q": "", "Periods": bi.Periods,
			"Group": "g1", "Groups": []bi.Group{{ID: "g1", Title: "Финансы", Dashboards: 1}},
			"Ready": []bi.Dashboard{{ID: "d0", Code: "leads", Title: "Лиды", Description: "Источники", Widgets: 8}}}},
		"bidash": {Data: map[string]any{"D": bi.Dashboard{ID: "d1", Title: "Продажи", Description: "Сделки", Period: "range", DateFrom: &now, DateTo: &later,
			Status: "draft", GroupID: "g1", GroupTitle: "Финансы"}, "Manage": true, "Groups": []bi.Group{{ID: "g1", Title: "Финансы"}},
			"Rights": []bi.Right{{ID: "r1", Kind: "all", Access: "view"}, {ID: "r2", Kind: "department", WhomID: "x", Whom: "Продажи", Access: "edit"}},
			"Users": []tasks.User{{ID: "u1", Name: "Анна"}}, "Departments": []drive.Department{{ID: "x", Name: "Продажи"}},
			"Accesses": bi.Accesses, "Statuses": bi.Statuses,
			"Panels": biSamplePanels(), "Period": "month", "PFrom": nil, "PTo": nil, "From": &now, "Last": &later, "Periods": bi.Periods,
			"Widths": bi.Widths, "Kinds": bi.Kinds, "Can": true, "Edit": true, "Settings": true, "Self": "/bi/d1",
			"Path": "/bi/d1", "Keep": "?period=month", "Link": "/bi/d1?period=month", "CanPin": true, "PinForm": true, "Sections": bi.Sections}},
		"bisection": {Data: map[string]any{"In": "crm", "Kind": "bi", "D": bi.Dashboard{ID: "d1", Title: "Продажи", Sections: []string{"crm"}},
			"Pinned": []bi.Dashboard{{ID: "d1", Title: "Продажи"}, {ID: "d2", Title: "Мой", Status: "draft"}}, "Panels": biSamplePanels(),
			"Period": "month", "From": &now, "Last": &later, "Periods": bi.Periods, "Path": "/crm/bi", "Keep": "", "Self": "/bi/d1",
			"Link": "/crm/bi?d=d1"}},
		"bifiles": {Data: map[string]any{"Admin": false, "UserID": "u1", "MaxMB": 20, "Files": []bi.File{{ID: 1, Name: "Продажи", Description: "По городам",
			Columns: []bi.FileColumn{{Code: "c1"}}, Rows: 3, CreatedBy: "u1", AuthorName: "Анна", UpdatedAt: now}}}},
		"bigroups": {Data: map[string]any{"Admin": true, "UserID": "u1", "Groups": []bi.Group{{ID: "g1", Title: "Финансы", CreatedBy: "u1", Dashboards: 2}}}},
		"bikeys": {Data: map[string]any{"Keys": []bi.Key{{ID: "k1", KeyID: "abc", Name: "Power BI", Datasets: []string{"crm_deal"},
			CreatedBy: "u1", AuthorName: "Борис", CreatedAt: now, LastUsedAt: &now, LastUsedIP: "10.0.0.1", Uses: 3},
			{ID: "k2", Name: "DataLens", CreatedAt: now, RevokedAt: &now}}, "Datasets": biNamedSets(bi.Datasets, func(k string) string { return k }), "Names": map[string]string{"crm_deal": "Сделки"},
			"NewToken": "go24bi_x", "BaseURL": "http://go24", "Periods": bi.Periods}},
		"biwidget": {Data: map[string]any{"D": bi.Dashboard{ID: "d1", Title: "Продажи"}, "W": biSamplePanels()[1].W,
			"V": biFormVals{Dim: "created_at", Grain: "month", Dim2: "stage", Measures: []bi.Measure{{Agg: "sum", Field: "amount"}, {}, {}},
				Filters: []biFilterVal{{Filter: bi.Filter{Field: "stage", Op: "in"}, Text: "a, b", Type: "string"}, {Filter: bi.Filter{Field: "closed_at", Op: "gte"}, Type: "date"}, {}}, Sort: "m_desc", Limit: 10},
			"DS": bi.DatasetOf("crm_deal"), "DimDate": true, "Dim2Date": false, "Datasets": bi.Datasets, "SetSections": bi.SetSections, "Panel": biSamplePanels()[2],
			"Kinds": bi.Kinds, "Widths": bi.Widths, "Grains": bi.Grains, "Aggs": bi.Aggregates, "Ops": biOps, "Period": "month"}},
		"bookingres": {Data: map[string]any{"Types": []booking.Type{{ID: "t1", Code: "room", Resources: 1}, {ID: "t2", Name: "Тренажёры"}},
			"Resources": []booking.Resource{{ID: "r1", TypeCode: "room", Name: "Переговорная", Description: "10 мест", Days: []int{1, 5}, From: 540, To: 1080, Slot: 30}},
			"Admin":     true, "Days": []int{1, 2, 3, 4, 5, 6, 7}, "Users": []tasks.User{{ID: "u1", Name: "Борис"}},
			"Edit": booking.Resource{ID: "r1", TypeID: "t1", Name: "Переговорная", UserID: "u1", Days: []int{1, 5}, From: 540, To: 1440, Slot: 30}}},
		"bookinglist": {Data: map[string]any{"F": booking.Filter{Status: "attention", Mine: "u1", Query: "Иван"}, "From": "2026-10-01",
			"To": "2026-10-31", "Statuses": booking.Statuses, "Limit": true,
			"All": []booking.Resource{{ID: "r1", Name: "Переговорная"}},
			"Rows": []bookingListRow{{B: booking.Booking{ID: "b1", ResourceName: "Переговорная", ExtraNames: []string{"Проектор"},
				Start: time.Date(2026, 10, 5, 10, 0, 0, 0, time.Local), End: time.Date(2026, 10, 5, 11, 0, 0, 0, time.Local),
				ClientName: "Иван", ClientPhone: "+7 900", Status: "confirmed", ContactID: "c1", ContactName: "Иван Петров",
				DealID: "d1", DealTitle: "Сделка", LinkName: "Запись", Services: []booking.Service{{Name: "Стрижка", Price: 1500, Currency: "RUB"}}},
				Late: true}, {B: booking.Booking{ID: "b2", ClientName: "Анна", Status: "cancelled", CreatedByName: "Борис"}}}}},
		"bookingstats": {Data: map[string]any{"From": "2026-10-01", "To": "2026-10-31", "Type": "t1", "Statuses": booking.Statuses, "Hours": hours,
			"Types": []booking.Type{{ID: "t1", Code: "room", Resources: 1}, {ID: "t2", Name: "Тренажёры"}},
			"Rows": []booking.Stat{{Resource: booking.Resource{ID: "r1", TypeID: "t1", Name: "Переговорная", UserName: "Анна"}, Work: 600,
				Busy: 150, Load: 25, Bookings: 2, Statuses: map[string]int{"came": 1, "no_show": 1}, NoShowShare: 50}},
			"Total": booking.Stat{Work: 600, Busy: 150, Load: 25, Bookings: 2, Statuses: map[string]int{"came": 1}, NoShowShare: -1}}},
		"kbspace": {Data: map[string]any{"Space": kb.Space{ID: "s1", Name: "Регламенты", CanEdit: true, CanManage: true},
			"Tree": []*kb.Page{{ID: "p1", SpaceID: "s1", Title: "Отпуск", Children: []*kb.Page{{ID: "p2", SpaceID: "s1", Title: "Больничный"}}}},
			"Page": kb.Page{ID: "p1", SpaceID: "s1", Title: "Отпуск", Body: "# Как\n**жирно**", Author: "Борис", Editor: "Анна", Created: now, Updated: now},
			"Old":  &kb.Page{Title: "Отпуск (было)", Body: "старое", Editor: "Борис", Updated: now}, "Version": "1",
			"Diff":     []kb.DiffLine{{Op: "=", Text: "# Отпуск"}, {Op: "-", Text: "старое"}, {Op: "+", Text: "**жирно**"}},
			"Targets":  []kb.Target{{SpaceID: "s1", SpaceName: "Регламенты"}, {SpaceID: "s1", PageID: "p2", Title: "Больничный", Depth: 1}},
			"Versions": []kb.Version{{ID: 1, Title: "Отпуск", Editor: "Борис", EditedAt: now}}, "Pub": "tok", "Base": "http://x"}},
		"kbpublic": {Data: map[string]any{"Public": kb.Public{Token: "tok", SpaceID: "s1", Name: "Регламенты", Description: "Для всех"},
			"Tree":   []*kb.Page{{ID: "p1", SpaceID: "s1", Title: "Отпуск", Children: []*kb.Page{{ID: "p2", SpaceID: "s1", Title: "Больничный"}}}},
			"Page":   kb.Page{ID: "p2", SpaceID: "s1", Title: "Больничный", Body: "## Как\n## Когда", Updated: now},
			"Crumbs": []*kb.Page{{ID: "p1", SpaceID: "s1", Title: "Отпуск"}}, "TOC": kb.Headings("## Как\n## Когда")}},
		"kbsettings": {Data: map[string]any{"Space": kb.Space{ID: "s1", Name: "Регламенты", CanEdit: true, CanManage: true, Restricted: true},
			"Rights": []kb.Right{{ID: "r1", Kind: "all", Access: "read"}, {ID: "r2", Kind: "department", WhomID: "d1", Whom: "Продажи", Access: "edit"},
				{ID: "r3", Kind: "user", WhomID: "u1", Whom: "Анна", Access: "full"}},
			"Users": []tasks.User{{ID: "u1", Name: "Анна"}}, "Departments": []drive.Department{{ID: "d1", Name: "Продажи"}}, "Accesses": kb.Accesses,
			"Links": []kb.PublicLink{{Token: "t1"}, {Token: "t2", PageID: "p1", Title: "Отпуск"}}, "Base": "http://x"}},
		"kbtrash":   {Data: map[string]any{"Items": []kb.TrashItem{{SpaceID: "s1", SpaceName: "Регламенты", PageID: "p1", Title: "Отпуск", DeletedBy: "Анна", DeletedAt: now, Pages: 1, PurgeAt: &now}, {SpaceID: "s2", SpaceName: "Старая", Title: "Старая", DeletedAt: now, Pages: 4}}}},
		"call":      {Data: map[string]any{"URL": "wss://lk", "Token": "t", "Name": "GO-1 Отчёт", "Back": "/t/GO-1", "Link": "https://x/call?task=GO-1", "Base": "http://x", "Guests": true, "Query": "task=GO-1"}},
		"callguest": {Data: map[string]any{"Token": "t1", "Title": "GO-1 Отчёт", "Password": true, "Expires": now, "Ask": true, "Wrong": true, "Name": "Гость"}},
		"settings":  {Data: map[string]any{"Sections": settingsSections}},
		"sections":  {Data: map[string]any{"Sections": portalSections, "Off": []string{"crm"}}},
		"sysstate": {Data: map[string]any{"Problems": []string{"Мало места"}, "R": sysReport{Version: "0.5.3", Go: "go1.25", CheckedAt: time.Now(),
			Nodes:    []sysinfo.Node{{Role: "web", Host: "a", Alive: true}, {Role: "worker", Host: "b"}},
			Database: &sysinfo.Database{Version: "17.2", Size: 1 << 30, Migration: "547_system_nodes.sql"},
			Services: []sysinfo.Check{{ID: "matrix", Target: "http://synapse:8008", OK: true}, {ID: "office", Error: "HTTP 503"}},
			Disks:    []sysinfo.Disk{{ID: "backup", Path: "/backups", Total: 100 << 30, Free: 1 << 30}}, BackupOn: true,
			Backup:   &backup.Run{Name: "go24-1", Status: "failed", Error: "pg_dump"}}}},
		"portal": {Data: map[string]any{"S": portal.Settings{Name: "Ромашка", HasLogo: true, DateFormat: "ymd", TimeFormat: "12", TZ: "Europe/Moscow"},
			"DateFormats": portal.DateFormatNames, "Samples": portalSamples(), "Zones": portalZones, "MaxLogo": 256}},
		"callsettings": {Data: map[string]any{"Recording": true, "Record": true, "Ready": false, "Services": []calls.ServiceConfig{
			{Service: "zoom", Enabled: true, AccountID: "a", ClientID: "c", HasSecret: true}, {Service: "telemost"}, {Service: "vk", GroupID: "1"}, {Service: "telegram", Enabled: true}, {Service: "max"}}}},
		"aisettings": {Data: map[string]any{"Chosen": "c1", "Using": "c1", "Providers": ai.Providers, "New": ai.Connection{Provider: "anthropic"},
			"Company": []ai.Connection{{ID: "c1", Name: "Общий", Provider: "openai", KeyTail: "1234", Default: true, CheckedAt: &now}},
			"Mine":    []ai.Connection{{ID: "c2", Owner: "u1", Name: "Свой", Provider: "compatible", BaseURL: "http://ollama:11434/v1", CheckError: "HTTP 401"}}}},
		"calllinks": {Data: map[string]any{"Rows": []map[string]any{{"Service": "zoom", "API": true, "Link": ""}, {"Service": "telegram", "API": false, "Link": "https://t.me/anna"}}}},
		"collabcreators": {Data: map[string]any{"Set": tasks.CollabCreators{Who: "some"}, "Users": []tasks.User{{ID: "u1", Name: "Анна"}},
			"Sel": []tasks.User{{ID: "dept:d1", Name: "Продажи"}, {ID: "u1"}}}},
		"phone": {TitleKey: "phone.title", Bare: true, Data: map[string]any{"ID": "0123456789abcdef", "Number": "101", "Login": "w101", "Password": "pw", "SIPHost": "192.168.1.15", "Dial": "102"}},
		"callbackpub": {Data: map[string]any{"Form": telephony.Callback{Token: "abc", Title: "Перезвоним", Text: "Оставьте номер"}, "Stamp": "x", "Embed": true, "Err": "Укажите номер", "Phone": "12"}},
		"phonestats": {Data: map[string]any{"Admin": true, "Users": []tasks.User{user}, "From": "2026-10-01", "To": "2026-10-26", "Line": "+74951234567", "UserID": "",
			"Trunks": []telephony.Trunk{{Name: "Основной", Number: "+74951234567"}}, "Top": 5, "Calls": "/telephony/calls", "Missed": "/telephony/calls?kind=missed",
			"Stats": telephony.CallStats{Total: telephony.StatRow{In: 5, Missed: 2, Out: 2, OutOK: 1, Seconds: 297, WaitSeconds: 3, Rated: 2, Ratings: 9},
				Days: []telephony.StatDay{{Day: now, In: 5, Missed: 2, Out: 2}, {Day: now}}},
			"Rows": []phoneStatRow{{StatRow: telephony.StatRow{Who: telephony.Who{UserID: "u1", Name: "Анна"}, In: 5, Missed: 2, Out: 2, OutOK: 1, Seconds: 297}, Calls: "/telephony/calls?user=u1"},
				{StatRow: telephony.StatRow{In: 1, Missed: 1}}}}},
		"phonecalls": {Data: map[string]any{"Speech": true, "Admin": true, "Users": []tasks.User{user}, "From": "2026-10-01", "To": "", "More": "/telephony/calls?before=x",
			"CRM": true, "Back": "/telephony/calls?kind=missed", "Filter": telephony.CallFilter{Kind: "missed", Number: "10"}, "Calls": []phoneCallRow{
				{Call: telephony.Call{ID: "c1", From: "101", To: "102", FromUser: telephony.Who{UserID: "u1", Name: "Анна"}, ToUser: telephony.Who{UserID: "u2", Name: "Борис"},
					Started: now, Answered: &now, Seconds: 75, Status: "answered", Record: "a.wav", Transcript: "Добрый день", Rating: 4}, Kind: "out", HasRecord: true, CanListen: true},
				{Call: telephony.Call{ID: "c2", From: "+79001234567", To: "101", ToUser: telephony.Who{UserID: "u1", Name: "Анна"}, Started: now, Status: "noanswer"}, Kind: "missed", Mine: true, Outside: "+79001234567"},
				{Call: telephony.Call{ID: "c4", From: "+79001234567", To: "101", ToUser: telephony.Who{UserID: "u1", Name: "Анна"}, Started: now, Status: "answered",
					CRMEntity: "contact", CRMID: "x1", CRMName: "ООО Ромашка"}, Kind: "in", Mine: true, Outside: "+79001234567", Blocked: true},
				{Call: telephony.Call{ID: "c3", From: "103", To: "104", Started: now, Status: "busy"}, Kind: "inner", HasRecord: true}}}},
		"telroles": {Data: map[string]any{"Rights": telephony.Rights, "Members": map[string][]tasks.User{"r2": {{ID: "dept:d1", Name: "Продажи"}}},
			"Roles": []telephony.Role{{ID: "r1", Code: "everyone", Default: true, Perms: telephony.Perms{"calls": "own", "records": "own", "outgoing": "all", "settings": "none"}},
				{ID: "r2", Name: "Операторы", Perms: telephony.Perms{"calls": "dept", "outgoing": "crm"}}}}},
		"telephony": {Data: map[string]any{"Speech": true, "Transcribe": true, "Enabled": true, "Record": true, "Lead": true, "CRM": true, "NewTrunk": telephony.Trunk{Register: true, OutAll: true},
			"Trunks": []telephony.Trunk{{ID: "0c6d1a2b-0000-4000-8000-000000000001", Name: "Основной", Host: "sip.example.ru:5060", Username: "u1", Number: "+74951234567",
				Register: true, RingMode: "queue", Ring: []string{"u3"}, Out: []string{"u3"}, Menu: "0c6d1a2b-0000-4000-8000-0000000000m1", RateOn: true, RateSum: "r1", Group: "0c6d1a2b-0000-4000-8000-0000000000g1"}},
			"Menus": []telephony.Menu{{ID: "0c6d1a2b-0000-4000-8000-0000000000m1", Name: "Главное", Wait: 5, Repeats: 2, Direct: true, GreetingSum: "abc",
				Items: []telephony.MenuItem{{Digit: "1", Act: "ring", Users: []string{"u3"}, Mode: "queue"}, {Digit: "2", Act: "forward", To: "+79005550011"}, {Digit: "3", Act: "group", Group: "0c6d1a2b-0000-4000-8000-0000000000g1"}},
				Timeout: telephony.MenuItem{Act: "voicemail"}}},
			"Callbacks": []telephony.Callback{{ID: "cb1", Token: "abc", Name: "Сайт", Trunk: "0c6d1a2b-0000-4000-8000-000000000001", Ring: []string{"u3"}, RingMode: "queue"}},
			"Requests": map[string][]telephony.CallbackRequest{"cb1": {{Number: "+79001234567", Name: "Пётр", Page: "https://example.com", Status: "later", Created: now}}},
			"NewCallback": telephony.Callback{RingMode: "all"}, "Root": "https://go24.example",
			"Groups": []telephony.Group{{ID: "0c6d1a2b-0000-4000-8000-0000000000g1", Name: "Продажи", Users: []string{"u3"}, Strategy: "evenly", Ring: 15, Wait: 90, MusicSum: "m1"}},
			"NewGroup": telephony.Group{Strategy: "all", Ring: 20, Wait: 120},
			"NewMenu": telephony.Menu{Wait: 5, Repeats: 2, Timeout: telephony.MenuItem{Act: "hangup"}}, "Version": "22.10.1", "SIPHost": "192.168.1.15", "Users": []tasks.User{{ID: "u3", Name: "Ева"}},
			"Blacklist": []telephony.Blocked{{ID: "b1", Number: "89001112233", Comment: "спам", ByName: "Анна", At: now}},
			"Online": map[string]bool{"101": true}, "Lines": []telephony.Line{{UserID: "u1", Name: "Анна", Number: "101"}, {UserID: "u2", Name: "Борис", Number: "101", Dup: true}}}},
		"disklimits": {Data: map[string]any{"Limits": drive.Limits{User: 5 << 30}, "User": "5", "Company": "", "Project": "", "Total": int64(3 << 30), "Rows": []map[string]any{{"D": drive.DriveUsage{Kind: "user", Name: "Анна", Used: 3 << 30}, "Usage": drive.Usage{Used: 3 << 30, Limit: 5 << 30}}, {"D": drive.DriveUsage{Kind: "project", Name: "go24", ProjectKey: "GO", Used: 1 << 20}, "Usage": drive.Usage{Used: 1 << 20}}}}},
		"boards": {Data: map[string]any{"Scope": "mine", "Q": "", "Scopes": []string{"", "mine", "shared"},
			"Boards": []drive.Document{{Recent: drive.Recent{File: drive.File{ID: "x1", Name: "Схема.board", Size: 2048, CreatedAt: now, FolderID: "f1", Where: "Идеи"}, Drive: "company"}, Base: "/disk/company", Changer: "Анна"}}}},
		"docs": {Data: map[string]any{"Scope": "", "Q": "", "Type": "", "Types": []string{"doc", "sheet", "slides"}, "Office": true, "Scopes": []string{"", "mine", "shared"}, "Me": "u1",
			"Docs": []drive.Document{{Recent: drive.Recent{File: drive.File{ID: "x1", Name: "план.docx", Size: 2048, CreatedAt: now, FolderID: "f1", Where: "Документы", Editors: "Иван"}, Drive: "project", ProjectKey: "GO", ProjectName: "go24"}, Base: "/disk/p/GO", Changer: "Анна"},
				{Recent: drive.Recent{File: drive.File{ID: "x2", Name: "Бюджет.xlsx", CreatedAt: now, Locker: "Иван", LockerID: "u2", LockedAt: &now}, Drive: "user"}, Base: "/disk/sh/r1", Owner: "Анна", Changer: "Иван"}}}},
		"diskcard": {Data: map[string]any{"Card": drive.Card{File: drive.File{ID: "x1", Name: "план.png", ContentType: "image/png", Size: 2048, Uploader: "Борис", CreatedAt: now, FolderID: "f1", Where: "Документы", Locker: "Иван", LockerID: "u2", LockedAt: &now},
			Drive: "company", Changer: "Анна", Comments: []drive.Comment{{ID: "c1", Body: "Готово", Author: "Иван", AuthorID: "u2", CreatedAt: now}, {ID: "c2", Body: "Спасибо", AuthorID: "u1", CreatedAt: now}}},
			"Type": "image", "At": "/disk/company?folder=f1", "Me": "u1", "Versions": []drive.Version{{No: 2, Size: 2048, Author: "Анна", CreatedAt: now}, {ID: "v1", No: 1, Size: 10, Author: "Борис", CreatedAt: now}}}},
		"office": {Data: map[string]any{"Config": `{"document":{"title":"план.docx"}}`, "Name": "план.docx", "Back": "/disk/my", "Edit": true, "Download": "/files/x1"}},
		"disklocal": {Data: map[string]any{"Name": "план.docx", "Back": "/disk/my", "Edit": false, "TTL": 24, "Link": "https://go24/dav-edit/t/план.docx",
			"Apps": []map[string]any{{"App": "msoffice", "Href": template.HTMLAttr(`href="ms-word:ofe|u|https://go24/dav-edit/t/x.docx"`)}, {"App": "libreoffice", "Href": template.HTMLAttr(`href="vnd.libreoffice.command:ofe|u|https://go24/dav-edit/t/x.docx"`)}}}},
		"diskboard": {Data: map[string]any{"Name": "план.board", "Back": "/disk/my", "Edit": true, "Scene": "/disk/my/board/x1?scene", "Download": "/files/x1", "About": "/files/x1/about", "Lang": "ru-RU"}},
		"disk": {Data: map[string]any{"Drive": drive.Drive{Kind: "project", OwnerID: "p1", ProjectKey: "GO", Name: "go24", CanWrite: true}, "Folder": "f1", "Me": "u1",
			"Trail": []drive.Folder{{ID: "f1", Name: "Документы"}}, "Folders": []drive.Folder{{ID: "f2", ParentID: "f1", Name: "Договоры"}},
			"Files":    []drive.File{{ID: "x1", Name: "план.pdf", Size: 2048, UploaderID: "u1", Uploader: "Борис", CreatedAt: now}},
			"Storages": []storages.Storage{{ID: "s1", Name: "NC"}}, "Projects": []tasks.Project{project},
			"Links": map[string]drive.Link{"x1": {Token: "t1", FileID: "x1", Password: true, ExpiresAt: &now, Downloads: 3}}, "Comments": map[string]int{"x1": 2}, "Base": "http://go24",
			"Types": []string{"doc", "image"}, "SortURL": map[string]string{"name": "?desc=1", "size": "?sort=size&desc=1", "date": "?sort=date&desc=1"}}},
		"share": {Data: map[string]any{"Link": drive.Shared{Link: drive.Link{Token: "t1", FolderID: "f1"}}, "Token": "t1", "Name": "Документы",
			"Folder": "f2", "Trail": []drive.Folder{{ID: "f1", Name: "Документы"}, {ID: "f2", Name: "Сканы"}}, "Folders": []drive.Folder{{ID: "f3", Name: "2026"}},
			"Files": []drive.File{{ID: "x1", Name: "скан.pdf", Size: 10}}}},
		"crm": {Data: func() map[string]any {
			st := []crm.Stage{{ID: "s1", Code: "new", Color: "39A8EF", Semantic: "process"}, {ID: "s2", Name: "Своя", Color: "7BD500", Semantic: "success"}}
			deal := crm.Deal{ID: "d1", Title: "Поставка", Amount: 150000, Currency: "RUB", Stage: st[0], Responsible: crm.Ref{Name: "Анна"}}
			p := crm.Pipeline{ID: "p1", Code: "default", Stages: st}
			return map[string]any{"Kind": "deals", "Q": "", "View": "", "Pipelines": []crm.Pipeline{p, {ID: "p2", Name: "Тендеры", Stages: st}},
				"Pipeline": p, "Items": []crm.Deal{deal}, "Columns": crmColumns(st, []crm.Deal{deal}, func(d crm.Deal) (string, float64) { return d.Stage.ID, d.Amount }),
				"Users": []tasks.User{user}, "Me": "u1", "Colors": tasks.StageColors, "ActCount": 2, "Stages": st, "F": crm.Filter{}, "Query": "",
				"Next": map[string]*crm.Activity{"d1": {ID: "a1", Entity: "deal", EntityID: "d1", Kind: "call", Subject: "Позвонить", Deadline: now}},
				"Sums": map[string]float64{"d1": 1000}, "Clients": []string{"Ромашка"}, "BuiltIn": crm.BuiltInPresets, "PresetKind": "deals", "Scope": "deal",
				"Presets": []crm.Preset{{ID: "p1", Name: "Крупные", Query: "min=1000", Default: true}}, "PresetQuery": "min=1000"}
		}()},
		"crmtypes": {Data: map[string]any{"Kind": "types", "Colors": tasks.StageColors, "ActCount": 0,
			"Types": []crm.Type{{ID: "t1", Name: "Договоры", Color: "FFA900", Items: 3}, {ID: "t2", Name: "Поставки", Color: "39A8EF"}},
			"Relations": map[string][]string{"t2": {"", "t1"}}}},
		"crmcurrencies": {Data: map[string]any{"Kind": "currencies", "ActCount": 0, "Base": "RUB", "CBR": crm.CBRURL, "Currencies": []crm.Currency{
			{Code: "RUB", Name: "Российский рубль", Rate: 1, Base: true, Auto: true, UpdatedAt: now},
			{Code: "USD", Rate: 92.12345, Auto: true, UpdatedAt: now}, {Code: "EUR", Name: "Евро", Rate: 100, UpdatedAt: now}}}},
		"crmrecycle": {Data: map[string]any{"Kind": "recycle", "ActCount": 0, "Days": 30, "Entity": "", "Q": "",
			"Items": []crm.RecycleItem{{ID: "r1", Entity: "deal", Title: "Поставка", DeletedBy: crm.Ref{Name: "Анна"}, DeletedAt: time.Now()},
				{ID: "r2", Entity: "deal", TypeName: "Договоры", DeletedAt: time.Now()}}}},
		"crmdups": {Data: map[string]any{"Kind": "duplicates", "ActCount": 0, "Entity": "contact", "By": "phone",
			"Criteria": crm.DupCriteria["contact"], "Merge": true, "Limit": 100, "Total": 1,
			"Groups": []crm.DupGroup{{Value: "+7 900 123-45-67", Cards: []crm.DupCard{{ID: "c1", Title: "Иван", Phone: "+7 900 123-45-67",
				Responsible: crm.Ref{Name: "Анна"}, CreatedAt: now}, {ID: "c2", Title: "Иван Петров", CreatedAt: now}}}}}},
		"crmforms": {Data: map[string]any{"Kind": "forms", "Mode": "edit", "ActCount": 0, "Root": "https://go24.test",
			"Form": crm.Form{ID: "f1", Token: "tk", Name: "Заявка", Scheme: "deal", Active: true, Results: 2},
			"Choice": []formChoice{{FormField: crm.FormField{Key: "name", Required: true}, On: true},
				{FormField: crm.FormField{Key: "f:x", Name: "Город", Kind: "string", Label: "Ваш город"}, Scheme: "deal", On: true},
				{FormField: crm.FormField{Key: "comment"}}},
			"Pipelines": []crm.Pipeline{{ID: "p1", Name: "Продажи"}}, "Users": []tasks.User{{ID: "u1", Name: "Анна"}}}},
		"crmformpub": {Data: map[string]any{"Form": crm.Form{Token: "tk", Name: "Заявка", Title: "Оставьте заявку", Text: "Перезвоним",
			Consent: "Согласен", Fields: []crm.FormField{{Key: "name", Label: "Имя", Required: true}, {Key: "comment", Label: "Комментарий"},
				{Key: "f:x", Label: "Гостей", Kind: "list", Options: []string{"1", "2"}}, {Key: "f:y", Label: "Нужна парковка", Kind: "bool"}}},
			"Values": map[string]string{"name": "Иван"}, "Err": "Проверьте e-mail", "Stamp": "1", "Embed": true, "Page": "https://site.ru/"}},
		"crmdicts": {Data: map[string]any{"Kind": "dicts", "ActCount": 0, "DictKinds": crm.DictKinds, "Dicts": map[string][]crm.DictItem{
			"source": {{Dict: crm.Dict{ID: "s1", Code: "CALL"}, Kind: "source", Used: 3}, {Dict: crm.Dict{ID: "s2", Name: "Партнёрская сеть"}, Kind: "source"}}}}},
		"crmcatalogsettings": {Data: map[string]any{"Kind": "products", "ActCount": 0, "Units": []unitView{{Unit: crm.Unit{ID: "u1", Code: "796", Default: true}, ShownName: "Штука", ShownSymbol: "шт."}},
			"Props": []crm.Prop{{ID: "p1", Name: "Цвет", Kind: crm.PropList, Options: []string{"красный"}, InList: true}}}},
		"crmproducts": {Data: map[string]any{"Kind": "products", "PType": "service", "Types": []crm.Type{{ID: "t1", Name: "Договоры", Color: "FFA900"}}, "Q": "", "ActCount": 0, "Currencies": []crm.Currency{{Code: "RUB", Base: true}}, "Items": []crm.Product{
			{ID: "p1", Name: "Лицензия", SKU: "L-1", Price: 1000, Currency: "RUB", VATRate: &vat20, VATIncluded: true, Active: true},
			{ID: "p2", Name: "Час работы", Kind: crm.KindService, Price: 2500, Currency: "RUB", Unit: "ч", VATRate: &vat7, SectionID: "s2", SectionPath: "Услуги / Работы", PhotoKey: "k", PurchasePrice: &vat7}},
			"Sections": []crm.Section{{ID: "s1", Name: "Услуги", Path: "Услуги"}, {ID: "s2", ParentID: "s1", Name: "Работы", Path: "Услуги / Работы", Depth: 1, Products: 1}},
			"Section":  &crm.Section{ID: "s1", Name: "Услуги", Path: "Услуги"}, "Crumbs": []crm.Section{{ID: "s1", Name: "Услуги"}},
			"New": &crm.Product{SectionID: "s1", VATIncluded: true}, "Stock": map[string]float64{"p1": 12.5}}},
		"stock": {Data: map[string]any{"Tab": "docs", "Rights": crm.AllStockRights(), "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses,
			"F": crm.StockFilter{Kind: "arrival"}, "Items": []crm.StockDoc{{ID: "sd1", Kind: "arrival", Number: 2, Title: "Поставка", Status: "conducted",
				Date: now, Supplier: crm.Ref{ID: "c1", Name: "ООО Ромашка"}, IncomingNumber: "17", Amount: 1200, Currency: "RUB", Responsible: crm.Ref{ID: "u1", Name: "Борис"}}}}},
		"stockdoc": {Data: map[string]any{"Tab": "docs", "Rights": crm.AllStockRights(), "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses, "ID": "sd1", "CanWrite": false,
			"Doc": crm.StockDoc{ID: "sd1", Kind: "moving", Number: 3, Status: "conducted", Date: now, Currency: "RUB", Responsible: crm.Ref{ID: "u1", Name: "Борис"},
				ConductedBy: crm.Ref{ID: "u1", Name: "Борис"}, ConductedAt: &now, Comment: "Перемещение\nв филиал",
				Rows: []crm.StockRow{{ProductID: "p1", Name: "Лицензия", Unit: "шт", Quantity: 2, From: "w1", FromName: "Основной", To: "w2", ToName: "Филиал"}, {Name: "Удалённый", Quantity: 1}}},
			"Warehouses": []crm.Warehouse{{ID: "w1", Name: "Основной", Default: true, Active: true}, {ID: "w2", Name: "Филиал", Active: true}},
			"Catalog":    []crm.Product{{ID: "p1", Name: "Лицензия", SectionPath: "Услуги"}}, "Users": []tasks.User{{ID: "u1", Name: "Борис"}},
			"Currencies": []crm.Currency{{Code: "RUB", Base: true}}}},
		"stocklist": {Data: map[string]any{"Tab": "stock", "Rights": crm.StockRights{}, "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses, "Q": "", "Section": "s1", "All": true,
			"Sections":   []crm.Section{{ID: "s1", Name: "Услуги", Path: "Услуги"}},
			"Warehouses": []crm.Warehouse{{ID: "w1", Name: "Основной", Default: true, Active: true}, {ID: "w2", Name: "Филиал", Active: true}},
			"Items": []crm.StockLine{{Product: crm.Product{ID: "p1", Name: "Лицензия", SKU: "L-1", SectionPath: "Услуги"}, On: map[string]float64{"w1": 3, "w2": 1.5}, Total: 4.5, Reserve: 5},
				{Product: crm.Product{ID: "p2", Name: "Час работы", Unit: "ч"}}}}},
		"stockreport": {Data: map[string]any{"Tab": "reports", "Rights": crm.AllStockRights(), "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses, "Report": "stock", "CSV": "/store/reports/stock?csv=1",
			"F": crm.StockReportFilter{Date: now}, "Sum": 1955.5, "Sections": []crm.Section{{ID: "s1", Name: "Услуги", Path: "Услуги"}},
			"Warehouses": []crm.Warehouse{{ID: "w1", Name: "Основной", Default: true, Active: true}, {ID: "w2", Name: "Филиал", Active: true}},
			"Items":      []crm.StockReportLine{{Product: crm.Product{ID: "p1", Name: "Лицензия", SKU: "L-1", SectionPath: "Услуги"}, On: map[string]float64{"w1": 15, "w2": 2}, Quantity: 17, Cost: 115, Sum: 1955.5}}}},
		"stocksettings": {Data: map[string]any{"Tab": "settings", "Rights": crm.AllStockRights(), "Admin": true, "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses,
			"Set": crm.StockSettings{AutoReserve: true, ReserveDays: 7, ShipOnWin: true}}},
		"stockwarehouses": {Data: map[string]any{"Tab": "warehouses", "Rights": crm.AllStockRights(), "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses, "Edit": "",
			"Users": []tasks.User{{ID: "u1", Name: "Борис"}}, "Items": []crm.Warehouse{{ID: "w1", Name: "Основной", Address: "Москва", Default: true, Active: true,
				Responsible: crm.Ref{ID: "u1", Name: "Борис"}, Products: 2, Value: 1500}, {ID: "w2", Name: "Старый"}}}},
		"stockroles": {Data: map[string]any{"Tab": "rights", "Rights": crm.AllStockRights(), "Admin": true, "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses,
			"Actions": crm.StockActions, "Users": []tasks.User{{ID: "u1", Name: "Борис"}}, "Warehouses": []crm.Warehouse{{ID: "w1", Name: "Основной"}, {ID: "w2", Name: "Север"}},
			"Roles": []crm.StockRole{{ID: "r1", Code: "everyone", Default: true, Perms: crm.StockPerms{Docs: map[string][]string{"arrival": {"read"}}}},
				{ID: "r2", Name: "Север", Perms: crm.StockPerms{Docs: map[string][]string{"arrival": {"read", "write"}}, Warehouses: []string{"w2"}, PurchasePrice: true},
					Members: []crm.Ref{{ID: "u1", Name: "Борис"}}}}}},
		"crminvoices": {Data: map[string]any{"Kind": "invoices", "DocKind": "invoice", "Status": "paid", "Statuses": crm.InvoiceStatuses, "ActCount": 0,
			"Items": []crm.Invoice{{ID: "i1", Kind: "invoice", Number: 3, Title: "Поставка", Status: "paid", Amount: 1500, Currency: "RUB", DealID: "d1", CardTitle: "Поставка",
				Company: crm.Ref{ID: "c1", Name: "Ромашка"}, IssueDate: now, DueDate: &now}}}},
		"crminvoice": {Data: map[string]any{"Item": crm.Invoice{ID: "i1", Kind: "quote", Number: 2, Title: "КП", Status: "draft", Amount: 1500, Currency: "RUB", DealID: "d1",
			CardTitle: "Поставка", Contact: crm.Ref{ID: "x1", Name: "Иван"}, IssueDate: now, Responsible: crm.Ref{ID: "u1", Name: "Анна"}},
			"Statuses": crm.InvoiceStatuses["quote"], "Me": "u1", "Users": []tasks.User{user}, "CanWrite": true, "Seller": "ООО Го24\nИНН 1",
			"Docs": []crm.DocTemplate{{ID: "t1", Name: "Счёт с печатью"}}, "PDF": true,
			"Documents": []crm.Document{{ID: "c1", TemplateID: "t1", TemplateName: "Счёт с печатью", Number: "5", Author: "Анна", CreatedAt: now, UpdatedAt: now}},
			"Signs": map[string]sign.Doc{"c1": {ID: "s1", Status: "signing"}},
			"Rows": []crm.ProductRow{{Name: "Лицензия", Price: 1500, Quantity: 1, VATRate: &vat20, VATIncluded: true}}, "RowsTotal": 1500.0, "RowsVAT": 250.0,
			"Catalog": []crm.Product{}}},
		"crmanalytics": {Data: func() map[string]any {
			st := crm.Stage{ID: "s1", Code: "new", Color: "39A8EF", Semantic: "process"}
			rep := crm.Report{From: now, To: now, Won: 2, Lost: 1, WonSum: 3000, LostSum: 500, CycleDays: 4.5,
				Funnel:   []crm.StageCount{{Stage: st, Count: 3, Sum: 4500}, {Stage: crm.Stage{ID: "s2", Name: "Своя", Color: "7BD500"}}},
				Managers: []crm.Manager{{Ref: crm.Ref{ID: "u1", Name: "Анна"}, InWork: 1, InWorkSum: 100, Won: 2, WonSum: 3000, Lost: 1, OverdueActivities: 2}},
				Leads:    []crm.StageCount{{Stage: crm.Stage{ID: "l1", Code: "lead_new", Color: "39A8EF"}, Count: 5}}, LeadsNew: 5, Converted: 1,
				Sources: []crm.Source{{Dict: crm.Dict{ID: "s1", Code: "WEB"}, Count: 4, Converted: 1}, {Dict: crm.Dict{Name: "Выставка"}, Count: 2}, {Count: 1}}}
			p := crm.Pipeline{ID: "p1", Code: "default"}
			return map[string]any{"Kind": "analytics", "Report": rep, "Pipelines": []crm.Pipeline{p, {ID: "p2", Name: "Опт"}}, "Pipeline": p, "From": &now, "To": &now, "ActCount": 0}
		}()},
		"crmplan": {Data: func() map[string]any {
			row := func(id, name string, target float64) crm.PlanRow {
				return crm.PlanRow{Ref: crm.Ref{ID: id, Name: name}, Target: target, Won: 2, WonSum: 150000, InWork: 1, InWorkSum: 50000, Metric: "sum"}
			}
			plan := crm.SalesPlan{ID: "pl1", PipelineID: "p1", Period: "quarter", Metric: "sum", Start: now, End: now, Currency: "RUB",
				Company: row("", "Компания", 300000), Departments: []crm.PlanRow{row("d1", "Продажи", 100000)},
				Users: []crm.PlanRow{row("u1", "Анна", 120000), row("u2", "Пётр", 0)}, UpdatedBy: crm.Ref{ID: "u1", Name: "Анна"}, UpdatedAt: now}
			p := crm.Pipeline{ID: "p1", Code: "default"}
			return map[string]any{"Kind": "plan", "Plan": plan, "Pipelines": []crm.Pipeline{p, {ID: "p2", Name: "Опт"}}, "Pipeline": p,
				"Periods": crm.PlanPeriods, "Label": "IV квартал 2026", "Prev": "2026-07-01", "Next": "2027-01-01", "Start": "2026-10-01",
				"Query": template.URL("pipeline=p1&period=quarter"), "Admin": true, "ActCount": 0}
		}()},
		"crmactivities": {Data: map[string]any{"Kind": "activities", "Filter": "today", "ActCount": 1, "Items": []crm.Activity{
			{ID: "a1", Entity: "deal", EntityID: "d1", Kind: "meeting", Subject: "Встреча", Deadline: now, Responsible: crm.Ref{Name: "Анна"}, CardTitle: "Поставка"}}}},
		"crmcard": {Data: map[string]any{"Kind": "leads", "Entity": "lead", "Me": "u1", "AppTabs": []apps.Placement{{ID: "p2", Placement: "CRM_LEAD_DETAIL_TAB", Handler: "https://app/l", Title: "Карта"}}, "Edit": false, "Users": []tasks.User{user},
			"Path": "/crm/leads/l1", "Tasks": []tasks.Task{task}, "CanWrite": true, "Chat": "c0ffee00-0000-0000-0000-000000000001",
			"Signs": map[string]sign.Doc{"x1": {ID: "s1", Status: "signing"}},
			"Documents": []crm.Document{{ID: "x1", TemplateID: "t1", TemplateName: "Договор", Number: "7", Author: "Иван", CreatedAt: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC)},
				{ID: "x2", TemplateName: "Акт", FileName: "Акт.docx", PublicToken: "p1"}}, "PDF": true, "Base": "http://go24",
			"MailForm": map[string]any{"Boxes": []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}}, "To": []crm.Addressee{{Email: "ivan@y.ru", Name: "Иван"}},
				"Templates": []crm.MailTemplate{{ID: "m1", Name: "КП", Entity: "lead"}}}, "CanDelete": true, "Docs": []crm.DocTemplate{{ID: "t1", Name: "Анкета", Entity: "lead"}},
			"Item": crm.Lead{ID: "l1", Title: "Заявка", Name: "Иван", Phone: "+7", Amount: 10, Currency: "RUB", Stage: crm.Stage{ID: "s1", Code: "lead_new"}, Responsible: crm.Ref{Name: "Анна"}, CreatedAt: now,
				Origin: crm.Dict{ID: "s1", Code: "CALL"}, Source: "Звонил в пятницу", Repeat: true, ContactID: "x1",
				Comms: []crm.Comm{{Kind: "phone", Type: "work", Value: "+7"}, {Kind: "phone", Type: "mobile", Value: "+79001112233"}}},
			"Dicts": map[string][]crm.DictItem{"source": {{Dict: crm.Dict{ID: "s1", Code: "CALL"}}, {Dict: crm.Dict{ID: "s2", Name: "Партнёры"}}},
				"deal_type": {{Dict: crm.Dict{ID: "t1", Code: "SALE"}}}, "industry": {{Dict: crm.Dict{ID: "i1", Code: "IT"}}}},
			"Stages":     []crm.Stage{{ID: "s1", Code: "lead_new", Color: "39A8EF"}, {ID: "s2", Name: "Своя", Color: "FF5752"}},
			"Activities": []crm.Activity{{ID: "a1", Entity: "lead", EntityID: "l1", Kind: "call", Subject: "Позвонить", Description: "по заявке", Deadline: now, Responsible: crm.Ref{ID: "u1", Name: "Анна"}}},
			"ActKinds":   crm.ActivityKinds, "ActDeadline": "2026-10-20T15:00",
			"Duplicates": []crm.Duplicate{{Entity: "lead", ID: "l2", Title: "Дубль", Phone: "+7 900", By: "phone"}},
			"Invoices":   []crm.Invoice{{ID: "i1", Kind: "invoice", Number: 3, Title: "Поставка", Status: "new", Amount: 1500, Currency: "RUB", IssueDate: now}},
			"Fields": []tasks.FieldValue{{Field: tasks.Field{ID: "f1", Name: "Источник", Kind: "list", Options: []string{"Сайт", "Звонок"}}, Value: "Сайт"},
				{Field: tasks.Field{ID: "f2", Name: "VIP", Kind: "bool"}, Value: "1"}, {Field: tasks.Field{ID: "f3", Name: "Дата", Kind: "date"}, Value: "2026-10-20"}},
			"Rows":      []crm.ProductRow{{ProductID: "p1", Name: "Лицензия", Price: 1000, Quantity: 2, Discount: 10, VATRate: &vat20, VATIncluded: true}, {Name: "Внедрение", Price: 500, Quantity: 1, Unit: "ч"}},
			"RowsTotal": 2300.0, "RowsVAT": 300.0, "Catalog": []crm.Product{{ID: "p1", Name: "Лицензия", Price: 1000, Currency: "RUB", VATRate: &vat20, VATIncluded: true}},
			"Timeline": []crm.Event{{ID: 7, AuthorName: "Анна", Kind: "activity", Body: "call:Позвонить", At: now}, {ID: 8, AuthorName: "Анна", Kind: "invoice", Body: "invoice:3:paid", At: now}, {ID: 4, AuthorName: "Анна", Kind: "document", Body: "Анкета", At: now}, {ID: 12, AuthorName: "Анна", Kind: "openline", Body: "c0ffee00-0000-0000-0000-000000000001", At: now}, {ID: 13, AuthorName: "Анна", Kind: "call", Body: `{"id":"c1","dir":"missed","number":"+79001234567","status":"noanswer"}`, At: now}, {ID: 14, AuthorName: "Анна", Kind: "call", Body: `{"id":"c2","dir":"out","number":"+79001234567","sec":75,"status":"answered","rec":true}`, At: now}, {ID: 3, AuthorName: "Анна", Kind: "robot", Body: "task:GO-5", At: now}, {ID: 9, AuthorName: "Анна", Kind: "robot", Body: "fail:mail:err.robot_email", At: now}, {ID: 10, AuthorName: "Анна", Kind: "robot", Body: "activity:call:Позвонить", At: now}, {ID: 11, AuthorName: "Анна", Kind: "robot", Body: "trigger:mail", At: now}, {ID: 15, AuthorName: "Анна", Kind: "robot", Body: "webhook:n8n.test", At: now}, {ID: 17, AuthorName: "Анна", Kind: "robot", Body: "document:Договор № 3", At: now}, {ID: 18, AuthorName: "Анна", Kind: "robot", Body: "close:2", At: now}, {ID: 16, AuthorName: "Анна", Kind: "repeat", Body: "contact:x1", At: now}, {ID: 2, AuthorName: "Анна", Kind: "stage", Body: "code:lead_new → Своя", At: now}, {ID: 1, AuthorName: "Анна", Kind: "created", At: now},
				{ID: 12, AuthorName: "Анна", Kind: "tunnel_to", Body: "d2|Поставка", At: now}, {ID: 13, AuthorName: "Анна", Kind: "pipeline", Body: "code:default → Тендеры", At: now}},
			"OtherPipes": []crm.Pipeline{{ID: "p2", Name: "Тендеры", Stages: []crm.Stage{{ID: "s3", Name: "Заявка"}, {ID: "s4", Code: "won"}}}}}},
		"robots": {Data: map[string]any{"Entity": "deal", "Pipeline": "p1", "Back": "/crm/robots", "Kinds": crm.RobotKinds, "ShareModes": crm.ShareModes,
			"Pipelines": []crm.Pipeline{{ID: "p1", Code: "default"}, {ID: "p2", Name: "Тендеры", Stages: []crm.Stage{{ID: "s3", Name: "Заявка"}}}},
			"Stages":    []crm.Stage{{ID: "s1", Code: "new", Color: "39A8EF"}, {ID: "s2", Name: "Своя", Color: "7BD500"}},
			"Robots": map[string][]crm.Robot{"s1": {{ID: "r1", StageID: "s1", Kind: "task", Active: true, Params: crm.RobotParams{Text: "Позвонить {title}", Project: "GO", To: "u1", Days: 2}},
				{ID: "r2", StageID: "s1", Kind: "notify", Active: true, Params: crm.RobotParams{Text: "Новая сделка", To: "responsible", Delay: 2, Unit: "hour", Min: 1000}},
				{ID: "r3", StageID: "s1", Kind: "activity", Active: true, Params: crm.RobotParams{Text: "Позвонить", Act: "call", To: "responsible"}},
				{ID: "r4", StageID: "s1", Kind: "mail", Active: true, Params: crm.RobotParams{Text: "Предложение"}},
				{ID: "r5", StageID: "s1", Kind: "responsible", Active: true, Params: crm.RobotParams{To: "u1"}},
				{ID: "r5b", StageID: "s1", Kind: "responsible", Active: true, Params: crm.RobotParams{To: "u1", Users: []string{"u1", "u2"}, Mode: "even", Skip: true}},
				{ID: "r6", StageID: "s1", Kind: "stage", Active: true, Params: crm.RobotParams{Stage: "s2", Max: 5000}},
				{ID: "r7", StageID: "s1", Kind: "copy", Active: true, Params: crm.RobotParams{Stage: "s3"}},
				{ID: "r8", StageID: "s1", Kind: "fields", Active: true, Params: crm.RobotParams{Set: []crm.RobotSet{{Field: "title", Value: "{title}!"}, {Field: "origin", Value: "src1"}, {Field: "uf:f1", Value: "1"}},
					If: &crm.RobotCond{Field: "responsible", Op: "eq", Value: "u1"}}},
				{ID: "r9", StageID: "s1", Kind: "webhook", Active: true, Params: crm.RobotParams{URL: "https://n8n.test/webhook/x", If: &crm.RobotCond{Field: "comment", Op: "empty"}}},
				{ID: "r10", StageID: "s1", Kind: "document", Active: true, Params: crm.RobotParams{Doc: "dt1"}},
				{ID: "r11", StageID: "s1", Kind: "bp", Active: true, Params: crm.RobotParams{BP: "bp1"}},
				{ID: "r12", StageID: "s1", Kind: "observers", Active: true, Params: crm.RobotParams{Obs: "set", Users: []string{"u1"}}},
				{ID: "r13", StageID: "s1", Kind: "close", Active: true, Params: crm.RobotParams{Act: "call"}}}},
			"DocTemplates": []crm.DocTemplate{{ID: "dt1", Name: "Договор"}}, "Processes": []bp.Template{{ID: "bp1", Name: "Согласование"}},
			"ObserverModes": crm.ObserverModes,
			"Fields": []robotField{{Key: "title"}, {Key: "origin", Pick: "source"}, {Key: "responsible", Pick: "users"}, {Key: "uf:f1", Name: "VIP", Pick: "bool"}, {Key: "uf:f2", Name: "Этап", Pick: "list", Options: []string{"КП"}}},
			"Dicts":  map[string][]crm.DictItem{"source": {{Dict: crm.Dict{ID: "src1", Code: "WEB"}}}}, "CondOps": crm.RobotCondOps, "SetRows": []int{0, 1, 2},
			"Tunnels": []crmTunnel{{Robot: crm.Robot{ID: "r7", Kind: "copy"}, FromPipe: crm.Pipeline{ID: "p1", Code: "default"}, From: crm.Stage{ID: "s1", Code: "new"},
				ToPipe: crm.Pipeline{ID: "p2", Name: "Тендеры"}, To: crm.Stage{ID: "s3", Name: "Заявка"}}}, "Acts": crm.ActivityKinds, "TriggerKinds": crm.TriggerKinds, "Triggers": map[string][]crm.Trigger{"s2": {{ID: "t1", StageID: "s2", Kind: "invoice_paid"}}},
			"Projects": []tasks.Project{project}, "Users": []tasks.User{user}}},
		"crmmailtpl": {Data: map[string]any{"Fields": docFields, "Me": "u1", "Admin": true, "Entities": []string{"deal", "lead", "contact", "company"},
			"Templates": []crm.MailTemplate{{ID: "m1", Name: "КП", Entity: "deal", Subject: "Предложение {Title}", Shared: true, CreatedBy: "u1", AuthorName: "Анна"}},
			"Edit":      &crm.MailTemplate{ID: "m1", Name: "КП", Entity: "deal", Subject: "Предложение {Title}", Body: "<p>Сумма {Amount}</p>"}}},

		"marketing": {Data: map[string]any{"Tab": "campaigns", "Campaigns": []marketing.Campaign{mktDraft, mktSent}}},
		"mktcampaign": {Title: "Акция", Data: map[string]any{"Tab": "campaigns", "C": mktDraft, "Segments": []marketing.Segment{mktSeg},
			"Mine": true, "In": map[string]bool{"g1": true}, "Out": map[string]bool{}, "Me": auth.Session{UserID: "u1", Name: "Анна"},
			"Boxes": []mail.Mailbox{{ID: "b1", Email: "anna@x"}}, "Letter": template.HTML("<p>Привет</p>"), "Count": 12}},
		"mktcalls": {Title: "Выставка", Data: map[string]any{"Tab": "calls", "C": marketing.CallList{ID: "k1", Title: "Выставка", Status: "draft", SegmentID: "g1",
			Assign: "staff", Staff: []string{"u1"}, Deadline: &now, Script: "Здравствуйте"}, "Mine": true, "Staff": map[string]bool{"u1": true},
			"Segments": []marketing.Segment{mktSeg}, "Users": []tasks.User{{ID: "u1", Name: "Анна"}}, "Count": 3}},
		"mktunsub": {Data: map[string]any{"Token": "t1", "Email": "ivan@x", "Campaign": "Акция"}},

		"pubdoc": {Title: "Договор № 7", Data: map[string]any{"Doc": crm.Document{PublicToken: "p1", Values: map[string]string{"DocumentDate": "25.10.2026"}},
			"Title": "Договор № 7", "PDF": true}},
		"crmdocvalues": {Title: "Договор", Data: map[string]any{"Kind": "deals", "ID": "d1", "CardTitle": "Сделка", "Back": "/crm/deals/d1",
			"Template": crm.DocTemplate{ID: "t1", Name: "Договор"}, "Doc": crm.Document{ID: "x1", TemplateID: "t1", Number: "Д-2026/7"},
			"Fields": []docField{{"Title", "Сделка"}, {"Срок поставки", strings.Repeat("а", 90)}}}},
		"documents": {Data: map[string]any{"Fields": docFields, "Templates": []crm.DocTemplate{{ID: "t1", Name: "Договор", Entity: "deal", FileID: "f1", FileName: "dog.docx"}},
			"Access": map[string][]tasks.User{"t1": {{ID: "dept:d1", Name: "Продажи"}}}, "Builtins": docBuiltins,
			"Images": map[string]string{"Stamp": "f2"}, "ImageKinds": crm.DocImageKinds}},
		"mail": {Data: map[string]any{"FolderID": "f1", "Q": "", "New": false, "Pane": "right", "Panes": mailPanes, "Unseen": 2,
			"Favs": []mailFav{{Ref: "f1", Folder: &mail.Folder{ID: "f1", MailboxID: "b1", Name: "INBOX", Role: "inbox", Selectable: true, Unseen: 2}, Box: "anna@x.ru"},
				{Ref: "f5", Folder: &mail.Folder{ID: "f5", MailboxID: "b1", Name: "Drafts", Role: "drafts", Selectable: true, Total: 3}},
				{Ref: "cat:c1", Cat: &mail.Category{ID: "c1", Name: "Важное", Color: "red"}}, {Ref: "search:s1", Search: &mail.Search{ID: "s1", Name: "Иван", Q: "from:ivan"}},
				{Ref: "all", Virtual: true}, {Ref: "flagged", Virtual: true}},
			"FavSet": map[string]bool{"f1": true, "f5": true, "cat:c1": true, "search:s1": true, "all": true, "flagged": true},
			"Folder": mail.Folder{ID: "f1", MailboxID: "b1", Name: "INBOX", Role: "inbox", Selectable: true, Unseen: 2},
			"Boxes": []mailBox{{Mailbox: mail.Mailbox{ID: "b1", Email: "anna@x.ru", SyncedAt: &now, Error: "err.mail_login", Unseen: 2}, Folders: []mail.Folder{
				{ID: "f1", MailboxID: "b1", Name: "INBOX", Delim: "/", Role: "inbox", Selectable: true, Unseen: 2, Total: 5},
				{ID: "f2", MailboxID: "b1", Name: "Проекты", Delim: "/", Selectable: true},
				{ID: "f3", MailboxID: "b1", Name: "Проекты/2026", Delim: "/", Selectable: true},
				{ID: "f4", MailboxID: "b1", Name: "Группа", Delim: "/"},
				{ID: "f5", MailboxID: "b1", Name: "Drafts", Delim: "/", Role: "drafts", Selectable: true, Total: 3}}},
				{Mailbox: mail.Mailbox{ID: "b2", Email: "info@x.ru"}}},
			"Box": &mail.Mailbox{ID: "b1", Email: "anna@x.ru"},
			"Messages": []mail.Message{{ID: "m1", MailboxID: "b1", FolderID: "f1", FolderRole: "inbox", FromName: "Клиент", FromAddr: "c@x.ru", Subject: "Заказ", Body: "Нужно 10", SentAt: now,
				Count: 2, Unread: 1, IDs: []string{"m0", "m1"}, Pinned: true, Flagged: true, FlagDue: &now, Categories: []string{"c1", "gone"}},
				{ID: "m2", MailboxID: "b1", FolderID: "f1", FromAddr: "d@x.ru", Seen: true, SentAt: now, FlagDone: true, SnoozeUntil: &later, IDs: []string{"m2"}, Importance: -1, Answered: "fwd", SMIMESign: "ok"},
				{ID: "m3", MailboxID: "b1", FolderID: "f1", FolderRole: "drafts", To: "x@y.ru", Seen: true, SentAt: now, IDs: []string{"m3"}, Draft: true,
					SendAt: &later, SendError: "err.mail_to", Importance: 1}},
			"Docs": map[string]string{"m1": mailDoc("<p>Нужно <b>10</b></p>")}, "Outside": true,
			"Categories": mailCats, "CatByID": catMap(mailCats), "Colors": mail.Colors, "Conv": true, "ConvOn": true, "Back": "/mail?folder=f1",
			"SnoozePresets": snoozePresets, "FlagPresets": flagPresets, "RemindPresets": remindPresets, "ActIDs": []string{"m0", "m1"},
			"Files":  map[string][]mail.File{"m1": {{ID: "f1", Name: "счёт.pdf", Size: 20480}}},
			"MoveTo": []mail.Folder{{ID: "f2", Name: "Проекты", Delim: "/", Selectable: true}},
			"Thread": []mail.Message{{ID: "m0", FolderID: "f3", FolderRole: "sent", FolderName: "Sent", FromAddr: "anna@x.ru", To: "c@x.ru", Body: "Предлагаем", SentAt: now, Seen: true},
				{ID: "m1", MailboxID: "b1", MailboxEmail: "anna@x.ru", FolderID: "f1", FolderRole: "inbox", FromName: "Клиент", FromAddr: "c@x.ru", CC: "e@x.ru", BCC: "z@x.ru", Subject: "Заказ", Body: "Нужно\n10", SentAt: now}},
			"Letter": mail.Message{ID: "m1", MailboxID: "b1", MailboxEmail: "anna@x.ru", FolderID: "f1", FolderRole: "inbox", FromName: "Клиент", FromAddr: "c@x.ru", Subject: "Заказ", Body: "Нужно\n10", SentAt: now,
				Seen: true, Pinned: true, Flagged: true, FlagDue: &now, SnoozeUntil: &later, Categories: []string{"c1"}, Importance: 1, ReceiptTo: "c@x.ru", Answered: "reply", AnsweredAt: &now,
				SMIMESign: "untrusted", SMIMESigner: "Клиент <c@x.ru>", SMIMEEnc: "yes"}}},
		"crmroles": {Data: map[string]any{"Levels": crm.Levels, "Actions": crm.Actions, "Entities": []string{"deal", "lead", "contact", "company"}, "Users": []tasks.User{user},
			"Roles": []crm.Role{{ID: "r1", Code: "everyone", Default: true, Perms: crm.Perms{"deal": {"read": "all", "write": "own", "delete": "none"}}},
				{ID: "r2", Name: "Менеджер", Perms: crm.Perms{}, Members: []crm.Ref{{ID: "u1", Name: "Анна"}}}}}},
		"taskroles": {Data: map[string]any{"Perms": tasks.PermLevels, "Users": []tasks.User{user},
			"Roles": []tasks.AccessRole{{ID: "r1", Code: "everyone", Default: true, Perms: tasks.AccessPerms{"view": "sub", "assign": "all"}},
				{ID: "r2", Name: "Отдел продаж", Perms: tasks.AccessPerms{}, Members: []tasks.User{user}}}}},
		"bp": {Data: map[string]any{"Tab": "approvals", "Templates": []bp.Template{{ID: "t1", Name: "Отпуск"}},
			"Approvals": []bp.Approval{{ID: "a1", InstanceID: "i1", Name: "Отпуск", Text: "Отпуск Анны", StartedBy: "Анна", CreatedAt: now}},
			"Instances": []bp.Instance{{ID: "i1", Name: "Отпуск", Status: "running", CreatedAt: now}}}},
		"bpstart": {Data: map[string]any{"EntityID": "", "T": bp.Template{ID: "t1", Name: "Отпуск", Description: "Заявка",
			Fields: []bp.Field{{Key: "from", Label: "С", Kind: "date", Required: true}, {Key: "why", Label: "Зачем", Kind: "text"}, {Key: "n", Label: "Дней", Kind: "number"}, {Key: "s", Label: "Куда", Kind: "string"}}}}},
		"bpinstance": {Data: map[string]any{"Card": "/crm/deals/d1", "Fields": []bp.Field{{Key: "from", Label: "С"}},
			"X": bp.Instance{ID: "i1", Name: "Отпуск", Status: "running", Step: 1, Values: map[string]string{"from": "2026-11-02"}, CreatedAt: now,
				Steps: []bp.Step{{Kind: "approve", Text: "Утвердить"}, {Kind: "wait", Days: 2}, {Kind: "task", Text: "Оформить"}}},
			"Mine":      &bp.Approval{ID: "a1", Text: "Утвердить?"},
			"Approvals": []bp.Approval{{User: company.Person{Name: "Борис"}, Decision: "approve", Comment: "ок"}, {User: company.Person{Name: "Анна"}}},
			"Log":       []bp.Line{{Kind: "started", Actor: "Анна", At: now}, {Kind: "rejected", Text: "нет", At: now}}}},
		"bplists": {Data: map[string]any{"Lists": []bp.List{{ID: "l1", Name: "Заявки", Description: "На оплату", Count: 3}}}},
		"bplist": {Data: map[string]any{"L": bp.List{ID: "l1", Name: "Заявки", Fields: []bp.Field{{Key: "sum", Label: "Сумма", Kind: "number"}, {Key: "who", Label: "Кто", Kind: "user"}, {Key: "ok", Label: "Ок", Kind: "bool"}}, Columns: []string{"sum", "who", "ok"}},
			"Elements": []bp.Element{{ID: "e1", Name: "Командировка", Values: map[string]string{"sum": "1500", "who": "u1", "ok": "Y"}, CreatedAt: now, CreatedBy: bp.Ref{ID: "u1", Name: "Анна"}}},
			"Users": []tasks.User{user}, "Q": "ком", "Sort": "sum", "Desc": true, "Page": 1, "Next": 2, "CanWrite": true}},
		"bplistedit": {Data: map[string]any{"L": bp.List{ID: "l1", Name: "Заявки", Fields: []bp.Field{{Key: "sum", Label: "Сумма", Kind: "number"}}, Columns: []string{"sum"}},
			"FieldRows": bpFieldRows([]bp.Field{{Key: "sum", Label: "Сумма", Kind: "number"}}, nil), "FieldKinds": bp.FieldKinds,
			"Readers": []tasks.User{{ID: "dept:d1", Name: "Продажи"}}, "Templates": []bp.Template{{ID: "t1", Name: "Согласование", Active: true, AutoCreate: true}}}},
		"bplistel": {Data: map[string]any{"L": bp.List{ID: "l1", Name: "Заявки", Fields: []bp.Field{{Key: "sum", Label: "Сумма", Kind: "number"}, {Key: "ok", Label: "Ок", Kind: "bool"}}},
			"E": bp.Element{ID: "e1", Name: "Командировка", Values: map[string]string{"sum": "1500"}, CreatedAt: now, ChangedAt: now.Add(time.Hour)},
			"Fields": []bp.Field{{Key: "sum", Label: "Сумма", Kind: "number", Default: "1500"}, {Key: "ok", Label: "Ок", Kind: "bool"}}, "Users": []tasks.User{user}, "CanWrite": true,
			"Log": []bpListLine{{ListLine: bp.ListLine{Author: bp.Ref{Name: "Анна"}, Changes: []bp.Change{{Key: "sum", Old: "1000", New: "1500"}}, At: now},
				Fields: []bp.Field{{Key: "sum", Label: "Сумма", Kind: "number"}}}, {ListLine: bp.ListLine{Created: true, At: now}}},
			"BPInstances": []bp.Instance{{ID: "i1", Name: "Согласование", Status: "running", CreatedAt: now}}, "BPTemplates": []bp.Template{{ID: "t1", Name: "Согласование"}}}},
		"bptemplates": {Data: map[string]any{"Templates": []bp.Template{{ID: "t1", Name: "Отпуск", Entity: "none", Active: true}, {ID: "t2", Name: "Дожать", Entity: "deal"}}}},
		"bptemplate": {Data: map[string]any{"FieldsText": "from | С | date | *", "Kinds": bp.StepKinds, "Users": []tasks.User{user}, "Projects": []tasks.Project{project},
			"Stages": []crm.Stage{{ID: "s1", Code: "new"}},
			"CardFields": bp.CardFields, "OwnFields": map[string][]string{"deal": {"Регион"}},
			"T": bp.Template{ID: "t1", Name: "Отпуск", Entity: "deal", Active: true, Steps: []bp.Step{{Kind: "approve", To: "manager", Text: "Утвердить"},
				{Kind: "task", To: "u1", Project: "GO", Text: "Оформить", Days: 2}, {Kind: "chat", From: "responsible", To: "u1", Users: []string{"manager"}, Text: "Привет"},
				{Kind: "mail", From: "starter", To: "client", Email: "a@b.c", Subject: "Счёт", Text: "Текст"},
				{Kind: "update", Sets: []bp.Assign{{Field: "amount", Value: "{sum}"}}}, {Kind: "create", Entity: "contact", Text: "{name}", To: "starter"}}}}},
		"openlines": {Data: map[string]any{"Tab": "mine", "Tabs": []struct {
			Tab, URL string
			Count    int
		}{{"mine", "/openlines?tab=mine", 1}, {"all", "/openlines", 2}}, "Q": "иван", "Keep": "tab=mine&q=%D0%B8",
			"Quick": []openlines.Quick{{ID: "q1", Title: "Доставка", Body: "1–2 дня"}}, "Users": []tasks.User{user},
			"Paused": true, "Sessions": []openlines.Session{{ID: "s1aaaaaa", Widget: "Сайт", Name: "Иван", Channel: "telegram", Pinned: true, Unread: 2, Last: "Есть доставка?", LastAt: now}, {ID: "s2bbbbbb", Widget: "Сайт", LastAt: now, Waiting: true, Status: "open", KPILate: 2}, {ID: "s3cccccc", Spam: true, LastAt: now}},
			"Current":  openlines.Session{ID: "s1aaaaaa", Widget: "Сайт", Name: "Иван", Phone: "+7", Page: "https://shop/", Status: "open", Channel: "telegram", ConsentAt: &now, Pinned: true, WidgetID: "w1", QueueID: "w2"},
			"Lines": []openlines.Widget{{ID: "w1", Name: "Сайт", Active: true}, {ID: "w2", Name: "Склад", Active: true}, {ID: "w3", Name: "Опт", Active: true}},
			"Messages": []openlines.Message{{ID: 1, Body: "Есть доставка?", At: now, FileName: "a.png", FileType: "image/png"},
				{ID: 2, Operator: "Анна", AuthorID: "u1", Body: "Да", At: now, FileName: "bill.pdf", FileSize: 2000}}, "Read": int64(2),
			"Past": []openlines.Session{{ID: "s4dddddd", Widget: "Бот", Channel: "telegram", Status: "closed", Last: "Спасибо", CreatedAt: now}, {ID: "s5eeeeee", Widget: "Сайт", Operator: "Ева", Status: "open", Last: "чужое", CreatedAt: now}},
			"PastOpen": map[string]bool{"s4dddddd": true}}},
		"widgets": {Data: map[string]any{"Base": "https://go24", "Users": []tasks.User{user},
			"Widgets": []openlines.Widget{{ID: "w1", Name: "Сайт", Color: "2FC6F6", Origins: []string{"https://shop"}, Operators: []string{"u1"}, Active: true, ConsentOn: true, ConsentText: "Согласие", KPIFirst: 5, KPIFurther: 10, KPIOperator: true, KPIHeads: true},
				{ID: "w2", Name: "Бот", Color: "2FC6F6"}},
			"Bots": map[string]openlines.TelegramBot{"w1": {Name: "shop_bot", Error: "telegram: 401 Unauthorized"}, "w2": {}}, "Heads": []string{"u1"}}},
		"olquick":       {Data: map[string]any{"Quick": []openlines.Quick{{ID: "q1", Title: "Доставка", Body: "1–2 дня", Common: true}, {ID: "q2", Title: "Моё", Body: "x"}}}},
		"widgetpreview": {Data: openlines.Widget{ID: "w1", Name: "Сайт"}},
		"olstats": {Data: map[string]any{"F": openlines.StatsFilter{From: now, To: now}, "By": "days", "BySet": []string{"days", "lines", "operators"},
			"ByLinks": map[string]string{"days": "/openlines/stats?by=days"}, "CSV": "/openlines/stats?csv=1", "Users": []tasks.User{user},
			"Widgets": []openlines.Widget{{ID: "w1", Name: "Сайт"}}, "Stats": openlines.Stats{Total: openlines.StatsRow{New: 2, Up: 1}},
			"Rows": []openlines.StatsRow{{Key: "2026-10-25", Day: now, New: 2}, {Key: "2026-10-26", Day: now}}}},
		"sites": {Data: map[string]any{"Base": "https://go24", "Sites": []sites.Site{{ID: "s1", Name: "Сайт", Slug: "shop", Published: true, Pages: 2, Updated: now}, {ID: "s2", Name: "Черновик", Slug: "draft", Updated: now}}}},
		"sitehistory": {Data: map[string]any{"Site": sites.Site{ID: "s1", Name: "Сайт", Role: sites.RoleEdit}, "Page": "p1", "Title": "Главная", "From": "p1",
			"Revisions": []sites.Revision{{ID: 3, Act: "save", BlockType: "cover", Author: "Анна", At: now, Count: 2}, {ID: 2, Act: "hide", BlockType: "text", At: now, Count: 2},
				{ID: 1, Act: "initial", At: now, Count: 1}}}},
		"sitetrash": {Data: map[string]any{"Days": 30, "Items": []struct {
			sites.TrashItem
			Expires time.Time
		}{{sites.TrashItem{SiteID: "s1", SiteName: "Сайт", PageID: "p1", Title: "Цены", DeletedBy: "Анна", Deleted: now}, now},
			{sites.TrashItem{SiteID: "s2", SiteName: "Старый", Title: "Старый", Deleted: now}, time.Time{}}}}},
		"sitepage": {Data: map[string]any{"Site": sites.Site{ID: "s1", Name: "Сайт", Slug: "shop", Published: true, Role: sites.RoleFull, Everyone: sites.RolePublish},
			"Roles": sites.Roles, "Access": map[string][]tasks.User{"1": {user}, "2": {{ID: "dept:d1", Name: "Продажи"}}},
			"Pages": []sites.Page{{ID: "p1", Title: "Главная", Home: true, Published: true}, {ID: "p2", Title: "Цены", Slug: "prices"}},
			"Page": sites.Page{ID: "p1", SiteID: "s1", Title: "Главная", Slug: "index", Home: true, Published: true, Editor: "Анна", Updated: now, Blocks: []sites.Block{{ID: "b1", Type: "menu", Data: map[string]string{"logo": "Сайт", "links": "О нас | #about\nПлохая | javascript:alert(1)", "color": "#ffeedd"}},
				{ID: "b2", Type: "cover", Data: map[string]string{"title": "Привет", "text": "a\nb\n\nc", "button": "Жми", "link": "#x", "image": "/sitefile/f1", "color": "#112233"}},
				{ID: "b3", Type: "title", Data: map[string]string{"title": "Раздел"}}, {ID: "b4", Type: "text", Hidden: true, Data: map[string]string{"text": "x"}},
				{ID: "b5", Type: "image"}, {ID: "b6", Type: "textimage", Data: map[string]string{"title": "О нас", "image": "https://x/y.png"}},
				{ID: "b7", Type: "features", Data: map[string]string{"items": "А | б\nВ | г"}}, {ID: "b8", Type: "button", Data: map[string]string{"button": "Ок"}},
				{ID: "b9", Type: "contacts", Data: map[string]string{"phone": "+7", "email": "a@b", "address": "Город"}}, {ID: "b10", Type: "footer", Data: map[string]string{"text": "©"}},
				{ID: "b11", Type: "booking", Data: map[string]string{"title": "Запишитесь", "booking": "tk"}}, {ID: "b12", Type: "booking"}}},
			"Types": sites.BlockTypes, "TypeOf": siteTypeOf(), "BookingLinks": []booking.Link{{Token: "tk", Name: "Запись", Active: true}, {Token: "tk2", Name: "Старая"}}}},
		"sitepublic": {Data: map[string]any{"Site": sites.Site{ID: "s1", Name: "Сайт", Slug: "shop", Description: "Описание"}, "Preview": true,
			"Page": sites.Page{ID: "p1", Title: "Главная", Blocks: []sites.Block{{ID: "b1", Type: "menu", Data: map[string]string{"logo": "Сайт", "links": "О нас | #about\nПлохая | javascript:alert(1)", "color": "#ffeedd"}},
				{ID: "b2", Type: "cover", Data: map[string]string{"title": "Привет", "text": "a\nb\n\nc", "button": "Жми", "link": "#x", "image": "/sitefile/f1", "color": "#112233"}},
				{ID: "b3", Type: "title", Data: map[string]string{"title": "Раздел"}}, {ID: "b4", Type: "text", Hidden: true, Data: map[string]string{"text": "x"}},
				{ID: "b5", Type: "image"}, {ID: "b6", Type: "textimage", Data: map[string]string{"title": "О нас", "image": "https://x/y.png"}},
				{ID: "b7", Type: "features", Data: map[string]string{"items": "А | б\nВ | г"}}, {ID: "b8", Type: "button", Data: map[string]string{"button": "Ок"}},
				{ID: "b9", Type: "contacts", Data: map[string]string{"phone": "+7", "email": "a@b", "address": "Город"}}, {ID: "b10", Type: "footer", Data: map[string]string{"text": "©"}},
				{ID: "b11", Type: "booking", Data: map[string]string{"title": "Запишитесь", "booking": "tk"}}, {ID: "b12", Type: "booking"}}}}},
		"admins": {Data: map[string]any{"Groups": []string{"go24-admins"}, "Users": []tasks.User{{ID: "u2", Name: "Иван"}},
			"Admins": []Admin{{ID: "u1", Name: "Борис", Group: true}, {ID: "u2", Name: "Иван", Granted: true, By: "Борис", At: &now}}}},
		"backup": {Data: map[string]any{"Set": backup.Settings{Dir: "ночные", Parts: []string{"go24", "config"}, Secret: "x", Schedule: "weekly", At: "03:00", Weekday: 7, Keep: 7,
			Target: backup.Target{Type: "s3", Endpoint: "https://s3.example.com", User: "AKID", Path: "bk", Secret: "y"}}, "Types": backup.TargetTypes, "RC": true,
			"Parts": backup.Parts, "Weekdays": []int{1, 2, 3, 4, 5, 6, 7}, "Next": &now,
			"Host": "/srv/go24-backups", "Busy": true, "Runs": []backup.Run{{ID: 2, Status: "running", CreatedAt: now, By: "Борис"},
				{ID: 1, Status: "done", CreatedAt: now, FinishedAt: &now, Name: "ночные/go24-20261026-010000", Parts: []string{"go24", "config", "secrets"}, Size: 12345,
					Target: "s3 bk/go24-20261026-010000"}, {ID: 3, Status: "done", CreatedAt: now, Name: "go24-x", UploadError: "403 Forbidden"},
				{ID: 0, Status: "failed", CreatedAt: now, Error: "pg_dump: connection refused", Auto: true},
				{ID: -1, Status: "done", CreatedAt: now, Name: "go24-20261020-030000", Removed: &now},
				{ID: 4, Kind: "restore", Status: "done", CreatedAt: now, FinishedAt: &now, Name: "go24-x", Parts: []string{"go24", "config"}, Target: "restore/go24-x-1"},
				{ID: 5, Kind: "restore", Status: "failed", CreatedAt: now, Name: "go24-x", Parts: []string{"config"}, Error: "backup.err_wrong_password"}}}},
		"backup_restore": {Data: map[string]any{"Copy": backup.Run{ID: 1, Status: "done", CreatedAt: now, Name: "ночные/go24-20261026-010000",
			Parts: []string{"go24", "media", "config", "secrets"}}, "Base": "go24-20261026-010000", "Parts": []string{"go24", "config"}, "Secrets": true,
			"Media": true, "Host": "/srv/go24-backups"}},
		"ipaccess": {Data: map[string]any{"IP": "10.0.0.7", "Users": []tasks.User{{ID: "u2", Name: "Иван"}}, "Rules": []ipRuleView{
			{Rule: ipaccess.Rule{ID: "r1", Title: "Офис", Users: []string{"u2"}, Nets: []string{"10.0.0.0/8", "1.1.1.1-1.1.1.9"}}, Sel: []tasks.User{{ID: "u2", Name: "Иван"}}},
			{Rule: ipaccess.Rule{ID: "r2", All: true, Nets: []string{"10.0.0.7"}}, Sel: []tasks.User{{ID: "all"}}}}}},
		"audit": {Data: map[string]any{"Kinds": audit.Kinds, "DaysChoice": audit.DaysChoice, "Days": 180, "Filter": audit.Filter{UserID: "u1"},
			"More": "/settings/events?before=1", "CSV": "/settings/events?format=csv", "Filtered": true,
			"Events": []audit.Event{{ID: 2, At: now, Kind: "user_fired", UserID: "u1", Name: "Борис", IP: "10.0.0.1", Target: "Иван", TargetID: "u2",
				Agent: "Mozilla/5.0 (Windows NT 10.0) Chrome/130.0.0.0 Safari/537.36"}, {ID: 1, At: now, Kind: "api_bad_key", Target: "GET /api/v1/tasks"},
				{Kind: "audit_days", UserID: "u1", Name: "Борис", Target: "30"}}}},
		"webhooks": {Data: map[string]any{"Events": hooks.Events, "Base": "https://go24",
			"Hooks": []hooks.Webhook{{ID: "h1", Name: "n8n", URL: "https://n8n/webhook/x", Secret: "abc", Events: []string{"deal.created"}, Active: true, LastAt: &now, LastError: "HTTP 500", Pending: 2}}}},
		"apps_settings": {Data: map[string]any{"Empty": apps.App{}, "Scopes": apikeys.AllScopes, "Base": "https://go24", "Access": map[string][]tasks.User{"a1": {{ID: "dept:d1", Name: "Продажи"}}},
			"Places": map[string][]apps.Placement{"a1": {{ID: "p1", Placement: "CRM_DYNAMIC_t1_DETAIL_TAB", Handler: "https://app/t", AppTitle: "Доставка"}}},
			"Events": map[string][]apps.Event{"a1": {{ID: "e1", Event: "ONCRMDEALADD", Handler: "https://app/e", LastAt: &now, LastError: "HTTP 500", Pending: 1}}},
			"Bots": map[string][]matrixsync.Bot{"a1": {{ID: "b1", Code: "helper", Name: "Помощник", WorkPosition: "Поддержка"}}}, "Apps": []apps.App{
			{ID: "a1", Name: "Доставка", HandlerURL: "https://app/x", Scopes: []string{"crm:read"}, ClientID: "local.1", ClientSecret: "s", InstalledAt: &now, Users: 2},
			{ID: "a2", Name: "Синхронизация", APIOnly: true, InstallURL: "https://app/i", InstallError: "HTTP 500", Scopes: []string{"tasks:read", "tasks:write"}}}}},
		"apps": {Data: map[string]any{"Admin": true, "Apps": []apps.App{{ID: "a1", Name: "Доставка", MenuTitle: "Расчёт доставки"}}}},
		"app": {Title: "Доставка", Data: map[string]any{"App": apps.App{ID: "a1", Name: "Доставка"}, "Action": "https://app/x?DOMAIN=go24",
			"Fields": [][2]string{{"AUTH_ID", "go24at_x"}, {"PLACEMENT", "DEFAULT"}}}},
		"deadlines": {Data: map[string]any{"Role": "responsible", "Roles": myRoles, "Columns": []deadlineColumn{
			{Period: "overdue", Tasks: []tasks.Task{task}}, {Period: "today"}, {Period: "none"}}}},
		"fields": {Data: map[string]any{"Base": "/settings/user-fields", "Title": "title.user_fields", "Sub": "fields.user_subtitle", "Kinds": tasks.FieldKinds, "Fields": []tasks.Field{{ID: "uf2", Name: "Этап", Kind: "list", Options: []string{"А", "Б"}}}}},
		"files": {Data: map[string]any{"Files": []storages.File{{ID: "f1", Name: "a.pdf", Size: 10, CreatedAt: now, TaskKey: "GO-7"}},
			"Storages": []storages.Storage{{ID: "s1", Name: "NC", Type: "webdav", Shared: true, CheckOK: true}}}},
		"storages": {Data: map[string]any{"Storages": []storages.Storage{{ID: "s1", Name: "NC", Type: "webdav", Mine: true, CheckedAt: &now},
			{ID: "s2", Name: "S3", Type: "s3", Shared: true, CheckOK: true}}, "Types": storages.Types, "Enabled": true, "CanShare": true}},
		"keys": {Data: map[string]any{"NewToken": "go24_x_y", "Scopes": apikeys.AllScopes, "BaseURL": "http://x",
			"Keys": []apikeys.Key{{ID: "k1", KeyID: "abc", Name: "CI", Scopes: []string{"tasks:read"}, CreatedAt: now, LastUsedAt: &now}}}},
		"tasks": {Data: map[string]any{"Tasks": []tasks.Task{task}, "ShowAll": false, "Projects": []tasks.Project{project}, "Project": project, "Limit": 1,
			"Q": url.Values{"status": {"in_progress"}, "responsible": {"me"}, "period": {"overdue"}}, "Query": "period=overdue", "Filtered": true,
			"Users": []tasks.User{user}, "Presets": []tasks.Preset{{ID: "p1", Name: "Мои срочные", Query: "prio=high"}}, "BuiltIn": builtInPresets,
			"Statuses": []tasks.Status{tasks.StatusPending, tasks.StatusInProgress}, "Periods": tasks.DeadlinePeriods,
			"Bulk": bulkBar{Back: "/tasks", Users: []tasks.User{user}, Ops: bulkOps}}},
		"search": {Query: "трекер", Data: []tasks.Task{task}},
		"gantt": {Data: map[string]any{"Project": project, "All": false, "Fold": "GO-9", "LinkTypes": tasks.LinkTypes, "View": buildGantt(tasks.GanttData{
			Tasks: []tasks.GanttTask{{Task: task, Start: now.AddDate(0, 0, -3), End: now.AddDate(0, 0, 2), Planned: true, Editable: true, Linkable: true},
				{Task: tasks.Task{ID: "t2", ProjectKey: "GO", Number: 8, Title: "Веха", Status: tasks.StatusPending, ParentKey: "GO-7", Deadline: &now}, Start: now, End: now, Planned: true},
				{Task: tasks.Task{ID: "t3", ProjectKey: "GO", Number: 9, Title: "Свёрнутая", Status: tasks.StatusPending, SubTotal: 1}, Start: now, End: now.AddDate(0, 0, 1)},
				{Task: tasks.Task{ID: "t4", ProjectKey: "GO", Number: 10, Title: "Скрытая", Status: tasks.StatusPending, ParentKey: "GO-9"}, Start: now, End: now}},
			Links: []tasks.Link{{FromID: "t1", ToID: "t2", Type: "start_finish", FromKey: "GO-7", ToKey: "GO-8"}}}, "", now, map[string]bool{"GO-9": true})}},
		"plan":    {Data: []tasks.PlanColumn{{Tasks: []tasks.Task{task}}, {ID: "s1", Code: "today"}, {ID: "s2", Title: "Жду ответа", Tasks: []tasks.Task{task}}}},
		"calsettings": {Data: map[string]any{"S": calendar.DefaultSettings(), "Days": []int{1, 2, 3, 4, 5, 6, 7}, "Hours": []int{0, 9, 18, 24}, "Reminds": calendar.RemindPresets}},
		"calslots": {Data: map[string]any{"Base": "https://go24.test", "Links": []calendar.SlotLink{
			{ID: "l1", Token: "t1", Length: 60, Rules: []calendar.SlotRule{{Days: []int{1, 2}, From: 540, To: 1080}}, DaysAhead: 30, Active: true},
			{ID: "l2", Token: "t2", Members: []string{"u2"}, MemberNames: []string{"Анна"}, Length: 30, Rules: []calendar.SlotRule{{Days: []int{5}, From: 600, To: 720}}, DaysAhead: 7, Meetings: 2}},
			"Edit": calendar.SlotLink{ID: "l2", Members: []string{"u2"}, Length: 30, DaysAhead: 7}, "Rules": []calendar.SlotRule{{Days: []int{5}, From: 600, To: 720}, {From: 540, To: 1080}},
			"Users": []tasks.User{user}, "Lengths": calendar.SlotLengths, "Days": []int{1, 2, 3, 4, 5, 6, 7}, "Minutes": []int{0, 540, 600, 720, 1080, 1440}}},
		"calopen": {Data: map[string]any{"Me": "u1", "Msg": "ok",
			"Categories": []calendar.Category{{ID: "c1", Name: "Обучение", Description: "Курсы", Creator: "Анна", Mine: true, Upcoming: 2}, {ID: "c2", Name: "Спорт", Muted: true, Closed: true}},
			"Cat":        calendar.Category{ID: "c1", Name: "Обучение", Description: "Курсы", Creator: "Анна", Mine: true, Upcoming: 2},
			"Edit":       calendar.Category{ID: "c1", Name: "Обучение"},
			"Events": []calendar.Event{{ID: "e1", Title: "Семинар", Start: time.Now(), End: time.Now().Add(time.Hour), OwnerID: "u2", OwnerName: "Анна", CategoryID: "c1", CategoryName: "Обучение"},
				{ID: "e2", Title: "Курс", Start: time.Now(), End: time.Now().Add(time.Hour), OwnerID: "u2", MyStatus: "accepted", Repeat: "weekly", CategoryID: "c1"},
				{ID: "e3", Title: "Свой", Start: time.Now(), End: time.Now().Add(time.Hour), OwnerID: "u1", AllDay: true, CategoryID: "c1"}}}},
		"calsync": {Data: map[string]any{"Feed": calendar.Feed{Token: "t", Tasks: true}, "Link": "https://go24.test/calendar/feed/t.ics", "CalDAV": "https://go24.test/caldav/", "Webcal": "webcal://go24.test/calendar/feed/t.ics"}},
		"rooms": {Data: map[string]any{"Rooms": []struct {
			calendar.Room
			Slots []calendar.Slot
		}{{calendar.Room{ID: "r1", Name: "Большая", Capacity: 8}, []calendar.Slot{{Start: time.Now(), End: time.Now().Add(time.Hour), Title: "Совет"}}}}}},
		"worktime": {Data: map[string]any{"Schedule": worktime.Default(), "Off": map[int]bool{0: true, 6: true}, "Days": []int{1, 2, 3, 4, 5, 6, 0},
			"Periods": company.ReportPeriods, "Departments": []*company.Department{dept, dept.Children[0]},
			"Rules": company.ReportRules{ReportRule: company.ReportRule{Period: "week", Weekday: 5}, Depts: map[string]company.ReportRule{"d2": {Period: "month", Monthday: 25}}}}},
		"workreports": {Data: map[string]any{"Tab": "team", "Team": true, "People": []company.Person{{ID: "u2", Name: "Анна"}}, "User": "u2", "Unchecked": false,
			"Next": company.ReportPeriod{From: now, To: now, Due: true, Rule: company.ReportRule{Period: "week", Weekday: 5}}, "Month": now,
			"PrevMonth": "2026-09", "NextMonth": "2026-11", "List": []company.WorkReport{{ID: 1, UserID: "u2", UserName: "Анна", From: now, To: now.AddDate(0, 0, 4),
				Worked: 3600, Sent: now, Mark: "good", CommentCount: 2}, {ID: 2, UserID: "u2", UserName: "Анна", From: now, To: now, Sent: now}}}},
		"workreport": {ReportDue: &company.ReportPeriod{From: now, To: now}, WorkdayOn: true, Data: map[string]any{"Report": company.WorkReport{ID: 1, UserID: "u2", UserName: "Анна",
			From: now, To: now.AddDate(0, 0, 4), Body: "Сделала квартал", Worked: 7200, Sent: now, Mark: "bad", MarkedBy: "Борис", MarkedAt: &now,
			Comments: []company.ReportComment{{ID: 1, AuthorID: "u1", AuthorName: "Борис", Body: "Почему?", At: now}}},
			"CanMark": true, "Marks": company.ReportMarks, "Worked": 7200, "Period": company.ReportPeriod{From: now, To: now.AddDate(0, 0, 4)},
			"Items": company.ReportItems{Tasks: []company.ReportTask{{Key: "GO-1", Title: "Квартал", Status: "completed", Closed: true, Seconds: 3600}, {Key: "GO-2", Title: "Дальше", Status: "in_progress"}},
				Events: []company.ReportEvent{{ID: "e1", Title: "Планёрка", Start: now, End: now}, {ID: "e2", Title: "Отпуск", Start: now, End: now, AllDay: true}}}}},
		"company": {Data: map[string]any{"Employees": []company.Profile{{ID: "u1", Name: "Пётр Панов", LastName: "Панов", FirstName: "Пётр",
			Position: "Инженер", Email: "p@x", WorkPhone: "+7 1", MobilePhone: "+7 2", Departments: []company.DeptRef{{ID: "d1", Name: "ИТ"}}}},
			"Departments": []*company.Department{dept, dept.Children[0]}, "Filter": company.Filter{Query: "x", DepartmentID: "d2", Preset: "absent"}, "Admin": true,
			"Presets": company.EmployeePresets, "Keep": listQuery{url.Values{"q": {"x"}}}, "Absent": map[string]company.Absence{"u1": {ID: "a1", Kind: "trip", From: now, To: now}}}},
		"structure": {Data: map[string]any{"Root": dept, "Departments": []*company.Department{dept, dept.Children[0]}, "Users": []tasks.User{user}, "Admin": true}},
		"staffchanges": {Data: map[string]any{"Departments": []*company.Department{dept}, "Kinds": company.StaffKinds,
			"Filter": company.StaffFilter{Kind: "moved", From: time.Now()}, "Days": []staffDay{{Day: time.Now(), List: []company.StaffChange{
				{Person: company.Person{ID: "u1", Name: "Ivan"}, Kind: "moved", Was: []string{"IT"}, Departments: []string{"Sales"}, ActorID: "u2", ActorName: "Boss"},
				{Person: company.Person{ID: "u3", Name: "Olga", Position: "HR"}, Kind: "hired"}}}}}},
		"meetings": {Data: map[string]any{"Filter": company.MeetingFilter{Role: "owner"}, "Statuses": company.MeetingStatuses, "Me": "u1",
			"List": []company.Meeting{{ID: 1, Title: "Планёрка", Place: "Зал", Start: now, Minutes: 30, Status: "action", Owner: company.Person{ID: "u1", Name: "Ivan"},
				Members: []company.Person{{ID: "u2", Name: "Olga"}}}}}},
		"meetingform": {Data: map[string]any{"M": company.Meeting{ID: 1, Title: "Планёрка", Start: now, Minutes: 30, Owner: company.Person{ID: "u1"}},
			"Users": []tasks.User{user}, "Back": "/worktime/meetings/1", "Members": []string{"u1"}, "TitleMax": 255, "Durations": []int{30, 60}}},
		"meeting": {Data: map[string]any{"Can": true, "Owner": true, "Me": "u1", "Users": []tasks.User{user}, "Deadline": now,
			"Projects": []tasks.Project{{Key: "OPS", Name: "Операции"}},
			"M": company.Meeting{ID: 2, Title: "Планёрка", Place: "Зал", Start: now, Minutes: 30, Status: "action", Owner: company.Person{ID: "u1", Name: "Ivan"},
				Keeper: company.Person{ID: "u2", Name: "Olga"}, Members: []company.Person{{ID: "u3", Name: "Petr"}}, PrevID: 1, PrevTitle: "Планёрка", PrevStart: now,
				EventID: "e1", Protocol: "Итог", Started: &now,
				Items: []company.MeetingItem{{ID: 1, Num: "1", Title: "Сделки", Reporter: company.Person{ID: "u3", Name: "Petr"}, Minutes: 10, Result: "Дожать",
					Tasks: []company.MeetingTask{{ItemID: 1, Key: "OPS-1", Title: "Позвонить", Status: "pending", Responsible: company.Person{ID: "u3", Name: "Petr"}, Deadline: &now}}},
					{ID: 2, ParentID: 1, Num: "1.1", Title: "Крупные"}},
				Carried: []company.MeetingTask{{ItemID: 9, ItemTitle: "Старое", Key: "OPS-2", Title: "Отчёт", Status: "in_progress", Responsible: company.Person{ID: "u1", Name: "Ivan"}}}}}},
		"honor": {Data: map[string]any{"Departments": []*company.Department{dept}, "Filter": company.HonorFilter{DepartmentID: dept.ID},
			"Can": true, "Today": now, "Add": "u1", "ReasonMax": company.HonorReasonMax, "Users": []company.Profile{{ID: "u1", Name: "Ivan", Position: "Dev"}},
			"List": []company.Honor{{ID: 1, Person: company.Person{ID: "u1", Name: "Ivan"}, Departments: []string{"Sales"}, Reason: "Best\nseller",
				From: now, ByID: "u2", ByName: "Boss"}, {ID: 2, Person: company.Person{ID: "u3", Name: "Olga"}, From: now.AddDate(0, 0, 3), To: &now}}}},
		"learning": {Data: map[string]any{"Tab": "people", "Keep": true, "Assign": true, "Today": now, "Departments": []*company.Department{dept},
			"Filter": company.LearnerFilter{CourseID: "c1", Status: "overdue"}, "Courses": []company.Course{{ID: "c1", Title: "Onboarding"}},
			"Learners": []company.Learner{{Person: company.Person{ID: "u1", Name: "Ivan"}, CourseID: "c1", CourseTitle: "Onboarding", Due: &now,
				ByName: "Boss", Assigned: now, Done: 1, Total: 3}, {Person: company.Person{ID: "u2", Name: "Olga"}, Done: 2, Total: 2}}}},
		"learncourse": {Data: map[string]any{"Keep": true, "Assign": true, "Today": now, "Users": []tasks.User{{ID: "u1", Name: "Ivan"}},
			"C": company.Course{ID: "c1", Title: "Onboarding", Description: "First\nweek", Color: "green", AuthorName: "Boss", Lessons: 2, Done: 1,
				Assigned: true, Due: &now, AssignedBy: "Boss"},
			"Lessons": []company.Lesson{{ID: "l1", Chapter: "Start", Title: "Hello", Done: true}, {ID: "l2", Chapter: "Start", Title: "Tools"}},
			"Next": &company.Lesson{ID: "l2"}, "Learners": []company.Learner{{Person: company.Person{ID: "u1", Name: "Ivan"}, CourseID: "c1", Done: 1, Total: 2, Assigned: now}}}},
		"learnlesson": {Data: map[string]any{"Keep": true, "N": 1, "C": company.Course{ID: "c1", Title: "Onboarding", Lessons: 2},
			"Lessons": []company.Lesson{{ID: "l1", Title: "Hello", Done: true}, {ID: "l2", Title: "Tools"}},
			"L": company.Lesson{ID: "l1", Title: "Hello", Body: "# Hi\n\n**bold**", Kind: "test", PassPct: 70, Minutes: 10, Done: true},
			"NextL": company.Lesson{ID: "l2"}, "Questions": 3, "Left": 1, "Tries": []company.Attempt{{ID: 1, Score: 2, Max: 3, Passed: true, Finished: &now},
				{ID: 2}}}},
		"learnattempt": {Data: map[string]any{"C": company.Course{ID: "c1", Title: "Onboarding"}, "Seconds": 90,
			"L": company.Lesson{ID: "t1", Title: "Quiz", Kind: "test", PassPct: 70, Minutes: 5},
			"A": company.Attempt{ID: 7, Questions: []string{"q1", "q2"}, Answers: map[string]any{}},
			"Questions": []company.Question{{ID: "q1", Kind: "many", Text: "Pick", Options: []company.Option{{Text: "A", Right: true}, {Text: "B"}}},
				{ID: "q2", Kind: "text", Text: "Type"}}}},
		"learnform": {Data: map[string]any{"Colors": company.CourseColors, "TitleMax": 200, "DescMax": 5000,
			"C": company.Course{ID: "c1", Title: "Onboarding", Color: "blue", Published: true}, "Access": []tasks.User{{ID: "dept:d1", Name: "Sales"}},
			"Lessons": []company.Lesson{{ID: "l1", Title: "Hello", Chapter: "Start"}, {ID: "l2", Title: "Tools"}}}},
		"learnlessonform": {Data: map[string]any{"TitleMax": 200, "Chapters": []string{"Start"}, "C": company.Course{ID: "c1", Title: "Onboarding"},
			"L": company.Lesson{ID: "l1", CourseID: "c1", Title: "Hello", Chapter: "Start", Body: "text", Kind: "test", PassPct: 80},
			"QKinds": company.QuestionKinds, "Q": company.Question{ID: "q1", Kind: "one", Text: "Pick", Points: 1, Options: []company.Option{{Text: "A", Right: true}, {Text: "B"}}},
			"Questions": []company.Question{{ID: "q1", Kind: "one", Text: "Pick", Points: 1, Options: []company.Option{{Text: "A", Right: true}, {Text: "B"}}},
				{ID: "q2", Kind: "text", Text: "Type", Points: 2, Answers: []string{"yes", "да"}}}}},
		"pulse": {Data: map[string]any{"Period": "week", "Dept": "", "Involved": company.PulseInvolved, "Periods": []string{"today", "week"},
			"P": &company.Pulse{From: now.AddDate(0, 0, -1), To: now, HasToday: true, Total: 3, Active: 2, Today: 1, Involved: 1,
				Days:  []company.PulseDay{{Day: now.AddDate(0, 0, -1), Users: 2}, {Day: now, Users: 1}},
				Tools: []company.PulseTool{{Tool: "tasks", Users: 2, Pct: 67, Daily: []int{2, 1}}, {Tool: "mobile", Users: 0, Daily: []int{0, 0}}},
				Depts: []company.PulseDept{{ID: "d1", Name: "Sales", Total: 3, Active: 2, Involved: 1}, {ID: "d2", Name: "B2B", Depth: 1, Total: 1}},
				Users: []company.PulseUser{{Person: company.Person{ID: "u1", Name: "Ivan", Position: "Dev"}, Tools: []string{"tasks", "im", "feed", "crm"}, Days: 2, Involved: true},
					{Person: company.Person{ID: "u2", Name: "Olga"}}}}}},
		"thanks": {Data: map[string]any{"Departments": []*company.Department{dept}, "Periods": thanksPeriods, "Period": "", "Person": "Ivan",
			"Badges": feed.Badges, "BadgeEmoji": feed.BadgeEmoji, "Filter": feed.BoardFilter{UserID: "u1", Badge: "cup", From: now},
			"Leaders": []feed.Leader{{Person: feed.Person{ID: "u1", Name: "Ivan"}, Position: "Dev", Count: 3, Badges: []feed.BadgeCount{{Badge: "cup", Count: 2}, {Badge: "star", Count: 1}}}},
			"List": []feed.BoardEntry{{PostID: "f1", Badge: "cup", Text: "За релиз", Author: feed.Person{ID: "u2", Name: "Boss"},
				People: []feed.Person{{ID: "u1", Name: "Ivan"}, {ID: "u3", Name: "Olga"}}, At: now}}}},
		"profile": {Phone: "102", PhoneURL: "https://go24/phone", Data: map[string]any{"Phone": map[string]any{"Line": telephony.Line{UserID: "u1", Name: "Анна", Number: "101", Password: "pw"}, "SIPHost": "192.168.1.15"}, "NumberLocked": true, "Tab": "", "Fields": []tasks.FieldValue{{Field: tasks.Field{ID: "uf1", Name: "Хобби", Kind: "string"}, Value: "шахматы"}, {Field: tasks.Field{ID: "uf2", Name: "Разряд", Kind: "list", Options: []string{"1", "2"}}}},
			"Thanks": []feed.Thanks{{PostID: "f4", Badge: "star", From: "Борис", Text: "за релиз", At: now}}, "P": company.Profile{ID: "u2", Name: "Пётр", Birthday: &now, Active: false, Departments: []company.DeptRef{{ID: "d2", Name: "ИТ"}}},
			"Managers": []company.Person{{ID: "u1", Name: "Борис", Position: "Директор"}}, "Subordinates": []company.Person{{ID: "u3", Name: "Анна"}},
			"Departments": []*company.Department{dept}, "InDept": map[string]bool{"d1": true}, "Tasks": []tasks.Task{task},
			"CanEdit": true, "Admin": true, "Me": false, "HR": company.HR{On: true, Rights: []string{"invite"}}, "HRRights": company.HRRights}},
		"scrum": {Data: map[string]any{"Project": scrumP, "Backlog": []scrumRow{{Task: task, CanMove: true, Epic: &tasks.Epic{ID: "e1", Name: "Эпик", Color: "69DAFF"}}},
			"Points": 3.5, "Epics": []tasks.Epic{{ID: "e1", Name: "Эпик", Color: "69DAFF", Tasks: 1, Points: 3}}, "Colors": tasks.StageColors, "NextName": "Спринт 2",
			"Sprints": []scrumSprint{{Sprint: tasks.Sprint{ID: "s1", Name: "Спринт 1", Status: "active", DateStart: &now, DateEnd: &now, Points: 5, Done: 2},
				Rows: []scrumRow{{Task: task}}}, {Sprint: tasks.Sprint{ID: "s2", Name: "Спринт 2", Status: "planned"}}}}},
		"sprints": {Data: map[string]any{"Project": scrumP, "Active": nil, "Done": nil,
			"Velocity": buildVelocity([]tasks.Sprint{{Name: "Спринт 2", Points: 8, Done: 5}, {Name: "Спринт 1", Points: 5, Done: 5}})}},
		"scrumteam": {Data: map[string]any{"Project": scrumP, "Team": tasks.ScrumTeam{Owner: &user, Team: []tasks.Member{{User: user, Role: "owner"}}},
			"Types": []tasks.ScrumType{{ID: "st1", Name: "Задача", Required: true, Items: []tasks.DodItem{{ID: "i1", Title: "Тесты"}}}, {ID: "st2", Name: "Ошибка"}}}},
		"feedsettings": {Data: map[string]any{"Users": []tasks.User{user}, "All": []tasks.User{{ID: "dept:d1", Name: "Отдел продаж"}, user}, "Important": nil}},
		"feed": {Data: map[string]any{"View": "", "More": "x", "Me": "u1", "Admin": true, "MayAll": true, "MayImportant": false, "Projects": []any{project},
			"Side": map[string]any{"Important": []struct{ ID, Author, Text string }{{"f9", "Анна", "Собрание в 10"}},
				"Birthdays": []company.Birthday{{Person: company.Person{ID: "u2", Name: "Анна"}, Date: now, Today: true}, {Person: company.Person{ID: "u3", Name: "Пётр"}, Date: now.AddDate(0, 0, 3)}},
				"Events": []feedMeeting{{Event: calendar.Event{ID: "e1", Title: "Планёрка", Start: now, End: now.Add(time.Hour), Video: true, Repeat: "daily"}, Day: "today"},
					{Event: calendar.Event{ID: "e2", Title: "Обзор", Start: now.AddDate(0, 0, 3), End: now.AddDate(0, 0, 3), MyStatus: "invited", AllDay: true}}}},
			"Users": []tasks.User{user, {ID: "u2", Name: "Анна"}}, "Badges": feed.Badges, "BadgeEmoji": feed.BadgeEmoji,
			"Reactions": reactionList(), "Items": []feed.Item{
				{Post: &feed.Post{ID: "f1", AuthorID: "u1", AuthorName: "Борис", Title: "Новость", Body: "текст\nстрока", Important: true,
					Reactions: feed.Summary{Mine: "laugh", Total: 3, Top: []feed.Count{{Reaction: "laugh", N: 2}, {Reaction: "like", N: 1}}}, Readers: 3,
					Comments: []feed.Comment{{ID: "c1", AuthorID: "u2", AuthorName: "Анна", Body: "ок", CreatedAt: now, Reactions: feed.Summary{Total: 1, Top: []feed.Count{{Reaction: "cry", N: 1}}}}}, CreatedAt: now}},
				{Post: &feed.Post{ID: "f2", AuthorID: "u2", AuthorName: "Анна", ProjectKey: "GO", ProjectName: "go24", Body: "в проект", CreatedAt: now,
					Files: []feed.File{{ID: "x1", Name: "схема.png", Size: 120000, ContentType: "image/png"}, {ID: "x2", Name: "план.pdf", Size: 2500000, ContentType: "application/pdf"}},
					Poll:  &feed.Poll{Question: "Когда?", Voters: 2, Voted: true, Options: []feed.PollOption{{ID: "o1", Text: "Пн", Votes: 2, Mine: true}, {ID: "o2", Text: "Вт"}}}}},
				{Post: &feed.Post{ID: "f4", AuthorName: "Борис", Body: "спасибо", CreatedAt: now,
					Gratitude: &feed.Gratitude{Badge: "cup", People: []feed.Person{{ID: "u2", Name: "Анна"}, {ID: "u3", Name: "Пётр"}}}}},
				{Post: &feed.Post{ID: "f3", AuthorName: "Анна", Body: "опрос", CreatedAt: now,
					Poll: &feed.Poll{Question: "Где?", Multiple: true, Options: []feed.PollOption{{ID: "o3", Text: "Тут"}, {ID: "o4", Text: "Там"}}}}},
				{Event: &tasks.Event{Kind: "created", TaskKey: "GO-1", TaskTitle: "Задача", ActorID: "u1", ActorName: "Борис", At: now}},
				{Event: &tasks.Event{Kind: "completed", TaskKey: "GO-1", TaskTitle: "Задача", ActorID: "u1", ActorName: "Борис", At: now}},
			}}},
		"im": {Data: map[string]any{"Calls": true, "Room": "!r", "Thread": "", "NoAccount": false, "Element": "https://el",
			"Rooms": []matrixsync.Room{{ID: "!r", Name: "Проект", Kind: "project", Unread: 2, Last: &matrixsync.Message{SenderName: "Анна", Body: "привет"}},
				{ID: "!d", Name: "Пётр", Kind: "dm", PeerID: "u2"}, {ID: "!e", Name: "Секрет", Kind: "group", Encrypted: true, Last: &matrixsync.Message{Encrypted: true}}},
			"Current": matrixsync.Room{ID: "!r", Name: "Проект"}}},
		"notify_settings": {Data: map[string]any{"Kinds": notify.Kinds, "Messenger": true,
			"Settings": notify.Settings{Off: map[string]bool{"like": true}, Matrix: true},
			"Links": []notifyout.Link{{Channel: "telegram", Linked: true, Account: "@anna", Off: map[string]bool{"like": true}},
				{Channel: "max", Code: "abc", URL: "https://max.ru/bot?start=abc"}, {Channel: "slack", Bot: "go24", Code: "def"}, {Channel: "mattermost"}},
			"Linked": []notifyout.Link{{Channel: "telegram", Linked: true, Account: "@anna", Off: map[string]bool{"like": true}}}}},
		"notify_bots": {Data: map[string]any{"Bots": []struct {
			notifyout.Bot
			NeedsServer bool
		}{{Bot: notifyout.Bot{Channel: "telegram", Enabled: true, Name: "go24bot", HasToken: true}}, {Bot: notifyout.Bot{Channel: "mattermost"}, NeedsServer: true}}}},
		"efficiency": {Data: map[string]any{"Mine": false, "From": "2026-10-01", "To": "2026-10-31", "Prev": "/p", "Next": "/n",
			"E": tasks.Efficiency{UserID: "u1", Name: "Анна", Percent: 75, Total: 4, Violated: 1, Days: []tasks.DayPoint{{Day: now, Percent: 100}, {Day: now, Percent: 75}},
				Violations: []tasks.Violation{{Key: "GO-1", Title: "Отчёт", Kind: "overdue", At: now}, {Key: "GO-2", Title: "Макет", Kind: "rework", At: now}}},
			"Chart": effChart([]tasks.DayPoint{{Day: now, Percent: 100}, {Day: now, Percent: 75}}),
			"Team":  []tasks.Efficiency{{UserID: "u1", Name: "Анна", Percent: 60, Total: 5, Violated: 2}}}},
		"flows": {Data: map[string]any{"Flows": []tasks.Flow{
			{ID: "f1", Name: "Поддержка", ProjectKey: "GO", ProjectName: "go", OwnerName: "Анна", Distribution: "queue", Active: true,
				CanCreate: true, CanManage: true, Stats: tasks.FlowStats{Queue: 2, InWork: 3, Done: 5, AvgQueue: 90, AvgWork: 600, Efficiency: 80}},
			{ID: "f2", Name: "Выключенный", Distribution: "himself", Stats: tasks.FlowStats{Efficiency: 100}}}}},
		"flow": {Data: map[string]any{"Link": "http://go24/flows/f1", "Template": tasks.Template{Description: "Опишите проблему", Checklist: []string{"a", "b"}},
			"F": tasks.Flow{ID: "f1", Name: "Поддержка", Description: "Заявки", ProjectKey: "GO", ProjectName: "go", OwnerName: "Анна",
				Distribution: "manually", PlannedMinutes: 480, CanCreate: true, CanManage: true, Members: []tasks.User{user},
				Stats: tasks.FlowStats{Queue: 1, AvgQueue: 30, Efficiency: 60}},
			"Tasks": []tasks.Task{task}}},
		"flowedit": {Data: map[string]any{"F": tasks.Flow{ID: "f1", Name: "Поддержка", ProjectID: project.ID, OwnerID: user.ID, Distribution: "queue",
			PlannedMinutes: 90, CreatorsAll: true, Active: true}, "Projects": []tasks.Project{project}, "Users": []tasks.User{user},
			"Templates": []tasks.Template{{ID: "t1", Title: "Заявка"}}, "Members": map[string]bool{user.ID: true}, "Creators": map[string]bool{},
			"PlannedHours": "1.5"}},
		"trash": {Data: map[string]any{"Days": 30, "CanSet": true, "Items": []tasks.TrashItem{
			{ID: "t1", Key: "GO-1", Title: "Отчёт", ProjectKey: "GO", ProjectName: "go", CreatorName: "Анна", DeletedByName: "Анна",
				DeletedAt: now, Subtasks: 2, PurgeAt: &now}, {ID: "t2", Key: "GO-2", Title: "Без срока", DeletedAt: now}}}},
		"templates": {Data: map[string]any{"Templates": []tasks.Template{
			{ID: "t1", Title: "Отчёт", ProjectKey: "GO", ProjectName: "go", ResponsibleName: "Анна", NextRun: &now, CreatedCount: 2,
				Repeat: tasks.Repeat{On: true, Period: "weekly", Every: 2, WeekDays: []int{1, 3}, At: "09:00"}},
			{ID: "t2", Title: "Ежемесячно", Repeat: tasks.Repeat{On: true, Period: "yearly", Every: 1, MonthDay: 5, YearMonth: 3, At: "10:00"}},
			{ID: "t3", Title: "Просто шаблон"}}}},
		"template": {Data: map[string]any{"Projects": []tasks.Project{project}, "T": tasks.Template{ID: "t1", Title: "Отчёт", LastRun: &now},
			"Form": templateForm(tasks.Template{ID: "t1", Title: "Отчёт", ProjectKey: "GO", Accomplices: []string{"u1"},
				Repeat: tasks.Repeat{On: true, Period: "monthly", Every: 1, MonthDay: 31, YearMonth: 1, At: "09:00", StartsOn: now}},
				[]tasks.User{user}, []tasks.Project{project})}},
		"calendar": {Data: map[string]any{"Month": now, "Prev": "2026-09", "Next": "2026-11", "Me": "u1", "View": "month", "Day": now, "WeekDays": []int{7, 1, 2, 3, 4, 5, 6}, "Weeks": [][]calDay{{
			{Date: now, InMonth: true, Today: true, Items: []calendar.Event{
				{ID: "e1", Kind: "event", Title: "Планёрка", Start: now, End: now, Company: true},
				{ID: "e2", Kind: "event", Title: "Созвон", Start: now, End: now, MyStatus: "invited", ProjectKey: "GO"},
				{Kind: "task", TaskKey: "GO-1", Title: "Отчёт", Start: now, Overdue: true}}},
			{Date: now.AddDate(0, 0, 1)}}}}},
		"event": {Data: map[string]any{"E": calendar.Event{ID: "e1", Title: "Планёрка", OwnerID: "u2", OwnerName: "Анна", Start: now, End: now,
			Repeat: "weekly", Every: 2, Until: &now,
			MyStatus: "invited", Attendees: []calendar.Attendee{{ID: "u1", Name: "Я", Status: "invited"}, {ID: "u3", Name: "Пётр", Status: "accepted"}}},
			"Users": []tasks.User{user}, "Invited": []tasks.User{user}, "Projects": []tasks.Project{project}, "Me": "u1", "CanEdit": true,
			"Remind": "15"}},
		"absences": {Data: map[string]any{"View": "", "Form": company.Absence{ID: "a1", UserID: "u1", UserName: "Анна", Kind: "vacation", From: now, To: now, Rights: []string{"view"}}, "Editing": true, "Back": "/company/absences", "Close": "/company/absences",
			"Users": []tasks.User{user}, "Kinds": company.AbsenceKinds, "Rights": company.ActingRights, "Me": "u1", "Today": now,
			"Rows": company.AbsenceChart([]company.Person{{ID: "u1", Name: "Анна"}, {ID: "u2", Name: "Пётр"}}, []company.Absence{company.Absence{ID: "a1", UserID: "u1", UserName: "Анна", Kind: "vacation", From: now, To: now, Rights: []string{"view"}}},
				now.AddDate(0, 0, -2), 7, func(t time.Time) bool { return t.Weekday() == time.Sunday }, now),
			"Dates": []time.Time{now}, "Off": map[string]bool{}, "From": now, "Last": now, "Prev": "2026-09-01", "Next": "2026-11-01",
			"TodayKey": now.Format(time.DateOnly), "Mine": map[string]bool{"u1": true}, "Keep": "from=2026-10-01",
			"Depts": []*company.Department{{ID: "d1", Name: "ИТ"}}}},
		"vacations": {Data: map[string]any{"Year": 2026, "Dept": "", "People": 2, "Days": 7, "NoPlan": 1, "Keep": "year=2026&dept=",
			"Depts": []*company.Department{{ID: "d1", Name: "ИТ"}}, "Report": []company.VacationDept{{ID: "d1", Name: "ИТ", Days: 7, NoPlan: 1,
				Lines: []company.VacationLine{{Person: company.Person{ID: "u1", Name: "Анна"}, Days: 7,
					Vacations: []company.Absence{{ID: "a1", UserID: "u1", Kind: "vacation", From: now, To: now.AddDate(0, 0, 6)}}},
					{Person: company.Person{ID: "u2", Name: "Пётр"}}}}, {Lines: []company.VacationLine{{Person: company.Person{ID: "u3", Name: "Ольга"}}}}}}},
		"birthdays": {Data: map[string]any{"List": []company.Birthday{{Person: company.Person{ID: "u1", Name: "Анна"}, Date: now, Today: true}}}},
		"notifications": {Data: map[string]any{"More": "5", "Items": []notify.Item{
			{ID: 5, Kind: "status", ActorID: "u1", ActorName: "Анна", TaskKey: "GO-1", TaskTitle: "Задача", Body: "completed", CreatedAt: now},
			{ID: 4, Kind: "deadline", ActorName: "Анна", TaskKey: "GO-1", Body: now.Format(time.RFC3339), CreatedAt: now, Read: true},
			{ID: 3, Kind: "participant", ActorName: "Анна", TaskKey: "GO-1", Body: "auditor", CreatedAt: now},
			{ID: 2, Kind: "post_comment", ActorName: "Анна", PostID: "p1", PostTitle: "Пост", Body: "ок", CreatedAt: now},
			{ID: 1, Kind: "like", ActorName: "Анна", PostID: "p1", CreatedAt: now},
			{ID: 6, Kind: "task_remind", ActorName: "Анна", TaskKey: "GO-1", TaskTitle: "Задача", CreatedAt: now},
			{ID: 7, Kind: "project_invite", ActorName: "Анна", ProjectKey: "IN", ProjectName: "Новый", Body: "member", CreatedAt: now}}}},
		"newtask": {Data: map[string]any{"Projects": []tasks.Project{project}, "Form": taskForm{New: true, Action: "/tasks/new", Cancel: "/my",
			Responsible: "u1", Deadline: &now, Priority: 1, Users: []tasks.User{user}, Projects: []tasks.Project{project}, Project: "GO",
			Accomplices: []tasks.User{user}, DayPlan: true, OL: &openlines.Session{ID: "s1aaaaaa", Name: "Иван"}}}},
		"taskrobots": {Data: map[string]any{"Project": project, "Kinds": tasks.RobotKinds, "Statuses": tasks.RobotStatuses, "Users": []tasks.User{user},
			"Stages": []tasks.Stage{{ID: "s1", Code: "new", Color: "00C4FB"}, {ID: "s2", Title: "Проверка", Color: "FFAB00"}, {ID: "s3", Code: "finish", Color: "75D900"}},
			"Robots": map[string][]tasks.Robot{"s1": {{ID: "r1", Kind: "notify", Active: true, Params: tasks.RobotParams{Text: "Новая: {title}", To: "creator"}},
				{ID: "r2", Kind: "task", Active: true, Params: tasks.RobotParams{Text: "Отчёт", To: "u1", Days: 2, Sub: true}},
				{ID: "r3", Kind: "stage", Params: tasks.RobotParams{Stage: "s2", Delay: 2, Unit: "hour"}},
				{ID: "r4", Kind: "status", Active: true, Params: tasks.RobotParams{Status: tasks.ActionComplete}},
				{ID: "r5", Kind: "deadline", Active: true, Params: tasks.RobotParams{Days: 3}},
				{ID: "r6", Kind: "auditor", Active: true, Params: tasks.RobotParams{To: "u1"}}}},
			"TriggerKinds": tasks.TriggerKinds, "TriggerStatuses": tasks.TriggerStatuses,
			"Triggers": map[string][]tasks.Trigger{"s2": {{ID: "g1", Kind: "status", Status: tasks.StatusInProgress}, {ID: "g2", Kind: "overdue"}}}}},
		"board": {Data: map[string]any{"Project": project, "Colors": tasks.StageColors, "FinishLimit": 1, "Columns": []boardColumn{
			{StageColumn: tasks.StageColumn{Stage: tasks.Stage{ID: "s1", Code: "new", Color: "00C4FB"}, Total: 1},
				Cards: []boardCard{{Task: task, CanMove: true, CardInfo: tasks.CardInfo{ChecklistDone: 1, ChecklistTotal: 2, Files: 1, Comments: 3}}}},
			{StageColumn: tasks.StageColumn{Stage: tasks.Stage{ID: "s2", Title: "На проверке", Color: "FFAB00"}}},
			{StageColumn: tasks.StageColumn{Stage: tasks.Stage{ID: "s3", Code: "finish", Color: "75D900"}, Total: 5},
				Cards: []boardCard{{Task: tasks.Task{ProjectKey: "GO", Number: 2, Title: "Готово", Status: tasks.StatusAwaitingControl, Priority: 2}}}},
		}}},
	}
	bundle, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	collab := tasks.Project{ID: "c1", Key: "CL1", Name: "Клиент", MyRole: "member", Kind: "collab", Visibility: "members", PhotoKey: "k"}
	cases["collabs"] = page{Data: map[string]any{"CanCreate": true, "Messenger": true, "Rooms": map[string]string{"c1": "!c"},
		"List": []tasks.Collab{{Project: collab, Members: 3, Guests: 1, CollabCounters: tasks.CollabCounters{Overdue: 2, Comments: 1}}, {Project: tasks.Project{ID: "c2", Key: "CL2", Name: "Без чата", MyRole: "owner", Kind: "collab"}, Members: 1}}}}
	invite := tasks.GuestInvite{ID: "i1", Token: "tok", Kind: "link", CreatedBy: "Анна", CreatedAt: now, Expires: later, Project: "Клиент", ProjectKey: "CL1"}
	cases["join"] = page{Data: map[string]any{"Invite": invite, "Signup": true}}
	extra := map[string]page{ // more data for a page: name → the page (before «/»)
		"marketing/segments": {Data: map[string]any{"Tab": "segments", "Segments": []marketing.Segment{mktSeg, {ID: "g2", Name: "Выставка", Entity: "list", Emails: "a@x", UpdatedAt: now}},
			"Counts": map[string]int{"g1": 3}, "Me": "u1", "Admin": false, "Entities": marketing.Entities}},
		"marketing/segment-edit": {Data: map[string]any{"Tab": "segments", "Segments": []marketing.Segment{mktSeg}, "Counts": map[string]int{}, "Me": "u1",
			"Entities": marketing.Entities, "Edit": &marketing.Segment{ID: "g3", Name: "Лиды", Entity: "lead", Filter: marketing.Filter{Stage: "s1", Semantic: "process"}},
			"Users": []tasks.User{user}, "Dicts": map[string][]crm.DictItem{"source": {{Dict: crm.Dict{ID: "src1", Name: "Сайт"}}}},
			"Stages": []crm.Stage{{ID: "s1", Code: "new"}, {ID: "s2", Name: "Звонок"}}}},
		"marketing/stoplist": {Query: "x", Data: map[string]any{"Tab": "stoplist", "Q": "x", "Stop": []marketing.Stopped{{Email: "x@y", CampaignTitle: "Акция", AddedBy: "Анна", At: now}}}},
		"marketing/calls": {Data: map[string]any{"Tab": "calls", "Calls": []marketing.CallList{{ID: "k1", Title: "Выставка", Status: "draft", SegmentName: "Все контакты",
			Deadline: &now}, {ID: "k2", Title: "Опрос", Status: "started", Total: 10, Done: 4, Deadline: &now, AuthorName: "Анна"}}}},
		"mktcalls/started": {Title: "Опрос", Data: map[string]any{"Tab": "calls", "C": marketing.CallList{ID: "k2", Title: "Опрос", Status: "started", Total: 3, Done: 1,
			Deadline: &now, StartedAt: &now, Script: "Вопрос 1\nВопрос 2"}, "Mine": true, "Staff": map[string]bool{},
			"Items": []marketing.CallItem{{Entity: "contact", EntityID: "x1", Name: "Иван", Phone: "+7 900", ResponsibleName: "Анна", ActivityID: "a1", DoneAt: &now},
				{Entity: "company", EntityID: "x2", Name: "ООО", Phone: "+7 901", ActivityID: "a2"}, {Entity: "lead", EntityID: "x3", Name: "Лид", Phone: "1"}},
			"Done": "", "Page": 1, "Pages": []int{1}}},
		"mktcampaign/ab": {Title: "Акция", Data: map[string]any{"Tab": "campaigns", "Mine": true, "Me": auth.Session{UserID: "u1"},
			"C": marketing.Campaign{ID: "c3", Title: "АБ", Subject: "Тема A", Body: "<p>A</p>", Status: "sending", StartedAt: &now, Total: 10, Sent: 4,
				ABSubject: "Тема B", ABShare: 20, ABHours: 4, ABMetric: "opened"},
			"AB": map[string]marketing.ABResult{"a": {Sent: 2, Opened: 1}, "b": {Sent: 2, Opened: 2, Clicked: 1}}, "Letter": template.HTML("<p>A</p>"),
			"LetterB": template.HTML("<p>A</p>"), "Recipients": []marketing.Addressee{{Email: "ab@x", Status: "held"}, {Email: "a@x", Status: "sent", Variant: "a"}},
			"Status": "", "Page": 1, "Pages": []int{1}}},
		"mktcampaign/sent": {Title: "Акция", Data: map[string]any{"Tab": "campaigns", "C": mktSent, "Segments": []marketing.Segment{mktSeg}, "Mine": false,
			"In": map[string]bool{"g1": true}, "Out": map[string]bool{}, "Me": auth.Session{UserID: "u2"}, "Letter": template.HTML("<p>Привет</p>"),
			"Recipients": []marketing.Addressee{{Email: "ivan@x", Name: "Иван", Status: "sent", SentAt: &now, OpenedAt: &now, ClickedAt: &now},
				{Email: "bad@x", Status: "error", Error: "550"}, {Email: "maria@x", Status: "skipped", Error: "stoplist"}}, "Status": "", "Page": 1, "Pages": []int{1, 2}}},
		"mktunsub/done": {Data: map[string]any{"Token": "t1", "Email": "ivan@x", "Campaign": "Акция", "Done": true}},
		"mktunsub/gone": {Data: map[string]any{"Token": "t1", "Gone": true}},
		"mktunsub/test": {Data: map[string]any{"Token": "test", "Test": true}},

		"join/gone":      {Data: map[string]any{"Invite": tasks.GuestInvite{}, "Gone": true}},
		"join/signed-in": {Data: map[string]any{"Invite": invite, "SignedIn": true}},
		"join/no-signup": {Data: map[string]any{"Invite": invite}},
		"absences/list": {Data: map[string]any{"View": "list", "Form": company.Absence{From: now, To: now}, "Back": "/company/absences?view=list",
			"List":  []company.Absence{{ID: "a1", UserID: "u1", UserName: "Анна", Kind: "trip", From: now, To: now, ActingID: "u2", ActingName: "Пётр", Rights: []string{"view"}}},
			"Users": []tasks.User{user}, "Kinds": company.AbsenceKinds, "Rights": company.ActingRights, "Me": "u1", "Today": now}},
		"project/collab-guests": {Data: map[string]any{"Project": tasks.Project{ID: "c1", Key: "CL1", Name: "Клиент", MyRole: "owner", Kind: "collab"}, "Messenger": true,
			"Members": []tasks.Member{{User: user, Role: "owner"}}, "Users": []tasks.User{user}, "Me": "u1", "DefaultDeadline": now,
			"CanInviteGuests": true, "Now": now, "Mailbox": true, "JoinLinks": map[string]string{"i1": "http://go24/join/tok", "i2": "http://go24/join/t2"},
			"GuestInvites": []tasks.GuestInvite{invite,
				{ID: "i2", Token: "t2", Kind: "phone", Phone: "+79991234567", Name: "Боб", CreatedBy: "Анна", Expires: later},
				{ID: "i3", Kind: "email", Email: "c@x.ru", CreatedBy: "Анна", Expires: later, Uses: 1, UsedBy: "Клиент"},
				{ID: "i4", Kind: "link", CreatedBy: "Анна", Expires: later, Revoked: true, Uses: 2},
				{ID: "i5", Kind: "link", CreatedBy: "Анна", Expires: now.Add(-time.Hour)}}}},
		"collabs/guest": {Guest: true, CollabCounters: tasks.CollabCounters{Overdue: 1, Comments: 2}, CollabRooms: "!c !d", Data: map[string]any{"CanCreate": false, "Rooms": map[string]string{}, "List": []tasks.Collab{{Project: collab, Members: 3, Guests: 1}}}},
		"profile/guest": {Guest: true, Data: map[string]any{"Guest": true, "Tab": "", "P": company.Profile{ID: "u2", Name: "Гость Петров", Guest: true}, "Tasks": []tasks.Task{task}, "Me": false}},
		"collabs/empty": {Data: map[string]any{"CanCreate": false, "Rooms": map[string]string{}, "List": []tasks.Collab{}}},
		"project/collab": {Data: map[string]any{"CanInvite": false, "InvitePerm": "managers", "Project": collab, "Tasks": []tasks.Task{task}, "ChatRoom": "!c", "Messenger": true,
			"Members": []tasks.Member{{User: user, Role: "owner"}, {User: tasks.User{ID: "g1", Name: "Гость", Guest: true}, Role: "member"}}, "Users": []tasks.User{user}, "Me": "u1", "DefaultDeadline": now}},
		"project/collab-manage": {Data: map[string]any{"Project": tasks.Project{ID: "c1", Key: "CL1", Name: "Клиент", MyRole: "owner", Kind: "collab"}, "Messenger": true,
			"Members": []tasks.Member{{User: user, Role: "owner"}, {User: tasks.User{ID: "g1", Name: "Гость", Guest: true}, Role: "member"}}, "Users": []tasks.User{user}, "Me": "u1", "DefaultDeadline": now,
			"CanAddMembers": true, "CanDelete": true,
			"Settings": tasks.CollabSettings{InvitePerm: "managers", GuestInvitePerm: "owner", ChatHistory: true, GuestFiles: true, StartTool: "tasks"}, "StartTools": tasks.StartTools,
			"Log": []tasks.CollabLogEntry{{At: now, Actor: "Анна", Action: "created", Detail: "Клиент"},
				{At: now, Actor: "Анна", Target: "Гость", TargetGuest: true, Action: "member_added", Detail: "member"},
				{At: now, Actor: "Гость", Target: "Гость", TargetGuest: true, Action: "joined", Detail: "link"},
				{At: now, Actor: "Анна", Action: "settings", Detail: "invite_perm=members,chat_history=off,guests_tasks=off"},
				{At: now, Actor: "Анна", Action: "invite_created", Detail: "email c@x.ru"}, {At: now, Actor: "Анна", Action: "archived"}}}},
		"project/collab-archived": {Data: map[string]any{"Project": tasks.Project{ID: "c1", Key: "CL1", Name: "Клиент", MyRole: "owner", Kind: "collab", Archived: true}, "Messenger": true,
			"Members": []tasks.Member{{User: user, Role: "owner"}}, "Users": []tasks.User{user}, "Me": "u1", "DefaultDeadline": now, "CanDelete": true,
			"Settings": tasks.CollabSettings{InvitePerm: "managers", GuestInvitePerm: "managers"}}},
		"collabs/archive": {Data: map[string]any{"Archived": true, "Rooms": map[string]string{}, "List": []tasks.Collab{}}},
		"calendar/collab": {Data: map[string]any{"Month": now, "Prev": "2026-09", "Next": "2026-11", "Me": "u1", "View": "month", "Day": now, "Project": collab,
			"Weeks": [][]calDay{{{Date: now, InMonth: true, Items: []calendar.Event{{ID: "e1", Kind: "event", Title: "Созвон", ProjectKey: "CL1", Start: now, End: later}}}}}}},
		"calendar/collab-week": {Data: map[string]any{"View": "week", "Cols": []calCol{{Date: now}}, "Hours": []int{9}, "HourPx": 48, "GridPx": 720,
			"Month": now, "Day": now, "End": now, "Prev": "2026-10-01", "Next": "2026-10-15", "Project": collab}},
		"im/collab": {Data: map[string]any{"Room": "!c", "Tab": "collab", "Element": "",
			"Rooms":   []matrixsync.Room{{ID: "!c", Name: "Клиент", Kind: "collab", Unread: 1}},
			"Current": matrixsync.Room{ID: "!c", Name: "Клиент", Kind: "collab"}, "CollabInvite": "guests",
			"Collab": tasks.Collab{Project: collab, Members: 3, Guests: 1, CollabCounters: tasks.CollabCounters{Overdue: 2, Comments: 1}}}},
		"im/collabchat": {Data: map[string]any{"Room": "!g", "Tab": "collab", "Element": "", "CollabMain": "!c", "CollabChatRemove": true,
			"Rooms": []matrixsync.Room{{ID: "!c", Name: "Клиент", Kind: "collab"}, {ID: "!g", Name: "Дизайн", Kind: "group", CollabID: "c1", CollabKey: "CL1", CollabName: "Клиент"},
				{ID: "!o", Name: "Своя", Kind: "group"}},
			"Current":     matrixsync.Room{ID: "!g", Name: "Дизайн", Kind: "group", CollabID: "c1", CollabKey: "CL1", CollabName: "Клиент"},
			"Group":       &matrixsync.Group{Owner: true, Members: []matrixsync.Member{{ID: "u1", Name: "Анна", Owner: true, Me: true}}},
			"CollabChats": []matrixsync.Room{{ID: "!g", Name: "Дизайн", Kind: "group"}}, "CollabChatAdd": []matrixsync.Room{{ID: "!o", Name: "Своя", Kind: "group"}},
			"Collab":      tasks.Collab{Project: collab, Members: 3}}},
		"im/owngroup": {Data: map[string]any{"Room": "!o", "Element": "", "Rooms": []matrixsync.Room{{ID: "!o", Name: "Своя", Kind: "group"}},
			"Current":       matrixsync.Room{ID: "!o", Name: "Своя", Kind: "group"},
			"Group":         &matrixsync.Group{Owner: true, Members: []matrixsync.Member{{ID: "u1", Name: "Анна", Owner: true, Me: true}}},
			"CollabTargets": []tasks.Collab{{Project: collab}}}},
		"disk/collab": {Data: map[string]any{"Drive": drive.Drive{Kind: "project", OwnerID: "c1", ProjectKey: "CL1", Name: "Клиент", Collab: true, CanWrite: true}, "Folder": "", "Me": "u1"}},
		"mail/compose": {Data: map[string]any{"FolderID": "f1", "New": true, "Pane": "right", "Panes": mailPanes, "Mode": "all",
			"FavSet": map[string]bool{},
			"Boxes":  []mailBox{{Mailbox: mail.Mailbox{ID: "b1", Email: "anna@x.ru"}}}, "Box": &mail.Mailbox{ID: "b1", Email: "anna@x.ru"},
			"Compose": &mailComposeData{Draft: mail.Draft{ID: "d1", MailboxID: "b2", To: `"Клиент" <c@x.ru>`, CC: "e@x.ru", Subject: "Re: Заказ", Mode: "all",
				ReplyTo: "m1", Importance: 1, Receipt: true, Sign: true, SendAt: &later, Scheduled: true, Error: "err.mail_send"}, SMIME: true,
				HTML: "<p>Привет</p>", Files: []mail.File{{ID: "f1", Name: "счёт.pdf", Size: 2048}},
				Boxes: []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}, {ID: "b2", Email: "info@x.ru"}}, SigOf: map[string]mail.Signatures{"b1": {New: "s1"}},
				Signatures: []mail.Snippet{{ID: "s1", Name: "Основная", HTML: "<p>Анна</p>"}}, Templates: []mail.Snippet{{ID: "t1", Name: "Спасибо", HTML: "Спасибо!"}},
				Prefs: mail.Prefs{UndoSeconds: 5, Font: "Arial", FontSize: "11pt"}, Fonts: mail.Fonts, FontSizes: mail.FontSizes,
				Schedule: sendPresets(now), Importance: []int{1, 0, -1}, FolderID: "f1", ShowCC: true},
			"Categories": mailCats, "CatByID": catMap(mailCats), "Colors": mail.Colors, "SnoozePresets": snoozePresets, "FlagPresets": flagPresets, "RemindPresets": remindPresets,
			"Folder": mail.Folder{ID: "f1", MailboxID: "b1", Name: "Junk", Role: "junk", Selectable: true}}},
		"mail/signatures": {Data: map[string]any{"Settings": &mailSettingsData{Tab: "signatures", Tabs: mailSettingTabs, Edit: "s1",
			Boxes: []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}}, SigOf: map[string]mail.Signatures{"b1": {New: "s1", Reply: ""}},
			Signatures: []mail.Snippet{{ID: "s1", Name: "Основная", HTML: "<p>Анна</p>"}}, Fonts: mail.Fonts, FontSizes: mail.FontSizes}}},
		"mail/templates": {Data: map[string]any{"Settings": &mailSettingsData{Tab: "templates", Tabs: mailSettingTabs, Fonts: mail.Fonts, FontSizes: mail.FontSizes}}},
		"mail/compose-settings": {Data: map[string]any{"Settings": &mailSettingsData{Tab: "compose", Tabs: mailSettingTabs, Undo: []int{0, 5, 10},
			Prefs: mail.Prefs{UndoSeconds: 5, Font: "Arial"}, Fonts: mail.Fonts, FontSizes: mail.FontSizes}}},
		"mail/crm-settings": {Data: map[string]any{"Settings": &mailSettingsData{Tab: "crm", Tabs: mailSettingTabs,
			Boxes: []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}}, CRMOf: map[string]mail.BoxCRM{"b1": {Bind: true}}}}},
		"mail/security-settings": {Data: map[string]any{"Settings": &mailSettingsData{Tab: "security", Tabs: mailSettingTabs, Security: mail.Security{Sign: true},
			Certs: []mail.Cert{{ID: "c1", Email: "anna@x.ru", Subject: "CN=Анна", Issuer: "CN=CA", SHA1: "AB12", NotAfter: now}}}}},
	}
	// the mail with the calendar, tasks and the CRM; shared mailboxes
	inv := mail.Invite{Method: "REQUEST", UID: "u1", Summary: "Планёрка", Location: "Зал", Start: now, End: later, Repeat: "weekly",
		Organizer: mail.Person{Email: "boss@x.ru", Name: "Босс"}, Attendees: []mail.Person{{Email: "anna@x.ru", Status: "accepted"}, {Email: "e@x.ru", Status: "needs-action"}}}
	md := cases["mail"].Data.(map[string]any)
	md["Sender"] = &senderCard{Addr: "c@x.ru", Name: "Клиент", Employee: &senderEmployee{ID: "u2", Name: "Пётр", Position: "Инженер", Departments: []string{"ИТ"}}}
	md["LetterTasks"] = []mail.TaskRef{{ID: "t1", Key: "GO-3", Title: "Ответить", Status: "pending"}}
	md["LetterCRM"] = []mail.CRMRef{{LinkID: "k1", Entity: "deal", EntityID: "d1", Title: "Поставка"}}
	md["CRMCan"] = map[string]bool{"lead": true, "deal": true, "contact": true, "company": false}
	md["Shares"] = []mail.Share{{MailboxID: "b9", Email: "sales@x.ru", Name: "Продажи", Open: 2, Level: "send"}}
	md["Invite"] = &invCard{Inv: inv, Answers: mail.Answers, State: mail.InviteState{Status: "tentative", EventID: "e1"}, Update: true,
		Me: &inv.Attendees[0], Conflicts: []calendar.Event{{ID: "e2", Kind: "event", Title: "Созвон", Start: now, End: later}}}
	withMail := func(k string, v any) map[string]any {
		m := maps.Clone(md)
		m[k] = v
		return m
	}
	extra["register/done"] = page{Data: map[string]any{"Open": true, "Done": "ok"}}
	extra["register/closed"] = page{Data: map[string]any{}}
	extra["mail/invite-cancel"] = page{Data: withMail("Invite", &invCard{Inv: inv, Cancel: true, State: mail.InviteState{Status: "accepted", EventID: "e1"}})}
	reply := inv
	reply.Method, reply.Comment = "REPLY", "Опоздаю"
	extra["mail/invite-reply"] = page{Data: withMail("Invite", &invCard{Inv: reply, Reply: &mail.Person{Email: "e@x.ru", Name: "Ева", Status: "declined"}, EventID: "e1"})}
	extra["mail/invite-free"] = page{Data: withMail("Invite", &invCard{Inv: inv, Answers: mail.Answers})}
	extra["mail/sender-crm"] = page{Data: withMail("Sender", &senderCard{Addr: "c@x.ru",
		Contacts:  []crm.Contact{{ID: "x1", Name: "Иван", Position: "Директор", Phone: "+7"}},
		Companies: []crm.Company{{ID: "co1", Title: "ООО Ромашка", Phone: "+7"}},
		Leads:     []crm.Lead{{ID: "l1", Title: "Заявка", Stage: crm.Stage{Code: "lead_new"}}},
		Deals:     []crm.Deal{{ID: "d1", Title: "Поставка", Amount: 100, Currency: "RUB", Stage: crm.Stage{Name: "Своя"}}},
		Tasks:     []tasks.Task{task}, Recent: []mail.Message{{ID: "m7", FolderID: "f1", Subject: "Ранее", SentAt: now}}})}
	extra["mail/sender-unknown"] = page{Data: withMail("Sender", &senderCard{Addr: "c@x.ru"})}
	share := mail.Share{MailboxID: "b9", Email: "sales@x.ru", Name: "Продажи", OwnerID: "u2", OwnerName: "Анна", DepartmentID: "d1", DepartmentName: "Продажи",
		DeptLevel: "read", Level: "manage", People: []mail.SharePerson{{UserID: "u3", Name: "Пётр", Level: "send"}}, Error: "err.mail_login"}
	shareURL := func(extra ...string) string { return sharedURL("b9", url.Values{}, extra...) }
	thread := []mail.Message{{ID: "m1", FolderID: "f1", FolderRole: "inbox", FromName: "Клиент", FromAddr: "c@x.ru", To: "sales@x.ru", CC: "e@x.ru", Subject: "Вопрос", Body: "Есть ли\nв наличии?", SentAt: now},
		{ID: "m2", FolderID: "f2", FolderRole: "sent", FromAddr: "sales@x.ru", To: "c@x.ru", Subject: "Re: Вопрос", Body: "Да", SentAt: now}}
	cases["mailshared"] = page{Data: map[string]any{"Share": share, "Statuses": mail.TicketStatuses, "Levels": mail.ShareLevels, "Q": url.Values{}, "Status": "", "Who": "me",
		"Me": "u1", "CanSend": true, "CanManage": true, "Owner": false, "URL": shareURL,
		"People": []mail.SharePerson{{UserID: "u1", Name: "Я", Level: "manage"}, {UserID: "u3", Name: "Пётр", Level: "send"}},
		"Tickets": []mail.Ticket{{Thread: "t1", LetterID: "m1", Subject: "Вопрос", FromName: "Клиент", LastAt: now, Count: 2, Unread: 1, Status: "progress", Reopened: true, AssigneeID: "u3", AssigneeName: "Пётр"},
			{Thread: "t2", LetterID: "m3", FromAddr: "z@x.ru", LastAt: now, Count: 1, Status: "new"}},
		"Ticket": mail.Ticket{Thread: "t1", LetterID: "m1", Status: "progress", AssigneeID: "u3", AssigneeName: "Пётр"}, "Thread": thread,
		"Files": map[string][]mail.File{"m1": {{ID: "f1", Name: "прайс.pdf", Size: 2048}}}, "Docs": map[string]string{"m2": mailDoc("<p>Да</p>")},
		"Answer": thread[0], "ReplySubject": "Re: Вопрос"}}
	since := now.AddDate(0, -1, 0)
	cases["mailexport"] = page{Data: map[string]any{"Boxes": []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}}, "Box": "b1", "Folder": "f1",
		"Folders": []mail.Folder{{ID: "f1", MailboxID: "b1", Name: "INBOX", Role: "inbox", Selectable: true, Total: 3},
			{ID: "f2", MailboxID: "b1", Name: "INBOX/Счета", Delim: "/", Selectable: true}, {ID: "f3", Name: "Мои", Local: true, Selectable: true}},
		"Formats": mail.ExportFormats, "Busy": true, "Exports": []mail.Export{
			{ID: "e1", Email: "anna@x.ru", Format: "mbox", Status: "done", Letters: 12, Size: 4096, URL: "/disk?folder=x", CreatedAt: now, Since: &since, Until: &now},
			{ID: "e2", Email: "anna@x.ru", Format: "eml", Status: "running", Letters: 3, Folders: []string{"f1"}, CreatedAt: now},
			{ID: "e3", Email: "anna@x.ru", Format: "eml", Status: "failed", Error: "Нет связи", CreatedAt: now},
			{ID: "e4", Email: "anna@x.ru", Format: "mbox", Status: "queued", CreatedAt: now}}}}
	extra["mailexport/none"] = page{Data: map[string]any{"Formats": mail.ExportFormats}}
	cases["mailimport"] = page{Data: map[string]any{"Boxes": []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}}, "Box": "b1", "Folder": "f1",
		"Folders": []mail.Folder{{ID: "f1", MailboxID: "b1", Name: "INBOX", Role: "inbox", Selectable: true, Total: 3},
			{ID: "f2", MailboxID: "b1", Name: "INBOX/Счета", Delim: "/", Selectable: true}},
		"Files": []importDriveFile{{ID: "d1", Name: "Почта.pst", Size: 1 << 20}}, "File": "d1", "Busy": true, "Imports": []mail.FileImport{
			{ID: "i1", Email: "anna@x.ru", Name: "Почта.pst", Format: "pst", Status: "done", ImportCount: mail.ImportCount{Letters: 12, Same: 2, Skipped: 1, Folders: 3}, CreatedAt: now},
			{ID: "i2", Email: "anna@x.ru", Name: "Inbox.mbox", Format: "mbox", Status: "running", FolderID: "f1", ImportCount: mail.ImportCount{Letters: 3}, CreatedAt: now},
			{ID: "i3", Email: "anna@x.ru", Name: "old.pst", Format: "pst", Status: "failed", Error: "Файл повреждён", CreatedAt: now},
			{ID: "i4", Email: "anna@x.ru", Name: "a.eml", Format: "eml", Status: "queued", CreatedAt: now}}}}
	extra["mailimport/none"] = page{Data: map[string]any{}}
	extra["mailshared/access"] = page{Data: map[string]any{"Share": share, "Tab": "access", "CanManage": true, "Owner": true, "Levels": mail.ShareLevels,
		"Depts": []deptRef{{ID: "d1", Name: "Продажи"}}, "Users": []tasks.User{user}, "Statuses": mail.TicketStatuses, "Q": url.Values{}, "URL": shareURL}}
	extra["mailshared/index"] = page{Data: map[string]any{"Shares": []mail.Share{share}, "Own": []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}},
		"Depts": []deptRef{{ID: "d1", Name: "Продажи"}}, "Levels": mail.ShareLevels}}
	extra["mailshared/empty"] = page{Data: map[string]any{"Share": mail.Share{MailboxID: "b9", Email: "sales@x.ru", Level: "read", DeptLevel: "read"}, "Statuses": mail.TicketStatuses,
		"Levels": mail.ShareLevels, "Q": url.Values{}, "URL": shareURL}}
	ed := maps.Clone(cases["event"].Data.(map[string]any))
	ed["Externals"] = []calendar.External{{Email: "e@x.ru", Name: "Ева", Status: "tentative"}, {Email: "z@x.ru", Status: "invited"}}
	ed["MailBoxes"], ed["MailboxID"], ed["ExternalText"], ed["ReplyTo"] = []mail.Mailbox{{ID: "b1", Email: "anna@x.ru"}}, "b1", "e@x.ru, z@x.ru", "m1"
	ed["FromLetter"] = mail.Message{ID: "m1", FromName: "Клиент", Subject: "Заказ"}
	extra["event/externals"] = page{Data: ed}
	cd := maps.Clone(cases["crmcard"].Data.(map[string]any))
	cd["Timeline"] = []crm.Event{{ID: 5, AuthorName: "Анна", Kind: "mail", Body: "k1", At: now, Mail: &crm.Mail{LinkID: "k1", LetterID: "m1", OwnerID: "u1",
		FromAddr: "c@x.ru", FromName: "Клиент", To: "anna@x.ru", Subject: "Заказ", Body: "Нужно\n10", SentAt: now}},
		{ID: 6, AuthorName: "Анна", Kind: "mail", Body: "k2", At: now, Mail: &crm.Mail{LinkID: "k2", Outgoing: true, FromAddr: "anna@x.ru", To: "c@x.ru", SentAt: now}}}
	extra["crmcard/mail"] = page{Data: cd}
	ud := maps.Clone(cases["crmcard"].Data.(map[string]any)) // «UTM-метки», of the lead's
	ud["UTM"], ud["UTMInherited"] = marketing.UTM{Source: "yandex", Campaign: "autumn"}, true
	extra["crmcard/utm"] = page{Data: ud}
	ue := maps.Clone(ud)
	ue["Edit"] = true
	extra["crmcard/utm-edit"] = page{Data: ue}
	adSrc := []marketing.AdSource{{ID: "a1", Name: "Яндекс Директ", UTM: []string{"yandex"}, Color: "#ff0000"}, {ID: "a2", Name: "VK", CRMSource: "s2"}}
	extra["marketing/ads"] = page{Data: map[string]any{"Tab": "ads", "Sources": adSrc, "Admin": true, "Me": "u1", "Today": now, "From": time.Time{}, "To": time.Time{},
		"Expenses": []marketing.Expense{{ID: "e1", SourceID: "a1", SourceName: "Яндекс Директ", Day: now, Amount: 1000.5, Currency: "RUB", Campaign: "autumn", CreatedBy: "u1"}},
		"Dicts": map[string][]crm.DictItem{}, "Currencies": []crm.Currency{{Code: "RUB", Base: true}}, "Edit": &adSrc[0]}}
	camp := &marketing.Channel{Name: "autumn", Expenses: 1000, Leads: 2, Won: 1, Revenue: 3000}
	extra["marketing/analytics"] = page{Data: map[string]any{"Tab": "analytics", "From": &now, "To": &now, "Report": marketing.Report{Currency: "RUB",
		Channels: []*marketing.Channel{{ID: "a1", Name: "Яндекс Директ", Expenses: 1500, Leads: 3, Deals: 2, Won: 1, Revenue: 3000, Campaigns: []*marketing.Channel{camp, {}}},
			{ID: "a2", Name: "VK", Expenses: 300}, {Leads: 1}},
		Total: marketing.Channel{Expenses: 1800, Leads: 4, Deals: 2, Won: 1, Revenue: 3000}}}}
	extra["marketing/analytics-empty"] = page{Data: map[string]any{"Tab": "analytics", "From": &now, "To": &now, "Report": marketing.Report{}}}
	rd := maps.Clone(cases["crmcard"].Data.(map[string]any)) // a deal repeated on schedule
	until := now.AddDate(1, 0, 0)
	rd["Kind"], rd["Entity"], rd["CanWrite"] = "deals", "deal", true
	rd["Item"] = crm.Deal{ID: "d1", Title: "Обслуживание", Currency: "RUB", Amount: 5000, Stage: crm.Stage{ID: "s1", Code: "new"}, CreatedAt: now,
		DealType: crm.Dict{ID: "t1", Code: "SERVICE"}, Origin: crm.Dict{ID: "s2", Name: "Партнёры"}}
	rd["Recurrence"] = &crm.Recurrence{DealID: "d1", Unit: "month", Every: 1, Start: now, Next: now, Until: &until, Limit: 12, Count: 3}
	rd["RecurUnits"], rd["Today"] = crm.RecurUnits, now.Format("2006-01-02")
	rd["Timeline"] = []crm.Event{{ID: 7, AuthorName: "Анна", Kind: "recurred", Body: "d2|Обслуживание", At: now}}
	extra["crmcard/recurring"] = page{Data: rd}
	rl := maps.Clone(rd) // «Связи»: an element with its parents, a deal with its children
	parent := crm.Deal{ID: "d9", Title: "Большая сделка", Stage: crm.Stage{ID: "s1", Code: "new", Color: "39A8EF"}}
	rl["Item"] = crm.Deal{ID: "e1", Title: "Договор", TypeID: "t1", Currency: "RUB", Stage: crm.Stage{ID: "s1", Code: "sp_new"},
		Company: crm.Ref{ID: "y1", Name: "ООО"}, Contacts: []crm.Ref{{ID: "x1", Name: "Иван"}}, CreatedAt: now}
	rl["ParentSlots"] = []crmParentSlot{{Kind: "", Parent: &parent, Options: []crm.Deal{parent, {ID: "d8", Title: "Другая"}}},
		{Kind: "t2", Name: "Поставки", Options: []crm.Deal{{ID: "e2", Title: "Поставка"}}}}
	rl["ChildGroups"] = []crmChildGroup{{crmSmart: crmSmart{Type: crm.Type{ID: "t2", Name: "Поставки", Color: "39A8EF"},
		Items: []crm.Deal{{ID: "e3", Title: "Поставка 1", Amount: 100, Currency: "RUB", Stage: crm.Stage{ID: "s2", Code: "sp_preparation"}}}}, Pipeline: "p2"},
		{crmSmart: crmSmart{Type: crm.Type{ID: "t3", Name: "Акты"}}, Pipeline: "p3"}}
	rl["Timeline"] = []crm.Event{{ID: 8, AuthorName: "Анна", Kind: "bound", Body: "Большая сделка", At: now}, {ID: 9, AuthorName: "Анна", Kind: "unbound", Body: "Другая", At: now}}
	delete(rl, "Recurrence")
	extra["crmcard/relations"] = page{Data: rl}
	ce := maps.Clone(cases["crmcard"].Data.(map[string]any))
	ce["Edit"] = true
	extra["crmcard/edit"] = page{Data: ce}
	for _, x := range []struct {
		kind, entity string
		item         any
	}{{"deals", "deal", rd["Item"]}, {"contacts", "contact", crm.Contact{ID: "x1", Name: "Иван", ContactType: crm.Dict{ID: "c1", Code: "CLIENT"}}},
		{"companies", "company", crm.Company{ID: "y1", Title: "ООО", Industry: crm.Dict{ID: "i1", Code: "IT"}}}} {
		e := maps.Clone(ce)
		e["Kind"], e["Entity"], e["Item"] = x.kind, x.entity, x.item
		delete(e, "Stages")
		extra["crmcard/edit-"+x.entity] = page{Data: e}
	}
	cl := maps.Clone(cases["crm"].Data.(map[string]any))
	min := 1000.0
	cl["View"], cl["Query"], cl["F"] = "list", "view=list&semantic=process", crm.Filter{Semantic: "process", Sort: "amount", AmountMin: &min, From: &now, Source: "s1"}
	cl["Dicts"] = map[string][]crm.DictItem{"source": {{Dict: crm.Dict{ID: "s1", Code: "CALL"}}, {Dict: crm.Dict{ID: "s2", Name: "Партнёры"}}}}
	extra["crm/list"] = page{Data: cl}
	ll := maps.Clone(cl)
	delete(ll, "Pipeline")
	ll["Kind"], ll["Items"] = "leads", []crm.Lead{{ID: "l1", Title: "Заявка", Name: "Иван", Phone: "+7", Stage: crm.Stage{ID: "s1", Code: "lead_new"}, CreatedAt: now, Repeat: true}}
	ll["F"] = crm.Filter{Repeat: "1"}
	extra["crm/leads"] = page{Data: ll}
	ct := maps.Clone(cases["crm"].Data.(map[string]any))
	ct["Type"], ct["Types"] = &crm.Type{ID: "t1", Name: "Договоры", Color: "FFA900"}, []crm.Type{{ID: "t1", Name: "Договоры", Color: "FFA900", Items: 1}}
	extra["crm/type"] = page{Data: ct}
	cc := maps.Clone(cases["crmcard"].Data.(map[string]any))
	cc["Kind"], cc["Entity"], cc["Item"] = "contacts", "contact", crm.Contact{ID: "x1", Name: "Иван"}
	delete(cc, "Stages")
	cc["SmartItems"] = []crmSmart{{Type: crm.Type{ID: "t1", Name: "Договоры", Color: "FFA900"}, Items: []crm.Deal{{ID: "d2", Title: "Договор 7", TypeID: "t1", Stage: crm.Stage{Code: "sp_new"}}}}}
	extra["crmcard/smart"] = page{Data: cc}
	cr := maps.Clone(cc)
	cr["CanWrite"], cr["ReqPresets"], cr["NewRequisite"] = true, crm.RequisitePresets, crm.Requisite{Preset: "person", Name: "Иван"}
	cr["Requisites"] = []crm.Requisite{{ID: "q1", Preset: "ip", Name: "ИП Иванов", INN: "770123456789", OGRN: "304770000000001", Director: "Иванов И. И.",
		Address: "Москва", Bank: "Банк", BIK: "044525225", Account: "40802810000000000001", Default: true}, {ID: "q2", Preset: "person", Name: "Иван"}}
	extra["crmcard/requisites"] = page{Data: cr}
	ib := maps.Clone(cases["crminvoice"].Data.(map[string]any))
	ib["Buyer"] = &crm.Requisite{Preset: "org", Name: "ООО «Ромашка»", INN: "7701234567", KPP: "770101001"}
	extra["crminvoice/buyer"] = page{Data: ib}
	td := maps.Clone(cases["task"].Data.(map[string]any))
	td["Letters"], td["Me"] = []mail.TaskLetter{{LetterID: "m1", OwnerID: "u1", Subject: "Заказ", FromName: "Клиент", SentAt: now}, {FromAddr: "c@x.ru", SentAt: now}}, "u1"
	extra["task/letters"] = page{Data: td}
	extra["newtask/letter"] = page{Data: map[string]any{"Projects": []tasks.Project{project}, "Form": taskForm{New: true, Action: "/tasks/new", Cancel: "/my",
		Responsible: "u1", Priority: 1, Users: []tasks.User{user}, Projects: []tasks.Project{project}, Title: "Заказ", CRM: "/crm/deals/d1",
		Mail: &mail.Message{ID: "m1", FromName: "Клиент"}, MailFiles: 2}}}
	extra["share/file"] = page{Data: map[string]any{"Link": drive.Shared{Link: drive.Link{Token: "t1", FileID: "x1"}}, "Token": "t1", "Name": "фото.png", "Size": int64(10), "Kind": "image"}}
	extra["share/password"] = page{Data: map[string]any{"AskPassword": true, "Token": "t1", "Wrong": true}}
	extra["share/gone"] = page{Data: map[string]any{"Gone": true}}
	dt := maps.Clone(cases["disk"].Data.(map[string]any))
	dt["InTrash"], dt["Trash"] = true, []drive.Trashed{{ID: "x1", Name: "план.pdf", Kind: "file", DeletedBy: "Борис", DeletedByID: "u1", DeletedAt: now, PurgeAt: &now}}
	extra["disk/trash"] = page{Data: dt}
	ds := maps.Clone(cases["disk"].Data.(map[string]any))
	ds["Searching"], ds["Q"], ds["Type"], ds["Sort"], ds["Desc"] = true, "план", "doc", "date", true
	ds["Folders"], ds["Found"] = nil, []drive.File{{ID: "f3", Name: "Планы", FolderID: "f1", Where: "Документы"}}
	ds["Files"] = []drive.File{{ID: "x1", Name: "план.pdf", Size: 2048, UploaderID: "u1", CreatedAt: now, FolderID: "f1", Where: "Документы"}}
	extra["disk/search"] = page{Data: ds}
	dsh := maps.Clone(cases["disk"].Data.(map[string]any))
	dsh["Drive"] = drive.Drive{Kind: "user", OwnerID: "u2", Root: "r1", Name: "Общая", Owner: "Анна", Access: "edit", CanWrite: true}
	extra["disk/sharedfolder"] = page{Data: dsh}
	dmy := maps.Clone(cases["disk"].Data.(map[string]any))
	dmy["Drive"] = drive.Drive{Kind: "user", OwnerID: "u1", CanWrite: true, CanManage: true}
	dmy["Shares"] = map[string][]drive.Share{"f2": {{ID: "s1", ItemID: "f2", Kind: "user", Whom: "Иван", Access: "read"}, {ID: "s2", ItemID: "f2", Kind: "department", Whom: "Продажи", Access: "full"}},
		"x1": {{ID: "s3", ItemID: "x1", Kind: "user", Whom: "Иван", Access: "edit"}}}
	dmy["Users"], dmy["Departments"], dmy["Accesses"] = []tasks.User{{ID: "u3", Name: "Иван"}}, []drive.Department{{ID: "d1", Name: "Продажи"}}, drive.Accesses
	dmy["FileAccesses"] = drive.FileAccesses
	extra["disk/shares"] = page{Data: dmy}
	dof := maps.Clone(cases["disk"].Data.(map[string]any))
	dof["Usage"], dof["DavURL"] = drive.Usage{Used: 950 << 20, Limit: 1 << 30}, "https://go24.test/dav/"
	dof["Office"], dof["Files"] = true, []drive.File{{ID: "x1", Name: "план.docx", UploaderID: "u1", CreatedAt: now}, {ID: "x2", Name: "счёт.pdf", CreatedAt: now, Locker: "Иван", LockerID: "u2", LockedAt: &now}}
	extra["disk/office"] = page{Data: dof}
	extra["office/view"] = page{Data: map[string]any{"Config": `{"document":{}}`, "Name": "план.docx", "Back": "/disk/my", "Edit": false, "Download": "/files/x1"}}
	extra["disk/shared"] = page{Data: map[string]any{"SharedPage": true, "Drive": drive.Drive{}, "Projects": []tasks.Project{project}, "Me": "u1",
		"Shared": []drive.SharedFolder{{ID: "r1", Name: "Общая", Owner: "Анна", OwnerID: "u2", Access: "full", SharedAt: now},
			{ID: "x9", Name: "смета.xlsx", Owner: "Анна", OwnerID: "u2", Access: "edit", SharedAt: now, File: true}}}}
	extra["docs/empty"] = page{Data: map[string]any{"Scope": "mine", "Q": "план", "Type": "sheet", "Types": []string{"doc", "sheet", "slides"}, "Scopes": []string{"", "mine", "shared"}}}
	extra["disk/recent"] = page{Data: map[string]any{"RecentPage": true, "Drive": drive.Drive{}, "Projects": []tasks.Project{project}, "Me": "u1",
		"Recent": []drive.Recent{{File: drive.File{ID: "x1", Name: "план.pdf", CreatedAt: now, FolderID: "f1", Where: "Документы"}, Drive: "project", ProjectKey: "GO", ProjectName: "go24"},
			{File: drive.File{ID: "x2", Name: "фото.png", ContentType: "image/png", CreatedAt: now}, Drive: "user"}}}}
	sdd := maps.Clone(cases["stockdoc"].Data.(map[string]any))
	sdd["CanWrite"], sdd["ID"] = true, "new"
	sdd["Doc"] = crm.StockDoc{Kind: "arrival", Status: "draft", Date: now, Responsible: crm.Ref{ID: "u1"},
		Rows: []crm.StockRow{{ProductID: "p1", Name: "Лицензия", Quantity: 2, Price: 100, To: "w1"}}}
	sdd["Suppliers"] = []crm.Company{{ID: "c1", Title: "ООО Ромашка"}}
	extra["stockdoc/draft"] = page{Data: sdd}
	sdx := maps.Clone(sdd)
	sdx["Doc"] = crm.StockDoc{Kind: "deduct", Status: "cancelled", ID: "sd2", Number: 4, Date: now, Reason: "Брак", Rows: []crm.StockRow{{ProductID: "p1", Name: "Лицензия", Quantity: 1, From: "w1"}}}
	extra["stockdoc/deduct"] = page{Data: sdx}
	sds := maps.Clone(sdd)
	sds["Doc"] = crm.StockDoc{Kind: "shipment", Status: "draft", ID: "sd3", Number: 5, Date: now, Deal: crm.Ref{ID: "d1", Name: "Офис"}, Currency: "RUB",
		Rows: []crm.StockRow{{ProductID: "p1", Name: "Лицензия", Quantity: 1, Price: 600, From: "w1"}}}
	extra["stockdoc/shipment"] = page{Data: sds}
	sdr := maps.Clone(sds)
	sdr["CanWrite"] = false
	extra["stockdoc/shipment-conducted"] = page{Data: sdr}
	free, short := 3.0, -1.0
	sk := maps.Clone(rd) // a won deal with its products reserved, not shipped yet
	sk["Item"] = crm.Deal{ID: "d1", Title: "Офис", Currency: "RUB", Stage: crm.Stage{ID: "s9", Code: "won", Semantic: "success"}, CreatedAt: now}
	sk["Rows"] = []crm.ProductRow{{ProductID: "p1", Name: "Лицензия", Price: 1000, Quantity: 2, WarehouseID: "w1", WarehouseName: "Основной", Reserve: 2, ReserveUntil: &now, Free: &free},
		{ProductID: "p1", Name: "Лицензия", Price: 1000, Quantity: 1, WarehouseID: "w2", WarehouseName: "Филиал", Reserve: 1, Free: &short}, {Name: "Доставка", Price: 500, Quantity: 1}}
	sk["Stock"] = map[string]any{"Warehouses": []crm.Warehouse{{ID: "w1", Name: "Основной", Default: true, Active: true}, {ID: "w2", Name: "Филиал", Active: true}}}
	sk["Shipments"] = []crm.StockDoc{{ID: "sd3", Kind: "shipment", Number: 5, Status: "draft", Date: now, Amount: 600, Currency: "RUB"}}
	sk["Keeper"], sk["Unshipped"] = true, 3.0
	extra["crmcard/stock"] = page{Data: sk}
	sr := maps.Clone(sk)
	sr["CanWrite"], sr["Shipments"], sr["Unshipped"] = false, []crm.StockDoc{}, nil
	extra["crmcard/stock-read"] = page{Data: sr}
	extra["workreport/new"] = page{Data: map[string]any{"New": true, "Period": company.ReportPeriod{From: now, To: now, Due: true}, "Worked": 0, "Managers": []company.Person{{ID: "u1", Name: "Борис"}},
			"Items": company.ReportItems{}}}
	extra["company/tiles"] = page{Data: map[string]any{"Employees": []company.Profile{{ID: "u1", Name: "Пётр Панов", Departments: []company.DeptRef{{ID: "d1", Name: "ИТ"}}}, {ID: "u2", Name: "Анна"}},
		"Filter": company.Filter{}, "Presets": company.EmployeePresets, "Keep": listQuery{url.Values{}}, "Tiles": true,
		"Absent": map[string]company.Absence{"u2": {ID: "a1", Kind: "vacation", From: now, To: now}}}}
	extra["stockreport/moves"] = page{Data: map[string]any{"Tab": "reports", "Rights": crm.StockRights{}, "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses, "Report": "moves", "CSV": "/store/reports/moves?csv=1",
		"F": crm.StockReportFilter{From: now, To: now, Warehouse: "w1"}, "Sum": 0.0, "Warehouses": []crm.Warehouse{{ID: "w1", Name: "Основной"}},
		"Items": []crm.MovementLine{{Product: crm.Product{ID: "p1", Name: "Лицензия", Unit: "шт"}, Opening: 10, In: 10, Out: 5, Closing: 15, Cost: 115, Sum: 1725}}}}
	extra["stockreport/product"] = page{Data: map[string]any{"Tab": "reports", "Rights": crm.AllStockRights(), "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses, "Report": "moves", "CSV": "/store/reports/moves?csv=1&product=p1",
		"F": crm.StockReportFilter{From: now, To: now, ProductID: "p1"}, "Sum": 0.0, "Warehouses": []crm.Warehouse{{ID: "w1", Name: "Основной"}},
		"Product": crm.Product{ID: "p1", Name: "Лицензия", SKU: "L-1"}, "Opening": 10.0,
		"Moves": []crm.StockMove{{Date: now, DocID: "sd1", Kind: "arrival", Number: 2, Title: "Поставка", Warehouse: crm.Ref{ID: "w1", Name: "Основной"}, Quantity: 10, Balance: 20},
			{Date: now, DocID: "sd2", Kind: "deduct", Number: 3, Warehouse: crm.Ref{ID: "w1", Name: "Основной"}, Quantity: -3, Balance: 17}}}}
	for _, st := range []string{"prepare", "done"} { // the other states of a meeting: preparing, finished (read-only)
		extra["meeting/"+st] = page{Data: map[string]any{"Can": st == "prepare", "Owner": true, "Me": "u1", "Users": []tasks.User{user}, "Deadline": now,
			"M": company.Meeting{ID: 3, Title: "Совет", Start: now, Minutes: 60, Status: st, Owner: company.Person{ID: "u1", Name: "Ivan"}, NextID: 4,
				Items: []company.MeetingItem{{ID: 1, Num: "1", Title: "Бюджет", Result: "Утвердить"}}}}}
	}
	extra["bifiles/set"] = page{Data: map[string]any{"Admin": false, "UserID": "u1", "MaxMB": 20, "Editable": true, "Types": bi.Types,
			"F": bi.File{ID: 1, Name: "Продажи", FileName: "a.csv", Rows: 2, AuthorName: "Анна", UpdatedAt: now,
				Columns: []bi.FileColumn{{Code: "c1", Name: "Город", Header: "Город", Type: "string"}, {Code: "c2", Name: "Сумма", Header: "Сумма", Type: "number"}}},
			"Unread": map[string]int{"c2": 1}, "Preview": [][]string{{"Тверь", "1 000,5"}, {"Москва", ""}}}}
	cy := maps.Clone(rd) // «Настроить карточку»: an own section, a hidden field; the editor of the view
	cy["Layout"] = []crm.CardSection{{Name: "Клиент", Fields: []string{"company", "contacts"}}, {Code: "main", Fields: []string{"amount", "responsible"}}}
	cy["Hidden"], cy["LayoutKind"], cy["LayoutOwn"], cy["LayoutCommon"] = []string{"comment"}, "deal", true, true
	cy["LayoutAdmin"] = true
	extra["crmcard/layout"] = page{Data: cy}
	cye := maps.Clone(cy)
	cye["Edit"] = true
	extra["crmcard/layout-edit"] = page{Data: cye}
	cyl := maps.Clone(cy)
	cyl["LayoutEdit"] = true
	extra["crmcard/layout-editor"] = page{Data: cyl}
	for name, p := range extra { // the other cards: the standard view of their kind
		if d, ok := p.Data.(map[string]any); ok && strings.HasPrefix(name, "crmcard/") && d["Layout"] == nil {
			crmTestLayout(d)
		}
	}
	crmTestLayout(cases["crmcard"].Data.(map[string]any))
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range bundle.Langs() {
		if len(cases) != len(pages[l.Code]) {
			t.Fatalf("test covers %d pages, templates define %d", len(cases), len(pages[l.Code]))
		}
		all := maps.Clone(cases)
		maps.Copy(all, extra)
		for name, p := range all {
			t.Run(l.Code+"/"+name, func(t *testing.T) {
				name, variant, _ := strings.Cut(name, "/")
				p.User = &auth.Session{UserID: "u1", Name: "Борис", Admin: true, AdminMode: true}
				p.Timer = &tasks.Timer{TaskKey: "GO-7", TaskTitle: "Сделать трекер", StartedAt: now}
				p.Lang, p.Langs = l.Code, bundle.Langs()
				var buf bytes.Buffer
				if err := pages[l.Code][name].Execute(&buf, p); err != nil {
					t.Fatal(err)
				}
				out := buf.String()
				if !strings.Contains(out, "</html>") {
					t.Fatal("layout not rendered")
				}
				if name == "collabs" && variant == "" && (!strings.Contains(out, `data-room-unread="!c"`) || !strings.Contains(out, `/my?view=overdue&amp;project=CL1`)) {
					t.Fatal("a collab's card without its counters")
				}
				if name == "im" && variant == "collab" && (!strings.Contains(out, `href="/disk/p/CL1"`) || !strings.Contains(out, `/my?view=overdue&amp;project=CL1`) ||
					!strings.Contains(out, `href="/p/CL1#guests"`)) {
					t.Fatal("a collab's chat without its tasks, drive, counters or «Пригласить» in the header")
				}
				if name == "im" && variant == "collabchat" && (!strings.Contains(out, `action="/im/collab/create"`) || !strings.Contains(out, `action="/im/collab/remove"`) ||
					!strings.Contains(out, `<option value="!o">Своя</option>`) || !strings.Contains(out, `href="/im?room=%21c"`) || !strings.Contains(out, `data-in="collab"`) ||
					!strings.Contains(out, `href="/disk/p/CL1"`)) {
					t.Fatal("a collab's group chat without the collab's chats, making one, adding one, taking it out or the collab in the header")
				}
				if name == "im" && variant == "owngroup" && (!strings.Contains(out, `action="/im/collab/add"`) || strings.Contains(out, `action="/im/collab/remove"`)) {
					t.Fatal("one's own group chat without «Добавить чат в коллаб»")
				}
				if p.Guest && name == "collabs" && !strings.Contains(out, `data-counter="collabs" data-base="3" data-rooms="!c !d"`) {
					t.Fatal("«Коллабы» in the menu without the counter")
				}
				if name == "robots" && l.Code == "ru-RU" && (!strings.Contains(out, "Название = {title}!; Источник = Веб-сайт; VIP = Да") ||
					!strings.Contains(out, "если Ответственный равно «Борис»") || !strings.Contains(out, "POST https://n8n.test/webhook/x") ||
					!strings.Contains(out, `data-pick="list" data-of="uf:f2"`)) {
					t.Fatal("robots: the fields and webhook robots or a condition are not shown")
				}
				if name == "robots" && l.Code == "ru-RU" && (!strings.Contains(out, "📄 Договор") || !strings.Contains(out, "⚙ Согласование") ||
					!strings.Contains(out, "Заменить наблюдателей: Борис") || !strings.Contains(out, `name="close_act"`) || !strings.Contains(out, `name="observers"`)) {
					t.Fatal("robots: the document, process, observers or deeds robots are not shown")
				}
				if name == "crmcard" && variant == "" && l.Code == "ru-RU" && (!strings.Contains(out, "создал документ «Договор № 3»") || !strings.Contains(out, "завершил дела: 2")) {
					t.Fatal("the card's history: what the document and deeds robots did")
				}
				if name == "crmcard" && variant == "" && l.Code == "ru-RU" && (!strings.Contains(out, "Повторный лид</b>") || strings.Contains(out, "Лид конвертирован") ||
					!strings.Contains(out, "повторный лид — найден клиент:")) {
					t.Fatal("a repeat lead's card: no mark, or shown converted")
				}
				if name == "crm" && variant == "leads" && l.Code == "ru-RU" && (!strings.Contains(out, `class="pill small crmrep"`) || !strings.Contains(out, `<option value="1" selected>Только повторные`)) {
					t.Fatal("the leads' list: no repeat mark or filter")
				}
				if name == "crmcard" && variant == "" && l.Code == "ru-RU" && (!strings.Contains(out, `href="tel:&#43;79001112233"`) || !strings.Contains(out, "мобильный")) {
					t.Fatal("a card without its second phone and the phones' types")
				}
				if name == "crmcard" && variant == "layout" && l.Code == "ru-RU" && (!strings.Contains(out, ">Клиент</h3>") ||
					strings.Index(out, ">Клиент</h3>") > strings.Index(out, ">О сделке</h3>") || strings.Contains(out, ">Дополнительно</h3>")) {
					t.Fatal("the card by its own sections")
				}
				if name == "crmcard" && variant == "layout-edit" && l.Code == "ru-RU" && (!strings.Contains(out, "<legend>Клиент</legend>") ||
					strings.Index(out, "Скрытые поля") > strings.Index(out, `name="comment"`)) {
					t.Fatal("the card's form by sections, the hidden comment under «Скрытые поля»")
				}
				if name == "crmcard" && variant == "layout-editor" && l.Code == "ru-RU" && (!strings.Contains(out, `data-crmlayout`) || !strings.Contains(out, `data-code="comment"`) ||
					!strings.Contains(out, "Сбросить на стандартный") || !strings.Contains(out, "Вернуть общий вид") || strings.Contains(out, `name="comm_value"`)) {
					t.Fatal("the editor of the card's view")
				}
				if name == "crmcard" && variant == "edit" && (strings.Count(out, `name="comm_value"`) != 3 || !strings.Contains(out, `value="&#43;79001112233"`) ||
					!strings.Contains(out, "data-comm-add")) {
					t.Fatal("the card's form: no rows of its phones and e-mails")
				}
				if name == "telephony" && l.Code == "ru-RU" && (!strings.Contains(out, "89001112233") || !strings.Contains(out, `action="/telephony/blacklist/b1/delete"`)) {
					t.Fatal("telephony: the black list is not shown")
				}
				if name == "phonecalls" && l.Code == "ru-RU" && (!strings.Contains(out, "В чёрном списке") || !strings.Contains(out, `name="back" value="/telephony/calls?kind=missed"`)) {
					t.Fatal("the call log: no black list mark or button")
				}
				if name == "crmcard" && strings.Contains(out, "crmcall") && !strings.Contains(out, `/telephony/calls/c2/record`) {
					t.Fatal("a call in the timeline without its recording")
				}
				if name == "phone" && (strings.Contains(out, `class="side"`) || strings.Contains(out, "menu.js") || !strings.Contains(out, "jssip")) {
					t.Fatal("the softphone window has the menu or no JsSIP")
				}
				if p.Phone != "" && !strings.Contains(out, "data-phone-open") {
					t.Fatal("no softphone button in the top bar")
				}
				if name == "call" && (!strings.Contains(out, `data-lk="wss://lk"`) || strings.Contains(out, `data-text-on="&#34;`)) {
					t.Fatal("the call page has no LiveKit address or quoted labels") // html/template: a wss: «url» attribute, «on…» as script
				}
				if name == "call" && (!strings.Contains(out, `data-base="/static/js/vendor/mediapipe/"`) || !strings.Contains(out, "js/callbg.js") || !strings.Contains(out, "data-call-noise")) {
					t.Fatal("the call page has no backgrounds or noise suppression") // GO24DEV-778
				}
				// an untranslated key would show up as "word.word" text
				if m := regexp.MustCompile(`>\s*((?:title|nav|status|action|role|field|scope|priority|props|form|keys|err|reminder|fields|deadlines|filter|preset|crm|bulk|project|projects|invites|poll|gratitude|workday|invite|kb|call|disk|crm|robots|docs|mail|crmroles|bp|ol|widget|hooks|collab|guests|join|office)\.[a-z_:.0-9]+)\s*<`).FindStringSubmatch(out); m != nil {
					t.Fatalf("untranslated key on page: %s", m[1])
				}
			})
		}
	}
}

// TestIcons keeps templates, SVG files and icons.css in sync, so a theme never loses an icon.
func TestIcons(t *testing.T) {
	css, err := staticFS.ReadFile("static/css/icons.css")
	if err != nil {
		t.Fatal(err)
	}
	for name := range iconNames {
		if !strings.Contains(string(css), ".i-"+name+" ") {
			t.Errorf("icons.css has no .i-%s", name)
		}
	}
	used := regexp.MustCompile(`icon "([a-z0-9-]+)"`)
	files, _ := fs.Glob(templatesFS, "templates/*.html")
	for _, f := range files {
		b, _ := templatesFS.ReadFile(f)
		for _, m := range used.FindAllStringSubmatch(string(b), -1) {
			if !iconNames[m[1]] {
				t.Errorf("%s uses unknown icon %q", f, m[1])
			}
		}
	}
	for _, a := range []tasks.Action{tasks.ActionStart, tasks.ActionPause, tasks.ActionComplete, tasks.ActionApprove,
		tasks.ActionDisapprove, tasks.ActionDefer, tasks.ActionRenew} {
		if name := baseFuncs["actionIcon"].(func(tasks.Action) string)(a); !iconNames[name] {
			t.Errorf("action %s has no icon", a)
		}
	}
}

// TestMessageKeys: every key used in templates and Go code, and every dynamic family
// (status.*, action.*, role.*…), is translated in every built-in language.
func TestMessageKeys(t *testing.T) {
	bundle, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{} // key → where it is used
	tplKey := regexp.MustCompile(`\bt "([a-z_]+\.[a-z_:.0-9]+)"`)
	files, _ := fs.Glob(templatesFS, "templates/*.html")
	for _, f := range files {
		b, _ := templatesFS.ReadFile(f)
		for _, m := range tplKey.FindAllStringSubmatch(string(b), -1) {
			keys[m[1]] = f
		}
	}
	goKey := regexp.MustCompile(`(?:i18n\.E|invalid|Ref)\("([a-z_]+\.[a-z_:.0-9]+)"`)
	filepath.WalkDir("..", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, _ := os.ReadFile(p)
		for _, m := range goKey.FindAllStringSubmatch(string(b), -1) {
			keys[m[1]] = p
		}
		return nil
	})
	for _, st := range tasks.AllStatuses {
		keys["status."+string(st)] = "tasks.AllStatuses"
	}
	for _, a := range []tasks.Action{tasks.ActionStart, tasks.ActionPause, tasks.ActionComplete, tasks.ActionApprove,
		tasks.ActionDisapprove, tasks.ActionDefer, tasks.ActionRenew} {
		keys["action."+string(a)] = "actions"
	}
	for _, r := range []string{"owner", "manager", "member", "admin"} {
		keys["collab.role."+r] = "collab roles"
	}
	for _, r := range []string{"owner", "manager", "member", "viewer", "admin", "responsible", "accomplice", "creator", "auditor"} {
		keys["role."+r] = "roles"
	}
	for _, f := range []string{"created", "status", "title", "description", "responsible", "priority", "deadline",
		"requires_control", "accomplice", "auditor", "estimate", "start_plan", "end_plan", "predecessor"} {
		keys["field."+f] = "history fields"
	}
	for m := 1; m <= 12; m++ {
		keys[fmt.Sprintf("month.%d", m)] = "gantt months"
	}
	for _, sc := range []string{"day", "week", "month"} {
		keys["gantt.scale."+sc] = "gantt scales"
	}
	for _, v := range []string{"open", "overdue", "control", "comments"} {
		keys["my.view."+v] = "my views"
	}
	for _, v := range []string{"members", "participants"} {
		keys["project.visibility."+v] = "project visibility"
	}
	for _, c := range []string{"today", "week", "later"} {
		keys["plan.stage."+c] = "plan_stages.code"
	}
	for _, ty := range storages.Types {
		keys["storage_type."+ty] = "storages.Types"
	}
	for _, sc := range apikeys.AllScopes {
		keys["scope."+sc] = "apikeys.AllScopes"
	}
	for _, k := range []string{"priority.0", "priority.1", "priority.2", "bool.true", "bool.false", "format.datetime"} {
		keys[k] = "fixed"
	}
	if len(keys) < 100 {
		t.Fatalf("found only %d keys — the scan is broken", len(keys))
	}
	for _, l := range bundle.Langs() {
		for k, where := range keys {
			if bundle.T(l.Code, k) == k {
				t.Errorf("%s: key %q (used in %s) is not translated", l.Code, k, where)
			}
		}
	}
}

// crmTestLayout gives a card's data the standard view of its kind with its own fields «Дополнительно».
func crmTestLayout(d map[string]any) {
	e, _ := d["Entity"].(string)
	typeID := ""
	if it, ok := d["Item"].(crm.Deal); ok {
		typeID = it.TypeID
	}
	scope := crm.CardScope(e, typeID)
	ids, by := []string{}, map[string]tasks.FieldValue{}
	if fs, ok := d["Fields"].([]tasks.FieldValue); ok {
		for _, f := range fs {
			ids = append(ids, f.ID)
			by["f:"+f.ID] = f
		}
	}
	d["Layout"], d["FieldBy"], d["LayoutKind"] = crm.DefaultCardLayout(scope, ids).Sections, by, crm.ColumnKind(scope)
}
