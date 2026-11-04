package tasks

import (
	"context"
	"slices"
	"strings"

	"go24/internal/i18n"
)

// «Возможности группы»: a group's (a project's, a scrum's) tools are switched off one by one and get
// names of the group's own; a switched-off tool leaves the group's tabs and does not open. A collab keeps its set.

// GroupTools are a group's tools in the order of its tabs.
var GroupTools = []string{"feed", "tasks", "calendar", "drive", "chat"}

// ErrToolOff: the group has the tool switched off.
var ErrToolOff = i18n.E("err.group_tool_off")

const maxToolName = 50

// Tool is one of «Возможности группы»: Name is the group's own one ("" — the usual).
type Tool struct {
	ID   string `json:"id"`
	On   bool   `json:"on"`
	Name string `json:"name"`
}

// ToolOn: the group has the tool (a collab has them all).
func (p Project) ToolOn(tool string) bool { return p.Collab() || !slices.Contains(p.ToolsOff, tool) }

// ToolName is the group's own name of the tool ("" — the usual one).
func (p Project) ToolName(tool string) string { return p.ToolNames[tool] }

// Tools are the group's «Возможности» in their order.
func (p Project) Tools() []Tool {
	out := make([]Tool, 0, len(GroupTools))
	for _, t := range GroupTools {
		out = append(out, Tool{ID: t, On: p.ToolOn(t), Name: p.ToolName(t)})
	}
	return out
}

// SetTools saves the group's «Возможности»: those who change the group (its owner and moderators) switch the tools
// on and off and name them; the tools not in the list keep what they had. A collab's tools are not changed.
func (s *Store) SetTools(ctx context.Context, p Project, tools []Tool) error {
	if !p.CanEdit() || p.Collab() || p.Archived {
		return ErrForbidden
	}
	off, names := append([]string{}, p.ToolsOff...), map[string]string{}
	for k, v := range p.ToolNames {
		names[k] = v
	}
	for _, t := range tools {
		if !slices.Contains(GroupTools, t.ID) {
			return invalid("err.bad_form")
		}
		name := strings.TrimSpace(t.Name)
		if len([]rune(name)) > maxToolName {
			return invalid("err.group_tool_name", maxToolName)
		}
		if name == "" {
			delete(names, t.ID)
		} else {
			names[t.ID] = name
		}
		off = slices.DeleteFunc(off, func(x string) bool { return x == t.ID })
		if !t.On {
			off = append(off, t.ID)
		}
	}
	slices.Sort(off)
	_, err := s.db.Exec(ctx, `UPDATE projects SET tools_off = $2, tool_names = $3, updated_at = now() WHERE id = $1`,
		p.ID, off, names)
	return err
}
