package kb

import (
	"errors"
	"testing"
)

// TestKBRights: «Права доступа» of a space close it to the others, open it to a department with its sub-departments
// and give read, edit or full access; the creator keeps it; the search follows.
func TestKBRights(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, sales, north, other := newUser("boss"), newUser("sales"), newUser("north"), newUser("other")
	var company, dept, sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&company))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Продажи') RETURNING id`, company).Scan(&dept))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Север') RETURNING id`, dept).Scan(&sub))
	_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, sales, dept, north, sub)
	must(err)
	s := NewStore(pool)
	sp, err := s.CreateSpace(ctx, boss, "Продажи", "", "")
	must(err)
	_, err = s.CreatePage(ctx, boss, sp, "", "Скрипт звонка", "Здравствуйте")
	must(err)

	if err := s.SetRight(ctx, other, sp, "all", "", "read"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an employee set the rights: %v", err)
	}
	must(s.SetRight(ctx, boss, sp, "department", dept, "read"))
	must(s.SetRight(ctx, boss, sp, "user", north, "edit"))
	if _, err := s.Space(ctx, other, sp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("one the base is not open to sees it: %v", err)
	}
	if hits, _ := s.Search(ctx, other, "звонка", ""); len(hits) != 0 {
		t.Fatalf("the search found a closed base: %+v", hits)
	}
	if got, _ := s.Space(ctx, sales, sp); got.CanEdit || got.CanManage || !got.Restricted {
		t.Fatalf("reader = %+v", got)
	}
	if got, _ := s.Space(ctx, north, sp); !got.CanEdit || got.CanManage { // the sub-department reads, and edits by name
		t.Fatalf("editor = %+v", got)
	}
	if _, err := s.CreatePage(ctx, sales, sp, "", "x", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a reader wrote a page: %v", err)
	}
	if got, _ := s.Space(ctx, boss, sp); !got.CanManage {
		t.Fatalf("the creator lost the base: %+v", got)
	}
	must(s.SetRight(ctx, boss, sp, "user", north, "full")) // changes the line, not a second one
	rights, _ := s.Rights(ctx, sp)
	if len(rights) != 2 || rights[0].Kind != "department" || rights[0].Whom != "Продажи" || rights[1].Access != "full" {
		t.Fatalf("rights = %+v", rights)
	}
	must(s.UpdateSpace(ctx, north, sp, "Отдел продаж", "Скрипты"))
	if err := s.SetRight(ctx, boss, sp, "user", "00000000-0000-0000-0000-000000000001", "read"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a right for nobody: %v", err)
	}
	must(s.DeleteRight(ctx, north, sp, rights[0].ID))
	if _, err := s.Space(ctx, sales, sp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the department still sees the base: %v", err)
	}
	must(s.ClearRights(ctx, boss, sp))
	if got, err := s.Space(ctx, other, sp); err != nil || !got.CanEdit || got.Restricted || got.Name != "Отдел продаж" {
		t.Fatalf("without rights = %+v, %v", got, err)
	}
}
