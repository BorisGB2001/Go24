package matrixsync

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Общий чат»: the chat of all the company's employees. go24 makes it, brings every working employee in
// (guests stay out) and takes out those dismissed; nobody leaves it or is taken out of it by hand — one who left comes
// back on the next round. An administrator chooses who writes in it (everyone or administrators) and whether the bot
// welcomes a new employee there.

const settingGeneral = "im_general" // company_settings

// General is the settings of the general chat.
type General struct {
	Who     string `json:"who"`               // all | admins: who writes in it
	NoHello bool   `json:"no_hello,omitempty"` // no «Новый сотрудник» for the new ones
}

var generalWho = []string{"all", "admins"}

// GeneralSettings are the company's settings; by default everyone writes and the new ones are welcomed.
func (s *Service) GeneralSettings(ctx context.Context) (General, error) {
	g := General{Who: "all"}
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingGeneral).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return g, nil
	}
	if err != nil {
		return g, err
	}
	if json.Unmarshal(raw, &g) != nil || !slices.Contains(generalWho, g.Who) {
		g.Who = "all"
	}
	return g, nil
}

// SetGeneralSettings saves the settings (the caller checks the actor is an administrator) and puts them on the room.
func (s *Service) SetGeneralSettings(ctx context.Context, actorID string, g General) error {
	if !slices.Contains(generalWho, g.Who) {
		return i18n.E("err.general_who")
	}
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingGeneral, raw, actorID); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// GeneralRoom is the general chat's room ("" — not made yet, or no messenger).
func (s *Service) GeneralRoom(ctx context.Context) (string, error) {
	if s == nil {
		return "", nil
	}
	return s.roomOf(ctx, "general", nil)
}

// GeneralClosed reports whether the person (a go24 user) may not write in the general chat: only administrators do.
func (s *Service) GeneralClosed(ctx context.Context, userID string) bool {
	g, err := s.GeneralSettings(ctx)
	if err != nil || g.Who == "all" {
		return false
	}
	var admin bool
	_ = s.DB.QueryRow(ctx, `SELECT admin OR groups && $2::text[] FROM users WHERE id::text = $1`, userID, s.AdminGroups).Scan(&admin)
	return !admin
}

// reconcileGeneral keeps the general chat: all the employees (all — their Matrix IDs, by go24 user) in it, its
// writers, and «Новый сотрудник» for each one who has not been welcomed — when they come in (a Matrix account is
// made at the first sign-in); those working when it was made are no new ones.
func (s *Service) reconcileGeneral(ctx context.Context, all map[string]string) error {
	if s.GeneralName == "" {
		return nil
	}
	room, made, err := s.ensureRoom(ctx, "general", nil, s.GeneralName, "go24")
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, mx := range all {
		want[mx] = true
	}
	if err := s.members(ctx, room, want); err != nil {
		return err
	}
	g, err := s.GeneralSettings(ctx)
	if err != nil {
		return err
	}
	var writers []string
	if g.Who == "admins" {
		writers = []string{}
		rows, err := s.DB.Query(ctx, `SELECT id::text FROM users WHERE active AND NOT guest AND (admin OR groups && $1::text[])`, s.AdminGroups)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if mx, ok := all[id]; ok {
				writers = append(writers, mx)
			}
		}
	}
	if err := s.collabRoomRules(ctx, room, true, false, writers); err != nil {
		return err
	}
	var welcomed int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM im_general_welcomed`).Scan(&welcomed); err != nil {
		return err
	}
	if made || welcomed == 0 { // the employees of before it, Matrix account or not yet, are no new ones
		if _, err := s.DB.Exec(ctx, `INSERT INTO im_general_welcomed (user_id) SELECT id FROM users WHERE active AND NOT guest
			ON CONFLICT DO NOTHING`); err != nil {
			return err
		}
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	rows, err := s.DB.Query(ctx, `SELECT u.id::text, u.name FROM users u WHERE u.id::text = ANY($1)
		AND NOT EXISTS (SELECT 1 FROM im_general_welcomed w WHERE w.user_id = u.id) ORDER BY u.created_at, u.name`, ids)
	if err != nil {
		return err
	}
	type person struct{ ID, Name string }
	fresh, err := pgx.CollectRows(rows, pgx.RowToStructByPos[person])
	if err != nil {
		return err
	}
	for _, p := range fresh { // a new employee, in the chat now
		if !g.NoHello && s.GeneralHello != nil {
			if _, err := s.M.Send(ctx, room, map[string]any{"msgtype": "m.notice", "body": s.GeneralHello(p.Name),
				"go24.general_hello": p.ID}, ""); err != nil {
				return err
			}
		}
		if _, err := s.DB.Exec(ctx, `INSERT INTO im_general_welcomed (user_id) VALUES ($1::uuid) ON CONFLICT DO NOTHING`, p.ID); err != nil {
			return err
		}
	}
	return nil
}
