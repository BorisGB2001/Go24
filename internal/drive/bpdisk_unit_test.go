package drive

import (
	"context"
	"errors"
	"testing"
)

func TestObjectLinks(t *testing.T) {
	// A file links to its about card; a folder links to its location within its drive.
	for _, c := range []struct {
		name       string
		obj        Object
		wantLink   string
		wantFolder string
	}{
		{
			name: "file at root of user drive",
			obj: Object{
				ID:    "f-101",
				Kind:  "file",
				Drive: Drive{Kind: "user"},
			},
			wantLink:   "/files/f-101/about",
			wantFolder: "/disk/my",
		},
		{
			name: "file inside a folder of company drive",
			obj: Object{
				ID:       "f-102",
				Kind:     "file",
				FolderID: "folder-42",
				Drive:    Drive{Kind: "company"},
			},
			wantLink:   "/files/f-102/about",
			wantFolder: "/disk/company?folder=folder-42",
		},
		{
			name: "folder at top of project drive",
			obj: Object{
				ID:    "folder-1",
				Kind:  "folder",
				Drive: Drive{Kind: "project", ProjectKey: "DEV"},
			},
			wantLink:   "/disk/p/DEV?folder=folder-1",
			wantFolder: "/disk/p/DEV",
		},
		{
			name: "nested folder within shared folder drive",
			obj: Object{
				ID:       "folder-2",
				Kind:     "folder",
				FolderID: "folder-parent",
				Drive:    Drive{Root: "sh-999"},
			},
			wantLink:   "/disk/sh/sh-999?folder=folder-2",
			wantFolder: "/disk/sh/sh-999?folder=folder-parent",
		},
		{
			name: "folder in shared file context",
			obj: Object{
				ID:       "folder-3",
				Kind:     "folder",
				FolderID: "folder-p",
				Drive:    Drive{File: "sf-888"},
			},
			wantLink:   "/disk/sf/sf-888?folder=folder-3",
			wantFolder: "/disk/sf/sf-888?folder=folder-p",
		},
		{
			name: "folder in employee drive managed by admin",
			obj: Object{
				ID:    "folder-4",
				Kind:  "folder",
				Drive: Drive{Employee: true, OwnerID: "u-admin"},
			},
			wantLink:   "/disk/u/u-admin?folder=folder-4",
			wantFolder: "/disk/u/u-admin",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.obj.Link(); got != c.wantLink {
				t.Errorf("Object.Link() = %q, want %q", got, c.wantLink)
			}
			if got := c.obj.FolderLink(); got != c.wantFolder {
				t.Errorf("Object.FolderLink() = %q, want %q", got, c.wantFolder)
			}
		})
	}
}

func TestObjectOfValidation(t *testing.T) {
	// ObjectOf parses ref strings and rejects malformed references before querying storage.
	s := NewStore(nil, nil)
	ctx := context.Background()

	for _, ref := range []string{
		"",
		"   ",
		"\t\n",
		"/files//about",
		"/disk/my?folder=&more",
		"ref with spaces",
		"invalid/path/part",
		"ref?question",
		"ref&ampersand",
	} {
		_, err := s.ObjectOf(ctx, "user-1", ref)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ObjectOf(%q) error = %v, want ErrNotFound", ref, err)
		}
	}
}
