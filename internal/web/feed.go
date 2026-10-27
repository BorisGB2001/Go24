package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/msgfmt"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// ---------- «Лента» ----------

func (s *Server) feedPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	f := feed.Filter{Important: q.Get("view") == "important", Mine: q.Get("view") == "mine", Project: q.Get("project"),
		Query: q.Get("q"), Author: q.Get("author"), Favorites: q.Get("view") == "fav", Hidden: q.Get("view") == "hidden", Tag: feed.NormTag(q.Get("tag")),
		Mentions: q.Get("view") == "mentions", To: slices.DeleteFunc(q["dest"], func(v string) bool { return v == "" })}
	f.Post = q.Get("post")
	if slices.Contains(feed.Kinds, q.Get("kind")) {
		f.Kind = q.Get("kind")
	}
	period := q.Get("period")
	if !slices.Contains(feed.Periods, period) {
		period = ""
	}
	f.From, f.Till = feed.Period(period, q.Get("from"), q.Get("till"), time.Now())
	// the filter as it is, for «more» and the tabs
	keep := url.Values{}
	for _, k := range []string{"q", "author", "kind", "period", "from", "till", "project", "post", "tag"} {
		if v := q.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	if len(f.To) > 0 {
		keep["dest"] = f.To
	}
	if b := q.Get("before"); b != "" {
		if t, err := time.Parse(time.RFC3339Nano, b); err == nil {
			f.Before = t
		}
	}
	// the general feed: the posts the person pinned are above it, not in it
	general := q.Get("view") == "" && len(keep) == 0
	var pinned []feed.Item
	if general {
		f.NoPinned = true
		if f.Before.IsZero() {
			var err error
			if pinned, err = s.Feed.List(r.Context(), sess.UserID, feed.Filter{Pinned: true}); err != nil {
				s.taskErr(w, r, err, "/my")
				return
			}
		}
	}
	items, err := s.Feed.List(r.Context(), sess.UserID, f)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.fillProcesses(r.Context(), sess.UserID, items, pinned)
	if general && f.Before.IsZero() && !tasks.IsGuestCtx(r.Context()) { // the menu counter starts over, what came since is marked new
		if since, err := s.Feed.Seen(r.Context(), sess.UserID); err == nil {
			feed.MarkNew(items, sess.UserID, since)
			feed.MarkNew(pinned, sess.UserID, since)
		}
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	var mine []any
	for _, p := range projects {
		if p.CanWork() && p.MyRole != "admin" {
			mine = append(mine, p)
		}
	}
	more := ""
	if len(items) == feed.PageSize {
		more = items[len(items)-1].At.Format(time.RFC3339Nano)
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	var processes []bp.Template
	if s.BP != nil && !tasks.IsGuestCtx(r.Context()) {
		if processes, err = s.BP.Templates(r.Context(), "none", true); err == nil {
			processes, err = s.BP.Startable(bpAdmin(r, sess), sess.UserID, processes)
		}
		if err != nil {
			s.taskErr(w, r, err, "/my")
			return
		}
	}
	mayAll, mayImportant, err := s.Feed.May(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	side, err := s.feedSide(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	saved, err := s.Feed.SavedFilters(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	depts, err := s.Feed.DeptNames(r.Context(), f.To)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	// the filter as a saved one has it: with the tab
	cur := url.Values{}
	for k, v := range keep {
		if k != "post" {
			cur[k] = v
		}
	}
	if v := q.Get("view"); v != "" {
		cur.Set("view", v)
	}
	data := map[string]any{
		"Saved": saved, "Current": cur.Encode(), "Dest": memberChips(f.To, depts), "Periods": feed.Periods, "Period": period, "TillText": q.Get("till"),
		"Side": side, "MayAll": mayAll, "MayImportant": mayImportant, // the rights of «Настройки ленты»
		"Items": items, "View": q.Get("view"), "Projects": mine, "More": more, "Me": sess.UserID, "Admin": sess.AdminActive(),
		"Users": users, "Badges": feed.Badges, "BadgeEmoji": feed.BadgeEmoji,
		"Pinned": pinned, "General": general, "F": f, "FromText": q.Get("from"), "Keep": keep.Encode(), "Kinds": feed.Kinds, "Untils": []string{"always", "day", "week", "month", "date"}, "Reactions": reactionList(), "Filtered": len(keep) > 0,
		// «Бизнес-процессы» of the composer: the standalone templates
		"Processes": processes,
		"IMProject": "", "ThankOpen": q.Has("thank"), "Thank": q.Get("thank"), // «Поблагодарить»: the gratitude form opened, ?thank=ID chosen
	}
	if room, ev := q.Get("im_room"), q.Get("im_event"); room != "" && ev != "" && s.Matrix != nil {
		s.postFromMessage(r, sess, room, ev, data) // «Создать пост в ленте» of a chat message
	}
	s.render(w, r, "feed", page{TitleKey: "title.feed", Nav: "feed", Data: data})
}

func (s *Server) feedFragment(r *http.Request, name string, data any) (string, error) {
	var buf bytes.Buffer
	err := s.pages[s.lang(r)]["feed"].ExecuteTemplate(&buf, name, data)
	return buf.String(), err
}

// feedPost publishes a post; a script gets the rendered post, a plain form goes back to the feed.
func (s *Server) feedPost(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	var files []*multipart.FileHeader
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			s.feedPostErr(w, r, i18n.E("err.file_too_big"))
			return
		}
		files = r.MultipartForm.File["files"]
	}
	in := newPost{Project: r.FormValue("project"), Title: r.FormValue("title"), Body: r.FormValue("body"),
		Important: r.FormValue("important") == "on", Files: files, Drive: r.Form["drive_file"], Tags: feed.ParseTags(r.FormValue("tags"))}
	if in.Project == "people" { // «Кому»: chosen people and departments
		in.Project, in.To = "", formAudience(r)
		if len(in.To.Users)+len(in.To.Depts) == 0 {
			s.feedPostErr(w, r, i18n.E("err.post_to"))
			return
		}
	}
	if r.FormValue("gratitude") == "on" {
		in.Gratitude = &newGratitude{Badge: r.FormValue("badge"), People: r.Form["thank"]}
	}
	if q := r.FormValue("poll_question"); strings.TrimSpace(q) != "" {
		in.Poll = formPoll(r)
	}
	var id string
	until, err := importantUntil(r)
	if err == nil {
		in.Until = until
		id, err = s.publishPost(r, sess, in)
	}
	if room, ev := r.FormValue("im_room"), r.FormValue("im_event"); err == nil && room != "" && ev != "" && s.Matrix != nil {
		s.postMessageDone(r, sess, room, ev, id) // of a chat message: its file to the post, the answer there
	}
	if !wantsJSON(r) {
		if err != nil {
			s.taskErr(w, r, err, "/feed")
			return
		}
		http.Redirect(w, r, "/feed", http.StatusSeeOther)
		return
	}
	var html string
	if err == nil {
		html, err = s.feedItemHTML(r, sess, id)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "html": html})
}

// newPost is a post being published, from the feed's form or over REST.
type newPost struct {
	Project     string // project ID; "" — all employees, or To
	To          feed.Audience
	Title, Body string
	Important   bool
	Until       time.Time               // «Важное до»: zero — important always
	Files       []*multipart.FileHeader // uploaded
	Drive       []string                // «Файл с Диска»: files of the drives
	Tags        []string                // «Теги» (the #tags of the text are added)
	Gratitude   *newGratitude
	Poll        *newPoll
}

type newGratitude struct {
	Badge  string
	People []string
}

type newPoll struct {
	Question string
	Options  []string
	feed.PollKind
}

// publishPost creates the post with its files, gratitude and poll; it is kept in Matrix: the post exists only if
// Matrix took it.
func (s *Server) publishPost(r *http.Request, sess auth.Session, in newPost) (string, error) {
	ctx := r.Context()
	if len(in.Files)+len(in.Drive) > maxPostFiles {
		return "", i18n.E("err.too_many_files", maxPostFiles)
	}
	id, err := s.Feed.CreatePostTo(ctx, sess.UserID, in.Project, in.To, in.Title, in.Body, in.Important)
	if err != nil {
		return "", err
	}
	if in.Important && !in.Until.IsZero() {
		err = s.Feed.SetUntil(ctx, sess.UserID, id, in.Until)
	}
	if err == nil {
		err = s.Feed.SetTags(ctx, id, in.Tags)
	}
	if err == nil {
		err = s.attachPostFiles(r, sess, storages.NewFile{PostID: id}, in.Files)
	}
	for _, f := range in.Drive {
		if err == nil {
			_, err = s.attachDriveFile(r, sess, f, storages.NewFile{PostID: id})
		}
	}
	if err == nil && in.Gratitude != nil {
		err = s.Feed.SetGratitude(ctx, sess.UserID, id, in.Gratitude.Badge, in.Gratitude.People)
	}
	if err == nil && in.Poll != nil {
		err = s.Feed.AddPoll(ctx, sess.UserID, id, in.Poll.Question, in.Poll.Options, in.Poll.PollKind)
	}
	if err == nil && s.Matrix != nil {
		if perr := s.Matrix.PublishPost(ctx, id); perr != nil {
			s.Log.Error("feed post to matrix", "err", perr)
			err = i18n.E("err.messenger")
		}
	}
	if err != nil {
		_ = s.Feed.DeletePost(ctx, sess.UserID, id)
		return "", err
	}
	return id, nil
}

// importantUntil reads «Важное до» of a new post: important_for is day | week | month | date (the end of the
// day important_until); none — important always.
func importantUntil(r *http.Request) (time.Time, error) {
	now := time.Now()
	switch r.FormValue("important_for") {
	case "day":
		return now.AddDate(0, 0, 1), nil
	case "week":
		return now.AddDate(0, 0, 7), nil
	case "month":
		return now.AddDate(0, 1, 0), nil
	case "date":
		d, err := time.ParseInLocation("2006-01-02", r.FormValue("important_until"), time.Local)
		if err != nil {
			return time.Time{}, i18n.E("err.bad_form")
		}
		if d = d.AddDate(0, 0, 1); !d.After(now) {
			return time.Time{}, i18n.E("err.until_past")
		}
		return d, nil
	}
	return time.Time{}, nil
}

// maxPostFiles is how many files one post may carry.
const maxPostFiles = 10

// attachPostFiles stores the files of a new post or comment (nf: whose they are).
func (s *Server) attachPostFiles(r *http.Request, sess auth.Session, nf storages.NewFile, files []*multipart.FileHeader) error {
	for _, h := range files {
		f, err := h.Open()
		if err != nil {
			return err
		}
		ct := h.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		name := path.Base(strings.ReplaceAll(h.Filename, "\\", "/"))
		key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
		err = s.Storages.Put(r.Context(), sess.UserID, false, "", key, f, h.Size, ct)
		f.Close()
		if err != nil {
			return err
		}
		nf.Key, nf.Name, nf.ContentType, nf.Size, nf.UploadedBy = key, name, ct, h.Size, sess.UserID
		id, err := s.Storages.AddFile(r.Context(), nf)
		if err != nil {
			return err
		}
		s.keepUploaded(r, sess, id)
	}
	return nil
}

// feedPostErr answers a refused post the way it was sent.
func (s *Server) feedPostErr(w http.ResponseWriter, r *http.Request, err error) {
	if wantsJSON(r) {
		s.jsonErr(w, r, err)
		return
	}
	s.taskErr(w, r, err, "/feed")
}

// feedComment adds a comment, with files and pictures (uploaded or «Файл с Диска»); the messenger
// gets its text (or the files' names).
func (s *Server) feedComment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	var files []*multipart.FileHeader
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			s.jsonErr(w, r, i18n.E("err.file_too_big"))
			return
		}
		files = r.MultipartForm.File["files"]
	} else if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	c, err := s.publishComment(r, sess, r.PathValue("id"), r.FormValue("body"), files, r.Form["drive_file"])
	var html string
	if err == nil {
		html, err = s.feedFragment(r, "fcomment", c)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"html": html})
}

// publishComment adds a comment with its files (uploaded and of the drives); the messenger gets its text (or the
// files' names), the comment exists only if it took it.
func (s *Server) publishComment(r *http.Request, sess auth.Session, postID, body string, files []*multipart.FileHeader, drives []string) (feed.Comment, error) {
	if len(files)+len(drives) > maxPostFiles {
		return feed.Comment{}, i18n.E("err.too_many_files", maxPostFiles)
	}
	var names []string
	for _, h := range files {
		names = append(names, path.Base(strings.ReplaceAll(h.Filename, "\\", "/")))
	}
	for _, id := range drives {
		m, err := s.fileAccess(r, sess, id)
		if err != nil { // no file the author may not read
			return feed.Comment{}, err
		}
		names = append(names, m.Name)
	}
	c, err := s.Feed.AddCommentFiles(r.Context(), sess.UserID, postID, body, names)
	if err != nil {
		return c, err
	}
	if len(files)+len(drives) > 0 {
		nf := storages.NewFile{PostID: postID, CommentID: c.ID}
		err = s.attachPostFiles(r, sess, nf, files)
		for _, f := range drives {
			if err == nil {
				_, err = s.attachDriveFile(r, sess, f, nf)
			}
		}
		if err == nil {
			c, err = s.Feed.Comment(r.Context(), sess.UserID, c.ID)
		}
	}
	if err == nil && s.Matrix != nil {
		mc := c
		if mc.Body == "" {
			mc.Body = "📎 " + strings.Join(names, ", ")
		}
		if perr := s.Matrix.PublishComment(r.Context(), postID, mc); perr != nil {
			s.Log.Error("feed comment to matrix", "err", perr)
			err = i18n.E("err.messenger")
		}
	}
	if err != nil {
		_ = s.Feed.DeleteComment(r.Context(), sess.UserID, c.ID)
		return feed.Comment{}, err
	}
	return c, nil
}

// feedCommentEdit changes a comment (its author); the messenger gets the edit first. The answer is the comment.
func (s *Server) feedCommentEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, err := s.changeComment(r.Context(), sess, r.PathValue("id"), r.FormValue("body"))
	var html string
	if err == nil {
		html, err = s.feedFragment(r, "fcomment", c)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// changeComment: a new text of the comment (its author); a comment of files may lose its text. It returns the
// comment now.
func (s *Server) changeComment(ctx context.Context, sess auth.Session, id, body string) (feed.Comment, error) {
	body = strings.TrimSpace(body)
	c, err := s.Feed.Comment(ctx, sess.UserID, id)
	if err == nil && !c.CanEdit {
		err = feed.ErrForbidden
	}
	if err == nil && body == "" && len(c.Files) == 0 {
		err = i18n.E("err.comment_empty")
	}
	if err == nil && s.Matrix != nil {
		mbody := body
		if mbody == "" {
			mbody = "📎 " + strings.Join(c.FileNames(), ", ")
		}
		if perr := s.Matrix.PublishCommentEdit(ctx, id, mbody); perr != nil {
			s.Log.Error("feed comment edit in matrix", "err", perr)
			err = i18n.E("err.messenger")
		}
	}
	if err == nil {
		err = s.Feed.EditComment(ctx, sess.UserID, id, body)
	}
	if err != nil {
		return feed.Comment{}, err
	}
	return s.Feed.Comment(ctx, sess.UserID, id)
}

// feedCommentDelete removes a comment (its author or an administrator), from the messenger first.
func (s *Server) feedCommentDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.removeComment(r.Context(), sess, r.PathValue("id")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) removeComment(ctx context.Context, sess auth.Session, id string) error {
	c, err := s.Feed.Comment(ctx, sess.UserID, id)
	if err == nil && !c.CanDelete {
		err = feed.ErrForbidden
	}
	if err == nil && s.Matrix != nil {
		if perr := s.Matrix.PublishCommentDelete(ctx, id); perr != nil {
			s.Log.Error("feed comment delete in matrix", "err", perr)
			err = i18n.E("err.messenger")
		}
	}
	if err != nil {
		return err
	}
	return s.Feed.DeleteComment(ctx, sess.UserID, id)
}

// feedMark turns the person's mark of a post on or off: fav («В избранное»), pin («Закрепить»), follow («Следить»)
// or hide («Скрыть»).
func (s *Server) feedMark(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	on, err := s.Feed.Mark(r.Context(), sess.UserID, r.PathValue("id"), path.Base(r.URL.Path))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"on": on})
}

// feedSee records that the person saw the posts (id, several) on the screen — «Просмотры».
func (s *Server) feedSee(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil || len(r.Form["id"]) > feed.PageSize*2 {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	if err := s.Feed.See(r.Context(), sess.UserID, r.Form["id"]); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// feedViewers: who saw the post and when, the latest first.
func (s *Server) feedViewers(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Feed.Viewers(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := make([]map[string]string, len(list))
	for i, v := range list {
		out[i] = map[string]string{"id": v.ID, "name": v.Name, "photo": company.PhotoURL(v.ID, v.Photo),
			"at": v.At.Local().Format(s.tr(r, "format.datetime"))}
	}
	writeJSON(w, http.StatusOK, out)
}

// reactionList is the picker of reactions.
func reactionList() []feed.Count {
	out := make([]feed.Count, len(feed.Reactions))
	for i, r := range feed.Reactions {
		out[i] = feed.Count{Reaction: r}
	}
	return out
}

// feedLike sets the person's reaction to a post (POST /feed/{id}/like) or to a comment (/feed/comments/{id}/like):
// reaction (by default «Нравится»), the same one again takes it back. The answer is the reactions block.
func (s *Server) feedLike(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, comment := r.PathValue("id"), strings.HasPrefix(r.URL.Path, "/feed/comments/")
	reaction := r.FormValue("reaction")
	if reaction == "" {
		reaction = "like"
	}
	sm, err := s.reactTo(r.Context(), sess, id, comment, reaction)
	url := "/feed/" + id
	if comment {
		url = "/feed/comments/" + id
	}
	var html string
	if err == nil {
		html, err = s.feedFragment(r, "freact", map[string]any{"U": url, "R": sm})
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": html, "mine": sm.Mine, "count": sm.Total})
}

// reactTo sets the person's reaction to a post or a comment, the same one again takes it back; the messenger
// follows. It returns the reactions now.
func (s *Server) reactTo(ctx context.Context, sess auth.Session, id string, comment bool, reaction string) (feed.Summary, error) {
	_, old := s.Feed.MyReaction(ctx, id, sess.UserID, comment)
	var (
		postID, commentID = id, ""
		sm                feed.Summary
		err               error
	)
	if comment {
		postID, err = s.Feed.CommentPostVisible(ctx, sess.UserID, id)
		commentID = id
	} else {
		err = s.Feed.CanSee(ctx, sess.UserID, id)
	}
	if err == nil && old != "" { // a changed or taken back reaction: remove it from the messenger while it is known
		s.Matrix.OnUnreact(ctx, postID, old)
	}
	if err == nil {
		if comment {
			_, sm, err = s.Feed.ReactComment(ctx, sess.UserID, id, reaction)
		} else {
			sm, err = s.Feed.React(ctx, sess.UserID, id, reaction)
		}
	}
	if err == nil && sm.Mine != "" {
		go s.Matrix.OnReact(context.WithoutCancel(ctx), postID, commentID, sess.UserID)
	}
	return sm, err
}

// feedLikes: «Кто оценил» a post or a comment — the people with their reactions, the latest first.
func (s *Server) feedLikes(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var (
		list []feed.Reactor
		err  error
	)
	if strings.HasPrefix(r.URL.Path, "/feed/comments/") {
		list, err = s.Feed.CommentReactors(r.Context(), sess.UserID, r.PathValue("id"))
	} else {
		list, err = s.Feed.Reactors(r.Context(), sess.UserID, r.PathValue("id"))
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := make([]map[string]string, len(list))
	for i, x := range list {
		out[i] = map[string]string{"id": x.ID, "name": x.Name, "photo": company.PhotoURL(x.ID, x.Photo),
			"reaction": x.Reaction, "emoji": x.Emoji()}
	}
	writeJSON(w, http.StatusOK, out)
}

// feedVote: the person's answer to the post's poll (no option — the vote is taken back); the answer is the post.
func (s *Server) feedVote(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	id := r.PathValue("id")
	err := s.Feed.Vote(r.Context(), sess.UserID, id, r.Form["option"])
	var html string
	if err == nil {
		html, err = s.feedItemHTML(r, sess, id)
	}
	if err != nil {
		if !wantsJSON(r) {
			s.taskErr(w, r, err, "/feed")
			return
		}
		s.jsonErr(w, r, err)
		return
	}
	if !wantsJSON(r) {
		http.Redirect(w, r, "/feed#post-"+id, http.StatusSeeOther)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// formPoll: the poll of the post form.
func formPoll(r *http.Request) *newPoll {
	return &newPoll{Question: r.FormValue("poll_question"), Options: strings.Split(r.FormValue("poll_options"), "\n"),
		PollKind: feed.PollKind{Multiple: r.FormValue("poll_multiple") == "on", Anonymous: r.FormValue("poll_anonymous") == "on"}}
}

// feedPollVoters: «Кто голосовал» — the people by answer (not of an anonymous poll).
func (s *Server) feedPollVoters(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Feed.PollVoters(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	out := make([]map[string]string, len(list))
	for i, x := range list {
		out[i] = map[string]string{"option": x.OptionID, "id": x.ID, "name": x.Name, "photo": company.PhotoURL(x.ID, x.Photo)}
	}
	writeJSON(w, http.StatusOK, out)
}

// feedPollClose: «Остановить опрос» (…/stop) / «Возобновить опрос» (…/resume) — the author or an admin; the answer
// is the post.
func (s *Server) feedPollClose(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	err := s.Feed.ClosePoll(r.Context(), sess.UserID, id, strings.HasSuffix(r.URL.Path, "/stop"))
	var html string
	if err == nil {
		html, err = s.feedItemHTML(r, sess, id)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// feedPollExport: «Выгрузить результаты» for Excel — the answers with their votes and, unless the poll is anonymous,
// who voted; the post's author or an admin.
func (s *Server) feedPollExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("id")
	err := s.Feed.CanChange(ctx, sess.UserID, id)
	var it feed.Item
	if err == nil {
		it, err = s.Feed.Item(ctx, sess.UserID, id)
	}
	if err == nil && (it.Post == nil || it.Post.Poll == nil) {
		err = feed.ErrNotFound
	}
	var voters []feed.PollVoter
	if err == nil && !it.Post.Poll.Anonymous {
		voters, err = s.Feed.PollVoters(ctx, sess.UserID, id)
	}
	if err != nil {
		s.taskErr(w, r, err, "/feed")
		return
	}
	poll := it.Post.Poll
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=poll-"+time.Now().Format(time.DateOnly)+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{poll.Question})
	head := []string{s.tr(r, "poll.csv_option"), s.tr(r, "poll.csv_votes"), "%"}
	if !poll.Anonymous {
		head = append(head, s.tr(r, "poll.csv_person"))
	}
	_ = cw.Write(head)
	for _, o := range poll.Options {
		_ = cw.Write([]string{o.Text, strconv.Itoa(o.Votes), strconv.Itoa(poll.Percent(o))})
		for _, v := range voters {
			if v.OptionID == o.ID {
				_ = cw.Write([]string{"", "", "", v.Name})
			}
		}
	}
	_ = cw.Write([]string{s.tr(r, "poll.voters", poll.Voters)})
	cw.Flush()
}

func (s *Server) feedRead(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	n, err := s.Feed.MarkRead(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": n, "text": s.tr(r, "feed.readers", n)})
}

// feedSeen: the open feed showed the person what came live — the menu counter starts over.
func (s *Server) feedSeen(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if _, err := s.Feed.Seen(r.Context(), sess.UserID); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"news": 0})
}

// feedFilterSave saves the filter of the feed under a name (POST /feed/filters: name, query) and opens it.
func (s *Server) feedFilterSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	vals, err := url.ParseQuery(r.FormValue("query"))
	if err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/feed")
		return
	}
	query := vals.Encode() // as the page encodes the current filter, to know it
	err = s.Feed.SaveFilter(r.Context(), sess.UserID, feed.SavedFilter{Name: r.FormValue("name"), Query: query})
	if errors.Is(err, feed.ErrFilterName) {
		err = i18n.E("feed.filter.name_required")
	}
	if err != nil {
		s.taskErr(w, r, err, "/feed")
		return
	}
	http.Redirect(w, r, "/feed?"+query, http.StatusSeeOther)
}

// feedFilterDelete forgets a saved filter (POST /feed/filters/delete: name).
func (s *Server) feedFilterDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Feed.DeleteFilter(r.Context(), sess.UserID, r.FormValue("name")); err != nil {
		s.taskErr(w, r, err, "/feed")
		return
	}
	http.Redirect(w, r, "/feed", http.StatusSeeOther)
}

func (s *Server) feedDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.removePost(r.Context(), sess, r.PathValue("id")); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// removePost deletes a post (its author or an administrator), from the messenger first.
func (s *Server) removePost(ctx context.Context, sess auth.Session, id string) error {
	if err := s.Feed.CanChange(ctx, sess.UserID, id); err != nil {
		return err
	}
	if s.Matrix != nil {
		if err := s.Matrix.PublishDelete(ctx, id); err != nil {
			s.Log.Error("feed delete in matrix", "err", err)
			return i18n.E("err.messenger")
		}
	}
	return s.Feed.DeletePost(ctx, sess.UserID, id)
}

// feedItemHTML renders one post for a script (after publishing or editing, and for live updates).
func (s *Server) feedItemHTML(r *http.Request, sess auth.Session, id string) (string, error) {
	return s.feedItem(r, sess, id, false)
}

// feedItem renders a post; fresh marks it new when someone else wrote it (it came live).
func (s *Server) feedItem(r *http.Request, sess auth.Session, id string, fresh bool) (string, error) {
	it, err := s.Feed.Item(r.Context(), sess.UserID, id)
	if err != nil {
		return "", err
	}
	if fresh && it.Post != nil {
		it.Post.New = it.Post.AuthorID != sess.UserID
	}
	s.fillProcesses(r.Context(), sess.UserID, []feed.Item{it})
	return s.feedFragment(r, "fitem", map[string]any{"I": it, "Me": sess.UserID, "Admin": sess.AdminActive(), "Guest": tasks.IsGuestCtx(r.Context())})
}

// feedEdit changes a post (its author); the messenger gets the edit too.
// With full=1 it is the whole edit form (feedEditForm): addressees, «Важное», files, the gratitude and the poll too.
func (s *Server) feedEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	var files []*multipart.FileHeader
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			s.jsonErr(w, r, i18n.E("err.file_too_big"))
			return
		}
		files = r.MultipartForm.File["files"]
	}
	title, body := r.FormValue("title"), r.FormValue("body") // parses the form
	var tags []string                                        // no «Теги» field — the typed tags stay
	if r.Form.Has("tags") {
		tags = append([]string{}, feed.ParseTags(r.FormValue("tags"))...)
	}
	err := s.Feed.CanChange(r.Context(), sess.UserID, id)
	var ch *postChange
	if err == nil && r.FormValue("full") == "1" {
		var c postChange
		c, err = s.readPostChange(r, id, files)
		ch = &c
	}
	if err == nil {
		err = s.changePost(r.Context(), sess, id, title, body, tags)
	}
	if err == nil && ch != nil {
		err = s.reworkPost(r, sess, id, *ch)
	}
	var html string
	if err == nil {
		html, err = s.feedItemHTML(r, sess, id)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html})
}

// changePost: a new title and text of the post (its author), its tags — typed (nil — as they are) and the #tags of
// the text; the edit goes to Matrix first.
func (s *Server) changePost(ctx context.Context, sess auth.Session, id, title, body string, tags []string) error {
	err := s.Feed.CanChange(ctx, sess.UserID, id)
	if err == nil && strings.TrimSpace(body) == "" {
		err = i18n.E("err.post_empty")
	}
	if err == nil && s.Matrix != nil {
		if perr := s.Matrix.PublishEdit(ctx, id, strings.TrimSpace(title), strings.TrimSpace(body)); perr != nil {
			s.Log.Error("feed edit in matrix", "err", perr)
			err = i18n.E("err.messenger")
		}
	}
	if err != nil {
		return err
	}
	if err := s.Feed.EditPost(ctx, sess.UserID, id, title, body); err != nil {
		return err
	}
	return s.Feed.SetTags(ctx, id, tags)
}

// formAudience reads the picked people and departments ("dept:" + id) of the form field "to".
func formAudience(r *http.Request) feed.Audience {
	var to feed.Audience
	for _, v := range r.Form["to"] {
		if d, ok := strings.CutPrefix(v, "dept:"); ok {
			to.Depts = append(to.Depts, d)
		} else if v != "" {
			to.Users = append(to.Users, v)
		}
	}
	return to
}

// feedShare adds addressees to a post («Поделиться»); the answer is the post with them.
func (s *Server) feedShare(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	var html string
	err := r.ParseForm()
	n := 0
	if err == nil {
		n, err = s.Feed.Share(r.Context(), sess.UserID, id, formAudience(r))
	}
	if err == nil {
		html, err = s.feedItemHTML(r, sess, id)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": html, "msg": s.tr(r, "feed.shared", n)})
}

// feedLive streams new posts (from go24 or the messenger) to an open feed.
func (s *Server) feedLive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Live == nil {
		http.NotFound(w, r)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	events, stop := s.Live.Subscribe("feed")
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case id := <-events:
			html, err := s.feedItem(r, sess, id, true)
			if err != nil {
				continue // not for this viewer
			}
			data, _ := json.Marshal(map[string]string{"id": id, "html": html})
			fmt.Fprintf(w, "event: post\ndata: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// markHashtags makes the #tags of an already escaped text links to the feed of the tag.
func markHashtags(escaped string) string {
	if !strings.Contains(escaped, "#") {
		return escaped
	}
	return feed.Hashtag.ReplaceAllStringFunc(escaped, func(m string) string {
		i := strings.IndexByte(m, '#')
		tag := feed.NormTag(m[i+1:])
		if tag == "" || strings.Trim(tag, "0123456789") == "" {
			return m
		}
		return m[:i] + `<a class="ftag" href="/feed?tag=` + url.QueryEscape(tag) + `">` + m[i:] + `</a>`
	})
}

// feedMeeting is a meeting of «Ближайшие события» with its day: today | tomorrow | "" (another one).
type feedMeeting struct {
	calendar.Event
	Day string
}

// feedSide is the right column of the feed: the coming birthdays of colleagues (with «Поздравить» — a
// gratitude to them), «Ближайшие события» of one's calendar for a week, the important posts the viewer has not confirmed yet; «Мои задачи» are the page's counters.
func (s *Server) feedSide(r *http.Request, sess auth.Session) (map[string]any, error) {
	side := map[string]any{}
	if tasks.IsGuestCtx(r.Context()) {
		return side, nil
	}
	if s.Company != nil {
		list, err := s.Company.Birthdays(r.Context(), 14)
		if err != nil {
			return nil, err
		}
		side["Birthdays"] = list[:min(len(list), 5)]
	}
	if s.Calendar != nil {
		now := time.Now()
		list, err := s.Calendar.Upcoming(r.Context(), sess.UserID, now, 7, 5)
		if err != nil {
			return nil, err
		}
		var near []feedMeeting
		for _, e := range list {
			m, day := feedMeeting{Event: e}, e.Start.Local().Format(time.DateOnly)
			if e.Start.Before(now) || day == now.Format(time.DateOnly) {
				m.Day = "today"
			} else if day == now.AddDate(0, 0, 1).Format(time.DateOnly) {
				m.Day = "tomorrow"
			}
			near = append(near, m)
		}
		side["Events"] = near
	}
	imp, err := s.Feed.List(r.Context(), sess.UserID, feed.Filter{Important: true, Posts: true})
	if err != nil {
		return nil, err
	}
	type note struct{ ID, Author, Text string }
	var unread []note
	for _, it := range imp {
		if p := it.Post; p != nil && !p.Read && p.AuthorID != sess.UserID && len(unread) < 3 {
			text := p.Title
			if text == "" {
				text = clipText(strings.Join(strings.Fields(msgfmt.Plain(p.Body)), " "), 90)
			}
			unread = append(unread, note{p.ID, p.AuthorName, text})
		}
	}
	side["Important"] = unread
	if p := s.feedPulse(r, sess); p != nil {
		side["Pulse"] = p
	}
	return side, nil
}
