package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// ---------- «Текущие звонки»: the conversations going on and joining them (the right «monitor») ----------

// phoneActiveRow is a call going on as the page shows it.
type phoneActiveRow struct {
	telephony.Active
	Seconds int
	Mine    bool // the person's own call: they do not join it
}

// phoneMonitored are whose calls the right «monitor» shows the person: nil — everyone's; false — none.
func (s *Server) phoneMonitored(ctx context.Context, sess auth.Session) ([]string, bool, error) {
	switch s.telPerms(ctx, sess)["monitor"] {
	case "all":
		return nil, true, nil
	case "dept":
		team, err := s.Telephony.Team(ctx, sess.UserID)
		return team, err == nil, err
	}
	return nil, false, nil
}

// phoneActive are the calls going on the person may join (their own ones too, to see them), the longest first.
func (s *Server) phoneActive(ctx context.Context, sess auth.Session) ([]telephony.Active, error) {
	if s.Telephony == nil {
		return nil, i18n.E("err.telephony_off")
	}
	users, ok, err := s.phoneMonitored(ctx, sess)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, i18n.E("err.forbidden")
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	list, err := s.Telephony.Active(actx, users)
	if err != nil {
		s.Log.Error("telephony active", "err", err)
		return nil, i18n.E("tel.err_asterisk")
	}
	return list, nil
}

// phoneSpy rings the person's phones and puts the one who answers on the call of the channel in the mode.
func (s *Server) phoneSpy(ctx context.Context, sess auth.Session, channel, mode string) error {
	if !slices.Contains(telephony.SpyModes, mode) {
		mode = "listen"
	}
	list, err := s.phoneActive(ctx, sess)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(a telephony.Active) bool { return a.Channel == channel })
	if i < 0 {
		return i18n.E("tel.spy_gone")
	}
	if list[i].UserID == sess.UserID {
		return i18n.E("err.forbidden")
	}
	l, ok := s.phoneLine(ctx, sess.UserID)
	if !ok || l.Dup {
		return i18n.E("err.telephony_no_number")
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Telephony.ARI.Spy(actx, l.Number, channel, mode); err != nil {
		s.Log.Error("telephony spy", "err", err)
		return i18n.E("tel.err_asterisk")
	}
	s.Log.Info("telephony spy", "by", sess.UserID, "channel", channel, "mode", mode)
	return nil
}

// phonePick rings the person's phones; the one who answers takes the call of the channel, ringing for a colleague
// («Перехватить»).
func (s *Server) phonePick(ctx context.Context, sess auth.Session, channel string) error {
	list, err := s.phoneActive(ctx, sess)
	if err != nil {
		return err
	}
	if on, err := s.Telephony.Picking(ctx); err != nil {
		return err
	} else if !on {
		return i18n.E("tel.pick_off")
	}
	i := slices.IndexFunc(list, func(a telephony.Active) bool { return a.Channel == channel })
	if i < 0 || list[i].Up {
		return i18n.E("tel.pick_gone")
	}
	if list[i].UserID == sess.UserID {
		return i18n.E("err.forbidden")
	}
	l, ok := s.phoneLine(ctx, sess.UserID)
	if !ok || l.Dup {
		return i18n.E("err.telephony_no_number")
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.Telephony.ARI.Pick(actx, l.Number, list[i].Number); err != nil {
		s.Log.Error("telephony pickup", "err", err)
		return i18n.E("tel.err_asterisk")
	}
	s.Log.Info("telephony pickup", "by", sess.UserID, "channel", channel)
	return nil
}

// phoneMonitor is the page «Текущие звонки»: it reloads itself every few seconds.
func (s *Server) phoneMonitor(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.phoneActive(r.Context(), sess)
	if err != nil {
		s.taskErr(w, r, err, "/telephony/calls")
		return
	}
	now := time.Now()
	rows := make([]phoneActiveRow, len(list))
	for i, a := range list {
		rows[i] = phoneActiveRow{Active: a, Seconds: a.Seconds(now), Mine: a.UserID == sess.UserID}
	}
	_, hasLine := s.phoneLine(r.Context(), sess.UserID)
	pick, err := s.Telephony.Picking(r.Context())
	if err != nil {
		s.fail(w, "telephony pickup", err)
		return
	}
	s.render(w, r, "phonemonitor", page{TitleKey: "tel.active", Nav: "telephony_calls",
		Data: map[string]any{"Calls": rows, "HasLine": hasLine, "Modes": telephony.SpyModes, "Pick": pick}})
}

// phoneMonitorSpy: «Прослушать», «Подсказать», «Подключиться» — the person's phone rings.
func (s *Server) phoneMonitorSpy(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.phoneSpy(r.Context(), sess, r.FormValue("channel"), r.FormValue("mode")); err != nil {
		s.taskErr(w, r, err, "/telephony/active")
		return
	}
	http.Redirect(w, r, "/telephony/active?msg="+url.QueryEscape(s.tr(r, "tel.spy_ringing")), http.StatusSeeOther)
}

// phoneMonitorPick: «Перехватить» — the person's phone rings.
func (s *Server) phoneMonitorPick(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.phonePick(r.Context(), sess, r.FormValue("channel")); err != nil {
		s.taskErr(w, r, err, "/telephony/active")
		return
	}
	http.Redirect(w, r, "/telephony/active?msg="+url.QueryEscape(s.tr(r, "tel.pick_ringing")), http.StatusSeeOther)
}
