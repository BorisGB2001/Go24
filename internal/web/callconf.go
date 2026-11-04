package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Видеоконференции»: /call/conferences ----------

// confErr turns the errors of conferences into the messages people see.
func confErr(err error) error {
	switch {
	case errors.Is(err, calls.ErrNoConference):
		return i18n.E("err.not_found")
	case errors.Is(err, calls.ErrConferenceTitle):
		return i18n.E("call.conf_err_title")
	}
	return err
}

// confOn: conferences work — calls are on and the person is an employee (a guest of a collab has none).
func (s *Server) confOn(ctx context.Context) bool {
	return s.Conferences != nil && s.Calls != nil && s.CallGuests != nil && !tasks.IsGuestCtx(ctx)
}

// confEdit is the conference the person may change: its organizer, or an administrator in admin mode.
func (s *Server) confEdit(ctx context.Context, sess auth.Session, id string) (calls.Conference, error) {
	c, err := s.Conferences.Get(ctx, id)
	if err != nil {
		return c, confErr(err)
	}
	if c.CreatedBy != sess.UserID && !sess.AdminActive() {
		return c, i18n.E("err.forbidden")
	}
	return c, nil
}

// confInput reads the form of a conference: title, members, broadcast=1 and presenters; password_set=1 — with the
// password typed (none typed — the conference keeps the one it has), without it — no password.
func confInput(r *http.Request) calls.ConferenceInput {
	in := calls.ConferenceInput{Title: r.PostFormValue("title"), Members: r.PostForm["members"],
		Broadcast: r.PostFormValue("broadcast") == "1", Presenters: r.PostForm["presenters"]}
	p := r.PostFormValue("password")
	if r.PostFormValue("password_set") != "1" {
		p = ""
	}
	if p != "" || r.PostFormValue("password_set") != "1" {
		in.Password = &p
	}
	return in
}

// confInvite: the people newly invited get a notification with the conference, and come into its chat.
func (s *Server) confInvite(ctx context.Context, sess auth.Session, c calls.Conference, ids []string) {
	if len(ids) == 0 {
		return
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
		SELECT u.id, $2::uuid, 'call_conference', left($3, 300), $4 FROM users u WHERE u.id::text = ANY($1) AND u.active AND u.id <> $2::uuid`,
		ids, sess.UserID, c.Title, "/call/conferences#c-"+c.ID); err != nil {
		s.Log.Warn("conference notify", "err", err)
	}
	if s.Matrix == nil {
		return
	}
	if c.Chat == "" {
		chat, err := s.Matrix.CreateGroup(ctx, c.CreatedBy, c.Title, c.Members, false)
		if err != nil {
			s.Log.Warn("conference chat", "err", err)
			return
		}
		if err := s.Conferences.SetChat(ctx, c.ID, chat); err != nil {
			s.Log.Warn("conference chat", "err", err)
		}
		return
	}
	if err := s.Matrix.AddToGroup(ctx, c.CreatedBy, c.Chat, ids); err != nil {
		s.Log.Warn("conference chat members", "err", err)
	}
}

// confURL is the conference's link: one for everyone — an employee joins by it at once, a guest with a name.
func (s *Server) confURL(c calls.Conference) string {
	if c.Token == "" {
		return ""
	}
	return s.guestLinkURL(c.Token)
}

// callConferences is the list «Видеоконференции»: the person's conferences and the form of a new one (?edit=ID —
// of that one).
func (s *Server) callConferences(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if !s.confOn(ctx) {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	list, err := s.Conferences.List(ctx, sess.UserID)
	if err != nil {
		s.fail(w, "conferences", err)
		return
	}
	users, err := s.Tasks.ListUsers(ctx)
	if err != nil {
		s.fail(w, "users", err)
		return
	}
	names := map[string]string{}
	for _, u := range users {
		names[u.ID] = u.Name
	}
	type row struct {
		calls.Conference
		URL, By string
		Mine    bool
		People  int
	}
	rows := make([]row, len(list))
	for i, c := range list {
		rows[i] = row{Conference: c, URL: s.confURL(c), By: names[c.CreatedBy], Mine: c.CreatedBy == sess.UserID || sess.AdminActive(),
			People: len(c.Members) + 1}
	}
	data := map[string]any{"Rows": rows, "Users": users, "Members": []string{}, "Presenters": []string{}}
	if id := r.URL.Query().Get("edit"); id != "" {
		c, err := s.confEdit(ctx, sess, id)
		if err != nil {
			s.taskErr(w, r, err, "/call/conferences")
			return
		}
		data["Edit"], data["Members"], data["Presenters"] = c, c.Members, c.Presenters
	}
	s.render(w, r, "callconf", page{TitleKey: "call.conf_title", Nav: "im", Data: data})
}

// callConferenceCreate makes a conference: its people are invited (a notification and the conference's chat).
func (s *Server) callConferenceCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if !s.confOn(ctx) {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/call/conferences")
		return
	}
	c, err := s.Conferences.Create(ctx, sess.UserID, confInput(r))
	if err != nil {
		s.taskErr(w, r, confErr(err), "/call/conferences")
		return
	}
	s.Log.Info("conference created", "user_id", sess.UserID, "conference", c.ID)
	s.confInvite(ctx, sess, c, c.Members)
	http.Redirect(w, r, "/call/conferences?msg="+url.QueryEscape(s.tr(r, "call.conf_created"))+"#c-"+c.ID, http.StatusSeeOther)
}

// callConferenceUpdate changes the conference (its organizer): action=link gives it a new link (the old one lets
// nobody in, its guests leave the call), action=delete removes it.
func (s *Server) callConferenceUpdate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if !s.confOn(ctx) {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	c, err := s.confEdit(ctx, sess, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/call/conferences")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/call/conferences")
		return
	}
	msg := "common.saved"
	switch r.PostFormValue("action") {
	case "delete", "link":
		var guests []string
		if r.PostFormValue("action") == "delete" {
			guests, err = s.Conferences.Delete(ctx, c.ID)
			msg = "call.conf_deleted"
		} else {
			guests, err = s.Conferences.NewLink(ctx, c.ID)
			msg = "call.conf_new_link_done"
		}
		if err != nil {
			s.taskErr(w, r, confErr(err), "/call/conferences")
			return
		}
		s.confExpel(ctx, c.ID, guests)
	default:
		added, err := s.Conferences.Update(ctx, c.ID, confInput(r))
		if err != nil {
			s.taskErr(w, r, confErr(err), "/call/conferences?edit="+c.ID)
			return
		}
		if title := r.PostFormValue("title"); c.Chat != "" && s.Matrix != nil && title != "" && title != c.Title {
			if err := s.Matrix.RenameGroup(ctx, c.CreatedBy, c.Chat, title); err != nil {
				s.Log.Warn("conference chat rename", "err", err)
			}
		}
		if c, err = s.Conferences.Get(ctx, c.ID); err == nil {
			s.confInvite(ctx, sess, c, added)
		}
	}
	http.Redirect(w, r, "/call/conferences?msg="+url.QueryEscape(s.tr(r, msg))+"#c-"+c.ID, http.StatusSeeOther)
}

// confExpel puts the guests of a link no longer valid out of the conference's call.
func (s *Server) confExpel(ctx context.Context, id string, guests []string) {
	for _, g := range guests {
		if err := s.Calls.RemoveParticipant(ctx, calls.ConferenceRoom(id), calls.GuestIdentity(g)); err != nil && !errors.Is(err, calls.ErrNoAPI) {
			s.Log.Warn("conference guest out", "err", err)
		}
	}
}

// ---------- REST: /api/v1/calls/conferences ----------

type apiConference struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	CreatedBy  string    `json:"created_by"`
	Members    []string  `json:"members"`
	Broadcast  bool      `json:"broadcast"`
	Presenters []string  `json:"presenters"`
	Password   bool      `json:"password"`
	Chat       string    `json:"chat,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

func (s *Server) apiConf(c calls.Conference) apiConference {
	return apiConference{ID: c.ID, Title: c.Title, URL: s.confURL(c), CreatedBy: c.CreatedBy, Members: c.Members,
		Broadcast: c.Broadcast, Presenters: c.Presenters, Password: c.Password, Chat: c.Chat, CreatedAt: c.CreatedAt}
}

type apiConferenceIn struct {
	Title      *string   `json:"title"`
	Members    *[]string `json:"members"`
	Broadcast  *bool     `json:"broadcast"`
	Presenters *[]string `json:"presenters"`
	Password   *string   `json:"password"` // "" — none; absent — kept
}

// apiConferences: GET /api/v1/calls/conferences — the key owner's conferences; POST makes one (title, members,
// broadcast, presenters, password).
func (s *Server) apiConferences(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if !s.confOn(ctx) {
		return i18n.E("err.calls_off")
	}
	if r.Method == http.MethodGet {
		list, err := s.Conferences.List(ctx, p.UserID)
		if err != nil {
			return err
		}
		out := make([]apiConference, len(list))
		for i, c := range list {
			out[i] = s.apiConf(c)
		}
		writeJSON(w, http.StatusOK, map[string]any{"conferences": out})
		return nil
	}
	var in apiConferenceIn
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ci := calls.ConferenceInput{Password: in.Password}
	if in.Title != nil {
		ci.Title = *in.Title
	}
	if in.Members != nil {
		ci.Members = *in.Members
	}
	if in.Presenters != nil {
		ci.Presenters = *in.Presenters
	}
	ci.Broadcast = in.Broadcast != nil && *in.Broadcast
	c, err := s.Conferences.Create(ctx, p.UserID, ci)
	if err != nil {
		return confErr(err)
	}
	s.confInvite(ctx, s.apiSess(r, p), c, c.Members)
	if c, err = s.Conferences.Get(ctx, c.ID); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.apiConf(c))
	return nil
}

// apiConference: GET, PATCH (the fields given change) and DELETE /api/v1/calls/conferences/{id}; POST …/{id}/link
// gives it a new link.
func (s *Server) apiConference(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx := r.Context()
	if !s.confOn(ctx) {
		return i18n.E("err.calls_off")
	}
	sess := s.apiSess(r, p)
	id := r.PathValue("id")
	if r.Method == http.MethodGet {
		c, err := s.Conferences.Get(ctx, id)
		if err != nil {
			return confErr(err)
		}
		if !c.Invited(p.UserID) && !sess.AdminActive() {
			return i18n.E("err.not_found")
		}
		writeJSON(w, http.StatusOK, s.apiConf(c))
		return nil
	}
	c, err := s.confEdit(ctx, sess, id)
	if err != nil {
		return err
	}
	switch {
	case r.Method == http.MethodDelete:
		guests, err := s.Conferences.Delete(ctx, c.ID)
		if err != nil {
			return confErr(err)
		}
		s.confExpel(ctx, c.ID, guests)
		w.WriteHeader(http.StatusNoContent)
		return nil
	case r.Method == http.MethodPost: // …/link
		guests, err := s.Conferences.NewLink(ctx, c.ID)
		if err != nil {
			return confErr(err)
		}
		s.confExpel(ctx, c.ID, guests)
	default:
		var in apiConferenceIn
		if err := readJSON(r, &in); err != nil {
			return err
		}
		ci := calls.ConferenceInput{Title: c.Title, Members: c.Members, Broadcast: c.Broadcast, Presenters: c.Presenters, Password: in.Password}
		if in.Title != nil {
			ci.Title = *in.Title
		}
		if in.Members != nil {
			ci.Members = *in.Members
		}
		if in.Broadcast != nil {
			ci.Broadcast = *in.Broadcast
		}
		if in.Presenters != nil {
			ci.Presenters = *in.Presenters
		}
		added, err := s.Conferences.Update(ctx, c.ID, ci)
		if err != nil {
			return confErr(err)
		}
		if ci.Title != c.Title && c.Chat != "" && s.Matrix != nil {
			if err := s.Matrix.RenameGroup(ctx, c.CreatedBy, c.Chat, ci.Title); err != nil {
				s.Log.Warn("conference chat rename", "err", err)
			}
		}
		if c, err = s.Conferences.Get(ctx, c.ID); err == nil {
			s.confInvite(ctx, sess, c, added)
		}
	}
	c, err = s.Conferences.Get(ctx, c.ID)
	if err != nil {
		return confErr(err)
	}
	writeJSON(w, http.StatusOK, s.apiConf(c))
	return nil
}
