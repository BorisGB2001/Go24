// Package backup is «Настройки → Резервное копирование» of go24: an administrator chooses what is copied — the
// databases (go24, Authentik, Matrix) and the configuration of the installation (deploy/: compose, the services'
// configs; the secrets — .env and files only their owner reads — encrypted with the backups' password) — and where
// to (a folder under the backups' folder of the server); «Сделать сейчас» queues a copy, go24-worker makes it
// (Runner) and writes in the journal how it went: the folder, the size, the error.
package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/scrypt"

	"go24/internal/i18n"
)

const settingKey = "backup" // company_settings

// Parts a copy can hold, in the order of the page: the databases, the files of go24 (the S3 bucket: Диск, files of
// tasks, mail, chats…), the media of Matrix (pictures and files of the messenger), the configuration.
var Parts = []string{"go24", "authentik", "synapse", "files", "media", "config"}

// Settings: Dir is the folder under the backups' folder ("" — right in it); Parts — what is copied; Secret — the
// backups' password sealed with the server's key (see seal), "" — none: the secrets are not copied. Schedule —
// "daily" or "weekly" (on Weekday, 1 — Monday … 7) at At ("HH:MM", go24's time zone), "" — by hand only; Since —
// when it was set (a copy is due from then, not for the hours already gone); Keep — how many latest copies are
// kept, the older ones removed (0 — all).
type Settings struct {
	Dir      string    `json:"dir"`
	Parts    []string  `json:"parts"`
	Secret   string    `json:"secret,omitempty"`
	Schedule string    `json:"schedule,omitempty"`
	At       string    `json:"at,omitempty"`
	Weekday  int       `json:"weekday,omitempty"`
	Since    time.Time `json:"since,omitempty"`
	Keep     int       `json:"keep"`
	Target   Target    `json:"target"`
}

// Secrets are the passwords a form gives (never shown back): Password — the backups' one ("" — keep it, Clear —
// remove it), TargetPassword — of the outside place ("" — keep it while the place is of the same type).
type Secrets struct {
	Password       string
	Clear          bool
	TargetPassword string
}

// DefaultKeep is how many copies are kept until an administrator chooses.
const DefaultKeep = 7

// Slot is the last time a scheduled copy was due at or before now; zero — no schedule.
func (s Settings) Slot(now time.Time) time.Time {
	if s.Schedule != "daily" && s.Schedule != "weekly" {
		return time.Time{}
	}
	h, m := 3, 0
	if t, err := time.Parse("15:04", s.At); err == nil {
		h, m = t.Hour(), t.Minute()
	}
	slot := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if slot.After(now) {
		slot = slot.AddDate(0, 0, -1)
	}
	if s.Schedule == "weekly" {
		wd := s.Weekday % 7 // time.Weekday: Sunday is 0
		for slot.Weekday() != time.Weekday(wd) {
			slot = slot.AddDate(0, 0, -1)
		}
	}
	return slot
}

// Next is when the next scheduled copy is due; zero — no schedule.
func (s Settings) Next(now time.Time) time.Time {
	slot := s.Slot(now)
	if slot.IsZero() {
		return slot
	}
	if s.Schedule == "weekly" {
		return slot.AddDate(0, 0, 7)
	}
	return slot.AddDate(0, 0, 1)
}

// HasPassword: the secrets of the configuration are copied (encrypted).
func (s Settings) HasPassword() bool { return s.Secret != "" }

// Has: the part is copied.
func (s Settings) Has(part string) bool {
	for _, p := range s.Parts {
		if p == part {
			return true
		}
	}
	return false
}

// Run is a line of the journal.
type Run struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"` // queued, running, done, failed
	CreatedAt  time.Time  `json:"created_at"`
	By         string     `json:"by"` // the name of who asked; "" — the schedule
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Name       string     `json:"name"`
	Parts      []string   `json:"parts"`
	Size       int64      `json:"size"`
	Error      string     `json:"error,omitempty"`
	Auto       bool       `json:"auto"`                 // by the schedule
	Removed    *time.Time `json:"removed_at,omitempty"` // removed as an old one (Keep)
	// Target is where on the outside place the copy went too («s3 bucket/folder/go24-…»); UploadError — why it did not
	Target      string `json:"target,omitempty"`
	TargetPath  string `json:"-"`
	UploadError string `json:"upload_error,omitempty"`
	// Kind: "backup" — a copy, "restore" — restoring from the copy Name (the parts of it in Parts; Target — the folder
	// its configuration was unpacked into)
	Kind string `json:"kind"`
}

type Store struct {
	db  *pgxpool.Pool
	key []byte // seals the backups' password kept in the database
}

// NewStore: secret is the server's secret (GO24_SESSION_SECRET) the backups' password is sealed with — a dump of the
// database alone does not tell it.
func NewStore(db *pgxpool.Pool, secret string) *Store {
	k := sha256.Sum256([]byte("go24-backup|" + secret))
	return &Store{db: db, key: k[:]}
}

// Settings are the saved ones; by default the database of go24 and the configuration.
func (s *Store) Settings(ctx context.Context) (Settings, error) {
	st := Settings{Parts: []string{"go24", "config"}, At: "03:00", Weekday: 7, Keep: DefaultKeep}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return Settings{Parts: []string{"go24", "config"}, At: "03:00", Weekday: 7, Keep: DefaultKeep}, nil
	}
	return st, nil
}

// CleanDir checks the folder of the copies: relative, inside the backups' folder.
func CleanDir(dir string) (string, error) {
	dir = strings.Trim(strings.TrimSpace(strings.ReplaceAll(dir, `\`, "/")), "/")
	if dir == "" {
		return "", nil
	}
	c := path.Clean(dir)
	if c == ".." || strings.HasPrefix(c, "../") || strings.ContainsAny(c, "\x00:") {
		return "", i18n.E("backup.err_dir")
	}
	return c, nil
}

// SaveSettings keeps the settings with the passwords given (see Secrets).
func (s *Store) SaveSettings(ctx context.Context, actorID string, st Settings, sec Secrets) error {
	password, clear := sec.Password, sec.Clear
	dir, err := CleanDir(st.Dir)
	if err != nil {
		return err
	}
	st.Dir = dir
	var parts []string
	for _, p := range Parts { // known ones, in their order
		if st.Has(p) {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return i18n.E("backup.err_parts")
	}
	st.Parts = parts
	if st.Schedule != "" && st.Schedule != "daily" && st.Schedule != "weekly" {
		return i18n.E("err.bad_form")
	}
	if st.At == "" {
		st.At = "03:00"
	}
	if _, err := time.Parse("15:04", st.At); err != nil {
		return i18n.E("backup.err_at")
	}
	if st.Weekday < 1 || st.Weekday > 7 {
		st.Weekday = 7
	}
	if st.Keep < 0 || st.Keep > 1000 {
		return i18n.E("backup.err_keep")
	}
	old, err := s.Settings(ctx)
	if err != nil {
		return err
	}
	st.Since = old.Since
	if st.Schedule != old.Schedule || st.At != old.At || st.Weekday != old.Weekday || st.Since.IsZero() {
		st.Since = time.Now()
	}
	if err := st.Target.check(); err != nil {
		return err
	}
	st.Target.Secret = ""
	if sec.TargetPassword != "" {
		if st.Target.Secret, err = s.seal(sec.TargetPassword); err != nil {
			return err
		}
	} else if st.Target.Type != "" && st.Target.Type == old.Target.Type {
		st.Target.Secret = old.Target.Secret
	}
	st.Secret = old.Secret
	if clear {
		st.Secret = ""
	}
	if password != "" {
		if len([]rune(password)) < 8 {
			return i18n.E("backup.err_password")
		}
		if st.Secret, err = s.seal(password); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingKey, raw, actorID)
	return err
}

// Password is the backups' password ("" — none).
func (s *Store) Password(st Settings) (string, error) {
	return s.openSecret(st.Secret)
}

func (s *Store) openSecret(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	return s.open(sealed)
}

func (s *Store) gcm() (cipher.AEAD, error) {
	b, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func (s *Store) seal(text string) (string, error) {
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(text), nil)), nil
}

func (s *Store) open(sealed string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(raw) < g.NonceSize() {
		return "", errors.New("backup: sealed password too short")
	}
	text, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], nil)
	if err != nil {
		return "", errors.New("backup: the password cannot be opened (the server's secret changed?)")
	}
	return string(text), nil
}

// Queue asks for a copy now; one asked or being made already — that one is kept.
func (s *Store) Queue(ctx context.Context, by string) (int64, error) {
	return s.queue(ctx, by, false)
}

func (s *Store) queue(ctx context.Context, by string, auto bool) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `SELECT id FROM backup_runs WHERE status IN ('queued', 'running') ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil {
		return id, i18n.E("backup.err_busy")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	err = s.db.QueryRow(ctx, `INSERT INTO backup_runs (created_by, auto) VALUES (NULLIF($1, '')::uuid, $2) RETURNING id`, by, auto).Scan(&id)
	return id, err
}

// queueDue asks for the scheduled copy whose time has come (once for its time); it tells whether it did.
func (s *Store) queueDue(ctx context.Context, now time.Time) (bool, error) {
	st, err := s.Settings(ctx)
	if err != nil {
		return false, err
	}
	slot := st.Slot(now)
	if slot.IsZero() || slot.Before(st.Since) {
		return false, nil
	}
	var made bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backup_runs WHERE auto AND created_at >= $1)`, slot).Scan(&made); err != nil || made {
		return false, err
	}
	if _, err := s.queue(ctx, "", true); err != nil {
		var ie *i18n.Error
		if errors.As(err, &ie) { // one is being made: asked again once it is done
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Runs is the journal, latest first.
func (s *Store) Runs(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.status, r.created_at, COALESCE(u.name, ''), r.started_at, r.finished_at, r.name, r.parts, r.size, r.error,
			r.auto, r.removed_at, r.target, r.target_path, r.upload_error, r.kind
		FROM backup_runs r LEFT JOIN users u ON u.id = r.created_by ORDER BY r.id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Run])
	if list == nil {
		list = []Run{}
	}
	return list, err
}

// job is a line of the journal taken by go24-worker: a copy to make or a restoring (see restore.go).
type job struct {
	ID     int64
	Kind   string
	By     string // uuid of who asked, "" — the schedule
	Name   string // restoring: the copy's folder
	Parts  []string
	Secret string // restoring: the copy's password, sealed
	Safety bool
}

// take marks the oldest queued copy (or restoring) as being made; none — ID 0. One left «running» by a stopped
// worker six hours ago is failed first. The password of a restoring is taken off the line.
func (s *Store) take(ctx context.Context) (job, error) {
	if _, err := s.db.Exec(ctx, `UPDATE backup_runs SET status = 'failed', finished_at = now(), error = 'interrupted', secret = ''
		WHERE status = 'running' AND started_at < now() - interval '6 hours'`); err != nil {
		return job{}, err
	}
	var j job
	err := s.db.QueryRow(ctx, `UPDATE backup_runs r SET status = 'running', started_at = now(), secret = ''
		FROM (SELECT id, secret FROM backup_runs WHERE status = 'queued' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED) q
		WHERE r.id = q.id
		RETURNING r.id, r.kind, COALESCE(r.created_by::text, ''), r.name, r.parts, q.secret, r.safety`).Scan(&j.ID, &j.Kind, &j.By, &j.Name, &j.Parts, &j.Secret, &j.Safety)
	if errors.Is(err, pgx.ErrNoRows) {
		return job{}, nil
	}
	return j, err
}

func (s *Store) finish(ctx context.Context, id int64, name string, parts []string, size int64, runErr error) error {
	return s.finishUp(ctx, id, name, parts, size, runErr, "", "", nil)
}

// finishUp writes how the copy went, with where it went on the outside place (or why not).
func (s *Store) finishUp(ctx context.Context, id int64, name string, parts []string, size int64, runErr error, target, targetPath string, upErr error) error {
	upMsg := ""
	if upErr != nil {
		upMsg = cutMsg(upErr.Error())
	}
	status, msg := "done", ""
	if runErr != nil {
		status, msg = "failed", cutMsg(runErr.Error())
	}
	if parts == nil {
		parts = []string{}
	}
	_, err := s.db.Exec(ctx, `UPDATE backup_runs SET status = $2, finished_at = now(), name = $3, parts = $4, size = $5, error = $6,
		target = $7, target_path = $8, upload_error = $9 WHERE id = $1`, id, status, name, parts, size, msg, target, targetPath, upMsg)
	return err
}

func cutMsg(msg string) string {
	if len(msg) > 2000 {
		return strings.ToValidUTF8(msg[:2000], "")
	}
	return msg
}

// lastDir is the folder of the latest copy kept that holds the part ("" — none), relative to the backups' folder.
func (s *Store) lastDir(ctx context.Context, part string) (string, error) {
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM backup_runs WHERE kind = 'backup' AND status = 'done' AND removed_at IS NULL AND name <> ''
		AND $1 = ANY(parts) ORDER BY id DESC LIMIT 1`, part).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// tellFailed notifies the administrators (the bell, «Резервное копирование» opens) that a copy failed.
func (s *Store) tellFailed(ctx context.Context, runErr error, adminGroups []string) error {
	return s.tell(ctx, "backup_failed", runErr.Error(), adminGroups)
}

// tell notifies the administrators: kind backup_failed, backup_restored (body — the copy), backup_restore_failed.
func (s *Store) tell(ctx context.Context, kind, msg string, adminGroups []string) error {
	if r := []rune(msg); len(r) > 300 {
		msg = string(r[:300]) + "…"
	}
	_, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
		SELECT id, $3, $1, '/settings/backup' FROM users
		WHERE active AND NOT guest AND (admin OR groups && $2::text[])`, msg, adminGroups, kind)
	return err
}

// The secrets' files are encrypted: "GO24ENC1", the salt of scrypt (16 bytes), then AES-256-GCM (nonce first).
const encMagic = "GO24ENC1"

func passKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
}

// Encrypt seals data with the backups' password.
func Encrypt(password string, data []byte) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := passKey(password, salt)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(encMagic), salt...)
	out = append(out, nonce...)
	return g.Seal(out, nonce, data, nil), nil
}

// Decrypt opens what Encrypt sealed.
func Decrypt(password string, data []byte) ([]byte, error) {
	if len(data) < len(encMagic)+16+12 || string(data[:len(encMagic)]) != encMagic {
		return nil, errors.New("backup: not an encrypted file of go24")
	}
	data = data[len(encMagic):]
	key, err := passKey(password, data[:16])
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	data = data[16:]
	plain, err := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], nil)
	if err != nil {
		return nil, i18n.E("backup.err_wrong_password")
	}
	return plain, nil
}
