package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/bi"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «BI-аналитика»: keys for external BI and the export of the data sets, as «Подключение внешних BI» ----------

// biKeysPage: the keys of external BI (administrators) — issue one for some or all data sets, revoke, delete; the
// token is shown once, in the response that issues it, with the addresses to give Power BI, Looker Studio, DataLens.
func (s *Server) biKeysPage(w http.ResponseWriter, r *http.Request, sess auth.Session, token string, keyErr error) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.BI.Keys(r.Context())
	if err != nil {
		s.fail(w, "bi keys", err)
		return
	}
	sets, err := s.biDatasets(r.Context(), func(k string) string { return s.tr(r, k) })
	if err != nil {
		s.fail(w, "bi keys", err)
		return
	}
	names := map[string]string{}
	for _, d := range sets {
		names[d.Code] = d.Name
	}
	flash := ""
	if keyErr != nil {
		flash = s.I18n.Err(s.lang(r), keyErr)
	}
	s.render(w, r, "bikeys", page{TitleKey: "bi.keys.title", Nav: "bi", Flash: flash, Data: map[string]any{
		"Keys": list, "Datasets": sets, "Names": names, "NewToken": token,
		"BaseURL": s.BaseURL, "Periods": bi.Periods}})
}

func (s *Server) biKeys(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.biKeysPage(w, r, sess, "", nil)
}

func (s *Server) biKeyCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.biKeysPage(w, r, sess, "", i18n.E("err.bad_form"))
		return
	}
	token, err := s.BI.CreateKey(r.Context(), sess.UserID, r.FormValue("name"), r.Form["datasets"]) // none — all
	if err != nil {
		s.biKeysPage(w, r, sess, "", err)
		return
	}
	s.audit(r, audit.Event{Kind: "bi_key_created", UserID: sess.UserID, Target: r.FormValue("name")})
	w.Header().Set("Cache-Control", "no-store")
	s.biKeysPage(w, r, sess, token, nil)
}

// biKeyAct: POST /bi/keys/{id}/{revoke|delete}.
func (s *Server) biKeyAct(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	id, act := r.PathValue("id"), r.PathValue("act")
	var name string
	_ = s.DB.QueryRow(r.Context(), `SELECT name FROM bi_keys WHERE id::text = $1`, id).Scan(&name)
	var err error
	switch act {
	case "revoke":
		err = s.BI.RevokeKey(r.Context(), id)
	case "delete":
		err = s.BI.DeleteKey(r.Context(), id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "bi key", err)
		return
	}
	if name != "" {
		s.audit(r, audit.Event{Kind: "bi_key_" + map[string]string{"revoke": "revoked", "delete": "deleted"}[act], UserID: sess.UserID, Target: name})
	}
	http.Redirect(w, r, "/bi/keys", http.StatusSeeOther)
}

// biExportAuth authenticates an export by the token of the Authorization header or of ?key=: a key of external BI
// reads its data sets with its author's rights in admin mode while they stay an administrator; a personal API key
// with bi:read reads with its owner's (and with X-Go24-Admin: 1, as the other API, in admin mode).
func (s *Server) biExportAuth(r *http.Request) (context.Context, string, *bi.Key, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		token = r.URL.Query().Get("key")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, "", nil, errNoAuth
	}
	ctx := r.Context()
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	bad := func(err error) (context.Context, string, *bi.Key, error) {
		if err == bi.ErrKeyInvalid || err == apikeys.ErrInvalid {
			s.audit(r, audit.Event{Kind: "api_bad_key", Target: r.Method + " " + r.URL.Path})
			err = apikeys.ErrInvalid
		}
		return nil, "", nil, err
	}
	if strings.HasPrefix(token, "go24bi_") {
		k, err := s.BI.Authenticate(ctx, token, ip)
		if err != nil {
			return bad(err)
		}
		if !s.apiAdmin(ctx, k.CreatedBy) { // the author is no longer an administrator: the key reads nothing
			return bad(apikeys.ErrInvalid)
		}
		return tasks.WithAdmin(tasks.WithViewer(ctx, k.CreatedBy, false)), k.CreatedBy, &k, nil
	}
	p, err := s.Keys.Authenticate(ctx, token, ip)
	if err != nil {
		return bad(err)
	}
	if !p.Can("bi:read") {
		return bad(i18n.E("err.api_no_scope", "bi:read"))
	}
	if guest, err := s.Tasks.IsGuest(ctx, p.UserID); err != nil || guest {
		return bad(tasks.ErrForbidden)
	}
	ctx = tasks.WithViewer(ctx, p.UserID, false)
	if r.Header.Get("X-Go24-Admin") == "1" {
		if !s.apiAdmin(ctx, p.UserID) {
			return bad(tasks.ErrForbidden)
		}
		ctx = tasks.WithAdmin(ctx)
	}
	return ctx, p.UserID, nil, nil
}

// biExportTr translates an export into ?lang=, else into the language of the portal.
func (s *Server) biExportTr(r *http.Request) func(string) string {
	lang := s.lang(r)
	if l := r.URL.Query().Get("lang"); l != "" {
		lang = s.I18n.Match(l)
	}
	return func(k string) string { return s.I18n.T(lang, k) }
}

// apiBIExportSets: GET /api/v1/bi/export — the data sets the key reads, their fields and the addresses of their export.
func (s *Server) apiBIExportSets(w http.ResponseWriter, r *http.Request) {
	_, _, k, err := s.biExportAuth(r)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	type set struct {
		biDataset
		JSON string `json:"json"`
		CSV  string `json:"csv"`
	}
	sets, err := s.biDatasets(r.Context(), s.biExportTr(r))
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	out := []set{}
	for _, d := range sets {
		if k == nil || k.Reads(d.Code) {
			u := s.BaseURL + "/api/v1/bi/export/" + d.Code
			out = append(out, set{d, u, u + "?format=csv"})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiBIExport: GET /api/v1/bi/export/{dataset} — the rows of a data set for an external BI: JSON (an array of
// objects) or ?format=csv; ?fields=a,b (all by default; the row's id comes first); the period ?period= (that of the
// dashboards) or ?from=&to= (days, the last counted whole) on ?date_field= (the set's own); ?filter=field:op:value,
// repeated (in — values by «|»); ?limit= (up to 50000) and ?offset=, the next page's in X-Next-Offset; ?names=1 —
// the columns by their names in ?lang=.
func (s *Server) apiBIExport(w http.ResponseWriter, r *http.Request) {
	ctx, viewer, k, err := s.biExportAuth(r)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	q := r.URL.Query()
	ds := r.PathValue("dataset")
	if k != nil && !k.Reads(ds) {
		s.apiFail(w, r, tasks.ErrForbidden)
		return
	}
	e := bi.Export{Dataset: ds, DateField: q.Get("date_field")}
	for _, f := range strings.Split(q.Get("fields"), ",") {
		if f = strings.TrimSpace(f); f != "" {
			e.Fields = append(e.Fields, f)
		}
	}
	for _, f := range q["filter"] {
		parts := strings.SplitN(f, ":", 3)
		if len(parts) < 2 {
			s.apiFail(w, r, i18n.E("bi.err.op"))
			return
		}
		fl := bi.Filter{Field: parts[0], Op: parts[1]}
		if len(parts) == 3 {
			fl.Value = parts[2]
			if fl.Op == "in" {
				fl.Values = strings.Split(parts[2], "|")
			}
		}
		e.Filters = append(e.Filters, fl)
	}
	period := q.Get("period")
	if period == "" && (q.Get("from") != "" || q.Get("to") != "") {
		period = "range"
	}
	e.From, e.To = bi.Span(period, biDate(q.Get("from")), biDate(q.Get("to")), time.Now())
	e.Limit, _ = strconv.Atoi(q.Get("limit"))
	e.Offset, _ = strconv.Atoi(q.Get("offset"))
	tr := s.biTr(r, ds, s.biExportTr(r))
	res, err := s.BI.Rows(ctx, viewer, e, tr)
	if err != nil {
		s.apiFail(w, r, err)
		return
	}
	limit := e.Limit
	if limit <= 0 || limit > bi.MaxExport {
		limit = bi.MaxExport
	}
	if len(res.Rows) == limit {
		w.Header().Set("X-Next-Offset", strconv.Itoa(max(e.Offset, 0)+limit))
	}
	names := make([]string, len(res.Columns))
	for i, c := range res.Columns {
		names[i] = c.Field
		if q.Get("names") == "1" {
			names[i] = tr("bi.f." + c.Field)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+ds+`.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write(names)
		for _, row := range res.Rows {
			rec := make([]string, len(row))
			for i, v := range row {
				switch v := v.(type) {
				case nil:
				case string:
					rec[i] = v
				case float64:
					rec[i] = strconv.FormatFloat(v, 'f', -1, 64)
				case bool:
					rec[i] = strconv.FormatBool(v)
				default:
					b, _ := json.Marshal(v)
					rec[i] = string(b)
				}
			}
			_ = cw.Write(rec)
		}
		cw.Flush()
		return
	}
	out := make([]map[string]any, len(res.Rows))
	for i, row := range res.Rows {
		m := make(map[string]any, len(row))
		for j, v := range row {
			m[names[j]] = v
		}
		out[i] = m
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(out)
}
