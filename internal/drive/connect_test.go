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

// TestDriveConnect: a folder shared with the user and a project's drive are connected to «Мой диск» once; not what is
// not theirs to open, nor their own drives; what they may no longer open is not listed; disconnecting keeps access.
func TestDriveConnect(t *testing.T) {
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
	var anna, ivan, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'e@t') RETURNING id`).Scan(&eve))
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	_, err = ts.CreateProject(ctx, anna, "CN", "Стройка", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, anna, "CN")
	must(ts.SetMember(ctx, p, ivan, "viewer"))
	my, _ := s.Open(ctx, anna, "my")
	folder, err := s.CreateFolder(ctx, anna, my, "", "Сметы")
	must(err)
	must(s.SetShare(ctx, anna, my, folder, "", "user", ivan, "read"))

	must(s.Connect(ctx, ivan, "sh:"+folder))
	must(s.Connect(ctx, ivan, "sh:"+folder)) // twice — one
	must(s.Connect(ctx, ivan, "CN"))
	list, err := s.Connections(ctx, ivan)
	must(err)
	if len(list) != 2 || list[0].Which != "sh:"+folder || list[0].Name != "Сметы" || list[0].Owner != "Анна" || list[0].Path != "/disk/sh/"+folder ||
		list[0].Project || list[1].Which != "CN" || list[1].Name != "Стройка" || !list[1].Project || list[1].Path != "/disk/p/CN" {
		t.Fatalf("connections = %+v", list)
	}
	sh, _ := s.Open(ctx, ivan, "sh:"+folder)
	if !s.IsConnected(ctx, ivan, sh) {
		t.Error("the shared folder is not connected")
	}
	for _, which := range []string{"my", "company", "sh:" + folder} {
		if err := s.Connect(ctx, eve, which); err == nil {
			t.Errorf("eve connects %s", which)
		}
	}
	if err := s.Connect(ctx, eve, "CN"); !errors.Is(err, tasks.ErrNotFound) && !errors.Is(err, ErrNotFound) && !errors.Is(err, tasks.ErrForbidden) {
		t.Errorf("eve connects a project not hers: %v", err)
	}
	// no longer shared: not listed; shared again — it is back
	must(pool.QueryRow(ctx, `DELETE FROM drive_shares WHERE folder_id = $1 RETURNING 1`, folder).Scan(new(int)))
	if list, _ = s.Connections(ctx, ivan); len(list) != 1 || list[0].Which != "CN" {
		t.Errorf("connections without the share = %+v", list)
	}
	must(s.SetShare(ctx, anna, my, folder, "", "user", ivan, "read"))
	must(s.Disconnect(ctx, ivan, "CN"))
	if list, _ = s.Connections(ctx, ivan); len(list) != 1 || list[0].Which != "sh:"+folder {
		t.Errorf("connections after disconnecting = %+v", list)
	}
	if _, err := s.Open(ctx, ivan, "CN"); err != nil {
		t.Errorf("the project's drive after disconnecting: %v", err)
	}
	must(s.Disconnect(ctx, ivan, "sh:"+folder))
	if list, _ = s.Connections(ctx, ivan); len(list) != 0 || s.IsConnected(ctx, ivan, sh) {
		t.Errorf("connections = %+v", list)
	}
}
