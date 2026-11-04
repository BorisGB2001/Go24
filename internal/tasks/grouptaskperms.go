package tasks

import (
	"context"
	"slices"
)

// «Права доступа» of a portal group to its tasks: who creates tasks in the group («Создание задач»), who changes
// any of its tasks («Изменение задач») and who deletes them («Удаление задач») — the owner only, the owner and the
// moderators, or all members. Changing and deleting go beyond one's own tasks: the creator always keeps them.
// A collab keeps its own settings and the usual rule (its moderators change its tasks).

// GroupTaskPerms are the rights in the order of the settings; GroupPermLevels are their values.
var (
	GroupTaskPerms  = []string{"create", "edit", "delete"}
	GroupPermLevels = []string{"owner", "managers", "members"}
)

// TaskPerms are a group's rights to its tasks by GroupTaskPerms.
type TaskPerms struct {
	Create string `json:"create"`
	Edit   string `json:"edit"`
	Delete string `json:"delete"`
}

// DefaultTaskPerms are those of a new group.
var DefaultTaskPerms = TaskPerms{Create: "members", Edit: "managers", Delete: "managers"}

// groupMay: does a member of the role (project_members.role, "admin" — admin mode) have the right of the level?
func groupMay(role, level string) bool {
	switch role {
	case "admin", "owner":
		return true
	case "manager":
		return level != "owner"
	case "member":
		return level == "members"
	}
	return false
}

// MayCreateTasks: the viewer's role lets them put tasks into the group (a collab — by its own settings).
func (p Project) MayCreateTasks() bool {
	return p.Collab() || groupMay(p.MyRole, p.TaskPerms().Create)
}

// TaskPerms are the group's rights to its tasks (the defaults where the project was loaded without them).
func (p Project) TaskPerms() TaskPerms {
	tp := TaskPerms{Create: p.TaskCreate, Edit: p.TaskEdit, Delete: p.TaskDelete}
	if tp.Create == "" {
		tp.Create = DefaultTaskPerms.Create
	}
	if tp.Edit == "" {
		tp.Edit = DefaultTaskPerms.Edit
	}
	if tp.Delete == "" {
		tp.Delete = DefaultTaskPerms.Delete
	}
	return tp
}

// SetTaskPerms saves the group's rights to its tasks: those who change the group (its owner and moderators) do it.
func (s *Store) SetTaskPerms(ctx context.Context, p Project, tp TaskPerms) error {
	if !p.CanEdit() || p.Collab() || p.Archived {
		return ErrForbidden
	}
	for _, l := range []string{tp.Create, tp.Edit, tp.Delete} {
		if !slices.Contains(GroupPermLevels, l) {
			return invalid("err.bad_form")
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE projects SET task_create = $2, task_edit = $3, task_delete = $4, updated_at = now()
		WHERE id = $1`, p.ID, tp.Create, tp.Edit, tp.Delete)
	return err
}
