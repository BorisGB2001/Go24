package update

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The files of the updates' folder (the volume /updates, shared by go24 and go24-updater).
const (
	LatestFile    = "latest.json"   // the latest release on GitHub, as last checked (go24)
	RequestFile   = "request.json"  // what go24 asks go24-updater to do (go24 writes, go24-updater takes it)
	StatusFile    = "status.json"   // what go24-updater does or did (go24-updater)
	RollbackDir   = "rollback"      // the previous version: files, settings, the database's dump (go24-updater)
	RollbackInfo  = "rollback.json" // in RollbackDir: which version it is
	AnnouncedFile = "announced"     // the id of the last status the administrators were told of (go24)
)

// Release is the latest release on GitHub, as go24 saw it.
type Release struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"` // its page on GitHub
	PublishedAt time.Time `json:"published_at"`
	Archive     string    `json:"archive"`   // go24-X.Y.Z.tar.gz
	Signature   string    `json:"signature"` // go24-X.Y.Z.tar.gz.sig
	CheckedAt   time.Time `json:"checked_at"`
	Error       string    `json:"error,omitempty"` // the last check failed (the release, if any, is from before)
}

// Request is a job for go24-updater: «update» to Version (its archive and signature are in the folder Version) or
// «rollback» to the version kept in RollbackDir.
type Request struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Version string    `json:"version,omitempty"`
	By      string    `json:"by"` // who asked (a name, for the log)
	At      time.Time `json:"at"`
}

// States of Status.
const (
	Installing = "installing"
	OK         = "ok"          // updated (or rolled back on request)
	RolledBack = "rolled_back" // the new version did not start: the previous one is back, with its database
	Failed     = "failed"      // nothing changed (a check failed) or the rollback itself failed: see Error and Log
)

// Status is what go24-updater does or did last; the page «Обновление» shows it live.
type Status struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	State      string     `json:"state"`
	From       string     `json:"from"`
	To         string     `json:"to"`
	By         string     `json:"by,omitempty"`
	Step       string     `json:"step"` // the step now (or where it ended): verify, unpack, backup, files, env, matrix, start, health, rollback, done
	Error      string     `json:"error,omitempty"`
	Log        []LogLine  `json:"log"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Done says whether the job is over.
func (s Status) Done() bool { return s.State != "" && s.State != Installing }

// RollbackMeta is the previous version kept for a rollback.
type RollbackMeta struct {
	Version string    `json:"version"`
	Dump    string    `json:"dump"`   // the database's dump, a path in the installation
	Images  []string  `json:"images"` // the images built locally, kept under rollback tags
	Files   []string  `json:"files"`  // the new version's files (what a rollback removes when the old one has not)
	At      time.Time `json:"at"`
}

// readJSON reads a JSON file of dir; a missing file is (false, nil).
func readJSON(dir, name string, v any) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

// writeJSON writes a JSON file of dir at once (a reader never sees half of it), readable by all.
func writeJSON(dir, name string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, name))
}

// ReadStatus is go24-updater's status (zero — it has done nothing yet).
func ReadStatus(dir string) (Status, error) {
	var s Status
	_, err := readJSON(dir, StatusFile, &s)
	return s, err
}

// ReadRollback is the previous version kept for a rollback (nil — none).
func ReadRollback(dir string) (*RollbackMeta, error) {
	var m RollbackMeta
	ok, err := readJSON(filepath.Join(dir, RollbackDir), RollbackInfo, &m)
	if !ok || err != nil {
		return nil, err
	}
	return &m, nil
}
