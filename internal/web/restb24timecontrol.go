package web

import (
	"math/big"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/worktime"
)

// REST compatible with the portal, «Контроль времени» and the office networks (GO24DEV-978):
// timeman.timecontrol.settings.get and set — the settings of «Настройки → Рабочее время»;
// timeman.timecontrol.report.add — the caller tells where they were during an absence; timeman.timecontrol.reports.get
// — the absences of a person's month by days; reports.settings.get and reports.users.get — whose reports the caller
// sees; timeman.networkrange.set — the office networks. The rights are those of «Рабочее время».

func init() {
	for name, m := range map[string]portalMethod{
		"timeman.timecontrol.settings.get":         {Scope: "company:read", Run: (*Server).portalTCSettings},
		"timeman.timecontrol.settings.set":         {Scope: "company:write", Run: (*Server).portalTCSettingsSet},
		"timeman.timecontrol.report.add":           {Scope: "company:write", Run: (*Server).portalTCReportAdd},
		"timeman.timecontrol.reports.get":          {Scope: "company:read", Run: (*Server).portalTCReports},
		"timeman.timecontrol.reports.settings.get": {Scope: "company:read", Run: (*Server).portalTCReportsSettings},
		"timeman.timecontrol.reports.users.get":    {Scope: "company:read", Run: (*Server).portalTCReportsUsers},
		"timeman.networkrange.set":                 {Scope: "company:write", Run: (*Server).portalTMRangesSet},
	} {
		portalMethods[name] = m
	}
}

// portalTCSettingsOf is «Контроль времени» gives it: go24 counts absences from the pages (idle) only, the
// person is asked the reason by everyone or no one («all» / «none»).
func portalTCSettingsOf(sched worktime.Schedule) map[string]any {
	ask := "none"
	if sched.TimeControl && sched.IdleAsk {
		ask = "all"
	}
	return map[string]any{"active": sched.TimeControl, "minimum_idle_for_report": int(sched.IdleAfter().Minutes()),
		"register_offline": false, "register_idle": sched.TimeControl, "register_desktop": false,
		"report_request_type": ask, "report_request_users": []string{}, "report_simple_type": "all", "report_simple_users": []string{},
		"report_full_type": "all", "report_full_users": []string{}}
}

func (s *Server) portalTCSettings(c portalCall) (portalOut, error) {
	sched, err := s.Tasks.Schedule(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: portalTCSettingsOf(sched)}, nil
}

// portalWorktimeAdmin: the caller may change the settings of working time.
func (s *Server) portalWorktimeAdmin(c portalCall) error {
	ok, err := s.Company.WorktimeSettings(c.r.Context(), c.p.UserID, s.apiAdmin(c.r.Context(), c.p.UserID))
	if err != nil {
		return err
	}
	if !ok {
		return i18n.E("err.forbidden")
	}
	return nil
}

// timeman.timecontrol.settings.set: ACTIVE, MINIMUM_IDLE_FOR_REPORT, REPORT_REQUEST_TYPE (all, user — asked; none —
// not); what is not given stays.
func (s *Server) portalTCSettingsSet(c portalCall) (portalOut, error) {
	if err := s.portalWorktimeAdmin(c); err != nil {
		return portalOut{}, err
	}
	old, err := s.Tasks.Schedule(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	on := portalYes(c.in, "ACTIVE", old.TimeControl)
	minutes := old.IdleMinutes
	if v := strings.TrimSpace(portalStr(portalParam(c.in, "MINIMUM_IDLE_FOR_REPORT"))); v != "" {
		if minutes, err = strconv.Atoi(v); err != nil || minutes < 1 {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "INVALID_MINIMUM_IDLE_FOR_REPORT", "err.portal_field", "MINIMUM_IDLE_FOR_REPORT")
		}
	}
	ask := old.IdleAsk || !old.TimeControl // switched on now — asked, as on the page
	if v := strings.ToLower(strings.TrimSpace(portalStr(portalParam(c.in, "REPORT_REQUEST_TYPE")))); v != "" {
		if !slices.Contains([]string{"all", "user", "none"}, v) {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "INVALID_REPORT_REQUEST_TYPE", "err.portal_field", "REPORT_REQUEST_TYPE")
		}
		ask = v != "none"
	}
	sched := old
	sched.SetTimeControl(old, on, ask, minutes, time.Now())
	if err := s.Tasks.SetSchedule(c.r.Context(), c.p.UserID, sched); err != nil {
		return portalOut{}, err
	}
	s.Log.Info("rest portal time control saved", "user_id", c.p.UserID, "active", on)
	return portalOut{Result: true}, nil
}

// portalIdleKinds: TYPE of timeman.timecontrol.report.add → the kind of the absence.
var portalIdleKinds = map[string]string{"WORK": "work", "PRIVATE": "personal", "PERSONAL": "personal"}

// timeman.timecontrol.report.add: REPORT_ID — the caller's absence, TEXT — where they were, TYPE — WORK (on business,
// when not given) or PRIVATE.
func (s *Server) portalTCReportAdd(c portalCall) (portalOut, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(portalStr(portalParam(c.in, "REPORT_ID"))), 10, 64)
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "INVALID_REPORT_ID", "err.portal_field", "REPORT_ID")
	}
	typ := strings.ToUpper(strings.TrimSpace(portalStr(portalParam(c.in, "TYPE"))))
	if typ == "" {
		typ = "WORK"
	}
	kind, ok := portalIdleKinds[typ]
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "INVALID_TYPE", "err.portal_field", "TYPE")
	}
	if err := s.Company.AnswerIdle(c.r.Context(), c.p.UserID, id, kind, portalStr(portalParam(c.in, "TEXT")), time.Now()); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// portalTCPerson is a person of the reports: the number, the name, the position, the photo.
func (s *Server) portalTCPerson(p company.Person, num int64) map[string]any {
	return map[string]any{"id": num, "name": p.Name, "position": p.Position, "avatar": s.portalPhoto(company.Profile{ID: p.ID, PhotoKey: p.PhotoKey})}
}

// timeman.timecontrol.reports.get: USER_ID (the caller when none), YEAR and MONTH (this month) → report.days — the days
// of the month with a working day or absences: the day's start, end and time worked, and its absences (reports) with
// the kind (WORK, PRIVATE, "" — not told), the text, the times and the duration in seconds.
func (s *Server) portalTCReports(c portalCall) (portalOut, error) {
	uid, err := s.portalTMUser(c, false)
	if err != nil {
		return portalOut{}, err
	}
	now := time.Now()
	year, month := now.Year(), int(now.Month())
	if v := strings.TrimSpace(portalStr(portalParam(c.in, "YEAR"))); v != "" {
		year, err = strconv.Atoi(v)
		if err != nil || year < 2000 || year > 2100 {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "INVALID_YEAR", "err.portal_field", "YEAR")
		}
	}
	if v := strings.TrimSpace(portalStr(portalParam(c.in, "MONTH"))); v != "" {
		month, err = strconv.Atoi(v)
		if err != nil || month < 1 || month > 12 {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "INVALID_MONTH", "err.portal_field", "MONTH")
		}
	}
	from := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
	to := from.AddDate(0, 1, 0)
	ctx := c.r.Context()
	days, err := s.Company.Workdays(ctx, uid, from, to)
	if err != nil {
		return portalOut{}, err
	}
	idles, err := s.Company.Idles(ctx, []string{uid}, from, to)
	if err != nil {
		return portalOut{}, err
	}
	byDay := map[int]map[string]any{}
	dayOf := func(d int) map[string]any {
		if byDay[d] == nil {
			byDay[d] = map[string]any{"index": d, "date": time.Date(year, time.Month(month), d, 0, 0, 0, 0, time.Local).Format(time.DateOnly),
				"workday_date_start": nil, "workday_date_finish": nil, "workday_time": 0, "workday_complete": false,
				"reports": []map[string]any{}}
		}
		return byDay[d]
	}
	for _, d := range days {
		x := dayOf(d.Day.Day())
		x["workday_date_start"], x["workday_time"] = d.Started.In(time.Local).Format(time.RFC3339), d.Worked(now)
		if d.Ended != nil {
			x["workday_date_finish"], x["workday_complete"] = d.Ended.In(time.Local).Format(time.RFC3339), true
		}
	}
	for _, i := range idles {
		x := dayOf(i.Day.Day())
		typ := map[string]string{"work": "WORK", "personal": "PRIVATE"}[i.Kind]
		x["reports"] = append(x["reports"].([]map[string]any), map[string]any{"id": i.ID, "type": typ, "text": i.Reason,
			"time_start": i.Started.In(time.Local).Format(time.RFC3339), "time_finish": i.Ended.In(time.Local).Format(time.RFC3339),
			"duration": i.Secs(), "calendar": false})
	}
	list := make([]map[string]any, 0, len(byDay))
	for d := 1; d <= 31; d++ {
		if x, ok := byDay[d]; ok {
			list = append(list, x)
		}
	}
	prof, err := s.Company.Profile(ctx, uid)
	if err != nil {
		return portalOut{}, err
	}
	num, err := s.portalUserNum(c, uid)
	if err != nil {
		return portalOut{}, err
	}
	sched, err := s.Tasks.Schedule(ctx)
	if err != nil {
		return portalOut{}, err
	}
	_, off := now.Zone()
	return portalOut{Result: map[string]any{"report": map[string]any{"days": list},
		"user": s.portalTCPerson(company.Person{ID: prof.ID, Name: prof.Name, Position: prof.Position, PhotoKey: prof.PhotoKey}, num),
		"info": map[string]any{"year": year, "month": month, "minimum_idle_for_report": int(sched.IdleAfter().Minutes()), "timezone_offset": off}}}, nil
}

// timeman.timecontrol.reports.settings.get: who the caller is for the reports — user_admin (manages the working time),
// department_manager (sees others' days), the departments of the reports (all for an administrator; else their own and
// those they head or are a deputy of), report_view_type full or simple (only their own).
func (s *Server) portalTCReportsSettings(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	admin := s.apiAdmin(ctx, c.p.UserID)
	settings, err := s.Company.WorktimeSettings(ctx, c.p.UserID, admin)
	if err != nil {
		return portalOut{}, err
	}
	under, err := s.Company.WorktimePeople(ctx, c.p.UserID, admin, "view")
	if err != nil {
		return portalOut{}, err
	}
	prof, err := s.Company.Profile(ctx, c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	_, depts, err := s.tree(c.r)
	if err != nil {
		return portalOut{}, err
	}
	var ids []string
	for _, d := range depts {
		mine := admin || d.HeadID == c.p.UserID || slices.ContainsFunc(d.Deputies, func(p company.Person) bool { return p.ID == c.p.UserID }) ||
			slices.ContainsFunc(prof.Departments, func(r company.DeptRef) bool { return r.ID == d.ID })
		if mine {
			ids = append(ids, d.ID)
		}
	}
	nums, err := s.portalNums(ctx, "departments", ids)
	if err != nil {
		return portalOut{}, err
	}
	list := []map[string]any{}
	own := int64(0)
	for _, d := range depts {
		if n, ok := nums[d.ID]; ok {
			list = append(list, map[string]any{"id": n, "name": d.Name})
			if own == 0 && slices.ContainsFunc(prof.Departments, func(r company.DeptRef) bool { return r.ID == d.ID }) {
				own = n
			}
		}
	}
	if own == 0 && len(list) > 0 { // not a member: the first department they head
		own = list[0]["id"].(int64)
	}
	num, err := s.portalUserNum(c, c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	sched, err := s.Tasks.Schedule(ctx)
	if err != nil {
		return portalOut{}, err
	}
	view := "simple"
	if admin || len(under) > 0 {
		view = "full"
	}
	return portalOut{Result: map[string]any{"user_id": num, "user_admin": settings, "department_manager": len(under) > 0,
		"department_id": own, "departments": list, "minimum_idle_for_report": int(sched.IdleAfter().Minutes()),
		"report_view_type": view, "active": sched.TimeControl}}, nil
}

// timeman.timecontrol.reports.users.get: DEPARTMENT_ID (with sub-departments; none — everyone) → the people whose
// reports the caller sees.
func (s *Server) portalTCReportsUsers(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	dept := ""
	if v := portalParam(c.in, "DEPARTMENT_ID"); strings.TrimSpace(portalStr(v)) != "" {
		id, err := s.portalNum(ctx, "departments", v)
		if err != nil || id == "" {
			return portalOut{}, i18n.E("err.not_found")
		}
		dept = id
	}
	people, err := s.sheetPeople(ctx, auth.Session{UserID: c.p.UserID, Admin: s.apiAdmin(ctx, c.p.UserID)}, dept)
	if err != nil {
		return portalOut{}, err
	}
	ids := make([]string, 0, len(people))
	for _, p := range people {
		ids = append(ids, p.ID)
	}
	nums, err := s.portalNums(ctx, "users", ids)
	if err != nil {
		return portalOut{}, err
	}
	out := make([]map[string]any, 0, len(people))
	for _, p := range people {
		out = append(out, s.portalTCPerson(p, nums[p.ID]))
	}
	return portalOut{Result: out}, nil
}

// portalRangePrefixes are the subnets covering the addresses first..last, as few as can be.
func portalRangePrefixes(first, last netip.Addr) []netip.Prefix {
	bits := first.BitLen()
	start, end := new(big.Int).SetBytes(first.AsSlice()), new(big.Int).SetBytes(last.AsSlice())
	one := big.NewInt(1)
	var out []netip.Prefix
	for start.Cmp(end) <= 0 {
		host := 0
		for host < bits {
			size := new(big.Int).Lsh(one, uint(host+1))
			top := new(big.Int).Add(start, size)
			if new(big.Int).Mod(start, size).Sign() != 0 || top.Sub(top, one).Cmp(end) > 0 {
				break
			}
			host++
		}
		raw := start.FillBytes(make([]byte, bits/8))
		a, _ := netip.AddrFromSlice(raw)
		out = append(out, netip.PrefixFrom(a, bits-host))
		start.Add(start, new(big.Int).Lsh(one, uint(host)))
	}
	return out
}

// portalParseRange reads ip_range: «first-last», one address or a subnet.
func portalParseRange(v string) ([]netip.Prefix, bool) {
	v = strings.TrimSpace(v)
	if p, err := netip.ParsePrefix(v); err == nil {
		return []netip.Prefix{p.Masked()}, true
	}
	a, b, isRange := strings.Cut(v, "-")
	first, err := netip.ParseAddr(strings.TrimSpace(a))
	if err != nil {
		return nil, false
	}
	first = first.Unmap()
	last := first
	if isRange {
		if last, err = netip.ParseAddr(strings.TrimSpace(b)); err != nil {
			return nil, false
		}
		last = last.Unmap()
	}
	if first.BitLen() != last.BitLen() || last.Less(first) {
		return nil, false
	}
	return portalRangePrefixes(first, last), true
}

// portalMaxPrefixes bounds the subnets of the ranges of one call.
const portalMaxPrefixes = 1000

// timeman.networkrange.set: RANGES — the list of {ip_range, name} that replaces the office networks.
func (s *Server) portalTMRangesSet(c portalCall) (portalOut, error) {
	if err := s.portalWorktimeAdmin(c); err != nil {
		return portalOut{}, err
	}
	var items []any
	switch v := portalParam(c.in, "RANGES").(type) {
	case []any:
		items = v
	case map[string]any: // RANGES[0][ip_range]=… of a form
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int { x, _ := strconv.Atoi(a); y, _ := strconv.Atoi(b); return x - y })
		for _, k := range keys {
			items = append(items, v[k])
		}
	case nil:
	default:
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "WRONG_RANGES", "err.portal_field", "RANGES")
	}
	offices := []worktime.Office{}
	for _, it := range items {
		m := portalMap(it)
		prefixes, ok := portalParseRange(portalStr(portalParam(m, "ip_range")))
		if m == nil || !ok {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "WRONG_RANGES", "err.portal_field", "RANGES")
		}
		for _, p := range prefixes {
			offices = append(offices, worktime.Office{Net: p.String(), Name: strings.TrimSpace(portalStr(portalParam(m, "name")))})
		}
		if len(offices) > portalMaxPrefixes {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "WRONG_RANGES", "err.portal_field", "RANGES")
		}
	}
	sched, err := s.Tasks.Schedule(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	sched.Offices = offices
	if err := s.Tasks.SetSchedule(c.r.Context(), c.p.UserID, sched); err != nil {
		return portalOut{}, err
	}
	s.Log.Info("rest portal office networks saved", "user_id", c.p.UserID, "networks", len(offices))
	return portalOut{Result: true}, nil
}
