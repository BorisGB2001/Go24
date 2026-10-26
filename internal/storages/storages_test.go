package storages

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"go24/internal/i18n"
)

func key(err error) string {
	var ie *i18n.Error
	if errors.As(err, &ie) {
		return ie.Key
	}
	return ""
}

func TestBuildParams(t *testing.T) {
	p, host, summary, err := buildParams(Input{Type: "webdav", URL: "https://cloud.example.ru/remote.php/dav/files/u/", Vendor: "nextcloud", User: "u", Password: "secret"})
	if err != nil || host != "cloud.example.ru" || p["vendor"] != "nextcloud" || p["pass"] != "secret" || summary != "cloud.example.ru · u" {
		t.Fatalf("webdav: %v %q %q %v", p, host, summary, err)
	}
	if p, _, _, err := buildParams(Input{Type: "sftp", Host: "files.example.ru", Port: "2222", User: "u"}); err != nil || p["shell_type"] != "none" || p["port"] != "2222" {
		t.Fatalf("sftp must disable remote shell: %v %v", p, err)
	}
	// Яндекс.Диск is WebDAV of its own address, Облако Mail.ru — rclone's mailru: a login and a password of an application
	if p, host, summary, err := buildParams(Input{Type: "yandex", User: " ivan@yandex.ru ", Password: "app"}); err != nil || p["url"] != yandexDAV ||
		p["user"] != "ivan@yandex.ru" || p["pass"] != "app" || host != "webdav.yandex.ru" || summary != "webdav.yandex.ru · ivan@yandex.ru" || rcloneType("yandex") != "webdav" {
		t.Fatalf("yandex: %v %q %q %v", p, host, summary, err)
	}
	if p, host, _, err := buildParams(Input{Type: "mailru", User: "ivan@mail.ru", Password: "app"}); err != nil || p["user"] != "ivan@mail.ru" ||
		p["pass"] != "app" || host != "cloud.mail.ru" || rcloneType("mailru") != "mailru" {
		t.Fatalf("mailru: %v %q %v", p, host, err)
	}
	if _, _, _, err := buildParams(Input{Type: "mailru", User: "ivan@mail.ru"}); key(err) != "err.storage_login_required" {
		t.Errorf("mailru without a password: %v", err)
	}
	// SMB: the share is the first folder; a path as Windows writes it is taken apart
	if p, host, _, err := buildParams(Input{Type: "smb", Host: "nas.lan", User: "u", Password: "pw", Domain: "OFFICE", Root: "Общая/go24"}); err != nil ||
		p["domain"] != "OFFICE" || p["host"] != "nas.lan" || host != "nas.lan" {
		t.Fatalf("smb: %v %v", p, err)
	}
	if _, _, _, err := buildParams(Input{Type: "smb", Host: "nas.lan"}); key(err) != "err.storage_smb_share" {
		t.Errorf("smb without a share: %v", err)
	}
	if in := smbPath(Input{Type: "smb", Host: `\\nas\Общая\go24`}); in.Host != "nas" || in.Root != "Общая/go24" {
		t.Errorf("smb path: %+v", in)
	}
	if in := smbPath(Input{Type: "smb", Host: "//nas/Общая", Root: "Другая"}); in.Host != "nas" || in.Root != "Другая" {
		t.Errorf("smb path with a folder given: %+v", in)
	}
	for _, in := range []Input{
		{Type: "local"}, // would expose the server's own disk
		{Type: "alias"}, // could point at another remote
		{Type: "webdav", URL: "file:///etc/passwd"},     // not http(s)
		{Type: "webdav", URL: "cloud.example.ru"},       // no scheme
		{Type: "sftp", Host: "a b"},                     // garbage host
		{Type: "sftp", Host: "h", Port: "99999"},        // bad port
		{Type: "s3", Endpoint: "ftp://storage.example"}, // not http(s)
	} {
		if _, _, _, err := buildParams(in); err == nil {
			t.Errorf("must reject %+v", in)
		}
	}
}

func TestCheckHostBlocksInternalAddresses(t *testing.T) {
	s := &Store{Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
		return map[string][]netip.Addr{
			"public.example": {netip.MustParseAddr("93.184.216.34")},
			"postgres":       {netip.MustParseAddr("172.18.0.3")},
			"nas.lan":        {netip.MustParseAddr("192.168.1.10")},
			"localhost":      {netip.MustParseAddr("127.0.0.1")},
			"metadata":       {netip.MustParseAddr("169.254.169.254")},
			"cgnat":          {netip.MustParseAddr("100.64.1.1")},
			"v6local":        {netip.MustParseAddr("::1")},
			"mapped":         {netip.MustParseAddr("::ffff:10.0.0.1")},
			"mixed.example":  {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")},
		}[host], nil
	}}
	ctx := context.Background()
	if err := s.checkHost(ctx, "public.example", false); err != nil {
		t.Fatalf("public host rejected: %v", err)
	}
	for _, h := range []string{"postgres", "nas.lan", "localhost", "metadata", "cgnat", "v6local", "mapped", "mixed.example"} {
		if err := s.checkHost(ctx, h, false); key(err) != "err.storage_private_host" {
			t.Errorf("%s: want private-host error, got %v", h, err)
		}
		if err := s.checkHost(ctx, h, true); err != nil {
			t.Errorf("%s: admin may connect internal storages, got %v", h, err)
		}
	}
	if err := s.checkHost(ctx, "unknown.example", false); key(err) != "err.storage_bad_host" {
		t.Errorf("unresolvable host: %v", err)
	}
}

func TestCleanRoot(t *testing.T) {
	for in, want := range map[string]string{"": "", "/": "", " go24/ ": "go24", "a/../b": "b", "../../etc": "etc", "bucket/dir": "bucket/dir"} {
		if got := cleanRoot(in); got != want {
			t.Errorf("cleanRoot(%q) = %q, want %q", in, got, want)
		}
	}
}
