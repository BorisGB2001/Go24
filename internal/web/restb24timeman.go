package web

import (
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/worktime"
)

// REST compatible with the portal, the working time (GO24DEV-948): timeman.status, open, pause, close — the working day
// of the caller (or of USER_ID: whom the caller manages may be looked at, an administrator starts and finishes anyone's
// day); timeman.settings — the person's rules of the day by their work schedule; timeman.schedule.get — a work
// schedule; timeman.networkrange.get and check — the office networks. The rights are those of «Рабочее время».

func init() {
	for name, m := range map[string]portalMethod{
		"timeman.status":             {Scope: "company:read", Run: (*Server).portalTMStatus},
		"timeman.open":               {Scope: "company:write", Run: (*Server).portalTMAction},
		"timeman.pause":              {Scope: "company:write", Run: (*Server).portalTMAction},
		"timeman.close":              {Scope: "company:write", Run: (*Server).portalTMAction},
		"timeman.settings":           {Scope: "company:read", Run: (*Server).portalTMSettings},
		"timeman.schedule.get":       {Scope: "company:read", Run: (*Server).portalTMSchedule},
		"timeman.networkrange.get":   {Scope: "company:read", Run: (*Server).portalTMRanges},
		"timeman.networkrange.check": {Scope: "company:read", Run: (*Server).portalTMRangeCheck},
	} {
		portalMethods[name] = m
	}
}

// portalTMUser is the person of USER_ID (the caller when none); another one is seen by whom they report to and by
// administrators, changed (change) — by administrators only.
func (s *Server) portalTMUser(c portalCall, change bool) (string, error) {
	v := strings.TrimSpace(portalStr(portalParam(c.in, "USER_ID")))
	if v == "" || v == "0" {
		return c.p.UserID, nil
	}
	id, err := s.portalNum(c.r.Context(), "users", v)
	if err != nil || id == "" {
		return "", i18n.E("err.not_found")
	}
	if id == c.p.UserID {
		return id, nil
	}
	admin := s.apiAdmin(c.r.Context(), c.p.UserID)
	if change {
		if !admin {
			return "", i18n.E("err.forbidden")
		}
		return id, nil
	}
	ok, err := s.Company.Sees(c.r.Context(), c.p.UserID, id, admin)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", i18n.E("err.forbidden")
	}
	return id, nil
}

// portalClock is a duration gives it: «HH:MM:SS».
func portalClock(secs int) string {
	secs = max(secs, 0)
	return fmt.Sprintf("%02d:%02d:%02d", secs/3600, secs/60%60, secs%60)
}

// portalTMDay is the person's day of timeman.status: today's, or an earlier one left open (EXPIRED).
func (s *Server) portalTMDay(c portalCall, userID string, now time.Time) (*company.Workday, bool, error) {
	d, err := s.Company.Today(c.r.Context(), userID, now)
	if err != nil || d != nil {
		return d, false, err
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	days, err := s.Company.Workdays(c.r.Context(), userID, day.AddDate(0, 0, -31), day)
	if err != nil || len(days) == 0 || days[0].Ended != nil {
		return nil, false, err
	}
	return &days[0], true, nil
}

// portalTMStatusOf is the state of the person's working day: STATUS is OPENED, PAUSED, CLOSED or EXPIRED (an earlier
// day not finished), the times, DURATION — worked without pauses, TIME_LEAKS — the pauses.
func (s *Server) portalTMStatusOf(c portalCall, userID string) (map[string]any, error) {
	now := time.Now()
	d, expired, err := s.portalTMDay(c, userID, now)
	if err != nil {
		return nil, err
	}
	_, off := now.Zone()
	out := map[string]any{"STATUS": "CLOSED", "TIME_START": nil, "TIME_FINISH": nil, "DURATION": portalClock(0), "TIME_LEAKS": portalClock(0),
		"ACTIVE": false, "IP_OPEN": "", "IP_CLOSE": "", "LAT_OPEN": 0, "LON_OPEN": 0, "LAT_CLOSE": 0, "LON_CLOSE": 0, "TZ_OFFSET": off}
	if d == nil {
		return out, nil
	}
	pauses := d.PauseSecs
	if d.PausedAt != nil && d.Ended == nil {
		pauses += int(now.Sub(*d.PausedAt).Seconds())
	}
	out["TIME_START"] = d.Started.In(time.Local).Format(time.RFC3339)
	out["DURATION"], out["TIME_LEAKS"], out["IP_OPEN"] = portalClock(d.Worked(now)), portalClock(pauses), d.Start.IP
	if g := d.Start.Geo; g != nil {
		out["LAT_OPEN"], out["LON_OPEN"] = g.Lat, g.Lon
	}
	switch d.State() {
	case "running":
		out["STATUS"], out["ACTIVE"] = "OPENED", true
	case "paused":
		out["STATUS"] = "PAUSED"
	default:
		out["TIME_FINISH"], out["IP_CLOSE"] = d.Ended.In(time.Local).Format(time.RFC3339), d.End.IP
		if g := d.End.Geo; g != nil {
			out["LAT_CLOSE"], out["LON_CLOSE"] = g.Lat, g.Lon
		}
	}
	if expired {
		out["STATUS"] = "EXPIRED"
	}
	return out, nil
}

func (s *Server) portalTMStatus(c portalCall) (portalOut, error) {
	uid, err := s.portalTMUser(c, false)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalTMStatusOf(c, uid)
	return portalOut{Result: out}, err
}

// portalTMAction: timeman.open starts the day (a paused one goes on, a finished one is continued, as «Продолжить рабочий
// день»; an open one stays as it is), timeman.pause pauses it, timeman.close finishes it with REPORT. TIME
// (another time than now) is not taken: the day is changed now, as on the page.
func (s *Server) portalTMAction(c portalCall) (portalOut, error) {
	uid, err := s.portalTMUser(c, true)
	if err != nil {
		return portalOut{}, err
	}
	now := time.Now()
	d, err := s.Company.Today(c.r.Context(), uid, now)
	if err != nil {
		return portalOut{}, err
	}
	state := "none"
	if d != nil {
		state = d.State()
	}
	action := map[string]map[string]string{
		"timeman.open":  {"none": "start", "paused": "resume", "done": "reopen"},
		"timeman.pause": {"running": "pause"},
		"timeman.close": {"running": "finish", "paused": "finish"},
	}[c.method][state]
	if action == "" && !(c.method == "timeman.open" && state == "running") {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_WRONG_STATE", "err.workday_state")
	}
	if action != "" {
		lat, _ := strconv.ParseFloat(portalStr(portalParam(c.in, "LAT")), 64)
		lon, _ := strconv.ParseFloat(portalStr(portalParam(c.in, "LON")), 64)
		at, err := s.workPlace(c.r, action, company.NewGeo(lat, lon, 0))
		if err != nil {
			return portalOut{}, err
		}
		at.Device = "app"
		if err := s.Company.WorkAction(c.r.Context(), uid, action, portalStr(portalParam(c.in, "REPORT")), now, at); err != nil {
			return portalOut{}, err
		}
		s.Log.Info("rest portal workday", "user_id", uid, "action", action, "by", c.p.UserID)
		s.tellViolations(c.r, uid, action)
		s.Telephony.Kick() // the clients' calls may pass them by now (or not)
	}
	out, err := s.portalTMStatusOf(c, uid)
	return portalOut{Result: out}, err
}

// portalHMS is «hh:mm» as «hh:mm:ss» ("" — nothing).
func portalHMS(hm string) any {
	if hm == "" {
		return nil
	}
	return hm + ":00"
}

// portalSecs is «hh:mm» in seconds from midnight (0 — none).
func portalSecs(hm string) int {
	t, err := time.Parse("15:04", hm)
	if err != nil {
		return 0
	}
	return t.Hour()*3600 + t.Minute()*60
}

// portalTMSettings: the person's rules of the working day, by their work schedule or the company's working time —
// UF_TM_FREE is a flexible schedule, UF_TM_MAX_START the latest start, UF_TM_MIN_FINISH the earliest end,
// UF_TM_MIN_DURATION the shortest day, UF_TM_REPORT_REQ the report on finishing (Y required, A optional, N none); ADMIN — the caller manages the working time.
func (s *Server) portalTMSettings(c portalCall) (portalOut, error) {
	uid, err := s.portalTMUser(c, false)
	if err != nil {
		return portalOut{}, err
	}
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	n, err := s.apiNorm(c.r, uid, day, day.AddDate(0, 0, 1))
	if err != nil {
		return portalOut{}, err
	}
	late, early, short, free := n.Company.LateAfter, n.Company.LeaveBefore, n.Company.MinDuration, false
	if late == "" {
		late = n.Company.Start
	}
	if n.WS != nil {
		late, early, short, free = n.WS.LateAfter, n.WS.LeaveBefore, n.WS.MinDuration, n.WS.Kind == "flexible"
	}
	rules, err := s.Company.DayRulesOf(c.r.Context(), uid)
	if err != nil {
		return portalOut{}, err
	}
	report := map[string]string{"required": "Y", "optional": "A", "none": "N"}[rules.Report]
	return portalOut{Result: map[string]any{"UF_TIMEMAN": true, "UF_TM_FREE": free, "UF_TM_MAX_START": portalHMS(late),
		"UF_TM_MIN_FINISH": portalHMS(early), "UF_TM_MIN_DURATION": portalHMS(short), "UF_TM_REPORT_REQ": report, "UF_TM_REPORT_TPL": []string{},
		"UF_TM_ALLOWED_DELTA": "-1", "ADMIN": s.apiAdmin(c.r.Context(), c.p.UserID)}}, nil
}

// portalWeekDays are working days writes them: «12345», Monday is 1, Sunday 7.
func portalWeekDays(days []int) string {
	var b strings.Builder
	for d := 1; d <= 7; d++ {
		for _, x := range days {
			if x == d%7 {
				b.WriteString(strconv.Itoa(d))
				break
			}
		}
	}
	return b.String()
}

// portalTMSchedule: timeman.schedule.get — the schedule of id: SCHEDULE_TYPE FIXED, SHIFT or FLEXTIME; SHIFTS are the
// working hours of a fixed schedule (one per set of days) or the shifts, the times in seconds from midnight.
func (s *Server) portalTMSchedule(c portalCall) (portalOut, error) {
	id, err := strconv.ParseInt(portalStr(portalParam(c.in, "id")), 10, 64)
	if err != nil {
		return portalOut{}, i18n.E("err.not_found")
	}
	ws, err := s.Company.WorkSchedule(c.r.Context(), id)
	if err != nil {
		return portalOut{}, err
	}
	shifts := []map[string]any{}
	shift := func(id int64, name, start, end string, brk int, days string) {
		shifts = append(shifts, map[string]any{"ID": strconv.FormatInt(id, 10), "SCHEDULE_ID": strconv.FormatInt(ws.ID, 10), "NAME": name,
			"WORK_TIME_START": portalSecs(start), "WORK_TIME_END": portalSecs(end), "BREAK_DURATION": brk * 60, "WORK_DAYS": days, "DELETED": "0"})
	}
	for i, t := range ws.Times {
		shift(int64(i+1), "", t.Start, t.End, t.Break, portalWeekDays(t.Days))
	}
	for _, sh := range ws.Shifts {
		shift(sh.ID, sh.Name, sh.Start, sh.End, sh.Break, "")
	}
	kind := map[string]string{"fixed": "FIXED", "shift": "SHIFT", "flexible": "FLEXTIME"}[ws.Kind]
	return portalOut{Result: map[string]any{"ID": strconv.FormatInt(ws.ID, 10), "NAME": ws.Name, "SCHEDULE_TYPE": kind,
		"REPORT_PERIOD": "MONTH", "IS_FOR_ALL_USERS": false, "DELETED": "0", "SHIFTS": shifts,
		"ALLOWED_DEVICES": map[string]bool{"browser": slices.Contains(ws.Devices, "browser"),
			"portaltime": slices.Contains(ws.Devices, "terminal"), "mobile": slices.Contains(ws.Devices, "app")},
		"VIOLATION_RULES": map[string]any{"MAX_EXACT_START": portalSecs(ws.LateAfter), "MIN_EXACT_END": portalSecs(ws.LeaveBefore),
			"MIN_DAY_DURATION": portalSecs(ws.MinDuration), "MAX_SHIFT_START_DELAY": ws.Tolerance * 60}}}, nil
}

// portalIPRange is an office network writes it: «first-last» address.
func portalIPRange(net string) string {
	p, err := netip.ParsePrefix(net)
	if err != nil {
		return net
	}
	first := p.Masked().Addr()
	last := first.As16()
	bits := p.Bits()
	if first.Is4() {
		bits += 96
	}
	for i := bits; i < 128; i++ {
		last[i/8] |= 1 << (7 - i%8)
	}
	end := netip.AddrFrom16(last)
	if first.Is4() {
		end = end.Unmap()
	}
	if end == first {
		return first.String()
	}
	return first.String() + "-" + end.String()
}

func portalRanges(sched worktime.Schedule) []map[string]any {
	out := []map[string]any{}
	for _, o := range sched.Offices {
		out = append(out, map[string]any{"ip_range": portalIPRange(o.Net), "name": o.Name})
	}
	return out
}

// portalTMRanges: timeman.networkrange.get — the office networks of «Рабочее время».
func (s *Server) portalTMRanges(c portalCall) (portalOut, error) {
	sched, err := s.Tasks.Schedule(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: portalRanges(sched)}, nil
}

// portalTMRangeCheck: timeman.networkrange.check — whether IP is in an office network: {ip, range_name}, the name
// false when outside.
func (s *Server) portalTMRangeCheck(c portalCall) (portalOut, error) {
	ip := strings.TrimSpace(portalStr(portalParam(c.in, "IP")))
	if _, err := netip.ParseAddr(ip); err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "WRONG_IP", "err.portal_field", "IP")
	}
	sched, err := s.Tasks.Schedule(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{"ip": ip, "range_name": false}
	if name, in := sched.OfficeOf(ip); in {
		out["range_name"] = name
	}
	return portalOut{Result: out}, nil
}
