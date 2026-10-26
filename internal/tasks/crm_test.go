package tasks

import (
	"context"
	"errors"
	"testing"

	"go24/internal/i18n"
)

func TestParseCRM(t *testing.T) {
	for raw, want := range map[string]CRMLink{
		"https://portal.example.com/crm/deal/details/123/":         {Kind: "deal", EntityID: 123},
		" https://portal.example.com/crm/contact/details/45/?x=1 ": {Kind: "contact", EntityID: 45},
		"https://portal.example.com/crm/type/177/details/5/":       {Kind: "item", EntityID: 5},
		"https://portal.example.com/crm/lead/details/9/":           {Kind: "lead", EntityID: 9},
		"https://example.com/anything":                             {Kind: "other"},
	} {
		got, err := ParseCRM(raw)
		if err != nil || got.Kind != want.Kind || got.EntityID != want.EntityID || got.Own != want.Own {
			t.Errorf("%q: %+v %v", raw, got, err)
		}
	}
	for _, bad := range []string{"", "crm/deal/details/1", "javascript:alert(1)", "ftp://x/crm/deal/details/1/", "/crm/deals/../../admin", "//evil/crm/deals/x"} {
		if _, err := ParseCRM(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestCRMLinks(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, resp, stranger := newUser("boss"), newUser("resp"), newUser("stranger")
	p, err := s.CreateProject(ctx, boss, "cr", "CRM", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, boss, p)
	must(err)
	key, err := s.CreateTask(ctx, boss, proj, NewTask{Title: "Звонок клиенту", ResponsibleID: resp})
	must(err)
	id, _ := s.TaskIDByKey(ctx, key)

	deal := "https://portal.example.com/crm/deal/details/123/"
	must(s.AddCRMLink(ctx, resp, id, deal))
	must(s.AddCRMLink(ctx, resp, id, deal)) // twice is once
	if err := s.AddCRMLink(ctx, stranger, id, deal); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger linked: %v", err)
	}
	var ve *i18n.Error
	if err := s.AddCRMLink(ctx, resp, id, "not a link"); !errors.As(err, &ve) {
		t.Fatalf("a bad link: %v", err)
	}
	links, err := s.CRMLinks(ctx, id)
	must(err)
	if len(links) != 1 || links[0].Kind != "deal" || links[0].EntityID != 123 {
		t.Fatalf("links = %+v", links)
	}
	must(s.RemoveCRMLink(ctx, boss, id, deal))
	var history int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'crm'`, id).Scan(&history))
	if links, _ = s.CRMLinks(ctx, id); len(links) != 0 || history != 2 {
		t.Fatalf("after remove: %+v, history %d", links, history)
	}
}
