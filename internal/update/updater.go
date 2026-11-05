package update

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Docker runs the docker command line (go24-updater talks to the server's Docker through its socket).
type Docker interface {
	// Run runs «docker args…», stdin and stdout may be nil; the error carries the end of what it wrote to stderr.
	Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error
}

// CLI is Docker by the docker program of the image (docker-cli, docker-cli-compose).
type CLI struct{}

func (CLI) Run(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	var errb tail
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		shown := args // «compose -f … --env-file …» is the same every time: its command is what tells
		if len(shown) > 5 && shown[0] == "compose" {
			shown = shown[5:]
		}
		return fmt.Errorf("docker %s: %w: %s", strings.Join(shown[:min(len(shown), 4)], " "), err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// tail keeps the last 2 KB written.
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2048 {
		t.b = t.b[len(t.b)-2048:]
	}
	return len(p), nil
}
func (t *tail) String() string { return string(t.b) }

// Self is the service of go24-updater in docker-compose.yml: an update never stops it on the way.
const Self = "go24-updater"

// Runner is go24-updater: it takes the jobs go24 leaves in the updates' folder and carries them out step by step,
// writing the status for the page.
type Runner struct {
	Dir     string // the installation (/srv/go24): mounted at the same path as on the server, the compose file's own paths work
	Updates string // the updates' folder (/updates)
	Docker  Docker
	Key     string                          // the releases' public key
	Health  func(ctx context.Context) error // go24 answers /healthz
	Wait    time.Duration                   // how long go24 may take to come up (10 minutes)
	Every   time.Duration                   // how often to look (5 s)
	Image   string                          // go24-updater's image, for the helper that recreates it ("" — not recreated)
	Log     *slog.Logger

	st Status
}

// HTTPHealth checks go24's /healthz at base (http://go24:8080).
func HTTPHealth(base string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("/healthz: %s", resp.Status)
		}
		return nil
	}
}

func (r *Runner) compose(args ...string) []string {
	return append([]string{"compose", "-f", filepath.Join(r.Dir, "deploy", "docker-compose.yml"),
		"--env-file", filepath.Join(r.Dir, "deploy", ".env")}, args...)
}

func (r *Runner) docker(ctx context.Context, args ...string) (string, error) {
	var out bytes.Buffer
	err := r.Docker.Run(ctx, nil, &out, args...)
	return out.String(), err
}

// Loop takes the jobs until ctx is done. A job left «installing» by a go24-updater stopped on the way is marked
// failed: what was done is in its log, the previous version is in the rollback folder.
func (r *Runner) Loop(ctx context.Context) {
	if st, err := ReadStatus(r.Updates); err == nil && st.State == Installing {
		r.st = st
		r.finish(Failed, errors.New("go24-updater was stopped during the job"))
	}
	for t := time.NewTicker(3 * time.Second); ; {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var req Request
		ok, err := readJSON(r.Updates, RequestFile, &req)
		if err != nil {
			r.Log.Error("update request", "err", err)
		}
		if !ok {
			continue
		}
		os.Remove(filepath.Join(r.Updates, RequestFile))
		if err == nil {
			r.Handle(ctx, req)
		}
	}
}

// Handle carries out a job and returns its final status.
func (r *Runner) Handle(ctx context.Context, req Request) Status {
	from, _ := os.ReadFile(filepath.Join(r.Dir, "VERSION"))
	r.st = Status{ID: req.ID, Kind: req.Kind, State: Installing, From: strings.TrimSpace(string(from)), To: req.Version, By: req.By,
		StartedAt: time.Now(), Log: []LogLine{}}
	r.logf("%s %s → %s, asked by %s", req.Kind, r.st.From, req.Version, req.By)
	switch req.Kind {
	case "update":
		r.update(ctx)
	case "rollback":
		if err := r.rollback(ctx); err != nil {
			r.finish(Failed, err)
		} else {
			r.finish(OK, nil)
			r.recreateSelf(ctx)
		}
	default:
		r.finish(Failed, fmt.Errorf("unknown job %q", req.Kind))
	}
	return r.st
}

func (r *Runner) step(name string) {
	r.st.Step = name
	r.logf("step: %s", name)
}

func (r *Runner) logf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	r.st.Log = append(r.st.Log, LogLine{At: time.Now(), Text: text})
	if r.Log != nil {
		r.Log.Info("updater", "job", r.st.ID, "step", r.st.Step, "text", text)
	}
	if err := writeJSON(r.Updates, StatusFile, r.st); err != nil && r.Log != nil {
		r.Log.Error("update status", "err", err)
	}
}

func (r *Runner) finish(state string, err error) {
	r.st.State = state
	if err != nil {
		r.st.Error = err.Error()
	}
	now := time.Now()
	r.st.FinishedAt = &now
	if err != nil {
		r.logf("%s: %v", state, err)
	} else {
		r.logf("%s", state)
	}
}

// update installs the release asked for; until the files are laid out a failure changes nothing, after it the
// previous version is put back.
func (r *Runner) update(ctx context.Context) {
	v := r.st.To
	r.step("verify")
	name := filepath.Join(r.Updates, v, "go24-"+v+".tar.gz")
	data, err := os.ReadFile(name)
	if err == nil {
		var sig []byte
		if sig, err = os.ReadFile(name + ".sig"); err == nil {
			err = Verify(r.Key, data, sig)
		}
	}
	if err == nil && !Newer(v, r.st.From) {
		err = fmt.Errorf("%s is not newer than the installed %s", v, r.st.From)
	}
	if err != nil {
		r.finish(Failed, err)
		return
	}
	r.logf("signature is good")

	r.step("unpack")
	stage := filepath.Join(r.Updates, v, "tree")
	root, err := r.unpack(data, stage)
	data = nil
	if err == nil {
		if got, _ := os.ReadFile(filepath.Join(root, "VERSION")); strings.TrimSpace(string(got)) != v {
			err = fmt.Errorf("the archive's VERSION is %q, not %s", strings.TrimSpace(string(got)), v)
		}
	}
	if err != nil {
		r.finish(Failed, err)
		return
	}

	r.step("backup")
	meta, err := r.backup(ctx)
	if err != nil {
		r.finish(Failed, err)
		return
	}

	if err := r.install(ctx, root, meta); err != nil {
		r.logf("the new version failed: %v", err)
		if rerr := r.rollback(ctx); rerr != nil {
			r.finish(Failed, fmt.Errorf("%v; the rollback failed too: %v", err, rerr))
			return
		}
		r.finish(RolledBack, err)
		r.recreateSelf(ctx)
		return
	}
	os.RemoveAll(filepath.Join(r.Updates, v))
	r.finish(OK, nil)
	r.recreateSelf(ctx)
}

func (r *Runner) unpack(data []byte, stage string) (string, error) {
	if err := os.RemoveAll(stage); err != nil {
		return "", err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", err
	}
	return Unpack(bytes.NewReader(data), stage)
}

// backup keeps what a rollback needs: the database's dump in deploy/backups, the files and .env of the installation,
// the images built here under rollback tags.
func (r *Runner) backup(ctx context.Context) (*RollbackMeta, error) {
	meta := &RollbackMeta{Version: r.st.From, At: time.Now()}
	backups := filepath.Join(r.Dir, "deploy", "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		return nil, err
	}
	meta.Dump = filepath.Join(backups, "before-"+r.st.From+"-"+time.Now().Format("2006-01-02-1504")+".sql.gz")
	f, err := os.OpenFile(meta.Dump, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(f)
	err = r.Docker.Run(ctx, nil, gz, r.compose("exec", "-T", "postgres", "pg_dumpall", "-U", "postgres", "--clean", "--if-exists")...)
	if cerr := gz.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(meta.Dump)
		return nil, fmt.Errorf("database dump: %w", err)
	}
	if st, err := os.Stat(meta.Dump); err == nil {
		r.logf("database dump: %s (%d bytes)", meta.Dump, st.Size())
	}

	tmp := filepath.Join(r.Updates, RollbackDir+".new")
	if err := os.RemoveAll(tmp); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	sf, err := os.Create(filepath.Join(tmp, "files.tar.gz"))
	if err != nil {
		return nil, err
	}
	err = Snapshot(r.Dir, sf)
	if cerr := sf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("files snapshot: %w", err)
	}
	if env, err := os.ReadFile(filepath.Join(r.Dir, "deploy", ".env")); err == nil {
		if err := os.WriteFile(filepath.Join(tmp, "env"), env, 0o600); err != nil {
			return nil, err
		}
	}
	r.logf("files of %s kept for a rollback", r.st.From)

	if out, err := r.docker(ctx, r.compose("images")...); err == nil {
		r.logf("images before the update:\n%s", strings.TrimSpace(out))
	}
	built, err := r.builtImages(ctx)
	if err != nil {
		return nil, err
	}
	for _, img := range built {
		if _, err := r.docker(ctx, "tag", img, rollbackTag(img)); err != nil {
			return nil, err
		}
		meta.Images = append(meta.Images, img)
	}
	if err := writeJSON(tmp, RollbackInfo, meta); err != nil {
		return nil, err
	}
	old := filepath.Join(r.Updates, RollbackDir)
	if err := os.RemoveAll(old); err != nil {
		return nil, err
	}
	return meta, os.Rename(tmp, old)
}

// builtImages are the images the compose file builds here (go24, piper): an update rebuilds them under the same tag,
// so the previous ones are kept under rollback tags; the pulled images of the other components stay by their tags.
func (r *Runner) builtImages(ctx context.Context) ([]string, error) {
	cfg, err := r.config(ctx)
	if err != nil {
		return nil, err
	}
	var imgs []string
	for _, svc := range cfg {
		if len(svc.Build) > 0 && svc.Image != "" && !slices.Contains(imgs, svc.Image) {
			imgs = append(imgs, svc.Image)
		}
	}
	slices.Sort(imgs)
	return imgs, nil
}

// pulledServices are the services whose images come from a registry: not built here, not the image of one built here.
func (r *Runner) pulledServices(ctx context.Context) ([]string, error) {
	cfg, err := r.config(ctx)
	if err != nil {
		return nil, err
	}
	built := map[string]bool{}
	for _, svc := range cfg {
		if len(svc.Build) > 0 {
			built[svc.Image] = true
		}
	}
	var out []string
	for name, svc := range cfg {
		if name != Self && len(svc.Build) == 0 && svc.Image != "" && !built[svc.Image] {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out, nil
}

type composeService struct {
	Image string          `json:"image"`
	Build json.RawMessage `json:"build"`
}

// config is the services of the compose file, as compose reads it.
func (r *Runner) config(ctx context.Context) (map[string]composeService, error) {
	out, err := r.docker(ctx, r.compose("config", "--format", "json")...)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Services map[string]composeService `json:"services"`
	}
	if err := json.Unmarshal([]byte(out), &cfg); err != nil {
		return nil, fmt.Errorf("compose config: %w", err)
	}
	return cfg.Services, nil
}

func rollbackTag(img string) string { return "go24-rollback/" + img }

// install lays out the new version and starts it; an error means it is not up.
func (r *Runner) install(ctx context.Context, root string, meta *RollbackMeta) error {
	r.step("files")
	before := r.matrixTemplates()
	var old []string
	if b, err := os.ReadFile(filepath.Join(r.Updates, "installed.txt")); err == nil {
		old = strings.Fields(string(b))
	}
	uid, gid := owner(r.Dir)
	files, err := Install(root, r.Dir, old, uid, gid)
	if err != nil {
		return err
	}
	meta.Files = files
	if err := writeJSON(filepath.Join(r.Updates, RollbackDir), RollbackInfo, meta); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(r.Updates, "installed.txt"), []byte(strings.Join(files, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	r.logf("%d files laid out", len(files))

	r.step("env")
	if err := r.mergeEnv(); err != nil {
		return err
	}

	r.step("matrix")
	if after := r.matrixTemplates(); after != before {
		if _, err := os.Stat(filepath.Join(r.Dir, "deploy", "matrix", "homeserver.yaml")); err == nil {
			if err := MatrixConfig(r.Dir); err != nil {
				return fmt.Errorf("matrix config: %w", err)
			}
			r.logf("the messenger's configuration is rebuilt from the new templates")
		}
	} else {
		r.logf("the messenger's templates have not changed")
	}

	if err := r.start(ctx, true); err != nil {
		return err
	}
	return r.waitHealthy(ctx)
}

// mergeEnv adds to deploy/.env what the new .env.example has and it has not.
func (r *Runner) mergeEnv() error {
	envPath := filepath.Join(r.Dir, "deploy", ".env")
	env, err := os.ReadFile(envPath)
	if err != nil {
		return err
	}
	example, err := os.ReadFile(filepath.Join(r.Dir, "deploy", ".env.example"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	text, added := MergeEnv(string(env), string(example), r.st.To, RandomSecret)
	if len(added) == 0 {
		r.logf("no new settings")
		return nil
	}
	st, err := os.Stat(envPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(envPath, []byte(text), st.Mode().Perm()); err != nil {
		return err
	}
	r.logf("settings added to deploy/.env: %s", strings.Join(added, ", "))
	return nil
}

// matrixTemplates is a digest of the messenger's templates.
func (r *Runner) matrixTemplates() string {
	h := sha256.New()
	for _, n := range []string{"homeserver.yaml.tmpl", "element-config.json.tmpl"} {
		b, _ := os.ReadFile(filepath.Join(r.Dir, "deploy", "matrix", n))
		h.Write(b)
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// services are the services of the compose file but go24-updater (and the ones in skip).
func (r *Runner) services(ctx context.Context, skip ...string) ([]string, error) {
	out, err := r.docker(ctx, r.compose("config", "--services")...)
	if err != nil {
		return nil, err
	}
	var s []string
	for _, n := range strings.Fields(out) {
		if n != Self && !slices.Contains(skip, n) {
			s = append(s, n)
		}
	}
	return s, nil
}

// start pulls the components' images (build — and builds go24's) and starts all but go24-updater.
func (r *Runner) start(ctx context.Context, build bool) error {
	r.step("start")
	svcs, err := r.services(ctx)
	if err != nil {
		return err
	}
	args := []string{"up", "-d"}
	if build {
		// the images built here (go24:dev of go24, go24-worker, go24-updater) are not in a registry: not pulled
		pull, err := r.pulledServices(ctx)
		if err != nil {
			return err
		}
		if len(pull) > 0 {
			if _, err := r.docker(ctx, r.compose(append([]string{"pull", "--quiet"}, pull...)...)...); err != nil {
				return err
			}
		}
		r.logf("images pulled")
		args = append(args, "--build")
	}
	if _, err := r.docker(ctx, r.compose(append(args, svcs...)...)...); err != nil {
		return err
	}
	r.logf("services started")
	return nil
}

var psLine = regexp.MustCompile(`^(\S+)\s+(\S+)\s*(\S*)$`)

// waitHealthy waits for go24 and go24-worker running and healthy and for go24's /healthz.
func (r *Runner) waitHealthy(ctx context.Context) error {
	r.step("health")
	deadline := time.Now().Add(r.Wait)
	last := ""
	for {
		out, err := r.docker(ctx, r.compose("ps", "-a", "--format", "{{.Service}} {{.State}} {{.Health}}", "go24", "go24-worker")...)
		good := 0
		if err != nil {
			last = err.Error()
		} else {
			last = strings.TrimSpace(out)
			sc := bufio.NewScanner(strings.NewReader(out))
			for sc.Scan() {
				if m := psLine.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil && m[2] == "running" && (m[3] == "" || m[3] == "healthy") {
					good++
				}
			}
		}
		if good == 2 {
			if err := r.Health(ctx); err == nil {
				r.logf("go24 and go24-worker are up")
				return nil
			} else {
				last = err.Error()
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("go24 did not come up in %s: %s", r.Wait, last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.Every):
		}
	}
}

// rollback puts back the version kept by backup: its files and settings, its images and its database.
func (r *Runner) rollback(ctx context.Context) error {
	r.step("rollback")
	meta, err := ReadRollback(r.Updates)
	if err != nil {
		return err
	}
	if meta == nil {
		return errors.New("no previous version is kept")
	}
	r.st.To = meta.Version
	keep := filepath.Join(r.Updates, RollbackDir)
	data, err := os.ReadFile(filepath.Join(keep, "files.tar.gz"))
	if err != nil {
		return err
	}
	root, err := r.unpack(data, filepath.Join(keep, "tree"))
	if err != nil {
		return err
	}
	uid, gid := owner(r.Dir)
	files, err := Install(root, r.Dir, meta.Files, uid, gid)
	if err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(keep, "tree"))
	if err := os.WriteFile(filepath.Join(r.Updates, "installed.txt"), []byte(strings.Join(files, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if env, err := os.ReadFile(filepath.Join(keep, "env")); err == nil {
		envPath := filepath.Join(r.Dir, "deploy", ".env")
		mode := fs.FileMode(0o600)
		if st, err := os.Stat(envPath); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.WriteFile(envPath, env, mode); err != nil {
			return err
		}
	}
	r.logf("files and settings of %s are back", meta.Version)
	for _, img := range meta.Images {
		if _, err := r.docker(ctx, "tag", rollbackTag(img), img); err != nil {
			return err
		}
	}
	r.logf("images of %s are back", meta.Version)

	// the database: everything that uses it is stopped, the dump (made with --clean) goes back
	svcs, err := r.services(ctx, "postgres")
	if err != nil {
		return err
	}
	if _, err := r.docker(ctx, r.compose(append([]string{"stop"}, svcs...)...)...); err != nil {
		return err
	}
	if _, err := r.docker(ctx, r.compose("up", "-d", "--wait", "postgres")...); err != nil {
		return err
	}
	f, err := os.Open(meta.Dump)
	if err != nil {
		return fmt.Errorf("database dump: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("database dump: %w", err)
	}
	if err := r.Docker.Run(ctx, gz, io.Discard, r.compose("exec", "-T", "postgres", "psql", "-q", "-U", "postgres", "-d", "postgres")...); err != nil {
		return fmt.Errorf("database restore: %w", err)
	}
	r.logf("database of %s is back from %s", meta.Version, meta.Dump)
	if err := r.start(ctx, false); err != nil {
		return err
	}
	if err := r.waitHealthy(ctx); err != nil {
		return err
	}
	return os.RemoveAll(keep)
}

// recreateSelf starts go24-updater anew from its new image: a helper container (the same image, docker in it) does it,
// since a container cannot recreate itself.
func (r *Runner) recreateSelf(ctx context.Context) {
	if r.Image == "" {
		return
	}
	args := append([]string{"run", "-d", "--rm", "--user", "0", "--entrypoint", "docker",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", r.Dir + ":" + r.Dir, r.Image},
		r.compose("up", "-d", "--no-deps", "--force-recreate", Self)...)
	if _, err := r.docker(ctx, args...); err != nil && r.Log != nil {
		r.Log.Warn("updater: recreate go24-updater", "err", err)
	}
}

// owner is the owner of the installation's folder: the files laid out get it (go24-updater runs as root).
func owner(dir string) (int, int) {
	st, err := os.Stat(dir)
	if err != nil {
		return -1, -1
	}
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid), int(s.Gid)
	}
	return -1, -1
}

// matrixVars are the parameters of deploy/.env the messenger's templates take (as MATRIX_VARS of the Makefile).
var matrixVars = []string{"MATRIX_SERVER_NAME", "MATRIX_PUBLIC_URL", "MATRIX_ELEMENT_URL", "SYNAPSE_DB_PASSWORD",
	"SYNAPSE_REGISTRATION_SECRET", "SYNAPSE_MACAROON_SECRET", "SYNAPSE_FORM_SECRET", "GO24_OIDC_ISSUER_BASE",
	"MATRIX_OIDC_CLIENT_ID", "MATRIX_OIDC_CLIENT_SECRET", "MATRIX_OIDC_SKIP_VERIFICATION"}

// MatrixConfig rebuilds homeserver.yaml (owned by synapse, uid 991, mode 600) and element-config.json from their
// templates and deploy/.env, as «make matrix-config».
func MatrixConfig(dir string) error {
	env, err := os.ReadFile(filepath.Join(dir, "deploy", ".env"))
	if err != nil {
		return err
	}
	_, vals := envKeys(string(env))
	subst := func(s string) string {
		for _, k := range matrixVars {
			s = strings.ReplaceAll(s, "${"+k+"}", vals[k])
			s = regexp.MustCompile(`\$`+k+`\b`).ReplaceAllLiteralString(s, vals[k])
		}
		return s
	}
	m := filepath.Join(dir, "deploy", "matrix")
	uid, gid := owner(dir)
	for _, f := range []struct {
		name     string
		mode     fs.FileMode
		uid, gid int
	}{{"homeserver.yaml", 0o600, 991, 991}, {"element-config.json", 0o644, uid, gid}} {
		tmpl, err := os.ReadFile(filepath.Join(m, f.name+".tmpl"))
		if err != nil {
			return err
		}
		p := filepath.Join(m, f.name)
		if err := os.WriteFile(p, []byte(subst(string(tmpl))), f.mode); err != nil {
			return err
		}
		if err := os.Chmod(p, f.mode); err != nil {
			return err
		}
		if f.uid >= 0 && os.Geteuid() == 0 {
			if err := os.Chown(p, f.uid, f.gid); err != nil {
				return err
			}
		}
	}
	return nil
}
