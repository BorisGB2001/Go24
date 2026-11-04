package web

import (
	"archive/zip"
	"cmp"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/sign"
)

// ---------- «Сейф» of «Подпись для сотрудников»: the signed HR documents, everyone's to HR officers ----------

// signSafeFilter is the filter of «Сейф» of the query: emp, dept, from, to (YYYY-MM-DD, local days), q. HR officers and
// administrators see everyone's documents, others — their own only.
func (s *Server) signSafeFilter(r *http.Request, sess auth.Session) sign.SafeFilter {
	q := r.URL.Query()
	f := sign.SafeFilter{UserID: sess.UserID, All: s.signHRAllowed(r, sess), Q: strings.TrimSpace(q.Get("q"))}
	if f.All {
		f.Employee, f.DepartmentID = q.Get("emp"), q.Get("dept")
	}
	f.From, _ = time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local)
	if to, err := time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local); err == nil {
		f.To = to.AddDate(0, 0, 1)
	}
	return f
}

// signSafeQuery is the filter as the query of its page.
func signSafeQuery(f sign.SafeFilter) string {
	v := url.Values{}
	for k, x := range map[string]string{"emp": f.Employee, "dept": f.DepartmentID, "q": f.Q} {
		if x != "" {
			v.Set(k, x)
		}
	}
	if !f.From.IsZero() {
		v.Set("from", f.From.Format(time.DateOnly))
	}
	if !f.To.IsZero() {
		v.Set("to", f.To.AddDate(0, 0, -1).Format(time.DateOnly))
	}
	return v.Encode()
}

// signSafeData puts «Сейф» into the data of the page of «Подпись».
func (s *Server) signSafeData(r *http.Request, sess auth.Session, data map[string]any) error {
	f := s.signSafeFilter(r, sess)
	docs, err := s.Sign.Safe(r.Context(), f)
	if err != nil {
		return err
	}
	data["Safe"], data["SafeF"], data["SafeQuery"] = docs, f, signSafeQuery(f)
	if f.To.IsZero() {
		data["SafeTo"] = ""
	} else {
		data["SafeTo"] = f.To.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	if !f.All {
		return nil
	}
	if s.Company != nil {
		if _, data["Departments"], err = s.tree(r); err != nil {
			return err
		}
	}
	data["Users"], err = s.Tasks.ListUsers(r.Context())
	return err
}

// signSafeZip gives the signed documents of «Сейф» by the filter in a ZIP archive: «Title — Employee (date).pdf».
func (s *Server) signSafeZip(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.signSafeZipOf(w, r, s.signSafeFilter(r, sess))
}

func (s *Server) signSafeZipOf(w http.ResponseWriter, r *http.Request, f sign.SafeFilter) {
	docs, err := s.Sign.Safe(r.Context(), f)
	if err != nil {
		s.fail(w, "sign safe", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s",
		url.PathEscape(s.tr(r, "sign.safe.zip_name")+" "+time.Now().Format("2006-01-02")+".zip")))
	zw := zip.NewWriter(w)
	used := map[string]int{}
	for _, d := range docs {
		if d.ResultFile == "" {
			continue
		}
		m, err := s.Storages.FileMeta(r.Context(), d.ResultFile)
		if err != nil {
			s.Log.Error("sign safe file", "doc", d.ID, "err", err)
			continue
		}
		name := d.Title
		if e := d.Employee(); e != nil && !strings.Contains(name, e.Name) { // a sending's copies have it in the title
			name = e.Name + " — " + name
		}
		if d.DoneAt != nil {
			name += " (" + d.DoneAt.In(time.Local).Format("02.01.2006") + ")"
		}
		name = strings.NewReplacer("/", "_", `\`, "_", ":", "_").Replace(name)
		if n := used[name]; n > 0 {
			used[name]++
			name = fmt.Sprintf("%s %d", name, n+1)
		} else {
			used[name] = 1
		}
		ext := cmp.Or(path.Ext(m.Name), ".pdf")
		h := &zip.FileHeader{Name: name + ext, Method: zip.Deflate}
		if d.DoneAt != nil {
			h.Modified = *d.DoneAt
		}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return
		}
		obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
		if err != nil {
			s.Log.Error("sign safe open", "doc", d.ID, "err", err)
			continue
		}
		_, err = io.Copy(fw, obj)
		obj.Close()
		if err != nil {
			return
		}
	}
	zw.Close()
}

// signSafeSees: HR officers and administrators see the HR documents of everyone (as in «Сейф»).
func (s *Server) signSafeSees(r *http.Request, sess auth.Session, d sign.Doc) bool {
	return d.Employee() != nil && s.signHRAllowed(r, sess)
}

// apiSignSafe is «Сейф»: the HR documents signed by all — everyone's to HR officers and administrators, the key
// owner's own to others; ?emp= (the employee), dept= (with sub-departments), from=, to= (YYYY-MM-DD), q= (in the
// title); format=zip — the signed files in a ZIP archive.
func (s *Server) apiSignSafe(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	f := s.signSafeFilter(r, s.apiSess(r, p))
	if r.URL.Query().Get("format") == "zip" {
		s.signSafeZipOf(w, r, f)
		return nil
	}
	docs, err := s.Sign.Safe(r.Context(), f)
	if err != nil {
		return err
	}
	out := make([]apiSignDoc, 0, len(docs))
	for _, d := range docs {
		out = append(out, s.apiSignOf(d, p.UserID))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
