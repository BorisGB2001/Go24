// Package storages manages storages that users connect themselves (Nextcloud/WebDAV, Яндекс.Диск, Облако Mail.ru,
// a Windows/NAS share by SMB, SFTP, S3, FTP) through
// rclone rcd, and stores/reads files either there or in the built-in storage.
package storages

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
	"go24/internal/storage"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

// Types that may be connected. Deliberately no "local", "alias", "union"…: they would expose the server's own files.
var Types = []string{"webdav", "yandex", "mailru", "smb", "sftp", "s3", "ftp"}

// yandexDAV is Яндекс.Диск's WebDAV: signed in with the login and a password of an application («Файлы WebDAV»).
const yandexDAV = "https://webdav.yandex.ru"

// rcloneType is the rclone backend of a type: Яндекс.Диск is WebDAV of its own address.
func rcloneType(t string) string {
	if t == "yandex" {
		return "webdav"
	}
	return t
}

type Storage struct {
	ID         string
	Name       string
	Type       string
	Root       string
	Summary    string
	Shared     bool // owner_id IS NULL
	Mine       bool
	CheckedAt  *time.Time
	CheckOK    bool
	CheckError string
}

// Input is the form for a new storage. Secret fields are sent to rclone (which obscures them) and never stored by go24.
type Input struct {
	Name     string
	Type     string
	Shared   bool   // administrators only
	URL      string // webdav
	Vendor   string // webdav: nextcloud | owncloud | other
	Host     string // sftp, ftp, smb
	Port     string // sftp, ftp, smb
	Domain   string // smb (WORKGROUP when empty)
	User     string // webdav, yandex, mailru, smb, sftp, ftp
	Password string // webdav, yandex, mailru (a password of an application there), smb, sftp, ftp
	Endpoint string // s3
	Region   string // s3
	KeyID    string // s3
	Secret   string // s3
	Root     string // folder, or bucket[/folder] for s3, share[/folder] for smb
}

type Store struct {
	db      *pgxpool.Pool
	rc      *storage.RC    // nil when rclone rcd is not configured
	builtin *storage.Store // the built-in S3 storage (rclone serve s3)
	// Resolve looks up a host; replaced in tests.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
}

func NewStore(db *pgxpool.Pool, rc *storage.RC, builtin *storage.Store) *Store {
	return &Store{db: db, rc: rc, builtin: builtin, Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
}

// Enabled reports whether users can connect their own storages (rclone rcd configured).
func (s *Store) Enabled() bool { return s.rc != nil }

// List returns the storages the user may use: their own plus shared ones (all of them for an admin).
func (s *Store) List(ctx context.Context, userID string, admin bool) ([]Storage, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id, name, type, root, summary, owner_id IS NULL, coalesce(owner_id = $1, false), checked_at, check_ok, check_error
		FROM storages WHERE owner_id IS NULL OR owner_id = $1 OR $2
		ORDER BY owner_id IS NOT NULL, name`, userID, admin)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Storage])
}

// Create validates the input, creates the rclone remote, checks the connection and saves the storage.
func (s *Store) Create(ctx context.Context, userID string, admin bool, in Input) (string, error) {
	if s.rc == nil {
		return "", i18n.E("err.storages_disabled")
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return "", i18n.E("err.storage_name_required")
	}
	if in.Shared && !admin {
		return "", ErrForbidden
	}
	in = smbPath(in)
	params, host, summary, err := buildParams(in)
	if err != nil {
		return "", err
	}
	if err := s.checkHost(ctx, host, admin); err != nil {
		return "", err
	}
	root := cleanRoot(strings.ReplaceAll(in.Root, `\`, "/"))
	remote := "go24_" + randomHex(6)
	if err := s.rc.CreateRemote(ctx, remote, rcloneType(in.Type), params); err != nil {
		return "", i18n.E("err.storage_rclone", err.Error())
	}
	// Create the folder first (a fresh Nextcloud has no "go24" folder yet), then prove we can list it.
	err = nil
	if root != "" {
		err = s.rc.Mkdir(ctx, remote, root)
	}
	if err == nil {
		err = s.rc.Check(ctx, remote+":"+root)
	}
	if err != nil {
		_ = s.rc.DeleteRemote(ctx, remote)
		return "", i18n.E("err.storage_check", err.Error())
	}
	var owner *string
	if !in.Shared {
		owner = &userID
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO storages (name, type, remote, root, summary, owner_id, created_by, checked_at, check_ok)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now(), true) RETURNING id`,
		in.Name, in.Type, remote, root, summary, owner, userID).Scan(&id)
	if err != nil {
		_ = s.rc.DeleteRemote(ctx, remote)
		return "", err
	}
	return id, nil
}

// get loads a storage the user may use (own, shared, or any in admin mode).
func (s *Store) get(ctx context.Context, userID string, admin bool, id string) (remote, root string, owner *string, err error) {
	err = s.db.QueryRow(ctx, `SELECT remote, root, owner_id FROM storages WHERE id = $1`, id).Scan(&remote, &root, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil, ErrNotFound
	}
	if err != nil {
		return "", "", nil, err
	}
	if owner != nil && *owner != userID && !admin {
		return "", "", nil, ErrForbidden
	}
	return remote, root, owner, nil
}

// Check re-tests the connection and records the result.
func (s *Store) Check(ctx context.Context, userID string, admin bool, id string) error {
	remote, root, _, err := s.get(ctx, userID, admin, id)
	if err != nil {
		return err
	}
	if s.rc == nil {
		return i18n.E("err.storages_disabled")
	}
	cerr := s.rc.Check(ctx, remote+":"+root)
	msg := ""
	if cerr != nil {
		msg = cerr.Error()
	}
	if _, err := s.db.Exec(ctx, `UPDATE storages SET checked_at = now(), check_ok = $2, check_error = $3 WHERE id = $1`,
		id, cerr == nil, msg); err != nil {
		return err
	}
	if cerr != nil {
		return i18n.E("err.storage_check", msg)
	}
	return nil
}

// Delete removes a storage: its owner, or an admin for shared ones. Storages with files are kept.
func (s *Store) Delete(ctx context.Context, userID string, admin bool, id string) error {
	remote, _, owner, err := s.get(ctx, userID, admin, id)
	if err != nil {
		return err
	}
	if owner == nil && !admin {
		return ErrForbidden
	}
	var used int
	if err := s.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM files WHERE storage_id = $1) + (SELECT count(*) FROM drive_file_versions WHERE storage_id = $1)`, id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return i18n.E("err.storage_in_use", used)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM storages WHERE id = $1`, id); err != nil {
		return err
	}
	if s.rc != nil {
		_ = s.rc.DeleteRemote(ctx, remote)
	}
	return nil
}

// Put stores a file under key; storageID "" means the built-in storage.
func (s *Store) Put(ctx context.Context, userID string, admin bool, storageID, key string, r io.Reader, size int64, contentType string) error {
	if storageID == "" {
		return s.builtin.Put(ctx, key, r, size, contentType)
	}
	remote, root, _, err := s.get(ctx, userID, admin, storageID)
	if err != nil {
		return err
	}
	if s.rc == nil {
		return i18n.E("err.storages_disabled")
	}
	return s.rc.Upload(ctx, remote+":"+root, path.Dir(key), path.Base(key), r)
}

// Open reads a stored file. Access to the file itself is decided by the caller (task visibility, uploader).
func (s *Store) Open(ctx context.Context, storageID *string, key string) (io.ReadCloser, error) {
	if storageID == nil {
		return s.builtin.Get(ctx, key)
	}
	var remote, root string
	if err := s.db.QueryRow(ctx, `SELECT remote, root FROM storages WHERE id = $1`, *storageID).Scan(&remote, &root); err != nil {
		return nil, err
	}
	if s.rc == nil {
		return nil, i18n.E("err.storages_disabled")
	}
	return s.rc.Open(ctx, remote+":"+root, key)
}

// Copy stores the bytes of a file once more under another key of the same storage (a copy on «Диск»). The caller
// has decided the file may be read; whoever may read a file may copy it next to itself.
func (s *Store) Copy(ctx context.Context, storageID *string, from, to string, size int64, contentType string) error {
	if size == 0 {
		return nil // an empty file has no stored object
	}
	obj, err := s.Open(ctx, storageID, from)
	if err != nil {
		return err
	}
	defer obj.Close()
	if storageID == nil {
		return s.builtin.Put(ctx, to, obj, size, contentType)
	}
	var remote, root string
	if err := s.db.QueryRow(ctx, `SELECT remote, root FROM storages WHERE id = $1`, *storageID).Scan(&remote, &root); err != nil {
		return err
	}
	return s.rc.Upload(ctx, remote+":"+root, path.Dir(to), path.Base(to), obj)
}

// buildParams turns the form into rclone parameters, the host to check, and a non-secret summary.
func buildParams(in Input) (params map[string]string, host, summary string, err error) {
	switch in.Type {
	case "webdav":
		u, perr := url.Parse(strings.TrimSpace(in.URL))
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, "", "", i18n.E("err.storage_bad_url")
		}
		vendor := in.Vendor
		if vendor != "nextcloud" && vendor != "owncloud" {
			vendor = "other"
		}
		params = map[string]string{"url": u.String(), "vendor": vendor, "user": in.User, "pass": in.Password}
		return params, u.Hostname(), fmt.Sprintf("%s · %s", u.Host, in.User), nil
	case "yandex", "mailru":
		user := strings.TrimSpace(in.User)
		if user == "" || in.Password == "" {
			return nil, "", "", i18n.E("err.storage_login_required")
		}
		if in.Type == "yandex" {
			params = map[string]string{"url": yandexDAV, "vendor": "other", "user": user, "pass": in.Password}
			return params, "webdav.yandex.ru", "webdav.yandex.ru · " + user, nil
		}
		return map[string]string{"user": user, "pass": in.Password}, "cloud.mail.ru", "cloud.mail.ru · " + user, nil
	case "sftp", "ftp", "smb":
		h := strings.TrimSpace(in.Host)
		if h == "" || strings.ContainsAny(h, "/\\ ") {
			return nil, "", "", i18n.E("err.storage_bad_host")
		}
		if in.Type == "smb" && cleanRoot(strings.ReplaceAll(in.Root, `\`, "/")) == "" { // the share is the first folder
			return nil, "", "", i18n.E("err.storage_smb_share")
		}
		params = map[string]string{"host": h, "user": in.User, "pass": in.Password}
		if in.Type == "smb" {
			if d := strings.TrimSpace(in.Domain); d != "" {
				params["domain"] = d
			}
		}
		if in.Port != "" {
			if p, perr := strconv.Atoi(in.Port); perr != nil || p < 1 || p > 65535 {
				return nil, "", "", i18n.E("err.storage_bad_port")
			}
			params["port"] = in.Port
		}
		if in.Type == "sftp" {
			params["shell_type"] = "none" // never run remote shell commands
		}
		return params, h, fmt.Sprintf("%s@%s", in.User, h), nil
	case "s3":
		u, perr := url.Parse(strings.TrimSpace(in.Endpoint))
		if perr != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, "", "", i18n.E("err.storage_bad_url")
		}
		params = map[string]string{"provider": "Other", "endpoint": u.String(), "access_key_id": in.KeyID,
			"secret_access_key": in.Secret, "region": in.Region}
		return params, u.Hostname(), u.Host, nil
	}
	return nil, "", "", i18n.E("err.storage_bad_type")
}

// smbPath takes a share's path as Windows writes it (\\nas\share\folder, or //nas/share) given as the server: the
// server is its first part, the share and the folder — the rest (unless a folder is given).
func smbPath(in Input) Input {
	h := strings.ReplaceAll(strings.TrimSpace(in.Host), `\`, "/")
	if in.Type != "smb" || !strings.HasPrefix(h, "//") {
		return in
	}
	host, rest, _ := strings.Cut(strings.TrimPrefix(h, "//"), "/")
	in.Host = host
	if strings.TrimSpace(in.Root) == "" {
		in.Root = rest
	}
	return in
}

// checkHost blocks SSRF: ordinary users may only connect storages on public addresses; internal addresses
// (Docker services, LAN, localhost) are allowed for administrators only.
func (s *Store) checkHost(ctx context.Context, host string, admin bool) error {
	if admin {
		return nil
	}
	ips, err := s.Resolve(ctx, host)
	if err != nil || len(ips) == 0 {
		return i18n.E("err.storage_bad_host")
	}
	for _, ip := range ips {
		ip = ip.Unmap()
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() || isCGNAT(ip) {
			return i18n.E("err.storage_private_host")
		}
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func isCGNAT(ip netip.Addr) bool { return cgnat.Contains(ip) }

func cleanRoot(r string) string {
	r = strings.Trim(strings.TrimSpace(r), "/")
	if r == "" {
		return ""
	}
	return strings.TrimPrefix(path.Clean("/"+r), "/")
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
