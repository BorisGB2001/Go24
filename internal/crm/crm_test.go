package crm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

func TestAmount(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 999: "999", 1500000: "1 500 000", 1234.5: "1 234,50", 12.05: "12,05"} {
		if got := Amount(v); got != want {
			t.Errorf("%v: %q, want %q", v, got, want)
		}
	}
}

// TestCRM: a lead goes through its stages and is converted into a deal with a contact and a company; the deal
// moves along the pipeline and closes; timelines; who deletes.
func TestCRM(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	anna, petr := newUser("anna"), newUser("petr")
	s := NewStore(pool)

	stages, err := s.LeadStages(ctx)
	must(err)
	pipes, err := s.Pipelines(ctx)
	must(err)
	if len(stages) != 5 || len(pipes) != 1 || len(pipes[0].Stages) != 7 || pipes[0].Code != "default" {
		t.Fatalf("defaults: %d lead stages, pipelines %+v", len(stages), pipes)
	}
	var ve *i18n.Error
	if _, err := s.SaveLead(ctx, anna, Lead{Title: " "}); !errors.As(err, &ve) {
		t.Fatalf("a lead without a title: %v", err)
	}
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка с сайта", Name: "Иван Петров", CompanyTitle: "ООО Ромашка",
		Phone: "+7 900 000-00-00", Amount: 150000})
	must(err)
	l, _ := s.Lead(ctx, lead)
	if l.Stage.Code != "lead_new" || l.Currency != "RUB" || l.Responsible.ID != anna {
		t.Fatalf("new lead = %+v", l)
	}
	if _, err := s.MoveLead(ctx, anna, lead, stages[3].ID); !errors.As(err, &ve) { // success only by converting
		t.Fatalf("a lead moved to success unconverted: %v", err)
	}
	_, err = s.MoveLead(ctx, anna, lead, stages[1].ID)
	must(err)
	deal, err := s.ConvertLead(ctx, petr, lead)
	must(err)
	again, _ := s.ConvertLead(ctx, petr, lead)
	l, _ = s.Lead(ctx, lead)
	d, err := s.Deal(ctx, deal)
	must(err)
	if again != deal || l.DealID != deal || l.Stage.Semantic != "success" || d.Amount != 150000 || d.Company.Name != "ООО Ромашка" ||
		d.Contact.Name != "Иван Петров" || d.Stage.Code != "new" {
		t.Fatalf("converted: lead %+v, deal %+v", l, d)
	}
	contacts, _ := s.Contacts(ctx, "", "", d.Company.ID)
	if len(contacts) != 1 || contacts[0].Phone != "+7 900 000-00-00" {
		t.Fatalf("company contacts = %+v", contacts)
	}

	// the deal along the pipeline: another pipeline's stage is refused, a final stage closes it
	won := pipes[0].Stages[5]
	if _, err := s.MoveDeal(ctx, anna, deal, stages[0].ID); !errors.As(err, &ve) {
		t.Fatalf("a deal moved to a lead stage: %v", err)
	}
	d, err = s.MoveDeal(ctx, anna, deal, won.ID)
	must(err)
	if d.ClosedAt == nil || !d.Stage.Closed() {
		t.Fatalf("won deal = %+v", d)
	}
	must(s.Comment(ctx, anna, "deal", deal, "Оплачено"))
	tl, _ := s.Timeline(ctx, "deal", deal)
	if len(tl) != 4 || tl[0].Kind != "comment" || tl[1].Kind != "stage" || tl[1].Body != "code:new → code:won" {
		t.Fatalf("deal timeline = %+v", tl)
	}
	if list, _ := s.Deals(ctx, "", pipes[0].ID, "заявка", "", ""); len(list) != 1 {
		t.Fatalf("deal search = %+v", list)
	}
	if err := s.DeleteDeal(ctx, newUser("eve"), deal); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger deleted a deal: %v", err)
	}
	// a document template of deals
	var file string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by) VALUES ('tpl', 'dog.docx', 1, $1) RETURNING id`, anna).Scan(&file))
	if err := s.AddDocTemplate(ctx, anna, "Договор", "invoice", file); !errors.As(err, &ve) {
		t.Fatalf("a template of an unknown card: %v", err)
	}
	must(s.AddDocTemplate(ctx, anna, "Договор", "deal", file))
	tpls, _ := s.DocTemplates(ctx, "deal")
	if len(tpls) != 1 || tpls[0].FileName != "dog.docx" {
		t.Fatalf("templates = %+v", tpls)
	}
	if other, _ := s.DocTemplates(ctx, "lead"); len(other) != 0 {
		t.Fatal("a deal template among lead templates")
	}
	// its documents: numbered by the template's numerator, kept at the card
	if tpls[0].Numerator != "{NUMBER}" {
		t.Fatalf("numerator = %q", tpls[0].Numerator)
	}
	if err := s.SetDocNumerator(ctx, tpls[0].ID, " "); !errors.As(err, &ve) {
		t.Fatalf("an empty numerator: %v", err)
	}
	must(s.SetDocNumerator(ctx, tpls[0].ID, "Д-{YEAR}/{NUMBER}"))
	at := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	n1, err := s.NextDocNumber(ctx, tpls[0].ID, at)
	must(err)
	n2, _ := s.NextDocNumber(ctx, tpls[0].ID, at)
	if n1 != "Д-2026/1" || n2 != "Д-2026/2" {
		t.Fatalf("numbers = %s, %s", n1, n2)
	}
	if got := FormatDocNumber("{DAY}.{MONTH}-{NUMBER}", 7, at); got != "25.10-7" {
		t.Fatalf("format = %s", got)
	}
	var docFile, docFile2 string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by) VALUES ('d1', 'Договор.docx', 10, $1) RETURNING id`, anna).Scan(&docFile))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by) VALUES ('d2', 'Договор.docx', 12, $1) RETURNING id`, anna).Scan(&docFile2))
	docID, err := s.AddDocument(ctx, Document{TemplateID: tpls[0].ID, TemplateName: "Договор", Entity: "deal", EntityID: deal,
		Number: n1, FileID: docFile, Values: map[string]string{"Title": "Заявка"}, AuthorID: anna})
	must(err)
	docs, _ := s.Documents(ctx, "deal", deal)
	if len(docs) != 1 || docs[0].Number != "Д-2026/1" || docs[0].Values["Title"] != "Заявка" || docs[0].Author == "" || docs[0].Size != 10 {
		t.Fatalf("documents = %+v", docs)
	}
	if tl, _ := s.Timeline(ctx, "deal", deal); tl[0].Kind != "document" || tl[0].Body != "Договор № Д-2026/1" {
		t.Fatalf("history = %+v", tl[0])
	}
	if _, err := s.Document(ctx, "deal", lead, docID); err != ErrNotFound {
		t.Fatalf("a document of another card: %v", err)
	}
	must(s.RemakeDocument(ctx, docID, docFile2, map[string]string{"Title": "Поставка"}))
	if d, _ := s.Document(ctx, "deal", deal, docID); d.FileID != docFile2 || d.Number != n1 || d.Values["Title"] != "Поставка" {
		t.Fatalf("remade = %+v", d)
	}
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE id::text = $1`, docFile).Scan(&left))
	if left != 0 {
		t.Fatal("the old file of a remade document is kept")
	}
	must(s.DeleteDocTemplate(ctx, tpls[0].ID))
	if d, _ := s.Document(ctx, "deal", deal, docID); d.TemplateID != "" || d.TemplateName != "Договор" {
		t.Fatalf("a document outlives its template: %+v", d)
	}
	must(s.DeleteDocument(ctx, docID))
	if docs, _ := s.Documents(ctx, "deal", deal); len(docs) != 0 {
		t.Fatal("a deleted document is listed")
	}
	if left, _ := s.DocTemplates(ctx, ""); len(left) != 0 {
		t.Fatal("the template outlived its file")
	}
	must(s.DeleteDeal(ctx, petr, deal)) // its author
	if l, _ = s.Lead(ctx, lead); l.DealID != "" {
		t.Fatal("the lead still points to a deleted deal")
	}
}

// TestRobots: entering a stage makes a task (linked to the deal), a notification and a history line.
func TestRobots(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, seller := newUser("boss"), newUser("seller")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "SALE", "Продажи", "")
	must(err)
	s := NewStore(pool)
	s.Tasks = ts
	pipes, _ := s.Pipelines(ctx)
	invoice := pipes[0].Stages[2]
	var ve *i18n.Error
	if err := s.AddRobot(ctx, boss, invoice.ID, "task", RobotParams{Text: "x"}); !errors.As(err, &ve) {
		t.Fatalf("a task robot without a project: %v", err)
	}
	must(s.AddRobot(ctx, boss, invoice.ID, "task", RobotParams{Text: "Выставить счёт: {title}", Project: "sale", Days: 2}))
	must(s.AddRobot(ctx, boss, invoice.ID, "notify", RobotParams{Text: "Счёт по «{title}» на {amount}", To: "creator"}))
	must(s.AddRobot(ctx, boss, invoice.ID, "comment", RobotParams{Text: "Ответственный: {responsible}"}))

	deal, err := s.SaveDeal(ctx, boss, Deal{Title: "Поставка", PipelineID: pipes[0].ID, Amount: 1000, Responsible: Ref{ID: seller}})
	must(err)
	_, err = s.MoveDeal(ctx, seller, deal, invoice.ID)
	must(err)
	linked, err := ts.ListTasks(tasks.WithAdmin(ctx), tasks.Filter{CRMLink: "/crm/deals/" + deal})
	must(err)
	if len(linked) != 1 || linked[0].Title != "Выставить счёт: Поставка" || linked[0].ResponsibleID != seller || linked[0].Deadline == nil {
		t.Fatalf("robot task = %+v", linked)
	}
	var note, link string
	must(pool.QueryRow(ctx, `SELECT body, url FROM notifications WHERE kind = 'crm_robot' AND user_id = $1`, boss).Scan(&note, &link))
	if note != "Счёт по «Поставка» на 1 000 RUB" || link != "/crm/deals/"+deal {
		t.Fatalf("robot notification = %q %q", note, link)
	}
	tl, _ := s.Timeline(ctx, "deal", deal)
	robots := 0
	for _, e := range tl {
		if e.Kind == "robot" {
			robots++
			if e.Body != "Ответственный: seller" && e.Body != "task:"+linked[0].Key() {
				t.Fatalf("robot history line %q", e.Body)
			}
		}
	}
	if robots != 2 {
		t.Fatalf("robot history lines = %d: %+v", robots, tl)
	}
}

// TestRights: the default role keeps the CRM open; a «manager» role sees and changes own deals only, a «head» role
// the department's; admin mode sees everything.
func TestRights(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	ann, bob, head, other := newUser("ann"), newUser("bob"), newUser("head"), newUser("other")
	var sales string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	for _, u := range []string{ann, bob, head} {
		_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, u, sales)
		must(err)
	}
	s := NewStore(pool)
	pipes, _ := s.Pipelines(ctx)
	annDeal, err := s.SaveDeal(ctx, ann, Deal{Title: "Анны", PipelineID: pipes[0].ID})
	must(err)
	_, err = s.SaveDeal(ctx, bob, Deal{Title: "Боба", PipelineID: pipes[0].ID})
	must(err)
	_, err = s.SaveDeal(ctx, other, Deal{Title: "Чужая", PipelineID: pipes[0].ID})
	must(err)
	count := func(user string) int {
		t.Helper()
		list, err := s.Deals(ctx, user, pipes[0].ID, "", "", "")
		must(err)
		return len(list)
	}
	if count(ann) != 3 {
		t.Fatal("the default role keeps the CRM open")
	}
	manager, err := s.SaveRole(ctx, Role{Name: "Менеджер", Perms: Perms{"deal": {"read": "own", "write": "own", "delete": "none"}}})
	must(err)
	chief, err := s.SaveRole(ctx, Role{Name: "Руководитель", Perms: Perms{"deal": {"read": "dept", "write": "dept"}}})
	must(err)
	must(s.SetMember(ctx, manager, ann, true))
	must(s.SetMember(ctx, manager, head, true))
	must(s.SetMember(ctx, chief, head, true))
	if count(ann) != 1 || count(head) != 2 || count(other) != 3 {
		t.Fatalf("visible deals: ann %d, head %d, other %d", count(ann), count(head), count(other))
	}
	if ok, _ := s.Can(ctx, ann, "deal", "write", bob); ok {
		t.Fatal("a manager changes someone else's deal")
	}
	if ok, _ := s.Can(ctx, head, "deal", "write", bob); !ok {
		t.Fatal("the head cannot change a deal of the department")
	}
	if ok, _ := s.Can(ctx, ann, "contact", "read", bob); ok {
		t.Fatal("a role without contacts reads them")
	}
	if list, _ := s.Deals(tasks.WithAdmin(ctx), ann, pipes[0].ID, "", "", ""); len(list) != 3 {
		t.Fatal("admin mode sees everything")
	}
	if err := s.DeleteRole(ctx, manager); err != nil {
		t.Fatal(err)
	}
	if count(ann) != 3 {
		t.Fatal("without roles the default applies again")
	}
	roles, _ := s.Roles(ctx)
	if err := s.DeleteRole(ctx, roles[0].ID); err == nil {
		t.Fatal("the default role deleted")
	}
	_ = annDeal
}
