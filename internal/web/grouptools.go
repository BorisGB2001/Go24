package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Возможности группы» (GO24DEV-824): the tabs of a group's tools, switching them off and naming them ----------

// toolTab is a tab of the group's tool.
type toolTab struct {
	tasks.Tool
	Href, Icon string
}

var toolIcons = map[string]string{"feed": "news", "tasks": "list-check", "calendar": "calendar-event", "drive": "folder", "chat": "message-circle"}

// groupTabs are the tabs of the group's tools that are on and open for the viewer (the chat — when the group has
// one in the messenger, a section switched off on the portal — none).
func (s *Server) groupTabs(r *http.Request, p tasks.Project) []toolTab {
	var out []toolTab
	for _, t := range p.Tools() {
		if !t.On {
			continue
		}
		tab := toolTab{Tool: t, Icon: toolIcons[t.ID]}
		switch t.ID {
		case "feed":
			tab.Href = "/feed?project=" + p.ID
		case "tasks":
			tab.Href = "/p/" + p.Key
		case "calendar":
			tab.Href = "/calendar?project=" + p.Key
		case "drive":
			tab.Href = "/disk/p/" + p.Key
		case "chat":
			if room, err := s.Matrix.ProjectRoom(r.Context(), p.ID); err == nil && room != "" {
				tab.Href = imRoomURL(room)
			}
		}
		if tab.Href == "" {
			continue
		}
		if _, off := s.offSection(r.Context(), strings.SplitN(tab.Href, "?", 2)[0]); off {
			continue
		}
		out = append(out, tab)
	}
	return out
}

// toolForm are all the group's tools, for the form of «Возможности группы».
func toolForm(p tasks.Project) []toolTab {
	var out []toolTab
	for _, t := range p.Tools() {
		out = append(out, toolTab{Tool: t, Icon: toolIcons[t.ID]})
	}
	return out
}

// toolsOfForm reads «Возможности группы» of the form: tool_<id>=on and name_<id>.
func toolsOfForm(f url.Values) []tasks.Tool {
	out := make([]tasks.Tool, 0, len(tasks.GroupTools))
	for _, id := range tasks.GroupTools {
		out = append(out, tasks.Tool{ID: id, On: f.Get("tool_"+id) == "on", Name: f.Get("name_" + id)})
	}
	return out
}

// groupTools saves «Возможности группы».
func (s *Server) groupTools(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		_ = r.ParseForm()
		err = s.Tasks.SetTools(r.Context(), p, toolsOfForm(r.Form))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("group tools", "user_id", sess.UserID, "project", p.Key)
	http.Redirect(w, r, back+"#tools", http.StatusSeeOther)
}

// apiGroupTools is GET (the list) and PATCH ([{id, on, name}], the tools not given keep theirs) of
// /api/v1/projects/{key}/tools.
func (s *Server) apiGroupTools(w http.ResponseWriter, r *http.Request, pr apikeys.Principal) error {
	p, err := s.Tasks.ProjectByKey(r.Context(), pr.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if r.Method == http.MethodPatch {
		var in []tasks.Tool
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Tasks.SetTools(r.Context(), p, in); err != nil {
			return err
		}
		if p, err = s.Tasks.ProjectByKey(r.Context(), pr.UserID, p.Key); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, p.Tools())
	return nil
}

// groupTaskPerms saves «Права доступа» of the group to its tasks (GO24DEV-861).
func (s *Server) groupTaskPerms(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.SetTaskPerms(r.Context(), p, tasks.TaskPerms{Create: r.FormValue("create"), Edit: r.FormValue("edit"), Delete: r.FormValue("delete")})
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#perms", http.StatusSeeOther)
}

// ---------- Departments of a group (GO24DEV-833) ----------

// syncGroupDepts brings the members of the group (projectID "" — of all groups) in line with its departments; the
// group's chat follows its members.
func (s *Server) syncGroupDepts(ctx context.Context, projectID string) {
	changed, err := s.Tasks.SyncGroupDepts(ctx, projectID)
	if err != nil {
		s.Log.Warn("group departments sync", "project", projectID, "err", err)
		return
	}
	if changed {
		s.Matrix.Wake()
	}
}

// groupDepts saves the group's departments (the picker's «dept:ID»; a person chosen there is not a department).
func (s *Server) groupDepts(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		_ = r.ParseForm()
		var ids []string
		for _, v := range r.Form["depts"] {
			if id, ok := strings.CutPrefix(v, "dept:"); ok {
				ids = append(ids, id)
			} else if v != "" {
				err = i18n.E("err.group_dept_people")
			}
		}
		if err == nil {
			err = s.Tasks.SetGroupDepts(r.Context(), p, ids)
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Matrix.Wake()
	s.Log.Info("group departments", "user_id", sess.UserID, "project", p.Key, "departments", len(r.Form["depts"]))
	http.Redirect(w, r, back+"#members", http.StatusSeeOther)
}

// apiGroupDepts is GET (the list [{id, name}]) and PUT ({"departments": [IDs]}, the whole list) of
// /api/v1/projects/{key}/departments.
func (s *Server) apiGroupDepts(w http.ResponseWriter, r *http.Request, pr apikeys.Principal) error {
	p, err := s.Tasks.ProjectByKey(r.Context(), pr.UserID, r.PathValue("key"))
	if err != nil {
		return err
	}
	if r.Method == http.MethodPut {
		var in struct {
			Departments []string `json:"departments"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Tasks.SetGroupDepts(r.Context(), p, in.Departments); err != nil {
			return err
		}
		s.Matrix.Wake()
	}
	list, err := s.Tasks.GroupDepts(r.Context(), p.ID)
	if err != nil {
		return err
	}
	type apiDept struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := make([]apiDept, 0, len(list))
	for _, d := range list {
		out = append(out, apiDept{strings.TrimPrefix(d.ID, "dept:"), d.Name})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
