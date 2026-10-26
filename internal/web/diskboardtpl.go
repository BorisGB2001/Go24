package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf8"
)

// boardTemplates are the templates «Создать доску» offers besides the empty board.
var boardTemplates = []string{"brainstorm", "retro", "kanban", "journey", "swot", "meeting"}

// boardZone is a labelled area of a template; boardNote is a sticky note in it.
type boardZone struct {
	x, y, w, h float64
	label, bg  string
}

type boardNote struct {
	x, y  float64
	label string
}

const (
	noteW, noteH = 180.0, 120.0
	noteBg       = "#ffec99"
)

// boardLayout is the template's areas, notes and title (the label keys of the locales).
func boardLayout(tpl string) (title string, zones []boardZone, notes []boardNote, ok bool) {
	columns := func(bgs []string, labels ...string) {
		for i, l := range labels {
			zones = append(zones, boardZone{float64(i) * 420, 0, 400, 640, l, bgs[i%len(bgs)]})
		}
	}
	switch tpl {
	case "brainstorm":
		zones = []boardZone{{-200, -90, 400, 180, "dboard.tpl_brainstorm_topic", "#d0ebff"}}
		for _, p := range [][2]float64{{-480, -300}, {-90, -330}, {300, -300}, {-480, 180}, {-90, 210}, {300, 180}} {
			notes = append(notes, boardNote{p[0], p[1], "dboard.tpl_note_idea"})
		}
	case "retro":
		columns([]string{"#d3f9d8", "#ffe3e3", "#d0ebff"}, "dboard.tpl_retro_good", "dboard.tpl_retro_bad", "dboard.tpl_retro_actions")
	case "kanban":
		columns([]string{"#f1f3f5", "#fff3bf", "#d3f9d8"}, "dboard.tpl_kanban_todo", "dboard.tpl_kanban_doing", "dboard.tpl_kanban_done")
		notes = []boardNote{{20, 80, "dboard.tpl_note_task"}, {20, 220, "dboard.tpl_note_task"}, {440, 80, "dboard.tpl_note_task"}}
	case "journey":
		stages := []string{"dboard.tpl_journey_aware", "dboard.tpl_journey_choose", "dboard.tpl_journey_buy", "dboard.tpl_journey_use", "dboard.tpl_journey_recommend"}
		rows := []string{"dboard.tpl_journey_actions", "dboard.tpl_journey_thoughts", "dboard.tpl_journey_feelings"}
		for i, l := range stages {
			zones = append(zones, boardZone{220 + float64(i)*320, 0, 300, 80, l, "#d0ebff"})
		}
		for j, l := range rows {
			zones = append(zones, boardZone{0, 100 + float64(j)*180, 200, 160, l, "#f1f3f5"})
			for i := range stages {
				zones = append(zones, boardZone{220 + float64(i)*320, 100 + float64(j)*180, 300, 160, "", "transparent"})
			}
		}
	case "swot":
		zones = []boardZone{{0, 0, 500, 400, "dboard.tpl_swot_s", "#d3f9d8"}, {520, 0, 500, 400, "dboard.tpl_swot_w", "#ffe3e3"},
			{0, 420, 500, 400, "dboard.tpl_swot_o", "#d0ebff"}, {520, 420, 500, 400, "dboard.tpl_swot_t", "#fff3bf"}}
	case "meeting":
		columns([]string{"#d0ebff", "#d3f9d8", "#fff3bf"}, "dboard.tpl_meeting_agenda", "dboard.tpl_meeting_decisions", "dboard.tpl_meeting_tasks")
	default:
		return "", nil, nil, false
	}
	return "dboard.tpl_" + tpl, zones, notes, true
}

// boardTemplateScene is the scene of a new board made by the template, its labels in the user's language; nil for
// the empty board or a template there is not.
func (s *Server) boardTemplateScene(r *http.Request, tpl string) []byte {
	title, zones, notes, ok := boardLayout(tpl)
	if !ok {
		return nil
	}
	var els []map[string]any
	n := 0
	el := func(typ string, x, y, w, h float64, more map[string]any) {
		n++
		e := map[string]any{"id": fmt.Sprintf("go24-%s-%d", tpl, n), "type": typ, "x": x, "y": y, "width": w, "height": h,
			"angle": 0, "strokeColor": "#1e1e1e", "backgroundColor": "transparent", "fillStyle": "solid", "strokeWidth": 1,
			"strokeStyle": "solid", "roughness": 1, "opacity": 100, "seed": 1000 + n, "version": 1, "versionNonce": n,
			"isDeleted": false, "groupIds": []string{}, "boundElements": nil, "link": nil, "locked": false}
		for k, v := range more {
			e[k] = v
		}
		els = append(els, e)
	}
	text := func(x, y float64, label string, size float64) {
		t := s.tr(r, label)
		el("text", x, y, float64(utf8.RuneCountInString(t))*size*0.6, size*1.25, map[string]any{"text": t, "originalText": t,
			"fontSize": size, "fontFamily": 5, "textAlign": "left", "verticalAlign": "top", "containerId": nil,
			"autoResize": true, "lineHeight": 1.25})
	}
	top := 0.0
	for _, z := range zones {
		top = min(top, z.y)
	}
	text(0, top-90, title, 36)
	for _, z := range zones {
		el("rectangle", z.x, z.y, z.w, z.h, map[string]any{"backgroundColor": z.bg, "roundness": map[string]any{"type": 3}})
		if z.label != "" {
			text(z.x+20, z.y+16, z.label, 24)
		}
	}
	for _, nt := range notes {
		el("rectangle", nt.x, nt.y, noteW, noteH, map[string]any{"backgroundColor": noteBg, "strokeColor": "#f08c00"})
		text(nt.x+16, nt.y+16, nt.label, 20)
	}
	scene, _ := json.Marshal(map[string]any{"type": "excalidraw", "version": 2, "source": "go24", "elements": els,
		"appState": map[string]any{}, "files": map[string]any{}})
	return scene
}
