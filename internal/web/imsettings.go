package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
)

// ---------- «Настройки» of the messenger: the look of chats, the sending key, the sound ----------

// imBackgrounds are the chat backgrounds to pick from ("" — the plain one).
var imBackgrounds = []string{"", "sky", "mint", "sand", "rose", "lilac", "night"}

// imSettings are the person's settings of the messenger, kept on the server (user_prefs) so they follow the person to
// any browser.
type imSettings struct {
	Background string `json:"background"`  // one of imBackgrounds
	AlignLeft  bool   `json:"align_left"`  // all messages on the left (else one's own on the right)
	CtrlEnter  bool   `json:"ctrl_enter"`  // Ctrl+Enter sends and Enter starts a new line (else the other way round)
	Sound      bool   `json:"sound"`       // a short sound on a new message
}

const prefIM = "im.settings"

// defaultIMSettings is what a person has until they change anything:, the sound is on.
var defaultIMSettings = imSettings{Sound: true}

func (s *Server) imSettingsOf(ctx context.Context, userID string) (imSettings, error) {
	set := defaultIMSettings
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefIM).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return set, nil
	}
	if err != nil {
		return set, err
	}
	_ = json.Unmarshal(raw, &set)
	if !slices.Contains(imBackgrounds, set.Background) {
		set.Background = ""
	}
	return set, nil
}

func (s *Server) setIMSettings(ctx context.Context, userID string, set imSettings) error {
	if !slices.Contains(imBackgrounds, set.Background) {
		return i18n.E("err.bad_form")
	}
	raw, _ := json.Marshal(set)
	_, err := s.DB.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefIM, raw)
	return err
}

// imSettingsSave takes the form of «Настройки» and goes back to the chat that was open.
func (s *Server) imSettingsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	set := imSettings{Background: r.FormValue("background"), AlignLeft: r.FormValue("align") == "left",
		CtrlEnter: r.FormValue("send") == "ctrl", Sound: r.FormValue("sound") == "1"}
	back := "/im"
	if room := r.FormValue("room"); room != "" {
		back = imRoomURL(room)
	}
	if err := s.setIMSettings(r.Context(), sess.UserID, set); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// apiIMSettings: GET gives the settings; PUT changes the fields given ({"sound": false}) and gives them all.
func (s *Server) apiIMSettings(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	set, err := s.imSettingsOf(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	if r.Method == http.MethodPut {
		if err := readJSON(r, &set); err != nil {
			return err
		}
		if err := s.setIMSettings(r.Context(), p.UserID, set); err != nil {
			return err
		}
	}
	writeJSON(w, http.StatusOK, set)
	return nil
}
