package web

import (
	"html/template"
	"net/http"
	"slices"
	"strings"

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

// docsPage is «Документы»: ?scope= (mine | shared), ?type= (doc | sheet | slides), ?q= — a part of the name.
func (s *Server) docsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	qs := r.URL.Query()
	f := drive.DocFilter{Scope: qs.Get("scope"), Q: strings.TrimSpace(qs.Get("q"))}
	if f.Scope != "mine" && f.Scope != "shared" {
		f.Scope = ""
	}
	var all, kinds []string
	ftype := ""
	for _, k := range docKinds {
		all = append(all, k.Exts...)
		kinds = append(kinds, k.Kind)
		if k.Kind == qs.Get("type") {
			f.Exts, ftype = k.Exts, k.Kind
		}
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
		"Scopes": []string{"", "mine", "shared"}, "Me": sess.UserID}})
}

// docsBack: «Документы» is where the editor and «Создать» go back to, when they were opened from it (?from=docs).
func docsBack(r *http.Request, back string) string {
	if r.FormValue("from") == "docs" {
		return "/docs"
	}
	return back
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
