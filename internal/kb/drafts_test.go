package kb

import (
	"errors"
	"testing"
	"time"
)

// TestKBDrafts: the editor's text is kept as a draft until saved, the others see who edits the page now, saving over
// a version someone has saved since is refused (ErrChanged) and keeps nothing of theirs overwritten, «Мои черновики»
// lists only what differs from the page and only in the bases one still sees.
func TestKBDrafts(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, dev, anna := newUser("boss"), newUser("dev"), newUser("anna")
	s := NewStore(pool)
	sp, err := s.CreateSpace(ctx, boss, "Регламенты", "", "")
	must(err)
	pg, err := s.CreatePage(ctx, boss, sp, "", "Отпуск", "текст")
	must(err)
	p, _, err := s.Page(ctx, boss, pg)
	must(err)
	base := p.Updated

	// the editor opened unchanged: anna sees dev editing, but it is no draft of his
	must(s.SaveDraft(ctx, dev, pg, "Отпуск", "текст", base))
	if names, _ := s.Editing(ctx, anna, pg); len(names) != 1 || names[0] != "dev" {
		t.Fatalf("editing %v", names)
	}
	if names, _ := s.Editing(ctx, dev, pg); len(names) != 0 {
		t.Fatalf("one's own editor %v", names)
	}
	if d, _ := s.DraftOf(ctx, dev, pg); d == nil || d.Changed || d.Stale {
		t.Fatalf("unchanged draft %+v", d)
	}
	if list, _ := s.Drafts(ctx, dev); len(list) != 0 {
		t.Fatalf("unchanged in drafts %v", list)
	}
	// leaving it unchanged: nothing is kept and no one edits
	must(s.LeaveDraft(ctx, dev, pg))
	if d, _ := s.DraftOf(ctx, dev, pg); d != nil {
		t.Fatalf("left unchanged %+v", d)
	}

	// dev types and closes the tab: the draft stays, he is no longer editing
	must(s.SaveDraft(ctx, dev, pg, "Отпуск", "текст dev", base))
	must(s.LeaveDraft(ctx, dev, pg))
	if d, _ := s.DraftOf(ctx, dev, pg); d == nil || !d.Changed || d.Stale || d.Body != "текст dev" || !d.Base.Equal(base) {
		t.Fatalf("draft %+v", d)
	}
	if names, _ := s.Editing(ctx, anna, pg); len(names) != 0 {
		t.Fatalf("closed editor %v", names)
	}
	if list, _ := s.Drafts(ctx, dev); len(list) != 1 || list[0].PageID != pg || list[0].SpaceName != "Регламенты" {
		t.Fatalf("my drafts %+v", list)
	}

	// boss saves meanwhile: dev's draft is stale, saving it over the old version is refused and changes nothing
	must(s.SavePageAt(ctx, boss, pg, "Отпуск", "текст boss", base))
	if d, _ := s.DraftOf(ctx, dev, pg); d == nil || !d.Stale {
		t.Fatalf("stale %+v", d)
	}
	if err := s.SavePageAt(ctx, dev, pg, "Отпуск", "текст dev", base); !errors.Is(err, ErrChanged) {
		t.Fatalf("over the old version: %v", err)
	}
	if p, _, _ := s.Page(ctx, anna, pg); p.Body != "текст boss" {
		t.Fatalf("overwritten %q", p.Body)
	}
	// «Сохранить мою версию» over the current one: saved, the draft goes, boss's text is in the history
	p, _, err = s.Page(ctx, dev, pg)
	must(err)
	must(s.SavePageAt(ctx, dev, pg, "Отпуск", "текст dev", p.Updated))
	if d, _ := s.DraftOf(ctx, dev, pg); d != nil {
		t.Fatalf("draft after saving %+v", d)
	}
	if v, _ := s.Versions(ctx, pg); len(v) != 2 {
		t.Fatalf("versions %d", len(v))
	}
	// without a base (the API without one, a restored version) — saved as before
	must(s.SavePageAt(ctx, boss, pg, "Отпуск", "текст 3", time.Time{}))

	// «Отменить черновик»
	must(s.SaveDraft(ctx, anna, pg, "", "набросок", time.Time{}))
	if d, _ := s.DraftOf(ctx, anna, pg); d == nil || !d.Changed {
		t.Fatalf("anna's draft %+v", d)
	}
	must(s.DropDraft(ctx, anna, pg))
	if d, _ := s.DraftOf(ctx, anna, pg); d != nil {
		t.Fatalf("dropped %+v", d)
	}

	// a closed base: no drafts there, nor are the drafts kept listed
	must(s.SaveDraft(ctx, anna, pg, "Отпуск", "набросок", time.Time{}))
	must(s.SetRight(ctx, boss, sp, "user", dev, "edit"))
	if err := s.SaveDraft(ctx, anna, pg, "Отпуск", "ещё", time.Time{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed base draft: %v", err)
	}
	if list, _ := s.Drafts(ctx, anna); len(list) != 0 {
		t.Fatalf("closed base in drafts %v", list)
	}
	// a reader may not keep drafts
	must(s.SetRight(ctx, boss, sp, "user", anna, "read"))
	if err := s.SaveDraft(ctx, anna, pg, "Отпуск", "ещё", time.Time{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("reader draft: %v", err)
	}
}
