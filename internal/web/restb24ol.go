package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/openlines"
)

// REST compatible with the portal, open lines (GO24DEV-930): imopenlines.config.* — the lines (CONFIG_ID; changes —
// administrators, as the page); imopenlines.dialog.get and imopenlines.session.history.get — a dialogue and its
// messages; imopenlines.operator.* and imopenlines.session.* — what an operator does on the page (take, skip, pass,
// close, spam, pin, intercept, the head's rating); imopenlines.crm.* — the dialogues of a CRM card, a lead of the
// dialogue, an answer. A dialogue is one number for both CHAT_ID and SESSION_ID (migration 866), a client's next
// dialogue is a new one. The rights are those of the page: an operator sees their dialogues and their lines' unassigned
// ones.

func init() {
	for name, m := range map[string]portalMethod{
		"imopenlines.revision.get":            {Scope: "openlines:read", Run: (*Server).portalOLRevision},
		"imopenlines.config.list.get":         {Scope: "openlines:read", Run: (*Server).portalOLConfigs},
		"imopenlines.config.get":              {Scope: "openlines:read", Run: (*Server).portalOLConfig},
		"imopenlines.config.path.get":         {Scope: "openlines:read", Run: (*Server).portalOLPath},
		"imopenlines.config.add":              {Scope: "openlines:write", Run: (*Server).portalOLConfigAdd},
		"imopenlines.config.update":           {Scope: "openlines:write", Run: (*Server).portalOLConfigUpdate},
		"imopenlines.config.delete":           {Scope: "openlines:write", Run: (*Server).portalOLConfigDelete},
		"imopenlines.dialog.get":              {Scope: "openlines:read", Run: (*Server).portalOLDialog},
		"imopenlines.session.history.get":     {Scope: "openlines:read", Run: (*Server).portalOLHistory},
		"imopenlines.session.intercept":       {Scope: "openlines:write", Run: (*Server).portalOLIntercept},
		"imopenlines.session.start":           {Scope: "openlines:write", Run: (*Server).portalOLStart},
		"imopenlines.session.mode.pin":        {Scope: "openlines:write", Run: (*Server).portalOLPin},
		"imopenlines.session.mode.pinall":     {Scope: "openlines:write", Run: (*Server).portalOLPinAll},
		"imopenlines.session.mode.unpinall":   {Scope: "openlines:write", Run: (*Server).portalOLPinAll},
		"imopenlines.session.head.vote":       {Scope: "openlines:write", Run: (*Server).portalOLHeadVote},
		"imopenlines.operator.answer":         {Scope: "openlines:write", Run: (*Server).portalOLAnswer},
		"imopenlines.operator.skip":           {Scope: "openlines:write", Run: (*Server).portalOLSkip},
		"imopenlines.operator.spam":           {Scope: "openlines:write", Run: (*Server).portalOLSpam},
		"imopenlines.operator.finish":         {Scope: "openlines:write", Run: (*Server).portalOLFinish},
		"imopenlines.operator.another.finish": {Scope: "openlines:write", Run: (*Server).portalOLFinish},
		"imopenlines.operator.transfer":       {Scope: "openlines:write", Run: (*Server).portalOLTransfer},
		"imopenlines.crm.chat.get":            {Scope: "openlines:read", Run: (*Server).portalOLCRMChats},
		"imopenlines.crm.lead.create":         {Scope: "openlines:write", Run: (*Server).portalOLLead},
		"imopenlines.crm.message.add":         {Scope: "openlines:write", Run: (*Server).portalOLMessageAdd},
		"imopenlines.session.mode.silent":     {Scope: "openlines:write", Run: (*Server).portalOLSilent},
		"imopenlines.session.join":            {Scope: "openlines:write", Run: (*Server).portalOLJoin},
		"imopenlines.message.quick.save":      {Scope: "openlines:write", Run: (*Server).portalOLQuickSave},
		"imopenlines.crm.chat.getlastid":      {Scope: "openlines:read", Run: (*Server).portalOLCRMLastChat},
		"imopenlines.crm.chat.user.add":       {Scope: "openlines:write", Run: (*Server).portalOLCRMUserAdd},
		"imopenlines.crm.chat.user.delete":    {Scope: "openlines:write", Run: (*Server).portalOLCRMUserDelete},
	} {
		portalMethods[name] = m
	}
}

// portalOLQueue: the queue's modes of go24 and the portal.
var portalOLQueue = map[string]string{"all": "all", "evenly": "evenly", "strict": "strictly"}

// portalOLMaxType: what the operator's limit counts — till the answer or the close.
var portalOLMaxType = map[string]string{"answer": "ANSWERED", "close": "CLOSED"}

// portalOLDays: the days off, 0 Sunday … 6 Saturday.
var portalOLDays = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// portalOLConnectors: where the client writes from — the connector and its title.
var portalOLConnectors = map[string][2]string{
	"widget": {"livechat", "Online chat"}, "telegram": {"telegrambot", "Telegram"}, "vk": {"vkgroup", "VK"}, "viber": {"viber", "Viber"},
	"ok": {"ok", "Odnoklassniki"}, "avito": {"avito", "Avito"}, "whatsapp": {"whatsappbyedna", "WhatsApp"}, "max": {"max", "MAX"},
}

func portalOLConnector(channel string) [2]string {
	if c, ok := portalOLConnectors[channel]; ok {
		return c
	}
	return portalOLConnectors["widget"]
}

// portalOLHour is «09:30» writes working hours — «9.30»; and back.
func portalOLHour(hm string) string {
	h, m, _ := strings.Cut(hm, ":")
	n, _ := strconv.Atoi(h)
	return fmt.Sprintf("%d.%s", n, cmp.Or(m, "00"))
}

func portalOLHourIn(v string) string {
	v = strings.TrimSpace(strings.ReplaceAll(v, ":", "."))
	h, m, _ := strings.Cut(v, ".")
	hn, err1 := strconv.Atoi(h)
	mn := 0
	var err2 error
	if m != "" {
		mn, err2 = strconv.Atoi(m)
	}
	if err1 != nil || err2 != nil {
		return v // cleanLine refuses it
	}
	return fmt.Sprintf("%02d:%02d", hn, mn)
}

// portalOLNums are the numbers of rows of a table (ol_widgets, ol_sessions, users) by their uuid.
func (s *Server) portalOLNums(ctx context.Context, table string, ids []string) (map[string]int64, error) {
	out := map[string]int64{}
	rows, err := s.DB.Query(ctx, `SELECT id::text, num FROM `+table+` WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

func (s *Server) portalOLNum(ctx context.Context, table, id string) int64 {
	if id == "" {
		return 0
	}
	m, _ := s.portalOLNums(ctx, table, []string{id})
	return m[id]
}

// portalOLLineID is the line of CONFIG_ID.
func (s *Server) portalOLLineID(c portalCall, v any) (string, error) {
	if portalStr(v) == "" {
		return "", s.portalErr(c.r, http.StatusBadRequest, "CONFIG_ID_EMPTY", "err.portal_config")
	}
	return s.portalNum(c.r.Context(), "ol_widgets", v)
}

// portalOLChat is the dialogue of CHAT_ID (or SESSION_ID, DIALOG_ID «chatN», USER_CODE «connector|line|N|…»), one the
// key's owner sees, and who they are.
func (s *Server) portalOLChat(c portalCall) (string, openlines.Who, error) {
	v := portalStr(portalParam(c.in, "chat_id"))
	if v == "" {
		v = portalStr(portalParam(c.in, "session_id"))
	}
	if v == "" {
		v = strings.TrimPrefix(portalStr(portalParam(c.in, "dialog_id")), "chat")
	}
	if code := strings.Split(portalStr(portalParam(c.in, "user_code")), "|"); v == "" && len(code) >= 3 {
		v = code[2]
	}
	if _, err := strconv.ParseInt(v, 10, 64); err != nil {
		return "", openlines.Who{}, s.portalErr(c.r, http.StatusBadRequest, "CHAT_ID_EMPTY", "err.portal_chat")
	}
	id, err := s.portalNum(c.r.Context(), "ol_sessions", v)
	if err == nil && id == "" {
		err = openlines.ErrNotFound
	}
	if err != nil {
		return "", openlines.Who{}, err
	}
	who, err := s.apiOLWho(c.r.Context(), c.p, id)
	return id, who, err
}

// portalOLSession is the dialogue as it is now.
func (s *Server) portalOLSession(ctx context.Context, id string) (openlines.Session, error) {
	list, err := s.OpenLines.Sessions(ctx, openlines.Filter{ID: id, Who: openlines.Who{All: true}})
	if err != nil {
		return openlines.Session{}, err
	}
	if len(list) == 0 {
		return openlines.Session{}, openlines.ErrNotFound
	}
	return list[0], nil
}

// ---------- the lines ----------

func (s *Server) portalOLRevision(c portalCall) (portalOut, error) {
	return portalOut{Result: map[string]int{"rest": 1, "web": 1, "mobile": 1}}, nil
}

// portalOLConfigOf is a line tells it; queue — with its operators' numbers.
func (s *Server) portalOLConfigOf(ctx context.Context, w openlines.Widget, num int64, queue bool) (map[string]any, error) {
	off := []string{}
	for d, name := range portalOLDays {
		if !slices.Contains(w.HoursDays, int32(d)) {
			off = append(off, name)
		}
	}
	crmCreate := "none"
	if w.CRMAuto {
		crmCreate = "lead"
	}
	out := map[string]any{
		"ID": num, "ACTIVE": portalYN(w.Active), "LINE_NAME": w.Name, "DATE_CREATE": w.CreatedAt.Format(time.RFC3339),
		"QUEUE_TYPE": portalOLQueue[w.QueueMode], "QUEUE_TIME": w.AnswerWait * 60, "NO_ANSWER_TIME": w.NoanswerAfter * 60,
		"CRM": "Y", "CRM_CREATE": crmCreate, "CRM_FORWARD": portalYN(w.CRMForward),
		"WORKTIME_ENABLE": portalYN(w.HoursOn), "WORKTIME_FROM": portalOLHour(w.HoursFrom), "WORKTIME_TO": portalOLHour(w.HoursTo),
		"WORKTIME_TIMEZONE": w.Loc().String(), "WORKTIME_HOLIDAYS": openlines.HolidaysDM(w.Holidays), "WORKTIME_DAYOFF": off, "WORKTIME_DAYOFF_RULE": "text", "WORKTIME_DAYOFF_TEXT": w.OfflineText,
		"WELCOME_MESSAGE": portalYN(w.WelcomeText != ""), "WELCOME_MESSAGE_TEXT": w.WelcomeText,
		"NO_ANSWER_RULE": map[bool]string{true: "text", false: "none"}[w.NoanswerText != ""], "NO_ANSWER_TEXT": w.NoanswerText,
		"CLOSE_RULE": map[bool]string{true: "text", false: "none"}[w.CloseText != ""], "CLOSE_TEXT": w.CloseText,
		"AUTO_CLOSE_RULE": map[bool]string{true: "text", false: "none"}[w.Autoclose > 0], "AUTO_CLOSE_TIME": w.Autoclose * 3600,
		"VOTE_MESSAGE": portalYN(w.VoteOn), "VOTE_MESSAGE_1_TEXT": w.VoteText, "AGREEMENT_MESSAGE": portalYN(w.ConsentOn),
		"MAX_CHAT": w.MaxChats, "TYPE_MAX_CHAT": portalOLMaxType[w.MaxChatsType],
		"KPI_FIRST_ANSWER_TIME": w.KPIFirst * 60, "KPI_FIRST_ANSWER_ALERT": portalYN(w.KPIFirst > 0 && (w.KPIOperator || w.KPIHeads)),
		"KPI_FURTHER_ANSWER_TIME": w.KPIFurther * 60, "KPI_FURTHER_ANSWER_ALERT": portalYN(w.KPIFurther > 0 && (w.KPIOperator || w.KPIHeads)),
		"HISTORY_SEND_ON": portalYN(w.HistoryOn), "SEND_WELCOME_EACH_SESSION": "Y",
	}
	if queue {
		nums, err := s.portalOLNums(ctx, "users", w.Operators)
		if err != nil {
			return nil, err
		}
		q := []int64{}
		for _, id := range w.Operators {
			if n, ok := nums[id]; ok {
				q = append(q, n)
			}
		}
		out["QUEUE"] = q
	}
	return out, nil
}

// imopenlines.config.list.get: PARAMS[filter][ACTIVE] (Y|N), PARAMS[filter][ID]; OPTIONS[QUEUE] = Y — with the queues.
func (s *Server) portalOLConfigs(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	list, err := s.OpenLines.Widgets(ctx)
	if err != nil {
		return portalOut{}, err
	}
	ids := make([]string, len(list))
	for i, w := range list {
		ids[i] = w.ID
	}
	nums, err := s.portalOLNums(ctx, "ol_widgets", ids)
	if err != nil {
		return portalOut{}, err
	}
	filter := portalMap(portalParam(portalMap(portalParam(c.in, "params")), "filter"))
	queue := strings.EqualFold(portalStr(portalParam(portalMap(portalParam(c.in, "options")), "queue")), "Y")
	out := []map[string]any{}
	for _, w := range list {
		if a := portalStr(portalParam(filter, "active")); a != "" && a != portalYN(w.Active) {
			continue
		}
		if id := portalStr(portalParam(filter, "id")); id != "" && id != strconv.FormatInt(nums[w.ID], 10) {
			continue
		}
		x, err := s.portalOLConfigOf(ctx, w, nums[w.ID], queue)
		if err != nil {
			return portalOut{}, err
		}
		out = append(out, x)
	}
	return portalOut{Result: out}, nil
}

// imopenlines.config.get: CONFIG_ID, WITH_QUEUE (Y by default).
func (s *Server) portalOLConfig(c portalCall) (portalOut, error) {
	id, err := s.portalOLLineID(c, portalParam(c.in, "config_id"))
	if err != nil {
		return portalOut{}, err
	}
	w, err := s.OpenLines.Widget(c.r.Context(), id)
	if err != nil {
		return portalOut{}, err
	}
	x, err := s.portalOLConfigOf(c.r.Context(), w, s.portalOLNum(c.r.Context(), "ol_widgets", id), portalYes(c.in, "with_queue", true))
	return portalOut{Result: x}, err
}

// imopenlines.config.path.get: where the lines are set up.
func (s *Server) portalOLPath(c portalCall) (portalOut, error) {
	base := strings.TrimRight(s.BaseURL, "/")
	return portalOut{Result: map[string]string{"SERVER_ADDRESS": base, "PUBLIC_PATH": base + "/openlines/widgets"}}, nil
}

// portalOLApply puts PARAMS into the line: LINE_NAME, ACTIVE, QUEUE (the operators' numbers), QUEUE_TYPE,
// QUEUE_TIME and NO_ANSWER_TIME (seconds), CRM_CREATE (lead | none), CRM_FORWARD, WORKTIME_ENABLE, WORKTIME_FROM/TO
// («9.30»), WORKTIME_DAYOFF, WORKTIME_DAYOFF_TEXT, WELCOME_MESSAGE_TEXT, NO_ANSWER_TEXT, CLOSE_TEXT, AUTO_CLOSE_TIME
// (seconds), VOTE_MESSAGE, VOTE_MESSAGE_1_TEXT, AGREEMENT_MESSAGE, MAX_CHAT, TYPE_MAX_CHAT, KPI_FIRST_ANSWER_TIME and
// KPI_FURTHER_ANSWER_TIME (seconds), HISTORY_SEND_ON.
func (s *Server) portalOLApply(c portalCall, w *openlines.Widget, in map[string]any) error {
	str := func(k string, to *string) {
		if v, ok := portalParamOK(in, k); ok {
			*to = portalStr(v)
		}
	}
	yes := func(k string, to *bool) {
		if _, ok := portalParamOK(in, k); ok {
			*to = portalYes(in, k, false)
		}
	}
	var bad string
	num := func(k string, to *int, unit int) {
		if v, ok := portalParamOK(in, k); ok {
			n, err := strconv.Atoi(portalStr(v))
			if err != nil || n < 0 {
				bad = strings.ToUpper(k)
				return
			}
			*to = (n + unit - 1) / unit
		}
	}
	str("line_name", &w.Name)
	yes("active", &w.Active)
	if v, ok := portalParamOK(in, "queue"); ok {
		ids, err := s.portalUserIDs(c, v)
		if err != nil {
			return err
		}
		w.Operators = ids
	}
	if v, ok := portalParamOK(in, "queue_type"); ok {
		w.QueueMode = ""
		for g, b := range portalOLQueue {
			if strings.EqualFold(portalStr(v), b) {
				w.QueueMode = g
			}
		}
		if w.QueueMode == "" {
			bad = "QUEUE_TYPE"
		}
	}
	num("queue_time", &w.AnswerWait, 60)
	num("no_answer_time", &w.NoanswerAfter, 60)
	if v, ok := portalParamOK(in, "crm_create"); ok {
		w.CRMAuto = strings.EqualFold(portalStr(v), "lead") || strings.EqualFold(portalStr(v), "deal")
	}
	yes("crm_forward", &w.CRMForward)
	yes("worktime_enable", &w.HoursOn)
	if v, ok := portalParamOK(in, "worktime_from"); ok {
		w.HoursFrom = portalOLHourIn(portalStr(v))
	}
	if v, ok := portalParamOK(in, "worktime_to"); ok {
		w.HoursTo = portalOLHourIn(portalStr(v))
	}
	if v, ok := portalParamOK(in, "worktime_dayoff"); ok {
		off := lowerAll(portalList(v))
		w.HoursDays = []int32{}
		for d, name := range portalOLDays {
			if !slices.Contains(off, strings.ToLower(name)) {
				w.HoursDays = append(w.HoursDays, int32(d))
			}
		}
	}
	str("worktime_timezone", &w.HoursTZ)
	if v, ok := portalParamOK(in, "worktime_holidays"); ok {
		w.Holidays = portalList(v)
	}
	str("worktime_dayoff_text", &w.OfflineText)
	str("welcome_message_text", &w.WelcomeText)
	if !portalYes(in, "welcome_message", true) {
		w.WelcomeText = ""
	}
	str("no_answer_text", &w.NoanswerText)
	str("close_text", &w.CloseText)
	num("auto_close_time", &w.Autoclose, 3600)
	yes("vote_message", &w.VoteOn)
	str("vote_message_1_text", &w.VoteText)
	yes("agreement_message", &w.ConsentOn)
	num("max_chat", &w.MaxChats, 1)
	if v, ok := portalParamOK(in, "type_max_chat"); ok {
		w.MaxChatsType = ""
		for g, b := range portalOLMaxType {
			if strings.EqualFold(portalStr(v), b) || strings.EqualFold(portalStr(v), b+"_NEW") {
				w.MaxChatsType = g
			}
		}
		if w.MaxChatsType == "" {
			bad = "TYPE_MAX_CHAT"
		}
	}
	num("kpi_first_answer_time", &w.KPIFirst, 60)
	num("kpi_further_answer_time", &w.KPIFurther, 60)
	yes("history_send_on", &w.HistoryOn)
	if bad != "" {
		return s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", bad)
	}
	return nil
}

func (s *Server) portalOLAdmin(c portalCall) error {
	if !s.apiAdmin(c.r.Context(), c.p.UserID) {
		return i18n.E("err.forbidden")
	}
	return nil
}

// imopenlines.config.add: PARAMS (portalOLApply) — the new line's number; a line without its own settings gets those
// of a new line of the page.
func (s *Server) portalOLConfigAdd(c portalCall) (portalOut, error) {
	if err := s.portalOLAdmin(c); err != nil {
		return portalOut{}, err
	}
	w := openlines.Widget{Active: true, QueueMode: "all", AnswerWait: 2, MaxChatsType: "answer",
		HoursFrom: "09:00", HoursTo: "18:00", HoursDays: []int32{1, 2, 3, 4, 5}}
	if err := s.portalOLApply(c, &w, portalMap(portalParam(c.in, "params"))); err != nil {
		return portalOut{}, err
	}
	if strings.TrimSpace(w.Name) == "" {
		w.Name = s.apiTr(c.r)("ol.new_widget")
	}
	id, err := s.OpenLines.SaveWidget(c.r.Context(), c.p.UserID, w)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: s.portalOLNum(c.r.Context(), "ol_widgets", id)}, nil
}

// imopenlines.config.update: CONFIG_ID, PARAMS — only the given settings change.
func (s *Server) portalOLConfigUpdate(c portalCall) (portalOut, error) {
	if err := s.portalOLAdmin(c); err != nil {
		return portalOut{}, err
	}
	id, err := s.portalOLLineID(c, portalParam(c.in, "config_id"))
	if err != nil {
		return portalOut{}, err
	}
	w, err := s.OpenLines.Widget(c.r.Context(), id)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalOLApply(c, &w, portalMap(portalParam(c.in, "params"))); err != nil {
		return portalOut{}, err
	}
	if _, err := s.OpenLines.SaveWidget(c.r.Context(), c.p.UserID, w); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

func (s *Server) portalOLConfigDelete(c portalCall) (portalOut, error) {
	if err := s.portalOLAdmin(c); err != nil {
		return portalOut{}, err
	}
	id, err := s.portalOLLineID(c, portalParam(c.in, "config_id"))
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.OpenLines.Widget(c.r.Context(), id); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.OpenLines.DeleteWidget(c.r.Context(), id)
}

// ---------- the dialogues ----------

// imopenlines.dialog.get: CHAT_ID, SESSION_ID, DIALOG_ID («chatN») or USER_CODE — the dialogue tells
// a chat of a line; owner — the operator's number (0 — nobody has it yet).
func (s *Server) portalOLDialog(c portalCall) (portalOut, error) {
	id, _, err := s.portalOLChat(c)
	if err != nil {
		return portalOut{}, err
	}
	x, err := s.portalOLSession(c.r.Context(), id)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: s.portalOLDialogOf(c.r.Context(), x)}, nil
}

func (s *Server) portalOLDialogOf(ctx context.Context, x openlines.Session) map[string]any {
	num := s.portalOLNum(ctx, "ol_sessions", x.ID)
	line := s.portalOLNum(ctx, "ol_widgets", x.WidgetID)
	conn := portalOLConnector(x.Channel)
	if x.Channel == "app" && x.ConnectorCode != "" { // an application's channel: its code and name
		conn = [2]string{x.ConnectorCode, x.Connector}
	}
	crmType, crmID := "", int64(0)
	if x.LeadID != "" {
		crmType, crmID = "LEAD", s.portalOLNum(ctx, "crm_leads", x.LeadID)
	} else if x.ContactID != "" {
		crmType, crmID = "CONTACT", s.portalOLNum(ctx, "crm_contacts", x.ContactID)
	}
	return map[string]any{
		"id": num, "dialog_id": "chat" + strconv.FormatInt(num, 10), "name": x.Title(), "type": "lines", "message_type": "L",
		"owner": s.portalOLNum(ctx, "users", x.OperatorID), "entity_type": "LINES",
		"entity_id":     fmt.Sprintf("%s|%d|%d|0", conn[0], line, num),
		"entity_data_1": fmt.Sprintf("%s|%s|%d|N|N|%d", portalYN(crmType != ""), crmType, crmID, num),
		"date_create":   x.CreatedAt.Format(time.RFC3339), "date_last": x.LastAt.Format(time.RFC3339),
		"session_id": num, "config_id": line, "status": x.Status, "spam": x.Spam, "pinned": x.Pinned, "waiting": x.Waiting,
		"connector_id": conn[0], "connector_title": conn[1],
		"client": map[string]string{"name": x.Name, "phone": x.Phone, "email": x.Email, "page": x.Page},
	}
}

// imopenlines.session.history.get: CHAT_ID or SESSION_ID — the dialogue's messages (it stays unread) as
// the history: message by its id, users by number (the client is 0), the head's rating.
func (s *Server) portalOLHistory(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	id, _, err := s.portalOLChat(c)
	if err != nil {
		return portalOut{}, err
	}
	x, err := s.portalOLSession(ctx, id)
	if err != nil {
		return portalOut{}, err
	}
	msgs, err := s.OpenLines.Messages(ctx, id, 0)
	if err != nil {
		return portalOut{}, err
	}
	authors := []string{}
	for _, m := range msgs {
		if m.AuthorID != "" && !slices.Contains(authors, m.AuthorID) {
			authors = append(authors, m.AuthorID)
		}
	}
	nums, err := s.portalOLNums(ctx, "users", authors)
	if err != nil {
		return portalOut{}, err
	}
	num := s.portalOLNum(ctx, "ol_sessions", id)
	users := map[string]any{"0": map[string]any{"id": 0, "name": x.Title(), "bot": false, "connector": true}}
	message, order := map[string]any{}, []int64{}
	for _, m := range msgs {
		author := nums[m.AuthorID]
		system := m.Kind == "note" || m.Auto()
		if m.AuthorID != "" {
			users[strconv.FormatInt(author, 10)] = map[string]any{"id": author, "name": m.Operator, "bot": false, "connector": false}
		}
		params := map[string]any{}
		if m.Kind == "hidden" {
			params["CLASS"] = "bx-messenger-content-item-system"
			params["IMOL_COMMENT_HIDDEN"] = "Y"
		}
		if m.FileName != "" {
			params["FILE"] = map[string]any{"name": m.FileName, "type": m.FileType, "size": m.FileSize,
				"url": strings.TrimRight(s.BaseURL, "/") + "/openlines/file/" + strconv.FormatInt(m.ID, 10)}
		}
		if len(m.Buttons) > 0 {
			params["KEYBOARD"] = m.Buttons
		}
		message[strconv.FormatInt(m.ID, 10)] = map[string]any{"id": m.ID, "chat_id": num, "author_id": author,
			"date": m.At.Format(time.RFC3339), "text": m.Body, "system": portalYN(system && m.AuthorID == ""), "params": params}
		order = append(order, m.ID)
	}
	can := s.olHead(c.r, auth.Session{UserID: c.p.UserID, Admin: s.apiAdmin(ctx, c.p.UserID)}, x.OperatorID)
	head := 0
	switch x.HeadVote {
	case 1:
		head = 5
	case -1:
		head = 1
	}
	return portalOut{Result: map[string]any{"chatId": num, "sessionId": num, "canJoin": portalYN(x.Status == "open"),
		"canVoteAsHead": portalYN(can), "sessionVoteHead": head, "sessionCommentHead": x.HeadComment,
		"message": message, "usersMessage": map[string]any{strconv.FormatInt(num, 10): order}, "users": users}}, nil
}

// portalOLDo runs an action on the dialogue of CHAT_ID; true when it is done.
func (s *Server) portalOLDo(c portalCall, do func(ctx context.Context, id string, who openlines.Who) error) (portalOut, error) {
	id, who, err := s.portalOLChat(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := do(c.r.Context(), id, who); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// imopenlines.session.intercept: the key's owner takes the dialogue.
func (s *Server) portalOLIntercept(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		return s.OpenLines.Intercept(ctx, c.p.UserID, id)
	})
}

// imopenlines.operator.answer: the key's owner takes the dialogue (their own already — nothing to do).
func (s *Server) portalOLAnswer(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		x, err := s.portalOLSession(ctx, id)
		if err != nil || x.OperatorID == c.p.UserID && x.Status == "open" {
			return err
		}
		return s.OpenLines.Intercept(ctx, c.p.UserID, id)
	})
}

// imopenlines.operator.skip: the operator turns the dialogue down — to the next one of the queue.
func (s *Server) portalOLSkip(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		return s.OpenLines.Skip(ctx, c.p.UserID, id)
	})
}

func (s *Server) portalOLSpam(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		return s.OpenLines.Spam(ctx, c.p.UserID, id)
	})
}

// imopenlines.operator.finish closes the operator's own dialogue; operator.another.finish — anyone's, for who sees
// every dialogue (administrators, the lines' heads).
func (s *Server) portalOLFinish(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, who openlines.Who) error {
		x, err := s.portalOLSession(ctx, id)
		if err != nil {
			return err
		}
		if c.method == "imopenlines.operator.another.finish" && !who.All || c.method == "imopenlines.operator.finish" && x.OperatorID != c.p.UserID {
			return i18n.E("err.forbidden")
		}
		return s.OpenLines.SetStatus(ctx, c.p.UserID, id, false)
	})
}

// imopenlines.session.start: the closed dialogue opens again.
func (s *Server) portalOLStart(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		return s.OpenLines.SetStatus(ctx, c.p.UserID, id, true)
	})
}

// imopenlines.operator.transfer: TRANSFER_ID — an employee's number or «queueN», the line N.
func (s *Server) portalOLTransfer(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		to := portalStr(portalParam(c.in, "transfer_id"))
		if line, ok := strings.CutPrefix(strings.ToLower(to), "queue"); ok {
			lineID, err := s.portalOLLineID(c, line)
			if err != nil {
				return err
			}
			return s.OpenLines.TransferLine(ctx, c.p.UserID, id, lineID)
		}
		if to == "" {
			return s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "TRANSFER_ID")
		}
		user, err := s.portalNum(ctx, "users", to)
		if err != nil {
			return err
		}
		return s.OpenLines.Transfer(ctx, c.p.UserID, id, user)
	})
}

// imopenlines.session.mode.pin: CHAT_ID, ACTIVATE (Y — pin, N — unpin), as «Закрепить за собой».
func (s *Server) portalOLPin(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		return s.OpenLines.Pin(ctx, c.p.UserID, id, portalYes(c.in, "activate", true))
	})
}

// imopenlines.session.mode.pinAll / unpinAll: all the key owner's open dialogues; the numbers of those changed.
func (s *Server) portalOLPinAll(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	on := c.method == "imopenlines.session.mode.pinall"
	rows, err := s.DB.Query(ctx, `SELECT id::text, num FROM ol_sessions WHERE operator_id = $1 AND status = 'open' AND pinned <> $2 ORDER BY num`, c.p.UserID, on)
	if err != nil {
		return portalOut{}, err
	}
	type chat struct {
		ID  string
		Num int64
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[chat])
	if err != nil {
		return portalOut{}, err
	}
	out := []int64{}
	for _, x := range list {
		if err := s.OpenLines.Pin(ctx, c.p.UserID, x.ID, on); err != nil {
			continue // closed or taken meanwhile
		}
		out = append(out, x.Num)
	}
	return portalOut{Result: out}, nil
}

// imopenlines.session.head.vote: SESSION_ID, RATING 1…5 (4 and 5 — 👍, 1 and 2 — 👎, 0 — no rating), COMMENT — the head's
// rating of the operator's work (administrators, the lines' heads, the operator's managers).
func (s *Server) portalOLHeadVote(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		rating, err := strconv.Atoi(portalStr(portalParam(c.in, "rating")))
		if err != nil || rating < 0 || rating > 5 || rating == 3 {
			return s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "RATING")
		}
		x, err := s.portalOLSession(ctx, id)
		if err != nil {
			return err
		}
		if !s.olHead(c.r, auth.Session{UserID: c.p.UserID, Admin: s.apiAdmin(ctx, c.p.UserID)}, x.OperatorID) {
			return i18n.E("err.forbidden")
		}
		vote := 0
		if rating >= 4 {
			vote = 1
		} else if rating > 0 {
			vote = -1
		}
		return s.OpenLines.HeadVote(ctx, c.p.UserID, id, vote, portalStr(portalParam(c.in, "comment")))
	})
}

// imopenlines.session.mode.silent: «Скрытые сообщения» are a message's kind in go24 (crm.message.add with
// HIDDEN = Y), so the mode only answers true.
func (s *Server) portalOLSilent(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(context.Context, string, openlines.Who) error { return nil })
}

// ---------- CRM ----------

// portalOLCard are the dialogues of the card CRM_ENTITY_TYPE (LEAD, CONTACT), CRM_ENTITY (its number) the key's owner
// sees, the latest first; activeOnly — the open ones.
type portalOLCardChat struct {
	ID      string
	Num     int64
	Channel string
}

func (s *Server) portalOLCard(c portalCall, activeOnly bool) ([]portalOLCardChat, openlines.Who, error) {
	ctx := c.r.Context()
	col := map[string]string{"lead": "lead_id", "contact": "contact_id"}[strings.ToLower(portalStr(portalParam(c.in, "crm_entity_type")))]
	if col == "" {
		return nil, openlines.Who{}, nil
	}
	card, err := s.portalNum(ctx, portalCRMTable(col[:len(col)-3]), portalParam(c.in, "crm_entity"))
	if err != nil || card == "" {
		return nil, openlines.Who{}, cmpErr(err, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "CRM_ENTITY"))
	}
	who, err := s.apiOLWho(ctx, c.p, "")
	if err != nil {
		return nil, who, err
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text, num, channel FROM ol_sessions WHERE `+col+`::text = $1 AND (status = 'open' OR NOT $2)
		ORDER BY last_at DESC`, card, activeOnly)
	if err != nil {
		return nil, who, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[portalOLCardChat])
	if err != nil {
		return nil, who, err
	}
	out := []portalOLCardChat{}
	for _, x := range list {
		if s.OpenLines.See(ctx, who, x.ID) == nil {
			out = append(out, x)
		}
	}
	return out, who, nil
}

// imopenlines.crm.chat.get: CRM_ENTITY_TYPE, CRM_ENTITY, ACTIVE_ONLY (Y — the open dialogues) — the card's dialogues
// the key's owner sees: [{CHAT_ID, CONNECTOR_ID, CONNECTOR_TITLE}], the latest first.
func (s *Server) portalOLCRMChats(c portalCall) (portalOut, error) {
	list, _, err := s.portalOLCard(c, portalYes(c.in, "active_only", false))
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, x := range list {
		conn := portalOLConnector(x.Channel)
		out = append(out, map[string]any{"CHAT_ID": x.Num, "CONNECTOR_ID": conn[0], "CONNECTOR_TITLE": conn[1]})
	}
	return portalOut{Result: out}, nil
}

// imopenlines.crm.chat.getLastId: CRM_ENTITY_TYPE, CRM_ENTITY — the number of the card's latest dialogue.
func (s *Server) portalOLCRMLastChat(c portalCall) (portalOut, error) {
	list, _, err := s.portalOLCard(c, false)
	if err != nil {
		return portalOut{}, err
	}
	if len(list) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "CHAT_NOT_FOUND", "err.not_found")
	}
	return portalOut{Result: list[0].Num}, nil
}

// portalOLCardUser is the card's dialogue (CHAT_ID, one of the card's; by default the latest) and the employee USER_ID.
func (s *Server) portalOLCardUser(c portalCall) (portalOLCardChat, string, openlines.Who, error) {
	list, who, err := s.portalOLCard(c, false)
	if err != nil {
		return portalOLCardChat{}, "", who, err
	}
	user, err := s.portalNum(c.r.Context(), "users", portalParam(c.in, "user_id"))
	if err != nil || user == "" {
		return portalOLCardChat{}, "", who, cmpErr(err, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "USER_ID"))
	}
	n := portalStr(portalParam(c.in, "chat_id"))
	for _, x := range list {
		if n == "" || n == "0" || strconv.FormatInt(x.Num, 10) == n {
			return x, user, who, nil
		}
	}
	return portalOLCardChat{}, "", who, s.portalErr(c.r, http.StatusBadRequest, "CHAT_NOT_FOUND", "err.not_found")
}

// imopenlines.crm.chat.user.add: CRM_ENTITY_TYPE, CRM_ENTITY, USER_ID, CHAT_ID — the employee is invited into the
// card's dialogue, as «Пригласить в диалог»; the dialogue's number.
func (s *Server) portalOLCRMUserAdd(c portalCall) (portalOut, error) {
	x, user, _, err := s.portalOLCardUser(c)
	if err == nil {
		err = s.OpenLines.Invite(c.r.Context(), c.p.UserID, x.ID, user)
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: x.Num}, nil
}

// imopenlines.crm.chat.user.delete: the same — the employee leaves the card's dialogue (the key's owner themselves,
// or its operator, a line's head, an administrator removes them).
func (s *Server) portalOLCRMUserDelete(c portalCall) (portalOut, error) {
	x, user, who, err := s.portalOLCardUser(c)
	if err == nil {
		err = s.OpenLines.Uninvite(c.r.Context(), who, x.ID, user)
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

func cmpErr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

// imopenlines.crm.lead.create: CHAT_ID — a lead of the dialogue, or its binding to the client CRM already knows by the
// visitor's phone or e-mail.
func (s *Server) portalOLLead(c portalCall) (portalOut, error) {
	if !c.p.Can("crm:write") {
		return portalOut{}, i18n.E("err.api_no_scope", "crm:write")
	}
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		if err := s.CRM.Check(ctx, c.p.UserID, "lead", "add", c.p.UserID); err != nil {
			return err
		}
		_, err := s.OpenLines.MakeLead(ctx, c.p.UserID, id, s.apiTr(c.r)("widget.source"))
		return err
	})
}

// imopenlines.crm.message.add: CHAT_ID, MESSAGE — the key owner's answer in the dialogue (the first to answer takes
// it); HIDDEN = Y — a hidden one, the client does not get it. USER_ID, when given, is the key's owner. The message's id.
func (s *Server) portalOLMessageAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	if u := portalStr(portalParam(c.in, "user_id")); u != "" {
		if id, err := s.portalNum(ctx, "users", u); err != nil || id != c.p.UserID {
			return portalOut{}, i18n.E("err.forbidden")
		}
	}
	text := portalStr(portalParam(c.in, "message"))
	if strings.TrimSpace(text) == "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "MESSAGE_EMPTY", "err.portal_message")
	}
	id, _, err := s.portalOLChat(c)
	if err != nil {
		return portalOut{}, err
	}
	if portalYes(c.in, "hidden", false) {
		err = s.OpenLines.Hide(ctx, c.p.UserID, id, text, nil)
	} else {
		err = s.OpenLines.Reply(ctx, c.p.UserID, id, text)
	}
	if err != nil {
		return portalOut{}, err
	}
	var msg int64
	err = s.DB.QueryRow(ctx, `SELECT id FROM ol_messages WHERE session_id::text = $1 AND author_id = $2 ORDER BY id DESC LIMIT 1`, id, c.p.UserID).Scan(&msg)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalOut{}, openlines.ErrNotFound
	}
	return portalOut{Result: msg}, err
}

// imopenlines.session.join: CHAT_ID — the key's owner joins a dialogue they see, as an invited colleague.
func (s *Server) portalOLJoin(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		return s.OpenLines.Invite(ctx, c.p.UserID, id, c.p.UserID)
	})
}

// imopenlines.message.quick.save: CHAT_ID, MESSAGE_ID — the dialogue's message becomes the key owner's own quick
// answer, its first line the title.
func (s *Server) portalOLQuickSave(c portalCall) (portalOut, error) {
	return s.portalOLDo(c, func(ctx context.Context, id string, _ openlines.Who) error {
		var body string
		err := s.DB.QueryRow(ctx, `SELECT body FROM ol_messages WHERE session_id::text = $1 AND id::text = $2`, id, portalStr(portalParam(c.in, "message_id"))).Scan(&body)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && strings.TrimSpace(body) == "" {
			return s.portalErr(c.r, http.StatusBadRequest, "MESSAGE_NOT_FOUND", "err.not_found")
		}
		if err != nil {
			return err
		}
		title := []rune(strings.TrimSpace(strings.SplitN(strings.TrimSpace(body), "\n", 2)[0]))
		if len(title) > 100 {
			title = append(title[:99], '…')
		}
		_, err = s.OpenLines.SaveQuick(ctx, c.p.UserID, false, openlines.Quick{Title: string(title), Body: body})
		return err
	})
}
