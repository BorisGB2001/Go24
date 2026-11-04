package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/apikeys"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST compatible with the portal (GO24DEV-760): the integrations written for the portal (the n8n node, 1С, Zapier,
// CRest, own scripts) call methods by name — <go24>/rest/<employee>/<key>/crm.deal.list.json (an incoming webhook)
// or <go24>/rest/crm.deal.list?auth=<token> (an application) — with the parameters in the query, a form of PHP
// arrays (filter[>ID]=5&select[]=TITLE) or JSON, and read {"result", "next", "total", "time"} or {"error",
// "error_description"}. The methods are the table portalMethods; the sections add theirs to it.

const portalPage = 50 // a list gives 50 records, «next» is where the next page starts

// portalOut is what a method gives: the result, and for a list — its total and where the next page starts.
type portalOut struct {
	Result any
	Total  int
	Next   int // 0 — the last page
	List   bool
}

type portalCall struct {
	r      *http.Request
	p      apikeys.Principal
	in     map[string]any
	method string // the method's name, in lower case
}

type portalMethod struct {
	Scope string // the key's scope the method needs ("" — any key)
	Run   func(s *Server, c portalCall) (portalOut, error)
}

// portalMethods: the name of the method → its go24 implementation.
var portalMethods = map[string]portalMethod{}

func init() {
	for name, m := range map[string]portalMethod{
		"server.time":  {Run: (*Server).portalServerTime},
		"profile":      {Run: (*Server).portalProfile},
		"user.current": {Run: (*Server).portalUserCurrent},
		"user.get":     {Scope: "company:read", Run: (*Server).portalUserGet},
		"scope":        {Run: (*Server).portalScope},
		"methods":      {Run: (*Server).portalMethodList},
		"method.get":   {Run: (*Server).portalMethodGet},
		"app.info":     {Run: (*Server).portalAppInfo},
	} {
		portalMethods[name] = m
	}
}

// portalScopes: a go24 scope → the scope a method of it belongs to.
var portalScopes = map[string]string{
	"projects": "sonet_group", "tasks": "task", "im": "im", "crm": "crm", "bp": "bizproc", "lists": "lists",
	"mail": "mailservice", "collabs": "socialnetwork", "calendar": "calendar", "disk": "disk", "feed": "log",
	"documents": "documentgenerator", "bi": "biconnector", "openlines": "imopenlines", "kb": "landing",
	"booking": "booking", "company": "department", "telephony": "telephony", "placement": "placement", "imbot": "imbot",
	"settings": "rpa",
}

// portalScopeOf is the portal scope of a method: «user» for the methods any key may call.
func portalScopeOf(m portalMethod) string {
	if m.Scope == "" {
		return "user"
	}
	sc, _, _ := strings.Cut(m.Scope, ":")
	return portalScopes[sc]
}

// portalError is an answer in the error format.
type portalError struct {
	Status int
	Code   string
	Desc   string
}

func (e *portalError) Error() string { return e.Code + ": " + e.Desc }

// portalFail answers an error; the go24 errors get the codes integrations check.
func (s *Server) portalFail(w http.ResponseWriter, r *http.Request, err error) {
	var be *portalError
	if !errors.As(err, &be) {
		be = s.portalErrorOf(r, err)
	}
	writeJSON(w, be.Status, map[string]string{"error": be.Code, "error_description": be.Desc})
}

func (s *Server) portalErrorOf(r *http.Request, err error) *portalError {
	var be *portalError
	if errors.As(err, &be) {
		return be
	}
	lang := s.I18n.Match(r.Header.Get("Accept-Language"))
	var ie *i18n.Error
	if !errors.As(err, &ie) {
		s.Log.Error("rest portal", "err", err)
		ie = i18n.E("err.internal")
	}
	e := &portalError{Status: http.StatusBadRequest, Code: "ERROR_CORE", Desc: s.I18n.Err(lang, ie)}
	switch {
	case errors.Is(err, apikeys.ErrInvalid), errors.Is(err, errNoAuth):
		e.Status, e.Code = http.StatusUnauthorized, "NO_AUTH_FOUND"
	case ie.Key == "err.api_no_scope":
		e.Status, e.Code = http.StatusUnauthorized, "insufficient_scope"
	case errors.Is(err, tasks.ErrForbidden), ie.Key == "err.forbidden", ie.Key == "ipaccess.denied":
		e.Status, e.Code = http.StatusForbidden, "ACCESS_DENIED"
	case errors.Is(err, tasks.ErrNotFound), ie.Key == "err.not_found":
		e.Code = "ERROR_NOT_FOUND"
	case ie.Key == "err.internal":
		e.Status, e.Code = http.StatusInternalServerError, "INTERNAL_SERVER_ERROR"
	}
	return e
}

func (s *Server) portalErr(r *http.Request, status int, code, key string, args ...any) *portalError {
	return &portalError{Status: status, Code: code, Desc: s.I18n.Err(s.I18n.Match(r.Header.Get("Accept-Language")), i18n.E(key, args...))}
}

// isPortalMethod: the path of an incoming webhook is a method, not a go24 REST path (crm/deals).
func isPortalMethod(path string) bool {
	if _, ok := portalMethods[path]; ok {
		return true
	}
	return path == "batch" || (strings.Contains(path, ".") && !strings.Contains(path, "/"))
}

// portalApp: POST|GET /rest/<method>(.json) — a call of an application: the token in «auth» or «Authorization: Bearer».
func (s *Server) portalApp(w http.ResponseWriter, r *http.Request) {
	in, err := portalParams(r)
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	token, _ := in["auth"].(string)
	if token == "" {
		token, _ = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if token = strings.TrimSpace(token); token == "" {
		s.portalFail(w, r, errNoAuth)
		return
	}
	s.portalServe(w, r, strings.TrimSuffix(r.PathValue("method"), ".json"), token, in)
}

// portalServe runs a method — or «batch» — for the token's owner and writes the answer.
func (s *Server) portalServe(w http.ResponseWriter, r *http.Request, method, token string, in map[string]any) {
	start := time.Now()
	if in == nil {
		var err error
		if in, err = portalParams(r); err != nil {
			s.portalFail(w, r, err)
			return
		}
	}
	delete(in, "auth")
	r, p, err := s.apiEnter(r, token, "")
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	var out portalOut
	if strings.ToLower(method) == "batch" {
		out, err = s.portalBatch(r, p, in)
	} else {
		out, err = s.portalRun(r, p, method, in)
	}
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	body := map[string]any{"result": out.Result, "time": portalTime(start)}
	if out.List {
		body["total"] = out.Total
		if out.Next > 0 {
			body["next"] = out.Next
		}
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) portalRun(r *http.Request, p apikeys.Principal, method string, in map[string]any) (portalOut, error) {
	m, ok := portalMethods[strings.ToLower(method)]
	if !ok {
		return portalOut{}, s.portalErr(r, http.StatusNotFound, "ERROR_METHOD_NOT_FOUND", "err.portal_method", method)
	}
	if m.Scope != "" && !p.Can(m.Scope) {
		return portalOut{}, i18n.E("err.api_no_scope", m.Scope)
	}
	if sec, _, _ := strings.Cut(m.Scope, ":"); sec != "" { // «Инструменты»: the section is switched off
		if key, off := s.offSection(r.Context(), "/"+sec); off {
			return portalOut{}, &portalError{Status: http.StatusNotFound, Code: "ERROR_METHOD_NOT_FOUND", Desc: s.tr(r, "sections.off", s.tr(r, key))}
		}
	}
	return m.Run(s, portalCall{r: r, p: p, in: in, method: strings.ToLower(method)})
}

// portalTime is the «time» block of every answer.
func portalTime(start time.Time) map[string]any {
	end := time.Now()
	sec := func(t time.Time) float64 { return float64(t.UnixMicro()) / 1e6 }
	return map[string]any{"start": sec(start), "finish": sec(end), "duration": end.Sub(start).Seconds(),
		"processing": end.Sub(start).Seconds(), "date_start": start.Format(time.RFC3339), "date_finish": end.Format(time.RFC3339),
		"operating": 0}
}

// ---------- parameters ----------

// portalParams reads the parameters of a call: the query, then a form or JSON in the body.
func portalParams(r *http.Request) (map[string]any, error) {
	in := phpParams(r.URL.Query())
	if r.Body == nil || r.Method == http.MethodGet {
		return in, nil
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	body := http.MaxBytesReader(nil, r.Body, 8<<20)
	switch ct {
	case "application/json":
		var j any
		dec := json.NewDecoder(body)
		dec.UseNumber()
		if err := dec.Decode(&j); err != nil && !errors.Is(err, io.EOF) {
			return nil, i18n.E("err.bad_json", err.Error())
		}
		switch x := j.(type) {
		case map[string]any:
			for k, v := range x {
				in[k] = v
			}
		case []any: // the old methods take their parameters by place: [taskId, {fields}]
			for i, v := range x {
				in[strconv.Itoa(i)] = v
			}
		}
	case "multipart/form-data":
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			return nil, i18n.E("err.bad_form")
		}
		for k, v := range phpParams(url.Values(r.MultipartForm.Value)) {
			in[k] = v
		}
	default:
		raw, err := io.ReadAll(body)
		if err != nil {
			return nil, i18n.E("err.bad_form")
		}
		vals, err := url.ParseQuery(string(raw))
		if err != nil {
			return nil, i18n.E("err.bad_form")
		}
		for k, v := range phpParams(vals) {
			in[k] = v
		}
	}
	return in, nil
}

// phpParams turns PHP arrays of a query or a form into maps and lists, as PHP reads them: filter[>ID]=5 →
// {"filter": {">ID": "5"}}, select[]=ID&select[]=TITLE → {"select": ["ID", "TITLE"]}.
func phpParams(v url.Values) map[string]any {
	out := map[string]any{}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		path := phpPath(k)
		for _, val := range v[k] {
			phpSet(out, path, val)
		}
	}
	for k, x := range out { // the top stays a map: 0=5&1[TITLE]=… are parameters by place
		out[k] = listify(x)
	}
	return out
}

// phpPath: a[b][][c] → a, b, "", c; a broken key stays whole.
func phpPath(k string) []string {
	i := strings.IndexByte(k, '[')
	if i <= 0 || !strings.HasSuffix(k, "]") {
		return []string{k}
	}
	path := []string{k[:i]}
	for rest := k[i:]; rest != ""; {
		if rest[0] != '[' {
			return []string{k}
		}
		j := strings.IndexByte(rest, ']')
		path = append(path, rest[1:j])
		rest = rest[j+1:]
	}
	return path
}

func phpSet(m map[string]any, path []string, val string) {
	for i, p := range path {
		if p == "" {
			p = strconv.Itoa(len(m))
		}
		if i == len(path)-1 {
			m[p] = val
			return
		}
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
}

// listify turns the maps keyed 0, 1, 2… into lists.
func listify(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for k, x := range m {
		m[k] = listify(x)
	}
	if len(m) == 0 {
		return m
	}
	list := make([]any, len(m))
	for k, x := range m {
		n, err := strconv.Atoi(k)
		if err != nil || n < 0 || n >= len(m) || strconv.Itoa(n) != k {
			return m
		}
		list[n] = x
	}
	return list
}

// portalStr is a scalar parameter as text.
func portalStr(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "Y"
		}
		return "N"
	default:
		return fmt.Sprint(x)
	}
}

// portalMap is a parameter that is a map (filter, fields); a list of pairs reads as nothing.
func portalMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// portalList is a parameter that is a list (select[]); one value — a list of one.
func portalList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, y := range x {
			out = append(out, portalStr(y))
		}
	case map[string]any:
		for _, y := range x {
			out = append(out, portalStr(y))
		}
	case nil:
	default:
		out = append(out, portalStr(x))
	}
	return out
}

// portalStart is «start» of a list: where the page begins; -1 (no count) reads as 0.
func portalStart(in map[string]any) int {
	n, _ := strconv.Atoi(portalStr(in["start"]))
	return max(n, 0)
}

// portalPaged cuts a page out of a list of `total` and says where the next one starts.
func portalPaged(result any, start, got, total int) portalOut {
	out := portalOut{Result: result, Total: total, List: true}
	if start+got < total {
		out.Next = start + portalPage
	}
	return out
}

// ---------- batch ----------

var portalRef = regexp.MustCompile(`\$result\[([^\]]+)\]((?:\[[^\]]*\])*)`)

// portalBatch runs up to 50 commands cmd[<name>]=<method>?<query> in one call; $result[<name>][FIELD]… in a command
// is a value of an earlier command's result. The commands run in the order of their names, each after the commands it
// refers to; halt=1 stops at the first error.
func (s *Server) portalBatch(r *http.Request, p apikeys.Principal, in map[string]any) (portalOut, error) {
	cmds := map[string]string{}
	switch c := in["cmd"].(type) {
	case map[string]any:
		for k, v := range c {
			cmds[k] = portalStr(v)
		}
	case []any:
		for i, v := range c {
			cmds[strconv.Itoa(i)] = portalStr(v)
		}
	}
	if len(cmds) > 50 {
		return portalOut{}, s.portalErr(r, http.StatusBadRequest, "ERROR_BATCH_LENGTH_EXCEEDED", "err.portal_batch_length", 50)
	}
	halt := portalStr(in["halt"]) == "1" || portalStr(in["halt"]) == "true" || portalStr(in["halt"]) == "Y"
	results, errs, totals, nexts, times := map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}
	names := make([]string, 0, len(cmds))
	for k := range cmds {
		names = append(names, k)
	}
	sort.Strings(names)
	done := map[string]bool{}
	for len(done) < len(names) {
		progress := false
		for _, name := range names {
			if done[name] || !portalRefsReady(cmds[name], done, cmds) {
				continue
			}
			done[name], progress = true, true
			start := time.Now()
			out, err := s.portalCommand(r, p, portalFill(cmds[name], results))
			times[name] = portalTime(start)
			if err != nil {
				be := s.portalErrorOf(r, err)
				errs[name] = map[string]string{"error": be.Code, "error_description": be.Desc}
				if halt {
					return portalBatchOut(results, errs, totals, nexts, times), nil
				}
				continue
			}
			results[name] = out.Result
			if out.List {
				totals[name] = out.Total
				if out.Next > 0 {
					nexts[name] = out.Next
				}
			}
		}
		if !progress { // the rest refer to each other or to what is not there
			for _, name := range names {
				if !done[name] {
					done[name] = true
					be := s.portalErr(r, http.StatusBadRequest, "ERROR_BATCH_REFERENCE", "err.portal_batch_ref", name)
					errs[name] = map[string]string{"error": be.Code, "error_description": be.Desc}
				}
			}
		}
	}
	return portalBatchOut(results, errs, totals, nexts, times), nil
}

func portalBatchOut(results, errs, totals, nexts, times map[string]any) portalOut {
	// an empty part is [] (an empty PHP array), and the integrations expect that
	part := func(m map[string]any) any {
		if len(m) == 0 {
			return []any{}
		}
		return m
	}
	return portalOut{Result: map[string]any{"result": part(results), "result_error": part(errs), "result_total": part(totals),
		"result_next": part(nexts), "result_time": part(times)}}
}

// portalRefsReady: every command this one refers to has run (a reference to a missing command never will).
func portalRefsReady(cmd string, done map[string]bool, cmds map[string]string) bool {
	for _, m := range portalRef.FindAllStringSubmatch(cmd, -1) {
		if _, ok := cmds[m[1]]; !ok || !done[m[1]] {
			return false
		}
	}
	return true
}

// portalFill puts the values of earlier results in place of $result[…]; what is not there becomes empty.
func portalFill(cmd string, results map[string]any) string {
	return portalRef.ReplaceAllStringFunc(cmd, func(ref string) string {
		m := portalRef.FindStringSubmatch(ref)
		v := results[m[1]]
		for _, k := range phpPath("x" + m[2])[1:] {
			switch x := v.(type) {
			case map[string]any:
				v = x[k]
			case []any:
				n, err := strconv.Atoi(k)
				if err != nil || n < 0 || n >= len(x) {
					return ""
				}
				v = x[n]
			default:
				// a struct answer (user.current): read it as JSON
				var j any
				if raw, err := json.Marshal(x); err == nil && json.Unmarshal(raw, &j) == nil {
					if jm, ok := j.(map[string]any); ok {
						v = jm[k]
						continue
					}
				}
				return ""
			}
		}
		return url.QueryEscape(portalStr(v))
	})
}

// portalCommand runs one command of a batch: «method?query».
func (s *Server) portalCommand(r *http.Request, p apikeys.Principal, cmd string) (portalOut, error) {
	method, query, _ := strings.Cut(cmd, "?")
	method = strings.TrimSuffix(strings.TrimSpace(method), ".json")
	if strings.EqualFold(method, "batch") {
		return portalOut{}, s.portalErr(r, http.StatusBadRequest, "ERROR_BATCH_METHOD_NOT_ALLOWED", "err.portal_batch_nested")
	}
	vals, err := url.ParseQuery(query)
	if err != nil {
		return portalOut{}, i18n.E("err.bad_form")
	}
	return s.portalRun(r, p, method, phpParams(vals))
}

// ---------- methods: the core ----------

func (s *Server) portalServerTime(c portalCall) (portalOut, error) {
	return portalOut{Result: time.Now().Format(time.RFC3339)}, nil
}

// portalUsers are the numbers of employees and departments for go24's uuid.
func (s *Server) portalUserNum(c portalCall, id string) (int64, error) {
	var n int64
	err := s.DB.QueryRow(c.r.Context(), `SELECT num FROM users WHERE id::text = $1 UNION ALL SELECT num FROM im_bots WHERE id::text = $1 LIMIT 1`,
		id).Scan(&n) // a bot's number is of the employees' row
	return n, err
}

func (s *Server) portalDeptNums(c portalCall, refs []company.DeptRef) ([]int64, error) {
	ids := make([]string, 0, len(refs))
	for _, d := range refs {
		ids = append(ids, d.ID)
	}
	out := []int64{}
	rows, err := s.DB.Query(c.r.Context(), `SELECT num FROM departments WHERE id = ANY($1::uuid[]) ORDER BY num`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// restTZ is the employee's time zone and its offset in seconds, as «profile» gives them.
func restTZ(tz string) (string, int) {
	loc := time.Local
	if l, err := time.LoadLocation(tz); tz != "" && err == nil {
		loc = l
	}
	_, off := time.Now().In(loc).Zone()
	return loc.String(), off
}

func (s *Server) portalPhoto(prof company.Profile) string {
	if prof.PhotoKey == "" {
		return ""
	}
	return strings.TrimRight(s.BaseURL, "/") + company.PhotoURL(prof.ID, prof.PhotoKey)
}

// profile: the basic data of the caller.
func (s *Server) portalProfile(c portalCall) (portalOut, error) {
	prof, err := s.Company.Profile(c.r.Context(), c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	num, err := s.portalUserNum(c, prof.ID)
	if err != nil {
		return portalOut{}, err
	}
	tz, off := restTZ(prof.TimeZone)
	first, last := prof.FirstName, prof.LastName
	if first == "" && last == "" {
		first = prof.EditFirst()
		last = prof.EditLast()
	}
	return portalOut{Result: map[string]any{"ID": strconv.FormatInt(num, 10), "ADMIN": s.apiAdmin(c.r.Context(), prof.ID),
		"NAME": first, "LAST_NAME": last, "PERSONAL_GENDER": prof.Gender, "PERSONAL_PHOTO": s.portalPhoto(prof),
		"TIME_ZONE": tz, "TIME_ZONE_OFFSET": off}}, nil
}

// portalUser is an employee in the fields user.get, with the user fields of profiles (UF_USR_*).
func (s *Server) portalUser(c portalCall, prof company.Profile, num int64, ufs []portalUF) (map[string]any, error) {
	depts, err := s.portalDeptNums(c, prof.Departments)
	if err != nil {
		return nil, err
	}
	values, err := s.Tasks.FieldValuesOf(c.r.Context(), tasks.UserFieldSet, prof.ID)
	if err != nil {
		return nil, err
	}
	yn := map[bool]string{true: "Y", false: "N"}
	date := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format(time.RFC3339)
	}
	first, last := prof.FirstName, prof.LastName
	if first == "" && last == "" {
		first, last = prof.EditFirst(), prof.EditLast()
	}
	kind := "employee"
	if prof.Guest {
		kind = "extranet"
	}
	tz, _ := restTZ(prof.TimeZone)
	u := map[string]any{"ID": strconv.FormatInt(num, 10), "XML_ID": prof.ID, "ACTIVE": prof.Active, "NAME": first,
		"LAST_NAME": last, "SECOND_NAME": prof.SecondName, "EMAIL": prof.Email, "LAST_LOGIN": "",
		"TIME_ZONE": tz, "PERSONAL_GENDER": prof.Gender, "PERSONAL_PHOTO": s.portalPhoto(prof), "PERSONAL_MOBILE": prof.MobilePhone,
		"PERSONAL_CITY": prof.City, "PERSONAL_WWW": prof.Website, "PERSONAL_BIRTHDAY": "", "WORK_PHONE": prof.WorkPhone,
		"WORK_POSITION": prof.Position, "UF_PHONE_INNER": prof.InnerPhone, "UF_DEPARTMENT": depts,
		"UF_EMPLOYMENT_DATE": date(prof.Employment), "IS_ONLINE": yn[!prof.LastLogin.IsZero() && time.Since(prof.LastLogin) < 15*time.Minute],
		"USER_TYPE": kind}
	if !prof.LastLogin.IsZero() {
		u["LAST_LOGIN"] = prof.LastLogin.Format(time.RFC3339)
	}
	if prof.Birthday != nil && (prof.BirthdayYear || prof.ID == c.p.UserID) {
		u["PERSONAL_BIRTHDAY"] = prof.Birthday.Format(time.RFC3339)
	}
	for _, f := range ufs {
		if i := slices.IndexFunc(values, func(v tasks.FieldValue) bool { return v.ID == f.ID }); i >= 0 {
			u[f.Key()] = portalUFValueOut(f, values[i].Value)
		}
	}
	return u, nil
}

// user.current: the caller as user.get gives an employee.
func (s *Server) portalUserCurrent(c portalCall) (portalOut, error) {
	prof, err := s.Company.Profile(c.r.Context(), c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	num, err := s.portalUserNum(c, prof.ID)
	if err != nil {
		return portalOut{}, err
	}
	ufs, err := s.portalUFs(c.r.Context(), "user", crm.FieldSight{})
	if err != nil {
		return portalOut{}, err
	}
	u, err := s.portalUser(c, prof, num, ufs)
	return portalOut{Result: u}, err
}

// user.get: employees by FILTER (ID, ACTIVE, EMAIL, NAME, LAST_NAME, UF_DEPARTMENT, USER_TYPE; ID also at the top),
// sorted by SORT/ORDER, a page of 50 from «start». Guests of collabs — for administrators.
func (s *Server) portalUserGet(c portalCall) (portalOut, error) {
	filter := portalMap(c.in["FILTER"])
	if filter == nil {
		filter = portalMap(c.in["filter"])
	}
	if filter == nil {
		filter = map[string]any{}
	}
	if id, ok := c.in["ID"]; ok {
		filter["ID"] = id
	}
	return s.portalUsers(c, filter)
}

// portalUsers is user.get and user.search: employees by the filter, a page of 50.
func (s *Server) portalUsers(c portalCall, filter map[string]any) (portalOut, error) {
	where, args := []string{"true"}, []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if !s.apiAdmin(c.r.Context(), c.p.UserID) {
		where = append(where, "(NOT u.guest OR u.id = "+arg(c.p.UserID)+")")
	}
	for k, v := range filter {
		key := strings.ToUpper(strings.TrimLeft(k, "=!<>%"))
		vals := portalList(v)
		switch key {
		case "ID":
			var nums []int64
			for _, x := range vals {
				if n, err := strconv.ParseInt(x, 10, 64); err == nil {
					nums = append(nums, n)
				}
			}
			where = append(where, "u.num = ANY("+arg(nums)+"::bigint[])")
		case "ACTIVE":
			where = append(where, "u.active = "+arg(len(vals) > 0 && (vals[0] == "Y" || vals[0] == "true" || vals[0] == "1")))
		case "EMAIL":
			where = append(where, "lower(u.email) = ANY("+arg(lowerAll(vals))+"::text[])")
		case "NAME", "LAST_NAME":
			col := map[string]string{"NAME": "u.first_name", "LAST_NAME": "u.last_name"}[key]
			where = append(where, "("+col+" ILIKE "+arg(strings.Join(vals, ""))+" OR u.name ILIKE '%' || "+arg(strings.Join(vals, ""))+" || '%')")
		case "UF_DEPARTMENT":
			where = append(where, "EXISTS (SELECT 1 FROM user_departments ud JOIN departments d ON d.id = ud.department_id"+
				" WHERE ud.user_id = u.id AND d.num::text = ANY("+arg(vals)+"::text[]))")
		case "USER_TYPE":
			guest := len(vals) > 0 && vals[0] == "extranet"
			where = append(where, "u.guest = "+arg(guest))
		case "FIND", "NAME_SEARCH": // user.search: any part of the name, e-mail or position
			for _, w := range strings.Fields(strings.Join(vals, " ")) {
				a := arg("%" + w + "%")
				where = append(where, "(u.name ILIKE "+a+" OR u.first_name ILIKE "+a+" OR u.last_name ILIKE "+a+
					" OR u.second_name ILIKE "+a+" OR u.email ILIKE "+a+" OR u.position ILIKE "+a+")")
			}
		case "WORK_POSITION", "PERSONAL_CITY":
			col := map[string]string{"WORK_POSITION": "u.position", "PERSONAL_CITY": "u.city"}[key]
			where = append(where, col+" ILIKE '%' || "+arg(strings.Join(vals, ""))+" || '%'")
		case "UF_PHONE_INNER":
			where = append(where, "u.inner_phone = ANY("+arg(vals)+"::text[])")
		case "UF_DEPARTMENT_NAME":
			where = append(where, "EXISTS (SELECT 1 FROM user_departments ud JOIN departments d ON d.id = ud.department_id"+
				" WHERE ud.user_id = u.id AND d.name ILIKE '%' || "+arg(strings.Join(vals, ""))+" || '%')")
		}
	}
	order := "u.num"
	sortBy := strings.ToUpper(portalStr(c.in["SORT"]) + portalStr(c.in["sort"]))
	desc := strings.EqualFold(portalStr(c.in["ORDER"])+portalStr(c.in["order"]), "desc")
	if col, ok := map[string]string{"NAME": "u.first_name", "LAST_NAME": "u.last_name", "EMAIL": "u.email",
		"LAST_LOGIN": "u.last_login_at"}[sortBy]; ok {
		order = col
	}
	if desc {
		order += " DESC"
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(c.r.Context(), `SELECT count(*) FROM users u WHERE `+cond, args...).Scan(&total); err != nil {
		return portalOut{}, err
	}
	start := portalStart(c.in)
	rows, err := s.DB.Query(c.r.Context(), `SELECT u.id::text, u.num FROM users u WHERE `+cond+` ORDER BY `+order+`, u.num
		OFFSET `+strconv.Itoa(start)+` LIMIT `+strconv.Itoa(portalPage), args...)
	if err != nil {
		return portalOut{}, err
	}
	type idNum struct {
		ID  string
		Num int64
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[idNum])
	if err != nil {
		return portalOut{}, err
	}
	ufs, err := s.portalUFs(c.r.Context(), "user", crm.FieldSight{})
	if err != nil {
		return portalOut{}, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		prof, err := s.Company.Profile(c.r.Context(), x.ID)
		if err != nil {
			return portalOut{}, err
		}
		u, err := s.portalUser(c, prof, x.Num, ufs)
		if err != nil {
			return portalOut{}, err
		}
		out = append(out, u)
	}
	return portalPaged(out, start, len(out), total), nil
}

func lowerAll(list []string) []string {
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = strings.ToLower(x)
	}
	return out
}

// scope: the scopes the caller has; full=true — all go24 knows.
func (s *Server) portalScope(c portalCall) (portalOut, error) {
	full := portalStr(c.in["full"]) == "true" || portalStr(c.in["full"]) == "1" || portalStr(c.in["full"]) == "Y"
	set := map[string]bool{"user": true, "user_basic": true}
	for sc, b := range portalScopes {
		if full || c.p.Can(sc+":read") || c.p.Can(sc+":write") || c.p.Can(sc) {
			set[b] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return portalOut{Result: out}, nil
}

// methods: the methods the caller may call (full=true — all of them; scope=crm — of one scope).
func (s *Server) portalMethodList(c portalCall) (portalOut, error) {
	full := portalStr(c.in["full"]) == "true" || portalStr(c.in["full"]) == "1"
	scope := portalStr(c.in["scope"])
	out := []string{"batch"}
	for name, m := range portalMethods {
		if scope != "" && portalScopeOf(m) != scope {
			continue
		}
		if full || m.Scope == "" || c.p.Can(m.Scope) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return portalOut{Result: out}, nil
}

// method.get: is there such a method, and may the caller call it.
func (s *Server) portalMethodGet(c portalCall) (portalOut, error) {
	name := strings.ToLower(portalStr(c.in["name"]))
	m, ok := portalMethods[name]
	if name == "batch" {
		return portalOut{Result: map[string]bool{"isExisting": true, "isAvailable": true}}, nil
	}
	return portalOut{Result: map[string]bool{"isExisting": ok, "isAvailable": ok && (m.Scope == "" || c.p.Can(m.Scope))}}, nil
}

// app.info: the application calling — for an application's token; a webhook has none.
func (s *Server) portalAppInfo(c portalCall) (portalOut, error) {
	client, ok := strings.CutPrefix(c.p.KeyID, "app:")
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "WRONG_AUTH_TYPE", "err.api_app_only")
	}
	lang := strings.SplitN(s.I18n.Match(c.r.Header.Get("Accept-Language")), "-", 2)[0]
	return portalOut{Result: map[string]any{"ID": client, "CODE": client, "VERSION": 1, "STATUS": "L", "INSTALLED": true,
		"PAYMENT_EXPIRED": "N", "DAYS": nil, "LANGUAGE_ID": lang, "LICENSE": "", "LICENSE_FAMILY": ""}}, nil
}
