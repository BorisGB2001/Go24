package web

import (
	"bytes"
	"context"
	"html/template"
	"io"
	"os/exec"
	"path"
	"strings"
	"time"

	"go24/internal/drive"
	"go24/internal/mail"
)

// contentMax bounds a file whose text is read for the search of «Диск».
const contentMax = 20 << 20

// IndexDrive reads the text of the files of the drives not read yet (new ones and new versions), up to max of them:
// how many were read. A file that cannot be read (no text, too big, the storage failed) is kept with none.
func (s *Server) IndexDrive(ctx context.Context, max int) (int, error) {
	list, err := s.Drive.Unindexed(ctx, max)
	if err != nil {
		return 0, err
	}
	for _, f := range list {
		text := ""
		if f.Size > 0 && f.Size <= contentMax && textual(f.Name, f.ContentType) {
			data, err := s.readContent(ctx, f)
			if err != nil {
				s.Log.Warn("drive content", "file", f.ID, "err", err)
			} else {
				text = fileText(ctx, f.Name, f.ContentType, data)
			}
		}
		if err := s.Drive.SetContent(ctx, f.ID, f.Key, text); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// snippets is where the files found by a search have its words in their text (the found by their name have none).
func (s *Server) snippets(ctx context.Context, files []drive.File, q string) (map[string]template.HTML, error) {
	ids := make([]string, len(files))
	for i, f := range files {
		ids[i] = f.ID
	}
	m, err := s.Drive.Snippets(ctx, ids, q)
	out := make(map[string]template.HTML, len(m))
	for id, h := range m {
		out[id] = template.HTML(h) // escaped by drive.Snippets, the words in <mark>
	}
	return out, err
}

// readContent is the bytes of the file's current version.
func (s *Server) readContent(ctx context.Context, f drive.Unindexed) ([]byte, error) {
	obj, err := s.Storages.Open(ctx, f.StorageID, f.Key)
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(io.LimitReader(obj, contentMax+1))
}

// textual: the file is a document whose text can be read — what the viewer shows as HTML, and PDF.
func textual(name, contentType string) bool {
	k := mail.PreviewKind(name, contentType)
	return k == mail.PreviewHTML || k == mail.PreviewPDF
}

// fileText is the plain text of a document: the viewer's HTML of it, or what pdftotext (poppler) reads of a PDF,
// when the image has it.
func fileText(ctx context.Context, name, contentType string, data []byte) string {
	if mail.PreviewKind(name, contentType) != mail.PreviewPDF {
		if h, ok := mail.Preview(name, contentType, data); ok {
			return mail.HTMLText(h)
		}
		return ""
	}
	if path.Ext(strings.ToLower(name)) != ".pdf" && !bytes.HasPrefix(data, []byte("%PDF")) {
		return ""
	}
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-q", "-enc", "UTF-8", "-", "-")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
