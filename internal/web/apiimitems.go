package web

import (
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/i18n"
)

// ---------- REST: a chat's tasks and meetings (the «Задачи» and «Встречи» of its panel ----------

// apiChatTask, apiChatEvent: a task or a meeting of the chat and the message it was made of ("" — of none).
type apiChatTask struct {
	apiTask
	Message string `json:"message"`
}

type apiChatEvent struct {
	apiEvent
	Message string `json:"message"`
}

// apiChatTasks: GET → the chat's tasks the person may see, the latest tied first.
func (s *Server) apiChatTasks(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	list, _, err := s.chatItems(r, p.UserID, r.PathValue("room"))
	if err != nil {
		return err
	}
	tr, out := s.apiTr(r), []apiChatTask{}
	for _, t := range list {
		out = append(out, apiChatTask{toAPITask(t.Task, tr), t.Message})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out})
	return nil
}

// apiChatEvents: GET → the chat's meetings the person may see, the latest tied first.
func (s *Server) apiChatEvents(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	_, list, err := s.chatItems(r, p.UserID, r.PathValue("room"))
	if err != nil {
		return err
	}
	out := []apiChatEvent{}
	for _, e := range list {
		out = append(out, apiChatEvent{toAPIEvent(e.Event), e.Message})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
	return nil
}

// apiChatTie: POST {"task": "KEY"} or {"event": "id"}, "message": the chat's message it is of (optional) → ties a task
// or a meeting the person sees to the chat.
func (s *Server) apiChatTie(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	var in struct {
		Task    string `json:"task"`
		Event   string `json:"event"`
		Message string `json:"message"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, room := r.Context(), r.PathValue("room")
	kind, id := "", ""
	switch {
	case in.Task != "":
		tid, err := s.Tasks.TaskIDByKey(ctx, in.Task)
		if err == nil {
			_, err = s.Tasks.RowCard(ctx, p.UserID, tid)
		}
		if err != nil {
			return err
		}
		kind, id = "task", tid
	case in.Event != "" && s.Calendar != nil:
		if _, err := s.Calendar.Get(ctx, p.UserID, in.Event); err != nil {
			return err
		}
		kind, id = "event", in.Event
	default:
		return i18n.E("err.bad_form")
	}
	if in.Message != "" {
		if _, _, _, err := s.Matrix.One(ctx, p.UserID, room, in.Message); err != nil {
			return err
		}
	}
	if err := s.Matrix.TieItem(ctx, p.UserID, room, kind, id, in.Message); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
