package backup

import (
	"context"
	"fmt"
	"path"
	"strings"

	"go24/internal/i18n"
)

// Target is the outside place a copy goes to after it is made in the backups' folder (that one stays: there the
// unchanged files are hard links), by rclone-rcd of go24: S3 (Endpoint, Region, User — the access key, Path —
// «bucket/folder»), SFTP (Host, Port, User), SMB (Host, User, Domain, Path — «share/folder»), WebDAV (URL, User).
// Type "" — the backups' folder only (a folder of NFS or SMB mounted on the server is set there, GO24_BACKUP_DIR).
// Secret is the password (the secret key of S3) sealed as the backups' password is.
type Target struct {
	Type     string `json:"type,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     string `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Region   string `json:"region,omitempty"`
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Secret   string `json:"secret,omitempty"`
}

// TargetTypes are the outside places offered, in the order of the page.
var TargetTypes = []string{"s3", "sftp", "smb", "webdav"}

// Remote is what the copies need of rclone-rcd (storage.RC).
type Remote interface {
	CreateRemote(ctx context.Context, name, typ string, params map[string]string) error
	DeleteRemote(ctx context.Context, name string) error
	Mkdir(ctx context.Context, remote, dir string) error
	CopyDir(ctx context.Context, srcFs, dstFs string) error
	Purge(ctx context.Context, fs, p string) error
}

// remoteName is the remote of rclone-rcd the copies go through (made anew for every copy from the settings).
const remoteName = "go24-backup"

// HasPassword: the password of the place is kept.
func (t Target) HasPassword() bool { return t.Secret != "" }

// at names a folder dst on the place for the journal: «s3 bucket/folder/go24-…», «sftp host:folder/go24-…».
func (t Target) at(dst string) string {
	switch t.Type {
	case "sftp", "smb":
		return t.Type + " " + t.Host + ":" + dst
	case "webdav":
		return t.Type + " " + strings.TrimRight(t.URL, "/") + "/" + dst
	}
	return t.Type + " " + dst
}

// check tells what is missing in the place's settings.
func (t *Target) check() error {
	if t.Type == "" {
		*t = Target{}
		return nil
	}
	if !contains(TargetTypes, t.Type) {
		return i18n.E("err.bad_form")
	}
	for _, f := range []*string{&t.Host, &t.Port, &t.User, &t.Domain, &t.Endpoint, &t.Region, &t.URL, &t.Path} {
		*f = strings.TrimSpace(*f)
	}
	t.Path = strings.Trim(path.Clean("/"+t.Path), "/")
	need := map[string][]string{"s3": {t.Endpoint, t.User, t.Path}, "sftp": {t.Host, t.User}, "smb": {t.Host, t.Path}, "webdav": {t.URL}}[t.Type]
	for _, v := range need {
		if v == "" {
			return i18n.E("backup.err_target")
		}
	}
	if t.Type == "s3" && strings.Contains(t.Endpoint, "/") && !strings.HasPrefix(t.Endpoint, "http") {
		return i18n.E("backup.err_target")
	}
	return nil
}

// params are the type and the parameters of the rclone remote for the place.
func (t Target) params(password string) (string, map[string]string) {
	switch t.Type {
	case "s3":
		p := map[string]string{"provider": "Other", "endpoint": t.Endpoint, "access_key_id": t.User, "secret_access_key": password}
		if t.Region != "" {
			p["region"] = t.Region
		}
		return "s3", p
	case "sftp":
		p := map[string]string{"host": t.Host, "user": t.User, "pass": password}
		if t.Port != "" {
			p["port"] = t.Port
		}
		return "sftp", p
	case "smb":
		p := map[string]string{"host": t.Host, "user": t.User, "pass": password}
		if t.Domain != "" {
			p["domain"] = t.Domain
		}
		return "smb", p
	}
	return "webdav", map[string]string{"url": t.URL, "vendor": "other", "user": t.User, "pass": password}
}

// connect makes the remote of rclone-rcd for the place anew and the folder of the copies on it.
func (s *Store) connect(ctx context.Context, rc Remote, t Target) error {
	password, err := s.openSecret(t.Secret)
	if err != nil {
		return err
	}
	typ, params := t.params(password)
	_ = rc.DeleteRemote(ctx, remoteName) // none yet: nothing to remove
	if err := rc.CreateRemote(ctx, remoteName, typ, params); err != nil {
		return err
	}
	if t.Path != "" {
		return rc.Mkdir(ctx, remoteName, t.Path)
	}
	return nil
}

// Check connects to the place of the saved settings (the folder of the copies is made there): «Проверить».
func (s *Store) Check(ctx context.Context, rc Remote) error {
	st, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	if st.Target.Type == "" || rc == nil {
		return i18n.E("backup.err_no_target")
	}
	if err := s.connect(ctx, rc, st.Target); err != nil {
		return i18n.E("backup.err_connect", err.Error())
	}
	return nil
}

// upload copies the made copy (rel — its folder under the backups' folder, rcRoot — that folder as rclone-rcd
// sees it) to the place; it gives where it went on the place.
func (s *Store) upload(ctx context.Context, rc Remote, t Target, rcRoot, rel string) (string, error) {
	if rc == nil {
		return "", fmt.Errorf("no rclone-rcd")
	}
	if err := s.connect(ctx, rc, t); err != nil {
		return "", err
	}
	dst := path.Base(rel)
	if t.Path != "" {
		dst = t.Path + "/" + dst
	}
	if err := rc.CopyDir(ctx, path.Join(rcRoot, rel), remoteName+":"+dst); err != nil {
		return "", err
	}
	return dst, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
