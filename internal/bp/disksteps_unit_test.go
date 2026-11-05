package bp

import (
	"testing"
	"time"

	"go24/internal/drive"
)

// TestDiskInfo: field mapping for file and folder objects in diskread step.
func TestDiskInfo(t *testing.T) {
	createdTime := time.Date(2026, 11, 2, 14, 30, 0, 0, time.Local)

	// 1. File object
	fileObj := drive.Object{
		ID:          "file-1",
		Name:        "report.pdf",
		Kind:        "file",
		Folder:      "Documents",
		Drive:       drive.Drive{Kind: "company"},
		ByID:        "usr-1",
		CreatedAt:   createdTime,
		Size:        2048576,
		ContentType: "application/pdf",
	}

	info := diskInfo(fileObj)
	if info["id"] != "file-1" || info["name"] != "report.pdf" || info["type"] != "file" {
		t.Errorf("file basic info mismatch: %+v", info)
	}
	if info["size"] != "2048576" {
		t.Errorf("file size = %q, want '2048576'", info["size"])
	}
	if info["content_type"] != "application/pdf" {
		t.Errorf("file content_type = %q, want 'application/pdf'", info["content_type"])
	}
	if info["created"] != "2026-11-02T14:30" {
		t.Errorf("file created = %q, want '2026-11-02T14:30'", info["created"])
	}

	// 2. Folder object
	folderObj := drive.Object{
		ID:        "folder-1",
		Name:      "Invoices",
		Kind:      "folder",
		Drive:     drive.Drive{Kind: "company"},
		ByID:      "usr-2",
		CreatedAt: createdTime,
	}

	fInfo := diskInfo(folderObj)
	if fInfo["type"] != "folder" {
		t.Errorf("folder type = %q, want 'folder'", fInfo["type"])
	}
	if fInfo["size"] != "" {
		t.Errorf("folder size should be empty string, got %q", fInfo["size"])
	}
}

