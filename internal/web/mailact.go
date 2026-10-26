package web

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: actions on letters and the categories ----------

// The presets of «Отложить» and of the flag's date, as Outlook's: later today, tomorrow, this weekend, next week;
// today, tomorrow, this week, next week, no date.
var (
	snoozePresets = []string{"later", "tomorrow", "weekend", "nextweek"}
	flagPresets   = []string{"today", "tomorrow", "week", "nextweek", "none"}
	remindPresets = []string{"later", "tomorrow", "nextweek"} // «Добавить напоминание»
)

// snoozeTime is the time a preset (or a datetime-local value) snoozes till; false — not a time to come.
func snoozeTime(v string, now time.Time) (time.Time, bool) {
	day := func(d int, hour int) time.Time {
		return time.Date(now.Year(), now.Month(), now.Day()+d, hour, 0, 0, 0, now.Location())
	}
	var t time.Time
	switch v {
	case "later": // in three hours, on the hour
		t = now.Add(3 * time.Hour).Truncate(time.Hour)
	case "tomorrow":
		t = day(1, 8)
	case "weekend": // the coming Saturday
		t = day((int(time.Saturday)-int(now.Weekday())+7)%7, 8)
		if !t.After(now) {
			t = t.AddDate(0, 0, 7)
		}
	case "nextweek": // the coming Monday
		t = day((int(time.Monday)-int(now.Weekday())+6)%7+1, 8)
	default: // a datetime-local value, or a full RFC 3339 time (the API)
		var err error
		if t, err = time.Parse(time.RFC3339, v); err != nil {
			if t, err = time.ParseInLocation("2006-01-02T15:04", v, now.Location()); err != nil {
				return t, false
			}
		}
	}
	return t, t.After(now)
}

// flagDue is the date (YYYY-MM-DD, "" — none) a preset (or a date) flags a letter for; false — not a date.
func flagDue(v string, now time.Time) (string, bool) {
	day := func(d int) string { return now.AddDate(0, 0, d).Format("2006-01-02") }
	switch v {
	case "", "none":
		return "", true
	case "today":
		return day(0), true
	case "tomorrow":
		return day(1), true
	case "week": // this Friday (today on Friday and the weekend)
		d := (int(time.Friday) - int(now.Weekday()) + 7) % 7
		if now.Weekday() == time.Saturday {
			d = 0
		}
		return day(d), true
	case "nextweek":
		return day((int(time.Monday)-int(now.Weekday())+6)%7 + 1), true
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return "", false
	}
	return v, true
}

// letterIDs: the letters of a form — each value may hold several ids (a conversation's), comma-separated.
func letterIDs(values []string) []string {
	var out []string
	for _, v := range values {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

// actArg turns the form's presets into what mail.Store.Act takes.
func actArg(action, arg string, now time.Time) (string, error) {
	switch action {
	case "snooze", "remind": // the flag's reminder has the snooze's presets
		t, ok := snoozeTime(arg, now)
		if !ok && action == "remind" {
			return "", i18n.E("err.mail_remind")
		} else if !ok {
			return "", i18n.E("err.mail_snooze")
		}
		return t.Format(time.RFC3339), nil
	case "flag":
		d, ok := flagDue(arg, now)
		if !ok {
			return "", i18n.E("err.bad_date")
		}
		return d, nil
	}
	return arg, nil
}

// mailAct does an action on letters (the toolbar, the reading pane, the hot keys): letter (ids), action, arg, back.
func (s *Server) mailAct(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	// a button of the toolbar is do=action[:arg]; a field next to it may give the arg (arg_<action>: a date, a time)
	ids, action, arg, back := letterIDs(r.Form["letter"]), r.FormValue("action"), r.FormValue("arg"), r.FormValue("back")
	if do := r.FormValue("do"); do != "" {
		action, arg, _ = strings.Cut(do, ":")
	}
	if arg == "" {
		arg = r.FormValue("arg_" + action)
	}
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	if action == "move" && arg == "" {
		arg = r.FormValue("folder")
	}
	switch action { // dialogs, as in Outlook: «Создать правило» (filled with the letter), «Очистка»
	case "rule":
		v := url.Values{"tab": {"rules"}, "edit": {"new"}}
		if len(ids) > 0 {
			v.Set("letter", ids[len(ids)-1]) // a conversation: its newest letter
		}
		http.Redirect(w, r, "/mail/settings?"+v.Encode(), http.StatusSeeOther)
		return
	case "sweep":
		http.Redirect(w, r, "/mail/sweep?"+url.Values{"letter": {strings.Join(ids, ",")}, "back": {back}}.Encode(), http.StatusSeeOther)
		return
	case "quick":
		s.mailQuickDo(w, r, sess, ids, arg, back)
		return
	}
	if leaves(action) {
		back = withLetter(back, r.FormValue("next"))
	}
	arg, err := actArg(action, arg, time.Now())
	if err == nil {
		err = s.Mail.Act(r.Context(), sess.UserID, ids, action, arg)
	}
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mail action", "user_id", sess.UserID, "action", action, "letters", len(ids))
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// leaves: the action takes the letter out of the folder (the tab).
func leaves(action string) bool {
	switch action {
	case "archive", "delete", "junk", "notjunk", "move", "focus", "other", "ignore", "unignore":
		return true
	}
	return false
}

// withLetter is the page back with the letter to open next instead of the one that left (none: the folder alone),
// as «После перемещения или удаления письма» says.
func withLetter(back, next string) string {
	back = withoutLetter(back)
	if next == "" || strings.ContainsAny(next, ",&#") {
		return back
	}
	if u, err := url.Parse(back); err == nil {
		q := u.Query()
		q.Set("letter", next)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return back
}

// withoutLetter is the page back without its open letter.
func withoutLetter(back string) string {
	if u, err := url.Parse(back); err == nil {
		q := u.Query()
		q.Del("letter")
		u.RawQuery = q.Encode()
		return u.String()
	}
	return back
}

// mailCategories are the user's categories; Outlook's six colours are made the first time.
func (s *Server) mailCategories(r *http.Request, userID string) ([]mail.Category, error) {
	var defaults [][2]string
	for _, c := range []string{"red", "orange", "yellow", "green", "blue", "purple"} {
		defaults = append(defaults, [2]string{c, s.tr(r, "mail.cat."+c)})
	}
	return s.Mail.Categories(r.Context(), userID, defaults...)
}

func (s *Server) mailCategorySave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	if _, err := s.Mail.SaveCategory(r.Context(), sess.UserID, r.PathValue("id"), r.FormValue("name"), r.FormValue("color")); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) mailCategoryDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeleteCategory(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") || strings.Contains(back, r.PathValue("id")) {
		back = "/mail"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// mailFiles are the files of letters (a row of the list: a letter or a conversation) for the paper clip's menu:
// GET /mail/files?letter=ID,ID → [{id, name, size, image}].
func (s *Server) mailFiles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	out := []map[string]any{}
	for _, id := range letterIDs(r.URL.Query()["letter"]) {
		files, err := s.Mail.Files(r.Context(), sess.UserID, id)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		for _, f := range files {
			out = append(out, map[string]any{"id": f.ID, "name": f.Name, "size": f.Size, "size_text": formatSize(func(k string, a ...any) string { return s.tr(r, k, a...) }, f.Size),
				"image": f.Image(), "letter": id})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// mailReadAll: «Отметить все как прочитанные» of a folder.
func (s *Server) mailReadAll(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Mail.ReadAll(r.Context(), sess.UserID, id); err != nil {
		s.mailErr(w, r, err, mailURL(id, ""))
		return
	}
	http.Redirect(w, r, mailURL(id, ""), http.StatusSeeOther)
}

// mailEmpty: «Очистить папку» — «Удалённые» or «Нежелательная почта».
func (s *Server) mailEmpty(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Mail.Empty(r.Context(), sess.UserID, id); err != nil {
		s.mailErr(w, r, err, mailURL(id, ""))
		return
	}
	s.Log.Info("mail folder emptied", "user_id", sess.UserID, "folder", id)
	http.Redirect(w, r, mailURL(id, ""), http.StatusSeeOther)
}

// mailImport: «Импорт писем» — files .eml and .msg chosen in the folder's menu or dropped onto a folder.
func (s *Server) mailImport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, 4*mail.MaxSendSize)
	var files []mail.ImportFile
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.mailErr(w, r, i18n.E("err.mail_import_size"), mailURL(id, ""))
		return
	}
	for _, h := range r.MultipartForm.File["file"] {
		f, err := h.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err == nil {
			files = append(files, mail.ImportFile{Name: path.Base(strings.ReplaceAll(h.Filename, "\\", "/")), Data: data})
		}
	}
	res, err := s.Mail.Import(r.Context(), sess.UserID, id, files)
	if err != nil {
		s.mailErr(w, r, err, mailURL(id, ""))
		return
	}
	s.Log.Info("mail imported", "user_id", sess.UserID, "folder", id, "letters", res.Done, "skipped", len(res.Skipped))
	msg := s.tr(r, "mail.imported", res.Done)
	if len(res.Skipped) > 0 {
		msg += " · " + s.tr(r, "mail.import_skipped", strings.Join(res.Skipped, ", "))
	}
	if r.Header.Get("Accept") == "application/json" {
		writeJSON(w, http.StatusOK, map[string]any{"done": res.Done, "skipped": res.Skipped, "msg": msg})
		return
	}
	http.Redirect(w, r, mailURL(id, "", "msg", msg), http.StatusSeeOther)
}

// mailGroup is the date group of a letter in the list, as Outlook's: pinned, today, yesterday, this week, last
// week, this month, last month, older.
func mailGroup(t time.Time, pinned bool, now time.Time) string {
	if pinned {
		return "pinned"
	}
	t, now = t.In(time.Local), now.In(time.Local)
	day := func(x time.Time) time.Time { return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.Local) }
	today := day(now)
	monday := today.AddDate(0, 0, -((int(today.Weekday()) + 6) % 7))
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	switch d := day(t); {
	case !d.Before(today):
		return "today"
	case !d.Before(today.AddDate(0, 0, -1)):
		return "yesterday"
	case !d.Before(monday):
		return "week"
	case !d.Before(monday.AddDate(0, 0, -7)):
		return "lastweek"
	case !d.Before(month):
		return "month"
	case !d.Before(month.AddDate(0, -1, 0)):
		return "lastmonth"
	}
	return "older"
}
