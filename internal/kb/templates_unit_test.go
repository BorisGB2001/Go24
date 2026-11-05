package kb

import (
	"encoding/hex"
	"reflect"
	"testing"
)

func TestCountPages(t *testing.T) {
	tests := []struct {
		name string
		list []TemplatePage
		want int
	}{
		{
			name: "empty list",
			list: nil,
			want: 0,
		},
		{
			name: "flat list of pages",
			list: []TemplatePage{
				{Title: "P1"},
				{Title: "P2"},
				{Title: "P3"},
			},
			want: 3,
		},
		{
			name: "nested pages tree",
			list: []TemplatePage{
				{
					Title: "Root 1",
					Children: []TemplatePage{
						{Title: "Child 1.1"},
						{
							Title: "Child 1.2",
							Children: []TemplatePage{
								{Title: "Grandchild 1.2.1"},
							},
						},
					},
				},
				{
					Title: "Root 2",
					Children: []TemplatePage{
						{Title: "Child 2.1"},
					},
				},
			},
			// Root1 (1) + Child1.1 (1) + Child1.2 (1) + Grandchild (1) + Root2 (1) + Child2.1 (1) = 6
			want: 6,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := countPages(tc.list)
			if got != tc.want {
				t.Fatalf("countPages() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFlattenAndFill(t *testing.T) {
	tree := []TemplatePage{
		{
			Title: "Root 1",
			Body:  "body-1",
			Children: []TemplatePage{
				{Title: "Child 1.1", Body: "body-1-1"},
				{
					Title: "Child 1.2",
					Body:  "body-1-2",
					Children: []TemplatePage{
						{Title: "Grandchild 1.2.1", Body: "body-1-2-1"},
					},
				},
			},
		},
		{
			Title: "Root 2",
			Body:  "body-2",
		},
	}

	var extracted []string
	flatten(tree, &extracted)

	wantExtracted := []string{"body-1", "body-1-1", "body-1-2", "body-1-2-1", "body-2"}
	if !reflect.DeepEqual(extracted, wantExtracted) {
		t.Fatalf("flatten() = %v, want %v", extracted, wantExtracted)
	}

	// Modify the bodies and use fill to put them back into tree copy
	newTexts := []string{"new-1", "new-1-1", "new-1-2", "new-1-2-1", "new-2"}
	toFill := newTexts
	fill(tree, &toFill)

	if len(toFill) != 0 {
		t.Fatalf("expected all texts to be consumed by fill, remaining: %v", toFill)
	}

	var refilled []string
	flatten(tree, &refilled)
	if !reflect.DeepEqual(refilled, newTexts) {
		t.Fatalf("refilled texts = %v, want %v", refilled, newTexts)
	}
}

func TestBuiltinTree(t *testing.T) {
	tr := func(key string) string {
		return "trans:" + key
	}

	if len(builtins) == 0 {
		t.Fatal("expected builtins list to be non-empty")
	}

	// Test the first builtin (onboarding)
	b := builtins[0]
	if b.id != "onboarding" {
		t.Fatalf("expected first builtin to be 'onboarding', got %q", b.id)
	}

	tree := b.tree(tr)
	if len(tree) != len(b.pages) {
		t.Fatalf("b.tree() roots len = %d, want %d", len(tree), len(b.pages))
	}

	// Check translation applied to title and body
	first := tree[0]
	if first.Title != "trans:kb.tpl.onboarding.welcome.title" {
		t.Fatalf("first title = %q, want %q", first.Title, "trans:kb.tpl.onboarding.welcome.title")
	}
	if first.Body != "trans:kb.tpl.onboarding.welcome.body" {
		t.Fatalf("first body = %q, want %q", first.Body, "trans:kb.tpl.onboarding.welcome.body")
	}

	// Check child pages populated
	if len(first.Children) != 2 {
		t.Fatalf("first children len = %d, want 2", len(first.Children))
	}
	if first.Children[0].Title != "trans:kb.tpl.onboarding.first_day.title" {
		t.Fatalf("child title = %q", first.Children[0].Title)
	}
}

func TestRandomHex(t *testing.T) {
	for _, n := range []int{4, 8, 16, 32} {
		h1 := randomHex(n)
		h2 := randomHex(n)

		if len(h1) != 2*n {
			t.Fatalf("randomHex(%d) length = %d, want %d", n, len(h1), 2*n)
		}
		if _, err := hex.DecodeString(h1); err != nil {
			t.Fatalf("randomHex(%d) produced invalid hex %q: %v", n, h1, err)
		}
		if h1 == h2 {
			t.Fatalf("two calls to randomHex(%d) produced identical result: %q", n, h1)
		}
	}
}
