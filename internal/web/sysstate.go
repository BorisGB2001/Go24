package web

import (
	"context"
	"net/http"
	"runtime"
	"slices"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/backup"
	"go24/internal/i18n"
	"go24/internal/sysinfo"
)

// sysReport is «Состояние системы»: what go24 knows of itself, its services and folders, and the troubles found.
type sysReport struct {
	Version   string            `json:"version"`
	Go        string            `json:"go"`
	CheckedAt time.Time         `json:"checked_at"`
	Nodes     []sysinfo.Node    `json:"nodes"`
	Database  *sysinfo.Database `json:"database,omitempty"`
	DBError   string            `json:"database_error,omitempty"`
	Services  []sysinfo.Check   `json:"services"`
	Disks     []sysinfo.Disk    `json:"disks"`
	Backup    *backup.Run       `json:"last_backup,omitempty"` // the last copy made (or failed)
	BackupOn  bool              `json:"backup_configured"`
	Problems  []sysProblem      `json:"problems"`
}

// sysProblem is a trouble found: a key of the locales and its argument.
type sysProblem struct {
	Key string `json:"code"`
	Arg string `json:"arg,omitempty"`
}

// SysDisk is a folder of go24 whose free space «Состояние системы» shows.
type SysDisk struct{ ID, Path string }

// backupAge is how old the last good copy may be before it is a trouble.
const backupAge = 48 * time.Hour

func (s *Server) sysReport(ctx context.Context) sysReport {
	rep := sysReport{Version: sysinfo.Version, Go: runtime.Version(), CheckedAt: time.Now(), Services: []sysinfo.Check{}, Problems: []sysProblem{}}
	add := func(key, arg string) { rep.Problems = append(rep.Problems, sysProblem{key, arg}) }
	if db, err := sysinfo.NewStore(s.DB).Database(ctx); err != nil {
		rep.DBError = err.Error()
		add("sys.p.db", err.Error())
	} else {
		rep.Database = &db
	}
	if nodes, err := sysinfo.NewStore(s.DB).Nodes(ctx); err == nil {
		rep.Nodes = nodes
	}
	background, versions := false, map[string]bool{}
	for _, n := range rep.Nodes {
		if n.Alive {
			background = background || n.Role != "web"
			versions[n.Version] = true
		}
	}
	if len(rep.Nodes) > 0 && !background {
		add("sys.p.worker", "")
	}
	if len(versions) > 1 {
		add("sys.p.versions", "")
	}
	rep.Services = sysinfo.Run(ctx, s.SysProbes, 5*time.Second)
	for _, c := range rep.Services {
		if !c.OK {
			add("sys.p.service", "sys.svc."+c.ID)
		}
	}
	for _, d := range s.SysDisks {
		disk := sysinfo.DiskOf(d.ID, d.Path)
		if disk.Low() {
			add("sys.p.disk", "sys.disk."+d.ID)
		}
		rep.Disks = append(rep.Disks, disk)
	}
	rep.BackupOn = s.Backup != nil
	if s.Backup == nil {
		add("sys.p.backup_off", "")
		return rep
	}
	runs, err := s.Backup.Runs(ctx, 50)
	if err != nil {
		return rep
	}
	var good *backup.Run
	for i, r := range runs {
		if r.Kind == "restore" || r.Status == "queued" || r.Status == "running" {
			continue
		}
		if rep.Backup == nil {
			rep.Backup = &runs[i]
		}
		if r.Status == "done" {
			good = &runs[i]
			break
		}
	}
	switch {
	case rep.Backup != nil && rep.Backup.Status == "failed":
		add("sys.p.backup_failed", "")
	case good == nil:
		add("sys.p.backup_none", "")
	case good.FinishedAt != nil && time.Since(*good.FinishedAt) > backupAge:
		add("sys.p.backup_old", "")
	}
	return rep
}

// systemPage is «Состояние системы» of «Настройки» (administrators), as «Проверка системы»; each opening
// checks anew.
func (s *Server) systemPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	rep := s.sysReport(r.Context())
	problems := make([]string, 0, len(rep.Problems))
	for _, p := range rep.Problems {
		switch p.Key {
		case "sys.p.service", "sys.p.disk":
			problems = append(problems, s.tr(r, p.Key, s.tr(r, p.Arg)))
		case "sys.p.db":
			problems = append(problems, s.tr(r, p.Key, p.Arg))
		default:
			problems = append(problems, s.tr(r, p.Key))
		}
	}
	slices.Sort(problems)
	s.render(w, r, "sysstate", page{TitleKey: "sys.title", Nav: "settings", Data: map[string]any{
		"R": rep, "Problems": slices.Compact(problems)}})
}

// apiSystem: GET /api/v1/settings/system — «Состояние системы» (administrators).
func (s *Server) apiSystem(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if !s.apiAdmin(r.Context(), p.UserID) {
		return i18n.E("err.forbidden")
	}
	writeJSON(w, http.StatusOK, s.sysReport(r.Context()))
	return nil
}
