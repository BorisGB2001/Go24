package web

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/ai"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// ---------- «Детализация звонков»: the log of the PBX's calls and the recorded conversations ----------

// phoneCDR: Asterisk tells about a call that ended (its dialplan, go24-cdr; the key is ASTERISK_SECRET).
func (s *Server) phoneCDR(w http.ResponseWriter, r *http.Request) {
	if s.Telephony == nil || s.Telephony.Key == "" || subtle.ConstantTimeCompare([]byte(r.FormValue("key")), []byte(s.Telephony.Key)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	c := telephony.CDR{UniqueID: r.FormValue("id"), From: r.FormValue("from"), To: r.FormValue("to"), Dial: r.FormValue("dial"),
		By: r.FormValue("by"), Line: r.FormValue("line"),
		Record: r.FormValue("rec"), Start: epoch(r.FormValue("start")), Answer: epoch(r.FormValue("answer")), End: epoch(r.FormValue("end")),
		Voicemail: r.FormValue("vm") == "1", XFrom: r.FormValue("xfrom"), XTo: r.FormValue("xto"),
		Mobile: r.FormValue("mob")}
	c.Rating, _ = strconv.Atoi(r.FormValue("rate"))
	if c.Voicemail && c.Record != "" && !s.phoneMessage(c.Record) { // the client hung up before the beep
		c.Record = ""
	}
	id, err := s.Telephony.SaveCall(r.Context(), c)
	if err != nil {
		s.Log.Error("telephony cdr", "err", err, "call", c.UniqueID)
		http.Error(w, "cdr", http.StatusBadRequest)
		return
	}
	if st := c.Status(); c.Record != "" && st != telephony.StatusAnswered && st != telephony.StatusVoicemail && s.Telephony.Records != "" { // silence of a call nobody answered
		if err := os.Remove(filepath.Join(s.Telephony.Records, filepath.Base(c.Record))); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.Log.Warn("telephony record", "err", err)
		}
	}
	if err := s.Telephony.TakeNote(r.Context(), id); err != nil { // the comment written while talking
		s.Log.Error("telephony note", "err", err, "call", id)
	}
	if err := s.phoneToCRM(r.Context(), id); err != nil { // the client's card gets the call
		s.Log.Error("telephony crm", "err", err, "call", id)
	}
	if err := s.phoneMissed(r.Context(), id); err != nil {
		s.Log.Error("telephony notify", "err", err, "call", id)
	}
	if err := s.phoneEvents(r.Context(), id); err != nil {
		s.Log.Error("telephony events", "err", err, "call", id)
	}
	if id != "" && s.phoneDriveOn(r.Context(), c) { // «Записанные звонки» on the company's drive
		go s.phoneDriveLater(id)
	}
	if st := c.Status(); id != "" && c.Record != "" && (st == telephony.StatusAnswered || st == telephony.StatusVoicemail) && s.Speech != nil {
		if on, err := s.Telephony.Transcribing(r.Context()); err != nil {
			s.Log.Error("telephony transcribing", "err", err)
		} else if on {
			go s.phoneTranscribeLater(id)
		}
	}
	s.Log.Info("telephony call", "id", id, "from", c.From, "to", c.To, "status", c.Status())
	w.WriteHeader(http.StatusNoContent)
}

// phoneMessage tells whether the voicemail's file has a message (more than its header and a moment of sound).
func (s *Server) phoneMessage(rec string) bool {
	if s.Telephony.Records == "" || strings.ContainsAny(rec, "/\\") {
		return false
	}
	st, err := os.Stat(filepath.Join(s.Telephony.Records, rec))
	return err == nil && st.Size() > 44+16000/2 // half a second of 8 kHz 16-bit
}

// phoneMissed tells the employee of a call from an external line nobody answered about it «Пропущенный
// звонок»), or about the client's voicemail.
func (s *Server) phoneMissed(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	c, err := s.Telephony.CallByID(ctx, id)
	if err != nil {
		return err
	}
	number, dir, emp := c.Outside()
	if c.Line == "" || dir != "in" || emp == "" || !c.Missed() {
		return nil
	}
	who := number
	if c.CRMName != "" {
		who += " (" + c.CRMName + ")"
	}
	key := "tel.notify.missed"
	if c.Status == telephony.StatusVoicemail {
		key = "tel.notify.voicemail"
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'phone', left($2, 300), $3)`,
		emp, s.portalTr(key, who, c.Line), "/telephony/calls?kind=missed&q="+url.QueryEscape(number))
	return err
}

// phoneEvents tells the applications (ONVOXIMPLANTCALLEND sends it) and the webhooks ("call.ended")
// about a call that ended, once its comment and client card are known.
func (s *Server) phoneEvents(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	list, err := s.Telephony.Calls(ctx, telephony.CallFilter{ID: id, Limit: 1})
	if err != nil || len(list) == 0 {
		return err
	}
	c := s.apiTelCallOf(list[0], "", false)
	// CALL_TYPE: 1 — outgoing, 2 — incoming; CALL_FAILED_CODE: 200 — answered, 304 — missed, 486 — busy
	typ, code := 1, "200"
	if c.Direction == "in" {
		typ = 2
	}
	switch c.Status {
	case telephony.StatusAnswered:
	case telephony.StatusBusy:
		code = "486"
	case telephony.StatusFailed:
		code = "603"
	default:
		code = "304"
	}
	emp := ""
	switch c.Direction {
	case "in":
		if c.ToUser != nil {
			emp = c.ToUser.ID
		}
	default:
		if c.FromUser != nil {
			emp = c.FromUser.ID
		}
	}
	number := c.PhoneNumber
	if number == "" {
		number = c.To
	}
	data := map[string]any{"CALL_ID": c.ID, "CALL_TYPE": typ, "PHONE_NUMBER": number, "PORTAL_NUMBER": c.Line,
		"PORTAL_USER_ID": emp, "CALL_DURATION": c.Seconds, "CALL_START_DATE": c.StartedAt.Format(time.RFC3339),
		"CALL_FAILED_CODE": code, "CALL_STATUS": c.Status}
	if c.CRM != nil {
		data["CRM_ENTITY_TYPE"], data["CRM_ENTITY_ID"] = strings.ToUpper(c.CRM.Entity), c.CRM.ID
	}
	ev, err := json.Marshal(data)
	if err != nil {
		return err
	}
	hook, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `SELECT go24_app_event('ONVOXIMPLANTCALLEND', $1::jsonb)`, string(ev)); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `SELECT go24_webhook('call.ended', $1::jsonb)`, string(hook))
	return err
}

// epoch is a time Asterisk gives as seconds since 1970 ("1729870000.123456"); zero — none ("0.000000", "").
func epoch(v string) time.Time {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return time.Time{}
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9))
}

// phoneCallRow is a line of the log as the one looking sees it: their call out, in, missed, or others' (inner).
type phoneCallRow struct {
	telephony.Call
	Kind                 string
	FromPhoto, ToPhoto   string
	CanListen, HasRecord bool
	Mine                 bool   // the one looking was on the call (or is an administrator): «Создать лид / сделку»
	Outside              string // the outside number of the call ("" — between employees)
	Blocked              bool   // the outside number is on the black list (shown to administrators)
}

// phoneCalls is «Детализация звонков»: the calls the right «calls» shows — the employee's own, their departments'
// or everyone's (or one's: user=); filters: kind (in, out, missed), the period, a number, the line. ?format=csv — all
// the calls of the filter for Excel.
func (s *Server) phoneCalls(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/my")
		return
	}
	perms := s.telPerms(r.Context(), sess)
	if !perms.Has("calls", "own") {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/my")
		return
	}
	q := r.URL.Query()
	f := telephony.CallFilter{Kind: q.Get("kind"), Number: q.Get("q"), Line: q.Get("line"), Limit: 50}
	var err error
	if f.UserID, f.Users, err = s.telScope(r.Context(), sess, perms["calls"], q.Get("user")); err != nil {
		s.fail(w, "telephony team", err)
		return
	}
	admin := s.telAdmin(r.Context(), sess)
	switch f.Kind {
	case "in", "out", "missed":
	default:
		f.Kind = ""
	}
	if d, err := time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local); err == nil {
		f.From = d
	}
	if d, err := time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local); err == nil {
		f.To = d.AddDate(0, 0, 1)
	}
	if t, err := time.Parse(time.RFC3339Nano, q.Get("before")); err == nil {
		f.Before = t
	}
	if q.Get("format") == "csv" {
		s.phoneCallsCSV(w, r, f)
		return
	}
	list, err := s.Telephony.Calls(r.Context(), f)
	if err != nil {
		s.fail(w, "telephony calls", err)
		return
	}
	me := f.UserID
	if me == "" {
		me = sess.UserID
	}
	black := map[string]bool{}
	if admin {
		if black, err = s.Telephony.BlackKeys(r.Context()); err != nil {
			s.fail(w, "telephony black list", err)
			return
		}
	}
	var team []string // whose records the level «dept» lets hear
	if perms["records"] == "dept" {
		if team, err = s.Telephony.Team(r.Context(), sess.UserID); err != nil {
			s.fail(w, "telephony team", err)
			return
		}
	}
	rows := make([]phoneCallRow, len(list))
	for i, c := range list {
		row := phoneCallRow{Call: c, Kind: "inner", HasRecord: c.Record != "" && s.Telephony.Records != ""}
		switch me {
		case c.FromUser.UserID:
			row.Kind = "out"
		case c.ToUser.UserID:
			row.Kind = "in"
			if c.Missed() {
				row.Kind = "missed"
			}
		}
		if c.FromUser.UserID != "" {
			row.FromPhoto = company.PhotoURL(c.FromUser.UserID, c.FromUser.PhotoKey)
		}
		if c.ToUser.UserID != "" {
			row.ToPhoto = company.PhotoURL(c.ToUser.UserID, c.ToUser.PhotoKey)
		}
		row.Mine = perms["calls"] != "own" || c.FromUser.UserID == sess.UserID || c.ToUser.UserID == sess.UserID
		row.CanListen = row.HasRecord && phoneHearsBy(perms, sess, c, team, nil)
		row.Outside, _, _ = c.Outside()
		row.Blocked = row.Outside != "" && black[telephony.BlackKey(row.Outside)]
		rows[i] = row
	}
	more := ""
	if len(list) == f.Limit {
		keep := url.Values{}
		for k, v := range q {
			keep[k] = v
		}
		keep.Set("before", list[len(list)-1].Started.Format(time.RFC3339Nano))
		more = "/telephony/calls?" + keep.Encode()
	}
	csvQuery := url.Values{"format": {"csv"}}
	for k, v := range q {
		if k != "before" {
			csvQuery[k] = v
		}
	}
	lines, err := s.phoneLines(r.Context())
	if err != nil {
		s.fail(w, "telephony lines", err)
		return
	}
	data := map[string]any{"Calls": rows, "Filter": f, "From": q.Get("from"), "To": q.Get("to"), "More": more,
		"Admin": admin, "CRM": s.CRM != nil, "Back": r.URL.RequestURI(), "Speech": s.Speech != nil, "AI": s.aiOn(r.Context(), sess.UserID, "telephony"),
		"Monitor": perms.Has("monitor", "dept"), "Lines": lines, "CSV": "/telephony/calls?" + csvQuery.Encode()}
	if s.AI != nil {
		if data["Scripts"], err = s.Telephony.Scripts(r.Context()); err != nil {
			s.fail(w, "telephony scripts", err)
			return
		}
	}
	if perms["calls"] != "own" {
		if data["Users"], err = s.telUsers(r.Context(), f.Users); err != nil {
			s.fail(w, "telephony users", err)
			return
		}
	}
	s.render(w, r, "phonecalls", page{TitleKey: "tel.calls", Nav: "telephony_calls", Data: data})
}

// phoneLine is an external line of the filter «Линия»: go24's own (a SIP trunk) or an outside PBX's.
type phoneLine struct{ Number, Name string }

// phoneLines are the external lines with a number, go24's first.
func (s *Server) phoneLines(ctx context.Context) ([]phoneLine, error) {
	trunks, err := s.Telephony.Trunks(ctx)
	if err != nil {
		return nil, err
	}
	ext, err := s.Telephony.ExtLines(ctx)
	if err != nil {
		return nil, err
	}
	var out []phoneLine
	seen := map[string]bool{}
	add := func(number, name string) {
		if number != "" && !seen[number] {
			seen[number] = true
			out = append(out, phoneLine{number, name})
		}
	}
	for _, t := range trunks {
		add(t.Number, t.Name)
	}
	for _, l := range ext {
		add(l.Number, l.Name)
	}
	return out, nil
}

// phoneCallsCSV writes all the calls of the filter (newest first, up to phoneCSVMax) for Excel.
func (s *Server) phoneCallsCSV(w http.ResponseWriter, r *http.Request, f telephony.CallFilter) {
	var list []telephony.Call
	f.Limit = 500
	for len(list) < phoneCSVMax {
		page, err := s.Telephony.Calls(r.Context(), f)
		if err != nil {
			s.fail(w, "telephony calls", err)
			return
		}
		list = append(list, page...)
		if len(page) < f.Limit {
			break
		}
		f.Before = page[len(page)-1].Started
	}
	tr := func(k string) string { return s.tr(r, k) }
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=calls-"+time.Now().Format("2006-01-02")+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{tr("tel.col.date"), tr("tel.col.kind"), tr("tel.col.from"), tr("tel.csv.from_number"), tr("tel.col.to"),
		tr("tel.csv.to_number"), tr("tel.col.line"), tr("tel.col.status"), tr("tel.col.duration"), tr("tel.csv.seconds"),
		tr("tel.csv.client"), tr("tel.csv.transferred"), tr("tel.comment"), tr("tel.csv.rating"), tr("tel.score")})
	for _, c := range list {
		kind := "inner"
		if _, dir, _ := c.Outside(); dir != "" {
			kind = dir
			if dir == "in" && c.Missed() {
				kind = "missed"
			}
		}
		transferred, rating, score := "", "", ""
		if c.XTo != "" {
			transferred = cmp.Or(c.XFromName, c.XFrom) + " → " + cmp.Or(c.XToName, c.XTo)
		}
		if c.Rating > 0 {
			rating = strconv.Itoa(c.Rating)
		}
		if c.Score >= 0 {
			score = strconv.Itoa(c.Score) + "%"
		}
		_ = cw.Write([]string{c.Started.In(time.Local).Format("02.01.2006 15:04:05"), tr("tel.kind." + kind), c.FromUser.Name, c.From,
			c.ToUser.Name, c.To, c.Line, tr("tel.status." + c.Status), c.Duration(), strconv.Itoa(c.Seconds),
			c.CRMName, transferred, c.Comment, rating, score})
	}
	cw.Flush()
}

// phoneCSVMax bounds the calls of one export.
const phoneCSVMax = 50000

// phoneRecord gives the recorded conversation (to listen, or download=1) to its employees and administrators.
func (s *Server) phoneRecord(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Telephony == nil || s.Telephony.Records == "" {
		http.NotFound(w, r)
		return
	}
	c, err := s.Telephony.CallByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && c.Record == "") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony record", err)
		return
	}
	if !s.phoneHears(r.Context(), sess, c) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	name := filepath.Base(c.Record)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	w.Header().Set("Content-Type", recordType(name))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, filepath.Join(s.Telephony.Records, name))
}

// recordType is the type of a record's file: Asterisk's are wav, an outside PBX's may be mp3 or ogg.
func recordType(name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); strings.HasPrefix(t, "audio/") {
		return t
	}
	return "audio/wav"
}

// phoneHears tells whether the employee may listen to the call's record (and read its text) by the right «records»:
// own — they were on the call or see the client's card in whose timeline the call is; dept — or someone of their
// departments was on it; all — any call. Administrators in admin mode hear all.
func (s *Server) phoneHears(ctx context.Context, sess auth.Session, c telephony.Call) bool {
	perms := s.telPerms(ctx, sess)
	var team []string
	if perms["records"] == "dept" {
		team, _ = s.Telephony.Team(ctx, sess.UserID)
	}
	return phoneHearsBy(perms, sess, c, team, func() bool { return s.crmCanRead(ctx, sess.UserID, c.CRMEntity, c.CRMID) })
}

// phoneHearsBy is phoneHears by the rights; team are the people of the level «dept», card (nil — not asked) tells
// whether the person sees the call's card.
func phoneHearsBy(perms telephony.Perms, sess auth.Session, c telephony.Call, team []string, card func() bool) bool {
	switch perms["records"] {
	case "all":
		return true
	case "none":
		return false
	case "dept":
		if c.FromUser.UserID != "" && slices.Contains(team, c.FromUser.UserID) || c.ToUser.UserID != "" && slices.Contains(team, c.ToUser.UserID) {
			return true
		}
	}
	return c.FromUser.UserID == sess.UserID || c.ToUser.UserID == sess.UserID || (card != nil && card())
}

// phoneTranscript: «Расшифровать» — the record's text made by Whisper and kept; back to where the button was.
func (s *Server) phoneTranscript(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/telephony/calls"
	if ref, err := url.Parse(r.Referer()); err == nil && strings.HasPrefix(ref.Path, "/") && !strings.HasPrefix(ref.Path, "//") {
		back = ref.RequestURI()
	}
	if s.Telephony == nil || s.Telephony.Records == "" {
		http.NotFound(w, r)
		return
	}
	c, err := s.Telephony.CallByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && c.Record == "") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony transcript", err)
		return
	}
	if !s.phoneHears(r.Context(), sess, c) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if c.Transcript == "" {
		if err := s.phoneTranscribe(r.Context(), c); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// phoneTranscribe makes the text of the call's record by Whisper (in the portal's language) and keeps it.
func (s *Server) phoneTranscribe(ctx context.Context, c telephony.Call) error {
	if s.Speech == nil {
		return i18n.E("err.speech_off")
	}
	f, err := os.Open(filepath.Join(s.Telephony.Records, filepath.Base(c.Record)))
	if err != nil {
		return err
	}
	defer f.Close()
	lang, _, _ := strings.Cut(s.portalLang(), "-")
	text, err := s.Speech.Transcribe(ctx, filepath.Base(c.Record), io.LimitReader(f, maxRecord), lang)
	if err != nil {
		s.Log.Error("whisper", "err", err, "call", c.ID)
		return i18n.E("err.speech_failed")
	}
	if text == "" {
		text = s.portalTr("im.voice_silent")
	}
	return s.Telephony.SetTranscript(ctx, c.ID, text)
}

// phoneTranscribeLater makes the text of a new call's record after Asterisk has been answered (Whisper takes its time).
func (s *Server) phoneTranscribeLater(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	c, err := s.Telephony.CallByID(ctx, id)
	if err == nil && c.Record != "" && c.Transcript == "" {
		err = s.phoneTranscribe(ctx, c)
	}
	if err != nil {
		s.Log.Error("telephony transcript", "err", err, "call", id)
		return
	}
	if s.AI == nil {
		return
	}
	if on, err := s.Telephony.Summarizing(ctx); err == nil && on {
		if c, err = s.Telephony.CallByID(ctx, id); err == nil {
			err = s.phoneSummarize(ctx, c, "")
		}
		if err != nil && !errors.Is(err, ai.ErrNone) {
			s.Log.Error("telephony summary", "err", err, "call", id)
		}
	}
	s.phoneFillAuto(ctx, id)
	if c, err = s.Telephony.CallByID(ctx, id); err != nil || c.Score >= 0 || strings.TrimSpace(c.Transcript) == "" || c.Transcript == s.portalTr("im.voice_silent") {
		return
	}
	_, dir, _ := c.Outside()
	sc, ok, err := s.Telephony.AutoScript(ctx, dir)
	if err == nil && ok && dir != "" {
		err = s.phoneScore(ctx, c, sc, "")
	}
	if err != nil && !errors.Is(err, ai.ErrNone) {
		s.Log.Error("telephony score", "err", err, "call", id)
	}
}

// phoneAssess: «Оценить» — CoPilot scores the conversation by the chosen script through the AI of the one who asks;
// back to where the button was.
func (s *Server) phoneAssess(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/telephony/calls"
	if ref, err := url.Parse(r.Referer()); err == nil && strings.HasPrefix(ref.Path, "/") && !strings.HasPrefix(ref.Path, "//") {
		back = ref.RequestURI()
	}
	if s.Telephony == nil || s.Telephony.Records == "" {
		http.NotFound(w, r)
		return
	}
	c, err := s.Telephony.CallByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && c.Record == "") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony score", err)
		return
	}
	if !s.phoneHears(r.Context(), sess, c) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	sc, err := s.Telephony.ScriptByID(r.Context(), r.FormValue("script"))
	if err == nil {
		err = s.phoneScore(r.Context(), c, sc, sess.UserID)
	} else if errors.Is(err, telephony.ErrNotFound) {
		err = i18n.E("err.script_none")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// phoneScore has the AI score the conversation by the script from the record's text (made first if there is none) and
// keeps the score; the client's CRM card gets it in its timeline, a low score is told to the employee's managers. The
// AI is that of userID, "" — of the employee who talked.
func (s *Server) phoneScore(ctx context.Context, c telephony.Call, sc telephony.Script, userID string) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	if c.Transcript == "" {
		if err := s.phoneTranscribe(ctx, c); err != nil {
			return err
		}
		var err error
		if c, err = s.Telephony.CallByID(ctx, c.ID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Transcript) == "" || c.Transcript == s.portalTr("im.voice_silent") {
		return i18n.E("err.tel_summary_silent")
	}
	number, dir, emp := c.Outside()
	if emp == "" {
		emp = cmp.Or(c.FromUser.UserID, c.ToUser.UserID)
	}
	if userID == "" {
		userID = emp
	}
	if userID == "" {
		return ai.ErrNone
	}
	lang := s.portalLang()
	about := s.I18n.T(lang, "tel.summary_call", c.From, c.To, c.Duration())
	if dir != "" {
		about += "\n" + s.I18n.T(lang, "tel.score_dir."+dir)
	}
	reply, err := s.AI.Write(ctx, userID, "telephony", s.I18n.T(lang, "tel.score_prompt"),
		s.I18n.T(lang, "tel.score_script", sc.Name)+"\n"+sc.Body+"\n\n"+about+"\n\n"+c.Transcript)
	if err != nil {
		return err
	}
	score, text, ok := telephony.ParseScore(reply)
	if !ok {
		return i18n.E("err.ai_call", "no score")
	}
	if err := s.Telephony.SetScore(ctx, c.ID, sc, score, text); err != nil {
		return err
	}
	when := c.Started.Local().Format(s.I18n.T(lang, "format.datetime"))
	if c.CRMEntity != "" && c.CRMID != "" && s.CRM != nil && c.Score < 0 {
		body := s.I18n.T(lang, "tel.score_crm", when, number, sc.Name, score) + "\n" + text
		if err := s.CRM.AddEvent(ctx, c.CRMEntity, c.CRMID, cmp.Or(emp, userID), "comment", body); err != nil {
			s.Log.Warn("telephony score crm", "err", err, "call", c.ID)
		}
	}
	if score < sc.Low && emp != "" && s.Company != nil {
		heads, err := s.Company.Managers(ctx, emp)
		if err != nil {
			return err
		}
		name := ""
		if p, err := s.Company.Profile(ctx, emp); err == nil {
			name = cmp.Or(p.Name, p.Email)
		}
		body := s.I18n.T(lang, "tel.score_low", score, sc.Name, name, cmp.Or(number, c.To), when)
		for _, h := range heads {
			if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'phone_score', left($2, 300), $3)`,
				h.ID, body, "/telephony/calls?user="+emp); err != nil {
				s.Log.Warn("telephony score notify", "err", err, "call", c.ID)
			}
		}
	}
	return nil
}

// phoneSummary: «Подвести итоги» — CoPilot sums the conversation up from the record's text through the AI of the one
// who asks; back to where the button was.
func (s *Server) phoneSummary(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/telephony/calls"
	if ref, err := url.Parse(r.Referer()); err == nil && strings.HasPrefix(ref.Path, "/") && !strings.HasPrefix(ref.Path, "//") {
		back = ref.RequestURI()
	}
	if s.Telephony == nil || s.Telephony.Records == "" {
		http.NotFound(w, r)
		return
	}
	c, err := s.Telephony.CallByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && c.Record == "") {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony summary", err)
		return
	}
	if !s.phoneHears(r.Context(), sess, c) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.phoneSummarize(r.Context(), c, sess.UserID); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// phoneSummarize has the AI write the summary of the conversation from the record's text (made first if there is
// none) and keeps it; the client's CRM card gets it in its timeline. The AI is that of userID, "" — of the employee
// who talked.
func (s *Server) phoneSummarize(ctx context.Context, c telephony.Call, userID string) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	if c.Transcript == "" {
		if err := s.phoneTranscribe(ctx, c); err != nil {
			return err
		}
		var err error
		if c, err = s.Telephony.CallByID(ctx, c.ID); err != nil {
			return err
		}
	}
	if strings.TrimSpace(c.Transcript) == "" || c.Transcript == s.portalTr("im.voice_silent") {
		return i18n.E("err.tel_summary_silent")
	}
	number, dir, emp := c.Outside()
	if emp == "" {
		emp = cmp.Or(c.FromUser.UserID, c.ToUser.UserID)
	}
	if userID == "" {
		userID = emp
	}
	if userID == "" {
		return ai.ErrNone
	}
	lang := s.portalLang()
	about := s.I18n.T(lang, "tel.summary_call", c.From, c.To, c.Duration())
	if dir != "" && c.CRMName != "" {
		about += "\n" + s.I18n.T(lang, "tel.summary_client", c.CRMName, number)
	}
	text, err := s.AI.Write(ctx, userID, "telephony", s.I18n.T(lang, "tel.summary_prompt"), about+"\n\n"+c.Transcript)
	if err != nil {
		return err
	}
	if text == "" {
		return i18n.E("err.ai_call", "empty")
	}
	if err := s.Telephony.SetSummary(ctx, c.ID, text); err != nil {
		return err
	}
	if c.CRMEntity != "" && c.CRMID != "" && s.CRM != nil && c.Summary == "" {
		body := s.I18n.T(lang, "tel.summary_crm", c.Started.Local().Format(s.I18n.T(lang, "format.datetime")), number) + "\n" + text
		if err := s.CRM.AddEvent(ctx, c.CRMEntity, c.CRMID, cmp.Or(emp, userID), "comment", body); err != nil {
			s.Log.Warn("telephony summary crm", "err", err, "call", c.ID)
		}
	}
	return nil
}

// maxRecord is the longest record go24 sends to Whisper (about an hour of 8 kHz WAV).
const maxRecord = 64 << 20
