package kb

import (
	"testing"
)

// TestKBFollow: stars and «Недавние» of what one sees, who watches a page (its author, through a page above, the
// base) is notified of changes, new subpages and the trash, once while unread, and not of their own changes; a
// closed base tells no one outside it.
func TestKBFollow(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, dev, anna := newUser("boss"), newUser("dev"), newUser("anna")
	s := NewStore(pool)
	sp, err := s.CreateSpace(ctx, boss, "Регламенты", "", "")
	must(err)
	top, err := s.CreatePage(ctx, boss, sp, "", "Отпуск", "текст")
	must(err)
	notes := func(user, kind string) (n int) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, user, kind).Scan(&n))
		return n
	}

	// the author watches the page; dev's change tells boss, not dev
	if m, _ := s.MarksAt(ctx, boss, sp, top); !m.WatchPage || m.FavPage {
		t.Fatalf("author marks %+v", m)
	}
	must(s.SavePage(ctx, dev, top, "Отпуск", "текст 2"))
	must(s.SavePage(ctx, dev, top, "Отпуск", "текст 3")) // still unread — one notice
	if notes(boss, "kb_page_changed") != 1 || notes(dev, "kb_page_changed") != 0 {
		t.Fatal("change notices")
	}
	must(s.SavePage(ctx, boss, top, "Отпуск", "текст 4")) // one's own change
	if notes(boss, "kb_page_changed") != 1 {
		t.Fatal("own change told")
	}

	// anna watches the base: a new subpage by dev tells her and boss (the page above), she sees it watched through the base
	must(s.SetMark(ctx, anna, MarkWatch, sp, "", true))
	sub, err := s.CreatePage(ctx, dev, sp, top, "Как оформить", "")
	must(err)
	if notes(anna, "kb_page_new") != 1 || notes(boss, "kb_page_new") != 1 {
		t.Fatal("new page notices")
	}
	if m, _ := s.MarksAt(ctx, anna, sp, sub); m.WatchPage || !m.Inherited || !m.WatchSpace {
		t.Fatalf("inherited %+v", m)
	}
	if m, _ := s.MarksAt(ctx, boss, sp, sub); m.WatchPage || !m.Inherited {
		t.Fatalf("through the page above %+v", m)
	}
	must(s.SetMark(ctx, anna, MarkWatch, sp, "", false))
	if _, err := s.DeletePage(ctx, boss, sub); err != nil {
		t.Fatal(err)
	}
	if notes(dev, "kb_page_trashed") != 1 || notes(anna, "kb_page_trashed") != 0 {
		t.Fatal("trash notices")
	}
	var url string
	must(pool.QueryRow(ctx, `SELECT url FROM notifications WHERE user_id = $1 AND kind = 'kb_page_new'`, boss).Scan(&url))
	if url != "/kb/"+sp+"/"+sub {
		t.Fatalf("url %q", url)
	}

	// stars and «Недавние»
	must(s.SetMark(ctx, anna, MarkFav, sp, "", true))
	must(s.SetMark(ctx, anna, MarkFav, sp, top, true))
	must(s.SetMark(ctx, anna, MarkFav, sp, top, true)) // twice — once
	must(s.Viewed(ctx, anna, top))
	favs, err := s.Favorites(ctx, anna)
	must(err)
	if len(favs) != 2 || favs[0].PageID != "" || favs[1].PageID != top || favs[1].SpaceName != "Регламенты" {
		t.Fatalf("favorites %+v", favs)
	}
	recent, err := s.Recent(ctx, anna, 10)
	must(err)
	if len(recent) != 1 || recent[0].Title != "Отпуск" {
		t.Fatalf("recent %+v", recent)
	}
	if err := s.SetMark(ctx, anna, "like", sp, "", true); err == nil {
		t.Fatal("bad kind")
	}

	// a base closed to anna: no notices, no stars or recent pages of it, she cannot mark it
	must(s.SetMark(ctx, anna, MarkWatch, sp, top, true))
	must(s.SetRight(ctx, boss, sp, "user", dev, "edit"))
	must(s.SavePage(ctx, dev, top, "Отпуск", "текст 5"))
	if notes(anna, "kb_page_changed") != 0 {
		t.Fatal("closed base told")
	}
	if favs, _ := s.Favorites(ctx, anna); len(favs) != 0 {
		t.Fatalf("closed favorites %+v", favs)
	}
	if recent, _ := s.Recent(ctx, anna, 10); len(recent) != 0 {
		t.Fatal("closed recent")
	}
	if err := s.SetMark(ctx, anna, MarkFav, sp, top, false); err != ErrNotFound {
		t.Fatalf("mark of a closed base: %v", err)
	}

	// marks move with a page to another base
	other, err := s.CreateSpace(ctx, boss, "Другая", "", "")
	must(err)
	must(s.MovePage(ctx, boss, top, other, ""))
	if m, _ := s.MarksAt(ctx, boss, other, top); !m.WatchPage {
		t.Fatalf("moved marks %+v", m)
	}
}
