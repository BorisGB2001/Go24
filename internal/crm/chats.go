package crm

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// «Чат» of the portal card: the employees discuss a deal, lead, contact or company in a group chat of the
// messenger. The card has one chat: the first «Чат» makes it (its responsible, observers and the one who opened it
// are in it), later ones open it and bring in whoever of them is not there yet. The card's history says it was made.

// ChatOf is the room of the card's chat ("" — none yet).
func (s *Store) ChatOf(ctx context.Context, entity, id string) (string, error) {
	var room string
	err := s.db.QueryRow(ctx, `SELECT room_id FROM crm_chats WHERE entity = $1 AND entity_id::text = $2`, entity, id).Scan(&room)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return room, err
}

// SetChat ties the room to the card and writes its history; false — the card had a chat already (made at the same
// time by someone else), which stays.
func (s *Store) SetChat(ctx context.Context, userID, entity, id, room string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO crm_chats (entity, entity_id, room_id, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, entity, id, room, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	if err := s.event(ctx, tx, entity, id, userID, "chat", room); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// ChatPeople are those the card's chat is for: its responsible and observers, then the chat's maker (who may add
// others while the rest are not in it yet).
func (s *Store) ChatPeople(ctx context.Context, entity, id string) ([]string, error) {
	var out []string
	resp, err := s.Responsible(ctx, entity, id)
	if err != nil {
		return nil, err
	}
	if resp != "" {
		out = append(out, resp)
	}
	obs, err := s.Observers(ctx, entity, id)
	if err != nil {
		return nil, err
	}
	out = append(out, ObserverIDs(obs)...)
	var maker string
	err = s.db.QueryRow(ctx, `SELECT created_by::text FROM crm_chats WHERE entity = $1 AND entity_id::text = $2`, entity, id).Scan(&maker)
	if err == nil {
		out = append([]string{maker}, out...)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return out, nil
}

// CardTitle is the card's name: a deal's, lead's or company's title, a contact's name.
func (s *Store) CardTitle(ctx context.Context, entity, id string) (string, error) {
	if plural[entity] == "" {
		return "", ErrNotFound
	}
	col := "title"
	if entity == "contact" {
		col = "name"
	}
	var t string
	err := s.db.QueryRow(ctx, `SELECT `+col+` FROM crm_`+plural[entity]+` WHERE id::text = $1`, id).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return t, err
}
