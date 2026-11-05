package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go24/internal/update"
)

// runUpdater is «go24 updater», the service go24-updater: it installs the releases go24 asks for (see internal/update).
// It needs the Docker socket and the installation mounted at the same path as on the server (GO24_INSTALL_DIR).
func runUpdater(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	dir := envOr("GO24_INSTALL_DIR", "/srv/go24")
	updates := envOr("GO24_UPDATES_DIR", "/updates")
	if os.Getenv("GO24_UPDATE") == "off" {
		log.Info("updater: GO24_UPDATE=off, updates come from git; idle")
		<-ctx.Done()
		return nil
	}
	// the updates' folder: go24 (uid 10001) writes the requests and the archives into it
	if err := os.MkdirAll(updates, 0o755); err != nil {
		return err
	}
	_ = os.Chown(updates, 10001, 10001)
	r := &update.Runner{Dir: dir, Updates: updates, Docker: update.CLI{}, Key: update.PublicKey,
		Health: update.HTTPHealth(envOr("GO24_UPDATE_HEALTH_URL", "http://go24:8080")),
		Wait:   10 * time.Minute, Every: 5 * time.Second, Log: log}
	// its own container: the image (for recreating it after an update) and the installation's mount, which must be at
	// the same path as on the server, or the relative paths of docker-compose.yml point elsewhere
	host, _ := os.Hostname()
	var out bytes.Buffer
	if err := r.Docker.Run(ctx, nil, &out, "inspect", "--format",
		`{{.Config.Image}}{{range .Mounts}} {{.Source}}={{.Destination}}{{end}}`, host); err != nil {
		return fmt.Errorf("updater: no access to Docker: %w", err)
	}
	f := strings.Fields(out.String())
	if len(f) == 0 {
		return fmt.Errorf("updater: cannot see its own container")
	}
	r.Image = f[0]
	if !strings.Contains(" "+strings.Join(f[1:], " ")+" ", " "+dir+"="+dir+" ") {
		return fmt.Errorf("updater: the installation must be mounted at the same path as on the server (%s:%s); set GO24_INSTALL_DIR in deploy/.env", dir, dir)
	}
	log.Info("updater ready", "dir", dir, "updates", updates, "image", r.Image)
	r.Loop(ctx)
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
