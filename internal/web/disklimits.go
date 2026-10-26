package web

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/i18n"
)

// ---------- «Диск: место»: the limits of the drives and how much each one holds (administrators) ----------

const gb = 1 << 30

// gbText is a number of bytes as gigabytes for a form ("" — no limit).
func gbText(b int64) string {
	if b <= 0 {
		return ""
	}
	return strconv.FormatFloat(float64(b)/gb, 'f', -1, 64)
}

// parseGB reads gigabytes of a form ("" or 0 — no limit).
func parseGB(v string) (int64, error) {
	v = strings.TrimSpace(strings.ReplaceAll(v, ",", "."))
	if v == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1<<20 || math.IsNaN(f) {
		return 0, i18n.E("disk.limit_bad")
	}
	return int64(math.Round(f * gb)), nil
}

// versionsText is how many former versions of a file are kept, for a form ("" — all of them).
func versionsText(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// parseVersions reads how many former versions of a file are kept ("" or 0 — all of them).
func parseVersions(v string) (int, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 10000 {
		return 0, i18n.E("disk.keep_versions_bad")
	}
	return n, nil
}

func (s *Server) diskLimitsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	l, err := s.Drive.Limits(r.Context())
	if err != nil {
		s.fail(w, "drive limits", err)
		return
	}
	list, err := s.Drive.Usages(r.Context())
	if err != nil {
		s.fail(w, "drive usages", err)
		return
	}
	rows := make([]map[string]any, 0, len(list))
	var total int64
	for _, u := range list {
		total += u.Used
		rows = append(rows, map[string]any{"D": u, "Usage": drive.Usage{Used: u.Used, Limit: l.Of(drive.Drive{Kind: u.Kind})}})
	}
	s.render(w, r, "disklimits", page{TitleKey: "disk.limits", Nav: "disklimits", Data: map[string]any{"Limits": l, "Rows": rows, "Total": total,
		"User": gbText(l.User), "Company": gbText(l.Company), "Project": gbText(l.Project), "Versions": versionsText(l.Versions)}})
}

func (s *Server) saveDiskLimits(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	l, err := s.Drive.Limits(r.Context()) // the switches stay as they are unless the form has them
	if err != nil {
		s.fail(w, "drive limits", err)
		return
	}
	if r.FormValue("features") != "" {
		l.NoLinks, l.NoLocks, l.NoContent = r.FormValue("links") == "", r.FormValue("locks") == "", r.FormValue("content") == ""
	}
	for _, f := range []struct {
		name string
		to   *int64
	}{{"user", &l.User}, {"company", &l.Company}, {"project", &l.Project}} {
		if err == nil {
			*f.to, err = parseGB(r.FormValue(f.name))
		}
	}
	if err == nil {
		l.Versions, err = parseVersions(r.FormValue("versions"))
	}
	if err == nil {
		err = s.Drive.SaveLimits(r.Context(), l)
	}
	if err != nil {
		s.taskErr(w, r, err, "/settings/disk")
		return
	}
	http.Redirect(w, r, "/settings/disk", http.StatusSeeOther)
}
