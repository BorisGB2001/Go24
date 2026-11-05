package openlines

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestCheckMessage(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		file    *File
		wantErr bool
		errKey  string
	}{
		{
			name:    "empty body and nil file",
			body:    "",
			file:    nil,
			wantErr: true,
			errKey:  "err.ol_message",
		},
		{
			name:    "body too long over 5000 runes",
			body:    strings.Repeat("ж", 5001),
			file:    nil,
			wantErr: true,
			errKey:  "err.ol_message",
		},
		{
			name:    "body exactly 5000 runes",
			body:    strings.Repeat("ж", 5000),
			file:    nil,
			wantErr: false,
		},
		{
			name:    "valid text only",
			body:    "Hello world!",
			file:    nil,
			wantErr: false,
		},
		{
			name:    "file missing key",
			body:    "look at this",
			file:    &File{Key: "", Name: "photo.jpg", Size: 1024},
			wantErr: true,
			errKey:  "err.ol_file",
		},
		{
			name:    "file missing name",
			body:    "look at this",
			file:    &File{Key: "openlines/2026/01/k/f", Name: "", Size: 1024},
			wantErr: true,
			errKey:  "err.ol_file",
		},
		{
			name:    "file size exceeds MaxFile",
			body:    "",
			file:    &File{Key: "k", Name: "big.zip", Size: MaxFile + 1},
			wantErr: true,
			errKey:  "err.ol_file",
		},
		{
			name:    "file size exactly MaxFile",
			body:    "",
			file:    &File{Key: "k", Name: "big.zip", Size: MaxFile},
			wantErr: false,
		},
		{
			name:    "valid file only without body",
			body:    "",
			file:    &File{Key: "k", Name: "doc.pdf", Size: 100},
			wantErr: false,
		},
		{
			name:    "valid body and file",
			body:    "Here is invoice",
			file:    &File{Key: "k", Name: "inv.pdf", Size: 200},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkMessage(tc.body, tc.file)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkMessage() error = %v; wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && tc.errKey != "" {
				var ve *i18n.Error
				if !errors.As(err, &ve) {
					t.Fatalf("expected i18n.Error, got %T (%v)", err, err)
				}
			}
		})
	}
}

func TestFileName(t *testing.T) {
	var nilFile *File
	if got := nilFile.name(); got != "" {
		t.Errorf("(*File)(nil).name() = %q; want empty", got)
	}
	f := &File{Name: "document.pdf"}
	if got := f.name(); got != "document.pdf" {
		t.Errorf("f.name() = %q; want 'document.pdf'", got)
	}
}

func TestStoredFileSecret(t *testing.T) {
	sf := StoredFile{
		secret: "correct-secret-12345",
	}

	if !sf.Secret("correct-secret-12345") {
		t.Error("Secret() must return true for matching secret")
	}
	if sf.Secret("wrong-secret") {
		t.Error("Secret() must return false for mismatching secret")
	}
	if sf.Secret("") {
		t.Error("Secret() must return false for empty string")
	}

	emptySF := StoredFile{secret: ""}
	if emptySF.Secret("") || emptySF.Secret("any") {
		t.Error("StoredFile with empty secret must never validate")
	}
}

func TestFileLink(t *testing.T) {
	m := Message{
		ID:         42,
		FileSecret: "secret789",
	}
	got := FileLink("https://crm.local", "wid123", m)
	want := "https://crm.local/w/wid123/file/42/secret789"
	if got != want {
		t.Errorf("FileLink() = %q; want %q", got, want)
	}
}

func TestCleanName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"image.png", "image.png"},
		{"/path/to/my_photo.jpg", "my_photo.jpg"},
		{`C:\Users\Admin\Desktop\report.pdf`, "report.pdf"},
		{`mixed/path\to/doc.docx`, "doc.docx"},
		{"", "file"},
		{".", "file"},
		{"..", "file"},
		{"/a/b/.", "file"},
		{"/a/b/..", "file"},
		{"/trailing/slash/", "file"},
		{`C:\trailing\slash\`, "file"},
		{"///", "file"},
		{`\\\`, "file"},
	}

	for _, tc := range cases {
		if got := CleanName(tc.in); got != tc.want {
			t.Errorf("CleanName(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestNullID(t *testing.T) {
	if got := nullID(""); got != nil {
		t.Errorf("nullID('') = %v; want nil", got)
	}
	if got := nullID("abc-123"); got != "abc-123" {
		t.Errorf("nullID('abc-123') = %v; want 'abc-123'", got)
	}
}

func TestStoreTr(t *testing.T) {
	sNil := &Store{}
	if got := sNil.tr("some.key", 123); got != "some.key" {
		t.Errorf("tr with nil Tr = %q; want 'some.key'", got)
	}

	sWithTr := &Store{
		Tr: func(key string, args ...any) string {
			return key + ":translated"
		},
	}
	if got := sWithTr.tr("some.key"); got != "some.key:translated" {
		t.Errorf("tr with custom Tr = %q; want 'some.key:translated'", got)
	}
}
