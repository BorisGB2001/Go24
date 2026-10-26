package web

import (
	"fmt"
	"strings"

	"go24/internal/tasks"
)

// userPick is the data of the people picker, the one for every form (template "upick", static/js/people.js),
// ui.entity-selector: the chosen people as chips and a field to search by typing.
//
//	{{template "upick" (upick "responsible" $f.Responsible .Users "required")}}
//	{{template "upick" (upick "members" $d.Members $d.Users "multi" "ph" (t "flows.team_hint"))}}
//	{{template "upick" (upick "to" "" $d.Users "+starter" (t "bp.to.starter"))}}
//
// sel is the chosen ID(s): a string, []string, map[string]bool or []tasks.User; users name them (the IDs it does
// not know — a fired person — the script asks the server about). Options: "multi"; "required" — a single choice
// without ×; "submit" — the form is sent on each change; "noself" — the viewer is not offered; "im" — only people
// with a messenger account; "ph" LABEL — the placeholder; "+ID" LABEL — a fixed choice before the people;
// "collab" KEY — only the members of that collab, without «Отделы» (KEY "" — everybody; "@project" — the collab
// chosen in the form's project field, see taskform); "depts" — a whole department is one choice «dept:ID» (it means
// its people now and later, with sub-departments) instead of its people one by one.
type userPick struct {
	Name        string
	Multiple    bool
	Required    bool
	Submit      bool
	Self        bool
	Messenger   bool
	Placeholder string
	Collab      string
	Sel         []tasks.User
	Extra       []tasks.User
	Missing     bool
	Depts       bool
}

func newUserPick(name string, sel any, users []tasks.User, opts ...any) (userPick, error) {
	p := userPick{Name: name, Self: true}
	for i := 0; i < len(opts); i++ {
		o, ok := opts[i].(string)
		if !ok {
			return p, fmt.Errorf("upick %s: option %v is not a string", name, opts[i])
		}
		switch {
		case o == "multi":
			p.Multiple = true
		case o == "required":
			p.Required = true
		case o == "submit":
			p.Submit = true
		case o == "noself":
			p.Self = false
		case o == "im":
			p.Messenger = true
		case o == "depts":
			p.Depts = true
		case o == "collab":
			if i+1 >= len(opts) {
				return p, fmt.Errorf("upick %s: %q needs a key", name, o)
			}
			i++
			p.Collab = fmt.Sprint(opts[i])
		case o == "ph" || strings.HasPrefix(o, "+"):
			if i+1 >= len(opts) {
				return p, fmt.Errorf("upick %s: %q needs a label", name, o)
			}
			i++
			label := fmt.Sprint(opts[i])
			if o == "ph" {
				p.Placeholder = label
			} else {
				p.Extra = append(p.Extra, tasks.User{ID: o[1:], Name: label})
			}
		default:
			return p, fmt.Errorf("upick %s: unknown option %q", name, o)
		}
	}
	known := map[string]tasks.User{}
	for _, u := range users {
		known[u.ID] = u
	}
	for _, u := range p.Extra {
		known[u.ID] = u
	}
	var chosen []tasks.User
	switch v := sel.(type) {
	case nil:
	case string:
		if v != "" {
			chosen = []tasks.User{{ID: v}}
		}
	case []string:
		for _, id := range v {
			chosen = append(chosen, tasks.User{ID: id})
		}
	case map[string]bool:
		for _, u := range users { // in the list's order
			if v[u.ID] {
				chosen = append(chosen, tasks.User{ID: u.ID})
			}
		}
	case []tasks.User:
		chosen = v
	default:
		return p, fmt.Errorf("upick %s: unsupported selection %T", name, sel)
	}
	for _, u := range chosen {
		if k, ok := known[u.ID]; ok { // the name, and the mark «гость» of a guest
			u.Name, u.Guest = k.Name, u.Guest || k.Guest
		}
		if u.Name == "" {
			p.Missing = true
		}
		p.Sel = append(p.Sel, u)
	}
	if !p.Multiple && len(p.Sel) > 1 {
		p.Sel = p.Sel[:1]
	}
	return p, nil
}
