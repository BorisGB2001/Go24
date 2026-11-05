package kb

import (
	"reflect"
	"testing"
)

func TestPath(t *testing.T) {
	// Build a sample page tree:
	// root1
	// ├── child11
	// │   └── grand111
	// └── child12
	// root2
	// └── child21

	grand111 := &Page{ID: "g111", Title: "Grandchild 1.1.1"}
	child11 := &Page{ID: "c11", Title: "Child 1.1", Children: []*Page{grand111}}
	child12 := &Page{ID: "c12", Title: "Child 1.2"}
	root1 := &Page{ID: "r1", Title: "Root 1", Children: []*Page{child11, child12}}

	child21 := &Page{ID: "c21", Title: "Child 2.1"}
	root2 := &Page{ID: "r2", Title: "Root 2", Children: []*Page{child21}}

	tree := []*Page{root1, root2}

	tests := []struct {
		name   string
		tree   []*Page
		target string
		want   []string // page IDs in order from root to target
	}{
		{
			name:   "root level match",
			tree:   tree,
			target: "r1",
			want:   []string{"r1"},
		},
		{
			name:   "second root level match",
			tree:   tree,
			target: "r2",
			want:   []string{"r2"},
		},
		{
			name:   "child level match",
			tree:   tree,
			target: "c11",
			want:   []string{"r1", "c11"},
		},
		{
			name:   "second child level match",
			tree:   tree,
			target: "c12",
			want:   []string{"r1", "c12"},
		},
		{
			name:   "grandchild level match",
			tree:   tree,
			target: "g111",
			want:   []string{"r1", "c11", "g111"},
		},
		{
			name:   "child in second root",
			tree:   tree,
			target: "c21",
			want:   []string{"r2", "c21"},
		},
		{
			name:   "not found in tree",
			tree:   tree,
			target: "nonexistent",
			want:   nil,
		},
		{
			name:   "empty tree",
			tree:   nil,
			target: "r1",
			want:   nil,
		},
		{
			name:   "empty target id",
			tree:   tree,
			target: "",
			want:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPages := Path(tc.tree, tc.target)
			var gotIDs []string
			if gotPages != nil {
				gotIDs = make([]string, len(gotPages))
				for i, p := range gotPages {
					gotIDs[i] = p.ID
				}
			}
			if !reflect.DeepEqual(gotIDs, tc.want) {
				t.Fatalf("Path(tree, %q) = %v, want %v", tc.target, gotIDs, tc.want)
			}
		})
	}
}

func TestDescendants(t *testing.T) {
	tests := []struct {
		name string
		page *Page
		want int
	}{
		{
			name: "leaf with nil children",
			page: &Page{ID: "p1", Children: nil},
			want: 0,
		},
		{
			name: "leaf with empty children",
			page: &Page{ID: "p1", Children: []*Page{}},
			want: 0,
		},
		{
			name: "node with direct children only",
			page: &Page{
				ID: "root",
				Children: []*Page{
					{ID: "c1"},
					{ID: "c2"},
					{ID: "c3"},
				},
			},
			want: 3,
		},
		{
			name: "nested tree multiple depths",
			page: &Page{
				ID: "root",
				Children: []*Page{
					{
						ID: "c1",
						Children: []*Page{
							{ID: "g11"},
							{
								ID: "g12",
								Children: []*Page{
									{ID: "gg121"},
								},
							},
						},
					},
					{
						ID: "c2",
						Children: []*Page{
							{ID: "g21"},
						},
					},
				},
			},
			// c1 + g11 + g12 + gg121 + c2 + g21 = 6 descendants
			want: 6,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Descendants(tc.page); got != tc.want {
				t.Fatalf("Descendants(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}
