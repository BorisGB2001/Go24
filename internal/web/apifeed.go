package web

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Лента» over REST, as log.blogpost.* and log.blogcomment.* ----------
//
// The posts the key's owner sees, as on the feed's page: to all employees, to a project, to chosen people and
// departments. Publishing, comments, reactions, polls and important posts work as there and reach the messenger.

func (s *Server) apiFeedRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/feed/posts", s.api("feed:read", s.apiFeedList))
	mux.HandleFunc("POST /api/v1/feed/posts", s.api("feed:write", s.apiFeedCreate))
	mux.HandleFunc("GET /api/v1/feed/posts/{id}", s.api("feed:read", s.apiFeedGet))
	mux.HandleFunc("PATCH /api/v1/feed/posts/{id}", s.api("feed:write", s.apiFeedEdit))
	mux.HandleFunc("DELETE /api/v1/feed/posts/{id}", s.api("feed:write", s.apiFeedDelete))
	mux.HandleFunc("POST /api/v1/feed/posts/{id}/comments", s.api("feed:write", s.apiFeedComment))
	mux.HandleFunc("PATCH /api/v1/feed/comments/{id}", s.api("feed:write", s.apiFeedCommentEdit))
	mux.HandleFunc("DELETE /api/v1/feed/comments/{id}", s.api("feed:write", s.apiFeedCommentDelete))
	mux.HandleFunc("PUT /api/v1/feed/posts/{id}/reaction", s.api("feed:write", s.apiFeedReact))
	mux.HandleFunc("DELETE /api/v1/feed/posts/{id}/reaction", s.api("feed:write", s.apiFeedReact))
	mux.HandleFunc("PUT /api/v1/feed/comments/{id}/reaction", s.api("feed:write", s.apiFeedReact))
	mux.HandleFunc("DELETE /api/v1/feed/comments/{id}/reaction", s.api("feed:write", s.apiFeedReact))
	mux.HandleFunc("GET /api/v1/feed/posts/{id}/reactions", s.api("feed:read", s.apiFeedReactors))
	mux.HandleFunc("POST /api/v1/feed/posts/{id}/vote", s.api("feed:write", s.apiFeedVote))
	mux.HandleFunc("GET /api/v1/feed/posts/{id}/poll/voters", s.api("feed:read", s.apiFeedPollVoters))
	mux.HandleFunc("POST /api/v1/feed/posts/{id}/poll/stop", s.api("feed:write", s.apiFeedPollClose))
	mux.HandleFunc("POST /api/v1/feed/posts/{id}/poll/resume", s.api("feed:write", s.apiFeedPollClose))
	mux.HandleFunc("POST /api/v1/feed/posts/{id}/read", s.api("feed:write", s.apiFeedRead))
	mux.HandleFunc("POST /api/v1/feed/posts/{id}/share", s.api("feed:write", s.apiFeedShare))
	mux.HandleFunc("GET /api/v1/feed/posts/{id}/views", s.api("feed:read", s.apiFeedViews))
	for _, m := range feedMarks {
		mux.HandleFunc("PUT /api/v1/feed/posts/{id}/"+m, s.api("feed:write", s.apiFeedMark))
		mux.HandleFunc("DELETE /api/v1/feed/posts/{id}/"+m, s.api("feed:write", s.apiFeedMark))
	}
	mux.HandleFunc("GET /api/v1/feed/files/{id}", s.api("feed:read", s.apiFeedFile))
}

// feedMarks are the person's marks of a post: «В избранное», «Закрепить», «Следить», «Скрыть».
var feedMarks = []string{"fav", "pin", "follow", "hide"}

type apiFeedFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"` // the bytes: GET with the same key
}

type apiReactions struct {
	Mine  string         `json:"mine,omitempty"` // the key owner's reaction
	Total int            `json:"total"`
	Top   map[string]int `json:"counts"`
}

type apiFeedComment struct {
	ID        string        `json:"id"`
	Author    apiUser       `json:"author"`
	Body      string        `json:"body"`
	CreatedAt time.Time     `json:"created_at"`
	Edited    bool          `json:"edited"`
	Files     []apiFeedFile `json:"files"`
	Reactions apiReactions  `json:"reactions"`
	CanEdit   bool          `json:"can_edit"`
	CanDelete bool          `json:"can_delete"`
}

type apiFeedRecipient struct {
	Kind string `json:"kind"` // user | department
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiFeedPollOption struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Votes int    `json:"votes"`
	Mine  bool   `json:"mine"`
}

type apiFeedPoll struct {
	Question  string              `json:"question"`
	Multiple  bool                `json:"multiple"`
	Anonymous bool                `json:"anonymous"` // who voted is not told
	Closed    bool                `json:"closed"`    // stopped: no more votes
	Voters    int                 `json:"voters"`
	Voted     bool                `json:"voted"`
	Options   []apiFeedPollOption `json:"options"`
}

type apiGratitude struct {
	Badge  string    `json:"badge"`
	People []apiUser `json:"people"`
}

type apiFeedPost struct {
	ID        string             `json:"id"`
	Author    apiUser            `json:"author"`
	Project   string             `json:"project,omitempty"` // the project's key; none — all employees or "to"
	To        []apiFeedRecipient `json:"to,omitempty"`
	Title     string             `json:"title,omitempty"`
	Body      string             `json:"body"`
	CreatedAt time.Time          `json:"created_at"`
	Edited    bool               `json:"edited"`
	Important bool               `json:"important"`
	Until     *time.Time         `json:"important_until,omitempty"`
	Read      bool               `json:"read"`    // the key owner confirmed the important post
	Readers   int                `json:"readers"` // who confirmed it
	Views     int                `json:"views"`
	Files     []apiFeedFile      `json:"files"`
	Poll      *apiFeedPoll       `json:"poll,omitempty"`
	Gratitude *apiGratitude      `json:"gratitude,omitempty"`
	Reactions apiReactions       `json:"reactions"`
	Comments  []apiFeedComment   `json:"comments"`
	Fav       bool               `json:"fav"`
	Pinned    bool               `json:"pinned"`
	Following bool               `json:"following"`
	Hidden    bool               `json:"hidden"`
	Tags      []string           `json:"tags"`              // «Теги»
	Process   string             `json:"process,omitempty"` // the business process the post is of
	URL       string             `json:"url"`               // the post on the feed's page
}

func apiFeedFiles(list []feed.File) []apiFeedFile {
	out := []apiFeedFile{}
	for _, f := range list {
		out = append(out, apiFeedFile{f.ID, f.Name, f.Size, f.ContentType, "/api/v1/feed/files/" + f.ID})
	}
	return out
}

func apiReactionsOf(sm feed.Summary) apiReactions {
	out := apiReactions{Mine: sm.Mine, Total: sm.Total, Top: map[string]int{}}
	for _, c := range sm.Top {
		out.Top[c.Reaction] = c.N
	}
	return out
}

func apiFeedCommentOf(c feed.Comment) apiFeedComment {
	return apiFeedComment{ID: c.ID, Author: apiUser{c.AuthorID, c.AuthorName}, Body: c.Body, CreatedAt: c.CreatedAt,
		Edited: c.Edited, Files: apiFeedFiles(c.Files), Reactions: apiReactionsOf(c.Reactions), CanEdit: c.CanEdit, CanDelete: c.CanDelete}
}

func (s *Server) apiFeedPostOf(p *feed.Post) apiFeedPost {
	out := apiFeedPost{ID: p.ID, Author: apiUser{p.AuthorID, p.AuthorName}, Project: p.ProjectKey, Title: p.Title, Body: p.Body,
		CreatedAt: p.CreatedAt, Edited: p.Edited, Important: p.Important, Read: p.Read, Readers: p.Readers, Views: p.Views,
		Files: apiFeedFiles(p.Files), Reactions: apiReactionsOf(p.Reactions), Comments: []apiFeedComment{},
		Fav: p.Fav, Pinned: p.Pinned, Following: p.Following, Hidden: p.Hidden, Tags: append([]string{}, p.Tags...), Process: p.Process,
		URL: strings.TrimRight(s.BaseURL, "/") + "/feed?post=" + p.ID}
	if p.Important && !p.Until.IsZero() {
		out.Until = &p.Until
	}
	for _, t := range p.To {
		kind := "user"
		if t.Dept {
			kind = "department"
		}
		out.To = append(out.To, apiFeedRecipient{kind, t.ID, t.Name})
	}
	if p.Poll != nil {
		out.Poll = &apiFeedPoll{Question: p.Poll.Question, Multiple: p.Poll.Multiple, Anonymous: p.Poll.Anonymous,
			Closed: p.Poll.Closed, Voters: p.Poll.Voters, Voted: p.Poll.Voted, Options: []apiFeedPollOption{}}
		for _, o := range p.Poll.Options {
			out.Poll.Options = append(out.Poll.Options, apiFeedPollOption{o.ID, o.Text, o.Votes, o.Mine})
		}
	}
	if p.Gratitude != nil {
		out.Gratitude = &apiGratitude{Badge: p.Gratitude.Badge, People: []apiUser{}}
		for _, x := range p.Gratitude.People {
			out.Gratitude.People = append(out.Gratitude.People, apiUser{x.ID, x.Name})
		}
	}
	for _, c := range p.Comments {
		out.Comments = append(out.Comments, apiFeedCommentOf(c))
	}
	return out
}

// apiFeedList: the posts the key's owner sees, the newest first, a page at a time (next — "before" of the next
// page). The filter is the feed's: view=important|mine|fav|pinned|hidden, project (key), q, author, kind=poll|gratitude,
// from (YYYY-MM-DD), tag.
func (s *Server) apiFeedList(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	view := q.Get("view")
	if view != "" && !slices.Contains([]string{"important", "mine", "fav", "pinned", "hidden"}, view) {
		return i18n.E("err.bad_form")
	}
	f := feed.Filter{Important: view == "important", Mine: view == "mine", Favorites: view == "fav", Pinned: view == "pinned",
		Hidden: view == "hidden", Query: q.Get("q"), Author: q.Get("author"), Tag: q.Get("tag")}
	if k := q.Get("kind"); k != "" {
		if k != "poll" && k != "gratitude" {
			return i18n.E("err.bad_form")
		}
		f.Kind = k
	}
	if key := q.Get("project"); key != "" {
		proj, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, key)
		if err != nil {
			return err
		}
		f.Project = proj.ID
	}
	if v := q.Get("from"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		if err != nil {
			return i18n.E("err.bad_form")
		}
		f.From = t
	}
	if b := q.Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			return i18n.E("err.bad_form")
		}
		f.Before = t
	}
	f.Posts = true // the events of tasks are the tasks' API
	items, err := s.Feed.List(r.Context(), p.UserID, f)
	if err != nil {
		return err
	}
	out := struct {
		Posts []apiFeedPost `json:"posts"`
		Next  string        `json:"next,omitempty"`
	}{Posts: []apiFeedPost{}}
	for _, it := range items {
		if it.Post != nil {
			out.Posts = append(out.Posts, s.apiFeedPostOf(it.Post))
		}
	}
	if len(items) == feed.PageSize {
		out.Next = items[len(items)-1].At.Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiFeedPost(ctx context.Context, userID, id string) (apiFeedPost, error) {
	it, err := s.Feed.Item(ctx, userID, id)
	if err != nil {
		return apiFeedPost{}, err
	}
	if it.Post == nil {
		return apiFeedPost{}, feed.ErrNotFound
	}
	return s.apiFeedPostOf(it.Post), nil
}

func (s *Server) apiFeedGet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	out, err := s.apiFeedPost(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiFeedTo struct {
	Users       []string `json:"users"`
	Departments []string `json:"departments"`
}

func (t *apiFeedTo) audience() feed.Audience {
	if t == nil {
		return feed.Audience{}
	}
	return feed.Audience{Users: t.Users, Depts: t.Departments}
}

// apiFeedCreate publishes a post: to all employees, to a project (its key) or to people and departments ("to");
// important — until a date or always; a poll or a gratitude; files — files of the drives (uploaded to the drive
// first, as «Файл с Диска»).
func (s *Server) apiFeedCreate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title          string     `json:"title"`
		Body           string     `json:"body"`
		Project        string     `json:"project"`
		To             *apiFeedTo `json:"to"`
		Important      bool       `json:"important"`
		ImportantUntil string     `json:"important_until"` // YYYY-MM-DD, important to the end of the day
		Files          []string   `json:"files"`
		Tags           []string   `json:"tags"`
		Poll           *struct {
			Question  string   `json:"question"`
			Options   []string `json:"options"`
			Multiple  bool     `json:"multiple"`
			Anonymous bool     `json:"anonymous"`
		} `json:"poll"`
		Gratitude *struct {
			Badge  string   `json:"badge"`
			People []string `json:"people"`
		} `json:"gratitude"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	np := newPost{Title: in.Title, Body: in.Body, Important: in.Important, To: in.To.audience(), Drive: in.Files, Tags: in.Tags}
	if in.Project != "" {
		if len(np.To.Users)+len(np.To.Depts) > 0 {
			return i18n.E("err.bad_form")
		}
		proj, err := s.Tasks.ProjectByKey(r.Context(), p.UserID, in.Project)
		if err != nil {
			return err
		}
		np.Project = proj.ID
	}
	if in.ImportantUntil != "" {
		d, err := time.ParseInLocation("2006-01-02", in.ImportantUntil, time.Local)
		if err != nil {
			return i18n.E("err.bad_form")
		}
		if d = d.AddDate(0, 0, 1); !d.After(time.Now()) {
			return i18n.E("err.until_past")
		}
		np.Until = d
	}
	if in.Poll != nil {
		np.Poll = &newPoll{Question: in.Poll.Question, Options: in.Poll.Options,
			PollKind: feed.PollKind{Multiple: in.Poll.Multiple, Anonymous: in.Poll.Anonymous}}
	}
	if in.Gratitude != nil {
		np.Gratitude = &newGratitude{Badge: in.Gratitude.Badge, People: in.Gratitude.People}
	}
	id, err := s.publishPost(r, auth.Session{UserID: p.UserID}, np)
	if err != nil {
		return err
	}
	out, err := s.apiFeedPost(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, out)
	return nil
}

// apiFeedEdit changes the post's title and text (the author); important_until — a new date of «Важное до»; tags — the
// typed tags.
func (s *Server) apiFeedEdit(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title          *string   `json:"title"`
		Body           *string   `json:"body"`
		ImportantUntil *string   `json:"important_until"` // YYYY-MM-DD; "" — important always
		Tags           *[]string `json:"tags"`            // the typed tags, all of them (the #tags of the text stay)
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, id, sess := r.Context(), r.PathValue("id"), auth.Session{UserID: p.UserID}
	cur, err := s.apiFeedPost(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	var tags []string
	if in.Tags != nil {
		tags = append([]string{}, *in.Tags...)
	}
	if in.Title == nil && in.Body == nil && in.Tags != nil { // the tags only: the text is not «изменено»
		if err := s.Feed.CanChange(ctx, p.UserID, id); err != nil {
			return err
		}
		if err := s.Feed.SetTags(ctx, id, tags); err != nil {
			return err
		}
	}
	if in.Title != nil || in.Body != nil {
		title, body := cur.Title, cur.Body
		if in.Title != nil {
			title = *in.Title
		}
		if in.Body != nil {
			body = *in.Body
		}
		if err := s.changePost(ctx, sess, id, title, body, tags); err != nil {
			return err
		}
	}
	if in.ImportantUntil != nil {
		var until time.Time
		if *in.ImportantUntil != "" {
			d, err := time.ParseInLocation("2006-01-02", *in.ImportantUntil, time.Local)
			if err != nil {
				return i18n.E("err.bad_form")
			}
			if until = d.AddDate(0, 0, 1); !until.After(time.Now()) {
				return i18n.E("err.until_past")
			}
		}
		if err := s.Feed.SetUntil(ctx, p.UserID, id, until); err != nil {
			return err
		}
	}
	out, err := s.apiFeedPost(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiFeedDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.removePost(r.Context(), auth.Session{UserID: p.UserID}, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiFeedComment adds a comment: a text and files of the drives.
func (s *Server) apiFeedComment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Body  string   `json:"body"`
		Files []string `json:"files"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := s.publishComment(r, auth.Session{UserID: p.UserID}, r.PathValue("id"), in.Body, nil, in.Files)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, apiFeedCommentOf(c))
	return nil
}

func (s *Server) apiFeedCommentEdit(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := s.changeComment(r.Context(), auth.Session{UserID: p.UserID}, r.PathValue("id"), in.Body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiFeedCommentOf(c))
	return nil
}

func (s *Server) apiFeedCommentDelete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.removeComment(r.Context(), auth.Session{UserID: p.UserID}, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiFeedReact: PUT sets the key owner's reaction to a post or a comment ({"reaction": like | kiss | laugh | wonder | cry | angry | facepalm}, like by default), DELETE
// takes it back. The answer is the reactions.
func (s *Server) apiFeedReact(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, id, sess := r.Context(), r.PathValue("id"), auth.Session{UserID: p.UserID}
	comment := strings.HasPrefix(r.URL.Path, "/api/v1/feed/comments/")
	reaction := "like"
	if r.Method == http.MethodPut && r.ContentLength != 0 {
		var in struct {
			Reaction string `json:"reaction"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if in.Reaction != "" {
			if _, ok := feed.ReactionEmoji[in.Reaction]; !ok {
				return i18n.E("err.bad_form")
			}
			reaction = in.Reaction
		}
	}
	// reactTo toggles: the same reaction again takes it back, so a PUT of the one there is or a DELETE of none
	// leaves things as they are
	mine, _ := s.Feed.MyReaction(ctx, id, p.UserID, comment)
	if r.Method == http.MethodDelete {
		reaction = mine
	}
	var (
		sm  feed.Summary
		err error
	)
	if (r.Method == http.MethodPut && mine == reaction) || reaction == "" {
		sm, err = s.apiFeedSummary(ctx, p.UserID, id, comment)
	} else {
		sm, err = s.reactTo(ctx, sess, id, comment, reaction)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiReactionsOf(sm))
	return nil
}

// apiFeedSummary: the reactions of a post or a comment the person sees, unchanged.
func (s *Server) apiFeedSummary(ctx context.Context, userID, id string, comment bool) (feed.Summary, error) {
	if comment {
		c, err := s.Feed.Comment(ctx, userID, id)
		return c.Reactions, err
	}
	it, err := s.Feed.Item(ctx, userID, id)
	if err != nil || it.Post == nil {
		return feed.Summary{}, feed.ErrNotFound
	}
	return it.Post.Reactions, nil
}

// apiFeedReactors: «Кто оценил» — the people and their reactions, the latest first.
func (s *Server) apiFeedReactors(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Feed.Reactors(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	type reactor struct {
		apiUser
		Reaction string `json:"reaction"`
	}
	out := []reactor{}
	for _, x := range list {
		out = append(out, reactor{apiUser{x.ID, x.Name}, x.Reaction})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiFeedVote answers the post's poll: {"options": [ids]}, none — the vote is taken back.
func (s *Server) apiFeedVote(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Options []string `json:"options"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := s.Feed.Vote(r.Context(), p.UserID, id, in.Options); err != nil {
		return err
	}
	out, err := s.apiFeedPost(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiFeedPollVoters: who voted for each answer of the post's poll — [{"option", "user": {id, name}}]; not of an
// anonymous poll.
func (s *Server) apiFeedPollVoters(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Feed.PollVoters(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	type voter struct {
		Option string  `json:"option"`
		User   apiUser `json:"user"`
	}
	out := make([]voter, len(list))
	for i, v := range list {
		out[i] = voter{v.OptionID, apiUser{v.ID, v.Name}}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiFeedPollClose stops the post's poll (stop) or resumes it (resume) — its author or an admin; the answer is the
// post.
func (s *Server) apiFeedPollClose(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	if err := s.Feed.ClosePoll(r.Context(), p.UserID, id, strings.HasSuffix(r.URL.Path, "/stop")); err != nil {
		return err
	}
	out, err := s.apiFeedPost(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiFeedRead confirms an important post as read («Прочитано»).
func (s *Server) apiFeedRead(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	n, err := s.Feed.MarkRead(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]int{"readers": n})
	return nil
}

// apiFeedShare adds addressees to the post («Поделиться»): {"users": [...], "departments": [...]}.
func (s *Server) apiFeedShare(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in apiFeedTo
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if _, err := s.Feed.Share(r.Context(), p.UserID, id, in.audience()); err != nil {
		return err
	}
	out, err := s.apiFeedPost(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiFeedViews: «Просмотры» — who saw the post and when, the latest first.
func (s *Server) apiFeedViews(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Feed.Viewers(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	type viewer struct {
		apiUser
		At time.Time `json:"at"`
	}
	out := []viewer{}
	for _, v := range list {
		out = append(out, viewer{apiUser{v.ID, v.Name}, v.At})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiFeedMark: PUT puts the key owner's mark (fav, pin, follow, hide) on the post, DELETE takes it off.
func (s *Server) apiFeedMark(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, id, mark := r.Context(), r.PathValue("id"), r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	cur, err := s.apiFeedPost(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	on := map[string]bool{"fav": cur.Fav, "pin": cur.Pinned, "follow": cur.Following, "hide": cur.Hidden}[mark]
	if on != (r.Method == http.MethodPut) { // Mark toggles
		if on, err = s.Feed.Mark(ctx, p.UserID, id, mark); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"on": on})
	return nil
}

// apiFeedFile gives the bytes of a file of a post or a comment the key's owner sees.
func (s *Server) apiFeedFile(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	m, err := s.fileAccess(r, auth.Session{UserID: p.UserID}, r.PathValue("id"))
	if err != nil {
		return err
	}
	if m.PostID == nil {
		return tasks.ErrNotFound // not a file of the feed
	}
	var obj io.ReadCloser = io.NopCloser(strings.NewReader(""))
	if m.Size > 0 {
		if obj, err = s.Storages.Open(r.Context(), m.StorageID, m.Key); err != nil {
			return err
		}
	}
	defer obj.Close()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Content-Length", fmt.Sprint(m.Size))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(m.Name)))
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("api feed file", "err", err)
	}
	return nil
}
