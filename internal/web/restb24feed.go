package web

import (
	"bytes"
	"encoding/base64"
	"maps"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/storages"
)

// REST compatible with the portal, the feed (GO24DEV-948): log.blogpost.get, add, update, delete, share,
// getusers.important; log.blogcomment.add, delete. Posts and comments go by their numbers (migration 892), people by
// theirs. The addressees (DEST) are those: UA — all employees, U<n> — a person, SG<n> — a group (one; the
// people and departments given with it are shared the post), D<n> and DR<n> — a department with its sub-departments.
// FILES are [name, base64] pairs, UF_BLOG_POST_FILE — files of the drives («n<number>»). The rights are those of the
// feed's page.

func init() {
	for name, m := range map[string]portalMethod{
		"log.blogpost.get":                {Scope: "feed:read", Run: (*Server).portalPostGet},
		"log.blogpost.add":                {Scope: "feed:write", Run: (*Server).portalPostAdd},
		"log.blogpost.update":             {Scope: "feed:write", Run: (*Server).portalPostUpdate},
		"log.blogpost.delete":             {Scope: "feed:write", Run: (*Server).portalPostDelete},
		"log.blogpost.share":              {Scope: "feed:write", Run: (*Server).portalPostShare},
		"log.blogpost.getusers.important": {Scope: "feed:read", Run: (*Server).portalPostReaders},
		"log.blogcomment.add":             {Scope: "feed:write", Run: (*Server).portalFeedCommentAdd},
		"log.blogcomment.delete":          {Scope: "feed:write", Run: (*Server).portalFeedCommentDelete},
	} {
		portalMethods[name] = m
	}
}

// portalDest are the addressees of a post: a project (its id), people and departments; all — UA (or nothing given).
type portalDest struct {
	Project string
	To      feed.Audience
	All     bool
}

// portalDestOf reads DEST (or SPERM of the older integrations: {"U": ["U1"], "SG": ["SG2"]}).
func (s *Server) portalDestOf(c portalCall, v any) (portalDest, error) {
	var codes []string
	if m := portalMap(v); m != nil {
		for _, x := range m {
			codes = append(codes, portalList(x)...)
		}
	} else {
		codes = portalList(v)
	}
	var d portalDest
	bad := s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "DEST")
	for _, code := range codes {
		code = strings.ToUpper(strings.TrimSpace(code))
		var kind, num string
		for _, k := range []string{"SG", "DR", "UA", "U", "D", "G"} {
			if strings.HasPrefix(code, k) {
				kind, num = k, code[len(k):]
				break
			}
		}
		switch {
		case code == "":
		case kind == "UA", code == "G2": // G2 — «all users» of the older integrations
			d.All = true
		case kind == "U":
			id, err := s.portalNum(c.r.Context(), "users", num)
			if err != nil || id == "" || strings.Trim(num, "0123456789") != "" {
				return d, bad
			}
			d.To.Users = append(d.To.Users, id)
		case kind == "D" || kind == "DR":
			id, err := s.portalNum(c.r.Context(), "departments", num)
			if err != nil || id == "" || strings.Trim(num, "0123456789") != "" {
				return d, bad
			}
			d.To.Depts = append(d.To.Depts, id)
		case kind == "SG":
			p, err := s.portalGroupNum(c, num, false)
			if err != nil {
				return d, err
			}
			if d.Project != "" && d.Project != p.ID {
				return d, bad // a post of go24 is in one group
			}
			d.Project = p.ID
		default:
			return d, bad
		}
	}
	if len(codes) == 0 {
		d.All = true
	}
	return d, nil
}

// portalDestParam is DEST, or SPERM; given — whether either was.
func (s *Server) portalDestParam(c portalCall) (portalDest, bool, error) {
	v, ok := portalParamOK(c.in, "DEST")
	if !ok {
		v, ok = portalParamOK(c.in, "SPERM")
	}
	if !ok {
		return portalDest{All: true}, false, nil
	}
	d, err := s.portalDestOf(c, v)
	return d, true, err
}

// portalFilePairs are the [name, base64] pairs of FILES (one pair alone, or a list of them).
func portalFilePairs(v any) ([][2]string, bool) {
	scalar := func(x any) bool {
		switch x.(type) {
		case []any, map[string]any, nil:
			return false
		}
		return true
	}
	pair := func(x any) ([2]string, bool) {
		var n, b any
		switch y := x.(type) {
		case []any:
			if len(y) != 2 {
				return [2]string{}, false
			}
			n, b = y[0], y[1]
		case map[string]any:
			if len(y) != 2 {
				return [2]string{}, false
			}
			n, b = portalParam(y, "0"), portalParam(y, "1")
		}
		if !scalar(n) || !scalar(b) {
			return [2]string{}, false
		}
		return [2]string{portalStr(n), portalStr(b)}, true
	}
	if v == nil {
		return nil, true
	}
	if p, ok := pair(v); ok {
		return [][2]string{p}, true
	}
	var items []any
	switch y := v.(type) {
	case []any:
		items = y
	case map[string]any:
		keys := slices.Collect(maps.Keys(y))
		slices.SortFunc(keys, func(a, b string) int { x, _ := strconv.Atoi(a); z, _ := strconv.Atoi(b); return x - z })
		for _, k := range keys {
			items = append(items, y[k])
		}
	default:
		return nil, false
	}
	var out [][2]string
	for _, it := range items {
		p, ok := pair(it)
		if !ok {
			return nil, false
		}
		out = append(out, p)
	}
	return out, true
}

// portalUploads are the files of FILES as an uploaded form gives them.
func (s *Server) portalUploads(c portalCall) ([]*multipart.FileHeader, error) {
	v, ok := portalParamOK(c.in, "FILES")
	if !ok {
		return nil, nil
	}
	bad := s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "FILES")
	pairs, ok := portalFilePairs(v)
	if !ok {
		return nil, bad
	}
	if len(pairs) == 0 {
		return nil, nil
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range pairs {
		data := p[1]
		if _, b64, ok := strings.Cut(data, ";base64,"); ok {
			data = b64
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
		if err != nil || strings.TrimSpace(p[0]) == "" {
			return nil, bad
		}
		part, err := mw.CreateFormFile("file", strings.TrimSpace(p[0]))
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(raw); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	form, err := multipart.NewReader(&buf, mw.Boundary()).ReadForm(32 << 20)
	if err != nil {
		return nil, err
	}
	return form.File["file"], nil
}

// portalDriveFiles are the files of the drives of UF_BLOG_POST_FILE: «n<number>» (or the number alone).
func (s *Server) portalDriveFiles(c portalCall, name string) ([]string, error) {
	var out []string
	for _, x := range portalList(portalParam(c.in, name)) {
		x = strings.TrimPrefix(strings.TrimSpace(x), "n")
		if x == "" {
			continue
		}
		id, err := s.portalNum(c.r.Context(), "files", x)
		if err != nil || id == "" {
			return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", name)
		}
		out = append(out, id)
	}
	return out, nil
}

// portalPostID is the go24 id of the post of POST_ID the caller sees.
func (s *Server) portalPostID(c portalCall) (string, error) {
	id, err := s.portalNum(c.r.Context(), "feed_posts", portalParam(c.in, "POST_ID"))
	if err == nil && id == "" {
		err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "POST_ID")
	}
	if err != nil {
		return "", err
	}
	return id, s.Feed.CanSee(c.r.Context(), c.p.UserID, id)
}

// portalImportantEnd is IMPORTANT_DATE_END: important to the end of that day (zero — always).
func (s *Server) portalImportantEnd(c portalCall) (time.Time, error) {
	v := strings.TrimSpace(portalStr(portalParam(c.in, "IMPORTANT_DATE_END")))
	if v == "" {
		return time.Time{}, nil
	}
	d, err := portalDate(v)
	if err != nil || d == nil {
		return time.Time{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "IMPORTANT_DATE_END")
	}
	end := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	if !end.After(time.Now()) {
		return time.Time{}, i18n.E("err.until_past")
	}
	return end, nil
}

// portalPostAdd: log.blogpost.add — POST_TITLE, POST_MESSAGE, DEST (all employees when none), IMPORTANT=Y with
// IMPORTANT_DATE_END, FILES, UF_BLOG_POST_FILE; the answer is the post's number.
func (s *Server) portalPostAdd(c portalCall) (portalOut, error) {
	dest, _, err := s.portalDestParam(c)
	if err != nil {
		return portalOut{}, err
	}
	np := newPost{Title: portalStr(portalParam(c.in, "POST_TITLE")), Body: portalStr(portalParam(c.in, "POST_MESSAGE")),
		Important: formTrue(portalStr(portalParam(c.in, "IMPORTANT")))}
	if np.Important {
		if np.Until, err = s.portalImportantEnd(c); err != nil {
			return portalOut{}, err
		}
	}
	if np.Files, err = s.portalUploads(c); err != nil {
		return portalOut{}, err
	}
	if np.Drive, err = s.portalDriveFiles(c, "UF_BLOG_POST_FILE"); err != nil {
		return portalOut{}, err
	}
	share := feed.Audience{}
	switch {
	case dest.All:
	case dest.Project != "":
		np.Project, share = dest.Project, dest.To
	default:
		np.To = dest.To
	}
	sess := auth.Session{UserID: c.p.UserID}
	id, err := s.publishPost(c.r, sess, np)
	if err != nil {
		return portalOut{}, err
	}
	if len(share.Users)+len(share.Depts) > 0 {
		if _, err := s.Feed.Share(c.r.Context(), c.p.UserID, id, share); err != nil {
			return portalOut{}, err
		}
	}
	return s.portalPostNum(c, id)
}

func (s *Server) portalPostNum(c portalCall, id string) (portalOut, error) {
	var n int64
	err := s.DB.QueryRow(c.r.Context(), `SELECT num FROM feed_posts WHERE id = $1`, id).Scan(&n)
	return portalOut{Result: n}, err
}

// portalPostUpdate: log.blogpost.update — the title and text (the author), DEST readdresses the post, FILES and
// UF_BLOG_POST_FILE add files; the answer is the post's number.
func (s *Server) portalPostUpdate(c portalCall) (portalOut, error) {
	id, err := s.portalPostID(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx, sess := c.r.Context(), auth.Session{UserID: c.p.UserID}
	if err := s.Feed.CanChange(ctx, c.p.UserID, id); err != nil {
		return portalOut{}, err
	}
	dest, readdress, err := s.portalDestParam(c)
	if err != nil {
		return portalOut{}, err
	}
	files, err := s.portalUploads(c)
	if err != nil {
		return portalOut{}, err
	}
	drives, err := s.portalDriveFiles(c, "UF_BLOG_POST_FILE")
	if err != nil {
		return portalOut{}, err
	}
	if n, err := s.Feed.FileCount(ctx, id); err != nil {
		return portalOut{}, err
	} else if n+len(files)+len(drives) > maxPostFiles {
		return portalOut{}, i18n.E("err.too_many_files", maxPostFiles)
	}
	title, tok := portalParamOK(c.in, "POST_TITLE")
	body, bok := portalParamOK(c.in, "POST_MESSAGE")
	if tok || bok {
		it, err := s.Feed.Item(ctx, c.p.UserID, id)
		if err != nil {
			return portalOut{}, err
		}
		if it.Post == nil {
			return portalOut{}, feed.ErrNotFound
		}
		t, b := it.Post.Title, it.Post.Body
		if tok {
			t = portalStr(title)
		}
		if bok {
			b = portalStr(body)
		}
		if err := s.changePost(ctx, sess, id, t, b, nil); err != nil {
			return portalOut{}, err
		}
	}
	if readdress {
		to := dest.To
		if dest.All || dest.Project != "" {
			to = feed.Audience{}
		}
		moved, _, err := s.Feed.SetAudience(ctx, c.p.UserID, id, dest.Project, to)
		if err != nil {
			return portalOut{}, err
		}
		if moved && s.Matrix != nil {
			if err := s.Matrix.MovePost(ctx, id); err != nil {
				s.Log.Error("feed move in matrix", "err", err)
				return portalOut{}, i18n.E("err.messenger")
			}
		}
		if share := dest.To; dest.Project != "" && len(share.Users)+len(share.Depts) > 0 {
			if _, err := s.Feed.Share(ctx, c.p.UserID, id, share); err != nil {
				return portalOut{}, err
			}
		}
	}
	if err := s.attachPostFiles(c.r, sess, storages.NewFile{PostID: id}, files); err != nil {
		return portalOut{}, err
	}
	for _, f := range drives {
		if _, err := s.attachDriveFile(c.r, sess, f, storages.NewFile{PostID: id}); err != nil {
			return portalOut{}, err
		}
	}
	return s.portalPostNum(c, id)
}

func (s *Server) portalPostDelete(c portalCall) (portalOut, error) {
	id, err := s.portalPostID(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.removePost(c.r.Context(), auth.Session{UserID: c.p.UserID}, id); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// portalPostShare: log.blogpost.share — DEST are added to the post's addressees; a post to all employees has them
// already.
func (s *Server) portalPostShare(c portalCall) (portalOut, error) {
	id, err := s.portalPostID(c)
	if err != nil {
		return portalOut{}, err
	}
	v, ok := portalParamOK(c.in, "DEST")
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "DEST")
	}
	dest, err := s.portalDestOf(c, v)
	if err != nil {
		return portalOut{}, err
	}
	if dest.Project != "" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "DEST")
	}
	var open bool
	if err := s.DB.QueryRow(c.r.Context(), `SELECT project_id IS NULL AND NOT limited FROM feed_posts WHERE id = $1`, id).Scan(&open); err != nil {
		return portalOut{}, err
	}
	if open || dest.All && len(dest.To.Users)+len(dest.To.Depts) == 0 {
		return portalOut{Result: open}, nil
	}
	if _, err := s.Feed.Share(c.r.Context(), c.p.UserID, id, dest.To); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// portalPostReaders: log.blogpost.getusers.important — the numbers of those who confirmed the important post as read.
func (s *Server) portalPostReaders(c portalCall) (portalOut, error) {
	id, err := s.portalPostID(c)
	if err != nil {
		return portalOut{}, err
	}
	rows, err := s.DB.Query(c.r.Context(), `SELECT u.num FROM feed_reads r JOIN users u ON u.id = r.user_id
		WHERE r.post_id = $1 ORDER BY r.read_at, u.num`, id)
	if err != nil {
		return portalOut{}, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return portalOut{}, err
		}
		out = append(out, n)
	}
	return portalOut{Result: out}, rows.Err()
}

// portalPostGet: log.blogpost.get — the posts the caller sees, the newest first, 50 a page from «start»; POST_ID — one
// post; LOG_RIGHTS — only those to these addressees (codes as DEST).
func (s *Server) portalPostGet(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	if v := strings.TrimSpace(portalStr(portalParam(c.in, "POST_ID"))); v != "" && v != "0" {
		id, err := s.portalPostID(c)
		if err != nil {
			return portalOut{}, err
		}
		it, err := s.Feed.Item(ctx, c.p.UserID, id)
		if err != nil {
			return portalOut{}, err
		}
		if it.Post == nil {
			return portalOut{}, feed.ErrNotFound
		}
		rows, err := s.portalPostRows(c, []*feed.Post{it.Post})
		return portalPaged(rows, 0, len(rows), len(rows)), err
	}
	f := feed.Filter{Posts: true}
	if v, ok := portalParamOK(c.in, "LOG_RIGHTS"); ok {
		dest, err := s.portalDestOf(c, v)
		if err != nil {
			return portalOut{}, err
		}
		f.Project, f.To = dest.Project, dest.To.Users
		for _, d := range dest.To.Depts {
			f.To = append(f.To, "dept:"+d)
		}
	}
	start := portalStart(c.in)
	var posts []*feed.Post
	more := false
	for len(posts) <= start+portalPage { // one more than the page: whether another follows
		items, err := s.Feed.List(ctx, c.p.UserID, f)
		if err != nil {
			return portalOut{}, err
		}
		for _, it := range items {
			if it.Post != nil {
				posts = append(posts, it.Post)
			}
		}
		if len(items) < feed.PageSize {
			break
		}
		f.Before = items[len(items)-1].At
		more = true
	}
	total := len(posts)
	page := posts[min(start, total):min(start+portalPage, total)]
	if more && total > start+portalPage {
		total = start + portalPage + 1 // at least one more page
	}
	rows, err := s.portalPostRows(c, page)
	return portalPaged(rows, start, len(rows), total), err
}

// portalPostRows are the posts in the fields.
func (s *Server) portalPostRows(c portalCall, posts []*feed.Post) ([]map[string]any, error) {
	ctx := c.r.Context()
	ids, people := []string{}, []string{}
	for _, p := range posts {
		ids, people = append(ids, p.ID), append(people, p.AuthorID)
	}
	nums, err := s.portalNums(ctx, "feed_posts", ids)
	if err != nil {
		return nil, err
	}
	users, err := s.portalNums(ctx, "users", people)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	rows, err := s.DB.Query(ctx, `SELECT post_id::text, count(*) FROM feed_comments WHERE post_id = ANY($1::uuid[]) GROUP BY post_id`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		counts[id] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range posts {
		n := strconv.FormatInt(nums[p.ID], 10)
		files, images := []string{}, false
		for _, f := range p.Files {
			files = append(files, f.ID)
			images = images || strings.HasPrefix(f.ContentType, "image/")
		}
		until := any(nil)
		if p.Important && !p.Until.IsZero() {
			until = p.Until.Add(-time.Second).In(time.Local).Format(time.RFC3339)
		}
		out = append(out, map[string]any{"ID": n, "BLOG_ID": "1", "PUBLISH_STATUS": "P", "TITLE": p.Title,
			"AUTHOR_ID": strconv.FormatInt(users[p.AuthorID], 10), "ENABLE_COMMENTS": "Y", "NUM_COMMENTS": strconv.Itoa(counts[p.ID]),
			"VIEWS": strconv.Itoa(p.Views), "CODE": nil, "MICRO": portalYN(p.Title == ""), "DETAIL_TEXT": p.Body,
			"DATE_PUBLISH": p.CreatedAt.In(time.Local).Format(time.RFC3339), "CATEGORY_ID": strings.Join(p.Tags, ","),
			"HAS_SOCNET_ALL": portalYN(p.ProjectKey == "" && len(p.To) == 0), "HAS_TAGS": portalYN(len(p.Tags) > 0),
			"HAS_IMAGES": portalYN(images), "HAS_PROPS": "Y", "HAS_COMMENT_IMAGES": "N", "FILES": files,
			"UF_BLOG_POST_IMPRTNT": map[bool]string{true: "1", false: "0"}[p.Important], "UF_IMPRTANT_DATE_END": until,
			"URL": strings.TrimRight(s.BaseURL, "/") + "/feed?post=" + p.ID})
	}
	return out, nil
}

// portalFeedCommentAdd: log.blogcomment.add — POST_ID, TEXT, FILES, UF_BLOG_COMMENT_FILE; the answer is the comment's
// number.
func (s *Server) portalFeedCommentAdd(c portalCall) (portalOut, error) {
	id, err := s.portalPostID(c)
	if err != nil {
		return portalOut{}, err
	}
	files, err := s.portalUploads(c)
	if err != nil {
		return portalOut{}, err
	}
	drives, err := s.portalDriveFiles(c, "UF_BLOG_COMMENT_FILE")
	if err != nil {
		return portalOut{}, err
	}
	cm, err := s.publishComment(c.r, auth.Session{UserID: c.p.UserID}, id, portalStr(portalParam(c.in, "TEXT")), files, drives)
	if err != nil {
		return portalOut{}, err
	}
	var n int64
	err = s.DB.QueryRow(c.r.Context(), `SELECT num FROM feed_comments WHERE id = $1`, cm.ID).Scan(&n)
	return portalOut{Result: n}, err
}

// portalFeedCommentDelete: log.blogcomment.delete — COMMENT_ID (its author or an administrator).
func (s *Server) portalFeedCommentDelete(c portalCall) (portalOut, error) {
	id, err := s.portalNum(c.r.Context(), "feed_comments", portalParam(c.in, "COMMENT_ID"))
	if err == nil && id == "" {
		err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "COMMENT_ID")
	}
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.Feed.Comment(c.r.Context(), c.p.UserID, id); err != nil {
		return portalOut{}, err
	}
	if err := s.removeComment(c.r.Context(), auth.Session{UserID: c.p.UserID}, id); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}
