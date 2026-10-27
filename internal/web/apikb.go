package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/kb"
	"go24/internal/tasks"
)

// ---------- «База знаний» over REST, as landing.site.* and landing.landing.* for knowledge bases ----------
//
// The rights are those of the pages: a base is seen and written in as «Права доступа» of the base say (or, without
// them, as its project or the company), managed by its creator, the project's managers and administrators. A page's
// text is Markdown; "html" is the same safe HTML the page shows. Deleted bases and pages go to the trash.

func (s *Server) apiKBRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/kb/spaces", s.api("kb:read", s.apiKBSpaces))
	mux.HandleFunc("POST /api/v1/kb/spaces", s.api("kb:write", s.apiKBCreateSpace))
	mux.HandleFunc("GET /api/v1/kb/spaces/{space}", s.api("kb:read", s.apiKBSpace))
	mux.HandleFunc("PATCH /api/v1/kb/spaces/{space}", s.api("kb:write", s.apiKBUpdateSpace))
	mux.HandleFunc("DELETE /api/v1/kb/spaces/{space}", s.api("kb:write", s.apiKBDeleteSpace))
	mux.HandleFunc("GET /api/v1/kb/spaces/{space}/rights", s.api("kb:read", s.apiKBRights))
	mux.HandleFunc("PUT /api/v1/kb/spaces/{space}/rights", s.api("kb:write", s.apiKBSetRight))
	mux.HandleFunc("DELETE /api/v1/kb/spaces/{space}/rights/{id}", s.api("kb:write", s.apiKBDeleteRight))
	mux.HandleFunc("POST /api/v1/kb/spaces/{space}/pages", s.api("kb:write", s.apiKBCreatePage))
	mux.HandleFunc("POST /api/v1/kb/spaces/{space}/files", s.api("kb:write", s.apiKBUpload))
	mux.HandleFunc("GET /api/v1/kb/spaces/{space}/export", s.api("kb:read", s.apiKBExport))
	mux.HandleFunc("POST /api/v1/kb/spaces/{space}/import", s.api("kb:write", s.apiKBImport))
	mux.HandleFunc("GET /api/v1/kb/pages/{page}", s.api("kb:read", s.apiKBPage))
	mux.HandleFunc("PATCH /api/v1/kb/pages/{page}", s.api("kb:write", s.apiKBSavePage))
	mux.HandleFunc("DELETE /api/v1/kb/pages/{page}", s.api("kb:write", s.apiKBDeletePage))
	mux.HandleFunc("POST /api/v1/kb/pages/{page}/move", s.api("kb:write", s.apiKBMovePage))
	mux.HandleFunc("POST /api/v1/kb/pages/{page}/copy", s.api("kb:write", s.apiKBCopyPage))
	mux.HandleFunc("GET /api/v1/kb/pages/{page}/versions", s.api("kb:read", s.apiKBVersions))
	mux.HandleFunc("GET /api/v1/kb/pages/{page}/versions/{v}", s.api("kb:read", s.apiKBVersion))
	mux.HandleFunc("POST /api/v1/kb/pages/{page}/versions/{v}/restore", s.api("kb:write", s.apiKBRestoreVersion))
	mux.HandleFunc("GET /api/v1/kb/search", s.api("kb:read", s.apiKBSearch))
	mux.HandleFunc("GET /api/v1/kb/templates", s.api("kb:read", s.apiKBTemplates))
	mux.HandleFunc("GET /api/v1/kb/page-templates", s.api("kb:read", s.apiKBPageTemplates))
	mux.HandleFunc("POST /api/v1/kb/pages/{page}/template", s.api("kb:write", s.apiKBSavePageTemplate))
	mux.HandleFunc("GET /api/v1/kb/trash", s.api("kb:read", s.apiKBTrash))
	mux.HandleFunc("POST /api/v1/kb/trash/restore", s.api("kb:write", s.apiKBTrashRestore))
	mux.HandleFunc("GET /api/v1/kb/files/{id}", s.api("kb:read", s.apiKBDownload))
	mux.HandleFunc("GET /api/v1/kb/favorites", s.api("kb:read", s.apiKBFavorites))
	mux.HandleFunc("GET /api/v1/kb/recent", s.api("kb:read", s.apiKBRecent))
	mux.HandleFunc("GET /api/v1/kb/spaces/{space}/marks", s.api("kb:read", s.apiKBMarks))
	mux.HandleFunc("PUT /api/v1/kb/spaces/{space}/marks/{kind}", s.api("kb:read", s.apiKBMark))
	mux.HandleFunc("DELETE /api/v1/kb/spaces/{space}/marks/{kind}", s.api("kb:read", s.apiKBMark))
	mux.HandleFunc("PUT /api/v1/kb/pages/{page}/marks/{kind}", s.api("kb:read", s.apiKBMark))
	mux.HandleFunc("DELETE /api/v1/kb/pages/{page}/marks/{kind}", s.api("kb:read", s.apiKBMark))
	mux.HandleFunc("GET /api/v1/kb/pages/{page}/comments", s.api("kb:read", s.apiKBComments))
	mux.HandleFunc("POST /api/v1/kb/pages/{page}/comments", s.api("kb:write", s.apiKBAddComment))
	mux.HandleFunc("PATCH /api/v1/kb/comments/{id}", s.api("kb:write", s.apiKBEditComment))
	mux.HandleFunc("DELETE /api/v1/kb/comments/{id}", s.api("kb:write", s.apiKBDeleteComment))
	mux.HandleFunc("PUT /api/v1/kb/pages/{page}/like", s.api("kb:read", s.apiKBLike))
	mux.HandleFunc("DELETE /api/v1/kb/pages/{page}/like", s.api("kb:read", s.apiKBLike))
	mux.HandleFunc("GET /api/v1/kb/pages/{page}/draft", s.api("kb:write", s.apiKBDraft))
	mux.HandleFunc("PUT /api/v1/kb/pages/{page}/draft", s.api("kb:write", s.apiKBSaveDraft))
	mux.HandleFunc("DELETE /api/v1/kb/pages/{page}/draft", s.api("kb:write", s.apiKBDropDraft))
}

type apiKBSpace struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Project     string        `json:"project,omitempty"` // the key of the project the base is of ("" — for everyone)
	ProjectName string        `json:"project_name,omitempty"`
	Pages       int           `json:"pages"`
	CanEdit     bool          `json:"can_edit"`
	CanManage   bool          `json:"can_manage"`
	Restricted  bool          `json:"restricted"`     // the base has «Права доступа» of its own
	Tree        []*apiKBTitle `json:"tree,omitempty"` // the pages, in their order (one base only)
}

type apiKBTitle struct {
	ID       string        `json:"id"`
	Title    string        `json:"title"`
	Children []*apiKBTitle `json:"children,omitempty"`
}

type apiKBPage struct {
	ID        string        `json:"id"`
	Space     string        `json:"space"`
	Parent    string        `json:"parent,omitempty"`
	Title     string        `json:"title"`
	Body      string        `json:"body"` // Markdown
	HTML      string        `json:"html"`
	Author    apiUser       `json:"author"`
	Editor    apiUser       `json:"editor"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Path      []*apiKBTitle `json:"path"`     // the pages above it, from the top
	Children  []*apiKBTitle `json:"children"` // the pages right under it
	Link      string        `json:"link"`     // the page in go24
	Favorite  bool          `json:"favorite"` // the key's owner starred it
	Watching  bool          `json:"watching"` // the key's owner watches it, or a page above it, or the base
	Comments  int           `json:"comments"` // with the replies
	Likes     []string      `json:"likes"`    // who likes it
	Liked     bool          `json:"liked"`    // the key's owner likes it
	Views     int           `json:"views"`    // how many people opened it
	Editing   []string      `json:"editing"`  // the others editing it now
}

// apiKBComment is a comment under a page; the top ones carry their replies.
type apiKBComment struct {
	ID        string          `json:"id"`
	Parent    string          `json:"parent,omitempty"`
	Author    apiUser         `json:"author"`
	Body      string          `json:"body"` // Markdown
	HTML      string          `json:"html"`
	CreatedAt time.Time       `json:"created_at"`
	EditedAt  *time.Time      `json:"edited_at,omitempty"`
	CanEdit   bool            `json:"can_edit"`
	CanDelete bool            `json:"can_delete"`
	Replies   []*apiKBComment `json:"replies,omitempty"`
}

func apiKBCommentOf(c *kb.Comment) *apiKBComment {
	out := &apiKBComment{ID: c.ID, Parent: c.ParentID, Author: apiUser{c.AuthorID, c.Author}, Body: c.Body, HTML: string(kb.Render(c.Body)),
		CreatedAt: c.Created, EditedAt: c.Edited, CanEdit: c.CanEdit, CanDelete: c.CanDelete}
	for _, r := range c.Replies {
		out.Replies = append(out.Replies, apiKBCommentOf(r))
	}
	return out
}

func apiKBSpaceOf(sp kb.Space) apiKBSpace {
	return apiKBSpace{ID: sp.ID, Name: sp.Name, Description: sp.Description, Project: sp.ProjectKey, ProjectName: sp.ProjectName,
		Pages: sp.Pages, CanEdit: sp.CanEdit, CanManage: sp.CanManage, Restricted: sp.Restricted}
}

func apiKBTitles(list []*kb.Page, deep bool) []*apiKBTitle {
	out := []*apiKBTitle{}
	for _, p := range list {
		t := &apiKBTitle{ID: p.ID, Title: p.Title}
		if deep {
			t.Children = apiKBTitles(p.Children, true)
			if len(t.Children) == 0 {
				t.Children = nil
			}
		}
		out = append(out, t)
	}
	return out
}

// apiKBSpaces are the bases the key's owner sees.
func (s *Server) apiKBSpaces(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.KB.Spaces(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []apiKBSpace{}
	for _, sp := range list {
		out = append(out, apiKBSpaceOf(sp))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiKBSpace is a base with the tree of its pages.
func (s *Server) apiKBSpace(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sp, err := s.KB.Space(r.Context(), p.UserID, r.PathValue("space"))
	if err != nil {
		return err
	}
	return s.apiKBWriteSpace(w, r, sp, http.StatusOK)
}

func (s *Server) apiKBWriteSpace(w http.ResponseWriter, r *http.Request, sp kb.Space, status int) error {
	tree, err := s.KB.Tree(r.Context(), sp.ID)
	if err != nil {
		return err
	}
	out := apiKBSpaceOf(sp)
	out.Tree = apiKBTitles(tree, true)
	writeJSON(w, status, out)
	return nil
}

// apiKBCreateSpace makes a base: {"name", "description", "project": "KEY" (none — for everyone), "template": "ID"
// (none — empty; see …/templates)}.
func (s *Server) apiKBCreateSpace(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Name, Description, Project, Template string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, project := r.Context(), ""
	if in.Project != "" {
		pr, err := s.Tasks.ProjectByKey(ctx, p.UserID, in.Project)
		if err != nil {
			return err
		}
		project = pr.ID
	}
	var id string
	var err error
	if in.Template != "" {
		id, err = s.KB.CreateFromTemplate(ctx, p.UserID, in.Template, in.Name, in.Description, project, s.apiTr(r), s.Storages.Copy)
	} else {
		id, err = s.KB.CreateSpace(ctx, p.UserID, in.Name, in.Description, project)
	}
	if err != nil {
		return err
	}
	sp, err := s.KB.Space(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	return s.apiKBWriteSpace(w, r, sp, http.StatusCreated)
}

// apiKBUpdateSpace renames a base or changes its description: {"name", "description"} — what is left out stays.
func (s *Server) apiKBUpdateSpace(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Name, Description *string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	sp, err := s.KB.Space(r.Context(), p.UserID, r.PathValue("space"))
	if err != nil {
		return err
	}
	name, desc := sp.Name, sp.Description
	if in.Name != nil {
		name = *in.Name
	}
	if in.Description != nil {
		desc = *in.Description
	}
	if err := s.KB.UpdateSpace(r.Context(), p.UserID, sp.ID, name, desc); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBDeleteSpace puts a base with its pages into the trash.
func (s *Server) apiKBDeleteSpace(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.KB.DeleteSpace(r.Context(), p.UserID, r.PathValue("space")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiKBRight struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // all | user | department
	WhomID string `json:"whom_id,omitempty"`
	Whom   string `json:"whom,omitempty"`
	Access string `json:"access"` // read | edit | full
}

// apiKBRights are «Права доступа» of a base (those who manage it); none — the base has the rules of its project or
// the company.
func (s *Server) apiKBRights(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sp, err := s.KB.Space(r.Context(), p.UserID, r.PathValue("space"))
	if err == nil && !sp.CanManage {
		err = kb.ErrForbidden
	}
	if err != nil {
		return err
	}
	list, err := s.KB.Rights(r.Context(), sp.ID)
	if err != nil {
		return err
	}
	out := []apiKBRight{}
	for _, x := range list {
		out = append(out, apiKBRight{x.ID, x.Kind, x.WhomID, x.Whom, x.Access})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiKBSetRight opens a base to everyone, an employee or a department (with its sub-departments), or changes their
// access: {"kind": "all" | "user" | "department", "whom_id", "access": "read" | "edit" | "full"}. The first line
// closes the base to all the others.
func (s *Server) apiKBSetRight(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Kind   string `json:"kind"`
		WhomID string `json:"whom_id"`
		Access string `json:"access"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.KB.SetRight(r.Context(), p.UserID, r.PathValue("space"), in.Kind, in.WhomID, in.Access); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) apiKBDeleteRight(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.KB.DeleteRight(r.Context(), p.UserID, r.PathValue("space"), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBPage is a page: its text, who wrote and changed it, where it is in the tree.
func (s *Server) apiKBPage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiKBWritePage(w, r, p, r.PathValue("page"), http.StatusOK)
}

func (s *Server) apiKBWritePage(w http.ResponseWriter, r *http.Request, p apikeys.Principal, id string, status int) error {
	pg, sp, err := s.KB.Page(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	tree, err := s.KB.Tree(r.Context(), sp.ID)
	if err != nil {
		return err
	}
	out := apiKBPage{ID: pg.ID, Space: sp.ID, Parent: pg.ParentID, Title: pg.Title, Body: pg.Body, HTML: string(kb.Render(pg.Body)),
		Author: apiUser{pg.AuthorID, pg.Author}, Editor: apiUser{pg.EditorID, pg.Editor}, CreatedAt: pg.Created, UpdatedAt: pg.Updated,
		Path: []*apiKBTitle{}, Children: []*apiKBTitle{}, Link: strings.TrimRight(s.BaseURL, "/") + "/kb/" + sp.ID + "/" + pg.ID}
	if path := kb.Path(tree, pg.ID); len(path) > 0 {
		out.Path, out.Children = apiKBTitles(path[:len(path)-1], false), apiKBTitles(path[len(path)-1].Children, false)
	}
	m, err := s.KB.MarksAt(r.Context(), p.UserID, sp.ID, pg.ID)
	if err != nil {
		return err
	}
	out.Favorite, out.Watching = m.FavPage, m.WatchPage || m.Inherited
	talk, err := s.KB.Discussion(r.Context(), p.UserID, pg, sp)
	if err != nil {
		return err
	}
	out.Comments, out.Likes, out.Liked, out.Views = talk.Count, talk.Likes, talk.Liked, talk.Views
	if out.Editing, err = s.KB.Editing(r.Context(), p.UserID, pg.ID); err != nil {
		return err
	}
	writeJSON(w, status, out)
	return nil
}

// apiKBCreatePage writes a new page: {"title", "body" (Markdown), "parent": "ID" (none — at the top)}.
func (s *Server) apiKBCreatePage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Title, Body, Parent, Template string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var id string
	var err error
	if in.Template != "" { // the text is the template's (…/page-templates), the title may be left out
		lang := s.I18n.Match(r.Header.Get("Accept-Language"))
		id, err = s.KB.CreatePageFromTemplate(r.Context(), p.UserID, r.PathValue("space"), in.Parent, in.Template, in.Title,
			kb.PageVars{Date: time.Now().Format(s.I18n.T(lang, "format.date"))}, s.apiTr(r), s.Storages.Copy)
	} else {
		id, err = s.KB.CreatePage(r.Context(), p.UserID, r.PathValue("space"), in.Parent, in.Title, in.Body)
	}
	if err != nil {
		return err
	}
	s.feedKBPage(r.Context(), p.UserID, id)
	return s.apiKBWritePage(w, r, p, id, http.StatusCreated)
}

// apiKBSavePage changes a page: {"title", "body"} — what is left out stays; the earlier text goes to the history.
// With "base" (the page's updated_at the text was written over) it is not saved if someone saved the page since:
// 409. The owner's draft of the page goes.
func (s *Server) apiKBSavePage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title, Body *string
		Base        time.Time
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	pg, _, err := s.KB.Page(r.Context(), p.UserID, r.PathValue("page"))
	if err != nil {
		return err
	}
	title, body := pg.Title, pg.Body
	if in.Title != nil {
		title = *in.Title
	}
	if in.Body != nil {
		body = *in.Body
	}
	if err := s.KB.SavePageAt(r.Context(), p.UserID, pg.ID, title, body, in.Base); err != nil {
		return err
	}
	return s.apiKBWritePage(w, r, p, pg.ID, http.StatusOK)
}

// apiKBDeletePage puts a page with the pages under it into the trash.
func (s *Server) apiKBDeletePage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.KB.DeletePage(r.Context(), p.UserID, r.PathValue("page")); err != nil {
		return err
	}
	s.withdraw(r.Context(), feed.SourceKB, r.PathValue("page"))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBMovePage puts a page with the pages under it elsewhere: {"space": "ID" (another base the owner writes in;
// none — its own), "parent": "ID" (none — at the top), "before": "ID" (in its own base: before that page; none —
// last)}.
func (s *Server) apiKBMovePage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Space, Parent, Before string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, id := r.Context(), r.PathValue("page")
	pg, _, err := s.KB.Page(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	if in.Space != "" && in.Space != pg.SpaceID {
		if in.Before != "" {
			return i18n.E("err.bad_json", "before")
		}
		err = s.KB.MovePage(ctx, p.UserID, id, in.Space, in.Parent)
	} else {
		err = s.KB.PlacePage(ctx, p.UserID, id, in.Parent, in.Before)
	}
	if err != nil {
		return err
	}
	return s.apiKBWritePage(w, r, p, id, http.StatusOK)
}

// apiKBCopyPage makes a copy of a page (without the pages under it) right after it: {"title"} — none, «Копия …».
func (s *Server) apiKBCopyPage(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Title string }
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	pg, _, err := s.KB.Page(r.Context(), p.UserID, r.PathValue("page"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(in.Title) == "" {
		in.Title = s.I18n.T(s.I18n.Match(r.Header.Get("Accept-Language")), "kb.copy_of", pg.Title)
	}
	id, err := s.KB.CopyPage(r.Context(), p.UserID, pg.ID, in.Title)
	if err != nil {
		return err
	}
	return s.apiKBWritePage(w, r, p, id, http.StatusCreated)
}

type apiKBVersion struct {
	ID       int64     `json:"id"`
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
	Editor   apiUser   `json:"editor"`
	EditedAt time.Time `json:"edited_at"`
}

// apiKBVersions are the page's earlier states, newest first (the last 50).
func (s *Server) apiKBVersions(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pg, _, err := s.KB.Page(r.Context(), p.UserID, r.PathValue("page"))
	if err != nil {
		return err
	}
	list, err := s.KB.Versions(r.Context(), pg.ID)
	if err != nil {
		return err
	}
	out := []apiKBVersion{}
	for _, v := range list {
		out = append(out, apiKBVersion{ID: v.ID, Title: v.Title, Editor: apiUser{v.EditorID, v.Editor}, EditedAt: v.EditedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func apiKBVersionID(r *http.Request) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue("v"), 10, 64)
	if err != nil {
		return 0, kb.ErrNotFound
	}
	return v, nil
}

// apiKBVersion is an earlier text of the page; ?diff=1 — also its lines against the current text («Сравнить с
// текущей»).
func (s *Server) apiKBVersion(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	v, err := apiKBVersionID(r)
	if err != nil {
		return err
	}
	pg, _, err := s.KB.Page(r.Context(), p.UserID, r.PathValue("page"))
	if err != nil {
		return err
	}
	old, err := s.KB.Version(r.Context(), p.UserID, pg.ID, v)
	if err != nil {
		return err
	}
	out := struct {
		apiKBVersion
		Diff []apiKBDiffLine `json:"diff,omitempty"`
	}{apiKBVersion: apiKBVersion{ID: v, Title: old.Title, Body: old.Body, Editor: apiUser{old.EditorID, old.Editor}, EditedAt: old.Updated}}
	if r.URL.Query().Get("diff") == "1" {
		out.Diff = []apiKBDiffLine{}
		for _, d := range kb.Diff("# "+old.Title+"\n"+old.Body, "# "+pg.Title+"\n"+pg.Body) {
			out.Diff = append(out.Diff, apiKBDiffLine{d.Op, d.Text})
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiKBDiffLine struct {
	Op   string `json:"op"` // "=" — in both, "-" — only in the version, "+" — only in the current text
	Text string `json:"text"`
}

// apiKBRestoreVersion makes an earlier version the page's text again; the text it had goes to the history.
func (s *Server) apiKBRestoreVersion(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	v, err := apiKBVersionID(r)
	if err != nil {
		return err
	}
	if err := s.KB.RestoreVersion(r.Context(), p.UserID, r.PathValue("page"), v); err != nil {
		return err
	}
	return s.apiKBWritePage(w, r, p, r.PathValue("page"), http.StatusOK)
}

type apiKBHit struct {
	ID        string `json:"id"`
	Space     string `json:"space"`
	SpaceName string `json:"space_name"`
	Title     string `json:"title"`
	Snippet   string `json:"snippet"`
}

// apiKBSearch finds pages by words in the title or the text: ?q=…, ?space=ID — in that base only (the first 50).
func (s *Server) apiKBSearch(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q, space := r.URL.Query().Get("q"), r.URL.Query().Get("space")
	if space != "" {
		if _, err := s.KB.Space(r.Context(), p.UserID, space); err != nil {
			return err
		}
	}
	hits, err := s.KB.Search(r.Context(), p.UserID, q, space)
	if err != nil {
		return err
	}
	out := []apiKBHit{}
	for _, h := range hits {
		out = append(out, apiKBHit{h.ID, h.SpaceID, h.SpaceName, h.Title, h.Snippet})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

type apiKBTemplate struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Builtin     bool       `json:"builtin"` // one of go24's
	Pages       int        `json:"pages"`
	Author      string     `json:"author,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
}

// apiKBTemplates are the templates a new base can be made from: go24's and the company's.
func (s *Server) apiKBTemplates(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.KB.Templates(r.Context(), p.UserID, s.apiTr(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiKBTemplateList(list))
	return nil
}

func apiKBTemplateList(list []kb.Template) []apiKBTemplate {
	out := []apiKBTemplate{}
	for _, t := range list {
		x := apiKBTemplate{ID: t.ID, Name: t.Name, Description: t.Description, Builtin: t.Builtin, Pages: t.Pages, Author: t.Author}
		if !t.Builtin {
			x.CreatedAt = &t.Created
		}
		out = append(out, x)
	}
	return out
}

// apiKBPageTemplates are the templates a new page can start from ("template" of POST …/spaces/{space}/pages):
// go24's and the company's.
func (s *Server) apiKBPageTemplates(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.KB.PageTemplates(r.Context(), p.UserID, s.apiTr(r))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiKBTemplateList(list))
	return nil
}

// apiKBSavePageTemplate saves the page as a template of the company: {"name", "description"} (both may be left out)
// → {"id"}.
func (s *Server) apiKBSavePageTemplate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Name, Description string }
	if r.ContentLength != 0 {
		if err := readJSON(r, &in); err != nil {
			return err
		}
	}
	id, err := s.KB.SavePageTemplate(r.Context(), p.UserID, r.PathValue("page"), in.Name, in.Description, s.Storages.Copy)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
	return nil
}

type apiKBTrashed struct {
	Space     string     `json:"space"`
	SpaceName string     `json:"space_name"`
	Page      string     `json:"page,omitempty"` // "" — the whole base
	Title     string     `json:"title,omitempty"`
	Pages     int        `json:"pages"`
	DeletedBy string     `json:"deleted_by"`
	DeletedAt time.Time  `json:"deleted_at"`
	PurgeAt   *time.Time `json:"purge_at,omitempty"`
}

// apiKBTrash: the deleted bases and pages the key's owner may restore.
func (s *Server) apiKBTrash(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	days, err := s.Tasks.TrashDays(r.Context())
	if err != nil {
		return err
	}
	list, err := s.KB.Trash(r.Context(), p.UserID, days)
	if err != nil {
		return err
	}
	out := []apiKBTrashed{}
	for _, t := range list {
		out = append(out, apiKBTrashed{t.SpaceID, t.SpaceName, t.PageID, t.Title, t.Pages, t.DeletedBy, t.DeletedAt, t.PurgeAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiKBTrashRestore takes a base ({"space"}) or a page ({"page"}) out of the trash.
func (s *Server) apiKBTrashRestore(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Space, Page string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var err error
	switch {
	case in.Page != "":
		_, err = s.KB.RestorePage(r.Context(), p.UserID, in.Page)
	case in.Space != "":
		err = s.KB.RestoreSpace(r.Context(), p.UserID, in.Space)
	default:
		err = kb.ErrNotFound
	}
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBUpload stores a picture or a file for the base's pages (multipart "file") and answers with what to put into
// a page's text: {"id", "name", "url", "image", "md"}.
func (s *Server) apiKBUpload(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sp, err := s.KB.Space(r.Context(), p.UserID, r.PathValue("space"))
	if err == nil && !sp.CanEdit {
		err = kb.ErrForbidden
	}
	if err != nil {
		return err
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		return i18n.E("err.file_required")
	}
	defer f.Close()
	up, err := s.kbStoreFile(r.Context(), p.UserID, sp.ID, f, hdr)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, up)
	return nil
}

// apiKBDownload gives a picture or a file of a base the key's owner sees.
func (s *Server) apiKBDownload(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil || m.KBSpaceID == "" {
		return tasks.ErrNotFound
	}
	if _, err := s.KB.Space(r.Context(), p.UserID, m.KBSpaceID); err != nil {
		return err
	}
	return s.apiSendFile(w, r, id)
}

// apiKBExport gives the base as a ZIP of Markdown files, as «Экспорт».
func (s *Server) apiKBExport(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sp, err := s.KB.Space(r.Context(), p.UserID, r.PathValue("space"))
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(sp.Name+".zip"))
	if err := s.KB.Export(r.Context(), p.UserID, sp.ID, w, s.Storages.Open); err != nil {
		s.Log.Error("api kb export", "space", sp.ID, "err", err)
	}
	return nil
}

// apiKBImport makes pages of a ZIP of Markdown files or of separate .md files (multipart "file", one or several),
// as «Импорт»: into the base or, with "parent", under that page. It answers {"pages": N}.
func (s *Server) apiKBImport(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return i18n.E("err.file_required")
	}
	defer r.MultipartForm.RemoveAll()
	files, err := kbImportFiles(r.MultipartForm.File["file"])
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return i18n.E("err.file_required")
	}
	put := func(ctx context.Context, key string, rd io.Reader, size int64, ct string) error {
		return s.Storages.Put(ctx, p.UserID, false, "", key, rd, size, ct)
	}
	n, err := s.KB.Import(r.Context(), p.UserID, r.PathValue("space"), r.FormValue("parent"), files, put)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]int{"pages": n})
	return nil
}

type apiKBItem struct {
	Space     string    `json:"space"`
	SpaceName string    `json:"space_name"`
	Page      string    `json:"page,omitempty"` // "" — the base itself
	Title     string    `json:"title,omitempty"`
	At        time.Time `json:"at"` // when starred or last opened
}

func apiKBItems(list []kb.Item) []apiKBItem {
	out := []apiKBItem{}
	for _, it := range list {
		out = append(out, apiKBItem{it.SpaceID, it.SpaceName, it.PageID, it.Title, it.At})
	}
	return out
}

// apiKBFavorites: «Избранное» — the bases and pages the key's owner starred.
func (s *Server) apiKBFavorites(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.KB.Favorites(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiKBItems(list))
	return nil
}

// apiKBRecent: «Недавние» — the pages the key's owner opened lately (?limit=, 20 by default, up to 100).
func (s *Server) apiKBRecent(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 100 {
		limit = 20
	}
	list, err := s.KB.Recent(r.Context(), p.UserID, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, apiKBItems(list))
	return nil
}

// apiKBMarks: what of the base the key's owner starred and watches — {"favorite", "watching", "pages": {"favorite": [...],
// "watching": [...]}}.
func (s *Server) apiKBMarks(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sp, err := s.KB.Space(r.Context(), p.UserID, r.PathValue("space"))
	if err != nil {
		return err
	}
	fav, watch, err := s.KB.MarkedPages(r.Context(), p.UserID, sp.ID)
	if err != nil {
		return err
	}
	m, err := s.KB.MarksAt(r.Context(), p.UserID, sp.ID, "")
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"favorite": m.FavSpace, "watching": m.WatchSpace,
		"pages": map[string][]string{"favorite": fav, "watching": watch}})
	return nil
}

// apiKBMark stars (PUT …/marks/favorite) or watches (PUT …/marks/watch) a base or a page; DELETE takes it back.
// Reading the base is enough, as in go24.
func (s *Server) apiKBMark(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	kind := map[string]string{"favorite": kb.MarkFav, "watch": kb.MarkWatch}[r.PathValue("kind")]
	if kind == "" {
		return kb.ErrNotFound
	}
	if err := s.KB.SetMark(r.Context(), p.UserID, kind, r.PathValue("space"), r.PathValue("page"), r.Method == http.MethodPut); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBComments: the comments under a page, the top ones with their replies, oldest first.
func (s *Server) apiKBComments(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	pg, sp, err := s.KB.Page(r.Context(), p.UserID, r.PathValue("page"))
	if err != nil {
		return err
	}
	talk, err := s.KB.Discussion(r.Context(), p.UserID, pg, sp)
	if err != nil {
		return err
	}
	out := []*apiKBComment{}
	for _, c := range talk.Comments {
		out = append(out, apiKBCommentOf(c))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiKBAddComment writes a comment under a page: {"body" (Markdown, «@Имя» mentions), "parent": "ID" — a reply}.
// Who sees the base may comment, as in go24.
func (s *Server) apiKBAddComment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Body, Parent string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	c, err := s.KB.AddComment(r.Context(), p.UserID, r.PathValue("page"), in.Parent, in.Body)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, apiKBCommentOf(&c))
	return nil
}

// apiKBEditComment changes the key owner's comment: {"body"}.
func (s *Server) apiKBEditComment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Body string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.KB.EditComment(r.Context(), p.UserID, r.PathValue("id"), in.Body); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBDeleteComment deletes a comment with its replies: its author, or who manages the base.
func (s *Server) apiKBDeleteComment(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.KB.DeleteComment(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBLike: PUT — «Нравится» of the key's owner at a page, DELETE — takes it back.
func (s *Server) apiKBLike(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.KB.Like(r.Context(), p.UserID, r.PathValue("page"), r.Method == http.MethodPut); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiKBDraftOut is the key owner's unsaved text of a page.
type apiKBDraftOut struct {
	Title   string    `json:"title"`
	Body    string    `json:"body"`
	Base    time.Time `json:"base"` // the page's updated_at it is written over
	SavedAt time.Time `json:"saved_at"`
	Changed bool      `json:"changed"` // it differs from the page
	Stale   bool      `json:"stale"`   // the page was saved by someone since base
}

// apiKBDraft is the key owner's draft of the page; 404 — none.
func (s *Server) apiKBDraft(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, _, err := s.KB.Page(r.Context(), p.UserID, r.PathValue("page")); err != nil {
		return err
	}
	d, err := s.KB.DraftOf(r.Context(), p.UserID, r.PathValue("page"))
	if err != nil {
		return err
	}
	if d == nil {
		return kb.ErrNotFound
	}
	writeJSON(w, http.StatusOK, apiKBDraftOut{d.Title, d.Body, d.Base, d.Saved, d.Changed, d.Stale})
	return nil
}

// apiKBSaveDraft keeps the key owner's text of the page without saving it: {"title", "body", "base" (none — the
// page as it is now)}; as in the editor, it shows them «editing now» for a minute and a half.
func (s *Server) apiKBSaveDraft(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Title, Body string
		Base        time.Time
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.KB.SaveDraft(r.Context(), p.UserID, r.PathValue("page"), in.Title, in.Body, in.Base); err != nil {
		return err
	}
	return s.apiKBDraft(w, r, p)
}

// apiKBDropDraft throws the key owner's draft of the page away.
func (s *Server) apiKBDropDraft(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, _, err := s.KB.Page(r.Context(), p.UserID, r.PathValue("page")); err != nil {
		return err
	}
	if err := s.KB.DropDraft(r.Context(), p.UserID, r.PathValue("page")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
