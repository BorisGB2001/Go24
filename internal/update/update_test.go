package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVersions(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"0.5.8", "0.5.7", true}, {"v0.6.0", "0.5.17", true}, {"0.5.10", "0.5.9", true}, {"1.0.0", "0.99.99", true},
		{"0.5.7", "0.5.7", false}, {"0.5.6", "0.5.7", false}, {"0.6.0", "0.6.0-dev", true}, {"0.6.0-dev", "0.6.0", false},
		{"0.6.0", "dev", true}, {"x", "0.5.7", false}, {"0.5", "0.4.0", false}, {"0.05.1", "0.4.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.newer {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
	for v, dev := range map[string]bool{"0.5.7": false, "0.6.0-dev": true, "dev": true, "": true} {
		if Dev(v) != dev {
			t.Errorf("Dev(%q) != %v", v, dev)
		}
	}
}

func keys(t *testing.T) (string, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub), priv
}

func sign(priv ed25519.PrivateKey, data []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)) + "\n")
}

func TestVerify(t *testing.T) {
	pub, priv := keys(t)
	_, other := keys(t)
	data := []byte("go24 release")
	if err := Verify(pub, data, sign(priv, data)); err != nil {
		t.Fatalf("good signature: %v", err)
	}
	if err := Verify(pub, data, sign(other, data)); !errors.Is(err, ErrSignature) {
		t.Fatalf("foreign key: %v", err)
	}
	if err := Verify(pub, []byte("go24 releasE"), sign(priv, data)); !errors.Is(err, ErrSignature) {
		t.Fatalf("damaged archive: %v", err)
	}
	bad := sign(priv, data)
	bad[3] ^= 1
	if err := Verify(pub, data, bad); !errors.Is(err, ErrSignature) {
		t.Fatalf("damaged signature: %v", err)
	}
	if err := Verify(pub, data, []byte("not base64!")); !errors.Is(err, ErrSignature) {
		t.Fatalf("garbage signature: %v", err)
	}
	if err := Verify("", data, sign(priv, data)); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}
}

func TestMergeEnv(t *testing.T) {
	env := "# mine\nBIND_IP=192.168.1.5\nGO24_DB_PASSWORD=secret1\nGO24_TZ=Europe/Moscow"
	example := `# comment
BIND_IP=127.0.0.1
GO24_DB_PASSWORD=
GO24_TZ=Europe/Amsterdam
NEW_DB_PASSWORD=
NEW_SECRET_KEY=
RCLONE_RC_PASS=
GO24_GDRIVE_CLIENT_SECRET=
NEW_FEATURE_URL=http://x:1
#COMMENTED=1
EMPTY_OPT=
`
	n := 0
	gen := func() string { n++; return fmt.Sprintf("gen%d", n) }
	out, added := MergeEnv(env, example, "0.5.8", gen)
	want := []string{"NEW_DB_PASSWORD", "NEW_SECRET_KEY", "RCLONE_RC_PASS", "GO24_GDRIVE_CLIENT_SECRET", "NEW_FEATURE_URL", "EMPTY_OPT"}
	if !slices.Equal(added, want) {
		t.Fatalf("added %v, want %v", added, want)
	}
	for _, line := range []string{"BIND_IP=192.168.1.5\n", "GO24_DB_PASSWORD=secret1\n", "GO24_TZ=Europe/Moscow\n", "NEW_DB_PASSWORD=gen1\n",
		"NEW_SECRET_KEY=gen2\n", "RCLONE_RC_PASS=gen3\n", "GO24_GDRIVE_CLIENT_SECRET=\n", "NEW_FEATURE_URL=http://x:1\n", "EMPTY_OPT=\n",
		"# added by go24-updater 0.5.8"} {
		if !strings.Contains(out, line) {
			t.Errorf("no %q in\n%s", line, out)
		}
	}
	if strings.Contains(out, "COMMENTED") || strings.Count(out, "BIND_IP=") != 1 {
		t.Errorf("merged badly:\n%s", out)
	}
	if again, added := MergeEnv(out, example, "0.5.9", gen); len(added) != 0 || again != out {
		t.Errorf("second merge added %v", added)
	}
	if len(RandomSecret()) != 64 {
		t.Error("RandomSecret is not 32 bytes in hex")
	}
}

// release makes a .tar.gz of files under the folder top ("" — none).
func release(t *testing.T, top string, files map[string]string) []byte {
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		mode := int64(0o644)
		if strings.HasSuffix(n, ".sh") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: top + n, Mode: mode, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(files[n]))
	}
	tw.Close()
	gz.Close()
	return b.Bytes()
}

func writeTree(t *testing.T, dir string, files map[string]string) {
	for n, c := range files {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readFile(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func TestUnpackInstall(t *testing.T) {
	stage, dir := t.TempDir(), t.TempDir()
	writeTree(t, dir, map[string]string{
		"VERSION": "0.5.7\n", "internal/old/gone.go": "package old", "internal/db/migrations/001_a.sql": "a",
		"deploy/.env": "SECRET=1", "deploy/backups/x.sql.gz": "dump", "deploy/theme/theme.css": "mine",
		"deploy/locales/de-DE.json": "{}", "deploy/matrix/homeserver.yaml": "conf", "notes.txt": "mine too",
	})
	root, err := Unpack(bytes.NewReader(release(t, "go24-0.5.8/", map[string]string{
		"VERSION": "0.5.8\n", "internal/db/migrations/001_a.sql": "a2", "internal/db/migrations/002_b.sql": "b",
		"deploy/.env": "EVIL=1", "deploy/theme/theme.css": "theirs", "deploy/locales/README.md": "langs", "tools/x.sh": "#!/bin/sh",
	})), stage)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) != "go24-0.5.8" {
		t.Fatalf("root %s", root)
	}
	files, err := Install(root, dir, nil, -1, -1)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"VERSION", "deploy/locales/README.md", "internal/db/migrations/001_a.sql", "internal/db/migrations/002_b.sql", "tools/x.sh"}; !slices.Equal(files, want) {
		t.Fatalf("files %v", files)
	}
	for n, want := range map[string]string{
		"VERSION": "0.5.8\n", "internal/db/migrations/001_a.sql": "a2", "internal/db/migrations/002_b.sql": "b",
		"deploy/.env": "SECRET=1", "deploy/backups/x.sql.gz": "dump", "deploy/theme/theme.css": "mine",
		"deploy/locales/de-DE.json": "{}", "deploy/locales/README.md": "langs", "deploy/matrix/homeserver.yaml": "conf",
		"notes.txt": "mine too", "internal/old/gone.go": "<open " + filepath.Join(dir, "internal/old/gone.go") + ": no such file or directory>",
	} {
		if got := readFile(t, filepath.Join(dir, n)); got != want {
			t.Errorf("%s = %q, want %q", n, got, want)
		}
	}
	if st, _ := os.Stat(filepath.Join(dir, "tools/x.sh")); st.Mode().Perm()&0o100 == 0 {
		t.Error("tools/x.sh lost its exec bit")
	}

	// an archive with a path out of the folder is refused
	if _, err := Unpack(bytes.NewReader(release(t, "", map[string]string{"../evil": "x"})), t.TempDir()); err == nil {
		t.Error("../evil unpacked")
	}

	// the snapshot and back: files of the old version not in the snapshot go (by the list of the new files)
	var snap bytes.Buffer
	if err := Snapshot(dir, &snap); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snap.Bytes(), []byte("SECRET=1")) {
		t.Error("the snapshot holds deploy/.env")
	}
	writeTree(t, dir, map[string]string{"VERSION": "0.5.9\n", "tools/new.sh": "new"})
	root2, err := Unpack(&snap, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Install(root2, dir, []string{"VERSION", "tools/new.sh"}, -1, -1); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, "VERSION")) != "0.5.8\n" {
		t.Error("VERSION not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "tools/new.sh")); err == nil {
		t.Error("tools/new.sh survived the rollback")
	}
}

// fakeDocker answers the docker commands go24-updater runs and writes down what it was asked.
type fakeDocker struct {
	calls    []string
	healthy  bool        // go24 comes up healthy
	healthIf func() bool // or when this says so
	failOn   string      // a command (its beginning, after «compose -f … --env-file …») that fails
	dump     string
	restore  string // what psql got
}

func (f *fakeDocker) Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := strings.Join(args, " ")
	if args[0] == "compose" {
		cmd = strings.Join(args[5:], " ")
	}
	f.calls = append(f.calls, cmd)
	if f.failOn != "" && strings.HasPrefix(cmd, f.failOn) {
		return errors.New("failed: " + cmd)
	}
	switch {
	case strings.HasPrefix(cmd, "exec -T postgres pg_dumpall"):
		io.WriteString(stdout, f.dump)
	case strings.HasPrefix(cmd, "exec -T postgres psql"):
		b, _ := io.ReadAll(stdin)
		f.restore = string(b)
	case cmd == "config --services":
		io.WriteString(stdout, "postgres\ngo24\ngo24-worker\ngo24-updater\nsynapse\n")
	case cmd == "config --format json":
		io.WriteString(stdout, `{"services": {"go24": {"image": "go24:dev", "build": {"context": "/srv/go24"}},
			"go24-worker": {"image": "go24:dev"}, "piper": {"image": "go24-piper:1.8.0", "build": {"context": "piper"}},
			"go24-updater": {"image": "go24:dev"}, "postgres": {"image": "postgres:17-alpine"}}}`)
	case strings.HasPrefix(cmd, "ps -a"):
		if f.healthy || (f.healthIf != nil && f.healthIf()) {
			io.WriteString(stdout, "go24 running healthy\ngo24-worker running healthy\n")
		} else {
			io.WriteString(stdout, "go24 restarting \ngo24-worker running unhealthy\n")
		}
	}
	return nil
}

// setup is an installation of 0.5.7 and a signed release 0.5.8 asked for.
func setup(t *testing.T, newFiles map[string]string) (*Runner, *fakeDocker) {
	pub, priv := keys(t)
	dir, updates := t.TempDir(), t.TempDir()
	writeTree(t, dir, map[string]string{
		"VERSION": "0.5.7\n", "cmd/go24/main.go": "old", "deploy/.env": "A=1\nGO24_DB_PASSWORD=x\n",
		"deploy/.env.example": "A=\nGO24_DB_PASSWORD=\n", "deploy/docker-compose.yml": "old compose",
	})
	if newFiles == nil {
		newFiles = map[string]string{"VERSION": "0.5.8\n", "cmd/go24/main.go": "new", "deploy/docker-compose.yml": "new compose",
			"deploy/.env.example": "A=\nGO24_DB_PASSWORD=\nNEW_PASSWORD=\nNEW_URL=http://n\n"}
	}
	data := release(t, "go24-0.5.8/", newFiles)
	os.MkdirAll(filepath.Join(updates, "0.5.8"), 0o755)
	os.WriteFile(filepath.Join(updates, "0.5.8", "go24-0.5.8.tar.gz"), data, 0o644)
	os.WriteFile(filepath.Join(updates, "0.5.8", "go24-0.5.8.tar.gz.sig"), sign(priv, data), 0o644)
	fd := &fakeDocker{healthy: true, dump: "-- dump of 0.5.7\n"}
	r := &Runner{Dir: dir, Updates: updates, Docker: fd, Key: pub, Wait: 50 * time.Millisecond, Every: 10 * time.Millisecond,
		Health: func(context.Context) error { return nil }, Image: "go24:dev"}
	return r, fd
}

func indexOf(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, prefix) })
}

func TestUpdaterSteps(t *testing.T) {
	r, fd := setup(t, nil)
	st := r.Handle(context.Background(), Request{ID: "j1", Kind: "update", Version: "0.5.8", By: "Boris"})
	if st.State != OK {
		t.Fatalf("state %s: %s\n%v", st.State, st.Error, st.Log)
	}
	if got := readFile(t, filepath.Join(r.Dir, "cmd/go24/main.go")); got != "new" {
		t.Errorf("main.go = %q", got)
	}
	env := readFile(t, filepath.Join(r.Dir, "deploy/.env"))
	if !strings.HasPrefix(env, "A=1\nGO24_DB_PASSWORD=x\n") || !strings.Contains(env, "NEW_URL=http://n\n") || strings.Contains(env, "NEW_PASSWORD=\n") {
		t.Errorf(".env:\n%s", env)
	}
	// the order: the dump, the images kept, pull, up --build of all but go24-updater, the health, go24-updater recreated
	order := []string{"exec -T postgres pg_dumpall -U postgres --clean --if-exists", "tag go24:dev go24-rollback/go24:dev",
		"pull --quiet postgres", "up -d --build postgres go24 go24-worker synapse", "ps -a", "run -d --rm"}
	last := -1
	for _, o := range order {
		i := indexOf(fd.calls, o)
		if i <= last {
			t.Fatalf("%q at %d (after %d) in %q", o, i, last, fd.calls)
		}
		last = i
	}
	if !strings.Contains(fd.calls[last], "up -d --no-deps --force-recreate go24-updater") {
		t.Errorf("self recreate: %s", fd.calls[last])
	}
	m, _ := ReadRollback(r.Updates)
	if m == nil || m.Version != "0.5.7" || len(m.Images) != 2 || !slices.Contains(m.Files, "cmd/go24/main.go") {
		t.Fatalf("rollback meta %+v", m)
	}
	if dump := readGz(t, m.Dump); dump != fd.dump {
		t.Errorf("dump %q", dump)
	}
	saved, _ := ReadStatus(r.Updates)
	if saved.State != OK || saved.From != "0.5.7" || saved.To != "0.5.8" || saved.FinishedAt == nil {
		t.Errorf("status %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(r.Updates, "0.5.8")); err == nil {
		t.Error("the release's folder is left")
	}
}

func readGz(t *testing.T, p string) string {
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(gz)
	return string(b)
}

func TestUpdaterRollsBackWhenNotHealthy(t *testing.T) {
	r, fd := setup(t, nil)
	fd.healthy = false
	st := r.Handle(context.Background(), Request{ID: "j2", Kind: "update", Version: "0.5.8"})
	// the old version does not come up either: failed, both reasons
	if st.State != Failed || !strings.Contains(st.Error, "did not come up") {
		t.Fatalf("state %s: %s", st.State, st.Error)
	}

	r, fd = setup(t, nil)
	fd.healthy = false
	// go24 is unhealthy until the old files are back
	fd.healthIf = func() bool { return readFile(t, filepath.Join(r.Dir, "VERSION")) == "0.5.7\n" }
	st = r.Handle(context.Background(), Request{ID: "j3", Kind: "update", Version: "0.5.8"})
	if st.State != RolledBack || !strings.Contains(st.Error, "did not come up") {
		t.Fatalf("state %s: %s\n%v", st.State, st.Error, st.Log)
	}
	if got := readFile(t, filepath.Join(r.Dir, "cmd/go24/main.go")); got != "old" {
		t.Errorf("main.go = %q after the rollback", got)
	}
	if got := readFile(t, filepath.Join(r.Dir, "deploy/.env")); got != "A=1\nGO24_DB_PASSWORD=x\n" {
		t.Errorf(".env = %q after the rollback", got)
	}
	if fd.restore != fd.dump {
		t.Errorf("database restored from %q", fd.restore)
	}
	last := indexOf(fd.calls, "up -d --build")
	for _, o := range []string{"tag go24-rollback/go24:dev go24:dev", "stop go24 go24-worker synapse", "up -d --wait postgres", "exec -T postgres psql -q -U postgres -d postgres", "up -d postgres go24 go24-worker synapse"} {
		i := indexOf(fd.calls[last+1:], o)
		if i < 0 {
			t.Fatalf("no %q after %d in %q", o, last, fd.calls)
		}
		last += 1 + i
	}
	if m, _ := ReadRollback(r.Updates); m != nil {
		t.Error("the rollback folder is kept after a rollback")
	}
}

func TestUpdaterRefuses(t *testing.T) {
	// a damaged signature: nothing is touched
	r, fd := setup(t, nil)
	sig := filepath.Join(r.Updates, "0.5.8", "go24-0.5.8.tar.gz.sig")
	b, _ := os.ReadFile(sig)
	b[5] ^= 1
	os.WriteFile(sig, b, 0o644)
	st := r.Handle(context.Background(), Request{ID: "j4", Kind: "update", Version: "0.5.8"})
	if st.State != Failed || !strings.Contains(st.Error, "signature") || len(fd.calls) != 0 {
		t.Fatalf("state %s: %s, calls %v", st.State, st.Error, fd.calls)
	}
	if readFile(t, filepath.Join(r.Dir, "VERSION")) != "0.5.7\n" {
		t.Error("files changed")
	}
	// not newer
	r, fd = setup(t, nil)
	os.WriteFile(filepath.Join(r.Dir, "VERSION"), []byte("0.5.8\n"), 0o644)
	if st := r.Handle(context.Background(), Request{ID: "j5", Kind: "update", Version: "0.5.8"}); st.State != Failed || len(fd.calls) != 0 {
		t.Fatalf("not newer: %s %s", st.State, st.Error)
	}
	// the archive says another version
	r, fd = setup(t, map[string]string{"VERSION": "0.5.9\n"})
	if st := r.Handle(context.Background(), Request{ID: "j6", Kind: "update", Version: "0.5.8"}); st.State != Failed || !strings.Contains(st.Error, "VERSION") {
		t.Fatalf("VERSION mismatch: %s %s", st.State, st.Error)
	}
	// the dump fails: nothing changed
	r, fd = setup(t, nil)
	fd.failOn = "exec -T postgres pg_dumpall"
	if st := r.Handle(context.Background(), Request{ID: "j7", Kind: "update", Version: "0.5.8"}); st.State != Failed || readFile(t, filepath.Join(r.Dir, "VERSION")) != "0.5.7\n" {
		t.Fatalf("dump failed: %s %s", st.State, st.Error)
	}
}

func TestClient(t *testing.T) {
	pub, priv := keys(t)
	data := release(t, "", map[string]string{"VERSION": "0.5.8\n"})
	sig := sign(priv, data)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.5.8","body":"- fixes","html_url":"https://github.com/x","published_at":"2026-11-04T10:00:00Z",
				"assets":[{"name":"go24-0.5.8.tar.gz","browser_download_url":"%[1]s/a.tgz"},{"name":"go24-0.5.8.tar.gz.sig","browser_download_url":"%[1]s/a.sig"}]}`, srv.URL)
		case "/a.tgz":
			w.Write(data)
		case "/a.sig":
			w.Write(sig)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := &Client{Dir: dir, Current: "0.5.7", Key: pub, API: srv.URL}
	if !c.Enabled() || (&Client{Dir: dir, Current: "0.5.7", Off: true}).Enabled() || (&Client{Dir: dir, Current: "0.6.0-dev"}).Enabled() {
		t.Fatal("Enabled")
	}
	if _, err := (&Client{Dir: dir, Current: "0.6.0-dev"}).Update(context.Background(), "x"); !errors.Is(err, ErrDev) {
		t.Fatalf("dev update: %v", err)
	}
	if _, err := c.Update(context.Background(), "Boris"); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("update before a check: %v", err)
	}
	rel, err := c.Check(context.Background())
	if err != nil || rel.Version != "0.5.8" || rel.Notes != "- fixes" || rel.Archive == "" || rel.Signature == "" {
		t.Fatalf("check: %+v %v", rel, err)
	}
	req, err := c.Update(context.Background(), "Boris")
	if err != nil || req.Version != "0.5.8" || req.Kind != "update" {
		t.Fatalf("update: %+v %v", req, err)
	}
	if !c.Busy() {
		t.Error("not busy with a request")
	}
	if _, err := c.Update(context.Background(), "Boris"); !errors.Is(err, ErrBusy) {
		t.Errorf("second update: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "0.5.8", "go24-0.5.8.tar.gz")); !bytes.Equal(got, data) {
		t.Error("archive not saved")
	}
	os.Remove(filepath.Join(dir, RequestFile))

	// a foreign signature: nothing is asked
	_, other := keys(t)
	sig = sign(other, data)
	if _, err := c.Update(context.Background(), "Boris"); !errors.Is(err, ErrSignature) || c.Busy() {
		t.Fatalf("foreign signature: %v", err)
	}
	if _, err := c.Rollback("Boris"); !errors.Is(err, ErrNoRollback) {
		t.Errorf("rollback with nothing kept: %v", err)
	}

	// the announcement of a finished job, once
	writeJSON(dir, StatusFile, Status{ID: "s1", State: OK, To: "0.5.8"})
	if st, ok, err := c.Announce(); !ok || err != nil || st.To != "0.5.8" {
		t.Fatalf("announce: %v %v", ok, err)
	}
	if _, ok, _ := c.Announce(); ok {
		t.Error("announced twice")
	}
	writeJSON(dir, StatusFile, Status{ID: "s2", State: Installing})
	if _, ok, _ := c.Announce(); ok {
		t.Error("announced a job under way")
	}
	// GitHub down: the error is kept with the release seen before
	srv.Close()
	if rel, err := c.Check(context.Background()); err == nil || rel.Version != "0.5.8" || rel.Error == "" {
		t.Errorf("check with GitHub down: %+v %v", rel, err)
	}
}

func TestMatrixConfig(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"deploy/.env":                            "MATRIX_SERVER_NAME=go24.lan\nSYNAPSE_DB_PASSWORD=pw\nOTHER=no\n",
		"deploy/matrix/homeserver.yaml.tmpl":     "server_name: \"$MATRIX_SERVER_NAME\"\npassword: ${SYNAPSE_DB_PASSWORD}\nkeep: $OTHER\n",
		"deploy/matrix/element-config.json.tmpl": `{"name": "${MATRIX_SERVER_NAME}"}`,
	})
	if err := MatrixConfig(dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "deploy/matrix/homeserver.yaml")); got != "server_name: \"go24.lan\"\npassword: pw\nkeep: $OTHER\n" {
		t.Errorf("homeserver.yaml = %q", got)
	}
	if st, _ := os.Stat(filepath.Join(dir, "deploy/matrix/homeserver.yaml")); st.Mode().Perm() != 0o600 {
		t.Errorf("homeserver.yaml mode %v", st.Mode())
	}
	if got := readFile(t, filepath.Join(dir, "deploy/matrix/element-config.json")); got != `{"name": "go24.lan"}` {
		t.Errorf("element-config.json = %q", got)
	}
}
