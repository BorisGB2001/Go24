package web

import (
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Изменить» a post as a whole ----------

// feedEditForm renders the edit form of a post (its author, or an administrator): the form of a new post filled in
// — addressees, «Важное» and its term, tags, files, the gratitude and the poll (while nobody voted).
func (s *Server) feedEditForm(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	d, err := s.Feed.Draft(ctx, sess.UserID, r.PathValue("id"))
	var users []tasks.User
	var projects []tasks.Project
	if err == nil {
		users, err = s.Tasks.ListUsers(ctx)
	}
	if err == nil {
		projects, err = s.Tasks.ListProjects(ctx, sess.UserID)
	}
	var mayAll, mayImportant bool
	if err == nil {
		mayAll, mayImportant, err = s.Feed.May(ctx, sess.UserID)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	type option struct{ ID, Name string }
	var mine []option // the projects the viewer works in, and the post's own one
	for _, p := range projects {
		if p.CanWork() && p.MyRole != "admin" || p.ID == d.ProjectID {
			mine = append(mine, option{p.ID, p.Name})
		}
	}
	if d.ProjectID != "" && !slices.ContainsFunc(mine, func(o option) bool { return o.ID == d.ProjectID }) {
		mine = append(mine, option{d.ProjectID, d.ProjectName})
	}
	var to, shared []tasks.User // the addressees as the picker's choice (a department is «dept:ID»)
	for _, rc := range d.To {
		u := tasks.User{ID: rc.ID, Name: rc.Name}
		if rc.Dept {
			u.ID = "dept:" + rc.ID
		}
		to = append(to, u)
	}
	if !d.Limited {
		to, shared = nil, to
	}
	var thank []string
	badge := feed.Badges[0]
	if g := d.Gratitude; g != nil {
		badge = g.Badge
		for _, u := range g.People {
			thank = append(thank, u.ID)
		}
	}
	var options []string
	if d.Poll != nil {
		for _, o := range d.Poll.Options {
			options = append(options, o.Text)
		}
	}
	untilDate := ""
	if !d.Until.IsZero() {
		untilDate = d.Until.Add(-time.Second).Format("2006-01-02") // «до даты» is the end of that day
	}
	html, err := s.feedFragment(r, "fedit", map[string]any{
		"D": &d, "Users": users, "Projects": mine, "To": to, "Shared": shared, "Thank": thank, "Badge": badge,
		"Badges": feed.Badges, "BadgeEmoji": feed.BadgeEmoji, "PollOptions": strings.Join(options, "\n"),
		"Untils": []string{"always", "day", "week", "month", "date"}, "UntilDate": untilDate,
		"UntilWas": d.Until.Format(time.RFC3339Nano), "Guest": tasks.IsGuestCtx(ctx), "MayAll": mayAll, "MayImportant": mayImportant,
	})
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// postChange is the rest of the edit form, besides the title, text and tags.
type postChange struct {
	Project   string
	To        feed.Audience
	Important bool
	Until     time.Time
	Detach    []string                // files taken off
	Files     []*multipart.FileHeader // uploaded
	Drive     []string                // «Файл с Диска»
	Gratitude *newGratitude           // nil — not a gratitude
	Poll      *newPoll                // nil — no poll
	PollEdit  bool                    // the form had the poll (it has no votes)
}

// readPostChange reads and checks the whole edit form (full=1) before anything changes.
func (s *Server) readPostChange(r *http.Request, id string, files []*multipart.FileHeader) (postChange, error) {
	ch := postChange{Project: r.FormValue("project"), Important: r.FormValue("important") == "on", Detach: r.Form["detach"],
		Files: files, Drive: r.Form["drive_file"], PollEdit: r.FormValue("poll_edit") == "1"}
	if ch.Project == "people" {
		ch.Project, ch.To = "", formAudience(r)
		if len(ch.To.Users)+len(ch.To.Depts) == 0 {
			return ch, i18n.E("err.post_to")
		}
	}
	if ch.Important {
		was, _ := time.Parse(time.RFC3339Nano, r.FormValue("important_was"))
		if r.FormValue("important_for") == "date" && !was.IsZero() && r.FormValue("important_until") == was.Add(-time.Second).Format("2006-01-02") {
			ch.Until = was // the term as it was
		} else {
			until, err := importantUntil(r)
			if err != nil {
				return ch, err
			}
			ch.Until = until
		}
	}
	if r.FormValue("gratitude") == "on" {
		ch.Gratitude = &newGratitude{Badge: r.FormValue("badge"), People: r.Form["thank"]}
		if !slices.Contains(feed.Badges, ch.Gratitude.Badge) || len(ch.Gratitude.People) == 0 {
			return ch, i18n.E("err.gratitude")
		}
	}
	if q := r.FormValue("poll_question"); ch.PollEdit && strings.TrimSpace(q) != "" {
		ch.Poll = formPoll(r)
		if err := feed.CheckPoll(ch.Poll.Question, ch.Poll.Options); err != nil {
			return ch, err
		}
	}
	n, err := s.Feed.FileCount(r.Context(), id)
	if err != nil {
		return ch, err
	}
	if n-len(ch.Detach)+len(ch.Files)+len(ch.Drive) > maxPostFiles {
		return ch, i18n.E("err.too_many_files", maxPostFiles)
	}
	return ch, nil
}

// reworkPost applies the rest of the edit form; a readdressed post moves to its new messenger room, a post that
// became important or ordinary gets its mark there.
func (s *Server) reworkPost(r *http.Request, sess auth.Session, id string, ch postChange) error {
	ctx := r.Context()
	marked, err := s.Feed.SetImportant(ctx, sess.UserID, id, ch.Important, ch.Until)
	if err != nil {
		return err
	}
	moved, told, err := s.Feed.SetAudience(ctx, sess.UserID, id, ch.Project, ch.To)
	if err != nil {
		return err
	}
	if marked && ch.Important {
		if err := s.Feed.TellImportant(ctx, sess.UserID, id, told); err != nil {
			return err
		}
	}
	if err := s.Feed.DetachFiles(ctx, id, ch.Detach); err != nil {
		return err
	}
	if err := s.attachPostFiles(r, sess, storages.NewFile{PostID: id}, ch.Files); err != nil {
		return err
	}
	for _, f := range ch.Drive {
		if _, err := s.attachDriveFile(r, sess, f, storages.NewFile{PostID: id}); err != nil {
			return err
		}
	}
	g := newGratitude{}
	if ch.Gratitude != nil {
		g = *ch.Gratitude
	}
	if err := s.Feed.EditGratitude(ctx, sess.UserID, id, g.Badge, g.People); err != nil {
		return err
	}
	if ch.PollEdit {
		p := newPoll{}
		if ch.Poll != nil {
			p = *ch.Poll
		}
		if err := s.Feed.EditPoll(ctx, id, p.Question, p.Options, p.PollKind); err != nil {
			return err
		}
	}
	if s.Matrix == nil {
		return nil
	}
	if moved {
		err = s.Matrix.MovePost(ctx, id)
	} else if marked {
		var b feed.Bridge
		if b, err = s.Feed.BridgeInfo(ctx, id); err == nil {
			err = s.Matrix.PublishEdit(ctx, id, b.Title, b.Body)
		}
	}
	if err != nil {
		s.Log.Error("feed rework in matrix", "err", err)
		return i18n.E("err.messenger")
	}
	return nil
}
