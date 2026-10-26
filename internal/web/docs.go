package web

import (
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/drive"
)

// ---------- «Документы»: the office documents of every drive the user works with, as the menu item ----------

// docKinds are the kinds of «Документы» with the extensions the editor opens (office.Kind).
var docKinds = []struct {
	Kind string
	Exts []string
}{
	{"doc", []string{"docx", "doc", "docm", "dotx", "odt", "ott", "rtf", "fodt"}},
	{"sheet", []string{"xlsx", "xls", "xlsm", "xltx", "ods", "ots", "csv", "fods"}},
	{"slides", []string{"pptx", "ppt", "pptm", "potx", "odp", "otp", "fodp"}},
}

// docsMax is how many documents «Документы» shows.
const docsMax = 500

// docScopes are the tabs of «Документы»: all, «Недавние» (opened by the user), «Мои», «Общие».
var docScopes = []string{"", "recent", "mine", "shared"}

// docFilter is the filter of «Документы» from ?scope=, ?type=, ?q=, and the extensions of all its kinds.
func docFilter(r *http.Request) (f drive.DocFilter, all []string, ftype string) {
	qs := r.URL.Query()
	f = drive.DocFilter{Scope: qs.Get("scope"), Q: strings.TrimSpace(qs.Get("q"))}
	if !slices.Contains(docScopes, f.Scope) {
		f.Scope = ""
	}
	for _, k := range docKinds {
		all = append(all, k.Exts...)
		if k.Kind == qs.Get("type") {
			f.Exts, ftype = k.Exts, k.Kind
		}
	}
	return f, all, ftype
}

// docsPage is «Документы»: ?scope= (recent | mine | shared), ?type= (doc | sheet | slides), ?q= — a part of the name.
func (s *Server) docsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	f, all, ftype := docFilter(r)
	var kinds []string
	for _, k := range docKinds {
		kinds = append(kinds, k.Kind)
	}
	list, err := s.Drive.Documents(r.Context(), sess.UserID, all, f, docsMax)
	var snippets map[string]template.HTML
	if err == nil {
		files := make([]drive.File, len(list))
		for i, d := range list {
			files[i] = d.File
		}
		snippets, err = s.snippets(r.Context(), files, f.Q)
	}
	if err != nil {
		s.taskErr(w, r, err, "/disk/my")
		return
	}
	s.render(w, r, "docs", page{TitleKey: "docs.title", Nav: "docs", Data: map[string]any{
		"Docs": list, "Snippets": snippets, "Scope": f.Scope, "Q": f.Q, "Type": ftype, "Types": kinds, "Office": s.Office != nil,
		"Scopes": docScopes, "Me": sess.UserID, "Accept": "." + strings.Join(all, ",."), "Base": s.BaseURL}})
}

// docsBack: «Документы» is where the editor, «Создать», «Загрузить» and the actions of a document go back to, when
// they were opened from it (?from=docs), with its tab (?scope=).
func docsBack(r *http.Request, back string) string {
	if r.FormValue("from") != "docs" {
		return back
	}
	if sc := r.FormValue("scope"); sc != "" && slices.Contains(docScopes, sc) {
		return "/docs?scope=" + sc
	}
	return "/docs"
}

// docKind is the kind of «Документы» of a file ("" — not a document).
func docKind(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 {
		return ""
	}
	ext := strings.ToLower(name[i+1:])
	for _, k := range docKinds {
		if slices.Contains(k.Exts, ext) {
			return k.Kind
		}
	}
	return ""
}

type apiDoc struct {
	apiDiskFile
	Drive    string     `json:"drive"`               // the drive to work with it by /api/v1/disk/drives/{drive}
	FolderID string     `json:"folder_id"`           // its folder on that drive ("" — the top)
	Kind     string     `json:"kind"`                // doc | sheet | slides
	Opened   *time.Time `json:"opened_at,omitempty"` // when the key's owner last opened it in the editor
}

// apiDiskDocs is «Документы», as the page: ?scope= (recent | mine | shared), ?type= (doc | sheet | slides), ?q=.
func (s *Server) apiDiskDocs(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	f, all, _ := docFilter(r)
	list, err := s.Drive.Documents(r.Context(), p.UserID, all, f, docsMax)
	if err != nil {
		return err
	}
	out := make([]apiDoc, len(list))
	for i, d := range list {
		out[i] = apiDoc{apiDiskFile: apiDiskFileOf(d.File), Drive: docDrive(d.Base), FolderID: d.FolderID, Kind: docKind(d.Name), Opened: d.Opened}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// docDrive is the drive of the API (see apiDiskRoutes) from the address of a drive of the pages.
func docDrive(base string) string {
	for _, p := range []struct{ prefix, which string }{{"/disk/sh/", "sh:"}, {"/disk/sf/", "sf:"}, {"/disk/p/", ""}, {"/disk/", ""}} {
		if rest, ok := strings.CutPrefix(base, p.prefix); ok {
			return p.which + rest
		}
	}
	return base
}
