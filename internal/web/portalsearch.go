package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go24/internal/auth"
	"go24/internal/drive"
	"go24/internal/msgfmt"
	"go24/internal/tasks"
)

// The portal's search, as «Поиск» in the header: one query finds people, tasks, projects and groups, CRM
// cards, files, pages of the knowledge base, meetings and messages — what the person may see — section by section.

// searchSections: the sections of the search in the order of the results.
var searchSections = []string{"people", "tasks", "projects", "crm", "disk", "kb", "calendar", "im"}

var searchIcons = map[string]string{"people": "user", "tasks": "checkbox", "projects": "users-group", "crm": "address-book",
	"disk": "files", "kb": "notebook", "calendar": "calendar-event", "im": "messages"}

const (
	searchFirst = 5  // a section's results on the page of all of them; «Показать все» opens the rest
	searchSectionMax   = 50 // the results of one section
)

// searchHit is one result: where it leads, its title, what it is (Sub), a found piece of its text, and its time.
type searchHit struct {
	URL, Icon, Title, Sub, Text string
	At                          *time.Time
}

// searchGroup is a section's results; More — it has more than the page shows.
type searchGroup struct {
	Key  string
	Hits []searchHit
	More bool
}

// searchResult is what the search found: the groups with results, the tasks themselves (for the task list of the
// section «Задачи»), and Switched — the query retyped in the other keyboard layout when only that found something.
type searchResult struct {
	Groups   []searchGroup
	Tasks    []tasks.Task
	Switched string
}

// Total is how many results the groups show.
func (r searchResult) Total() int {
	n := 0
	for _, g := range r.Groups {
		n += len(g.Hits)
	}
	return n
}

// portalSearch searches the sections (only — just that one, "" — all), each up to searchSectionMax results, in parallel; a
// section that fails is left out (and logged). Nothing found but people (who are found in either layout anyway) and
// the query may be typed in the wrong layout («ghbdtn») — the search is repeated with the retyped query, which wins
// when it finds more.
func (s *Server) portalSearch(ctx context.Context, sess auth.Session, q, only string) searchResult {
	res := s.searchOnce(ctx, sess, q, only)
	if len(res.Groups) == 0 || (len(res.Groups) == 1 && res.Groups[0].Key == "people") {
		if alt := tasks.SwitchLayout(q); alt != strings.ToLower(q) {
			if again := s.searchOnce(ctx, sess, alt, only); again.Total() > res.Total() {
				again.Switched = alt
				return again
			}
		}
	}
	return res
}

func (s *Server) searchOnce(ctx context.Context, sess auth.Session, q, only string) searchResult {
	var res searchResult
	q = strings.TrimSpace(q)
	if utf8.RuneCountInString(q) < 2 {
		return res
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	guest := tasks.IsGuestCtx(ctx)
	found := make([][]searchHit, len(searchSections))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, sec := range searchSections {
		if only != "" && only != sec {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			hits, list, err := s.searchSection(ctx, sess.UserID, sec, q, guest)
			if err != nil {
				s.Log.Warn("search", "section", sec, "err", err)
				return
			}
			found[i] = hits
			if list != nil {
				mu.Lock()
				res.Tasks = list
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	for i, hits := range found {
		if len(hits) == 0 {
			continue
		}
		hits = hits[:min(len(hits), searchSectionMax)]
		g := searchGroup{Key: searchSections[i], Hits: hits}
		if only == "" && len(hits) > searchFirst {
			g.Hits, g.More = hits[:searchFirst], true
		}
		res.Groups = append(res.Groups, g)
	}
	return res
}

// searchSection finds q in one section; the section «Задачи» also returns the tasks themselves.
func (s *Server) searchSection(ctx context.Context, userID, sec, q string, guest bool) ([]searchHit, []tasks.Task, error) {
	var out []searchHit
	add := func(url, title, sub, text string, at *time.Time) {
		out = append(out, searchHit{URL: url, Icon: searchIcons[sec], Title: title, Sub: sub, Text: text, At: at})
	}
	switch sec {
	case "people":
		list, err := s.Tasks.People(ctx, userID, tasks.PeopleQuery{Q: q, Self: true, Limit: searchSectionMax})
		if err != nil {
			return nil, nil, err
		}
		for _, p := range list {
			add("/u/"+p.ID, p.Name, strings.Join(slices.DeleteFunc([]string{p.Position, p.Department}, func(x string) bool { return x == "" }), " · "), "", nil)
		}
	case "tasks":
		list, err := s.Tasks.Search(ctx, userID, q)
		if err != nil {
			return nil, nil, err
		}
		for _, t := range list {
			add("/t/"+t.Key(), t.Title, t.Key()+" · "+t.ResponsibleName, "", t.Deadline)
		}
		if list == nil {
			list = []tasks.Task{}
		}
		return out, list, nil
	case "projects":
		list, err := s.Tasks.ListProjects(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		for _, p := range list {
			if searchHas(p.Name, q) || searchHas(p.Key, q) || searchHas(p.Description, q) {
				add("/p/"+p.Key, p.Name, p.Key, searchSnippet(p.Description, q), nil)
			}
		}
	case "crm":
		if s.CRM == nil || guest {
			return nil, nil, nil
		}
		deals, err := s.CRM.Deals(ctx, userID, "", q, "", "")
		if err != nil {
			return nil, nil, err
		}
		for _, d := range deals {
			if d.TypeID == "" {
				add("/crm/deals/"+d.ID, d.Title, strings.Trim(d.Stage.Name+" · "+d.Company.Name, " ·"), "", nil)
			}
		}
		leads, err := s.CRM.Leads(ctx, userID, q)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range leads {
			add("/crm/leads/"+l.ID, l.Title, strings.Trim(l.Stage.Name+" · "+l.Name, " ·"), "", nil)
		}
		contacts, err := s.CRM.Contacts(ctx, userID, q, "")
		if err != nil {
			return nil, nil, err
		}
		for _, c := range contacts {
			add("/crm/contacts/"+c.ID, c.Name, strings.Trim(c.Company.Name+" · "+c.Email, " ·"), "", nil)
		}
		companies, err := s.CRM.Companies(ctx, userID, q)
		if err != nil {
			return nil, nil, err
		}
		for _, c := range companies {
			add("/crm/companies/"+c.ID, c.Title, c.Email, "", nil)
		}
	case "disk":
		if s.Drive == nil {
			return nil, nil, nil
		}
		list, err := s.Drive.Documents(ctx, userID, nil, drive.DocFilter{Q: q}, searchSectionMax)
		if err != nil {
			return nil, nil, err
		}
		var ids []string
		for _, d := range list {
			ids = append(ids, d.ID)
		}
		snippets, _ := s.Drive.Snippets(ctx, ids, q)
		for _, d := range list {
			if guest && d.Base == "/disk/my" { // a guest has no «Мой диск»
				continue
			}
			url := d.Base
			if d.FolderID != "" {
				url += "?folder=" + d.FolderID
			}
			where := d.ProjectName
			if d.Where != "" {
				where = strings.Trim(where+" › "+d.Where, " ›")
			}
			at := d.CreatedAt
			add(url, d.Name, where, snippets[d.ID], &at)
		}
	case "kb":
		if s.KB == nil || guest {
			return nil, nil, nil
		}
		list, err := s.KB.Search(ctx, userID, q, "")
		if err != nil {
			return nil, nil, err
		}
		for _, h := range list {
			add("/kb/"+h.SpaceID+"/"+h.ID, h.Title, h.SpaceName, h.Snippet, nil)
		}
	case "calendar":
		if s.Calendar == nil {
			return nil, nil, nil
		}
		list, err := s.Calendar.Search(ctx, userID, q, "")
		if err != nil {
			return nil, nil, err
		}
		for _, e := range list {
			start := e.Start
			add("/calendar/"+e.ID, e.Title, e.Where, searchSnippet(e.Description, q), &start)
		}
	case "im":
		if s.Matrix == nil {
			return nil, nil, nil
		}
		rooms, err := s.Matrix.Rooms(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		names := map[string]string{}
		for _, rm := range rooms {
			names[rm.ID] = rm.Name
			if rm.Kind != "dm" && rm.Kind != "notes" && searchHas(rm.Name, q) { // a chat by its name
				add("/im?room="+rm.ID, rm.Name, "", "", nil)
			}
		}
		msgs, err := s.Matrix.Search(ctx, userID, q, "")
		if err != nil {
			return nil, nil, err
		}
		for _, m := range msgs {
			url := "/im?room=" + m.RoomID + "&at=" + m.ID
			if m.ThreadRoot != "" {
				url = "/im?room=" + m.RoomID + "&thread=" + m.ThreadRoot
			}
			text := msgfmt.Plain(m.Body)
			if m.File != nil {
				text = m.File.Name
			}
			at := m.At
			add(url, strings.Trim(names[m.RoomID]+" · "+m.SenderName, " ·"), "", searchSnippet(text, q), &at)
		}
	}
	return out, nil, nil
}

// searchFold lowers the text and reads ё as е, as the search compares.
func searchFold(s string) string { return strings.ReplaceAll(strings.ToLower(s), "ё", "е") }

// searchHas: the text has q (case and ё/е aside).
func searchHas(text, q string) bool { return text != "" && strings.Contains(searchFold(text), searchFold(q)) }

// searchSnippet is the piece of the text around q (up to some 160 letters), "" when the text has no q.
func searchSnippet(text, q string) string {
	text = strings.Join(strings.Fields(text), " ")
	at := strings.Index(searchFold(text), searchFold(q))
	if at < 0 {
		return ""
	}
	r := []rune(text)
	pos := utf8.RuneCountInString(searchFold(text)[:at])
	from, to := max(pos-60, 0), min(pos+100, len(r))
	out := string(r[from:to])
	if from > 0 {
		out = "…" + out
	}
	if to < len(r) {
		out += "…"
	}
	return out
}

// search is the page of the portal's search (?q=, ?in=section): a task's key (GO-12) leads straight to the task.
func (s *Server) search(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if _, _, err := tasks.ParseKey(q); err == nil {
		// only a task one may see: a key of someone else's collab or project does not even say it exists
		if id, err := s.Tasks.TaskIDByKey(r.Context(), q); err == nil && s.Tasks.CheckVisible(r.Context(), sess.UserID, id) == nil {
			http.Redirect(w, r, "/t/"+strings.ToUpper(q), http.StatusFound)
			return
		}
	}
	in := r.URL.Query().Get("in")
	if !slices.Contains(searchSections, in) {
		in = ""
	}
	res := s.portalSearch(r.Context(), sess, q, in)
	s.render(w, r, "search", page{TitleKey: "title.search", Query: q, Data: map[string]any{
		"Q": q, "In": in, "Sections": searchSections, "Icons": searchIcons, "Result": res}})
}
