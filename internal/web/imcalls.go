package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// ---------- «Звонки» of the messenger: the person's call history ----------

// imCall is a line of the history with the names to show.
type imCall struct {
	calls.Call
	Room   matrixsync.Room // the chat («» name: a chat the person is no longer in)
	ByName string
	Names  []string // who took part
}

// callHistory gives the person's calls with their chats and names.
func (s *Server) callHistory(ctx context.Context, userID string, missed bool, limit int) ([]imCall, error) {
	list, err := s.Rings.History(ctx, userID, missed, limit)
	if err != nil || len(list) == 0 {
		return []imCall{}, err
	}
	rooms := map[string]matrixsync.Room{}
	if s.Matrix != nil {
		all, err := s.Matrix.Rooms(ctx, userID)
		if err != nil {
			return nil, err
		}
		for _, rm := range all {
			rooms[rm.ID] = rm
		}
	}
	var ids []string
	for _, c := range list {
		ids = append(ids, c.By)
		ids = append(ids, c.People...)
	}
	names := map[string]string{}
	rows, err := s.DB.Query(ctx, `SELECT id::text, name FROM users WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	pairs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ ID, Name string }])
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		names[p.ID] = p.Name
	}
	out := make([]imCall, 0, len(list))
	for _, c := range list {
		ic := imCall{Call: c, Room: rooms[c.Chat], ByName: names[c.By]}
		if ic.Room.ID == "" {
			ic.Room = matrixsync.Room{ID: c.Chat, Name: ic.ByName, Kind: "gone"}
		}
		for _, p := range c.People {
			ic.Names = append(ic.Names, names[p])
		}
		out = append(out, ic)
	}
	return out, nil
}

// imCalls gives the list «Звонки» (?missed=1 — only the missed ones); the person has looked at their missed calls.
func (s *Server) imCalls(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.Rings == nil {
		s.jsonErr(w, r, i18n.E("err.calls_off"))
		return
	}
	list, err := s.callHistory(r.Context(), sess.UserID, r.URL.Query().Get("missed") == "1", 100)
	var html string
	if err == nil {
		html, err = s.imFragment(r, "imcalls", map[string]any{"Calls": list, "Missed": r.URL.Query().Get("missed") == "1", "Video": s.Calls != nil})
	}
	if err == nil {
		err = s.Rings.SeeAll(r.Context(), sess.UserID)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": html})
}

// apiCalls: GET /api/v1/im/calls?missed=1&limit=50 — the person's call history, the latest first; missed=1 — only
// the missed and declined calls. ?seen=1 counts them looked at (the counter of the messenger's tab goes).
func (s *Server) apiCalls(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.Rings == nil {
		return i18n.E("err.calls_off")
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	list, err := s.callHistory(r.Context(), p.UserID, r.URL.Query().Get("missed") == "1", limit)
	if err != nil {
		return err
	}
	unseen, err := s.Rings.Unseen(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	if r.URL.Query().Get("seen") == "1" {
		if err := s.Rings.SeeAll(r.Context(), p.UserID); err != nil {
			return err
		}
	}
	type person struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type call struct {
		ID       string    `json:"id"`
		Kind     string    `json:"kind"` // out | in | missed | declined
		Room     string    `json:"room"`
		RoomName string    `json:"room_name"`
		Video    bool      `json:"video"`
		By       person    `json:"by"`
		People   []person  `json:"people"`
		At       time.Time `json:"at"`
		Seconds  int       `json:"seconds"`
		Live     bool      `json:"live,omitempty"`
		NoAnswer bool      `json:"no_answer,omitempty"` // an outgoing call nobody came to
	}
	out := []call{}
	for _, c := range list {
		x := call{ID: c.ID, Kind: c.Kind, Room: c.Chat, Video: c.Video, By: person{c.By, c.ByName}, People: []person{},
			At: c.At, Seconds: c.Secs, Live: c.Live, NoAnswer: c.Alone()}
		if c.Room.Kind != "gone" {
			x.RoomName = c.Room.Name
		}
		for i, id := range c.People {
			x.People = append(x.People, person{id, c.Names[i]})
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": out, "unseen": unseen})
	return nil
}
