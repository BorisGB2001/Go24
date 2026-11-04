package drive

import (
	"testing"
)

func TestSharingRuled(t *testing.T) {
	for _, c := range []struct {
		name string
		sh   Sharing
		want bool
	}{
		{
			name: "no folders above file",
			sh:   Sharing{},
			want: false,
		},
		{
			name: "folders without explicit rights (inherits whole drive access)",
			sh: Sharing{
				Folders: []Reached{
					{ID: "f1", Name: "Folder 1", Rights: nil},
					{ID: "f2", Name: "Folder 2", Rights: []Right{}},
				},
			},
			want: false,
		},
		{
			name: "one folder with explicit rights makes sharing ruled",
			sh: Sharing{
				Folders: []Reached{
					{ID: "f1", Name: "Folder 1"},
					{ID: "f2", Name: "Folder 2", Rights: []Right{{ID: "r1", Access: "read"}}},
				},
			},
			want: true,
		},
		{
			name: "multiple folders with rights",
			sh: Sharing{
				Folders: []Reached{
					{ID: "f1", Rights: []Right{{ID: "r1"}}},
					{ID: "f2", Rights: []Right{{ID: "r2"}}},
				},
			},
			want: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.sh.Ruled(); got != c.want {
				t.Errorf("Sharing.Ruled() = %v, want %v", got, c.want)
			}
		})
	}
}
