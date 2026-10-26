package kb

import (
	"errors"
	"testing"
	"time"
)

// TestKBTrash: a deleted page goes to the trash with its subpages and comes back to its place; a page whose parent
// is in the trash comes to the top; a base goes and comes back with its pages; who may restore and purge; the
// expired go by themselves.
func TestKBTrash(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, guest := newUser("boss"), newUser("guest")
	s := NewStore(pool)
	space, err := s.CreateSpace(ctx, boss, "Регламенты", "", "")
	must(err)
	root, err := s.CreatePage(ctx, guest, space, "", "Отпуск", "")
	must(err)
	child, err := s.CreatePage(ctx, guest, space, root, "Больничный", "")
	must(err)
	grand, err := s.CreatePage(ctx, guest, space, child, "Справка", "")
	must(err)

	if _, err := s.DeletePage(ctx, guest, child); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Page(ctx, boss, grand); !errors.Is(err, ErrNotFound) {
		t.Fatal("a subpage of a deleted page is still seen")
	}
	if hits, _ := s.Search(ctx, boss, "Справка", ""); len(hits) != 0 {
		t.Fatalf("search finds the trash: %+v", hits)
	}
	list, err := s.Trash(ctx, guest, 30)
	must(err)
	if len(list) != 1 || list[0].PageID != child || list[0].Pages != 1 || list[0].DeletedBy != "guest" || list[0].PurgeAt == nil {
		t.Fatalf("trash = %+v", list)
	}
	// the page above goes too: the restored one comes to the top of the base
	if _, err := s.DeletePage(ctx, guest, root); err != nil {
		t.Fatal(err)
	}
	if sp, err := s.RestorePage(ctx, guest, child); err != nil || sp != space {
		t.Fatalf("restore: %q %v", sp, err)
	}
	p, _, err := s.Page(ctx, boss, child)
	must(err)
	if p.ParentID != "" {
		t.Fatalf("restored under a deleted page: %+v", p)
	}
	if _, _, err := s.Page(ctx, boss, grand); err != nil {
		t.Fatal("the subpage did not come back:", err)
	}
	if _, err := s.RestorePage(ctx, guest, grand); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a live page restored: %v", err)
	}
	// purging the page above keeps a page deleted under it on its own
	if _, err := s.DeletePage(ctx, guest, child); err != nil {
		t.Fatal(err)
	}
	must(s.PurgePage(ctx, guest, root))
	if list, _ := s.Trash(ctx, guest, 0); len(list) != 1 || list[0].PageID != child || list[0].PurgeAt != nil {
		t.Fatalf("trash after purge = %+v", list)
	}

	// a base: the guest writes in it but does not manage it
	if err := s.DeleteSpace(ctx, guest, space); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest deleted the base: %v", err)
	}
	must(s.DeleteSpace(ctx, boss, space))
	if _, err := s.Space(ctx, boss, space); !errors.Is(err, ErrNotFound) {
		t.Fatal("a deleted base is still seen")
	}
	if list, _ := s.Trash(ctx, guest, 0); len(list) != 0 {
		t.Fatalf("guest sees the deleted base: %+v", list)
	}
	if list, _ := s.Trash(ctx, boss, 0); len(list) != 1 || list[0].PageID != "" || list[0].SpaceID != space {
		t.Fatalf("boss's trash = %+v", list)
	}
	if err := s.RestoreSpace(ctx, guest, space); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest restored the base: %v", err)
	}
	must(s.RestoreSpace(ctx, boss, space))
	if list, _ := s.Trash(ctx, guest, 0); len(list) != 1 || list[0].PageID != child { // the page deleted before stays there
		t.Fatalf("trash after the base came back = %+v", list)
	}

	must(s.DeleteSpace(ctx, boss, space))
	if n, err := s.PurgeExpired(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("purged the fresh: %d %v", n, err)
	}
	if n, err := s.PurgeExpired(ctx, time.Now().Add(time.Hour)); err != nil || n != 2 {
		t.Fatalf("purge expired: %d %v", n, err)
	}
	var left int
	must(pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM kb_spaces_all) + (SELECT count(*) FROM kb_pages_all)`).Scan(&left))
	if left != 0 {
		t.Fatalf("left after purge: %d", left)
	}
}
