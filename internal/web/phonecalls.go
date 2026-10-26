package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
		Voicemail: r.FormValue("vm") == "1"}
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
	if err := s.phoneToCRM(r.Context(), id); err != nil { // the client's card gets the call
		s.Log.Error("telephony crm", "err", err, "call", id)
	}
	if err := s.phoneMissed(r.Context(), id); err != nil {
		s.Log.Error("telephony notify", "err", err, "call", id)
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
}

// phoneCalls is «Детализация звонков»: an employee sees their calls, administrators — everyone's (or one's: user=);
// filters: kind (in, out, missed), the period, a number.
func (s *Server) phoneCalls(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/my")
		return
	}
	q := r.URL.Query()
	f := telephony.CallFilter{UserID: q.Get("user"), Kind: q.Get("kind"), Number: q.Get("q"), Limit: 50}
	if !sess.AdminActive() {
		f.UserID = sess.UserID
	}
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
	list, err := s.Telephony.Calls(r.Context(), f)
	if err != nil {
		s.fail(w, "telephony calls", err)
		return
	}
	me := f.UserID
	if me == "" {
		me = sess.UserID
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
		row.Mine = sess.AdminActive() || c.FromUser.UserID == sess.UserID || c.ToUser.UserID == sess.UserID
		row.CanListen = row.HasRecord && row.Mine
		row.Outside, _, _ = c.Outside()
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
	data := map[string]any{"Calls": rows, "Filter": f, "From": q.Get("from"), "To": q.Get("to"), "More": more,
		"Admin": sess.AdminActive(), "CRM": s.CRM != nil}
	if sess.AdminActive() {
		if data["Users"], err = s.Tasks.ListUsers(r.Context()); err != nil {
			s.fail(w, "telephony users", err)
			return
		}
	}
	s.render(w, r, "phonecalls", page{TitleKey: "tel.calls", Nav: "telephony_calls", Data: data})
}

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
	if !sess.AdminActive() && c.FromUser.UserID != sess.UserID && c.ToUser.UserID != sess.UserID &&
		!s.crmCanRead(r.Context(), sess.UserID, c.CRMEntity, c.CRMID) { // the client's card, in whose timeline the call is
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	name := filepath.Base(c.Record)
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, filepath.Join(s.Telephony.Records, name))
}
