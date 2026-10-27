package drive

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestDriveLeaveShare: «Отказаться от доступа» — a folder or a file shared in person or with the department goes from
// «Общие со мной», «Документы» and the connected «Мой диск» of the one who gave it up, the owner is told; opened to
// them in person again, it comes back.
func TestDriveLeaveShare(t *testing.T) {
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
	var anna, ivan, olga, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('olga', 'Ольга', 'o@t') RETURNING id`).Scan(&olga))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $2)`, ivan, sales, olga)
	must(err)
	s := NewStore(pool, tasks.NewStore(pool))
	my, _ := s.Open(ctx, anna, "my")
	folder, err := s.CreateFolder(ctx, anna, my, "", "Работа")
	must(err)
	var plan string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
		VALUES (gen_random_uuid()::text, 'план.docx', 3, $1, 'user', $1, NULL) RETURNING id`, anna).Scan(&plan))
	must(s.SetShare(ctx, anna, my, folder, "", "user", ivan, "edit"))
	must(s.SetShare(ctx, anna, my, folder, "", "department", sales, "read"))
	must(s.SetShare(ctx, anna, my, "", plan, "department", sales, "read"))
	must(s.Connect(ctx, ivan, "sh:"+folder))
	if list, _ := s.SharedWithMe(ctx, ivan); len(list) != 2 {
		t.Fatalf("shared with Иван before = %+v", list)
	}

	// only what is shared with the user, not one's own or another's
	if err := s.LeaveShare(ctx, anna, folder, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("the owner leaving: %v", err)
	}
	if err := s.LeaveShare(ctx, ivan, plan, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a file as a folder: %v", err)
	}
	must(s.LeaveShare(ctx, ivan, folder, false))
	must(s.LeaveShare(ctx, ivan, plan, true))
	if list, _ := s.SharedWithMe(ctx, ivan); len(list) != 0 {
		t.Errorf("shared with Иван after = %+v", list)
	}
	if _, err := s.Open(ctx, ivan, "sh:"+folder); err == nil {
		t.Error("the folder given up still opens")
	}
	if _, err := s.Open(ctx, ivan, "sf:"+plan); err == nil {
		t.Error("the file given up still opens")
	}
	if conns, _ := s.Connections(ctx, ivan); len(conns) != 0 {
		t.Errorf("connections = %+v", conns)
	}
	if err := s.LeaveShare(ctx, ivan, folder, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("leaving twice: %v", err)
	}
	// the department share stays for the others, Иван's own share is gone
	if list, _ := s.SharedWithMe(ctx, olga); len(list) != 2 {
		t.Errorf("shared with Ольга = %+v", list)
	}
	shares, _ := s.Shares(ctx, []string{folder})
	if len(shares[folder]) != 1 || shares[folder][0].Kind != "department" {
		t.Errorf("shares of the folder = %+v", shares[folder])
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND actor_id = $2 AND kind = 'disk_share_left'`, anna, ivan).Scan(&n))
	if n != 2 {
		t.Errorf("the owner's notifications = %d", n)
	}

	// opened to him in person again — back
	must(s.SetShare(ctx, anna, my, folder, "", "user", ivan, "read"))
	if list, _ := s.SharedWithMe(ctx, ivan); len(list) != 1 || list[0].ID != folder || list[0].Access != "read" {
		t.Errorf("shared with Иван again = %+v", list)
	}
}
