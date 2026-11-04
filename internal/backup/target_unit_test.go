package backup

import (
	"context"
	"errors"
	"path"
	"strings"
	"testing"
)

// mockRemote records rclone-rcd calls and allows simulating errors.
type mockRemote struct {
	calls        []string
	createErr    error
	deleteErr    error
	mkdirErr     error
	copyDirErr   error
	purgeErr     error
	remoteParams map[string]string
}

func (m *mockRemote) CreateRemote(_ context.Context, name, typ string, params map[string]string) error {
	m.calls = append(m.calls, "create "+name+" "+typ)
	m.remoteParams = params
	return m.createErr
}

func (m *mockRemote) DeleteRemote(_ context.Context, name string) error {
	m.calls = append(m.calls, "delete "+name)
	return m.deleteErr
}

func (m *mockRemote) Mkdir(_ context.Context, remote, dir string) error {
	m.calls = append(m.calls, "mkdir "+remote+":"+dir)
	return m.mkdirErr
}

func (m *mockRemote) CopyDir(_ context.Context, srcFs, dstFs string) error {
	m.calls = append(m.calls, "copy "+srcFs+" "+dstFs)
	return m.copyDirErr
}

func (m *mockRemote) Purge(_ context.Context, fs, p string) error {
	m.calls = append(m.calls, "purge "+fs+" "+p)
	return m.purgeErr
}

// TestTargetHasPassword tests HasPassword method on Target.
func TestTargetHasPassword(t *testing.T) {
	if (Target{}).HasPassword() {
		t.Error("expected HasPassword=false on empty target")
	}
	if !(Target{Secret: "sealed"}).HasPassword() {
		t.Error("expected HasPassword=true when secret is present")
	}
}

// TestTargetAt tests formatting the external destination path for journal display.
func TestTargetAt(t *testing.T) {
	tests := []struct {
		target Target
		dst    string
		want   string
	}{
		{
			target: Target{Type: "s3"},
			dst:    "mybucket/go24-20261028-120000",
			want:   "s3 mybucket/go24-20261028-120000",
		},
		{
			target: Target{Type: "sftp", Host: "sftp.example.com"},
			dst:    "remote/go24-20261028-120000",
			want:   "sftp sftp.example.com:remote/go24-20261028-120000",
		},
		{
			target: Target{Type: "smb", Host: "192.168.1.100"},
			dst:    "share/go24-20261028-120000",
			want:   "smb 192.168.1.100:share/go24-20261028-120000",
		},
		{
			target: Target{Type: "webdav", URL: "https://dav.server.com/remote.php/webdav/"},
			dst:    "backups/go24-20261028-120000",
			want:   "webdav https://dav.server.com/remote.php/webdav/backups/go24-20261028-120000",
		},
		{
			target: Target{Type: "webdav", URL: "https://dav.server.com/dav"},
			dst:    "go24-20261028-120000",
			want:   "webdav https://dav.server.com/dav/go24-20261028-120000",
		},
	}
	for _, tt := range tests {
		got := tt.target.at(tt.dst)
		if got != tt.want {
			t.Errorf("%+v at(%q) = %q, want %q", tt.target, tt.dst, got, tt.want)
		}
	}
}

// TestTargetCheck tests validation rules, whitespace trimming, and path normalization for Target.
func TestTargetCheck(t *testing.T) {
	// Empty type should clear struct and return nil.
	empty := Target{Host: "should-be-cleared"}
	if err := empty.check(); err != nil || empty != (Target{}) {
		t.Fatalf("check on empty type failed: %v, cleared: %+v", err, empty)
	}

	// Unknown type.
	unknown := Target{Type: "nfs"}
	if err := unknown.check(); err == nil {
		t.Error("expected error for unsupported target type")
	}

	// S3 validation:
	s3Valid := Target{
		Type:     "s3",
		Endpoint: "  s3.amazonaws.com  ",
		User:     "  AKIAIOSFODNN7EXAMPLE  ",
		Path:     "  /mybucket/backups/  ",
	}
	if err := s3Valid.check(); err != nil {
		t.Fatalf("valid s3 failed: %v", err)
	}
	if s3Valid.Endpoint != "s3.amazonaws.com" || s3Valid.User != "AKIAIOSFODNN7EXAMPLE" || s3Valid.Path != "mybucket/backups" {
		t.Errorf("s3 field trimming or path clean mismatch: %+v", s3Valid)
	}

	// S3 missing endpoint, user, or path:
	for _, badS3 := range []Target{
		{Type: "s3", User: "AK", Path: "bucket"},
		{Type: "s3", Endpoint: "s3.amazonaws.com", Path: "bucket"},
		{Type: "s3", Endpoint: "s3.amazonaws.com", User: "AK"},
		{Type: "s3", Endpoint: "s3.amazonaws.com/custom", User: "AK", Path: "bucket"}, // has / but no http prefix
	} {
		if err := badS3.check(); err == nil {
			t.Errorf("expected error for bad S3 config: %+v", badS3)
		}
	}

	// S3 endpoint with http/https prefix and path:
	s3Http := Target{Type: "s3", Endpoint: "https://minio.company.local:9000/v1", User: "AK", Path: "bucket"}
	if err := s3Http.check(); err != nil {
		t.Errorf("valid s3 with https endpoint failed: %v", err)
	}

	// SFTP validation:
	sftpValid := Target{Type: "sftp", Host: "10.0.0.1", User: "backupuser"}
	if err := sftpValid.check(); err != nil {
		t.Fatalf("valid sftp failed: %v", err)
	}
	sftpNoUser := Target{Type: "sftp", Host: "10.0.0.1"}
	if err := sftpNoUser.check(); err == nil {
		t.Error("expected error for sftp missing user")
	}
	sftpNoHost := Target{Type: "sftp", User: "user"}
	if err := sftpNoHost.check(); err == nil {
		t.Error("expected error for sftp missing host")
	}

	// SMB validation:
	smbValid := Target{Type: "smb", Host: "nas.local", Path: "share/backups"}
	if err := smbValid.check(); err != nil {
		t.Fatalf("valid smb failed: %v", err)
	}
	smbNoPath := Target{Type: "smb", Host: "nas.local"}
	if err := smbNoPath.check(); err == nil {
		t.Error("expected error for smb missing path")
	}
	smbNoHost := Target{Type: "smb", Path: "share"}
	if err := smbNoHost.check(); err == nil {
		t.Error("expected error for smb missing host")
	}

	// WebDAV validation:
	webdavValid := Target{Type: "webdav", URL: "https://nextcloud.example.com/dav"}
	if err := webdavValid.check(); err != nil {
		t.Fatalf("valid webdav failed: %v", err)
	}
	webdavNoURL := Target{Type: "webdav"}
	if err := webdavNoURL.check(); err == nil {
		t.Error("expected error for webdav missing URL")
	}
}

// TestTargetParams tests generating rclone remote parameters for each supported backend.
func TestTargetParams(t *testing.T) {
	// S3 without and with region.
	s3 := Target{Type: "s3", Endpoint: "s3.us-east-1.amazonaws.com", User: "AKID"}
	typ, params := s3.params("secret_s3_key")
	if typ != "s3" || params["provider"] != "Other" || params["endpoint"] != "s3.us-east-1.amazonaws.com" ||
		params["access_key_id"] != "AKID" || params["secret_access_key"] != "secret_s3_key" {
		t.Errorf("s3 params mismatch: typ=%s, params=%+v", typ, params)
	}
	if _, ok := params["region"]; ok {
		t.Error("expected no region in params when not specified")
	}

	s3Region := Target{Type: "s3", Endpoint: "s3.us-east-1.amazonaws.com", User: "AKID", Region: "us-east-1"}
	_, paramsReg := s3Region.params("pass")
	if paramsReg["region"] != "us-east-1" {
		t.Errorf("region mismatch: %v", paramsReg["region"])
	}

	// SFTP without and with port.
	sftp := Target{Type: "sftp", Host: "sftp.server.com", User: "sftpuser"}
	typSftp, paramsSftp := sftp.params("sftppass")
	if typSftp != "sftp" || paramsSftp["host"] != "sftp.server.com" || paramsSftp["user"] != "sftpuser" || paramsSftp["pass"] != "sftppass" {
		t.Errorf("sftp params mismatch: typ=%s, params=%+v", typSftp, paramsSftp)
	}
	if _, ok := paramsSftp["port"]; ok {
		t.Error("expected no port when not specified")
	}

	sftpPort := Target{Type: "sftp", Host: "sftp.server.com", User: "sftpuser", Port: "2222"}
	_, paramsPort := sftpPort.params("pass")
	if paramsPort["port"] != "2222" {
		t.Errorf("port mismatch: %v", paramsPort["port"])
	}

	// SMB without and with domain.
	smb := Target{Type: "smb", Host: "smb.nas", User: "smbuser"}
	typSmb, paramsSmb := smb.params("smbpass")
	if typSmb != "smb" || paramsSmb["host"] != "smb.nas" || paramsSmb["user"] != "smbuser" || paramsSmb["pass"] != "smbpass" {
		t.Errorf("smb params mismatch: typ=%s, params=%+v", typSmb, paramsSmb)
	}
	if _, ok := paramsSmb["domain"]; ok {
		t.Error("expected no domain when not specified")
	}

	smbDomain := Target{Type: "smb", Host: "smb.nas", User: "smbuser", Domain: "CORP"}
	_, paramsDomain := smbDomain.params("pass")
	if paramsDomain["domain"] != "CORP" {
		t.Errorf("domain mismatch: %v", paramsDomain["domain"])
	}

	// WebDAV.
	webdav := Target{Type: "webdav", URL: "https://dav.test", User: "davuser"}
	typDav, paramsDav := webdav.params("davpass")
	if typDav != "webdav" || paramsDav["url"] != "https://dav.test" || paramsDav["vendor"] != "other" ||
		paramsDav["user"] != "davuser" || paramsDav["pass"] != "davpass" {
		t.Errorf("webdav params mismatch: typ=%s, params=%+v", typDav, paramsDav)
	}
}

// TestStoreConnectAndUpload tests connecting to the target and uploading backups via mock Remote.
func TestStoreConnectAndUpload(t *testing.T) {
	ctx := context.Background()
	store := NewStore(nil, "target-store-secret")

	sealedPass, err := store.seal("target-pass-456")
	if err != nil {
		t.Fatal(err)
	}

	target := Target{
		Type:   "sftp",
		Host:   "remote.backup.net",
		User:   "agent",
		Path:   "storage/backups",
		Secret: sealedPass,
	}

	// Test connect success:
	rc := &mockRemote{}
	if err := store.connect(ctx, rc, target); err != nil {
		t.Fatalf("store.connect failed: %v", err)
	}
	expectedCalls := "delete go24-backup|create go24-backup sftp|mkdir go24-backup:storage/backups"
	if strings.Join(rc.calls, "|") != expectedCalls {
		t.Fatalf("rc calls mismatch: got %q, want %q", strings.Join(rc.calls, "|"), expectedCalls)
	}
	if rc.remoteParams["pass"] != "target-pass-456" {
		t.Errorf("unsealed pass mismatch: %q", rc.remoteParams["pass"])
	}

	// Connect without path: Mkdir should not be called.
	rcNoPath := &mockRemote{}
	targetNoPath := Target{Type: "sftp", Host: "remote.backup.net", User: "agent"}
	if err := store.connect(ctx, rcNoPath, targetNoPath); err != nil {
		t.Fatalf("connect without path failed: %v", err)
	}
	if strings.Join(rcNoPath.calls, "|") != "delete go24-backup|create go24-backup sftp" {
		t.Fatalf("calls without path: %q", strings.Join(rcNoPath.calls, "|"))
	}

	// Error when CreateRemote fails.
	rcCreateErr := &mockRemote{createErr: errors.New("cannot create remote")}
	if err := store.connect(ctx, rcCreateErr, target); err == nil {
		t.Error("expected error when CreateRemote fails")
	}

	// Error when Mkdir fails.
	rcMkdirErr := &mockRemote{mkdirErr: errors.New("cannot create dir")}
	if err := store.connect(ctx, rcMkdirErr, target); err == nil {
		t.Error("expected error when Mkdir fails")
	}

	// Test upload:
	rcUpload := &mockRemote{}
	rcRoot := "/backups/root"
	rel := "sub/go24-20261028-120000"

	dst, err := store.upload(ctx, rcUpload, target, rcRoot, rel)
	if err != nil {
		t.Fatalf("store.upload failed: %v", err)
	}
	wantDst := "storage/backups/go24-20261028-120000"
	if dst != wantDst {
		t.Errorf("upload dst = %q, want %q", dst, wantDst)
	}
	expectedCopy := "copy " + path.Join(rcRoot, rel) + " go24-backup:" + wantDst
	if rcUpload.calls[len(rcUpload.calls)-1] != expectedCopy {
		t.Errorf("last upload call = %q, want %q", rcUpload.calls[len(rcUpload.calls)-1], expectedCopy)
	}

	// Upload without target.Path: dst is just base name.
	rcUploadNoPath := &mockRemote{}
	dstNoPath, err := store.upload(ctx, rcUploadNoPath, targetNoPath, rcRoot, rel)
	if err != nil {
		t.Fatalf("upload without path failed: %v", err)
	}
	if dstNoPath != "go24-20261028-120000" {
		t.Errorf("dstNoPath = %q, want go24-20261028-120000", dstNoPath)
	}

	// Upload with nil Remote returns error.
	if _, err := store.upload(ctx, nil, target, rcRoot, rel); err == nil {
		t.Error("expected error when rc is nil")
	}

	// Upload fails when CopyDir fails.
	rcCopyErr := &mockRemote{copyDirErr: errors.New("network failure during copy")}
	if _, err := store.upload(ctx, rcCopyErr, target, rcRoot, rel); err == nil {
		t.Error("expected error when CopyDir fails")
	}
}

// TestContains tests the contains helper function.
func TestContains(t *testing.T) {
	list := []string{"apple", "banana", "cherry"}
	if !contains(list, "apple") {
		t.Error("expected contains apple = true")
	}
	if !contains(list, "cherry") {
		t.Error("expected contains cherry = true")
	}
	if contains(list, "orange") {
		t.Error("expected contains orange = false")
	}
	if contains(nil, "apple") {
		t.Error("expected contains on nil = false")
	}
	if contains([]string{}, "apple") {
		t.Error("expected contains on empty = false")
	}
}
