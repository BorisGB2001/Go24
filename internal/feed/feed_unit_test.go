package feed

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

func TestFileImage(t *testing.T) {
	for _, c := range []struct {
		ct   string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"image/gif", true},
		{"image/webp", true},
		{"image/svg+xml", false}, // SVG is excluded (can carry scripts)
		{"image/svg", true},
		{"text/plain", false},
		{"application/pdf", false},
		{"", false},
	} {
		f := File{ContentType: c.ct}
		if got := f.Image(); got != c.want {
			t.Errorf("File{ContentType: %q}.Image() = %v, want %v", c.ct, got, c.want)
		}
	}
}

func TestCommentFileNames(t *testing.T) {
	cEmpty := Comment{}
	if got := cEmpty.FileNames(); len(got) != 0 {
		t.Errorf("Comment{}.FileNames() = %v, want empty", got)
	}

	cFiles := Comment{
		Files: []File{
			{Name: "report.pdf"},
			{Name: "photo.png"},
		},
	}
	want := []string{"report.pdf", "photo.png"}
	if got := cFiles.FileNames(); !slices.Equal(got, want) {
		t.Errorf("Comment.FileNames() = %v, want %v", got, want)
	}
}

func TestFilterEvents(t *testing.T) {
	for _, c := range []struct {
		name string
		f    Filter
		want bool
	}{
		{"default filter", Filter{}, true},
		{"kind tasks", Filter{Kind: "tasks"}, true},
		{"kind empty", Filter{Kind: ""}, true},
		{"posts only", Filter{Posts: true}, false},
		{"important", Filter{Important: true}, false},
		{"hidden", Filter{Hidden: true}, false},
		{"mine", Filter{Mine: true}, false},
		{"favorites", Filter{Favorites: true}, false},
		{"pinned", Filter{Pinned: true}, false},
		{"mentions", Filter{Mentions: true}, false},
		{"to audience", Filter{To: []string{"user1"}}, false},
		{"project set", Filter{Project: "proj1"}, false},
		{"query search", Filter{Query: "announcement"}, false},
		{"author set", Filter{Author: "user1"}, false},
		{"post id set", Filter{Post: "post1"}, false},
		{"tag set", Filter{Tag: "release"}, false},
		{"kind poll", Filter{Kind: "poll"}, false},
		{"kind gratitude", Filter{Kind: "gratitude"}, false},
		{"kind bp", Filter{Kind: "bp"}, false},
		{"kind welcome", Filter{Kind: SourceWelcome}, false},
		{"kind meeting", Filter{Kind: SourceMeeting}, false},
		{"kind kb", Filter{Kind: SourceKB}, false},
	} {
		if got := c.f.events(); got != c.want {
			t.Errorf("%s: Filter.events() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMarkNew(t *testing.T) {
	viewer := "user_viewer"
	other := "user_other"
	since := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	beforeSince := since.Add(-1 * time.Hour)
	afterSince := since.Add(1 * time.Hour)

	items := []Item{
		// Non-post item (e.g. task event): Post is nil, should not panic
		{Event: &tasks.Event{}, At: afterSince},

		// Viewer's own post, created after since: New must be false
		{
			Post: &Post{
				ID:        "p1",
				AuthorID:  viewer,
				CreatedAt: afterSince,
			},
		},

		// Other author's post, created before since: New must be false
		{
			Post: &Post{
				ID:        "p2",
				AuthorID:  other,
				CreatedAt: beforeSince,
			},
		},

		// Other author's post, created exactly at since: New must be false
		{
			Post: &Post{
				ID:        "p3",
				AuthorID:  other,
				CreatedAt: since,
			},
		},

		// Other author's post, created after since: New must be true; with comments
		{
			Post: &Post{
				ID:        "p4",
				AuthorID:  other,
				CreatedAt: afterSince,
				Comments: []Comment{
					// Comment by viewer after since: New must be false
					{ID: "c1", AuthorID: viewer, CreatedAt: afterSince},
					// Comment by other before since: New must be false
					{ID: "c2", AuthorID: other, CreatedAt: beforeSince},
					// Comment by other after since: New must be true
					{ID: "c3", AuthorID: other, CreatedAt: afterSince},
				},
			},
		},
	}

	MarkNew(items, viewer, since)

	if items[1].Post.New {
		t.Error("viewer's own post marked as New")
	}
	if items[2].Post.New {
		t.Error("post before since marked as New")
	}
	if items[3].Post.New {
		t.Error("post at exactly since marked as New")
	}
	if !items[4].Post.New {
		t.Error("post after since by other author not marked as New")
	}

	c := items[4].Post.Comments
	if c[0].New {
		t.Error("comment by viewer marked as New")
	}
	if c[1].New {
		t.Error("comment before since marked as New")
	}
	if !c[2].New {
		t.Error("comment after since by other author not marked as New")
	}
}

func TestExtraIDs(t *testing.T) {
	if got := extraIDs(nil); len(got) != 0 {
		t.Errorf("extraIDs(nil) = %v, want empty", got)
	}
	if got := extraIDs([]tasks.Mention{}); len(got) != 0 {
		t.Errorf("extraIDs([]) = %v, want empty", got)
	}

	list := []tasks.Mention{
		{ID: "u1", Name: "User One"},
		{ID: "u2", Name: "User Two"},
	}
	want := []string{"u1", "u2"}
	if got := extraIDs(list); !slices.Equal(got, want) {
		t.Errorf("extraIDs(%v) = %v, want %v", list, got, want)
	}
}

func TestCreatePostValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Empty body
	_, err := s.CreatePost(ctx, "author", "", "Title", "", false)
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.post_empty" {
		t.Errorf("CreatePost(empty body) err = %v, want err.post_empty", err)
	}

	// Whitespace body
	_, err = s.CreatePost(ctx, "author", "", "Title", "   \t\n  ", false)
	if !errors.As(err, &ie) || ie.Key != "err.post_empty" {
		t.Errorf("CreatePost(whitespace body) err = %v, want err.post_empty", err)
	}

	// Body too long (> 20000 runes)
	longBody := strings.Repeat("a", 20001)
	_, err = s.CreatePost(ctx, "author", "", "Title", longBody, false)
	if !errors.As(err, &ie) || ie.Key != "err.post_long" {
		t.Errorf("CreatePost(body > 20000 runes) err = %v, want err.post_long", err)
	}

	// Title too long (> 300 runes)
	longTitle := strings.Repeat("t", 301)
	_, err = s.CreatePost(ctx, "author", "", longTitle, "Body", false)
	if !errors.As(err, &ie) || ie.Key != "err.post_long" {
		t.Errorf("CreatePost(title > 300 runes) err = %v, want err.post_long", err)
	}

	// Project and limited audience together is invalid
	_, err = s.CreatePostTo(ctx, "author", "proj1", Audience{Users: []string{"u1"}}, "Title", "Body", false)
	if !errors.As(err, &ie) || ie.Key != "err.bad_form" {
		t.Errorf("CreatePostTo(project and limited) err = %v, want err.bad_form", err)
	}
}

func TestAddCommentValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Empty comment without files
	_, err := s.AddComment(ctx, "author", "p1", "")
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.comment_empty" {
		t.Errorf("AddComment(empty body) err = %v, want err.comment_empty", err)
	}

	// Whitespace comment without files
	_, err = s.AddComment(ctx, "author", "p1", "   \n\t  ")
	if !errors.As(err, &ie) || ie.Key != "err.comment_empty" {
		t.Errorf("AddComment(whitespace body) err = %v, want err.comment_empty", err)
	}

	// Comment too long (> 5000 runes)
	longBody := strings.Repeat("c", 5001)
	_, err = s.AddComment(ctx, "author", "p1", longBody)
	if !errors.As(err, &ie) || ie.Key != "err.post_long" {
		t.Errorf("AddComment(body > 5000 runes) err = %v, want err.post_long", err)
	}
}

func TestEditPostValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Empty body
	err := s.EditPost(ctx, "author", "p1", "Title", "")
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.post_empty" {
		t.Errorf("EditPost(empty body) err = %v, want err.post_empty", err)
	}

	// Whitespace body
	err = s.EditPost(ctx, "author", "p1", "Title", "   \t  ")
	if !errors.As(err, &ie) || ie.Key != "err.post_empty" {
		t.Errorf("EditPost(whitespace body) err = %v, want err.post_empty", err)
	}

	// Body too long (> 20000 runes)
	err = s.EditPost(ctx, "author", "p1", "Title", strings.Repeat("b", 20001))
	if !errors.As(err, &ie) || ie.Key != "err.post_long" {
		t.Errorf("EditPost(body > 20000 runes) err = %v, want err.post_long", err)
	}

	// Title too long (> 300 runes)
	err = s.EditPost(ctx, "author", "p1", strings.Repeat("t", 301), "Body")
	if !errors.As(err, &ie) || ie.Key != "err.post_long" {
		t.Errorf("EditPost(title > 300 runes) err = %v, want err.post_long", err)
	}
}

func TestMarkValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Unknown mark type
	for _, m := range []string{"", "star", "bookmark", "like", "unknown"} {
		on, err := s.Mark(ctx, "user1", "p1", m)
		if on {
			t.Errorf("Mark(%q) on = true, want false", m)
		}
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.bad_form" {
			t.Errorf("Mark(%q) err = %v, want err.bad_form", m, err)
		}
	}
}

func TestShareValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Empty audience
	for _, a := range []Audience{
		{},
		{Users: []string{}, Depts: []string{}},
	} {
		n, err := s.Share(ctx, "user1", "p1", a)
		if n != 0 {
			t.Errorf("Share(empty) n = %d, want 0", n)
		}
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != "err.post_to" {
			t.Errorf("Share(empty) err = %v, want err.post_to", err)
		}
	}
}

func TestSeeValidation(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// Empty postIDs must return nil immediately without calling DB
	if err := s.See(ctx, "user1", nil); err != nil {
		t.Errorf("See(nil) err = %v, want nil", err)
	}
	if err := s.See(ctx, "user1", []string{}); err != nil {
		t.Errorf("See([]) err = %v, want nil", err)
	}
}
