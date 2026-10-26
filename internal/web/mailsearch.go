package web

import (
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/mail"
)

// ---------- «Почта»: the list's tabs, filters, order and searches ----------

// As Outlook: an inbox (and «Все входящие») shows «Отсортированные» and «Другие» (?view=other; the setting turns
// them off), «Фильтр» narrows the list (?filter=), «Сортировка» orders it (?sort=…&turn=1, kept in the cookie
// mail_sort), the search box takes Outlook's operators (mail.ParseQuery) in the folder or in all folders
// (?scope=all), the advanced search form makes such a search of its fields (?adv=1&s_from=…), a search is saved as
// a search folder (folder=search:ID).

// mailList is how the page shows the list.
type mailList struct {
	mail.ListOpts
	Focus      bool   // the tabs are shown
	Everywhere bool   // a search through all folders
	Saved      string // the saved search shown (its id)
}

// mailSortOf is the order of the list: ?sort= (kept in the cookie mail_sort), else the cookie's.
func mailSortOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query()
	v := ""
	if c, err := r.Cookie("mail_sort"); err == nil {
		v = c.Value
	}
	if s := q.Get("sort"); s != "" {
		v = s
		if q.Get("turn") == "1" {
			v += ".1"
		}
		http.SetCookie(w, &http.Cookie{Name: "mail_sort", Value: v, Path: "/mail", MaxAge: 31536000, SameSite: http.SameSiteLaxMode})
	}
	sort, turn, _ := strings.Cut(v, ".")
	for _, x := range mail.Sorts {
		if x == sort {
			return sort, turn == "1"
		}
	}
	return "date", false
}

// mailSearchQ is the search of the page: the search box, or what the advanced search form makes.
func mailSearchQ(q url.Values) string {
	if q.Get("adv") == "" {
		return strings.TrimSpace(q.Get("q"))
	}
	return mail.Advanced{From: q.Get("s_from"), To: q.Get("s_to"), Subject: q.Get("s_subject"), Words: q.Get("s_words"),
		After: q.Get("s_after"), Before: q.Get("s_before"), Attach: q.Get("s_attach") != "", Unread: q.Get("s_unread") != "",
		Flagged: q.Get("s_flagged") != ""}.String()
}

// mailListOf is the list the page asks for in folder fid (role: the folder's role; focused: the user's setting).
func mailListOf(w http.ResponseWriter, r *http.Request, fid, role string, focused, conv bool) mailList {
	q := r.URL.Query()
	l := mailList{ListOpts: mail.ListOpts{Folder: fid, Q: mailSearchQ(q), Conv: conv}}
	l.Sort, l.Turn = mailSortOf(w, r)
	for _, f := range mail.Filters {
		if q.Get("filter") == f {
			l.Filter = f
		}
	}
	if strings.HasPrefix(fid, mail.SearchFolder) {
		l.Saved, l.Everywhere = fid[len(mail.SearchFolder):], true
	} else if l.Q != "" && q.Get("scope") == "all" {
		l.All, l.Everywhere = true, true
	}
	if focused && l.Q == "" && (role == "inbox" || fid == mail.AllInboxes) { // a search looks in both tabs
		l.Focus, l.View = true, mail.ViewFocused
		if q.Get("view") == mail.ViewOther {
			l.View = mail.ViewOther
		}
	}
	return l
}

// URL is the list's address with its tab, filter and search (sort is the cookie's), less the keys given.
func (l mailList) URL(drop ...string) string {
	v := url.Values{}
	set := func(k, x string) {
		if x == "" {
			return
		}
		for _, d := range drop {
			if d == k {
				return
			}
		}
		v.Set(k, x)
	}
	set("folder", l.Folder)
	if l.View == mail.ViewOther {
		set("view", l.View)
	}
	set("filter", l.Filter)
	if l.Saved == "" {
		set("q", l.Q)
		if l.All && v.Get("q") != "" {
			set("scope", "all")
		}
	}
	return "/mail?" + v.Encode()
}

// With is the list's address with a key changed.
func (l mailList) With(k, x string) string {
	u := l.URL(k)
	if x == "" {
		return u
	}
	return u + "&" + url.QueryEscape(k) + "=" + url.QueryEscape(x)
}

// mailSuggest is what the search box suggests as the user types: the recent searches and the people the user
// writes with (from: them). GET /mail/suggest?q= → {recent: [q], people: [{name, email}]}.
func (s *Server) mailSuggest(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, q := r.Context(), strings.TrimSpace(r.URL.Query().Get("q"))
	recent, people := []string{}, []map[string]string{}
	if list, err := s.Mail.Recent(ctx, sess.UserID, q, 5); err == nil {
		for _, x := range list {
			recent = append(recent, x.Q)
		}
	} else {
		s.Log.Warn("mail recent searches", "err", err)
	}
	// the word being typed: the last one, without its operator
	word := q
	if i := strings.LastIndexAny(word, " \t"); i >= 0 {
		word = word[i+1:]
	}
	if _, v, ok := strings.Cut(word, ":"); ok {
		word = v
	}
	if word = strings.Trim(word, `"`); word != "" {
		if list, err := s.Mail.Correspondents(ctx, sess.UserID, word, 6); err == nil {
			for _, x := range list {
				people = append(people, map[string]string{"name": x.Name, "email": x.Email})
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"recent": recent, "people": people})
}

// mailSearchSave saves a search as a search folder (id: renames or changes one): name, q.
func (s *Server) mailSearchSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	id, err := s.Mail.SaveSearch(r.Context(), sess.UserID, r.PathValue("id"), r.FormValue("name"), r.FormValue("q"))
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, mailURL(mail.SearchFolder+id, ""), http.StatusSeeOther)
}

func (s *Server) mailSearchDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeleteSearch(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	http.Redirect(w, r, "/mail", http.StatusSeeOther)
}

// mailRecentClear forgets the recent searches (the suggestions' «Очистить»).
func (s *Server) mailRecentClear(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.ForgetRecent(r.Context(), sess.UserID); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": true})
}

// mailFocusSave turns «Отсортированные» and «Другие» on or off (the settings' tab «Отсортированные»).
func (s *Server) mailFocusSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.SetFocused(r.Context(), sess.UserID, r.FormValue("focused") != ""); err != nil {
		s.mailErr(w, r, err, settingsURL("focused"))
		return
	}
	http.Redirect(w, r, settingsURL("focused", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailFocusRule sets a sender's «Всегда перемещать» (sender, view) or takes it off (do=delete).
func (s *Server) mailFocusRule(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error
	if r.FormValue("do") == "delete" {
		err = s.Mail.DropFocusRule(r.Context(), sess.UserID, r.FormValue("sender"))
	} else {
		err = s.Mail.SetFocusRule(r.Context(), sess.UserID, r.FormValue("sender"), r.FormValue("view"))
	}
	if err != nil {
		s.mailErr(w, r, err, settingsURL("focused"))
		return
	}
	http.Redirect(w, r, settingsURL("focused"), http.StatusSeeOther)
}
