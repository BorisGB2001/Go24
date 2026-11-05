package tasks

import (
	"slices"
	"testing"
)

func TestProjectTools(t *testing.T) {
	// Regular project with some tools off
	pRegular := Project{
		Kind:     "project",
		ToolsOff: []string{"calendar", "chat"},
		ToolNames: map[string]string{
			"tasks": "Задачки",
		},
	}

	if !pRegular.ToolOn("tasks") {
		t.Error("tasks must be on for regular project")
	}
	if pRegular.ToolOn("calendar") {
		t.Error("calendar must be off for regular project")
	}
	if pRegular.ToolOn("chat") {
		t.Error("chat must be off for regular project")
	}
	if got := pRegular.ToolName("tasks"); got != "Задачки" {
		t.Errorf("ToolName(tasks) = %q, want %q", got, "Задачки")
	}
	if got := pRegular.ToolName("feed"); got != "" {
		t.Errorf("ToolName(feed) = %q, want empty", got)
	}

	// Collab project always has all tools on
	pCollab := Project{
		Kind:     "collab",
		ToolsOff: []string{"calendar", "chat", "drive"}, // ignored for collab
		ToolNames: map[string]string{
			"drive": "Файлы",
		},
	}
	for _, tool := range GroupTools {
		if !pCollab.ToolOn(tool) {
			t.Errorf("collab must have tool %q on despite ToolsOff", tool)
		}
	}
	if got := pCollab.ToolName("drive"); got != "Файлы" {
		t.Errorf("collab ToolName(drive) = %q, want %q", got, "Файлы")
	}

	// Tools() slice ordering and properties
	tools := pRegular.Tools()
	if len(tools) != len(GroupTools) {
		t.Fatalf("Tools() length = %d, want %d", len(tools), len(GroupTools))
	}

	var ids []string
	for _, tl := range tools {
		ids = append(ids, tl.ID)
	}
	if !slices.Equal(ids, GroupTools) {
		t.Errorf("Tools() order = %v, want %v", ids, GroupTools)
	}

	for _, tl := range tools {
		if tl.ID == "tasks" {
			if !tl.On || tl.Name != "Задачки" {
				t.Errorf("tasks tool = %+v, want on=true, name='Задачки'", tl)
			}
		}
		if tl.ID == "calendar" {
			if tl.On {
				t.Errorf("calendar tool must be off, got %+v", tl)
			}
		}
	}
}
